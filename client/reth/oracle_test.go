//go:build cgo_reth && oracle

package reth

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/nerolation/state-actor/generator"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

// oracleDatadir holds paths for an oracle test's datadir.
type oracleDatadir struct {
	// hostPath is the path on the test-container (or host) filesystem where
	// RunCgo writes the datadir.
	hostPath string
	// volMount is the -v argument for `docker run`: either "volname:/data"
	// (named-volume DinD mode) or "hostpath:/data" (direct mode).
	volMount string
	// containerDatadir is the path to pass as --datadir to the reth container.
	// In named-volume DinD mode this is "/data/<subdir>"; in direct mode it
	// is "/data".
	containerDatadir string
}

// acquireOracleDatadir returns an oracleDatadir for the calling test and a
// cleanup function. It honours the RETH_ORACLE_DATADIR / RETH_ORACLE_VOL
// env vars that make test-reth-oracle injects when running inside Docker
// (docker-in-docker via socket mount).
//
// When RETH_ORACLE_DATADIR is set, a unique sub-directory named after the
// test is created inside it so that multiple tests sharing the same named
// volume do not collide. The reth container is pointed at the sub-path inside
// the mounted volume.
//
// When neither env var is set the function falls back to t.TempDir() —
// suitable for direct host runs if libmdbx is available.
func acquireOracleDatadir(t *testing.T) (oracleDatadir, func()) {
	t.Helper()
	baseDir := os.Getenv("RETH_ORACLE_DATADIR")
	if baseDir == "" {
		datadir := t.TempDir()
		return oracleDatadir{
			hostPath:         datadir,
			volMount:         datadir + ":/data",
			containerDatadir: "/data",
		}, func() {}
	}

	// Derive a unique sub-directory from the test name so each test gets its
	// own fresh datadir even though they share the same named volume.
	subName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	hostPath := baseDir + "/" + subName
	if err := os.MkdirAll(hostPath, 0o755); err != nil {
		t.Fatalf("acquireOracleDatadir: mkdir %s: %v", hostPath, err)
	}
	t.Logf("using datadir=%s", hostPath)

	vol := os.Getenv("RETH_ORACLE_VOL")
	volMount := hostPath + ":/data"
	containerDatadir := "/data"
	if vol != "" {
		// Named-volume DinD mode: mount the full named volume at /data and
		// point reth at the per-test sub-path within it.
		volMount = vol + ":/data"
		containerDatadir = "/data/" + subName
	}

	return oracleDatadir{
		hostPath:         hostPath,
		volMount:         volMount,
		containerDatadir: containerDatadir,
	}, func() {}
}

// rethImageRef returns the fully-qualified reth image reference from the
// pinned constants, falling back to known-good defaults.
func rethImageRef() string {
	image := iReth.PinnedRethImage
	if image == "" {
		image = "ghcr.io/paradigmxyz/reth"
	}
	tag := iReth.PinnedRethRelease
	if tag == "" {
		tag = "v2.1.0"
	}
	return image + ":" + tag
}

// parseDbStatsEntries extracts the numeric entry count for table from the
// output of `reth db stats`. Returns (count, ok). The output format uses pipe
// separators; the table name appears in column 1 and the entry count in
// column 2.
//
// Conservative: returns false if the format doesn't match expectations.
func parseDbStatsEntries(output, table string) (int, bool) {
	for _, line := range strings.Split(output, "\n") {
		if !strings.Contains(line, table) {
			continue
		}
		fields := strings.Fields(strings.ReplaceAll(line, "|", " "))
		// Find the table name field, then take the next numeric field as count.
		for i, f := range fields {
			if f == table && i+1 < len(fields) {
				if n, err := strconv.Atoi(strings.TrimSpace(fields[i+1])); err == nil {
					return n, true
				}
			}
		}
	}
	return 0, false
}

// TestRethDbStats generates an empty-alloc datadir via RunCgo, then invokes
// the stock paradigmxyz/reth Docker image's `db stats` subcommand against
// it. If our datadir layout is structurally invalid (wrong page size,
// missing tables, wrong schema version), `db stats` exits non-zero.
//
// Gated by both `cgo_reth` AND `oracle` build tags. Run via
// `make test-reth-oracle` — the plain `go test` does not include either tag.
//
// When running inside Docker (docker-in-docker via socket mount), the test
// process's tmp path is not directly accessible to the host Docker daemon.
// Set RETH_ORACLE_DATADIR to a directory that is visible to both the test
// container and the Docker daemon (e.g. a host path bind-mounted into both
// containers, or a Docker named-volume mount point). make test-reth-oracle
// sets this automatically.
func TestRethDbStats(t *testing.T) {
	if testing.Short() {
		t.Skip("oracle test in short mode")
	}

	dd, cleanup := acquireOracleDatadir(t)
	defer cleanup()

	cfg := generator.Config{DBPath: dd.hostPath}
	if _, err := RunCgo(context.Background(), cfg, Options{}); err != nil {
		t.Fatalf("RunCgo: %v", err)
	}

	cmd := exec.Command("docker", "run", "--rm",
		"-v", dd.volMount,
		rethImageRef(),
		"db", "--datadir", dd.containerDatadir, "stats",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reth db stats failed:\noutput:\n%s\nerr: %v", out, err)
	}

	// Sanity: check the output mentions some expected tables.
	output := string(out)
	for _, table := range []string{"PlainAccountState", "HashedAccounts", "Bytecodes"} {
		if !strings.Contains(output, table) {
			t.Errorf("expected table %q in db stats output, got:\n%s", table, output)
		}
	}
}

// TestRethDbStatsSyntheticEOAs generates a 100-EOA datadir and verifies
// reth's `db stats` shows the expected row counts in the EOA-touched tables.
func TestRethDbStatsSyntheticEOAs(t *testing.T) {
	if testing.Short() {
		t.Skip("oracle test in short mode")
	}

	const numAccounts = 100

	dd, cleanup := acquireOracleDatadir(t)
	defer cleanup()

	cfg := generator.Config{
		DBPath:      dd.hostPath,
		NumAccounts: numAccounts,
		Seed:        12345,
	}
	stats, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("RunCgo: %v", err)
	}
	if stats.AccountsCreated != numAccounts {
		t.Fatalf("AccountsCreated = %d, want %d", stats.AccountsCreated, numAccounts)
	}

	cmd := exec.Command("docker", "run", "--rm",
		"-v", dd.volMount,
		rethImageRef(),
		"db", "--datadir", dd.containerDatadir, "stats",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("reth db stats failed:\noutput:\n%s\nerr: %v", out, err)
	}

	output := string(out)

	// Verify the four EOA-touched tables show >= numAccounts entries.
	checks := map[string]int{
		"PlainAccountState": numAccounts,
		"HashedAccounts":    numAccounts,
		"AccountChangeSets": numAccounts,
		"AccountsHistory":   numAccounts,
	}
	for table, minEntries := range checks {
		count, ok := parseDbStatsEntries(output, table)
		if !ok {
			t.Errorf("could not parse entry count for %q from db stats output:\n%s", table, output)
			continue
		}
		if count < minEntries {
			t.Errorf("table %q: %d entries, want >= %d", table, count, minEntries)
		}
	}
}
