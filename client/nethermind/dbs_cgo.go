//go:build cgo_neth

package nethermind

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/linxGnu/grocksdb"
)

// nethDBNames mirrors the Nethermind.Db/DbNames.cs constants we need for
// genesis-bootability. State, Code, Blocks, Headers, BlockNumbers, and
// BlockInfos are simple single-CF databases. Receipts has 3 column
// families (default / Transactions / Blocks) per
// Nethermind.Db/ReceiptsColumns.cs:7-11; we open all 3 even though
// genesis only writes to the Blocks CF (an empty receipts list at the
// genesis row).
const (
	dbNameState        = "state"
	dbNameCode         = "code"
	dbNameBlocks       = "blocks"
	dbNameHeaders      = "headers"
	dbNameBlockNumbers = "blockNumbers"
	dbNameBlockInfos   = "blockInfos"
	dbNameReceipts     = "receipts"
)

// receiptsCFNames must match Nethermind.Db/ReceiptsColumns.cs exactly.
// The default CF is named "default" (lowercase) per DbOnTheRocks.cs:175,
// which case-maps "Default" → "default" at open time.
var receiptsCFNames = []string{"default", "Transactions", "Blocks"}

// nethDBs holds the open grocksdb handles state-actor writes during a
// Nethermind genesis emission. Caller closes via Close() when done.
type nethDBs struct {
	state        *grocksdb.DB
	code         *grocksdb.DB
	blocks       *grocksdb.DB
	headers      *grocksdb.DB
	blockNumbers *grocksdb.DB
	blockInfos   *grocksdb.DB

	receipts         *grocksdb.DB
	receiptsCFs      []*grocksdb.ColumnFamilyHandle // [default, Transactions, Blocks]
	receiptsBlocksCF *grocksdb.ColumnFamilyHandle   // alias to receiptsCFs[2]

	// Held for Destroy() during Close — grocksdb requires explicit
	// option-bag cleanup or it leaks C++ allocations.
	openedOpts []*grocksdb.Options
}

// openNethDBs opens (or creates) the 7 RocksDB instances directly under
// dataDir/<name>/. dataDir typically comes from the user's --db flag, and
// matches Nethermind's `BaseDbPath` convention 1:1 — point Nethermind at
// the same path state-actor wrote to and it finds the DBs immediately, no
// subdir gymnastics. (Earlier revisions used a `db/` subdir to mirror
// geth's `geth/chaindata/` convention; that produced silent boot failures
// because Nethermind opened freshly-created empty DBs at dataDir/<name>/
// and ignored the populated ones at dataDir/db/<name>/.)
//
// On any error, partially-opened DBs are closed before returning so
// callers don't have to handle a half-initialized struct.
func openNethDBs(dataDir string) (*nethDBs, error) {
	dbRoot := dataDir

	// grocksdb's CreateIfMissing only creates the leaf directory, not its
	// parents. Pre-create the per-DB subdirs so the open call succeeds on
	// a fresh dataDir.
	for _, name := range []string{
		dbNameState, dbNameCode, dbNameBlocks, dbNameHeaders,
		dbNameBlockNumbers, dbNameBlockInfos, dbNameReceipts,
	} {
		if err := os.MkdirAll(filepath.Join(dbRoot, name), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir %s: %w", name, err)
		}
	}

	dbs := &nethDBs{}

	// Track everything we open so Close() / cleanup works in either path.
	cleanup := func() {
		dbs.Close()
	}

	// Helper: open a single-CF database with create-if-missing.
	open := func(name string) (*grocksdb.DB, error) {
		path := filepath.Join(dbRoot, name)
		opts := grocksdb.NewDefaultOptions()
		opts.SetCreateIfMissing(true)
		dbs.openedOpts = append(dbs.openedOpts, opts)

		db, err := grocksdb.OpenDb(opts, path)
		if err != nil {
			return nil, fmt.Errorf("open %s db at %s: %w", name, path, err)
		}
		return db, nil
	}

	var err error
	if dbs.state, err = open(dbNameState); err != nil {
		cleanup()
		return nil, err
	}
	if dbs.code, err = open(dbNameCode); err != nil {
		cleanup()
		return nil, err
	}
	if dbs.blocks, err = open(dbNameBlocks); err != nil {
		cleanup()
		return nil, err
	}
	if dbs.headers, err = open(dbNameHeaders); err != nil {
		cleanup()
		return nil, err
	}
	if dbs.blockNumbers, err = open(dbNameBlockNumbers); err != nil {
		cleanup()
		return nil, err
	}
	if dbs.blockInfos, err = open(dbNameBlockInfos); err != nil {
		cleanup()
		return nil, err
	}

	// Receipts: 3 column families. grocksdb requires per-CF Options;
	// passing the same default for each is fine for state-actor's
	// genesis-only writes (we don't tune compaction etc. — Nethermind
	// rewrites those metadata files itself on first read).
	receiptsPath := filepath.Join(dbRoot, dbNameReceipts)
	receiptsOpts := grocksdb.NewDefaultOptions()
	receiptsOpts.SetCreateIfMissing(true)
	receiptsOpts.SetCreateIfMissingColumnFamilies(true)
	dbs.openedOpts = append(dbs.openedOpts, receiptsOpts)

	cfOpts := make([]*grocksdb.Options, len(receiptsCFNames))
	for i := range cfOpts {
		cfOpts[i] = grocksdb.NewDefaultOptions()
		dbs.openedOpts = append(dbs.openedOpts, cfOpts[i])
	}

	receiptsDB, cfHandles, err := grocksdb.OpenDbColumnFamilies(
		receiptsOpts, receiptsPath, receiptsCFNames, cfOpts,
	)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open receipts db at %s: %w", receiptsPath, err)
	}
	dbs.receipts = receiptsDB
	dbs.receiptsCFs = cfHandles
	dbs.receiptsBlocksCF = cfHandles[2] // index 2 = "Blocks" per receiptsCFNames

	return dbs, nil
}

// Close releases all open grocksdb resources. Safe to call multiple times
// and on partially-opened structs.
func (d *nethDBs) Close() {
	for _, h := range d.receiptsCFs {
		if h != nil {
			h.Destroy()
		}
	}
	d.receiptsCFs = nil
	d.receiptsBlocksCF = nil

	for _, db := range []**grocksdb.DB{
		&d.state, &d.code, &d.blocks, &d.headers,
		&d.blockNumbers, &d.blockInfos, &d.receipts,
	} {
		if *db != nil {
			(*db).Close()
			*db = nil
		}
	}

	for _, o := range d.openedOpts {
		o.Destroy()
	}
	d.openedOpts = nil
}
