//go:build cgo_besu

package besu

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/internal/besu/keys"
)

// Besu's RocksDB layout differs structurally from Nethermind's. Where
// Nethermind opens 7 separate RocksDB instances under <datadir>/<name>/,
// Besu opens ONE instance under <datadir>/database/ with N declared column
// families (KeyValueStorageProvider.java; verified at TIer 1 §A).
//
// We declare the 8 CFs Besu Bonsai expects on a fresh DB. Even
// TRIE_LOG_STORAGE — which receives no writes during genesis — must be
// declared on OpenDb or RocksDB will reject the open later when Besu adds
// it (per RocksDBColumnarKeyValueStorage.java:144-179, the factory passes
// its full segments list to RocksDB.listColumnFamilies on every open and
// fails if any declared CF is missing).

// CF indices into besuDB.cfs. Must match the order of cfNames in openBesuDB.
const (
	cfIdxDefault = iota
	cfIdxBlockchain
	cfIdxAccountInfoState
	cfIdxCodeStorage
	cfIdxAccountStorageStorage
	cfIdxTrieBranchStorage
	cfIdxTrieLogStorage
	cfIdxVariables
)

// besuDB holds the open grocksdb handle and the 8 CF handles. Caller closes
// via Close() when done.
type besuDB struct {
	db   *grocksdb.DB
	cfs  []*grocksdb.ColumnFamilyHandle
	path string

	// Held for Destroy() during Close. grocksdb requires explicit option-bag
	// cleanup or it leaks C++ allocations.
	dbOpts        *grocksdb.Options
	cfOpts        []*grocksdb.Options
	tableOpts     []*grocksdb.BlockBasedTableOptions
	bloomFilter   *grocksdb.NativeFilterPolicy
}

