//go:build go1.27

package apexjson

import "testing"

// TestMarshalMapGo127 pins the map encoder on the Go versions whose runtime
// type layout differs from the one the encoder was written against.
//
// The encoder's fast path reads the runtime type's kind byte to learn whether
// a value is stored directly in an interface. Go 1.27 moved the
// direct-interface flag out of that byte and into the TFlag byte, so the
// encoder misclassified every map as indirectly stored and walked a map header
// as though it were the address of a slot holding the map. The result was a
// garbage map pointer and a dead process, not an error.
//
// This file only builds on Go 1.27 and later, so the failure cannot come back
// unnoticed: a module that declares an older Go version still compiles and
// tests on the older toolchain, where the layout has not moved.
func TestMarshalMapGo127(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string-int", map[string]int{"a": 1, "b": 2}, `{"a":1,"b":2}`},
		{"string-string", map[string]string{"a": "b"}, `{"a":"b"}`},
		{"string-interface", map[string]interface{}{"a": "b", "n": int64(3)}, `{"a":"b","n":3}`},
		{"string-slice", map[string][]int{"a": {1}}, `{"a":[1]}`},
		{"nested", map[string]interface{}{"m": map[string]interface{}{"x": "y"}}, `{"m":{"x":"y"}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Marshal(testCase.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != testCase.want {
				t.Errorf("got %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestMarshalStructWithMapGo127 pins the same path for a struct that carries a
// map: the encoder reaches the map through the field, not through the
// interface word.
func TestMarshalStructWithMapGo127(t *testing.T) {
	type payload struct {
		Name string                 `json:"name"`
		Tags map[string]interface{} `json:"tags"`
	}
	got, err := Marshal(payload{"x", map[string]interface{}{"k": "v"}})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"name":"x","tags":{"k":"v"}}`; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
