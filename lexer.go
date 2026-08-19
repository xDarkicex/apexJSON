package apexJSON

// NewLexer creates a lexer with pooled buffers
func NewLexer(data []byte) *Lexer {
	return &Lexer{
		data:     data,
		tokenBuf: getTokenBuf(), // From tokenBufPool
	}
}

// NextToken returns the next JSON token using pooled buffers
func (l *Lexer) NextToken() Token {
	for l.pos < len(l.data) && isWhitespace(l.data[l.pos]) {
		l.pos++
	}

	if l.pos >= len(l.data) {
		return Token{Type: TokenEOF}
	}

	// Structural token fast path (no allocation)
	switch c := l.data[l.pos]; c {
	case '{':
		l.pos++
		return Token{Type: TokenObjectStart}
	case '}':
		l.pos++
		return Token{Type: TokenObjectEnd}
	case '[':
		l.pos++
		return Token{Type: TokenArrayStart}
	case ']':
		l.pos++
		return Token{Type: TokenArrayEnd}
	case ',':
		l.pos++
		return Token{Type: TokenComma}
	case ':':
		l.pos++
		return Token{Type: TokenColon}
	case '"':
		return l.readString()
	case 't':
		return l.readLiteral("true", TokenTrue)
	case 'f':
		return l.readLiteral("false", TokenFalse)
	case 'n':
		return l.readLiteral("null", TokenNull)
	}

	// Number detection (including negative)
	if c := l.data[l.pos]; c == '-' || (c >= '0' && c <= '9') {
		return l.readNumber()
	}

	return Token{Type: TokenError}
}

// readString parses JSON string with pooled buffer
func (l *Lexer) readString() Token {
	l.pos++ // Skip opening quote

	// Reset pooled buffer but keep allocated memory
	*l.tokenBuf = (*l.tokenBuf)[:0]

	for l.pos < len(l.data) {
		c := l.data[l.pos]

		switch {
		case c == '"':
			l.pos++
			return Token{
				Type:  TokenString,
				Value: *l.tokenBuf, // Reference pooled buffer
			}
		case c == '\\':
			l.pos++
			if l.pos >= len(l.data) {
				return Token{Type: TokenError}
			}
			l.handleEscape()
		case c < 0x20:
			return Token{Type: TokenError}
		default:
			*l.tokenBuf = append(*l.tokenBuf, c)
			l.pos++
		}
	}

	return Token{Type: TokenError}
}

// handleEscape processes escape sequences in strings
func (l *Lexer) handleEscape() {
	switch c := l.data[l.pos]; c {
	case '"', '\\', '/':
		*l.tokenBuf = append(*l.tokenBuf, c)
	case 'b':
		*l.tokenBuf = append(*l.tokenBuf, '\b')
	case 'f':
		*l.tokenBuf = append(*l.tokenBuf, '\f')
	case 'n':
		*l.tokenBuf = append(*l.tokenBuf, '\n')
	case 'r':
		*l.tokenBuf = append(*l.tokenBuf, '\r')
	case 't':
		*l.tokenBuf = append(*l.tokenBuf, '\t')
	case 'u':
		if l.pos+4 >= len(l.data) {
			l.err = getSyntaxError()
			return
		}

		// Parse 4 hex digits
		var code rune
		for i := 1; i <= 4; i++ {
			hex := l.data[l.pos+i]
			code <<= 4
			switch {
			case hex >= '0' && hex <= '9':
				code |= rune(hex - '0')
			case hex >= 'a' && hex <= 'f':
				code |= rune(hex - 'a' + 10)
			case hex >= 'A' && hex <= 'F':
				code |= rune(hex - 'A' + 10)
			default:
				l.err = getSyntaxError()
				return
			}
		}

		// Encode as UTF-8
		if code < 0x80 {
			*l.tokenBuf = append(*l.tokenBuf, byte(code))
		} else if code < 0x800 {
			*l.tokenBuf = append(*l.tokenBuf, byte(0xC0|(code>>6)))
			*l.tokenBuf = append(*l.tokenBuf, byte(0x80|(code&0x3F)))
		} else {
			*l.tokenBuf = append(*l.tokenBuf, byte(0xE0|(code>>12)))
			*l.tokenBuf = append(*l.tokenBuf, byte(0x80|((code>>6)&0x3F)))
			*l.tokenBuf = append(*l.tokenBuf, byte(0x80|(code&0x3F)))
		}
		l.pos += 4
	default:
		*l.tokenBuf = append(*l.tokenBuf, c)
	}
	l.pos++
}

// readLiteral parses JSON literals (true, false, null)
func (l *Lexer) readLiteral(lit string, tokenType TokenType) Token {
	litLen := len(lit)
	if l.pos+litLen <= len(l.data) {
		for i := 0; i < litLen; i++ {
			if l.data[l.pos+i] != lit[i] {
				return Token{Type: TokenError}
			}
		}
		l.pos += litLen
		return Token{Type: tokenType}
	}
	return Token{Type: TokenError}
}

// readNumber parses JSON number with zero allocations
func (l *Lexer) readNumber() Token {
	start := l.pos

	// Optional minus sign
	if l.data[l.pos] == '-' {
		l.pos++
	}

	// Integer part
	if l.pos >= len(l.data) {
		return Token{Type: TokenError}
	}

	// No leading zeros unless it's just "0"
	if l.data[l.pos] == '0' {
		l.pos++
	} else if l.data[l.pos] >= '1' && l.data[l.pos] <= '9' {
		l.pos++
		for l.pos < len(l.data) && isDigit(l.data[l.pos]) {
			l.pos++
		}
	} else {
		return Token{Type: TokenError}
	}

	// Fractional part
	if l.pos < len(l.data) && l.data[l.pos] == '.' {
		l.pos++
		if l.pos >= len(l.data) || !isDigit(l.data[l.pos]) {
			return Token{Type: TokenError}
		}
		for l.pos < len(l.data) && isDigit(l.data[l.pos]) {
			l.pos++
		}
	}

	// Exponent part
	if l.pos < len(l.data) && (l.data[l.pos] == 'e' || l.data[l.pos] == 'E') {
		l.pos++
		if l.pos < len(l.data) && (l.data[l.pos] == '+' || l.data[l.pos] == '-') {
			l.pos++
		}
		if l.pos >= len(l.data) || !isDigit(l.data[l.pos]) {
			return Token{Type: TokenError}
		}
		for l.pos < len(l.data) && isDigit(l.data[l.pos]) {
			l.pos++
		}
	}

	return Token{
		Type:  TokenNumber,
		Value: l.data[start:l.pos],
	}
}

// Close releases lexer resources back to pools
func (l *Lexer) Close() {
	if l.tokenBuf != nil {
		putTokenBuf(l.tokenBuf)
		l.tokenBuf = nil
	}
}
