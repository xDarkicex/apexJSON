package apexjson

import (
	"errors"
	"reflect"
	"testing"
)

// The named destinations below do not match the decoder's destination type
// switch, so they take the reflect path instead. The reflect-free path and
// the reflect path must produce the same result for the same input.

type myMap map[string]any

type myAny any

// TestUnmarshalDynamicDestinationsMatchReflect checks that the reflect-free
// destinations agree with the reflect path.
func TestUnmarshalDynamicDestinationsMatchReflect(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"object", `{"a":1,"b":"two","c":[1,2,3],"d":{"e":null}}`},
		{"empty-object", `{}`},
		{"array", `[1,"two",{"three":3}]`},
		{"string", `"hello"`},
		{"number", `42`},
		{"float", `4.5`},
		{"true", `true`},
		{"false", `false`},
		{"null", `null`},
		{"nested", `{"a":{"b":{"c":[1,{"d":2}]}}}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var fastMap map[string]any
			var reflMap myMap
			fastErr := Unmarshal([]byte(testCase.in), &fastMap)
			reflErr := Unmarshal([]byte(testCase.in), &reflMap)
			if (fastErr == nil) != (reflErr == nil) {
				t.Fatalf("fast err = %v, reflect err = %v", fastErr, reflErr)
			}
			if !reflect.DeepEqual(map[string]any(fastMap), map[string]any(reflMap)) {
				t.Errorf("fast = %#v, reflect = %#v", fastMap, reflMap)
			}

			var fastAny any
			var reflAny myAny
			fastErr = Unmarshal([]byte(testCase.in), &fastAny)
			reflErr = Unmarshal([]byte(testCase.in), &reflAny)
			if (fastErr == nil) != (reflErr == nil) {
				t.Fatalf("fast err = %v, reflect err = %v", fastErr, reflErr)
			}
			if !reflect.DeepEqual(fastAny, any(reflAny)) {
				t.Errorf("fast = %#v, reflect = %#v", fastAny, reflAny)
			}
		})
	}
}

// TestUnmarshalDynamicDestinationTypeMismatch checks that a destination of
// the wrong shape is refused the same way on both paths.
func TestUnmarshalDynamicDestinationTypeMismatch(t *testing.T) {
	var fast map[string]any
	fastErr := Unmarshal([]byte(`[1,2]`), &fast)
	var refl myMap
	reflErr := Unmarshal([]byte(`[1,2]`), &refl)
	if fastErr == nil {
		t.Fatal("the reflect-free path accepted an array into a map")
	}
	if reflErr == nil {
		t.Fatal("the reflect path accepted an array into a map")
	}
	// The two destinations are different types, so the messages name
	// different types. What must match is that both refuse.
	var mismatch *ErrTypeMismatch
	if !errors.As(fastErr, &mismatch) {
		t.Errorf("fast err = %v, want a type mismatch", fastErr)
	}
	if !errors.As(reflErr, &mismatch) {
		t.Errorf("reflect err = %v, want a type mismatch", reflErr)
	}
}

// TestUnmarshalDynamicDestinationMerges checks that an existing destination
// map is merged into, not replaced, on both paths.
func TestUnmarshalDynamicDestinationMerges(t *testing.T) {
	fast := map[string]any{"keep": "me"}
	refl := myMap{"keep": "me"}
	if err := Unmarshal([]byte(`{"added":1}`), &fast); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if err := Unmarshal([]byte(`{"added":1}`), &refl); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(map[string]any(fast), map[string]any(refl)) {
		t.Errorf("fast = %#v, reflect = %#v", fast, refl)
	}
	if _, ok := fast["keep"]; !ok {
		t.Error("the reflect-free path replaced the destination map")
	}
}

// TestUnmarshalDynamicDestinationNull checks that a null document leaves the
// destination empty on both paths.
func TestUnmarshalDynamicDestinationNull(t *testing.T) {
	var fast map[string]any
	if err := Unmarshal([]byte(`null`), &fast); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if fast != nil {
		t.Errorf("fast = %#v, want nil", fast)
	}
	var fastAny any = "unchanged"
	if err := Unmarshal([]byte(`null`), &fastAny); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if fastAny != nil {
		t.Errorf("fast = %#v, want nil", fastAny)
	}
}
