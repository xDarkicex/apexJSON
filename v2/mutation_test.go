package apexjson

import (
	"testing"
	"unsafe"
)

func TestValueMutationCopyOnWrite(t *testing.T) {
	src := []byte(`{"name":"alice","age":30,"active":true}`)
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()
	if err := dec.Parse(src); err != nil {
		t.Fatal(err)
	}

	oldAge := dec.Get("age")
	name := dec.Get("name")
	if err := name.SetString("bob"); err != nil {
		t.Fatal(err)
	}
	if string(src) != `{"name":"alice","age":30,"active":true}` {
		t.Fatalf("copy-on-write modified caller input: %s", src)
	}
	if got := dec.Get("name").Str(); got != "bob" {
		t.Fatalf("name = %q, want bob", got)
	}
	if oldAge.Exists() {
		t.Fatal("value handle from before mutation remained valid")
	}

	age := dec.Get("age")
	if err := age.SetInt(42); err != nil {
		t.Fatal(err)
	}
	if got := dec.Get("age").Int(); got != 42 {
		t.Fatalf("age = %d, want 42", got)
	}
}

func TestValueMutateUnsafeUsesOffHeapCandidate(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()
	if err := dec.Parse([]byte(`{"name":"alice","ok":true}`)); err != nil {
		t.Fatal(err)
	}

	name := dec.Get("name")
	if err := name.MutateUnsafe(func(ptr unsafe.Pointer, n int) {
		b := unsafe.Slice((*byte)(ptr), n)
		// The callback receives the complete JSON span, including quotes.
		b[1] = 'e'
	}); err != nil {
		t.Fatal(err)
	}
	if got := dec.Get("name").Str(); got != "elice" {
		t.Fatalf("name = %q, want elice", got)
	}
}

func TestValueMutationIsAtomicOnInvalidBytes(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()
	if err := dec.Parse([]byte(`{"age":30}`)); err != nil {
		t.Fatal(err)
	}

	age := dec.Get("age")
	err = age.MutateBytes(func(b []byte) { b[0] = 'x' })
	if err == nil {
		t.Fatal("invalid mutation unexpectedly succeeded")
	}
	if got := dec.Get("age").Int(); got != 30 {
		t.Fatalf("age after rejected mutation = %d, want 30", got)
	}
}

func TestOneShotValueMutation(t *testing.T) {
	v := Get([]byte(`{"value":1}`), "value")
	if err := v.SetRaw([]byte(`{"nested":true}`)); err != nil {
		t.Fatal(err)
	}
	if v.Type() != TypeObject {
		t.Fatalf("mutated type = %v, want object", v.Type())
	}
}

func BenchmarkValueSetIntFixed(b *testing.B) {
	dec, err := NewDecoder()
	if err != nil {
		b.Fatal(err)
	}
	defer dec.Free()
	if err := dec.Parse([]byte(`{"age":30}`)); err != nil {
		b.Fatal(err)
	}
	v := dec.Get("age")
	if err := v.SetInt(31); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.SetInt(31); err != nil {
			b.Fatal(err)
		}
	}
}
