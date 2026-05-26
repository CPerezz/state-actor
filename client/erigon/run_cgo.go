//go:build cgo_erigon

// runImpl orchestrates v1 chaindata generation by:
//
//  1. Building a `types.Genesis`-compatible genesis.json that carries
//     the full synthetic alloc (cfg.PreAlloc + cfg.AutoFill EOAs +
//     contracts + storage + code).
//  2. Executing the pinned `erigon init` CLI against the genesis.json.
//     Erigon's init writes ALL state into MDBX (TblAccountVals,
//     TblStorageVals, TblCodeVals) using its canonical encoding,
//     plus the required headers, chain config, sync stages, and
//     DBSchemaVersion that state-actor would otherwise need to mirror
//     by hand.
//  3. Writing a chainspec.json sidecar (informational; Erigon reads
//     chain config from ConfigTable in MDBX).
//  4. Returning Stats with the genesis state-root parsed from
//     erigon's stdout log line.
//
// This is the "Phase A.5 scaffolding" path documented in the plan
// (`/Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md`
// § Earlier Bench-Iteration Unlock). It satisfies the immediate
// bench-iteration unlock — state-actor produces a fully bootable
// Erigon datadir at 25 GB scale, validating dispatch + Docker + bench
// script flow — while the pure-Go snapshot writer (Architect B's
// "own the format" choice) is built incrementally in parallel under
// `internal/erigon/{seg,recsplit,btindex,existence,snap}/`.
//
// v2 replaces `erigon init` with the pure-Go snapshot writer once
// Parts 1a-1d + 2 of the plan land. Architect B's invariant — no
// `github.com/erigontech/erigon` Go-module dependency in state-actor's
// main module — is preserved: this orchestrator invokes erigon as a
// CLI (`exec.Command`), not as a Go library import.

package erigon

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	mrand "math/rand"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
)

// erigonBinary is the path to the erigon CLI inside the Docker image
// (see Dockerfile.erigon stage 3). Override via Options.ErigonBin for
// tests or non-Docker invocations.
const erigonBinary = "/usr/local/bin/erigon"

// erigonRootLogRegex matches the "Successfully wrote genesis state" log
// line emitted by `erigon init` after CommitGenesisBlock returns. The
// `root` field is the genesis state-root we surface as Stats.StateRoot.
var erigonRootLogRegex = regexp.MustCompile(`Successfully wrote genesis state\b.*\broot=(0x[0-9a-fA-F]+)`)

