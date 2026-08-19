package apexJSON

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
)

func (e *SyntaxError) Error() string {
	b := getBuilder()
	defer putBuilder(b)
	b.WriteString("json syntax error at offset ")
	b.WriteString(strconv.FormatInt(e.Offset, 10))
	b.WriteString(": ")
	b.WriteString(e.Msg)

	return b.String()
}

func (e *UnmarshalTypeError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("json: cannot unmarshal %s into Go struct field %s of type %s",
			e.Value, e.Field, e.Type.String())
	}
	return fmt.Sprintf("json: cannot unmarshal %s into Go value of type %s", e.Value, e.Type.String())
}

// ### Core Functions ###

func Marshal(v interface{}) ([]byte, error) {
	buf := getBuffer()
	defer putBuffer(buf)

	// Create encoder
	enc := NewEncoder(buf)

	// Encode value
	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	// Create a copy of the buffer contents
	result := make([]byte, buf.off)
	copy(result, buf.buf[:buf.off])
	return result, nil
}
func Unmarshal(data []byte, v interface{}) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return &InvalidUnmarshalError{reflect.TypeOf(v)}
	}

	// Create unmarshal handler
	handler := NewUnmarshalHandler(rv.Elem(), nil)

	// Create parser with handler
	parser := NewParser(handler)

	// Parse the input
	return parser.Parse(data)
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		w:          w,
		buf:        getBufferSize(2048),
		escapeHTML: true,
	}
}

func NewDecoder(r io.Reader) *Decoder {
	fmt.Println("Creating new decoder")
	d := &Decoder{
		r:         r,
		buf:       make([]byte, 0, 4096),
		tokenBuf:  *getTokenBuf(),
		useNumber: false,
	}
	fmt.Printf("NewDecoder: buf len=%d, cap=%d\n", len(d.buf), cap(d.buf))
	d.readPos = 0
	return d
}

func (e *Encoder) Encode(v interface{}) error {
	e.buf.Reset()

	if err := marshalValue(reflect.ValueOf(v), e.buf); err != nil {
		return err
	}

	// Write the encoded value followed by a newline
	if _, err := e.w.Write(e.buf.Bytes()); err != nil {
		return err
	}
	if _, err := e.w.Write(jsonNewline); err != nil {
		return err
	}

	return nil
}

func (e *Encoder) SetEscapeHTML(on bool) {
	e.escapeHTML = on
}

// GetObject extracts a map from JSON at the specified path
func GetObject(data []byte, path ...string) (map[string]interface{}, bool) {
	// First use Extract to get the raw JSON at the path
	value, ok := Extract(data, path...)
	if !ok {
		return nil, false
	}

	// Create specialized object collector
	collector := NewObjectCollector()
	defer collector.Close()

	// Use our state machine parser
	parser := NewParser(collector)

	// Parse the extracted JSON
	if err := parser.Parse(value); err != nil {
		return nil, false
	}

	// Get results from collector
	return collector.Result()
}

// ### Extraction ###

// GetArray extracts an array from JSON at the specified path
func GetArray(data []byte, path ...string) ([]interface{}, bool) {
	// First use Extract to get the raw JSON at the path
	value, ok := Extract(data, path...)
	if !ok {
		return nil, false
	}

	// Create specialized array collector
	collector := NewArrayCollector()
	defer collector.Close()

	// Use our state machine parser
	parser := NewParser(collector)

	// Parse the extracted JSON
	if err := parser.Parse(value); err != nil {
		return nil, false
	}

	// Get results from collector
	return collector.Result()
}

func NewArrayCollector() *ArrayCollector {
	return &ArrayCollector{
		result:     getArraySlice(), // From arraySlicePool
		objStack:   make([]map[string]interface{}, 0, 8),
		arrayStack: make([][]interface{}, 0, 8),
	}
}

// Close releases resources back to pools
func (h *ArrayCollector) Close() {
	// Release any pooled resources
	for _, obj := range h.objStack {
		if obj != nil {
			putObjectMap(obj)
		}
	}

	for _, arr := range h.arrayStack {
		if arr != nil {
			putArraySlice(arr)
		}
	}
}

