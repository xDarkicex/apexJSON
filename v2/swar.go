package apexjson

import (
	"math/bits"
	"unsafe"
)

const (
	swWordBytes      = 8
	swWordMask       = uint64(0x8080808080808080)
	swWordReplicator = uint64(0x0101010101010101)
)

// swFindByte returns the smallest i in [start,end) with src[i]==b,
// or end if none. end<=start returns end.
func swFindByte(src []byte, start, end uint32, b byte) uint32 {
	if start >= end {
		return end
	}
	// Scalar head: walk to 8-byte alignment, also catches any match in the
	// first 0-7 bytes regardless of alignment.
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		if src[start] == b {
			return start
		}
		start++
	}
	// Aligned 8-byte middle.
	for end-start >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[start]))
		mask := swByteEqualMask(w, b)
		if mask != 0 {
			return start + uint32(bits.TrailingZeros64(mask)>>3)
		}
		start += swWordBytes
	}
	// Scalar tail.
	for start < end {
		if src[start] == b {
			return start
		}
		start++
	}
	return end
}

// swFindByteNot returns the smallest i in [start,end) with src[i]!=b,
// or end if none.
func swFindByteNot(src []byte, start, end uint32, b byte) uint32 {
	if start >= end {
		return end
	}
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		if src[start] != b {
			return start
		}
		start++
	}
	for end-start >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[start]))
		// eqMask: high bit set per byte where byte == b.
		eqMask := swByteEqualMask(w, b)
		if eqMask != swWordMask {
			nonMatch := ^eqMask & swWordMask
			return start + uint32(bits.TrailingZeros64(nonMatch)>>3)
		}
		start += swWordBytes
	}
	for start < end {
		if src[start] != b {
			return start
		}
		start++
	}
	return end
}

// swSkipWS advances pos past leading JSON whitespace (' ', '\t', '\n', '\r')
// up to end. Returns the first non-WS position, or end if all WS.
//
// Implementation note: byte-range SWAR formulas can suffer a borrow-chain
// false-positive bug, so this implementation uses four exact equality masks
// instead of a range trick. This avoids treating adjacent non-whitespace
// bytes as whitespace.
func swSkipWS(src []byte, pos, end uint32) uint32 {
	if pos >= end || !isWS(src[pos]) {
		return pos
	}
	for pos < end && uintptr(unsafe.Pointer(&src[pos]))%swWordBytes != 0 {
		if !isWS(src[pos]) {
			return pos
		}
		pos++
	}
	for end-pos >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[pos]))
		ws := swByteEqualMask(w, ' ') |
			swByteEqualMask(w, '\t') |
			swByteEqualMask(w, '\n') |
			swByteEqualMask(w, '\r')
		if ws != swWordMask {
			nonWS := ^ws & swWordMask
			return pos + uint32(bits.TrailingZeros64(nonWS)>>3)
		}
		pos += swWordBytes
	}
	for pos < end && isWS(src[pos]) {
		pos++
	}
	return pos
}

// swFindAnyByte8 returns the smallest i in [start,end) with src[i] in sigs,
// or end if none. sigs must have length 1..8.
func swFindAnyByte8(src []byte, start, end uint32, sigs [8]byte) uint32 {
	if start >= end {
		return end
	}
	// Aligned head.
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		if inSigs(src[start], sigs[:]) {
			return start
		}
		start++
	}
	for end-start >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[start]))
		var mask uint64
		for i := 0; i < 8; i++ {
			b := sigs[i]
			if b == 0 && i > 0 {
				break
			}
			mask |= swByteEqualMask(w, b)
		}
		if mask != 0 {
			// Find lowest matching byte.
			return start + uint32(bits.TrailingZeros64(mask)>>3)
		}
		start += swWordBytes
	}
	for start < end {
		if inSigs(src[start], sigs[:]) {
			return start
		}
		start++
	}
	return end
}

// swFindNonDigit returns the first byte that is not an ASCII decimal digit.
// The SWAR masks are conservative; every candidate is checked byte-for-byte
// before it is returned, preserving exact number-token semantics.
func swFindNonDigit(src []byte, start, end uint32) uint32 {
	return uint32(swFindNonDigitInt(src, int(start), int(end)))
}

func swFindNonDigitInt(src []byte, start, end int) int {
	if start >= end {
		return end
	}
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		if src[start] < '0' || src[start] > '9' {
			return start
		}
		start++
	}
	for end-start >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[start]))
		// Build an exact digit mask. Ten equality checks are worthwhile
		// here because number runs are uncommon but can be very long, and
		// unlike byte-range subtraction this cannot borrow across lanes.
		digitMask := uint64(0)
		for digit := byte('0'); digit <= '9'; digit++ {
			digitMask |= swByteEqualMask(w, digit)
		}
		mask := ^digitMask & swWordMask
		for mask != 0 {
			i := start + bits.TrailingZeros64(mask)>>3
			if src[i] < '0' || src[i] > '9' {
				return i
			}
			mask &^= uint64(0x80) << (uint((i-start)&7) * 8)
		}
		start += swWordBytes
	}
	for start < end {
		if src[start] < '0' || src[start] > '9' {
			return start
		}
		start++
	}
	return end
}

