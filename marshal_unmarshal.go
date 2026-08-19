package apexJSON

import (
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"time"
)

var _ = time.RFC3339

func marshalValue(v reflect.Value, buf *Buffer) error {
	// 1. Handle nil values first (common case)
	if !v.IsValid() {
		buf.Write(jsonNull)
		return nil
	}

	// 2. Handle pointer indirection with a loop to avoid recursion
	// This ensures proper handling of pointers to all types including primitives
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			buf.Write(jsonNull)
			return nil
		}
		v = v.Elem()
	}

	// 3. Direct kind handling for most common types - avoids Interface() calls
	switch v.Kind() {
	case reflect.String:
		buf.WriteByte(jsonQuote)
		writeEscapedStringString(buf, v.String()) // Use string-direct version
		buf.WriteByte(jsonQuote)
		return nil
	case reflect.Bool:
		if v.Bool() {
			buf.Write(jsonTrue)
		} else {
			buf.Write(jsonFalse)
		}
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		numBuf := getNumberBuf()
		*numBuf = strconv.AppendInt((*numBuf)[:0], v.Int(), 10)
		buf.Write(*numBuf)
		putNumberBuf(numBuf)
		return nil
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("json: unsupported float value: %v", f)
		}
		numBuf := getNumberBuf()
		*numBuf = strconv.AppendFloat((*numBuf)[:0], f, 'g', -1, 64)
		buf.Write(*numBuf)
		putNumberBuf(numBuf)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		numBuf := getNumberBuf()
		*numBuf = strconv.AppendUint((*numBuf)[:0], v.Uint(), 10)
		buf.Write(*numBuf)
		putNumberBuf(numBuf)
		return nil
	}

	// 4. Only use Interface() for special types that need it
	if v.CanInterface() {
		switch x := v.Interface().(type) {
		case time.Time:
			buf.WriteByte(jsonQuote)
			// Use direct buffer writing for time
			if cap(buf.buf)-buf.off < len(time.RFC3339) {
				buf.grow(len(time.RFC3339))
			}
			b := x.AppendFormat(buf.buf[buf.off:buf.off], time.RFC3339)
			buf.off += len(b)
			buf.WriteByte(jsonQuote)
			return nil
		case Marshaler:
			data, err := x.MarshalJSON()
			if err != nil {
				return err
			}
			buf.Write(data)
			return nil
		case []byte:
			buf.WriteByte(jsonQuote)
			encodedLen := base64.StdEncoding.EncodedLen(len(x))
			if encodedLen > 0 {
				// Encode directly into buffer if possible
				if buf.off+encodedLen <= cap(buf.buf) {
					buf.grow(encodedLen)
					base64.StdEncoding.Encode(buf.buf[buf.off:], x)
					buf.off += encodedLen
				} else {
					// Fallback to temporary buffer from pool
					tmpBuf := getBufferSize(encodedLen)
					base64.StdEncoding.Encode(tmpBuf.buf, x)
					buf.Write(tmpBuf.buf[:encodedLen])
					putBuffer(tmpBuf)
				}
			}
			buf.WriteByte(jsonQuote)
			return nil
		}
	}

	// 5. Handle interface indirection
	if v.Kind() == reflect.Interface && !v.IsNil() {
		v = v.Elem()

		// If the interface contains a string, handle it directly
		if v.Kind() == reflect.String {
			buf.WriteByte(jsonQuote)
			writeEscapedStringString(buf, v.String())
			buf.WriteByte(jsonQuote)
			return nil
		}

		// For all other types, process the concrete value recursively
		return marshalValue(v, buf)
	}

	// 6. Type-specific encoding for remaining types
	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		// Special case for empty arrays
		if v.Len() == 0 {
			buf.WriteByte(jsonOpenBracket)
			buf.WriteByte(jsonCloseBracket)
			return nil
		}

		// Special case for []byte
		if v.Type().Elem().Kind() == reflect.Uint8 && v.CanInterface() {
			return marshalBytes(v.Interface().([]byte), buf)
		}

		return marshalArray(v, buf)
	case reflect.Map:
		// Special case for empty maps
		if v.Len() == 0 {
			buf.WriteByte(jsonOpenBrace)
			buf.WriteByte(jsonCloseBrace)
			return nil
		}

		return marshalMap(v, buf)
	case reflect.Struct:
		return marshalStruct(v, buf)
	default:
		return fmt.Errorf("json: unsupported type: %s", v.Type().String())
	}
}

