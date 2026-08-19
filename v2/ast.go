package apexjson

import (
	"unsafe"

	"github.com/xDarkicex/memory"
)

// Node kinds — JSON has exactly six value kinds plus the invalid sentinel.
const (
	nodeInvalid nodeKind = iota
	nodeObject
	nodeArray
	nodeString
	nodeNumber
	nodeTrue
	nodeFalse
	nodeNull
)

type nodeKind uint8

func (k nodeKind) String() string {
	switch k {
	case nodeObject:
		return "object"
	case nodeArray:
		return "array"
	case nodeString:
		return "string"
	case nodeNumber:
		return "number"
	case nodeTrue:
		return "true"
	case nodeFalse:
		return "false"
	case nodeNull:
		return "null"
	}
	return "invalid"
}

// nodeView is a temporary scalar view used by reflection-heavy decode error
// and dispatch branches. It is never stored in the tape.
type nodeView struct {
	Kind  nodeKind
	Flags tokenFlags
	Start uint32
	End   uint32
	Skip  int32
}

const noNode = int32(-1)

// nodeSize is the raw byte cost of one logical entry across the four columns.
// Callers may use it to size an arena; allocNodeColumns adds each column's
// alignment slack separately.
var nodeSize = int(unsafe.Sizeof(uint16(0)) + unsafe.Sizeof(uint32(0)) +
	unsafe.Sizeof(uint32(0)) + unsafe.Sizeof(int32(0)))

// nodeColumns is the physical tape layout. Each column is a dense typed
// array, which lets scans that need one field avoid loading the other fields.
// kind and flags share one uint16 metadata column because both are byte-sized
// attributes of the same node. The slices point into memory.Arena storage.
type nodeColumns struct {
	meta   []uint16
	starts []uint32
	ends   []uint32
	skips  []int32
}

// slab is the AST storage. It is a true structure-of-arrays tape: there is no
// []node backing array and no per-node pointer. n is kept separately so an
// arena-backed column can retain its full capacity without exposing stale
// entries as part of the current document.
type slab struct {
	nodeColumns
	n         int
	arena     *memory.Arena
	metaBase  unsafe.Pointer
	startBase unsafe.Pointer
	endBase   unsafe.Pointer
	skipBase  unsafe.Pointer
}

func (s *slab) reset() { s.n = 0 }

func (s *slab) len() int { return s.n }

func (s *slab) kind(idx int32) nodeKind {
	return nodeKind(s.meta[idx] & 0xff)
}

func (s *slab) flag(idx int32) tokenFlags {
	return tokenFlags(s.meta[idx] >> 8)
}
func (s *slab) start(idx int32) uint32 { return s.starts[idx] }
func (s *slab) end(idx int32) uint32   { return s.ends[idx] }
func (s *slab) skip(idx int32) int32   { return s.skips[idx] }

// view is a scalar logical view for reflection-heavy decode branches. It
// does not represent storage: the compiler can keep these fields in
// registers while the tape remains columnar in memory.
func (s *slab) view(idx int32) nodeView {
	return nodeView{
		Kind:  nodeKind(s.meta[idx] & 0xff),
		Flags: tokenFlags(s.meta[idx] >> 8),
		Start: s.starts[idx],
		End:   s.ends[idx],
		Skip:  s.skips[idx],
	}
}

func (s *slab) newLeaf(kind nodeKind, start, end uint32) int32 {
	return s.appendFixedNode(kind, 0, start, end)
}

func (s *slab) newLeafFlags(kind nodeKind, start, end uint32, flags tokenFlags) int32 {
	return s.appendFixedNode(kind, flags, start, end)
}

func (s *slab) openContainer(kind nodeKind, start uint32) int32 {
	return s.appendFixedNode(kind, 0, start, start)
}

func (s *slab) closeContainer(idx int32, end uint32) {
	off := uintptr(idx)
	*(*uint32)(unsafe.Add(s.endBase, off*unsafe.Sizeof(uint32(0)))) = end
	*(*int32)(unsafe.Add(s.skipBase, off*unsafe.Sizeof(int32(0)))) = int32(s.n)
}

