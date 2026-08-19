// Package compat provides an encoding/json-shaped facade over ApexJSON.
//
// The native apexjson package is the performance-first API: it exposes
// reusable off-heap decoders, lazy Values, iterators, and pointer-oriented
// marshal entry points. This package is for applications that want to switch
// the common encoding/json import with minimal source changes.
//
// Marshal, MarshalIndent, Unmarshal, and the common streaming Decode path
// delegate to ApexJSON. Token traversal is built from ApexJSON's off-heap
// tape. Strict options that are not yet represented in the native decoder use
// a compatibility fallback for that individual operation.
package compat

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"

	core "github.com/xDarkicex/apexJSON/v2"
)

// Standard-library-compatible public types.
type (
	Delim                 = json.Delim
	InvalidUnmarshalError = json.InvalidUnmarshalError
	Marshaler             = json.Marshaler
	MarshalerError        = json.MarshalerError
	Number                = json.Number
	RawMessage            = json.RawMessage
	SyntaxError           = json.SyntaxError
	Token                 = json.Token
	UnmarshalTypeError    = json.UnmarshalTypeError
	Unmarshaler           = json.Unmarshaler
	UnsupportedTypeError  = json.UnsupportedTypeError
	UnsupportedValueError = json.UnsupportedValueError
)

// Marshal returns the JSON encoding of v.
//
// ApexJSON's encoder is used for the actual serialization. The compatibility
// facade applies encoding/json's default HTML escaping to preserve the usual
// output contract for strings containing <, >, &, U+2028, or U+2029.
func Marshal(v any) ([]byte, error) {
	if v != nil {
		rv := reflect.ValueOf(v)
		if !compatTypeSupported(rv.Type()) || containsNonFinite(rv) {
			return json.Marshal(v)
		}
	}
	raw, err := core.Marshal(v)
	if err != nil {
		return nil, err
	}
	return escapeHTMLIfNeeded(raw), nil
}

// MarshalIndent is the indented form of Marshal.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	raw, err := Marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, prefix, indent); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Unmarshal parses JSON into v using ApexJSON's direct typed decoder and its
// correctness-preserving fallback for unsupported destination shapes.
func Unmarshal(data []byte, v any) error {
	if v == nil {
		return json.Unmarshal(data, v)
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return json.Unmarshal(data, v)
	}
	if !compatTypeSupported(rv.Type()) {
		return json.Unmarshal(data, v)
	}
	if err := core.Unmarshal(data, v); err != nil {
		// The native path is deliberately optimized for the successful case.
		// On failure, ask encoding/json for its canonical error type/message
		// and its exact partial-write behavior.
		return json.Unmarshal(data, v)
	}
	return nil
}

// Valid reports whether data contains one valid JSON value.
func Valid(data []byte) bool { return json.Valid(data) }

// Compact appends compact JSON to dst.
func Compact(dst *bytes.Buffer, src []byte) error { return json.Compact(dst, src) }

// Indent appends indented JSON to dst.
func Indent(dst *bytes.Buffer, src []byte, prefix, indent string) error {
	return json.Indent(dst, src, prefix, indent)
}

// HTMLEscape appends HTML-safe JSON to dst.
func HTMLEscape(dst *bytes.Buffer, src []byte) { json.HTMLEscape(dst, src) }

// Encoder writes JSON to an io.Writer. It uses ApexJSON for the value
// serialization and applies the standard encoder's optional presentation
// transforms afterward.
type Encoder struct {
	w          io.Writer
	prefix     string
	indent     string
	escapeHTML bool
}

// NewEncoder returns an ApexJSON-backed compatibility encoder.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w, escapeHTML: true}
}

