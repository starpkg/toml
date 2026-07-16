// Package toml provides a Starlark module for decoding and encoding TOML.
//
// Both directions are hardened against untrusted script input. A deeply nested
// document overflows the goroutine stack — a Go fatal error that recover() cannot
// catch — so before the recursive codec runs, decode caps the number of bracket
// openers in the text (a sound upper bound on parser recursion that needs no
// string lexing and so cannot be bypassed) and encode walks the value's depth.
// Decode additionally bounds input size and enforces the exact max_depth / node
// count on the parsed tree (capwalk); both codec calls recover ordinary panics
// into errors. The depth / node / input-size caps are host-only (no script
// setter), so a script cannot widen them. TOML's native date/time values are
// surfaced as strings (RFC 3339 for full timestamps), never as surprise opaque
// values.
package toml

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/1set/starlet"
	"github.com/1set/starlet/dataconv"
	"github.com/1set/starlet/dataconv/types"
	gotoml "github.com/BurntSushi/toml"
	"github.com/starpkg/base"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
)

// ModuleName is the name used in Starlark's load() for this module.
const ModuleName = "toml"

const (
	configKeyMaxDepth      = "max_depth"
	configKeyMaxNodes      = "max_nodes"
	configKeyMaxInputBytes = "max_input_bytes"
)

const (
	defaultMaxDepth      = 64
	defaultMaxNodes      = 100000
	defaultMaxInputBytes = 5 << 20 // 5 MiB
)

var none = starlark.None

// Module wraps a ConfigurableModule with TOML functions.
type Module struct {
	cfgMod *base.ConfigurableModule
	ext    *base.ConfigurableModuleExt
}

// NewModule creates a new Module with default configuration.
func NewModule() *Module {
	cm, _ := base.NewConfigurableModuleWithConfigOptions(
		// All three are resource/DoS limits the module enforces against untrusted
		// script input, so they are host-only: base emits no set_<name> builtin
		// and snapshots the env at construction, so a script cannot re-widen or
		// zero-out a cap at runtime. Configure them host-side via TOML_* env vars.
		genConfigOption(configKeyMaxDepth, "Maximum nesting depth when decoding or encoding", defaultMaxDepth).SetHostOnly(true),
		genConfigOption(configKeyMaxNodes, "Maximum total nodes when decoding", defaultMaxNodes).SetHostOnly(true),
		genConfigOption(configKeyMaxInputBytes, "Maximum input size in bytes when decoding", defaultMaxInputBytes).SetHostOnly(true),
	)
	return &Module{cfgMod: cm, ext: cm.Extend()}
}

func genConfigOption[T any](name, description string, defaultValue T) *base.ConfigOption[T] {
	return base.NewConfigOption(defaultValue).
		WithName(name).
		WithDescription(description).
		WithEnvVar("TOML_" + upper(name))
}

// LoadModule returns the Starlark module loader.
func (m *Module) LoadModule() starlet.ModuleLoader {
	funcs := starlark.StringDict{
		"decode": starlark.NewBuiltin(ModuleName+".decode", m.decode),
		"encode": starlark.NewBuiltin(ModuleName+".encode", m.encode),
	}
	return m.cfgMod.LoadModule(ModuleName, funcs)
}

// decode(text) -> dict
func (m *Module) decode(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var text types.StringOrBytes
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "text", &text); err != nil {
		return none, err
	}
	src := text.GoString()
	if maxBytes := m.ext.GetInt(configKeyMaxInputBytes); maxBytes > 0 && len(src) > maxBytes {
		return none, fmt.Errorf("toml.decode: input exceeds max_input_bytes (%d)", maxBytes)
	}
	// Reject a crash-inducing amount of bracket nesting BEFORE handing the text to
	// the recursive-descent parser. gotoml.Decode recurses once per nested
	// array/inline-table, so deep input (e.g. "a = " + "["*N + "]"*N, which fits
	// under max_input_bytes) overflows the goroutine stack — a Go *fatal error*,
	// not a panic, so the recover() in unmarshal cannot catch it and the whole
	// host process crashes. The post-parse toStarlark depth cap runs too late.
	//
	// The guard counts total '[' and '{' openers: the parser's recursion depth
	// can never exceed the number of openers in the text (each nested level needs
	// its own opener), so an input that stays under maxParseBrackets cannot drive
	// the parser deep enough to overflow — regardless of how the brackets are
	// arranged or quoted. Counting every opener (even ones inside strings) only
	// makes the bound MORE conservative, so there is nothing to bypass; a precise
	// string-aware scan is deliberately avoided because any divergence from the
	// parser's own lexer would be an undercount, i.e. a crash bypass. The user's
	// max_depth is still enforced exactly, on the parsed data, by toStarlark.
	maxDepth := m.ext.GetInt(configKeyMaxDepth)
	if n := countBracketOpens(src); n > maxParseBrackets {
		return none, fmt.Errorf("toml.decode: input has too many nesting brackets (%d) to parse safely; limit is %d", n, maxParseBrackets)
	}
	parsed, err := unmarshal(src)
	if err != nil {
		return none, err
	}
	nodes := 0
	return toStarlark(parsed, 1, &nodes, maxDepth, m.ext.GetInt(configKeyMaxNodes))
}

