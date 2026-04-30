package reth

import (
	"sort"
	"testing"
)

// expectedStageIDs is a hand-checked manifest of reth's StageId::ALL at
// PinnedRethCommit. Sourced from
// /Users/random_anon/dev/clients/reth/crates/stages/types/src/id.rs.
//
// On reth version bumps, update both this manifest AND StageIDsAll in
// stage_ids.go in the same commit.
var expectedStageIDs = []string{
	"Era",
	"Headers",
	"Bodies",
	"SenderRecovery",
	"Execution",
	"PruneSenderRecovery",
	"MerkleUnwind",
	"AccountHashing",
	"StorageHashing",
	"MerkleExecute",
	"TransactionLookup",
	"IndexStorageHistory",
	"IndexAccountHistory",
	"Prune",
	"Finish",
}

func TestStageIDsAllMatchManifest(t *testing.T) {
	got := append([]string(nil), StageIDsAll...)
	want := append([]string(nil), expectedStageIDs...)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("StageIDsAll len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("stage[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStageIDsAllNoDuplicates(t *testing.T) {
	seen := make(map[string]bool, len(StageIDsAll))
	for _, s := range StageIDsAll {
		if seen[s] {
			t.Errorf("duplicate stage ID: %q", s)
		}
		seen[s] = true
	}
}
