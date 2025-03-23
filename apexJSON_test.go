package apexJSON_test

import (
	"apexJSON"
	"encoding/json"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	goccy "github.com/goccy/go-json"
	jsoniter "github.com/json-iterator/go"
	segmentio "github.com/segmentio/encoding/json"
	"github.com/tidwall/gjson"
)

// Test structures
type SimpleStruct struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

type ComplexStruct struct {
	ID       int                    `json:"id"`
	Name     string                 `json:"name"`
	IsActive bool                   `json:"is_active"`
	Score    float64                `json:"score"`
	Tags     []string               `json:"tags"`
	Data     []interface{}          `json:"data"`
	Metadata map[string]interface{} `json:"metadata"`
	Address  *Address               `json:"address,omitempty"`
}

type Address struct {
	Street  string `json:"street"`
	City    string `json:"city"`
	Country string `json:"country"`
	Zip     string `json:"zip"`
}

var (
	simple = SimpleStruct{
		Name: "John Doe",
		Age:  30,
	}

	complex = ComplexStruct{
		ID:       12345,
		Name:     "Complex Object",
		IsActive: true,
		Score:    99.5,
		Tags:     []string{"tag1", "tag2", "tag3"},
		Data:     []interface{}{1, "string", true, 42.5},
		Metadata: map[string]interface{}{
			"created":  1710804000,
			"owner":    "system",
			"priority": 3,
		},
		Address: &Address{
			Street:  "123 Main St",
			City:    "Anytown",
			Country: "USA",
			Zip:     "12345",
		},
	}

	// Pre-generated JSON for unmarshal tests
	simpleJSON, _  = json.Marshal(simple)
	complexJSON, _ = json.Marshal(complex)
)

// Standard library benchmarks
func BenchmarkStdMarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(simple)
	}
}

func BenchmarkStdMarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(complex)
	}
}

func BenchmarkStdUnmarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s SimpleStruct
		_ = json.Unmarshal(simpleJSON, &s)
	}
}

func BenchmarkStdUnmarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c ComplexStruct
		_ = json.Unmarshal(complexJSON, &c)
	}
}

// apexJSON benchmarks
func BenchmarkApexMarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = apexJSON.Marshal(simple)
	}
}

func BenchmarkApexMarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = apexJSON.Marshal(complex)
	}
}

func BenchmarkApexUnmarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s SimpleStruct
		_ = apexJSON.Unmarshal(simpleJSON, &s)
	}
}

func BenchmarkApexUnmarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c ComplexStruct
		_ = apexJSON.Unmarshal(complexJSON, &c)
	}
}

// jsoniter benchmarks
func BenchmarkJsoniterMarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = jsoniter.Marshal(simple)
	}
}

func BenchmarkJsoniterMarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = jsoniter.Marshal(complex)
	}
}

func BenchmarkJsoniterUnmarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s SimpleStruct
		_ = jsoniter.Unmarshal(simpleJSON, &s)
	}
}

func BenchmarkJsoniterUnmarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c ComplexStruct
		_ = jsoniter.Unmarshal(complexJSON, &c)
	}
}

// sonic benchmarks
func BenchmarkSonicMarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = sonic.Marshal(simple)
	}
}

func BenchmarkSonicMarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = sonic.Marshal(complex)
	}
}

func BenchmarkSonicUnmarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s SimpleStruct
		_ = sonic.Unmarshal(simpleJSON, &s)
	}
}

func BenchmarkSonicUnmarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c ComplexStruct
		_ = sonic.Unmarshal(complexJSON, &c)
	}
}

// segmentio benchmarks
func BenchmarkSegmentioMarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = segmentio.Marshal(simple)
	}
}

func BenchmarkSegmentioMarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = segmentio.Marshal(complex)
	}
}

func BenchmarkSegmentioUnmarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s SimpleStruct
		_ = segmentio.Unmarshal(simpleJSON, &s)
	}
}

func BenchmarkSegmentioUnmarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c ComplexStruct
		_ = segmentio.Unmarshal(complexJSON, &c)
	}
}

// goccy/go-json benchmarks
func BenchmarkGoccyMarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = goccy.Marshal(simple)
	}
}

func BenchmarkGoccyMarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = goccy.Marshal(complex)
	}
}

func BenchmarkGoccyUnmarshalSimple(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var s SimpleStruct
		_ = goccy.Unmarshal(simpleJSON, &s)
	}
}

func BenchmarkGoccyUnmarshalComplex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var c ComplexStruct
		_ = goccy.Unmarshal(complexJSON, &c)
	}
}

// Extract benchmarks
func BenchmarkStdExtractNestedValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var m map[string]interface{}
		_ = json.Unmarshal(complexJSON, &m)
		address := m["address"].(map[string]interface{})
		_ = address["city"].(string)
	}
}

func BenchmarkApexExtractNestedValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = apexJSON.Extract(complexJSON, "address", "city")
	}
}

func BenchmarkGjsonExtractNestedValue(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = gjson.GetBytes(complexJSON, "address.city").String()
	}
}

