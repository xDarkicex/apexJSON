package apexjson

import (
	"bytes"
	"encoding"
	"fmt"
	"math/bits"
	"os"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"unsafe"
)

var stringAnyMapType = reflect.TypeOf(map[string]any(nil))
var debugUnescape = os.Getenv("APEXJSON_DEBUG") != ""

// Decoder reads a JSON document and populates Go values.
//
// Unmarshal parses the JSON-encoded data and stores the result in the
// value pointed to by v. v must be a non-nil pointer.
//
// Performance characteristics:
//   - First call per type uses reflection once to build typeInfo, which
//     is published into an atomic read-mostly cache keyed by runtime type.
//   - Subsequent calls walk the cached typeInfo without touching
//     reflection. Hot-path field writes use unsafe.Pointer offsets to
//     avoid reflect.Value.Set overhead.
//
// json.Unmarshaler and encoding.TextUnmarshaler are honored before the
// direct destination-type paths.

// Unmarshal parses JSON-encoded data and stores the result in the value
// pointed to by v.
//
// Unmarshal checks out a pooled Decoder, parses, decodes, and returns
// the Decoder to the pool. The pool keeps the slab and off-heap arena
// alive across calls so the hot path is zero-alloc after the first
// warm-up.
func Unmarshal(data []byte, v any) error {
	if v == nil {
		return &SyntaxError{Msg: "Unmarshal called with nil target"}
	}
	// The dynamic destinations are handled before any reflection: the tape
	// is built once and the value is materialized natively, so the only
	// reflect work left is the destination's own type.
	switch dst := v.(type) {
	case *map[string]any:
		return unmarshalStringAnyMap(data, dst)
	case *any:
		return unmarshalAnyValue(data, dst)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return &ErrTypeMismatch{SrcKind: "value", DstType: fmt.Sprintf("%T", v)}
	}
	if rv.Elem().Type() == rawMessageType {
		return unmarshalRawMessage(data, (*RawMessage)(unsafe.Pointer(rv.Elem().UnsafeAddr())))
	}
	if directTypeSupported(rv.Elem().Type(), 0) {
		return unmarshalDirect(data, rv.Elem())
	}
	dec, acquireErr := acquireUnmarshalDecoder(len(data))
	if acquireErr != nil {
		return acquireErr
	}
	if err := dec.Parse(data); err != nil {
		decoderPool.Put(dec)
		return err
	}
	root := dec.Root()
	err := decodeValue(&dec.doc, root.idx, dec.src, rv.Elem())
	decoderPool.Put(dec)
	return err
}

// unmarshalStringAnyMap decodes into a map[string]any without reflect.
//
// The tape is built by the parser, which has no reflect in it, and the map is
// materialized by decodeAnyMapReuse, which walks the tape directly. The
// destination is reached through the concrete pointer, so nothing here needs
// a reflect.Value.
func unmarshalStringAnyMap(data []byte, dst *map[string]any) error {
	dec, err := acquireUnmarshalDecoder(len(data))
	if err != nil {
		return err
	}
	if err := dec.Parse(data); err != nil {
		decoderPool.Put(dec)
		return err
	}
	root := dec.Root()
	n := dec.doc.slab.view(root.idx)
	if n.Kind == nodeNull {
		decoderPool.Put(dec)
		*dst = nil
		return nil
	}
	if n.Kind != nodeObject {
		decoderPool.Put(dec)
		return &ErrTypeMismatch{SrcKind: n.Kind.String(), DstType: "map[string]interface {}"}
	}
	mp := *dst
	if mp == nil {
		// The AST stores object children as key/value pairs. Reserve the
		// exact pair count so the map does not repeatedly grow while it is
		// being filled.
		mp = make(map[string]any, countObjectPairs(&dec.doc, root.idx))
	}
	mp, err = decodeAnyMapReuse(&dec.doc, root.idx, dec.src, mp)
	decoderPool.Put(dec)
	if err != nil {
		return err
	}
	*dst = mp
	return nil
}

// unmarshalAnyValue decodes into an interface destination without reflect.
func unmarshalAnyValue(data []byte, dst *any) error {
	dec, err := acquireUnmarshalDecoder(len(data))
	if err != nil {
		return err
	}
	if err := dec.Parse(data); err != nil {
		decoderPool.Put(dec)
		return err
	}
	root := dec.Root()
	value, err := decodeAnyNodeReuse(&dec.doc, root.idx, dec.src, nil)
	decoderPool.Put(dec)
	if err != nil {
		return err
	}
	*dst = value
	return nil
}

// unmarshalRawMessage validates one complete JSON document with the
// allocation-free scanner and then borrows the caller's bytes. This avoids
// building a tape for a destination that only needs the raw span.
func unmarshalRawMessage(data []byte, dst *RawMessage) error {
	valueStart := 0
	for valueStart < len(data) && isWS(data[valueStart]) {
		valueStart++
	}
	var d directDecoder
	d.src = data
	d.scan.reset(data)
	tok, err := d.nextValueToken()
	if err != nil {
		return err
	}
	if err := d.skipFromToken(tok, 0); err != nil {
		return err
	}
	valueEnd := d.scan.pos
	tail, more := d.scan.next()
	if more || tail.Kind == kindError {
		return directSyntaxError(tail, "unexpected "+tail.Kind.String())
	}
	*dst = RawMessage(data[valueStart:valueEnd])
	return nil
}

// decoderPool keeps parsedDoc/arena state alive across Unmarshal calls.
// The pooled Decoder owns the mmap-backed AST arena, so Parse and Unmarshal
// can reuse parser storage without putting Go-managed result values in the
// off-heap region.
var decoderPool = sync.Pool{
	New: func() any {
		dec, err := NewDecoder()
		if err != nil {
			panic("apexjson: NewDecoder failed: " + err.Error())
		}
		return dec
	},
}

// acquireUnmarshalDecoder returns a pooled decoder with enough off-heap tape
// capacity for this input. Public Unmarshal has no caller-supplied nodeCap,
// so it must not inherit NewDecoder's small default and panic on large dynamic
// documents. The byte-count bound also keeps malformed deeply nested input
// as a normal syntax error rather than an arena-capacity panic.
func acquireUnmarshalDecoder(inputLen int) (*Decoder, error) {
	dec := decoderPool.Get().(*Decoder)
	required := inputLen + 16
	if required <= dec.nodeCap {
		return dec, nil
	}
	dec.Free()
	arenaSize := uint64(required)*uint64(nodeSize) + 4*64
	return NewDecoderSize(arenaSize, required)
}

// decodeValue dispatches on the Go destination type and recursively
// decodes the value at the given slab node.
//
// rootIdx is the direct index of the value in the AST. Passing the index
// through recursion avoids rescanning the slab to rediscover containers.
func decodeValue(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	n := doc.slab.view(rootIdx)
	if rv.CanAddr() {
		ptrType := rv.Addr().Type()
		if ptrType.Implements(textUnmarshalerType) {
			return decodeTextUnmarshaler(n.Kind, n.Start, n.End, src, rv)
		}
		if ptrType.Implements(unmarshalerType) {
			return decodeJSONUnmarshaler(n.Kind, n.Start, n.End, src, rv)
		}
	}
	switch rv.Kind() {
	case reflect.Interface:
		return decodeIntoAny(doc, rootIdx, src, rv)
	case reflect.Ptr:
		return decodeIntoPtr(doc, rootIdx, src, rv)
	case reflect.String:
		return decodeIntoString(n.Kind, n.Start, n.End, n.Flags, src, rv)
	case reflect.Bool:
		return decodeIntoBool(n.Kind, src, rv)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return decodeIntoInt(n.Kind, n.Start, n.End, src, rv)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return decodeIntoUint(n.Kind, n.Start, n.End, src, rv)
	case reflect.Float32, reflect.Float64:
		return decodeIntoFloat(n.Kind, n.Start, n.End, src, rv)
	case reflect.Slice:
		return decodeIntoSlice(doc, rootIdx, src, rv)
	case reflect.Array:
		return decodeIntoArray(doc, rootIdx, src, rv)
	case reflect.Map:
		return decodeIntoMap(doc, rootIdx, src, rv)
	case reflect.Struct:
		return decodeIntoStruct(doc, rootIdx, src, rv)
	}
	return &ErrUnsupportedType{Type: rv.Type().String()}
}

