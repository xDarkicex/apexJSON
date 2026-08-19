package apexJSON

import (
	"fmt"
	"strconv"
	"time"
)

func NewExtractHandler(path []string) *ExtractHandler {
	buffer := getBuffer()
	stateChan := make(chan PathUpdate, 32) // Buffered channel

	h := &ExtractHandler{
		pathTracker:  NewPathTracker(path), // Use proper constructor
		buffer:       buffer,
		contextStack: make([]ParserContext, 0, 8),
		result:       make([]byte, 0, 256),
	}

	// Initialize path tracker with notification channel
	h.pathTracker.SetNotifyChannel(stateChan)

	// Start state sync handler
	go h.stateSyncHandler(stateChan)

	return h
}

// Extract retrieves a value from JSON based on a path
func Extract(data []byte, path ...string) ([]byte, bool) {
	if debug {
		fmt.Println("\n==== EXTRACT FUNCTION START ====")
		fmt.Printf("Target path: %v\n", path)
	}

	// Create handler with pooled resources
	handler := NewExtractHandler(path)
	defer handler.Close()

	// Initialize path tracker with target path
	handler.pathTracker.Reset(path)

	if debug {
		fmt.Printf("Initial handler state: matched=%v, capturing=%v, pathStack=%v\n",
			handler.pathTracker.matched, handler.capturing, handler.pathTracker.stack)
	}

	// Create parser with the handler
	parser := NewParser(handler)

	if debug {
		fmt.Println("\n-- PARSING --")
	}
	if err := parser.Parse(data); err != nil {
		fmt.Printf("ERROR: Parse failed: %v\n", err)
		if len(handler.errors) > 0 {
			fmt.Println("Additional errors:")
			for _, e := range handler.errors {
				fmt.Println(e)
			}
		}
		return nil, false
	}

	result, found := handler.Result()

	// Post-processing cleanup
	if found && len(result) > 0 {
		// Handle array/object bracket balancing
		result = normalizeBrackets(result)
	}

	if debug {
		fmt.Printf("\nFinal result: found=%v, result=%s\n", found, string(result))
		fmt.Println("==== EXTRACT FUNCTION END ====")
	}

	return result, found
}

// Helper function to fix bracket mismatches
func normalizeBrackets(input []byte) []byte {
	if len(input) == 0 {
		return input
	}

	openChar := input[0]
	var closeChar byte
	if openChar == '[' {
		closeChar = ']'
	} else if openChar == '{' {
		closeChar = '}'
	} else {
		return input
	}

	depth := 0
	lastValid := 0
	for i, c := range input {
		switch c {
		case openChar:
			depth++
		case closeChar:
			depth--
		}

		if depth < 0 {
			break
		}
		lastValid = i
	}

	// Add missing closing brackets if needed
	if depth > 0 {
		needed := depth
		newLen := lastValid + 1 + needed
		if newLen > cap(input) {
			newBuf := make([]byte, newLen)
			copy(newBuf, input[:lastValid+1])
			input = newBuf
		} else {
			input = input[:newLen]
		}
		for i := 0; i < needed; i++ {
			input[lastValid+1+i] = closeChar
		}
	}

	return input[:lastValid+1]
}

func (h *ExtractHandler) stateSyncHandler(updates <-chan PathUpdate) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("State sync recovered: %v\n", r)
		}
	}()

	for update := range updates {
		switch update.Operation {
		case PushOp:
			h.handlePushUpdate(update)
		case PopOp:
			h.handlePopUpdate(update)
		case UpdateIndexOp:
			h.handleIndexUpdate(update)
		}
		h.validateState() // Validate after each operation
	}
}