// Helper function for byte array marshaling
func marshalBytes(data []byte, buf *Buffer) error {
	buf.WriteByte(jsonQuote)
	encodedLen := base64.StdEncoding.EncodedLen(len(data))
	if encodedLen > 0 {
		if encodedLen+buf.off > cap(buf.buf) {
			buf.grow(encodedLen)
		}
		dst := buf.buf[buf.off : buf.off+encodedLen]
		base64.StdEncoding.Encode(dst, data)
		buf.off += encodedLen
	}
	buf.WriteByte(jsonQuote)
	return nil
}

// MarshalToWriter allows compatibility with io.Writer
func MarshalToWriter(v interface{}, w io.Writer) error {
	// For Buffer type, use direct path
	if buf, ok := w.(*Buffer); ok {
		return marshalValue(reflect.ValueOf(v), buf)
	}

	// Otherwise, use buffer pool
	buf := getBuffer()
	defer putBuffer(buf)

	if err := marshalValue(reflect.ValueOf(v), buf); err != nil {
		return err
	}

	_, err := w.Write(buf.Bytes())
	return err
}

func marshalArray(v reflect.Value, buf *Buffer) error {
	length := v.Len()

	// Fast path for empty arrays
	if length == 0 {
		buf.WriteByte(jsonOpenBracket)
		buf.WriteByte(jsonCloseBracket)
		return nil
	}

	// Special case for []byte - optimize base64 encoding
	if v.Type().Elem().Kind() == reflect.Uint8 && v.CanInterface() {
		byteSlice := v.Interface().([]byte)
		buf.WriteByte(jsonQuote)

		// Calculate encoded length and pre-grow buffer
		encodedLen := base64.StdEncoding.EncodedLen(len(byteSlice))
		if buf.off+encodedLen > cap(buf.buf) {
			buf.grow(encodedLen)
		}

		// Encode directly into buffer's backing array
		dst := buf.buf[buf.off : buf.off+encodedLen]
		base64.StdEncoding.Encode(dst, byteSlice)
		buf.off += encodedLen

		buf.WriteByte(jsonQuote)
		return nil
	}

	// Estimate buffer size needed for array
	estimatedSize := 2 // [] brackets
	if length > 0 {
		estimatedSize += length - 1 // commas between elements

		elemKind := v.Type().Elem().Kind()
		switch elemKind {
		case reflect.Int, reflect.Int64:
			estimatedSize += length * 20 // Conservative estimate for int64
		case reflect.Float64:
			estimatedSize += length * 24 // Conservative estimate for float64
		case reflect.Bool:
			estimatedSize += length * 5 // true/false
		case reflect.String:
			sampleSize := min(length, 3)
			totalLen := 0
			for i := 0; i < sampleSize; i++ {
				s := v.Index(i).String()
				totalLen += len(s) + 2              // quotes
				totalLen += countEscapeChars(s) * 5 // escaping estimate
			}
			avg := totalLen / sampleSize
			estimatedSize += avg * length
		default:
			elemSize := estimateValueSize(v.Index(0))
			estimatedSize += length * elemSize
		}
	}

	if buf.off+estimatedSize > cap(buf.buf) {
		buf.grow(estimatedSize)
	}

	buf.WriteByte(jsonOpenBracket)

	// Fast paths for common array types
	elemKind := v.Type().Elem().Kind()
	switch elemKind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		for i := 0; i < length; i++ {
			if i > 0 {
				buf.WriteByte(jsonComma)
			}
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendInt((*numBuf)[:0], v.Index(i).Int(), 10)
			buf.Write(*numBuf)
			putNumberBuf(numBuf)
		}

	case reflect.Float32, reflect.Float64:
		for i := 0; i < length; i++ {
			if i > 0 {
				buf.WriteByte(jsonComma)
			}
			f := v.Index(i).Float()
			if math.IsInf(f, 0) || math.IsNaN(f) {
				return fmt.Errorf("json: unsupported float value: %v", f)
			}
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendFloat((*numBuf)[:0], f, 'g', -1, 64)
			buf.Write(*numBuf)
			putNumberBuf(numBuf)
		}

	case reflect.Bool:
		for i := 0; i < length; i++ {
			if i > 0 {
				buf.WriteByte(jsonComma)
			}
			if v.Index(i).Bool() {
				buf.Write(jsonTrue)
			} else {
				buf.Write(jsonFalse)
			}
		}

	case reflect.String:
		for i := 0; i < length; i++ {
			if i > 0 {
				buf.WriteByte(jsonComma)
			}
			str := v.Index(i).String()
			buf.WriteByte(jsonQuote)
			if !needsEscaping(str) {
				buf.WriteString(str)
			} else {
				writeEscapedStringString(buf, str)
			}
			buf.WriteByte(jsonQuote)
		}

	default:
		for i := 0; i < length; i++ {
			if i > 0 {
				buf.WriteByte(jsonComma)
			}
			if err := marshalValue(v.Index(i), buf); err != nil {
				return err
			}
		}
	}

	buf.WriteByte(jsonCloseBracket)
	return nil
}