// --- scalar decoders -----------------------------------------------------

func decodeIntoString(kind nodeKind, start, end uint32, flags tokenFlags, src []byte, rv reflect.Value) error {
	if kind != nodeString {
		return &ErrTypeMismatch{SrcKind: kind.String(), DstType: "string"}
	}
	raw := src[start:end]
	if stringNeedsUnescapeFlags(flags, raw) {
		// Has escapes or invalid UTF-8: allocate and unescape.
		rv.SetString(bytesToString(unescapeJSONString(raw)))
		return nil
	}
	// No escapes, valid UTF-8: alias src directly to avoid the copy.
	// Safe because src is owned by the caller and outlives this
	// Unmarshal call. The user must not mutate src while the
	// decoded value is in use — same constraint as
	// json.RawMessage.
	rv.SetString(*(*string)(unsafe.Pointer(&raw)))
	return nil
}

func decodeIntoBool(kind nodeKind, src []byte, rv reflect.Value) error {
	switch kind {
	case nodeTrue:
		rv.SetBool(true)
		return nil
	case nodeFalse:
		rv.SetBool(false)
		return nil
	}
	return &ErrTypeMismatch{SrcKind: kind.String(), DstType: rv.Type().String()}
}

func decodeIntoInt(kind nodeKind, start, end uint32, src []byte, rv reflect.Value) error {
	if kind != nodeNumber {
		return &ErrTypeMismatch{SrcKind: kind.String(), DstType: rv.Type().String()}
	}
	n, err := parseIntBytes(src[start:end], intBits(rv.Kind()))
	if err != nil {
		return err
	}
	rv.SetInt(n)
	return nil
}

func decodeIntoUint(kind nodeKind, start, end uint32, src []byte, rv reflect.Value) error {
	if kind != nodeNumber {
		return &ErrTypeMismatch{SrcKind: kind.String(), DstType: rv.Type().String()}
	}
	n, err := parseUintBytes(src[start:end], uintBits(rv.Kind()))
	if err != nil {
		return err
	}
	rv.SetUint(n)
	return nil
}

func decodeIntoFloat(kind nodeKind, start, end uint32, src []byte, rv reflect.Value) error {
	if kind != nodeNumber {
		return &ErrTypeMismatch{SrcKind: kind.String(), DstType: rv.Type().String()}
	}
	bits := 64
	if rv.Kind() == reflect.Float32 {
		bits = 32
	}
	f, err := parseFloatBytes(src[start:end], bits)
	if err != nil {
		return err
	}
	rv.SetFloat(f)
	return nil
}

// --- interface / ptr / slice / map / struct -------------------------------

func decodeIntoAny(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	n := doc.slab.view(rootIdx)
	switch n.Kind {
	case nodeString:
		raw := src[n.Start:n.End]
		if stringNeedsUnescapeFlags(n.Flags, raw) {
			rv.Set(reflect.ValueOf(bytesToString(unescapeJSONString(raw))))
		} else {
			rv.Set(reflect.ValueOf(*(*string)(unsafe.Pointer(&raw))))
		}
	case nodeNumber:
		// Match encoding/json: numbers unmarshal into any as float64.
		f, err := parseFloatBytes(src[n.Start:n.End], 64)
		if err != nil {
			return err
		}
		rv.Set(reflect.ValueOf(f))
	case nodeTrue:
		rv.Set(reflect.ValueOf(true))
	case nodeFalse:
		rv.Set(reflect.ValueOf(false))
	case nodeNull:
		rv.Set(reflect.Zero(rv.Type()))
	case nodeObject:
		return decodeIntoInterfaceMap(doc, rootIdx, src, rv)
	case nodeArray:
		return decodeIntoInterfaceSlice(doc, rootIdx, src, rv)
	}
	return nil
}

// decodeIntoInterfaceMap materializes an object into a map[string]any.
func decodeIntoInterfaceMap(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	var mp map[string]any
	if !rv.IsNil() {
		mp, _ = rv.Interface().(map[string]any)
	}
	if mp == nil {
		mp = make(map[string]any, countObjectPairs(doc, rootIdx))
	}
	if _, err := decodeAnyMapReuse(doc, rootIdx, src, mp); err != nil {
		return err
	}
	rv.Set(reflect.ValueOf(mp))
	return nil
}

// decodeIntoInterfaceSlice materializes an array into a []any.
//
// Zero per-element allocations (except for string elements, where the
// string header can't fit in the interface{} data word and must be heap-)
// allocated by the runtime).
func decodeIntoInterfaceSlice(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	var old []any
	if !rv.IsNil() {
		old, _ = rv.Interface().([]any)
	}
	sl, err := decodeAnySliceReuse(doc, rootIdx, src, old)
	if err != nil {
		return err
	}
	rv.Set(reflect.ValueOf(sl))
	return nil
}

// decodeAnyNode materializes one interface{} value without creating a
// temporary reflect.Value for nested containers. The returned maps/slices
// remain Go-managed because they escape to the caller; only parser metadata
// stays in the off-heap memory.Arena.
func decodeAnyNode(doc *parsedDoc, idx int32, src []byte) (any, error) {
	return decodeAnyNodeReuse(doc, idx, src, nil)
}

func decodeAnyNodeReuse(doc *parsedDoc, idx int32, src []byte, old any) (any, error) {
	n := doc.slab.view(idx)
	switch n.Kind {
	case nodeString:
		raw := src[n.Start:n.End]
		return decodeJSONStringValueFlags(raw, n.Flags), nil
	case nodeNumber:
		return parseFloatBytes(src[n.Start:n.End], 64)
	case nodeTrue:
		return true, nil
	case nodeFalse:
		return false, nil
	case nodeNull:
		return nil, nil
	case nodeObject:
		mp, _ := old.(map[string]any)
		if mp == nil {
			mp = make(map[string]any, countObjectPairs(doc, idx))
		}
		return decodeAnyMapReuse(doc, idx, src, mp)
	case nodeArray:
		sl, _ := old.([]any)
		return decodeAnySliceReuse(doc, idx, src, sl)
	}
	return nil, &ErrUnsupportedType{Type: n.Kind.String()}
}

func decodeAnyMapReuse(doc *parsedDoc, rootIdx int32, src []byte, mp map[string]any) (map[string]any, error) {
	for c := firstChild(&doc.slab, rootIdx); c != noNode; {
		keyNode := doc.slab.view(c)
		key := decodeJSONStringValueFlags(src[keyNode.Start:keyNode.End], keyNode.Flags)
		valueIdx := c + 1
		value, err := decodeAnyNodeReuse(doc, valueIdx, src, mp[key])
		if err != nil {
			return nil, err
		}
		mp[key] = value
		c = nextChild(&doc.slab, rootIdx, valueIdx)
	}
	return mp, nil
}

