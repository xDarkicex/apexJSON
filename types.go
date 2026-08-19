package apexJSON

import (
	"io"
	"reflect"
	"sync"
)

// Constants organized by type
const hex = "0123456789abcdef"

type PathOp uint8

const (
	PushOp PathOp = iota
	PopOp
	UpdateIndexOp
)

// JSON formatting constants
const (
	FloatPrecision2     = "%.2f"
	FloatPrecision3     = "%.3f"
	FloatPrecision4     = "%.4f"
	FloatGeneral        = "%g"
	FloatScientificE    = "%e"
	FloatScientificCapE = "%E"
	IntFormat           = "%d"
	IntHex              = "%x"  // Hexadecimal lowercase
	IntHexUpper         = "%X"  // Hexadecimal uppercase
	IntBinary           = "%b"  // Binary
	IntOctal            = "%o"  // Octal
	FloatComma          = "%'f" // With thousand separators
)

// Token types for lexer
const (
	TokenError TokenType = iota
	TokenEOF
	TokenObjectStart // {
	TokenObjectEnd   // }
	TokenArrayStart  // [
	TokenArrayEnd    // ]
	TokenString
	TokenNumber
	TokenTrue
	TokenFalse
	TokenNull
	TokenComma
	TokenColon
)

// Parser state types
const (
	StateStart StateType = iota
	StateValue
	StateObjectKey
	StateObjectColon
	StateObjectValue
	StateObjectComma
	StateArrayValue
	StateArrayComma
	StateString
	StateNumber
	StateLiteral
	StateComplete
	StateError
)

// ContainerType enum
const (
	ContainerObject ContainerType = iota
	ContainerArray
)

// JSON text constants - grouped and byte-optimized
var (
	jsonQuoteColon   = []byte{'"', ':'}
	jsonQuote        = []byte{'"'}[0]
	jsonColon        = []byte{':', '"'}
	jsonComma        = []byte{','}[0]
	jsonTrue         = []byte("true")
	jsonFalse        = []byte("false")
	jsonNull         = []byte("null")
	jsonOpenBrace    = []byte{'{'}[0]
	jsonCloseBrace   = []byte{'}'}[0]
	jsonOpenBracket  = []byte{'['}[0]
	jsonCloseBracket = []byte{']'}[0]
	jsonQuoteComma   = []byte{'"', ','}
	jsonEmpty        = []byte("empty")
	jsonHexPrefix    = []byte("0x")
	jsonMapOpen      = []byte("map[")
	jsonMapClose     = []byte("]")
	jsonNewline      = []byte{'\n'}
)

// Escape map for string encoding
var escapeMap = [256][]byte{
	'"':  []byte(`\"`),
	'\\': []byte(`\\`),
	'\n': []byte(`\n`),
	'\r': []byte(`\r`),
	'\t': []byte(`\t`),
}

// Type aliases and simple types
type (
	TokenType     uint8
	StateType     int
	ContainerType uint8
	Number        string
	tagOptions    string
)

// Interfaces section
// -----------------

// Marshaler is the interface implemented by types that can marshal themselves into JSON
type Marshaler interface {
	MarshalJSON() ([]byte, error)
}

// Unmarshaler is the interface implemented by types that can unmarshal JSON data
type Unmarshaler interface {
	UnmarshalJSON([]byte) error
}

// TokenHandler processes JSON tokens and generates output
type TokenHandler interface {
	OnObjectStart()
	OnObjectEnd()
	OnObjectKey(key string)
	OnArrayStart()
	OnArrayEnd()
	OnString(value string)
	OnNumber(value float64)
	OnBool(value bool)
	OnNull()
	OnComma()
}

// Structs section - organized for memory alignment
// ----------------------------------------------

// SyntaxError optimized for 8-byte alignment
type SyntaxError struct {
	Offset int64  // 8 bytes
	Msg    string // 16 bytes (ptr + len)
}

// UnmarshalTypeError with fields arranged from largest to smallest
type UnmarshalTypeError struct {
	Type   reflect.Type // 16 bytes (interface)
	Value  string       // 16 bytes (ptr + len)
	Field  string       // 16 bytes (ptr + len)
	Offset int64        // 8 bytes
}

// InvalidUnmarshalError with proper alignment
type InvalidUnmarshalError struct {
	Type reflect.Type // 16 bytes (interface)
}

// Field with slices grouped together and bool at the end
type Field struct {
	nameBytes           []byte // 24 bytes (ptr + len + cap)
	nameWithQuotesBytes []byte // 24 bytes (ptr + len + cap)
	index               []int  // 24 bytes (ptr + len + cap)
	omitEmpty           bool   // 1 byte
	stringOpt           bool   // 1 byte
	// 6 bytes padding
}

// Token represents a parsed JSON token with pooled memory
type Token struct {
	Type TokenType // 1 byte
	// 7 bytes padding
	Value []byte // 24 bytes (ptr + len + cap)
}

// Buffer with largest field first for better alignment
type Buffer struct {
	buf []byte // 24 bytes (ptr + len + cap)
	off int    // 8 bytes
}

