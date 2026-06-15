# 📑 `toml` — TOML for Starlark

[![Go Reference](https://pkg.go.dev/badge/github.com/starpkg/toml.svg)](https://pkg.go.dev/github.com/starpkg/toml)
[![codecov](https://codecov.io/gh/starpkg/toml/graph/badge.svg)](https://codecov.io/gh/starpkg/toml)
![binary footprint](https://img.shields.io/badge/binary_footprint-%2B0.4_MB-blue)

Decode and encode [TOML](https://toml.io/) from Starlark, built on
[BurntSushi/toml](https://github.com/BurntSushi/toml).

Decoding is **hardened**: input size, nesting depth, and total node count are
bounded; panics become errors; and TOML's native date/time values are surfaced
as strings.

## Overview

starpkg modules give Starlark scripts *support for necessary local operations*
plus *simple abstractions over common online services*, for ease of use. `toml`
is a **local capability**: it is a pure in-process codec — no network, no
filesystem, no host services — turning TOML text into Starlark values and back.

- **`decode(text)`** — parse a TOML document (string or bytes) into Starlark
  values (a `dict`).
- **`encode(value)`** — serialize a `dict` to TOML text.
- **Hardened decode** — bounded input size, nesting depth, and node count;
  panics recovered into errors; date/time surfaced as strings; deterministic
  key order.

For the complete per-builtin reference — signatures, parameters, returns,
errors, examples — and the configuration accessors, see
**[docs/API.md](docs/API.md)**.

## Installation

```bash
go get github.com/starpkg/toml
```

## Quickstart

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

## Starlark API at a glance

Top-level builtins (`load("toml", …)`):

- `decode(text)` — parse a TOML document (string or bytes) into a `dict`.
- `encode(value)` — serialize a `dict` to TOML text (the top-level value must be
  a `dict`, since TOML documents are tables).

See **[docs/API.md](docs/API.md)** for the full signatures, decoded value types,
return values, errors, and examples of both builtins.

## Configuration

The module's `decode` caps (`max_depth`, `max_nodes`, `max_input_bytes`) are
configured via environment variables (`TOML_*`) or per-option `get_<key>` /
`set_<key>` accessor builtins. See the
[Configuration section of docs/API.md](docs/API.md#configuration) for the full
option table, defaults, and accessors.

## License

MIT
