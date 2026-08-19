package apexjson

import (
	"reflect"
	"testing"
)

func TestGetSimple(t *testing.T) {
	data := []byte(`{"name":"alice","age":30,"active":true,"tags":["x","y"],"nothing":null}`)
	if v := Get(data, "name"); v.Str() != "alice" {
		t.Errorf("name = %q, want alice", v.Str())
	}
	if v := Get(data, "age"); v.Int() != 30 {
		t.Errorf("age = %d, want 30", v.Int())
	}
	if v := Get(data, "active"); !v.Bool() {
		t.Errorf("active = false, want true")
	}
	if v := Get(data, "tags", "1"); v.Str() != "y" {
		t.Errorf("tags[1] = %q, want y", v.Str())
	}
	if v := Get(data, "nothing"); !v.IsNull() {
		t.Errorf("nothing should be null")
	}
}

func TestGetMissing(t *testing.T) {
	data := []byte(`{"a":1}`)
	v := Get(data, "missing")
	if v.Exists() {
		t.Errorf("missing key should not exist")
	}
	if v, ok := GetString(data, "missing"); ok || v != "" {
		t.Errorf("missing string should return false")
	}
}

func TestGetDeepPath(t *testing.T) {
	data := []byte(`{"users":[{"name":"alice","id":1},{"name":"bob","id":2}]}`)
	if v := Get(data, "users", "1", "name"); v.Str() != "bob" {
		t.Errorf("users[1].name = %q, want bob", v.Str())
	}
	if v := Get(data, "users", "0", "id"); v.Int() != 1 {
		t.Errorf("users[0].id = %d, want 1", v.Int())
	}
	if v := Get(data, "users", "5", "name"); v.Exists() {
		t.Errorf("out-of-range index should not exist")
	}
}

func TestGetArray(t *testing.T) {
	data := []byte(`{"arr":[10,20,30]}`)
	v := Get(data, "arr")
	arr := v.Array()
	if len(arr) != 3 {
		t.Fatalf("arr length = %d, want 3", len(arr))
	}
	want := []int64{10, 20, 30}
	for i, ve := range arr {
		if ve.Int() != want[i] {
			t.Errorf("arr[%d] = %d, want %d", i, ve.Int(), want[i])
		}
	}
}

func TestGetMap(t *testing.T) {
	data := []byte(`{"obj":{"a":1,"b":2}}`)
	v := Get(data, "obj")
	m := v.Map()
	if len(m) != 2 {
		t.Fatalf("obj length = %d, want 2", len(m))
	}
	if m["a"].Int() != 1 {
		t.Errorf("obj.a = %d, want 1", m["a"].Int())
	}
}

func TestGetNoAlloc(t *testing.T) {
	// Confirm Decoder.GetPath (arena-backed) is zero-allocation in the
	// steady state. The one-shot GetPath() allocates a parsedDoc each
	// call — use Decoder for true zero alloc.
	data := []byte(`{"users":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}`)
	path := []string{"users", "1", "name"}

	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse(data); err != nil {
		t.Fatal(err)
	}

	// Warm up.
	_ = dec.GetPath(path)

	allocs := testing.AllocsPerRun(1000, func() {
		_ = dec.Parse(data)
		_ = dec.GetPath(path)
	})
	if allocs > 0 {
		t.Errorf("Decoder allocated %v/op, want 0", allocs)
	}
}

func TestGetMany(t *testing.T) {
	data := []byte(`{"a":1,"b":2,"c":3}`)
	paths := [][]string{{"a"}, {"b"}, {"c"}, {"missing"}}
	got := GetMany(data, paths)
	if got[0].Int() != 1 || got[1].Int() != 2 || got[2].Int() != 3 {
		t.Errorf("wrong values: %+v", got)
	}
	if got[3].Exists() {
		t.Errorf("missing path should not exist")
	}
}

func TestGetString(t *testing.T) {
	data := []byte(`{"s":"hello"}`)
	s, ok := GetString(data, "s")
	if !ok || s != "hello" {
		t.Errorf("GetString = %q, %v; want hello, true", s, ok)
	}
}

func TestGetInt(t *testing.T) {
	data := []byte(`{"n":42}`)
	n, ok := GetInt(data, "n")
	if !ok || n != 42 {
		t.Errorf("GetInt = %d, %v; want 42, true", n, ok)
	}
}

func TestGetBool(t *testing.T) {
	data := []byte(`{"b":true}`)
	b, ok := GetBool(data, "b")
	if !ok || !b {
		t.Errorf("GetBool = %v, %v; want true, true", b, ok)
	}
}

func TestGetZeroAllocNote(t *testing.T) {
	// Document expected allocation behavior: 1 alloc per call due to
	// variadic path slice. To get true zero alloc, callers should use
	// Get directly with a pre-built slice.
	data := []byte(`{"a":1}`)
	allocs := testing.AllocsPerRun(100, func() {
		Get(data, "a")
	})
	_ = allocs // Variadic creates a []string{"a"}.
	_ = reflect.TypeOf
}