func (h *ExtractHandler) handleIndexUpdate(update PathUpdate) {
	if update.Segment == nil || update.Depth >= len(h.contextStack) {
		h.errors = append(h.errors, "invalid index update")
		return
	}

	// Update both path tracker and context
	ctx := &h.contextStack[update.Depth]
	if ctx.containerType != ContainerArray {
		h.errors = append(h.errors,
			"index update for non-array context")
		return
	}

	// Synchronize indices
	ctx.elementCount = update.ArrayIndex + 1
	h.pathTracker.stack[update.Depth].CurrentIndex = update.ArrayIndex

	if debug {
		fmt.Printf("Array index updated@%d: %d (target=%d)\n",
			update.Depth, update.ArrayIndex,
			h.pathTracker.stack[update.Depth].TargetIndex)
	}

	// Automatic capture management
	if h.pathTracker.ShouldCapture() && !h.capturing {
		h.buffer.Reset()
		h.capturing = true
		if h.pathTracker.InArrayContext() {
			h.buffer.WriteByte('[')
		}
	}
}

func (h *ExtractHandler) handlePushUpdate(update PathUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.buffer == nil {
		h.errors = append(h.errors, "buffer closed during push update")
		return
	}
	if update.Segment == nil {
		h.errors = append(h.errors, "received nil segment in push update")
		return
	}

	ctx := ParserContext{
		containerType: ContainerObject,
		pathSegment:   SafeString(update.Segment.Name),
		bufferOffset:  h.buffer.off,
		elementCount:  0,
	}

	if update.Segment.IsArray {
		ctx.containerType = ContainerArray
		ctx.elementCount = update.Segment.CurrentIndex + 1
	}

	h.contextStack = append(h.contextStack, ctx)

	if debug {
		fmt.Printf("Context pushed@%d: %+v\n",
			len(h.contextStack)-1, ctx)
	}
}

func (h *ExtractHandler) handlePopUpdate(update PathUpdate) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.contextStack) == 0 {
		h.errors = append(h.errors, "pop update on empty context stack")
		return
	}

	// Validate before popping
	ctx := h.contextStack[len(h.contextStack)-1]
	if update.Segment != nil {
		if ctx.containerType == ContainerArray != update.Segment.IsArray {
			h.errors = append(h.errors, fmt.Sprintf(
				"container type mismatch during pop: %v vs %v",
				ctx.containerType, update.Segment.IsArray))
		}
	}

	h.contextStack = h.contextStack[:len(h.contextStack)-1]

	if debug {
		fmt.Printf("Context popped@%d\n", len(h.contextStack))
	}
}

// OnComma handles commas between array elements or object properties
func (h *ExtractHandler) OnComma() {
	if h.capturing {
		h.buffer.WriteByte(',')
	}
}

// InitializeMatching checks if we should match the empty path
func (h *ExtractHandler) InitializeMatching() {
	if len(h.pathTracker.targetPath) == 0 {
		h.pathTracker.matched = true
		h.capturing = true
		h.buffer.Reset()
		return
	}

	// Handle root array capture case
	if len(h.pathTracker.targetPath) >= 1 {
		// Check if first path segment is array index
		if idx, err := strconv.Atoi(h.pathTracker.targetPath[0]); err == nil {
			h.pathTracker.PushArray(idx)
			// Only set capture ready if index matches immediately
			if idx == 0 {
				h.pathTracker.SetCaptureReady()
			}
		} else {
			// Handle object path initialization
			h.pathTracker.PushObject(h.pathTracker.targetPath[0])
		}
	}

	// Initialize nested paths
	for depth := 1; depth < len(h.pathTracker.targetPath); depth++ {
		if idx, err := strconv.Atoi(h.pathTracker.targetPath[depth]); err == nil {
			h.pathTracker.PushArray(idx)
		} else {
			h.pathTracker.PushObject(h.pathTracker.targetPath[depth])
		}
	}
}

func (h *ExtractHandler) Result() ([]byte, bool) {
	return h.result, h.found
}

