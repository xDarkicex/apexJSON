package apexjson

import (
	"time"
	"unsafe"
)

// fusedParser is the production DOM/tape path. It combines structural
// classification, grammar consumption, and node emission in one forward
// pass. Unlike the simdjson two-stage design, it does not materialize a
// second structural index: this parser's off-heap node tape is the reusable
// representation for queries.
//
// The parser uses Go's growing call stack for nesting rather than imposing an
// application-defined depth limit. The tape remains off-heap; only the
// parser's transient control state lives on the stack.
type fusedParser struct {
	doc     *parsedDoc
	src     []byte
	srcBase unsafe.Pointer
	pos     int
	end     int
	err     error
	stats   *parseStats
}

// parseStats is deliberately internal and opt-in. The production parser has
// no timing or counter work unless a benchmark/debug caller attaches it.
// Stage timings are diagnostic rather than throughput measurements: attaching
// stats adds a clock read around each scalar scan.
type parseStats struct {
	TotalNS uint64

	WhitespaceNS  uint64
	StringScanNS  uint64
	NumberScanNS  uint64
	LiteralScanNS uint64

	WhitespaceBytes uint64
	Strings         uint64
	StringBytes     uint64
	Numbers         uint64
	NumberBytes     uint64
	Literals        uint64
	Objects         uint64
	Arrays          uint64
	Keys            uint64
	Nodes           uint64
}

func (p *fusedParser) reset(doc *parsedDoc, src []byte) {
	p.doc = doc
	p.src = src
	if len(src) != 0 {
		p.srcBase = unsafe.Pointer(&src[0])
	} else {
		p.srcBase = nil
	}
	p.pos = 0
	p.end = len(src)
	p.err = nil
}

// byteAt is used only after the parser has established pos < end. Keeping the
// source base lets the grammar loop avoid rebuilding slice bounds checks for
// every punctuation byte.
func (p *fusedParser) byteAt(pos int) byte {
	return *(*byte)(unsafe.Add(p.srcBase, uintptr(pos)))
}

//go:noinline
func (p *fusedParser) failAt(offset int, msg string) bool {
	if p.err == nil {
		p.err = &SyntaxError{Offset: int64(offset), Msg: msg}
	}
	return false
}

func (p *fusedParser) skipWS() {
	if p.pos >= p.end || p.byteAt(p.pos) > ' ' {
		return
	}
	if p.stats != nil {
		start := p.pos
		started := time.Now()
		for p.pos < p.end && isWS(p.byteAt(p.pos)) {
			p.pos++
		}
		p.stats.WhitespaceBytes += uint64(p.pos - start)
		p.stats.WhitespaceNS += uint64(time.Since(started))
		return
	}
	if p.end-p.pos >= swWordBytes {
		p.pos = int(swSkipWS(p.src, uint32(p.pos), uint32(p.end)))
		return
	}
	for p.pos < p.end && isWS(p.byteAt(p.pos)) {
		p.pos++
	}
}

func (p *fusedParser) parseDocument() (int32, bool) {
	p.skipWS()
	idx, ok := p.parseValue()
	if !ok {
		return noNode, false
	}
	p.skipWS()
	if p.pos != p.end {
		return noNode, p.failAt(p.pos, "unexpected trailing input")
	}
	return idx, true
}

func (p *fusedParser) parseValue() (int32, bool) {
	if p.pos >= p.end {
		return noNode, p.failAt(p.pos, "unexpected end of input")
	}
	start := p.pos
	switch p.byteAt(p.pos) {
	case '{':
		p.pos++
		return p.parseObject(start)
	case '[':
		p.pos++
		return p.parseArray(start)
	case '"':
		var strStart uint32
		var end uint32
		var flags tokenFlags
		var ok bool
		if p.stats == nil {
			strStart, end, flags, ok = p.scanStringFast()
		} else {
			strStart, end, flags, ok = p.scanStringTimed()
		}
		if !ok {
			return noNode, false
		}
		return p.doc.slab.newLeafFlags(nodeString, strStart, end, flags), true
	case 't':
		var ok bool
		if p.stats == nil {
			ok = p.scanLiteralFast("true")
		} else {
			ok = p.scanLiteralTimed("true")
		}
		if !ok {
			return noNode, false
		}
		return p.doc.slab.newLeaf(nodeTrue, uint32(start), uint32(p.pos)), true
	case 'f':
		var ok bool
		if p.stats == nil {
			ok = p.scanLiteralFast("false")
		} else {
			ok = p.scanLiteralTimed("false")
		}
		if !ok {
			return noNode, false
		}
		return p.doc.slab.newLeaf(nodeFalse, uint32(start), uint32(p.pos)), true
	case 'n':
		var ok bool
		if p.stats == nil {
			ok = p.scanLiteralFast("null")
		} else {
			ok = p.scanLiteralTimed("null")
		}
		if !ok {
			return noNode, false
		}
		return p.doc.slab.newLeaf(nodeNull, uint32(start), uint32(p.pos)), true
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var ok bool
		if p.stats == nil {
			ok = p.scanNumberFast(start)
		} else {
			ok = p.scanNumberTimed(start)
		}
		if !ok {
			return noNode, false
		}
		return p.doc.slab.newLeaf(nodeNumber, uint32(start), uint32(p.pos)), true
	default:
		return noNode, p.failAt(start, "invalid token")
	}
}

