package reth

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/genesis"
)

// defaultTestConfig returns a Config small enough to run quickly in unit
// tests but large enough to exercise EOAs, contracts and storage slots.
func defaultTestConfig(tmpDir string) generator.Config {
	return generator.Config{
		DBPath:       filepath.Join(tmpDir, "reth-db"),
		NumAccounts:  5,
		NumContracts: 3,
		MaxSlots:     10,
		MinSlots:     2,
		Distribution: generator.Uniform,
		Seed:         12345,
		BatchSize:    100,
		Workers:      1,
		CodeSize:     64,
		TrieMode:     generator.TrieModeMPT,
	}
}

// TestValidateConfigRejectsUnsupported proves that the Reth path fails fast
// on flags it doesn't support — the alternative (silent fallback / partial
// execution) would let users waste minutes generating a wrong database.
func TestValidateConfigRejectsUnsupported(t *testing.T) {
	base := defaultTestConfig(t.TempDir())

	cases := []struct {
		name     string
		mutate   func(*generator.Config)
		needsSub string
	}{
		{"binary trie", func(c *generator.Config) { c.TrieMode = generator.TrieModeBinary }, "binary-trie"},
		{"target size", func(c *generator.Config) { c.TargetSize = 1 << 30 }, "target-size"},
		{"deep branch", func(c *generator.Config) {
			c.DeepBranch = generator.DeepBranchConfig{NumAccounts: 1, Depth: 8, KnownSlots: 1}
		}, "deep-branch"},
		{"empty db path", func(c *generator.Config) { c.DBPath = "" }, "--db"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := validateConfig(cfg)
			if err == nil {
				t.Fatalf("expected error mentioning %q, got nil", tc.needsSub)
			}
			if !strings.Contains(err.Error(), tc.needsSub) {
				t.Fatalf("expected error to mention %q, got: %v", tc.needsSub, err)
			}
		})
	}
}

// TestDeriveChainID covers the priority ordering (override > genesis >
// default). The ordering is load-bearing: when both a --chain-id override
// and a genesis config are present, the override wins.
func TestDeriveChainID(t *testing.T) {
	g := &genesis.Genesis{Config: &params.ChainConfig{ChainID: big.NewInt(5)}}

	if got := deriveChainID(42, g); got != 42 {
		t.Errorf("override wins: want 42, got %d", got)
	}
	if got := deriveChainID(0, g); got != 5 {
		t.Errorf("genesis ChainID used when no override: want 5, got %d", got)
	}
	if got := deriveChainID(0, nil); got != 1337 {
		t.Errorf("default 1337 when no genesis + no override: got %d", got)
	}
	empty := &genesis.Genesis{Config: &params.ChainConfig{}}
	if got := deriveChainID(0, empty); got != 1337 {
		t.Errorf("default 1337 when genesis config lacks chainID: got %d", got)
	}
}

// TestWriteChainSpecDefaults asserts the no-genesis path produces a
// well-formed JSON chainspec with the expected chainId and the crucial
// empty alloc. Empty alloc is a contract with `reth init-state` — the
// state dump is the source of truth.
func TestWriteChainSpecDefaults(t *testing.T) {
	out := filepath.Join(t.TempDir(), "chainspec.json")
	if err := writeChainSpec("", out, 0); err != nil {
		t.Fatalf("writeChainSpec: %v", err)
	}

	spec := decodeChainSpec(t, out)

	cfg, ok := spec["config"].(map[string]any)
	if !ok {
		t.Fatalf("expected config object, got %T", spec["config"])
	}
	if got := cfg["chainId"]; got != float64(1337) {
		t.Errorf("default chainId=1337, got %v", got)
	}
	alloc, ok := spec["alloc"].(map[string]any)
	if !ok {
		t.Fatalf("expected alloc object, got %T", spec["alloc"])
	}
	if len(alloc) != 0 {
		t.Errorf("alloc must be empty (state comes from dump), got %d entries", len(alloc))
	}
	// Spot-check a few header fields so regressions in buildChainSpec are caught.
	for _, k := range []string{"nonce", "gasLimit", "extraData", "difficulty", "baseFeePerGas"} {
		if _, ok := spec[k]; !ok {
			t.Errorf("chainspec missing %q", k)
		}
	}
}

