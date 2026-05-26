//go:build cgo_erigon

package mdbx

import (
	"fmt"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
)

// Geometry constants. Mirrors Erigon's kv_mdbx.go defaults:
//
//   - PageSize 4096: explicit pin to avoid darwin (16 KiB) vs linux
//     (4 KiB) divergence — Verifier A's Correction 6.
//   - MapSize 4 TiB: matches reth's pin (internal/reth/dbs_cgo.go) and
//     gives headroom for 25 GB bench plus post-spamoor growth.
//   - GrowthStep 4 GiB: faster commits than Erigon's default 2 GiB at
//     the cost of slightly more sparse virtual address.
//   - MaxDBs 200: Erigon's kv_mdbx.go default.
const (
	PageSize    = 4096
	MapSize     = 4 * 1024 * 1024 * 1024 * 1024
	GrowthStep  = 4 * 1024 * 1024 * 1024
	MaxDBs uint = 200
)

// Env wraps an open MDBX environment and the resolved DBI handles for
// the tables this package writes. Always close via Env.Close.
type Env struct {
	Env  *mdbx.Env
	DBIs map[string]mdbx.DBI
}

// OpenForWrite opens (or creates) the MDBX env at <dbPath>/chaindata
// with read-write access + Erigon's geometry. Resolves DBI handles for
// the tables in tables.go.
//
// Caller MUST close via env.Close() to release the file lock; Erigon's
// daemon won't be able to reopen the chaindata otherwise.
func OpenForWrite(dbPath string) (*Env, error) {
	chaindataDir := filepath.Join(dbPath, "chaindata")

	env, err := mdbx.NewEnv(mdbx.Label("chaindata"))
	if err != nil {
		return nil, fmt.Errorf("mdbx.NewEnv: %w", err)
	}
	if err := env.SetOption(mdbx.OptMaxDB, uint64(MaxDBs)); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.SetOption(MaxDB): %w", err)
	}
	// SetGeometry args: sizeLower, sizeNow, sizeUpper, growthStep,
	// shrinkThreshold, pageSize. -1 means "current/auto" for the field.
	if err := env.SetGeometry(-1, -1, MapSize, GrowthStep, -1, PageSize); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.SetGeometry: %w", err)
	}
	// Durable mode — fsync on commit. Don't use WriteMap / NoMemInit
	// here because Erigon's daemon will reopen with its own flags.
	if err := env.Open(chaindataDir, mdbx.Durable, 0o644); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.Open(%s): %w", chaindataDir, err)
	}

	out := &Env{
		Env:  env,
		DBIs: make(map[string]mdbx.DBI, 6),
	}
	// Open the DBIs we will write to. They MUST already exist (erigon
	// init created them in Phase A); pass the SAME flags Erigon used
	// at creation so MDBX's compatibility check passes.
	//
	// DupSort: tables where multiple values can share a key. Identified
	// via grep against db/kv/tables.go's ChaindataTablesCfg DupSort map.
	// All "Vals", "Idx", "HistoryKeys", "HistoryVals" for non-large-vals
	// domains are DupSort. TblCodeVals is the exception (LargeValues=true,
	// not DupSort).
	// MVP scope: only open the storage-write tables. Account/code tables
	// and headers are owned by `erigon init`; extending into them lands
	// in Phase C+D of the plan.
	type dbiSpec struct {
		name  string
		flags uint
	}
	specs := []dbiSpec{
		{TblStorageVals, mdbx.DupSort},
		{TblStorageIdx, mdbx.DupSort},
		{TblStorageHistoryKeys, mdbx.DupSort},
		{TblStorageHistoryVals, mdbx.DupSort},
	}
	for _, s := range specs {
		var dbi mdbx.DBI
		if err := env.View(func(txn *mdbx.Txn) error {
			d, err := txn.OpenDBI(s.name, s.flags, nil, nil)
			if err != nil {
				return err
			}
			dbi = d
			return nil
		}); err != nil {
			out.Close()
			return nil, fmt.Errorf("mdbx.OpenDBI(%s, flags=0x%x): %w", s.name, s.flags, err)
		}
		out.DBIs[s.name] = dbi
	}
	return out, nil
}

// Close releases the env. Idempotent.
func (e *Env) Close() {
	if e == nil || e.Env == nil {
		return
	}
	e.Env.Close()
	e.Env = nil
}
