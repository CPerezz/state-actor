package reth

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

// TestValidateConfigRejectsUnsupported proves that the Reth path fails
// fast on flags it doesn't support — the alternative (silent fallback /
// partial execution) would let users waste minutes generating a wrong DB.
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
// default). When both a --chain-id override and a genesis config are
// present, the override wins.
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

// TestWriteChainSpecEmptyAlloc asserts the no-genesis path produces a
// well-formed JSON chainspec with the expected chainId and an empty alloc
// block when the callback writes nothing.
func TestWriteChainSpecEmptyAlloc(t *testing.T) {
	out := filepath.Join(t.TempDir(), "chainspec.json")
	if err := writeChainSpec("", out, 0, func(*bufio.Writer) error { return nil }); err != nil {
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
		t.Errorf("alloc must be empty, got %d entries", len(alloc))
	}
	for _, k := range []string{"nonce", "gasLimit", "extraData", "difficulty", "baseFeePerGas"} {
		if _, ok := spec[k]; !ok {
			t.Errorf("chainspec missing %q", k)
		}
	}
}

// TestWriteChainSpecWithGenesis proves that user-supplied genesis header
// fields flow through verbatim (except alloc, which comes from our
// callback) and that the chainID override wins over the genesis chainId.
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
	if err := writeChainSpec(genPath, out, 42, func(*bufio.Writer) error { return nil }); err != nil {
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
		t.Errorf("alloc must not include genesis file's accounts (we stream our own): got %d", len(alloc))
	}
}

// TestStreamAllocProducesValidJSON feeds streamAlloc into a chainspec and
// re-parses the whole chainspec to prove the emitted alloc is valid JSON.
// This catches mis-quoted strings, trailing commas, missing separators.
func TestStreamAllocProducesValidJSON(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())
	out := filepath.Join(t.TempDir(), "chainspec.json")

	var stats generator.Stats
	allocFn := func(w *bufio.Writer) error { return streamAlloc(cfg, w, &stats) }
	if err := writeChainSpec("", out, 0, allocFn); err != nil {
		t.Fatalf("writeChainSpec: %v", err)
	}

	spec := decodeChainSpec(t, out)
	alloc := spec["alloc"].(map[string]any)
	if len(alloc) == 0 {
		t.Fatal("alloc unexpectedly empty")
	}
	expected := cfg.NumAccounts + cfg.NumContracts
	if len(alloc) != expected {
		t.Errorf("want %d alloc entries, got %d", expected, len(alloc))
	}
	if stats.AccountsCreated != cfg.NumAccounts {
		t.Errorf("accounts: want %d got %d", cfg.NumAccounts, stats.AccountsCreated)
	}
	if stats.ContractsCreated != cfg.NumContracts {
		t.Errorf("contracts: want %d got %d", cfg.NumContracts, stats.ContractsCreated)
	}

	for addr, entry := range alloc {
		if !strings.HasPrefix(addr, "0x") {
			t.Errorf("address should start with 0x, got %q", addr)
		}
		e := entry.(map[string]any)
		if _, ok := e["balance"]; !ok {
			t.Errorf("entry %s missing balance", addr)
		}
		if code, ok := e["code"]; ok {
			if !strings.HasPrefix(code.(string), "0x") {
				t.Errorf("code should be 0x-prefixed: %q", code)
			}
		}
	}
}

// TestStreamAllocReproducibility guards determinism: running streamAlloc
// twice with the same seed MUST produce byte-identical chainspecs.
func TestStreamAllocReproducibility(t *testing.T) {
	var outs [2][]byte
	for i := range 2 {
		cfg := defaultTestConfig(t.TempDir())
		path := filepath.Join(t.TempDir(), "chainspec.json")
		var stats generator.Stats
		allocFn := func(w *bufio.Writer) error { return streamAlloc(cfg, w, &stats) }
		if err := writeChainSpec("", path, 0, allocFn); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		var err error
		outs[i], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(outs[0], outs[1]) {
		t.Errorf("chainspec bytes differ between runs (len %d vs %d)", len(outs[0]), len(outs[1]))
	}
}

// TestPopulateSkipReth exercises the full Populate pipeline without
// actually invoking the reth binary. This catches integration regressions
// (chainspec wiring, datadir prep, config plumbing) without a CI-hostile
// binary dependency.
func TestPopulateSkipReth(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())
	chainSpecPath := filepath.Join(t.TempDir(), "chainspec.json")
	opts := Options{
		SkipRethInvocation: true,
		KeepChainSpec:      true,
		ChainSpecPath:      chainSpecPath,
	}

	stats, err := Populate(context.Background(), cfg, opts)
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if stats == nil {
		t.Fatal("stats nil")
	}
	if stats.AccountsCreated != cfg.NumAccounts {
		t.Errorf("want %d accounts, got %d", cfg.NumAccounts, stats.AccountsCreated)
	}
	if stats.ContractsCreated != cfg.NumContracts {
		t.Errorf("want %d contracts, got %d", cfg.NumContracts, stats.ContractsCreated)
	}

	spec := decodeChainSpec(t, chainSpecPath)
	if _, ok := spec["alloc"].(map[string]any); !ok {
		t.Errorf("chainspec alloc unexpectedly missing / wrong type: %T", spec["alloc"])
	}

	if _, err := os.Stat(cfg.DBPath); err != nil {
		t.Errorf("datadir should have been created: %v", err)
	}
}

// TestPopulateRejectsExistingDatabase ensures we don't silently clobber a
// pre-existing Reth DB. Detection covers both <datadir>/db/mdbx.dat and
// <datadir>/<chain>/db/mdbx.dat layouts.
func TestPopulateRejectsExistingDatabase(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())

	dbDir := filepath.Join(cfg.DBPath, "db")
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

// TestGenesisAccountsIncludedInChainspec validates that --genesis-alloc
// accounts make it into the chainspec's alloc. Without this the generated
// DB would be missing the pre-funded accounts the user expects.
func TestGenesisAccountsIncludedInChainspec(t *testing.T) {
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

	path := filepath.Join(t.TempDir(), "chainspec.json")
	var stats generator.Stats
	allocFn := func(w *bufio.Writer) error { return streamAlloc(cfg, w, &stats) }
	if err := writeChainSpec("", path, 0, allocFn); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(genAddr.Hex())) {
		t.Errorf("chainspec missing genesis address %s", genAddr.Hex())
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
