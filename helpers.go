package apexJSON

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unsafe"
)

// Int64 converts the Number to an int64.
// Uses base 10 for parsing.
func (n Number) Int64() (int64, error) {
	return strconv.ParseInt(string(n), 10, 64)
}

// Float64 converts the Number to a float64.
// Returns the 64-bit floating point representation.
func (n Number) Float64() (float64, error) {
	return strconv.ParseFloat(string(n), 64)
}

// String returns the string representation of the Number.
// This method satisfies the fmt.Stringer interface.
func (n Number) String() string {
	return string(n)
}

// MustInt64 returns the int64 value or panics if conversion fails.
// Useful for situations where you know the conversion will succeed.
func (n Number) MustInt64() int64 {
	i, err := n.Int64()
	if err != nil {
		panic(err)
	}
	return i
}

// MustFloat64 returns the float64 value or panics if conversion fails.
// Useful for situations where you know the conversion will succeed.
func (n Number) MustFloat64() float64 {
	f, err := n.Float64()
	if err != nil {
		panic(err)
	}
	return f
}

// IsInt returns true if the number is an integer.
// Useful for type checking before conversion.
func (n Number) IsInt() bool {
	_, err := n.Int64()
	return err == nil
}

// IsFloat returns true if the number is a float.
// Will return true for integers as well since they can be represented as floats.
func (n Number) IsFloat() bool {
	_, err := n.Float64()
	return err == nil
}

// Format applies the specified format to the Number value.
// Designed to mirror the time.Time Format approach with specialized
// handling for each format constant.
func (n Number) Format(format string) string {
	// Handle each format according to its specific requirements
	switch format {
	// Integer formats - each needs proper type conversion
	case IntFormat:
		// Decimal integer
		if i, err := n.Int64(); err == nil {
			return fmt.Sprintf(format, i)
		}
		// Fallback to float → int conversion
		f, err := n.Float64()
		if err == nil {
			return fmt.Sprintf(format, int64(f))
		}
		return string(n)

	case IntHex:
		// Lowercase hex
		if i, err := n.Int64(); err == nil {
			return fmt.Sprintf(format, i)
		}
		f, err := n.Float64()
		if err == nil {
			return fmt.Sprintf(format, int64(f))
		}
		return string(n)

	case IntHexUpper:
		// Uppercase hex
		if i, err := n.Int64(); err == nil {
			return fmt.Sprintf(format, i)
		}
		f, err := n.Float64()
		if err == nil {
			return fmt.Sprintf(format, int64(f))
		}
		return string(n)

	case IntBinary:
		// Binary representation
		if i, err := n.Int64(); err == nil {
			return fmt.Sprintf(format, i)
		}
		f, err := n.Float64()
		if err == nil {
			return fmt.Sprintf(format, int64(f))
		}
		return string(n)

	case IntOctal:
		// Octal representation
		if i, err := n.Int64(); err == nil {
			return fmt.Sprintf(format, i)
		}
		f, err := n.Float64()
		if err == nil {
			return fmt.Sprintf(format, int64(f))
		}
		return string(n)

	// Float formats - each gets its own case for clarity
	case FloatPrecision2:
		// 2 decimal places
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}
		return fmt.Sprintf(format, f)

	case FloatPrecision3:
		// 3 decimal places
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}
		return fmt.Sprintf(format, f)

	case FloatPrecision4:
		// 4 decimal places
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}
		return fmt.Sprintf(format, f)

	case FloatGeneral:
		// General format (most compact)
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}
		return fmt.Sprintf(format, f)

	case FloatScientificE:
		// Scientific notation with lowercase e
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}
		return fmt.Sprintf(format, f)

	case FloatScientificCapE:
		// Scientific notation with uppercase E
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}
		return fmt.Sprintf(format, f)

	case FloatComma:
		// Thousands separator format (Go 1.23+)
		f, err := n.Float64()
		if err != nil {
			return string(n)
		}

		// Custom implementation for compatibility
		intPart := int64(f)
		fracPart := math.Abs(f - float64(intPart))

		// Handle negative numbers
		prefix := ""
		if f < 0 {
			prefix = "-"
			intPart = -intPart
		}

		// Format integer part with commas
		intStr := fmt.Sprintf("%d", intPart)
		var result strings.Builder
		result.WriteString(prefix)

		for i, r := range intStr {
			if i > 0 && (len(intStr)-i)%3 == 0 {
				result.WriteRune(',')
			}
			result.WriteRune(r)
		}

		// Add fractional part with 2 decimal places
		fracStr := fmt.Sprintf("%.2f", fracPart)
		if len(fracStr) > 2 {
			result.WriteString(fracStr[1:]) // Add just the decimal part
		}

		return result.String()

	default:
		// Custom format string
		// Try float64 first as it's more general
		f, err := n.Float64()
		if err == nil {
			return fmt.Sprintf(format, f)
		}

		// Maybe it's an integer format?
		i, err := n.Int64()
		if err == nil {
			return fmt.Sprintf(format, i)
		}

		// Fallback to original string
		return string(n)
	}
}

