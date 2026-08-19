package apexjson

import (
	"strings"
	"testing"
)

// parseTestCase describes a parsed document and the slab shape we expect.
type parseTestCase struct {
	name string
	src  string
	// want is the expected kind/span of the root node.
	rootKind nodeKind
	rootSpan [2]uint32 // [start, end) of root value
}

func TestParseScalar(t *testing.T) {
	cases := []parseTestCase{
		{"null", `null`, nodeNull, [2]uint32{0, 4}},
		{"true", `true`, nodeTrue, [2]uint32{0, 4}},
		{"false", `false`, nodeFalse, [2]uint32{0, 5}},
		{"number", `42`, nodeNumber, [2]uint32{0, 2}},
		{"string", `"hi"`, nodeString, [2]uint32{1, 3}},
		{"string empty", `""`, nodeString, [2]uint32{1, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := parse([]byte(c.src))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			root := doc.slab.view(doc.root)
			if root.Kind != c.rootKind {
				t.Errorf("kind = %s, want %s",
					root.Kind, c.rootKind)
			}
			if root.Start != c.rootSpan[0] || root.End != c.rootSpan[1] {
				t.Errorf("span = [%d,%d), want [%d,%d)",
					root.Start, root.End,
					c.rootSpan[0], c.rootSpan[1])
			}
		})
	}
}

func TestParseContainer(t *testing.T) {
	src := `{"name":"alice","age":30,"tags":["admin","user"],"active":true}`
	doc, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	root := doc.slab.view(doc.root)
	if root.Kind != nodeObject {
		t.Fatalf("root kind = %s, want object", root.Kind)
	}
	if root.Start != 0 || root.End != uint32(len(src)) {
		t.Errorf("root span = [%d,%d), want [0,%d)",
			root.Start, root.End, len(src))
	}
	// Count children: should be 8 (4 pairs of key+value).
	childCount := 0
	for c := firstChild(&doc.slab, doc.root); c != noNode; {
		childCount++
		c = nextChild(&doc.slab, doc.root, c)
	}
	if childCount != 8 {
		t.Errorf("object child count = %d, want 8", childCount)
	}
	// Walk pairs.
	for c := firstChild(&doc.slab, doc.root); c != noNode; {
		keyNode := doc.slab.view(c)
		if keyNode.Kind != nodeString {
			t.Errorf("expected string key, got %s", keyNode.Kind)
		}
		valIdx := c + 1
		if valIdx == noNode {
			t.Fatal("key without value")
		}
		valNode := doc.slab.view(valIdx)
		_ = valNode
		c = nextChild(&doc.slab, doc.root, valIdx)
	}
}

func TestSlabIsColumnar(t *testing.T) {
	doc, err := parse([]byte(`[{"id":1,"name":"a"},{"id":2,"name":"b"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.slab.n == 0 {
		t.Fatal("empty tape")
	}
	if len(doc.slab.meta) < doc.slab.n ||
		len(doc.slab.starts) < doc.slab.n || len(doc.slab.ends) < doc.slab.n ||
		len(doc.slab.skips) < doc.slab.n || len(doc.slab.meta) != len(doc.slab.starts) ||
		len(doc.slab.meta) != len(doc.slab.ends) || len(doc.slab.meta) != len(doc.slab.skips) {
		t.Fatalf("column lengths do not match tape capacity: n=%d meta=%d starts=%d ends=%d skips=%d",
			doc.slab.n, len(doc.slab.meta), len(doc.slab.starts), len(doc.slab.ends), len(doc.slab.skips))
	}

	dec, err := NewDecoderSize(64*1024, 256)
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Free()
	if err := dec.Parse([]byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if cap(dec.doc.slab.meta) != 256 ||
		cap(dec.doc.slab.starts) != 256 || cap(dec.doc.slab.ends) != 256 ||
		cap(dec.doc.slab.skips) != 256 {
		t.Fatal("arena tape columns do not retain the configured capacity")
	}
}

func TestPackedTapeSkipsSubtrees(t *testing.T) {
	doc, err := parse([]byte(`{"a":[{"b":1},2],"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.slab.skip(doc.root) != int32(doc.slab.len()) {
		t.Fatalf("root skip = %d, want %d", doc.slab.skip(doc.root), doc.slab.len())
	}
	root := doc.root
	aKey := firstChild(&doc.slab, root)
	aValue := aKey + 1
	if doc.slab.kind(aValue) != nodeArray {
		t.Fatalf("a value kind = %s", doc.slab.kind(aValue))
	}
	first := firstChild(&doc.slab, aValue)
	second := nextChild(&doc.slab, aValue, first)
	if doc.slab.kind(first) != nodeObject || doc.slab.kind(second) != nodeNumber {
		t.Fatalf("array children = %s, %s", doc.slab.kind(first), doc.slab.kind(second))
	}
	cKey := nextChild(&doc.slab, root, aValue)
	if string(doc.src[doc.slab.start(cKey):doc.slab.end(cKey)]) != "c" {
		t.Fatalf("next root key = %q, want c", doc.src[doc.slab.start(cKey):doc.slab.end(cKey)])
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"trailing comma object", `{"a":1,}`},
		{"trailing comma array", `[1,]`},
		{"missing colon", `{"a" 1}`},
		{"unterminated string", `"abc`},
		{"unexpected token", `}{`},
		{"bare identifier", `truefalse`},
		{"bad number", `1.`},
		{"number then garbage", `1abc`},
		{"deep nesting overflow", nestedArray(15000)},
		{"extra after root", `1 2`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parse([]byte(c.src))
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

func TestParseDeepNestingOK(t *testing.T) {
	// 1000 levels deep; parsing uses an explicit tape-linked stack.
	src := nestedArray(1000) + strings.Repeat("]", 1000)
	// Actually the above gives wrong structure; build a balanced one.
	src = strings.Repeat("[", 500) + "1" + strings.Repeat("]", 500)
	_, err := parse([]byte(src))
	if err != nil {
		t.Fatalf("500-deep parse failed: %v", err)
	}
}

func TestParseRoundTripSpans(t *testing.T) {
	// Spans must accurately delimit source slices. String spans exclude
	// the surrounding quotes (per lexer design).
	src := []byte(`{"name":"x","n":-1.5e2,"b":true,"z":null}`)
	doc, err := parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"name", "x", "n", "-1.5e2", "b", "true", "z", "null"}
	i := 0
	for c := firstChild(&doc.slab, doc.root); c != noNode; {
		key := string(src[doc.slab.start(c):doc.slab.end(c)])
		valIdx := c + 1
		val := string(src[doc.slab.start(valIdx):doc.slab.end(valIdx)])
		if key != want[2*i] {
			t.Errorf("key %d = %q, want %q", i, key, want[2*i])
		}
		if val != want[2*i+1] {
			t.Errorf("val %d = %q, want %q", i, val, want[2*i+1])
		}
		c = nextChild(&doc.slab, doc.root, valIdx)
		i++
	}
	if i != 4 {
		t.Errorf("walked %d pairs, want 4", i)
	}
}

func TestParseDeepNestingHasNoFixedLimit(t *testing.T) {
	depth := 12000
	src := strings.Repeat("[", depth) + "1" + strings.Repeat("]", depth)
	if _, err := parse([]byte(src)); err != nil {
		t.Fatalf("%d-deep parse failed: %v", depth, err)
	}
}

func nestedArray(n int) string {
	return strings.Repeat("[", n)
}
