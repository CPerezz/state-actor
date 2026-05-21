package sizecal

// SizeApproximator translates a target on-disk byte budget into a synthetic
// storage-slot count. The client parameter is reserved for per-client overrides;
// the default implementation ignores it.
type SizeApproximator interface {
	SlotsForBytes(client string, targetBytes uint64) int
}

// bytesPerSlot is the TRIE-only on-disk B/slot cost. Flat-state rows (Pebble
// snapshot, Bonsai flat, reth MDBX flat tables) are ADDITIONAL and not counted
// toward target_bytes. Calibration anchor and per-client flat-state overhead
// numbers live in docs/CALIBRATION.md.
const bytesPerSlot uint64 = 140

// bytesPerAccount is the TRIE-only on-disk B/account cost.
const bytesPerAccount uint64 = 175

// Default returns the package-level SizeApproximator backed by the single
// global trie-only constant. Identical across clients — required by the
// cross-client genesis-root invariance gate.
func Default() SizeApproximator {
	return defaultSizer{}
}

// NewFixed returns a SizeApproximator that always uses the given bytes-per-slot
// ratio. Used by tests to decouple test sizing from the production constant.
func NewFixed(bytesPerSlot uint64) SizeApproximator {
	return fixedSizer{bytesPerSlot: bytesPerSlot}
}

// BytesPerSlot returns the global trie-only on-disk B/slot cost.
func BytesPerSlot(_ string) uint64 {
	return bytesPerSlot
}

// BytesPerAccount returns the global trie-only on-disk B/account cost.
func BytesPerAccount(_ string) uint64 {
	return bytesPerAccount
}

type defaultSizer struct{}

func (defaultSizer) SlotsForBytes(_ string, targetBytes uint64) int {
	if bytesPerSlot == 0 {
		return 0
	}
	return int(targetBytes / bytesPerSlot)
}

type fixedSizer struct{ bytesPerSlot uint64 }

func (s fixedSizer) SlotsForBytes(_ string, targetBytes uint64) int {
	if s.bytesPerSlot == 0 {
		return 0
	}
	return int(targetBytes / s.bytesPerSlot)
}