// Result returns the collected array
func (h *ArrayCollector) Result() ([]interface{}, bool) {
	if !h.inArray {
		putArraySlice(h.result)
		return nil, false
	}

	// Create a copy to return (can't return pooled slice directly)
	finalResult := make([]interface{}, len(h.result))
	copy(finalResult, h.result)

	// Return pooled slice
	putArraySlice(h.result)
	return finalResult, true
}

// Fix for ArrayCollector.OnArrayStart()
func (h *ArrayCollector) OnArrayStart() {
	h.depth++

	// If we haven't found the target array yet, this is it
	if !h.inArray {
		h.inArray = true
		return
	}

	// Create a new array for nesting
	arr := getArraySlice()
	h.arrayStack = append(h.arrayStack, arr)
}
func (h *ArrayCollector) OnArrayEnd() {
	// Root array completion
	if h.depth == 1 {
		h.depth--
		return
	}

	// Process nested array completion
	if len(h.arrayStack) > 0 {
		arr := h.arrayStack[len(h.arrayStack)-1]
		h.arrayStack = h.arrayStack[:len(h.arrayStack)-1]

		// Create copy of array values
		arrCopy := make([]interface{}, len(arr))
		copy(arrCopy, arr)

		// Add to parent container
		h.addValueToContainer(arrCopy)
		putArraySlice(arr)
	}

	h.depth--
}

// addValueToContainer adds a value to the current container (array or object)
func (h *ArrayCollector) addValueToContainer(value interface{}) {
	// Check if we are in the root array
	if h.inArray {
		h.result = append(h.result, value)
		return
	}

	// Handle nested objects
	if len(h.objStack) > 0 {
		currentObj := h.objStack[len(h.objStack)-1]
		currentObj[h.currentKey] = value
		return
	}

	// Handle nested arrays
	if len(h.arrayStack) > 0 {
		currentArray := h.arrayStack[len(h.arrayStack)-1]
		h.arrayStack[len(h.arrayStack)-1] = append(currentArray, value)
	}
}

// OnObjectStart handles the start of a JSON object
func (h *ArrayCollector) OnObjectStart() {
	h.depth++

	// Create a new object from pool
	obj := getObjectMap()
	h.objStack = append(h.objStack, obj)
}

// OnObjectEnd handles the end of a JSON object
func (h *ArrayCollector) OnObjectEnd() {
	if len(h.objStack) > 0 {
		// Get the completed object
		obj := h.objStack[len(h.objStack)-1]
		h.objStack = h.objStack[:len(h.objStack)-1]

		// Create a copy (can't return pooled map directly)
		objCopy := make(map[string]interface{}, len(obj))
		for k, v := range obj {
			objCopy[k] = v
		}

		// Add to parent container
		h.addValueToContainer(objCopy)

		// Return to pool
		putObjectMap(obj)
	}

	h.depth--
}

// OnObjectKey handles object keys
func (h *ArrayCollector) OnObjectKey(key string) {
	h.currentKey = key
}

// OnString handles string values
func (h *ArrayCollector) OnString(value string) {
	h.addValueToContainer(value)
}

// OnNumber handles numeric values
func (h *ArrayCollector) OnNumber(value float64) {
	h.addValueToContainer(value)
}

// OnBool handles boolean values
func (h *ArrayCollector) OnBool(value bool) {
	h.addValueToContainer(value)
}

// OnNull handles null values
func (h *ArrayCollector) OnNull() {
	h.addValueToContainer(nil)
}

func NewObjectCollector() *ObjectCollector {
	result := getObjectMap()
	// Verify map is empty
	if len(result) > 0 {
		// Map from pool wasn't reset, create a new one
		fmt.Printf("WARNING: Object map from pool not empty, creating new\n")
		result = make(map[string]interface{})
	}

	return &ObjectCollector{
		result:     result,
		objStack:   make([]map[string]interface{}, 0, 8),
		arrayStack: make([][]interface{}, 0, 8),
	}
}

