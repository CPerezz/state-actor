package eliasfano

import "math/bits"

// Build finalizes the encoding by populating the jump table that supports
// O(1) random access into the upper-bits sequence. Must be called once,
// after all AddOffset calls, before AppendBytes/Serialize/Bytes.
//
// Mirrors `(ef *EliasFano) Build()` at elias_fano.go:233-263. The
// algorithm: for each set bit in upperBits (iterated via `word &= word-1`
// which clears the lowest set bit), record either a superQ-block header
// (at every 16384-th set bit) or an inside-superQ jump offset (at every
// 256-th set bit).
func (b *Builder) Build() {
	if b.built {
		return
	}
	var c uint64
	var lastSuperQ uint64
	for i := uint64(0); i < uint64(b.wordsUpperBits); i++ {
		for word := b.upperBits[i]; word != 0; word &= word - 1 {
			// `bits.TrailingZeros64(word)` is the bit index of the
			// lowest set bit (which `word &= word-1` will clear next iter).
			bIdx := uint64(bits.TrailingZeros64(word))
			if (c & superQMask) == 0 {
				lastSuperQ = i*64 + bIdx
				b.jump[(c/superQ)*superQSize] = lastSuperQ
			}
			if (c & qMask) != 0 {
				c++
				continue
			}
			offset := i*64 + bIdx - lastSuperQ
			// Erigon panics when offset >= (1<<32) — keep the invariant
			// here as a sanity guard rather than a runtime error since
			// it indicates a structural bug, not a user mistake.
			if offset >= (1 << 32) {
				panic("eliasfano: jump-table offset overflow (should be unreachable)")
			}
			jumpSuperQ := (c / superQ) * superQSize
			jumpInsideSuperQ := (c % superQ) / q
			idx64 := jumpSuperQ + 1 + (jumpInsideSuperQ >> 1)
			shift := uint(32 * (jumpInsideSuperQ % 2))
			mask := uint64(0xffffffff) << shift
			b.jump[idx64] = (b.jump[idx64] &^ mask) | (offset << shift)
			c++
		}
	}
	b.built = true
}
