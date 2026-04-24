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
// reth via PATH, clean up the temp JSONL, log only when cfg.Verbose is set.
type Options struct {
	// RethBinaryPath overrides the resolution of the `reth` binary. When
	// empty, PATH lookup is used.
	RethBinaryPath string

	// KeepDumpFile, when true, leaves the generated JSONL state dump file
	// on disk after a successful run. Useful for debugging. The path is
	// returned in Stats.DumpPath.
	KeepDumpFile bool

	// DumpDir specifies the directory for the intermediate JSONL file.
	// When empty, os.TempDir() is used. The file itself is named
	// reth-statedump-<nanos>.jsonl.
	DumpDir string

	// SkipRethInvocation, when true, skips the final reth init-state call.
	// The JSONL dump and chainspec are still produced (so unit tests can
	// assert on their contents without needing the reth binary). In this
	// mode Stats.StateRoot is still populated.
	SkipRethInvocation bool

	// ChainSpecPath, when non-empty, causes the produced chainspec to be
	// written to this path (normally a caller-chosen stable location).
	// When empty, a temporary file is used and removed after success.
	ChainSpecPath string
}

// Populate is the entry point for --client=reth. It generates state
// deterministically from cfg, writes a JSONL state dump, computes the MPT
// state root, and invokes `reth init-state` to create a Reth-compatible
// MDBX database at cfg.DBPath.
//
// On success returns a populated generator.Stats (matching the shape the
// geth path returns, so main.go can summarize both uniformly).
//
// Limitations vs the geth path:
//   - cfg.TargetSize is ignored (no way to mid-generation stop; feature-flag
//     this at the CLI).
//   - cfg.DeepBranch is ignored (the deep-branch encoding relies on
//     hash-scheme trie nodes in the DB that Reth won't accept).
//   - cfg.TrieMode must be MPT; binary-trie is rejected.
//   - cfg.WriteTrieNodes is irrelevant (Reth writes its own trie).
//
// The caller is expected to validate these at CLI parse time; if Populate
// is called with an unsupported config, it returns an error immediately.
func Populate(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}

	// Resolve reth binary up-front so callers fail fast if it's missing,
	// rather than after spending minutes generating a multi-GB dump.
	var rethBin string
	if !opts.SkipRethInvocation {
		override := RethBinaryPath
		if opts.RethBinaryPath != "" {
			override = opts.RethBinaryPath
		}
		resolved, err := func() (string, error) {
			if override != "" {
				RethBinaryPath = override
				return findRethBinary()
			}
			return findRethBinary()
		}()
		if err != nil {
			return nil, err
		}
		rethBin = resolved
	}

	// 1. Build the chainspec file.
	chainSpecPath := opts.ChainSpecPath
	chainSpecTemp := false
	if chainSpecPath == "" {
		chainSpecTemp = true
		f, err := os.CreateTemp(opts.DumpDir, "reth-chainspec-*.json")
		if err != nil {
			return nil, fmt.Errorf("create chainspec temp: %w", err)
		}
		chainSpecPath = f.Name()
		f.Close()
	}
	gen, err := loadGenesisForReth(genesisPathFromCfg(cfg))
	if err != nil {
		return nil, err
	}
	chainID := deriveChainID(chainIDFromCfg(cfg), gen)
	if err := writeChainSpec(genesisPathFromCfg(cfg), chainSpecPath, chainID); err != nil {
		return nil, err
	}
	if chainSpecTemp {
		defer os.Remove(chainSpecPath)
	}

	// 2. Stream accounts to a temp JSONL file; compute the MPT state root.
	dumpDir := opts.DumpDir
	if dumpDir == "" {
		dumpDir = os.TempDir()
	}
	accountsTempPath := filepath.Join(dumpDir, fmt.Sprintf("reth-accounts-%d.jsonl", time.Now().UnixNano()))
	finalDumpPath := filepath.Join(dumpDir, fmt.Sprintf("reth-statedump-%d.jsonl", time.Now().UnixNano()))

	start := time.Now()
	root, stats, err := streamToTempDump(cfg, accountsTempPath)
	if err != nil {
		os.Remove(accountsTempPath)
		return nil, err
	}
	// Join root header line with the accounts body.
	if err := writeFinalDump(finalDumpPath, accountsTempPath, root); err != nil {
		os.Remove(accountsTempPath)
		os.Remove(finalDumpPath)
		return nil, err
	}
	os.Remove(accountsTempPath) // always clean temp; keep only final
	stats.GenerationTime = time.Since(start)

	if !opts.KeepDumpFile && !opts.SkipRethInvocation {
		defer os.Remove(finalDumpPath)
	}

	// 3. Ensure the target datadir exists and is empty (reth init-state
	// refuses to overwrite an existing state).
	if err := prepareDatadir(cfg.DBPath); err != nil {
		return nil, err
	}

	// 4. Invoke reth init-state unless the caller asked to skip (tests).
	if !opts.SkipRethInvocation {
		if cfg.Verbose {
			log.Printf("[reth] invoking: %s init-state %s --chain %s --datadir %s",
				rethBin, finalDumpPath, chainSpecPath, cfg.DBPath)
		}
		writeStart := time.Now()
		if err := runRethInitState(ctx, rethBin, finalDumpPath, chainSpecPath, cfg.DBPath, cfg.Verbose); err != nil {
			return nil, err
		}
		stats.DBWriteTime = time.Since(writeStart)
	}

	stats.StateRoot = common.BytesToHash(root[:])
	return &stats, nil
}

// streamToTempDump is a thin shim so the temp-file lifecycle is owned in one
// place: it opens the file, passes the writer to writeDump, and closes.
func streamToTempDump(cfg generator.Config, path string) (common.Hash, generator.Stats, error) {
	f, err := os.Create(path)
	if err != nil {
		return common.Hash{}, generator.Stats{}, fmt.Errorf("create temp dump: %w", err)
	}
	defer f.Close()
	return writeDump(cfg, f)
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
// Reth MDBX database. Reth's init-state refuses to overwrite, so we
// fail fast with a clear error instead of letting reth's internal check
// fire.
func prepareDatadir(dbPath string) error {
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		return fmt.Errorf("create datadir: %w", err)
	}
	// Reth puts its MDBX under <datadir>/<chain>/db/mdbx.dat. We can't
	// easily know the chain subdir name up front, so we do a shallow scan:
	// if the datadir contains any subdirectory with a mdbx.dat, refuse.
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
// override" fields (they're resolved by main.go into the Genesis* maps
// before Config reaches the generator). To integrate with --client=reth
// without expanding Config, we route the two values through metadata
// fields we're adding on the Config struct. For this first iteration
// we recover them from the Config's derived state:
//
//   - genesisPath: empty when GenesisAccounts is empty; otherwise
//     inferred from the upstream caller via a package-level var
//     (GenesisFilePath) set from main.go.
//   - chainIDOverride: passed via ChainIDOverride (also a package-level
//     var set from main.go).
//
// This keeps generator.Config backward-compatible. Future PRs can fold
// these into the Config struct if another client needs them.

// GenesisFilePath is set by main.go when --client=reth is selected, so
// the Reth chainspec can mirror the user's genesis.json verbatim. Empty
// means "no genesis file — use the built-in dev chainspec".
var GenesisFilePath string

// ChainIDOverride is set by main.go from the --chain-id flag. Zero means
// "use the value from genesis.json's config.chainId, or 1337 if missing".
var ChainIDOverride int64

func genesisPathFromCfg(_ generator.Config) string { return GenesisFilePath }
func chainIDFromCfg(_ generator.Config) int64      { return ChainIDOverride }
