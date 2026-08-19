package apexjson

import (
	"reflect"
	"time"
	"unsafe"
)

const (
	directSupportUnknown uint32 = iota
	directSupportYes
	directSupportNo
)

// unmarshalDirect decodes supported typed destinations while the scanner is
// already walking the source. It deliberately does not build the AST: the
// public Decoder.Parse path still builds the off-heap tape for queries, while
// Unmarshal no longer pays for a tape it immediately discards.
func unmarshalDirect(src []byte, rv reflect.Value) error {
	var d directDecoder
	d.src = src
	d.scan.reset(src)
	if err := d.value(rv, 0); err != nil {
		return err
	}
	tok, more := d.scan.next()
	if more || tok.Kind == kindError {
		return directSyntaxError(tok, "unexpected "+tok.Kind.String())
	}
	return nil
}

// directTypeSupported is intentionally conservative. Unsupported features
// fall back to the complete tape-backed decoder, so custom unmarshaler,
// interface, and map behavior remains unchanged.
func directTypeSupported(t reflect.Type, _ int) bool {
	if t == nil {
		return false
	}
	ti := getTypeInfo(t)
	if state := ti.directSupport.Load(); state != directSupportUnknown {
		return state == directSupportYes
	}

	seen := make(map[reflect.Type]struct{}, 8)
	supported := directTypeSupportedSeen(t, seen)
	state := directSupportNo
	if supported {
		state = directSupportYes
	}
	ti.directSupport.CompareAndSwap(directSupportUnknown, state)
	return supported
}

func directTypeSupportedSeen(t reflect.Type, seen map[reflect.Type]struct{}) bool {
	if t == nil {
		return false
	}
	if _, exists := seen[t]; exists {
		// Recursive type graphs use the tape-backed path. This is a graph
		// cycle check, not an arbitrary input nesting limit.
		return false
	}
	seen[t] = struct{}{}
	defer delete(seen, t)

	ti := getTypeInfo(t)
	if typeInfoHasCustomUnmarshal(ti) {
		return false
	}
	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Ptr:
		return directTypeSupportedSeen(t.Elem(), seen)
	case reflect.Slice, reflect.Array:
		return directTypeSupportedSeen(t.Elem(), seen)
	case reflect.Struct:
		for _, fi := range ti.fields {
			if !directTypeSupportedSeen(fi.typ, seen) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

type directDecoder struct {
	scan  scanner
	src   []byte
	stats *directStats
}

// directStats is attached only by diagnostic benchmarks. The normal direct
// decoder leaves it nil, so token and field lookup timing is not paid for in
// production.
type directStats struct {
	TotalNS       uint64
	TokenNS       uint64
	ValueTokenNS  uint64
	KeyScanNS     uint64
	FieldLookupNS uint64
	FieldDecodeNS uint64
	FastScalarNS  uint64
	SeparatorNS   uint64
	Tokens        uint64
	ValueTokens   uint64
	KeyTokens     uint64
	FieldLookups  uint64
	FieldDecodes  uint64
	FastScalars   uint64
	Separators    uint64
	FieldHits     uint64
	FieldMisses   uint64
}

func (d *directDecoder) value(rv reflect.Value, depth int) error {
	tok, err := d.nextValueToken()
	if err != nil {
		return err
	}
	return d.valueFromToken(tok, rv, depth)
}

func (d *directDecoder) valueFromToken(tok token, rv reflect.Value, depth int) error {
	if tok.Kind == kindNull {
		switch rv.Kind() {
		case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Interface:
			rv.Set(reflect.Zero(rv.Type()))
			return nil
		case reflect.Struct:
			return nil
		default:
			return &ErrTypeMismatch{SrcKind: "null", DstType: rv.Type().String()}
		}
	}
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		return d.valueFromToken(tok, rv.Elem(), depth)
	}
	switch tok.Kind {
	case kindObjectStart:
		if rv.Kind() != reflect.Struct {
			return &ErrTypeMismatch{SrcKind: "object", DstType: rv.Type().String()}
		}
		return d.object(tok, rv, depth)
	case kindArrayStart:
		if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
			return &ErrTypeMismatch{SrcKind: "array", DstType: rv.Type().String()}
		}
		return d.array(tok, rv, depth)
	default:
		return directScalar(tok, d.src, rv)
	}
}

