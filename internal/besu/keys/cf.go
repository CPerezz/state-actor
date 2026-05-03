// Package keys provides column-family byte identifiers, sentinel key byte
// slices, and BLOCKCHAIN CF prefix key constructors for the Besu Bonsai
// RocksDB database.
//
// Column family names: Besu passes segment.getId() directly as the CF
// descriptor name. Each ID is a SINGLE byte (not UTF-8 text). The default
// CF uses the literal UTF-8 string "default" as required by RocksDB.
//
// Citation: KeyValueSegmentIdentifier.java:27-77 (besu tag 26.5.0).
package keys

// Column family byte-slice identifiers.
// These are the exact bytes Besu passes to ColumnFamilyDescriptor.
// A single-byte CF name is NOT the same as the digit character "1" (0x31) —
// it is the raw byte 0x01.
var (
	// CFBlockchain is the BLOCKCHAIN column family. Stores block headers,
	// bodies, receipts, canonical hash index, and total difficulty.
	// BlobDB is ENABLED for this CF.
	// KeyValueSegmentIdentifier.java:29 — getId() returns new byte[]{1}.
	CFBlockchain = []byte{1}

	// CFAccountInfoState is the ACCOUNT_INFO_STATE column family.
	// Stores Bonsai flat account state: key=keccak256(addr), value=RLP account.
	// KeyValueSegmentIdentifier.java:37 — getId() returns new byte[]{6}.
	CFAccountInfoState = []byte{6}

	// CFCodeStorage is the CODE_STORAGE column family.
	// Stores contract bytecode: key=keccak256(code), value=bytecode (default mode).
	// KeyValueSegmentIdentifier.java:38 — getId() returns new byte[]{7}.
	CFCodeStorage = []byte{7}

	// CFAccountStorageStorage is the ACCOUNT_STORAGE_STORAGE column family.
	// Stores Bonsai flat storage: key=keccak256(addr)++keccak256(slot), value=RLP.
	// KeyValueSegmentIdentifier.java:39 — getId() returns new byte[]{8}.
	CFAccountStorageStorage = []byte{8}

	// CFTrieBranchStorage is the TRIE_BRANCH_STORAGE column family.
	// Stores Bonsai path-keyed trie nodes and sentinel values (worldRoot, etc.).
	// KeyValueSegmentIdentifier.java:40 — getId() returns new byte[]{9}.
	CFTrieBranchStorage = []byte{9}

	// CFTrieLogStorage is the TRIE_LOG_STORAGE column family.
	// Must be declared on DB open (Besu rejects open if CF in DB but not declared)
	// but receives NO writes for genesis-only generation.
	// BlobDB is ENABLED for this CF.
	// KeyValueSegmentIdentifier.java:41 — getId() returns new byte[]{10}.
	CFTrieLogStorage = []byte{10}

	// CFVariables is the VARIABLES column family.
	// Stores the chain head pointer: key="chainHeadHash", value=32B genesis hash.
	// KeyValueSegmentIdentifier.java:66 — getId() returns new byte[]{11}.
	CFVariables = []byte{11}

	// CFDefault is the mandatory default RocksDB column family.
	// RocksDB requires "default" to be declared on open; no writes needed.
	// Name is UTF-8 string "default", NOT a single-byte ID.
	CFDefault = []byte("default")
)
