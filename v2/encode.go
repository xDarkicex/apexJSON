package apexjson

import (
	"encoding/json" // for stdlib-compatible Marshaler interfaces
	"errors"
	"io"
	"math"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/xDarkicex/memory"
)

// Encoder writes JSON-encoded values to an output stream.
type Encoder struct {
	w       io.Writer
	buf     []byte
	scratch *marshalScratch
	err     error
	closed  bool
}

// NewEncoder returns a new encoder that writes to w.
func NewEncoder(w io.Writer) *Encoder {
	e := &Encoder{w: w, scratch: marshalBufPool.Get().(*marshalScratch)}
	e.buf = e.scratch.buf[:0]
	runtime.SetFinalizer(e, func(e *Encoder) {
		if e.scratch != nil {
			marshalBufPool.Put(e.scratch)
			e.scratch = nil
		}
	})
	return e
}

// Encode writes the JSON encoding of v to the underlying writer.
func (e *Encoder) Encode(v any) error {
	if e.closed {
		return ErrEncoderClosed
	}
	if e.err != nil {
		return e.err
	}
	// Reattach the reusable off-heap buffer after any previous oversized
	// encode may have caused append to use a temporary heap backing array.
	e.buf = e.scratch.buf[:0]
	if err := encodeAny(v, &e.buf); err != nil {
		e.err = err
		return err
	}
	e.buf = append(e.buf, '\n')
	if _, err := e.w.Write(e.buf); err != nil {
		e.err = err
		return err
	}
	return nil
}

// Close returns the encoder's off-heap scratch arena to the reuse pool. It is
// optional for correctness, but should be called when an Encoder has a
// bounded lifetime so its mmap region can be reused immediately.
func (e *Encoder) Close() error {
	if e == nil || e.closed {
		return nil
	}
	e.closed = true
	runtime.SetFinalizer(e, nil)
	if e.scratch != nil {
		e.scratch.buf = e.scratch.buf[:0]
		marshalBufPool.Put(e.scratch)
		e.scratch = nil
	}
	return nil
}

// ErrEncoderClosed is returned when Encode is called after Close.
var ErrEncoderClosed = errors.New("apexjson: encoder is closed")

// SetIndent enables indented output. placeholder.
func (e *Encoder) SetIndent(prefix, indent string) {
	// TODO: implement indent encoding
}