func (d *directDecoder) object(open token, rv reflect.Value, depth int) error {
	ti := getTypeInfo(rv.Type())
	return d.objectAt(open, unsafe.Pointer(rv.UnsafeAddr()), ti, depth)
}

// objectAt is the reflection-free struct branch used by the fused decoder.
// The caller has already resolved the destination type and base address. In
// particular, array/slice-of-struct decoding can stay on this path for every
// row instead of constructing rv.Index(i) and reloading type metadata.
func (d *directDecoder) objectAt(open token, base unsafe.Pointer, ti *typeInfo, depth int) error {
	tok, fieldIndex, colonConsumed, err := d.nextObjectKey(ti)
	if err != nil {
		return directSyntaxError(tok, "unexpected end of input in object")
	}
	if tok.Kind == kindObjectEnd {
		return nil
	}
	for {
		if tok.Kind != kindString {
			return directSyntaxError(tok, "expected string key, got "+tok.Kind.String())
		}
		if !colonConsumed && d.stats == nil {
			if d.scan.pos < d.scan.end && isWS(d.scan.byteAt(d.scan.pos)) {
				d.scan.pos = int(swSkipWS(d.scan.src, uint32(d.scan.pos), uint32(d.scan.end)))
			}
			if d.scan.pos >= d.scan.end || d.scan.byteAt(d.scan.pos) != ':' {
				return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected ':' after object key")
			}
			d.scan.pos++
		} else if !colonConsumed && !d.scan.consumeByte(':') {
			return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected ':' after object key")
		}
		lookupStarted := time.Time{}
		if d.stats != nil {
			lookupStarted = time.Now()
			d.stats.FieldLookups++
		}
		var fi *fieldInfo
		if fieldIndex >= 0 {
			fi = &ti.fields[fieldIndex]
		} else if fieldIndex == fieldIndexNeedsLookup {
			fi, _ = directFieldForKey(ti, d.src[tok.Start:tok.End])
		}
		if d.stats != nil {
			if fi != nil {
				d.stats.FieldHits++
			} else {
				d.stats.FieldMisses++
			}
			d.stats.FieldLookupNS += uint64(time.Since(lookupStarted))
		}

		// Once the key is resolved, scalar fields can parse and write directly
		// from the scanner cursor. This removes the intermediate value token,
		// the generic token-kind switch, and the second field-kind switch from
		// the common typed struct path. Complex fields retain the complete
		// token-backed path below.
		fastScalar := false
		if fi != nil {
			var fieldErr error
			if d.stats == nil {
				fastScalar, fieldErr = d.decodeKnownScalarAt(base, fi)
			} else {
				started := time.Now()
				fastScalar, fieldErr = d.decodeKnownScalarAt(base, fi)
				d.stats.FastScalarNS += uint64(time.Since(started))
				if fastScalar {
					d.stats.FastScalars++
				}
			}
			if fastScalar && fieldErr != nil {
				return fieldErr
			}
		}

		if !fastScalar {
			valueTok, valueErr := d.nextValueToken()
			if valueErr != nil {
				return directSyntaxError(valueTok, "unexpected end of input in object")
			}
			if fi != nil {
				var fieldErr error
				if d.stats == nil {
					fieldErr = d.fieldFromToken(valueTok, base, fi, depth+1)
				} else {
					started := time.Now()
					fieldErr = d.fieldFromToken(valueTok, base, fi, depth+1)
					d.stats.FieldDecodeNS += uint64(time.Since(started))
					d.stats.FieldDecodes++
				}
				if fieldErr != nil {
					return fieldErr
				}
			} else if err := d.skipFromToken(valueTok, depth+1); err != nil {
				return err
			}
		}

		var sep byte
		var ok bool
		if d.stats == nil {
			if d.scan.pos < d.scan.end && isWS(d.scan.byteAt(d.scan.pos)) {
				d.scan.pos = int(swSkipWS(d.scan.src, uint32(d.scan.pos), uint32(d.scan.end)))
			}
			if d.scan.pos < d.scan.end {
				sep = d.scan.byteAt(d.scan.pos)
				if sep == ',' || sep == '}' {
					d.scan.pos++
					ok = true
				}
			}
		} else {
			started := time.Now()
			sep, ok = d.scan.consumeSeparator('}')
			d.stats.SeparatorNS += uint64(time.Since(started))
			d.stats.Separators++
		}
		if !ok {
			return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected ',' or '}'")
		}
		if sep == '}' {
			return nil
		}
		tok, fieldIndex, colonConsumed, err = d.nextObjectKey(ti)
		if err != nil || tok.Kind == kindObjectEnd {
			return directSyntaxError(tok, "expected string key after ','")
		}
	}
}