// appendFixedNode adds one tape entry to pre-sized off-heap columns. The
// parser sizes the columns before entering the hot loop; overflow is a
// configuration error and is kept out of the normal writer path.
func (s *slab) appendFixedNode(kind nodeKind, flags tokenFlags, start, end uint32) int32 {
	i := s.n
	if i >= cap(s.meta) {
		panic("apexjson: node slab capacity exceeded")
	}
	off := uintptr(i)
	*(*uint16)(unsafe.Add(s.metaBase, off<<1)) =
		uint16(kind) | uint16(flags)<<8
	*(*uint32)(unsafe.Add(s.startBase, off<<2)) = start
	*(*uint32)(unsafe.Add(s.endBase, off<<2)) = end
	*(*int32)(unsafe.Add(s.skipBase, off<<2)) = int32(i + 1)
	s.n = i + 1
	return int32(i)
}

// appendFixedOverflow is cold: normal parses reserve enough columns before
// entering the parser. A tape cannot grow after parsing starts because its
// columns are mmap-backed and all child indexes are written in-place.
func (s *slab) appendFixedOverflow(kind nodeKind, flags tokenFlags, start, end uint32) int32 {
	panic("apexjson: node slab capacity exceeded")
}

// initArena allocates each tape column from the supplied off-heap arena.
// Allocating columns independently preserves the SoA layout and gives each
// column its natural typed access without unsafe field-offset arithmetic.
func (s *slab) initArena(arena *memory.Arena, capacity int) error {
	cols, err := allocNodeColumns(arena, capacity)
	if err != nil {
		return err
	}
	s.nodeColumns = cols
	s.n = 0
	s.arena = arena
	s.metaBase = unsafe.Pointer(&cols.meta[0])
	s.startBase = unsafe.Pointer(&cols.starts[0])
	s.endBase = unsafe.Pointer(&cols.ends[0])
	s.skipBase = unsafe.Pointer(&cols.skips[0])
	return nil
}

func allocNodeColumns(arena *memory.Arena, capacity int) (nodeColumns, error) {
	var cols nodeColumns
	var err error
	alloc := func(size uintptr) (unsafe.Pointer, error) {
		return arena.Alloc(uint64(size) * uint64(capacity))
	}
	ptr, err := alloc(unsafe.Sizeof(uint16(0)))
	if err != nil {
		return cols, err
	}
	cols.meta = unsafe.Slice((*uint16)(ptr), capacity)
	ptr, err = alloc(unsafe.Sizeof(uint32(0)))
	if err != nil {
		return nodeColumns{}, err
	}
	cols.starts = unsafe.Slice((*uint32)(ptr), capacity)
	ptr, err = alloc(unsafe.Sizeof(uint32(0)))
	if err != nil {
		return nodeColumns{}, err
	}
	cols.ends = unsafe.Slice((*uint32)(ptr), capacity)
	ptr, err = alloc(unsafe.Sizeof(int32(0)))
	if err != nil {
		return nodeColumns{}, err
	}
	cols.skips = unsafe.Slice((*int32)(ptr), capacity)
	return cols, nil
}

// parsedDoc is one parsed JSON document. Its tape columns are always
// arena-backed. One-shot documents own their arena until the last returned
// Value is unreachable; Decoder documents reuse their caller-owned arena.
type parsedDoc struct {
	slab slab
	src  []byte
	root int32

	// generation invalidates Value handles after a reparse. The source is
	// borrowed until the first mutation, then moved into ownedSource. Keeping
	// the source arena separate from the tape arena is important: Decoder.Parse
	// resets the tape arena on every document, while a mutated source must stay
	// alive until the next parse or Free.
	generation       uint64
	ownedSource      *memory.Arena
	ownedSourceCap   int
	sourceScratch    *memory.Arena
	sourceScratchCap int
	depth            int // parser bookkeeping retained for parser compatibility
}

func (d *parsedDoc) releaseOwnedSource() {
	if d == nil || d.ownedSource == nil {
		return
	}
	if d.sourceScratch != nil && d.sourceScratch != d.ownedSource {
		_ = d.sourceScratch.Free()
	}
	d.sourceScratch = d.ownedSource
	d.sourceScratchCap = d.ownedSourceCap
	d.ownedSource = nil
	d.ownedSourceCap = 0
}
