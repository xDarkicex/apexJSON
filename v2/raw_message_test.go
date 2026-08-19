package apexjson

import (
	"bytes"
	"testing"
)

func TestRawMessageNativeSemantics(t *testing.T) {
	raw := RawMessage(`{"id":7,"ok":true}`)
	owned, err := Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(owned, raw) {
		t.Fatalf("top-level marshal = %s, want %s", owned, raw)
	}
	dst := make([]byte, 0, 64)
	got, err := AppendMarshalPtr(dst, &raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("marshal = %s, want %s", got, raw)
	}

	src := []byte(`  [1,{"x":true}]  `)
	var decoded RawMessage
	if err := Unmarshal(src, &decoded); err != nil {
		t.Fatal(err)
	}
	wantDecoded := RawMessage(`[1,{"x":true}]`)
	if !bytes.Equal(decoded, wantDecoded) {
		t.Fatalf("unmarshal = %s, want %s", decoded, wantDecoded)
	}
	src[2] = '{'
	if decoded[0] != '{' {
		t.Fatal("RawMessage did not retain the borrowed input span")
	}

	clone := decoded.Clone()
	src[0] = '['
	if clone[0] != '{' {
		t.Fatal("Clone did not create owned storage")
	}
}

func TestRawMessageNested(t *testing.T) {
	type envelope struct {
		Payload RawMessage `json:"payload"`
	}
	var got envelope
	src := []byte(`{"payload":{"items":[1,2,3]}}`)
	if err := Unmarshal(src, &got); err != nil {
		t.Fatal(err)
	}
	want := RawMessage(`{"items":[1,2,3]}`)
	if !bytes.Equal(got.Payload, want) {
		t.Fatalf("payload = %s, want %s", got.Payload, want)
	}
	encoded, err := Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, src) {
		t.Fatalf("round trip = %s, want %s", encoded, src)
	}
}

func TestRawMessageRejectsInvalidRoot(t *testing.T) {
	var raw RawMessage
	if err := Unmarshal([]byte(`{"broken":}`), &raw); err == nil {
		t.Fatal("invalid raw JSON was accepted")
	}
}

func TestRawMessageAppendZeroAlloc(t *testing.T) {
	raw := RawMessage(`{"id":123,"name":"alice"}`)
	dst := make([]byte, 0, 64)
	allocs := testing.AllocsPerRun(1000, func() {
		out, err := AppendMarshalPtr(dst[:0], &raw)
		if err != nil || !bytes.Equal(out, raw) {
			t.Fatalf("append raw = %s, err=%v", out, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("AppendMarshalPtr RawMessage allocated %v times", allocs)
	}
}

func TestRawMessageUnmarshalZeroAlloc(t *testing.T) {
	src := []byte(`{"id":123,"name":"alice"}`)
	var raw RawMessage
	if err := Unmarshal(src, &raw); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(1000, func() {
		if err := Unmarshal(src, &raw); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("Unmarshal RawMessage allocated %v times", allocs)
	}
}
