package apexjson

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func newMutationTestDecoder(t *testing.T, src string) *Decoder {
	t.Helper()
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	if err := dec.Parse([]byte(src)); err != nil {
		dec.Free()
		t.Fatal(err)
	}
	return dec
}

func mutationRoot(t *testing.T, dec *Decoder) string {
	t.Helper()
	raw := dec.Root().RawJSON()
	if raw == nil {
		t.Fatal("missing root")
	}
	return string(raw)
}

func TestSetPathRawValues(t *testing.T) {
	tests := []struct {
		name string
		src  string
		path []string
		raw  string
		want string
	}{
		{"number shorter", `{"v":12345}`, []string{"v"}, `7`, `{"v":7}`},
		{"number longer", `{"v":7}`, []string{"v"}, `12345`, `{"v":12345}`},
		{"escaped string", `{"v":"old"}`, []string{"v"}, `"a\\\"b"`, `{"v":"a\\\"b"}`},
		{"object", `{"v":1}`, []string{"v"}, `{"nested":true}`, `{"v":{"nested":true}}`},
		{"array", `{"v":1}`, []string{"v"}, `[1,2]`, `{"v":[1,2]}`},
		{"boolean", `{"v":1}`, []string{"v"}, `true`, `{"v":true}`},
		{"null", `{"v":1}`, []string{"v"}, `null`, `{"v":null}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := newMutationTestDecoder(t, tt.src)
			defer dec.Free()
			changed, err := dec.SetPathRaw(tt.path, []byte(tt.raw), false)
			if err != nil || !changed {
				t.Fatalf("SetPathRaw changed=%v err=%v", changed, err)
			}
			if got := mutationRoot(t, dec); got != tt.want {
				t.Fatalf("document = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSetPathRawRootAndEmptyPath(t *testing.T) {
	dec := newMutationTestDecoder(t, ` {"a":1} `)
	defer dec.Free()

	changed, err := dec.SetPathRaw(nil, []byte(`[1,2]`), false)
	if err != nil || !changed {
		t.Fatalf("root SetPathRaw changed=%v err=%v", changed, err)
	}
	if got := string(dec.Root().RawJSON()); got != `[1,2]` {
		t.Fatalf("root = %s, want [1,2]", got)
	}

	changed, err = dec.SetPathRaw([]string{}, []byte(`[1,2]`), false)
	if err != nil || changed {
		t.Fatalf("identical root replacement changed=%v err=%v", changed, err)
	}

	changed, err = dec.InsertPathRaw(nil, []byte(`9`), false)
	if err != nil || changed {
		t.Fatalf("empty InsertPathRaw changed=%v err=%v", changed, err)
	}
}

func TestSetPathRawMissingAndEscapedKeys(t *testing.T) {
	dec := newMutationTestDecoder(t, `{"profile":{},"a\u0062":1}`)
	defer dec.Free()

	changed, err := dec.SetPathRaw([]string{"profile", "age"}, []byte(`42`), true)
	if err != nil || !changed {
		t.Fatalf("create missing changed=%v err=%v", changed, err)
	}
	if got := mutationRoot(t, dec); got != `{"profile":{"age":42},"a\u0062":1}` {
		t.Fatalf("created document = %s", got)
	}

	changed, err = dec.SetPathRaw([]string{"profile", "missing"}, []byte(`1`), false)
	if err != nil || changed {
		t.Fatalf("createMissing=false changed=%v err=%v", changed, err)
	}
	changed, err = dec.SetPathRaw([]string{"absent", "nested"}, []byte(`1`), true)
	if err != nil || changed {
		t.Fatalf("missing intermediate changed=%v err=%v", changed, err)
	}
	changed, err = dec.SetPathRaw([]string{"ab"}, []byte(`2`), false)
	if err != nil || !changed {
		t.Fatalf("escaped key changed=%v err=%v", changed, err)
	}
	if got := mutationRoot(t, dec); got != `{"profile":{"age":42},"a\u0062":2}` {
		t.Fatalf("escaped-key document = %s", got)
	}
}

func TestSetPathRawArrays(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		raw     string
		want    string
		create  bool
		changed bool
	}{
		{"zero", "0", `9`, `[9,1,2]`, false, true},
		{"middle", "1", `9`, `[0,9,2]`, false, true},
		{"final", "2", `9`, `[0,1,9]`, false, true},
		{"negative", "-1", `9`, `[0,1,9]`, false, true},
		{"append", "3", `9`, `[0,1,2,9]`, true, true},
		{"prepend", "-4", `9`, `[9,0,1,2]`, true, true},
		{"positive out of range no-op", "3", `9`, `[0,1,2]`, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := newMutationTestDecoder(t, `{"items":[0,1,2]}`)
			defer dec.Free()
			changed, err := dec.SetPathRaw([]string{"items", tt.path}, []byte(tt.raw), tt.create)
			if err != nil || changed != tt.changed {
				t.Fatalf("changed=%v err=%v, want changed=%v", changed, err, tt.changed)
			}
			want := `{"items":` + tt.want + `}`
			if got := mutationRoot(t, dec); got != want {
				t.Fatalf("document = %s, want %s", got, want)
			}
		})
	}

	for _, path := range []string{"0", "-1", "9", "-9"} {
		dec := newMutationTestDecoder(t, `{"items":[]}`)
		changed, err := dec.SetPathRaw([]string{"items", path}, []byte(`1`), true)
		if err != nil || !changed || mutationRoot(t, dec) != `{"items":[1]}` {
			t.Fatalf("empty array index %q changed=%v err=%v doc=%s", path, changed, err, mutationRoot(t, dec))
		}
		dec.Free()
	}

	dec := newMutationTestDecoder(t, `{"rows":[[1,2],[3,4]]}`)
	defer dec.Free()
	changed, err := dec.SetPathRaw([]string{"rows", "-1", "0"}, []byte(`8`), false)
	if err != nil || !changed || mutationRoot(t, dec) != `{"rows":[[1,2],[8,4]]}` {
		t.Fatalf("nested array changed=%v err=%v doc=%s", changed, err, mutationRoot(t, dec))
	}
}

func TestInsertPathRaw(t *testing.T) {
	tests := []struct {
		name  string
		path  []string
		after bool
		want  string
	}{
		{"before middle", []string{"items", "1"}, false, `{"items":[0,9,1,2]}`},
		{"after middle", []string{"items", "1"}, true, `{"items":[0,1,9,2]}`},
		{"after negative final", []string{"items", "-1"}, true, `{"items":[0,1,2,9]}`},
		{"positive append", []string{"items", "99"}, false, `{"items":[0,1,2,9]}`},
		{"negative prepend", []string{"items", "-99"}, true, `{"items":[9,0,1,2]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dec := newMutationTestDecoder(t, `{"items":[0,1,2]}`)
			defer dec.Free()
			changed, err := dec.InsertPathRaw(tt.path, []byte(`9`), tt.after)
			if err != nil || !changed || mutationRoot(t, dec) != tt.want {
				t.Fatalf("changed=%v err=%v doc=%s, want %s", changed, err, mutationRoot(t, dec), tt.want)
			}
		})
	}

	dec := newMutationTestDecoder(t, `{"items":[]}`)
	changed, err := dec.InsertPathRaw([]string{"items", "0"}, []byte(`9`), false)
	if err != nil || !changed || mutationRoot(t, dec) != `{"items":[9]}` {
		t.Fatalf("empty insert changed=%v err=%v doc=%s", changed, err, mutationRoot(t, dec))
	}
	dec.Free()

	dec = newMutationTestDecoder(t, `{"a":1}`)
	defer dec.Free()
	changed, err = dec.InsertPathRaw([]string{"b"}, []byte(`2`), false)
	if err != nil || !changed || mutationRoot(t, dec) != `{"a":1,"b":2}` {
		t.Fatalf("object insert changed=%v err=%v doc=%s", changed, err, mutationRoot(t, dec))
	}
	changed, err = dec.InsertPathRaw([]string{"a"}, []byte(`3`), false)
	if err != nil || changed || mutationRoot(t, dec) != `{"a":1,"b":2}` {
		t.Fatalf("existing object insert changed=%v err=%v doc=%s", changed, err, mutationRoot(t, dec))
	}
}