// Lexer handles low-level tokenization with zero allocations
type Lexer struct {
	data     []byte     // 24 bytes (ptr + len + cap)
	tokenBuf *[]byte    // 8 bytes (ptr)
	pos      int        // 8 bytes
	err      error      // 16 bytes (interface)
	pool     *sync.Pool // 8 bytes (ptr)
}

// Container tracks JSON structure state
type Container struct {
	Type ContainerType // 1 byte
	// 7 bytes padding
	Count int // 8 bytes
}

// Parser implements the state machine for JSON parsing
type Parser struct {
	state          StateType    // 8 bytes (int)
	depth          int          // 8 bytes
	currentKey     string       // 16 bytes (ptr + len)
	stateStack     []StateType  // 24 bytes (ptr + len + cap)
	containerStack []Container  // 24 bytes (ptr + len + cap)
	handler        TokenHandler // 16 bytes (interface)
}

// PathSegment represents a node in the JSON structure hierarchy
type PathSegment struct {
	Name         string // Object key or array index string
	IsArray      bool   // True for array index contexts
	TargetIndex  int    // Expected array index from path (-1 for non-arrays)
	CurrentIndex int    // Actual array position during parsing
	Depth        int    // Nesting depth for validation
}

type PathTracker struct {
	targetPath  []string          // Immutable target path (24 bytes)
	stack       []*PathSegment    // Stack-based tracking (24 bytes)
	matched     bool              // Match status (1 byte)
	notifyChan  chan<- PathUpdate // State channel (8 bytes)
	segmentPool *sync.Pool        // Memory management (8 bytes)

	// 7-byte padding for 64-bit alignment
	_ [7]byte
}

// ParserContext tracks parsing state at specific nesting levels
type ParserContext struct {
	containerType ContainerType // 1 byte (object/array)
	pathSegment   string        // Current path segment name
	bufferOffset  int           // Position in output buffer
	elementCount  int           // Number of elements processed

	// 6-byte padding for 64-bit alignment
	_ [6]byte
}

// ExtractHandler implements TokenHandler with stack-based path tracking
type ExtractHandler struct {
	mu           sync.Mutex      // Mutex for thread-safe operations
	pathTracker  *PathTracker    // Central path tracking system
	buffer       *Buffer         // Output buffer for captured JSON
	contextStack []ParserContext // Parsing context history
	capturing    bool            // Actively capturing JSON content
	found        bool            // Target value found flag
	result       []byte          // Final extracted result
	errors       []string        // Error messages
	// State flags packed for memory efficiency
	// inArrayContext  bool
	// inObjectContext bool
	//afterArrayIndex bool
	currentKey string // Track current object key during parsing

	_ [3]byte // Padding
}

// Implement TokenHandler interface
var _ TokenHandler = (*ExtractHandler)(nil)

type PathUpdate struct {
	Operation  PathOp
	Segment    *PathSegment // Pointer to actual segment
	Depth      int
	ArrayIndex int
}

// BufferCapture handles value capturing separately from path tracking
type BufferCapture struct {
	capturing bool // 1 byte
	found     bool // 1 byte
	// 6 bytes padding
	buffer *Buffer // 8 bytes (ptr)
	result []byte  // 24 bytes (ptr + len + cap)
}

// ArrayCollector implements TokenHandler for collecting array values
type ArrayCollector struct {
	depth   int  // 8 bytes
	inArray bool // 1 byte
	// 7 bytes padding
	currentKey string                   // 16 bytes (ptr + len)
	result     []interface{}            // 24 bytes (ptr + len + cap)
	objStack   []map[string]interface{} // 24 bytes (ptr + len + cap)
	arrayStack [][]interface{}          // 24 bytes (ptr + len + cap)
}

// ObjectCollector implements TokenHandler for collecting object properties
type ObjectCollector struct {
	depth    int  // 8 bytes
	inObject bool // 1 byte
	// 7 bytes padding
	currentKey string                   // 16 bytes (ptr + len)
	result     map[string]interface{}   // 8 bytes (ptr)
	objStack   []map[string]interface{} // 24 bytes (ptr + len + cap)
	arrayStack [][]interface{}          // 24 bytes (ptr + len + cap)
}

// UnmarshalHandler implements TokenHandler for unmarshaling JSON to Go values
type UnmarshalHandler struct {
	target     reflect.Value   // 16 bytes (interface)
	decoder    *Decoder        // 8 bytes (ptr)
	currentKey string          // 16 bytes (ptr + len)
	keyStack   []string        // 24 bytes (ptr + len + cap)
	valueStack []reflect.Value // 24 bytes (ptr + len + cap)
}

// Encoder optimized to minimize padding
type Encoder struct {
	w          io.Writer // 16 bytes (interface)
	buf        *Buffer   // 8 bytes (ptr)
	escapeHTML bool      // 1 byte
	// 7 bytes padding
}

// Decoder optimized with slices grouped together and largest fields first
type Decoder struct {
	r         io.Reader // 16 bytes (interface)
	readPos   int       // 8 bytes
	useNumber bool      // 1 byte
	// 7 bytes padding
	buf      []byte // 24 bytes (ptr + len + cap)
	tokenBuf []byte // 24 bytes (ptr + len + cap)
}

// fieldCacheKey for struct field caching
type fieldCacheKey struct {
	rtype reflect.Type // 16 bytes (interface)
}