// Close releases resources back to pools
func (h *ObjectCollector) Close() {
	// Release any pooled resources
	for _, obj := range h.objStack {
		if obj != nil {
			putObjectMap(obj)
		}
	}

	for _, arr := range h.arrayStack {
		if arr != nil {
			putArraySlice(arr)
		}
	}
}

// Result returns the collected object with deep copying
func (h *ObjectCollector) Result() (map[string]interface{}, bool) {
	if !h.inObject {
		putObjectMap(h.result)
		return nil, false
	}

	// Create a copy to return (can't return pooled map directly)
	finalResult := make(map[string]interface{}, len(h.result))

	fmt.Printf("ObjectCollector.Result: Creating result map with %d entries\n", len(h.result))

	// Log all keys to verify uniqueness
	fmt.Printf("ObjectCollector.Result: Keys in map: ")
	for k := range h.result {
		fmt.Printf("%q ", k)
	}
	fmt.Println()

	// Use a key isolation approach to prevent reference issues
	for k, v := range h.result {
		// Create a new string for the key to break any reference issues
		newKey := string([]byte(k))

		fmt.Printf("ObjectCollector.Result: Copying key=%q, value=%v\n", newKey, v)

		// Deep copy the value
		copiedValue := deepCopyValue(v)

		// Store with the new key
		finalResult[newKey] = copiedValue

		fmt.Printf("ObjectCollector.Result: After copy, finalResult[%q]=%v\n", newKey, finalResult[newKey])
	}

	// Verify final map has all entries
	fmt.Printf("ObjectCollector.Result: Final map has %d entries\n", len(finalResult))

	// Return pooled map to the pool AFTER we're done with it
	putObjectMap(h.result)
	return finalResult, true
}

// Fix for the nested object property access
func deepCopyValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case string:
		// Always create a new string from bytes to ensure memory isolation
		return string([]byte(val))
	case map[string]interface{}:
		newMap := make(map[string]interface{}, len(val))
		for k, v := range val {
			// Isolate both keys and values
			newKey := string([]byte(k))
			newMap[newKey] = deepCopyValue(v)
		}
		return newMap
	case []interface{}:
		newSlice := make([]interface{}, len(val))
		for i, v := range val {
			newSlice[i] = deepCopyValue(v)
		}
		return newSlice
	case Number:
		return val
	case float64:
		// Return float64 directly - this is the standard JSON number type
		return val
	case int:
		// Convert int to float64 for JSON consistency
		return float64(val)
	case int64:
		// Convert int64 to float64 for JSON consistency
		return float64(val)
	default:
		// String, bool, etc.
		return val
	}
}

func (h *ObjectCollector) OnComma() {
	fmt.Printf("ObjectCollector.OnComma: depth=%d\n", h.depth)
	// No special logic needed for object collectors
}

// OnObjectStart handles the start of a JSON object
func (h *ObjectCollector) OnObjectStart() {
	fmt.Printf("ObjectCollector.OnObjectStart: depth=%d, inObject=%v, objStack=%d\n",
		h.depth, h.inObject, len(h.objStack))

	h.depth++

	// If we haven't found the target object yet, this is it
	if !h.inObject {
		h.inObject = true
		fmt.Printf("ObjectCollector.OnObjectStart: Root object found, setting inObject=true\n")
		return
	}

	// Create a new object for nesting
	obj := getObjectMap()
	fmt.Printf("ObjectCollector.OnObjectStart: Creating nested object, stack size before=%d\n",
		len(h.objStack))
	h.objStack = append(h.objStack, obj)
	fmt.Printf("ObjectCollector.OnObjectStart: Stack size after=%d\n", len(h.objStack))

	fmt.Printf("ObjectCollector.OnObjectStart (after): depth=%d, inObject=%v, objStack=%d\n",
		h.depth, h.inObject, len(h.objStack))
}