func TestPathMutationValidationAndAtomicity(t *testing.T) {
	dec := newMutationTestDecoder(t, `{"items":[1,2],"value":3}`)
	defer dec.Free()
	input := []byte(`{"items":[1,2],"value":3}`)
	inputBefore := append([]byte(nil), input...)
	dec.Reset()
	if err := dec.Parse(input); err != nil {
		t.Fatal(err)
	}
	src := append([]byte(nil), dec.doc.src...)
	old := dec.Get("value")

	for _, raw := range [][]byte{nil, []byte(`1 2`), []byte(`{"broken":}`)} {
		changed, err := dec.SetPathRaw([]string{"value"}, raw, false)
		if err == nil || changed {
			t.Fatalf("invalid replacement %q changed=%v err=%v", raw, changed, err)
		}
		if !bytes.Equal(dec.doc.src, src) || !old.Exists() || dec.Get("value").Int() != 3 {
			t.Fatalf("invalid replacement changed live document: %s", dec.doc.src)
		}
		changed, err = dec.InsertPathRaw([]string{"items", "bad-index"}, []byte(`4`), false)
		if !errors.Is(err, ErrInvalidPath) || changed {
			t.Fatalf("invalid index changed=%v err=%v", changed, err)
		}
	}

	changed, err := dec.SetPathRaw([]string{"items", "1"}, []byte(`4`), false)
	if err != nil || !changed || old.Exists() {
		t.Fatalf("successful mutation changed=%v err=%v oldExists=%v", changed, err, old.Exists())
	}
	if !bytes.Equal(input, inputBefore) {
		t.Fatalf("path mutation modified caller input: %s", input)
	}
	if err := dec.Parse([]byte(`{"after":true}`)); err != nil {
		t.Fatal(err)
	}
	dec.Reset()
	if dec.Root().Exists() {
		t.Fatal("Reset retained path-mutated root")
	}
	if err := dec.Parse([]byte(`{"after":true}`)); err != nil {
		t.Fatal(err)
	}
	dec.Free()
}

