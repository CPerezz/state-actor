//go:build cgo_erigon

package erigon

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
)

// writeSyncStageMarkers opens the chaindata MDBX created by `erigon init`
// and writes SyncStage progress rows so that the daemon's boot-time
// `AllSegmentsDownloadComplete` gate
// (db/rawdb/accessors_metadata.go:118-121) returns true.
//
// Without this, `engine_forkchoiceUpdated` against a genesis-only-init'd
// chaindata responds `SYNCING` forever — block production never starts.
// Verified via 15 bench-iteration attempts on stateless-bloatnet-benchmarks
// (2026-05-26); the relevant Erigon code:
//
//	func AllSegmentsDownloadComplete(tx kv.Getter) (bool, error) {
//	    snap, _ := stages.GetStageProgress(tx, stages.Snapshots)
//	    return snap > 0, err
//	}
//
// `stages.Snapshots` is keyed `"OtterSync"` on disk (see
// erigon/execution/stagedsync/stages/stages.go:35). We write 1 so the
// `>0` gate clears. Other stages are written to 0 (the no-progress
// sentinel) so the stage runner's `minProgress` arithmetic doesn't
// race backward when the stage spins up.
//
// MDBX schema reference: kv.SyncStageProgress = "SyncStage" table
// (erigon/db/kv/tables.go:90). Value encoding: 8-byte big-endian uint64
// (erigon/execution/stagedsync/stages/stages.go:126-130).
//
// Architect-B invariant preserved: this code does NOT import
// `github.com/erigontech/erigon`. It uses `mdbx-go` (already a
// state-actor dep via reth) and the SyncStage byte schema from the
// Erigon source as a reference, hardcoded here.
func writeSyncStageMarkers(dbPath string) error {
	chaindataDir := filepath.Join(dbPath, "chaindata")

	// mdbx-go v0.40+ requires a Label argument to NewEnv. The label is
	// telemetry-only; using "chaindata" for parity with Erigon's own naming
	// (db/kv/kv_mdbx.go labels its primary env "chaindata"). The empty/zero
	// label is also accepted by the binding.
	env, err := mdbx.NewEnv(mdbx.Label("chaindata"))
	if err != nil {
		return fmt.Errorf("mdbx.NewEnv: %w", err)
	}
	defer env.Close()

	// Match erigon's MDBX env config: 200 max DBs (per Erigon's
	// kv_mdbx.go default), page size auto-detected from existing file.
	if err := env.SetOption(mdbx.OptMaxDB, 200); err != nil {
		return fmt.Errorf("mdbx.SetOption(OptMaxDB): %w", err)
	}

	// Read-write open. Use plain Durable so erigon's next open sees
	// the writes through fsync. Don't use WriteMap / NoMemInit here
	// because erigon will reopen with its own flags and we don't want
	// to influence that.
	const flags = mdbx.Durable
	if err := env.Open(chaindataDir, flags, 0o644); err != nil {
		return fmt.Errorf("mdbx.Open(%s): %w", chaindataDir, err)
	}

	// Stage entries to write. Names are the on-disk keys from
	// execution/stagedsync/stages/stages.go:35-46.
	type stageEntry struct {
		Name     string
		Progress uint64
	}
	stages := []stageEntry{
		// Snapshots gate: must be > 0 for AllSegmentsDownloadComplete.
		// Value 1 = "first block has been seen"; matches genesis at
		// block 0 + 1 system tx.
		{"OtterSync", 1},
		// Other stages: 0 is the no-progress sentinel. erigon's stage
		// runner sees this as "stage not yet run" rather than
		// "missing"; the FCU flow handles 0 correctly post the gate.
		{"Headers", 0},
		{"BlockHashes", 0},
		{"Bodies", 0},
		{"Senders", 0},
		{"Execution", 0},
		{"CustomTrace", 0},
		{"TxLookup", 0},
		{"Finish", 0},
	}

	return env.Update(func(txn *mdbx.Txn) error {
		// SyncStage DBI must already exist (erigon init creates it).
		dbi, err := txn.OpenDBI("SyncStage", 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(SyncStage): %w", err)
		}
		var valBuf [8]byte
		for _, s := range stages {
			binary.BigEndian.PutUint64(valBuf[:], s.Progress)
			if err := txn.Put(dbi, []byte(s.Name), valBuf[:], 0); err != nil {
				return fmt.Errorf("Put(SyncStage[%s]): %w", s.Name, err)
			}
		}
		return nil
	})
}