func (h *ExtractHandler) OnBool(value bool) {
	// Use PathTracker as the single source of truth
	shouldCapture := h.pathTracker.ShouldCapture()
	inArray := h.pathTracker.InArrayContext()

	// Capture initialization logic
	if shouldCapture && !h.capturing {
		requiredLen := 4 // "true" or "false"
		if h.buffer.off+requiredLen > cap(h.buffer.buf) {
			h.errors = append(h.errors, "buffer overflow in bool capture")
			return
		}

		h.buffer.Reset()
		h.capturing = true
		h.pathTracker.BeginCapture()

		str := "false"
		if value {
			str = "true"
		}

		if _, err := h.buffer.WriteString(str); err != nil {
			h.errors = append(h.errors, "bool write failed: "+err.Error())
			h.pathTracker.CancelCapture()
			return
		}

		h.found = true
		h.result = make([]byte, h.buffer.off)
		copy(h.result, h.buffer.buf[:h.buffer.off])
		return
	}

	if h.capturing {
		// Handle comma insertion based on container context
		if h.buffer.off > 0 {
			lastByte := h.buffer.buf[h.buffer.off-1]
			if inArray {
				if lastByte != '[' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "array comma write failed: "+err.Error())
						return
					}
				}
			} else {
				if lastByte != '{' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "object comma write failed: "+err.Error())
						return
					}
				}
			}
		}

		// Write value with buffer safety
		str := "false"
		if value {
			str = "true"
		}

		if h.buffer.off+len(str) > cap(h.buffer.buf) {
			h.errors = append(h.errors,
				fmt.Sprintf("bool overflow: need %d, have %d",
					len(str), cap(h.buffer.buf)-h.buffer.off))
			return
		}

		if _, err := h.buffer.WriteString(str); err != nil {
			h.errors = append(h.errors, "nested bool write failed: "+err.Error())
		}
	}
}

func (h *ExtractHandler) OnNull() {
	// Use PathTracker as the single source of truth
	shouldCapture := h.pathTracker.ShouldCapture()
	inArray := h.pathTracker.InArrayContext()

	// Capture initialization logic
	if shouldCapture && !h.capturing {
		requiredLen := 4 // "null"
		if h.buffer.off+requiredLen > cap(h.buffer.buf) {
			h.errors = append(h.errors, "buffer overflow in null capture")
			return
		}

		h.buffer.Reset()
		h.capturing = true
		h.pathTracker.BeginCapture()

		if _, err := h.buffer.WriteString("null"); err != nil {
			h.errors = append(h.errors, "null write failed: "+err.Error())
			h.pathTracker.CancelCapture()
			return
		}

		h.found = true
		h.result = make([]byte, h.buffer.off)
		copy(h.result, h.buffer.buf[:h.buffer.off])
		return // Early return after capturing the value
	}

	if h.capturing {
		// Handle nested values with comma insertion
		if h.buffer.off > 0 {
			lastByte := h.buffer.buf[h.buffer.off-1]
			if inArray {
				// In array context
				if lastByte != '[' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "array comma write failed: "+err.Error())
						return
					}
				}
			} else {
				// In object context
				if lastByte != '{' && lastByte != ':' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "object comma write failed: "+err.Error())
						return
					}
				}
			}
		}

		// Check buffer capacity before writing
		if h.buffer.off+4 > cap(h.buffer.buf) {
			h.errors = append(h.errors,
				fmt.Sprintf("null overflow: need %d, have %d",
					4, cap(h.buffer.buf)-h.buffer.off))
			return
		}

		if _, err := h.buffer.WriteString("null"); err != nil {
			h.errors = append(h.errors, "nested null write failed: "+err.Error())
		}
	}
}

// OnArrayStart handles the start of a JSON array with support for numeric indices
func (h *ExtractHandler) OnArrayStart() {
	// Get target index from path
	targetIndex := -1
	currentDepth := h.PathDepth()

	if currentDepth < len(h.pathTracker.targetPath) {
		// Directly parse from target path
		if idx, err := strconv.Atoi(h.pathTracker.targetPath[currentDepth]); err == nil {
			targetIndex = idx
		}
	}

	h.pathTracker.PushArray(targetIndex) // Pass the index

	h.contextStack = append(h.contextStack, ParserContext{
		containerType: ContainerArray,
		bufferOffset:  h.buffer.off,
	})

	if debug {
		fmt.Printf("Array tracking started at depth %d (targetIdx=%d)\n",
			h.PathDepth(), targetIndex)
	}

	// Start capturing if matched
	if h.pathTracker.matched && h.PathDepth() == len(h.pathTracker.targetPath) {
		h.capturing = true
		if h.buffer.off+1 <= cap(h.buffer.buf) {
			h.buffer.WriteByte('[')
		}
	}
}

