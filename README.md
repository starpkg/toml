# 📑 `toml` — TOML for Starlark

[![Go Reference](https://pkg.go.dev/badge/github.com/starpkg/toml.svg)](https://pkg.go.dev/github.com/starpkg/toml)
[![codecov](https://codecov.io/gh/starpkg/toml/graph/badge.svg)](https://codecov.io/gh/starpkg/toml)
![binary footprint](https://img.shields.io/badge/binary_footprint-%2B0.2_MB-blue)

Decode and encode [TOML](https://toml.io/) from Starlark, built on
[BurntSushi/toml](https://github.com/BurntSushi/toml).

Decoding is **hardened**: input size, nesting depth, and total node count are
bounded; panics become errors; and TOML's native date/time values are surfaced
as strings.

> **Where this sits.** starpkg modules give Starlark scripts *support for
> necessary local operations* plus *simple abstractions over common online
> services*, for ease of use. `toml` is a **local capability**: it is a pure
> in-process codec — no network, no filesystem, no host services — turning TOML
> text into Starlark values and back.

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

## Notes

TOML has **no anchors, aliases, or merge keys** — unlike YAML, it has no
cross-reference mechanism; every value is written explicitly (this is by design
in the TOML spec). To reuse configuration across sections, decode the document
and compose the resulting dicts in your Starlark script. Nested tables
(`[a.b]`), dotted keys (`a.b = 1`), inline tables (`{ x = 1 }`), and arrays of
tables (`[[items]]`) are all supported.

## Configuration

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `max_depth` | `int` | `64` | Maximum nesting depth when decoding |
| `max_nodes` | `int` | `100000` | Maximum total nodes when decoding |
| `max_input_bytes` | `int` | `5242880` | Maximum input size in bytes (5 MiB) |

Settable via `TOML_MAX_DEPTH` / `TOML_MAX_NODES` / `TOML_MAX_INPUT_BYTES`.