// setNull sets a reflect.Value to its zero value
func setNull(v reflect.Value) error {
	switch v.Kind() {
	case reflect.Interface, reflect.Ptr, reflect.Map, reflect.Slice:
		v.Set(reflect.Zero(v.Type()))
		return nil
	}

	return &UnmarshalTypeError{Value: "null", Type: v.Type()}
}

// setBool sets a reflect.Value to a boolean
func setBool(v reflect.Value, b bool) error {
	switch v.Kind() {
	case reflect.Bool:
		v.SetBool(b)
		return nil
	case reflect.Interface:
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf(b))
			return nil
		}
	}

	return &UnmarshalTypeError{Value: "bool", Type: v.Type()}
}

// setString sets a reflect.Value to a string
func setString(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
		return nil
	case reflect.Interface:
		if v.NumMethod() == 0 {
			v.Set(reflect.ValueOf(s))
			return nil
		}
	}

	return &UnmarshalTypeError{Value: "string", Type: v.Type()}
}

func writeEscapedString(w io.Writer, s []byte) {
	// Fast path for Buffer type - direct writing without interface calls
	if buf, ok := w.(*Buffer); ok {
		start := 0
		// Pre-grow buffer to avoid multiple resizes
		buf.grow(len(s) + 16) // Extra space for potential escapes

		for i := 0; i < len(s); i++ {
			if esc := escapeMap[s[i]]; esc != nil {
				// Write unescaped portion directly
				if start < i {
					copy(buf.buf[buf.off:], s[start:i])
					buf.off += i - start
				}

				// Write escape sequence directly
				copy(buf.buf[buf.off:], esc)
				buf.off += len(esc)
				start = i + 1
			}
		}

		// Write final unescaped portion
		if start < len(s) {
			copy(buf.buf[buf.off:], s[start:])
			buf.off += len(s) - start
		}

		return
	}

	// Fallback for non-Buffer writers
	start := 0
	for i := 0; i < len(s); i++ {
		if esc := escapeMap[s[i]]; esc != nil {
			if start < i {
				w.Write(s[start:i])
			}
			w.Write(esc)
			start = i + 1
		}
	}

	if start < len(s) {
		w.Write(s[start:])
	}
}