// OnArrayEnd handles the end of a JSON array with proper path tracking cleanup
func (h *ExtractHandler) OnArrayEnd() {
	// Validate state using path tracker
	h.validateState()

	// Get and validate context
	context := h.popContext()
	if context.containerType != ContainerArray {
		h.errors = append(h.errors, fmt.Sprintf(
			"array end mismatch: expected array context, got %v",
			context.containerType))
	}

	if h.capturing {
		// Ensure buffer capacity before writing
		if h.buffer.off >= cap(h.buffer.buf) {
			h.errors = append(h.errors, "buffer full at array end")
			return
		}
		h.buffer.WriteByte(']')

		// Finalize capture if we've matched the full path
		if h.pathTracker.ShouldCapture() {
			h.found = true
			h.result = make([]byte, h.buffer.off)
			copy(h.result, h.buffer.buf[:h.buffer.off])
		}
	}

	// Update path tracking
	if h.pathTracker.PathDepth() > 0 {
		// Check if top segment is an array before popping
		if h.pathTracker.InArrayContext() {
			// Pop array segment from path tracker
			h.pathTracker.PopSegment()

			fmt.Printf("Array closed at depth %d, remaining path: %v\n",
				h.pathTracker.PathDepth(), h.pathTracker.stack)
		}
	}

	// Final validation
	if h.pathTracker.PathDepth() != h.ParsingDepth() {
		h.errors = append(h.errors, fmt.Sprintf(
			"depth mismatch after array end: path=%d vs context=%d",
			h.pathTracker.PathDepth(), h.ParsingDepth()))
	}
}

// OnArrayElement handles notification of array element processing
func (h *ExtractHandler) OnArrayElement(index int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Validate critical components
	if h.pathTracker == nil || h.buffer == nil {
		h.errors = append(h.errors, "OnArrayElement: nil path tracker or buffer")
		return
	}

	if debug {
		fmt.Printf("OnArrayElement: index=%d, pathDepth=%d, capturing=%v\n",
			index, h.pathTracker.PathDepth(), h.capturing)
	}

	// Update array index tracking
	if h.pathTracker.InArrayContext() {
		// Store current state before modification
		previousIndex := h.pathTracker.top().CurrentIndex
		expectedIndex := h.pathTracker.top().TargetIndex

		h.pathTracker.IncrementIndex()
		h.pathTracker.updateMatchState()

		if debug {
			currentSeg := h.pathTracker.top()
			if currentSeg != nil {
				fmt.Printf("Array index update: %d->%d (target=%d) at depth=%d\n",
					previousIndex, currentSeg.CurrentIndex, expectedIndex,
					h.pathTracker.PathDepth()-1)
			}
		}

		// Start capturing if we just matched the target index
		if h.pathTracker.ShouldCapture() && !h.capturing {
			if debug {
				fmt.Printf("Beginning capture at array index %d\n", index)
			}

			h.capturing = true
			h.buffer.Reset()

			// Write opening bracket if in array context
			if h.pathTracker.InArrayContext() && h.buffer.off+1 <= cap(h.buffer.buf) {
				h.buffer.WriteByte('[')
			}
		}
	}

	// Handle buffer operations if capturing
	if h.capturing {
		// Ensure buffer capacity before writing
		if h.buffer.off >= cap(h.buffer.buf) {
			h.errors = append(h.errors,
				fmt.Sprintf("buffer full at array index %d", index))
			return
		}

		// Add comma if needed (after first element)
		if index > 0 {
			if h.buffer.off > 0 {
				lastByte := h.buffer.buf[h.buffer.off-1]

				// Only add comma if not after opening bracket
				if lastByte != '[' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors,
							fmt.Sprintf("comma write failed at index %d: %v", index, err))
					}
				}
			}
		}

		// Final validation before index update
		if h.pathTracker.PathDepth() != len(h.contextStack) {
			h.errors = append(h.errors,
				fmt.Sprintf("depth mismatch during array element %d", index))
		}
	}
}

