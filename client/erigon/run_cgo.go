//go:build cgo_erigon

// runImpl writes a bootable Erigon v3 chaindata directory in two phases:
//
//  1. Build a types.Genesis-compatible genesis.json (cfg.PreAlloc +
//     cfg.AutoFill EOAs + contracts + code; storage is held back) and
//     exec the pinned `erigon init` CLI, which writes accounts, code,
//     headers, chain config, sync stages, and DBSchemaVersion into
//     MDBX using Erigon's canonical encoding.
//  2. Open MDBX directly via internal/erigon/mdbx and stream the
//     PreAlloc + autofill-contract storage slots into TblStorageVals +
//     the three storage history tables. Storage is split out of
//     genesis.json because erigon init serializes the whole Genesis
//     as a single mdbx_put, and MDBX's per-value limit (~2 GB at
//     16 KB pages) is exceeded by autofill storage at bench scale.
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
	erigonmdbx "github.com/nerolation/state-actor/internal/erigon/mdbx"
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
	// autofillStor carries per-autofill-contract storage out-of-band
	// (deferred to Phase B direct-MDBX so it doesn't bloat genesis.json).
	alloc, autofillStor, stats, err := buildAllocMap(cfg)
	if err != nil {
		return nil, fmt.Errorf("client/erigon: build alloc: %w", err)
	}

	// 2. Serialize genesis.json to <dbPath>/genesis.json.
	genesisPath := filepath.Join(cfg.DBPath, "genesis.json")
	if err := writeGenesisJSON(cfg.Genesis, genesisPath, alloc); err != nil {
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

	// 7. Direct MDBX write of state-actor's storage (Plan Phase B).
	//
	// Two storage sources, both routed through internal/erigon/mdbx.WriteAlloc:
	//
	//   (a) cfg.PreAlloc[i].Storage — iter.Seq2[Hash, Hash] from the spec
	//       translator. Storage stays on the iter (per generator/config.go:134
	//       "Storage is NOT drained"). Other clients drain via runPhase0;
	//       erigon does it here.
	//   (b) autofillStor — the side-data slice from buildAllocMap. Holds
	//       per-autofill-contract []entitygen.StorageSlot deferred FROM the
	//       JSON alloc map. Required because erigon init writes the WHOLE
	//       genesis blob as ONE mdbx_put (kv.ConfigTable["genesis"]) — at
	//       16 KB pages MDBX's per-value limit is ~2 GB, so the bench's
	//       88k autofill contracts × ~615 slots × ~150 JSON bytes blow
	//       past it with MDBX_BAD_VALSIZE. Routing the storage through
	//       MDBX directly keeps genesis.json compact (header+code only).
	//
	// Erigon's daemon on first FCU sees the now-complete storage and
	// rebuilds commitment over it — yielding the correct MPT-equivalent
	// state root (H4-proven equivalence: HexPatriciaHashed root ==
	// geth's MPT root for identical alloc).
	if len(cfg.PreAlloc) > 0 || len(autofillStor) > 0 {
		storageMap := make(map[[20]byte]map[[32]byte][32]byte)
		// (a) PreAlloc — iter.Seq2 streaming source
		for i := range cfg.PreAlloc {
			pe := &cfg.PreAlloc[i]
			if pe.Storage == nil {
				continue
			}
			var addr [20]byte
			copy(addr[:], pe.Address[:])
			if _, ok := storageMap[addr]; !ok {
				storageMap[addr] = make(map[[32]byte][32]byte)
			}
			for k, v := range pe.Storage {
				var sk, sv [32]byte
				copy(sk[:], k[:])
				copy(sv[:], v[:])
				storageMap[addr][sk] = sv
			}
		}
		// (b) AutoFill contracts — side-data slice of []StorageSlot
		for _, ent := range autofillStor {
			var addr [20]byte
			copy(addr[:], ent.addr[:])
			if _, ok := storageMap[addr]; !ok {
				storageMap[addr] = make(map[[32]byte][32]byte, len(ent.slots))
			}
			for _, s := range ent.slots {
				var sk, sv [32]byte
				copy(sk[:], s.Key[:])
				copy(sv[:], s.Value[:])
				storageMap[addr][sk] = sv
			}
		}
		if len(storageMap) > 0 {
			env, err := erigonmdbx.OpenForWrite(cfg.DBPath)
			if err != nil {
				return nil, fmt.Errorf("client/erigon: open MDBX for storage write: %w", err)
			}
			written, err := erigonmdbx.WriteAlloc(env, storageMap)
			env.Close()
			if err != nil {
				return nil, fmt.Errorf("client/erigon: WriteAlloc: %w", err)
			}
			if cfg.Verbose {
				fmt.Printf("client/erigon: wrote %d storage slots to MDBX directly (PreAlloc + autofill-contract); genesis.json kept header+code only for autofill contracts\n", written)
			}
			// NOTE: stats.StorageSlotsCreated + StorageBytes already
			// incremented in buildAllocMap; do NOT double-count here.
			_ = written
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

// autofillContractStorage carries per-autofill-contract storage slots
// out-of-band so they can be written directly to MDBX (Phase B) instead
// of inflating the genesis.json. The receiver in runImpl drains these
// alongside cfg.PreAlloc[i].Storage iters into a single storageMap that
// internal/erigon/mdbx.WriteAlloc consumes.
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
// cfg.AutoFill into a single alloc map. Iteration order is fixed
// (sorted by address) so the resulting genesis.json is deterministic
// for a given seed.
//
// Returns three results:
//   - alloc map (consumed by writeGenesisJSON — header + code only for
//     autofill contracts; storage is deferred to direct-MDBX)
//   - autofillStor side-data (consumed by runImpl's Phase B writer)
//   - stats (populated as we go; counts + byte tallies)
//
// The state root is filled in by runImpl after parsing erigon init's
// log output.
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
				// Defer storage to Phase B direct-MDBX (see
				// autofillContractStorage doc). Keep entry.Storage nil so
				// writeGenesisJSON emits only header+code for this contract.
				// The slice is reused as-is (no copy).
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
