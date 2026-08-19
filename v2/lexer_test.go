package apexjson

import (
	"reflect"
	"strings"
	"testing"
)

func TestScannerTokens(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want []tokKind
	}{
		{"empty", "", []tokKind{kindEOF}},
		{"ws only", "   \t\n\r  ", []tokKind{kindEOF}},
		{"null", "null", []tokKind{kindNull, kindEOF}},
		{"true", "true", []tokKind{kindTrue, kindEOF}},
		{"false", "false", []tokKind{kindFalse, kindEOF}},
		{"number int", "42", []tokKind{kindNumber, kindEOF}},
		{"number neg", "-3.14", []tokKind{kindNumber, kindEOF}},
		{"number exp", "1e10", []tokKind{kindNumber, kindEOF}},
		{"number zero", "0", []tokKind{kindNumber, kindEOF}},
		{"string", `"hello"`, []tokKind{kindString, kindEOF}},
		{"string empty", `""`, []tokKind{kindString, kindEOF}},
		{"string escape", `"a\nb"`, []tokKind{kindString, kindEOF}},
		{"object empty", `{}`, []tokKind{kindObjectStart, kindObjectEnd, kindEOF}},
		{"array empty", `[]`, []tokKind{kindArrayStart, kindArrayEnd, kindEOF}},
		{"complex", `{"a":1,"b":[true,null,"x"]}`,
			[]tokKind{kindObjectStart, kindString, kindColon, kindNumber,
				kindComma, kindString, kindColon, kindArrayStart,
				kindTrue, kindComma, kindNull, kindComma, kindString,
				kindArrayEnd, kindObjectEnd, kindEOF}},
		{"unterminated", `"abc`, []tokKind{kindError}},
		{"bad literal", "tru", []tokKind{kindError}},
		{"bad number", "1.", []tokKind{kindError}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var s scanner
			s.reset([]byte(tt.src))
			var got []tokKind
			for {
				tok, ok := s.next()
				got = append(got, tok.Kind)
				if !ok || tok.Kind == kindEOF {
					break
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScannerTokenSpans(t *testing.T) {
	src := []byte(`{"k":42,"s":"hi"}`)
	var s scanner
	s.reset(src)

	want := []struct {
		start, end uint32
		kind       tokKind
	}{
		{0, 1, kindObjectStart},
		{2, 3, kindString}, // "k" content (quotes at 1,4)
		{4, 5, kindColon},
		{5, 7, kindNumber}, // 42
		{7, 8, kindComma},
		{9, 10, kindString}, // "s" content (quotes at 8,11)
		{11, 12, kindColon},
		{13, 15, kindString}, // "hi" content (quotes at 12,16)
		{16, 17, kindObjectEnd},
	}
	for i, w := range want {
		tok, ok := s.next()
		if !ok && w.kind != kindEOF {
			t.Fatalf("step %d: unexpected EOF", i)
		}
		if tok.Start != w.start || tok.End != w.end || tok.Kind != w.kind {
			t.Fatalf("step %d: got (%d,%d,%s), want (%d,%d,%s)",
				i, tok.Start, tok.End, tok.Kind,
				w.start, w.end, w.kind)
		}
	}
}

func TestScannerZeroAlloc(t *testing.T) {
	src := []byte(`{"users":[{"id":1,"name":"alice"},{"id":2,"name":"bob"}],"next":null}`)
	var s scanner
	allocs := testing.AllocsPerRun(100, func() {
		s.reset(src)
		for {
			_, ok := s.next()
			if !ok {
				break
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("lexer allocated %v/op, want 0", allocs)
	}
}

func TestSWARHelpers(t *testing.T) {
	src := []byte("hello world hello world")
	if got := swFindByte(src, 0, uint32(len(src)), 'w'); got != 6 {
		t.Errorf("FindByte 'w' = %d, want 6", got)
	}
	if got := swFindByte(src, 7, uint32(len(src)), 'w'); got != 18 {
		t.Errorf("FindByte 'w' from 7 = %d, want 18", got)
	}
	if got := swFindByteNot(src, 0, uint32(len(src)), 'h'); got != 1 {
		t.Errorf("FindByteNot 'h' = %d, want 1", got)
	}
	if got := swSkipWS([]byte("   \n\tabc"), 0, 8); got != 5 {
		t.Errorf("SkipWS = %d, want 5", got)
	}
	if got := swSkipWS([]byte("abc"), 0, 3); got != 0 {
		t.Errorf("SkipWS no-ws = %d, want 0", got)
	}
	digits := []byte("123456789012345xrest")
	if got := swFindNonDigit(digits, 0, uint32(len(digits))); got != 15 {
		t.Errorf("FindNonDigit = %d, want 15", got)
	}
	if got := swFindNonDigit(digits, 1, 16); got != 15 {
		t.Errorf("FindNonDigit unaligned = %d, want 15", got)
	}
}

func TestSWARStringEvents(t *testing.T) {
	plain := strings.Repeat("abcdef", 32)
	if got := swFindStringEvent([]byte(plain+`"`), 0, uint32(len(plain)+1)); got != uint32(len(plain)) {
		t.Fatalf("plain string event = %d, want %d", got, len(plain))
	}
	plainBytes := []byte(plain + `"`)
	if got := swFindStringEventInt(plainBytes, 0, len(plainBytes)); got != len(plain) {
		t.Fatalf("plain marshal string event = %d, want %d", got, len(plain))
	}

	escaped := strings.Repeat("abcdef", 16) + `\n` + strings.Repeat("xyz", 16)
	if got := swFindStringEvent([]byte(escaped), 0, uint32(len(escaped))); got != uint32(96) {
		t.Fatalf("escape event = %d, want 96", got)
	}
	escapedBytes := []byte(escaped)
	if got := swFindStringEventInt(escapedBytes, 0, len(escapedBytes)); got != 96 {
		t.Fatalf("marshal escape event = %d, want 96", got)
	}

	control := []byte(strings.Repeat("a", 64) + "\x01")
	if got := swFindStringEvent(control, 0, uint32(len(control))); got != 64 {
		t.Fatalf("control event = %d, want 64", got)
	}
	if got := swFindStringEventInt(control, 0, len(control)); got != 64 {
		t.Fatalf("marshal control event = %d, want 64", got)
	}

	long := []byte(`"` + strings.Repeat("payload", 128) + `"`)
	var s scanner
	s.reset(long)
	tok, ok := s.next()
	if !ok || tok.Kind != kindString || tok.End-tok.Start != uint32(len(long)-2) {
		t.Fatalf("long string token = %#v, ok=%v", tok, ok)
	}
}
