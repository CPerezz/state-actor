package reth

import (
	"bufio"
	"bytes"
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

// TestValidateConfigRejectsUnsupported proves the Reth path fails fast on
// flags it doesn't support.
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
// well-formed JSON chainspec. Alloc must be empty: `reth init-state
// --without-evm` uses our streamed dump as the authoritative state, so
// duplicating accounts in chainspec.alloc would be wasted bytes.
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
		t.Errorf("alloc must be empty (state comes from dump), got %d", len(alloc))
	}
}

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
  "alloc": {"0x1111111111111111111111111111111111111111": {"balance": "0x64"}}
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
		t.Errorf("nonce should pass through, got %v", got)
	}
	alloc := spec["alloc"].(map[string]any)
	if len(alloc) != 0 {
		t.Errorf("alloc must always be empty, got %d entries", len(alloc))
	}
}

// TestWriteDumpJSONLFormat checks writeDump + writeFinalDump produce the
// exact {"root":...}\n + one-account-per-line JSONL that reth init-state
// expects.
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
	first, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var hdr struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal([]byte(first), &hdr); err != nil {
		t.Fatalf("first line must be JSON header, got %q (err %v)", first, err)
	}
	if hdr.Root != root.Hex() {
		t.Errorf("header root %q != computed %q", hdr.Root, root.Hex())
	}

	lines := 0
	for {
		line, err := br.ReadString('\n')
		line = strings.TrimSpace(line)
		if line != "" {
			lines++
			var acc struct{ Address, Balance string }
			if uerr := json.Unmarshal([]byte(line), &acc); uerr != nil {
				t.Fatalf("bad account JSON: %q (%v)", line, uerr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	want := stats.AccountsCreated + stats.ContractsCreated
	if lines != want {
		t.Errorf("want %d account lines, got %d", want, lines)
	}
}

// TestWriteDumpReproducibility guards determinism across runs.
func TestWriteDumpReproducibility(t *testing.T) {
	var roots [2]common.Hash
	var dumps [2][]byte
	for i := range 2 {
		cfg := defaultTestConfig(t.TempDir())
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
		roots[i] = root
		dumps[i], err = os.ReadFile(dumpPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	if roots[0] != roots[1] {
		t.Errorf("roots differ: %s vs %s", roots[0].Hex(), roots[1].Hex())
	}
	if !bytes.Equal(dumps[0], dumps[1]) {
		t.Errorf("dumps differ (len %d vs %d)", len(dumps[0]), len(dumps[1]))
	}
}

// TestBuildGenesisHeader verifies the header round-trips through RLP and
// that the same inputs produce the same hash (reproducibility).
func TestBuildGenesisHeader(t *testing.T) {
	var root common.Hash
	for i := range root {
		root[i] = byte(i)
	}
	h1, err := buildGenesisHeader(nil, 1337, root)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := buildGenesisHeader(nil, 1337, root)
	if err != nil {
		t.Fatal(err)
	}
	if h1.Hash() != h2.Hash() {
		t.Errorf("header hash not deterministic: %s vs %s", h1.Hash().Hex(), h2.Hash().Hex())
	}
	if h1.Root != root {
		t.Errorf("header root not set: want %s got %s", root.Hex(), h1.Root.Hex())
	}
	if h1.Number.Sign() != 0 {
		t.Errorf("genesis header number must be 0, got %s", h1.Number)
	}

	// Different stateRoot → different hash.
	var root2 common.Hash
	root2[0] = 0xFF
	h3, err := buildGenesisHeader(nil, 1337, root2)
	if err != nil {
		t.Fatal(err)
	}
	if h1.Hash() == h3.Hash() {
		t.Error("different state roots must yield different header hashes")
	}
}

// TestWriteHeaderFile checks RLP round-trip.
func TestWriteHeaderFile(t *testing.T) {
	var root common.Hash
	root[0] = 0xAB
	h, err := buildGenesisHeader(nil, 1337, root)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "header.rlp")
	got, err := writeHeaderFile(h, p)
	if err != nil {
		t.Fatal(err)
	}
	if got != h.Hash() {
		t.Errorf("writeHeaderFile hash %s != header.Hash() %s", got.Hex(), h.Hash().Hex())
	}
	if info, err := os.Stat(p); err != nil || info.Size() == 0 {
		t.Errorf("header file missing or empty: %v", err)
	}
}

// TestPopulateSkipReth exercises the full pipeline (chainspec + dump +
// header) without actually invoking the reth binary.
func TestPopulateSkipReth(t *testing.T) {
	cfg := defaultTestConfig(t.TempDir())
	opts := Options{SkipRethInvocation: true, KeepDumpFile: true}
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

	// All three artifacts land at the documented paths.
	for _, f := range []string{
		filepath.Join(cfg.DBPath, "chainspec.json"),
		filepath.Join(cfg.DBPath, "genesis-header.rlp"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("expected %s to exist: %v", f, err)
		}
	}
	// Find the dump file (name has a timestamp).
	matches, _ := filepath.Glob(filepath.Join(cfg.DBPath, "dump", "statedump-*.jsonl"))
	if len(matches) != 1 {
		t.Errorf("expected exactly one statedump file, found %d", len(matches))
	}
}

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

// TestGenesisAccountsIncludedInDump validates --genesis-alloc accounts
// make it into the JSONL dump.
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
	if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(genAddr.Hex())) {
		t.Errorf("dump missing genesis address %s", genAddr.Hex())
	}
}

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
