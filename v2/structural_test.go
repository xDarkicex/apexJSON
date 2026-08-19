package apexjson

import "testing"

func TestDecoderFusedParserZeroAlloc(t *testing.T) {
	dec, err := NewDecoderSize(256*1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()
	src := []byte("{\n  \"a\": [1, 2, 3],\n  \"b\": {\"x\": true}\n}")
	if err := dec.Parse(src); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		if err := dec.Parse(src); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("fused parser allocated %v/op, want 0", allocs)
	}
}

func TestDecoderFusedParserEscapes(t *testing.T) {
	dec, err := NewDecoderSize(64*1024, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()
	src := []byte("{\n  \"s\": \"x\\\"y:z\",\n  \"arr\": [1, 2]\n}")
	if err := dec.Parse(src); err != nil {
		t.Fatal(err)
	}
	if got := dec.GetPath([]string{"s"}).Str(); got != `x"y:z` {
		t.Fatalf("escaped value = %q", got)
	}
}
