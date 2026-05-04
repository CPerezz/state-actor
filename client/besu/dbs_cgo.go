//go:build cgo_besu

package besu

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/internal/besu/keys"
)

// Besu opens ONE RocksDB instance under <datadir>/database/ with all 8 column
// families declared. Even TRIE_LOG_STORAGE (no genesis-time writes) must be
// declared on OpenDb — RocksDB compares the open's CF list against on-disk
// CFs and fails if any is missing on subsequent reopens.

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
// Refuses to open into an existing dir: re-running on top of a partial run
// could leave genesis block keys, world-state sentinels, and chainHeadHash
// inconsistent, with Besu booting off whichever wrote last.
//
// We open with plain OpenDbColumnFamilies, not OpenOptimisticTransactionDb —
// the on-disk file format is identical (the optimistic-tx wrapper only adds
// in-memory conflict checking), so Besu's OptimisticTransactionDB boot reads
// what we write here.
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

	// CF names use LITERAL bytes (0x01..0x0b for segment CFs, UTF-8 "default"
	// for the default CF) per KeyValueSegmentIdentifier.java:27-77. Order
	// must match the cfIdx* constants above.
	cfNames := []string{
		string(keys.CFDefault),
		string(keys.CFBlockchain),
		string(keys.CFAccountInfoState),
		string(keys.CFCodeStorage),
		string(keys.CFAccountStorageStorage),
		string(keys.CFTrieBranchStorage),
		string(keys.CFTrieLogStorage),
		string(keys.CFVariables),
	}

	// Match Besu's per-CF settings from RocksDBColumnarKeyValueStorage.java:
	// LZ4 compression, block-based table format_version=5, 32KB blocks,
	// BloomFilter(10), dynamic-level compaction, BlobDB on BLOCKCHAIN +
	// TRIE_LOG_STORAGE only. Matching avoids "files look weird, Besu silently
	// re-tunes on first open" surprises.
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
		if i == cfIdxBlockchain || i == cfIdxTrieLogStorage {
			opts.EnableBlobFiles(true)
			opts.SetMinBlobSize(100)
			opts.SetBlobCompressionType(grocksdb.LZ4Compression)
			// Besu enables blob GC on TRIE_LOG_STORAGE only.
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
// guarantee ordered durability — chainHeadHash MUST be the last on-disk write
// or a crash mid-write can leave Besu booting against partial state.
func (b *besuDB) putSync(cfIdx int, key, value []byte) error {
	wo := grocksdb.NewDefaultWriteOptions()
	defer wo.Destroy()
	wo.SetSync(true)
	return b.db.PutCF(wo, b.cfs[cfIdx], key, value)
}
