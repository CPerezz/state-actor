package erigon

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"

	internalerigon "github.com/ethereum/state-actor/internal/erigon"
)

// TestPatchGenesisHeaderStateRoot_ReKeysAllTables is the regression
// gate for the bug fix: after mutating Header.Root, every one of the
// 8 hash-dependent MDBX tables must reference the NEW block hash.
// Without this all-tables-rekey, the chaindata is internally
// inconsistent and Erigon's daemon hangs in SYNCING. It also asserts
// step 9 — the "fat genesis" MaxTxNum[0]=StepSize-1 overwrite that lets
// the chain advance past block 2 (see genesis_patch.go).
//
// Setup: build a synthetic block-0 header, populate the 8 buckets with
// oldHash references plus MaxTxNum[0]=1 (as erigon init does). Action:
// call patchGenesisHeaderStateRoot. Verify: in a fresh read txn, every
// table references the newHash (old-hash entries gone) and MaxTxNum[0]
// == StepSize-1.
func TestPatchGenesisHeaderStateRoot_ReKeysAllTables(t *testing.T) {
	dbPath := t.TempDir()

	// Fixture header — block 0, deterministic.
	oldRoot := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	h := types.Header{
		ParentHash: common.Hash{},
		Number:     big.NewInt(0),
		Difficulty: big.NewInt(1),
		GasLimit:   60_000_000,
		Root:       oldRoot,
	}
	oldHash := h.Hash()
	oldRLP, err := rlp.EncodeToBytes(&h)
	if err != nil {
		t.Fatalf("rlp.EncodeToBytes(oldHeader): %v", err)
	}

	blockNumKey := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumKey, 0)
	oldHeadersKey := append(append(make([]byte, 0, 8+32), blockNumKey...), oldHash[:]...)

	// Synthetic values for the rekey-only tables (preserved verbatim).
	fakeTD := mustRLP(t, new(big.Int).SetUint64(0))
	fakeBody := []byte("fake-body-rlp")
	fakeConfig := []byte(`{"chainId":1337}`)
	// erigon init writes MaxTxNum[BE(0)]=1; step 9 of the patch overwrites
	// it to StepSize-1 ("fat genesis"). Seed the init value so the table
	// exists for the rekey txn to open and overwrite.
	initMaxTxNum := make([]byte, 8)
	binary.BigEndian.PutUint64(initMaxTxNum, 1)

	// Setup: populate ALL 8 hash buckets + MaxTxNum with oldHash refs.
	mustSetup(t, dbPath, func(txn *mdbx.Txn) error {
		return setupFixtures(txn, []bucketRow{
			{bucketHeaderCanonical, blockNumKey, oldHash[:]},
			{bucketHeaders, oldHeadersKey, oldRLP},
			{bucketHeaderNumber, oldHash[:], blockNumKey},
			{bucketHeaderTD, oldHeadersKey, fakeTD},
			{bucketBlockBody, oldHeadersKey, fakeBody},
			{bucketLastBlock, []byte(bucketLastBlock), oldHash[:]},
			{bucketLastHeader, []byte(bucketLastHeader), oldHash[:]},
			{bucketConfig, oldHash[:], fakeConfig},
			{bucketMaxTxNum, blockNumKey, initMaxTxNum},
		})
	})

	// Action: patch with a different Root.
	newRoot := common.HexToHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err := patchGenesisHeaderStateRoot(dbPath, newRoot); err != nil {
		t.Fatalf("patchGenesisHeaderStateRoot: %v", err)
	}

	// Compute expected newHash + newRLP.
	h.Root = newRoot
	newHash := h.Hash()
	newRLP, err := rlp.EncodeToBytes(&h)
	if err != nil {
		t.Fatalf("rlp.EncodeToBytes(newHeader): %v", err)
	}
	if oldHash == newHash {
		t.Fatal("test bug: oldHash == newHash — Root mutation must change the hash")
	}
	newHeadersKey := append(append(make([]byte, 0, 8+32), blockNumKey...), newHash[:]...)

	// Verify all 8 tables.
	mustVerify(t, dbPath, func(txn *mdbx.Txn) {
		// 1. Headers: old absent, new present with new RLP.
		assertAbsent(t, txn, bucketHeaders, oldHeadersKey)
		assertEqual(t, txn, bucketHeaders, newHeadersKey, newRLP)

		// 2. CanonicalHeader[BE(0)] now points at newHash.
		assertEqual(t, txn, bucketHeaderCanonical, blockNumKey, newHash[:])

		// 3. HeaderNumber: oldHash entry removed; newHash entry present.
		assertAbsent(t, txn, bucketHeaderNumber, oldHash[:])
		assertEqual(t, txn, bucketHeaderNumber, newHash[:], blockNumKey)

		// 4. HeadersTotalDifficulty: rekeyed, value preserved.
		assertAbsent(t, txn, bucketHeaderTD, oldHeadersKey)
		assertEqual(t, txn, bucketHeaderTD, newHeadersKey, fakeTD)

		// 5. BlockBody: rekeyed, value preserved.
		assertAbsent(t, txn, bucketBlockBody, oldHeadersKey)
		assertEqual(t, txn, bucketBlockBody, newHeadersKey, fakeBody)

		// 6. LastBlock singleton: value updated to newHash.
		assertEqual(t, txn, bucketLastBlock, []byte(bucketLastBlock), newHash[:])

		// 7. LastHeader singleton: value updated to newHash.
		assertEqual(t, txn, bucketLastHeader, []byte(bucketLastHeader), newHash[:])

		// 8. Config: rekeyed, value preserved.
		assertAbsent(t, txn, bucketConfig, oldHash[:])
		assertEqual(t, txn, bucketConfig, newHash[:], fakeConfig)

		// 9. MaxTxNum[BE(0)] overwritten to StepSize-1 ("fat genesis").
		expectedMaxTxNum := make([]byte, 8)
		binary.BigEndian.PutUint64(expectedMaxTxNum, internalerigon.StepSize-1)
		assertEqual(t, txn, bucketMaxTxNum, blockNumKey, expectedMaxTxNum)
	})
}

