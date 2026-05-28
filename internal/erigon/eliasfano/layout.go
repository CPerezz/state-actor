package eliasfano

import "math/bits"

// Layout constants ported verbatim from Erigon's
// db/recsplit/eliasfano32/elias_fano.go:44-52. Changing any of these
// invalidates the wire format.
const (
	log2q      uint64 = 8
	q          uint64 = 1 << log2q          // 256
	qMask      uint64 = q - 1               // 0xff
	superQ     uint64 = 1 << 14             // 16384
	superQMask uint64 = superQ - 1          // 0x3fff
	qPerSuperQ uint64 = superQ / q          // 64
	superQSize uint64 = 1 + qPerSuperQ/2    // 33

	uint64Size = 8 // bytes per uint64; Erigon's const uint64Size at elias_fano.go:796
)

// jumpSizeWords returns the count of uint64 words the jump table occupies
// for a builder with this count value. Mirrors `(ef *EliasFano) jumpSizeWords()`
// at elias_fano.go:177-183.
func (b *Builder) jumpSizeWords() int {
	size := ((b.count + 1) / superQ) * superQSize
	if (b.count+1)%superQ != 0 {
		// Partial block: 1 header word + ⌈(partial-q-count + 3)/2⌉.
		size += 1 + (((b.count+1)%superQ+q-1)/q+3)/2
	}
	return int(size)
}

// computeLayout derives the bit-width `l` and the word counts that
// partition `b.data`. Shared by `deriveFields` (the heap path).
// Mirrors elias_fano.go:187-195.
func (b *Builder) computeLayout() (l uint64, wordsLowerBits, wordsUpperBits, totalWords int) {
	if b.u/(b.count+1) != 0 {
		// l = ⌊log₂(u/(count+1))⌋ via leading-zero count.
		l = 63 ^ uint64(bits.LeadingZeros64(b.u/(b.count+1)))
	}
	// +1 padding word on lowerBits — load-bearing per Erigon's note at
	// elias_fano.go:191. setBits writes one word past the bit-array end
	// when the value crosses a 64-bit boundary; the padding prevents OOB.
	wordsLowerBits = int(((b.count+1)*l+63)/64 + 1)
	wordsUpperBits = int((b.count + 1 + (b.u >> l) + 63) / 64)
	totalWords = wordsLowerBits + wordsUpperBits + b.jumpSizeWords()
	return
}

// deriveFields finalizes layout-derived fields on the Builder and
// allocates b.data accordingly. Mirrors elias_fano.go:197-216 (heap path).
func (b *Builder) deriveFields() int {
	l, wordsLowerBits, wordsUpperBits, totalWords := b.computeLayout()
	b.l = l
	b.lowerBitsMask = (uint64(1) << b.l) - 1
	if cap(b.data) < totalWords {
		b.data = make([]uint64, totalWords)
	} else {
		b.data = b.data[:totalWords]
		clear(b.data)
	}
	b.lowerBits = b.data[:wordsLowerBits]
	b.upperBits = b.data[wordsLowerBits : wordsLowerBits+wordsUpperBits]
	b.jump = b.data[wordsLowerBits+wordsUpperBits:]
	return wordsUpperBits
}
