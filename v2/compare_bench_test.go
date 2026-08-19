package apexjson

import (
	stdjson "encoding/json"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	jsonv2 "github.com/go-json-experiment/json"
	goccyjson "github.com/goccy/go-json"
)

type typedLatencyResult struct {
	p50NS        int64
	p99NS        int64
	p999NS       int64
	allocBytes   uint64
	allocs       uint64
	retainedHeap int64
}

func percentileNS(samples []int64, numerator, denominator int) int64 {
	idx := (len(samples)*numerator+denominator-1)/denominator - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(samples) {
		idx = len(samples) - 1
	}
	return samples[idx]
}

func measureTypedLatency(src []byte, fn compareUnmarshalFunc, samples int) (typedLatencyResult, error) {
	rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
	dst := &rows
	for i := 0; i < 32; i++ {
		if err := fn(src, dst); err != nil {
			return typedLatencyResult{}, err
		}
	}

	// Measure allocation volume over a batch. Reading MemStats per operation
	// would materially distort the latency samples.
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	const allocationSamples = 100
	for i := 0; i < allocationSamples; i++ {
		if err := fn(src, dst); err != nil {
			return typedLatencyResult{}, err
		}
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	result := typedLatencyResult{
		allocs: (after.Mallocs - before.Mallocs) / allocationSamples,
	}
	if result.allocs != 0 {
		result.allocBytes = (after.TotalAlloc - before.TotalAlloc) / allocationSamples
	}

	// Measure retained heap separately. The destination is intentionally kept
	// alive, so this includes buffers retained by the decoder but excludes
	// transient garbage after collection.
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := fn(src, dst); err != nil {
		return typedLatencyResult{}, err
	}
	runtime.GC()
	runtime.ReadMemStats(&after)
	result.retainedHeap = int64(after.HeapAlloc) - int64(before.HeapAlloc)

	latencies := make([]int64, samples)
	for i := range latencies {
		started := time.Now()
		if err := fn(src, dst); err != nil {
			return typedLatencyResult{}, err
		}
		latencies[i] = time.Since(started).Nanoseconds()
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	result.p50NS = percentileNS(latencies, 1, 2)
	result.p99NS = percentileNS(latencies, 99, 100)
	result.p999NS = percentileNS(latencies, 999, 1000)
	return result, nil
}

// TestCompareTypedLatency1MiB reports steady-state latency percentiles that
// testing.B cannot expose. Run with -v to compare p50/p99/p99.9 alongside the
// normal GB/s, B/op, and allocs/op benchmark.
func TestCompareTypedLatency1MiB(t *testing.T) {
	if os.Getenv("APEXJSON_RUN_LATENCY") != "1" {
		t.Skip("set APEXJSON_RUN_LATENCY=1 to run percentile latency collection")
	}
	samples := 3000
	if value := os.Getenv("APEXJSON_LATENCY_SAMPLES"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 1000 {
			samples = parsed
		}
	}
	benchmarks := []struct {
		name string
		fn   compareUnmarshalFunc
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		benchmark := benchmark
		t.Run(benchmark.name, func(t *testing.T) {
			result, err := measureTypedLatency(benchLarge1MiB, benchmark.fn, samples)
			if err != nil {
				t.Fatal(err)
			}
			// With bytes and nanoseconds, the numerical ratio is already GB/s:
			// bytes/ns == (bytes/s)/1e9.
			gbps := float64(len(benchLarge1MiB)) / float64(result.p50NS)
			t.Logf("p50=%dns p99=%dns p99.9=%dns throughput@p50=%.4f GB/s alloc=%d B/op allocs=%.2f/op retained-heap=%d B", result.p50NS, result.p99NS, result.p999NS, gbps, result.allocBytes, float64(result.allocs), result.retainedHeap)
		})
	}
}

type compareUnmarshalFunc func([]byte, any) error

func unmarshalJSONv2(src []byte, dst any) error {
	return jsonv2.Unmarshal(src, dst)
}

func marshalJSONv2(value any) ([]byte, error) {
	return jsonv2.Marshal(value)
}

func benchmarkCompareUnmarshal(
	b *testing.B,
	src []byte,
	fn compareUnmarshalFunc,
	dst any,
	valid func() bool,
) {
	b.Helper()
	if err := fn(src, dst); err != nil {
		b.Fatal(err)
	}
	if valid != nil && !valid() {
		b.Fatal("warm-up decode produced an empty result")
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := fn(src, dst); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if valid != nil && !valid() {
		b.Fatal("decode produced an empty result")
	}
	reportGBPerSec(b, len(src))
}

func BenchmarkCompareTypedUnmarshal1MiB(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   compareUnmarshalFunc
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
			benchmarkCompareUnmarshal(b, benchLarge1MiB, benchmark.fn, &rows, func() bool {
				return len(rows) != 0
			})
		})
	}
}

