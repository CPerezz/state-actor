//go:build cgo_erigon

// runImpl writes a bootable Erigon v3 chaindata directory. Today, it
// only drives `erigon init` against a synthetic genesis.json: the bloat
// data path (account / storage / code into snapshot files) is being
// rebuilt under the snapshot-tier refactor described in
// `/Users/random_anon/.claude/plans/so-what-we-have-enumerated-lantern.md`.
// The streaming snapshot orchestrator will land in PART 5 of that plan.
//
// state-actor's main module does NOT import github.com/erigontech/erigon;
// erigon is invoked as a CLI from the Docker image built by
// Dockerfile.erigon.

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

	// 1. Build the in-memory alloc from PreAlloc + AutoFill +
	// GenesisAccounts. autofillStor carries per-autofill-contract
	// storage out-of-band; the streaming snapshot orchestrator
	// (writeSnapshots, snapshot_cgo.go) folds the alloc + autofillStor
	// into the per-domain streamsort.Stores. NONE of this is written
	// to genesis.json — every state-actor-generated entry ends up in
	// the snapshot tier only.
	alloc, autofillStor, stats, err := buildAllocMap(cfg)
	if err != nil {
		return nil, fmt.Errorf("client/erigon: build alloc: %w", err)
	}

	// 2. Serialize genesis.json with an EMPTY alloc. This makes
	// erigon init's single-value mdbx_put into kv.ConfigTable["genesis"]
	// trivially small (no MDBX_BAD_VALSIZE risk at any --target-size),
	// and keeps MDBX free of state-actor data. Erigon's WriteGenesisBlock
	// auto-adds the per-fork system contracts based on ChainConfig
	// (EIP-4788 BeaconRoot etc.); those are the only writes erigon init
	// produces. Every state-actor entry — including PreAlloc /
	// GenesisAccounts / AutoFill — flows into snapshot files instead.
	genesisPath := filepath.Join(cfg.DBPath, "genesis.json")
	if err := writeGenesisJSON(cfg.Genesis, genesisPath, nil); err != nil {
		return nil, fmt.Errorf("client/erigon: write genesis.json: %w", err)
	}

	// 3. Exec `erigon init <genesis.json> --datadir <dbPath>`.
	bin := erigonBinary
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

	// 5. Write a chainspec.json sidecar (informational — Erigon reads
	// the chain config from MDBX's ConfigTable after init). Some
	// external tooling (oracle tests, bench scripts) reads
	// <dbPath>/chainspec.json, so emit it for compatibility with the
	// reth/besu/nethermind precedent. Empty alloc here too — sidecar
	// is for chain config inspection; state lives in snapshots.
	chainspecPath := filepath.Join(cfg.DBPath, "chainspec.json")
	if err := writeGenesisJSON(cfg.Genesis, chainspecPath, nil); err != nil {
		// Non-fatal: erigon init already succeeded.
		_ = err
	}

	// 5b. Write SyncStage progress markers so erigon's daemon-boot
	// AllSegmentsDownloadComplete gate clears (>0 OtterSync). Without
	// this, engine_forkchoiceUpdated returns SYNCING forever and no
	// blocks are produced. See sync_stages_cgo.go for the mechanism.
	// TODO(plan PART 7): once snapshot-tier presence alone satisfies
	// the gate, this call may become unnecessary.
	if err := writeSyncStageMarkers(cfg.DBPath); err != nil {
		return nil, fmt.Errorf("client/erigon: writeSyncStageMarkers: %w", err)
	}

	// 6. Make the datadir world-readable + group-writable. The state-actor
	// container runs as root, so erigon init writes chaindata as root.
	// The downstream `erigontech/erigon:v3.4.2` container runs as a
	// non-root user (uid 1001 "erigon") and would fail with "permission
	// denied" trying to read /data/nodekey / chaindata/. A blanket chmod
	// is safe in this test-only flow because the datadir lives in a bind
	// mount the user controls.
	if err := chmodRecursive(cfg.DBPath, 0o777); err != nil {
		// Non-fatal: surface a warning via the error path but don't
		// fail the run.
		_ = err
	}

	// 7. Streaming snapshot orchestrator.
	//
	// Writes accounts/storage/code/commitment .kv snapshot files + the
	// FS preconditions (salt-state.txt, erigondb.toml), computes the
	// HPH commitment root over the post-bloat state, and patches
	// block 0's header.stateRoot so the daemon's first FCU validates.
	//
	// Skipped when opts.WriteSnapshots == false (default during landing).
	// In that mode stats.StateRoot stays whatever erigon init reported
	// (system contracts only — bloat is unreachable, but useful for
	// isolating snapshot bugs from rest-of-pipeline bugs during dev).
	if opts.WriteSnapshots {
		root, err := writeSnapshots(ctx, cfg.DBPath, cfg.Seed, alloc, autofillStor, cfg.Verbose)
		if err != nil {
			return nil, fmt.Errorf("client/erigon: writeSnapshots: %w", err)
		}
		if err := patchGenesisHeaderStateRoot(cfg.DBPath, root); err != nil {
			return nil, fmt.Errorf("client/erigon: patchGenesisHeaderStateRoot: %w", err)
		}
		stats.StateRoot = root
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

// autofillContractStorage carries per-autofill-contract storage slots
// out-of-band so they can be fed into the streaming snapshot
// orchestrator without inflating the genesis.json that `erigon init`
// reads. The orchestrator (plan PART 5) will consume these alongside
// cfg.PreAlloc[i].Storage iters into a single storage streamsort.Store.
//
// Why this exists: erigon init writes the WHOLE genesis blob as one
// mdbx_put into kv.ConfigTable["genesis"]; the per-value limit at
// 16 KB pages is ~2 GB. Without this split, 88k autofill contracts ×
// ~615 storage slots × ~150 JSON bytes/slot ≈ 8 GB of storage entries
// were going into the JSON, pushing the blob past the MDBX limit and
// triggering MDBX_BAD_VALSIZE during erigon init's genesis write.
type autofillContractStorage struct {
	addr  common.Address
	slots []entitygen.StorageSlot
}

// buildAllocMap materializes cfg.PreAlloc + cfg.GenesisAccounts +
// cfg.AutoFill into a single in-memory alloc map. ALL entries — spec
// foundational and autofill alike — flow ONLY into the snapshot
// streamsorts + HPH commitment walk. NONE of them are written to
// genesis.json (writeGenesisJSON receives nil, producing an empty
// alloc; see runImpl). The only thing erigon init writes to MDBX is
// the per-fork system contracts that Erigon's WriteGenesisBlock
// auto-adds based on ChainConfig — and the chain-config tables
// themselves. The block-0 header.stateRoot is patched by
// patchGenesisHeaderStateRoot to the HPH-over-the-whole-alloc value.
//
// Why nothing goes to genesis.json: erigon init serialises the whole
// Genesis struct to a single mdbx_put into kv.ConfigTable["genesis"].
// MDBX's per-value limit at 16 KB pages is ~2 GB. At
// --target-size=25GB the autofill plan draws ~30 M EOAs + 512 K
// contracts (~8 GB JSON), busting the limit. More importantly, the
// project goal is to put ALL state into the snapshot tier (the cold
// path that production Erigon serves from) — not the hot MDBX tier
// that erigon init populates. So we keep MDBX empty of state-actor
// data and let the snapshot writer carry the whole load.
//
// autofillStor carries per-autofill-contract storage out-of-band so
// the streaming orchestrator can drain it into the storage streamsort
// without inflating the alloc map's storage maps.
//
// Returns three results:
//   - alloc        — full alloc (foundational + autofill); consumed by
//                    writeSnapshots and runCommitmentPhase only
//   - autofillStor — per-autofill-contract storage slots
//   - stats        — counts + byte tallies
//
// The state root reported by erigon init reflects only the per-fork
// system contracts (empty alloc otherwise); patchGenesisHeaderStateRoot
// overwrites it with the HPH-over-EVERYTHING root once writeSnapshots
// + commitment complete.
func buildAllocMap(cfg generator.Config) (map[common.Address]*allocAccount, []autofillContractStorage, *generator.Stats, error) {
	alloc := make(map[common.Address]*allocAccount)
	var autofillStor []autofillContractStorage
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
	//
	// The dedup-redraw loop must match client/geth/state_writer.go:116-148
	// byte-for-byte: geth burns RNG draws on collision until it finds a
	// non-colliding address, with no nil-checks. Any nil-check break here
	// would skip the assignment but still advance the RNG, desynchronizing
	// from geth's draw sequence and producing a different alloc + a
	// different cross-client genesis state-root.
	//
	// AutoFill.Draw{EOA,Contract} return non-nil for all valid plans (a
	// nil return would crash geth at the same point — we mirror that
	// contract rather than guard against it).
	genesisAddrs := make(map[common.Address]struct{}, len(alloc))
	for addr := range alloc {
		genesisAddrs[addr] = struct{}{}
	}
	if cfg.AutoFill != nil {
		rng := mrand.New(mrand.NewSource(cfg.Seed))
		for i := 0; i < cfg.AutoFill.NumEOAs; i++ {
			acc := cfg.AutoFill.DrawEOA(rng)
			for _, dup := genesisAddrs[acc.Address]; dup; {
				acc = cfg.AutoFill.DrawEOA(rng)
				_, dup = genesisAddrs[acc.Address]
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
			for _, dup := genesisAddrs[c.Address]; dup; {
				c = cfg.AutoFill.DrawContract(rng)
				_, dup = genesisAddrs[c.Address]
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
				// Per-contract storage carried out-of-band so the
				// streaming orchestrator can drain it directly into the
				// storage streamsort without inflating the alloc entry's
				// Storage map. The slice is reused as-is (no copy).
				autofillStor = append(autofillStor, autofillContractStorage{
					addr:  c.Address,
					slots: c.Storage,
				})
				stats.StorageSlotsCreated += len(c.Storage)
				stats.StorageBytes += uint64(len(c.Storage)) * 64
			}
			alloc[c.Address] = entry
			stats.ContractsCreated++
		}
	}

	stats.TotalBytes = stats.StorageBytes + stats.CodeBytes
	return alloc, autofillStor, stats, nil
}

// init-time sanity check: silence imports of types we may temporarily not
// reference if the implementation evolves. Removed once the orchestrator
// matures past this v1 scaffolding.
var (
	_ = sort.Slice
	_ = big.NewInt
	_ = entitygen.Account{}
)
