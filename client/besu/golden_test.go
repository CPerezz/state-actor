//go:build cgo_besu

package besu

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/besu/keys"
)

// TestBesuGoldenStateRoot pins the state root the full cgo_besu pipeline
// produces for seed=12345/10/5/PowerLaw/MaxSlots=100/CodeSize=256. The hash
// MUST equal internal/entitygen.TestCanonicalEntitygenMPTRoot — every
// entitygen-using MPT adapter shares this constant. Drift requires a
// coordinated update across nethermind, besu, reth.
func TestBesuGoldenStateRoot(t *testing.T) {
	const expectedRoot = "0xddbfa7c1941ff70fe5a692f7552149adc1ae29ebb2b5dc8bb3544c1368bcb0c3"

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "besu-golden")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := generator.Config{
		DBPath:       dbPath,
		NumAccounts:  10,
		NumContracts: 5,
		MaxSlots:     100,
		MinSlots:     1,
		Distribution: generator.PowerLaw,
		Seed:         12345,
		BatchSize:    100,
		Workers:      1,
		CodeSize:     256,
		Verbose:      false,
	}

	stats, err := Run(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats == nil {
		t.Fatal("Run returned nil stats")
	}
	if stats.StateRoot == (common.Hash{}) {
		t.Fatal("Run returned zero state root — pipeline didn't populate stats.StateRoot")
	}
	if got := stats.StateRoot.Hex(); got != expectedRoot {
		t.Fatalf("besu golden state root mismatch:\n  got:  %s\n  want: %s\n  Diverging here means a coordinated update across all entitygen-using adapters is needed (see internal/entitygen/canonical_mpt_test.go).",
			got, expectedRoot)
	}
}

// TestBesuReproducibility verifies that the same config + seed produces the
// same on-disk worldRoot sentinel across runs. Guards against any latent
// non-determinism in entity generation, Pebble batch ordering, or trie
// commit traversal.
func TestBesuReproducibility(t *testing.T) {
	cfg := generator.Config{
		NumAccounts:  20,
		NumContracts: 5,
		MaxSlots:     50,
		MinSlots:     1,
		Distribution: generator.PowerLaw,
		Seed:         42,
		BatchSize:    100,
		Workers:      1,
		CodeSize:     256,
	}

	runOnce := func() (common.Hash, []byte) {
		dbPath := filepath.Join(t.TempDir(), "besu-repro")
		if err := os.MkdirAll(dbPath, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		c := cfg
		c.DBPath = dbPath
		stats, err := Run(context.Background(), c, Options{})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}

		// Read the persisted worldRoot sentinel as cross-check that the
		// stats hash actually landed on disk (catches any future bug where
		// stats reports a value that doesn't match what Besu would read).
		_, rootBytes, err := readWorldRoot(dbPath)
		if err != nil {
			t.Fatalf("readWorldRoot: %v", err)
		}
		return stats.StateRoot, rootBytes
	}

	statsRoot1, diskRoot1 := runOnce()
	statsRoot2, diskRoot2 := runOnce()

	if statsRoot1 != statsRoot2 {
		t.Fatalf("stats StateRoot non-deterministic: %s vs %s", statsRoot1.Hex(), statsRoot2.Hex())
	}
	if !bytes.Equal(diskRoot1, diskRoot2) {
		t.Fatalf("on-disk worldRoot non-deterministic: %x vs %x", diskRoot1, diskRoot2)
	}
	if !bytes.Equal(statsRoot1.Bytes(), diskRoot1) {
		t.Fatalf("stats vs on-disk root mismatch: stats=%s, disk=%x", statsRoot1.Hex(), diskRoot1)
	}
}

// readWorldRoot opens the produced RocksDB and reads the
// TRIE_BRANCH_STORAGE[WorldRootKey] sentinel. Returns both the hash form
// (for cross-check vs stats.StateRoot) and the raw bytes (for byte-equal
// comparison across reproducibility runs).
func readWorldRoot(datadir string) (common.Hash, []byte, error) {
	dbPath := filepath.Join(datadir, "database")

	// Match the writer's CF set so RocksDB can open the DB.
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
	cfOpts := make([]*grocksdb.Options, len(cfNames))
	for i := range cfOpts {
		cfOpts[i] = grocksdb.NewDefaultOptions()
	}
	defer func() {
		for _, o := range cfOpts {
			o.Destroy()
		}
	}()

	dbOpts := grocksdb.NewDefaultOptions()
	defer dbOpts.Destroy()

	db, cfs, err := grocksdb.OpenDbColumnFamilies(dbOpts, dbPath, cfNames, cfOpts)
	if err != nil {
		return common.Hash{}, nil, err
	}
	defer func() {
		for _, h := range cfs {
			h.Destroy()
		}
		db.Close()
	}()

	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	got, err := db.GetCF(ro, cfs[cfIdxTrieBranchStorage], keys.WorldRootKey)
	if err != nil {
		return common.Hash{}, nil, err
	}
	defer got.Free()

	raw := make([]byte, got.Size())
	copy(raw, got.Data())
	return common.BytesToHash(raw), raw, nil
}
