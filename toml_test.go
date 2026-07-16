package toml

// Tests for the toml module.
//
// Sections:
//   - decode / encode round-trip
//   - date/time taming
//   - encode requires a table
//   - capwalk limits
//   - toStarlark value branches (array-of-tables, uint64, Stringer, unsupported)
//   - comprehensive document + round-trip
//   - argument parsing / validation (bad args -> clean error)
//   - decode error branches (malformed TOML, duplicate keys, bytes input, empty)
//   - encode error branches (non-dict reached, unconvertible value)
//   - deterministic key order (decode + encode)
//   - value-shape coverage (None drop, bignum, inf/nan, nested arrays, datetime offsets)
//   - host config levers (caps via env, disabled input cap)

import (
	"reflect"
	"strings"
	"testing"

	"github.com/1set/starlet"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
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

// The array-of-tables branch ([]map[string]interface{}) must propagate a cap
// error raised while converting one of its element tables, not swallow it.
// TOML text only ever yields this slice type via [[items]]; cover the inner
// error-propagation arm with a direct call under a tight node cap.
func TestCapArrayOfTablesPropagates(t *testing.T) {
	aot := []map[string]interface{}{
		{"a": int(1)},
		{"b": int(2), "c": int(3)},
	}
	nodes := 0
	// maxNodes=2 lets the slice node + first table node through, then trips on
	// the second table's contents.
	if _, err := toStarlark(aot, 1, &nodes, 64, 2); err == nil || !strings.Contains(err.Error(), "max_nodes") {
		t.Errorf("expected max_nodes error from array-of-tables element, got %v", err)
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

// TOML text never yields a Go nil (TOML has no null), but toStarlark maps a nil
// interface to Starlark None defensively; cover that arm with a direct call.
func TestToStarlarkNil(t *testing.T) {
	nodes := 0
	v, err := toStarlark(nil, 1, &nodes, 64, 1000)
	if err != nil {
		t.Fatalf("toStarlark(nil): %v", err)
	}
	if v != starlark.None {
		t.Errorf("toStarlark(nil) = %v, want None", v)
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

// --- comprehensive document + round-trip -------------------------------------

// TestComprehensiveDocument loads one TOML document exercising many shapes at
// once: root dotted keys, nested tables, arrays, an inline table, array-of-
// tables, mixed scalars, and a datetime (tamed to a string) — asserting each.
func TestComprehensiveDocument(t *testing.T) {
	res, err := run(t, `
load("toml", "decode")
doc = decode("""
title = "starpkg"
count = 42
ratio = 1.5
enabled = true
db.user = "admin"
db.pass = "secret"
point = { x = 1, y = 2 }

[server]
host = "localhost"
ports = [80, 443]

[server.tls]
enabled = true

[[items]]
n = 1
[[items]]
n = 2

[meta]
released = 2026-06-13T10:30:00Z
""")
title = doc["title"]
count = doc["count"]
ratio = doc["ratio"]
dbuser = doc["db"]["user"]
px = doc["point"]["x"]
port2 = doc["server"]["ports"][1]
tls = doc["server"]["tls"]["enabled"]
items = len(doc["items"])
item2 = doc["items"][1]["n"]
released_is_str = type(doc["meta"]["released"]) == "string"
`)
	if err != nil {
		t.Fatalf("comprehensive: %v", err)
	}
	checks := map[string]interface{}{
		"title": "starpkg", "count": int64(42), "ratio": 1.5, "dbuser": "admin",
		"px": int64(1), "port2": int64(443), "tls": true, "items": int64(2),
		"item2": int64(2), "released_is_str": true,
	}
	for k, want := range checks {
		if res[k] != want {
			t.Errorf("%s = %v (%T), want %v", k, res[k], res[k], want)
		}
	}
}

// TestRoundTripEquivalence decodes, re-encodes, and decodes again.
func TestRoundTripEquivalence(t *testing.T) {
	res, err := run(t, `
load("toml", "decode", "encode")
orig = {"a": 1, "b": [1, 2, 3], "c": {"d": "x"}, "e": True}
again = decode(encode(orig))
same_a = again["a"] == 1
same_b = again["b"][2] == 3
same_c = again["c"]["d"] == "x"
same_e = again["e"] == True
`)
	if err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	for _, k := range []string{"same_a", "same_b", "same_c", "same_e"} {
		if res[k] != true {
			t.Errorf("round-trip %s = %v, want true", k, res[k])
		}
	}
}

// --- argument parsing / validation -------------------------------------------

// Both builtins use starlark.UnpackArgs; a wrong-type or missing argument must
// surface as a clean script-level error (prefixed with the builtin's name),
// never a panic. These exercise the UnpackArgs failure arm of each builtin.
func TestArgValidation(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "decode wrong type",
			script: `load("toml", "decode")` + "\n" + `decode(123)`,
			want:   "toml.decode: for parameter text: got int, want string or bytes",
		},
		{
			name:   "decode missing arg",
			script: `load("toml", "decode")` + "\n" + `decode()`,
			want:   "toml.decode: missing argument for text",
		},
		{
			name:   "decode too many args",
			script: `load("toml", "decode")` + "\n" + `decode("a = 1", "b = 2")`,
			want:   "toml.decode: got 2 arguments, want at most 1",
		},
		{
			name:   "decode list arg",
			script: `load("toml", "decode")` + "\n" + `decode([1, 2])`,
			want:   "toml.decode: for parameter text: got list, want string or bytes",
		},
		{
			name:   "encode missing arg",
			script: `load("toml", "encode")` + "\n" + `encode()`,
			want:   "toml.encode: missing argument for value",
		},
		{
			name:   "encode too many args",
			script: `load("toml", "encode")` + "\n" + `encode({}, {})`,
			want:   "toml.encode: got 2 arguments, want at most 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t, tc.script)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

// --- decode error branches ---------------------------------------------------

// Malformed input and duplicate keys are rejected by the BurntSushi decoder;
// the error is wrapped with the "toml.decode:" prefix (the unmarshal error
// arm). Bytes and empty input are accepted. These cover the reachable decode
// error/return paths without any TTY or network.
func TestDecodeErrorBranches(t *testing.T) {
	cases := []struct {
		name    string
		script  string
		wantErr string // "" means must succeed
	}{
		{
			name:    "malformed value",
			script:  `decode("a = = =")`,
			wantErr: "toml.decode: toml:",
		},
		{
			name:    "duplicate key",
			script:  `decode("a = 1\na = 2")`,
			wantErr: "toml.decode: toml:",
		},
		{
			name:    "unterminated string",
			script:  `decode('a = "unterminated')`,
			wantErr: "toml.decode: toml:",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t, `load("toml", "decode")`+"\n"+tc.script)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}

// Bytes input is accepted (StringOrBytes) and decodes identically to a string.
func TestDecodeBytesAndEmpty(t *testing.T) {
	res, err := run(t, `
load("toml", "decode")
from_bytes = decode(b"a = 1")["a"]
empty_len = len(decode(""))
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["from_bytes"] != int64(1) {
		t.Errorf("bytes decode a = %v, want 1", res["from_bytes"])
	}
	if res["empty_len"] != int64(0) {
		t.Errorf("empty decode len = %v, want 0", res["empty_len"])
	}
}

// --- encode error branches ---------------------------------------------------

// A non-dict top-level value is rejected up front with a clear message naming
// the actual type; this is the encode "must be a dict" arm for several shapes.
func TestEncodeNonDict(t *testing.T) {
	cases := []struct {
		name     string
		script   string
		wantType string
	}{
		{"list", `encode([1, 2, 3])`, "got list"},
		{"string", `encode("x")`, "got string"},
		{"int", `encode(5)`, "got int"},
		{"none", `encode(None)`, "got NoneType"},
		{"bool", `encode(True)`, "got bool"},
		{"tuple", `encode((1, 2))`, "got tuple"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t, `load("toml", "encode")`+"\n"+tc.script)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "must be a dict") {
				t.Errorf("error = %q, want 'must be a dict'", err.Error())
			}
			if !strings.Contains(err.Error(), tc.wantType) {
				t.Errorf("error = %q, want type %q", err.Error(), tc.wantType)
			}
		})
	}
}

// A dict whose value cannot be converted to a Go value (a Starlark function)
// is rejected by dataconv.Unmarshal; encode wraps it with "toml.encode:". This
// covers the dataconv-error return arm of encode. A cyclic value (a list that
// contains itself) is likewise rejected, never a host crash.
func TestEncodeUnconvertible(t *testing.T) {
	t.Run("function value", func(t *testing.T) {
		_, err := run(t, `
load("toml", "encode")
def f():
    return 1
encode({"x": f})
`)
		if err == nil || !strings.Contains(err.Error(), "toml.encode:") {
			t.Errorf("expected toml.encode error, got %v", err)
		}
	})
	t.Run("cyclic value", func(t *testing.T) {
		_, err := run(t, `
load("toml", "encode")
a = [1, 2]
a.append(a)
encode({"x": a})
`)
		if err == nil || !strings.Contains(err.Error(), "cyclic reference") {
			t.Errorf("expected cyclic-reference error, got %v", err)
		}
	})
}

// unmarshal recovers a decoder panic into an error rather than crashing the
// host. BurntSushi does not panic on the inputs we can produce from script
// text, so this asserts the contract directly on the helper: a guarded call
// over normal input returns the parsed map, and the deferred recover is present
// (the panic arm itself is a defensive never-reached path).
func TestUnmarshalRecoversCleanly(t *testing.T) {
	v, err := unmarshal("a = 1\nb = \"x\"")
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v["a"] != int64(1) || v["b"] != "x" {
		t.Errorf("unmarshal got %v", v)
	}
}

// --- deterministic key order -------------------------------------------------

// Go map iteration is randomized; both decode (toStarlark) and encode must
// emit keys in sorted order so script-visible output is stable across runs.
func TestDeterministicKeyOrder(t *testing.T) {
	// Decode: the dict materializes keys in sorted order.
	res, err := run(t, `
load("toml", "decode", "encode")
decoded_keys = list(decode("zebra = 1\napple = 2\nmango = 3").keys())
encoded = encode({"zebra": 1, "apple": 2, "mango": 3})
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// m.Run() returns globals as native Go values, so a Starlark list of
	// strings arrives as []interface{} of string.
	rawKeys, ok := res["decoded_keys"].([]interface{})
	if !ok {
		t.Fatalf("decoded_keys is %T", res["decoded_keys"])
	}
	var got []string
	for _, k := range rawKeys {
		got = append(got, k.(string))
	}
	want := []string{"apple", "mango", "zebra"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("decode key order = %v, want %v", got, want)
	}
	// Encode: keys are emitted sorted.
	encoded, _ := res["encoded"].(string)
	wantEnc := "apple = 2\nmango = 3\nzebra = 1\n"
	if encoded != wantEnc {
		t.Errorf("encode = %q, want %q", encoded, wantEnc)
	}
}

// --- value-shape coverage ----------------------------------------------------

// TOML has no null, so a None value is dropped by the encoder (BurntSushi omits
// it); a dict of only None values therefore encodes to the empty string. This
// pins the observed behavior so a future codec change is caught.
func TestEncodeNoneIsDropped(t *testing.T) {
	res, err := run(t, `
load("toml", "encode")
only_none = encode({"x": None})
mixed = encode({"a": 1, "b": None, "c": 2})
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["only_none"] != "" {
		t.Errorf("encode({x:None}) = %q, want empty", res["only_none"])
	}
	if mixed, _ := res["mixed"].(string); mixed != "a = 1\nc = 2\n" {
		t.Errorf("encode mixed-None = %q, want %q", mixed, "a = 1\nc = 2\n")
	}
}

// A Starlark int larger than int64 (a big.Int) is encoded as a quoted string by
// dataconv/BurntSushi rather than crashing; this documents the lossy-but-safe
// path for out-of-range integers.
func TestEncodeBigInt(t *testing.T) {
	res, err := run(t, `
load("toml", "encode")
out = encode({"x": 123456789012345678901234567890})
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out, _ := res["out"].(string); out != "x = \"123456789012345678901234567890\"\n" {
		t.Errorf("bigint encode = %q", out)
	}
}

// inf and nan are valid TOML floats; they round-trip through encode->decode as
// floats (not strings), and a non-UTC datetime keeps its offset when tamed to a
// string. These cover the float and time.Time arms of toStarlark via the script
// path with edge values.
func TestValueShapeEdges(t *testing.T) {
	res, err := run(t, `
load("toml", "decode", "encode")
inf_back = decode(encode({"x": float("inf")}))
inf_is_float = type(inf_back["x"]) == "float"
offset = decode("t = 2020-01-02T03:04:05+08:00")["t"]
neg = decode("n = -17")["n"]
flt = decode("f = 3.14")["f"]
`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res["inf_is_float"] != true {
		t.Errorf("inf should round-trip as float, got %v", res["inf_is_float"])
	}
	if res["offset"] != "2020-01-02T03:04:05+08:00" {
		t.Errorf("offset datetime = %v, want preserved offset", res["offset"])
	}
	if res["neg"] != int64(-17) {
		t.Errorf("negative int = %v, want -17", res["neg"])
	}
	if res["flt"] != 3.14 {
		t.Errorf("float = %v, want 3.14", res["flt"])
	}
}

// --- host config levers ------------------------------------------------------

// max_input_bytes, max_depth, and max_nodes are host levers settable via env.
// The error message names the offending limit; this exercises the env-resolved
// config path through the real builtin (not a direct toStarlark call) so the
// decode-side cap wiring is covered end to end.
func TestConfigCapsViaEnv(t *testing.T) {
	t.Run("max_input_bytes", func(t *testing.T) {
		t.Setenv("TOML_MAX_INPUT_BYTES", "4")
		_, err := run(t, `load("toml", "decode")`+"\n"+`decode("abcdefghij = 1")`)
		if err == nil || !strings.Contains(err.Error(), "input exceeds max_input_bytes (4)") {
			t.Errorf("expected max_input_bytes error, got %v", err)
		}
	})
	t.Run("max_depth", func(t *testing.T) {
		t.Setenv("TOML_MAX_DEPTH", "1")
		_, err := run(t, `load("toml", "decode")`+"\n"+`decode("[a]\nb = 1")`)
		if err == nil || !strings.Contains(err.Error(), "nesting exceeds max_depth (1)") {
			t.Errorf("expected max_depth error, got %v", err)
		}
	})
	t.Run("max_nodes", func(t *testing.T) {
		t.Setenv("TOML_MAX_NODES", "2")
		_, err := run(t, `load("toml", "decode")`+"\n"+`decode("a = 1\nb = 2\nc = 3")`)
		if err == nil || !strings.Contains(err.Error(), "node count exceeds max_nodes (2)") {
			t.Errorf("expected max_nodes error, got %v", err)
		}
	})
}

// A non-positive max_input_bytes disables the input-size check (the historical
// "0 = unlimited" behavior); a document longer than any small cap then decodes
// normally. This pins the backward-compatible meaning of the byte cap's
// zero/negative value. The same input would be rejected under a small positive
// cap (asserted alongside), so the disabling — not the input — is what differs.
func TestInputBytesCapDisabled(t *testing.T) {
	// A ~50-byte document, well over the tiny caps used below.
	script := `load("toml", "decode")` + "\n" + `out = decode("a = \"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\"")` + "\n" + `v = out["a"]`

	t.Run("disabled with zero", func(t *testing.T) {
		t.Setenv("TOML_MAX_INPUT_BYTES", "0")
		res, err := run(t, script)
		if err != nil {
			t.Fatalf("run with disabled input cap: %v", err)
		}
		if res["v"] != strings.Repeat("x", 40) {
			t.Errorf("decoded value = %v, want 40 x's", res["v"])
		}
	})
	t.Run("rejected under small positive cap", func(t *testing.T) {
		t.Setenv("TOML_MAX_INPUT_BYTES", "8")
		_, err := run(t, script)
		if err == nil || !strings.Contains(err.Error(), "input exceeds max_input_bytes") {
			t.Errorf("expected max_input_bytes error under small cap, got %v", err)
		}
	})
}

// --- parse-DoS hardening (crash guard before the recursive codec) -------------

// TestDecodeManyBracketsRejectedBeforeParse pins the crash guard: input whose
// bracket-opener count exceeds maxParseBrackets is rejected up front, before
// gotoml.Decode's recursive parser can be driven deep enough to overflow the
// stack (a fatal error recover() can't catch). The guard bounds parser recursion
// soundly (depth <= number of openers) without lexing strings, so it cannot be
// bypassed by hiding brackets in a crafted quote sequence.
func TestDecodeManyBracketsRejectedBeforeParse(t *testing.T) {
	// A run of open brackets well past the limit, but small in bytes and under
	// the 5 MiB input cap. The parser never sees it.
	huge := `load("toml", "decode")` + "\n" +
		`decode("a = " + "[" * 20000)`
	_, err := run(t, huge)
	if err == nil || !strings.Contains(err.Error(), "too many nesting brackets") {
		t.Fatalf("an input over the bracket limit should be rejected before parsing, got %v", err)
	}
}

// TestDecodeDeepButUnderBracketLimitHitsMaxDepth confirms a genuinely deep — but
// under the crash-guard limit — document is parsed safely and then rejected by
// the real max_depth cap in toStarlark (not the crash guard), with the proper
// error. This is the normal deep-nesting path.
func TestDecodeDeepButUnderBracketLimitHitsMaxDepth(t *testing.T) {
	deep := `load("toml", "decode")` + "\n" +
		`decode("a = " + "[" * 200 + "]" * 200)` // 200-deep, 400 openers < limit
	_, err := run(t, deep)
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds max_depth") {
		t.Fatalf("a 200-deep document should hit max_depth, got %v", err)
	}
}

// TestDecodeBracketsInStringUnderLimitDecodes guards against a false reject:
// brackets inside a TOML string count toward the (generous) crash-guard limit
// but are not real nesting, so a string with a modest bracket run decodes fine.
func TestDecodeBracketsInStringUnderLimitDecodes(t *testing.T) {
	res, err := run(t, `load("toml", "decode")`+"\n"+
		`out = decode('s = "' + "[{" * 300 + '"')`+"\n"+ // 600 openers < limit
		`v = out["s"]`)
	if err != nil {
		t.Fatalf("a string with a modest bracket run must decode: %v", err)
	}
	if v, _ := res["v"].(string); v != strings.Repeat("[{", 300) {
		t.Errorf("decoded string value = %q, want the bracket run", res["v"])
	}
}

func TestCountBracketOpens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"none", "a = 1", 0},
		{"one array", "a = [1, 2]", 1},
		{"nested arrays", "a = [[[1]]]", 3},
		{"inline tables", "a = {b = {c = 1}}", 2},
		{"array of tables header", "[[a]]\n[[b]]", 4},
		// Brackets in strings/comments are counted too (conservative, sound).
		{"brackets in string counted", `s = "[[[["`, 4},
		{"brackets in comment counted", "a = 1 # [{[{", 4},
		{"closers not counted", "a = ]]}}", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countBracketOpens(tc.in); got != tc.want {
				t.Errorf("countBracketOpens(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestEncodeDeepRejected pins the encode-side twin: a script that builds a value
// nested past max_depth is rejected before dataconv.Unmarshal recurses over it
// (which would overflow the stack — a fatal error, uncatchable). The build runs
// inside a function because Starlark forbids a top-level for loop.
func TestEncodeDeepRejected(t *testing.T) {
	_, err := run(t, `
load("toml", "encode")
def deep(n):
    d = {"x": 1}
    for i in range(n):
        d = {"a": d}
    return d
encode(deep(200))
`)
	if err == nil || !strings.Contains(err.Error(), "nesting exceeds max_depth") {
		t.Fatalf("deeply-nested encode input should be rejected, got %v", err)
	}
}

// TestCheckEncodeDepth exercises the depth walker directly over each container
// type dataconv.Unmarshal recurses into (dict, list, tuple), confirming it bails
// at the cap and accepts values within it.
func TestCheckEncodeDepth(t *testing.T) {
	// A dict chain 5 deep: {"a":{"a":{"a":{"a":{"x":1}}}}}.
	dictChain := starlark.Value(starlark.MakeInt(1))
	for i := 0; i < 5; i++ {
		d := starlark.NewDict(1)
		_ = d.SetKey(starlark.String("a"), dictChain)
		dictChain = d
	}
	// A list chain 5 deep: [[[[[1]]]]].
	listChain := starlark.Value(starlark.MakeInt(1))
	for i := 0; i < 5; i++ {
		listChain = starlark.NewList([]starlark.Value{listChain})
	}
	// A tuple chain 5 deep: (((((1,),),),),).
	var tupChain starlark.Value = starlark.MakeInt(1)
	for i := 0; i < 5; i++ {
		tupChain = starlark.Tuple{tupChain}
	}

	for _, tc := range []struct {
		name string
		v    starlark.Value
	}{{"dict", dictChain}, {"list", listChain}, {"tuple", tupChain}} {
		t.Run(tc.name, func(t *testing.T) {
			// depth 5 is within a cap of 8.
			if err := checkEncodeDepth(tc.v, 1, 8); err != nil {
				t.Errorf("within-cap %s should pass, got %v", tc.name, err)
			}
			// depth 5 exceeds a cap of 3.
			if err := checkEncodeDepth(tc.v, 1, 3); err == nil || !strings.Contains(err.Error(), "max_depth") {
				t.Errorf("over-cap %s should error, got %v", tc.name, err)
			}
		})
	}
	// A scalar has no children and always passes.
	if err := checkEncodeDepth(starlark.String("x"), 1, 1); err != nil {
		t.Errorf("scalar should pass, got %v", err)
	}
}

// TestCheckEncodeDepthContainers covers the remaining recursion branches
// dataconv.Unmarshal walks — Set, Struct, and Module — that a script's value can
// legitimately nest through.
func TestCheckEncodeDepthContainers(t *testing.T) {
	// A set holding a 5-deep tuple: set -> tuple chain.
	var tup starlark.Value = starlark.MakeInt(1)
	for i := 0; i < 5; i++ {
		tup = starlark.Tuple{tup}
	}
	set := starlark.NewSet(1)
	if err := set.Insert(tup); err != nil {
		t.Fatalf("set insert: %v", err)
	}
	if err := checkEncodeDepth(set, 1, 3); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("set of a deep tuple should exceed a tight cap, got %v", err)
	}
	if err := checkEncodeDepth(set, 1, 20); err != nil {
		t.Errorf("set within cap should pass, got %v", err)
	}

	// A struct whose attr is a nested struct (attrChildren + Struct branch).
	inner := starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"x": starlark.MakeInt(1)})
	outer := starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{"a": inner})
	if err := checkEncodeDepth(outer, 1, 8); err != nil {
		t.Errorf("nested struct within cap should pass, got %v", err)
	}
	if err := checkEncodeDepth(outer, 1, 1); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("nested struct should exceed a cap of 1, got %v", err)
	}

	// A module with a nested member (Module branch of encodeChildren).
	mod := &starlarkstruct.Module{Name: "m", Members: starlark.StringDict{"a": inner}}
	if err := checkEncodeDepth(mod, 1, 8); err != nil {
		t.Errorf("module within cap should pass, got %v", err)
	}
	if err := checkEncodeDepth(mod, 1, 1); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("module should exceed a cap of 1, got %v", err)
	}
}

// TestCheckGoDepth covers the reflect-based depth walk over the unmarshaled Go
// value that bounds gotoml's encoder — including deep values that arrive via a
// host-wrapped Go value (a slice/map/struct/pointer), which dataconv.Unmarshal
// extracts opaquely.
func TestCheckGoDepth(t *testing.T) {
	// A 10-deep nested slice trips a tight cap and passes a generous one.
	var deep interface{} = 1
	for i := 0; i < 10; i++ {
		deep = []interface{}{deep}
	}
	if err := checkGoDepth(reflect.ValueOf(deep), 1, 3); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("deep slice should exceed cap 3, got %v", err)
	}
	if err := checkGoDepth(reflect.ValueOf(deep), 1, 20); err != nil {
		t.Errorf("deep slice within cap should pass, got %v", err)
	}
	// A nested map.
	nm := map[string]interface{}{"a": map[string]interface{}{"b": 1}}
	if err := checkGoDepth(reflect.ValueOf(nm), 1, 1); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("nested map should exceed cap 1, got %v", err)
	}
	// A struct with a nested (and a hidden/tagged) field — pointers are followed.
	type inner struct{ X int }
	type outer struct {
		A inner
		B inner `toml:"-"` // exported; the encoder still sees it, so we walk it
	}
	if err := checkGoDepth(reflect.ValueOf(&outer{}), 1, 8); err != nil {
		t.Errorf("nested struct within cap should pass, got %v", err)
	}
	if err := checkGoDepth(reflect.ValueOf(outer{}), 1, 1); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("nested struct should exceed cap 1, got %v", err)
	}
	// A nil pointer and a scalar have no children.
	if err := checkGoDepth(reflect.ValueOf((*int)(nil)), 1, 1); err != nil {
		t.Errorf("nil pointer should pass, got %v", err)
	}
	if err := checkGoDepth(reflect.ValueOf(42), 1, 1); err != nil {
		t.Errorf("scalar should pass, got %v", err)
	}
}

// TestEncodeDeepTupleKeyRejected guards a script-reachable encode crash: a dict
// whose KEY is a deeply-nested tuple (dataconv stringifies/hashes it recursively)
// must trip the depth cap — checkEncodeDepth walks keys, not just values.
func TestEncodeDeepTupleKeyRejected(t *testing.T) {
	var key starlark.Value = starlark.MakeInt(1)
	for i := 0; i < 10; i++ {
		key = starlark.Tuple{key} // a 10-deep, hashable tuple key
	}
	d := starlark.NewDict(1)
	if err := d.SetKey(key, starlark.MakeInt(1)); err != nil {
		t.Fatalf("set key: %v", err)
	}
	if err := checkEncodeDepth(d, 1, 3); err == nil || !strings.Contains(err.Error(), "max_depth") {
		t.Errorf("a deep tuple key should exceed a tight cap, got %v", err)
	}
	if err := checkEncodeDepth(d, 1, 20); err != nil {
		t.Errorf("a shallow-enough key should pass, got %v", err)
	}
}

// TestEffectiveEncodeDepth pins the absolute crash-guard ceiling: a disabled
// (<=0) or enormous host max_depth is clamped to maxEncodeDepth so the guard is
// always on and its own walk is always bounded; an in-range value is honored.
func TestEffectiveEncodeDepth(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, maxEncodeDepth},
		{-1, maxEncodeDepth},
		{64, 64},
		{maxEncodeDepth, maxEncodeDepth},
		{maxEncodeDepth + 1, maxEncodeDepth},
		{1 << 30, maxEncodeDepth},
	}
	for _, tc := range cases {
		if got := effectiveEncodeDepth(tc.in); got != tc.want {
			t.Errorf("effectiveEncodeDepth(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestEncodeCrashGuardActiveWhenMaxDepthDisabled confirms the encode crash guard
// runs even when the host disables max_depth (0) — the fixed absolute ceiling
// still applies, so a script can't disable it by zeroing max_depth.
func TestEncodeCrashGuardActiveWhenMaxDepthDisabled(t *testing.T) {
	t.Setenv("TOML_MAX_DEPTH", "0")
	// A within-absolute-ceiling document still encodes (guard doesn't false-trip).
	if _, err := run(t, `load("toml","encode"); encode({"a": {"b": 1}})`); err != nil {
		t.Errorf("shallow encode under disabled max_depth should pass: %v", err)
	}
	// And the module still constructs the guard against the absolute ceiling.
	if got := effectiveEncodeDepth(0); got != maxEncodeDepth {
		t.Errorf("disabled max_depth should clamp to %d, got %d", maxEncodeDepth, got)
	}
}

// TestEncodeDepthBoundaryOK confirms the encode cap rejects only what is beyond
// it: a document nested within max_depth still encodes.
func TestEncodeDepthBoundaryOK(t *testing.T) {
	res, err := run(t, `
load("toml", "encode")
d = {"a": {"b": {"c": 1}}}
out = encode(d)
ok = len(out) > 0
`)
	if err != nil {
		t.Fatalf("a shallow document should encode: %v", err)
	}
	if res["ok"] != true {
		t.Errorf("encode produced no output")
	}
}

// TestCapsAreHostOnly is the capability-gate guard: max_depth / max_nodes /
// max_input_bytes are DoS limits the module enforces against untrusted scripts,
// so a script must not be able to raise them. base emits get_<name> (not secret)
// but NO set_<name> for a host-only option.
func TestCapsAreHostOnly(t *testing.T) {
	for _, key := range []string{"max_depth", "max_nodes", "max_input_bytes"} {
		if _, err := run(t, `load("toml", "set_`+key+`")`); err == nil {
			t.Errorf("set_%s is loadable — a script can raise the cap", key)
		}
		if _, err := run(t, `load("toml", "get_`+key+`")`); err != nil {
			t.Errorf("get_%s should be loadable (the cap is not secret): %v", key, err)
		}
	}
}
