package erigon

// Options is the per-client option bag passed to Run. Mirrors the
// pattern other clients use (`besu.Run(ctx, cfg, besu.Options{})`,
// `nethermind.Run(ctx, cfg, nethermind.Options{})`, etc.).
type Options struct {
	// WriteSnapshots toggles the streaming snapshot orchestrator (plan
	// PART 5). When true (production default), the bloat data is
	// written into Erigon's flat snapshot files
	// (accounts/storage/code/commitment .kv + accessors). When false,
	// only `erigon init` runs — the bloat is built into the alloc map
	// but discarded. Useful for isolating MDBX-side bugs from
	// snapshot-side bugs during development.
	//
	// Default (zero value) is currently false during the refactor
	// landing window; will flip to true (via Run setting it) once
	// PART 5 lands.
	WriteSnapshots bool
}