// Marshal returns the JSON encoding of v.
//
// Zero-reflection hot path: extracts the interface's type pointer and
// data pointer via unsafe, then dispatches via a kind byte read directly
// from the runtime type descriptor — no reflect.Value, no rv.Type() calls,
// no per-call interface boxing in the typeInfo cache lookup.
func Marshal(v any) ([]byte, error) {
	// Marshal returns the final byte slice to the caller, so encoding directly
	// into that allocation avoids the previous full-document scratch copy.
	buf := make([]byte, 0, marshalCapacityHint(v))
	if err := encodeAny(v, &buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// marshalCapacityHint is deliberately type-based and bounded. It is paid
// once per Marshal call and avoids walking the value just to count output
// bytes. The 2x element-size heuristic covers ordinary record slices while
// allowing append to grow for unusually long strings or escaped values.
func marshalCapacityHint(v any) int {
	// The JSON shapes are sized from the value itself, with no reflection.
	switch value := v.(type) {
	case nil:
		return 4
	case string:
		return clampCapacityHint(len(value)*2 + 2)
	case map[string]any:
		return clampCapacityHint(len(value)*48 + 2)
	case map[string]string:
		return clampCapacityHint(len(value)*48 + 2)
	case []any:
		return clampCapacityHint(len(value)*30 + 2)
	case []string:
		return clampCapacityHint(len(value)*30 + 2)
	case bool, int, int32, int64, uint, uint32, uint64, float32, float64:
		return 64
	}
	// The remaining shapes resolve their type from the interface header and
	// take the cached estimate when the type alone determines it. A struct's
	// estimate is computed once, when its type info is built.
	words := (*[2]unsafe.Pointer)(unsafe.Pointer(&v))
	ti := loadTypeInfo(uintptr(words[0]))
	if ti != nil && ti.hintFromType {
		return clampCapacityHint(ti.sizeHint)
	}
	rv := reflect.ValueOf(v)
	return marshalTypeCapacityHint(rv.Type(), rv, 0)
}

// clampCapacityHint bounds a capacity estimate.
func clampCapacityHint(n int) int {
	const maxInt = int(^uint(0) >> 1)
	if n < 64 {
		return 64
	}
	if n > maxInt/2 {
		return maxInt
	}
	return n
}

func marshalTypeCapacityHint(t reflect.Type, rv reflect.Value, depth int) int {
	if t == nil || depth > 2 {
		return 1024
	}
	const maxInt = int(^uint(0) >> 1)
	clamp := clampCapacityHint
	switch t.Kind() {
	case reflect.String:
		return clamp(rv.Len()*2 + 2)
	case reflect.Struct:
		return clamp(int(t.Size()) + 16)
	case reflect.Slice, reflect.Array:
		n := rv.Len()
		elem := t.Elem()
		per := int(elem.Size()) * 15 / 8
		if per < 32 {
			per = 32
		}
		if n > (maxInt-2)/per {
			return maxInt
		}
		return clamp(n*per + 2)
	case reflect.Map:
		return clamp(rv.Len()*48 + 2)
	case reflect.Ptr, reflect.Interface:
		if rv.IsNil() {
			return 4
		}
		return marshalTypeCapacityHint(t.Elem(), rv.Elem(), depth+1)
	default:
		return 64
	}
}

// MarshalPtr returns the JSON encoding of *v. It keeps the concrete pointer
// on the unsafe encoder path, so the only steady-state heap allocation is the
// returned byte slice itself.
func MarshalPtr[T any](v *T) ([]byte, error) {
	scratch := marshalBufPool.Get().(*marshalScratch)
	defer marshalBufPool.Put(scratch)
	scratch.buf = scratch.buf[:0]
	if v == nil {
		scratch.buf = append(scratch.buf, "null"...)
	} else {
		ti := getTypeInfo(reflect.TypeOf((*T)(nil)).Elem())
		if err := encodeRawAddressable(unsafe.Pointer(v), ti, &scratch.buf); err != nil {
			return nil, err
		}
	}
	out := make([]byte, len(scratch.buf))
	copy(out, scratch.buf)
	return out, nil
}

// AppendMarshal appends the JSON encoding of v to dst. Callers that retain
// and size dst can use this path for steady-state zero-allocation encoding.
// Unlike Marshal, it does not copy the result out of a reusable scratch
// buffer because ownership of the destination is supplied by the caller.
func AppendMarshal(dst []byte, v any) ([]byte, error) {
	if err := encodeAny(v, &dst); err != nil {
		return dst, err
	}
	return dst, nil
}

// AppendMarshalPtr appends the JSON encoding of *v to dst using the concrete
// pointer directly. It is the allocation-free marshal entry point when dst
// already has capacity: there is no interface boxing of v and the hot path
// reads fields through cached offsets and unsafe.Pointer arithmetic.
//
// The pointer must remain valid for the duration of the call. A nil pointer
// encodes as JSON null.
func AppendMarshalPtr[T any](dst []byte, v *T) ([]byte, error) {
	if v == nil {
		return append(dst, "null"...), nil
	}
	ti := getTypeInfo(reflect.TypeOf((*T)(nil)).Elem())
	if err := encodeRawAddressable(unsafe.Pointer(v), ti, &dst); err != nil {
		return dst, err
	}
	return dst, nil
}

type marshalScratch struct {
	arena *memory.Arena
	buf   []byte
}

const marshalScratchCapacity = 2 << 20

var marshalBufPool = sync.Pool{
	New: func() any { return newMarshalScratch() },
}

func newMarshalScratch() *marshalScratch {
	arena, err := memory.NewArena(marshalScratchCapacity, 64)
	if err != nil {
		// mmap failure is exceptional. Keep Marshal's historical behavior
		// available as a last-resort fallback rather than making the public
		// API panic during process startup.
		return &marshalScratch{buf: make([]byte, 0, 1024)}
	}
	ptr, err := arena.Alloc(marshalScratchCapacity)
	if err != nil {
		_ = arena.Free()
		return &marshalScratch{buf: make([]byte, 0, 1024)}
	}
	s := &marshalScratch{
		arena: arena,
		buf:   unsafe.Slice((*byte)(ptr), marshalScratchCapacity)[:0:marshalScratchCapacity],
	}
	runtime.SetFinalizer(s, func(s *marshalScratch) {
		if s != nil && s.arena != nil {
			_ = s.arena.Free()
			s.arena = nil
		}
	})
	return s
}

// encodeAny is the entry point for top-level Marshal/Encoder.Encode.
// It reads the interface header directly and dispatches through the cached
// typeInfo fast path. Reflection remains only for direct-interface values
// and custom marshalers.
//
// For the typed-struct hot path, encodeStructAddr is called directly
// from encodeValue, bypassing reflect.Value.Field(i).
func encodeAny(v any, buf *[]byte) error {
	// The JSON shapes are encoded without reflect. Everything else falls
	// through to the interface-header path below.
	if handled, err := encodeAnyFast(v, buf); handled {
		return err
	}
	if v == nil {
		*buf = append(*buf, "null"...)
		return nil
	}
	words := (*[2]unsafe.Pointer)(unsafe.Pointer(&v))
	typePtr := words[0]
	ti := loadTypeInfo(uintptr(typePtr))
	if ti == nil {
		ti = getTypeInfo(reflect.TypeOf(v))
	}
	if ti.isRawMessage {
		return appendRawMessage(words[1], buf)
	}
	// A top-level value in an interface is not addressable, so pointer
	// receiver methods must not be synthesized here. Value receiver
	// marshalers still require Interface() and therefore use the fallback.
	if ti.hasMarshaler || ti.hasTextMarshal {
		return encodeValue(reflect.ValueOf(v), buf)
	}
	// Pointer inputs are still dispatched to the unsafe address walker. Use the
	// runtime's pointer accessor only for this direct-interface case: unlike
	// passing a reinterpreted interface word to another function, it does not
	// make the caller's interface value escape.
	if ti.kind == reflect.Ptr && !typeIfaceIndir(typePtr) {
		return encodeDirectPointer(unsafe.Pointer(reflect.ValueOf(v).Pointer()), ti, buf)
	}
	// Indirect values already point at their value bytes. Direct-interface
	// values (pointers, maps, chans, and funcs) need reflect's representation
	// handling, and are not the struct/scalar hot path this function targets.
	if !typeIfaceIndir(typePtr) {
		return encodeValue(reflect.ValueOf(v), buf)
	}
	return encodeRaw(words[1], ti, buf)
}

func encodeDirectPointer(p unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if p == nil {
		*buf = append(*buf, "null"...)
		return nil
	}
	elem := ti.elementInfo()
	if elem.kind == reflect.Struct && !elem.hasMarshaler && !elem.hasPtrMarshaler &&
		!elem.hasTextMarshal && !elem.hasPtrTextMarshal {
		return encodeStructAddr(p, elem, buf)
	}
	return encodeRawAddressable(p, elem, buf)
}

// encodeRaw dispatches using immutable metadata. The data pointer always
// points at the value bytes, never at an inferred interface representation.
// Keeping that invariant is what makes the unsafe scalar reads correct for
// narrow integers and float32 values as well as their 64-bit counterparts.
func encodeRaw(dataPtr unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if ti == nil {
		*buf = append(*buf, "null"...)
		return nil
	}
	if ti.isRawMessage {
		return appendRawMessage(dataPtr, buf)
	}
	switch ti.kind {
	case reflect.Bool:
		if *(*bool)(dataPtr) {
			*buf = append(*buf, "true"...)
		} else {
			*buf = append(*buf, "false"...)
		}
	case reflect.Int:
		*buf = appendIntFast(*buf, int64(*(*int)(dataPtr)))
	case reflect.Int8:
		*buf = appendIntFast(*buf, int64(*(*int8)(dataPtr)))
	case reflect.Int16:
		*buf = appendIntFast(*buf, int64(*(*int16)(dataPtr)))
	case reflect.Int32:
		*buf = appendIntFast(*buf, int64(*(*int32)(dataPtr)))
	case reflect.Int64:
		*buf = appendIntFast(*buf, *(*int64)(dataPtr))
	case reflect.Uint:
		*buf = appendUintFast(*buf, uint64(*(*uint)(dataPtr)))
	case reflect.Uint8:
		*buf = appendUintFast(*buf, uint64(*(*uint8)(dataPtr)))
	case reflect.Uint16:
		*buf = appendUintFast(*buf, uint64(*(*uint16)(dataPtr)))
	case reflect.Uint32:
		*buf = appendUintFast(*buf, uint64(*(*uint32)(dataPtr)))
	case reflect.Uint64:
		*buf = appendUintFast(*buf, *(*uint64)(dataPtr))
	case reflect.Uintptr:
		*buf = appendUintFast(*buf, uint64(*(*uintptr)(dataPtr)))
	case reflect.Float32:
		f := float64(*(*float32)(dataPtr))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			*buf = append(*buf, "null"...)
		} else {
			*buf = appendFloat32Fast(*buf, float32(f))
		}
	case reflect.Float64:
		f := *(*float64)(dataPtr)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			*buf = append(*buf, "null"...)
		} else {
			*buf = appendFloat64Fast(*buf, f)
		}
	case reflect.String:
		*buf = appendQuotedString(*buf, *(*string)(dataPtr))
	case reflect.Struct:
		return encodeStructAddr(dataPtr, ti, buf)
	case reflect.Ptr:
		p := *(*unsafe.Pointer)(dataPtr)
		if p == nil {
			*buf = append(*buf, "null"...)
			return nil
		}
		elem := ti.elementInfo()
		if elem.kind == reflect.Struct && !elem.hasMarshaler && !elem.hasPtrMarshaler {
			return encodeStructAddr(p, elem, buf)
		}
		return encodeAddressed(ti.typ.Elem(), p, buf)
	case reflect.Slice:
		return encodeSliceAddr(dataPtr, ti, buf)
	case reflect.Array:
		return encodeArrayAddr(dataPtr, ti, buf)
	case reflect.Map:
		return encodeMapAddr(dataPtr, ti, buf)
	case reflect.Interface:
		return encodeAddressed(ti.typ, dataPtr, buf)
	default:
		return &ErrUnsupportedType{ti.typ.String()}
	}
	return nil
}