func TestPathMutationResetAndFree(t *testing.T) {
	dec := newMutationTestDecoder(t, `{"value":"old"}`)
	stale := dec.Get("value")
	changed, err := dec.SetPathRaw([]string{"value"}, []byte(`"a-longer-value"`), false)
	if err != nil || !changed || stale.Exists() || dec.doc.ownedSource == nil {
		t.Fatalf("path mutation changed=%v err=%v stale=%v owned=%v", changed, err, stale.Exists(), dec.doc.ownedSource != nil)
	}
	dec.Reset()
	if dec.doc.ownedSource != nil || dec.Root().Exists() {
		t.Fatal("Reset did not clear path-mutated state")
	}
	if err := dec.Parse([]byte(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	if changed, err := dec.SetPathRaw([]string{"value"}, []byte(`2`), false); err != nil || !changed {
		t.Fatalf("mutation after Reset changed=%v err=%v", changed, err)
	}
	dec.Free()
	if dec.arena != nil || dec.doc.ownedSource != nil {
		t.Fatal("Free retained mutation-owned storage")
	}
}

func TestPathMutationOneDecoderPerGoroutine(t *testing.T) {
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dec, err := NewDecoder()
			if err != nil {
				errs <- err
				return
			}
			defer dec.Free()
			if err := dec.Parse([]byte(`{"items":[1,2]}`)); err != nil {
				errs <- err
				return
			}
			if changed, err := dec.InsertPathRaw([]string{"items", "1"}, []byte(`9`), false); err != nil || !changed {
				if err == nil {
					err = ErrInvalidMutation
				}
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestRawJSON(t *testing.T) {
	dec := newMutationTestDecoder(t, `{"s":"hello","n":12,"obj":{"x":true},"arr":[1,2]}`)
	defer dec.Free()

	tests := map[string]string{
		"s":   `"hello"`,
		"n":   `12`,
		"obj": `{"x":true}`,
		"arr": `[1,2]`,
	}
	for path, want := range tests {
		if got := string(dec.Get(path).RawJSON()); got != want {
			t.Errorf("RawJSON(%s) = %s, want %s", path, got, want)
		}
	}

	stale := dec.Get("s")
	old := dec.Get("s")
	borrowed := old.RawJSON()
	if err := old.SetString("new-value"); err != nil {
		t.Fatal(err)
	}
	if stale.Exists() {
		t.Fatal("value handle remained valid after RawJSON mutation")
	}
	if borrowed == nil || string(borrowed) != `"hello"` {
		t.Fatalf("borrowed RawJSON was unexpectedly modified: %q", borrowed)
	}
	if err := dec.Parse([]byte(`{"x":1}`)); err != nil {
		t.Fatal(err)
	}
	if dec.Root().RawJSON() == nil {
		t.Fatal("RawJSON unavailable after reparse")
	}
}

func BenchmarkSetPathRawExisting(b *testing.B) {
	dec := newMutationBenchmarkDecoder(b, `{"profile":{"count":1}}`)
	defer dec.Free()
	path := []string{"profile", "count"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		raw := []byte(`1`)
		if i&1 != 0 {
			raw = []byte(`2`)
		}
		if _, err := dec.SetPathRaw(path, raw, false); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSetPathRawCreateMissing(b *testing.B) {
	path := []string{"profile", "age"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec := newMutationBenchmarkDecoder(b, `{"profile":{}}`)
		if _, err := dec.SetPathRaw(path, []byte(`42`), true); err != nil {
			dec.Free()
			b.Fatal(err)
		}
		dec.Free()
	}
}

func BenchmarkInsertPathRawArray(b *testing.B) {
	path := []string{"items", "1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec := newMutationBenchmarkDecoder(b, `{"items":[0,1,2]}`)
		if _, err := dec.InsertPathRaw(path, []byte(`9`), false); err != nil {
			dec.Free()
			b.Fatal(err)
		}
		dec.Free()
	}
}

func BenchmarkInsertPathRawNested(b *testing.B) {
	path := []string{"rows", "1", "items", "1"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec := newMutationBenchmarkDecoder(b, `{"rows":[{"items":[0,1]},{"items":[2,3]}]}`)
		if _, err := dec.InsertPathRaw(path, []byte(`9`), true); err != nil {
			dec.Free()
			b.Fatal(err)
		}
		dec.Free()
	}
}

func BenchmarkValueSetRaw(b *testing.B) {
	dec := newMutationBenchmarkDecoder(b, `{"value":1}`)
	defer dec.Free()
	v := dec.Get("value")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := v.SetRaw([]byte(`1`)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRawJSON(b *testing.B) {
	dec := newMutationBenchmarkDecoder(b, `{"payload":{"items":[1,2,3]}}`)
	defer dec.Free()
	v := dec.Get("payload")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(v.RawJSON()) == 0 {
			b.Fatal("empty RawJSON")
		}
	}
}

func newMutationBenchmarkDecoder(b *testing.B, src string) *Decoder {
	b.Helper()
	dec, err := NewDecoder()
	if err != nil {
		b.Fatal(err)
	}
	if err := dec.Parse([]byte(src)); err != nil {
		dec.Free()
		b.Fatal(err)
	}
	return dec
}