func decodeAnySliceReuse(doc *parsedDoc, rootIdx int32, src []byte, out []any) ([]any, error) {
	oldOut := out
	oldLen := len(oldOut)
	if cap(out) == 0 {
		// The first materialization still reserves an exact-sized slice. All
		// steady-state calls reuse that capacity and avoid counting the tape
		// children in a separate pass.
		n := countChildren(doc, rootIdx)
		out = make([]any, n)
	} else {
		// Expose the full reusable backing array while filling it. The final
		// length is trimmed after the one child walk completes.
		out = out[:cap(out)]
	}
	if len(out) == 0 {
		if out == nil {
			out = make([]any, 0)
		}
		return out, nil
	}
	i := 0
	for c := firstChild(&doc.slab, rootIdx); c != noNode; i, c = i+1, nextChild(&doc.slab, rootIdx, c) {
		if i >= len(out) {
			out = append(out, nil)
		}
		var old any
		if i < oldLen {
			old = oldOut[i]
		}
		value, err := decodeAnyNodeReuse(doc, c, src, old)
		if err != nil {
			return nil, err
		}
		out[i] = value
	}
	if i < len(out) {
		clear(out[i:])
	}
	out = out[:i]
	return out, nil
}

func decodeIntoPtr(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	n := doc.slab.view(rootIdx)
	if n.Kind == nodeNull {
		rv.Set(reflect.Zero(rv.Type()))
		return nil
	}
	if rv.IsNil() {
		rv.Set(reflect.New(rv.Type().Elem()))
	}
	return decodeValue(doc, rootIdx, src, rv.Elem())
}

func decodeIntoSlice(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	n := doc.slab.view(rootIdx)
	if n.Kind == nodeNull {
		rv.Set(reflect.Zero(rv.Type()))
		return nil
	}
	if n.Kind != nodeArray {
		return &ErrTypeMismatch{SrcKind: n.Kind.String(), DstType: rv.Type().String()}
	}
	if firstChild(&doc.slab, rootIdx) == noNode {
		// Match encoding/json: empty array → non-nil empty slice.
		if rv.IsNil() {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
		} else {
			rv.SetLen(0)
		}
		return nil
	}
	elemType := rv.Type().Elem()

	// Fast path for []string: zero-alloc element copies via unsafe
	// string aliasing into src.
	if elemType.Kind() == reflect.String {
		return decodeIntoStringSlice(doc, rootIdx, src, rv)
	}

	count := countChildren(doc, rootIdx)
	if rv.IsNil() || rv.Cap() < count {
		rv.Set(reflect.MakeSlice(rv.Type(), count, count))
	} else {
		sh := (*decodeSliceHeader)(unsafe.Pointer(rv.UnsafeAddr()))
		oldLen := sh.len
		if oldLen > count {
			// Clear truncated elements so a reused backing array does not
			// keep old pointer-bearing values alive.
			zero := reflect.Zero(elemType)
			for i := count; i < oldLen; i++ {
				rv.Index(i).Set(zero)
			}
		}
		sh.len = count
	}
	if elemType.Kind() == reflect.Struct {
		ti := getTypeInfo(elemType)
		if !typeInfoHasCustomUnmarshal(ti) {
			return decodeIntoStructSlice(doc, rootIdx, src, rv, ti)
		}
	}
	i := 0
	for c := firstChild(&doc.slab, rootIdx); c != noNode; c = nextChild(&doc.slab, rootIdx, c) {
		if err := decodeValue(doc, c, src, rv.Index(i)); err != nil {
			return err
		}
		i++
	}
	return nil
}

func decodeIntoStructSlice(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value, ti *typeInfo) error {
	sh := (*decodeSliceHeader)(unsafe.Pointer(rv.UnsafeAddr()))
	base := sh.data
	elemSize := ti.typ.Size()
	i := 0
	for c := firstChild(&doc.slab, rootIdx); c != noNode; c = nextChild(&doc.slab, rootIdx, c) {
		fieldBase := unsafe.Add(base, uintptr(i)*elemSize)
		if err := decodeIntoStructAt(doc, c, src, fieldBase, ti); err != nil {
			return err
		}
		i++
	}
	return nil
}

func decodeIntoArray(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	root := doc.slab.view(rootIdx)
	if root.Kind != nodeArray {
		return &ErrTypeMismatch{SrcKind: root.Kind.String(), DstType: rv.Type().String()}
	}
	i := 0
	for c := firstChild(&doc.slab, rootIdx); c != noNode; c = nextChild(&doc.slab, rootIdx, c) {
		if i < rv.Len() {
			if err := decodeValue(doc, c, src, rv.Index(i)); err != nil {
				return err
			}
		}
		i++
	}
	return nil
}

// decodeIntoStringSlice is a zero-allocation fast path for []string.
// Each element string aliases the source byte slice via unsafe.String.
// One allocation total: the backing slice.
func decodeIntoStringSlice(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	n := countChildren(doc, rootIdx)
	// Directly reuse an existing []string backing array. The fast path is
	// deliberately specialized: this avoids both reflect.Value.Slice and
	// reflect.Value.Set on steady-state unmarshals.
	var base unsafe.Pointer
	if rv.IsNil() || rv.Cap() < n {
		sl := reflect.MakeSlice(rv.Type(), n, n)
		base = sl.UnsafePointer()
		rv.Set(sl)
	} else {
		sh := (*decodeSliceHeader)(unsafe.Pointer(rv.UnsafeAddr()))
		oldLen := sh.len
		if oldLen > n {
			clear(unsafe.Slice((*string)(sh.data), oldLen)[n:oldLen])
		}
		sh.len = n
		base = sh.data
	}
	strSize := unsafe.Sizeof("")
	i := 0
	for c := firstChild(&doc.slab, rootIdx); c != noNode; c = nextChild(&doc.slab, rootIdx, c) {
		vn := doc.slab.view(c)
		if vn.Kind != nodeString {
			return &ErrTypeMismatch{SrcKind: vn.Kind.String(), DstType: rv.Type().String()}
		}
		raw := src[vn.Start:vn.End]
		var s string
		if stringNeedsUnescapeFlags(vn.Flags, raw) {
			s = bytesToString(unescapeJSONString(raw))
		} else {
			s = *(*string)(unsafe.Pointer(&raw))
		}
		*(*string)(unsafe.Pointer(uintptr(base) + uintptr(i)*strSize)) = s
		i++
	}
	return nil
}

type decodeSliceHeader struct {
	data unsafe.Pointer
	len  int
	cap  int
}

func decodeIntoMap(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	n := doc.slab.view(rootIdx)
	if n.Kind == nodeNull {
		rv.Set(reflect.Zero(rv.Type()))
		return nil
	}
	if n.Kind != nodeObject {
		return &ErrTypeMismatch{SrcKind: n.Kind.String(), DstType: rv.Type().String()}
	}
	if rv.Type() == stringAnyMapType {
		return decodeIntoStringAnyMap(doc, rootIdx, src, rv)
	}
	if rv.IsNil() {
		// The AST stores object children as key/value pairs. Reserve the
		// exact pair count so the destination map does not repeatedly grow
		// while materializing medium and large documents.
		rv.Set(reflect.MakeMapWithSize(rv.Type(), countObjectPairs(doc, rootIdx)))
	}
	keyType := rv.Type().Key()
	elemType := rv.Type().Elem()
	for c := firstChild(&doc.slab, rootIdx); c != noNode; {
		keyNode := doc.slab.view(c)
		valIdx := c + 1
		key := reflect.New(keyType).Elem()
		// Apply UTF-8 normalization for object keys (matches stdlib).
		unescaped := unescapeJSONStringFlags(keyNode.Flags, src[keyNode.Start:keyNode.End])
		switch key.Kind() {
		case reflect.String:
			key.SetString(bytesToString(unescaped))
		default:
			if err := decodeIntoString(nodeString, keyNode.Start, keyNode.End, keyNode.Flags, src, key); err != nil {
				return err
			}
		}
		elem := reflect.New(elemType).Elem()
		if err := decodeValue(doc, valIdx, src, elem); err != nil {
			return err
		}
		rv.SetMapIndex(key, elem)
		c = nextChild(&doc.slab, rootIdx, valIdx)
	}
	return nil
}

