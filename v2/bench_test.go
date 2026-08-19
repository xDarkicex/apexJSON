package apexjson

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func reportGBPerSec(b *testing.B, bytesPerOp int) {
	b.ReportMetric(float64(int64(b.N)*int64(bytesPerOp))/b.Elapsed().Seconds()/1e9, "GB/s")
}

var benchSmall = []byte(`{"name":"alice","age":30,"active":true,"tags":["x","y","z"]}`)
var benchScalar = []byte(`42`)
var benchIntSlice = []byte(`[1,2,3,4,5]`)
var benchMedium = []byte(`{
	"id": 12345,
	"name": "example",
	"tags": ["alpha","beta","gamma","delta","epsilon"],
	"counts": [1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20],
	"meta": {"created": "2025-01-01T00:00:00Z", "updated": "2025-08-17T12:00:00Z"},
	"flags": {"enabled": true, "verified": false, "draft": false},
	"users": [
		{"id":1,"name":"alice","email":"alice@example.com"},
		{"id":2,"name":"bob","email":"bob@example.com"},
		{"id":3,"name":"carol","email":"carol@example.com"}
	]
}`)

const benchLargeRecordJSON = `{"id":123456,"name":"alice","active":true,"score":12.5,"message":"abcdefghijklmnopqrstuvwxyz0123456789"}`
const benchLargePrettyRecordJSON = `{
  "id": 123456,
  "name": "alice",
  "active": true,
  "score": 12.5,
  "message": "abcdefghijklmnopqrstuvwxyz0123456789"
}`

var (
	benchLarge1MiB       = buildLargeBenchJSON(1 << 20)
	benchLarge16MiB      = buildLargeBenchJSON(16 << 20)
	benchLargePretty1MiB = buildLargeBenchJSONFrom(1<<20, []byte(benchLargePrettyRecordJSON))
)

type benchPerson struct {
	Name   string   `json:"name"`
	Age    int      `json:"age"`
	Active bool     `json:"active"`
	Tags   []string `json:"tags"`
}

type benchLargeRecord struct {
	ID      int     `json:"id"`
	Name    string  `json:"name"`
	Active  bool    `json:"active"`
	Score   float64 `json:"score"`
	Message string  `json:"message"`
}

func buildLargeBenchJSON(target int) []byte {
	return buildLargeBenchJSONFrom(target, []byte(benchLargeRecordJSON))
}

