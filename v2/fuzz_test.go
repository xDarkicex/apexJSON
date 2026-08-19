package apexjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

// FuzzUnmarshalVsStdlib fuzzes random inputs against encoding/json and
// checks that we agree on whether the input is valid JSON. When both
// accept, our Unmarshal-into-bytes preserves byte equivalence.
func FuzzUnmarshalVsStdlib(f *testing.F) {
	// Seed corpus with interesting cases.
	f.Add([]byte(`null`))
	f.Add([]byte(`true`))
	f.Add([]byte(`42`))
	f.Add([]byte(`"hello"`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"a":1,"b":[true,null,2.5]}`))
	f.Add([]byte(`{"name":"alice","age":30,"tags":["x","y","z"]}`))
	f.Add([]byte(`[1,2,3,4,5,6,7,8,9,10]`))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Try stdlib; if it fails, our parser should also fail.
		var stdVal any
		stdErr := json.Unmarshal(data, &stdVal)

		var ourVal any
		ourErr := Unmarshal(data, &ourVal)

		switch {
		case stdErr == nil && ourErr != nil:
			t.Errorf("stdlib accepted %q but we rejected: %v", data, ourErr)
		case stdErr != nil && ourErr == nil:
			t.Errorf("stdlib rejected %q but we accepted: %v", data, stdErr)
		case stdErr == nil && ourErr == nil:
			// Both accepted — verify both decode to the same canonical
			// form. json.Marshal is sensitive to invalid UTF-8 in
			// strings (which both libraries accept but handle
			// differently on re-marshal), so compare via a custom
			// printer that preserves raw bytes.
			fmt.Fprintf(os.Stderr, "FUZZ input=%q stdVal=%#v ourVal=%#v\n", data, stdVal, ourVal)
			if !sameDecoded(stdVal, ourVal) {
				t.Errorf("mismatch on %q:\nstdlib: %s\nours:    %s",
					data, fmtJSON(stdVal), fmtJSON(ourVal))
			}
		}
	})
}

// fmtJSON produces a canonical JSON byte slice for comparing two decoded
// values. Numbers come back as string from our interface{} path; stdlib
// uses float64. We compare via Re-encoding.
func fmtJSON(v any) string {
	if v == nil {
		return "null"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// sameDecoded compares two decoded values structurally. We compare
// Go-typed fields directly rather than via json.Marshal because invalid
// UTF-8 inside string values causes the two libraries to round-trip
// differently through json.Marshal.
func sameDecoded(a, b any) bool {
	switch av := a.(type) {
	case nil:
		r := b == nil || isNilish(b)
		if !r {
			fmt.Fprintf(os.Stderr, "  nil mismatch: b=%#v\n", b)
		}
		return r
	case bool:
		bo, ok := b.(bool)
		return ok && av == bo
	case float64:
		f, ok := b.(float64)
		return ok && av == f
	case string:
		s, ok := b.(string)
		if !ok {
			fmt.Fprintf(os.Stderr, "  string type mismatch: b=%#v\n", b)
			return false
		}
		if av != s {
			fmt.Fprintf(os.Stderr, "  string content mismatch: a=%q b=%q\n", av, s)
			return false
		}
		return true
	case []any:
		bs, ok := b.([]any)
		if !ok {
			fmt.Fprintf(os.Stderr, "  slice type mismatch: b=%#v\n", b)
			return false
		}
		if len(av) != len(bs) {
			fmt.Fprintf(os.Stderr, "  slice len mismatch: %d vs %d\n", len(av), len(bs))
			return false
		}
		for i := range av {
			if !sameDecoded(av[i], bs[i]) {
				fmt.Fprintf(os.Stderr, "  slice elem %d\n", i)
				return false
			}
		}
		return true
	case map[string]any:
		bm, ok := b.(map[string]any)
		if !ok {
			fmt.Fprintf(os.Stderr, "  map type mismatch: b=%#v\n", b)
			return false
		}
		if len(av) != len(bm) {
			fmt.Fprintf(os.Stderr, "  map len mismatch: %d vs %d\n", len(av), len(bm))
			return false
		}
		for k, v := range av {
			if !sameDecoded(v, bm[k]) {
				fmt.Fprintf(os.Stderr, "  map key %q\n", k)
				return false
			}
		}
		return true
	}
	fmt.Fprintf(os.Stderr, "  unknown type: a=%T b=%T\n", a, b)
	return false
}

func isNilish(v any) bool {
	return v == nil
}

var _ = bytes.Equal