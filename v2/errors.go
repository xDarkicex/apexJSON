package apexjson

import "fmt"

// SyntaxError describes a JSON parse failure. Offset is the byte offset
// into the source where the error was detected. Convert to line/column
// with OffsetToLineCol when you have the source available.
type SyntaxError struct {
	Offset int64
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("apexjson: %s at offset %d", e.Msg, e.Offset)
}

// OffsetToLineCol converts a byte offset into src to 1-based line and
// column by walking src[:offset] counting '\n' bytes. O(offset) but
// only called on error paths, never in the hot path.
func OffsetToLineCol(src []byte, offset int64) (line, column int) {
	if offset < 0 {
		offset = 0
	}
	if offset > int64(len(src)) {
		offset = int64(len(src))
	}
	line = 1
	column = 1
	for i := int64(0); i < offset; i++ {
		if src[i] == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return
}

// ErrUnsupportedType is returned by Marshal when given a value whose
// concrete type the encoder cannot represent.
type ErrUnsupportedType struct {
	Type string
}

func (e *ErrUnsupportedType) Error() string {
	return fmt.Sprintf("apexjson: unsupported type: %s", e.Type)
}

// ErrTypeMismatch is returned by Unmarshal when the JSON value's kind
// doesn't match the destination Go type (e.g., decoding an object into
// a string pointer).
type ErrTypeMismatch struct {
	SrcKind string
	DstType string
}

func (e *ErrTypeMismatch) Error() string {
	return fmt.Sprintf("apexjson: cannot unmarshal %s into %s", e.SrcKind, e.DstType)
}