func decodeIntoStringAnyMap(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	var mp map[string]any
	if !rv.IsNil() {
		mp, _ = rv.Interface().(map[string]any)
	}
	if mp == nil {
		mp = make(map[string]any, countObjectPairs(doc, rootIdx))
	}
	if _, err := decodeAnyMapReuse(doc, rootIdx, src, mp); err != nil {
		return err
	}
	if rv.IsNil() {
		rv.Set(reflect.ValueOf(mp))
	}
	return nil
}

// decodeJSONStringValue returns a string without copying valid, unescaped
// input. For escaped input, unescapeJSONString owns the newly allocated byte
// buffer, so converting its header directly avoids a second copy. This package
// intentionally exposes the same source-lifetime contract as decodeIntoString:
// callers must keep src alive while decoded values are in use.
func decodeJSONStringValue(raw []byte) string {
	return bytesToString(unescapeJSONString(raw))
}

func decodeJSONStringValueFlags(raw []byte, flags tokenFlags) string {
	return bytesToString(unescapeJSONStringFlags(flags, raw))
}

func unescapeJSONStringFlags(flags tokenFlags, raw []byte) []byte {
	if !stringNeedsUnescapeFlags(flags, raw) {
		return raw
	}
	return unescapeJSONString(raw)
}

func stringNeedsUnescapeFlags(flags tokenFlags, raw []byte) bool {
	if flags&tokenStringEscaped != 0 {
		return true
	}
	if flags&tokenStringASCII != 0 {
		return false
	}
	return jsonStringNeedsUnescape(raw)
}

func bytesToString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

// decodeIntoStruct uses cached typeInfo. First call per type builds
// typeInfo via reflection; subsequent calls use the cache.
//
// Zero-allocation path: iterates AST children once and byte-compares
// each key against the cached field name bytes. No key string
// allocations, no key→child map.
func decodeIntoStruct(doc *parsedDoc, rootIdx int32, src []byte, rv reflect.Value) error {
	ti := getTypeInfo(rv.Type())
	return decodeIntoStructAt(doc, rootIdx, src, unsafe.Pointer(rv.UnsafeAddr()), ti)
}

func decodeIntoStructAt(doc *parsedDoc, rootIdx int32, src []byte, base unsafe.Pointer, ti *typeInfo) error {
	n := doc.slab.view(rootIdx)
	if n.Kind == nodeNull {
		return nil
	}
	if n.Kind != nodeObject {
		return &ErrTypeMismatch{SrcKind: n.Kind.String(), DstType: ti.typ.String()}
	}

	for c := firstChild(&doc.slab, rootIdx); c != noNode; {
		keyNode := doc.slab.view(c)
		keyBytes := src[keyNode.Start:keyNode.End]
		valIdx := c + 1

		if fi, ok := fieldForKey(ti, keyBytes); ok {
			if err := decodeStructFieldAt(doc, valIdx, src, base, *fi); err != nil {
				return err
			}
		}
		c = nextChild(&doc.slab, rootIdx, valIdx)
	}
	return nil
}

// decodeStructFieldAt is the hot path for ordinary exported struct fields.
// Type metadata already contains the field offset and exact Go kind, so
// rebuilding reflect.Values for every field would only add dispatch overhead.
// Complex fields and custom unmarshaler types fall back to decodeValue to
// preserve the complete reflection/API semantics.
func decodeStructFieldAt(doc *parsedDoc, rootIdx int32, src []byte, base unsafe.Pointer, fi fieldInfo) error {
	fieldPtr := unsafe.Add(base, fi.offset)
	if fi.hasCustomUnmarshaler {
		field := reflect.NewAt(fi.typ, fieldPtr).Elem()
		return decodeValue(doc, rootIdx, src, field)
	}
	switch fi.kind {
	case fkBool, fkInt, fkUint, fkFloat, fkString:
		// Continue through the direct scalar stores below.
	default:
		field := reflect.NewAt(fi.typ, fieldPtr).Elem()
		return decodeValue(doc, rootIdx, src, field)
	}
	n := doc.slab.view(rootIdx)
	switch fi.kind {
	case fkBool:
		switch n.Kind {
		case nodeTrue:
			*(*bool)(fieldPtr) = true
			return nil
		case nodeFalse:
			*(*bool)(fieldPtr) = false
			return nil
		}
	case fkInt:
		if n.Kind == nodeNumber {
			value, err := parseIntBytes(src[n.Start:n.End], intBits(fi.goKind))
			if err == nil {
				storeIntAt(fieldPtr, fi.goKind, value)
			}
			return err
		}
	case fkUint:
		if n.Kind == nodeNumber {
			value, err := parseUintBytes(src[n.Start:n.End], uintBits(fi.goKind))
			if err == nil {
				storeUintAt(fieldPtr, fi.goKind, value)
			}
			return err
		}
	case fkFloat:
		if n.Kind == nodeNumber {
			bits := 64
			if fi.goKind == reflect.Float32 {
				bits = 32
			}
			value, err := parseFloatBytes(src[n.Start:n.End], bits)
			if err == nil {
				if fi.goKind == reflect.Float32 {
					*(*float32)(fieldPtr) = float32(value)
				} else {
					*(*float64)(fieldPtr) = value
				}
			}
			return err
		}
	case fkString:
		if n.Kind == nodeString {
			raw := src[n.Start:n.End]
			if stringNeedsUnescapeFlags(n.Flags, raw) {
				*(*string)(fieldPtr) = bytesToString(unescapeJSONString(raw))
			} else {
				*(*string)(fieldPtr) = *(*string)(unsafe.Pointer(&raw))
			}
			return nil
		}
	}
	return &ErrTypeMismatch{SrcKind: n.Kind.String(), DstType: fi.typ.String()}
}

func storeIntAt(p unsafe.Pointer, kind reflect.Kind, value int64) {
	switch kind {
	case reflect.Int:
		*(*int)(p) = int(value)
	case reflect.Int8:
		*(*int8)(p) = int8(value)
	case reflect.Int16:
		*(*int16)(p) = int16(value)
	case reflect.Int32:
		*(*int32)(p) = int32(value)
	case reflect.Int64:
		*(*int64)(p) = value
	}
}

func storeUintAt(p unsafe.Pointer, kind reflect.Kind, value uint64) {
	switch kind {
	case reflect.Uint:
		*(*uint)(p) = uint(value)
	case reflect.Uint8:
		*(*uint8)(p) = uint8(value)
	case reflect.Uint16:
		*(*uint16)(p) = uint16(value)
	case reflect.Uint32:
		*(*uint32)(p) = uint32(value)
	case reflect.Uint64:
		*(*uint64)(p) = value
	case reflect.Uintptr:
		*(*uintptr)(p) = uintptr(value)
	}
}

// bytesEqual compares two byte slices without allocation. Replaces
// strings.EqualFold / bytes.Equal where we know the input is exact-match
// (no escape sequences in JSON keys).
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- helpers --------------------------------------------------------------

func firstChild(nodes *slab, parent int32) int32 {
	child := parent + 1
	if child >= nodes.skip(parent) {
		return noNode
	}
	return child
}

func nextChild(nodes *slab, parent, child int32) int32 {
	next := nodes.skip(child)
	if next >= nodes.skip(parent) {
		return noNode
	}
	return next
}

func countChildren(doc *parsedDoc, parent int32) int {
	n := 0
	for c := firstChild(&doc.slab, parent); c != noNode; c = nextChild(&doc.slab, parent, c) {
		n++
	}
	return n
}

func countObjectPairs(doc *parsedDoc, parent int32) int {
	pairs := 0
	for c := firstChild(&doc.slab, parent); c != noNode; {
		pairs++
		value := c + 1
		c = nextChild(&doc.slab, parent, value)
	}
	return pairs
}

// --- typeInfo cache -------------------------------------------------------

