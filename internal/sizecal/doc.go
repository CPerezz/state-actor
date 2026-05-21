// Package sizecal converts a target on-disk byte budget into a synthetic
// storage-slot count via a single global trie-only bytesPerSlot constant.
//
// "Trie-only" means: flat-state (Pebble snapshot rows, Bonsai flat rows,
// reth's MDBX flat tables) is ADDITIONAL on-disk usage and NOT counted
// toward target_bytes. The user-facing --target-size flag targets the
// trie DB component, not the total disk footprint.
//
// The CI invariance gate uses NewFixed(64) — an explicit calibration
// decoupled from the production value so test sizing can't silently mask
// a Default() drift.
package sizecal