// openBesuDB creates a fresh Besu Bonsai RocksDB at <datadir>/database/.
//
// Fresh-dir precondition: refuses to open if <datadir>/database/ already
// exists. Mirrors nethermind's e4722af partial-state hazard fix —
// without this, a re-run on top of a half-finished previous run could leave
// the genesis block keys, world-state sentinels, and chainHeadHash in
// inconsistent states, with Besu then keying its boot off whichever wrote
// last. Combined with the "VARIABLES['chainHeadHash'] LAST" write order in
// genesis_cgo.go, this gives us "all-or-nothing within a single run".
//
// Note: we open with vanilla grocksdb.OpenDbColumnFamilies, NOT
// OpenOptimisticTransactionDb. The on-disk file format is identical between
// the two open modes (the optimistic-tx wrapper only adds in-memory
// conflict checking at commit time), so Besu's OptimisticTransactionDB boot
// reads what we write here.
func openBesuDB(datadir string) (*besuDB, error) {
	dbPath := filepath.Join(datadir, "database")

	// Fresh-dir precondition.
	if _, err := os.Stat(dbPath); err == nil {
		return nil, fmt.Errorf(
			"--db=%s already contains a Besu DB at database/. "+
				"Refusing to write into it: a partial previous run could leave the world-state "+
				"sentinels and chainHeadHash inconsistent. Pass --db= to a fresh path, or "+
				"`rm -rf %s` first.",
			datadir, datadir,
		)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("besu: stat %s: %w", dbPath, err)
	}

	if err := os.MkdirAll(datadir, 0o755); err != nil {
		return nil, fmt.Errorf("besu: mkdir datadir: %w", err)
	}

	// CF names, in the order matching the cfIdx* constants above. CF byte
	// names are LITERAL bytes (single-byte 0x01..0x0b for the segment-id
	// CFs, UTF-8 "default" for the default CF) per
	// KeyValueSegmentIdentifier.java:27-77. See internal/besu/keys/cf.go
	// for the per-CF byte definitions.
	cfNames := []string{
		string(keys.CFDefault),               // {default}
		string(keys.CFBlockchain),            // {1} — BlobDB on
		string(keys.CFAccountInfoState),      // {6}
		string(keys.CFCodeStorage),           // {7}
		string(keys.CFAccountStorageStorage), // {8}
		string(keys.CFTrieBranchStorage),     // {9}
		string(keys.CFTrieLogStorage),        // {10} — declared, BlobDB on, NO writes
		string(keys.CFVariables),             // {11}
	}

	// Match Besu's per-CF settings from RocksDBColumnarKeyValueStorage.java:
	//   - LZ4 compression for ALL CFs (line 229)
	//   - Block-based table with format_version=5 (line 76), block_size=32KB
	//     (line 77), BloomFilter(10, false), partition_filters=true,
	//     cacheIndexAndFilterBlocks=false (lines 283-297)
	//   - Dynamic level compaction (default true)
	//   - BlobDB on BLOCKCHAIN and TRIE_LOG_STORAGE only (containsStaticData=true
	//     in KeyValueSegmentIdentifier.java:29, :41)
	//
	// Note: we tune defaults to match Besu's writer so files produced here
	// "look like" what Besu would write itself. RocksDB doesn't strictly
	// require this — Besu can read files written with different per-CF
	// options — but matching reduces the surface area for "looks weird,
	// might silently re-tune on first open" failures.
	bf := grocksdb.NewBloomFilter(10)

	mkTable := func() *grocksdb.BlockBasedTableOptions {
		t := grocksdb.NewDefaultBlockBasedTableOptions()
		t.SetBlockSize(32 << 10) // 32 KB
		t.SetFormatVersion(5)
		t.SetFilterPolicy(bf)
		// SetPartitionFilters not exposed by grocksdb — accept default.
		// SetCacheIndexAndFilterBlocks not exposed by grocksdb — accept default.
		return t
	}

	cfOpts := make([]*grocksdb.Options, len(cfNames))
	tableOpts := make([]*grocksdb.BlockBasedTableOptions, len(cfNames))
	for i := range cfNames {
		opts := grocksdb.NewDefaultOptions()
		opts.SetCompression(grocksdb.LZ4Compression)
		opts.SetLevelCompactionDynamicLevelBytes(true)
		t := mkTable()
		opts.SetBlockBasedTableFactory(t)
		// BlobDB on BLOCKCHAIN ({1}) and TRIE_LOG_STORAGE ({10}) only —
		// matches RocksDBColumnarKeyValueStorage.configureBlobDBForSegment
		// at lines 239-263.
		if i == cfIdxBlockchain || i == cfIdxTrieLogStorage {
			opts.EnableBlobFiles(true)
			opts.SetMinBlobSize(100)
			opts.SetBlobCompressionType(grocksdb.LZ4Compression)
			// Garbage collection: Besu enables it on TRIE_LOG_STORAGE
			// (staticDataGarbageCollectionEnabled=true at
			// KeyValueSegmentIdentifier.java:41). For BLOCKCHAIN it's
			// off by default. We mirror.
			if i == cfIdxTrieLogStorage {
				opts.EnableBlobGC(true)
			}
		}
		cfOpts[i] = opts
		tableOpts[i] = t
	}

	dbOpts := grocksdb.NewDefaultOptions()
	dbOpts.SetCreateIfMissing(true)
	dbOpts.SetCreateIfMissingColumnFamilies(true)
	dbOpts.SetMaxTotalWalSize(1 << 30) // 1 GB — Besu's default
	dbOpts.SetKeepLogFileNum(7)        // 1 week of daily rotation

	db, cfHandles, err := grocksdb.OpenDbColumnFamilies(
		dbOpts, dbPath, cfNames, cfOpts,
	)
	if err != nil {
		// Free the option bags we'd otherwise have leaked.
		dbOpts.Destroy()
		for _, o := range cfOpts {
			o.Destroy()
		}
		for _, t := range tableOpts {
			t.Destroy()
		}
		bf.Destroy()
		return nil, fmt.Errorf("besu: open RocksDB at %s: %w", dbPath, err)
	}

	return &besuDB{
		db:          db,
		cfs:         cfHandles,
		path:        dbPath,
		dbOpts:      dbOpts,
		cfOpts:      cfOpts,
		tableOpts:   tableOpts,
		bloomFilter: bf,
	}, nil
}

// Close releases all open grocksdb resources. Safe to call multiple times
// and on partially-initialized structs.
func (b *besuDB) Close() {
	for _, h := range b.cfs {
		if h != nil {
			h.Destroy()
		}
	}
	b.cfs = nil

	if b.db != nil {
		b.db.Close()
		b.db = nil
	}

	for _, t := range b.tableOpts {
		if t != nil {
			t.Destroy()
		}
	}
	b.tableOpts = nil

	for _, o := range b.cfOpts {
		if o != nil {
			o.Destroy()
		}
	}
	b.cfOpts = nil

	if b.dbOpts != nil {
		b.dbOpts.Destroy()
		b.dbOpts = nil
	}

	if b.bloomFilter != nil {
		b.bloomFilter.Destroy()
		b.bloomFilter = nil
	}
}

// put writes a key/value to the named CF. Fast path for single writes; bulk
// writes use writeBatch (see node_sink_cgo.go).
func (b *besuDB) put(cfIdx int, key, value []byte) error {
	wo := grocksdb.NewDefaultWriteOptions()
	defer wo.Destroy()
	return b.db.PutCF(wo, b.cfs[cfIdx], key, value)
}

// putSync writes with sync=true. Used for the final chainHeadHash write to
// guarantee ordered durability per the partial-state hazard mitigation
// (mirrors nethermind e4722af blockInfos-last write order).
func (b *besuDB) putSync(cfIdx int, key, value []byte) error {
	wo := grocksdb.NewDefaultWriteOptions()
	defer wo.Destroy()
	wo.SetSync(true)
	return b.db.PutCF(wo, b.cfs[cfIdx], key, value)
}
