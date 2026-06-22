package recsplit

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"math/bits"
	"unsafe"
)

// Layout constants ported verbatim from eliasfano16/elias_fano.go:38-46.
// These differ from eliasfano32 — superQSize is 1 + qPerSuperQ/4 = 17
// here (16-bit jump offsets) instead of 1 + qPerSuperQ/2 = 33 (32-bit).
const (
	ef16log2q      uint64 = 8
	ef16q          uint64 = 1 << ef16log2q     // 256
	ef16qMask      uint64 = ef16q - 1          // 0xff
	ef16superQ     uint64 = 1 << 14            // 16384
	ef16superQMask uint64 = ef16superQ - 1     // 0x3fff
	ef16qPerSuperQ uint64 = ef16superQ / ef16q // 64
	ef16superQSize uint64 = 1 + ef16qPerSuperQ/4
)

func init() {
	var x uint64 = 1
	if *(*byte)(unsafe.Pointer(&x)) != 1 {
		panic("recsplit: big-endian platform not supported (Erigon's wire format is host-byte-order LE)")
	}
}

// DoubleEliasFano encodes TWO monotonically-non-decreasing sequences
// (cumKeys and position) sharing the same length. It interleaves the
// lower-bits arrays for cache locality. Port of
// eliasfano16/elias_fano.go:251-389 — the writer-only subset.
//
// Used by RecSplit to encode the (bucketSizeAccumulator,
// bucketBitPositionAccumulator) pair so a reader can locate any bucket.
type DoubleEliasFano struct {
	data                  []uint64
	lowerBits             []uint64
	upperBitsPosition     []uint64
	upperBitsCumKeys      []uint64
	jump                  []uint64
	lowerBitsMaskCumKeys  uint64
	lowerBitsMaskPosition uint64
	numBuckets            uint64
	uCumKeys              uint64
	uPosition             uint64
	lPosition             uint64
	lCumKeys              uint64
	cumKeysMinDelta       uint64
	posMinDelta           uint64
}

// jumpSizeWords mirrors eliasfano16/elias_fano.go:408-414.
func (ef *DoubleEliasFano) jumpSizeWords() int {
	size := ((ef.numBuckets + 1) / ef16superQ) * ef16superQSize * 2 // whole blocks
	if (ef.numBuckets+1)%ef16superQ != 0 {
		size += (1 + (((ef.numBuckets+1)%ef16superQ+ef16q-1)/ef16q+3)/4) * 2
	}
	return int(size)
}

// deriveFields computes the bit-widths + word counts. Mirrors
// eliasfano16/elias_fano.go:270-282 (which dispatches to efcommon).
func (ef *DoubleEliasFano) deriveFields() (wordsCumKeys, wordsPosition int) {
	if ef.uPosition/(ef.numBuckets+1) == 0 {
		ef.lPosition = 0
	} else {
		ef.lPosition = 63 ^ uint64(bits.LeadingZeros64(ef.uPosition/(ef.numBuckets+1)))
	}
	if ef.uCumKeys/(ef.numBuckets+1) == 0 {
		ef.lCumKeys = 0
	} else {
		ef.lCumKeys = 63 ^ uint64(bits.LeadingZeros64(ef.uCumKeys/(ef.numBuckets+1)))
	}
	if ef.lCumKeys*2+ef.lPosition > 56 {
		panic(fmt.Sprintf("ef.lCumKeys (%d) * 2 + ef.lPosition (%d) > 56", ef.lCumKeys, ef.lPosition))
	}
	ef.lowerBitsMaskCumKeys = (uint64(1) << ef.lCumKeys) - 1
	ef.lowerBitsMaskPosition = (uint64(1) << ef.lPosition) - 1

	wordsLowerBits := int(((ef.numBuckets+1)*(ef.lCumKeys+ef.lPosition)+63)/64 + 1)
	wordsCumKeys = int((ef.numBuckets + 1 + (ef.uCumKeys >> ef.lCumKeys) + 63) / 64)
	wordsPosition = int((ef.numBuckets + 1 + (ef.uPosition >> ef.lPosition) + 63) / 64)
	totalWords := wordsLowerBits + wordsCumKeys + wordsPosition + ef.jumpSizeWords()
	if ef.data == nil {
		ef.data = make([]uint64, totalWords)
	} else {
		ef.data = ef.data[:totalWords]
	}
	ef.lowerBits = ef.data[:wordsLowerBits]
	ef.upperBitsCumKeys = ef.data[wordsLowerBits : wordsLowerBits+wordsCumKeys]
	ef.upperBitsPosition = ef.data[wordsLowerBits+wordsCumKeys : wordsLowerBits+wordsCumKeys+wordsPosition]
	ef.jump = ef.data[wordsLowerBits+wordsCumKeys+wordsPosition:]
	return wordsCumKeys, wordsPosition
}

