package apexjson

import (
	"errors"
	"strconv"

	"github.com/xDarkicex/memory"
)

// ErrInvalidPath reports a malformed path component, such as a non-numeric
// array index. A missing path is not an error when PostgreSQL-compatible
// mutation semantics define it as a no-op.
var ErrInvalidPath = errors.New("apexjson: invalid mutation path")

type mutationPathState uint8

const (
	mutationPathNoop mutationPathState = iota
	mutationPathFound
	mutationPathMissingFinal
)

type mutationPathTarget struct {
	state      mutationPathState
	idx        int32
	parent     int32
	parentKind nodeKind
	component  string

	// Array details are populated for both an existing array element and a
	// missing final array component. arrayIndex is normalized for an existing
	// element and retains the requested integer for a missing element.
	arrayIndex int
	arrayLen   int
}

// SetPathRaw replaces one JSON value addressed by path. It follows the
// jsonb_set rule that only the final missing component may be created when
// createMissing is true; missing intermediate components are no-ops.
func (d *Decoder) SetPathRaw(path []string, replacement []byte, createMissing bool) (bool, error) {
	if err := validateMutationReplacement(replacement); err != nil {
		return false, err
	}
	target, err := d.resolveMutationPath(path)
	if err != nil {
		return false, err
	}
	switch target.state {
	case mutationPathNoop:
		return false, nil
	case mutationPathFound:
		start, end, ok := rawSpanAt(&d.doc, target.idx)
		if !ok {
			return false, ErrStaleValue
		}
		if bytesEqual(d.doc.src[start:end], replacement) {
			return false, nil
		}
		idx, err := d.doc.replaceSpan(start, end, replacement)
		if err != nil {
			return false, err
		}
		d.publishMutation(idx)
		return true, nil
	case mutationPathMissingFinal:
		if !createMissing {
			return false, nil
		}
		return d.createMissingPathValue(target, replacement)
	default:
		return false, ErrInvalidPath
	}
}

// InsertPathRaw inserts replacement relative to an array element or creates
// a missing object key. Existing object keys are valid no-ops. For arrays,
// insertAfter applies to an existing element; out-of-range indexes use the
// PostgreSQL behavior of prepending for negative indexes and appending for
// positive indexes.
func (d *Decoder) InsertPathRaw(path []string, replacement []byte, insertAfter bool) (bool, error) {
	if err := validateMutationReplacement(replacement); err != nil {
		return false, err
	}
	if len(path) == 0 {
		return false, nil
	}
	target, err := d.resolveMutationPath(path)
	if err != nil {
		return false, err
	}
	switch target.state {
	case mutationPathNoop:
		return false, nil
	case mutationPathFound:
		switch target.parentKind {
		case nodeObject:
			// jsonb_insert does not overwrite an existing object key.
			return false, nil
		case nodeArray:
			return d.insertAroundArrayValue(target, replacement, insertAfter)
		default:
			return false, nil
		}
	case mutationPathMissingFinal:
		switch target.parentKind {
		case nodeObject:
			return d.insertMissingObjectField(target, replacement)
		case nodeArray:
			return d.insertMissingArrayValue(target, replacement)
		default:
			return false, nil
		}
	default:
		return false, ErrInvalidPath
	}
}

