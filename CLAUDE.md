# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

`starpkg/toml` is an **L4 domain module** of the Star\* ecosystem: it exposes TOML decoding and encoding to Starlark scripts. A script imports the module, calls `decode(text)` to turn a TOML document into Starlark values (a dict), or `encode(value)` to serialize a dict back into TOML text.

starpkg modules give scripts *support for necessary local operations* plus *simple abstractions over common online services*, for ease of use. `toml` is squarely on the **local** side: it is a pure in-process codec — **no network, no filesystem, no host services**. Input is a string, output is a string or Starlark values; nothing leaves the process. (Contrast with siblings like `sqlite`/`s3`/`email`, which front an online service.)

It is pure Go and wraps [`github.com/BurntSushi/toml`](https://github.com/BurntSushi/toml) as the underlying codec. Layer position: depends downward on `starpkg/base` (the module/config system), `1set/starlet` (the Machine + `dataconv` for Starlark⇄Go conversion), and transitively `1set/starlight` + `go.starlark.net`. Nothing in the ecosystem depends on it.

## Dev commands

Pure Go library with a Makefile. From this repo:

```bash
make test                                  # -race -cover, the working bar
make ci                                    # -race -cover profile + bench compile (what CI runs)
make bench                                 # benchmarks only
go test ./... -run TestDecode              # a single test
gofmt -l . && go vet ./...                 # must be clean before commit
go run github.com/1set/meta/doccov@master .   # doc-coverage gate: every script-facing builtin must be a backtick word in README
```

**Verify on the go floor in Docker** — this repo's floor is **go 1.19** (its `go.mod`), and the pinned `go.starlark.net` baseline uses `maphash.String` (needs ≥1.19), so behavior on the floor must be checked in a container, not just on the newer local toolchain:

```bash
docker run --rm -v "$PWD":/src -v "$HOME/go/pkg/mod":/go/pkg/mod -w /src golang:1.19 go test -race -count=1 ./...
```

There are no external integration fixtures: this module is a self-contained codec, so `toml_test.go` is the whole suite (script-driven through the in-process `run` helper — a `starlet` Machine with the module lazy-loaded). No `../test/toml` directory in the private `starpkg/test` repo is needed or used.

## Architecture (the part that spans files)

The module is small and single-file by design — a **bounded, panic-safe codec bridge**. `toml.go` is the whole module:

- **Module entry.** `Module` holds a `base.ConfigurableModule` plus its `ext` accessor; `NewModule()` constructs it, registering three config options (`max_depth`, `max_nodes`, `max_input_bytes`) with `TOML_`-prefixed env vars. `LoadModule()` exposes two builtins under the `toml` name: **`decode`** and **`encode`**.
- **`decode(text)`** — accepts a string or bytes (`types.StringOrBytes`), enforces `max_input_bytes` up front, parses via `unmarshal` (a `recover`-guarded `gotoml.Decode` into `map[string]interface{}`), then walks the Go tree with `toStarlark`, threading depth + a node counter to enforce `max_depth`/`max_nodes`. Returns a Starlark `dict`.
- **`encode(value)`** — requires the top-level value to be a `*starlark.Dict` (TOML documents are tables; anything else is a script-level error), converts to Go via `starlet/dataconv.Unmarshal`, then `marshal` (a `recover`-guarded `gotoml.NewEncoder`). Returns a Starlark `string`.
- **`toStarlark`** — the Go→Starlark dispatch: a type switch over the kinds BurntSushi produces (`bool`/`int`/`int64`/`uint64`/`float64`/`string`/`time.Time`/`[]map[string]interface{}` for arrays-of-tables/`[]interface{}`/`map[string]interface{}`). Map keys are emitted in sorted order. The `default` arm tames any `fmt.Stringer` (TOML's local date/time types) to a string and errors on anything genuinely unsupported.
- **`unmarshal` / `marshal`** — the only third-party SDK wrap points; both are deferred-`recover` shells so a codec panic becomes a Starlark error, never a host crash.

Data flow: `script string → max_input_bytes check → gotoml.Decode → map → toStarlark(capwalk) → dict` for decode; `dict → dataconv.Unmarshal → gotoml.Encode → string` for encode.

## Invariants / hardening (preserve when editing)

The decode path is the untrusted surface (a script can hand it arbitrary text), so it is hardened. Keep these properties when touching `toml.go`:

1. **No host panics from script input.** Both `unmarshal` and `marshal` wrap the BurntSushi call in a deferred `recover()` that converts a panic into an error. Don't remove the recover; any new codec call must be guarded the same way.
2. **Bounded decode (capwalk).** `decode` rejects input over `max_input_bytes` before parsing, and `toStarlark` enforces `max_depth` (nesting) and `max_nodes` (total node count) while walking — a single counter threaded by pointer. New tree-walking paths must route through `toStarlark`, not allocate unbounded.
3. **Deterministic order.** Go map iteration is randomized; `toStarlark` sorts map keys (`sort.Strings`) before materializing the dict so script-visible order is stable.
4. **Date/time are strings, never opaque.** `time.Time` is formatted RFC 3339; any other `fmt.Stringer` (TOML local date/time types) falls through to its `String()`. A decoded document never surfaces a surprise opaque host value.
5. **`encode` requires a table.** TOML documents are tables, so the top-level value must be a dict; a list/scalar is rejected with a clear error rather than producing invalid TOML.
6. **Backward compatibility (iron rule).** The caps default to generous values (`max_depth=64`, `max_nodes=100000`, `max_input_bytes=5 MiB`) chosen so ordinary documents are unaffected; old scripts must keep decoding/encoding identically. Any new lever must default to the historical behavior.

## Test organization

Group by functional goal — **do not add one `*_test.go` per fix.** `toml_test.go` is the home, opened with a commented section list: decode/encode round-trip, date/time taming, encode-requires-a-table, capwalk limits, the `toStarlark` value branches (array-of-tables, uint64, Stringer, unsupported), and a comprehensive multi-shape document. Add a new test as a **section here**, not a new file. Tests are table/script-driven through the `run` helper (a `starlet` Machine with the module lazy-loaded); no third-party test framework. The capwalk/branch tests call `toStarlark` directly to cover arms (like `uint64`) unreachable from TOML text.

## Documentation

Three layers must stay in sync (enforced by the doc standard, `plan/starpkg文档标准（DOC-STD）`):

- **`README.md`** — every script-facing builtin (`decode`, `encode`) documented as a backtick whole-word, with signature/args/return; host levers (`max_depth`, `max_nodes`, `max_input_bytes` and their env vars) under *Configuration* / *Hardening*. Names and signatures must match the code.
- **GoDoc** — package comment + a doc comment on every exported symbol (`ModuleName`, `Module`, `NewModule`, `LoadModule`), first word = symbol name (gated by `revive`'s `exported` rule in CI).
- **The doccov gate** — `1set/meta/doccov` is wired into CI (`doc-coverage: true` in `.github/workflows/build.yml`); it fails the build if a registered builtin is missing from the README as a backtick word. The gate only catches *omissions* — accuracy (case, args, behavior) is a PR-review responsibility.

## Release discipline

- **Floor = go 1.19** (this repo's `go.mod`); the floor only rises in this repo's own pin PR.
- **CI matrix** = `[1.19.x, 1.25.x]` via the centralized reusable workflow in `1set/meta` (`go-ci.yml`, pinned to a commit SHA). Bumping the pin happens when meta's workflow changes.
- **Pin upgrade is a separate, last PR.** Upgrading the `go.starlark.net` pin / 1set deps / go floor is one isolated PR done *after* feature/bug work — don't fold it into a docs or feature change, and don't tag a release before it merges.
- **Bumping the version, the go floor, or tagging are user-confirmed actions** — never tag autonomously; draft title + notes and get explicit approval first; default to patch bumps; a published tag is immutable in the Go module proxy.
