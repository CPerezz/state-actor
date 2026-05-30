package erigon

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

// MDBX env geometry — mirrors Erigon's kv_mdbx.go defaults so the
// daemon's compatibility check passes when it later reopens chaindata.
const (
	headerPatchPageSize    = 4096
	headerPatchMapSize     = 4 * 1024 * 1024 * 1024 * 1024
	headerPatchGrowthStep  = 4 * 1024 * 1024 * 1024
	headerPatchMaxDBs uint = 200
)

// Bucket names — verbatim from upstream Erigon's kv.* constants
// (db/kv/tables.go on commit 14273f79a6). Note: kv.Headers's string is
// "Header" (singular) and kv.HeadBlockKey / kv.HeadHeaderKey are
// SINGLE-KEY tables whose own bucket name doubles as the only key in
// them — that's upstream's convention, not a state-actor invention.
const (
	bucketHeaders         = "Header"
	bucketHeaderCanonical = "CanonicalHeader"
	bucketHeaderNumber    = "HeaderNumber"
	bucketHeaderTD        = "HeadersTotalDifficulty"
	bucketBlockBody       = "BlockBody"
	bucketLastBlock       = "LastBlock"
	bucketLastHeader      = "LastHeader"
	bucketConfig          = "Config"
)

// openChaindataEnv opens the chaindata MDBX env at <dbPath>/chaindata
// with the geometry erigon's compatibility check expects. Caller owns
// env.Close().
func openChaindataEnv(dbPath string) (*mdbx.Env, error) {
	chaindataDir := filepath.Join(dbPath, "chaindata")

	env, err := mdbx.NewEnv(mdbx.Label("chaindata"))
	if err != nil {
		return nil, fmt.Errorf("mdbx.NewEnv: %w", err)
	}
	if err := env.SetOption(mdbx.OptMaxDB, uint64(headerPatchMaxDBs)); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.SetOption(MaxDB): %w", err)
	}
	if err := env.SetGeometry(-1, -1, headerPatchMapSize, headerPatchGrowthStep, -1, headerPatchPageSize); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.SetGeometry: %w", err)
	}
	if err := env.Open(chaindataDir, mdbx.Durable, 0o644); err != nil {
		env.Close()
		return nil, fmt.Errorf("mdbx.Open(%s): %w", chaindataDir, err)
	}
	return env, nil
}

// strictGet wraps txn.Get with a descriptive error on missing rows.
// Returns a wrapped error that includes the bucket label + key shape
// when mdbx.IsNotFound(err) is true. Strict mode: a missing required
// row is a fatal error, signalling that upstream's genesis-write set
// has drifted from what patchGenesisHeaderStateRoot expects.
func strictGet(txn *mdbx.Txn, dbi mdbx.DBI, key []byte, label string) ([]byte, error) {
	v, err := txn.Get(dbi, key)
	if err != nil {
		if mdbx.IsNotFound(err) {
			return nil, fmt.Errorf("%s: key %x not found (genesis-write set drift?)", label, key)
		}
		return nil, fmt.Errorf("%s: Get(%x): %w", label, key, err)
	}
	return v, nil
}

