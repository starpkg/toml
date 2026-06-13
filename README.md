# 📑 `toml` — TOML for Starlark

[![Go Reference](https://pkg.go.dev/badge/github.com/starpkg/toml.svg)](https://pkg.go.dev/github.com/starpkg/toml)

Decode and encode [TOML](https://toml.io/) from Starlark, built on
[BurntSushi/toml](https://github.com/BurntSushi/toml).

Decoding is **hardened**: input size, nesting depth, and total node count are
bounded; panics become errors; and TOML's native date/time values are surfaced
as strings.

## Installation

```bash
go get github.com/starpkg/toml
```

## Functions

| Function | Signature | Description |
|----------|-----------|-------------|
| `decode` | `decode(text) -> dict` | Parse a TOML document into Starlark values. |
| `encode` | `encode(value) -> str` | Serialize a dict to TOML text (the top-level value must be a dict — TOML documents are tables). |

## Usage

```python
load("toml", "decode", "encode")

doc = decode("""
name = "Ada"
langs = ["go", "python"]

[nested]
k = "v"
""")
doc["name"]            # => "Ada"
doc["langs"][0]        # => "go"
doc["nested"]["k"]     # => "v"

encode({"a": 1, "b": [1, 2]})   # => "a = 1\nb = [1, 2]\n"
```

## Hardening

- **Date/time are strings.** TOML datetimes/dates/times are surfaced as strings
  (RFC 3339 for full timestamps), never surprise opaque values.
- **Bounded decode (capwalk).** Rejects input over `max_input_bytes`, nesting
  deeper than `max_depth`, or more than `max_nodes` total nodes.
- **No host panics.** Decode and encode recover panics into errors.
- **Deterministic order.** Table keys are emitted in sorted order.

## Configuration

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `max_depth` | `int` | `64` | Maximum nesting depth when decoding |
| `max_nodes` | `int` | `100000` | Maximum total nodes when decoding |
| `max_input_bytes` | `int` | `5242880` | Maximum input size in bytes (5 MiB) |

Settable via `TOML_MAX_DEPTH` / `TOML_MAX_NODES` / `TOML_MAX_INPUT_BYTES`.
