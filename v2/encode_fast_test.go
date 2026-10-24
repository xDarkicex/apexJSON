package apexjson

import (
	"math"
	"testing"
)

// The reflective shapes below are named types, so they do not match the
// encoder's type switch and take the reflect path instead. The fast path and
// the reflect path must produce the same bytes for the same data.

type rMap map[string]any

type rSlice []any

type rString string

type rInt int

type rInt32 int32

type rInt64 int64

type rUint uint

type rUint32 uint32

type rUint64 uint64

type rFloat32 float32

type rFloat64 float64

type rBool bool

type rMapString map[string]string

type rSliceString []string

// TestFastPathMatchesReflectPath checks that the reflect-free path produces
// byte-identical output to the reflective path for every JSON shape.
func TestFastPathMatchesReflectPath(t *testing.T) {
	nested := map[string]any{
		"a": "one",
		"b": float64(2),
		"c": []any{"x", "y", nil},
		"d": map[string]any{"deep": true},
		"e": nil,
	}
	cases := []struct {
		name string
		fast any
		refl any
	}{
		{"nil", nil, nil},
		{"string", "hello", rString("hello")},
		{"string-escapes", "a\"b\\c\nd\te", rString("a\"b\\c\nd\te")},
		{"string-empty", "", rString("")},
		{"bool-true", true, rBool(true)},
		{"bool-false", false, rBool(false)},
		{"int", 42, rInt(42)},
		{"int-negative", -42, rInt(-42)},
		{"int32", int32(42), rInt32(42)},
		{"int64", int64(42), rInt64(42)},
		{"uint", uint(42), rUint(42)},
		{"uint32", uint32(42), rUint32(42)},
		{"uint64", uint64(42), rUint64(42)},
		{"float32", float32(1.5), rFloat32(1.5)},
		{"float64", float64(1.5), rFloat64(1.5)},
		{"float64-whole", float64(3), rFloat64(3)},
		{"float64-nan", math.NaN(), rFloat64(math.NaN())},
		{"float64-inf", math.Inf(1), rFloat64(math.Inf(1))},
		{"map-empty", map[string]any{}, rMap{}},
		{"map", map[string]any{"a": "one"}, rMap{"a": "one"}},
		{"map-nested", nested, rMap(nested)},
		{"slice-empty", []any{}, rSlice{}},
		{"slice", []any{"a", 1, true, nil}, rSlice{"a", 1, true, nil}},
		{"slice-nested", []any{map[string]any{"k": "v"}}, rSlice{map[string]any{"k": "v"}}},
		{"map-string", map[string]string{"a": "b"}, rMapString{"a": "b"}},
		{"slice-string", []string{"a", "b"}, rSliceString{"a", "b"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fast, err := Marshal(testCase.fast)
			if err != nil {
				t.Fatalf("Marshal fast: %v", err)
			}
			refl, err := Marshal(testCase.refl)
			if err != nil {
				t.Fatalf("Marshal reflect: %v", err)
			}
			if string(fast) != string(refl) {
				t.Errorf("fast = %s, reflect = %s", fast, refl)
			}
		})
	}
}

// TestFastPathMatchesReflectPathNested checks the same for values nested
// inside a container, where the fast path recurses through encodeAny.
func TestFastPathMatchesReflectPathNested(t *testing.T) {
	fast := map[string]any{
		"list": []any{
			map[string]any{"x": []any{1, 2, 3}},
			"plain",
		},
	}
	refl := rMap{
		"list": rSlice{
			rMap{"x": rSlice{int64(1), int64(2), int64(3)}},
			rString("plain"),
		},
	}
	fastOut, err := Marshal(fast)
	if err != nil {
		t.Fatalf("Marshal fast: %v", err)
	}
	reflOut, err := Marshal(refl)
	if err != nil {
		t.Fatalf("Marshal reflect: %v", err)
	}
	if string(fastOut) != string(reflOut) {
		t.Errorf("fast = %s, reflect = %s", fastOut, reflOut)
	}
}

// TestFastPathSortsKeys checks that the fast path is byte-stable, the way the
// reflective path is.
func TestFastPathSortsKeys(t *testing.T) {
	m := map[string]any{"c": 1, "a": 2, "b": 3}
	first, err := Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"a":2,"b":3,"c":1}`; string(first) != want {
		t.Errorf("got %s, want %s", first, want)
	}
	for i := 0; i < 32; i++ {
		again, err := Marshal(m)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(again) != string(first) {
			t.Fatalf("output is not stable: %s vs %s", again, first)
		}
	}
}
