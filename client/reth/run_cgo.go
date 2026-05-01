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
//  4. Synthetic accounts (if NumAccounts > 0 or NumContracts > 0):
//     a. Generate EOAs (NumAccounts) via entitygen.GenerateEOA → WriteEOAs
//     b. Generate contracts (NumContracts) via entitygen.GenerateContract → WriteContracts
//     c. Combine EOAs + contracts; ComputeStateRoot over the merged set
//     Else: state root = emptyMPTRoot
//  5. Persist chainspec.json
//  6. Build genesis header with computed state root + WriteMetadata (5 tables)
//  7. WriteStaticFiles (block-0 segment files)
//  8. Return Stats (Close deferred)
//
// For empty alloc (NumAccounts=0, NumContracts=0) the state root is the
// canonical empty-MPT hash 0x56e81f17...
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

	// Phase 4: synthetic account generation + injected accounts.
	stateRoot := emptyMPTRoot
	accountsCreated := 0
	contractsCreated := 0
	var allAccounts []*entitygen.Account // populated below; passed to writeChainSpec

	// Phase 4a (new): inject pre-funded accounts (e.g. Anvil dev account
	// 0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266) so spamoor and other test
	// harnesses have a known-funded sender. Each gets 999_999_999 ETH, nonce 0,
	// no code, no storage. Address is taken verbatim from cfg.InjectAddresses.
	for _, addr := range cfg.InjectAddresses {
		acc := buildInjectedAccount(addr)
		allAccounts = append(allAccounts, acc)
	}
	if len(cfg.InjectAddresses) > 0 {
		if err := WriteEOAs(envs, allAccounts, 0); err != nil {
			return nil, fmt.Errorf("RunCgo: WriteEOAs(injected): %w", err)
		}
		accountsCreated += len(cfg.InjectAddresses)
	}

	if cfg.NumAccounts > 0 || cfg.NumContracts > 0 {
		seed := cfg.Seed
		if seed == 0 {
			seed = 42
		}
		rng := mrand.New(mrand.NewSource(seed))

		// Phase 4b: synthetic EOAs.
		if cfg.NumAccounts > 0 {
			eoas := make([]*entitygen.Account, cfg.NumAccounts)
			for i := 0; i < cfg.NumAccounts; i++ {
				eoas[i] = entitygen.GenerateEOA(rng)
			}
			if err := WriteEOAs(envs, eoas, 0); err != nil {
				return nil, fmt.Errorf("RunCgo: WriteEOAs: %w", err)
			}
			allAccounts = append(allAccounts, eoas...)
			accountsCreated += cfg.NumAccounts
		}

		// Phase 4c: contracts.
		if cfg.NumContracts > 0 {
			codeSize := cfg.CodeSize
			if codeSize <= 0 {
				codeSize = 256
			}
			slotCount := 5
			if cfg.MinSlots > 0 && cfg.MaxSlots >= cfg.MinSlots {
				slotCount = (cfg.MinSlots + cfg.MaxSlots) / 2
				if slotCount < cfg.MinSlots {
					slotCount = cfg.MinSlots
				}
			}
			contracts := make([]*entitygen.Account, cfg.NumContracts)
			for i := 0; i < cfg.NumContracts; i++ {
				contracts[i] = entitygen.GenerateContract(rng, codeSize, slotCount)
			}
			if err := WriteContracts(envs, contracts, 0); err != nil {
				return nil, fmt.Errorf("RunCgo: WriteContracts: %w", err)
			}
			allAccounts = append(allAccounts, contracts...)
			contractsCreated = cfg.NumContracts
		}
	}

	// Phase 4d: compute global state root over injected + EOAs + contracts.
	if len(allAccounts) > 0 {
		root, err := ComputeStateRoot(allAccounts)
		if err != nil {
			return nil, fmt.Errorf("RunCgo: ComputeStateRoot: %w", err)
		}
		stateRoot = root
	}

	// Phase 5a: resolve genesis + chainID, persist chainspec.json.
	// The chainspec alloc is populated with all generated accounts so that
	// reth's state_root_ref_unhashed(&alloc) matches our ComputeStateRoot,
	// making the genesis hash consistent between chainspec and database.
	genesisPath := genesisPathFromCfg(cfg)
	gen, err := loadGenesisForReth(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("RunCgo: loadGenesisForReth: %w", err)
	}
	chainID := deriveChainID(chainIDFromCfg(cfg), gen)

	chainspecPath := filepath.Join(cfg.DBPath, "chainspec.json")
	if err := writeChainSpec(genesisPath, chainspecPath, chainID, allAccounts); err != nil {
		return nil, fmt.Errorf("RunCgo: writeChainSpec: %w", err)
	}

	// Phase 5b: build genesis header (block 0) + write MDBX metadata tables.
	header, err := buildBlock0Header(gen, chainID)
	if err != nil {
		return nil, fmt.Errorf("RunCgo: buildBlock0Header: %w", err)
	}
	// Use the computed state root (empty-MPT for no alloc, or trie root for accounts).
	header.Root = stateRoot

	if err := WriteMetadata(envs, header, uint64(chainID)); err != nil {
		return nil, fmt.Errorf("RunCgo: WriteMetadata: %w", err)
	}

	// Phase 6: write block-0 static-files segments.
	if err := WriteStaticFiles(cfg.DBPath, header); err != nil {
		return nil, fmt.Errorf("RunCgo: WriteStaticFiles: %w", err)
	}

	// Phase 7: return stats (envs.Close is deferred above).
	return &generator.Stats{
		StateRoot:        stateRoot,
		AccountsCreated:  accountsCreated,
		ContractsCreated: contractsCreated,
	}, nil
}
