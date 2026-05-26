package eliasfano

import "errors"

// Sentinel errors. Erigon panics on these; we prefer error returns to
// match the rest of state-actor's writer-package conventions.
var (
	// ErrNonMonotonic is returned by AddOffset when the supplied offset
	// is less than the previously-added offset (or, equivalently, when
	// the resulting bit position would precede the last one).
	ErrNonMonotonic = errors.New("eliasfano: offsets must be monotonically non-decreasing")
	// ErrTooMany is returned when AddOffset is called more times than
	// the builder's count.
	ErrTooMany = errors.New("eliasfano: too many offsets for declared count")
	// ErrOutOfRange is returned when an offset exceeds maxOffset.
	ErrOutOfRange = errors.New("eliasfano: offset exceeds maxOffset")
	// ErrBuiltAlready signals AddOffset / Build called after Build.
	ErrBuiltAlready = errors.New("eliasfano: already built")
)

// setBits stores `value` at bit position `start` within `bits`. Caller is
// responsible for the layout invariant (monotonic writes; +1 padding word
// on lowerBits). Ported verbatim from elias_fano.go:975-980.
//
// Verifier B note: Erigon uses `_ = bits[idx64+1]` as a bounds-check hint
// to prove BCE; we keep the same pattern.
func setBits(bits []uint64, start uint64, value uint64) {
	idx64 := start >> 6
	shift := uint(start & 63)
	_ = bits[idx64+1]
	bits[idx64] |= value << shift
	// When shift == 0, value >> 64 in C is UB; in Go it's defined as 0,
	// but the value-bytes upper write is still semantically a no-op.
	// Erigon's note at elias_fano.go:974: "When shift+width <= 64,
	// value>>(64-shift) == 0, so the write is a no-op."
	if shift > 0 {
		bits[idx64+1] |= value >> (64 - shift)
	}
}

// set turns on a single bit at position `pos`. Ported from
// elias_fano.go:982-985.
func set(bits []uint64, pos uint64) {
	bits[pos/64] |= uint64(1) << (pos % 64)
}

// AddOffset appends `offset` to the encoded sequence. Offsets MUST be
// monotonically non-decreasing and bounded by maxOffset.
//
// Mirrors `(ef *EliasFano) AddOffset(offset uint64)` at elias_fano.go:166-175,
// with monotonicity + range checks added (Erigon trusts callers).
func (b *Builder) AddOffset(offset uint64) error {
	if b.built {
		return ErrBuiltAlready
	}
	if b.i > b.count {
		return ErrTooMany
	}
	if offset > b.maxOffset {
		return ErrOutOfRange
	}
	if offset < b.lastAdded {
		return ErrNonMonotonic
	}
	if b.l != 0 {
		setBits(b.lowerBits, b.i*b.l, offset&b.lowerBitsMask)
	}
	set(b.upperBits, (offset>>b.l)+b.i)
	b.i++
	b.lastAdded = offset
	return nil
}
