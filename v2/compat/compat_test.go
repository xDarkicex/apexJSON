package compat

import (
	"bytes"
	stdjson "encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

type compatRecord struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

func TestMarshalUnmarshal(t *testing.T) {
	data, err := Marshal(struct {
		Text string `json:"text"`
	}{Text: "<value>"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"text":"\u003cvalue\u003e"}`; got != want {
		t.Fatalf("Marshal = %q, want %q", got, want)
	}

	var got compatRecord
	if err := Unmarshal([]byte(`{"name":"Ada","n":42}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada" || got.N != 42 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestEncoderOptions(t *testing.T) {
	var out bytes.Buffer
	enc := NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]string{"text": "<value>"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "{\n  \"text\": \"<value>\"\n}\n"; got != want {
		t.Fatalf("Encode = %q, want %q", got, want)
	}
}

func TestStreamingDecoderSurface(t *testing.T) {
	dec := NewDecoder(strings.NewReader(`{"a":1} {"b":2}`))
	var first, second map[string]int
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first["a"] != 1 || second["b"] != 2 {
		t.Fatalf("decoded %v and %v", first, second)
	}
}

func TestTokenAndNumberOptions(t *testing.T) {
	dec := NewDecoder(strings.NewReader(`[1,2]`))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != Delim('[') {
		t.Fatalf("opening token = %v, %v", tok, err)
	}
	if !dec.More() {
		t.Fatal("More reported false before first element")
	}
	tok, err = dec.Token()
	if err != nil {
		t.Fatal(err)
	}
	if number, ok := tok.(Number); !ok || number.String() != "1" {
		t.Fatalf("number token = %#v", tok)
	}
}

type oneByteReader struct {
	data []byte
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestDecoderReadsIncrementally(t *testing.T) {
	src := []byte(`{"a":1} {"b":2}`)
	dec := NewDecoder(&oneByteReader{data: append([]byte(nil), src...)})
	var first, second map[string]int
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if got, want := dec.InputOffset(), int64(len(`{"a":1}`)); got != want {
		t.Fatalf("InputOffset after first value = %d, want %d", got, want)
	}
	if err := dec.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first["a"] != 1 || second["b"] != 2 {
		t.Fatalf("decoded %v and %v", first, second)
	}
	if err := dec.Decode(&second); err != io.EOF {
		t.Fatalf("final Decode = %v, want io.EOF", err)
	}
}

func TestTokenStringsSurviveCompaction(t *testing.T) {
	dec := NewDecoder(&oneByteReader{data: []byte(`{"k\u0065y":"value"} {"next":"x"}`)})
	want := []any{Delim('{'), "key", "value", Delim('}')}
	for i, expected := range want {
		got, err := dec.Token()
		if err != nil {
			t.Fatalf("Token %d: %v", i, err)
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("Token %d = %#v, want %#v", i, got, expected)
		}
	}
	got, err := dec.Token()
	if err != nil || got != Delim('{') {
		t.Fatalf("next document opening token = %#v, %v", got, err)
	}
	got, err = dec.Token()
	if err != nil || got != "next" {
		t.Fatalf("next document key token = %#v, %v", got, err)
	}
}

func TestFastPathErrorsMatchStdlib(t *testing.T) {
	type typed struct {
		N int `json:"n"`
	}
	src := []byte(`{"n":"not-an-int"}`)
	var want, got typed
	wantErr := stdjson.Unmarshal(src, &want)
	gotErr := Unmarshal(src, &got)
	if (wantErr == nil) != (gotErr == nil) {
		t.Fatalf("error presence mismatch: stdlib=%v apex=%v", wantErr, gotErr)
	}
	if reflect.TypeOf(wantErr) != reflect.TypeOf(gotErr) || wantErr.Error() != gotErr.Error() {
		t.Fatalf("error mismatch: stdlib (%T) %q, apex (%T) %q", wantErr, wantErr, gotErr, gotErr)
	}
}
