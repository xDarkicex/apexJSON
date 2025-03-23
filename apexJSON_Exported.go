package apexJSON

import (
	"reflect"
)

// Exported only for testing
func MarshalValue(v reflect.Value, buf *Buffer) error {
	return marshalValue(v, buf)
}
