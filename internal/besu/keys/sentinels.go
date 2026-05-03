package keys

// Sentinel keys for the TRIE_BRANCH_STORAGE and VARIABLES column families.
// All sentinel key bytes are the literal UTF-8 encoding of the key name string.
//
// Citations:
//   - TRIE_BRANCH_STORAGE sentinels: PathBasedWorldStateKeyValueStorage.java:65-73
//   - flatDbStatus: FlatDbStrategyProvider.java:38, FlatDbMode.java:38-42
//   - VARIABLES: VariablesKeyValueStorage.java:28, 44-46
//   - worldBlockNumber: PathBasedWorldState.java:219-225
//
// (besu tag 26.5.0)

// Sentinel keys in TRIE_BRANCH_STORAGE.
var (
	// WorldRootKey is the TRIE_BRANCH_STORAGE key that holds the 32-byte
	// current world state root hash.
	// PathBasedWorldStateKeyValueStorage.java:66 — "worldRoot" UTF-8, 9 bytes.
	WorldRootKey = []byte("worldRoot")

	// WorldBlockHashKey is the TRIE_BRANCH_STORAGE key that holds the 32-byte
	// genesis block hash.
	// PathBasedWorldStateKeyValueStorage.java:68 — "worldBlockHash" UTF-8, 14 bytes.
	WorldBlockHashKey = []byte("worldBlockHash")

	// WorldBlockNumberKey is the TRIE_BRANCH_STORAGE key that holds the
	// 8-byte big-endian block number (Bytes.ofUnsignedLong(N)).
	// For genesis (block 0): 8 zero bytes.
	// PathBasedWorldStateKeyValueStorage.java:71 — "worldBlockNumber" UTF-8, 16 bytes.
	WorldBlockNumberKey = []byte("worldBlockNumber")

	// FlatDbStatusKey is the TRIE_BRANCH_STORAGE key that holds the
	// FlatDbMode byte: 0x00=PARTIAL, 0x01=FULL, 0x02=ARCHIVE.
	// Must be written as 0x01 (FULL) to avoid trie-fallback degradation.
	// FlatDbStrategyProvider.java:38 — "flatDbStatus" UTF-8, 12 bytes.
	FlatDbStatusKey = []byte("flatDbStatus")

	// FlatDbStatusFull is the value to write to FlatDbStatusKey to signal
	// that the flat database is complete (FULL mode).
	// FlatDbMode.java:41 — FULL = Bytes.of(0x01).
	FlatDbStatusFull = []byte{0x01}

	// WorldBlockNumberGenesis is the value for WorldBlockNumberKey at block 0.
	// Bytes.ofUnsignedLong(0) returns 8 zero bytes.
	// PathBasedWorldState.java:224.
	WorldBlockNumberGenesis = []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
)

// Sentinel keys in VARIABLES CF.
var (
	// ChainHeadHashKey is the VARIABLES CF key that holds the 32-byte
	// genesis block hash (chain head pointer).
	// VariablesKeyValueStorage.java:28,44 — "chainHeadHash" UTF-8, 13 bytes.
	ChainHeadHashKey = []byte("chainHeadHash")
)
