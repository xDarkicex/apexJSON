package apexjson

import (
	"math/bits"
	"unsafe"
)

// Lexer: bytes → tokens. Tokens are pure byte-offset spans into the
// source `[]byte`; consumers materialize via `src[start:end]`.
//
// The lexer owns no string data and allocates nothing per token. Failed
// state is sticky: once a malformed byte or unterminated string is hit,
// subsequent Next() calls return KindEOF until Reset().

// Token kinds. JSON's surface is tiny — ~10 kinds.
const (
	kindInvalid tokKind = iota
	kindEOF
	kindError
	// Structural (single-byte punctuation).
	kindObjectStart // {
	kindObjectEnd   // }
	kindArrayStart  // [
	kindArrayEnd    // ]
	kindColon       // :
	kindComma       // ,
	// Scalars.
	kindString // "..." content (quotes NOT included in span)
	kindNumber // number literal span
	kindTrue   // true
	kindFalse  // false
	kindNull   // null
)

type tokKind uint8

type tokenFlags uint8

const (
	tokenStringEscaped tokenFlags = 1 << iota
	tokenStringASCII
	// tokenNumberInteger is set only by scanNumber after the full JSON
	// number grammar has been validated and no fraction or exponent was
	// present. It lets typed decoding avoid re-validating ordinary integers.
	tokenNumberInteger
)

func (k tokKind) String() string {
	switch k {
	case kindInvalid:
		return "invalid"
	case kindEOF:
		return "eof"
	case kindError:
		return "error"
	case kindObjectStart:
		return "{"
	case kindObjectEnd:
		return "}"
	case kindArrayStart:
		return "["
	case kindArrayEnd:
		return "]"
	case kindColon:
		return ":"
	case kindComma:
		return ","
	case kindString:
		return "string"
	case kindNumber:
		return "number"
	case kindTrue:
		return "true"
	case kindFalse:
		return "false"
	case kindNull:
		return "null"
	}
	return "?"
}

// Token is a 12-byte span into the source. No string payload; the
// consumer reads content via src[Start:End] (zero-copy) or copies when
// escapes force materialization.
type token struct {
	Start uint32 // inclusive byte offset
	End   uint32 // exclusive byte offset
	Kind  tokKind
	Flags tokenFlags
}

var scanPunct = [256]tokKind{
	'{': kindObjectStart,
	'}': kindObjectEnd,
	'[': kindArrayStart,
	']': kindArrayEnd,
	':': kindColon,
	',': kindComma,
}

// scanner tokenizes a single JSON document. Reset between documents.
// Not safe for concurrent use; caller owns a scanner per goroutine or
// serializes access.
type scanner struct {
	src     []byte
	srcBase unsafe.Pointer
	pos     int
	end     int
	failed  bool // sticky: once set, Next returns KindEOF
	// keyColonConsumed is set by nextObjectKey when a known field also
	// consumes its following colon. It is internal scanner state used by the
	// direct typed path to fuse key dispatch and grammar transition.
	keyColonConsumed bool
}

// reset re-initializes the scanner for a new document. No allocations.
func (s *scanner) reset(src []byte) {
	s.src = src
	if len(src) != 0 {
		s.srcBase = unsafe.Pointer(&src[0])
	} else {
		s.srcBase = nil
	}
	s.pos = 0
	s.end = len(src)
	s.failed = false
	s.keyColonConsumed = false
}

// byteAt is used only after the caller has established pos < end. The direct
// typed decoder is an offset-based hot path, so keep grammar byte loads out
// of slice indexing and its repeated bounds checks.
func (s *scanner) byteAt(pos int) byte {
	return *(*byte)(unsafe.Add(s.srcBase, uintptr(pos)))
}

// errOffset returns the current byte offset (for error reporting).
func (s *scanner) errOffset() int64 {
	return int64(s.pos)
}

// next produces the next token, skipping whitespace. ok=false on EOF
// or after a sticky error.
//
// Token lifetime: every token's span is src[Start:End]. The bytes
// referenced by the token are stable for the lifetime of the source
// []byte. No pool, no shared buffer.
func (s *scanner) next() (token, bool) {
	if s.failed {
		return token{Kind: kindEOF}, false
	}
	for s.pos < s.end && isWS(s.byteAt(s.pos)) {
		s.pos++
	}
	if s.pos >= s.end {
		return token{Kind: kindEOF}, false
	}
	start := s.pos
	c := s.byteAt(s.pos)
	s.pos++
	if kind := scanPunct[c]; kind != kindInvalid {
		return token{Start: uint32(start), End: uint32(s.pos), Kind: kind}, true
	}
	switch c {
	case '"':
		return s.scanString(start)
	case 't':
		return s.scanLiteral(start, kindTrue, "true")
	case 'f':
		return s.scanLiteral(start, kindFalse, "false")
	case 'n':
		return s.scanLiteral(start, kindNull, "null")
	case '-':
		return s.scanNumber(start)
	default:
		if c >= '0' && c <= '9' {
			return s.scanNumber(start)
		}
		s.fail()
		return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, false
	}
}