func buildLargeBenchJSONFrom(target int, item []byte) []byte {
	out := make([]byte, 0, target+2)
	out = append(out, '[')
	for len(out)+len(item)+1 < target {
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

func newLargeBenchDecoder(b *testing.B, src []byte) *Decoder {
	b.Helper()
	// This record shape uses fewer than one tape node per four input bytes.
	// Leave generous headroom so the benchmark measures parsing, not capacity
	// selection or arena growth.
	nodeCap := len(src)/4 + 4096
	dec, err := NewDecoderSize(uint64(nodeCap*nodeSize+4096), nodeCap)
	if err != nil {
		b.Fatal(err)
	}
	return dec
}

func benchmarkLargeParse(b *testing.B, src []byte) {
	dec := newLargeBenchDecoder(b, src)
	defer dec.Free()
	if err := dec.Parse(src); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dec.Parse(src); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(src))
}

// parseLargeWithStats runs the same production parser with diagnostic stage
// accounting attached. It intentionally lives in the benchmark file so the
// normal API and hot path do not grow a public instrumentation mode.
func parseLargeWithStats(dec *Decoder, src []byte) (parseStats, error) {
	var stats parseStats
	dec.err = nil
	dec.src = src
	dec.root = noNode
	dec.arena.Reset()
	dec.doc.slab.reset()
	dec.doc.src = src

	var p fusedParser
	p.reset(&dec.doc, src)
	p.stats = &stats
	started := time.Now()
	idx, ok := p.parseDocument()
	stats.TotalNS = uint64(time.Since(started))
	stats.Nodes = uint64(dec.doc.slab.len())
	if !ok {
		if p.err != nil {
			dec.err = p.err
		} else {
			dec.err = &SyntaxError{Msg: "parse failed"}
		}
		return stats, dec.err
	}
	dec.root = idx
	dec.doc.root = idx
	return stats, nil
}

func (s *parseStats) add(other parseStats) {
	s.TotalNS += other.TotalNS
	s.WhitespaceNS += other.WhitespaceNS
	s.StringScanNS += other.StringScanNS
	s.NumberScanNS += other.NumberScanNS
	s.LiteralScanNS += other.LiteralScanNS
	s.WhitespaceBytes += other.WhitespaceBytes
	s.Strings += other.Strings
	s.StringBytes += other.StringBytes
	s.Numbers += other.Numbers
	s.NumberBytes += other.NumberBytes
	s.Literals += other.Literals
	s.Objects += other.Objects
	s.Arrays += other.Arrays
	s.Keys += other.Keys
	s.Nodes += other.Nodes
}

func reportParseStageStats(b *testing.B, stats parseStats) {
	perOp := float64(b.N)
	b.ReportMetric(float64(stats.WhitespaceNS)/perOp, "whitespace-ns/op")
	b.ReportMetric(float64(stats.StringScanNS)/perOp, "string-scan-ns/op")
	b.ReportMetric(float64(stats.NumberScanNS)/perOp, "number-scan-ns/op")
	b.ReportMetric(float64(stats.LiteralScanNS)/perOp, "literal-scan-ns/op")
	scanned := stats.WhitespaceNS + stats.StringScanNS +
		stats.NumberScanNS + stats.LiteralScanNS
	if stats.TotalNS > scanned {
		b.ReportMetric(float64(stats.TotalNS-scanned)/perOp, "grammar-emit-ns/op")
	}
	b.ReportMetric(float64(stats.Nodes)/perOp, "nodes/op")
	b.ReportMetric(float64(stats.Keys)/perOp, "keys/op")
	b.ReportMetric(float64(stats.Strings)/perOp, "strings/op")
	b.ReportMetric(float64(stats.StringBytes)/perOp, "string-bytes/op")
	b.ReportMetric(float64(stats.NumberBytes)/perOp, "number-bytes/op")
	b.ReportMetric(float64(stats.WhitespaceBytes)/perOp, "whitespace-bytes/op")
}

// BenchmarkLargeParseStages1MiB is a diagnostic benchmark. Its timing is
// intentionally instrumented and must not be compared to the throughput
// benchmark; use it to identify which stage deserves optimization.
func BenchmarkLargeParseStages1MiB(b *testing.B) {
	benchmarkParseStages(b, benchLarge1MiB)
}

func BenchmarkLargePrettyParseStages1MiB(b *testing.B) {
	benchmarkParseStages(b, benchLargePretty1MiB)
}

func benchmarkParseStages(b *testing.B, src []byte) {
	dec := newLargeBenchDecoder(b, src)
	defer dec.Free()
	if _, err := parseLargeWithStats(dec, src); err != nil {
		b.Fatal(err)
	}
	var total parseStats
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats, err := parseLargeWithStats(dec, src)
		if err != nil {
			b.Fatal(err)
		}
		total.add(stats)
	}
	b.StopTimer()
	reportParseStageStats(b, total)
}

// BenchmarkLargeTapeEmission1MiB replays the exact node shape produced by the
// large fixture but measures only off-heap tape writes. It is intentionally a
// benchmark-only split: production appendFixedNode remains fully inlined and
// pays no instrumentation branch or clock read.
func BenchmarkLargeTapeEmission1MiB(b *testing.B) {
	benchmarkTapeEmission(b, benchLarge1MiB)
}

func BenchmarkLargePrettyTapeEmission1MiB(b *testing.B) {
	benchmarkTapeEmission(b, benchLargePretty1MiB)
}

func benchmarkTapeEmission(b *testing.B, src []byte) {
	dec := newLargeBenchDecoder(b, src)
	defer dec.Free()
	if err := dec.Parse(src); err != nil {
		b.Fatal(err)
	}
	nodes := make([]nodeView, dec.doc.slab.len())
	for i := range nodes {
		nodes[i] = dec.doc.slab.view(int32(i))
	}
	dec.doc.slab.reset()

	var checksum int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec.doc.slab.reset()
		for j, node := range nodes {
			idx := dec.doc.slab.appendFixedNode(node.Kind, node.Flags, node.Start, node.End)
			if (node.Kind == nodeObject || node.Kind == nodeArray) && node.Skip == int32(j+1) {
				dec.doc.slab.closeContainer(idx, node.End)
			}
		}
		checksum += dec.doc.slab.len()
	}
	b.StopTimer()
	if checksum == 0 {
		b.Fatal("emission checksum was zero")
	}
	b.ReportMetric(float64(len(nodes)), "nodes/op")
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(nodes)), "emit-ns/node")
}