// typeInfoTable is immutable after publication. New type metadata is
// published by replacing the table pointer, so the hot path is one atomic
// load and one ordinary map lookup.
type typeInfoTable struct {
	byPtr map[uintptr]*typeInfo
}

var typeInfoCache atomic.Pointer[typeInfoTable]

func init() {
	typeInfoCache.Store(&typeInfoTable{byPtr: make(map[uintptr]*typeInfo)})
}

type typeInfo struct {
	typ                   reflect.Type
	typePtr               unsafe.Pointer
	kind                  reflect.Kind
	isRawMessage          bool
	fields                []fieldInfo
	hasOmitEmpty          bool
	flatPrimitive         bool // plain scalar/string struct marshal fast path
	hasMarshaler          bool // implements json.Marshaler
	hasTextMarshal        bool // implements encoding.TextMarshaler
	hasPtrMarshaler       bool // *T implements json.Marshaler
	hasPtrTextMarshal     bool // *T implements encoding.TextMarshaler
	hasUnmarshaler        bool // implements json.Unmarshaler
	hasTextUnmarshaler    bool // implements encoding.TextUnmarshaler
	hasPtrUnmarshaler     bool // *T implements json.Unmarshaler
	hasPtrTextUnmarshaler bool // *T implements encoding.TextUnmarshaler
	sizeHint              int  // cached capacity estimate, when it depends only on the type
	hintFromType          bool // sizeHint is valid
	directSupport         atomic.Uint32
	fieldByFirst          [256]int32
	fieldBitmap           []uint16
	fieldLengthMask       []uint16
	elemInfo              unsafe.Pointer // atomically cached typeInfo for ptr/slice/array/map elements
}

type fieldInfo struct {
	name                    string // canonical JSON key (for diagnostics)
	nameBytes               []byte // cached for hot path byte comparison
	marshalPrefix           []byte // cached JSON key prefix: `"name":`
	marshalPrefixComma      []byte // cached JSON key prefix with separator: `,"name":`
	marshalPrefixWords      [2]uint64
	marshalPrefixCommaWords [2]uint64
	fieldIdx                int     // reflect.Value.Field index
	offset                  uintptr // offset from struct base (for unsafe direct writes)
	kind                    fieldKind
	goKind                  reflect.Kind // exact Go kind; fieldKind groups kinds for decoding
	typ                     reflect.Type
	elemType                reflect.Type // for ptr/slice/map/struct fields
	elemSize                uintptr      // cached type size for unsafe arithmetic
	omitempty               bool
	hasCustomUnmarshaler    bool
	hasMarshaler            bool
	hasTextMarshal          bool
	hasPtrMarshaler         bool
	hasPtrTextMarshal       bool
	marshalOp               marshalOp
	childInfo               unsafe.Pointer // atomically cached typeInfo for marshal branches
}

// marshalOp is compiled once into typeInfo. Keeping the operation beside the
// field offset removes the second kind/custom-method dispatch from the
// per-record marshal loop.
type marshalOp uint8

const (
	marshalOpInvalid marshalOp = iota
	marshalOpBool
	marshalOpInt
	marshalOpInt8
	marshalOpInt16
	marshalOpInt32
	marshalOpInt64
	marshalOpUint
	marshalOpUint8
	marshalOpUint16
	marshalOpUint32
	marshalOpUint64
	marshalOpUintptr
	marshalOpFloat32
	marshalOpFloat64
	marshalOpString
	marshalOpStruct
	marshalOpPtr
	marshalOpSlice
	marshalOpArray
	marshalOpMap
	marshalOpInterface
	marshalOpCustom
)

type fieldKind uint8

const (
	fkInvalid fieldKind = iota
	fkBool
	fkInt
	fkUint
	fkFloat
	fkString
	fkSlice
	fkMap
	fkStruct
	fkPtr
	fkInterface
	fkArray
)

func (k fieldKind) String() string {
	switch k {
	case fkBool:
		return "bool"
	case fkInt:
		return "int"
	case fkUint:
		return "uint"
	case fkFloat:
		return "float"
	case fkString:
		return "string"
	case fkSlice:
		return "slice"
	case fkMap:
		return "map"
	case fkStruct:
		return "struct"
	case fkPtr:
		return "ptr"
	case fkInterface:
		return "interface"
	case fkArray:
		return "array"
	}
	return "invalid"
}

func getTypeInfo(t reflect.Type) *typeInfo {
	if t == nil {
		return nil
	}
	key := uintptr(typePtrFromReflectType(t))
	if cached := loadTypeInfo(key); cached != nil {
		return cached
	}
	ti := buildTypeInfo(t)
	return publishTypeInfo(key, ti)
}

func typeInfoHasCustomUnmarshal(ti *typeInfo) bool {
	return ti.hasUnmarshaler || ti.hasTextUnmarshaler ||
		ti.hasPtrUnmarshaler || ti.hasPtrTextUnmarshaler
}

func loadTypeInfo(key uintptr) *typeInfo {
	table := typeInfoCache.Load()
	if table == nil {
		return nil
	}
	return table.byPtr[key]
}

// publishTypeInfo uses copy-on-write publication. The map held by a table is
// never mutated after Store, so readers never take a lock and never observe a
// partially-built entry. Duplicate construction under a type-discovery race
// is harmless; only one pointer is retained by the published table.
func publishTypeInfo(key uintptr, ti *typeInfo) *typeInfo {
	for {
		old := typeInfoCache.Load()
		if old != nil {
			if existing := old.byPtr[key]; existing != nil {
				return existing
			}
		}

		entries := make(map[uintptr]*typeInfo, 1)
		if old != nil {
			entries = make(map[uintptr]*typeInfo, len(old.byPtr)+1)
			for k, v := range old.byPtr {
				entries[k] = v
			}
		}
		entries[key] = ti
		next := &typeInfoTable{byPtr: entries}
		if typeInfoCache.CompareAndSwap(old, next) {
			return ti
		}
	}
}