// OnObjectEnd handles closing of object
func (h *ObjectCollector) OnObjectEnd() {
	fmt.Printf("ObjectCollector.OnObjectEnd: depth=%d, inObject=%v, objStack=%d\n",
		h.depth, h.inObject, len(h.objStack))

	// Root object completion
	if h.depth == 1 {
		fmt.Printf("ObjectCollector.OnObjectEnd: Root object complete\n")
		h.depth--
		return
	}

	// Process nested object completion
	if len(h.objStack) > 0 {
		obj := h.objStack[len(h.objStack)-1]
		h.objStack = h.objStack[:len(h.objStack)-1]

		// Create copy of object
		objCopy := make(map[string]interface{}, len(obj))
		for k, v := range obj {
			objCopy[k] = v
			fmt.Printf("ObjectCollector.OnObjectEnd: Copied key-value: %s=%v\n", k, v)
		}

		// Add to parent container
		fmt.Printf("ObjectCollector.OnObjectEnd: Adding completed object to parent, currentKey=%s\n",
			h.currentKey)
		h.addValueToContainer(objCopy)
		putObjectMap(obj)
	} else {
		fmt.Printf("ObjectCollector.OnObjectEnd: WARNING - No object on stack to close!\n")
	}

	h.depth--
	fmt.Printf("ObjectCollector.OnObjectEnd (after): depth=%d, inObject=%v, objStack=%d\n",
		h.depth, h.inObject, len(h.objStack))
}

// OnObjectKey handles object keys
func (h *ObjectCollector) OnObjectKey(key string) {
	// Create a new string copy to prevent reference issues
	h.currentKey = SafeString(key)
	fmt.Printf("ObjectCollector.OnObjectKey: key=%q, depth=%d, inObject=%v, objStack=%d\n",
		key, h.depth, h.inObject, len(h.objStack))
	fmt.Printf("ObjectCollector.OnObjectKey (after): currentKey=%q\n", h.currentKey)
}

// OnArrayStart handles start of JSON array
func (h *ObjectCollector) OnArrayStart() {
	fmt.Printf("ObjectCollector.OnArrayStart: depth=%d, currentKey=%q\n", h.depth, h.currentKey)

	// Create a new array for nesting
	arr := getArraySlice()
	fmt.Printf("ObjectCollector.OnArrayStart: Creating array, stack size before=%d\n",
		len(h.arrayStack))
	h.arrayStack = append(h.arrayStack, arr)
	fmt.Printf("ObjectCollector.OnArrayStart: Stack size after=%d\n", len(h.arrayStack))
}

func (h *ObjectCollector) OnArrayEnd() {
	fmt.Printf("ObjectCollector.OnArrayEnd: depth=%d, arrayStack=%d\n",
		h.depth, len(h.arrayStack))

	// Process array completion
	if len(h.arrayStack) > 0 {
		arr := h.arrayStack[len(h.arrayStack)-1]
		h.arrayStack = h.arrayStack[:len(h.arrayStack)-1]

		// Create copy of array values
		arrCopy := make([]interface{}, len(arr))
		copy(arrCopy, arr)

		// Add to parent container
		fmt.Printf("ObjectCollector.OnArrayEnd: Adding completed array to parent, currentKey=%s\n",
			h.currentKey)
		h.addValueToContainer(arrCopy)
		putArraySlice(arr)
	} else {
		fmt.Printf("ObjectCollector.OnArrayEnd: WARNING - No array on stack to close!\n")
	}

	fmt.Printf("ObjectCollector.OnArrayEnd (after): arrayStack=%d\n", len(h.arrayStack))
}

func (h *ObjectCollector) OnString(value string) {
	safeValue := SafeString(value)
	h.addValueToContainer(safeValue)
}

// OnNumber handles numeric values
func (h *ObjectCollector) OnNumber(value float64) {
	h.addValueToContainer(value)
}

// OnBool handles boolean values
func (h *ObjectCollector) OnBool(value bool) {
	fmt.Printf("ObjectCollector.OnBool: value=%v, currentKey=%q\n", value, h.currentKey)
	h.addValueToContainer(value)
}

// OnNull handles null values
func (h *ObjectCollector) OnNull() {
	fmt.Printf("ObjectCollector.OnNull: currentKey=%q\n", h.currentKey)
	h.addValueToContainer(nil)
}

