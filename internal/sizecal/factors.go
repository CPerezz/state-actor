package sizecal

// SizeApproximator translates a target on-disk byte budget into a synthetic
// storage-slot count. Implementations no longer branch on client name —
// the parameter is preserved for forward-compatibility with a future opt-in
// that lets a caller override per-client.
//
// The interface is duplicated (in shape) in internal/templates/template.go
// to avoid an import cycle. Both interfaces are signature-compatible.
type SizeApproximator interface {
	SlotsForBytes(client string, targetBytes uint64) int
}

// bytesPerSlot is the on-disk TRIE-only byte cost per storage slot, calibrated
// to geth's empirical 136 B/slot from the May-17 bloatnet run.
//
// Empirical derivation from `geth db inspect`:
//
//	Path trie storage nodes   : 261.94 GiB / 2,261,206,613 items (uncompressed)
//	Storage snapshot (flat)   : 149.92 GiB / 1,643,328,993 items
//	Total uncompressed        : 411.86 GiB
//	On-disk (du -sh)          : 334 GiB  ⇒ Pebble compression ratio ≈ 0.81
//
// Trie on disk = 261.94 × 0.81 ≈ 212 GB / 1.56 B slots ≈ 136 B/slot.
// Rounded up to 140 for safety against trie-depth growth at deeper scales
// + Pebble compaction variance.
//
// IMPORTANT: this is TRIE-only. Flat-state (Pebble `o` snapshot rows on geth,
// Bonsai flat rows on besu, reth's 4 MDBX flat tables) is ADDITIONAL on-disk
// bytes and NOT counted toward target_bytes. Per-client snapshot adds-on:
// geth ~78 B, besu ~74 B, reth ~70-100 B, nethermind 0 (no flat state).
//
// Cross-client cross-check: nethermind has no flat state, so its entire
// 186 GB / 1.56 B = 119 B/slot is trie-only. Two independent measurements
// (Pebble Path-trie vs RocksDB HalfPath) converging on ~120-140 is strong
// empirical evidence.
const bytesPerSlot uint64 = 140

// bytesPerAccount is the on-disk TRIE-only byte cost per account, calibrated
// to geth's account-trie cost. Empirical anchor from May-17 `db inspect`'s
// path-trie-account-nodes table (165 KiB / 1414 nodes ≈ 120 B/account in the
// truncated run), rounded up to 175 for safety since that bench was capped by
// Bug A — a real-world wider account-trie amortises slightly higher.
const bytesPerAccount uint64 = 175

// Default returns the package-level SizeApproximator backed by the single
// global trie-only constant. Same value for every client — preserves the
// cross-client genesis-root invariance gate (same YAML → same slot count →
// same state root).
func Default() SizeApproximator {
	return defaultSizer{}
}

// NewFixed returns a SizeApproximator that always uses the given bytes-per-
// slot ratio regardless of client. Used by the cross-client CI invariance
// suite (sizecal.NewFixed(64)) to force byte-identical PreAlloc across the
// four clients in tests; that calibration is explicitly decoupled from the
// production value so production-sizing drift can't mask a writer regression.
func NewFixed(bytesPerSlot uint64) SizeApproximator {
	return fixedSizer{bytesPerSlot: bytesPerSlot}
}

// BytesPerSlot returns the global trie-only on-disk B/slot cost. The client
// parameter is ignored — kept for API stability.
func BytesPerSlot(_ string) uint64 {
	return bytesPerSlot
}

// BytesPerAccount returns the global trie-only on-disk B/account cost. The
// client parameter is ignored — kept for API stability.
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
