package apexjson

import (
	"github.com/xDarkicex/memory"
)

// Decoder parses JSON documents and exposes their AST.
//
// A Decoder owns an off-heap memory.Arena for the AST slab. Parsing is
// zero-alloc in the steady state — the slab is sliced from the arena
// once and reused across calls via Reset.
//
// The arena is 8-byte aligned, suitable for SIMD scans on the tape columns.
// JSON node data lives in two places: the columnar tape (off-heap, arena) and
// the source bytes (caller-owned, passed in).
//
// Construction:
//
//	dec, err := apexjson.NewDecoder()
//	defer dec.Free()
//
// Repeated parses:
//
//	if err := dec.Parse(data); err != nil { ... }
//	v := dec.Get("users", "0", "name")
//	fmt.Println(v.Str())
//
// Decoders are NOT safe for concurrent use (the arena is
// single-producer). Use one Decoder per goroutine.
type Decoder struct {
	arena   *memory.Arena
	doc     parsedDoc
	src     []byte
	root    int32
	err     error
	nodeCap int // max nodes per parse (arena-allocated region)
}

// NewDecoder returns a Decoder with a 256KB off-heap arena backing up
// to ~10000 AST nodes per parse. Adjust with NewDecoderSize for larger
// or smaller workloads.
func NewDecoder() (*Decoder, error) {
	return NewDecoderSize(decoderDefaultArenaSize, decoderDefaultNodeCap)
}

// NewDecoderSize returns a Decoder with explicit arena and node
// capacities. arenaSize is the off-heap memory pool size in bytes;
// nodeCap is the maximum number of AST nodes per parse. Panics
// on overflow during parse.
//
// align is the byte alignment of allocations within the arena.
// 64 is the safest choice for SIMD (AVX-512) compatibility; 32
// works for AVX/AVX2; 16 for SSE2. The arena base address and
// every Alloc result are guaranteed to be aligned to this value.
func NewDecoderSize(arenaSize uint64, nodeCap int) (*Decoder, error) {
	return NewDecoderSizeAlign(arenaSize, nodeCap, 64)
}

// NewDecoderSizeAlign is NewDecoderSize with a custom alignment.
func NewDecoderSizeAlign(arenaSize uint64, nodeCap int, align uint64) (*Decoder, error) {
	arena, err := memory.NewArena(arenaSize, align)
	if err != nil {
		return nil, err
	}
	d := &Decoder{arena: arena, nodeCap: nodeCap}
	if err := d.doc.slab.initArena(arena, nodeCap); err != nil {
		_ = arena.Free()
		return nil, err
	}
	return d, nil
}

const (
	decoderDefaultArenaSize = 256 * 1024 // 256KB arena
	decoderDefaultNodeCap   = 4096       // ~10000 nodes per MB arena
)

// Parse decodes src into the Decoder's arena-backed AST. On success,
// the AST is accessible via Root and Get. On failure, Err returns the
// cause.
//
// Parse resets the arena — any Value returned from a previous Parse
// becomes invalid after the next Parse. Hold Values only across calls
// that don't re-parse.
func (d *Decoder) Parse(src []byte) error {
	d.err = nil
	d.doc.releaseOwnedSource()
	d.doc.generation++
	d.src = src
	d.root = noNode

	d.arena.Reset()
	// Arena.Reset rewinds the same mmap region; it does not invalidate the
	// existing column slices. Reusing them avoids four CAS allocations per
	// parse. Every emitted node overwrites all four fields before it is read.
	d.doc.slab.reset()
	d.doc.src = src

	var p fusedParser
	p.reset(&d.doc, src)
	idx, ok := p.parseDocument()
	if !ok {
		if p.err != nil {
			d.err = p.err
		} else {
			d.err = &SyntaxError{Msg: "parse failed"}
		}
		return d.err
	}
	d.root = idx
	d.doc.root = idx
	return nil
}

// Reset invalidates all Values produced by the decoder, releases any
// mutation-owned source buffer, and rewinds the existing off-heap arena
// without parsing a sentinel document. The Decoder remains usable for a
// subsequent Parse call.
//
// Reset is intended for callers that pool decoders. It performs no heap
// allocation and does not release the decoder's arena.
func (d *Decoder) Reset() {
	if d == nil {
		return
	}
	d.doc.generation++
	d.doc.root = noNode
	d.root = noNode
	d.err = nil
	d.doc.releaseOwnedSource()
	d.src = nil
	d.doc.src = nil
	if d.arena != nil {
		d.arena.Reset()
		d.doc.slab.reset()
	}
}

// Free releases the arena's mmap'd memory. After Free, the Decoder is
// unusable.
func (d *Decoder) Free() {
	d.doc.generation++
	d.doc.root = noNode
	d.root = noNode
	d.doc.slab.reset()
	d.doc.releaseOwnedSource()
	if d.doc.sourceScratch != nil {
		d.doc.sourceScratch.Free()
		d.doc.sourceScratch = nil
		d.doc.sourceScratchCap = 0
	}
	if d.arena != nil {
		d.arena.Free()
		d.arena = nil
	}
}

// Err returns the first error from the most recent Parse.
func (d *Decoder) Err() error { return d.err }

// Root returns the root value of the most recent successful Parse, or
// a zero Value on failure.
func (d *Decoder) Root() Value {
	if d.err != nil || d.root == noNode {
		return Value{}
	}
	return valueAt(&d.doc, d.root)
}

// Get returns the Value at the given path. Convenience for
// d.Root().get(p1).get(p2).... Zero-alloc.
func (d *Decoder) Get(path ...string) Value {
	v := d.Root()
	for _, p := range path {
		v = v.get(p)
		if !v.found {
			return v
		}
	}
	return v
}

// GetPath is like Get but takes a pre-built []string to avoid variadic
// overhead. Zero-alloc.
func (d *Decoder) GetPath(path []string) Value {
	v := d.Root()
	for _, p := range path {
		v = v.get(p)
		if !v.found {
			return v
		}
	}
	return v
}