// Encode writes one JSON value followed by a newline.
func (e *Encoder) Encode(v any) error {
	if !e.escapeHTML && v != nil && (!compatTypeSupported(reflect.TypeOf(v)) || containsNonFinite(reflect.ValueOf(v))) {
		// Preserve stdlib's exact no-HTML behavior for complex/custom values;
		// the native encoder intentionally has different fast-path semantics
		// for some of these shapes.
		std := json.NewEncoder(e.w)
		std.SetEscapeHTML(false)
		if e.indent != "" {
			std.SetIndent(e.prefix, e.indent)
		}
		return std.Encode(v)
	}
	raw, err := Marshal(v)
	if err != nil {
		return err
	}
	if !e.escapeHTML {
		// Marshal applies the standard default escaping. Re-encode through the
		// core only for the explicitly disabled compatibility option.
		raw, err = core.Marshal(v)
		if err != nil {
			return err
		}
	}
	if e.indent != "" {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, e.prefix, e.indent); err != nil {
			return err
		}
		raw = pretty.Bytes()
	}
	out := append(raw, '\n')
	n, err := e.w.Write(out)
	if err == nil && n != len(out) {
		return io.ErrShortWrite
	}
	return err
}

// SetIndent specifies the indentation used by subsequent Encode calls.
func (e *Encoder) SetIndent(prefix, indent string) {
	e.prefix = prefix
	e.indent = indent
}

// SetEscapeHTML controls escaping of HTML-significant characters.
func (e *Encoder) SetEscapeHTML(on bool) { e.escapeHTML = on }

// Decoder is an encoding/json-shaped streaming adapter over ApexJSON. Input
// is read incrementally and each value is parsed through the native off-heap decoder.
// The adapter is intentionally less allocation-focused than the native
// Decoder because it retains stream/token compatibility state.
type Decoder struct {
	r               io.Reader
	buf             []byte
	base            int64
	pos             int
	eof             bool
	readErr         error
	emptyReads      int
	disallowUnknown bool
	useNumber       bool
	tokens          []json.Token
	tokenPos        int
	tokenEnd        int
}

// NewDecoder returns a compatibility decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// Decode reads the next JSON value and stores it in v. Plain typed
// destinations use ApexJSON's direct decoder; strict unknown-field and
// interface-number options use encoding/json for that operation until their
// corresponding native flags are added.
func (d *Decoder) Decode(v any) error {
	if d.tokens != nil {
		if d.tokenPos < len(d.tokens) {
			return errors.New("apexjson/compat: consume the current Token sequence before Decode")
		}
		d.finishTokens()
	}
	start, end, err := d.nextSpan()
	if err != nil {
		return err
	}
	d.pos = end
	// The native direct decoder may borrow clean string bytes. Give each
	// decoded value stable ownership before compacting the stream buffer.
	src := append([]byte(nil), d.buf[start:end]...)
	if d.disallowUnknown || (d.useNumber && destinationContainsInterface(reflect.TypeOf(v))) {
		err = decodeWithStdOptions(src, v, d.useNumber, d.disallowUnknown)
	} else {
		err = Unmarshal(src, v)
	}
	d.compact()
	return err
}

func decodeWithStdOptions(src []byte, v any, useNumber, disallowUnknown bool) error {
	dec := json.NewDecoder(bytes.NewReader(src))
	if useNumber {
		dec.UseNumber()
	}
	if disallowUnknown {
		dec.DisallowUnknownFields()
	}
	return dec.Decode(v)
}

// Token returns the next token from the current JSON value. Tokens are
// materialized from ApexJSON's lazy tape because the stdlib API requires an
// interface-valued result.
func (d *Decoder) Token() (json.Token, error) {
	if d.tokens == nil {
		if err := d.loadTokens(); err != nil {
			return nil, err
		}
	}
	if d.tokenPos >= len(d.tokens) {
		d.finishTokens()
		if err := d.loadTokens(); err != nil {
			return nil, err
		}
	}
	tok := d.tokens[d.tokenPos]
	d.tokenPos++
	return tok, nil
}

// More reports whether the current array or object has another element.
func (d *Decoder) More() bool {
	if d.tokens == nil {
		if err := d.loadTokens(); err != nil {
			return false
		}
	}
	if d.tokenPos >= len(d.tokens) {
		return false
	}
	switch tok := d.tokens[d.tokenPos].(type) {
	case json.Delim:
		return tok != ']' && tok != '}'
	default:
		return true
	}
}