func (h *ExtractHandler) OnObjectStart() {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Push new context with object type
	h.contextStack = append(h.contextStack, ParserContext{
		containerType: ContainerObject,
		pathSegment:   "", // Will be set in OnObjectKey
		bufferOffset:  h.buffer.off,
	})

	// Start capturing logic
	if h.pathTracker.ShouldCapture() && !h.capturing {
		// Begin capturing at exact match point
		h.buffer.Reset()
		h.capturing = true
		h.pathTracker.BeginCapture()

		// Check buffer capacity before writing
		if h.buffer.off+1 > cap(h.buffer.buf) {
			h.errors = append(h.errors, "buffer overflow in object start capture")
			h.pathTracker.CancelCapture()
			return
		}

		h.buffer.WriteByte('{')
	} else if h.capturing {
		// Nested object in captured structure
		if h.buffer.off+1 <= cap(h.buffer.buf) {
			// Add comma if needed for object properties
			if h.buffer.off > 0 {
				lastByte := h.buffer.buf[h.buffer.off-1]
				if lastByte != '{' && lastByte != '[' && lastByte != ':' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "comma write failed before nested object: "+err.Error())
						return
					}
				}
			}

			h.buffer.WriteByte('{')
		} else {
			h.errors = append(h.errors, "buffer overflow in nested object start")
			return
		}
	}

	if debug {
		fmt.Printf("Object started at depth %d\n", h.pathTracker.PathDepth())
	}
}

func (h *ExtractHandler) OnObjectEnd() {
	// Validate context stack
	if h.ParsingDepth() == 0 {
		h.errors = append(h.errors, "OnObjectEnd called with empty context stack")
		return
	}

	context := h.popContext()

	if h.capturing {
		// Only process object closures, skip for primitive values
		if context.containerType == ContainerObject {
			// Buffer safety check
			if h.buffer.off+1 > cap(h.buffer.buf) {
				h.errors = append(h.errors, "buffer full at object end")
				return
			}

			// Remove trailing comma if present
			if h.buffer.off > 0 && h.buffer.buf[h.buffer.off-1] == ',' {
				h.buffer.off--
			}

			// Write closing brace only for object contexts
			h.buffer.WriteByte('}')
		}

		// Finalize capture when path matches and we're at target depth
		if h.pathTracker.matched && h.PathDepth() == len(h.pathTracker.targetPath) {
			h.found = true
			h.result = make([]byte, h.buffer.off)
			copy(h.result, h.buffer.buf[:h.buffer.off])

			// Immediately stop capturing after finalization
			h.capturing = false
		}
	}

	// Path tracking cleanup
	if h.PathDepth() > 0 {
		lastSeg := h.pathTracker.stack[h.PathDepth()-1]
		if !lastSeg.IsArray && lastSeg.Name == context.pathSegment {
			h.pathTracker.PopSegment()
		}
	}

	// Validate path-context synchronization
	if h.pathTracker.PathDepth() != h.ParsingDepth() {
		h.errors = append(h.errors, fmt.Sprintf(
			"path-context depth mismatch after OnObjectEnd: %d vs %d",
			h.pathTracker.PathDepth(), h.ParsingDepth()))

		// Reset to recover from invalid state
		h.pathTracker.Reset(h.pathTracker.targetPath)
	}

	// Buffer integrity check
	if h.buffer.off < context.bufferOffset {
		h.errors = append(h.errors, fmt.Sprintf(
			"buffer offset regression: %d < %d",
			h.buffer.off, context.bufferOffset))
	}

	// Final cleanup for nested structures
	if h.capturing {
		// Remove trailing comma in array/object contexts
		if h.buffer.off > 0 {
			lastByte := h.buffer.buf[h.buffer.off-1]
			if lastByte == ',' {
				h.buffer.off--
			}
		}
	}
}