func (d *Decoder) resolveMutationPath(path []string) (mutationPathTarget, error) {
	if d == nil || d.arena == nil || d.err != nil || d.root == noNode || d.doc.root == noNode {
		if d != nil && d.err != nil {
			return mutationPathTarget{}, d.err
		}
		return mutationPathTarget{}, ErrPathNotFound
	}
	if len(path) == 0 {
		return mutationPathTarget{state: mutationPathFound, idx: d.root, parent: noNode}, nil
	}

	current := d.root
	for i, component := range path {
		if current == noNode || int(current) >= d.doc.slab.n {
			return mutationPathTarget{}, nil
		}
		last := i == len(path)-1
		switch d.doc.slab.kind(current) {
		case nodeObject:
			value := objectValueForKey(&d.doc, current, component)
			if value == noNode {
				if !last {
					return mutationPathTarget{}, nil
				}
				return mutationPathTarget{
					state:      mutationPathMissingFinal,
					parent:     current,
					parentKind: nodeObject,
					component:  component,
				}, nil
			}
			if last {
				return mutationPathTarget{
					state:      mutationPathFound,
					idx:        value,
					parent:     current,
					parentKind: nodeObject,
					component:  component,
				}, nil
			}
			current = value

		case nodeArray:
			requested, err := parseMutationArrayIndex(component)
			if err != nil {
				return mutationPathTarget{}, err
			}
			length := countChildren(&d.doc, current)
			index := normalizeExistingArrayIndex(requested, length)
			if index < 0 || index >= length {
				if !last {
					return mutationPathTarget{}, nil
				}
				return mutationPathTarget{
					state:      mutationPathMissingFinal,
					parent:     current,
					parentKind: nodeArray,
					component:  component,
					arrayIndex: requested,
					arrayLen:   length,
				}, nil
			}
			value := arrayValueAt(&d.doc, current, index)
			if value == noNode {
				return mutationPathTarget{}, nil
			}
			if last {
				return mutationPathTarget{
					state:      mutationPathFound,
					idx:        value,
					parent:     current,
					parentKind: nodeArray,
					component:  component,
					arrayIndex: index,
					arrayLen:   length,
				}, nil
			}
			current = value

		default:
			// A scalar cannot contain a path component. This is the
			// jsonb_set/jsonb_insert missing-intermediate no-op.
			return mutationPathTarget{}, nil
		}
	}
	return mutationPathTarget{}, nil
}

func (d *Decoder) createMissingPathValue(target mutationPathTarget, replacement []byte) (bool, error) {
	switch target.parentKind {
	case nodeObject:
		return d.insertMissingObjectField(target, replacement)
	case nodeArray:
		return d.insertMissingArrayValue(target, replacement)
	default:
		return false, nil
	}
}

func (d *Decoder) insertMissingObjectField(target mutationPathTarget, replacement []byte) (bool, error) {
	fragment := buildObjectFieldFragment(target.component, replacement, countObjectPairs(&d.doc, target.parent) != 0)
	start := int(d.doc.slab.end(target.parent)) - 1
	idx, err := d.doc.replaceSpan(start, start, fragment)
	if err != nil {
		return false, err
	}
	d.publishMutation(idx)
	return true, nil
}

func (d *Decoder) insertMissingArrayValue(target mutationPathTarget, replacement []byte) (bool, error) {
	position := normalizeArrayInsertionIndex(target.arrayIndex, target.arrayLen)
	at, fragment := arrayInsertionAt(&d.doc, target.parent, position, replacement)
	idx, err := d.doc.replaceSpan(at, at, fragment)
	if err != nil {
		return false, err
	}
	d.publishMutation(idx)
	return true, nil
}

func (d *Decoder) insertAroundArrayValue(target mutationPathTarget, replacement []byte, insertAfter bool) (bool, error) {
	start, end, ok := rawSpanAt(&d.doc, target.idx)
	if !ok {
		return false, ErrStaleValue
	}
	if insertAfter {
		return d.insertAt(end, appendCommaPrefix(replacement))
	}
	return d.insertAt(start, appendCommaSuffix(replacement))
}

func (d *Decoder) insertAt(at int, fragment []byte) (bool, error) {
	idx, err := d.doc.replaceSpan(at, at, fragment)
	if err != nil {
		return false, err
	}
	d.publishMutation(idx)
	return true, nil
}

func (d *Decoder) publishMutation(idx int32) {
	d.root = idx
	d.src = d.doc.src
	d.err = nil
}

