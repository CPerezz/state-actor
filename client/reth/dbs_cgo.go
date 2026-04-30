//go:build cgo_reth

package reth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/linxGnu/grocksdb"

	iReth "github.com/nerolation/state-actor/internal/reth"
)

// Reth MDBX geometry — matches reth's default in
// crates/storage/db/src/implementation/mdbx/mod.rs:72-240.
//
// Page size: reth uses default_page_size() = OS page size clamped to
// [4096, 65536]. Passing 0 to mdbx-go is treated as MDBX_MIN_PAGESIZE
// (256 bytes), which would produce a database reth refuses to open
// on platforms where its expected page size differs (notably macOS
// arm64 with 16 KiB pages). We compute the same value reth would
// produce: clamp(os.Getpagesize(), 4096, 65536).
const (
	mdbxSizeMin      = int(0)
	mdbxSizeNow      = int(0)
	mdbxSizeMax      = int(8 * 1024 * 1024 * 1024 * 1024) // 8 TiB
	mdbxGrowthStep   = int(4 * 1024 * 1024 * 1024)        // 4 GiB
	mdbxShrinkThresh = int(0)
)

// mdbxDefaultPageSize matches reth's default_page_size() in
// crates/storage/db/src/implementation/mdbx/mod.rs:139,160.
func mdbxDefaultPageSize() int {
	ps := os.Getpagesize()
	if ps < 4096 {
		return 4096
	}
	if ps > 65536 {
		return 65536
	}
	return ps
}

// rocksdbCFNames lists the v2 history-table column families reth uses.
var rocksdbCFNames = []string{
	"default",
	"AccountsHistory",
	"StoragesHistory",
	"TransactionHashNumbers",
}

// Envs holds the open MDBX env + named DBIs and the RocksDB env + CFs.
// Caller must call Close() when done.
type Envs struct {
	Mdbx     *mdbx.Env
	MdbxDBIs map[string]mdbx.DBI

	RocksDB  *grocksdb.DB
	RocksCFs map[string]*grocksdb.ColumnFamilyHandle

	closed bool
}

// OpenEnvs creates a fresh datadir at dataDir and opens the MDBX env +
// RocksDB. freshDir=true REFUSES to open if any reth artifact (db/mdbx.dat,
// rocksdb/CURRENT, static_files/) is already present.
func OpenEnvs(dataDir string, freshDir bool) (*Envs, error) {
	if freshDir {
		if err := requireFreshDir(dataDir); err != nil {
			return nil, err
		}
	}

	dbDir := filepath.Join(dataDir, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dbDir, err)
	}
	rocksdbDir := filepath.Join(dataDir, "rocksdb")
	if err := os.MkdirAll(rocksdbDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", rocksdbDir, err)
	}

	envs := &Envs{
		MdbxDBIs: make(map[string]mdbx.DBI, len(iReth.Tables)),
		RocksCFs: make(map[string]*grocksdb.ColumnFamilyHandle, len(rocksdbCFNames)),
	}

	// --- MDBX env ---
	env, err := mdbx.NewEnv()
	if err != nil {
		return nil, fmt.Errorf("mdbx.NewEnv: %w", err)
	}

	if err := env.SetGeometry(
		mdbxSizeMin,
		mdbxSizeNow,
		mdbxSizeMax,
		mdbxGrowthStep,
		mdbxShrinkThresh,
		mdbxDefaultPageSize(),
	); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.SetGeometry: %w", err)
	}

	if err := env.SetOption(mdbx.OptMaxDB, uint64(len(iReth.Tables))); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.SetOption(OptMaxDB): %w", err)
	}

	if err := env.Open(dbDir, 0, 0o644); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.Open(%s): %w", dbDir, err)
	}
	envs.Mdbx = env

	// Pre-resolve all named DBIs. Update (write txn) is required because
	// mdbx.Create needs write access.
	if err := env.Update(func(txn *mdbx.Txn) error {
		for _, ts := range iReth.Tables {
			flags := uint(mdbx.Create)
			if ts.DupSort {
				flags |= uint(mdbx.DupSort)
			}
			dbi, err := txn.OpenDBI(ts.Name, flags, nil, nil)
			if err != nil {
				return fmt.Errorf("OpenDBI(%s): %w", ts.Name, err)
			}
			envs.MdbxDBIs[ts.Name] = dbi
		}
		return nil
	}); err != nil {
		envs.Close()
		return nil, err
	}

	// --- RocksDB env with column families ---
	rocksOpts := grocksdb.NewDefaultOptions()
	defer rocksOpts.Destroy() // C-allocated; release after OpenDbColumnFamilies returns
	rocksOpts.SetCreateIfMissing(true)
	rocksOpts.SetCreateIfMissingColumnFamilies(true)

	cfOpts := make([]*grocksdb.Options, len(rocksdbCFNames))
	for i := range cfOpts {
		cfOpts[i] = grocksdb.NewDefaultOptions()
		defer cfOpts[i].Destroy()
	}

	rdb, cfs, err := grocksdb.OpenDbColumnFamilies(rocksOpts, rocksdbDir, rocksdbCFNames, cfOpts)
	if err != nil {
		envs.Close()
		return nil, fmt.Errorf("grocksdb.OpenDbColumnFamilies: %w", err)
	}
	envs.RocksDB = rdb
	for i, name := range rocksdbCFNames {
		envs.RocksCFs[name] = cfs[i]
	}

	return envs, nil
}

// requireFreshDir errors if dataDir already contains a reth datadir.
func requireFreshDir(dataDir string) error {
	for _, p := range []string{
		filepath.Join(dataDir, "db", "mdbx.dat"),
		filepath.Join(dataDir, "rocksdb", "CURRENT"),
		filepath.Join(dataDir, "static_files"),
	} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("requireFreshDir: %s already exists; refusing to overwrite", p)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("requireFreshDir stat %s: %w", p, err)
		}
	}
	return nil
}

// Close tears down the MDBX and RocksDB environments. Idempotent.
func (e *Envs) Close() error {
	if e == nil || e.closed {
		return nil
	}
	e.closed = true
	if e.RocksDB != nil {
		for _, cf := range e.RocksCFs {
			cf.Destroy()
		}
		e.RocksDB.Close()
	}
	if e.Mdbx != nil {
		e.Mdbx.Close()
	}
	return nil
}
