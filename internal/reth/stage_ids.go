package reth

// StageIDsAll lists the canonical stage IDs reth writes to StageCheckpoints
// during init_genesis. Sourced from
// /Users/random_anon/dev/clients/reth/crates/stages/types/src/id.rs at
// PinnedRethCommit.
//
// Each entry's value type in the StageCheckpoints table is StageCheckpoint
// (just BlockNumber for our use case; see types.go).
//
// If reth adds/renames a stage, this slice and the test in
// stage_ids_test.go must be updated in lockstep.
var StageIDsAll = []string{
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
