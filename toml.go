// Package toml provides a Starlark module for decoding and encoding TOML.
//
// Decoding is hardened: input size, nesting depth, and total node count are
// bounded (capwalk), and panics are recovered into errors. TOML's native
// date/time values are surfaced as strings (RFC 3339 for full timestamps),
// never as surprise opaque values.
package toml

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"github.com/1set/starlet"
	"github.com/1set/starlet/dataconv"
	"github.com/1set/starlet/dataconv/types"
	gotoml "github.com/BurntSushi/toml"
	"github.com/starpkg/base"
	"go.starlark.net/starlark"
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
		genConfigOption(configKeyMaxDepth, "Maximum nesting depth when decoding", defaultMaxDepth),
		genConfigOption(configKeyMaxNodes, "Maximum total nodes when decoding", defaultMaxNodes),
		genConfigOption(configKeyMaxInputBytes, "Maximum input size in bytes when decoding", defaultMaxInputBytes),
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
	if maxBytes := m.ext.GetInt(configKeyMaxInputBytes); maxBytes > 0 && len(text.GoString()) > maxBytes {
		return none, fmt.Errorf("toml.decode: input exceeds max_input_bytes (%d)", maxBytes)
	}
	parsed, err := unmarshal(text.GoString())
	if err != nil {
		return none, err
	}
	nodes := 0
	return toStarlark(parsed, 1, &nodes, m.ext.GetInt(configKeyMaxDepth), m.ext.GetInt(configKeyMaxNodes))
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
	goVal, err := dataconv.Unmarshal(value)
	if err != nil {
		return none, fmt.Errorf("toml.encode: %w", err)
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