func encodeRawAddressable(dataPtr unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if ti != nil && ti.isRawMessage {
		return appendRawMessage(dataPtr, buf)
	}
	if ti != nil && (ti.hasMarshaler || ti.hasTextMarshal || ti.hasPtrMarshaler || ti.hasPtrTextMarshal) {
		return encodeAddressed(ti.typ, dataPtr, buf)
	}
	return encodeRaw(dataPtr, ti, buf)
}

// encodeAddressed creates a reflect.Value over an existing address. It does
// not allocate; it is used only for interfaces, maps, and custom marshalers
// where the runtime itself owns the representation.
func encodeAddressed(t reflect.Type, p unsafe.Pointer, buf *[]byte) error {
	return encodeValue(reflect.NewAt(t, p).Elem(), buf)
}

// encodeStructAddr writes a struct at base using cached typeInfo.
func encodeStructAddr(base unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if ti == nil {
		return &ErrUnsupportedType{"<nil>"}
	}
	// A nested unsafe path must still honor both value and pointer receiver
	// marshalers. The address is valid for the duration of this call.
	if ti.hasMarshaler || ti.hasPtrMarshaler || ti.hasTextMarshal || ti.hasPtrTextMarshal {
		return encodeAddressed(ti.typ, base, buf)
	}
	if ti.flatPrimitive {
		*buf = appendFlatPrimitiveStruct(*buf, base, ti)
		return nil
	}
	*buf = append(*buf, '{')
	if !ti.hasOmitEmpty {
		if len(ti.fields) == 0 {
			*buf = append(*buf, '}')
			return nil
		}
		first := &ti.fields[0]
		*buf = appendPackedPrefixUnsafe(*buf, first.marshalPrefix, &first.marshalPrefixWords)
		firstPtr := unsafe.Pointer(uintptr(base) + first.offset)
		if err := encodePlannedField(firstPtr, first, buf); err != nil {
			return err
		}
		for i := 1; i < len(ti.fields); i++ {
			fi := &ti.fields[i]
			*buf = appendPackedPrefixUnsafe(*buf, fi.marshalPrefixComma, &fi.marshalPrefixCommaWords)
			fldPtr := unsafe.Pointer(uintptr(base) + fi.offset)
			if err := encodePlannedField(fldPtr, fi, buf); err != nil {
				return err
			}
		}
		*buf = append(*buf, '}')
		return nil
	}
	first := true
	for i := range ti.fields {
		fi := &ti.fields[i]
		fldPtr := unsafe.Pointer(uintptr(base) + fi.offset)
		if fi.omitempty && isEmptyField(fldPtr, fi) {
			continue
		}
		if !first {
			*buf = appendPackedPrefixUnsafe(*buf, fi.marshalPrefixComma, &fi.marshalPrefixCommaWords)
		} else {
			*buf = appendPackedPrefixUnsafe(*buf, fi.marshalPrefix, &fi.marshalPrefixWords)
		}
		first = false
		if err := encodePlannedField(fldPtr, fi, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, '}')
	return nil
}

// appendFlatPrimitiveStruct is the specialized repeated-record marshal path.
// Its eligibility is compiled into typeInfo, so this loop only handles
// primitive scalar/string operations. In particular, it does not re-enter the
// general encodePlannedField switch for every field in every record.
func appendFlatPrimitiveStruct(dst []byte, base unsafe.Pointer, ti *typeInfo) []byte {
	dst = append(dst, '{')
	for i := range ti.fields {
		fi := &ti.fields[i]
		if i == 0 {
			dst = appendPackedPrefixUnsafe(dst, fi.marshalPrefix, &fi.marshalPrefixWords)
		} else {
			dst = appendPackedPrefixUnsafe(dst, fi.marshalPrefixComma, &fi.marshalPrefixCommaWords)
		}
		p := unsafe.Add(base, fi.offset)
		switch fi.marshalOp {
		case marshalOpBool:
			if *(*bool)(p) {
				dst = append(dst, "true"...)
			} else {
				dst = append(dst, "false"...)
			}
		case marshalOpInt:
			dst = appendIntFast(dst, int64(*(*int)(p)))
		case marshalOpInt8:
			dst = appendIntFast(dst, int64(*(*int8)(p)))
		case marshalOpInt16:
			dst = appendIntFast(dst, int64(*(*int16)(p)))
		case marshalOpInt32:
			dst = appendIntFast(dst, int64(*(*int32)(p)))
		case marshalOpInt64:
			dst = appendIntFast(dst, *(*int64)(p))
		case marshalOpUint:
			dst = appendUintFast(dst, uint64(*(*uint)(p)))
		case marshalOpUint8:
			dst = appendUintFast(dst, uint64(*(*uint8)(p)))
		case marshalOpUint16:
			dst = appendUintFast(dst, uint64(*(*uint16)(p)))
		case marshalOpUint32:
			dst = appendUintFast(dst, uint64(*(*uint32)(p)))
		case marshalOpUint64:
			dst = appendUintFast(dst, *(*uint64)(p))
		case marshalOpUintptr:
			dst = appendUintFast(dst, uint64(*(*uintptr)(p)))
		case marshalOpFloat32:
			f := float64(*(*float32)(p))
			if math.IsNaN(f) || math.IsInf(f, 0) {
				dst = append(dst, "null"...)
			} else {
				dst = appendFloat32Fast(dst, float32(f))
			}
		case marshalOpFloat64:
			f := *(*float64)(p)
			if math.IsNaN(f) || math.IsInf(f, 0) {
				dst = append(dst, "null"...)
			} else {
				dst = appendFloat64Fast(dst, f)
			}
		case marshalOpString:
			dst = appendQuotedString(dst, *(*string)(p))
		}
	}
	return append(dst, '}')
}

// appendStaticPrefixUnsafe appends a cached, immutable field prefix directly
// into dst. The normal Marshal path pre-sizes dst, so the hot path writes
// through pointers without creating a temporary slice or issuing a second
// append for the separator. Capacity growth retains the ordinary append
// fallback. The fixed prefixes are safe to write in bounded unaligned words
// on the supported 64-bit targets; the final partial word is always written
// through the existing slice bounds and never crosses the logical destination
// length.
func appendPackedPrefixUnsafe(dst, src []byte, words *[2]uint64) []byte {
	n := len(src)
	oldLen := len(dst)
	if n == 0 || n > 16 || n > cap(dst)-oldLen || strconv.IntSize != 64 {
		return append(dst, src...)
	}
	dst = dst[:oldLen+n]
	if n < 8 {
		copy(dst[oldLen:], src)
		return dst
	}
	dstPtr := unsafe.Add(unsafe.Pointer(unsafe.SliceData(dst)), oldLen)
	if n == 8 {
		*(*uint64)(dstPtr) = words[0]
		return dst
	}
	if n > 8 {
		*(*uint64)(dstPtr) = words[0]
		if n == 16 {
			*(*uint64)(unsafe.Add(dstPtr, 8)) = words[1]
			return dst
		}
		copy(dst[oldLen+8:], src[8:])
		return dst
	}
	return dst
}

// isEmptyField returns true if the field at p should be omitted under
// "omitempty". Direct typed reads use the field's exact Go kind.
func isEmptyField(p unsafe.Pointer, fi *fieldInfo) bool {
	switch fi.goKind {
	case reflect.String:
		return len(*(*string)(p)) == 0
	case reflect.Slice:
		sh := (*struct {
			data unsafe.Pointer
			len  int
			cap  int
		})(p)
		return sh.len == 0
	case reflect.Map:
		return reflect.NewAt(fi.typ, p).Elem().Len() == 0
	case reflect.Array:
		return fi.typ.Len() == 0
	case reflect.Bool:
		return !*(*bool)(p)
	case reflect.Int:
		return *(*int)(p) == 0
	case reflect.Int8:
		return *(*int8)(p) == 0
	case reflect.Int16:
		return *(*int16)(p) == 0
	case reflect.Int32:
		return *(*int32)(p) == 0
	case reflect.Int64:
		return *(*int64)(p) == 0
	case reflect.Uint:
		return *(*uint)(p) == 0
	case reflect.Uint8:
		return *(*uint8)(p) == 0
	case reflect.Uint16:
		return *(*uint16)(p) == 0
	case reflect.Uint32:
		return *(*uint32)(p) == 0
	case reflect.Uint64:
		return *(*uint64)(p) == 0
	case reflect.Uintptr:
		return *(*uintptr)(p) == 0
	case reflect.Float32:
		return *(*float32)(p) == 0
	case reflect.Float64:
		return *(*float64)(p) == 0
	case reflect.Ptr, reflect.Interface:
		return *(*unsafe.Pointer)(p) == nil
	}
	return false
}

// encodePlannedField executes the precomputed marshal operation. Scalar
// fields take one operation switch and one unsafe load; there is no second
// reflect.Kind switch and no typeInfo lookup in the common record path.
func encodePlannedField(p unsafe.Pointer, fi *fieldInfo, buf *[]byte) error {
	switch fi.marshalOp {
	case marshalOpBool:
		if *(*bool)(p) {
			*buf = append(*buf, "true"...)
		} else {
			*buf = append(*buf, "false"...)
		}
	case marshalOpInt:
		*buf = appendIntFast(*buf, int64(*(*int)(p)))
	case marshalOpInt8:
		*buf = appendIntFast(*buf, int64(*(*int8)(p)))
	case marshalOpInt16:
		*buf = appendIntFast(*buf, int64(*(*int16)(p)))
	case marshalOpInt32:
		*buf = appendIntFast(*buf, int64(*(*int32)(p)))
	case marshalOpInt64:
		*buf = appendIntFast(*buf, *(*int64)(p))
	case marshalOpUint:
		*buf = appendUintFast(*buf, uint64(*(*uint)(p)))
	case marshalOpUint8:
		*buf = appendUintFast(*buf, uint64(*(*uint8)(p)))
	case marshalOpUint16:
		*buf = appendUintFast(*buf, uint64(*(*uint16)(p)))
	case marshalOpUint32:
		*buf = appendUintFast(*buf, uint64(*(*uint32)(p)))
	case marshalOpUint64:
		*buf = appendUintFast(*buf, *(*uint64)(p))
	case marshalOpUintptr:
		*buf = appendUintFast(*buf, uint64(*(*uintptr)(p)))
	case marshalOpFloat32:
		f := float64(*(*float32)(p))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			*buf = append(*buf, "null"...)
		} else {
			*buf = appendFloat32Fast(*buf, float32(f))
		}
	case marshalOpFloat64:
		f := *(*float64)(p)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			*buf = append(*buf, "null"...)
		} else {
			*buf = appendFloat64Fast(*buf, f)
		}
	case marshalOpString:
		*buf = appendQuotedString(*buf, *(*string)(p))
	case marshalOpCustom:
		return encodeAddressed(fi.typ, p, buf)
	case marshalOpStruct:
		child := fi.marshalTypeInfo()
		return encodeStructAddr(p, child, buf)
	case marshalOpPtr:
		if *(*unsafe.Pointer)(p) == nil {
			*buf = append(*buf, "null"...)
			return nil
		}
		return encodeRawAddressable(p, fi.marshalTypeInfo(), buf)
	case marshalOpSlice:
		return encodeSliceAddr(p, fi.marshalTypeInfo(), buf)
	case marshalOpArray:
		return encodeArrayAddr(p, fi.marshalTypeInfo(), buf)
	case marshalOpMap:
		return encodeMapAddr(p, fi.marshalTypeInfo(), buf)
	case marshalOpInterface:
		return encodeAddressed(fi.typ, p, buf)
	default:
		return &ErrUnsupportedType{Type: fi.kind.String()}
	}
	return nil
}