// addValueToContainer adds a value to the current container (object or array)
func (h *ObjectCollector) addValueToContainer(value interface{}) {
	// Create a unique copy of the key to prevent reference sharing and corruption
	keyCopy := SafeString(h.currentKey)

	fmt.Printf("ObjectCollector.addValueToContainer: key=%q, value=%v, inObject=%v, objStack=%d, arrayStack=%d\n",
		keyCopy, value, h.inObject, len(h.objStack), len(h.arrayStack))

	// Add safety check for edge cases
	if !h.inObject && len(h.objStack) == 0 && len(h.arrayStack) == 0 {
		fmt.Printf("WARNING: Value not added to any container: %v\n", value)
		// Fix: If we're at the root level but inObject isn't set, set it now
		h.inObject = true
		fmt.Printf("ObjectCollector.addValueToContainer: Fixed - set inObject=true\n")
	}

	// Check if we are in the root object - now correctly checks depth
	if h.inObject && h.depth == 1 {
		fmt.Printf("ObjectCollector.addValueToContainer: Adding to root object: %s=%v\n",
			keyCopy, value)
		h.result[keyCopy] = value
		return
	}

	// Handle nested objects
	if len(h.objStack) > 0 {
		currentObj := h.objStack[len(h.objStack)-1]
		fmt.Printf("ObjectCollector.addValueToContainer: Adding to nested object: %s=%v\n",
			keyCopy, value)
		currentObj[keyCopy] = value
		return
	}

	// Handle nested arrays
	if len(h.arrayStack) > 0 {
		currentArray := h.arrayStack[len(h.arrayStack)-1]
		fmt.Printf("ObjectCollector.addValueToContainer: Adding to array: %v\n", value)
		h.arrayStack[len(h.arrayStack)-1] = append(currentArray, value)
		return
	}

	// Fix for when we're processing a top-level object but depth tracking had issues
	if h.inObject {
		fmt.Printf("ObjectCollector.addValueToContainer: Adding to root as fallback: %s=%v\n",
			keyCopy, value)
		h.result[keyCopy] = value
		return
	}

	fmt.Printf("ObjectCollector.addValueToContainer: WARNING - Value not added: %v\n", value)
}

func NewUnmarshalHandler(v reflect.Value, dec *Decoder) *UnmarshalHandler {
	return &UnmarshalHandler{
		target:     v,
		keyStack:   make([]string, 0, 8),
		valueStack: make([]reflect.Value, 0, 8),
		decoder:    dec,
	}
}

// OnObjectStart implements TokenHandler interface
func (h *UnmarshalHandler) OnObjectStart() {
	// Create or push appropriate container based on current value
	if len(h.valueStack) > 0 {
		current := h.valueStack[len(h.valueStack)-1]
		var newVal reflect.Value

		switch current.Kind() {
		case reflect.Map:
			if current.IsNil() {
				current.Set(reflect.MakeMap(current.Type()))
			}
			newVal = current
		case reflect.Interface:
			mapType := reflect.TypeOf(map[string]interface{}{})
			newMap := reflect.MakeMap(mapType)
			newVal = newMap
		case reflect.Struct:
			newVal = current
		default:
			newVal = reflect.Value{}
		}

		// Only push for nested values
		if current.Kind() != reflect.Struct {
			h.valueStack = append(h.valueStack, newVal)
		}
	} else {
		// Root level - use target
		h.valueStack = append(h.valueStack, h.target)
	}
}

// Implement other TokenHandler methods
func (h *UnmarshalHandler) OnObjectEnd() {
	if len(h.valueStack) > 1 {
		val := h.valueStack[len(h.valueStack)-1]
		h.valueStack = h.valueStack[:len(h.valueStack)-1]

		parent := h.valueStack[len(h.valueStack)-1]
		if parent.Kind() == reflect.Interface && !parent.IsNil() {
			parent.Set(val)
		}
	}
}

func (h *UnmarshalHandler) OnObjectKey(key string) {
	h.currentKey = key
}

