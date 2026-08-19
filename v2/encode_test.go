package apexjson

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

type encPerson struct {
	Name   string   `json:"name"`
	Age    int      `json:"age"`
	Email  string   `json:"email,omitempty"`
	Tags   []string `json:"tags,omitempty"`
	Active bool     `json:"active,omitempty"`
}

func TestMarshalScalars(t *testing.T) {
	tests := []struct {
		in   any
		want string
	}{
		{nil, "null"},
		{"hi", `"hi"`},
		{"", `""`},
		{true, "true"},
		{false, "false"},
		{int(42), "42"},
		{int64(-7), "-7"},
		{uint64(100), "100"},
		{float64(3.14), "3.14"},
		{[]byte("hi"), `"hi"`},
	}
	for _, tt := range tests {
		t.Run(reflect.ValueOf(tt.in).Kind().String(), func(t *testing.T) {
			got, err := Marshal(tt.in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMarshalEscapes(t *testing.T) {
	in := "line1\nline2\ttab\"quote\\back"
	got, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `"line1\nline2\ttab\"quote\\back"`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarshalControlChars(t *testing.T) {
	in := "\x00\x01\x1f"
	got, err := Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := "\"\\u0000\\u0001\\u001f\""
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarshalStruct(t *testing.T) {
	p := encPerson{Name: "alice", Age: 30, Tags: []string{"a", "b"}}
	got, err := Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"name":"alice","age":30,"tags":["a","b"]}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarshalUnsafeFusedPrefixes(t *testing.T) {
	type record struct {
		ID      int     `json:"id"`
		Name    string  `json:"name"`
		Active  bool    `json:"active"`
		Score   float64 `json:"score"`
		Message string  `json:"message"`
	}
	rows := make([]record, 128)
	for i := range rows {
		rows[i] = record{
			ID:      123456 + i,
			Name:    "alice",
			Active:  i&1 == 0,
			Score:   12.5 + float64(i)/10,
			Message: "abcdefghijklmnopqrstuvwxyz0123456789",
		}
	}
	got, err := Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("unsafe fused-prefix output mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestMarshalOmitEmpty(t *testing.T) {
	p := encPerson{Name: "bob", Age: 25} // email/tags/active empty
	got, err := Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"name":"bob","age":25}`
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMarshalMap(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2}
	got, err := Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"a":1,"b":2}` {
		t.Errorf("got %q", got)
	}
}

func TestMarshalSlice(t *testing.T) {
	s := []int{1, 2, 3}
	got, err := Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != "[1,2,3]" {
		t.Errorf("got %q", got)
	}
}

func TestMarshalNested(t *testing.T) {
	type Inner struct {
		Value int `json:"v"`
	}
	type Outer struct {
		I Inner `json:"i"`
	}
	o := Outer{I: Inner{Value: 42}}
	got, err := Marshal(o)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"i":{"v":42}}` {
		t.Errorf("got %q", got)
	}
}

func TestMarshalUnsafeWidthsAndNestedValues(t *testing.T) {
	type narrow struct {
		I8  int8    `json:"i8"`
		I16 int16   `json:"i16"`
		I32 int32   `json:"i32"`
		U8  uint8   `json:"u8"`
		U16 uint16  `json:"u16"`
		F32 float32 `json:"f32"`
		P   uintptr `json:"p"`
		A   [2]int8 `json:"a"`
	}
	got, err := Marshal(narrow{I8: -8, I16: -16, I32: -32, U8: 8, U16: 16, F32: 1.25, P: 9, A: [2]int8{7, -7}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"i8":-8,"i16":-16,"i32":-32,"u8":8,"u16":16,"f32":1.25,"p":9,"a":[7,-7]}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}

	type nested struct {
		Value encCustom `json:"value"`
		List  []int8    `json:"list"`
	}
	got, err = Marshal(nested{Value: encCustom{val: 7}, List: []int8{1, -2, 3}})
	if err != nil {
		t.Fatal(err)
	}
	const nestedWant = `{"value":{"custom":7},"list":[1,-2,3]}`
	if string(got) != nestedWant {
		t.Fatalf("nested got %s, want %s", got, nestedWant)
	}
}

func TestMarshalFastNumberEdges(t *testing.T) {
	tests := []struct {
		name string
		got  func() ([]byte, error)
		want string
	}{
		{"int-min", func() ([]byte, error) { return Marshal(int64(math.MinInt64)) }, "-9223372036854775808"},
		{"int-max", func() ([]byte, error) { return Marshal(int64(math.MaxInt64)) }, "9223372036854775807"},
		{"uint-max", func() ([]byte, error) { return Marshal(^uint64(0)) }, "18446744073709551615"},
		{"float-small", func() ([]byte, error) { return Marshal(1e-7) }, "1e-7"},
		{"float-fixed", func() ([]byte, error) { return Marshal(1e-6) }, "0.000001"},
		{"float-large-fixed", func() ([]byte, error) { return Marshal(1e20) }, "100000000000000000000"},
		{"float-large", func() ([]byte, error) { return Marshal(1e21) }, "1e+21"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.got()
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

type pointerMarshalValue struct{ Value int }

func (v *pointerMarshalValue) MarshalJSON() ([]byte, error) {
	return []byte(`{"pointer":` + itoa(v.Value) + `}`), nil
}

func TestMarshalPointerReceiverNested(t *testing.T) {
	type holder struct {
		Value pointerMarshalValue `json:"value"`
	}
	got, err := Marshal(holder{Value: pointerMarshalValue{Value: 11}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"value":{"pointer":11}}` {
		t.Fatalf("got %s", got)
	}
}

func TestMarshalOmitEmptyMap(t *testing.T) {
	type holder struct {
		M map[string]int `json:"m,omitempty"`
	}
	got, err := Marshal(holder{M: map[string]int{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{}` {
		t.Fatalf("got %s, want {}", got)
	}
}

func TestMarshalPointersAndInterfaces(t *testing.T) {
	v := encPerson{Name: "pointer", Age: 3}
	got, err := Marshal(&v)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"name":"pointer","age":3}` {
		t.Fatalf("pointer got %s", got)
	}

	type holder struct {
		Any any `json:"any"`
	}
	got, err = Marshal(holder{Any: int8(-12)})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"any":-12}` {
		t.Fatalf("interface got %s", got)
	}
}

func TestMarshalNaN(t *testing.T) {
	// NaN becomes null per JSON convention.
	got, err := Marshal(math.NaN())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != "null" {
		t.Errorf("got %q, want null", got)
	}
}

type encCustom struct {
	val int
}

func (e encCustom) MarshalJSON() ([]byte, error) {
	return []byte(`{"custom":` + itoa(e.val) + `}`), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestMarshalJSONMarshaler(t *testing.T) {
	got, err := Marshal(encCustom{val: 42})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != `{"custom":42}` {
		t.Errorf("got %q", got)
	}
}

func TestMarshalDashTag(t *testing.T) {
	type s struct {
		FName string `json:"FName"`
		X     int    `json:"-"`
	}
	v := s{FName: "hi", X: 99}
	got, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(got), `"FName":"hi"`) {
		t.Errorf("got %q", got)
	}
	if strings.Contains(string(got), `"X":99`) || strings.Contains(string(got), `99`) {
		t.Errorf("X should not appear: %q", got)
	}
}

func TestEncoder(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(map[string]int{"b": 2}); err != nil {
		t.Fatal(err)
	}
	want := `{"a":1}` + "\n" + `{"b":2}` + "\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	p := encPerson{Name: "alice", Age: 30, Tags: []string{"a"}}
	b, err := Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var back encPerson
	if err := Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, back) {
		t.Errorf("round-trip mismatch: %+v vs %+v", p, back)
	}
}
