package apexJSON

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
)

// Token types
const (
	TokenError = iota
	TokenObjectStart
	TokenObjectEnd
	TokenArrayStart
	TokenArrayEnd
	TokenString
	TokenNumber
	TokenBool
	TokenNull
	TokenColon
	TokenComma
)

const hex = "0123456789abcdef"

const (
	FloatPrecision2     = "%.2f"
	FloatPrecision3     = "%.3f"
	FloatPrecision4     = "%.4f"
	FloatGeneral        = "%g"
	FloatScientificE    = "%e"
	FloatScientificCapE = "%E"
	IntFormat           = "%d"
	IntHex              = "%x"  // Hexadecimal lowercase
	IntHexUpper         = "%X"  // Hexadecimal uppercase
	IntBinary           = "%b"  // Binary
	IntOctal            = "%o"  // Octal
	FloatComma          = "%'f" // With thousand separators (Go 1.23+)
)

var (
	jsonQuoteColon   = []byte{'"', ':'}
	jsonQuote        = []byte{'"'}[0]
	jsonColon        = []byte{':', '"'}
	jsonComma        = []byte{','}[0]
	jsonTrue         = []byte("true")
	jsonFalse        = []byte("false")
	jsonNull         = []byte("null")
	jsonOpenBrace    = []byte{'{'}[0]
	jsonCloseBrace   = []byte{'}'}[0]
	jsonOpenBracket  = []byte{'['}[0]
	jsonCloseBracket = []byte{']'}[0]
	jsonQuoteComma   = []byte{'"', ','}
	jsonEmpty        = []byte("empty")
	jsonHexPrefix    = []byte("0x")
	jsonMapOpen      = []byte("map[")
	jsonMapClose     = []byte("]")
	jsonNewline      = []byte{'\n'}
)
var escapeMap = [256][]byte{
	'"':  []byte(`\"`),
	'\\': []byte(`\\`),
	'\n': []byte(`\n`),
	'\r': []byte(`\r`),
	'\t': []byte(`\t`),
}

func (e *SyntaxError) Error() string {
	b := getBuilder()
	defer putBuilder(b)
	b.WriteString("json syntax error at offset ")
	b.WriteString(strconv.FormatInt(e.Offset, 10))
	b.WriteString(": ")
	b.WriteString(e.Msg)

	return b.String()
}

func (e *UnmarshalTypeError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("json: cannot unmarshal %s into Go struct field %s of type %s",
			e.Value, e.Field, e.Type.String())
	}
	return fmt.Sprintf("json: cannot unmarshal %s into Go value of type %s", e.Value, e.Type.String())
}

// ### Core Functions ###

func Marshal(v interface{}) ([]byte, error) {
	buf := getBuffer()
	defer putBuffer(buf)

	if err := marshalValue(reflect.ValueOf(v), buf); err != nil {
		return nil, err
	}

	// Create a copy of the buffer contents
	result := make([]byte, buf.off)
	copy(result, buf.buf[:buf.off])
	return result, nil
}
func Unmarshal(data []byte, v interface{}) error {
	p := NewParser(data)
	// should I defer p.Close()?
	return unmarshalValue(p, reflect.ValueOf(v).Elem())
}

func NewParser(data []byte) *Parser {
	return &Parser{
		data: data,
		pos:  0,
	}
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		w:          w,
		buf:        getBufferSize(2048),
		escapeHTML: true,
	}
}

func NewDecoder(r io.Reader) *Decoder {
	fmt.Println("Creating new decoder")
	d := &Decoder{
		r:         r,
		buf:       make([]byte, 0, 4096),
		tokenBuf:  *getTokenBuf(),
		useNumber: false,
	}
	fmt.Printf("NewDecoder: buf len=%d, cap=%d\n", len(d.buf), cap(d.buf))
	d.readPos = 0
	return d
}