func (h *UnmarshalHandler) OnArrayStart() {
	// Similar to OnObjectStart but for arrays/slices
	if len(h.valueStack) > 0 {
		current := h.valueStack[len(h.valueStack)-1]
		var newVal reflect.Value

		switch current.Kind() {
		case reflect.Slice:
			newVal = reflect.MakeSlice(current.Type(), 0, 8)
		case reflect.Array:
			newVal = current
		case reflect.Interface:
			sliceType := reflect.TypeOf([]interface{}{})
			newVal = reflect.MakeSlice(sliceType, 0, 8)
		default:
			newVal = reflect.Value{}
		}

		h.valueStack = append(h.valueStack, newVal)
	} else {
		h.valueStack = append(h.valueStack, h.target)
	}
}

func (h *UnmarshalHandler) OnArrayEnd() {
	if len(h.valueStack) > 1 {
		val := h.valueStack[len(h.valueStack)-1]
		h.valueStack = h.valueStack[:len(h.valueStack)-1]

		parent := h.valueStack[len(h.valueStack)-1]
		switch parent.Kind() {
		case reflect.Slice:
			parent.Set(val)
		case reflect.Interface:
			parent.Set(val)
		}
	}
}

func (h *UnmarshalHandler) OnString(value string) {
	h.setValue(reflect.ValueOf(value))
}

func (h *UnmarshalHandler) OnNumber(value float64) {
	h.setValue(reflect.ValueOf(value))
}

func (h *UnmarshalHandler) OnBool(value bool) {
	h.setValue(reflect.ValueOf(value))
}

func (h *UnmarshalHandler) OnNull() {
	if len(h.valueStack) == 0 {
		return
	}

	current := h.valueStack[len(h.valueStack)-1]
	switch current.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice:
		if current.CanSet() {
			current.Set(reflect.Zero(current.Type()))
		}
	}
}

// setValue handles setting values in the current container
func (h *UnmarshalHandler) setValue(val reflect.Value) {
	if len(h.valueStack) == 0 {
		return
	}

	current := h.valueStack[len(h.valueStack)-1]
	switch current.Kind() {
	case reflect.Map:
		// For maps, set key/value pair
		keyVal := reflect.ValueOf(h.currentKey)
		if keyVal.Type() != current.Type().Key() {
			keyVal = h.convertKey(keyVal, current.Type().Key())
		}

		valType := current.Type().Elem()
		convertedVal := h.convertValue(val, valType)
		current.SetMapIndex(keyVal, convertedVal)

	case reflect.Struct:
		// For structs, set field by name
		fields := getCachedFields(current.Type())
		for _, f := range fields {
			if string(f.nameBytes) == h.currentKey {
				field := current.FieldByIndex(f.index)
				if field.CanSet() {
					convertedVal := h.convertValue(val, field.Type())
					field.Set(convertedVal)
				}
				break
			}
		}

	case reflect.Slice:
		// For slices, append new value
		elemType := current.Type().Elem()
		convertedVal := h.convertValue(val, elemType)
		newSlice := reflect.Append(current, convertedVal)
		current.Set(newSlice)

	case reflect.Interface:
		// For interfaces, set value directly
		if current.CanSet() {
			current.Set(val)
		}
	}
}

// Helper methods for type conversion
func (h *UnmarshalHandler) convertKey(key reflect.Value, keyType reflect.Type) reflect.Value {
	// Simplified conversion logic
	if key.Type().ConvertibleTo(keyType) {
		return key.Convert(keyType)
	}
	return reflect.Zero(keyType)
}

func (h *UnmarshalHandler) convertValue(val reflect.Value, targetType reflect.Type) reflect.Value {
	// Simplified conversion logic
	if val.Type().AssignableTo(targetType) {
		return val
	}
	if val.Type().ConvertibleTo(targetType) {
		return val.Convert(targetType)
	}
	return reflect.Zero(targetType)
}

// IsCapturing returns true if currently capturing JSON content
func (b *BufferCapture) IsCapturing() bool {
	return b.capturing
}

// CaptureKey adds a JSON key to the buffer
func (b *BufferCapture) CaptureKey(key string) {
	b.buffer.WriteByte('"')
	writeEscapedStringString(b.buffer, key)
	b.buffer.WriteByte('"')
	b.buffer.WriteByte(':')
}

// StartCapture begins capturing JSON content
func (b *BufferCapture) StartCapture(char byte) {
	b.capturing = true
	b.buffer.WriteByte(char)
}
