package snap

import (
	"context"
	"fmt"

	"github.com/nerolation/state-actor/internal/streamsort"
)

// KeyCommitmentState is the on-disk key whose value carries the HPH
// trie-state header that Erigon's daemon reads on first FCU to anchor
// commitment continuation. Mirrors `commitmentdb.KeyCommitmentState`
// in upstream (`execution/commitment/commitmentdb/commitment_context.go`,
// line 587-589). The exact bytes are ASCII "state".
//
// In the snapshot commitment .kv this record lives alongside the HPH
// branch nodes. Sort position is naturally mid-stream — branch keys are
// short nibble paths typically starting with low bytes, while "state"
// is 0x73,0x74,0x61,0x74,0x65 — so streamsort's LSM sort places it
// wherever the binary comparator dictates without any special handling
// from this package.
var KeyCommitmentState = []byte("state")

// WriteCommitment emits the Commitment-domain snapshot file set
// (commitment.<from>-<to>.kv + .kvi + .kvei) for the given step range
// from a streamsort.Store of HPH branch nodes. It additionally Puts the
// KeyCommitmentState record into the store before delegating to
// WriteDomain — so the caller does NOT pre-insert it.
//
// `keyState` MUST be the output of the KeyCommitmentState value encoder
// (BE u64 txNum + BE u64 blockNum + BE u16 trieStateLen + raw
// EncodeCurrentState bytes; ~683-815 bytes for a genesis HPH). See the
// state-actor commitment package for the encoder.
//
// `branchCount` is the count of branches already Put into `branches`.
// WriteCommitment passes `branchCount+1` to WriteDomain so the bloom +
// recsplit sizing accounts for the synthetic KeyCommitmentState record.
//
// The commitment domain's default AccessorMask
// (AccessorHashMap | AccessorExistence per state_schema.go:261) is used
// — no AccessorBTree. WriteDomain emits .kvi (RecSplit MPHF) and .kvei
// (existence bloom) sidecars.
//
// Postcondition: `branches` has been iterated to completion and is
// safe to Close. WriteCommitment does NOT Close it (caller owns
// lifecycle).
func WriteCommitment(
	ctx context.Context,
	w *Writer,
	r StepRange,
	keyState []byte,
	branches *streamsort.Store,
	branchCount uint64,
) error {
	if len(keyState) == 0 {
		return fmt.Errorf("snap.WriteCommitment: keyState is empty")
	}
	if err := branches.Put(KeyCommitmentState, keyState); err != nil {
		return fmt.Errorf("snap.WriteCommitment: put KeyCommitmentState: %w", err)
	}
	return w.WriteDomain(ctx, DomainCommitment, r, branchCount+1, FromStreamsort(branches))
}

// WriteCommitmentPlaceholder emits a 1-entry commitment.<from>-<to>.kv
// containing ONLY the KeyCommitmentState record (no branch nodes).
// Used by the multi-range orchestrator for the older ranges in the
// tiered LSM pyramid layout — the daemon's first-FCU SeekCommitment reads
// only the NEWEST visible commitment file (db/state/domain.go:1290-1369
// newest-wins GetLatest), so the placeholder's KeyCommitmentState is
// inert; its sole purpose is to satisfy the integrity-checker
// AddDependencyBtwnDomains(AccountsDomain, CommitmentDomain) rule
// (state_schema.go:69) that requires a matching commitment file at
// every accounts range boundary.
//
// `keyState` is typically EncodeKeyCommitmentStateValue(0, 0, nil) —
// an empty-trie-state 18-byte header. Caller may pass a populated
// keyState if they want all ranges to carry the same anchor (no
// functional difference; daemon reads only the newest).
//
// Internally creates a fresh ephemeral streamsort.Store, puts the one
// entry, calls WriteDomain. The store is closed before returning.
func WriteCommitmentPlaceholder(
	ctx context.Context,
	w *Writer,
	r StepRange,
	keyState []byte,
) error {
	if len(keyState) == 0 {
		return fmt.Errorf("snap.WriteCommitmentPlaceholder: keyState is empty")
	}
	tmpStore, err := streamsort.New("")
	if err != nil {
		return fmt.Errorf("snap.WriteCommitmentPlaceholder: open ephemeral streamsort: %w", err)
	}
	defer tmpStore.Close()
	if err := tmpStore.Put(KeyCommitmentState, keyState); err != nil {
		return fmt.Errorf("snap.WriteCommitmentPlaceholder: put KeyCommitmentState: %w", err)
	}
	return w.WriteDomain(ctx, DomainCommitment, r, 1, FromStreamsort(tmpStore))
}