func (p *fusedParser) parseObject(start int) (int32, bool) {
	if p.stats != nil {
		p.stats.Objects++
	}
	idx := p.doc.slab.openContainer(nodeObject, uint32(start))
	p.skipWS()
	if p.pos < p.end && p.byteAt(p.pos) == '}' {
		p.pos++
		p.doc.slab.closeContainer(idx, uint32(p.pos))
		return idx, true
	}

	for {
		if p.pos >= p.end || p.byteAt(p.pos) != '"' {
			return noNode, p.failAt(p.pos, "expected string key")
		}
		var keyStart, keyEnd uint32
		var flags tokenFlags
		var ok bool
		if p.stats == nil {
			keyStart, keyEnd, flags, ok = p.scanStringFast()
		} else {
			keyStart, keyEnd, flags, ok = p.scanStringTimed()
		}
		if !ok {
			return noNode, false
		}
		if p.stats != nil {
			p.stats.Keys++
		}
		p.doc.slab.newLeafFlags(nodeString, keyStart, keyEnd, flags)

		// The string scanner leaves pos at the byte immediately after the
		// closing quote. In valid compact JSON that byte is the colon, so keep
		// the common transition in this loop instead of paying a helper call for
		// every field. Whitespace is checked before calling the SWAR skipper.
		if p.pos >= p.end {
			return noNode, p.failAt(p.pos, "expected ':' after object key")
		}
		if p.byteAt(p.pos) <= ' ' {
			p.skipWS()
			if p.pos >= p.end {
				return noNode, p.failAt(p.pos, "expected ':' after object key")
			}
		}
		if p.byteAt(p.pos) != ':' {
			return noNode, p.failAt(p.pos, "expected ':' after object key")
		}
		p.pos++
		if p.pos < p.end && p.byteAt(p.pos) <= ' ' {
			p.skipWS()
		}
		if _, ok := p.parseValue(); !ok {
			return noNode, false
		}

		// Consume the value delimiter and, for a comma, position the next
		// iteration directly at its key. This is the grammar hot path; the
		// error cases stay in the surrounding branch and do not allocate on
		// valid input.
		if p.pos >= p.end {
			return noNode, p.failAt(p.pos, "expected ',' or '}'")
		}
		if p.byteAt(p.pos) <= ' ' {
			p.skipWS()
			if p.pos >= p.end {
				return noNode, p.failAt(p.pos, "expected ',' or '}'")
			}
		}
		switch p.byteAt(p.pos) {
		case '}':
			p.pos++
			p.doc.slab.closeContainer(idx, uint32(p.pos))
			return idx, true
		case ',':
			p.pos++
			if p.pos < p.end && p.byteAt(p.pos) <= ' ' {
				p.skipWS()
			}
		default:
			return noNode, p.failAt(p.pos, "expected ',' or '}'")
		}
	}
}

