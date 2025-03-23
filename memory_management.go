package apexJSON

import (
	"io"
	"reflect"
	"strings"
	"sync"
)

var (
	builderPool = sync.Pool{
		New: func() interface{} {
			return &strings.Builder{}
		},
	}
	tinyBuffers = sync.Pool{
		New: func() interface{} {
			return &Buffer{buf: make([]byte, 0, 64)}
		},
	}
	smallBuffers = sync.Pool{
		New: func() interface{} {
			return &Buffer{buf: make([]byte, 0, 256)}
		},
	}
	mediumBuffers = sync.Pool{
		New: func() interface{} {
			return &Buffer{buf: make([]byte, 0, 1024)}
		},
	}
	largeBuffers = sync.Pool{
		New: func() interface{} {
			return &Buffer{buf: make([]byte, 0, 4096)}
		},
	}

	objectMapPool = sync.Pool{
		New: func() interface{} {
			return make(map[string]interface{}, 16)
		},
	}
	arraySlicePool = sync.Pool{
		New: func() interface{} {
			return make([]interface{}, 0, 16)
		},
	}

	syntaxErrorPool = sync.Pool{
		New: func() interface{} {
			return &SyntaxError{}
		},
	}
	tokenBufPool = sync.Pool{
		New: func() interface{} {
			b := make([]byte, 0, 1024)
			return &b
		},
	}
	keySlicePool = sync.Pool{
		New: func() interface{} {
			ksPool := make([]reflect.Value, 0, 16)
			return &ksPool
		},
	}
	fieldMapPool = sync.Pool{
		New: func() interface{} {
			return make(map[string]Field, 16) // Start with reasonable capacity
		},
	}
	numberBufPool = sync.Pool{
		New: func() interface{} {
			b := make([]byte, 0, 24) // Enough for most numeric conversions
			return &b
		},
	}
	// Index slice pool for struct fields
	indexSlicePool = sync.Pool{
		New: func() interface{} {
			isPool := make([]int, 0, 8)
			return &isPool
		},
	}
	keysPool = sync.Pool{
		New: func() interface{} {
			kP := make([]reflect.Value, 0, 16)
			return &kP
		},
	}

	fieldCache sync.Map
)

func init() {
	WarmupPools()
	commonTypes := []interface{}{
		"", 0, false,
		struct{}{},
		map[string]interface{}{},
		[]interface{}{},
	}

	for _, v := range commonTypes {
		t := reflect.TypeOf(v)
		if t.Kind() == reflect.Struct {
			getCachedFields(t)
		}
	}
}

// ### Buffer Pool Management ###

func WarmupPools() {
	for i := 0; i < 32; i++ {
		tinyBuffers.Put(&Buffer{buf: make([]byte, 0, 64)})
		smallBuffers.Put(&Buffer{buf: make([]byte, 0, 256)})
		mediumBuffers.Put(&Buffer{buf: make([]byte, 0, 1024)})
	}

	for i := 0; i < 4; i++ {
		largeBuffers.Put(&Buffer{buf: make([]byte, 0, 4096)})
	}
}

// getBuffer returns a buffer from the appropriate pool based on the requested size
// with optimized memory alignment and improved cache behavior
func getBuffer() *Buffer {
	return getBufferSize(256) // Default reasonable size for most JSON operations
}

// getBufferSize returns a buffer with at least the specified capacity
func getBufferSize(sizeHint int) *Buffer {
	var buf *Buffer

	// Fast path for common size ranges
	if sizeHint <= 64 {
		buf = tinyBuffers.Get().(*Buffer)
	} else if sizeHint <= 256 {
		buf = smallBuffers.Get().(*Buffer)
	} else if sizeHint <= 4096 {
		buf = mediumBuffers.Get().(*Buffer)
	} else {
		// For large buffers, round up to power of 2 for better memory alignment
		// This helps with cache line optimization and reduces fragmentation
		sizeHint--
		sizeHint |= sizeHint >> 1
		sizeHint |= sizeHint >> 2
		sizeHint |= sizeHint >> 4
		sizeHint |= sizeHint >> 8
		sizeHint |= sizeHint >> 16
		sizeHint++

		// Don't create excessively large buffers that won't be reused
		if sizeHint > 65536 {
			// For very large buffers, round to nearest 4KB page
			alignedSize := (sizeHint + 4095) &^ 4095
			buf = &Buffer{buf: make([]byte, 0, alignedSize)}
		} else {
			buf = largeBuffers.Get().(*Buffer)
			// If the buffer is too small, replace it
			if cap(buf.buf) < sizeHint {
				buf.buf = make([]byte, 0, sizeHint)
			}
		}
	}

	// Inline Reset for performance (avoids function call overhead)
	buf.buf = buf.buf[:0]
	buf.off = 0

	return buf
}