// TestWriteChainSpecWithGenesis proves that user-supplied genesis header
// fields flow through verbatim (except alloc, which we zero out) and that
// chainID override wins over the value inside the genesis config.
func TestWriteChainSpecWithGenesis(t *testing.T) {
	dir := t.TempDir()
	genPath := filepath.Join(dir, "genesis.json")
	genContents := `{
  "config": {"chainId": 99, "homesteadBlock": 0},
  "nonce": "0xdeadbeef",
  "timestamp": "0x1234",
  "extraData": "0xcafe",
  "gasLimit": "0x5f5e100",
  "difficulty": "0x0",
  "alloc": {
    "0x1111111111111111111111111111111111111111": {"balance": "0x64"}
  }
}`
	if err := os.WriteFile(genPath, []byte(genContents), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "chainspec.json")
	if err := writeChainSpec(genPath, out, 42); err != nil {
		t.Fatalf("writeChainSpec: %v", err)
	}
	spec := decodeChainSpec(t, out)

	cfg := spec["config"].(map[string]any)
	if got := cfg["chainId"]; got != float64(42) {
		t.Errorf("override chainId=42 should win, got %v", got)
	}
	if got := spec["nonce"]; got != "0xdeadbeef" {
		t.Errorf("nonce should pass through from genesis, got %v", got)
	}
	if got := spec["timestamp"]; got != "0x1234" {
		t.Errorf("timestamp should pass through, got %v", got)
	}
	if got := spec["gasLimit"]; got != "0x5f5e100" {
		t.Errorf("gasLimit should pass through, got %v", got)
	}
	alloc := spec["alloc"].(map[string]any)
	if len(alloc) != 0 {
		t.Errorf("alloc must always be empty, got %d entries", len(alloc))
	}
}

// TestWriteDumpJSONLFormat checks that writeDump + writeFinalDump together
// produce the exact JSONL layout `reth init-state` expects: line 1 is the
// {"root":"0x.."} header, subsequent lines are one account per line. This
// is the thinnest end-to-end shape test — the state root value itself is
// covered by TestWriteDumpReproducibility.
func TestWriteDumpJSONLFormat(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())

	dir := t.TempDir()
	accountsPath := filepath.Join(dir, "accounts.jsonl")
	dumpPath := filepath.Join(dir, "dump.jsonl")

	root, stats, err := streamToTempDump(cfg, accountsPath)
	if err != nil {
		t.Fatalf("streamToTempDump: %v", err)
	}
	if err := writeFinalDump(dumpPath, accountsPath, root); err != nil {
		t.Fatalf("writeFinalDump: %v", err)
	}

	f, err := os.Open(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	br := bufio.NewReader(f)
	firstLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal([]byte(firstLine), &header); err != nil {
		t.Fatalf("first line must be JSON header, got %q (err %v)", firstLine, err)
	}
	if header.Root != root.Hex() {
		t.Errorf("header root %q != computed %q", header.Root, root.Hex())
	}

	// Count subsequent account lines; must equal stats.AccountsCreated +
	// ContractsCreated. EOFs are fine.
	accountLines := 0
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			accountLines++
			var acc struct {
				Address string `json:"address"`
				Balance string `json:"balance"`
			}
			if uerr := json.Unmarshal([]byte(line), &acc); uerr != nil {
				t.Fatalf("account line must be JSON: %q (err %v)", line, uerr)
			}
			if !strings.HasPrefix(acc.Address, "0x") || len(acc.Address) != 42 {
				t.Errorf("malformed address %q", acc.Address)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	expected := stats.AccountsCreated + stats.ContractsCreated
	if accountLines != expected {
		t.Errorf("want %d account lines, got %d", expected, accountLines)
	}
}

// TestWriteDumpReproducibility guards determinism: running the generator
// twice with the same seed MUST produce identical dumps (byte-for-byte) and
// identical state roots. If a future change introduces non-determinism
// (e.g. map iteration in the RNG path), this fails loudly.
func TestWriteDumpReproducibility(t *testing.T) {
	var roots [2]common.Hash
	var dumps [2][]byte

	for i := range 2 {
		cfg := defaultTestConfig(t.TempDir())
		// Use the same seed across both runs (already set in default); the
		// DBPath and temp paths must differ so both runs have independent
		// filesystem state.
		dir := t.TempDir()
		accountsPath := filepath.Join(dir, "accounts.jsonl")
		dumpPath := filepath.Join(dir, "dump.jsonl")

		root, _, err := streamToTempDump(cfg, accountsPath)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := writeFinalDump(dumpPath, accountsPath, root); err != nil {
			t.Fatalf("run %d finalize: %v", i, err)
		}
		roots[i] = root
		dumps[i], err = os.ReadFile(dumpPath)
		if err != nil {
			t.Fatal(err)
		}
	}

	if roots[0] != roots[1] {
		t.Errorf("state roots differ across runs: %s vs %s", roots[0].Hex(), roots[1].Hex())
	}
	if string(dumps[0]) != string(dumps[1]) {
		t.Errorf("dump bytes differ across runs (len %d vs %d)", len(dumps[0]), len(dumps[1]))
	}
}

// TestPopulateSkipReth exercises the full Populate pipeline without
// actually invoking the reth binary. This catches integration regressions
// (chainspec wiring, datadir prep, config plumbing) without a CI-hostile
// binary dependency.
func TestPopulateSkipReth(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())
	opts := Options{
		SkipRethInvocation: true,
		KeepDumpFile:       true,
		DumpDir:            t.TempDir(),
	}

	stats, err := Populate(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if stats == nil {
		t.Fatal("stats nil")
	}
	if stats.StateRoot == (common.Hash{}) {
		t.Error("state root not populated")
	}
	if stats.AccountsCreated != cfg.NumAccounts {
		t.Errorf("want %d accounts, got %d", cfg.NumAccounts, stats.AccountsCreated)
	}
	if stats.ContractsCreated != cfg.NumContracts {
		t.Errorf("want %d contracts, got %d", cfg.NumContracts, stats.ContractsCreated)
	}

	// The datadir should exist and be empty-of-db (we didn't invoke reth).
	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Errorf("datadir should have been created: %v", err)
	}
}

