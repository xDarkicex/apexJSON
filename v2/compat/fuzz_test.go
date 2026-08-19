package compat

import (
	"bytes"
	stdjson "encoding/json"
	"io"
	"reflect"
	"testing"
)

type fuzzRecord struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Values []int  `json:"values"`
}

func FuzzUnmarshalConformance(f *testing.F) {
	for _, seed := range []string{
		`{"id":1,"name":"Ada","active":true,"values":[1,2]}`,
		`{"ID":2,"NAME":"Grace"}`,
		`null`,
		`[]`,
		`{"id":-9223372036854775808}`,
		`{"name":"line1\nline2"}`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		var want, got fuzzRecord
		wantErr := stdjson.Unmarshal(src, &want)
		gotErr := Unmarshal(src, &got)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("acceptance mismatch for %q: stdlib=%v apex=%v", src, wantErr, gotErr)
		}
		if wantErr == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded mismatch for %q: stdlib=%+v apex=%+v", src, want, got)
		}
	})
}

func FuzzDecoderStreamConformance(f *testing.F) {
	for _, seed := range []string{
		`{"id":1,"name":"Ada"} {"id":2,"active":true}`,
		`[1,2] {"id":3}`,
		`  {"name":"line1\nline2"}\n`,
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, src []byte) {
		stdDec := stdjson.NewDecoder(bytes.NewReader(src))
		compatDec := NewDecoder(bytes.NewReader(src))
		for i := 0; i < 4; i++ {
			var want, got streamRecord
			wantErr := stdDec.Decode(&want)
			gotErr := compatDec.Decode(&got)
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("stream acceptance mismatch for %q: stdlib=%v apex=%v", src, wantErr, gotErr)
			}
			if wantErr != nil {
				if wantErr == io.EOF || gotErr == io.EOF {
					if wantErr != io.EOF || gotErr != io.EOF {
						t.Fatalf("EOF mismatch for %q: stdlib=%v apex=%v", src, wantErr, gotErr)
					}
				}
				break
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("stream mismatch for %q: stdlib=%+v apex=%+v", src, want, got)
			}
		}
	})
}

type streamRecord struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}