// Return a buffer to the appropriate pool after use
func putBuffer(buf *Buffer) {
	if buf == nil || cap(buf.buf) > 65536 {
		return
	}
	buf.Reset()

	// Use bitmask for size classification
	switch {
	case cap(buf.buf) <= 64:
		tinyBuffers.Put(buf)
	case cap(buf.buf) <= 256:
		smallBuffers.Put(buf)
	case cap(buf.buf) <= 4096:
		mediumBuffers.Put(buf)
	default:
		largeBuffers.Put(buf)
	}
}

// ### Builder Management ###

func getBuilder() *strings.Builder {
	v := builderPool.Get()
	if v == nil {
		return new(strings.Builder)
	}
	b, ok := v.(*strings.Builder)
	if !ok {
		return new(strings.Builder)
	}
	b.Reset()
	return b
}

func putBuilder(b *strings.Builder) {
	builderPool.Put(b)
}

// ### Object Map Pool Management ###

func getObjectMap() map[string]interface{} {
	return objectMapPool.Get().(map[string]interface{})
}

func putObjectMap(m map[string]interface{}) {
	if len(m) > 1024 {
		return // Don't pool oversize maps
	}
	for k := range m {
		delete(m, k)
	}
	objectMapPool.Put(m)
}

// ### Array Slice Pool Management ###

func getArraySlice() []interface{} {
	return arraySlicePool.Get().([]interface{})
}

func putArraySlice(s []interface{}) {
	s = s[:0]
	arraySlicePool.Put(s)
}

// ### Syntax Error Pool Management ###

func getSyntaxError() *SyntaxError {
	return syntaxErrorPool.Get().(*SyntaxError)
}

func putSyntaxError(e *SyntaxError) {
	e.Offset = 0
	e.Msg = ""
	syntaxErrorPool.Put(e)
}

// ### Token Buffer Pool Management ###

func getTokenBuf() *[]byte {
	return tokenBufPool.Get().(*[]byte)
}

func putTokenBuf(buf *[]byte) {
	*buf = (*buf)[:0] // Reset length but preserve capacity
	tokenBufPool.Put(buf)
}

// ### Key Slice Pool Management ###

func getKeysSlice() *[]reflect.Value {
	return keySlicePool.Get().(*[]reflect.Value)
}

func putKeysSlice(keys *[]reflect.Value) {
	*keys = (*keys)[:0]
	keySlicePool.Put(keys)
}

// ### Field Map Pool Management ###

func getFieldMap() map[string]Field {
	return fieldMapPool.Get().(map[string]Field)
}

func putFieldMap(m map[string]Field) {
	for k := range m {
		delete(m, k)
	}
	fieldMapPool.Put(m)
}

// ### Number Buffer Pool Management ###

func getNumberBuf() *[]byte {
	return numberBufPool.Get().(*[]byte)
}

func putNumberBuf(buf *[]byte) {
	*buf = (*buf)[:0] // Reset length but preserve capacity
	numberBufPool.Put(buf)
}

// ### Index Slice Pool Management ###

func getIndexSlice() []int {
	return indexSlicePool.Get().([]int)[:0]
}

func putIndexSlice(indexes []int) {
	indexSlicePool.Put(indexes[:0])
}

func (b *Buffer) Write(p []byte) (n int, err error) {
	b.grow(len(p))
	return copy(b.buf[b.off:], p), nil
}

func (b *Buffer) WriteByte(c byte) error {
	b.grow(1)
	b.buf[b.off] = c
	b.off++
	return nil
}

// func (b *Buffer) grow(n int) {
// 	needed := b.off + n

// 	if needed <= cap(b.buf) {
// 		b.buf = b.buf[:needed]
// 		return
// 	}

// 	// New growth strategy
// 	newCap := cap(b.buf)
// 	switch {
// 	case newCap == 0:
// 		newCap = 64
// 	case newCap < 4096:
// 		newCap <<= 1 // Double until 4KB
// 	default:
// 		newCap += newCap / 4 // 25% growth beyond 4KB
// 	}

// 	if newCap < needed {
// 		newCap = needed
// 	}

// 	newBuf := make([]byte, needed, newCap)
// 	copy(newBuf, b.buf[:b.off])
// 	b.buf = newBuf
// }