// decimalPairs is indexed by digit pair. The table avoids the division and
// remainder work needed to emit the final two decimal digits of each chunk.
const decimalPairs = "00010203040506070809" +
	"10111213141516171819" +
	"20212223242526272829" +
	"30313233343536373839" +
	"40414243444546474849" +
	"50515253545556575859" +
	"60616263646566676869" +
	"70717273747576777879" +
	"80818283848586878889" +
	"90919293949596979899"

// appendUintFast emits an unsigned integer using two decimal digits per
// iteration. The local buffer is stack-resident and append remains the only
// possible destination growth operation.
func appendUintFast(dst []byte, n uint64) []byte {
	if n < 10 {
		return append(dst, byte('0'+n))
	}
	if n < 100 {
		i := int(n) << 1
		return append(dst, decimalPairs[i], decimalPairs[i+1])
	}
	var buf [20]byte
	i := len(buf)
	for n >= 100 {
		q := n / 100
		i -= 2
		pair := int(n-q*100) << 1
		buf[i] = decimalPairs[pair]
		buf[i+1] = decimalPairs[pair+1]
		n = q
	}
	if n < 10 {
		i--
		buf[i] = byte('0' + n)
	} else {
		i -= 2
		pair := int(n) << 1
		buf[i] = decimalPairs[pair]
		buf[i+1] = decimalPairs[pair+1]
	}
	return append(dst, buf[i:]...)
}