func BenchmarkCompareUnmarshalSmall(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   compareUnmarshalFunc
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			var value benchPerson
			benchmarkCompareUnmarshal(b, benchSmall, benchmark.fn, &value, nil)
		})
	}
}

func BenchmarkCompareMarshalSmall(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   func(any) ([]byte, error)
	}{
		{name: "ApexJSON", fn: Marshal},
		{name: "Stdlib", fn: stdjson.Marshal},
		{name: "GoJSON", fn: goccyjson.Marshal},
		{name: "JSONv2", fn: marshalJSONv2},
	}
	value := benchPerson{Name: "alice", Age: 30, Active: true, Tags: []string{"x", "y", "z"}}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			if _, err := benchmark.fn(value); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := benchmark.fn(value); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			reportGBPerSec(b, len(benchSmall))
		})
	}
}

func BenchmarkCompareMarshalLarge1MiB(b *testing.B) {
	rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
	if err := Unmarshal(benchLarge1MiB, &rows); err != nil {
		b.Fatal(err)
	}
	benchmarks := []struct {
		name string
		fn   func(any) ([]byte, error)
	}{
		{name: "ApexJSON", fn: Marshal},
		{name: "Stdlib", fn: stdjson.Marshal},
		{name: "GoJSON", fn: goccyjson.Marshal},
		{name: "JSONv2", fn: marshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			warmup, err := benchmark.fn(rows)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := benchmark.fn(rows); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			reportGBPerSec(b, len(warmup))
		})
	}
}

func BenchmarkAppendMarshalSmall(b *testing.B) {
	value := benchPerson{Name: "alice", Age: 30, Active: true, Tags: []string{"x", "y", "z"}}
	buf := make([]byte, 0, len(benchSmall)*2)
	if _, err := AppendMarshal(buf, value); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		var err error
		buf, err = AppendMarshal(buf, value)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchSmall))
}

func BenchmarkAppendMarshalLarge1MiB(b *testing.B) {
	rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
	if err := Unmarshal(benchLarge1MiB, &rows); err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 0, len(benchLarge1MiB)+64)
	if _, err := AppendMarshal(buf, rows); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		var err error
		buf, err = AppendMarshal(buf, rows)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchLarge1MiB))
}

func BenchmarkAppendMarshalPtrSmall(b *testing.B) {
	value := benchPerson{Name: "alice", Age: 30, Active: true, Tags: []string{"x", "y", "z"}}
	buf := make([]byte, 0, len(benchSmall)*2)
	if _, err := AppendMarshalPtr(buf, &value); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		var err error
		buf, err = AppendMarshalPtr(buf, &value)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchSmall))
}

func BenchmarkAppendMarshalPtrLarge1MiB(b *testing.B) {
	rows := make([]benchLargeRecord, 0, len(benchLarge1MiB)/len(benchLargeRecordJSON)+1)
	if err := Unmarshal(benchLarge1MiB, &rows); err != nil {
		b.Fatal(err)
	}
	buf := make([]byte, 0, len(benchLarge1MiB)+64)
	if _, err := AppendMarshalPtr(buf, &rows); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		var err error
		buf, err = AppendMarshalPtr(buf, &rows)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportGBPerSec(b, len(benchLarge1MiB))
}

func BenchmarkCompareDynamicUnmarshal1MiB(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   compareUnmarshalFunc
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			var values []any
			benchmarkCompareUnmarshal(b, benchLarge1MiB, benchmark.fn, &values, func() bool {
				return len(values) != 0
			})
		})
	}
}

func BenchmarkCompareDynamicUnmarshalMedium(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   compareUnmarshalFunc
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			var values map[string]any
			benchmarkCompareUnmarshal(b, benchMedium, benchmark.fn, &values, func() bool {
				return len(values) != 0
			})
		})
	}
}
