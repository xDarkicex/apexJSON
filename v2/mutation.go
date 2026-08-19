package apexjson

import (
	"errors"
	"math"
	"strconv"
	"unsafe"

	"github.com/xDarkicex/memory"
)

// ErrStaleValue reports that a Value was retained across a reparse or a
// variable-size mutation. Reacquire it from Decoder.Root/Get after the
// mutation. This makes pointer/index invalidation explicit instead of
// allowing an old node index to silently address a different value.
var ErrStaleValue = errors.New("apexjson: value is stale after document mutation")

// ErrInvalidMutation reports that a mutation produced invalid JSON. The
// original document remains intact when this is returned.
var ErrInvalidMutation = errors.New("apexjson: mutation produced invalid JSON")

func (v Value) source() []byte {
	if v.owner != nil {
		return v.owner.src
	}
	return v.src
}

func (v Value) validForMutation() bool {
	if v.owner != nil && v.epoch != v.owner.generation {
		return false
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n {
		return false
	}
	return true
}

// rawSpan returns the complete JSON span for a node. String node offsets are
// content-only in the tape, so their quotes are restored for raw replacement.
func (v Value) rawSpan() (int, int, bool) {
	if !v.validForMutation() {
		return 0, 0, false
	}
	return rawSpanAt(&parsedDoc{slab: *v.slab, src: v.source()}, v.idx)
}

// SetRaw replaces this value with one complete JSON value. The source is
// copied into off-heap storage on the first mutation (copy-on-write), the
// replacement is applied through raw byte offsets, and the tape is rebuilt
// only after the candidate parses successfully. Existing Value handles are
// invalidated; the receiver is updated and remains usable.
func (v *Value) SetRaw(raw []byte) error {
	if v == nil || !v.validForMutation() {
		return ErrStaleValue
	}
	start, end, ok := v.rawSpan()
	if !ok {
		return ErrStaleValue
	}
	doc := v.owner
	if doc == nil {
		return ErrStaleValue
	}
	idx, err := doc.replaceSpan(start, end, raw)
	if err != nil {
		return err
	}
	v.src = doc.src
	v.slab = &doc.slab
	v.idx = idx
	v.epoch = doc.generation
	return nil
}

// SetString replaces this value with a JSON string. It preserves the lazy
// source representation: unescaped strings continue to point at the owned
// off-heap source and escaped strings are decoded only when read.
func (v *Value) SetString(s string) error {
	var raw []byte
	raw = appendQuotedString(raw, s)
	flags := stringTokenFlags(raw)
	if v != nil && v.validForMutation() && v.slab.kind(v.idx) == nodeString {
		if done, err := v.replaceFixed(raw, nodeString, flags); done || err != nil {
			return err
		}
	}
	return v.SetRaw(raw)
}

// SetInt replaces this value with a signed JSON integer.
func (v *Value) SetInt(n int64) error {
	var scratch [32]byte
	raw := strconv.AppendInt(scratch[:0], n, 10)
	if done, err := v.replaceFixed(raw, nodeNumber, 0); done || err != nil {
		return err
	}
	return v.SetRaw(raw)
}

// SetUint replaces this value with an unsigned JSON integer.
func (v *Value) SetUint(n uint64) error {
	var scratch [32]byte
	raw := strconv.AppendUint(scratch[:0], n, 10)
	if done, err := v.replaceFixed(raw, nodeNumber, 0); done || err != nil {
		return err
	}
	return v.SetRaw(raw)
}

// SetFloat replaces this value with a JSON float.
func (v *Value) SetFloat(n float64) error {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return v.SetNull()
	}
	var scratch [32]byte
	raw := strconv.AppendFloat(scratch[:0], n, 'g', -1, 64)
	if done, err := v.replaceFixed(raw, nodeNumber, 0); done || err != nil {
		return err
	}
	return v.SetRaw(raw)
}