// encode(value) -> str. TOML documents are tables, so value must be a dict.
func (m *Module) encode(thread *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	if err := starlark.UnpackArgs(b.Name(), args, kwargs, "value", &value); err != nil {
		return none, err
	}
	if _, ok := value.(*starlark.Dict); !ok {
		return none, fmt.Errorf("toml.encode: top-level value must be a dict (TOML documents are tables), got %s", value.Type())
	}
	// Reject over-deep nesting BEFORE the recursive codecs, which each overflow the
	// goroutine stack on a deep value — a fatal error the marshal() recover cannot
	// catch. Two guards, because two recursions run: checkEncodeDepth bounds
	// dataconv.Unmarshal's walk of the Starlark value (Dict keys+values, List,
	// Tuple, Set, Struct, Module), and checkGoDepth bounds gotoml's encoder walk of
	// the resulting Go value (which also covers host-wrapped Go values that
	// Unmarshal extracts as opaque leaves). Both use effectiveEncodeDepth, clamped
	// to a fixed absolute maximum so the guard is always active and its own walk
	// can't overflow, regardless of the host-configured max_depth.
	limit := effectiveEncodeDepth(m.ext.GetInt(configKeyMaxDepth))
	if err := checkEncodeDepth(value, 1, limit); err != nil {
		return none, err
	}
	goVal, err := dataconv.Unmarshal(value)
	if err != nil {
		return none, fmt.Errorf("toml.encode: %w", err)
	}
	if err := checkGoDepth(reflect.ValueOf(goVal), 1, limit); err != nil {
		return none, err
	}
	out, err := marshal(goVal)
	if err != nil {
		return none, err
	}
	return starlark.String(out), nil
}

func unmarshal(data string) (v map[string]interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = nil, fmt.Errorf("toml.decode: parse panic: %v", r)
		}
	}()
	v = map[string]interface{}{}
	if _, derr := gotoml.Decode(data, &v); derr != nil {
		return nil, fmt.Errorf("toml.decode: %w", derr)
	}
	return v, nil
}

func marshal(v interface{}) (s string, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = "", fmt.Errorf("toml.encode: encode panic: %v", r)
		}
	}()
	var buf bytes.Buffer
	if merr := gotoml.NewEncoder(&buf).Encode(v); merr != nil {
		return "", fmt.Errorf("toml.encode: %w", merr)
	}
	return buf.String(), nil
}

// toStarlark converts a decoded Go value to a Starlark value, enforcing the
// depth and node caps. Date/time values (time.Time, and any Stringer such as
// TOML's local date/time types) are surfaced as strings.
func toStarlark(v interface{}, depth int, nodes *int, maxDepth, maxNodes int) (starlark.Value, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("toml.decode: nesting exceeds max_depth (%d)", maxDepth)
	}
	*nodes++
	if *nodes > maxNodes {
		return nil, fmt.Errorf("toml.decode: node count exceeds max_nodes (%d)", maxNodes)
	}
	switch x := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(x), nil
	case int:
		return starlark.MakeInt(x), nil
	case int64:
		return starlark.MakeInt64(x), nil
	case uint64:
		return starlark.MakeUint64(x), nil
	case float64:
		return starlark.Float(x), nil
	case string:
		return starlark.String(x), nil
	case time.Time:
		return starlark.String(x.Format(time.RFC3339)), nil
	case []map[string]interface{}:
		// Array of tables.
		elems := make([]starlark.Value, 0, len(x))
		for _, e := range x {
			sv, err := toStarlark(e, depth+1, nodes, maxDepth, maxNodes)
			if err != nil {
				return nil, err
			}
			elems = append(elems, sv)
		}
		return starlark.NewList(elems), nil
	case []interface{}:
		elems := make([]starlark.Value, 0, len(x))
		for _, e := range x {
			sv, err := toStarlark(e, depth+1, nodes, maxDepth, maxNodes)
			if err != nil {
				return nil, err
			}
			elems = append(elems, sv)
		}
		return starlark.NewList(elems), nil
	case map[string]interface{}:
		d := starlark.NewDict(len(x))
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			sv, err := toStarlark(x[k], depth+1, nodes, maxDepth, maxNodes)
			if err != nil {
				return nil, err
			}
			_ = d.SetKey(starlark.String(k), sv)
		}
		return d, nil
	default:
		// TOML local date/time types (and any other Stringer) are tamed to strings.
		if s, ok := v.(fmt.Stringer); ok {
			return starlark.String(s.String()), nil
		}
		return nil, fmt.Errorf("toml.decode: unsupported value of type %T", v)
	}
}

