package reth

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// RethBinaryPath is the resolved path to the reth binary. Callers can
// override it by setting RethBinaryPath before calling Populate (useful
// for tests pointing at a specific build).
var RethBinaryPath string

// findRethBinary returns a path to reth, preferring RethBinaryPath if set
// and non-empty, else falling back to exec.LookPath.
func findRethBinary() (string, error) {
	if RethBinaryPath != "" {
		if _, err := os.Stat(RethBinaryPath); err != nil {
			return "", fmt.Errorf("reth binary path %q not found: %w", RethBinaryPath, err)
		}
		return RethBinaryPath, nil
	}
	p, err := exec.LookPath("reth")
	if err != nil {
		return "", fmt.Errorf("reth binary not found in PATH — install from github.com/paradigmxyz/reth")
	}
	return p, nil
}

// runRethInitState invokes `reth init-state --without-evm --header <hdr>
// --header-hash <hash> --chain <chainspec> --datadir <dir> <dump>`.
//
// `--without-evm` + `--header` skips Reth's chainspec→state-root derivation,
// letting us provide both the state (as a streamed JSONL dump) and the
// genesis header ourselves. This is the only scalable path: Reth's default
// `init` parses the entire chainspec alloc into memory, which OOMs at
// multi-hundred-GB state sizes.
func runRethInitState(
	ctx context.Context,
	rethBin, dumpPath, chainSpecPath, datadir, headerPath, headerHash string,
	verbose bool,
) error {
	args := []string{
		"init-state",
		"--chain", chainSpecPath,
		"--datadir", datadir,
		"--without-evm",
		"--header", headerPath,
		"--header-hash", headerHash,
		dumpPath,
	}
	cmd := exec.CommandContext(ctx, rethBin, args...)

	var stderrBuf bytes.Buffer
	if verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)
	} else {
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderrBuf
	}

	if err := cmd.Run(); err != nil {
		tail := lastLines(stderrBuf.String(), 20)
		return fmt.Errorf("reth init-state failed: %w\n--- last 20 lines of stderr ---\n%s", err, tail)
	}
	return nil
}

// lastLines returns the last n lines of s, or s if fewer than n. Used to
// surface the tail of reth's stderr on failure without spewing the whole log.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