const (
	fieldIndexKnownMiss   int32 = -1
	fieldIndexNeedsLookup int32 = -2
)

// nextObjectKey is the typed-decoder key path. It consumes the key and, when
// the type has a bounded bitmap, resolves the field candidate while the key
// bytes are already in flight. This avoids scanning the key once in the
// lexer, again in a resolver, and again in bytesEqual.
func (s *scanner) nextObjectKey(ti *typeInfo) (token, int32, bool) {
	s.keyColonConsumed = false
	if s.failed {
		return token{Kind: kindEOF}, fieldIndexKnownMiss, false
	}
	for s.pos < s.end && isWS(s.byteAt(s.pos)) {
		s.pos++
	}
	if s.pos >= s.end {
		return token{Kind: kindEOF}, fieldIndexKnownMiss, false
	}
	start := s.pos
	if s.byteAt(s.pos) == '}' {
		s.pos++
		return token{Start: uint32(start), End: uint32(s.pos), Kind: kindObjectEnd}, fieldIndexKnownMiss, true
	}
	if s.byteAt(s.pos) != '"' {
		s.fail()
		return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
	}

	s.pos++
	// Most production schemas have unique first bytes for their fields. Keep
	// those keys on the existing SWAR string scanner and use the bitmap only
	// where multiple fields share a first byte. This preserves the fused
	// collision fast path without making every ordinary key scalar.
	if s.pos < s.end {
		base := s.srcBase
		first := *(*byte)(unsafe.Add(base, uintptr(s.pos)))
		if candidate := ti.fieldByFirst[first]; candidate >= 0 {
			// A cached unique field can be recognized without entering the
			// general string-event scanner. This is deliberately a lookahead:
			// unknown keys, collisions, escapes, and malformed strings fall
			// through to scanString so the normal validator remains authoritative.
			fi := &ti.fields[candidate]
			if tok, ok := s.scanKnownKey(fi.name); ok {
				return s.finishKnownKey(tok, candidate)
			}
			tok, ok := s.scanString(start)
			if !ok {
				return tok, fieldIndexKnownMiss, false
			}
			if int(tok.End-tok.Start) == len(fi.nameBytes) && bytesEqual(s.src[tok.Start:tok.End], fi.nameBytes) {
				return s.finishKnownKey(tok, candidate)
			}
			return tok, fieldIndexNeedsLookup, true
		}
	}
	contentStart := s.pos
	flags := tokenStringASCII
	bitmapActive := len(ti.fieldBitmap) != 0
	var candidates uint16
	if bitmapActive {
		if len(ti.fields) == maxBitmapFields {
			candidates = ^uint16(0)
		} else {
			candidates = uint16((uint32(1) << uint(len(ti.fields))) - 1)
		}
	}
	base := s.srcBase
	for s.pos < s.end {
		c := *(*byte)(unsafe.Add(base, uintptr(s.pos)))
		switch c {
		case '"':
			end := s.pos
			s.pos++
			tok := token{Start: uint32(contentStart), End: uint32(end), Kind: kindString, Flags: flags}
			if !bitmapActive {
				return tok, fieldIndexNeedsLookup, true
			}
			keyLen := end - contentStart
			if keyLen >= len(ti.fieldLengthMask) {
				return tok, fieldIndexNeedsLookup, true
			}
			candidates &= ti.fieldLengthMask[keyLen]
			if candidates == 0 {
				return tok, fieldIndexNeedsLookup, true
			}
			return s.finishKnownKey(tok, int32(bits.TrailingZeros16(candidates)))
		case '\\':
			flags |= tokenStringEscaped
			bitmapActive = false
			s.pos++
			if s.pos >= s.end {
				s.fail()
				return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
			}
			esc := *(*byte)(unsafe.Add(base, uintptr(s.pos)))
			s.pos++
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if s.end-s.pos < 4 {
					s.fail()
					return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
				}
				for i := 0; i < 4; i++ {
					if !isHex(*(*byte)(unsafe.Add(base, uintptr(s.pos)))) {
						s.fail()
						return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
					}
					s.pos++
				}
			default:
				s.fail()
				return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
			}
		case 0:
			// NUL is not valid JSON text and cannot be a string terminator.
			fallthrough
		default:
			if c < 0x20 {
				s.fail()
				return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
			}
			if bitmapActive {
				pos := s.pos - contentStart
				if pos >= len(ti.fieldLengthMask) {
					bitmapActive = false
				} else if candidates != 0 {
					candidates &= ti.fieldBitmap[pos*256+int(c)]
				}
			}
			if c >= 0x80 {
				flags &^= tokenStringASCII
			}
			s.pos++
		}
	}
	s.fail()
	return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, fieldIndexKnownMiss, false
}

