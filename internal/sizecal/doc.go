// Package sizecal converts a target on-disk byte budget into a synthetic
// storage-slot count. The single global constant bytesPerSlot is calibrated
// to the heaviest trie-only on-disk B/slot client (geth empirical, 136 B/slot
// from `geth db inspect` on the May-17 bloatnet run, + safety margin → 140).
//
// "Trie-only" means: flat-state (Pebble snapshot rows on geth, Bonsai flat
// rows on besu, reth's 4 MDBX flat tables) is ADDITIONAL on-disk disk usage
// and is NOT counted toward target_bytes. The user-facing target-size flag
// targets the trie DB component, not the total disk.
//
// Per-client trie-on-disk landings under the calibrated 140 B/slot:
//   - geth       ~97 % of target_bytes (calibration baseline)
//   - nethermind ~85 % of target_bytes (no flat state; entire DB is trie)
//   - besu       close to target (final % refined after current bench)
//   - reth       ~15-20 % of target_bytes (MDBX DupSort packs StoragesTrie
//                very densely; structural outlier on the LOW side)
//
// CI invariance gate (`internal/e2e_testing/spec_setup.go`) uses NewFixed(64)
// — an explicit calibration decoupled from the production value so a future
// drift in Default() can't silently mask a writer regression at unit-test
// level. Same effect holds for Default() post-refactor (one constant for all
// clients), but the explicit decoupling stays for hygiene.
package sizecal
