package apexjson

import (
	"bytes"
	"strconv"
)

// Number preserves the exact JSON number token when a caller needs
// encoding/json.Number-compatible semantics at a native API boundary.
//
// Decoder.Value.Bytes is preferred for zero-copy inspection. Number is for
// values that must cross an interface boundary without converting integers
// and fraction/exponent forms to float64.
type Number string

func (n Number) String() string { return string(n) }

func (n Number) Float64() (float64, error) { return strconv.ParseFloat(string(n), 64) }

func (n Number) Int64() (int64, error) { return strconv.ParseInt(string(n), 10, 64) }

func (n Number) MarshalJSON() ([]byte, error) {
	b := bytes.TrimSpace([]byte(n))
	if len(b) == 0 {
		return nil, &SyntaxError{Msg: "empty JSON number"}
	}
	var d directDecoder
	d.src = b
	d.scan.reset(b)
	tok, err := d.nextValueToken()
	if err != nil {
		return nil, err
	}
	if tok.Kind != kindNumber {
		return nil, &SyntaxError{Msg: "invalid JSON number"}
	}
	if _, more := d.scan.next(); more {
		return nil, &SyntaxError{Msg: "invalid JSON number"}
	}
	return b, nil
}

// UnmarshalJSON retains the number token instead of converting it to
// float64. This mirrors encoding/json.Number and preserves integer,
// fraction, and exponent forms for callers that need exact JSON semantics.
func (n *Number) UnmarshalJSON(data []byte) error {
	if n == nil {
		return &SyntaxError{Msg: "nil JSON number target"}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return &SyntaxError{Msg: "empty JSON number"}
	}
	var d directDecoder
	d.src = data
	d.scan.reset(data)
	tok, err := d.nextValueToken()
	if err != nil {
		return err
	}
	if tok.Kind != kindNumber {
		return &SyntaxError{Msg: "invalid JSON number"}
	}
	if _, more := d.scan.next(); more {
		return &SyntaxError{Msg: "invalid JSON number"}
	}
	*n = Number(data)
	return nil
}