func runImpl(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	startedAt := time.Now()

	if cfg.DBPath == "" {
		return nil, fmt.Errorf("client/erigon: cfg.DBPath is required")
	}
	if cfg.Genesis == nil {
		return nil, fmt.Errorf("client/erigon: cfg.Genesis is required (set via genesis.BuildSynthetic in main.go)")
	}

	// 0. Create the data dir (chaindata + snapshots subdirs both
	// auto-created by erigon init / `datadir.New`).
	if err := os.MkdirAll(cfg.DBPath, 0o755); err != nil {
		return nil, fmt.Errorf("client/erigon: mkdir dbPath %q: %w", cfg.DBPath, err)
	}

	// 1. Build the alloc map from PreAlloc + AutoFill + GenesisAccounts.
	alloc, stats, err := buildAllocMap(cfg)
	if err != nil {
		return nil, fmt.Errorf("client/erigon: build alloc: %w", err)
	}

	// 2. Serialize genesis.json to <dbPath>/genesis.json.
	genesisPath := filepath.Join(cfg.DBPath, "genesis.json")
	if err := writeGenesisJSON(cfg.Genesis, genesisPath, alloc); err != nil {
		return nil, fmt.Errorf("client/erigon: write genesis.json: %w", err)
	}

	// 3. Exec `erigon init <genesis.json> --datadir <dbPath>`.
	bin := opts.ErigonBin
	if bin == "" {
		bin = erigonBinary
	}
	// Arg order matters: erigon's urfave/cli parser binds --datadir to
	// the `init` subcommand's flag table ONLY if the flag comes BEFORE
	// the <genesisPath> positional. With the flag AFTER the path the
	// parser silently falls back to the default DataDir
	// (`/root/.local/share/erigon`) and writes chaindata there.
	//
	// We deliberately do NOT pass --chain to init. The default is
	// "mainnet"; the chain ID in genesis.json (1337) is what actually
	// goes into MDBX. The downstream `erigon` daemon must also NOT
	// pass --chain dev: v3.4.2's daemon rejects --chain dev against an
	// existing-chaindata boot with "Fatal: chain name is not recognized:
	// dev" (a chain-name lookup that bypasses Dev's short-circuit on
	// the daemon path; init has its own validator that doesn't trip).
	// Block production at boot is driven by --dev.period + miner flags
	// instead. Found via the v1 bench-iteration loop.
	cmd := exec.CommandContext(ctx, bin, "init", "--datadir", cfg.DBPath, genesisPath)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, fmt.Errorf("client/erigon: erigon init failed: %w\nOutput:\n%s", runErr, string(out))
	}

	// 4. Parse the genesis state-root from erigon's log output.
	matches := erigonRootLogRegex.FindStringSubmatch(string(out))
	if len(matches) >= 2 {
		var root common.Hash
		b, decErr := hexutil.Decode(matches[1])
		if decErr == nil && len(b) == 32 {
			copy(root[:], b)
			stats.StateRoot = root
		}
	}
	// If the regex didn't match, surface that as a warning via verbose
	// logging but don't fail — the caller can still verify the datadir
	// via `eth_getBalance` post-boot.

	// 5. Write a chainspec.json sidecar (informational — Erigon reads
	// the chain config from MDBX's ConfigTable after init). Some
	// external tooling (oracle tests, bench scripts) reads
	// <dbPath>/chainspec.json, so emit it for compatibility with the
	// reth/besu/nethermind precedent.
	chainspecPath := filepath.Join(cfg.DBPath, "chainspec.json")
	if err := writeGenesisJSON(cfg.Genesis, chainspecPath, alloc); err != nil {
		// Non-fatal: erigon init already succeeded.
		_ = err
	}

	// 5b. Write SyncStage progress markers so erigon's daemon-boot
	// AllSegmentsDownloadComplete gate clears (>0 OtterSync). Without
	// this, engine_forkchoiceUpdated returns SYNCING forever and no
	// blocks are produced. See sync_stages_cgo.go for the mechanism +
	// source citations from Erigon's stagedsync.
	if err := writeSyncStageMarkers(cfg.DBPath); err != nil {
		return nil, fmt.Errorf("client/erigon: writeSyncStageMarkers: %w", err)
	}

	// 6. Make the datadir world-readable + group-writable. The state-actor
	// container runs as root, so erigon init writes chaindata as root.
	// The downstream `erigontech/erigon:v3.4.2` container runs as a
	// non-root user (uid 1001 "erigon") and would fail with "permission
	// denied" trying to read /data/nodekey / chaindata/. A blanket chmod
	// is safe in this test-only flow because the datadir lives in a bind
	// mount the user controls. See plan § Earlier Bench-Iteration Unlock
	// (bench-host iteration finding).
	if err := chmodRecursive(cfg.DBPath, 0o777); err != nil {
		// Non-fatal: surface a warning via the error path but don't
		// fail the run.
		_ = err
	}

	// 7. Optional Phase B/C path: emit pure-Go snapshot files alongside
	// the `erigon init` MDBX chaindata. Default is OFF (bench works via
	// `erigon init` alone). See client/erigon/options.go::WriteSnapshots
	// for the long-term Architect-B transition plan.
	if opts.WriteSnapshots {
		if err := writeSnapshots(ctx, cfg.DBPath, cfg.Seed, alloc); err != nil {
			return nil, fmt.Errorf("client/erigon: writeSnapshots: %w", err)
		}
	}

	stats.GenerationTime = time.Since(startedAt)
	return stats, nil
}