func buildTypeInfo(t reflect.Type) *typeInfo {
	if t == nil {
		return nil
	}
	ti := &typeInfo{
		typ:                t,
		typePtr:            typePtrFromReflectType(t),
		kind:               t.Kind(),
		isRawMessage:       t == rawMessageType,
		hasMarshaler:       t.Implements(marshalerType),
		hasTextMarshal:     t.Implements(textMarshalerType),
		hasUnmarshaler:     t.Implements(unmarshalerType),
		hasTextUnmarshaler: t.Implements(textUnmarshalerType),
	}
	if t.Kind() != reflect.Ptr {
		pt := reflect.PointerTo(t)
		ti.hasPtrMarshaler = pt.Implements(marshalerType)
		ti.hasPtrTextMarshal = pt.Implements(textMarshalerType)
		ti.hasPtrUnmarshaler = pt.Implements(unmarshalerType)
		ti.hasPtrTextUnmarshaler = pt.Implements(textUnmarshalerType)
	}
	// A struct's capacity estimate depends only on its type, so it is
	// computed once here rather than walked with reflection on every call.
	if t.Kind() == reflect.Struct {
		ti.sizeHint = int(t.Size()) + 16
		ti.hintFromType = true
	}
	if t.Kind() != reflect.Struct {
		return ti
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name, opts := parseJSONTag(tag)
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		omitempty := false
		for _, o := range opts {
			if o == "omitempty" {
				omitempty = true
			}
		}
		ft := f.Type
		var elemType reflect.Type
		switch ft.Kind() {
		case reflect.Ptr:
			elemType = ft.Elem()
		case reflect.Slice, reflect.Map:
			elemType = ft.Elem()
		}
		hasCustomUnmarshaler := ft.Implements(unmarshalerType) || ft.Implements(textUnmarshalerType)
		hasMarshaler := ft.Implements(marshalerType)
		hasTextMarshal := ft.Implements(textMarshalerType)
		hasPtrMarshaler := false
		hasPtrTextMarshal := false
		if ft.Kind() != reflect.Ptr {
			pt := reflect.PointerTo(ft)
			hasCustomUnmarshaler = hasCustomUnmarshaler ||
				pt.Implements(unmarshalerType) || pt.Implements(textUnmarshalerType)
			hasPtrMarshaler = pt.Implements(marshalerType)
			hasPtrTextMarshal = pt.Implements(textMarshalerType)
		}
		marshalPrefix := appendQuotedString(nil, name)
		marshalPrefix = append(marshalPrefix, ':')
		marshalPrefixComma := make([]byte, 0, len(marshalPrefix)+1)
		marshalPrefixComma = append(marshalPrefixComma, ',')
		marshalPrefixComma = append(marshalPrefixComma, marshalPrefix...)
		ti.fields = append(ti.fields, fieldInfo{
			name:                    name,
			nameBytes:               []byte(name),
			marshalPrefix:           marshalPrefix,
			marshalPrefixComma:      marshalPrefixComma,
			marshalPrefixWords:      packMarshalPrefix(marshalPrefix),
			marshalPrefixCommaWords: packMarshalPrefix(marshalPrefixComma),
			fieldIdx:                i,
			offset:                  f.Offset,
			kind:                    classifyField(ft),
			goKind:                  ft.Kind(),
			typ:                     ft,
			elemType:                elemType,
			elemSize:                ft.Size(),
			omitempty:               omitempty,
			hasCustomUnmarshaler:    hasCustomUnmarshaler,
			hasMarshaler:            hasMarshaler,
			hasTextMarshal:          hasTextMarshal,
			hasPtrMarshaler:         hasPtrMarshaler,
			hasPtrTextMarshal:       hasPtrTextMarshal,
			marshalOp:               classifyMarshalOp(ft, hasMarshaler, hasTextMarshal, hasPtrMarshaler, hasPtrTextMarshal),
		})
		if omitempty {
			ti.hasOmitEmpty = true
		}
	}
	for i := range ti.fieldByFirst {
		ti.fieldByFirst[i] = -1
	}
	for i, fi := range ti.fields {
		if len(fi.nameBytes) == 0 {
			continue
		}
		first := &ti.fieldByFirst[fi.nameBytes[0]]
		if *first == -1 {
			*first = int32(i)
		} else {
			// A collision keeps the slower exact scan for that byte. It
			// preserves the existing field-order behavior for duplicate
			// prefixes and unusual JSON tags.
			*first = -2
		}
	}
	// Keep the aggressive marshal specialization deliberately narrow. It is
	// valid only when the object can be emitted in one straight-line scalar
	// loop; anything with omission, nesting, pointers, containers, interfaces,
	// or custom methods remains on the general correctness path.
	ti.flatPrimitive = !ti.hasOmitEmpty && len(ti.fields) > 0
	if ti.flatPrimitive {
		for i := range ti.fields {
			switch ti.fields[i].marshalOp {
			case marshalOpBool,
				marshalOpInt, marshalOpInt8, marshalOpInt16, marshalOpInt32, marshalOpInt64,
				marshalOpUint, marshalOpUint8, marshalOpUint16, marshalOpUint32, marshalOpUint64, marshalOpUintptr,
				marshalOpFloat32, marshalOpFloat64, marshalOpString:
			default:
				ti.flatPrimitive = false
			}
			if !ti.flatPrimitive {
				break
			}
		}
	}
	buildFieldBitmap(ti)
	return ti
}

const (
	maxBitmapFields = 16
	maxBitmapKeyLen = 64
)

// buildFieldBitmap creates a compact candidate bitmap for ordinary small
// structs. Each field owns one bit. For every key position and byte value, the
// table stores the fields that contain that byte at that position. A length
// mask removes short-prefix false positives before the final exact compare.
//
// The bounded table keeps the common path map-free and generated-code-free.
// Larger or unusually long structs retain the collision-safe first-byte path.
func buildFieldBitmap(ti *typeInfo) {
	if len(ti.fields) == 0 || len(ti.fields) > maxBitmapFields {
		return
	}
	maxLen := 0
	for _, fi := range ti.fields {
		if len(fi.nameBytes) > maxLen {
			maxLen = len(fi.nameBytes)
		}
	}
	if maxLen == 0 || maxLen > maxBitmapKeyLen {
		return
	}

	ti.fieldBitmap = make([]uint16, maxLen*256)
	ti.fieldLengthMask = make([]uint16, maxLen+1)
	for i, fi := range ti.fields {
		bit := uint16(1) << uint(i)
		name := fi.nameBytes
		ti.fieldLengthMask[len(name)] |= bit
		for pos, c := range name {
			ti.fieldBitmap[pos*256+int(c)] |= bit
		}
	}
}

// fieldForKey resolves a JSON object key against cached type metadata. The
// bitmap path rejects candidates using only bitwise operations and unsafe byte
// loads; bytesEqual remains the final correctness check. The legacy path is
// retained for large/long-field structs where a bitmap would be wasteful.
func fieldForKey(ti *typeInfo, key []byte) (*fieldInfo, bool) {
	if len(key) == 0 {
		return nil, false
	}
	if len(ti.fieldBitmap) != 0 {
		if len(key) >= len(ti.fieldLengthMask) {
			return nil, false
		}
		candidates := ti.fieldLengthMask[len(key)]
		keyPtr := unsafe.Pointer(&key[0])
		for pos := 0; pos < len(key); pos++ {
			c := *(*byte)(unsafe.Add(keyPtr, uintptr(pos)))
			candidates &= ti.fieldBitmap[pos*256+int(c)]
			if candidates == 0 {
				break
			}
		}
		if candidates != 0 {
			idx := bits.TrailingZeros16(candidates)
			if idx < len(ti.fields) {
				fi := &ti.fields[idx]
				if bytesEqual(key, fi.nameBytes) {
					return fi, true
				}
			}
		}
		// Match encoding/json's case-insensitive fallback only after the
		// exact bitmap candidate failed. This keeps the common exact-key path
		// branch- and allocation-free.
		for i := range ti.fields {
			if bytes.EqualFold(key, ti.fields[i].nameBytes) {
				return &ti.fields[i], true
			}
		}
		return nil, false
	}

	return fieldForKeyLegacy(ti, key)
}

func fieldForKeyLegacy(ti *typeInfo, key []byte) (*fieldInfo, bool) {
	if len(key) == 0 {
		return nil, false
	}
	keyPtr := unsafe.Pointer(&key[0])
	idx := ti.fieldByFirst[*(*byte)(keyPtr)]
	if idx >= 0 {
		fi := &ti.fields[idx]
		if bytesEqual(key, fi.nameBytes) {
			return fi, true
		}
		for i := range ti.fields {
			if bytes.EqualFold(key, ti.fields[i].nameBytes) {
				return &ti.fields[i], true
			}
		}
		return nil, false
	}
	if idx == -2 {
		for i := range ti.fields {
			if bytesEqual(key, ti.fields[i].nameBytes) {
				return &ti.fields[i], true
			}
		}
		for i := range ti.fields {
			if bytes.EqualFold(key, ti.fields[i].nameBytes) {
				return &ti.fields[i], true
			}
		}
	}
	return nil, false
}

func classifyField(t reflect.Type) fieldKind {
	switch t.Kind() {
	case reflect.Bool:
		return fkBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return fkInt
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return fkUint
	case reflect.Float32, reflect.Float64:
		return fkFloat
	case reflect.String:
		return fkString
	case reflect.Slice:
		return fkSlice
	case reflect.Map:
		return fkMap
	case reflect.Struct:
		return fkStruct
	case reflect.Ptr:
		return fkPtr
	case reflect.Interface:
		return fkInterface
	case reflect.Array:
		return fkArray
	}
	return fkInvalid
}

