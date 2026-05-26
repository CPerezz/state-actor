package erigon

// Options carries Erigon-specific knobs that don't fit naturally on
// generator.Config (which is shared across client backends).
//
// All fields are reserved for the Phase C orchestrator (Tasks 70-77 in
// /Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md) and the
// oracle test harness; the !cgo_erigon stub ignores them today.
type Options struct {
	// ErigonBin is an absolute path to an `erigon` binary (or shell
	// wrapper) to spawn for a post-write boot validation in oracle
	// tests. Empty disables the validation / defers to the Docker image
	// pinned in internal/erigon/constants.go. Reserved for follow-up
	// wiring; runImpl ignores it today.
	ErigonBin string

	// SkipCommitment, when true, omits the Commitment domain .kv files.
	// Zero value (false) = SHIP commitment — the writer builds the
	// commitment domain itself via an internal HexPatriciaHashed walk
	// over the Accounts/Storage/Code domains.
	//
	// **Why default-ship**: per Verifier A's review of the plan
	// (§ Critical Verifier Corrections > Correction 1 in
	// /Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md),
	// the original "let Erigon rebuild on first boot" idea is WRONG:
	// `RebuildCommitmentFilesWithHistory` (`db/state/squeeze.go:436`)
	// is CLI-only (`cmd/integration/commands/commitment.go:397`), never
	// called by the `cmd/erigon` daemon. A datadir missing commitment
	// files won't boot. State-actor MUST ship commitment in v1.
	//
	// The inverse-named SkipCommitment lets Options{}'s zero value carry
	// the safe default (ship). Reserved for follow-up wiring; runImpl
	// ignores it today. v2 callers can flip to SkipCommitment=true once
	// `cmd/integration commitment rebuild` is wired into the bench-host
	// pre-boot step.
	SkipCommitment bool

	// ShipHistory, when true, additionally produces .v / .vi (history)
	// and .ef / .efi (inverted-index) snapshot files. False in v1 — the
	// value + commitment domains alone are sufficient for an Erigon node
	// to boot and serve RPC at the canonical state. Archive-mode reads
	// (`eth_getProof` at historical blocks) require the history files;
	// defer to v2.
	//
	// Reserved for follow-up wiring; runImpl ignores it today.
	ShipHistory bool

	// SkipBootValidation skips the post-write Erigon boot smoke in
	// oracle tests. Currently unused; reserved for follow-up wiring.
	SkipBootValidation bool

	// WriteSnapshots, when true, runs the pure-Go snap.Writer pass
	// AFTER `erigon init`, emitting .kv/.bt/.kvei (value domains) and
	// .kv/.kvi/.kvei (commitment domain) under <dbPath>/snapshots/.
	//
	// Defaults to FALSE: the bench's working path is `erigon init` alone
	// (Phase A.5 hot-tier scaffolding). The snap.Writer pass is layered
	// in as a side-by-side artifact for byte-equality verification + the
	// long-term Architect-B transition; flipping the default to true
	// requires the commitment-trie computation to ship (Plan Task 72)
	// since Erigon's reader needs commitment in EITHER MDBX or cold
	// snapshots, not neither.
	WriteSnapshots bool
}
