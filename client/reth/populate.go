package reth

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nerolation/state-actor/generator"
)

// Options gives tests and library users fine-grained control over the
// population workflow. The zero value is the Populate CLI default: resolve
// reth via PATH, clean up temp files, log only when cfg.Verbose is set.
type Options struct {
	// RethBinaryPath overrides the resolution of the `reth` binary. When
	// empty, PATH lookup is used.
	RethBinaryPath string

	// SkipRethInvocation, when true, skips the final `reth init` call. The
	// chainspec is still produced (so unit tests can assert on its shape
	// without needing the reth binary).
	SkipRethInvocation bool

	// ChainSpecPath, when non-empty, causes the produced chainspec to be
	// written to this path (normally a caller-chosen stable location).
	// When empty, a temp file is used and removed after success unless
	// KeepChainSpec is set.
	ChainSpecPath string

	// KeepChainSpec, when true, leaves the generated chainspec on disk
	// after a successful run. Useful for debugging and for running reth
	// outside of state-actor against the same chain config.
	KeepChainSpec bool
}

// Populate is the entry point for --client=reth. It streams every generated
// account into a chainspec alloc, then invokes `reth init` to let Reth
// build its own MDBX database + genesis block from that chainspec.
//
// On success returns a populated generator.Stats (matching the shape the
// geth path returns, so main.go can summarize both uniformly). StateRoot
// is left zero: Reth computes it from alloc, and reading it back from MDBX
// is out of scope for this package. Users can verify via RPC after boot.
//
// Limitations vs the geth path:
//   - cfg.TargetSize is ignored (no way to mid-generation stop).
//   - cfg.DeepBranch is ignored (encoding relies on hash-scheme trie nodes).
//   - cfg.TrieMode must be MPT; binary-trie is rejected.
//   - cfg.WriteTrieNodes is irrelevant (Reth writes its own trie).
//
// The caller is expected to validate these at CLI parse time; if Populate
// is called with an unsupported config, it returns an error immediately.
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

	// 1. Prepare the chainspec output path.
	chainSpecPath := opts.ChainSpecPath
	chainSpecTemp := false
	if chainSpecPath == "" {
		chainSpecTemp = true
		f, err := os.CreateTemp("", "reth-chainspec-*.json")
		if err != nil {
			return nil, fmt.Errorf("create chainspec temp: %w", err)
		}
		chainSpecPath = f.Name()
		f.Close()
	}
	if chainSpecTemp && !opts.KeepChainSpec && !opts.SkipRethInvocation {
		defer os.Remove(chainSpecPath)
	}

	g, err := loadGenesisForReth(genesisPathFromCfg(cfg))
	if err != nil {
		return nil, err
	}
	chainID := deriveChainID(chainIDFromCfg(cfg), g)

	// 2. Stream all accounts into the chainspec's alloc. stats is built up
	// as the walk proceeds.
	var stats generator.Stats
	start := time.Now()
	allocFn := func(w *bufio.Writer) error {
		return streamAlloc(cfg, w, &stats)
	}
	if err := writeChainSpec(genesisPathFromCfg(cfg), chainSpecPath, chainID, allocFn); err != nil {
		return nil, fmt.Errorf("write chainspec: %w", err)
	}
	stats.GenerationTime = time.Since(start)

	// 3. Ensure the target datadir exists and is empty (reth init refuses
	// to overwrite an existing state).
	if err := prepareDatadir(cfg.DBPath); err != nil {
		return nil, err
	}

	// 4. Invoke `reth init` unless the caller asked to skip (tests).
	if !opts.SkipRethInvocation {
		if cfg.Verbose {
			log.Printf("[reth] invoking: %s init --chain %s --datadir %s",
				rethBin, chainSpecPath, cfg.DBPath)
		}
		writeStart := time.Now()
		if err := runRethInit(ctx, rethBin, chainSpecPath, cfg.DBPath, cfg.Verbose); err != nil {
			return nil, err
		}
		stats.DBWriteTime = time.Since(writeStart)
	}

	return &stats, nil
}

// validateConfig enforces the Reth-path's supported subset of Config fields.
// Returning early with a clear error beats silently ignoring unsupported
// features and producing a DB that surprises the user later.
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

// prepareDatadir ensures cfg.DBPath exists and does not already contain a
// Reth MDBX database. Reth's init refuses to overwrite, so we fail fast
// with a clear error instead of letting reth's internal check fire.
func prepareDatadir(dbPath string) error {
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		return fmt.Errorf("create datadir: %w", err)
	}
	// Reth puts its MDBX under <datadir>/db/mdbx.dat OR
	// <datadir>/<chain>/db/mdbx.dat depending on whether the datadir is
	// chain-specific. Cover both: direct /db/mdbx.dat and any
	// <subdir>/db/mdbx.dat.
	if _, err := os.Stat(filepath.Join(dbPath, "db", "mdbx.dat")); err == nil {
		return fmt.Errorf("reth database already present at %s/db/mdbx.dat; remove it before re-running with --client=reth", dbPath)
	}
	entries, err := os.ReadDir(dbPath)
	if err != nil {
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
// override" fields; they're resolved by main.go into the Genesis* maps
// before Config reaches the generator. To integrate with --client=reth
// without expanding Config, we route the two values through package-level
// variables set from main.go. Future PRs can fold these into Config if
// another client needs them.

// GenesisFilePath is set by main.go when --client=reth is selected, so
// the Reth chainspec can mirror the user's genesis.json verbatim. Empty
// means "no genesis file — use the built-in dev chainspec".
var GenesisFilePath string

// ChainIDOverride is set by main.go from the --chain-id flag. Zero means
// "use the value from genesis.json's config.chainId, or 1337 if missing".
var ChainIDOverride int64

func genesisPathFromCfg(_ generator.Config) string { return GenesisFilePath }
func chainIDFromCfg(_ generator.Config) int64      { return ChainIDOverride }
