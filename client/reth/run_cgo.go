//go:build cgo_reth

package reth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
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
//     4a. Persist chainspec.json
//     4b. Build genesis header + WriteMetadata (5 tables)
//  5. Return Stats (Close deferred)
//
// For empty alloc (NumAccounts=0, NumContracts=0) the state root is the
// canonical empty-MPT hash 0x56e81f17... Slices D+E will compute it from
// generated entities.
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

	// Phase 4a: resolve genesis + chainID, persist chainspec.json.
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

	// Phase 4b: build genesis header (block 0) + write MDBX metadata tables.
	// For empty alloc the state root is the canonical empty-MPT hash; Slices
	// D+E will pass the hash computed from generated entities instead.
	header, err := buildBlock0Header(gen, chainID)
	if err != nil {
		return nil, fmt.Errorf("RunCgo: buildBlock0Header: %w", err)
	}
	// Override the state root with the empty-MPT hash (block 0 has no alloc).
	header.Root = emptyMPTRoot

	if err := WriteMetadata(envs, header, uint64(chainID)); err != nil {
		return nil, fmt.Errorf("RunCgo: WriteMetadata: %w", err)
	}

	// Phase 5: return stats (envs.Close is deferred above).
	return &generator.Stats{
		StateRoot:        header.Root,
		AccountsCreated:  0,
		ContractsCreated: 0,
	}, nil
}