func (b *Buffer) grow(n int) {
	needed := b.off + n
	if needed <= cap(b.buf) {
		b.buf = b.buf[:needed]
		return
	}

	// Pre-estimate buffer size more accurately to reduce reallocations
	curCap := cap(b.buf)
	var newCap int

	if curCap == 0 {
		// Start with power-of-two sizes for better memory alignment
		newCap = 64
		for newCap < needed {
			newCap <<= 1
		}
	} else if curCap < 512 {
		// For very small buffers (highly optimized for JSON keys/small strings)
		newCap = max(curCap*4, needed)
		// Round to next power of two for small buffers
		newCap--
		newCap |= newCap >> 1
		newCap |= newCap >> 2
		newCap |= newCap >> 4
		newCap |= newCap >> 8
		newCap++
	} else if curCap < 8192 { // 8KB instead of 4KB
		// For small buffers, double
		newCap = max(curCap*2, needed)
	} else {
		// For larger buffers, use more aggressive growth (50%)
		// The profile shows we're doing too many small growths
		newCap = max(curCap+(curCap/2), needed)
	}

	// Same max cap and buffer creation
	const maxBufferSize = 32 * 1024 * 1024
	if newCap > maxBufferSize && needed <= maxBufferSize {
		newCap = maxBufferSize
	}

	newBuf := make([]byte, needed, newCap)
	copy(newBuf, b.buf[:b.off])
	b.buf = newBuf
}

func (b *Buffer) Reset() {
	b.buf = b.buf[:0]
	b.off = 0
}

func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	var total int64
	for {
		if b.off == len(b.buf) {
			b.grow(1024)
		}
		n, err := r.Read(b.buf[b.off:cap(b.buf)])
		b.off += n
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
}

func (b *Buffer) ReadString(length int) string {
	if b.off+length > len(b.buf) {
		length = len(b.buf) - b.off
	}
	s := GetString(b.buf[b.off : b.off+length])
	b.off += length
	return s
}

func (b *Buffer) Seek(offset int) {
	if offset >= 0 && offset <= len(b.buf) {
		b.off = offset
	}
}

func AppendBuffers(buffers [][]byte) []byte {
	// Early return for empty or single buffer cases
	if len(buffers) == 0 {
		return []byte{}
	}
	if len(buffers) == 1 {
		result := make([]byte, len(buffers[0]))
		copy(result, buffers[0])
		return result
	}

	// Calculate total size once
	totalSize := 0
	for _, b := range buffers {
		totalSize += len(b)
	}

	// Get pooled buffer of appropriate size
	buf := getBufferSize(totalSize)
	defer putBuffer(buf)

	// Single append for each buffer
	for _, b := range buffers {
		buf.Write(b)
	}

	// Create result copy
	result := make([]byte, buf.off)
	copy(result, buf.buf[:buf.off])

	return result
}

func (b *Buffer) Bytes() []byte {
	return b.buf[b.off:]
}

// Add this method to your Buffer type
func (b *Buffer) WriteString(s string) (int, error) {
	// Pre-grow the buffer if needed
	if b.off+len(s) > cap(b.buf) {
		b.grow(len(s))
	}

	// Copy the string directly into the buffer's slice
	n := copy(b.buf[b.off:], s)
	b.off += n
	return n, nil
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

// computeStructFields analyzes a struct type and extracts field information
func computeStructFields(t reflect.Type) []Field {
	// Pre-allocate fields slice with exact capacity needed
	numField := t.NumField()
	fields := make([]Field, 0, numField)

	for i := 0; i < numField; i++ {
		f := t.Field(i)

		// Skip unexported fields
		if f.PkgPath != "" {
			continue
		}

		// Get JSON field name from tag
		name := f.Name
		tag := f.Tag.Get("json")

		if tag == "-" {
			// Field is explicitly excluded
			continue
		}

		// Parse tag without allocations
		omitEmpty := false
		if tag != "" {
			// Find first comma in tag
			commaIndex := -1
			for j := 0; j < len(tag); j++ {
				if tag[j] == ',' {
					commaIndex = j
					break
				}
			}

			// Extract name part
			if commaIndex != -1 {
				if commaIndex > 0 {
					name = tag[:commaIndex]
				}

				// Check for omitempty flag without allocations
				tagRest := tag[commaIndex+1:]
				j := 0
				for j < len(tagRest) {
					// Find start of option
					optionStart := j

					// Find end of option (next comma or end of string)
					for j < len(tagRest) && tagRest[j] != ',' {
						j++
					}

					// Check if this option is "omitempty"
					option := tagRest[optionStart:j]
					if option == "omitempty" {
						omitEmpty = true
						break
					}

					// Move past comma
					j++
				}
			} else if tag != "" {
				// No comma, the whole tag is the name
				name = tag
			}
		}

		// Create and append Field
		// Create index slice only once per field
		index := make([]int, len(f.Index))
		copy(index, f.Index)

		// Create nameBytes only once
		nameBytes := []byte(name)

		// Create the nameWithQuotes format more efficiently
		nameWithQuotesBytes := make([]byte, len(name)+3) // "name":
		nameWithQuotesBytes[0] = '"'
		copy(nameWithQuotesBytes[1:], nameBytes)
		nameWithQuotesBytes[len(name)+1] = '"'
		nameWithQuotesBytes[len(name)+2] = ':'

		fields = append(fields, Field{
			nameBytes:           nameBytes,
			nameWithQuotesBytes: nameWithQuotesBytes,
			index:               index,
			omitEmpty:           omitEmpty,
		})
	}

	return fields
}