// patchGenesisHeaderStateRoot rewrites block 0's header.stateRoot in
// the chaindata MDBX env AND re-keys every dependent table to use the
// new block hash. This is the only way to keep chaindata internally
// consistent after mutating Header.Root, because Header.Hash() =
// keccak256(rlp(Header)) — mutating Root changes the hash, so EVERY
// table that was keyed by or stored the old hash must be rewritten.
//
// 8 tables are mutated, all atomically in a single MDBX env.Update():
//
//	1. CanonicalHeader  [BE(0)]            value: oldHash       -> newHash
//	2. Header           [BE(0) || hash]    REKEY old->new + new RLP value
//	3. HeaderNumber     [hash]             REKEY old->new (value BE(0) preserved)
//	4. HeadersTotalDifficulty [BE(0) || hash] REKEY old->new (RLP TD preserved)
//	5. BlockBody        [BE(0) || hash]    REKEY old->new (RLP body preserved)
//	6. LastBlock        ["LastBlock"]      value: oldHash       -> newHash
//	7. LastHeader       ["LastHeader"]     value: oldHash       -> newHash
//	8. Config           [hash]             REKEY old->new (JSON chain.Config preserved)
//
// MaxTxNum is NOT re-keyed because its key is BE(blockNum) alone with
// no hash component — patching the Root doesn't invalidate it.
//
// Required because `erigon init` writes block 0 with whatever Root its
// empty genesis alloc produced. State-actor's snapshot writer then
// emits all bloat into the snapshot tier — the daemon's first FCU
// recomputes commitment over visible (snapshot) state and rejects the
// erigon-init root. This patch overwrites that Root with the
// HPH-over-everything value computed by commitment.ComputeGenesisRoot.
//
// Without the full re-key (only Header[oldKey] = newRLP, the
// pre-2026-05-30 behavior), the resulting MDBX is four-way
// inconsistent: 7 other tables still reference oldHash; the new RLP
// sits under the wrong Header key; no chain lookup by newHash
// resolves. engine_forkchoiceUpdated falls through to SYNCING and
// the daemon never opens RoSnapshots.ready.
//
// Strict mode: returns a fatal error if any of the 8 tables is
// missing its expected row. This catches upstream genesis-write-set
// drift (a new Erigon pin removing or renaming a table) at the first
// bench instead of silently producing a broken chaindata.
func patchGenesisHeaderStateRoot(dbPath string, root common.Hash) error {
	env, err := openChaindataEnv(dbPath)
	if err != nil {
		return err
	}
	defer env.Close()

	return env.Update(func(txn *mdbx.Txn) error {
		canonicalDBI, err := txn.OpenDBI(bucketHeaderCanonical, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketHeaderCanonical, err)
		}
		headersDBI, err := txn.OpenDBI(bucketHeaders, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketHeaders, err)
		}
		headerNumDBI, err := txn.OpenDBI(bucketHeaderNumber, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketHeaderNumber, err)
		}
		tdDBI, err := txn.OpenDBI(bucketHeaderTD, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketHeaderTD, err)
		}
		bodyDBI, err := txn.OpenDBI(bucketBlockBody, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketBlockBody, err)
		}
		lastBlockDBI, err := txn.OpenDBI(bucketLastBlock, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketLastBlock, err)
		}
		lastHeaderDBI, err := txn.OpenDBI(bucketLastHeader, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketLastHeader, err)
		}
		configDBI, err := txn.OpenDBI(bucketConfig, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketConfig, err)
		}

		blockNumKey := make([]byte, 8)
		binary.BigEndian.PutUint64(blockNumKey, 0)

		oldHash, err := strictGet(txn, canonicalDBI, blockNumKey, bucketHeaderCanonical)
		if err != nil {
			return err
		}
		if len(oldHash) != 32 {
			return fmt.Errorf("%s[0]: len=%d, want 32", bucketHeaderCanonical, len(oldHash))
		}
		oldHeadersKey := append(append(make([]byte, 0, 8+32), blockNumKey...), oldHash...)

		headerRLP, err := strictGet(txn, headersDBI, oldHeadersKey, bucketHeaders)
		if err != nil {
			return err
		}
		var h types.Header
		if err := rlp.DecodeBytes(headerRLP, &h); err != nil {
			return fmt.Errorf("RLP decode block-0 header: %w", err)
		}
		h.Root = root
		newRLP, err := rlp.EncodeToBytes(&h)
		if err != nil {
			return fmt.Errorf("RLP encode patched block-0 header: %w", err)
		}
		newHash := h.Hash()
		newHeadersKey := append(append(make([]byte, 0, 8+32), blockNumKey...), newHash[:]...)

		// 1. Header — rekey + new RLP value.
		if err := txn.Del(headersDBI, oldHeadersKey, nil); err != nil {
			return fmt.Errorf("Del(%s, BE(0)||oldHash): %w", bucketHeaders, err)
		}
		if err := txn.Put(headersDBI, newHeadersKey, newRLP, 0); err != nil {
			return fmt.Errorf("Put(%s, BE(0)||newHash): %w", bucketHeaders, err)
		}

		// 2. CanonicalHeader — overwrite the singleton-by-blockNum value.
		if err := txn.Put(canonicalDBI, blockNumKey, newHash[:], 0); err != nil {
			return fmt.Errorf("Put(%s, BE(0)): %w", bucketHeaderCanonical, err)
		}

		// 3. HeaderNumber — rekey (delete oldHash entry, put newHash entry).
		storedBlockNum, err := strictGet(txn, headerNumDBI, oldHash, bucketHeaderNumber)
		if err != nil {
			return err
		}
		if !bytes.Equal(storedBlockNum, blockNumKey) {
			return fmt.Errorf("%s[oldHash]: stored blockNum=%x, want %x", bucketHeaderNumber, storedBlockNum, blockNumKey)
		}
		if err := txn.Del(headerNumDBI, oldHash, nil); err != nil {
			return fmt.Errorf("Del(%s, oldHash): %w", bucketHeaderNumber, err)
		}
		if err := txn.Put(headerNumDBI, newHash[:], blockNumKey, 0); err != nil {
			return fmt.Errorf("Put(%s, newHash): %w", bucketHeaderNumber, err)
		}

		// 4. HeadersTotalDifficulty — rekey (preserve RLP TD value).
		tdVal, err := strictGet(txn, tdDBI, oldHeadersKey, bucketHeaderTD)
		if err != nil {
			return err
		}
		if err := txn.Del(tdDBI, oldHeadersKey, nil); err != nil {
			return fmt.Errorf("Del(%s, BE(0)||oldHash): %w", bucketHeaderTD, err)
		}
		if err := txn.Put(tdDBI, newHeadersKey, tdVal, 0); err != nil {
			return fmt.Errorf("Put(%s, BE(0)||newHash): %w", bucketHeaderTD, err)
		}

		// 5. BlockBody — rekey (preserve RLP body value).
		bodyVal, err := strictGet(txn, bodyDBI, oldHeadersKey, bucketBlockBody)
		if err != nil {
			return err
		}
		if err := txn.Del(bodyDBI, oldHeadersKey, nil); err != nil {
			return fmt.Errorf("Del(%s, BE(0)||oldHash): %w", bucketBlockBody, err)
		}
		if err := txn.Put(bodyDBI, newHeadersKey, bodyVal, 0); err != nil {
			return fmt.Errorf("Put(%s, BE(0)||newHash): %w", bucketBlockBody, err)
		}

		// 6. LastBlock — overwrite singleton value (table-name == sole key).
		if err := txn.Put(lastBlockDBI, []byte(bucketLastBlock), newHash[:], 0); err != nil {
			return fmt.Errorf("Put(%s, %q): %w", bucketLastBlock, bucketLastBlock, err)
		}

		// 7. LastHeader — overwrite singleton value (table-name == sole key).
		if err := txn.Put(lastHeaderDBI, []byte(bucketLastHeader), newHash[:], 0); err != nil {
			return fmt.Errorf("Put(%s, %q): %w", bucketLastHeader, bucketLastHeader, err)
		}

		// 8. Config — rekey the 32-byte hash entry (preserve JSON value).
		// NOTE: Config also holds an entry under the kv.GenesisKey string
		// (the full genesis-JSON), keyed by a fixed string rather than the
		// hash. We MUST NOT touch that one. Reading Config[oldHash]
		// targets the 32-byte hash entry specifically.
		cfgVal, err := strictGet(txn, configDBI, oldHash, bucketConfig)
		if err != nil {
			return err
		}
		if err := txn.Del(configDBI, oldHash, nil); err != nil {
			return fmt.Errorf("Del(%s, oldHash): %w", bucketConfig, err)
		}
		if err := txn.Put(configDBI, newHash[:], cfgVal, 0); err != nil {
			return fmt.Errorf("Put(%s, newHash): %w", bucketConfig, err)
		}

		return nil
	})
}
