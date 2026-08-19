package apexJSON

import (
	"fmt"
	"strconv"
)

// NewParser creates a new parser with the given token handler
func NewParser(handler TokenHandler) *Parser {
	return &Parser{
		state:          StateStart,
		stateStack:     make([]StateType, 0, 32),
		containerStack: make([]Container, 0, 32),
		handler:        handler,
		depth:          0,
	}
}

// Parse processes JSON text using the state machine
func (p *Parser) Parse(data []byte) error {
	lexer := NewLexer(data)
	defer lexer.Close()

	p.state = StateStart

	for {
		token := lexer.NextToken()

		if token.Type == TokenError {
			return fmt.Errorf("lexer error at position %d", lexer.pos)
		}

		if token.Type == TokenEOF {
			if p.state != StateComplete && p.state != StateStart {
				return p.errorExpected("end of input")
			}
			break
		}

		if err := p.ProcessToken(token); err != nil {
			return err
		}

		if p.state == StateComplete {
			break
		}
	}

	return nil
}

// State stack management
func (p *Parser) pushState(s StateType) {
	p.stateStack = append(p.stateStack, p.state)
	p.state = s
}

func (p *Parser) popState() {
	if len(p.stateStack) > 0 {
		p.state = p.stateStack[len(p.stateStack)-1]
		p.stateStack = p.stateStack[:len(p.stateStack)-1]
	} else {
		p.state = StateComplete
	}
}

// Container stack management
func (p *Parser) pushContainer(cType ContainerType) {
	p.containerStack = append(p.containerStack, Container{
		Type:  cType,
		Count: 0,
	})
	p.depth++
}

func (p *Parser) popContainer() Container {
	if len(p.containerStack) == 0 {
		return Container{}
	}

	container := p.containerStack[len(p.containerStack)-1]
	p.containerStack = p.containerStack[:len(p.containerStack)-1]
	p.depth--
	return container
}

// ========================
// START WORK HERE TOMORROW
// ========================
//
// parser isnt adhering to new path tracking and extracthandler types

// ProcessToken processes a token based on current state
// This is the heart of the state machine
func (p *Parser) ProcessToken(token Token) error {
	switch p.state {
	case StateStart:
		switch token.Type {
		case TokenObjectStart:
			p.handler.OnObjectStart()
			p.pushContainer(ContainerObject)
			p.state = StateObjectKey
		case TokenArrayStart:
			p.handler.OnArrayStart()
			p.pushContainer(ContainerArray)
			p.state = StateArrayValue
		default:
			if err := p.processValueToken(token); err != nil {
				return err
			}
			p.state = StateComplete
		}

	case StateObjectKey:
		if token.Type == TokenString {
			p.currentKey = GetString(token.Value)
			p.handler.OnObjectKey(p.currentKey)
			p.state = StateObjectColon
		} else if token.Type == TokenObjectEnd {
			p.handler.OnObjectEnd()
			p.popContainer()
			p.transitionAfterValue()
		} else {
			return p.errorExpected("string key or '}'")
		}

	case StateObjectColon:
		if token.Type != TokenColon {
			return p.errorExpected("colon")
		}
		p.state = StateObjectValue

	case StateObjectValue:
		if err := p.processValueToken(token); err != nil {
			return err
		}
		if token.Type == TokenObjectStart || token.Type == TokenArrayStart {
			return nil
		}
		p.state = StateObjectComma

	case StateObjectComma:
		switch token.Type {
		case TokenComma:
			p.handler.OnComma()
			p.state = StateObjectKey
		case TokenObjectEnd:
			p.handler.OnObjectEnd()
			p.popContainer()
			p.transitionAfterValue()
		default:
			return p.errorExpected("comma or '}'")
		}

	case StateArrayValue:
		if token.Type != TokenArrayEnd {
			p.notifyArrayElement()
			if handler, ok := p.handler.(*ExtractHandler); ok {
				if handler.pathTracker.ExpectingObjectAfterArray() &&
					token.Type == TokenObjectStart {
					handler.pathTracker.ClearArrayFlags()
					p.handler.OnObjectStart()
					p.pushContainer(ContainerObject)
					p.state = StateObjectKey
					return nil
				}
			}
		}

		if token.Type == TokenArrayEnd {
			p.handler.OnArrayEnd()
			p.popContainer()
			p.transitionAfterValue()
			return nil
		}

		if err := p.processValueToken(token); err != nil {
			return err
		}
		p.state = StateArrayComma

	case StateArrayComma:
		switch token.Type {
		case TokenComma:
			p.handler.OnComma()
			p.state = StateArrayValue
			if handler, ok := p.handler.(*ExtractHandler); ok {
				handler.pathTracker.ClearArrayFlags()
			}
		case TokenArrayEnd:
			p.handler.OnArrayEnd()
			p.popContainer()
			p.transitionAfterValue()
		default:
			return p.errorExpected("comma or ']'")
		}

	case StateValue:
		if err := p.processValueToken(token); err != nil {
			return err
		}
		p.transitionAfterValue()

	case StateString, StateNumber, StateLiteral:
		return p.errorExpected("value processing by lexer")

	case StateComplete:
		return p.errorExpected("end of input")

	case StateError:
		return fmt.Errorf("parser in error state")
	}

	// State validation hook for ExtractHandler
	if handler, ok := p.handler.(*ExtractHandler); ok {
		switch token.Type {
		case TokenObjectStart, TokenObjectEnd, TokenArrayStart, TokenArrayEnd:
			handler.validateState()

			// Additional validation after structural changes
			if token.Type == TokenObjectEnd || token.Type == TokenArrayEnd {
				// Ensure context stack matches parser's container stack
				if handler.ParsingDepth() != len(p.containerStack) {
					handler.errors = append(handler.errors,
						"container stack depth mismatch after structural end")
				}
			}
		}
	}

	return nil
}

