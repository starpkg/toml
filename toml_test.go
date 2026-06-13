package toml

// Tests for the toml module.
//
// Sections:
//   - decode / encode round-trip
//   - date/time taming
//   - encode requires a table
//   - capwalk limits
//   - toStarlark value branches (array-of-tables, uint64, Stringer)

import (
	"strings"
	"testing"

	"github.com/1set/starlet"
	"go.starlark.net/starlark"
)

func run(t *testing.T, script string) (map[string]interface{}, error) {
	t.Helper()
	m := starlet.NewDefault()
	m.SetScriptContent([]byte(script))
	m.SetLazyloadModules(map[string]starlet.ModuleLoader{ModuleName: NewModule().LoadModule()})
	return m.Run()
}

// --- decode / encode ---------------------------------------------------------

func TestDecode(t *testing.T) {
	res, err := run(t, `
load("toml", "decode")
doc = decode("""
name = "Ada"
age = 36
langs = ["go", "python"]

[nested]
k = "v"
""")
name = doc["name"]
age = doc["age"]
first = doc["langs"][0]
nv = doc["nested"]["k"]
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["name"] != "Ada" || res["age"] != int64(36) || res["first"] != "go" || res["nv"] != "v" {
		t.Errorf("decoded wrong: %v %v %v %v", res["name"], res["age"], res["first"], res["nv"])
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	res, err := run(t, `
load("toml", "decode", "encode")
text = encode({"a": 1, "b": [1, 2], "c": "x"})
back = decode(text)
a = back["a"]
b1 = back["b"][1]
c = back["c"]
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["a"] != int64(1) || res["b1"] != int64(2) || res["c"] != "x" {
		t.Errorf("round-trip wrong: a=%v b1=%v c=%v", res["a"], res["b1"], res["c"])
	}
}

// --- date/time taming --------------------------------------------------------

func TestDatetimeIsString(t *testing.T) {
	res, err := run(t, `
load("toml", "decode")
doc = decode("when = 2020-01-02T03:04:05Z\nday = 2020-01-02")
when_str = type(doc["when"]) == "string"
day_str = type(doc["day"]) == "string"
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["when_str"] != true || res["day_str"] != true {
		t.Errorf("datetime/date should be strings: when=%v day=%v", res["when_str"], res["day_str"])
	}
}

// --- encode requires a table -------------------------------------------------

func TestEncodeRequiresDict(t *testing.T) {
	_, err := run(t, `
load("toml", "encode")
encode([1, 2, 3])
`)
	if err == nil || !strings.Contains(err.Error(), "must be a dict") {
		t.Errorf("expected dict-required error, got %v", err)
	}
}

// --- capwalk limits ----------------------------------------------------------

func TestCapDepth(t *testing.T) {
	nested := []interface{}{[]interface{}{[]interface{}{int(1)}}}
	nodes := 0
	if _, err := toStarlark(nested, 1, &nodes, 2, 1000); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("expected max_depth error, got %v", err)
	}
}

func TestCapNodes(t *testing.T) {
	list := make([]interface{}, 10)
	for i := range list {
		list[i] = int(i)
	}
	nodes := 0
	if _, err := toStarlark(list, 1, &nodes, 64, 3); err == nil || !strings.Contains(err.Error(), "max_nodes") {
		t.Errorf("expected max_nodes error, got %v", err)
	}
}

func TestCapInputBytes(t *testing.T) {
	t.Setenv("TOML_MAX_INPUT_BYTES", "8")
	_, err := run(t, `
load("toml", "decode")
decode("a = 12345678901234567890")
`)
	if err == nil || !strings.Contains(err.Error(), "max_input_bytes") {
		t.Errorf("expected max_input_bytes error, got %v", err)
	}
}

// --- toStarlark value branches -----------------------------------------------

// Array of tables ([[items]]) decodes to []map[string]interface{}; exercise it
// end-to-end through the script path so the dedicated slice branch is covered.
func TestDecodeArrayOfTables(t *testing.T) {
	res, err := run(t, `
load("toml", "decode")
doc = decode("""
[[items]]
name = "a"
qty = 1

[[items]]
name = "b"
qty = 2
""")
n = len(doc["items"])
first = doc["items"][0]["name"]
second_qty = doc["items"][1]["qty"]
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["n"] != int64(2) || res["first"] != "a" || res["second_qty"] != int64(2) {
		t.Errorf("array-of-tables wrong: n=%v first=%v second_qty=%v", res["n"], res["first"], res["second_qty"])
	}
}

// A uint64 cannot be produced by decoding TOML text (BurntSushi yields int64 and
// errors on overflow), but toStarlark handles it defensively; cover that arm
// with a direct call, matching the capwalk tests' style.
func TestToStarlarkUint64(t *testing.T) {
	nodes := 0
	v, err := toStarlark(uint64(18446744073709551615), 1, &nodes, 64, 1000)
	if err != nil {
		t.Fatalf("toStarlark(uint64): %v", err)
	}
	got, ok := v.(starlark.Int)
	if !ok {
		t.Fatalf("expected starlark.Int, got %T", v)
	}
	want := starlark.MakeUint64(18446744073709551615)
	if cmp, cerr := got.Cmp(want, 0); cerr != nil || cmp != 0 {
		t.Errorf("uint64 wrong: got %v want %v (cmp=%d err=%v)", got, want, cmp, cerr)
	}
}

// stringerVal is a non-time fmt.Stringer, standing in for any value that reaches
// the default-case Stringer fallthrough in toStarlark.
type stringerVal struct{ s string }

func (sv stringerVal) String() string { return sv.s }

func TestToStarlarkStringerFallthrough(t *testing.T) {
	nodes := 0
	v, err := toStarlark(stringerVal{s: "1979-05-27"}, 1, &nodes, 64, 1000)
	if err != nil {
		t.Fatalf("toStarlark(Stringer): %v", err)
	}
	got, ok := starlark.AsString(v)
	if !ok || got != "1979-05-27" {
		t.Errorf("Stringer fallthrough wrong: ok=%v got=%q", ok, got)
	}
}

// A value that is neither a known kind nor a Stringer is rejected, covering the
// final error arm.
func TestToStarlarkUnsupported(t *testing.T) {
	nodes := 0
	if _, err := toStarlark(struct{ X int }{X: 1}, 1, &nodes, 64, 1000); err == nil ||
		!strings.Contains(err.Error(), "unsupported value of type") {
		t.Errorf("expected unsupported-type error, got %v", err)
	}
}