func (s *scanner) finishKnownKey(tok token, fieldIndex int32) (token, int32, bool) {
	if s.consumeByte(':') {
		s.keyColonConsumed = true
	}
	return tok, fieldIndex, true
}

// scanKnownKey recognizes an exact, unescaped field name by lookahead. It is
// used only for a field whose first byte is unique in the destination type.
// A false result does not advance the scanner; the caller then invokes the
// complete string scanner, which handles unknown keys, escaped keys, and all
// malformed-input cases with the normal validation rules.
func (s *scanner) scanKnownKey(name string) (token, bool) {
	contentStart := s.pos
	if len(name) == 0 || s.end-contentStart < len(name)+1 {
		return token{}, false
	}
	base := s.srcBase
	end := contentStart + len(name)
	raw := s.src[contentStart:end]
	if *(*string)(unsafe.Pointer(&raw)) != name {
		return token{}, false
	}
	if *(*byte)(unsafe.Add(base, uintptr(end))) != '"' {
		return token{}, false
	}
	s.pos = end + 1
	return token{Start: uint32(contentStart), End: uint32(end), Kind: kindString, Flags: tokenStringASCII}, true
}

// consumeByte consumes one expected structural byte. Parser hot paths use it
// when the grammar already knows which punctuation must come next, avoiding a
// token construction and a switch for every colon.
func (s *scanner) consumeByte(want byte) bool {
	for s.pos < s.end && isWS(s.byteAt(s.pos)) {
		s.pos++
	}
	if s.pos >= s.end || s.byteAt(s.pos) != want {
		return false
	}
	s.pos++
	return true
}

// consumeSeparator consumes either a comma or the caller-provided closing
// delimiter. It returns false without consuming an invalid byte, preserving
// the current position for error reporting.
func (s *scanner) consumeSeparator(close byte) (byte, bool) {
	for s.pos < s.end && isWS(s.byteAt(s.pos)) {
		s.pos++
	}
	if s.pos >= s.end {
		return 0, false
	}
	c := s.byteAt(s.pos)
	if c != ',' && c != close {
		return c, false
	}
	s.pos++
	return c, true
}

// fail sets the sticky failed flag. Subsequent Next() calls return
// KindEOF without consuming more input.
func (s *scanner) fail() {
	s.failed = true
}

// scanString reads `"..."`, advancing past the closing quote. The
// returned token spans the content BETWEEN quotes (Start at byte after
// opening quote, End at byte of closing quote). The caller can copy
// src[Start:End] when escapes are absent (zero-copy), or run unescape
// when escapes are present.
func (s *scanner) scanString(openQuote int) (token, bool) {
	contentStart, contentEnd, flags, ok := s.scanStringSpan(openQuote)
	if !ok {
		return token{Start: uint32(contentStart), End: uint32(contentEnd), Kind: kindError}, false
	}
	return token{Start: uint32(contentStart), End: uint32(contentEnd), Kind: kindString, Flags: flags}, true
}