// OnObjectKey handles object keys with special handling for paths with array indices
func (h *ExtractHandler) OnObjectKey(key string) {
	safeKey := SafeString(key)
	h.currentKey = safeKey

	// Update path tracking
	h.pathTracker.PushObject(safeKey)

	// Buffer operations only when capturing
	if h.capturing && h.pathTracker.PathDepth() <= len(h.pathTracker.targetPath) {
		// Check buffer capacity first
		requiredSpace := len(safeKey) + 3 // Quotes + colon
		if h.buffer.off+requiredSpace > cap(h.buffer.buf) {
			h.errors = append(h.errors,
				fmt.Sprintf("buffer overflow for key '%s' (need %d, have %d)",
					safeKey, requiredSpace, cap(h.buffer.buf)-h.buffer.off))
			return
		}

		// Add comma only if not first property
		if h.buffer.off > 0 {
			lastByte := h.buffer.buf[h.buffer.off-1]
			if lastByte != '{' && lastByte != '[' {
				if err := h.buffer.WriteByte(','); err != nil {
					h.errors = append(h.errors,
						fmt.Sprintf("comma write failed for key '%s': %v", safeKey, err))
					return
				}
			}
		}

		// Write key with proper escaping
		h.buffer.WriteByte('"')
		writeEscapedStringString(h.buffer, safeKey)
		h.buffer.WriteByte('"')
		h.buffer.WriteByte(':')

		// Update context stack
		if len(h.contextStack) > 0 {
			h.contextStack[len(h.contextStack)-1].bufferOffset = h.buffer.off
			h.contextStack[len(h.contextStack)-1].pathSegment = safeKey
		}
	}

	// Update match state after key processing
	h.pathTracker.updateMatchState()

	// Start capturing if we just matched the target path
	if h.pathTracker.ShouldCapture() && !h.capturing {
		h.capturing = true
		h.buffer.Reset()

		// Write opening brace if capturing object structure
		if h.buffer.off+1 <= cap(h.buffer.buf) {
			h.buffer.WriteByte('{')
		}
	}
}

func (h *ExtractHandler) OnString(value string) {
	// Use PathTracker as the single source of truth
	shouldCapture := h.pathTracker.ShouldCapture()
	inArray := h.pathTracker.InArrayContext()
	safeValue := SafeString(value)
	requiredLength := len(safeValue) + 2 // Quotes

	// Capture initialization logic
	if shouldCapture && !h.capturing {
		if h.buffer.off+requiredLength > cap(h.buffer.buf) {
			h.errors = append(h.errors, "buffer overflow in string capture")
			return
		}

		// Reset buffer to discard any previously written key
		h.buffer.Reset()
		h.capturing = true
		h.pathTracker.BeginCapture()

		// Write only the string value with proper escaping
		h.buffer.WriteByte('"')
		writeEscapedStringString(h.buffer, safeValue)
		h.buffer.WriteByte('"')

		// Finalize result immediately for primitive values
		h.found = true
		h.result = make([]byte, h.buffer.off)
		copy(h.result, h.buffer.buf[:h.buffer.off])
		fmt.Printf("Captured string at path %v: %s\n",
			h.pathTracker.targetPath, safeValue)

		// Stop capturing to prevent nested writes
		h.capturing = false
		h.pathTracker.CancelCapture()
	} else if h.capturing {
		// Handle nested values with buffer safety
		if h.buffer.off+requiredLength > cap(h.buffer.buf) {
			h.errors = append(h.errors, "buffer overflow in nested string")
			return
		}

		// Add comma if needed based on container context
		if h.buffer.off > 0 {
			lastByte := h.buffer.buf[h.buffer.off-1]
			if inArray {
				// Array context
				if lastByte != '[' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "array comma write failed: "+err.Error())
						return
					}
				}
			} else {
				// Object context
				if lastByte != '{' && lastByte != '[' && lastByte != ':' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "object comma write failed: "+err.Error())
						return
					}
				}
			}
		}

		h.buffer.WriteByte('"')
		writeEscapedStringString(h.buffer, safeValue)
		h.buffer.WriteByte('"')

		if debug {
			fmt.Printf("Appended string to buffer: %s\n", safeValue)
		}
	}

	// Sync with context stack
	if len(h.contextStack) > 0 {
		h.contextStack[len(h.contextStack)-1].bufferOffset = h.buffer.off
	}
}

