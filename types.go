package apexJSON

import (
	"io"
	"reflect"
)

// ### Type Definitions ###

// Marshaler is the interface implemented by types that can marshal themselves into JSON
type Marshaler interface {
	MarshalJSON() ([]byte, error)
}

// Unmarshaler is the interface implemented by types that can unmarshal JSON data
type Unmarshaler interface {
	UnmarshalJSON([]byte) error
}

// SyntaxError optimized for 8-byte alignment
type SyntaxError struct {
	Msg    string // 16 bytes (ptr + len)
	Offset int64  // 8 bytes
}

// UnmarshalTypeError with fields arranged from largest to smallest
type UnmarshalTypeError struct {
	Type   reflect.Type // 16 bytes (interface)
	Value  string       // 16 bytes (ptr + len)
	Field  string       // 16 bytes (ptr + len)
	Offset int64        // 8 bytes
}

// Parser with slice first for better alignment
type Parser struct {
	data []byte // 24 bytes (ptr + len + cap)
	pos  int    // 8 bytes
}

// Encoder optimized to minimize padding
type Encoder struct {
	w          io.Writer // 16 bytes (interface)
	buf        *Buffer   // 8 bytes (ptr)
	escapeHTML bool      // 1 byte (padded to 8)
	// 7 bytes padding here, could add future fields
}

// Decoder optimized with slices grouped together and largest fields first
type Decoder struct {
	buf      []byte    // 24 bytes (ptr + len + cap)
	tokenBuf []byte    // 24 bytes (ptr + len + cap)
	r        io.Reader // 16 bytes (interface)
	readPos  int       // 8 bytes
}

// Field with slices grouped together and bool at the end to minimize padding
type Field struct {
	nameBytes           []byte // 24 bytes (ptr + len + cap)
	nameWithQuotesBytes []byte // 24 bytes (ptr + len + cap)
	index               []int  // 24 bytes (ptr + len + cap)
	omitEmpty           bool   // 1 byte (padded to 8)
	// 7 bytes padding here, could add future fields
}

// Buffer with largest field first
type Buffer struct {
	buf []byte // 24 bytes (ptr + len + cap)
	off int    // 8 bytes
}

type fieldCacheKey struct {
	rtype reflect.Type
}

type tagOptions string
