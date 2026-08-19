package apexJSON

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
)

var pathSegmentPool = sync.Pool{
	New: func() interface{} {
		return &PathSegment{}
	},
}

func getPathSegment() *PathSegment {
	return pathSegmentPool.Get().(*PathSegment)
}

func putPathSegment(seg *PathSegment) {
	// Reset fields before returning to pool
	seg.Name = ""
	seg.IsArray = false
	seg.TargetIndex = -1
	seg.CurrentIndex = -1
	seg.Depth = 0
	pathSegmentPool.Put(seg)
}

// NewPathTracker creates a new PathTracker with initialized resources
func NewPathTracker(targetPath []string) *PathTracker {
	return &PathTracker{
		targetPath:  targetPath,
		stack:       make([]*PathSegment, 0, 8),
		segmentPool: &pathSegmentPool,
		notifyChan:  nil,
	}
}

func (pt *PathTracker) SetNotifyChannel(ch chan<- PathUpdate) {
	pt.notifyChan = ch
}

func (pt *PathTracker) sendUpdate(op PathOp, seg *PathSegment, idx int) {
	if pt == nil || seg == nil {
		return
	}
	if pt.notifyChan != nil {
		pt.notifyChan <- PathUpdate{
			Operation:  op,
			Segment:    seg,
			Depth:      seg.Depth,
			ArrayIndex: idx,
		}
	}
}

// PushObject handles object key path segments
func (pt *PathTracker) PushObject(key string) {
	seg := getPathSegment()
	seg.Name = SafeString(key)
	seg.IsArray = false
	seg.Depth = len(pt.stack)

	pt.stack = append(pt.stack, seg)
	pt.sendUpdate(PushOp, seg, -1)
	pt.updateMatchState()

	if debug {
		fmt.Printf("Pushed object key: %s@%d\n", seg.Name, seg.Depth)
	}
}

// PushArray handles array context entry
func (pt *PathTracker) PushArray(targetIndex int) {
	seg := getPathSegment()
	seg.IsArray = true
	seg.TargetIndex = targetIndex
	seg.CurrentIndex = -1
	seg.Depth = len(pt.stack)

	pt.stack = append(pt.stack, seg)
	pt.sendUpdate(PushOp, seg, -1)
	pt.updateMatchState()

	if debug {
		fmt.Printf("Pushed array context@%d (targetIdx=%d)\n",
			seg.Depth, targetIndex)
	}
}

func (pt *PathTracker) InArrayMode() bool {
	return len(pt.stack) > 0 && pt.stack[len(pt.stack)-1].IsArray
}

func (pt *PathTracker) IncrementIndex() {
	top := pt.top()
	if top == nil || !top.IsArray {
		return
	}

	top.CurrentIndex++
	pt.sendUpdate(UpdateIndexOp, top, top.CurrentIndex)
	pt.updateMatchState()
}

// PopSegment removes the top path segment
func (pt *PathTracker) PopSegment() {
	if len(pt.stack) == 0 {
		return
	}

	seg := pt.stack[len(pt.stack)-1]
	pt.sendUpdate(PopOp, seg, -1)
	putPathSegment(seg)
	pt.stack = pt.stack[:len(pt.stack)-1]
	pt.updateMatchState()

	if debug {
		fmt.Printf("Popped segment@%d\n", seg.Depth)
	}
}

// checkArrayExpectation validates array index requirements
func (pt *PathTracker) checkArrayExpectation() {
	currentDepth := len(pt.stack)
	if currentDepth > len(pt.targetPath) {
		return
	}

	currentSeg := pt.stack[currentDepth-1]
	if !currentSeg.IsArray {
		return
	}

	// Use the already set TargetIndex from PushArray
	target := pt.targetPath[currentDepth-1]
	if strTarget := strconv.Itoa(currentSeg.TargetIndex); strTarget != target {
		pt.stack[currentDepth-1].TargetIndex = -1 // Reset if mismatch
	}
}