func (h *ExtractHandler) validateState() {
	// Depth validation
	pathDepth := h.pathTracker.PathDepth()
	ctxDepth := len(h.contextStack)

	// Synchronize depths
	for ctxDepth > pathDepth {
		h.contextStack = h.contextStack[:ctxDepth-1]
		ctxDepth--
		h.errors = append(h.errors, "trimmed excess context entries")
	}

	for i := ctxDepth; i < pathDepth; i++ {
		if i >= len(h.pathTracker.stack) {
			h.errors = append(h.errors,
				"path tracker stack underflow during validation")
			break
		}
		h.contextStack = append(h.contextStack, ParserContext{
			containerType: ContainerObject,
			pathSegment:   h.pathTracker.stack[i].Name,
		})
	}

	// Segment-by-segment validation
	for i := 0; i < pathDepth; i++ {
		if i >= len(h.contextStack) || i >= len(h.pathTracker.stack) {
			continue
		}

		ctx := h.contextStack[i]
		seg := h.pathTracker.stack[i]

		// Type validation
		if ctx.containerType == ContainerArray != seg.IsArray {
			h.errors = append(h.errors, fmt.Sprintf(
				"type mismatch@%d: ctx=%v vs path=%v",
				i, ctx.containerType, seg.IsArray))
		}

		// Name validation for objects
		if !seg.IsArray && ctx.pathSegment != seg.Name {
			h.errors = append(h.errors, fmt.Sprintf(
				"name mismatch@%d: '%s' vs '%s'",
				i, ctx.pathSegment, seg.Name))
		}

		// Array index validation
		if seg.IsArray {
			expected := seg.CurrentIndex + 1
			if ctx.elementCount != expected {
				h.errors = append(h.errors, fmt.Sprintf(
					"array index@%d: ctx=%d vs actual=%d",
					i, ctx.elementCount, expected))
			}
		}
	}

	// Recovery mechanism for critical errors
	if len(h.errors) > 3 { // Too many errors indicates fundamental mismatch
		h.pathTracker.Reset(h.pathTracker.targetPath)
		h.contextStack = h.contextStack[:0]
		h.capturing = false
	}
}

// popContext pops and returns the top context from the stack with enhanced validation
func (h *ExtractHandler) popContext() ParserContext {
	ctxDepth := h.ParsingDepth()
	if ctxDepth == 0 {
		return ParserContext{}
	}

	// Get and remove the context
	context := h.contextStack[ctxDepth-1]
	h.contextStack = h.contextStack[:ctxDepth-1]

	// Validate container type using path tracker
	if context.containerType != ContainerObject && context.containerType != ContainerArray {
		h.errors = append(h.errors, fmt.Sprintf(
			"invalid container type %d at depth %d",
			context.containerType, ctxDepth-1))
	}

	// Object validation
	if context.containerType == ContainerObject {
		// Check path segment consistency
		if h.PathDepth() > 0 {
			currentSeg := h.pathTracker.stack[h.PathDepth()-1]
			if !currentSeg.IsArray && currentSeg.Name != context.pathSegment {
				h.errors = append(h.errors, fmt.Sprintf(
					"path segment mismatch: context=%s vs path=%s",
					context.pathSegment, currentSeg.Name))
			}
		}

		// Buffer integrity check
		if h.capturing && h.buffer.off < context.bufferOffset {
			h.errors = append(h.errors, "buffer offset regression detected")
		}
	}

	// Array validation
	if context.containerType == ContainerArray && h.PathDepth() > 0 {
		currentSeg := h.pathTracker.stack[h.PathDepth()-1]
		if currentSeg.IsArray && currentSeg.CurrentIndex+1 != context.elementCount {
			h.errors = append(h.errors, fmt.Sprintf(
				"array count mismatch: context=%d vs actual=%d",
				context.elementCount, currentSeg.CurrentIndex+1))
		}
	}

	return context
}