func classifyMarshalOp(t reflect.Type, hasMarshaler, hasTextMarshaler, hasPtrMarshaler, hasPtrTextMarshaler bool) marshalOp {
	if hasMarshaler || hasTextMarshaler || hasPtrMarshaler || hasPtrTextMarshaler {
		return marshalOpCustom
	}
	switch t.Kind() {
	case reflect.Bool:
		return marshalOpBool
	case reflect.Int:
		return marshalOpInt
	case reflect.Int8:
		return marshalOpInt8
	case reflect.Int16:
		return marshalOpInt16
	case reflect.Int32:
		return marshalOpInt32
	case reflect.Int64:
		return marshalOpInt64
	case reflect.Uint:
		return marshalOpUint
	case reflect.Uint8:
		return marshalOpUint8
	case reflect.Uint16:
		return marshalOpUint16
	case reflect.Uint32:
		return marshalOpUint32
	case reflect.Uint64:
		return marshalOpUint64
	case reflect.Uintptr:
		return marshalOpUintptr
	case reflect.Float32:
		return marshalOpFloat32
	case reflect.Float64:
		return marshalOpFloat64
	case reflect.String:
		return marshalOpString
	case reflect.Struct:
		return marshalOpStruct
	case reflect.Ptr:
		return marshalOpPtr
	case reflect.Slice:
		return marshalOpSlice
	case reflect.Array:
		return marshalOpArray
	case reflect.Map:
		return marshalOpMap
	case reflect.Interface:
		return marshalOpInterface
	}
	return marshalOpInvalid
}

// packMarshalPrefix stores the first 16 prefix bytes in machine-native word
// order. Writing bytes into the uint64 storage, rather than shifting them in
// Go integers, preserves both little- and big-endian targets; the same words
// are later written back to memory by the unsafe marshal path.
func packMarshalPrefix(src []byte) [2]uint64 {
	var words [2]uint64
	if len(src) > 16 {
		src = src[:16]
	}
	for i, b := range src {
		*(*byte)(unsafe.Add(unsafe.Pointer(&words[0]), i)) = b
	}
	return words
}

func parseJSONTag(tag string) (name string, opts []string) {
	if tag == "" {
		return "", nil
	}
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			return tag[:i], splitCSV(tag[i+1:])
		}
	}
	return tag, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

// --- number parsing -------------------------------------------------------

func parseIntBytes(b []byte, bits int) (int64, error) {
	if len(b) == 0 || bits <= 0 || bits > 64 {
		return parseIntBytesFallback(b, bits)
	}
	i := 0
	negative := false
	if b[0] == '-' {
		negative = true
		i++
	} else if b[0] == '+' {
		return parseIntBytesFallback(b, bits)
	}
	if i == len(b) {
		return parseIntBytesFallback(b, bits)
	}
	limit := uint64(1) << uint(bits-1)
	if !negative {
		limit--
	}
	var n uint64
	for ; i < len(b); i++ {
		c := b[i]
		if c < '0' || c > '9' {
			return parseIntBytesFallback(b, bits)
		}
		digit := uint64(c - '0')
		if n > (limit-digit)/10 {
			return parseIntBytesFallback(b, bits)
		}
		n = n*10 + digit
	}
	if negative {
		return -int64(n), nil
	}
	return int64(n), nil
}

// parseInt64DigitsFast is used after the scanner has already validated the
// JSON number grammar and marked the token as an integer. The bounded form is
// safe for int64: an optional sign plus at most 18 digits cannot overflow.
// Narrow Go integer types and longer values use parseIntBytes so range checks
// remain exact.
func parseInt64DigitsFast(b []byte, bits int) (int64, bool) {
	if bits != 64 || len(b) == 0 || len(b) > 18 {
		return 0, false
	}
	i := 0
	negative := false
	if b[0] == '-' {
		negative = true
		i = 1
	}
	if i == len(b) {
		return 0, false
	}
	var n uint64
	for ; i < len(b); i++ {
		n = n*10 + uint64(b[i]-'0')
	}
	if negative {
		return -int64(n), true
	}
	return int64(n), true
}

func parseUintBytes(b []byte, bits int) (uint64, error) {
	if len(b) == 0 || bits <= 0 || bits > 64 || b[0] == '-' || b[0] == '+' {
		return parseUintBytesFallback(b, bits)
	}
	var limit uint64 = ^uint64(0)
	if bits < 64 {
		limit = (uint64(1) << uint(bits)) - 1
	}
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return parseUintBytesFallback(b, bits)
		}
		digit := uint64(c - '0')
		if n > (limit-digit)/10 {
			return parseUintBytesFallback(b, bits)
		}
		n = n*10 + digit
	}
	return n, nil
}

// parseUint64DigitsFast is the unsigned counterpart of
// parseInt64DigitsFast. The scanner has already ruled out signs and
// non-digits; the length bound keeps the unchecked accumulation in range.
func parseUint64DigitsFast(b []byte, bits int) (uint64, bool) {
	if bits != 64 || len(b) == 0 || len(b) > 18 {
		return 0, false
	}
	var n uint64
	for _, c := range b {
		n = n*10 + uint64(c-'0')
	}
	return n, true
}

func parseFloatBytes(b []byte, bits int) (float64, error) {
	if value, ok := parseSimpleDecimal(b, bits); ok {
		return value, nil
	}
	// Most JSON numbers in typed and interface{} workloads are integer
	// literals. Parse those without strconv's decimal-float machinery; keep
	// ParseFloat for fractions, exponents, signed zero, and large overflow
	// cases where its exact compatibility matters.
	if isIntegerJSONNumber(b) && !(len(b) == 2 && b[0] == '-' && b[1] == '0') {
		if b[0] == '-' {
			if n, err := parseIntBytes(b, 64); err == nil {
				return float64(n), nil
			}
		} else if n, err := parseUintBytes(b, 64); err == nil {
			return float64(n), nil
		}
	}
	f, err := strconv.ParseFloat(*(*string)(unsafe.Pointer(&b)), bits)
	if err != nil {
		return 0, &SyntaxError{Msg: "invalid number: " + string(b)}
	}
	return f, nil
}

// parseSimpleDecimal handles the common non-exponent decimal form without
// entering strconv's general decimal parser. It is deliberately limited to
// 18 total digits so float64 conversion and division stay in the range where
// they agree with the standard conversion for ordinary JSON data. Complex,
// high-precision, and overflow forms use strconv below.
func parseSimpleDecimal(b []byte, bits int) (float64, bool) {
	if len(b) == 0 || bits <= 0 || bits > 64 {
		return 0, false
	}
	i := 0
	negative := false
	if b[0] == '-' {
		negative = true
		i++
	}
	if i == len(b) || b[i] == '+' {
		return 0, false
	}
	var n uint64
	digits := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		if digits == 18 {
			return 0, false
		}
		n = n*10 + uint64(b[i]-'0')
		digits++
		i++
	}
	if i >= len(b) || b[i] != '.' || digits == 0 {
		return 0, false
	}
	i++
	fracDigits := 0
	for i < len(b) && b[i] >= '0' && b[i] <= '9' {
		if digits == 18 {
			return 0, false
		}
		n = n*10 + uint64(b[i]-'0')
		digits++
		fracDigits++
		i++
	}
	if i != len(b) || fracDigits == 0 {
		return 0, false
	}
	value := float64(n) / floatPowers10[fracDigits]
	if negative {
		value = -value
	}
	if bits == 32 {
		value = float64(float32(value))
	}
	return value, true
}

var floatPowers10 = [...]float64{
	1,
	1e1,
	1e2,
	1e3,
	1e4,
	1e5,
	1e6,
	1e7,
	1e8,
	1e9,
	1e10,
	1e11,
	1e12,
	1e13,
	1e14,
	1e15,
	1e16,
	1e17,
	1e18,
}