func (p *fusedParser) parseArray(start int) (int32, bool) {
	if p.stats != nil {
		p.stats.Arrays++
	}
	idx := p.doc.slab.openContainer(nodeArray, uint32(start))
	p.skipWS()
	if p.pos < p.end && p.byteAt(p.pos) == ']' {
		p.pos++
		p.doc.slab.closeContainer(idx, uint32(p.pos))
		return idx, true
	}

	for {
		if _, ok := p.parseValue(); !ok {
			return noNode, false
		}
		if p.pos >= p.end {
			return noNode, p.failAt(p.pos, "expected ',' or ']'")
		}
		if p.byteAt(p.pos) <= ' ' {
			p.skipWS()
			if p.pos >= p.end {
				return noNode, p.failAt(p.pos, "expected ',' or ']'")
			}
		}
		switch p.byteAt(p.pos) {
		case ']':
			p.pos++
			p.doc.slab.closeContainer(idx, uint32(p.pos))
			return idx, true
		case ',':
			p.pos++
			if p.pos < p.end && p.byteAt(p.pos) <= ' ' {
				p.skipWS()
			}
			if p.pos >= p.end || p.byteAt(p.pos) == ']' {
				return noNode, p.failAt(p.pos, "trailing comma in array")
			}
		default:
			return noNode, p.failAt(p.pos, "expected ',' or ']'")
		}
	}
}

func (p *fusedParser) scanLiteralFast(lit string) bool {
	start := p.pos
	if p.end-p.pos < len(lit) {
		p.pos = p.end
		return p.failAt(start, "invalid literal")
	}
	for i := 0; i < len(lit); i++ {
		if p.byteAt(p.pos+i) != lit[i] {
			return p.failAt(p.pos+i, "invalid literal")
		}
	}
	p.pos += len(lit)
	return true
}

func (p *fusedParser) scanLiteralTimed(lit string) bool {
	start := p.pos
	stats := p.stats
	var started time.Time
	if stats != nil {
		started = time.Now()
		stats.Literals++
	}
	defer func() {
		if stats != nil {
			stats.LiteralScanNS += uint64(time.Since(started))
		}
	}()
	if p.end-p.pos < len(lit) {
		p.pos = p.end
		return p.failAt(start, "invalid literal")
	}
	for i := 0; i < len(lit); i++ {
		if p.byteAt(p.pos+i) != lit[i] {
			return p.failAt(p.pos+i, "invalid literal")
		}
	}
	p.pos += len(lit)
	return true
}

func (p *fusedParser) scanNumberFast(start int) bool {
	pos := start
	if p.byteAt(pos) == '-' {
		pos++
		if pos >= p.end || p.byteAt(pos) < '0' || p.byteAt(pos) > '9' {
			p.pos = pos
			return p.failAt(pos, "invalid number")
		}
	}
	if p.byteAt(pos) == '0' {
		pos++
	} else {
		if pos >= p.end || p.byteAt(pos) < '1' || p.byteAt(pos) > '9' {
			p.pos = pos
			return p.failAt(pos, "invalid number")
		}
		pos = p.scanDigits(pos)
	}
	if pos < p.end && p.byteAt(pos) == '.' {
		pos++
		fracStart := pos
		pos = p.scanDigits(pos)
		if pos == fracStart {
			p.pos = pos
			return p.failAt(pos, "invalid number fraction")
		}
	}
	if pos < p.end && (p.byteAt(pos) == 'e' || p.byteAt(pos) == 'E') {
		pos++
		if pos < p.end && (p.byteAt(pos) == '+' || p.byteAt(pos) == '-') {
			pos++
		}
		expStart := pos
		pos = p.scanDigits(pos)
		if pos == expStart {
			p.pos = pos
			return p.failAt(pos, "invalid number exponent")
		}
	}
	p.pos = pos
	return true
}

func (p *fusedParser) scanNumberTimed(start int) bool {
	stats := p.stats
	var started time.Time
	if stats != nil {
		started = time.Now()
		stats.Numbers++
	}
	defer func() {
		if stats != nil {
			stats.NumberScanNS += uint64(time.Since(started))
			if p.pos >= start {
				stats.NumberBytes += uint64(p.pos - start)
			}
		}
	}()
	pos := start
	if p.byteAt(pos) == '-' {
		pos++
		if pos >= p.end || p.byteAt(pos) < '0' || p.byteAt(pos) > '9' {
			p.pos = pos
			return p.failAt(pos, "invalid number")
		}
	}
	if p.byteAt(pos) == '0' {
		pos++
	} else {
		if pos >= p.end || p.byteAt(pos) < '1' || p.byteAt(pos) > '9' {
			p.pos = pos
			return p.failAt(pos, "invalid number")
		}
		pos = p.scanDigits(pos)
	}
	if pos < p.end && p.byteAt(pos) == '.' {
		pos++
		fracStart := pos
		pos = p.scanDigits(pos)
		if pos == fracStart {
			p.pos = pos
			return p.failAt(pos, "invalid number fraction")
		}
	}
	if pos < p.end && (p.byteAt(pos) == 'e' || p.byteAt(pos) == 'E') {
		pos++
		if pos < p.end && (p.byteAt(pos) == '+' || p.byteAt(pos) == '-') {
			pos++
		}
		expStart := pos
		pos = p.scanDigits(pos)
		if pos == expStart {
			p.pos = pos
			return p.failAt(pos, "invalid number exponent")
		}
	}
	p.pos = pos
	return true
}