// maxParseBrackets caps the number of array/inline-table openers ('[' and '{')
// decode will hand to the recursive-descent parser. The parser recurses once per
// nested opener, so its recursion depth is at most the number of openers in the
// text; keeping that well below the ~10^5-10^6 levels that overflow the default
// 1 GiB goroutine stack guarantees the parse cannot crash the host, with a wide
// margin that also holds under a reduced stack limit. 10000 is far above any real
// TOML document (TOML is a config format, not a bulk-data one) yet far below the
// crash threshold. This is the crash guard only; the user's max_depth is enforced
// exactly on the parsed data by toStarlark.
const maxParseBrackets = 10000

// countBracketOpens returns the number of '[' and '{' characters in s. It
// deliberately does not skip strings or comments: an over-count only makes the
// crash guard more conservative, whereas any under-count (from diverging from the
// parser's own lexer) would be a bypass.
func countBracketOpens(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '[' || s[i] == '{' {
			n++
		}
	}
	return n
}

// maxEncodeDepth is the absolute ceiling on the encode depth walk, independent of
// the user's max_depth. It keeps the crash guard active even when max_depth is
// disabled (<=0) or set enormously large, and bounds the walk's own recursion so
// it can never overflow. 10000 is far above any real document, far below the
// stack-overflow threshold.
const maxEncodeDepth = 10000

// effectiveEncodeDepth clamps the configured max_depth into (0, maxEncodeDepth]
// so the encode crash guard is always on and its walk is always bounded.
func effectiveEncodeDepth(configured int) int {
	if configured <= 0 || configured > maxEncodeDepth {
		return maxEncodeDepth
	}
	return configured
}

// checkEncodeDepth errors if v nests deeper than maxDepth, walked BEFORE
// dataconv.Unmarshal / gotoml's encoder (which recurse once per nesting level and
// would overflow the goroutine stack on a deep *acyclic* value — a fatal error,
// not a recoverable panic). It bails at maxDepth+1, so the check itself recurses
// at most maxDepth+1 deep and cannot overflow.
func checkEncodeDepth(v starlark.Value, depth, maxDepth int) error {
	return checkEncodeDepthVisited(v, depth, maxDepth, map[starlark.Value]bool{})
}

func checkEncodeDepthVisited(v starlark.Value, depth, maxDepth int, visited map[starlark.Value]bool) error {
	// Stop at a cycle among mutable containers (Dict/List/Set) FIRST — before the
	// depth test — so dataconv.Unmarshal reports its own clear "cyclic reference"
	// error rather than our depth error even when the cycle is first revisited at
	// maxDepth+1. Only pointer-identity container types are tracked (a Tuple is a
	// value and cannot contain itself); sharing that is not a cycle is walked in
	// each branch (the entry is removed on the way out), matching Unmarshal.
	if cyclableContainer(v) {
		if visited[v] {
			return nil
		}
		visited[v] = true
		defer delete(visited, v)
	}
	if depth > maxDepth {
		return fmt.Errorf("toml.encode: nesting exceeds max_depth (%d)", maxDepth)
	}
	for _, child := range encodeChildren(v) {
		if err := checkEncodeDepthVisited(child, depth+1, maxDepth, visited); err != nil {
			return err
		}
	}
	return nil
}

// cyclableContainer reports whether v is a mutable container that can participate
// in a reference cycle (and so needs visited-tracking during the depth walk).
func cyclableContainer(v starlark.Value) bool {
	switch v.(type) {
	case *starlark.Dict, *starlark.List, *starlark.Set:
		return true
	}
	return false
}

