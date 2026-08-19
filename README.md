# ApexJSON

ApexJSON is an unsafe-first, pure-Go JSON engine for workloads where parsing cost, allocation pressure, and memory layout matter.

The current engine lives in the v2 module:

```text
github.com/xDarkicex/apexJSON/v2
```

> **v1 is 100% deprecated.** The root `github.com/xDarkicex/apexJSON`
> package is retained only as historical code. It receives no new features or
> performance work. Use `github.com/xDarkicex/apexJSON/v2` for the native
> engine, or `github.com/xDarkicex/apexJSON/v2/compat` for an
> `encoding/json`-shaped migration surface.

For an `encoding/json`-shaped migration surface, use the compatibility
facade:

```go
import json "github.com/xDarkicex/apexJSON/v2/compat"
```

The facade provides the familiar `Marshal`, `MarshalIndent`, `Unmarshal`,
`NewEncoder`, and `NewDecoder(io.Reader)` APIs. It also exposes `Token`,
`More`, `Buffered`, `UseNumber`, `DisallowUnknownFields`, `SetIndent`, and
`SetEscapeHTML`. Its normal `Decode` path uses ApexJSON's direct decoder, and
its token path builds tokens from ApexJSON's off-heap tape. The adapter reads
`io.Reader` input incrementally, compacts consumed bytes, and preserves
stdlib-shaped errors when a native fast path rejects a value. Strict options
that are not represented in the native decoder use a narrow fallback for that
operation. Use the native `v2` package when you want the reusable off-heap
decoder, lazy values, iterators, mutation, and pointer-oriented
zero-allocation paths.

Compatibility is an on-ramp, not the native hot-path API. It keeps the familiar
`encoding/json` surface while routing ordinary work through ApexJSON. Native
features such as reusable off-heap decoders, lazy values, iterators, and
copy-on-write mutation are available from the main `v2` package.

### Compatibility behavior

The compatibility `Decoder` is a real ApexJSON-backed stream adapter:

- `NewDecoder(r)` reads incrementally from `io.Reader` instead of calling
  `io.ReadAll`.
- It keeps only the current buffered values, compacts consumed prefixes, and
  maintains absolute positions through `InputOffset`.
- Normal typed `Decode` uses ApexJSON's direct decoder. If that fast path
  rejects a value, the operation is retried through `encoding/json` so callers
  receive the standard error type and message.
- `Token` parses the current value through the off-heap tape and copies string
  and key tokens before the stream buffer is compacted.
- `DisallowUnknownFields` and interface-number decoding use the standard
  decoder for that individual operation where native flags do not yet exist.

The compatibility surface may allocate for stream ownership, token
materialization, and narrow fallbacks. Use the native `v2` APIs for reusable
off-heap parsing and allocation-sensitive hot paths.

It is built around three ideas:

- direct typed decoding when the destination type is known;
- a reusable off-heap, structure-of-arrays tape for queries and iteration;
- unsafe pointer and offset operations on the hot paths, with ordinary Go fallbacks for complex values.

The baseline implementation uses portable SWAR scanning. It does not require SIMD instructions, generated code, a JIT, or architecture-specific assembly.

## What makes ApexJSON different

### Direct typed decoding

`Unmarshal` uses a fused scanner and writes supported typed values directly into the destination. It avoids building an intermediate AST when the destination can be handled by the direct decoder.

```go
package main

import (
	"fmt"

	apexjson "github.com/xDarkicex/apexJSON/v2"
)

type Record struct {
	ID     int     `json:"id"`
	Name   string  `json:"name"`
	Active bool    `json:"active"`
	Score  float64 `json:"score"`
}

func main() {
	var records []Record
	if err := apexjson.Unmarshal(input, &records); err != nil {
		panic(err)
	}
	fmt.Println(records[0].Name)
}
```

The direct path supports scalar fields, strings, structs, pointers, arrays, and slices. Unsupported or custom-marshaler-heavy destinations fall back to the complete tape-backed decoder so correctness behavior is preserved.

### Native zero-copy raw JSON

The native package provides `apexjson.RawMessage` for trusted embedded JSON:

```go
var payload apexjson.RawMessage
if err := apexjson.Unmarshal(src, &payload); err != nil {
	panic(err)
}

dst, err := apexjson.AppendMarshalPtr(dst[:0], &payload)
```

`RawMessage` borrows the validated input span on unmarshal and appends that
span directly on marshal. With a reusable destination capacity, these paths
are zero-allocation. Keep the source buffer alive and immutable while the raw
message is in use; call `payload.Clone()` when an owned copy is required.