// marshalMap serializes a map to JSON with optimized memory usage
func marshalMap(v reflect.Value, buf *Buffer) error {
	// Handle nil maps
	if v.IsNil() {
		buf.Write(jsonNull)
		return nil
	}

	// Fast path for map[string]interface{} - extremely common case
	if v.Type().Key().Kind() == reflect.String {
		stringKeyMap := true

		// Check if this is a common map type we can handle directly
		if v.CanInterface() {
			switch v.Interface().(type) {
			case map[string]interface{}:
				return marshalStringInterfaceMap(v.Interface().(map[string]interface{}), buf)
			case map[string]string:
				return marshalStringStringMap(v.Interface().(map[string]string), buf)
			case map[string]int:
				return marshalStringIntMap(v.Interface().(map[string]int), buf)
			}
		}

		// Handle generic string key maps more efficiently
		if stringKeyMap {
			keys := getKeysSlice()
			*keys = append(*keys, v.MapKeys()...)
			defer putKeysSlice(keys)

			// Pre-size buffer based on map size
			mapLen := v.Len()
			estimatedSize := 2 + (mapLen * 8) // {} plus average key/value size
			if buf.off+estimatedSize > cap(buf.buf) {
				buf.grow(estimatedSize)
			}

			buf.WriteByte(jsonOpenBrace)

			// Process keys with optimized string key handling
			for i, key := range *keys {
				if i > 0 {
					buf.WriteByte(jsonComma)
				}

				// Write key (we know it's a string)
				buf.WriteByte(jsonQuote)
				var s string
				if key.Kind() == reflect.String {
					s = key.String() // Use String() for reflect.String kind
				} else if key.CanInterface() {
					// Try to extract string via interface
					switch k := key.Interface().(type) {
					case string:
						s = k
					default:
						// Last resort - use fmt to convert to string
						s = fmt.Sprintf("%v", k)
					}
				} else {
					// Fallback
					s = fmt.Sprintf("%v", key)
				}

				if !needsEscaping(s) {
					_, err := buf.WriteString(s)
					if err != nil {
						return err
					}
				} else {
					writeEscapedStringString(buf, s)
				}

				buf.Write(jsonQuoteColon)

				// Marshal value with original key
				if err := marshalValue(v.MapIndex(key), buf); err != nil {
					return err
				}
			}

			buf.WriteByte(jsonCloseBrace)
			return nil
		}
	}

	// General case for non-string key maps
	keys := getKeysSlice()
	*keys = append(*keys, v.MapKeys()...)
	defer putKeysSlice(keys)

	buf.WriteByte(jsonOpenBrace)

	for i, key := range *keys {
		if i > 0 {
			buf.WriteByte(jsonComma)
		}

		// Write key with opening quote
		buf.WriteByte(jsonQuote)

		// Handle interface/pointer indirection for keys
		originalKey := key
		if key.Kind() == reflect.Interface && !key.IsNil() {
			key = key.Elem()
		}
		if key.Kind() == reflect.Ptr && !key.IsNil() {
			key = key.Elem()
		}

		// First check kind for common cases without interface conversion
		switch key.Kind() {
		case reflect.String:
			s := key.String()
			if !needsEscaping(s) {
				buf.WriteString(s)
			} else {
				writeEscapedStringString(buf, s)
			}

		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendInt((*numBuf)[:0], key.Int(), 10)
			buf.Write(*numBuf)
			putNumberBuf(numBuf)

		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendUint((*numBuf)[:0], key.Uint(), 10)
			buf.Write(*numBuf)
			putNumberBuf(numBuf)

		case reflect.Float32, reflect.Float64:
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendFloat((*numBuf)[:0], key.Float(), 'g', -1, key.Type().Bits())
			buf.Write(*numBuf)
			putNumberBuf(numBuf)

		case reflect.Bool:
			if key.Bool() {
				buf.Write(jsonTrue)
			} else {
				buf.Write(jsonFalse)
			}

		default:
			// Use interface only for complex cases
			if key.CanInterface() {
				switch k := key.Interface().(type) {
				case []byte:
					if len(k) == 0 {
						buf.Write(jsonEmpty)
					} else {
						buf.Write(jsonHexPrefix)
						// Direct hex writing
						hexLen := len(k) * 2
						if buf.off+hexLen <= cap(buf.buf) {
							buf.grow(hexLen)
							for _, b := range k {
								buf.buf[buf.off] = hex[b>>4]
								buf.off++
								buf.buf[buf.off] = hex[b&0xF]
								buf.off++
							}
						} else {
							// Fallback for large byte arrays
							hexBuf := getBufferSize(hexLen)
							for _, b := range k {
								hexBuf.WriteByte(hex[b>>4])
								hexBuf.WriteByte(hex[b&0xF])
							}
							buf.Write(hexBuf.buf[:hexBuf.off])
							putBuffer(hexBuf)
						}
					}

				case time.Time:
					buf.WriteByte(jsonQuote)
					timeLen := len(time.RFC3339)
					if buf.off+timeLen <= cap(buf.buf) {
						buf.grow(timeLen)
						n := copy(buf.buf[buf.off:], k.AppendFormat(buf.buf[buf.off:buf.off], time.RFC3339))
						buf.off += n
					} else {
						// Fallback
						timeBuf := getNumberBuf()
						*timeBuf = k.AppendFormat((*timeBuf)[:0], time.RFC3339)
						buf.Write(*timeBuf)
						putNumberBuf(timeBuf)
					}
					buf.WriteByte(jsonQuote)

				default:
					switch key.Kind() {
					case reflect.Array, reflect.Slice:
						if key.Len() == 0 {
							buf.Write(jsonEmpty)
						} else {
							buf.WriteString("array")
						}

					case reflect.Map:
						buf.Write(jsonMapOpen)
						numBuf := getNumberBuf()
						*numBuf = strconv.AppendInt((*numBuf)[:0], int64(key.Len()), 10)
						buf.Write(*numBuf)
						putNumberBuf(numBuf)
						buf.Write(jsonMapClose)

					case reflect.Struct:
						if stringer, ok := key.Interface().(fmt.Stringer); ok {
							writeEscapedStringString(buf, stringer.String())
						} else {
							b := getBuilder()
							fmt.Fprint(b, key.Interface())
							keyStr := b.String()
							writeEscapedStringString(buf, keyStr)
							putBuilder(b)
						}

					default:
						b := getBuilder()
						fmt.Fprint(b, key.Interface())
						keyStr := b.String()
						writeEscapedStringString(buf, keyStr)
						putBuilder(b)
					}
				}
			} else {
				// Fallback for non-interfaceable types
				typeStr := key.Type().String()
				buf.WriteString(typeStr)
			}
		}

		// Complete key-value pair
		buf.Write(jsonQuoteColon)

		// Marshal the value
		if err := marshalValue(v.MapIndex(originalKey), buf); err != nil {
			return err
		}
	}

	buf.WriteByte(jsonCloseBrace)
	return nil
}

