package apexjson

import (
	stdjson "encoding/json"
	"testing"

	goccyjson "github.com/goccy/go-json"
)

// BenchmarkMarshalMap measures the map path of the encoder.
func BenchmarkMarshalMap(b *testing.B) {
	m := map[string]interface{}{"alpha": "one", "beta": "two", "gamma": "three"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Marshal(m); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMarshalMapLarge measures a map with enough entries to be worth
// sorting.
func BenchmarkMarshalMapLarge(b *testing.B) {
	m := make(map[string]interface{}, 64)
	for i := 0; i < 64; i++ {
		m[string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Marshal(m); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMarshalStruct measures the typed fast path, which the encoder's
// unsafe walker serves without reflection.
func BenchmarkMarshalStruct(b *testing.B) {
	type payload struct {
		Name  string   `json:"name"`
		Count int      `json:"count"`
		Ratio float64  `json:"ratio"`
		Tags  []string `json:"tags"`
	}
	p := payload{"x", 7, 1.5, []string{"a", "b", "c"}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Marshal(p); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMarshalScalars measures the scalar path.
func BenchmarkMarshalScalars(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Marshal("hello"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompareMarshalMap compares the encoders on the shape that dominates
// real payloads: a generic map, decoded the way encoding/json decodes into
// any.
func BenchmarkCompareMarshalMap(b *testing.B) {
	value := map[string]any{
		"name":    "alice",
		"age":     float64(30),
		"active":  true,
		"tags":    []any{"x", "y", "z"},
		"address": map[string]any{"city": "lisbon", "zip": "1000"},
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
		})
	}
}

// BenchmarkCompareMarshalMapLarge compares the encoders on a large generic map.
func BenchmarkCompareMarshalMapLarge(b *testing.B) {
	var rows []any
	if err := stdjson.Unmarshal(benchLarge1MiB, &rows); err != nil {
		b.Fatal(err)
	}
	value := map[string]any{"rows": rows}
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
		})
	}
}

// BenchmarkUnmarshalMap measures decoding into a generic map.
func BenchmarkUnmarshalMap(b *testing.B) {
	var dst map[string]any
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst = nil
		if err := Unmarshal(benchSmall, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUnmarshalAny measures decoding into an interface destination.
func BenchmarkUnmarshalAny(b *testing.B) {
	var dst any
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst = nil
		if err := Unmarshal(benchSmall, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkUnmarshalMapLarge measures decoding a large document into a map.
func BenchmarkUnmarshalMapLarge(b *testing.B) {
	// The corpus is a JSON array; wrap it so the destination is a map.
	wrapped := append(append([]byte(`{"rows":`), benchLarge1MiB...), '}')
	var dst map[string]any
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dst = nil
		if err := Unmarshal(wrapped, &dst); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCompareUnmarshalMap compares the decoders on a generic map
// destination.
func BenchmarkCompareUnmarshalMap(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   func([]byte, any) error
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			var dst map[string]any
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst = nil
				if err := benchmark.fn(benchSmall, &dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkCompareUnmarshalAny compares the decoders on an interface
// destination.
func BenchmarkCompareUnmarshalAny(b *testing.B) {
	benchmarks := []struct {
		name string
		fn   func([]byte, any) error
	}{
		{name: "ApexJSON", fn: Unmarshal},
		{name: "Stdlib", fn: stdjson.Unmarshal},
		{name: "GoJSON", fn: goccyjson.Unmarshal},
		{name: "JSONv2", fn: unmarshalJSONv2},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			var dst any
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst = nil
				if err := benchmark.fn(benchSmall, &dst); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
