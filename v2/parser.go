package apexjson

import (
	"runtime"

	"github.com/xDarkicex/memory"
)

// parse parses src into the fused, off-heap-compatible tape builder. The
// reusable Decoder uses the same fusedParser with a persistent arena. A
// one-shot document keeps its mmap-backed arena alive through Value.owner.
func parse(src []byte) (*parsedDoc, error) {
	doc := &parsedDoc{src: src}
	// A malformed input can contain one unmatched container byte per emitted
	// node, so use the byte count as the safe one-shot upper bound. This keeps
	// syntax errors as errors instead of turning arena exhaustion into a panic.
	capacity := len(src) + 16
	// Four independently aligned columns need a small amount of alignment
	// slack in addition to their logical byte size.
	arenaSize := uint64(capacity*nodeSize + 4*64)
	arena, err := memory.NewArena(arenaSize, 64)
	if err != nil {
		return nil, err
	}
	if err := doc.slab.initArena(arena, capacity); err != nil {
		_ = arena.Free()
		return nil, err
	}
	runtime.SetFinalizer(doc, finalizeParsedDoc)
	var p fusedParser
	p.reset(doc, src)
	idx, ok := p.parseDocument()
	if !ok {
		runtime.SetFinalizer(doc, nil)
		finalizeParsedDoc(doc)
		return nil, p.err
	}
	doc.root = idx
	return doc, nil
}

func finalizeParsedDoc(doc *parsedDoc) {
	if doc == nil {
		return
	}
	if doc.slab.arena != nil {
		_ = doc.slab.arena.Free()
		doc.slab.arena = nil
	}
	doc.releaseOwnedSource()
}