// marshalStruct serializes a struct to JSON with efficient memory usage
func marshalStruct(v reflect.Value, buf *Buffer) error {
	// Get cached fields for struct type
	fields := getCachedFields(v.Type())

	// Start object
	buf.WriteByte(jsonOpenBrace)

	// Track if we've written any fields yet
	first := true

	// Process each field
	for _, f := range fields {
		// Get the field value
		fv := v.FieldByIndex(f.index)

		// Skip zero values if omitEmpty is set
		if f.omitEmpty && isEmptyValue(fv) {
			continue
		}

		// Add comma if this isn't the first field
		if !first {
			buf.WriteByte(jsonComma)
		}
		first = false

		// Write field name with quotes
		if f.nameWithQuotesBytes != nil {
			buf.Write(f.nameWithQuotesBytes)
		} else {
			buf.WriteByte(jsonQuote)
			buf.Write(f.nameBytes)
			buf.WriteByte(jsonQuote)
		}

		// Write colon
		buf.WriteByte(':')

		// Marshal the field value
		if err := marshalValue(fv, buf); err != nil {
			return err
		}
	}

	// Close object
	buf.WriteByte(jsonCloseBrace)
	return nil
}

// Specialized implementations for common map types
func marshalStringInterfaceMap(m map[string]interface{}, buf *Buffer) error {
	buf.WriteByte(jsonOpenBrace)
	first := true

	// Pre-grow buffer
	buf.grow(len(m) * 16)

	for k, v := range m {
		if !first {
			buf.WriteByte(jsonComma)
		}
		first = false

		// Write key
		buf.WriteByte(jsonQuote)
		if !needsEscaping(k) {
			buf.WriteString(k)
		} else {
			writeEscapedStringString(buf, k)
		}
		buf.Write(jsonQuoteColon)

		// Write value directly without reflection where possible
		switch val := v.(type) {
		case string:
			buf.WriteByte(jsonQuote)
			if !needsEscaping(val) {
				buf.WriteString(val)
			} else {
				writeEscapedStringString(buf, val)
			}
			buf.WriteByte(jsonQuote)
		case int:
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendInt((*numBuf)[:0], int64(val), 10)
			buf.Write(*numBuf)
			putNumberBuf(numBuf)
		case float64:
			numBuf := getNumberBuf()
			*numBuf = strconv.AppendFloat((*numBuf)[:0], val, 'g', -1, 64)
			buf.Write(*numBuf)
			putNumberBuf(numBuf)
		case bool:
			if val {
				buf.Write(jsonTrue)
			} else {
				buf.Write(jsonFalse)
			}
		case nil:
			buf.Write(jsonNull)
		default:
			if err := marshalValue(reflect.ValueOf(v), buf); err != nil {
				return err
			}
		}
	}

	buf.WriteByte(jsonCloseBrace)
	return nil
}