func validateMutationReplacement(raw []byte) error {
	if len(raw) == 0 {
		return &SyntaxError{Msg: "empty JSON replacement"}
	}
	capacity := len(raw) + 16
	arenaSize := uint64(capacity*nodeSize + 4*64)
	arena, err := newMutationArena(arenaSize)
	if err != nil {
		return err
	}
	defer arena.Free()

	var doc parsedDoc
	if err := doc.slab.initArena(arena, capacity); err != nil {
		return err
	}
	var p fusedParser
	p.reset(&doc, raw)
	_, ok := p.parseDocument()
	if !ok {
		if p.err != nil {
			return p.err
		}
		return ErrInvalidMutation
	}
	return nil
}

func newMutationArena(size uint64) (*memory.Arena, error) {
	return memory.NewArena(size, 64)
}

func parseMutationArrayIndex(component string) (int, error) {
	if component == "" {
		return 0, ErrInvalidPath
	}
	index, err := strconv.Atoi(component)
	if err != nil {
		return 0, ErrInvalidPath
	}
	return index, nil
}

func normalizeExistingArrayIndex(index, length int) int {
	if index < 0 {
		return length + index
	}
	return index
}

func normalizeArrayInsertionIndex(index, length int) int {
	if index < 0 {
		index += length
		if index < 0 {
			return 0
		}
		return index
	}
	if index > length {
		return length
	}
	return index
}

func objectValueForKey(doc *parsedDoc, parent int32, key string) int32 {
	src := doc.src
	for child := firstChild(&doc.slab, parent); child != noNode; {
		start, end := doc.slab.start(child), doc.slab.end(child)
		if bytesEqualString(unescapeJSONStringFlags(doc.slab.flag(child), src[start:end]), key) {
			return child + 1
		}
		value := child + 1
		child = nextChild(&doc.slab, parent, value)
	}
	return noNode
}

func arrayValueAt(doc *parsedDoc, parent int32, index int) int32 {
	child := firstChild(&doc.slab, parent)
	for i := 0; child != noNode; i++ {
		if i == index {
			return child
		}
		child = nextChild(&doc.slab, parent, child)
	}
	return noNode
}

func rawSpanAt(doc *parsedDoc, idx int32) (int, int, bool) {
	if doc == nil || idx == noNode || idx < 0 || int(idx) >= doc.slab.n {
		return 0, 0, false
	}
	start := int(doc.slab.start(idx))
	end := int(doc.slab.end(idx))
	if doc.slab.kind(idx) == nodeString {
		start--
		end++
	}
	if start < 0 || end < start || end > len(doc.src) {
		return 0, 0, false
	}
	return start, end, true
}

func buildObjectFieldFragment(key string, replacement []byte, withComma bool) []byte {
	fragment := make([]byte, 0, len(key)+len(replacement)+4)
	if withComma {
		fragment = append(fragment, ',')
	}
	fragment = appendQuotedString(fragment, key)
	fragment = append(fragment, ':')
	return append(fragment, replacement...)
}

func arrayInsertionAt(doc *parsedDoc, parent int32, position int, replacement []byte) (int, []byte) {
	length := countChildren(doc, parent)
	if length == 0 {
		return int(doc.slab.end(parent)) - 1, append([]byte(nil), replacement...)
	}
	if position <= 0 {
		child := firstChild(&doc.slab, parent)
		start, _, _ := rawSpanAt(doc, child)
		return start, appendCommaSuffix(replacement)
	}
	if position >= length {
		return int(doc.slab.end(parent)) - 1, appendCommaPrefix(replacement)
	}
	child := arrayValueAt(doc, parent, position)
	start, _, _ := rawSpanAt(doc, child)
	return start, appendCommaSuffix(replacement)
}

func appendCommaPrefix(replacement []byte) []byte {
	fragment := make([]byte, 1, len(replacement)+1)
	fragment[0] = ','
	return append(fragment, replacement...)
}

func appendCommaSuffix(replacement []byte) []byte {
	fragment := make([]byte, 0, len(replacement)+1)
	fragment = append(fragment, replacement...)
	return append(fragment, ',')
}
