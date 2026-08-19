package apexjson

// ObjectIter walks an object's fields without allocating. It retains a
// pointer to the columnar tape and advances using subtree skip offsets.
type ObjectIter struct {
	src    []byte
	slab   *slab
	owner  *parsedDoc
	epoch  uint64
	parent int32
	child  int32
	key    int32
	value  int32
}

// ObjectIter returns a zero-allocation iterator for an object value.
func (v Value) ObjectIter() ObjectIter {
	var it ObjectIter
	it.Reset(v)
	return it
}

// Reset reuses it for v and rewinds it before the first field.
func (it *ObjectIter) Reset(v Value) {
	it.src = v.source()
	it.slab = v.slab
	it.owner = v.owner
	it.epoch = v.epoch
	it.parent = noNode
	it.child = noNode
	it.key = noNode
	it.value = noNode
	if v.owner != nil && v.epoch != v.owner.generation {
		return
	}
	if v.found && v.slab != nil && v.idx != noNode && int(v.idx) < v.slab.n && v.slab.kind(v.idx) == nodeObject {
		it.parent = v.idx
		it.child = firstChild(v.slab, v.idx)
	}
}

// Next advances to the next field.
func (it *ObjectIter) Next() bool {
	if it.child == noNode {
		it.key = noNode
		it.value = noNode
		return false
	}
	it.key = it.child
	it.value = it.child + 1
	it.child = nextChild(it.slab, it.parent, it.value)
	return true
}

// KeyBytes returns the current key without quotes. Escapes remain encoded.
func (it *ObjectIter) KeyBytes() []byte {
	if it.key == noNode || it.slab == nil || int(it.key) >= it.slab.n ||
		(it.owner != nil && it.epoch != it.owner.generation) {
		return nil
	}
	src := it.src
	if it.owner != nil {
		src = it.owner.src
	}
	return src[it.slab.start(it.key):it.slab.end(it.key)]
}

// Key returns the current key without quotes and without allocating.
func (it *ObjectIter) Key() string { return bytesToString(it.KeyBytes()) }

// Value returns the current field's lazy value.
func (it *ObjectIter) Value() Value {
	if it.value == noNode || it.slab == nil || int(it.value) >= it.slab.n ||
		(it.owner != nil && it.epoch != it.owner.generation) {
		return Value{}
	}
	if it.owner != nil {
		return valueAt(it.owner, it.value)
	}
	return Value{src: it.src, slab: it.slab, idx: it.value, epoch: it.epoch, found: true}
}

// ArrayIter walks an array without allocating.
type ArrayIter struct {
	src     []byte
	slab    *slab
	owner   *parsedDoc
	epoch   uint64
	parent  int32
	child   int32
	current int32
	index   int
}

// ArrayIter returns a zero-allocation iterator for an array value.
func (v Value) ArrayIter() ArrayIter {
	var it ArrayIter
	it.Reset(v)
	return it
}

// Reset reuses it for v and rewinds it before the first element.
func (it *ArrayIter) Reset(v Value) {
	it.src = v.source()
	it.slab = v.slab
	it.owner = v.owner
	it.epoch = v.epoch
	it.parent = noNode
	it.child = noNode
	it.current = noNode
	it.index = -1
	if v.owner != nil && v.epoch != v.owner.generation {
		return
	}
	if v.found && v.slab != nil && v.idx != noNode && int(v.idx) < v.slab.n && v.slab.kind(v.idx) == nodeArray {
		it.parent = v.idx
		it.child = firstChild(v.slab, v.idx)
	}
}

// Next advances to the next array element.
func (it *ArrayIter) Next() bool {
	if it.child == noNode {
		it.current = noNode
		return false
	}
	it.index++
	current := it.child
	it.current = current
	it.child = nextChild(it.slab, it.parent, current)
	return true
}

// Index returns the current zero-based array index, or -1 when exhausted.
func (it *ArrayIter) Index() int {
	if it.current == noNode {
		return -1
	}
	return it.index
}

// Value returns the current array element's lazy value.
func (it *ArrayIter) Value() Value {
	if it.current == noNode || it.slab == nil || int(it.current) >= it.slab.n ||
		(it.owner != nil && it.epoch != it.owner.generation) {
		return Value{}
	}
	if it.owner != nil {
		return valueAt(it.owner, it.current)
	}
	return Value{src: it.src, slab: it.slab, idx: it.current, epoch: it.epoch, found: true}
}