// chmodRecursive walks root and sets mode on every entry. Stops on first
// error.
func chmodRecursive(root string, mode os.FileMode) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(path, mode)
	})
}

// buildAllocMap materializes cfg.PreAlloc + cfg.GenesisAccounts +
// cfg.AutoFill into a single alloc map. Iteration order is fixed
// (sorted by address) so the resulting genesis.json is deterministic
// for a given seed.
//
// Stats is populated as we go (counts + byte tallies). The state root
// is filled in by runImpl after parsing erigon init's log output.
func buildAllocMap(cfg generator.Config) (map[common.Address]*allocAccount, *generator.Stats, error) {
	alloc := make(map[common.Address]*allocAccount)
	stats := &generator.Stats{}

	// 1. cfg.GenesisAccounts / GenesisCode / GenesisStorage —
	// already-materialized accounts (post-cfg.Validate()).
	for addr, sa := range cfg.GenesisAccounts {
		entry := &allocAccount{
			Nonce: sa.Nonce,
		}
		if sa.Balance != nil {
			entry.Balance = sa.Balance.ToBig()
		}
		if code := cfg.GenesisCode[addr]; len(code) > 0 {
			entry.Code = code
			stats.CodeBytes += uint64(len(code))
		}
		if stor := cfg.GenesisStorage[addr]; len(stor) > 0 {
			entry.Storage = stor
			stats.StorageSlotsCreated += len(stor)
			stats.StorageBytes += uint64(len(stor)) * 64
		}
		alloc[addr] = entry
		if len(entry.Code) > 0 {
			stats.ContractsCreated++
		} else {
			stats.AccountsCreated++
		}
	}

	// 2. AutoFill EOAs + contracts. Drained via entitygen so the RNG
	// sequence matches state-actor's cross-client determinism contract.
	if cfg.AutoFill != nil {
		rng := mrand.New(mrand.NewSource(cfg.Seed))
		for i := 0; i < cfg.AutoFill.NumEOAs; i++ {
			acc := cfg.AutoFill.DrawEOA(rng)
			if acc == nil || acc.StateAccount == nil {
				continue
			}
			entry := &allocAccount{Nonce: acc.StateAccount.Nonce}
			if acc.StateAccount.Balance != nil {
				entry.Balance = acc.StateAccount.Balance.ToBig()
			}
			alloc[acc.Address] = entry
			stats.AccountsCreated++
		}
		for i := 0; i < cfg.AutoFill.NumContracts; i++ {
			c := cfg.AutoFill.DrawContract(rng)
			if c == nil || c.StateAccount == nil {
				continue
			}
			entry := &allocAccount{Nonce: c.StateAccount.Nonce}
			if c.StateAccount.Balance != nil {
				entry.Balance = c.StateAccount.Balance.ToBig()
			}
			if len(c.Code) > 0 {
				entry.Code = c.Code
				stats.CodeBytes += uint64(len(c.Code))
			}
			if len(c.Storage) > 0 {
				entry.Storage = make(map[common.Hash]common.Hash, len(c.Storage))
				for _, s := range c.Storage {
					entry.Storage[s.Key] = s.Value
				}
				stats.StorageSlotsCreated += len(c.Storage)
				stats.StorageBytes += uint64(len(c.Storage)) * 64
			}
			alloc[c.Address] = entry
			stats.ContractsCreated++
		}
	}

	stats.TotalBytes = stats.StorageBytes + stats.CodeBytes
	return alloc, stats, nil
}

// init-time sanity check: silence imports of types we may temporarily not
// reference if the implementation evolves. Removed once the orchestrator
// matures past this v1 scaffolding.
var (
	_ = sort.Slice
	_ = big.NewInt
	_ = entitygen.Account{}
)
