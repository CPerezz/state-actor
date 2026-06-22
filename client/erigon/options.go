package erigon

// Options is the per-client option bag passed to Run. Mirrors the
// pattern other clients use (`besu.Run(ctx, cfg, besu.Options{})`,
// `nethermind.Run(ctx, cfg, nethermind.Options{})`, etc.). Erigon always
// writes the snapshot tier, so there are no options today.
type Options struct{}