func directFieldForKey(ti *typeInfo, key []byte) (*fieldInfo, bool) {
	return fieldForKey(ti, key)
}

// decodeKnownScalarAt is the allocation-free, token-free scalar path used by
// typed struct decoding. It only claims values whose first byte matches the
// destination kind; mismatches return handled=false so the generic path can
// preserve the normal type-error behavior.
func (d *directDecoder) decodeKnownScalarAt(base unsafe.Pointer, fi *fieldInfo) (handled bool, err error) {
	if d.scan.pos < d.scan.end && isWS(d.scan.byteAt(d.scan.pos)) {
		d.scan.pos = int(swSkipWS(d.scan.src, uint32(d.scan.pos), uint32(d.scan.end)))
	}
	if d.scan.pos >= d.scan.end {
		return true, directSyntaxError(token{Start: uint32(d.scan.pos), Kind: kindEOF}, "unexpected end of input")
	}
	p := unsafe.Add(base, fi.offset)
	c := d.scan.byteAt(d.scan.pos)
	start := d.scan.pos
	switch fi.kind {
	case fkBool:
		if c != 't' && c != 'f' {
			return false, nil
		}
		d.scan.pos++
		lit := "false"
		kind := kindFalse
		if c == 't' {
			lit = "true"
			kind = kindTrue
		}
		tok, ok := d.scan.scanLiteral(start, kind, lit)
		if !ok {
			return true, directSyntaxError(tok, "invalid token")
		}
		*(*bool)(p) = c == 't'
		return true, nil
	case fkString:
		if c != '"' {
			return false, nil
		}
		d.scan.pos++
		contentStart, contentEnd, flags, ok := d.scan.scanStringSpan(start)
		if !ok {
			return true, directSyntaxError(token{Start: uint32(start), End: uint32(contentEnd), Kind: kindError}, "invalid token")
		}
		raw := d.src[contentStart:contentEnd]
		if stringNeedsUnescapeFlags(flags, raw) {
			*(*string)(p) = bytesToString(unescapeJSONString(raw))
		} else {
			*(*string)(p) = *(*string)(unsafe.Pointer(&raw))
		}
		return true, nil
	case fkInt, fkUint, fkFloat:
		if c != '-' && (c < '0' || c > '9') {
			return false, nil
		}
		if fi.kind == fkInt || fi.kind == fkUint {
			if fast, fastErr := d.decodeKnownIntegerAt(p, fi); fast {
				return true, fastErr
			}
		}
		d.scan.pos++
		numberEnd, numberFlags, ok := d.scan.scanNumberSpan(start)
		if !ok {
			return true, directSyntaxError(token{Start: uint32(start), End: uint32(numberEnd), Kind: kindError}, "invalid token")
		}
		raw := d.src[start:numberEnd]
		switch fi.kind {
		case fkInt:
			if numberFlags&tokenNumberInteger != 0 {
				if value, fast := parseInt64DigitsFast(raw, intBits(fi.goKind)); fast {
					storeIntAt(p, fi.goKind, value)
					return true, nil
				}
			}
			value, parseErr := parseIntBytes(raw, intBits(fi.goKind))
			if parseErr == nil {
				storeIntAt(p, fi.goKind, value)
			}
			return true, parseErr
		case fkUint:
			if numberFlags&tokenNumberInteger != 0 {
				if value, fast := parseUint64DigitsFast(raw, uintBits(fi.goKind)); fast {
					storeUintAt(p, fi.goKind, value)
					return true, nil
				}
			}
			value, parseErr := parseUintBytes(raw, uintBits(fi.goKind))
			if parseErr == nil {
				storeUintAt(p, fi.goKind, value)
			}
			return true, parseErr
		case fkFloat:
			bits := 64
			if fi.goKind == reflect.Float32 {
				bits = 32
			}
			value, parseErr := parseFloatBytes(raw, bits)
			if parseErr == nil {
				if fi.goKind == reflect.Float32 {
					*(*float32)(p) = float32(value)
				} else {
					*(*float64)(p) = value
				}
			}
			return true, parseErr
		}
	}
	return false, nil
}

