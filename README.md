# apexJSON: High-Performance JSON for Go 🚀

[![GoDoc](https://godoc.org/github.com/xDarkicex/apexJSON)](https://godoc.org/github.com/xDarkicex/apexJSON)
[![Go Report Card](https://goreportcard.com/badge/github.com/xDarkicex/apexJSON)](https://goreportcard.com/report/github.com/xDarkicex/apexJSON)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
a high-performance, drop-in replacement for Go's standard `encoding/json` package. It's designed for developers who need blazing-fast JSON processing without external dependencies. 

## ✨ Why apexJSON?

* **High Performance** - Outperforms the standard library in many benchmarks, especially with complex types
* **Zero Dependencies** - No external libraries, just pure Go code ready to use
* **Drop-In Replacement** - Compatible API with the standard library for painless integration
* **Memory Efficient** - Significantly fewer allocations and memory usage
* **Extraction Superpowers** - Extract nested values with zero allocations at lightning speed

## 📊 Benchmark Showdown

We've benchmarked apexJSON against the Go standard library and popular alternatives like jsoniter, sonic, segmentio, and goccy. The results speak for themselves:

### Marshaling Simple Types

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 118.1 | 24 | 1 |
| Std | 114.1 | 56 | 2 |
| Jsoniter | 112.1 | 56 | 2 |
| Sonic | 226.0 | 73 | 3 |
| Segmentio | 74.28 | 56 | 2 |
| Goccy | 71.22 | 56 | 2 |

### Marshaling Complex Types

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 435.5 | 176 | 4 |
| Std | 1033 | 824 | 12 |
| Jsoniter | 792.4 | 520 | 4 |
| Sonic | 1414 | 422 | 3 |
| Segmentio | 524.9 | 400 | 2 |
| Goccy | 666.3 | 400 | 2 |

### Unmarshaling Simple Types

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 273.2 | 24 | 1 |
| Std | 400.9 | 256 | 7 |
| Jsoniter | 120.5 | 32 | 2 |
| Sonic | 209.4 | 211 | 4 |
| Segmentio | 81.88 | 32 | 2 |
| Goccy | 91.12 | 56 | 2 |

### Unmarshaling Complex Types

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 2379 | 1088 | 34 |
| Std | 3344 | 1336 | 46 |
| Jsoniter | 1176 | 1008 | 36 |
| Sonic | 926.7 | 1377 | 17 |
| Segmentio | 1577 | 5664 | 32 |
| Goccy | 1051 | 1185 | 26 |

### Extraction (Nested Value)

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 429.8 | 0 | 0 |
| Std | 3763 | 2216 | 80 |
| Gjson | 209.0 | 16 | 1 |

### Marshaling Complex User-Defined Types

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 2584 | 2150 | 16 |
| Std | 2796 | 2090 | 15 |
| Jsoniter | 2063 | 1880 | 9 |
| Sonic | 3313 | 1850 | 8 |
| Segmentio | 1208 | 1554 | 4 |
| Goccy | 1770 | 1762 | 7 |

### Unmarshaling Complex User-Defined Types

| Package | Time (ns/op) | Bytes (B/op) | Allocs (allocs/op) |
|---------|--------------|--------------|-------------------|
| **apexJSON** | 9056 | 2875 | 49 |
| Std | 9698 | 2544 | 65 |
| Jsoniter | 3202 | 2569 | 64 |
| Sonic | 2481 | 3347 | 23 |
| Segmentio | 3154 | 8208 | 37 |
| Goccy | 2608 | 3225 | 33 |

## 🚀 Installation

Getting started with apexJSON is simple:

```go
import "github.com/yourusername/apexJSON"
```

That's it! No external dependencies to manage—just pure, optimized JSON handling.

## 📝 Usage Examples

### Marshaling

```go
type User struct {
    Name string
    Age  int
}

user := User{Name: "Alex", Age: 28}
data, err := apexJSON.Marshal(user)
if err != nil {
    panic(err)
}
fmt.Println(string(data)) // {"Name":"Alex","Age":28}
```

### Unmarshaling

```go
var user User
err := apexJSON.Unmarshal([]byte(`{"Name":"Alex","Age":28}`), &user)
if err != nil {
    panic(err)
}
fmt.Println(user.Name, user.Age) // Alex 28
```

### Extraction (Nested Value)

```go
data := []byte(`{"user":{"info":{"id":42}}}`)
value, ok := apexJSON.Extract(data, "user", "info", "id")
if ok {
    fmt.Println("Extracted ID:", value) // Extracted ID: 42
}
```

## 💎 The Zero Dependencies Advantage

In a world of dependency sprawl, apexJSON stands out by offering high performance with zero external dependencies:

* **Simplicity**: No dependency management headaches or version conflicts
* **Security**: Smaller attack surface with fewer potential vulnerabilities
* **Portability**: Works anywhere Go runs—from cloud servers to IoT devices
* **Lightweight**: Perfect for CLI tools, embedded systems, or resource-constrained environments

## 🔍 Use Cases

apexJSON shines in these scenarios:

* **API Servers** - Process large volumes of JSON with minimal overhead
* **Real-time Applications** - Where every millisecond matters
* **Resource-constrained Environments** - When memory efficiency is critical
* **Containerized Applications** - Keep your images small and dependencies minimal
* **High-traffic Systems** - Reduce CPU and memory pressure from JSON processing

## 🏁 Conclusion

apexJSON offers a powerful combination of performance, compatibility, and simplicity. With zero dependencies and optimized memory usage, it's the ideal JSON library for projects where efficiency matters.

Give it a try in your next project and experience the difference for yourself!

## 📜 License

MIT License - see the [LICENSE](LICENSE) file for details.