func appendIntFast(dst []byte, n int64) []byte {
	if n >= 0 {
		return appendUintFast(dst, uint64(n))
	}
	dst = append(dst, '-')
	// -(MinInt64) overflows, so offset before negating and add the missing 1
	// in unsigned space.
	return appendUintFast(dst, uint64(-(n+1))+1)
}

func appendFloat32Fast(dst []byte, f float32) []byte {
	abs := float32(math.Abs(float64(f)))
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	dst = strconv.AppendFloat(dst, float64(f), format, -1, 32)
	return trimFloatExponent(dst, format)
}

func appendFloat64Fast(dst []byte, f float64) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	dst = strconv.AppendFloat(dst, f, format, -1, 64)
	return trimFloatExponent(dst, format)
}

func trimFloatExponent(dst []byte, format byte) []byte {
	if format != 'e' {
		return dst
	}
	n := len(dst)
	if n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
		dst[n-2] = dst[n-1]
		return dst[:n-1]
	}
	return dst
}

// typePtrFromReflectType recovers the runtime type pointer from a reflect.Type.
// reflect.Type is an iface with the *rtype pointer in the data word
// (word index 1).
func typePtrFromReflectType(t reflect.Type) unsafe.Pointer {
	if t == nil {
		return nil
	}
	iface := *(*[2]unsafe.Pointer)(unsafe.Pointer(&t))
	return iface[1]
}