// updateMatchState validates current stack against target path
func (pt *PathTracker) updateMatchState() {
	// Check if we've already matched the target path
	if pt.matched && len(pt.stack) > len(pt.targetPath) {
		// If we've matched the target path, stay matched even when going deeper
		return
	}

	pt.matched = false
	if len(pt.stack) < len(pt.targetPath) {
		return
	}

	// Check path segment matching
	for i, seg := range pt.stack {
		if i >= len(pt.targetPath) {
			// Extra segments are allowed if we already matched
			break
		}

		target := pt.targetPath[i]
		if seg.IsArray {
			// Validate array index match
			if strconv.Itoa(seg.TargetIndex) != target {
				return
			}

			// Validate current position matches target
			if seg.CurrentIndex != seg.TargetIndex {
				return
			}
		} else if seg.Name != target {
			return
		}
	}

	pt.matched = true
}

// InArrayContext checks if current top is array
func (pt *PathTracker) InArrayContext() bool {
	return pt.top() != nil && pt.top().IsArray
}

// CurrentTargetIndex returns expected array index for current depth
func (pt *PathTracker) CurrentTargetIndex() int {
	if top := pt.top(); top != nil && top.IsArray {
		return top.TargetIndex
	}
	return -1
}

// top returns the current stack top segment
func (pt *PathTracker) top() *PathSegment {
	if len(pt.stack) == 0 {
		return nil
	}
	return pt.stack[len(pt.stack)-1]
}

// DebugString returns formatted stack state
func (pt *PathTracker) DebugString() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Target: %v\n", pt.targetPath)
	fmt.Fprintf(&b, "Matched: %v\n", pt.matched)

	for i, seg := range pt.stack {
		fmt.Fprintf(&b, "[%d] %s@%d ", i, seg.Name, seg.Depth)
		if seg.IsArray {
			fmt.Fprintf(&b, "(Array current:%d target:%d)",
				seg.CurrentIndex, seg.TargetIndex)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Reset clears the tracker for reuse
func (pt *PathTracker) Reset(newPath []string) {
	for _, seg := range pt.stack {
		putPathSegment(seg)
	}
	pt.stack = pt.stack[:0]
	pt.targetPath = newPath
	pt.matched = false
}

func (pt *PathTracker) ShouldCapture() bool {
	// Only capture when:
	// 1. Full path matches
	// 2. Current array index matches target
	if !pt.matched {
		return false
	}

	for _, seg := range pt.stack {
		if seg.IsArray && seg.CurrentIndex != seg.TargetIndex {
			return false
		}
	}
	return true
}

func (pt *PathTracker) SetCaptureReady() {
	pt.matched = pt.PathDepth() == len(pt.targetPath)-1
}

func (pt *PathTracker) PathDepth() int {
	return len(pt.stack)
}

// ExpectingObjectAfterArray checks if next path segment is an object key
func (pt *PathTracker) ExpectingObjectAfterArray() bool {
	if len(pt.stack) == 0 || len(pt.targetPath) <= len(pt.stack) {
		return false
	}
	currentDepth := len(pt.stack)
	_, err := strconv.Atoi(pt.targetPath[currentDepth])
	return err != nil // Next segment is non-numeric = object key
}

// InArrayIndexMode checks if processing array index
func (pt *PathTracker) InArrayIndexMode() bool {
	if len(pt.stack) == 0 {
		return false
	}
	last := pt.stack[len(pt.stack)-1]
	return last.IsArray && last.CurrentIndex != last.TargetIndex
}

func (pt *PathTracker) ClearArrayFlags() {
	if len(pt.stack) > 0 {
		if seg := pt.stack[len(pt.stack)-1]; seg.IsArray {
			seg.CurrentIndex = -1 // Reset tracking
		}
	}
}

func (pt *PathTracker) BeginCapture() {
	pt.matched = true
	if len(pt.stack) > 0 {
		current := pt.stack[len(pt.stack)-1]
		if current.IsArray {
			current.CurrentIndex = current.TargetIndex
		}
	}
}

func (pt *PathTracker) CancelCapture() {
	pt.matched = false
	// Reset array tracking if needed
	for i := range pt.stack {
		if pt.stack[i].IsArray {
			pt.stack[i].CurrentIndex = -1
		}
	}
}