func (h *ExtractHandler) OnNumber(value float64) {
	// Safe number conversion with error tracking
	numStr := strconv.FormatFloat(value, 'g', -1, 64)
	if len(numStr) == 0 {
		h.errors = append(h.errors, "empty number string conversion")
		return
	}

	// Use PathTracker as the single source of truth
	shouldCapture := h.pathTracker.ShouldCapture()
	inArray := h.pathTracker.InArrayContext()

	// Capture initialization logic
	if shouldCapture && !h.capturing {
		// Write with buffer safety check
		if h.buffer.off+len(numStr) > cap(h.buffer.buf) {
			h.errors = append(h.errors,
				fmt.Sprintf("buffer overflow in OnNumber (need %d, have %d)",
					len(numStr), cap(h.buffer.buf)-h.buffer.off))
			return
		}

		h.buffer.Reset()
		h.capturing = true
		h.pathTracker.BeginCapture()

		n, err := h.buffer.WriteString(numStr)
		if err != nil || n != len(numStr) {
			h.errors = append(h.errors,
				fmt.Sprintf("number write failed: %v (wrote %d/%d bytes)",
					err, n, len(numStr)))
			h.pathTracker.CancelCapture()
			return
		}

		// Finalize result with copy safety
		if h.buffer.off > 0 {
			h.result = make([]byte, h.buffer.off)
			if copied := copy(h.result, h.buffer.buf[:h.buffer.off]); copied != h.buffer.off {
				h.errors = append(h.errors,
					fmt.Sprintf("partial result copy: %d/%d bytes", copied, h.buffer.off))
			}
		}
		h.found = true
		if debug {
			fmt.Printf("Captured number at path %v: %s\n",
				h.pathTracker.targetPath, numStr)
		}
		return // Early return after capturing
	}

	if h.capturing {
		// Handle nested values with comma insertion based on context
		if h.buffer.off > 0 {
			lastByte := h.buffer.buf[h.buffer.off-1]
			if inArray {
				// Array context
				if lastByte != '[' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "array comma write failed: "+err.Error())
						return
					}
				}
			} else {
				// Object context
				if lastByte != '{' && lastByte != '[' && lastByte != ':' {
					if err := h.buffer.WriteByte(','); err != nil {
						h.errors = append(h.errors, "object comma write failed: "+err.Error())
						return
					}
				}
			}
		}

		// Check number write capacity
		if h.buffer.off+len(numStr) > cap(h.buffer.buf) {
			h.errors = append(h.errors,
				fmt.Sprintf("buffer overflow in nested number (need %d, have %d)",
					len(numStr), cap(h.buffer.buf)-h.buffer.off))
			return
		}

		n, err := h.buffer.WriteString(numStr)
		if err != nil || n != len(numStr) {
			h.errors = append(h.errors,
				fmt.Sprintf("nested number write failed: %v (wrote %d/%d bytes)",
					err, n, len(numStr)))
		}
		if debug {
			fmt.Printf("Appended number to buffer: %s\n", numStr)
		}
	}
}

// Close releases resources back to pools
func (h *ExtractHandler) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.buffer != nil {
		putBuffer(h.buffer)
		h.buffer = nil
	}

	if h.pathTracker != nil {
		// Ensure all updates are processed before closing
		time.Sleep(50 * time.Millisecond)
		close(h.pathTracker.notifyChan)
		h.pathTracker = nil
	}
}

// Get current depth from PathTracker
func (h *ExtractHandler) PathDepth() int {
	return h.pathTracker.PathDepth()
}

// Get current parsing depth from context stack
func (h *ExtractHandler) ParsingDepth() int {
	return len(h.contextStack)
}

func (h *ExtractHandler) InArrayContext() bool {
	return h.pathTracker.InArrayIndexMode()
}

func (h *ExtractHandler) InObjectContext() bool {
	return len(h.pathTracker.stack) > 0 &&
		!h.pathTracker.stack[len(h.pathTracker.stack)-1].IsArray
}

func (h *ExtractHandler) ShouldCapture() bool {
	return h.pathTracker.matched &&
		h.PathDepth() == len(h.pathTracker.targetPath)
}