func unmarshalDirectWithStats(src []byte, rv reflect.Value, stats *directStats) error {
	var d directDecoder
	d.src = src
	d.stats = stats
	d.scan.reset(src)
	if err := d.value(rv, 0); err != nil {
		return err
	}
	tok, more := d.scan.next()
	if more || tok.Kind == kindError {
		return directSyntaxError(tok, "unexpected "+tok.Kind.String())
	}
	return nil
}

func (s *directStats) add(other directStats) {
	s.TotalNS += other.TotalNS
	s.TokenNS += other.TokenNS
	s.ValueTokenNS += other.ValueTokenNS
	s.KeyScanNS += other.KeyScanNS
	s.FieldLookupNS += other.FieldLookupNS
	s.FieldDecodeNS += other.FieldDecodeNS
	s.FastScalarNS += other.FastScalarNS
	s.SeparatorNS += other.SeparatorNS
	s.Tokens += other.Tokens
	s.ValueTokens += other.ValueTokens
	s.KeyTokens += other.KeyTokens
	s.FieldLookups += other.FieldLookups
	s.FieldDecodes += other.FieldDecodes
	s.FastScalars += other.FastScalars
	s.Separators += other.Separators
	s.FieldHits += other.FieldHits
	s.FieldMisses += other.FieldMisses
}

// BenchmarkLargeDirectTypedDecodeStages1MiB isolates scanner time from field
// lookup time in the direct typed path. Like the parse-stage benchmark, this
// is diagnostic and includes clock reads.
func BenchmarkLargeDirectTypedDecodeStages1MiB(b *testing.B) {
	rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
	dst := reflect.ValueOf(&rows).Elem()
	var warmup directStats
	if err := unmarshalDirectWithStats(benchLarge1MiB, dst, &warmup); err != nil {
		b.Fatal(err)
	}

	var total directStats
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var stats directStats
		started := time.Now()
		if err := unmarshalDirectWithStats(benchLarge1MiB, dst, &stats); err != nil {
			b.Fatal(err)
		}
		stats.TotalNS = uint64(time.Since(started))
		total.add(stats)
	}
	b.StopTimer()
	perOp := float64(b.N)
	b.ReportMetric(float64(total.TotalNS)/perOp, "decode-ns/op")
	b.ReportMetric(float64(total.TokenNS)/perOp, "token-scan-ns/op")
	b.ReportMetric(float64(total.ValueTokenNS)/perOp, "value-token-ns/op")
	b.ReportMetric(float64(total.KeyScanNS)/perOp, "key-scan-ns/op")
	b.ReportMetric(float64(total.FieldLookupNS)/perOp, "field-lookup-ns/op")
	b.ReportMetric(float64(total.FieldDecodeNS)/perOp, "field-decode-ns/op")
	b.ReportMetric(float64(total.FastScalarNS)/perOp, "fast-scalar-ns/op")
	b.ReportMetric(float64(total.SeparatorNS)/perOp, "separator-ns/op")
	b.ReportMetric(float64(total.Tokens)/perOp, "tokens/op")
	b.ReportMetric(float64(total.ValueTokens)/perOp, "value-tokens/op")
	b.ReportMetric(float64(total.KeyTokens)/perOp, "key-tokens/op")
	b.ReportMetric(float64(total.FieldLookups)/perOp, "field-lookups/op")
	b.ReportMetric(float64(total.FieldDecodes)/perOp, "field-decodes/op")
	b.ReportMetric(float64(total.FastScalars)/perOp, "fast-scalars/op")
	b.ReportMetric(float64(total.Separators)/perOp, "separators/op")
	b.ReportMetric(float64(total.FieldHits)/perOp, "field-hits/op")
	b.ReportMetric(float64(total.FieldMisses)/perOp, "field-misses/op")
}

func benchmarkLargeScan(b *testing.B, src []byte) {
	var scan scanner
	var checksum uint32
	scan.reset(src)
	for {
		tok, ok := scan.next()
		if !ok {
			if tok.Kind == kindError {
				b.Fatal("large benchmark fixture failed scanning")
			}
			break
		}
		checksum += tok.Start + tok.End + uint32(tok.Kind)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scan.reset(src)
		for {
			tok, ok := scan.next()
			if !ok {
				if tok.Kind == kindError {
					b.Fatal("large benchmark fixture failed scanning")
				}
				break
			}
			checksum += tok.Start + tok.End + uint32(tok.Kind)
		}
	}
	b.StopTimer()
	if checksum == 0 {
		b.Fatal("scanner checksum was zero")
	}
	reportGBPerSec(b, len(src))
}

func benchmarkLargeStdlibValid(b *testing.B, src []byte) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !json.Valid(src) {
			b.Fatal("large benchmark fixture is invalid")
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(src))
}

