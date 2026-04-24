package reth

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/nerolation/state-actor/generator"
)

// Options gives tests and library users fine-grained control over the
// population workflow. The zero value is the Populate CLI default: resolve
// reth via PATH, clean up temp files, log only when cfg.Verbose is set.
type Options struct {
	// RethBinaryPath overrides the resolution of the `reth` binary. When
	// empty, PATH lookup is used.
	RethBinaryPath string

	// SkipRethInvocation, when true, skips the final `reth init-state`
	// call. The chainspec, state dump, and header files are still produced
	// (so unit tests can assert on their shapes without needing the reth
	// binary).
	SkipRethInvocation bool

	// ChainSpecPath, when non-empty, overrides the default chainspec
	// output path (<dbPath>/chainspec.json). The chainspec is always
	// persisted because `reth node` revalidates the genesis hash on every
	// boot.
	ChainSpecPath string

	// KeepDumpFile leaves the JSONL state dump on disk after a successful
	// run; the default is to remove it since at 100GB+ scale it dominates
	// disk usage and is not needed once Reth has imported the state.
	KeepDumpFile bool

	// DumpDir is where the intermediate JSONL dump is written. Defaults to
	// <dbPath>/dump/ so it's on the same volume as the DB (avoids
	// cross-device copies).
	DumpDir string
}

// Populate is the entry point for --client=reth. It:
//  1. Streams every generated account into a JSONL state dump while
//     computing the MPT state root via go-ethereum's StackTrie.
//  2. Builds the genesis block header from the chainspec + computed root,
//     RLP-encodes it, and records its keccak hash.
//  3. Invokes `reth init-state --without-evm --header <path> --header-hash
//     <hex>` which imports the state line-by-line (streaming) and uses our
//     pre-built header for the genesis block.
//
// The --without-evm + --header path avoids Reth's default `init` flow,
// which parses the entire chainspec.alloc into memory — unworkable at
// 100 GB+ state sizes. With streaming init-state, Reth's peak memory is
// bounded by its own write buffers, not by input size.
//
// Stats.StateRoot is populated with the Go-computed root. Callers should
// verify via `eth_getBlockByNumber` after boot that Reth accepted it.
func Populate(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	// Resolve reth binary up-front so callers fail fast if it's missing.
	var rethBin string
	if !opts.SkipRethInvocation {
		if opts.RethBinaryPath != "" {
			RethBinaryPath = opts.RethBinaryPath
		}
		resolved, err := findRethBinary()
		if err != nil {
			return nil, err
		}
		rethBin = resolved
	}

	if err := os.MkdirAll(cfg.DBPath, 0o755); err != nil {
		return nil, fmt.Errorf("create datadir: %w", err)
	}
	chainSpecPath := opts.ChainSpecPath
	if chainSpecPath == "" {
		chainSpecPath = filepath.Join(cfg.DBPath, "chainspec.json")
	}
	headerPath := filepath.Join(cfg.DBPath, "genesis-header.rlp")

	gen, err := loadGenesisForReth(genesisPathFromCfg(cfg))
	if err != nil {
		return nil, err
	}
	chainID := deriveChainID(chainIDFromCfg(cfg), gen)

	if err := writeChainSpec(genesisPathFromCfg(cfg), chainSpecPath, chainID); err != nil {
		return nil, fmt.Errorf("write chainspec: %w", err)
	}

	// 1. Stream the JSONL state dump. The temp-file lifecycle is owned here
	// so writeDump can stay oblivious to it. Dump lives on the same volume
	// as the DB to avoid a cross-device copy in writeFinalDump.
	dumpDir := opts.DumpDir
	if dumpDir == "" {
		dumpDir = filepath.Join(cfg.DBPath, "dump")
	}
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		return nil, fmt.Errorf("create dump dir: %w", err)
	}
	accountsTempPath := filepath.Join(dumpDir, fmt.Sprintf("accounts-%d.jsonl", time.Now().UnixNano()))
	finalDumpPath := filepath.Join(dumpDir, fmt.Sprintf("statedump-%d.jsonl", time.Now().UnixNano()))

	start := time.Now()
	root, stats, err := streamToTempDump(cfg, accountsTempPath)
	if err != nil {
		os.Remove(accountsTempPath)
		return nil, err
	}
	if err := writeFinalDump(finalDumpPath, accountsTempPath, root); err != nil {
		os.Remove(accountsTempPath)
		os.Remove(finalDumpPath)
		return nil, err
	}
	os.Remove(accountsTempPath)
	stats.GenerationTime = time.Since(start)

	if !opts.KeepDumpFile && !opts.SkipRethInvocation {
		defer os.Remove(finalDumpPath)
	}

	// 2. Build + write the genesis header with our computed root.
	header, err := buildGenesisHeader(gen, chainID, root)
	if err != nil {
		return nil, fmt.Errorf("build genesis header: %w", err)
	}
	headerHash, err := writeHeaderFile(header, headerPath)
	if err != nil {
		return nil, err
	}
	if cfg.Verbose {
		log.Printf("[reth] state_root=%s header_hash=%s", root.Hex(), headerHash.Hex())
	}

	if err := prepareDatadir(cfg.DBPath); err != nil {
		return nil, err
	}

	// 3. Invoke reth init-state --without-evm --header.
	if !opts.SkipRethInvocation {
		if cfg.Verbose {
			log.Printf("[reth] invoking: %s init-state %s --chain %s --datadir %s --without-evm --header %s --header-hash %s",
				rethBin, finalDumpPath, chainSpecPath, cfg.DBPath, headerPath, headerHash.Hex())
		}
		writeStart := time.Now()
		if err := runRethInitState(
			ctx, rethBin, finalDumpPath, chainSpecPath, cfg.DBPath,
			headerPath, headerHash.Hex(), cfg.Verbose,
		); err != nil {
			return nil, err
		}
		stats.DBWriteTime = time.Since(writeStart)
	}

	stats.StateRoot = root
	return &stats, nil
}

