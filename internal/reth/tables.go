package reth

// TableSpec describes one MDBX named DB: its reth table name and whether the
// table uses DupSort semantics. Names match crates/storage/db-api/src/tables/mod.rs
// at PinnedRethCommit exactly — drift = silent boot failure.
type TableSpec struct {
	Name    string
	DupSort bool
}

// Tables is the canonical registry of every MDBX named DB reth declares.
// Order is the same as the tables! macro in mod.rs:308-540.
var Tables = []TableSpec{
	{Name: "CanonicalHeaders"},
	{Name: "HeaderTerminalDifficulties"},
	{Name: "HeaderNumbers"},
	{Name: "Headers"},
	{Name: "BlockBodyIndices"},
	{Name: "BlockOmmers"},
	{Name: "BlockWithdrawals"},
	{Name: "Transactions"},
	{Name: "TransactionHashNumbers"},
	{Name: "TransactionBlocks"},
	{Name: "Receipts"},
	{Name: "Bytecodes"},
	{Name: "PlainAccountState"},
	{Name: "PlainStorageState", DupSort: true},
	{Name: "AccountsHistory"},
	{Name: "StoragesHistory"},
	{Name: "AccountChangeSets", DupSort: true},
	{Name: "StorageChangeSets", DupSort: true},
	{Name: "HashedAccounts"},
	{Name: "HashedStorages", DupSort: true},
	{Name: "AccountsTrie"},
	{Name: "StoragesTrie", DupSort: true},
	{Name: "TransactionSenders"},
	{Name: "StageCheckpoints"},
	{Name: "StageCheckpointProgresses"},
	{Name: "PruneCheckpoints"},
	{Name: "VersionHistory"},
	{Name: "ChainState"},
	{Name: "Metadata"},
}