// efSetBits stores a value at bit position `start` within `bits`. Caller
// invariant: lowerBits has +1 padding word; values written in monotonic
// order so the target bits are zero, so `|=` is safe instead of clear+set.
// Mirrors eliasfano16/elias_fano.go:396-401.
func efSetBits(b []uint64, start uint64, value uint64) {
	idx64, shift := start>>6, int(start&63)
	_ = b[idx64+1] // BCE hint
	b[idx64] |= value << shift
	b[idx64+1] |= value >> (64 - shift)
}

// efSet turns on a single bit. Mirrors eliasfano16/elias_fano.go:403-406.
func efSet(b []uint64, pos uint64) {
	b[pos/64] |= uint64(1) << (pos % 64)
}

// Build constructs the DoubleEliasFano encoding from two monotone
// uint64 sequences of equal length. Port of
// eliasfano16/elias_fano.go:285-389.
func (ef *DoubleEliasFano) Build(cumKeys []uint64, position []uint64) {
	if len(cumKeys) != len(position) {
		panic("len(cumKeys) != len(position)")
	}
	ef.numBuckets = uint64(len(cumKeys) - 1)
	ef.posMinDelta = math.MaxUint64
	ef.cumKeysMinDelta = math.MaxUint64
	for i := uint64(1); i <= ef.numBuckets; i++ {
		if cumKeys[i] < cumKeys[i-1] {
			panic("cumKeys[i] < cumKeys[i-1]")
		}
		nkeysDelta := cumKeys[i] - cumKeys[i-1]
		if nkeysDelta < ef.cumKeysMinDelta {
			ef.cumKeysMinDelta = nkeysDelta
		}
		if position[i] < position[i-1] {
			panic("position[i] < position[i-1]")
		}
		bucketBits := position[i] - position[i-1]
		if bucketBits < ef.posMinDelta {
			ef.posMinDelta = bucketBits
		}
	}
	ef.uPosition = position[ef.numBuckets] - ef.numBuckets*ef.posMinDelta + 1
	ef.uCumKeys = cumKeys[ef.numBuckets] - ef.numBuckets*ef.cumKeysMinDelta + 1
	wordsCumKeys, wordsPosition := ef.deriveFields()

	for i, cumDelta, bitDelta := uint64(0), uint64(0), uint64(0); i <= ef.numBuckets; i, cumDelta, bitDelta = i+1, cumDelta+ef.cumKeysMinDelta, bitDelta+ef.posMinDelta {
		if ef.lCumKeys != 0 {
			efSetBits(ef.lowerBits, i*(ef.lCumKeys+ef.lPosition), (cumKeys[i]-cumDelta)&ef.lowerBitsMaskCumKeys)
		}
		efSet(ef.upperBitsCumKeys, ((cumKeys[i]-cumDelta)>>ef.lCumKeys)+i)

		if ef.lPosition != 0 {
			efSetBits(ef.lowerBits, i*(ef.lCumKeys+ef.lPosition)+ef.lCumKeys, (position[i]-bitDelta)&ef.lowerBitsMaskPosition)
		}
		efSet(ef.upperBitsPosition, ((position[i]-bitDelta)>>ef.lPosition)+i)
	}

	// Jump table for upperBitsCumKeys (CumKeys side: idx is `idx16 = 4*jumpSuperQ+8+2*jumpInsideSuperQ`,
	// CumKeys is the EVEN slot, Position is the ODD slot at idx16+1).
	for i, c, lastSuperQ := uint64(0), uint64(0), uint64(0); i < uint64(wordsCumKeys); i++ {
		for word := ef.upperBitsCumKeys[i]; word != 0; word &= word - 1 {
			b := uint64(bits.TrailingZeros64(word))
			if (c & ef16superQMask) == 0 {
				lastSuperQ = i*64 + b
				ef.jump[(c/ef16superQ)*(ef16superQSize*2)] = lastSuperQ
			}
			if (c & ef16qMask) == 0 {
				offset := i*64 + b - lastSuperQ
				if offset >= (1 << 16) {
					panic("doubleEf: jump-table offset overflow (CumKeys side)")
				}
				jumpSuperQ := (c / ef16superQ) * (ef16superQSize * 2)
				jumpInsideSuperQ := 2 * (c % ef16superQ) / ef16q
				idx64 := jumpSuperQ + 2 + (jumpInsideSuperQ >> 2)
				shift := 16 * (jumpInsideSuperQ % 4)
				mask := uint64(0xffff) << shift
				ef.jump[idx64] = (ef.jump[idx64] &^ mask) | (offset << shift)
			}
			c++
		}
	}

	// Jump table for upperBitsPosition (Position side; idx16+1, hence jumpInsideSuperQ shifted by 1).
	for i, c, lastSuperQ := uint64(0), uint64(0), uint64(0); i < uint64(wordsPosition); i++ {
		for word := ef.upperBitsPosition[i]; word != 0; word &= word - 1 {
			b := uint64(bits.TrailingZeros64(word))
			if (c & ef16superQMask) == 0 {
				lastSuperQ = i*64 + b
				ef.jump[(c/ef16superQ)*(ef16superQSize*2)+1] = lastSuperQ
			}
			if (c & ef16qMask) == 0 {
				offset := i*64 + b - lastSuperQ
				if offset >= (1 << 16) {
					panic("doubleEf: jump-table offset overflow (Position side)")
				}
				jumpSuperQ := (c / ef16superQ) * (ef16superQSize * 2)
				jumpInsideSuperQ := 2*((c%ef16superQ)/ef16q) + 1
				idx64 := jumpSuperQ + 2 + (jumpInsideSuperQ >> 2)
				shift := 16 * (jumpInsideSuperQ % 4)
				mask := uint64(0xffff) << shift
				ef.jump[idx64] = (ef.jump[idx64] &^ mask) | (offset << shift)
			}
			c++
		}
	}
}

// Data exposes the underlying word slice (used by Write).
func (ef *DoubleEliasFano) Data() []uint64 { return ef.data }

// Write serializes the DoubleEliasFano state.
// Header: 5 * 8B BE (numBuckets, uCumKeys, uPosition, cumKeysMinDelta, posMinDelta)
// Payload: `len(data) * 8` bytes of host-LE uint64s.
//
// Port of eliasfano16/elias_fano.go:502-530 — the unsafe slice-cast is
// replaced by binary.LittleEndian.
func (ef *DoubleEliasFano) Write(w io.Writer) error {
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], ef.numBuckets)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(numBuf[:], ef.uCumKeys)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(numBuf[:], ef.uPosition)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(numBuf[:], ef.cumKeysMinDelta)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(numBuf[:], ef.posMinDelta)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	scratch := make([]byte, 8*len(ef.data))
	for i, x := range ef.data {
		binary.LittleEndian.PutUint64(scratch[i*8:(i+1)*8], x)
	}
	_, err := w.Write(scratch)
	return err
}
