package apexjson

import (
	"errors"
	"reflect"
)

// RawMessage is a borrowed raw JSON value.
//
// Unlike encoding/json.RawMessage, native UnmarshalJSON stores the supplied
// bytes without copying. Keep the input alive and unchanged for as long as
// the RawMessage is in use. Call Clone when an owned copy is required.
type RawMessage []byte

var (
	rawMessageType   = reflect.TypeOf(RawMessage(nil))
	errRawMessageNil = errors.New("apexjson.RawMessage: UnmarshalJSON on nil pointer")
)

// MarshalJSON returns the raw JSON bytes without copying. A nil RawMessage is
// encoded as JSON null, matching encoding/json.RawMessage.
func (m RawMessage) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return m, nil
}

// UnmarshalJSON borrows data instead of copying. The native Unmarshal entry
// point validates the complete document before assigning a root RawMessage;
// nested RawMessages are called after the parser validates their document.
func (m *RawMessage) UnmarshalJSON(data []byte) error {
	if m == nil {
		return errRawMessageNil
	}
	*m = RawMessage(data)
	return nil
}

// String returns the raw JSON as a string.
func (m RawMessage) String() string { return string(m) }

// Clone returns an owned copy of m. It is the safe choice when the source
// buffer may be reused or mutated after decoding.
func (m RawMessage) Clone() RawMessage {
	if m == nil {
		return nil
	}
	out := make(RawMessage, len(m))
	copy(out, m)
	return out
}