// Buffered returns the unread portion of the adapter's input buffer.
func (d *Decoder) Buffered() io.Reader {
	return bytes.NewReader(d.buf[d.pos:])
}

// InputOffset returns the byte offset at the current value boundary.
func (d *Decoder) InputOffset() int64 { return d.base + int64(d.pos) }

// UseNumber preserves JSON numbers as Number values when decoding into
// interface-containing destinations and when producing tokens.
func (d *Decoder) UseNumber() { d.useNumber = true }

// DisallowUnknownFields enables strict object decoding for subsequent values.
func (d *Decoder) DisallowUnknownFields() { d.disallowUnknown = true }

func (d *Decoder) loadTokens() error {
	start, end, err := d.nextSpan()
	if err != nil {
		return err
	}
	src := d.buf[start:end]
	native, err := core.NewDecoderSize(uint64(len(src))*32+4096, len(src)+16)
	if err != nil {
		return err
	}
	defer native.Free()
	if err := native.Parse(src); err != nil {
		return err
	}
	tokens, err := appendValueTokens(nil, native.Root(), d.useNumber)
	if err != nil {
		return err
	}
	d.tokens = tokens
	d.tokenPos = 0
	d.tokenEnd = end
	return nil
}

func (d *Decoder) finishTokens() {
	d.pos = d.tokenEnd
	d.tokens = nil
	d.tokenPos = 0
	d.tokenEnd = 0
	d.compact()
}

// nextSpan returns the next complete top-level JSON value. It asks the
// reader for more bytes only when the current buffer ends in the middle of a
// value, allowing consumed prefixes to be discarded after each Decode.
func (d *Decoder) nextSpan() (start, end int, err error) {
	for {
		start, end, err = nextJSONSpan(d.buf, d.pos, d.eof)
		if err == nil {
			return start, end, nil
		}
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			return 0, 0, err
		}
		if d.eof {
			if d.readErr != nil {
				return 0, 0, d.readErr
			}
			return 0, 0, err
		}
		if d.fill() {
			continue
		}
		// Re-run the scanner after an EOF or reader error so trailing
		// whitespace is reported as io.EOF and a complete value returned
		// together with a reader error is still delivered first.
	}
}

// fill appends one reader chunk. Readers may legally return (0, nil), so
// tolerate a bounded number of empty reads before reporting no progress.
func (d *Decoder) fill() bool {
	if d.eof {
		return false
	}
	if d.r == nil {
		d.eof = true
		d.readErr = errors.New("apexjson/compat: nil reader")
		return false
	}
	var chunk [32 * 1024]byte
	n, err := d.r.Read(chunk[:])
	if n > 0 {
		d.buf = append(d.buf, chunk[:n]...)
		d.emptyReads = 0
	} else if err == nil {
		d.emptyReads++
	}
	if err != nil {
		d.eof = true
		if err != io.EOF {
			d.readErr = err
		}
	}
	if n == 0 && err == nil && d.emptyReads >= 100 {
		d.eof = true
		d.readErr = io.ErrNoProgress
	}
	return n > 0
}

// compact drops bytes consumed through the current value. base preserves the
// absolute stream position while Buffered continues to expose unread bytes.
func (d *Decoder) compact() {
	if d.pos == 0 {
		return
	}
	oldPos := d.pos
	copy(d.buf, d.buf[oldPos:])
	d.buf = d.buf[:len(d.buf)-oldPos]
	d.base += int64(oldPos)
	d.pos = 0
}