func swByteEqualMask(w uint64, b byte) uint64 {
	x := w ^ (uint64(b) * swWordReplicator)
	return (x - swWordReplicator) &^ x & swWordMask
}

// swByteLessMask returns high bits for bytes less than n. The candidate
// returned by this mask is validated by the caller because byte-wise borrow
// tricks can conservatively flag a neighbouring byte.
func swByteLessMask(w uint64, n byte) uint64 {
	return (w - uint64(n)*swWordReplicator) &^ w & swWordMask
}

// swFindStringEvent finds the next quote, escape, or unescaped control byte.
// It is the hot path for long JSON strings: valid text is examined eight
// bytes at a time, while escape/control handling remains scalar and exact.
func swFindStringEvent(src []byte, start, end uint32) uint32 {
	event, _ := swFindStringEventMeta(src, start, end)
	return event
}

func swFindStringEventMeta(src []byte, start, end uint32) (uint32, bool) {
	event, ascii := swFindStringEventMetaInt(src, int(start), int(end))
	return uint32(event), ascii
}

func swFindStringEventMetaInt(src []byte, start, end int) (int, bool) {
	if start >= end {
		return end, true
	}
	ascii := true
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		b := src[start]
		if b >= 0x80 {
			ascii = false
		}
		if b == '"' || b == '\\' || b < 0x20 {
			return start, ascii
		}
		start++
	}
	for end-start >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[start]))
		if w&swWordMask != 0 {
			ascii = false
		}
		mask := swByteEqualMask(w, '"') | swByteEqualMask(w, '\\') | swByteLessMask(w, 0x20)
		for mask != 0 {
			i := start + bits.TrailingZeros64(mask)>>3
			b := src[i]
			if b == '"' || b == '\\' || b < 0x20 {
				return i, ascii
			}
			// Conservative less-than masks may identify a neighbour;
			// discard that candidate and continue within the word.
			mask &^= uint64(0x80) << (uint((i-start)&7) * 8)
		}
		start += swWordBytes
	}
	for start < end {
		b := src[start]
		if b >= 0x80 {
			ascii = false
		}
		if b == '"' || b == '\\' || b < 0x20 {
			return start, ascii
		}
		start++
	}
	return end, ascii
}

// swFindStringEventInt returns the next byte that must be escaped while
// marshaling a string. Unlike swFindStringEventMetaInt, it does not track
// whether the span is ASCII: the encoder only needs the event position, so
// maintaining that second result would add a high-bit test and a branch for
// every eight-byte word on the hot clean-string path.
func swFindStringEventInt(src []byte, start, end int) int {
	if start >= end {
		return end
	}
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		b := src[start]
		if b == '"' || b == '\\' || b < 0x20 {
			return start
		}
		start++
	}
	for end-start >= swWordBytes {
		w := *(*uint64)(unsafe.Pointer(&src[start]))
		mask := swByteEqualMask(w, '"') | swByteEqualMask(w, '\\') | swByteLessMask(w, 0x20)
		for mask != 0 {
			i := start + bits.TrailingZeros64(mask)>>3
			b := src[i]
			if b == '"' || b == '\\' || b < 0x20 {
				return i
			}
			// swByteLessMask can conservatively identify a neighbouring byte;
			// discard only that candidate and validate the next one.
			mask &^= uint64(0x80) << (uint((i-start)&7) * 8)
		}
		start += swWordBytes
	}
	for start < end {
		b := src[start]
		if b == '"' || b == '\\' || b < 0x20 {
			return start
		}
		start++
	}
	return end
}

func swRangeASCII(src []byte, start, end int) bool {
	for start < end && uintptr(unsafe.Pointer(&src[start]))%swWordBytes != 0 {
		if src[start] >= 0x80 {
			return false
		}
		start++
	}
	for end-start >= swWordBytes {
		if *(*uint64)(unsafe.Pointer(&src[start]))&swWordMask != 0 {
			return false
		}
		start += swWordBytes
	}
	for start < end {
		if src[start] >= 0x80 {
			return false
		}
		start++
	}
	return true
}

func isWS(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func inSigs(b byte, sigs []byte) bool {
	for _, s := range sigs {
		if s == 0 {
			return false
		}
		if b == s {
			return true
		}
	}
	return false
}
