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
//
// Default resolution: look up "reth" in PATH.
var RethBinaryPath string

// findRethBinary returns a path to reth, preferring RethBinaryPath if set
// and non-empty, else falling back to exec.LookPath. Returns a clear error
// when neither is available so the user knows to install reth.
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

// runRethInit invokes `reth init --chain <chainSpecPath> --datadir <datadir>`,
// which parses the chainspec's alloc, computes the genesis state root, and
// writes the full MDBX DB (state + genesis block + stage checkpoints).
//
// Output is routed to stdout/stderr when verbose; otherwise stderr is
// captured and only the tail is surfaced on failure.
func runRethInit(ctx context.Context, rethBin, chainSpecPath, datadir string, verbose bool) error {
	args := []string{
		"init",
		"--chain", chainSpecPath,
		"--datadir", datadir,
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
		return fmt.Errorf("reth init failed: %w\n--- last 20 lines of stderr ---\n%s", err, tail)
	}
	return nil
}

// lastLines returns the last n lines of s (newline-separated), or s if it
// has fewer than n lines. Used to surface the tail of reth's stderr on
// failure without spewing the whole log.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
