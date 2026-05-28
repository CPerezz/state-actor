package recsplit

import (
	"encoding/binary"
	"io"
)

// GolombRice is a bit-stream encoder for Golomb-Rice codes. The Golomb
// parameter is always a power of two in this codebase, so we work with
// log2(parameter) directly and use a fixed (truncated binary) + unary
// representation.
//
// Mirrors db/recsplit/golomb_rice.go GolombRice (verbatim, except `Write`
// uses `binary.LittleEndian.PutUint64` instead of Erigon's
// `(*[maxDataSize]byte)(unsafe.Pointer(&g.data[0]))` slice-cast).
type GolombRice struct {
	data     []uint64 // backing storage; bits packed LSB-first within each uint64
	bitCount int      // total bits currently in `data`
}

// Bits returns the current bit-count.
func (g *GolombRice) Bits() int { return g.bitCount }

// Data exposes the underlying word slice (used by Write).
func (g *GolombRice) Data() []uint64 { return g.data }

// appendUnaryAll appends a unary encoding of each value: for each u, write
// u zero-bits followed by a single 1-bit (so u uses u+1 bits).
//
// Port of golomb_rice.go:39-56. The trailing 1-bit insertion uses
// `g.data[appendPtr] |= uint64(1) << (g.bitCount & 63)` after advancing
// `g.bitCount` by u zero-bits.
func (g *GolombRice) appendUnaryAll(unary []uint64) {
	bitInc := 0
	for _, u := range unary {
		bitInc += int(u) + 1
	}
	targetSize := (g.bitCount + bitInc + 63) / 64
	for len(g.data) < targetSize {
		g.data = append(g.data, 0)
	}
	for _, u := range unary {
		g.bitCount += int(u)
		appendPtr := g.bitCount / 64
		g.data[appendPtr] |= uint64(1) << (g.bitCount & 63)
		g.bitCount++
	}
}

// appendFixed appends the low `log2golomb` bits of `v` to the bit-stream.
// Port of golomb_rice.go:61-83.
//
// The encoding writes `v & ((1<<log2golomb)-1)` shifted left by the
// existing usedBits-within-word amount; if that crosses a word boundary,
// the overflow goes into the next word.
func (g *GolombRice) appendFixed(v uint64, log2golomb int) {
	if log2golomb == 0 {
		return
	}
	lowerBits := v & ((uint64(1) << log2golomb) - 1)
	usedBits := g.bitCount & 63
	targetSize := (g.bitCount + log2golomb + 63) / 64
	for len(g.data) < targetSize {
		g.data = append(g.data, 0)
	}
	appendPtr := g.bitCount / 64
	curWord := g.data[appendPtr]
	curWord |= lowerBits << usedBits
	if usedBits+log2golomb > 64 {
		g.data[appendPtr] = curWord
		appendPtr++
		curWord = lowerBits >> (64 - usedBits)
	}
	g.data[appendPtr] = curWord
	g.bitCount += log2golomb
}

// Append concatenates another GolombRice bit-stream onto this one,
// shifting it to align with the current bit position. Port of
// golomb_rice.go:91-113.
func (g *GolombRice) Append(other *GolombRice) {
	if other.bitCount == 0 {
		return
	}
	shift := g.bitCount & 63
	targetSize := (g.bitCount + other.bitCount + 63) / 64
	for len(g.data) < targetSize {
		g.data = append(g.data, 0)
	}
	appendPtr := g.bitCount / 64
	nWords := (other.bitCount + 63) / 64
	if shift == 0 {
		copy(g.data[appendPtr:], other.data[:nWords])
	} else {
		for i, w := range other.data[:nWords] {
			g.data[appendPtr+i] |= w << shift
			if appendPtr+i+1 < len(g.data) {
				g.data[appendPtr+i+1] |= w >> (64 - shift)
			}
		}
	}
	g.bitCount += other.bitCount
}

// Write serializes the GolombRice state. Header is 8B BE word count;
// payload is `len(data) * 8` bytes of host-LE uint64s.
//
// Port of golomb_rice.go:185-197 with the `unsafe.Pointer` slice-cast
// replaced by a binary.LittleEndian loop. On LE platforms (the only ones
// state-actor ships on) the result is bit-identical.
func (g *GolombRice) Write(w io.Writer) error {
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], uint64(len(g.data)))
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	scratch := make([]byte, 8*len(g.data))
	for i, x := range g.data {
		binary.LittleEndian.PutUint64(scratch[i*8:(i+1)*8], x)
	}
	_, err := w.Write(scratch)
	return err
}