// SetBool replaces this value with true or false.
func (v *Value) SetBool(b bool) error {
	if b {
		if done, err := v.replaceFixed([]byte("true"), nodeTrue, 0); done || err != nil {
			return err
		}
		return v.SetRaw([]byte("true"))
	}
	if done, err := v.replaceFixed([]byte("false"), nodeFalse, 0); done || err != nil {
		return err
	}
	return v.SetRaw([]byte("false"))
}

// SetNull replaces this value with JSON null.
func (v *Value) SetNull() error {
	if done, err := v.replaceFixed([]byte("null"), nodeNull, 0); done || err != nil {
		return err
	}
	return v.SetRaw([]byte("null"))
}

// MutateUnsafe gives a temporary pointer to this value's complete JSON byte
// span in owned off-heap memory. The callback must keep the span length
// unchanged and leave valid JSON in it. The callback pointer is invalid after
// return. A candidate copy is parsed before commit, so invalid edits do not
// corrupt the live document.
//
// This is intentionally unsafe and pointer-oriented. It is the escape hatch
// for callers that already have a hand-rolled byte mutation kernel.
func (v *Value) MutateUnsafe(fn func(unsafe.Pointer, int)) error {
	if v == nil || fn == nil || !v.validForMutation() {
		return ErrStaleValue
	}
	start, end, ok := v.rawSpan()
	if !ok {
		return ErrStaleValue
	}
	doc := v.owner
	if doc == nil {
		return ErrStaleValue
	}
	idx, err := doc.mutateSpan(start, end, fn)
	if err != nil {
		return err
	}
	v.src = doc.src
	v.slab = &doc.slab
	v.idx = idx
	v.epoch = doc.generation
	return nil
}

// MutateBytes is the safer slice form of MutateUnsafe. The slice aliases an
// off-heap candidate and is valid only for the callback duration.
func (v *Value) MutateBytes(fn func([]byte)) error {
	if fn == nil {
		return ErrStaleValue
	}
	return v.MutateUnsafe(func(ptr unsafe.Pointer, n int) {
		fn(unsafe.Slice((*byte)(ptr), n))
	})
}

func (v *Value) replaceFixed(raw []byte, kind nodeKind, flags tokenFlags) (bool, error) {
	if v == nil || !v.validForMutation() || v.owner == nil {
		return false, nil
	}
	start, end, ok := v.rawSpan()
	if !ok || end-start != len(raw) {
		return false, nil
	}
	doc := v.owner
	if err := doc.ensureOwnedSource(); err != nil {
		return true, err
	}
	copy(doc.src[start:end], raw)
	off := uintptr(v.idx)
	*(*uint16)(unsafe.Add(doc.slab.metaBase, off<<1)) = uint16(kind) | uint16(flags)<<8
	v.src = doc.src
	return true, nil
}

func stringTokenFlags(raw []byte) tokenFlags {
	if len(raw) < 2 {
		return 0
	}
	flags := tokenStringASCII
	for _, b := range raw[1 : len(raw)-1] {
		if b >= 0x80 {
			flags &^= tokenStringASCII
		}
		if b == '\\' {
			flags |= tokenStringEscaped
		}
	}
	return flags
}

func (d *parsedDoc) ensureOwnedSource() error {
	if d.ownedSource != nil {
		return nil
	}
	capacity := len(d.src)*2 + 64
	if capacity < 64 {
		capacity = 64
	}
	arena, owned, arenaCap, err := d.cloneSourceWithCapacity(d.src, capacity)
	if err != nil {
		return err
	}
	d.ownedSource = arena
	d.ownedSourceCap = arenaCap
	d.src = owned
	return nil
}

