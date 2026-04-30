//go:build cgo_reth

package reth

import (
	"context"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
)

// runCgoNotAvailableError is nil under -tags cgo_reth. Kept as a symbol so
// TestRunCgoStubBuildPath compiles in both build modes.
var runCgoNotAvailableError error = nil

// emptyMPTRoot is the canonical Merkle-Patricia trie root of the empty trie:
// keccak256(rlp([])) = 0x56e81f17...
var emptyMPTRoot = common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

// RunCgo is the cgo direct-write entry point for --client=reth. Builds a
// reth-compatible datadir end-to-end without spawning the reth binary.
//
// Phases:
//  1. Pre-flight: DBPath required, mkdir, freshDir precondition (via OpenEnvs)
//  2. OpenEnvs (MDBX env + RocksDB CFs)
//  3. WriteDatabaseVersion sidecar
//  4. Synthetic EOAs (if NumAccounts > 0): generate via entitygen, WriteEOAs,
//     ComputeStateRoot; else state root = emptyMPTRoot
//  5. Persist chainspec.json
//  6. Build genesis header with computed state root + WriteMetadata (5 tables)
//  7. Return Stats (Close deferred)
//
// For empty alloc (NumAccounts=0, NumContracts=0) the state root is the
// canonical empty-MPT hash 0x56e81f17... Slice E will extend this for
// contracts (storage tables + StoragesTrie).
//
// On error, partially written files in cfg.DBPath are NOT cleaned up; the
// freshDir precondition will reject the next invocation until the caller
// manually removes the directory.
func RunCgo(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	if cfg.DBPath == "" {
		return nil, fmt.Errorf("RunCgo: cfg.DBPath required")
	}
	if err := os.MkdirAll(cfg.DBPath, 0o755); err != nil {
		return nil, fmt.Errorf("RunCgo: mkdir datadir: %w", err)
	}

	// Phase 2: open MDBX + RocksDB.
	envs, err := OpenEnvs(cfg.DBPath, true)
	if err != nil {
		return nil, fmt.Errorf("RunCgo: OpenEnvs: %w", err)
	}
	defer envs.Close()

	// Phase 3: write <dbDir>/database.version sidecar.
	if err := WriteDatabaseVersion(filepath.Join(cfg.DBPath, "db")); err != nil {
		return nil, fmt.Errorf("RunCgo: WriteDatabaseVersion: %w", err)
	}

	// Phase 4 (new): synthetic EOA generation when NumAccounts > 0.
	stateRoot := emptyMPTRoot
	accountsCreated := 0

	if cfg.NumAccounts > 0 {
		seed := cfg.Seed
		if seed == 0 {
			seed = 42
		}
		rng := mrand.New(mrand.NewSource(seed))
		accounts := make([]*entitygen.Account, cfg.NumAccounts)
		for i := 0; i < cfg.NumAccounts; i++ {
			accounts[i] = entitygen.GenerateEOA(rng)
		}
		if err := WriteEOAs(envs, accounts, 0); err != nil {
			return nil, fmt.Errorf("RunCgo: WriteEOAs: %w", err)
		}
		root, err := ComputeStateRoot(accounts)
		if err != nil {
			return nil, fmt.Errorf("RunCgo: ComputeStateRoot: %w", err)
		}
		stateRoot = root
		accountsCreated = len(accounts)
	}

	// Phase 5a: resolve genesis + chainID, persist chainspec.json.
	genesisPath := genesisPathFromCfg(cfg)
	gen, err := loadGenesisForReth(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("RunCgo: loadGenesisForReth: %w", err)
	}
	chainID := deriveChainID(chainIDFromCfg(cfg), gen)

	chainspecPath := filepath.Join(cfg.DBPath, "chainspec.json")
	if err := writeChainSpec(genesisPath, chainspecPath, chainID); err != nil {
		return nil, fmt.Errorf("RunCgo: writeChainSpec: %w", err)
	}

	// Phase 5b: build genesis header (block 0) + write MDBX metadata tables.
	header, err := buildBlock0Header(gen, chainID)
	if err != nil {
		return nil, fmt.Errorf("RunCgo: buildBlock0Header: %w", err)
	}
	// Use the computed state root (empty-MPT for no alloc, or trie root for EOAs).
	header.Root = stateRoot

	if err := WriteMetadata(envs, header, uint64(chainID)); err != nil {
		return nil, fmt.Errorf("RunCgo: WriteMetadata: %w", err)
	}

	// Phase 6: return stats (envs.Close is deferred above).
	return &generator.Stats{
		StateRoot:        stateRoot,
		AccountsCreated:  accountsCreated,
		ContractsCreated: 0,
	}, nil
}
