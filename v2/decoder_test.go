package apexjson

import (
	"testing"
)

func TestDecoderParse(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	data := []byte(`{"name":"alice","age":30,"tags":["a","b"]}`)
	if err := dec.Parse(data); err != nil {
		t.Fatalf("Parse: %v", err)
	}

	v := dec.Get("name")
	if v.Str() != "alice" {
		t.Errorf("name = %q, want alice", v.Str())
	}

	v = dec.Get("age")
	if v.Int() != 30 {
		t.Errorf("age = %d, want 30", v.Int())
	}

	v = dec.Get("tags", "1")
	if v.Str() != "b" {
		t.Errorf("tags[1] = %q, want b", v.Str())
	}
}

func TestDecoderReuse(t *testing.T) {
	// Parse multiple docs; verify the arena is reused and results stay
	// correct.
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	docs := [][]byte{
		[]byte(`{"a":1}`),
		[]byte(`{"b":[true,null,"x"]}`),
		[]byte(`[1,2,3,{"deep":{"nested":[true]}}]`),
	}

	for i, doc := range docs {
		if err := dec.Parse(doc); err != nil {
			t.Fatalf("Parse %d: %v", i, err)
		}
		root := dec.Root()
		if !root.Exists() {
			t.Errorf("doc %d: root not found", i)
		}
	}

	// Final doc check: deep nested path.
	v := dec.Get("3", "deep", "nested", "0")
	if v.Type() != TypeBool {
		t.Errorf("final deep value type = %v, want bool", v.Type())
	}
	if !v.Bool() {
		t.Errorf("final deep value bool = false, want true")
	}
}

func TestDecoderErrors(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse([]byte(`{"a":1,}`)); err == nil {
		t.Errorf("expected error on trailing comma, got nil")
	}
	// Parse a valid doc to verify error state clears.
	if err := dec.Parse([]byte(`{"a":1}`)); err != nil {
		t.Errorf("recovery parse failed: %v", err)
	}
	if v := dec.Get("a"); v.Int() != 1 {
		t.Errorf("recovery get failed: %v", v.Int())
	}
}

func TestDecoderCustomSize(t *testing.T) {
	// 4KB arena, 100 node cap — for small docs.
	dec, err := NewDecoderSize(4*1024, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse([]byte(`{"k":"v"}`)); err != nil {
		t.Fatal(err)
	}
	if v := dec.Get("k"); v.Str() != "v" {
		t.Errorf("k = %q", v.Str())
	}
}