func appendValueTokens(out []json.Token, v core.Value, useNumber bool) ([]json.Token, error) {
	switch v.Type() {
	case core.TypeObject:
		out = append(out, json.Delim('{'))
		it := v.ObjectIter()
		for it.Next() {
			out = append(out, tokenString(it.Key()))
			var err error
			out, err = appendValueTokens(out, it.Value(), useNumber)
			if err != nil {
				return nil, err
			}
		}
		return append(out, json.Delim('}')), nil
	case core.TypeArray:
		out = append(out, json.Delim('['))
		it := v.ArrayIter()
		for it.Next() {
			var err error
			out, err = appendValueTokens(out, it.Value(), useNumber)
			if err != nil {
				return nil, err
			}
		}
		return append(out, json.Delim(']')), nil
	case core.TypeString:
		return append(out, strings.Clone(v.Str())), nil
	case core.TypeNumber:
		raw := v.Bytes()
		if useNumber {
			return append(out, json.Number(string(raw))), nil
		}
		n, err := strconv.ParseFloat(string(raw), 64)
		if err != nil {
			return nil, err
		}
		return append(out, n), nil
	case core.TypeBool:
		return append(out, v.Bool()), nil
	case core.TypeNull:
		return append(out, nil), nil
	default:
		return nil, errors.New("apexjson/compat: invalid token value")
	}
}

// tokenString converts an object key's raw JSON contents into the decoded
// string required by encoding/json.Token. The native iterator intentionally
// exposes escaped key bytes, so only the uncommon escaped-key case needs the
// compatibility decoder.
func tokenString(raw string) string {
	if !strings.ContainsRune(raw, '\\') {
		return strings.Clone(raw)
	}
	quoted := make([]byte, 0, len(raw)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, raw...)
	quoted = append(quoted, '"')
	var decoded string
	if err := json.Unmarshal(quoted, &decoded); err != nil {
		return strings.Clone(raw)
	}
	return decoded
}

func destinationContainsInterface(t reflect.Type) bool {
	return destinationContainsInterfaceSeen(t, make(map[reflect.Type]bool))
}

func destinationContainsInterfaceSeen(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	defer delete(seen, t)
	switch t.Kind() {
	case reflect.Interface, reflect.Map:
		return true
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return destinationContainsInterfaceSeen(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			if t.Field(i).PkgPath == "" && destinationContainsInterfaceSeen(t.Field(i).Type, seen) {
				return true
			}
		}
	}
	return false
}

func nextJSONSpan(src []byte, pos int, final bool) (start, end int, err error) {
	for pos < len(src) {
		switch src[pos] {
		case ' ', '\t', '\r', '\n':
			pos++
		default:
			start = pos
			goto found
		}
	}
	if final {
		return 0, 0, io.EOF
	}
	return 0, 0, io.ErrUnexpectedEOF

found:
	switch src[start] {
	case '{', '[':
		depth := 1
		inString := false
		escaped := false
		for i := start + 1; i < len(src); i++ {
			b := src[i]
			if inString {
				if escaped {
					escaped = false
				} else if b == '\\' {
					escaped = true
				} else if b == '"' {
					inString = false
				}
				continue
			}
			switch b {
			case '"':
				inString = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return start, i + 1, nil
				}
			}
		}
		return 0, 0, io.ErrUnexpectedEOF
	case '"':
		escaped := false
		for i := start + 1; i < len(src); i++ {
			if escaped {
				escaped = false
				continue
			}
			switch src[i] {
			case '\\':
				escaped = true
			case '"':
				return start, i + 1, nil
			}
		}
		return 0, 0, io.ErrUnexpectedEOF
	default:
		for i := start; i < len(src); i++ {
			switch src[i] {
			case ' ', '\t', '\r', '\n':
				return start, i, nil
			}
		}
		if !final {
			return 0, 0, io.ErrUnexpectedEOF
		}
		return start, len(src), nil
	}
}

func escapeHTMLIfNeeded(src []byte) []byte {
	if !needsHTMLEscape(src) {
		return src
	}
	var out bytes.Buffer
	json.HTMLEscape(&out, src)
	return out.Bytes()
}

