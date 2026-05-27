//go:build cgo_erigon

package mdbx

// Erigon E3 chaindata DBI names. Mirrored from erigon's
// db/kv/tables.go at the version pinned in internal/erigon/constants.go.
// Re-running `grep -nE "Tbl(Account|Storage|Code|Commitment)Vals"
// tables.go` against a newer Erigon checkout will catch any rename.
//
// Schema reference (db/kv/tables.go:144-159):
//
//	TblAccountVals    = "AccountVals"     // latest account state
//	TblStorageVals    = "StorageVals"     // latest storage values
//	TblCodeVals       = "CodeVals"        // bytecode keyed by codeHash
//	TblCommitmentVals = "CommitmentVals"  // commitment trie branches
//
// Genesis writes hit only these four "Vals" tables plus the system-
// tables (Headers, HeaderCanonical, HeaderTD, MaxTxNum, SyncStage,
// Config) which `erigon init` has already populated via Phase A.
const (
	// TblAccountVals — latest-state account table.
	// Key:   address[20]
	// Value: account.EncodeForStorage(...) (fieldset byte + variable-length fields)
	TblAccountVals = "AccountVals"

	// TblStorageVals — latest-state storage table.
	// Key:   address[20] || slot[32] (52 bytes; no incarnation, no hashing)
	// Value: trimmed-leading-zero value bytes
	TblStorageVals = "StorageVals"

	// TblCodeVals — code blobs.
	// Key:   codeHash[32]
	// Value: code bytes
	TblCodeVals = "CodeVals"

	// TblCommitmentVals — commitment trie branch nodes.
	// Key:   trie prefix (nibble-encoded path from root)
	// Value: BranchData encoding (as produced by commitment.PutBranch)
	TblCommitmentVals = "CommitmentVals"

	// Headers — block headers.
	// Key:   block_num_u64 || hash[32]
	// Value: RLP(header)
	Headers = "Headers"

	// HeaderCanonical — canonical chain markers.
	// Key:   block_num_u64
	// Value: hash[32]
	HeaderCanonical = "CanonicalHeader"

	// History tables. All three for the Accounts/Storage/Code domains;
	// CommitmentDomain has HistoryDisabled=true so no history writes
	// even though the table names exist.
	//
	// Schema (per the schema investigation):
	//   TblXxxIdx (DupSort)         : key=primary_key, value=txNum[8]
	//   TblXxxHistoryKeys (DupSort) : key=txNum[8],    value=primary_key
	//   TblXxxHistoryVals (DupSort) : key=primary_key, value=txNum[8]||prevValue
	//
	// At genesis prevValue is empty — TblXxxHistoryVals values are
	// exactly 8 bytes (the txNum).
	TblAccountIdx         = "AccountIdx"
	TblAccountHistoryKeys = "AccountHistoryKeys"
	TblAccountHistoryVals = "AccountHistoryVals"

	TblStorageIdx         = "StorageIdx"
	TblStorageHistoryKeys = "StorageHistoryKeys"
	TblStorageHistoryVals = "StorageHistoryVals"

	TblCodeIdx         = "CodeIdx"
	TblCodeHistoryKeys = "CodeHistoryKeys"
	TblCodeHistoryVals = "CodeHistoryVals"
)