const (
	reflectKindOffset       = 23 // internal/abi.Type.Kind_ on Go 1.25
	reflectKindDirectIface  = 1 << 5
	reflectTFlagOffset      = 20     // internal/abi.Type.TFlag
	reflectTFlagDirectIface = 1 << 5 // abi.TFlagDirectIface on Go 1.27
)

// typeIfaceIndir reports whether a value of the given runtime type is stored
// indirectly in an interface.
//
// Go 1.27 moved the direct-interface flag out of the type's kind byte and into
// its TFlag byte. Reading only the kind byte therefore made every type look
// indirect on Go 1.27, and the encoder then walked a map's header as though
// it were the address of a slot holding the map. Bit 5 of TFlag is unused on
// Go 1.25 and earlier, so consulting both bytes is safe on every supported
// version.
func typeIfaceIndir(typePtr unsafe.Pointer) bool {
	if typePtr == nil {
		return true
	}
	if *(*uint8)(unsafe.Add(typePtr, reflectTFlagOffset))&reflectTFlagDirectIface != 0 {
		return false
	}
	k := *(*uint8)(unsafe.Add(typePtr, reflectKindOffset))
	return k&reflectKindDirectIface == 0
}

// reflect.Value is three machine words on supported Go versions. The first
// two words are the runtime type and value pointer; flagIndir tells whether
// the value word points at the bytes or contains the bytes directly. This is
// intentionally isolated here so the rest of the encoder only deals in
// already-normalized data pointers.
type valueHeader struct {
	typ  unsafe.Pointer
	ptr  unsafe.Pointer
	flag uintptr
}

const reflectFlagIndir uintptr = 1 << 7

func valueTypePtr(rv reflect.Value) unsafe.Pointer {
	return (*valueHeader)(unsafe.Pointer(&rv)).typ
}

func valueDataPtr(rv reflect.Value) unsafe.Pointer {
	h := (*valueHeader)(unsafe.Pointer(&rv))
	if h.flag&reflectFlagIndir != 0 {
		return h.ptr
	}
	return unsafe.Pointer(&h.ptr)
}

