package toml

// Tests for the toml module.
//
// Sections:
//   - decode / encode round-trip
//   - date/time taming
//   - encode requires a table
//   - capwalk limits

import (
	"strings"
	"testing"

	"github.com/1set/starlet"
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