func (p *fusedParser) scanDigits(pos int) int {
	if p.end-pos < swWordBytes {
		for pos < p.end && p.byteAt(pos) >= '0' && p.byteAt(pos) <= '9' {
			pos++
		}
		return pos
	}
	return swFindNonDigitInt(p.src, pos, p.end)
}

func (p *fusedParser) scanStringFast() (uint32, uint32, tokenFlags, bool) {
	open := p.pos
	p.pos++
	contentStart := p.pos
	flags := tokenStringASCII
	for p.pos < p.end {
		event, ascii := swFindStringEventMetaInt(p.src, p.pos, p.end)
		if !ascii {
			flags &^= tokenStringASCII
		}
		if event == p.end {
			p.pos = p.end
			return 0, 0, 0, p.failAt(open, "unterminated string")
		}
		p.pos = event
		c := p.byteAt(p.pos)
		switch {
		case c == '"':
			end := p.pos
			p.pos++
			return uint32(contentStart), uint32(end), flags, true
		case c == '\\':
			flags |= tokenStringEscaped
			p.pos++
			if p.pos >= p.end {
				return 0, 0, 0, p.failAt(p.pos, "unterminated escape")
			}
			esc := p.byteAt(p.pos)
			p.pos++
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if p.end-p.pos < 4 {
					return 0, 0, 0, p.failAt(p.pos, "short unicode escape")
				}
				for i := 0; i < 4; i++ {
					if !isHex(p.byteAt(p.pos)) {
						return 0, 0, 0, p.failAt(p.pos, "invalid unicode escape")
					}
					p.pos++
				}
			default:
				return 0, 0, 0, p.failAt(p.pos-1, "invalid escape")
			}
		case c < 0x20:
			return 0, 0, 0, p.failAt(p.pos, "unescaped control character")
		default:
			p.pos++
		}
	}
	return 0, 0, 0, p.failAt(open, "unterminated string")
}

func (p *fusedParser) scanStringTimed() (uint32, uint32, tokenFlags, bool) {
	open := p.pos
	stats := p.stats
	var started time.Time
	if stats != nil {
		started = time.Now()
		stats.Strings++
	}
	defer func() {
		if stats != nil {
			stats.StringScanNS += uint64(time.Since(started))
			if p.pos >= open {
				stats.StringBytes += uint64(p.pos - open)
			}
		}
	}()
	p.pos++
	contentStart := p.pos
	flags := tokenStringASCII
	for p.pos < p.end {
		event, ascii := swFindStringEventMetaInt(p.src, p.pos, p.end)
		if !ascii {
			flags &^= tokenStringASCII
		}
		if event == p.end {
			p.pos = p.end
			return 0, 0, 0, p.failAt(open, "unterminated string")
		}
		p.pos = event
		c := p.byteAt(p.pos)
		switch {
		case c == '"':
			end := p.pos
			p.pos++
			return uint32(contentStart), uint32(end), flags, true
		case c == '\\':
			flags |= tokenStringEscaped
			p.pos++
			if p.pos >= p.end {
				return 0, 0, 0, p.failAt(p.pos, "unterminated escape")
			}
			esc := p.byteAt(p.pos)
			p.pos++
			switch esc {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if p.end-p.pos < 4 {
					return 0, 0, 0, p.failAt(p.pos, "short unicode escape")
				}
				for i := 0; i < 4; i++ {
					if !isHex(p.byteAt(p.pos)) {
						return 0, 0, 0, p.failAt(p.pos, "invalid unicode escape")
					}
					p.pos++
				}
			default:
				return 0, 0, 0, p.failAt(p.pos-1, "invalid escape")
			}
		case c < 0x20:
			return 0, 0, 0, p.failAt(p.pos, "unescaped control character")
		default:
			p.pos++
		}
	}
	return 0, 0, 0, p.failAt(open, "unterminated string")
}