// TestPopulateRejectsExistingDatabase ensures we don't silently clobber a
// pre-existing Reth DB. The detection is best-effort (scans for
// <datadir>/*/db/mdbx.dat) but catches the common case.
func TestPopulateRejectsExistingDatabase(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())

	// Simulate a pre-existing reth DB layout.
	dbDir := filepath.Join(cfg.DBPath, "dev", "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "mdbx.dat"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Populate(context.Background(), cfg, Options{SkipRethInvocation: true})
	if err == nil {
		t.Fatal("expected error about existing DB, got nil")
	}
	if !strings.Contains(err.Error(), "already present") {
		t.Errorf("expected 'already present' in error, got: %v", err)
	}
}

// TestGenesisAccountsIncludedInDump validates that --genesis-alloc accounts
// make it into the JSONL dump. Without this the generated DB would be
// missing the pre-funded accounts the user expects post `init-state`.
func TestGenesisAccountsIncludedInDump(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())
	genAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	bal := new(uint256.Int).SetUint64(1_000_000_000)
	cfg.GenesisAccounts = map[common.Address]*types.StateAccount{
		genAddr: {
			Nonce:    0,
			Balance:  bal,
			Root:     types.EmptyRootHash,
			CodeHash: types.EmptyCodeHash.Bytes(),
		},
	}

	dir := t.TempDir()
	accountsPath := filepath.Join(dir, "accounts.jsonl")
	dumpPath := filepath.Join(dir, "dump.jsonl")
	root, _, err := streamToTempDump(cfg, accountsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFinalDump(dumpPath, accountsPath, root); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), strings.ToLower(genAddr.Hex())) {
		t.Errorf("dump missing genesis address %s:\n%s", genAddr.Hex(), body)
	}
}

// decodeChainSpec is a tiny helper so individual tests aren't cluttered
// with json.Unmarshal boilerplate.
func decodeChainSpec(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read chainspec: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parse chainspec: %v", err)
	}
	return m
}