func benchmarkLargeTypedDecode(b *testing.B, src []byte, stdlib bool) {
	rows := make([]benchLargeRecord, 0, len(src)/len(benchLargeRecordJSON)+1)
	if stdlib {
		if err := json.Unmarshal(src, &rows); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := json.Unmarshal(src, &rows); err != nil {
				b.Fatal(err)
			}
		}
	} else {
		dec := newLargeBenchDecoder(b, src)
		defer dec.Free()
		dst := reflect.ValueOf(&rows).Elem()
		if err := dec.Parse(src); err != nil {
			b.Fatal(err)
		}
		if err := decodeValue(&dec.doc, dec.root, dec.src, dst); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := dec.Parse(src); err != nil {
				b.Fatal(err)
			}
			if err := decodeValue(&dec.doc, dec.root, dec.src, dst); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(src))
}

func benchmarkLargeAnyDecode(b *testing.B, src []byte, stdlib bool) {
	var out []any
	if stdlib {
		if err := json.Unmarshal(src, &out); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := json.Unmarshal(src, &out); err != nil {
				b.Fatal(err)
			}
		}
	} else {
		dec := newLargeBenchDecoder(b, src)
		defer dec.Free()
		dst := reflect.ValueOf(&out).Elem()
		if err := dec.Parse(src); err != nil {
			b.Fatal(err)
		}
		if err := decodeValue(&dec.doc, dec.root, dec.src, dst); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := dec.Parse(src); err != nil {
				b.Fatal(err)
			}
			if err := decodeValue(&dec.doc, dec.root, dec.src, dst); err != nil {
				b.Fatal(err)
			}
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(src))
}

func BenchmarkLargeParse1MiB(b *testing.B) { benchmarkLargeParse(b, benchLarge1MiB) }

func BenchmarkLargePrettyParse1MiB(b *testing.B) {
	benchmarkLargeParse(b, benchLargePretty1MiB)
}

func BenchmarkLargeScan1MiB(b *testing.B) { benchmarkLargeScan(b, benchLarge1MiB) }

func BenchmarkLargeParse16MiB(b *testing.B) { benchmarkLargeParse(b, benchLarge16MiB) }

func BenchmarkLargeStdlibValid1MiB(b *testing.B) {
	benchmarkLargeStdlibValid(b, benchLarge1MiB)
}

func BenchmarkLargeStdlibValid16MiB(b *testing.B) {
	benchmarkLargeStdlibValid(b, benchLarge16MiB)
}

func BenchmarkLargeTypedDecode1MiB(b *testing.B) {
	benchmarkLargeTypedDecode(b, benchLarge1MiB, false)
}

func BenchmarkLargeDirectTypedDecode1MiB(b *testing.B) {
	rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
	if err := Unmarshal(benchLarge1MiB, &rows); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Unmarshal(benchLarge1MiB, &rows); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchLarge1MiB))
}

func BenchmarkLargeDirectTypedDecodePretty1MiB(b *testing.B) {
	rows := make([]benchLargeRecord, 0, len(benchLargePretty1MiB)/len(benchLargePrettyRecordJSON)+1)
	if err := Unmarshal(benchLargePretty1MiB, &rows); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Unmarshal(benchLargePretty1MiB, &rows); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchLargePretty1MiB))
}

func BenchmarkLargeValueQuery1MiB(b *testing.B) {
	dec := newLargeBenchDecoder(b, benchLarge1MiB)
	defer dec.Free()
	path := []string{"0", "message"}
	if err := dec.Parse(benchLarge1MiB); err != nil {
		b.Fatal(err)
	}
	if !dec.GetPath(path).Exists() {
		b.Fatal("large benchmark query did not resolve")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dec.Parse(benchLarge1MiB); err != nil {
			b.Fatal(err)
		}
		if !dec.GetPath(path).Exists() {
			b.Fatal("large benchmark query did not resolve")
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchLarge1MiB))
}

func BenchmarkLargeValueIter1MiB(b *testing.B) {
	dec := newLargeBenchDecoder(b, benchLarge1MiB)
	defer dec.Free()
	if err := dec.Parse(benchLarge1MiB); err != nil {
		b.Fatal(err)
	}
	var array ArrayIter
	var object ObjectIter
	var checksum int
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := dec.Parse(benchLarge1MiB); err != nil {
			b.Fatal(err)
		}
		array.Reset(dec.Root())
		for array.Next() {
			object.Reset(array.Value())
			for object.Next() {
				checksum += len(object.KeyBytes()) + int(object.Value().Type())
			}
		}
	}
	b.StopTimer()
	if checksum == 0 {
		b.Fatal("iterator checksum was zero")
	}
	reportGBPerSec(b, len(benchLarge1MiB))
}