This is intentionally different from plain `[]byte`, which retains JSON's
binary-data convention and encodes as a base64 string. The compatibility
package keeps `encoding/json.RawMessage` semantics for migration callers.

### Off-heap reusable tape

`Decoder` parses into an arena-backed structure-of-arrays tape. The logical node metadata is stored in dense columns:

```text
meta   uint16  // kind + flags
starts uint32
ends   uint32
skips  int32
```

This is a 14-byte logical node footprint without a linked node object or per-node Go pointer. Skip offsets allow subtree navigation without recursively walking every child. The arena is reused across parses, keeping parser metadata out of the Go heap and avoiding steady-state GC work.

```go
dec, err := apexjson.NewDecoder()
if err != nil {
	panic(err)
}
defer dec.Free()

if err := dec.Parse(data); err != nil {
	panic(err)
}

userID := dec.Get("user", "id").Int()
name := dec.Get("user", "name").Str()
fmt.Println(userID, name)
```

Use `NewDecoderSize` when the default arena or node capacity is not appropriate for the documents being processed:

```go
dec, err := apexjson.NewDecoderSize(
	64<<20, // off-heap arena bytes
	1<<20,  // maximum tape nodes
)
```

`Decoder` is reusable but not safe for concurrent use. Use one decoder per goroutine.

### Zero-allocation navigation

`Decoder.Get` and `Decoder.GetPath` return lazy `Value` handles into the tape and source buffer. They do not materialize maps or interface trees.

For repeated traversal, use the iterators:

```go
obj := dec.Root()
it := obj.ObjectIter()
for it.Next() {
	fmt.Println(it.Key(), it.Value().Type())
}

items := dec.Get("items")
ait := items.ArrayIter()
for ait.Next() {
	item := ait.Value()
	_ = item.Bytes()
}
```

The iterator methods are designed for zero-allocation traversal. `Value.Map()` and `Value.Array()` intentionally materialize Go collections and therefore allocate; use `ObjectIter` and `ArrayIter` when allocation-free traversal matters.

The package-level helpers are convenient for one-shot access:

```go
value := apexjson.Get(data, "request", "user", "id")
if value.Exists() {
	fmt.Println(value.Int())
}
```

For hot repeated queries, prefer one reusable `Decoder` rather than reparsing through the one-shot helper.

## Unsafe mutation and copy-on-write

Parsed values can be updated without converting the document into a tree of Go maps. The first variable-size mutation copy-on-write clones the borrowed source into the decoder's off-heap arena, then reparses and republishes the affected document state.

```go
userName := dec.Get("user", "name")
if err := userName.SetString("Ada"); err != nil {
	panic(err)
}

count := dec.Get("user", "login_count")
if err := count.SetInt(42); err != nil {
	panic(err)
}
```

For hand-written byte kernels, `MutateUnsafe` exposes a temporary pointer to the value's JSON span:

```go
err := count.MutateUnsafe(func(ptr unsafe.Pointer, n int) {
	// The callback must preserve the span length and leave valid JSON.
	bytes := unsafe.Slice((*byte)(ptr), n)
	_ = bytes
})
```

`MutateBytes` provides the same temporary alias as a byte slice. The alias is valid only during the callback. Invalid candidates are rejected without corrupting the live document, and old `Value` handles are invalidated after reparses or variable-size mutations.

### Native path mutation

The reusable decoder also supports path-aware mutation without materializing a
Go object tree:

```go
path := []string{"profile", "login_count"}
changed, err := dec.SetPathRaw(path, []byte(`42`), true)
if err != nil {
	panic(err)
}
_ = changed

changed, err = dec.InsertPathRaw(
		[]string{"items", "1"},
		[]byte(`{"id":3}`),
		false, // insert before array element 1
)
if err != nil {
	panic(err)
}
```

`SetPathRaw` replaces an existing value and, when requested, creates only a
missing final object key. Missing intermediate paths are valid no-ops. Array
indexes support zero-based, negative, and out-of-range PostgreSQL-compatible
behavior: positive out-of-range creation appends and negative out-of-range
creation prepends. `InsertPathRaw` inserts before or after an array element;
existing object keys are valid no-ops. An empty `SetPathRaw` path replaces the
root; an empty `InsertPathRaw` path is a valid no-op.

Both methods require `replacement` to contain exactly one valid JSON value.
They build a candidate in off-heap storage, validate it through the native
tape parser, and publish it only after parsing succeeds. The caller's input
and replacement slices are never modified. Failed mutations leave the live
document and existing `Value` handles unchanged; successful path mutations
reparse the document and make previously acquired handles stale. Reacquire
values from `Root()` or `GetPath()` after a successful mutation.

