package apexJSON

import (
	"strconv"
)

func (p *Parser) skipWhitespace() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *Parser) parseString() (int, []byte) {
	if p.pos >= len(p.data) || p.data[p.pos] != '"' {
		return TokenError, nil
	}

	start := p.pos
	p.pos++

	for p.pos < len(p.data) {
		if p.data[p.pos] == '\\' {
			p.pos += 2
			continue
		}
		if p.data[p.pos] == '"' {
			p.pos++
			return TokenString, p.data[start+1 : p.pos-1]
		}
		p.pos++
	}

	return TokenError, nil
}

func (p *Parser) parseStringToBuffer(buf *Buffer) int {
	if p.pos >= len(p.data) || p.data[p.pos] != '"' {
		return TokenError
	}

	p.pos++     // Skip opening quote
	buf.off = 0 // Reset buffer position

	for p.pos < len(p.data) {
		if p.data[p.pos] == '\\' {
			buf.WriteByte(p.data[p.pos+1])
			p.pos += 2
			continue
		}

		if p.data[p.pos] == '"' {
			p.pos++ // Skip closing quote
			return TokenString
		}

		buf.WriteByte(p.data[p.pos])
		p.pos++
	}

	return TokenError
}

func (p *Parser) parseNumber() (int, []byte) {
	start := p.pos

	// Check for negative sign
	if p.pos < len(p.data) && p.data[p.pos] == '-' {
		p.pos++
	}

	// Parse integer part
	if p.pos >= len(p.data) || !isDigit(p.data[p.pos]) {
		return TokenError, nil
	}

	// Skip first digit (could be 0)
	p.pos++

	// Parse remaining digits
	for p.pos < len(p.data) && isDigit(p.data[p.pos]) {
		p.pos++
	}

	// Parse fractional part
	if p.pos < len(p.data) && p.data[p.pos] == '.' {
		p.pos++

		// Must have at least one digit after decimal point
		if p.pos >= len(p.data) || !isDigit(p.data[p.pos]) {
			return TokenError, nil
		}

		// Skip fractional digits
		for p.pos < len(p.data) && isDigit(p.data[p.pos]) {
			p.pos++
		}
	}

	// Parse exponent
	if p.pos < len(p.data) && (p.data[p.pos] == 'e' || p.data[p.pos] == 'E') {
		p.pos++

		// Handle exponent sign
		if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
			p.pos++
		}

		// Must have at least one digit in exponent
		if p.pos >= len(p.data) || !isDigit(p.data[p.pos]) {
			return TokenError, nil
		}

		// Skip exponent digits
		for p.pos < len(p.data) && isDigit(p.data[p.pos]) {
			p.pos++
		}
	}

	return TokenNumber, p.data[start:p.pos]
}

func (p *Parser) matchLiteral(literal string) bool {
	if p.pos+len(literal) > len(p.data) {
		return false
	}

	for i := 0; i < len(literal); i++ {
		if p.data[p.pos+i] != literal[i] {
			return false
		}
	}

	p.pos += len(literal)
	return true
}

func skipValue(p *Parser) bool {
	p.skipWhitespace()

	if p.pos >= len(p.data) {
		return false
	}

	switch p.data[p.pos] {
	case '{': // object
		p.pos++ // Skip opening brace
		for {
			p.skipWhitespace()
			if p.pos >= len(p.data) {
				return false
			}

			if p.data[p.pos] == '}' {
				p.pos++ // Skip closing brace
				return true
			}

			if p.data[p.pos] == ',' {
				p.pos++ // Skip comma
				continue
			}

			// Skip key
			tokenType, _ := p.parseString()
			if tokenType != TokenString {
				return false
			}

			// Skip colon
			p.skipWhitespace()
			if p.pos >= len(p.data) || p.data[p.pos] != ':' {
				return false
			}
			p.pos++ // Skip colon

			// Skip value
			if !skipValue(p) {
				return false
			}
		}

	case '[': // array
		p.pos++ // Skip opening bracket
		for {
			p.skipWhitespace()
			if p.pos >= len(p.data) {
				return false
			}

			if p.data[p.pos] == ']' {
				p.pos++ // Skip closing bracket
				return true
			}

			if p.data[p.pos] == ',' {
				p.pos++ // Skip comma
				continue
			}

			// Skip value
			if !skipValue(p) {
				return false
			}
		}

	case '"': // string
		tokenType, _ := p.parseString()
		return tokenType == TokenString

	case 't': // true
		return p.matchLiteral("true")

	case 'f': // false
		return p.matchLiteral("false")

	case 'n': // null
		return p.matchLiteral("null")

	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9': // number
		tokenType, _ := p.parseNumber()
		return tokenType == TokenNumber

	default:
		return false
	}
}

// ExtractNumber extracts a number value at the current position
func (p *Parser) ExtractNumber() (float64, bool) {
	tokenType, value := p.parseNumber()
	if tokenType != TokenNumber {
		return 0, false
	}

	n, err := strconv.ParseFloat(GetString(value), 64)
	if err != nil {
		return 0, false
	}

	return n, true
}

// ExtractBool extracts a boolean value at the current position
func (p *Parser) ExtractBool() (bool, bool) {
	p.skipWhitespace()

	if p.pos >= len(p.data) {
		return false, false
	}

	if p.data[p.pos] == 't' {
		if p.matchLiteral("true") {
			return true, true
		}
	} else if p.data[p.pos] == 'f' {
		if p.matchLiteral("false") {
			return false, true
		}
	}

	return false, false
}

// ExtractString extracts a string value at the current position
func (p *Parser) ExtractString() (string, bool) {
	start := p.pos
	if p.data[p.pos] != '"' {
		return "", false
	}

	p.pos++ // Skip opening quote
	startContent := p.pos

	for p.pos < len(p.data) {
		if p.data[p.pos] == '\\' {
			// Need to process escapes, can't use direct slice
			p.pos = start
			tokenType, value := p.parseString()
			if tokenType != TokenString {
				return "", false
			}
			return GetString(value), true
		}

		if p.data[p.pos] == '"' {
			s := GetString(p.data[startContent:p.pos])
			p.pos++ // Skip closing quote
			return s, true
		}

		p.pos++
	}

	// Reset position if we didn't find closing quote
	p.pos = start
	return "", false
}

func countEscapeChars(s string) int {
	count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' {
			count++
		}
	}
	return count
}

// Helper function to check if a string needs JSON escaping
func needsEscaping(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' {
			return true
		}
	}
	return false
}