// streamToTempDump owns the temp-file lifecycle for writeDump.
func streamToTempDump(cfg generator.Config, path string) (common.Hash, generator.Stats, error) {
	f, err := os.Create(path)
	if err != nil {
		return common.Hash{}, generator.Stats{}, fmt.Errorf("create temp dump: %w", err)
	}
	defer f.Close()
	return writeDump(cfg, f)
}

// validateConfig enforces the Reth-path's supported subset of Config fields.
func validateConfig(cfg generator.Config) error {
	if cfg.DBPath == "" {
		return fmt.Errorf("--db is required for --client=reth")
	}
	if cfg.TrieMode == generator.TrieModeBinary {
		return fmt.Errorf("--binary-trie is not supported with --client=reth (Reth does not implement EIP-7864 binary trie)")
	}
	if cfg.TargetSize > 0 {
		return fmt.Errorf("--target-size is not yet supported with --client=reth")
	}
	if cfg.DeepBranch.Enabled() {
		return fmt.Errorf("--deep-branch-* is not yet supported with --client=reth")
	}
	return nil
}

// prepareDatadir refuses to overwrite an existing Reth DB.
func prepareDatadir(dbPath string) error {
	if _, err := os.Stat(filepath.Join(dbPath, "db", "mdbx.dat")); err == nil {
		return fmt.Errorf("reth database already present at %s/db/mdbx.dat; remove it before re-running with --client=reth", dbPath)
	}
	entries, err := os.ReadDir(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read datadir: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(dbPath, e.Name(), "db", "mdbx.dat")
		if _, err := os.Stat(candidate); err == nil {
			return fmt.Errorf("reth database already present at %s; remove it before re-running with --client=reth", candidate)
		}
	}
	return nil
}

// --- Config helpers ----------------------------------------------------
//
// generator.Config carries no explicit "genesis file path" or "chain id
// override" fields; main.go routes them through package globals. Folded
// into Config in a follow-up if a second client needs the same values.

var (
	// GenesisFilePath is set by main.go when --client=reth is selected.
	GenesisFilePath string
	// ChainIDOverride is set by main.go from the --chain-id flag.
	ChainIDOverride int64
)

func genesisPathFromCfg(_ generator.Config) string { return GenesisFilePath }
func chainIDFromCfg(_ generator.Config) int64      { return ChainIDOverride }
