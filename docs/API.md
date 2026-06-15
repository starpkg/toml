# `toml` — Starlark API Reference

The complete reference for every script-facing builtin and configuration
accessor exposed by the `toml` module. For an overview, installation, and a
quickstart, see the [README](../README.md).

The module exposes two top-level builtins via `load("toml", …)` — `decode` and
`encode` — plus a set of configuration accessors (`get_<key>` / `set_<key>`)
generated from the module's options. `toml` is a pure in-process codec: no
network, no filesystem, no host services — input is a string (or bytes), output
is a string or Starlark values, and nothing leaves the process.

## Contents

- [Functions](#functions)
  - [`decode`](#decodetext---dict)
  - [`encode`](#encodevalue---str)
- [Decoded value types](#decoded-value-types)
- [Hardening](#hardening)
- [Notes](#notes)
- [Configuration](#configuration)

## Functions

### `decode(text) -> dict`

Parses a TOML document into Starlark values, returning the top-level table as a
`dict`.

**Parameters:**

- `text` (string or bytes): The TOML document to parse. Bytes are accepted and
  decode identically to the equivalent string.

**Returns:** A `dict` mapping the document's top-level keys to their decoded
values. See [Decoded value types](#decoded-value-types) for how each TOML type
maps to a Starlark value. Map keys are materialized in sorted order.

**Errors:** Raises a script error if `text` exceeds `max_input_bytes`, if the
TOML is malformed (e.g. a syntax error or a duplicate key), if nesting is deeper
than `max_depth`, or if the document has more than `max_nodes` total nodes. A
panic inside the underlying codec is recovered into an error, never a host
crash.

**Example:**

```python
load("toml", "decode")

doc = decode("""
name = "Ada"
langs = ["go", "python"]

[nested]
k = "v"
""")
doc["name"]            # => "Ada"
doc["langs"][0]        # => "go"
doc["nested"]["k"]     # => "v"

# Bytes input decodes identically to a string.
decode(b"a = 1")["a"]  # => 1
```

### `encode(value) -> str`

Serializes a `dict` to TOML text. TOML documents are tables, so the top-level
value must be a `dict`.

**Parameters:**

- `value` (dict): The table to serialize. Must be a `dict` — a list, scalar, or
  any other type is rejected.

**Returns:** A `str` holding the TOML representation of the table. Table keys are
emitted in sorted (deterministic) order.

**Errors:** Raises a script error if `value` is not a `dict` (TOML documents are
tables), if a contained value cannot be marshalled to Go, or if the underlying
encoder fails. A panic inside the encoder is recovered into an error.

**Example:**

```python
load("toml", "encode")

encode({"a": 1, "b": [1, 2]})   # => "a = 1\nb = [1, 2]\n"
```

## Decoded value types

`decode` maps each TOML type to a Starlark value:

| TOML type | Starlark type | Notes |
|-----------|---------------|-------|
| Boolean | bool | |
| Integer | int | |
| Float | float | |
| String | string | |
| Datetime (offset) | string | Formatted RFC 3339 |
| Local date / time / datetime | string | Surfaced via the value's `String()` |
| Array | list | |
| Array of tables (`[[items]]`) | list of dicts | |
| Table / inline table / dotted keys | dict | Keys in sorted order |

Nested tables (`[a.b]`), dotted keys (`a.b = 1`), inline tables (`{ x = 1 }`),
and arrays of tables (`[[items]]`) are all supported.

## Hardening

Decoding is the untrusted surface (a script can hand it arbitrary text), so it is
hardened:

- **Date/time are strings.** TOML datetimes/dates/times are surfaced as strings
  (RFC 3339 for full timestamps), never surprise opaque values.
- **Bounded decode (capwalk).** Rejects input over `max_input_bytes`, nesting
  deeper than `max_depth`, or more than `max_nodes` total nodes.
- **No host panics.** `decode` and `encode` recover panics into errors.
- **Deterministic order.** Table keys are emitted in sorted order.

The caps default to generous values (`max_depth=64`, `max_nodes=100000`,
`max_input_bytes=5 MiB`) chosen so ordinary documents are unaffected; tune them
through the [configuration accessors](#configuration) below.

## Notes

TOML has **no anchors, aliases, or merge keys** — unlike YAML, it has no
cross-reference mechanism; every value is written explicitly (this is by design
in the TOML spec). To reuse configuration across sections, decode the document
and compose the resulting dicts in your Starlark script.

## Configuration

Each module configuration option is exposed to scripts as a pair of generated
accessor builtins (loaded from the `toml` module alongside the functions above):

- **`get_<key>()`** — returns the current value of the option.
- **`set_<key>(value)`** — sets the option (returns `None`).

An option's value resolves in priority order: an explicit `set_<key>` value, the
environment variable, then the default. These options bound the `decode` path
(`encode` has no caps).

None of the `toml` options are secret, so every option exposes **both**
`get_<key>` and `set_<key>`. (A secret option would expose only its `set_<key>`
accessor — never a getter — but this module has none.)

| Option | Getter | Setter | Type | Env var | Default | Description |
|--------|--------|--------|------|---------|---------|-------------|
| `max_depth` | `get_max_depth` | `set_max_depth` | int | `TOML_MAX_DEPTH` | `64` | Maximum nesting depth when decoding |
| `max_nodes` | `get_max_nodes` | `set_max_nodes` | int | `TOML_MAX_NODES` | `100000` | Maximum total nodes when decoding |
| `max_input_bytes` | `get_max_input_bytes` | `set_max_input_bytes` | int | `TOML_MAX_INPUT_BYTES` | `5242880` | Maximum input size in bytes when decoding (5 MiB) |

**Example:**

```python
load(
    "toml",
    "decode",
    # getters
    "get_max_depth", "get_max_nodes", "get_max_input_bytes",
    # setters
    "set_max_depth", "set_max_nodes", "set_max_input_bytes",
)

set_max_depth(16)
print(get_max_depth())  # 16

decode("a = 1")  # decoded under the tightened cap
```