// decodeKnownIntegerAt fuses scanning and conversion for the common 64-bit
// integer field. The JSON grammar checks that would affect the result—sign,
// leading zero, digit run, and decimal/exponent form—are handled before the
// value is committed. Anything outside this bounded fast case returns false
// so scanNumber and parseIntBytes/parseUintBytes retain their full behavior.
func (d *directDecoder) decodeKnownIntegerAt(p unsafe.Pointer, fi *fieldInfo) (bool, error) {
	bits := intBits(fi.goKind)
	if fi.kind == fkUint {
		bits = uintBits(fi.goKind)
	}
	if bits != 64 {
		return false, nil
	}

	pos := d.scan.pos
	negative := false
	if d.scan.byteAt(pos) == '-' {
		if fi.kind == fkUint {
			return false, nil
		}
		negative = true
		pos++
		if pos >= d.scan.end {
			return false, nil
		}
	}
	first := d.scan.byteAt(pos)
	if first < '0' || first > '9' {
		return false, nil
	}
	if first == '0' {
		pos++
		// Let scanNumber report the usual malformed-number behavior for a
		// leading zero followed by another digit, or for a decimal/exponent.
		if pos < d.scan.end {
			next := d.scan.byteAt(pos)
			if (next >= '0' && next <= '9') || next == '.' || next == 'e' || next == 'E' {
				return false, nil
			}
		}
		if negative {
			*(*int64)(p) = 0
		} else if fi.kind == fkInt {
			*(*int64)(p) = 0
		} else {
			*(*uint64)(p) = 0
		}
		d.scan.pos = pos
		return true, nil
	}

	var n uint64
	digits := 0
	for pos < d.scan.end {
		c := d.scan.byteAt(pos)
		if c < '0' || c > '9' {
			break
		}
		digits++
		if digits > 18 {
			return false, nil
		}
		n = n*10 + uint64(c-'0')
		pos++
	}
	if digits == 0 || (pos < d.scan.end && (d.scan.byteAt(pos) == '.' || d.scan.byteAt(pos) == 'e' || d.scan.byteAt(pos) == 'E')) {
		return false, nil
	}
	if negative {
		*(*int64)(p) = -int64(n)
	} else if fi.kind == fkInt {
		*(*int64)(p) = int64(n)
	} else {
		*(*uint64)(p) = n
	}
	d.scan.pos = pos
	return true, nil
}

func (d *directDecoder) fieldFromToken(tok token, base unsafe.Pointer, fi *fieldInfo, depth int) error {
	p := unsafe.Add(base, fi.offset)
	switch fi.kind {
	case fkBool, fkInt, fkUint, fkFloat, fkString:
		return directScalarAt(tok, d.src, p, fi)
	case fkStruct:
		if tok.Kind == kindObjectStart {
			return d.objectAt(tok, p, getTypeInfo(fi.typ), depth)
		}
		return d.valueFromToken(tok, reflect.NewAt(fi.typ, p).Elem(), depth)
	default:
		return d.valueFromToken(tok, reflect.NewAt(fi.typ, p).Elem(), depth)
	}
}

