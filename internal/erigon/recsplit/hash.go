package recsplit

import (
	"math/bits"

	"github.com/spaolacci/murmur3"
)

// remix mixes the bits of a 64-bit integer using David Stafford's 13th
// MurmurHash3 finalizer variant. Ported verbatim from recsplit.go:81-85.
//
//	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
//	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
//	z = z ^ (z >> 31)
//
// Used by findBijection / findSplit to derive per-key bucket positions
// from (fingerprint + salt).
func remix(z uint64) uint64 {
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// remap converts a uniformly-distributed uint64 x into a value in [0, n)
// using fixed-point multiplication (Lemire's bias-free mod alternative).
// Ported verbatim from recsplit.go:421-425.
//
//	hi, _ = bits.Mul64(x, n)
//
// hi = floor(x*n / 2^64) is the result; the discarded lo is x*n mod 2^64.
// Compared with `x % n`, this avoids division and the small modulo bias
// when n is not a power of two.
func remap(x uint64, n uint64) (hi uint64) {
	hi, _ = bits.Mul64(x, n)
	return hi
}

// mask48 keeps the low 48 bits.
const mask48 uint64 = (1 << 48) - 1

// remap16 is the n<2^16 specialization of remap. Ported verbatim from
// recsplit.go:431-433:
//
//	return uint16(((x & mask48) * uint64(n)) >> 48)
//
// The 48-bit truncation is load-bearing: it keeps the intermediate
// product (x & mask48) * n bounded by 2^48 * 2^16 = 2^64, avoiding overflow.
func remap16(x uint64, n uint16) uint16 {
	return uint16(((x & mask48) * uint64(n)) >> 48)
}

// keyHash returns the (hi, lo) murmur3-128 hash of `key` seeded by `salt`.
// Matches `murmur3.Sum128WithSeed(key, salt)` at recsplit.go:521.
// The bucket index is `remap(hi, bucketCount)`; the per-bucket fingerprint
// is `lo`.
//
// Wrapping the call here lets us swap implementations in a hash-vector
// unit test without touching the writer.
func keyHash(key []byte, salt uint32) (hi, lo uint64) {
	return murmur3.Sum128WithSeed(key, salt)
}