// real world benchmarks
type User struct {
	ID        int       `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	Profile   Profile   `json:"profile"`
	Posts     []Post    `json:"posts"`
	Settings  Settings  `json:"settings"`
}

type Profile struct {
	FullName    string   `json:"full_name"`
	Age         int      `json:"age"`
	Bio         string   `json:"bio"`
	Interests   []string `json:"interests"`
	AvatarURL   string   `json:"avatar_url"`
	SocialLinks []string `json:"social_links"`
}

type Post struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Tags      []string  `json:"tags"`
	Likes     int       `json:"likes"`
	Comments  []Comment `json:"comments"`
}

type Comment struct {
	ID        int       `json:"id"`
	UserID    int       `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type Settings struct {
	Notifications bool              `json:"notifications"`
	Privacy       string            `json:"privacy"`
	Theme         string            `json:"theme"`
	Preferences   map[string]string `json:"preferences"`
}

var complexUser = User{
	ID:        1,
	Username:  "johndoe",
	Email:     "john@example.com",
	CreatedAt: time.Now(),
	Profile: Profile{
		FullName:    "John Doe",
		Age:         30,
		Bio:         "Software engineer and tech enthusiast",
		Interests:   []string{"programming", "AI", "blockchain"},
		AvatarURL:   "https://example.com/avatar.jpg",
		SocialLinks: []string{"https://twitter.com/johndoe", "https://github.com/johndoe"},
	},
	Posts: []Post{
		{
			ID:        101,
			Title:     "My First Blog Post",
			Content:   "This is the content of my first blog post...",
			CreatedAt: time.Now().Add(-24 * time.Hour),
			Tags:      []string{"tech", "programming"},
			Likes:     15,
			Comments: []Comment{
				{
					ID:        1001,
					UserID:    2,
					Content:   "Great post!",
					CreatedAt: time.Now().Add(-23 * time.Hour),
				},
				{
					ID:        1002,
					UserID:    3,
					Content:   "Looking forward to more content.",
					CreatedAt: time.Now().Add(-22 * time.Hour),
				},
			},
		},
		{
			ID:        102,
			Title:     "Reflections on Modern Web Development",
			Content:   "In this post, I'll share my thoughts on the current state of web development...",
			CreatedAt: time.Now().Add(-12 * time.Hour),
			Tags:      []string{"web", "javascript", "react"},
			Likes:     8,
			Comments:  []Comment{},
		},
	},
	Settings: Settings{
		Notifications: true,
		Privacy:       "friends",
		Theme:         "dark",
		Preferences: map[string]string{
			"language": "en",
			"timezone": "UTC",
		},
	},
}

// Pre-generated JSON for unmarshal tests
var complexUserJSON, _ = json.Marshal(complexUser)

// Standard library complex user benchmarks
func BenchmarkStdMarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(complexUser)
	}
}

func BenchmarkStdUnmarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u User
		_ = json.Unmarshal(complexUserJSON, &u)
	}
}

// apexJSON complex user benchmarks
func BenchmarkApexMarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = apexJSON.Marshal(complexUser)
	}
}

func BenchmarkApexUnmarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u User
		_ = apexJSON.Unmarshal(complexUserJSON, &u)
	}
}

// jsoniter complex user benchmarks
func BenchmarkJsoniterMarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = jsoniter.Marshal(complexUser)
	}
}

func BenchmarkJsoniterUnmarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u User
		_ = jsoniter.Unmarshal(complexUserJSON, &u)
	}
}

// sonic complex user benchmarks
func BenchmarkSonicMarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = sonic.Marshal(complexUser)
	}
}

func BenchmarkSonicUnmarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u User
		_ = sonic.Unmarshal(complexUserJSON, &u)
	}
}

// segmentio complex user benchmarks
func BenchmarkSegmentioMarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = segmentio.Marshal(complexUser)
	}
}

func BenchmarkSegmentioUnmarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u User
		_ = segmentio.Unmarshal(complexUserJSON, &u)
	}
}

// goccy complex user benchmarks
func BenchmarkGoccyMarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_, _ = goccy.Marshal(complexUser)
	}
}

func BenchmarkGoccyUnmarshalComplexUser(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u User
		_ = goccy.Unmarshal(complexUserJSON, &u)
	}
}

// profiling test functions
func TestProfileMarshalCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping profile test in short mode")
	}

	f, err := os.Create("marshal_cpu.prof")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = pprof.StartCPUProfile(f)
	if err != nil {
		t.Fatal(err)
	}
	defer pprof.StopCPUProfile()

	// Run complex marshaling 10,000 times to get a good profile
	for i := 0; i < 10000; i++ {
		_, err := apexJSON.Marshal(complexUser)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileMarshalMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping profile test in short mode")
	}

	// Run the GC to clear out any existing allocations
	runtime.GC()

	// Create memory profile before
	beforeF, err := os.Create("marshal_mem_before.prof")
	if err != nil {
		t.Fatal(err)
	}
	defer beforeF.Close()
	err = pprof.WriteHeapProfile(beforeF)
	if err != nil {
		t.Fatal(err)
	}

	// Run complex marshaling 10,000 times
	for i := 0; i < 10000; i++ {
		_, err := apexJSON.Marshal(complexUser)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Create memory profile after
	afterF, err := os.Create("marshal_mem_after.prof")
	if err != nil {
		t.Fatal(err)
	}
	defer afterF.Close()
	err = pprof.WriteHeapProfile(afterF)
	if err != nil {
		t.Fatal(err)
	}
}

// Helper function for comparing benchmark results
func TestCompareAllLibraries(t *testing.T) {
	// This is a placeholder test that can be run to generate comprehensive benchmarks
	// Run with: go test -bench=. -benchmem > benchmark_results.txt
	t.Skip("This is not a real test, just a placeholder for benchmark comparison")
}
