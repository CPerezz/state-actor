package besu

// Options carries Besu-specific knobs that don't fit naturally on
// generator.Config (which is shared across client backends).
//
// Both fields are reserved for an in-process post-write boot validation that
// isn't wired up yet — boot smoke is currently a Docker `make smoke-besu`
// target. The fields are still parsed so callers can pre-set them; runImpl
// ignores them today.
type Options struct {
	// BesuBin is an absolute path to a Besu binary (or `besu` shell
	// wrapper) to spawn for a post-write boot validation. Empty disables
	// the validation. Currently unused; reserved for follow-up wiring.
	BesuBin string

	// SkipBootValidation skips the post-write Besu boot smoke. Currently
	// unused; reserved for follow-up wiring.
	SkipBootValidation bool
}