func (d *directDecoder) array(open token, rv reflect.Value, depth int) error {
	isSlice := rv.Kind() == reflect.Slice
	elemType := rv.Type().Elem()
	if fi, ok := directScalarField(elemType); ok {
		return d.scalarArray(rv, depth, fi)
	}
	var structInfo *typeInfo
	if elemType.Kind() == reflect.Struct {
		ti := getTypeInfo(elemType)
		if !typeInfoHasCustomUnmarshal(ti) {
			structInfo = ti
		}
	}
	if isSlice {
		if rv.IsNil() {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
		} else {
			rv.SetLen(0)
		}
	}

	tok, err := d.nextValueToken()
	if err != nil {
		return directSyntaxError(tok, "unexpected end of input in array")
	}
	if tok.Kind == kindArrayEnd {
		if isSlice && rv.IsNil() {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 0))
		}
		return nil
	}

	i := 0
	for {
		var dst reflect.Value
		var fieldBase unsafe.Pointer
		hasDst := false
		if isSlice {
			if i >= rv.Cap() {
				newCap := rv.Cap() * 2
				if newCap < i+1 {
					newCap = i + 1
				}
				next := reflect.MakeSlice(rv.Type(), rv.Len(), newCap)
				reflect.Copy(next, rv)
				rv.Set(next)
			}
			rv.SetLen(i + 1)
			hasDst = true
			if structInfo != nil {
				sh := (*decodeSliceHeader)(unsafe.Pointer(rv.UnsafeAddr()))
				fieldBase = unsafe.Add(sh.data, uintptr(i)*structInfo.typ.Size())
			} else {
				dst = rv.Index(i)
			}
		} else if i < rv.Len() {
			hasDst = true
			if structInfo != nil {
				fieldBase = unsafe.Add(unsafe.Pointer(rv.UnsafeAddr()), uintptr(i)*structInfo.typ.Size())
			} else {
				dst = rv.Index(i)
			}
		}
		if structInfo != nil && hasDst && tok.Kind == kindObjectStart {
			if err := d.objectAt(tok, fieldBase, structInfo, depth+1); err != nil {
				return err
			}
		} else if structInfo != nil && hasDst {
			if err := d.valueFromToken(tok, reflect.NewAt(elemType, fieldBase).Elem(), depth+1); err != nil {
				return err
			}
		} else if dst.IsValid() {
			if err := d.valueFromToken(tok, dst, depth+1); err != nil {
				return err
			}
		} else if err := d.skipFromToken(tok, depth+1); err != nil {
			return err
		}
		i++

		sep, ok := d.scan.consumeSeparator(']')
		if !ok {
			return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected ',' or ']'")
		}
		if sep == ']' {
			return nil
		}
		tok, err = d.nextValueToken()
		if err != nil || tok.Kind == kindArrayEnd {
			return directSyntaxError(tok, "expected value after ','")
		}
	}
}

// directScalarField returns the compact metadata needed to write a scalar
// array element without constructing reflect.Values for each element.
func directScalarField(t reflect.Type) (fieldInfo, bool) {
	kind := classifyField(t)
	switch kind {
	case fkBool, fkInt, fkUint, fkFloat, fkString:
		return fieldInfo{kind: kind, goKind: t.Kind(), typ: t, elemSize: t.Size()}, true
	default:
		return fieldInfo{}, false
	}
}