func (p *Parser) notifyArrayElement() {
	if len(p.containerStack) > 0 {
		container := &p.containerStack[len(p.containerStack)-1]
		if container.Type == ContainerArray {
			// Determine if we're at the start or end of an element
			// and use the appropriate index
			index := container.Count

			// Call OnArrayElement if the handler implements it
			if handler, ok := p.handler.(interface{ OnArrayElement(int) }); ok {
				handler.OnArrayElement(index)
			}
		}
	}
}

// Add helper methods for state transitions and error reporting
func (p *Parser) transitionAfterValue() {
	if len(p.containerStack) == 0 {
		p.state = StateComplete
	} else {
		container := &p.containerStack[len(p.containerStack)-1]
		container.Count++

		if container.Type == ContainerObject {
			p.state = StateObjectComma
		} else {
			p.state = StateArrayComma
		}
	}
}

// processValueToken processes a token that represents a JSON value
func (p *Parser) processValueToken(token Token) error {
	switch token.Type {
	case TokenString:
		p.handler.OnString(GetString(token.Value))
		return nil
	case TokenNumber:
		n, err := strconv.ParseFloat(GetString(token.Value), 64)
		if err != nil {
			return fmt.Errorf("invalid number: %s", GetString(token.Value))
		}
		p.handler.OnNumber(n)
		return nil
	case TokenTrue:
		p.handler.OnBool(true)
		return nil
	case TokenFalse:
		p.handler.OnBool(false)
		return nil
	case TokenNull:
		p.handler.OnNull()
		return nil
	case TokenObjectStart:
		p.handler.OnObjectStart()
		p.pushContainer(ContainerObject)
		p.state = StateObjectKey
		return nil
	case TokenArrayStart:
		p.handler.OnArrayStart()
		p.pushContainer(ContainerArray)
		p.state = StateArrayValue
		return nil
	default:
		return p.errorExpected("value")
	}
}

// errorExpected creates a syntax error with context information
func (p *Parser) errorExpected(expected string) error {
	syntaxErr := getSyntaxError()
	syntaxErr.Msg = fmt.Sprintf("expected %s in state %s", expected, p.stateString())
	return syntaxErr
}

// stateString returns a human-readable representation of the current state
func (p *Parser) stateString() string {
	switch p.state {
	case StateStart:
		return "start"
	case StateValue:
		return "value"
	case StateObjectKey:
		return "object key"
	case StateObjectColon:
		return "colon after object key"
	case StateObjectValue:
		return "object value"
	case StateObjectComma:
		return "comma or } after object value"
	case StateArrayValue:
		return "array value"
	case StateArrayComma:
		return "comma or ] after array value"
	case StateString:
		return "string"
	case StateNumber:
		return "number"
	case StateLiteral:
		return "literal"
	case StateComplete:
		return "complete"
	case StateError:
		return "error"
	default:
		return fmt.Sprintf("unknown state (%d)", p.state)
	}
}

// ExtractNumber extracts a number value from JSON at the specified path
func ExtractNumber(data []byte, path ...string) (float64, bool) {
	// First use Extract to get the raw JSON at the path
	value, ok := Extract(data, path...)
	if !ok {
		return 0, false
	}

	// Create a lexer to tokenize the value
	lexer := NewLexer(value)
	defer lexer.Close()

	// Get the token
	token := lexer.NextToken()

	// Check if it's a number
	if token.Type != TokenNumber {
		return 0, false
	}

	// Parse the number
	n, err := strconv.ParseFloat(GetString(token.Value), 64)
	if err != nil {
		return 0, false
	}

	return n, true
}

// ExtractBool extracts a boolean value from JSON at the specified path
func ExtractBool(data []byte, path ...string) (bool, bool) {
	// First use Extract to get the raw JSON at the path
	value, ok := Extract(data, path...)
	if !ok {
		return false, false
	}

	// Create a lexer to tokenize the value
	lexer := NewLexer(value)
	defer lexer.Close()

	// Get the token
	token := lexer.NextToken()

	// Check if it's a boolean
	switch token.Type {
	case TokenTrue:
		return true, true
	case TokenFalse:
		return false, true
	default:
		return false, false
	}
}

// ExtractString extracts a string value from JSON at the specified path
func ExtractString(data []byte, path ...string) (string, bool) {
	// First use Extract to get the raw JSON at the path
	value, ok := Extract(data, path...)
	if !ok {
		return "", false
	}

	// Create a lexer to tokenize the value
	lexer := NewLexer(value)
	defer lexer.Close()

	// Get the token
	token := lexer.NextToken()

	// Check if it's a string
	if token.Type != TokenString {
		return "", false
	}

	return GetString(token.Value), true
}

func countEscapeChars(s string) int {
	count := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == '"' || c == '\\' {
			count++
		}
	}
	return count
}
