package apexjson

import (
	"errors"
	"strconv"
)

// Value is a lazy reference into parsed JSON. It holds the source bytes and a
// pointer to the columnar tape; no node is copied into the Value. A Value can
// be mutated through pointer-receiver Set*/Mutate* methods. The first such
// mutation CoW-clones the borrowed source into the off-heap memory arena.
type Value struct {
	src   []byte
	slab  *slab
	owner *parsedDoc // keeps one-shot mmap storage alive while the Value is live
	idx   int32
	epoch uint64
	found bool
}

func valueAt(doc *parsedDoc, idx int32) Value {
	if doc == nil {
		return Value{}
	}
	return Value{
		src:   doc.src,
		slab:  &doc.slab,
		owner: doc,
		idx:   idx,
		epoch: doc.generation,
		found: true,
	}
}

// Type returns the JSON type of the value.
func (v Value) Type() Type {
	if v.owner != nil && v.epoch != v.owner.generation {
		return TypeNull
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n {
		return TypeNull
	}
	switch v.slab.kind(v.idx) {
	case nodeObject:
		return TypeObject
	case nodeArray:
		return TypeArray
	case nodeString:
		return TypeString
	case nodeNumber:
		return TypeNumber
	case nodeTrue, nodeFalse:
		return TypeBool
	case nodeNull:
		return TypeNull
	}
	return TypeNull
}

// Type is the set of JSON value kinds.
type Type int

const (
	TypeNull Type = iota
	TypeBool
	TypeNumber
	TypeString
	TypeArray
	TypeObject
)

func (t Type) String() string {
	switch t {
	case TypeNull:
		return "null"
	case TypeBool:
		return "bool"
	case TypeNumber:
		return "number"
	case TypeString:
		return "string"
	case TypeArray:
		return "array"
	case TypeObject:
		return "object"
	}
	return "?"
}

// Exists reports whether the path resolved to a value.
func (v Value) Exists() bool {
	return v.found && (v.owner == nil || v.epoch == v.owner.generation)
}

// Str returns the string value. Returns "" if not a string.
func (v Value) Str() string {
	if v.owner != nil && v.epoch != v.owner.generation {
		return ""
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n || v.slab.kind(v.idx) != nodeString {
		return ""
	}
	src := v.source()
	flags := v.slab.flag(v.idx)
	start, end := v.slab.start(v.idx), v.slab.end(v.idx)
	return bytesToString(unescapeJSONStringFlags(flags, src[start:end]))
}

// Bytes returns the raw bytes of the value (for strings, no quotes; for
// numbers, the literal; for objects/arrays, the full value; for null and
// bools, their literal).
func (v Value) Bytes() []byte {
	if v.owner != nil && v.epoch != v.owner.generation {
		return nil
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n {
		return nil
	}
	src := v.source()
	switch v.slab.kind(v.idx) {
	case nodeTrue:
		return []byte("true")
	case nodeFalse:
		return []byte("false")
	case nodeNull:
		return []byte("null")
	}
	return src[v.slab.start(v.idx):v.slab.end(v.idx)]
}

// RawJSON returns the complete borrowed JSON span for this value. String
// values include their quotes; containers include their delimiters. The
// returned slice aliases the decoder's source and becomes invalid after
// Parse, Reset, Free, or any mutation that reparses the document.
func (v Value) RawJSON() []byte {
	if v.owner != nil && v.epoch != v.owner.generation {
		return nil
	}
	start, end, ok := v.rawSpan()
	if !ok {
		return nil
	}
	return v.source()[start:end]
}

// Int parses the number as int64. Returns 0 on failure.
func (v Value) Int() int64 {
	n, _ := parseIntBytes(v.Bytes(), 64)
	return n
}

// Float parses the number as float64. Returns 0 on failure.
func (v Value) Float() float64 {
	f, _ := parseFloatBytes(v.Bytes(), 64)
	return f
}

// Bool returns the boolean value.
func (v Value) Bool() bool {
	return v.Exists() && v.slab != nil && v.slab.kind(v.idx) == nodeTrue
}

// IsNull reports whether the value is JSON null.
func (v Value) IsNull() bool {
	return v.Exists() && v.slab != nil && v.slab.kind(v.idx) == nodeNull
}

// Array returns the value's elements if it is an array. Otherwise nil.
func (v Value) Array() []Value {
	if v.owner != nil && v.epoch != v.owner.generation {
		return nil
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n || v.slab.kind(v.idx) != nodeArray {
		return nil
	}
	out := make([]Value, 0, 8)
	for c := firstChild(v.slab, v.idx); c != noNode; {
		out = append(out, valueAt(v.owner, c))
		c = nextChild(v.slab, v.idx, c)
	}
	return out
}

// Map returns the value's fields if it is an object. The map key is the JSON
// key string; the value is a lazy Value for the field.
func (v Value) Map() map[string]Value {
	if v.owner != nil && v.epoch != v.owner.generation {
		return nil
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n || v.slab.kind(v.idx) != nodeObject {
		return nil
	}
	src := v.source()
	out := make(map[string]Value, 8)
	for c := firstChild(v.slab, v.idx); c != noNode; {
		start, end := v.slab.start(c), v.slab.end(c)
		valIdx := c + 1
		out[string(src[start:end])] = valueAt(v.owner, valIdx)
		c = nextChild(v.slab, v.idx, valIdx)
	}
	return out
}

// Get extracts a value at the given path.
func Get(data []byte, path ...string) Value {
	doc, err := parse(data)
	if err != nil {
		return Value{}
	}
	v := valueAt(doc, doc.root)
	for _, p := range path {
		v = v.get(p)
		if !v.found {
			return v
		}
	}
	return v
}

// GetPath is like Get but takes a pre-built []string path.
func GetPath(data []byte, path []string) Value {
	doc, err := parse(data)
	if err != nil {
		return Value{}
	}
	v := valueAt(doc, doc.root)
	for _, p := range path {
		v = v.get(p)
		if !v.found {
			return v
		}
	}
	return v
}

// GetMany returns multiple values at the given paths in one parse.
func GetMany(data []byte, paths [][]string) []Value {
	doc, err := parse(data)
	if err != nil {
		return make([]Value, len(paths))
	}
	out := make([]Value, len(paths))
	for i, path := range paths {
		v := valueAt(doc, doc.root)
		for _, p := range path {
			v = v.get(p)
			if !v.found {
				break
			}
		}
		out[i] = v
	}
	return out
}

// get follows one path step. p is either a JSON key or an array index.
func (v Value) get(p string) Value {
	if v.owner != nil && v.epoch != v.owner.generation {
		return Value{}
	}
	if !v.found || v.slab == nil || v.idx == noNode || int(v.idx) >= v.slab.n {
		return Value{}
	}
	src := v.source()
	switch v.slab.kind(v.idx) {
	case nodeObject:
		for c := firstChild(v.slab, v.idx); c != noNode; {
			start, end := v.slab.start(c), v.slab.end(c)
			valIdx := c + 1
			if bytesEqualString(src[start:end], p) {
				return valueAt(v.owner, valIdx)
			}
			c = nextChild(v.slab, v.idx, valIdx)
		}
	case nodeArray:
		idx, err := strconv.Atoi(p)
		if err != nil || idx < 0 {
			return Value{}
		}
		c := firstChild(v.slab, v.idx)
		for i := 0; i < idx && c != noNode; i++ {
			c = nextChild(v.slab, v.idx, c)
		}
		if c != noNode {
			return valueAt(v.owner, c)
		}
	}
	return Value{}
}

func bytesEqualString(a []byte, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ErrPathNotFound is returned when an extraction path doesn't resolve.
var ErrPathNotFound = errors.New("apexjson: path not found")

func GetString(data []byte, path ...string) (string, bool) {
	v := Get(data, path...)
	if !v.Exists() || v.Type() != TypeString {
		return "", false
	}
	return v.Str(), true
}

func GetInt(data []byte, path ...string) (int64, bool) {
	v := Get(data, path...)
	if !v.Exists() || v.Type() != TypeNumber {
		return 0, false
	}
	return v.Int(), true
}

func GetBool(data []byte, path ...string) (bool, bool) {
	v := Get(data, path...)
	if !v.Exists() || v.Type() != TypeBool {
		return false, false
	}
	return v.Bool(), true
}