func (d *directDecoder) scalarArray(rv reflect.Value, depth int, fi fieldInfo) error {
	isSlice := rv.Kind() == reflect.Slice
	elemSize := fi.elemSize
	oldLen := 0
	if isSlice {
		if rv.IsNil() {
			rv.Set(reflect.MakeSlice(rv.Type(), 0, 8))
		} else {
			oldLen = rv.Len()
			if fi.kind == fkString && oldLen != 0 {
				sh := (*decodeSliceHeader)(unsafe.Pointer(rv.UnsafeAddr()))
				clear(unsafe.Slice((*string)(sh.data), oldLen))
			}
			rv.SetLen(0)
		}
	}

	tok, err := d.nextValueToken()
	if err != nil {
		return directSyntaxError(tok, "unexpected end of input in array")
	}
	if tok.Kind == kindArrayEnd {
		return nil
	}

	for i := 0; ; i++ {
		hasDst := !isSlice && i < rv.Len()
		var p unsafe.Pointer
		if isSlice {
			if i >= rv.Cap() {
				newCap := rv.Cap() * 2
				if newCap < 8 {
					newCap = 8
				}
				if newCap < i+1 {
					newCap = i + 1
				}
				next := reflect.MakeSlice(rv.Type(), rv.Len(), newCap)
				reflect.Copy(next, rv)
				rv.Set(next)
			}
			rv.SetLen(i + 1)
			hasDst = true
			sh := (*decodeSliceHeader)(unsafe.Pointer(rv.UnsafeAddr()))
			p = unsafe.Add(sh.data, uintptr(i)*elemSize)
		} else if hasDst {
			p = unsafe.Add(unsafe.Pointer(rv.UnsafeAddr()), uintptr(i)*elemSize)
		}

		if hasDst {
			if err := directScalarAt(tok, d.src, p, &fi); err != nil {
				return err
			}
		} else if err := d.skipFromToken(tok, depth+1); err != nil {
			return err
		}

		sep, ok := d.scan.consumeSeparator(']')
		if !ok {
			return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected ',' or ']'")
		}
		if sep == ']' {
			return nil
		}
		tok, err = d.nextValueToken()
		if err != nil || tok.Kind == kindArrayEnd {
			return directSyntaxError(tok, "expected value after ','")
		}
	}
}

func (d *directDecoder) skipFromToken(tok token, depth int) error {
	if tok.Kind != kindObjectStart && tok.Kind != kindArrayStart {
		return nil
	}
	endKind := kindObjectEnd
	if tok.Kind == kindArrayStart {
		endKind = kindArrayEnd
	}
	next, err := d.nextValueToken()
	if err != nil {
		return directSyntaxError(next, "unexpected end of input")
	}
	if next.Kind == endKind {
		return nil
	}
	for {
		if tok.Kind == kindObjectStart {
			if next.Kind != kindString {
				return directSyntaxError(next, "expected string key")
			}
			if !d.scan.consumeByte(':') {
				return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected ':' after object key")
			}
			next, err = d.nextValueToken()
			if err != nil {
				return directSyntaxError(next, "unexpected end of input")
			}
		}
		if err := d.skipFromToken(next, depth+1); err != nil {
			return err
		}
		close := byte('}')
		if tok.Kind == kindArrayStart {
			close = ']'
		}
		sep, ok := d.scan.consumeSeparator(close)
		if !ok {
			return directSyntaxError(token{Start: uint32(d.scan.pos)}, "expected separator")
		}
		if sep == close {
			return nil
		}
		next, err = d.nextValueToken()
		if err != nil || (tok.Kind == kindObjectStart && next.Kind != kindString) {
			return directSyntaxError(next, "expected value")
		}
	}
}

func (d *directDecoder) nextValueToken() (token, error) {
	if d.stats == nil {
		return d.nextValueTokenUntracked()
	}
	started := time.Now()
	tok, ok := d.scan.next()
	d.stats.Tokens++
	elapsed := uint64(time.Since(started))
	d.stats.TokenNS += elapsed
	d.stats.ValueTokenNS += elapsed
	d.stats.ValueTokens++
	if ok {
		return tok, nil
	}
	if tok.Kind == kindError {
		return tok, directSyntaxError(tok, "invalid token")
	}
	return tok, directSyntaxError(tok, "unexpected end of input")
}

