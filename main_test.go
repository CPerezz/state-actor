package main

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestMainBenchmarkPrintsStats verifies the --benchmark flag's stdout
// path end-to-end:
//   - the flag is wired (main.go's `if *benchmark { ... }` branch runs)
//   - every writer populates non-zero AccountBytes / StorageBytes /
//     CodeBytes (otherwise the printed stats are silently zero — the
//     class of bug issue #70 was filed for)
//
// Runs against --client=geth (the default; no Docker dependency, in-
// process Populate). 10 EOAs + 3 contracts with at least 1 slot each is
// enough to make all three byte counts strictly positive.
//
// `go run .` compiles state-actor on the fly, so the build tag matrix
// (cgo_neth, cgo_reth, etc.) doesn't matter — geth is the always-built
// default. The test compiles the binary once via `go build` to avoid
// paying the compile cost per assertion if more cases are added later.
func TestMainBenchmarkPrintsStats(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark e2e in short mode")
	}

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "state-actor")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build state-actor: %v\n%s", err, out)
	}

	dbDir := filepath.Join(binDir, "chaindata")
	run := exec.Command(binPath,
		"--db", dbDir,
		"--accounts", "10",
		"--contracts", "3",
		"--min-slots", "1",
		"--max-slots", "2",
		"--seed", "42",
		"--benchmark",
	)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("state-actor --benchmark exited: %v\n%s", err, out)
	}
	stdout := string(out)

	// Section header must appear (proves the `if *benchmark { ... }` branch fired).
	if !strings.Contains(stdout, "=== Detailed Stats ===") {
		t.Fatalf("--benchmark output missing '=== Detailed Stats ===' section header.\nFull stdout:\n%s", stdout)
	}

	// Per-category byte counts must be strictly positive — a writer that
	// doesn't populate stats.{Account,Storage,Code}Bytes would print 0.
	for _, category := range []string{"Account Bytes", "Storage Bytes", "Code Bytes"} {
		if got := parseByteRow(t, stdout, category); got == 0 {
			t.Errorf("--benchmark reports %q == 0 (writer didn't populate the corresponding Stats field). Full stdout:\n%s", category, stdout)
		}
	}
}

// parseByteRow extracts the numeric byte count from a "Category: N.NN MB"
// row in state-actor's stdout. Returns 0 if the row is absent OR the value
// is "0 B". formatBytes (main.go) emits one of the SI scales so this matches
// "N B", "N KB", "N MB", "N GB", "N TB" — any non-empty number scales to
// non-zero, and "0 B" returns 0.
func parseByteRow(t *testing.T, stdout, category string) uint64 {
	t.Helper()
	// e.g. "Account Bytes:     1.23 KB" or "Account Bytes:     0 B"
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(category) + `:\s+([0-9.]+)\s+([KMGT]?B)$`)
	m := re.FindStringSubmatch(stdout)
	if m == nil {
		t.Errorf("could not find %q row in stdout", category)
		return 0
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Errorf("parse %q value %q: %v", category, m[1], err)
		return 0
	}
	// Any non-zero numeric is fine — we just need to detect zero.
	mult := uint64(1)
	switch m[2] {
	case "B":
		mult = 1
	case "KB":
		mult = 1024
	case "MB":
		mult = 1024 * 1024
	case "GB":
		mult = 1024 * 1024 * 1024
	case "TB":
		mult = 1024 * 1024 * 1024 * 1024
	}
	return uint64(val * float64(mult))
}