func needsHTMLEscape(src []byte) bool {
	for i, b := range src {
		switch b {
		case '<', '>', '&':
			return true
		case 0xe2:
			if i+2 < len(src) && src[i+1] == 0x80 && (src[i+2] == 0xa8 || src[i+2] == 0xa9) {
				return true
			}
		}
	}
	return false
}

var (
	marshalerType       = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	unmarshalerType     = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	textMarshalerType   = reflect.TypeOf((*encodingTextMarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encodingTextUnmarshaler)(nil)).Elem()
)

// These local interfaces keep the compatibility package independent of
// implementation details while recognizing the same extension points as the
// standard library. Complex/custom types use encoding/json so embedded-field,
// map, interface, RawMessage, and custom-method semantics remain exact.
type encodingTextMarshaler interface {
	MarshalText() ([]byte, error)
}

type encodingTextUnmarshaler interface {
	UnmarshalText([]byte) error
}

// compatTypeSupported describes the deliberately conservative set for which
// the ApexJSON typed path has the same layout semantics as encoding/json.
// Complex shapes remain fully compatible by using the standard fallback.
func compatTypeSupported(t reflect.Type) bool {
	return compatTypeSupportedSeen(t, make(map[reflect.Type]bool))
}

func compatTypeSupportedSeen(t reflect.Type, seen map[reflect.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	defer delete(seen, t)

	if t.Implements(marshalerType) || t.Implements(unmarshalerType) ||
		t.Implements(textMarshalerType) || t.Implements(textUnmarshalerType) {
		return false
	}
	if t.Kind() != reflect.Ptr {
		pt := reflect.PointerTo(t)
		if pt.Implements(marshalerType) || pt.Implements(unmarshalerType) ||
			pt.Implements(textMarshalerType) || pt.Implements(textUnmarshalerType) {
			return false
		}
	}

	switch t.Kind() {
	case reflect.Bool, reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Ptr:
		return compatTypeSupportedSeen(t.Elem(), seen)
	case reflect.Array:
		return compatTypeSupportedSeen(t.Elem(), seen)
	case reflect.Slice:
		// encoding/json base64-encodes []byte; ApexJSON's native []byte path
		// emits a JSON string, so keep this compatibility case on stdlib.
		if t.Elem().Kind() == reflect.Uint8 {
			return false
		}
		return compatTypeSupportedSeen(t.Elem(), seen)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				return false
			}
			if f.PkgPath != "" {
				continue
			}
			tag, ok := f.Tag.Lookup("json")
			if ok {
				parts := strings.Split(tag, ",")
				if parts[0] == "-" {
					continue
				}
				// The native fast path currently handles ordinary names only.
				// Let stdlib handle ,string and future tag options exactly.
				if len(parts) > 1 {
					return false
				}
			}
			// Keep struct fields on the strict scalar record path. The
			// standard library has important null/empty and nested-container
			// semantics that the native direct decoder intentionally optimizes
			// differently.
			switch f.Type.Kind() {
			case reflect.Bool, reflect.String,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
				reflect.Float32, reflect.Float64:
			default:
				return false
			}
			if !compatTypeSupportedSeen(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}

func containsNonFinite(v reflect.Value) bool {
	return containsNonFiniteSeen(v, make(map[visit]bool))
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

func containsNonFiniteSeen(v reflect.Value, seen map[visit]bool) bool {
	if !v.IsValid() {
		return false
	}
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return false
		}
		if v.Kind() == reflect.Pointer {
			key := visit{typ: v.Type(), ptr: v.Pointer()}
			if seen[key] {
				return false
			}
			seen[key] = true
			defer delete(seen, key)
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		return math.IsNaN(f) || math.IsInf(f, 0)
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if containsNonFiniteSeen(v.Index(i), seen) {
				return true
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.PkgPath != "" || f.Anonymous {
				continue
			}
			tag, ok := f.Tag.Lookup("json")
			if ok && strings.Split(tag, ",")[0] == "-" {
				continue
			}
			if containsNonFiniteSeen(v.Field(i), seen) {
				return true
			}
		}
	}
	return false
}