// scanStringSpan is the token-free form used by direct typed decoding. The
// caller has already consumed the opening quote. On failure, the returned
// span is the same error span that scanString historically exposed.
func (s *scanner) scanStringSpan(openQuote int) (contentStart, contentEnd int, flags tokenFlags, ok bool) {
	contentStart = s.pos
	flags = tokenStringASCII
	for s.pos < s.end {
		event, ascii := swFindStringEventMetaInt(s.src, s.pos, s.end)
		if !ascii {
			flags &^= tokenStringASCII
		}
		if event == s.end {
			s.pos = s.end
			break
		}
		s.pos = event
		c := s.byteAt(s.pos)
		switch {
		case c == '"':
			contentEnd = s.pos
			s.pos++
			return contentStart, contentEnd, flags, true
		case c == '\\':
			flags |= tokenStringEscaped
			// Escape sequence — we still need to find the closing
			// quote, but the caller will need to unescape. We don't
			// validate escape contents here; the decoder does.
			s.pos++
			if s.pos >= s.end {
				s.fail()
				return openQuote, s.pos, 0, false
			}
			esc := s.byteAt(s.pos)
			s.pos++
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				// ok
			case 'u':
				// Need 4 hex digits. Validate per RFC 8259.
				if s.end-s.pos < 4 {
					s.fail()
					return openQuote, s.pos, 0, false
				}
				for k := 0; k < 4; k++ {
					c := s.byteAt(s.pos)
					if !isHex(c) {
						s.fail()
						return openQuote, s.pos, 0, false
					}
					s.pos++
				}
			default:
				s.fail()
				return openQuote, s.pos, 0, false
			}
		case c < 0x20:
			// Unescaped control char — invalid JSON.
			s.fail()
			return openQuote, s.pos, 0, false
		default:
			// A conservative SWAR candidate was not an actual event.
			s.pos++
		}
	}
	// Unterminated string.
	s.fail()
	return openQuote, s.pos, 0, false
}

// scanLiteral verifies the next N bytes match the expected literal
// (true / false / null). JSON is case-sensitive — exact byte match.
func (s *scanner) scanLiteral(start int, kind tokKind, lit string) (token, bool) {
	n := len(lit)
	if s.end-s.pos+1 < n {
		// +1 because s.pos was already advanced past the first byte.
		s.fail()
		return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, false
	}
	for i := 1; i < n; i++ {
		if s.byteAt(s.pos) != lit[i] {
			s.fail()
			return token{Start: uint32(start), End: uint32(s.pos), Kind: kindError}, false
		}
		s.pos++
	}
	return token{Start: uint32(start), End: uint32(s.pos), Kind: kind}, true
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// scanNumber reads a JSON number literal. JSON number grammar:
//
//	number = [-] int [frac] [exp]
//	int = "0" | digit19 digit*
//	frac = "." digit+
//	exp = ("e" | "E") ["+" | "-"] digit+
//
// Returns the full literal span. The decoder parses the value lazily.
// The caller has already advanced past the first byte, which is either
// '-' or a digit. Leading zeros are forbidden ("01", "00" are errors).
func (s *scanner) scanNumber(start int) (token, bool) {
	end, flags, ok := s.scanNumberSpan(start)
	if !ok {
		return token{Start: uint32(start), End: uint32(end), Kind: kindError}, false
	}
	return token{Start: uint32(start), End: uint32(end), Kind: kindNumber, Flags: flags}, true
}

// scanNumberSpan is the token-free number scanner used by direct typed
// decoding. The caller has already consumed the first byte of the number.
func (s *scanner) scanNumberSpan(start int) (end int, flags tokenFlags, ok bool) {
	p := s.pos
	integer := true

	// Integer part.
	if s.byteAt(start) == '-' {
		// First char was '-'. We need at least one digit.
		if p >= s.end || s.byteAt(p) < '0' || s.byteAt(p) > '9' {
			s.fail()
			return p, 0, false
		}
		if s.byteAt(p) == '0' {
			// "-0" — accept single zero; following must be . e E or end.
			p++
		} else {
			// "-digit19" — consume more digits.
			p = s.scanDigits(p)
		}
	} else if s.byteAt(start) == '0' {
		// "0" — single zero; following must be . e E or end.
		p = start + 1
	} else {
		// digit19 — consume more digits.
		p = s.scanDigits(p)
	}

	// Fractional part.
	if p < s.end && s.byteAt(p) == '.' {
		integer = false
		p++
		fracStart := p
		p = s.scanDigits(p)
		if p == fracStart {
			s.fail()
			return p, 0, false
		}
	}

	// Exponent.
	if p < s.end && (s.byteAt(p) == 'e' || s.byteAt(p) == 'E') {
		integer = false
		p++
		if p < s.end && (s.byteAt(p) == '+' || s.byteAt(p) == '-') {
			p++
		}
		expStart := p
		p = s.scanDigits(p)
		if p == expStart {
			s.fail()
			return p, 0, false
		}
	}
	s.pos = p
	if integer {
		flags |= tokenNumberInteger
	}
	return s.pos, flags, true
}

func (s *scanner) scanDigits(pos int) int {
	if s.end-pos < swWordBytes {
		for pos < s.end && s.byteAt(pos) >= '0' && s.byteAt(pos) <= '9' {
			pos++
		}
		return pos
	}
	return swFindNonDigitInt(s.src, pos, s.end)
}