`Value.RawJSON()` returns the complete borrowed JSON span, including quotes for
strings and delimiters for objects and arrays:

```go
raw := dec.Get("payload").RawJSON()
```

`RawJSON` does not allocate or materialize a tree. Its slice aliases decoder
source storage and is invalid after `Parse`, `Reset`, `Free`, or any mutation
that reparses the document. Keep the input buffer alive for borrowed values.

All decoder mutation APIs follow the same concurrency rule as parsing: a
single `Decoder` must not be used concurrently; use one decoder per goroutine.

## Marshal paths

ApexJSON caches type metadata, JSON key prefixes, field offsets, and operation classifications. Struct fields are read through unsafe offsets rather than repeated `reflect.Value.Field` operations.

```go
data, err := apexjson.Marshal(record)

dst := make([]byte, 0, 1024)
dst, err = apexjson.AppendMarshal(dst, record)

// Concrete pointer path: avoids interface-based value dispatch.
dst, err = apexjson.AppendMarshalPtr(dst, &record)
```

For plain slices of primitive/string structs, the encoder uses a specialized flat loop over the cached stride and offsets. It bypasses the generic per-field operation dispatcher while retaining the generic path for nested structs, pointers, maps, slices, interfaces, `omitempty`, and custom marshalers.

`AppendMarshalPtr` can run with zero allocations when `dst` already has enough capacity. `Marshal` returns an owned byte slice and may allocate the result buffer; zero allocation is not possible when a new result must be returned to the caller.

## Memory and ownership

- AST/tape columns and reusable parser scratch memory are backed by `github.com/xDarkicex/memory` arenas.
- The input `[]byte` is borrowed by parsed values and should remain alive and unchanged while those values are in use.
- Clean strings on the direct typed path may alias the input bytes to avoid copying. Retain the input buffer for as long as those strings are needed.
- `Decoder.Free` releases the arena and invalidates values obtained from that decoder.
- A `Value` retained across `Parse` or a variable-size mutation becomes stale and reports no live data.
- Off-heap storage removes parser metadata from normal Go heap accounting; it does not make returned byte slices, Go destinations, or explicitly materialized maps allocation-free.

## Performance model

ApexJSON is optimized for different costs through the API the caller already needs:

| Workload | Preferred API | Internal strategy |
| --- | --- | --- |
| Typed structs and record batches | `Unmarshal` | Direct scanner and unsafe destination writes |
| Reusable document access | `Decoder.Parse` | Fused SWAR/scalar parser and off-heap SoA tape |
| Repeated path queries | `Decoder.Get` / `GetPath` | Lazy tape navigation and subtree skips |
| Allocation-free traversal | `ObjectIter` / `ArrayIter` | Cursor movement over skip offsets |
| In-place document edits | `Value.Set*` / `Mutate*` | Off-heap copy-on-write source and reparsed tape |
| Typed serialization | `Marshal` / `AppendMarshalPtr` | Cached unsafe field plans and flat primitive kernels |

There are no public parser modes to configure. The entry point expresses the operation, while the implementation selects the appropriate internal path.

## Benchmarking

Benchmarks are workload- and machine-dependent, especially on systems with shared CPU contention. The repository reports throughput, latency, bytes allocated, allocation count, and parser-stage timings rather than treating one headline number as universal.

Run the v2 tests and benchmarks from the v2 module:

```bash
cd v2
go test ./...
go test -run '^$' -bench . -benchmem -count=5
```

The comparison benchmarks include large record arrays, typed decode, marshal, append marshal, parser stages, dynamic decode, and query paths. Percentile latency collection is opt-in:

```bash
APEXJSON_RUN_LATENCY=1 \
APEXJSON_LATENCY_SAMPLES=3000 \
go test -run TestCompareTypedLatency1MiB -v
```

When comparing libraries, report the full tuple: GB/s, ns/op, p50, p99, p99.9, B/op, allocs/op, and retained memory. Run competing implementations under the same workload and hardware conditions.

## Current scope

ApexJSON is actively optimized and its public surface is still evolving. It currently prioritizes:

- portable SWAR before architecture-specific SIMD;
- unsafe pointer arithmetic and cached metadata on hot paths;
- off-heap arena reuse and minimal GC interaction;
- structure-of-arrays metadata for navigation and skipping;
- correctness-preserving fallbacks for complex Go types;
- zero allocation where the API and ownership model allow it.

It is not yet a generated-code or SIMD engine. Those may become future implementation details, but the portable engine is intended to remain useful on its own.

## License

MIT. See [LICENSE](LICENSE).
