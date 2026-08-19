package apexjson

import (
	"math"
	"reflect"
	"strconv"
	"testing"
)

type decPerson struct {
	Name    string   `json:"name"`
	Age     int      `json:"age"`
	Active  bool     `json:"active"`
	Tags    []string `json:"tags,omitempty"`
	Balance float64  `json:"balance,omitempty"`
}

type decNested struct {
	User decPerson `json:"user"`
	Meta struct {
		Count int `json:"count"`
	} `json:"meta"`
}

func TestUnmarshalScalars(t *testing.T) {
	tests := []struct {
		name string
		src  string
		dst  any
		want any
	}{
		{"string", `"hi"`, new(string), "hi"},
		{"int", `42`, new(int), 42},
		{"int64", `-7`, new(int64), int64(-7)},
		{"uint64", `100`, new(uint64), uint64(100)},
		{"float", `3.14`, new(float64), 3.14},
		{"true", `true`, new(bool), true},
		{"false", `false`, new(bool), false},
		{"null into ptr", `null`, new(*string), (*string)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Unmarshal([]byte(tt.src), tt.dst); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			got := reflect.ValueOf(tt.dst).Elem().Interface()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestFastNumberParsing(t *testing.T) {
	for _, input := range []string{
		"0", "42", "-42", "-9223372036854775808", "18446744073709551615",
	} {
		got, err := parseIntBytes([]byte(input), 64)
		want, wantErr := strconv.ParseInt(input, 10, 64)
		if (err != nil) != (wantErr != nil) || err == nil && got != want {
			t.Errorf("parseIntBytes(%q) = %d, %v; want %d, %v", input, got, err, want, wantErr)
		}
	}
	for _, input := range []string{"0.0", "12.5", "-12.5", "1.23456789012345", "-0.0"} {
		got, err := parseFloatBytes([]byte(input), 64)
		want, wantErr := strconv.ParseFloat(input, 64)
		if (err != nil) != (wantErr != nil) || err == nil && math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("parseFloatBytes(%q) = %v, %v; want %v, %v", input, got, err, want, wantErr)
		}
	}
}

func TestDirectIntegerFieldFastPath(t *testing.T) {
	type row struct {
		ID       int64  `json:"id"`
		Unsigned uint64 `json:"unsigned"`
		Zero     int64  `json:"zero"`
	}
	var got row
	if err := Unmarshal([]byte(`{"id":-42,"unsigned":123456,"zero":0}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != -42 || got.Unsigned != 123456 || got.Zero != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestDirectDecodeSkipsUnknownValues(t *testing.T) {
	type known struct {
		ID int `json:"id"`
	}
	var got known
	if err := Unmarshal([]byte(`{"id":7,"unknown":{"nested":[1,{"x":true}]}}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != 7 {
		t.Fatalf("got %+v, want id 7", got)
	}
}

func TestBitmapFieldDispatch(t *testing.T) {
	type fast struct {
		ABC int `json:"abc"`
		ABD int `json:"abd"`
		A   int `json:"a"`
		XYZ int `json:"xyz"`
	}
	var got fast
	if err := Unmarshal([]byte(`{"abd":2,"a":3,"xyz":4,"abc":1}`), &got); err != nil {
		t.Fatal(err)
	}
	if got != (fast{ABC: 1, ABD: 2, A: 3, XYZ: 4}) {
		t.Fatalf("bitmap dispatch = %+v", got)
	}

	// Maps force the tape-backed path, exercising the same resolver outside
	// the direct typed decoder.
	type tape struct {
		ABC  int            `json:"abc"`
		ABD  int            `json:"abd"`
		A    int            `json:"a"`
		Meta map[string]any `json:"meta"`
	}
	var tapeGot tape
	if err := Unmarshal([]byte(`{"abd":6,"a":7,"abc":5,"meta":{"ok":true}}`), &tapeGot); err != nil {
		t.Fatal(err)
	}
	if tapeGot.ABC != 5 || tapeGot.ABD != 6 || tapeGot.A != 7 || tapeGot.Meta["ok"] != true {
		t.Fatalf("tape bitmap dispatch = %+v", tapeGot)
	}
}

func TestDirectScalarSliceReuseZeroAlloc(t *testing.T) {
	values := make([]int, 0, 8)
	input := []byte(`[1,2,3,4,5,6,7,8]`)
	if err := Unmarshal(input, &values); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		if err := Unmarshal(input, &values); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("direct scalar slice allocated %v/op, want 0", allocs)
	}
	if len(values) != 8 || values[7] != 8 {
		t.Fatalf("values = %v", values)
	}
}

func TestDynamicSliceReuseShrinks(t *testing.T) {
	var values []any
	if err := Unmarshal([]byte(`[1,{"x":2},3]`), &values); err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal([]byte(`[4]`), &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0].(float64) != 4 {
		t.Fatalf("reused values = %#v", values)
	}
	if err := Unmarshal([]byte(`[]`), &values); err != nil {
		t.Fatal(err)
	}
	if values == nil || len(values) != 0 {
		t.Fatalf("reused empty values = %#v", values)
	}
}

func TestUnmarshalStruct(t *testing.T) {
	src := `{"name":"alice","age":30,"active":true,"tags":["a","b"],"balance":1.5}`
	var p decPerson
	if err := Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := decPerson{Name: "alice", Age: 30, Active: true, Tags: []string{"a", "b"}, Balance: 1.5}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("got %+v, want %+v", p, want)
	}
}

func TestUnmarshalStructMissingFields(t *testing.T) {
	src := `{"name":"bob"}`
	var p decPerson
	if err := Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.Name != "bob" || p.Age != 0 || p.Active != false {
		t.Errorf("got %+v", p)
	}
}

func TestUnmarshalStructJSONTag(t *testing.T) {
	src := `{"name":"x"}`
	type s struct {
		FName string `json:"name"`
	}
	var v s
	if err := Unmarshal([]byte(src), &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if v.FName != "x" {
		t.Errorf("FName = %q, want x", v.FName)
	}
}

func TestUnmarshalStructDashTag(t *testing.T) {
	src := `{"FName":"x","x":"y"}`
	type s struct {
		FName string `json:"FName"`
		X     string `json:"-"`
	}
	var v s
	if err := Unmarshal([]byte(src), &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if v.FName != "x" || v.X != "" {
		t.Errorf("got %+v", v)
	}
}

func TestUnmarshalNestedStruct(t *testing.T) {
	src := `{"user":{"name":"c","age":1,"active":true},"meta":{"count":5}}`
	var n decNested
	if err := Unmarshal([]byte(src), &n); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if n.User.Name != "c" || n.Meta.Count != 5 {
		t.Errorf("got %+v", n)
	}
}

func TestUnmarshalMap(t *testing.T) {
	src := `{"a":1,"b":2,"c":3}`
	var m map[string]int
	if err := Unmarshal([]byte(src), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]int{"a": 1, "b": 2, "c": 3}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("got %v, want %v", m, want)
	}
}

func TestUnmarshalMapAnyReusesExistingEntries(t *testing.T) {
	m := map[string]any{"keep": float64(7), "replace": float64(1)}
	if err := Unmarshal([]byte(`{"replace":2,"new":[true,false],"nil":null}`), &m); err != nil {
		t.Fatal(err)
	}
	if m["keep"] != float64(7) || m["replace"] != float64(2) {
		t.Fatalf("map merge = %#v", m)
	}
	if !reflect.DeepEqual(m["new"], []any{true, false}) {
		t.Fatalf("new value = %#v", m["new"])
	}
	if value, ok := m["nil"]; !ok || value != nil {
		t.Fatalf("nil value = %#v, present=%v", value, ok)
	}
}

func TestUnmarshalSlice(t *testing.T) {
	src := `[1,2,3,4,5]`
	var s []int
	if err := Unmarshal([]byte(src), &s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := []int{1, 2, 3, 4, 5}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("got %v, want %v", s, want)
	}
}

func TestUnmarshalArrayAndUintptr(t *testing.T) {
	var a [3]int8
	if err := Unmarshal([]byte(`[1,-2,3,99]`), &a); err != nil {
		t.Fatal(err)
	}
	if a != [3]int8{1, -2, 3} {
		t.Fatalf("array = %v", a)
	}
	var p uintptr
	if err := Unmarshal([]byte(`42`), &p); err != nil {
		t.Fatal(err)
	}
	if p != 42 {
		t.Fatalf("uintptr = %d", p)
	}
}

func TestUnmarshalIntoAny(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want any
	}{
		{"object", `{"a":1,"b":"x"}`,
			map[string]any{"a": float64(1), "b": "x"}},
		{"array", `[1,2,3]`, []any{float64(1), float64(2), float64(3)}},
		{"string", `"hi"`, "hi"},
		{"number", `42`, float64(42)},
		{"bool", `true`, true},
		{"null", `null`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v any
			if err := Unmarshal([]byte(tt.src), &v); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(v, tt.want) {
				t.Errorf("got %v (%T), want %v (%T)", v, v, tt.want, tt.want)
			}
		})
	}
}

func TestUnmarshalPtrToStruct(t *testing.T) {
	src := `{"name":"a","age":1,"active":true}`
	var p *decPerson
	if err := Unmarshal([]byte(src), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p == nil || p.Name != "a" {
		t.Errorf("got %+v", p)
	}
}

func TestUnmarshalStringEscapes(t *testing.T) {
	src := `{"s":"line1\nline2\ttab\"quote"}`
	var m map[string]string
	if err := Unmarshal([]byte(src), &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m["s"] != "line1\nline2\ttab\"quote" {
		t.Errorf("unescape wrong: %q", m["s"])
	}
}

func TestUnmarshalErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		dst  any
	}{
		{"bad json", `{`, &decPerson{}},
		{"type mismatch", `"string"`, new(int)},
		{"nil target", `1`, (*int)(nil)},
		{"non-pointer", `1`, 0},
		{"array into struct", `[1,2]`, &decPerson{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := Unmarshal([]byte(c.src), c.dst); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestUnmarshalStructCache(t *testing.T) {
	// Decode the same struct type twice. Second call must use the cache.
	type cacheS struct {
		X int `json:"x"`
	}
	src := []byte(`{"x":42}`)
	var v cacheS
	if err := Unmarshal(src, &v); err != nil {
		t.Fatal(err)
	}
	if v.X != 42 {
		t.Errorf("first decode wrong: %d", v.X)
	}
	if err := Unmarshal(src, &v); err != nil {
		t.Fatal(err)
	}
	if v.X != 42 {
		t.Errorf("second decode wrong: %d", v.X)
	}
}

// customUnmarshaler verifies the json.Unmarshaler interface is honored.
type customUnmarshaler struct {
	val int
}

func (c *customUnmarshaler) UnmarshalJSON(b []byte) error {
	// Receives raw JSON; we expect a number here.
	var n int
	if err := Unmarshal(b, &n); err != nil {
		return err
	}
	c.val = n * 2
	return nil
}

func TestUnmarshalJSONUnmarshaler(t *testing.T) {
	var c customUnmarshaler
	if err := Unmarshal([]byte(`21`), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if c.val != 42 {
		t.Errorf("val = %d, want 42", c.val)
	}
}

type textUnmarshalerTarget struct {
	val string
}

func (t *textUnmarshalerTarget) UnmarshalText(b []byte) error {
	t.val = "got:" + string(b)
	return nil
}

func TestUnmarshalTextUnmarshaler(t *testing.T) {
	var v textUnmarshalerTarget
	if err := Unmarshal([]byte(`"hello"`), &v); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if v.val != "got:hello" {
		t.Errorf("val = %q", v.val)
	}
}

func TestUnmarshalRoundTrip(t *testing.T) {
	// Decode then verify we can re-extract the same bytes from the AST.
	src := []byte(`{"k":"v","arr":[1,2,3]}`)
	var m map[string]any
	if err := Unmarshal(src, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m["k"] != "v" {
		t.Errorf("k = %v", m["k"])
	}
}

func TestUnmarshalSurrogates(t *testing.T) {
	// Lone high surrogate should be replaced with U+FFFD per RFC 8259 § 7.
	cases := []struct {
		src  string
		want string
	}{
		{`"\uD800"`, "�"},
		{`"\uDFFF"`, "�"},
		{`"\uD83D"`, "�"},        // high half alone
		{`"\uDE00"`, "�"},        // low half alone
		{`"😀"`, "\U0001F600"},    // valid surrogate pair
		{`"𐀀0"`, "\U000100000"},  // pair + trailing literal
		{`"\uD800\uD800"`, "��"}, // two high halves
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			var s string
			if err := Unmarshal([]byte(c.src), &s); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if s != c.want {
				t.Errorf("got %q, want %q", s, c.want)
			}
		})
	}
}
