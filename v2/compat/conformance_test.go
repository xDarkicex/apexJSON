package compat

import (
	stdjson "encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

type conformanceRecord struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	Values []int  `json:"values"`
	Hidden string `json:"-"`
}

type conformanceEmbedded struct {
	ID int `json:"id"`
}

type conformanceWire struct {
	conformanceEmbedded
	Name   string             `json:"name"`
	Raw    stdjson.RawMessage `json:"raw"`
	Number stdjson.Number     `json:"number"`
}

type conformanceCustom int

func (v conformanceCustom) MarshalJSON() ([]byte, error) {
	return []byte(`"custom"`), nil
}

func TestMarshalMatchesStdlib(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{name: "record", value: conformanceRecord{ID: 7, Name: "Ada", Active: true, Values: []int{1, 2}}},
		{name: "records", value: []conformanceRecord{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}},
		{name: "embedded", value: conformanceWire{
			conformanceEmbedded: conformanceEmbedded{ID: 9},
			Name:                "Ada",
			Raw:                 stdjson.RawMessage(`{"ok":true}`),
			Number:              stdjson.Number("12.5"),
		}},
		{name: "bytes", value: []byte("hello")},
		{name: "custom", value: conformanceCustom(1)},
		{name: "non-finite", value: math.NaN()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, wantErr := stdjson.Marshal(tc.value)
			got, gotErr := Marshal(tc.value)
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("error mismatch: stdlib=%v apex=%v", wantErr, gotErr)
			}
			if wantErr == nil && string(got) != string(want) {
				t.Fatalf("Marshal mismatch:\nstdlib: %s\napex:   %s", want, got)
			}
		})
	}
}

func TestMarshalIndentMatchesStdlib(t *testing.T) {
	value := conformanceRecord{ID: 7, Name: "Ada", Active: true, Values: []int{1, 2}}
	want, err := stdjson.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got, err := MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("MarshalIndent mismatch:\nstdlib: %s\napex:   %s", want, got)
	}
}

func TestUnmarshalMatchesStdlib(t *testing.T) {
	cases := []string{
		`{"id":7,"name":"Ada","active":true,"values":[1,2]}`,
		`{"ID":8,"NAME":"Grace","ACTIVE":false}`,
		`{"id":null,"name":null,"values":null}`,
		`{"unknown":1,"name":"ignored by neither"}`,
	}
	for _, src := range cases {
		t.Run(src, func(t *testing.T) {
			var want, got conformanceRecord
			wantErr := stdjson.Unmarshal([]byte(src), &want)
			gotErr := Unmarshal([]byte(src), &got)
			if (wantErr == nil) != (gotErr == nil) {
				t.Fatalf("error mismatch: stdlib=%v apex=%v", wantErr, gotErr)
			}
			if wantErr == nil && !reflect.DeepEqual(got, want) {
				t.Fatalf("value mismatch: stdlib=%+v apex=%+v", want, got)
			}
		})
	}

	var want, got conformanceWire
	src := []byte(`{"id":9,"name":"Ada","raw":{"ok":true},"number":12.5}`)
	if err := stdjson.Unmarshal(src, &want); err != nil {
		t.Fatal(err)
	}
	if err := Unmarshal(src, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("complex value mismatch: stdlib=%+v apex=%+v", want, got)
	}
}

func TestDecoderOptionsMatchStdlib(t *testing.T) {
	src := `{"name":"Ada","extra":1}`
	stdDec := stdjson.NewDecoder(strings.NewReader(src))
	stdDec.DisallowUnknownFields()
	var stdValue conformanceRecord
	stdErr := stdDec.Decode(&stdValue)

	compatDec := NewDecoder(strings.NewReader(src))
	compatDec.DisallowUnknownFields()
	var compatValue conformanceRecord
	compatErr := compatDec.Decode(&compatValue)
	if (stdErr == nil) != (compatErr == nil) {
		t.Fatalf("unknown-field behavior mismatch: stdlib=%v compat=%v", stdErr, compatErr)
	}
}