func writeEscapedStringString(w io.Writer, s string) {
	// Fast path for Buffer type - direct string handling
	if buf, ok := w.(*Buffer); ok {
		start := 0
		// Pre-grow buffer to avoid multiple resizes
		buf.grow(len(s) + 16) // Extra space for potential escapes

		for i := 0; i < len(s); i++ {
			if esc := escapeMap[s[i]]; esc != nil {
				// Write unescaped portion directly
				if start < i {
					buf.off += copy(buf.buf[buf.off:], s[start:i])
				}

				// Write escape sequence directly
				buf.off += copy(buf.buf[buf.off:], esc)
				start = i + 1
			}
		}

		// Write final unescaped portion
		if start < len(s) {
			buf.off += copy(buf.buf[buf.off:], s[start:])
		}

		return
	}

	// Fallback for non-Buffer writers (use the optimized writeEscapedString)
	writeEscapedString(w, []byte(s))
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

func setNumber(v reflect.Value, s string, decoder *Decoder) error {
	b := getBuilder()   // Get builder from pool
	defer putBuilder(b) // Return to pool when done

	// Helper function for type errors
	makeTypeError := func(s string, v reflect.Value, unsupported ...bool) *UnmarshalTypeError {
		b.WriteString("number")

		// Check if this is the "unsupported type" case
		if len(unsupported) > 0 && unsupported[0] {
			b.WriteString(" (unsupported type)")
		} else {
			b.WriteString(" ")
			b.WriteString(s)
		}

		return &UnmarshalTypeError{Value: b.String(), Type: v.Type()}
	}

	if v.Type().Name() == "Number" {
		v.SetString(s)
		return nil
	} else if v.Kind() == reflect.Interface && v.NumMethod() == 0 {
		// Use Number type if useNumber is enabled, otherwise use float64
		if decoder != nil && decoder.useNumber {
			v.Set(reflect.ValueOf(Number(s)))
			return nil
		} else {
			// Try float64 first for all numbers (standard behavior)
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				v.Set(reflect.ValueOf(n))
				return nil
			}
		}
	}

	// Regular type handling
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Fast path for small integers (avoids allocation in strconv.ParseInt)
		if len(s) < 10 && v.Kind() == reflect.Int64 || v.Kind() == reflect.Int {
			var n int64
			var neg bool

			// Check for negative sign
			i := 0
			if len(s) > 0 && s[0] == '-' {
				neg = true
				i++
			}

			// Manual parsing of digits
			for ; i < len(s); i++ {
				if s[i] < '0' || s[i] > '9' {
					// Not a simple integer, fall back to standard parsing
					goto standardIntParse
				}
				digit := int64(s[i] - '0')

				// Check for overflow: n*10 + digit > MaxInt64
				if n > (math.MaxInt64-digit)/10 {
					// Would overflow, fall back to standard parsing
					goto standardIntParse
				}

				n = n*10 + digit
			}

			// Apply sign
			if neg {
				n = -n
			}

			// Check type overflow
			if v.OverflowInt(n) {
				return makeTypeError(s, v)
			}

			v.SetInt(n)
			return nil
		}

	standardIntParse:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return makeTypeError(s, v)
		}
		if v.OverflowInt(n) {
			return makeTypeError(s, v)
		}
		v.SetInt(n)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		// Fast path for small unsigned integers
		if len(s) < 10 && v.Kind() == reflect.Uint64 || v.Kind() == reflect.Uint {
			var n uint64

			// Manual parsing of digits
			for i := 0; i < len(s); i++ {
				if s[i] < '0' || s[i] > '9' {
					// Not a simple integer, fall back to standard parsing
					goto standardUintParse
				}
				digit := uint64(s[i] - '0')

				// Check for overflow: n*10 + digit > MaxUint64
				if n > (math.MaxUint64-digit)/10 {
					// Would overflow, fall back to standard parsing
					goto standardUintParse
				}

				n = n*10 + digit
			}

			// Check type overflow
			if v.OverflowUint(n) {
				return makeTypeError(s, v)
			}

			v.SetUint(n)
			return nil
		}

	standardUintParse:
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return makeTypeError(s, v)
		}
		if v.OverflowUint(n) {
			return makeTypeError(s, v)
		}
		v.SetUint(n)
		return nil

	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, v.Type().Bits())
		if err != nil {
			return makeTypeError(s, v)
		}
		if v.OverflowFloat(n) {
			return makeTypeError(s, v)
		}
		v.SetFloat(n)
		return nil

	case reflect.Interface:
		if v.NumMethod() == 0 {
			// Try float64 first for all numbers (standard behavior)
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				v.Set(reflect.ValueOf(n))
				return nil
			}
		}

		// Just "number" for general type error
		return makeTypeError(s, v)
	}

	// Unsupported type
	return makeTypeError(s, v, true)
}

// isDigit returns true if c is an ASCII digit
func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// GetString converts a byte slice to a string without allocation
func GetString(b []byte) string {
	return *(*string)(unsafe.Pointer(&b))
}

