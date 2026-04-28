package nethermind

// Options carries Nethermind-specific knobs that don't fit naturally on
// generator.Config (which is shared across client backends).
//
// Currently both fields are reserved for an in-process boot validation
// that wasn't wired up in this PR — Tier 3 boot smoke runs as a Docker
// `make smoke-nethermind` target instead. The fields are still parsed so
// callers can pre-set them; runImpl ignores them today.
type Options struct {
	// NethermindBin is an absolute path to a Nethermind.Runner binary
	// to spawn for a post-write boot validation. Empty disables the
	// validation. Currently unused; reserved for follow-up wiring.
	NethermindBin string

	// SkipBootValidation skips the post-write Nethermind.Runner boot
	// smoke. Currently unused; reserved for follow-up wiring.
	SkipBootValidation bool
}
