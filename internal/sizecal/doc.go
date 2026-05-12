// Package sizecal converts a target on-disk byte budget into a synthetic
// storage-slot count, per client. The factor is the empirical bytes-per-
// slot ratio observed when state-actor's writer emits N slots and we
// measure the resulting on-disk delta (`dirSize`).
//
// Why per-client: each writer has a different storage trie + DB layout.
// reth's MDBX compact-encoded StorageEntry plus the four storage tables
// it touches per slot is denser than geth's Pebble PathDB layout, which
// is itself different from nethermind's RocksDB HalfPath layout and besu's
// Bonsai trie. The bytes-per-slot ratio differs by a factor of ~1.3x
// across clients today.
//
// The v1 factors are hand-tuned initial guesses. A follow-up calibration
// benchmark (build tag `calibration` in calibrate_test.go) will fit them
// empirically against real workloads and write the updated table back
// to factors.json. The static-default `Load()` consults the JSON at
// runtime; CI's nightly calibration workflow regenerates it.
//
// The bytes-per-slot factor is approximate (±25% accuracy expected for
// v1). For users who need exact slot counts, the YAML schema also accepts
// an explicit `parameters.holders: N` on ERC-20 entries — see
// internal/templates/erc20.go.
package sizecal