// encodeSliceAddr walks a slice using cached element metadata.
func encodeSliceAddr(p unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if ti == nil {
		return &ErrUnsupportedType{"<nil>"}
	}
	elemType := ti.typ.Elem()
	// []byte fast path.
	if elemType.Kind() == reflect.Uint8 {
		b := *(*[]byte)(p)
		*buf = appendQuotedBytes(*buf, b)
		return nil
	}
	sliceHeader := *(*struct {
		data unsafe.Pointer
		len  int
		cap  int
	})(p)
	elemInfo := ti.elementInfo()
	n := sliceHeader.len
	if n == 0 {
		*buf = append(*buf, '[', ']')
		return nil
	}
	if elemInfo != nil && elemInfo.kind == reflect.Struct &&
		!elemInfo.hasMarshaler && !elemInfo.hasTextMarshal &&
		!elemInfo.hasPtrMarshaler && !elemInfo.hasPtrTextMarshal &&
		!elemInfo.hasOmitEmpty {
		return encodeStructSliceAddr(sliceHeader.data, n, elemType.Size(), elemInfo, buf)
	}
	*buf = append(*buf, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		elemPtr := unsafe.Pointer(uintptr(sliceHeader.data) + uintptr(i)*uintptr(elemType.Size()))
		if err := encodeRawAddressable(elemPtr, elemInfo, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, ']')
	return nil
}

func encodeArrayAddr(p unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if ti == nil {
		return &ErrUnsupportedType{"<nil>"}
	}
	elemType := ti.typ.Elem()
	elemInfo := ti.elementInfo()
	if elemInfo != nil && elemInfo.kind == reflect.Struct &&
		!elemInfo.hasMarshaler && !elemInfo.hasTextMarshal &&
		!elemInfo.hasPtrMarshaler && !elemInfo.hasPtrTextMarshal &&
		!elemInfo.hasOmitEmpty {
		return encodeStructSliceAddr(p, ti.typ.Len(), elemType.Size(), elemInfo, buf)
	}
	*buf = append(*buf, '[')
	for i := 0; i < ti.typ.Len(); i++ {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		elemPtr := unsafe.Pointer(uintptr(p) + uintptr(i)*uintptr(elemType.Size()))
		if err := encodeRawAddressable(elemPtr, elemInfo, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, ']')
	return nil
}

// encodeStructSliceAddr is the dense record path. Elements are already
// addressable in the caller's slice/array, so it skips the generic
// encodeRawAddressable and encodeRaw dispatch layers for every element.
func encodeStructSliceAddr(data unsafe.Pointer, n int, stride uintptr, ti *typeInfo, buf *[]byte) error {
	*buf = append(*buf, '[')
	if ti.flatPrimitive {
		dst := *buf
		for i := 0; i < n; i++ {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendFlatPrimitiveStruct(dst, unsafe.Add(data, i*int(stride)), ti)
		}
		*buf = append(dst, ']')
		return nil
	}
	for i := 0; i < n; i++ {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		if err := encodeStructAddr(unsafe.Add(data, i*int(stride)), ti, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, ']')
	return nil
}

// encodeMapAddr encodes a map at p using its cached type metadata.
func encodeMapAddr(p unsafe.Pointer, ti *typeInfo, buf *[]byte) error {
	if ti == nil {
		return &ErrUnsupportedType{"<nil>"}
	}
	t := ti.typ
	if t.Key().Kind() != reflect.String {
		return &ErrUnsupportedType{Type: "map with non-string key"}
	}
	rv := reflect.NewAt(t, p).Elem()
	return encodeMapRV(rv, buf)
}

func getTypeInfoByPtr(typePtr unsafe.Pointer) *typeInfo {
	return loadTypeInfo(uintptr(typePtr))
}

func (ti *typeInfo) elementInfo() *typeInfo {
	if ti == nil || ti.typ == nil {
		return nil
	}
	if ptr := atomic.LoadPointer(&ti.elemInfo); ptr != nil {
		return (*typeInfo)(ptr)
	}
	child := getTypeInfo(ti.typ.Elem())
	if child == nil {
		return nil
	}
	if atomic.CompareAndSwapPointer(&ti.elemInfo, nil, unsafe.Pointer(child)) {
		return child
	}
	return (*typeInfo)(atomic.LoadPointer(&ti.elemInfo))
}

func (fi *fieldInfo) marshalTypeInfo() *typeInfo {
	if fi == nil || fi.typ == nil {
		return nil
	}
	if ptr := atomic.LoadPointer(&fi.childInfo); ptr != nil {
		return (*typeInfo)(ptr)
	}
	child := getTypeInfo(fi.typ)
	if child == nil {
		return nil
	}
	if atomic.CompareAndSwapPointer(&fi.childInfo, nil, unsafe.Pointer(child)) {
		return child
	}
	return (*typeInfo)(atomic.LoadPointer(&fi.childInfo))
}

// --- string quoting --------------------------------------------------------

func appendQuotedString(buf []byte, s string) []byte {
	buf = append(buf, '"')
	if len(s) >= 16 {
		src := unsafe.Slice(unsafe.StringData(s), len(s))
		start := 0
		for start < len(src) {
			event := swFindStringEventInt(src, start, len(src))
			if event == len(src) {
				buf = append(buf, src[start:]...)
				return append(buf, '"')
			}
			buf = append(buf, src[start:event]...)
			b := src[event]
			switch b {
			case '"':
				buf = append(buf, '\\', '"')
			case '\\':
				buf = append(buf, '\\', '\\')
			case '\b':
				buf = append(buf, '\\', 'b')
			case '\f':
				buf = append(buf, '\\', 'f')
			case '\n':
				buf = append(buf, '\\', 'n')
			case '\r':
				buf = append(buf, '\\', 'r')
			case '\t':
				buf = append(buf, '\\', 't')
			case 0:
				buf = append(buf, '\\', 'u', '0', '0', '0', '0')
			default:
				buf = append(buf, '\\', 'u', '0', '0', hexChar(b>>4), hexChar(b&0xf))
			}
			start = event + 1
		}
		return append(buf, '"')
	}
	i := 0
	for i < len(s) {
		b := s[i]
		if b < 0x20 || b == '"' || b == '\\' {
			break
		}
		i++
	}
	if i > 0 {
		buf = append(buf, s[:i]...)
	}
	for i < len(s) {
		b := s[i]
		switch {
		case b == '"':
			buf = append(buf, '\\', '"')
		case b == '\\':
			buf = append(buf, '\\', '\\')
		case b == '\b':
			buf = append(buf, '\\', 'b')
		case b == '\f':
			buf = append(buf, '\\', 'f')
		case b == '\n':
			buf = append(buf, '\\', 'n')
		case b == '\r':
			buf = append(buf, '\\', 'r')
		case b == '\t':
			buf = append(buf, '\\', 't')
		case b < 0x20:
			buf = append(buf, '\\', 'u', '0', '0',
				hexChar(b>>4), hexChar(b&0xf))
		default:
			buf = append(buf, b)
		}
		i++
	}
	return append(buf, '"')
}

// appendQuotedBytes is the allocation-free byte-slice counterpart used by
// the unsafe slice walker. The source slice is read only for the duration of
// this call, so its temporary string view does not escape.
func appendQuotedBytes(buf []byte, b []byte) []byte {
	return appendQuotedString(buf, *(*string)(unsafe.Pointer(&b)))
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'a' + b - 10
}

// --- reflect-driven fallbacks (kept for non-hot paths) -------------------

func encodeSliceRV(rv reflect.Value, buf *[]byte) error {
	if rv.Type().Elem().Kind() == reflect.Uint8 {
		*buf = appendQuotedBytes(*buf, rv.Bytes())
		return nil
	}
	return encodeArrayLike(rv, buf, rv.Len())
}

func encodeArrayRV(rv reflect.Value, buf *[]byte) error {
	return encodeArrayLike(rv, buf, rv.Len())
}

func encodeArrayLike(rv reflect.Value, buf *[]byte, n int) error {
	*buf = append(*buf, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		if err := encodeValue(rv.Index(i), buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, ']')
	return nil
}

func encodeMapRV(rv reflect.Value, buf *[]byte) error {
	if rv.Type().Key().Kind() != reflect.String {
		return &ErrUnsupportedType{Type: "map with non-string key"}
	}
	*buf = append(*buf, '{')
	keys := rv.MapKeys()
	sorted := make([]string, len(keys))
	for i, k := range keys {
		sorted[i] = k.String()
	}
	sort.Strings(sorted)
	first := true
	for _, ks := range sorted {
		v := rv.MapIndex(reflect.ValueOf(ks))
		if !first {
			*buf = append(*buf, ',')
		}
		first = false
		*buf = appendQuotedString(*buf, ks)
		*buf = append(*buf, ':')
		if err := encodeValue(v, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, '}')
	return nil
}

func encodeStructRV(rv reflect.Value, buf *[]byte) error {
	ti := getTypeInfo(rv.Type())
	*buf = append(*buf, '{')
	first := true
	for _, fi := range ti.fields {
		f := rv.Field(fi.fieldIdx)
		if fi.omitempty && isEmptyValueRV(f) {
			continue
		}
		if !first {
			*buf = append(*buf, ',')
		}
		first = false
		*buf = appendQuotedString(*buf, fi.name)
		*buf = append(*buf, ':')
		if err := encodeValue(f, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, '}')
	return nil
}

func isEmptyValueRV(rv reflect.Value) bool {
	switch rv.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return rv.Len() == 0
	case reflect.Bool:
		return !rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return rv.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return rv.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return rv.IsNil()
	}
	return false
}

// encodeValue is the reflect-driven entry for non-top-level calls.
// Uses cached typeInfo for Marshaler flags (common case: skip).
func encodeValue(rv reflect.Value, buf *[]byte) error {
	if !rv.IsValid() {
		*buf = append(*buf, "null"...)
		return nil
	}
	ti := loadTypeInfo(uintptr(valueTypePtr(rv)))
	if ti == nil {
		ti = getTypeInfo(rv.Type())
	}
	if ti.isRawMessage {
		return appendRawMessage(valueDataPtr(rv), buf)
	}
	if ti.hasMarshaler && rv.CanInterface() {
		return encodeMarshaler(rv, buf)
	}
	if ti.hasTextMarshal && rv.CanInterface() {
		return encodeTextMarshaler(rv, buf)
	}
	if ti.hasPtrMarshaler && rv.CanAddr() {
		addr := rv.Addr()
		if addr.CanInterface() {
			return encodeMarshaler(addr, buf)
		}
	}
	if ti.hasPtrTextMarshal && rv.CanAddr() {
		addr := rv.Addr()
		if addr.CanInterface() {
			return encodeTextMarshaler(addr, buf)
		}
	}
	if ti.kind == reflect.Interface {
		if rv.IsNil() {
			*buf = append(*buf, "null"...)
			return nil
		}
		return encodeValue(rv.Elem(), buf)
	}
	if ti.kind == reflect.Ptr && rv.IsNil() {
		*buf = append(*buf, "null"...)
		return nil
	}
	return encodeRaw(valueDataPtr(rv), ti, buf)
}

// appendRawMessage emits a RawMessage directly from its slice header. It is
// the native zero-allocation marshal path when dst has enough capacity.
func appendRawMessage(dataPtr unsafe.Pointer, buf *[]byte) error {
	m := *(*RawMessage)(dataPtr)
	if m == nil {
		*buf = append(*buf, "null"...)
		return nil
	}
	*buf = append(*buf, m...)
	return nil
}

// --- Marshaler interfaces --------------------------------------------------

var (
	marshalerType     = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encodingTextMarshaler)(nil)).Elem()
)

type encodingTextMarshaler interface {
	MarshalText() ([]byte, error)
}

func encodeMarshaler(rv reflect.Value, buf *[]byte) error {
	m := rv.Interface().(json.Marshaler)
	b, err := m.MarshalJSON()
	if err != nil {
		return err
	}
	*buf = append(*buf, b...)
	return nil
}

func encodeTextMarshaler(rv reflect.Value, buf *[]byte) error {
	m := rv.Interface().(encodingTextMarshaler)
	b, err := m.MarshalText()
	if err != nil {
		return err
	}
	*buf = appendQuotedString(*buf, string(b))
	return nil
}