func (d *directDecoder) nextObjectKey(ti *typeInfo) (token, int32, bool, error) {
	if d.stats == nil {
		tok, fieldIndex, ok := d.scan.nextObjectKey(ti)
		colonConsumed := d.scan.keyColonConsumed
		if ok {
			return tok, fieldIndex, colonConsumed, nil
		}
		if tok.Kind == kindError {
			return tok, fieldIndex, colonConsumed, directSyntaxError(tok, "invalid token")
		}
		return tok, fieldIndex, colonConsumed, directSyntaxError(tok, "unexpected end of input")
	}
	started := time.Now()
	tok, fieldIndex, ok := d.scan.nextObjectKey(ti)
	colonConsumed := d.scan.keyColonConsumed
	d.stats.Tokens++
	elapsed := uint64(time.Since(started))
	d.stats.TokenNS += elapsed
	d.stats.KeyScanNS += elapsed
	d.stats.KeyTokens++
	if ok {
		return tok, fieldIndex, colonConsumed, nil
	}
	if tok.Kind == kindError {
		return tok, fieldIndex, colonConsumed, directSyntaxError(tok, "invalid token")
	}
	return tok, fieldIndex, colonConsumed, directSyntaxError(tok, "unexpected end of input")
}

func (d *directDecoder) nextValueTokenUntracked() (token, error) {
	tok, ok := d.scan.next()
	if ok {
		return tok, nil
	}
	if tok.Kind == kindError {
		return tok, directSyntaxError(tok, "invalid token")
	}
	return tok, directSyntaxError(tok, "unexpected end of input")
}

func directScalar(tok token, src []byte, rv reflect.Value) error {
	if rv.CanAddr() {
		fi := fieldInfo{
			kind:   classifyField(rv.Type()),
			goKind: rv.Kind(),
			typ:    rv.Type(),
		}
		return directScalarAt(tok, src, unsafe.Pointer(rv.UnsafeAddr()), &fi)
	}
	return &ErrUnsupportedType{Type: rv.Type().String()}
}

func directScalarAt(tok token, src []byte, p unsafe.Pointer, fi *fieldInfo) error {
	switch fi.kind {
	case fkBool:
		if tok.Kind == kindTrue {
			*(*bool)(p) = true
			return nil
		}
		if tok.Kind == kindFalse {
			*(*bool)(p) = false
			return nil
		}
	case fkInt:
		if tok.Kind == kindNumber {
			value, err := parseIntBytes(src[tok.Start:tok.End], intBits(fi.goKind))
			if err == nil {
				storeIntAt(p, fi.goKind, value)
			}
			return err
		}
	case fkUint:
		if tok.Kind == kindNumber {
			value, err := parseUintBytes(src[tok.Start:tok.End], uintBits(fi.goKind))
			if err == nil {
				storeUintAt(p, fi.goKind, value)
			}
			return err
		}
	case fkFloat:
		if tok.Kind == kindNumber {
			bits := 64
			if fi.goKind == reflect.Float32 {
				bits = 32
			}
			value, err := parseFloatBytes(src[tok.Start:tok.End], bits)
			if err == nil {
				if fi.goKind == reflect.Float32 {
					*(*float32)(p) = float32(value)
				} else {
					*(*float64)(p) = value
				}
			}
			return err
		}
	case fkString:
		if tok.Kind == kindString {
			raw := src[tok.Start:tok.End]
			if stringNeedsUnescapeFlags(tok.Flags, raw) {
				*(*string)(p) = bytesToString(unescapeJSONString(raw))
			} else {
				*(*string)(p) = *(*string)(unsafe.Pointer(&raw))
			}
			return nil
		}
	}
	return &ErrTypeMismatch{SrcKind: tok.Kind.String(), DstType: fi.typ.String()}
}

func directSyntaxError(tok token, msg string) error {
	return &SyntaxError{Offset: int64(tok.Start), Msg: msg}
}
