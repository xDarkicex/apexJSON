# ApexJSON v2 — Native Path-Aware JSON Mutation

## Objective

Extend `github.com/xDarkicex/apexJSON/v2` with native path-aware mutation while
preserving the existing off-heap, copy-on-write design.

ApexJSON already supports mutation of existing values through `Value.SetRaw`,
`SetString`, `SetInt`, `SetUint`, `SetFloat`, `SetBool`, `SetNull`,
`MutateBytes`, and `MutateUnsafe`. It also provides reusable decoder arenas,
off-heap source ownership, atomic candidate validation, and stale-value
invalidation.

The missing capability is mutation by nested path, including replacing an
existing value, creating a missing final object key, and inserting into arrays.

Do not introduce a second JSON engine. Do not use `encoding/json`, Go JSON
trees, `map[string]interface{}`, `[]interface{}`, `Value.Map()`, or
`Value.Array()` for the implementation.

## Read first

```text
v2/mutation.go
v2/decoder.go
v2/extract.go
v2/ast.go
v2/fused_parser.go
v2/iter.go
v2/mutation_test.go
v2/decoder_test.go
README.md
```

Build on the existing `replaceSpan`, `mutateSpan`,
`cloneSourceWithCapacity`, `commitCandidate`, `parseSource`, document
generation, and owned-source arena mechanisms.

Do not expose `parsedDoc`, `slab`, `memory.Arena`, tape indexes, or unsafe
internal offsets.

## Public API

Add:

```go
func (d *Decoder) SetPathRaw(path []string, replacement []byte, createMissing bool) (changed bool, err error)
func (d *Decoder) InsertPathRaw(path []string, replacement []byte, insertAfter bool) (changed bool, err error)
func (v Value) RawJSON() []byte
```

The decoder is the receiver because path mutation may reparse the document and
invalidate previous `Value` handles. Callers must reacquire values through
`Root()` or `GetPath()` after mutation.

`changed` must be true only when the document changed. Valid no-ops return
`false, nil`. Invalid input returns `false, err`.

`RawJSON` returns the complete borrowed JSON span:

- strings include quotes;
- numbers include the complete literal;
- objects include `{...}`;
- arrays include `[...]`;
- booleans and null include their complete literals.

It must not materialize maps or arrays. Document that the returned slice is
invalid after `Parse`, `Reset`, `Free`, or mutation.

## SetPathRaw semantics

Implement PostgreSQL-compatible `jsonb_set` path behavior.

Existing target:

```json
{"profile":{"name":"Alice"}}
```

```go
d.SetPathRaw([]string{"profile", "name"}, []byte(`"Bob"`), false)
```

must produce:

```json
{"profile":{"name":"Bob"}}
```

Support replacement by string, number, boolean, null, object, or array.

When the final object key is missing and `createMissing == true`, create it:

```json
{"profile":{}}
```

with path `[]string{"profile", "age"}` and replacement `42` becomes:

```json
{"profile":{"age":42}}
```

When creation is disabled, return a valid no-op. Preserve PostgreSQL behavior
for missing intermediate paths; do not invent nested-object creation rules.

Support array path components, including zero, middle, final, negative, and
out-of-range indexes, append/prepend behavior, empty arrays, and nested arrays.

Define and test behavior for an empty path. Invalid replacement bytes must be
rejected without changing the document.

## InsertPathRaw semantics

Implement PostgreSQL-compatible `jsonb_insert` behavior.

Support insertion before or after array indexes, negative and out-of-range
indexes, nested and empty arrays, object-key behavior, existing-key no-op
behavior, and invalid paths.

For example:

```go
d.InsertPathRaw([]string{"items", "1"}, []byte(`{"id":3}`), false)
```

inserts before array element `1`; `insertAfter == true` inserts after it.

Replacement bytes must contain exactly one valid JSON value and must be
validated before live state changes.

## Implementation requirements

Use this flow:

```text
resolve path against tape spans
validate replacement JSON
locate target, parent, and insertion span
build candidate directly in off-heap memory
parse candidate
commit only after successful parsing
invalidate old Values
release previous owned source after commit
```

For existing values, reuse raw-span replacement through the current mutation
machinery.

For missing object keys and array insertion, preserve the original source
bytes and construct a new off-heap candidate with the required key, value,
commas, and separators. Do not create an object or array tree.

`MutateBytes` alone cannot implement insertion because its callback preserves
span length. Use the existing variable-size candidate and commit path.

If candidate parsing fails, restore the original document, free the candidate
arena, and return the error. The decoder must remain usable after failure.

The caller's original input slice must never be modified.

## Tests

Add tests under `v2/` for nested replacement, root replacement, scalar,
escaped string, object, array, boolean, number, null, shorter and longer
replacement; missing final keys with creation enabled and disabled; missing
intermediate paths; empty objects and arrays; escaped keys; empty paths; array
indexes zero, middle, final, negative, and out-of-range; append/prepend;
nested arrays; insert before/after; existing object keys; invalid indexes;
invalid paths; malformed replacements; multiple JSON values; failed candidate
parsing; atomic rollback; caller-input ownership; stale values; `RawJSON`
lifetime; and mutation followed by `Parse`, `Reset`, and `Free`.

Add race coverage using one decoder per goroutine. Keep the documented rule
that a single decoder is not safe for concurrent use.

## Benchmarks

Add:

```text
BenchmarkSetPathRawExisting
BenchmarkSetPathRawCreateMissing
BenchmarkInsertPathRawArray
BenchmarkInsertPathRawNested
BenchmarkValueSetRaw
BenchmarkRawJSON
```

Report `ns/op`, `B/op`, and `allocs/op`. Measure Go heap allocations separately
from off-heap arena use; do not claim zero allocation without evidence.

Run:

```bash
cd v2
go test -run '^$' -bench 'Mutation|RawJSON' -benchmem -count=5
```

## Documentation

Update `README.md` with examples for `SetPathRaw`, `InsertPathRaw`, and
`RawJSON`. Document copy-on-write behavior, borrowed source ownership,
stale-value invalidation, decoder concurrency, variable-size mutation,
atomic failure guarantees, and the materialization costs of `Value.Map()` and
`Value.Array()`.

## Acceptance criteria

Complete only when:

1. Existing tests pass.
2. New path-mutation tests pass.
3. Atomicity and ownership tests pass.
4. Race tests pass.
5. Reset and Free tests pass after mutation.
6. Benchmarks report measured allocation behavior.
7. No Go JSON tree is created.
8. No standard-library JSON dependency is added.
9. No internal tape or arena type is exposed.
10. README is updated.
11. `cd v2 && go test ./...` passes.