func BenchmarkLargeStdlibTypedDecode1MiB(b *testing.B) {
	benchmarkLargeTypedDecode(b, benchLarge1MiB, true)
}

func BenchmarkLargeAnyDecode1MiB(b *testing.B) {
	benchmarkLargeAnyDecode(b, benchLarge1MiB, false)
}

func BenchmarkLargeStdlibAnyDecode1MiB(b *testing.B) {
	benchmarkLargeAnyDecode(b, benchLarge1MiB, true)
}

func BenchmarkUnmarshalSmall(t *testing.B) {
	var p benchPerson
	for i := 0; i < t.N; i++ {
		_ = Unmarshal(benchSmall, &p)
	}
	reportGBPerSec(t, len(benchSmall))
}

func BenchmarkStdlibUnmarshalSmall(t *testing.B) {
	var p benchPerson
	for i := 0; i < t.N; i++ {
		_ = json.Unmarshal(benchSmall, &p)
	}
	reportGBPerSec(t, len(benchSmall))
}

func BenchmarkUnmarshalScalar(t *testing.B) {
	var n int
	for i := 0; i < t.N; i++ {
		_ = Unmarshal(benchScalar, &n)
	}
	reportGBPerSec(t, len(benchScalar))
}

func BenchmarkUnmarshalSmallReuse(t *testing.B) {
	p := benchPerson{Tags: make([]string, 3)}
	for i := 0; i < t.N; i++ {
		_ = Unmarshal(benchSmall, &p)
	}
	reportGBPerSec(t, len(benchSmall))
}

func BenchmarkUnmarshalIntSlice(t *testing.B) {
	var values []int
	for i := 0; i < t.N; i++ {
		_ = Unmarshal(benchIntSlice, &values)
	}
	reportGBPerSec(t, len(benchIntSlice))
}

func BenchmarkUnmarshalMedium(t *testing.B) {
	var m map[string]any
	for i := 0; i < t.N; i++ {
		_ = Unmarshal(benchMedium, &m)
	}
	reportGBPerSec(t, len(benchMedium))
}

func BenchmarkStdlibUnmarshalMedium(t *testing.B) {
	var m map[string]any
	for i := 0; i < t.N; i++ {
		_ = json.Unmarshal(benchMedium, &m)
	}
	reportGBPerSec(t, len(benchMedium))
}

func BenchmarkMarshalSmall(t *testing.B) {
	p := benchPerson{Name: "alice", Age: 30, Active: true, Tags: []string{"x", "y", "z"}}
	for i := 0; i < t.N; i++ {
		_, _ = Marshal(p)
	}
	reportGBPerSec(t, len(benchSmall))
}

func BenchmarkStdlibMarshalSmall(t *testing.B) {
	p := benchPerson{Name: "alice", Age: 30, Active: true, Tags: []string{"x", "y", "z"}}
	for i := 0; i < t.N; i++ {
		_, _ = json.Marshal(p)
	}
	reportGBPerSec(t, len(benchSmall))
}

func BenchmarkDecoderGet(t *testing.B) {
	dec, _ := NewDecoder()
	defer dec.Free()
	path := []string{"users", "0", "name"}
	_ = dec.Parse(benchMedium)
	t.ResetTimer()
	for i := 0; i < t.N; i++ {
		_ = dec.Parse(benchMedium)
		_ = dec.GetPath(path)
	}
	reportGBPerSec(t, len(benchMedium))
}

func BenchmarkDecoderParse(t *testing.B) {
	dec, _ := NewDecoder()
	defer dec.Free()
	for i := 0; i < t.N; i++ {
		_ = dec.Parse(benchMedium)
	}
	reportGBPerSec(t, len(benchMedium))
}

func BenchmarkDecoderZeroAlloc(t *testing.B) {
	// Verify the steady-state allocation count is 0 for the
	// reusable Decoder path.
	dec, _ := NewDecoder()
	defer dec.Free()
	path := []string{"users", "0", "name"}
	_ = dec.Parse(benchMedium)
	allocs := testing.AllocsPerRun(1000, func() {
		_ = dec.Parse(benchMedium)
		_ = dec.GetPath(path)
	})
	if allocs > 0 {
		t.Errorf("Decoder allocated %v/op, want 0", allocs)
	}
}
