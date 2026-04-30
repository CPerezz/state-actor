//go:build cgo_reth && oracle

package reth

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/nerolation/state-actor/generator"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

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

	// Determine the datadir path. When RETH_ORACLE_DATADIR is set (injected
	// by make test-reth-oracle via a shared Docker named volume), use that
	// path so the reth container can access it. Otherwise fall back to a
	// fresh sub-directory of t.TempDir() (works when running on the host).
	datadir := os.Getenv("RETH_ORACLE_DATADIR")
	if datadir == "" {
		datadir = t.TempDir()
	} else {
		// When using a pre-created directory from a named volume we still
		// need RunCgo to write into it; no cleanup is needed since the
		// volume is removed by the Makefile after the test.
		t.Logf("using RETH_ORACLE_DATADIR=%s", datadir)
	}

	cfg := generator.Config{DBPath: datadir}
	if _, err := RunCgo(context.Background(), cfg, Options{}); err != nil {
		t.Fatalf("RunCgo: %v", err)
	}

	// Pinned reth image and tag from internal/reth/constants.go.
	// Reth is published to GHCR (ghcr.io/paradigmxyz/reth), not Docker Hub.
	// Fall back to known-good values if either constant is empty.
	image := iReth.PinnedRethImage
	if image == "" {
		image = "ghcr.io/paradigmxyz/reth"
	}
	tag := iReth.PinnedRethRelease
	if tag == "" {
		tag = "v2.1.0"
	}
	imageRef := image + ":" + tag

	// The volume mount source must be a path that the Docker daemon
	// (running on the host) can reach. When RETH_ORACLE_DATADIR is set
	// the Makefile guarantees it is a named-volume mount point; the
	// volume name is passed via RETH_ORACLE_VOL.
	volSpec := datadir + ":/data"
	if vol := os.Getenv("RETH_ORACLE_VOL"); vol != "" {
		// Named-volume form: "volname:/data" instead of "hostpath:/data".
		volSpec = vol + ":/data"
	}

	cmd := exec.Command("docker", "run", "--rm",
		"-v", volSpec,
		imageRef,
		"db", "--datadir", "/data", "stats",
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