func parseIntBytesFallback(b []byte, bits int) (int64, error) {
	n, err := strconv.ParseInt(*(*string)(unsafe.Pointer(&b)), 10, bits)
	if err != nil {
		return 0, &SyntaxError{Msg: "invalid number: " + string(b)}
	}
	return n, nil
}

func parseUintBytesFallback(b []byte, bits int) (uint64, error) {
	n, err := strconv.ParseUint(*(*string)(unsafe.Pointer(&b)), 10, bits)
	if err != nil {
		return 0, &SyntaxError{Msg: "invalid number: " + string(b)}
	}
	return n, nil
}

func isIntegerJSONNumber(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	i := 0
	if b[0] == '-' {
		i++
	}
	if i == len(b) {
		return false
	}
	for ; i < len(b); i++ {
		if b[i] < '0' || b[i] > '9' {
			return false
		}
	}
	return true
}

func intBits(k reflect.Kind) int {
	switch k {
	case reflect.Int:
		return 64
	case reflect.Int8:
		return 8
	case reflect.Int16:
		return 16
	case reflect.Int32:
		return 32
	case reflect.Int64:
		return 64
	}
	return 64
}

func uintBits(k reflect.Kind) int {
	switch k {
	case reflect.Uint:
		return 64
	case reflect.Uint8:
		return 8
	case reflect.Uint16:
		return 16
	case reflect.Uint32:
		return 32
	case reflect.Uint64:
		return 64
	case reflect.Uintptr:
		return strconv.IntSize
	}
	return 64
}

// --- string unescape ------------------------------------------------------

// unescapeJSONString returns the unescaped content of a JSON string
// (without surrounding quotes). For strings without escapes, returns
// the input as-is — zero-copy. For strings with escapes, allocates a
// new buffer. Invalid UTF-8 sequences are replaced with U+FFFD to
// match encoding/json behavior.
func unescapeJSONString(s []byte) []byte {
	needsUnescape := jsonStringNeedsUnescape(s)
	if debugUnescape {
		fmt.Fprintf(os.Stderr, "unescape: len=%d needsUnescape=%v bytes=%v\n", len(s), needsUnescape, s)
	}
	if !needsUnescape {
		return s
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == '\\' {
			i++
			if i >= len(s) {
				break
			}
			switch s[i] {
			case '"':
				out = append(out, '"')
			case '\\':
				out = append(out, '\\')
			case '/':
				out = append(out, '/')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'u':
				if i+4 >= len(s) {
					return out
				}
				r := hexToRune(s[i+1 : i+5])
				// Surrogate handling (RFC 8259 § 7): high surrogate
				// may be followed by a low surrogate to form a
				// supplementary plane code point. Lone surrogates
				// become U+FFFD.
				if r >= 0xD800 && r < 0xDC00 {
					// High surrogate — try to combine with a
					// following low surrogate. The 4 hex digits
					// for the current \u end at position i+4; the
					// next bytes start at i+5.
					if i+10 < len(s) && s[i+5] == '\\' && s[i+6] == 'u' {
						lo := hexToRune(s[i+7 : i+11])
						if lo >= 0xDC00 && lo < 0xE000 {
							r = 0x10000 + ((r - 0xD800) << 10) + (lo - 0xDC00)
							i += 6 // skip past the second \uXXXX
						} else {
							r = 0xFFFD
						}
					} else {
						r = 0xFFFD
					}
				} else if r >= 0xDC00 && r < 0xE000 {
					// Lone low surrogate.
					r = 0xFFFD
				}
				out = append(out, encodeRune(r)...)
				i += 4
			default:
				out = append(out, c)
			}
			i++
			continue
		}
		// High byte: decode UTF-8. If invalid, emit U+FFFD.
		if c < 0x80 {
			out = append(out, c)
			i++
			continue
		}
		r, size := decodeRune(s[i:])
		if r < 0 {
			out = append(out, 0xEF, 0xBF, 0xBD)
			i++
			continue
		}
		out = append(out, encodeRune(r)...)
		i += size
	}
	return out
}

// decodeRune decodes one UTF-8 rune from b. Returns (-1, -1) on invalid.
func decodeRune(b []byte) (rune, int) {
	if len(b) == 0 {
		return -1, -1
	}
	c := b[0]
	switch {
	case c < 0x80:
		return rune(c), 1
	case c < 0xC2:
		return -1, -1
	case c < 0xE0:
		if len(b) < 2 || b[1]&0xC0 != 0x80 {
			return -1, -1
		}
		r := rune(c&0x1F)<<6 | rune(b[1]&0x3F)
		if r < 0x80 {
			return -1, -1
		}
		return r, 2
	case c < 0xF0:
		if len(b) < 3 || b[1]&0xC0 != 0x80 || b[2]&0xC0 != 0x80 {
			return -1, -1
		}
		r := rune(c&0x0F)<<12 | rune(b[1]&0x3F)<<6 | rune(b[2]&0x3F)
		if r < 0x800 {
			return -1, -1
		}
		// Surrogates are invalid.
		if r >= 0xD800 && r < 0xE000 {
			return -1, -1
		}
		return r, 3
	case c < 0xF5:
		if len(b) < 4 || b[1]&0xC0 != 0x80 || b[2]&0xC0 != 0x80 || b[3]&0xC0 != 0x80 {
			return -1, -1
		}
		r := rune(c&0x07)<<18 | rune(b[1]&0x3F)<<12 | rune(b[2]&0x3F)<<6 | rune(b[3]&0x3F)
		if r < 0x10000 || r > 0x10FFFF {
			return -1, -1
		}
		return r, 4
	}
	return -1, -1
}

func jsonStringNeedsUnescape(s []byte) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c < 0x20 {
			return true
		}
		if c < 0x80 {
			continue
		}
		_, size := decodeRune(s[i:])
		if size < 0 {
			return true
		}
		i += size - 1
	}
	return false
}

func hexToRune(b []byte) rune {
	var r rune
	for i := 0; i < 4; i++ {
		c := b[i]
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return '?'
		}
		r = r*16 + d
	}
	return r
}

func encodeRune(r rune) []byte {
	if r < 0x80 {
		return []byte{byte(r)}
	}
	if r < 0x800 {
		return []byte{
			byte(0xc0 | (r >> 6)),
			byte(0x80 | (r & 0x3f)),
		}
	}
	if r < 0x10000 {
		return []byte{
			byte(0xe0 | (r >> 12)),
			byte(0x80 | ((r >> 6) & 0x3f)),
			byte(0x80 | (r & 0x3f)),
		}
	}
	return []byte{
		byte(0xf0 | (r >> 18)),
		byte(0x80 | ((r >> 12) & 0x3f)),
		byte(0x80 | ((r >> 6) & 0x3f)),
		byte(0x80 | (r & 0x3f)),
	}
}

// --- interface implementations --------------------------------------------

var (
	unmarshalerType     = reflect.TypeOf((*jsonUnmarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	_                   = fmt.Sprintf
)

type jsonUnmarshaler interface {
	UnmarshalJSON([]byte) error
}

func decodeJSONUnmarshaler(kind nodeKind, start, end uint32, src []byte, rv reflect.Value) error {
	// Hand the user the raw JSON bytes for the value (including any
	// surrounding braces/brackets).
	raw := src[start:end]
	return rv.Addr().Interface().(jsonUnmarshaler).UnmarshalJSON(raw)
}

func decodeTextUnmarshaler(kind nodeKind, start, end uint32, src []byte, rv reflect.Value) error {
	if kind != nodeString {
		return &ErrTypeMismatch{SrcKind: kind.String(), DstType: rv.Type().String()}
	}
	text := unescapeJSONString(src[start:end])
	return rv.Addr().Interface().(encoding.TextUnmarshaler).UnmarshalText(text)
}