// estimateKeySize provides a rough size estimate for a key (used for pre-allocation)
func estimateKeySize(key reflect.Value) int {
	switch key.Kind() {
	case reflect.String:
		return len(key.String()) + 2 // Quotes + content
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 12 // Rough max for int64
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return 12 // Rough max for uint64
	case reflect.Float32, reflect.Float64:
		return 16 // Rough max for float64
	case reflect.Bool:
		return 5 // "true" or "false"
	case reflect.Array, reflect.Slice:
		if key.Type().Elem().Kind() == reflect.Uint8 {
			return 2 + (key.Len() * 2) // "0x" + hex bytes
		}
		return 8 // "empty" or "array"
	case reflect.Struct:
		if key.Type().String() == "time.Time" {
			return len(time.RFC3339)
		}
		return 16 // Rough estimate for structs
	case reflect.Map:
		return 10 // Rough estimate for "map[n]"
	default:
		return 16 // Conservative default
	}
}

// estimateValueSize provides a rough size estimate for a value (used for pre-allocation)
func estimateValueSize(v reflect.Value) int {
	if !v.IsValid() || (v.Kind() == reflect.Ptr && v.IsNil()) {
		return 4 // "null"
	}

	switch v.Kind() {
	case reflect.Bool:
		return 5 // "true" or "false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return 12 // Rough max for int64
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return 12 // Rough max for uint64
	case reflect.Float32, reflect.Float64:
		return 16 // Rough max for float64
	case reflect.String:
		return len(v.String()) + 2 // Quotes + content
	case reflect.Array, reflect.Slice:
		if v.Len() == 0 {
			return 2 // "[]"
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return 2 + (v.Len() * 2) // "[" + bytes + "]"
		}
		return 2 + (v.Len() * 4) // Rough estimate for arrays
	case reflect.Map:
		if v.Len() == 0 {
			return 2 // "{}"
		}
		return 2 + (v.Len() * 8) // Rough estimate for maps
	case reflect.Struct:
		return 32 // Conservative estimate for structs
	default:
		return 16 // Conservative default
	}
}

// isEmptyValue reports whether v is considered empty for omitempty
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Ptr:
		return v.IsNil()
	case reflect.Struct:
		// Special case for time.Time
		if v.Type().String() == "time.Time" && v.CanInterface() {
			if t, ok := v.Interface().(time.Time); ok {
				return t.IsZero()
			}
		}
		// Otherwise check all fields
		for i := 0; i < v.NumField(); i++ {
			if !isEmptyValue(v.Field(i)) {
				return false
			}
		}
		return true
	}
	return false
}

// getCachedFields retrieves field information from cache or computes it
func getCachedFields(t reflect.Type) []Field {
	key := fieldCacheKey{rtype: t}

	// Check cache first
	if cached, ok := fieldCache.Load(key); ok {
		return cached.([]Field)
	}

	// Not in cache - compute field information
	fields := computeStructFields(t)

	// Store in cache for future use
	fieldCache.Store(key, fields)

	return fields
}

// Helper to check if character is whitespace
func isWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// Check if a literal is complete
func isCompleteLiteral(s string) bool {
	if len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		return false
	}
	// Check for null, true, false
	if s == "null" || s == "true" || s == "false" {
		return true
	}

	// Number validation
	if len(s) == 0 {
		return false
	}

	// Check if it's a properly formatted number
	i := 0

	// Handle negative sign
	if s[i] == '-' {
		i++
		if i >= len(s) {
			return false // Just a minus sign
		}
	}

	// First digit
	if s[i] == '0' {
		i++
		// After 0, we can only have decimal point or exponent
	} else if s[i] >= '1' && s[i] <= '9' {
		i++
		// Consume additional digits
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	} else {
		return false // Not a valid start to a number
	}

	// Decimal part
	if i < len(s) && s[i] == '.' {
		i++
		// Need at least one digit after decimal
		if i >= len(s) || s[i] < '0' || s[i] > '9' {
			return false
		}
		// Consume fractional digits
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}

	// Exponent part
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i >= len(s) {
			return false // Nothing after exponent marker
		}

		// Optional sign
		if s[i] == '+' || s[i] == '-' {
			i++
			if i >= len(s) {
				return false // Nothing after exponent sign
			}
		}

		// Need at least one digit in exponent
		if s[i] < '0' || s[i] > '9' {
			return false
		}

		// Consume exponent digits
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}

	// We should have consumed the entire string for a valid number
	return i == len(s)
}