// encodeChildren returns every nested Starlark value that dataconv.Unmarshal or
// gotoml's encoder recurses into for v. This covers the six containers Unmarshal
// walks (Dict — both KEYS and values, since a nested tuple key is recursively
// hashed/stringified — plus List, Tuple, Set, Struct, Module) AND the
// host-wrapped Go values Unmarshal extracts as leaves but gotoml's encoder then
// traverses (via the generic Iterable / IterableMapping / HasAttrs cases). An
// undercount here would be an encode-crash bypass, so err toward walking. Scalars
// have no children.
func encodeChildren(v starlark.Value) []starlark.Value {
	switch x := v.(type) {
	case *starlark.Dict:
		items := x.Items()
		out := make([]starlark.Value, 0, len(items)*2)
		for _, it := range items {
			out = append(out, it[0], it[1]) // key (may be a nested tuple) and value
		}
		return out
	case starlark.Tuple:
		return append([]starlark.Value(nil), x...)
	case *starlark.List:
		return iterChildren(x)
	case *starlark.Set:
		return iterChildren(x)
	case *starlarkstruct.Struct:
		return attrChildren(x)
	case *starlarkstruct.Module:
		return attrChildren(x)
	}
	// Host-wrapped Go values (convert.GoMap/GoSlice/GoStruct/GoInterface) are
	// leaves to dataconv.Unmarshal — it extracts the underlying Go value without
	// recursing — so they have no Starlark children here. gotoml's encoder DOES
	// traverse those extracted Go values, and checkGoDepth (run on the unmarshaled
	// Go value) bounds that separately.
	return nil
}

// checkGoDepth errors if the Go value rv nests deeper than maxDepth. It is run on
// the value dataconv.Unmarshal produces, BEFORE gotoml's encoder, which recurses
// per nesting level and would overflow the stack on a deep value (including one
// that came from a host-wrapped Go value Unmarshal extracted opaquely). It bails
// at maxDepth+1 so its own recursion is bounded. It walks exactly the kinds
// gotoml's encoder recurses into: maps, slices/arrays, and struct fields
// (pointers/interfaces are dereferenced), mirroring the encoder's traversal.
func checkGoDepth(rv reflect.Value, depth, maxDepth int) error {
	if depth > maxDepth {
		return fmt.Errorf("toml.encode: nesting exceeds max_depth (%d)", maxDepth)
	}
	for _, child := range goChildren(rv) {
		if err := checkGoDepth(child, depth+1, maxDepth); err != nil {
			return err
		}
	}
	return nil
}

// derefValue follows pointers and interfaces to the concrete value, returning an
// invalid reflect.Value (Kind Invalid) for a nil along the way. The hop count is
// bounded so a cyclic pointer/interface chain (only constructible by a host that
// wraps a pathological Go value — never by a pure Starlark input, whose
// Unmarshal result is maps/slices/scalars) can't spin this loop forever; an
// over-long chain resolves to a non-container Kind and is treated as a leaf.
func derefValue(rv reflect.Value) reflect.Value {
	for i := 0; i < maxEncodeDepth && (rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface); i++ {
		if rv.IsNil() {
			return reflect.Value{}
		}
		rv = rv.Elem()
	}
	return rv
}

// goChildren returns the nested Go values gotoml's encoder recurses into for rv
// (map values, slice/array elements, exported struct fields), dereferencing
// pointers and interfaces first. Scalars (and nils) have no children.
func goChildren(rv reflect.Value) []reflect.Value {
	rv = derefValue(rv)
	switch rv.Kind() {
	case reflect.Map:
		out := make([]reflect.Value, 0, rv.Len())
		for _, k := range rv.MapKeys() {
			out = append(out, rv.MapIndex(k))
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]reflect.Value, rv.Len())
		for i := range out {
			out[i] = rv.Index(i)
		}
		return out
	case reflect.Struct:
		var out []reflect.Value
		for i := 0; i < rv.NumField(); i++ {
			if f := rv.Field(i); f.CanInterface() { // skip unexported fields the encoder ignores
				out = append(out, f)
			}
		}
		return out
	}
	return nil
}

func iterChildren(it starlark.Iterable) []starlark.Value {
	var out []starlark.Value
	iter := it.Iterate()
	defer iter.Done()
	var e starlark.Value
	for iter.Next(&e) {
		out = append(out, e)
	}
	return out
}

func attrChildren(h starlark.HasAttrs) []starlark.Value {
	var out []starlark.Value
	for _, name := range h.AttrNames() {
		if a, err := h.Attr(name); err == nil && a != nil {
			out = append(out, a)
		}
	}
	return out
}

func upper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