func (e *Encoder) Encode(v interface{}) error {
	e.buf.Reset()

	if err := marshalValue(reflect.ValueOf(v), e.buf); err != nil {
		return err
	}

	// Write the encoded value followed by a newline
	if _, err := e.w.Write(e.buf.Bytes()); err != nil {
		return err
	}
	if _, err := e.w.Write(jsonNewline); err != nil {
		return err
	}

	return nil
}

func (d *Decoder) Decode(v interface{}) error {
	fmt.Printf("Decode called: buf len=%d, readPos=%d\n", len(d.buf), d.readPos)
	// Skip any whitespace
	if err := d.skipWhitespace(); err != nil {
		if err == io.EOF {
			return io.EOF
		}
		return err
	}

	// Create a parser from the buffer
	value, err := d.readValue()
	if err != nil {
		return err
	}

	// Unmarshal the value
	err = Unmarshal(value, v)

	// Check if it's a pooled SyntaxError and return it to the pool
	if syntaxErr, ok := err.(*SyntaxError); ok {
		// Create a copy of the error information
		errCopy := fmt.Errorf("json syntax error at offset %d: %s",
			syntaxErr.Offset, syntaxErr.Msg)

		// Return the original error to the pool
		putSyntaxError(syntaxErr)

		return errCopy
	}

	// For other error types, just return them directly
	return err
}

func (e *Encoder) SetEscapeHTML(on bool) {
	e.escapeHTML = on
}

// ValueType returns the type of the current JSON value
func (p *Parser) ValueType() int {
	p.skipWhitespace()
	if p.pos >= len(p.data) {
		return TokenError
	}

	switch p.data[p.pos] {
	case '{':
		return TokenObjectStart
	case '}':
		return TokenObjectEnd
	case '[':
		return TokenArrayStart
	case ']':
		return TokenArrayEnd
	case '"':
		return TokenString
	case 't', 'f':
		return TokenBool
	case 'n':
		return TokenNull
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return TokenNumber
	case ':':
		return TokenColon
	case ',':
		return TokenComma
	}

	return TokenError
}

func (d *Decoder) Close() {
	putTokenBuf(&d.tokenBuf)
	d.tokenBuf = nil
}

// GetObject extracts a map from JSON at the specified path
func GetObject(data []byte, path ...string) (map[string]interface{}, bool) {
	value, ok := Extract(data, path...)
	if !ok {
		return nil, false
	}

	p := NewParser(value)

	// Create a deferred error handler
	var syntaxErr *SyntaxError = nil
	defer func() {
		if syntaxErr != nil {
			putSyntaxError(syntaxErr)
		}
	}()

	// Check if this is actually an object
	if p.ValueType() != TokenObjectStart {
		return nil, false
	}

	// Get map from pool
	result := getObjectMap()

	// Skip the opening brace
	p.pos++

	// Parse all key-value pairs
	for {
		p.skipWhitespace()

		// Check for end of object
		if p.pos >= len(p.data) || p.data[p.pos] == '}' {
			p.pos++ // Skip closing brace
			break
		}

		// Parse key
		tokenType, keyBytes := p.parseString()
		if tokenType != TokenString {
			syntaxErr = getSyntaxError()
			syntaxErr.Offset = int64(p.pos)
			syntaxErr.Msg = "expected string key in object"
			putObjectMap(result)
			return nil, false
		}

		key := GetString(keyBytes)

		// Skip colon
		p.skipWhitespace()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			syntaxErr = getSyntaxError()
			syntaxErr.Offset = int64(p.pos)
			syntaxErr.Msg = "expected colon after object key"
			putObjectMap(result)
			return nil, false
		}
		p.pos++

		// Parse value based on type
		p.skipWhitespace()
		switch p.ValueType() {
		case TokenString:
			if val, ok := p.ExtractString(); ok {
				result[key] = val
			}
		case TokenNumber:
			if val, ok := p.ExtractNumber(); ok {
				result[key] = val
			}
		case TokenBool:
			if val, ok := p.ExtractBool(); ok {
				result[key] = val
			}
		case TokenNull:
			p.matchLiteral("null")
			result[key] = nil
		case TokenObjectStart:
			if obj, ok := GetObject(p.data[p.pos:], ""); ok {
				result[key] = obj
				// Skip the object we just parsed
				depth := 1
				p.pos++
				for depth > 0 && p.pos < len(p.data) {
					if p.data[p.pos] == '{' {
						depth++
					} else if p.data[p.pos] == '}' {
						depth--
					}
					p.pos++
				}
			}
		case TokenArrayStart:
			if arr, ok := GetArray(p.data[p.pos:], ""); ok {
				result[key] = arr
				// Skip the array we just parsed
				depth := 1
				p.pos++
				for depth > 0 && p.pos < len(p.data) {
					if p.data[p.pos] == '[' {
						depth++
					} else if p.data[p.pos] == ']' {
						depth--
					}
					p.pos++
				}
			}
		}

		// Skip comma or end of object
		p.skipWhitespace()
		if p.pos >= len(p.data) {
			syntaxErr = getSyntaxError()
			syntaxErr.Offset = int64(p.pos)
			syntaxErr.Msg = "unexpected end of JSON input"
			putObjectMap(result)
			return nil, false
		}

		if p.data[p.pos] == '}' {
			p.pos++ // Skip closing brace
			break
		}

		if p.data[p.pos] != ',' {
			syntaxErr = getSyntaxError()
			syntaxErr.Offset = int64(p.pos)
			syntaxErr.Msg = "expected comma after object property"
			putObjectMap(result)
			return nil, false
		}

		p.pos++ // Skip comma
	}

	// Create a new map to return - we can't return the pooled one directly
	finalResult := make(map[string]interface{}, len(result))
	for k, v := range result {
		finalResult[k] = v
	}

	putObjectMap(result)
	return finalResult, true
}