func marshalStringStringMap(m map[string]string, buf *Buffer) error {
	buf.WriteByte(jsonOpenBrace)
	first := true

	// Pre-grow buffer based on map content
	totalSize := 2 // {}
	for k, v := range m {
		if !first {
			totalSize++ // comma
		}
		totalSize += len(k) + len(v) + 5 // "key":"value"
		first = false
	}

	if buf.off+totalSize > cap(buf.buf) {
		buf.grow(totalSize)
	}

	first = true
	for k, v := range m {
		if !first {
			buf.WriteByte(jsonComma)
		}
		first = false

		buf.WriteByte(jsonQuote)
		if !needsEscaping(k) {
			buf.WriteString(k)
		} else {
			writeEscapedStringString(buf, k)
		}
		buf.Write(jsonQuoteColon)

		buf.WriteByte(jsonQuote)
		if !needsEscaping(v) {
			buf.WriteString(v)
		} else {
			writeEscapedStringString(buf, v)
		}
		buf.WriteByte(jsonQuote)
	}

	buf.WriteByte(jsonCloseBrace)
	return nil
}

func marshalStringIntMap(m map[string]int, buf *Buffer) error {
	buf.WriteByte(jsonOpenBrace)
	first := true

	// Pre-estimate size
	buf.grow(len(m) * 16)

	for k, v := range m {
		if !first {
			buf.WriteByte(jsonComma)
		}
		first = false

		buf.WriteByte(jsonQuote)
		if !needsEscaping(k) {
			buf.WriteString(k)
		} else {
			writeEscapedStringString(buf, k)
		}
		buf.Write(jsonQuoteColon)

		numBuf := getNumberBuf()
		*numBuf = strconv.AppendInt((*numBuf)[:0], int64(v), 10)
		buf.Write(*numBuf)
		putNumberBuf(numBuf)
	}

	buf.WriteByte(jsonCloseBrace)
	return nil
}

func unmarshalValue(data []byte, v reflect.Value) error {
	// Check for custom unmarshaler first
	if v.CanAddr() && v.Addr().Type().Implements(reflect.TypeOf((*Unmarshaler)(nil)).Elem()) {
		return v.Addr().Interface().(Unmarshaler).UnmarshalJSON(data)
	}

	// Create unmarshal handler for the target value
	handler := NewUnmarshalHandler(v, nil)

	// Create parser with the handler
	parser := NewParser(handler)

	// Use the state machine to parse and populate the value
	return parser.Parse(data)
}
