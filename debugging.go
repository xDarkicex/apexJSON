package apexJSON

import (
	"fmt"
)

var debug bool = true

// debugLexer creates a debug wrapper around the lexer to monitor token stream
func debugLexer(data []byte) error {
	lexer := NewLexer(data)
	defer lexer.Close()
	fmt.Println("\n-- TOKEN STREAM --")
	for {
		token := lexer.NextToken()
		var tokenValue string
		if token.Value != nil {
			tokenValue = string(token.Value)
		}

		fmt.Printf("Token: Type=%v, Value=%q, Position=%d\n",
			tokenTypeToString(token.Type), tokenValue, lexer.pos)

		if token.Type == TokenError {
			fmt.Printf("ERROR: Lexer error at position %d\n", lexer.pos)
			return fmt.Errorf("lexer error at position %d", lexer.pos)
		}

		if token.Type == TokenEOF {
			fmt.Println("End of input reached")
			break
		}

		// The original function referenced handler state here, but
		// since we're making a standalone function, those references
		// would need to be passed in from the caller
	}
	return nil
}

// Helper function to convert token types to strings for debugging
func tokenTypeToString(t TokenType) string {
	switch t {
	case TokenError:
		return "TokenError"
	case TokenEOF:
		return "TokenEOF"
	case TokenObjectStart:
		return "TokenObjectStart"
	case TokenObjectEnd:
		return "TokenObjectEnd"
	case TokenArrayStart:
		return "TokenArrayStart"
	case TokenArrayEnd:
		return "TokenArrayEnd"
	case TokenString:
		return "TokenString"
	case TokenNumber:
		return "TokenNumber"
	case TokenTrue:
		return "TokenTrue"
	case TokenFalse:
		return "TokenFalse"
	case TokenNull:
		return "TokenNull"
	case TokenComma:
		return "TokenComma"
	case TokenColon:
		return "TokenColon"
	default:
		return fmt.Sprintf("Unknown(%d)", t)
	}
}

// For ArrayCollector
func (h *ArrayCollector) OnComma() {
	// No special logic needed for array collectors
}

// For UnmarshalHandler
func (h *UnmarshalHandler) OnComma() {
	// No special logic needed for unmarshal handlers
}