// skipWhitespace skips whitespace in the decoder's buffer
func (d *Decoder) skipWhitespace() error {
	for {
		// Skip any whitespace in current buffer
		for d.readPos < len(d.buf) {
			c := d.buf[d.readPos]
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				return nil
			}
			d.readPos++
		}

		// Need more data - use refillBuffer instead of direct read
		if err := d.refillBuffer(); err != nil {
			if err == io.EOF {
				return err
			}
			return err
		}

		// If buffer is still empty after refill, we're done
		if len(d.buf) == 0 {
			return io.EOF
		}
	}
}

func (d *Decoder) UseNumber() *Decoder {
	d.useNumber = true
	return d
}

func (d *Decoder) readValue() ([]byte, error) {
	// Get a fresh token buffer from the pool
	oldBuf := d.tokenBuf
	d.tokenBuf = *getTokenBuf()
	defer func() {
		// Always return the buffer to the pool if we exit early
		if &d.tokenBuf != &oldBuf {
			putTokenBuf(&d.tokenBuf)
		}
	}()

	// Make sure we have data to read
	if len(d.buf) == 0 || d.readPos >= len(d.buf) {
		if err := d.refillBuffer(); err != nil {
			d.tokenBuf = oldBuf
			return nil, err
		}
	}

	// Track parsing state
	depth := 0
	inString := false
	escaped := false
	buffers := make([][]byte, 0, 4)

	// Record first character for validation
	firstChar := d.buf[d.readPos]

	// Skip initial whitespace
	for d.readPos < len(d.buf) && isWhitespace(d.buf[d.readPos]) {
		d.readPos++
		if d.readPos >= len(d.buf) {
			if err := d.refillBuffer(); err != nil {
				d.tokenBuf = oldBuf
				return nil, err
			}
		}
	}

	// Ensure we have non-whitespace data
	if d.readPos >= len(d.buf) {
		d.tokenBuf = oldBuf
		return nil, io.EOF
	}

	// Main parsing loop
	for {
		// Ensure we have data
		if d.readPos >= len(d.buf) {
			if err := d.refillBuffer(); err != nil {
				if err == io.EOF && len(d.tokenBuf) > 0 {
					// We have a partial value but no more data
					if depth > 0 {
						// Unclosed object or array
						d.tokenBuf = oldBuf
						return nil, fmt.Errorf("unexpected end of JSON input: unclosed structure")
					}

					// Return what we have if it makes sense as a complete value
					tokenStr := string(d.tokenBuf)
					if isCompleteLiteral(tokenStr) {
						result := AppendBuffers(append(buffers, d.tokenBuf))
						d.tokenBuf = oldBuf
						return result, nil
					} else if len(d.tokenBuf) >= 2 &&
						((d.tokenBuf[0] == '{' && d.tokenBuf[len(d.tokenBuf)-1] == '}') ||
							(d.tokenBuf[0] == '[' && d.tokenBuf[len(d.tokenBuf)-1] == ']')) {
						result := AppendBuffers(append(buffers, d.tokenBuf))
						d.tokenBuf = oldBuf
						return result, nil
					}
				}

				d.tokenBuf = oldBuf
				return nil, err
			}
		}

		// Process the current character
		c := d.buf[d.readPos]
		d.tokenBuf = append(d.tokenBuf, c)
		d.readPos++

		// Handle string context
		if inString {
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				inString = false

				// If we're at the top level and this is a standalone string, we're done
				if depth == 0 && firstChar == '"' {
					result := AppendBuffers(append(buffers, d.tokenBuf))
					d.tokenBuf = oldBuf
					return result, nil
				}
			}
			continue // Skip other processing for string content
		}

		// Handle structural elements and literals when not in a string
		switch c {
		case '"':
			inString = true

		case '{', '[':
			depth++

		case '}', ']':
			depth--

			// If we've closed the outermost structure, we're done
			if depth == 0 {
				// Ensure brackets match: { must close with }, [ with ]
				isValid := (firstChar == '{' && c == '}') || (firstChar == '[' && c == ']')
				if !isValid {
					d.tokenBuf = oldBuf
					return nil, fmt.Errorf("mismatched brackets in JSON")
				}

				result := AppendBuffers(append(buffers, d.tokenBuf))
				d.tokenBuf = oldBuf
				return result, nil
			} else if depth < 0 {
				// This means we have an extra closing brace/bracket
				d.tokenBuf = oldBuf
				return nil, fmt.Errorf("unexpected closing character in JSON")
			}

		case ' ', '\t', '\r', '\n':
			// Whitespace terminates top-level literals (but not nested ones)
			if depth == 0 && firstChar != '{' && firstChar != '[' && firstChar != '"' {
				// Check if we have a complete literal (excluding this whitespace)
				tokenLen := len(d.tokenBuf)
				valueBytes := d.tokenBuf[:tokenLen-1] // Exclude the whitespace

				if isCompleteLiteral(string(valueBytes)) {
					result := AppendBuffers(append(buffers, valueBytes))
					d.tokenBuf = oldBuf
					// Adjust read position back by one since we didn't consume this whitespace
					d.readPos--
					return result, nil
				}
			}

		case ',', ':':
			// These characters are only valid inside objects/arrays
			if depth == 0 {
				d.tokenBuf = oldBuf
				return nil, fmt.Errorf("unexpected character in JSON literal: %c", c)
			}
		}

		// If the current token buffer is getting large, add it to buffers and get a new one
		if len(d.tokenBuf) >= 4096 {
			// Create a copy to avoid buffer modification issues
			tokenCopy := make([]byte, len(d.tokenBuf))
			copy(tokenCopy, d.tokenBuf)
			buffers = append(buffers, tokenCopy)

			putTokenBuf(&d.tokenBuf)
			d.tokenBuf = *getTokenBuf()
		}
	}
}

