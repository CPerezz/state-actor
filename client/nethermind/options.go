package nethermind

// Options carries Nethermind-specific knobs that don't fit naturally on
// generator.Config (which is shared across client backends).
//
// SkipBootValidation defaults to false; B6's Tier 3 boot smoke test sets
// it to true when running against a stub Nethermind binary. NethermindBin
// is the path to a Nethermind.Runner build that the post-write boot smoke
// will exec; left empty in production runs (no boot smoke).
//
// Stage 1 (this commit): both fields are accepted but ignored — Run
// returns "not yet implemented" before either takes effect.
type Options struct {
	// NethermindBin is an absolute path to a Nethermind.Runner binary
	// to spawn for a post-write boot validation. Empty disables the
	// validation. Only consulted when SkipBootValidation is false.
	NethermindBin string

	// SkipBootValidation skips the post-write Nethermind.Runner boot
	// smoke. Use in unit tests that don't have a real Nethermind binary
	// available; production runs leave it false.
	SkipBootValidation bool
}
