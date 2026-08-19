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

func TestDecoderReset(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse([]byte(`{"a":{"b":1}}`)); err != nil {
		t.Fatal(err)
	}
	old := dec.Get("a", "b")
	if !old.Exists() {
		t.Fatal("expected value before reset")
	}
	oldRoot := dec.Root()
	arena := dec.arena
	meta := dec.doc.slab.metaBase
	starts := dec.doc.slab.startBase
	ends := dec.doc.slab.endBase
	skips := dec.doc.slab.skipBase
	metaCap := cap(dec.doc.slab.meta)
	startsCap := cap(dec.doc.slab.starts)
	endsCap := cap(dec.doc.slab.ends)
	skipsCap := cap(dec.doc.slab.skips)

	dec.Reset()
	if old.Exists() {
		t.Fatal("value remained valid after reset")
	}
	if old.Type() != TypeNull || old.Bytes() != nil || old.Str() != "" {
		t.Fatal("stale value remained readable after reset")
	}
	if oldRoot.Exists() {
		t.Fatal("old root remained valid after reset")
	}
	if dec.Root().Exists() {
		t.Fatal("root remained valid after reset")
	}
	if dec.src != nil || dec.doc.src != nil {
		t.Fatal("reset retained source bytes")
	}
	if dec.Err() != nil {
		t.Fatalf("reset retained parse error: %v", dec.Err())
	}
	if dec.root != noNode || dec.doc.root != noNode || dec.doc.slab.n != 0 {
		t.Fatal("reset did not clear decoder state")
	}
	if dec.arena != arena {
		t.Fatal("reset replaced the decoder arena")
	}
	if dec.doc.slab.metaBase != meta || dec.doc.slab.startBase != starts ||
		dec.doc.slab.endBase != ends || dec.doc.slab.skipBase != skips {
		t.Fatal("reset replaced tape storage")
	}
	if cap(dec.doc.slab.meta) != metaCap || cap(dec.doc.slab.starts) != startsCap ||
		cap(dec.doc.slab.ends) != endsCap || cap(dec.doc.slab.skips) != skipsCap {
		t.Fatal("reset did not retain tape capacity")
	}

	if err := dec.Parse([]byte(`{"b":2}`)); err != nil {
		t.Fatal(err)
	}
	if got := dec.Get("b").Int(); got != 2 {
		t.Fatalf("b = %d, want 2", got)
	}
}

func TestDecoderResetReleasesMutationSource(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse([]byte(`{"name":"alice","ok":true}`)); err != nil {
		t.Fatal(err)
	}
	name := dec.Get("name")
	if err := name.SetString("a-longer-name"); err != nil {
		t.Fatal(err)
	}
	if dec.doc.ownedSource == nil {
		t.Fatal("variable-size mutation did not acquire an owned source arena")
	}
	owned := dec.doc.ownedSource

	dec.Reset()
	if dec.doc.ownedSource != nil {
		t.Fatal("reset retained mutation-owned source arena")
	}
	if dec.src != nil || dec.doc.src != nil {
		t.Fatal("reset retained mutation-owned source bytes")
	}
	if dec.arena == nil || owned == dec.arena {
		t.Fatal("mutation source unexpectedly shared the decoder tape arena")
	}

	if err := dec.Parse([]byte(`{"fresh":true}`)); err != nil {
		t.Fatalf("parse after mutation-source reset: %v", err)
	}
	if !dec.Get("fresh").Bool() {
		t.Fatal("parse after mutation-source reset returned the wrong value")
	}
}

func TestDecoderResetClearsError(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse([]byte(`{"broken":}`)); err == nil {
		t.Fatal("expected parse error")
	}
	if dec.Err() == nil {
		t.Fatal("decoder did not retain parse error before reset")
	}
	dec.Reset()
	if dec.Err() != nil {
		t.Fatalf("reset retained parse error: %v", dec.Err())
	}
	if err := dec.Parse([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("parse after error reset: %v", err)
	}
}

func TestDecoderResetZeroAlloc(t *testing.T) {
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()

	if err := dec.Parse([]byte(`{"name":"alice"}`)); err != nil {
		t.Fatal(err)
	}
	name := dec.Get("name")
	if err := name.SetString("a-longer-name"); err != nil {
		t.Fatal(err)
	}
	dec.Reset()

	allocs := testing.AllocsPerRun(1000, func() {
		dec.Reset()
	})
	if allocs != 0 {
		t.Fatalf("Decoder.Reset allocated %v times per call", allocs)
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