// Helper function to refill the buffer
func (d *Decoder) refillBuffer() error {
	// Use a smaller fixed size instead of full capacity
	fmt.Printf("refillBuffer: attempting to read with buf cap=%d\n", cap(d.buf))
	temp := make([]byte, 512) // Or even smaller like 128
	n, err := d.r.Read(temp)
	fmt.Printf("refillBuffer: read %d bytes, err=%v\n", n, err)
	if n > 0 {
		fmt.Printf("refillBuffer: first byte=%d (%c)\n", temp[0], temp[0])
	}
	// Handle EOF with data case explicitly
	if err == io.EOF && n > 0 {
		err = nil // EOF with data is not an error for us
	}

	if n > 0 {
		// Append to existing buffer if needed
		d.buf = append(d.buf, temp[:n]...)
		// Or replace if empty: d.buf = temp[:n]
	}

	if err != nil && err != io.EOF {
		return err
	}

	d.readPos = 0
	return nil
}

// ### Extraction ###

// Extract retrieves a value from JSON based on a path
func Extract(data []byte, path ...string) ([]byte, bool) {
	if len(path) == 0 {
		return data, true
	}

	p := NewParser(data)

	// Create a deferred error handler
	var syntaxErr *SyntaxError = nil
	defer func() {
		if syntaxErr != nil {
			putSyntaxError(syntaxErr)
		}
	}()

	// Skip initial whitespace
	p.skipWhitespace()

	// Handle the root path segment
	for _, segment := range path {
		p.skipWhitespace()

		if p.pos >= len(p.data) {
			syntaxErr = getSyntaxError()
			syntaxErr.Offset = int64(p.pos)
			syntaxErr.Msg = "unexpected end of JSON input"
			return nil, false
		}

		// Must be an object to extract by key
		if p.data[p.pos] != '{' {
			// Not a syntax error, just wrong path
			return nil, false
		}

		p.pos++ // Skip '{'

		found := false
		for {
			p.skipWhitespace()

			if p.pos >= len(p.data) {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "unexpected end of JSON input"
				return nil, false
			}

			// Check for end of object
			if p.data[p.pos] == '}' {
				return nil, false // Key not found - not a syntax error
			}

			// Parse key
			tokenType, keyBytes := p.parseString()
			if tokenType != TokenString {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "expected string key in object"
				return nil, false
			}

			// Check if this is the key we want
			key := GetString(keyBytes)

			// Skip colon
			p.skipWhitespace()
			if p.pos >= len(p.data) || p.data[p.pos] != ':' {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "expected colon after object key"
				return nil, false
			}
			p.pos++ // Skip colon

			if key == segment {
				// Found our key
				found = true
				break
			}

			// Skip value
			if !skipValue(p) {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid JSON value"
				return nil, false
			}

			// Skip comma or end of object
			p.skipWhitespace()
			if p.pos >= len(p.data) {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "unexpected end of JSON input"
				return nil, false
			}

			if p.data[p.pos] == '}' {
				return nil, false // Key not found - not a syntax error
			}

			if p.data[p.pos] != ',' {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "expected comma after object property"
				return nil, false
			}

			p.pos++ // Skip comma
		}

		if !found {
			return nil, false // Not a syntax error
		}

		// If this is the last segment, extract the value
		if len(path) == 1 {
			p.skipWhitespace()
			start := p.pos
			if !skipValue(p) {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid JSON value"
				return nil, false
			}
			return p.data[start:p.pos], true
		}

		// Otherwise, continue with the next path segment
		path = path[1:]
	}

	return nil, false
}