func (d *parsedDoc) replaceSpan(start, end int, replacement []byte) (int32, error) {
	old := d.src
	if start < 0 || end < start || end > len(old) {
		return noNode, ErrStaleValue
	}
	capacity := len(old)*2 + len(replacement) + 64
	if capacity < len(old)-end+start+len(replacement) {
		capacity = len(old) - (end - start) + len(replacement)
	}
	if capacity < 64 {
		capacity = 64
	}
	arena, candidate, arenaCap, err := d.cloneSourceWithCapacity(old, capacity)
	if err != nil {
		return noNode, err
	}
	newLen := len(old) - (end - start) + len(replacement)
	candidate = candidate[:newLen:newLen]
	copy(candidate[start+len(replacement):], old[end:])
	copy(candidate[start:], replacement)
	return d.commitCandidate(arena, arenaCap, candidate)
}

func (d *parsedDoc) mutateSpan(start, end int, fn func(unsafe.Pointer, int)) (int32, error) {
	old := d.src
	if start < 0 || end < start || end > len(old) {
		return noNode, ErrStaleValue
	}
	arena, candidate, arenaCap, err := d.cloneSourceWithCapacity(old, len(old))
	if err != nil {
		return noNode, err
	}
	fn(unsafe.Pointer(unsafe.SliceData(candidate[start:end])), end-start)
	return d.commitCandidate(arena, arenaCap, candidate)
}

func (d *parsedDoc) cloneSourceWithCapacity(src []byte, capacity int) (*memory.Arena, []byte, int, error) {
	if capacity < len(src) {
		capacity = len(src)
	}
	if capacity < 64 {
		capacity = 64
	}
	arena := d.sourceScratch
	arenaCap := d.sourceScratchCap
	if arena != nil && arenaCap < capacity {
		_ = arena.Free()
		arena = nil
		arenaCap = 0
	}
	if arena == nil {
		var err error
		arena, err = memory.NewArena(uint64(capacity), 64)
		if err != nil {
			return nil, nil, 0, err
		}
		arenaCap = capacity
	} else {
		arena.Reset()
	}
	d.sourceScratch = nil
	d.sourceScratchCap = 0
	ptr, err := arena.Alloc(uint64(arenaCap))
	if err != nil {
		_ = arena.Free()
		return nil, nil, 0, err
	}
	dst := unsafe.Slice((*byte)(ptr), arenaCap)[:len(src):arenaCap]
	copy(dst, src)
	return arena, dst, arenaCap, nil
}

func (d *parsedDoc) commitCandidate(candidateArena *memory.Arena, candidateCap int, candidate []byte) (int32, error) {
	oldSrc := d.src
	oldRoot := d.root
	idx, err := d.parseSource(candidate)
	if err != nil {
		// Parsing the candidate may have overwritten the tape. Restore the
		// old source/tape before returning the error so mutation is atomic.
		restoreIdx, restoreErr := d.parseSource(oldSrc)
		if restoreErr == nil {
			d.root = oldRoot
			_ = restoreIdx
		}
		d.sourceScratch = candidateArena
		d.sourceScratchCap = candidateCap
		if restoreErr != nil {
			return noNode, ErrInvalidMutation
		}
		return noNode, err
	}

	oldArena := d.ownedSource
	oldArenaCap := d.ownedSourceCap
	d.ownedSource = candidateArena
	d.ownedSourceCap = candidateCap
	d.src = candidate
	d.root = idx
	d.generation++
	if oldArena != nil {
		if d.sourceScratch != nil && d.sourceScratch != oldArena {
			_ = d.sourceScratch.Free()
		}
		d.sourceScratch = oldArena
		d.sourceScratchCap = oldArenaCap
	}
	return idx, nil
}

func (d *parsedDoc) parseSource(src []byte) (int32, error) {
	d.slab.arena.Reset()
	d.slab.reset()
	var p fusedParser
	p.reset(d, src)
	idx, ok := p.parseDocument()
	if !ok {
		if p.err != nil {
			return noNode, p.err
		}
		return noNode, ErrInvalidMutation
	}
	return idx, nil
}
