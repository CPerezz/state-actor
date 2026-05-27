package erigon

// Options is the per-client option bag passed to Run. It is currently
// empty — the type is retained so the Run signature matches the other
// clients (`besu.Run(ctx, cfg, besu.Options{})`,
// `nethermind.Run(ctx, cfg, nethermind.Options{})`, etc.) and so future
// knobs can be added without touching main.go.
type Options struct{}