// GetArray extracts an array from JSON at the specified path
func GetArray(data []byte, path ...string) ([]interface{}, bool) {
	value, ok := Extract(data, path...)
	if !ok {
		return nil, false
	}

	p := NewParser(value)

	// Create a deferred error handler
	var syntaxErr *SyntaxError = nil
	defer func() {
		if syntaxErr != nil {
			putSyntaxError(syntaxErr)
		}
	}()

	// Check if this is actually an array
	if p.ValueType() != TokenArrayStart {
		return nil, false
	}

	// Get slice from pool
	result := getArraySlice()

	// Skip opening bracket
	p.pos++

	// Parse array elements
	for {
		p.skipWhitespace()

		// Check for end of array
		if p.pos >= len(p.data) || p.data[p.pos] == ']' {
			p.pos++ // Skip closing bracket
			break
		}

		// Expect comma between elements (but not before first element)
		if len(result) > 0 {
			if p.data[p.pos] != ',' {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "expected comma after array element"
				putArraySlice(result)
				return nil, false
			}
			p.pos++ // Skip comma
			p.skipWhitespace()
		}

		// Parse value based on type
		switch p.ValueType() {
		case TokenString:
			if val, ok := p.ExtractString(); ok {
				result = append(result, val)
			} else {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid string in array"
				putArraySlice(result)
				return nil, false
			}
		case TokenNumber:
			if val, ok := p.ExtractNumber(); ok {
				result = append(result, val)
			} else {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid number in array"
				putArraySlice(result)
				return nil, false
			}
		case TokenBool:
			if val, ok := p.ExtractBool(); ok {
				result = append(result, val)
			} else {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid boolean in array"
				putArraySlice(result)
				return nil, false
			}
		case TokenNull:
			if p.matchLiteral("null") {
				result = append(result, nil)
			} else {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid null in array"
				putArraySlice(result)
				return nil, false
			}
		case TokenObjectStart:
			if obj, ok := GetObject(p.data[p.pos:], ""); ok {
				result = append(result, obj)
				// Skip the object we just parsed
				depth := 1
				p.pos++
				for depth > 0 && p.pos < len(p.data) {
					if p.data[p.pos] == '{' {
						depth++
					} else if p.data[p.pos] == '}' {
						depth--
					}
					p.pos++
				}
			} else {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid object in array"
				putArraySlice(result)
				return nil, false
			}
		case TokenArrayStart:
			if arr, ok := GetArray(p.data[p.pos:], ""); ok {
				result = append(result, arr)
				// Skip the array we just parsed
				depth := 1
				p.pos++
				for depth > 0 && p.pos < len(p.data) {
					if p.data[p.pos] == '[' {
						depth++
					} else if p.data[p.pos] == ']' {
						depth--
					}
					p.pos++
				}
			} else {
				syntaxErr = getSyntaxError()
				syntaxErr.Offset = int64(p.pos)
				syntaxErr.Msg = "invalid array in array"
				putArraySlice(result)
				return nil, false
			}
		default:
			syntaxErr = getSyntaxError()
			syntaxErr.Offset = int64(p.pos)
			syntaxErr.Msg = "invalid JSON value in array"
			putArraySlice(result)
			return nil, false
		}
	}

	// Create a new slice to return - we can't return the pooled one directly
	finalResult := make([]interface{}, len(result))
	copy(finalResult, result)

	// Return the slice to the pool
	putArraySlice(result)

	return finalResult, true
}

func structFields(t reflect.Type) []Field {
	return getCachedFields(t)
}

// Contains reports whether the tag options contain the specified option
//
//go:inline
func (o tagOptions) Contains(option string) bool {
	if len(o) == 0 {
		return false
	}

	optLen := len(option)
	if optLen == 0 {
		return false
	}

	s := string(o)
	sLen := len(s)

	// Check if this is exactly the option
	if s == option {
		return true
	}

	i := 0
	for i < sLen {
		// Find the start of the next option
		start := i

		// Find the end of the current option (comma or end of string)
		for i < sLen && s[i] != ',' {
			i++
		}

		// Check if this segment matches our option
		if i-start == optLen && s[start:i] == option {
			return true
		}

		// Skip the comma
		if i < sLen {
			i++
		}
	}

	return false
}

// parseTag splits a struct field's json tag into its name and options
//
//go:inline
func parseTag(tag string) (string, tagOptions) {
	if len(tag) == 0 {
		return "", ""
	}

	// Manually search for the first comma
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			// Return slices of the original string instead of creating new ones
			return tag[:i], tagOptions(tag[i+1:])
		}
	}

	// No comma found, return the whole tag as the name
	return tag, ""
}