// TestPatchGenesisHeaderStateRoot_StrictMissingTableFails is the
// strict-mode safety net: if any of the 8 required tables is missing
// its expected entry, patchGenesisHeaderStateRoot must return an
// error that names the missing bucket — catching upstream
// genesis-write-set drift at first bench instead of silently
// producing an inconsistent chaindata.
func TestPatchGenesisHeaderStateRoot_StrictMissingTableFails(t *testing.T) {
	dbPath := t.TempDir()

	oldRoot := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	h := types.Header{
		ParentHash: common.Hash{},
		Number:     big.NewInt(0),
		Difficulty: big.NewInt(1),
		GasLimit:   60_000_000,
		Root:       oldRoot,
	}
	oldHash := h.Hash()
	oldRLP, err := rlp.EncodeToBytes(&h)
	if err != nil {
		t.Fatalf("rlp.EncodeToBytes: %v", err)
	}

	blockNumKey := make([]byte, 8)
	binary.BigEndian.PutUint64(blockNumKey, 0)
	oldHeadersKey := append(append(make([]byte, 0, 8+32), blockNumKey...), oldHash[:]...)

	// Setup populates 7 of 8 — INTENTIONALLY skips BlockBody.
	mustSetup(t, dbPath, func(txn *mdbx.Txn) error {
		return setupFixtures(txn, []bucketRow{
			{bucketHeaderCanonical, blockNumKey, oldHash[:]},
			{bucketHeaders, oldHeadersKey, oldRLP},
			{bucketHeaderNumber, oldHash[:], blockNumKey},
			{bucketHeaderTD, oldHeadersKey, mustRLP(t, new(big.Int))},
			// BlockBody skipped.
			{bucketLastBlock, []byte(bucketLastBlock), oldHash[:]},
			{bucketLastHeader, []byte(bucketLastHeader), oldHash[:]},
			{bucketConfig, oldHash[:], []byte(`{}`)},
		})
	})

	err = patchGenesisHeaderStateRoot(dbPath, common.HexToHash("0xAAAA"))
	if err == nil {
		t.Fatal("expected error from strict missing-table check; got nil")
	}
	if !strings.Contains(err.Error(), bucketBlockBody) {
		t.Errorf("error should mention %q; got: %v", bucketBlockBody, err)
	}
}

// --- test helpers ---

type bucketRow struct {
	bucket     string
	key, value []byte
}

func mustSetup(t *testing.T, dbPath string, fn func(*mdbx.Txn) error) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dbPath, "chaindata"), 0o755); err != nil {
		t.Fatalf("MkdirAll chaindata: %v", err)
	}
	env, err := openChaindataEnv(dbPath)
	if err != nil {
		t.Fatalf("openChaindataEnv (setup): %v", err)
	}
	defer env.Close()
	if err := env.Update(func(txn *mdbx.Txn) error { return fn(txn) }); err != nil {
		t.Fatalf("setup Update: %v", err)
	}
}

func setupFixtures(txn *mdbx.Txn, rows []bucketRow) error {
	for _, r := range rows {
		dbi, err := txn.OpenDBI(r.bucket, mdbx.Create, nil, nil)
		if err != nil {
			return err
		}
		if err := txn.Put(dbi, r.key, r.value, 0); err != nil {
			return err
		}
	}
	return nil
}

// bucket helpers accept the const-string buckets via implicit conversion below.
func init() {
	// silence unused-bucket warnings when this file is built standalone
	_ = bucketRow{}
}

func mustVerify(t *testing.T, dbPath string, fn func(*mdbx.Txn)) {
	t.Helper()
	env, err := openChaindataEnv(dbPath)
	if err != nil {
		t.Fatalf("openChaindataEnv (verify): %v", err)
	}
	defer env.Close()
	if err := env.View(func(txn *mdbx.Txn) error {
		fn(txn)
		return nil
	}); err != nil {
		t.Fatalf("verify View: %v", err)
	}
}

func assertAbsent(t *testing.T, txn *mdbx.Txn, bucket string, key []byte) {
	t.Helper()
	dbi, err := txn.OpenDBI(bucket, 0, nil, nil)
	if err != nil {
		t.Errorf("OpenDBI(%s) in verify: %v", bucket, err)
		return
	}
	got, err := txn.Get(dbi, key)
	if err == nil {
		t.Errorf("%s[%x]: expected absent, got value len=%d", bucket, key, len(got))
		return
	}
	if !mdbx.IsNotFound(err) {
		t.Errorf("%s[%x]: expected NotFound, got %v", bucket, key, err)
	}
}

func assertEqual(t *testing.T, txn *mdbx.Txn, bucket string, key, want []byte) {
	t.Helper()
	dbi, err := txn.OpenDBI(bucket, 0, nil, nil)
	if err != nil {
		t.Errorf("OpenDBI(%s) in verify: %v", bucket, err)
		return
	}
	got, err := txn.Get(dbi, key)
	if err != nil {
		t.Errorf("%s[%x]: expected present, got error: %v", bucket, key, err)
		return
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s[%x]:\n  got:  %x\n  want: %x", bucket, key, got, want)
	}
}

func mustRLP(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := rlp.EncodeToBytes(v)
	if err != nil {
		t.Fatalf("rlp.EncodeToBytes: %v", err)
	}
	return b
}
