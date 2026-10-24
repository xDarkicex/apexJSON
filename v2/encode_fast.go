package apexjson

import (
	"math"
	"slices"
)

// encodeAnyFast encodes the JSON shapes without reflect.
//
// A type switch supplies the concrete type statically, so a container can be
// iterated natively: no reflect.Value, no MapKeys, no MapIndex, and no
// copyVal — reflect's copy of the map's values out one at a time was 29% of
// every object allocated on the map path. Elements are dispatched through
// encodeAny, which reads the interface header directly and resolves the type
// through the cached type table.
//
// The cases here are exact type matches. A named map or slice type does not
// match map[string]any, and only the unnamed builtin types match, so none of
// these can carry a MarshalJSON method. A value that does have one still goes
// down the reflective path, where the marshaler is found.
//
// The bool reports whether the value was handled.
func encodeAnyFast(v any, buf *[]byte) (bool, error) {
	switch value := v.(type) {
	case nil:
		*buf = append(*buf, "null"...)
		return true, nil
	case string:
		*buf = appendQuotedString(*buf, value)
		return true, nil
	case bool:
		if value {
			*buf = append(*buf, "true"...)
		} else {
			*buf = append(*buf, "false"...)
		}
		return true, nil
	case int:
		*buf = appendIntFast(*buf, int64(value))
		return true, nil
	case int32:
		*buf = appendIntFast(*buf, int64(value))
		return true, nil
	case int64:
		*buf = appendIntFast(*buf, value)
		return true, nil
	case uint:
		*buf = appendUintFast(*buf, uint64(value))
		return true, nil
	case uint32:
		*buf = appendUintFast(*buf, uint64(value))
		return true, nil
	case uint64:
		*buf = appendUintFast(*buf, value)
		return true, nil
	case float32:
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			*buf = append(*buf, "null"...)
		} else {
			*buf = appendFloat32Fast(*buf, value)
		}
		return true, nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			*buf = append(*buf, "null"...)
		} else {
			*buf = appendFloat64Fast(*buf, value)
		}
		return true, nil
	case map[string]any:
		return true, encodeStringAnyMap(value, buf)
	case []any:
		return true, encodeAnySlice(value, buf)
	case []string:
		return true, encodeStringSlice(value, buf)
	case map[string]string:
		return true, encodeStringStringMap(value, buf)
	}
	return false, nil
}

// encodePair is one map entry, carried through the sort so the map is read
// once rather than once for the keys and once for the values.
//
// Reading a map twice is not free: the second pass is a hash lookup per
// entry, which was a quarter of the map path's time.
type encodePair struct {
	key string
	val any
}

// mapStackScratch is the number of map entries whose scratch is held on the
// stack.
//
// Below it, the encoder sorts the entries in place with an insertion sort:
// it has no comparison closure, and the swaps are few enough that the write
// barriers on a pointer-bearing slice do not matter. Above it, pdqsort's
// comparisons cost less than the lookups, and the write barriers of moving
// whole entries cost more than both — so the entries are sorted by key and
// the values are looked back up.
const mapStackScratch = 16

// encodeStringAnyMap encodes a map[string]any without reflect.
//
// Keys are sorted, so the output is byte-stable for a given map, exactly as
// the reflective path is.
func encodeStringAnyMap(m map[string]any, buf *[]byte) error {
	*buf = append(*buf, '{')
	if len(m) == 0 {
		*buf = append(*buf, '}')
		return nil
	}
	if len(m) <= mapStackScratch {
		// The scratch lives on the stack. A document full of small objects
		// would otherwise allocate a slice per object, and the allocation
		// count is what a JSON encoder is judged on.
		var stack [mapStackScratch]encodePair
		pairs := stack[:0]
		for k, v := range m {
			pairs = append(pairs, encodePair{key: k, val: v})
		}
		// Insertion sort, written out rather than called: a helper taking
		// the slice would make the array escape to the heap.
		for i := 1; i < len(pairs); i++ {
			pair := pairs[i]
			j := i - 1
			for j >= 0 && pairs[j].key > pair.key {
				pairs[j+1] = pairs[j]
				j--
			}
			pairs[j+1] = pair
		}
		for i, pair := range pairs {
			if i > 0 {
				*buf = append(*buf, ',')
			}
			*buf = appendQuotedString(*buf, pair.key)
			*buf = append(*buf, ':')
			if err := encodeAny(pair.val, buf); err != nil {
				return err
			}
		}
		*buf = append(*buf, '}')
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		*buf = appendQuotedString(*buf, k)
		*buf = append(*buf, ':')
		if err := encodeAny(m[k], buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, '}')
	return nil
}

// encodeStringStringMap encodes a map[string]string without reflect.
func encodeStringStringMap(m map[string]string, buf *[]byte) error {
	*buf = append(*buf, '{')
	if len(m) == 0 {
		*buf = append(*buf, '}')
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for i, k := range keys {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		*buf = appendQuotedString(*buf, k)
		*buf = append(*buf, ':')
		*buf = appendQuotedString(*buf, m[k])
	}
	*buf = append(*buf, '}')
	return nil
}

// encodeAnySlice encodes a []any without reflect.
func encodeAnySlice(s []any, buf *[]byte) error {
	*buf = append(*buf, '[')
	for i, v := range s {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		if err := encodeAny(v, buf); err != nil {
			return err
		}
	}
	*buf = append(*buf, ']')
	return nil
}

// encodeStringSlice encodes a []string without reflect.
func encodeStringSlice(s []string, buf *[]byte) error {
	*buf = append(*buf, '[')
	for i, v := range s {
		if i > 0 {
			*buf = append(*buf, ',')
		}
		*buf = appendQuotedString(*buf, v)
	}
	*buf = append(*buf, ']')
	return nil
}
