//go:build oracle

package geth

import (
	"context"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	stategenesis "github.com/nerolation/state-actor/genesis"
	"github.com/nerolation/state-actor/generator"
	e2e "github.com/nerolation/state-actor/internal/e2e_testing"
	"github.com/nerolation/state-actor/internal/oracle"
	"github.com/nerolation/state-actor/internal/rpcprobe"
	"github.com/nerolation/state-actor/internal/sizecal"
	stspec "github.com/nerolation/state-actor/internal/spec"
	"github.com/nerolation/state-actor/internal/specbuild"
	"github.com/nerolation/state-actor/internal/templates"
)

// defaultGethImage is the upstream geth Docker tag the e2e suite pins
// against. Override with GETH_IMAGE=ethereum/client-go:vX.Y.Z to test a
// pre-release version or a fork.
const defaultGethImage = "ethereum/client-go:v1.17.2"

func gethImageRef() string {
	if v := os.Getenv("GETH_IMAGE"); v != "" {
		return v
	}
	return defaultGethImage
}

// freeTCPPort asks the kernel for an available TCP port. The returned
// port is briefly bound and then released — there's a small race window
// before docker -p binds it back, but in practice this is the standard
// trick for picking parallel-test-safe ports.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// TestE2ESuite — see client/besu/e2e_test.go for the full phase
// description. geth-specific bits:
//   - --fork=osaka (geth's MaxForkForClient ceiling, after the writer migration to internal/genesisheader.Build).
//   - 60M gas matches mainnet-current.
//   - geth runs in --dev mode (PoA + self-emulated CL); --dev.period=1
//     for 1s blocks; --dev.gaslimit=60000000 to match the genesis
//     ceiling. No engine-API mock — geth's --dev mode handles consensus
//     internally.
//   - No DinD; geth runs with -p host-port mapping (geth image is
//     amd64+arm64 so no platform override needed for ubuntu CI).
func TestE2ESuite(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e suite skipped in short mode")
	}

	const (
		seed         = int64(42)
		numAccounts  = 100
		numContracts = 15_000 // ~100 MB warmup before spamoor (avg 27 slots × 240 B/entry)
		codeSize     = 128
		minSlots     = 5
		maxSlots     = 50
	)

	// Pin --fork=osaka (geth's MaxForkForClient ceiling). 60M gas matches
	// mainnet-current.
	g, err := stategenesis.BuildSynthetic("osaka", big.NewInt(1337), 60_000_000,
		1_700_000_000, []byte{0xde, 0xad, 0xbe, 0xef})
	if err != nil {
		t.Fatalf("BuildSynthetic: %v", err)
	}

	// Datadir layout: state-actor writes to <datadir>/geth/chaindata; geth
	// itself takes --datadir=<datadir> and looks for geth/chaindata under
	// it. We mount the parent into the container at /data.
	datadir := t.TempDir()
	cfg := generator.Config{
		DBPath:          filepath.Join(datadir, "geth", "chaindata"),
		NumAccounts:     numAccounts,
		NumContracts:    numContracts,
		CodeSize:        codeSize,
		MinSlots:        minSlots,
		MaxSlots:        maxSlots,
		Seed:            seed,
		Verbose:         true,
		TrieMode:        generator.TrieModeMPT,
		Genesis:         g,
		InjectAddresses: []common.Address{oracle.SpamoorSenderAddr},
	}
	// Deploy EIP-4788/2935/7002/7251 system contracts at their canonical
	// addresses — required for the cross-client genesis-root invariant
	// (besu refuses to boot without them; geth/neth/reth tolerate but
	// would compute a different state root than besu without them).
	oracle.AddPragueSystemContracts(&cfg)

	if _, err := Populate(context.Background(), cfg, Options{}); err != nil {
		t.Fatalf("Populate: %v", err)
	}

	eoas, contracts := oracle.Reproduce(oracle.ReproduceCfg{
		Seed:         seed,
		NumAccounts:  numAccounts,
		NumContracts: numContracts,
		CodeSize:     codeSize,
		MinSlots:     minSlots,
		MaxSlots:     maxSlots,
		Distribution: generator.PowerLaw,
	})

	// Boot upstream geth in --dev mode (PoA, self-emulated CL).
	// --dev.period=1 mines blocks every 1s so spamoor advances quickly.
	// --dev.gaslimit matches the genesis 60M ceiling. --networkid matches
	// the chainID in state-actor's genesis. --db.engine=pebble points
	// geth at our Pebble datadir. --http.api includes txpool so spamoor
	// can monitor pending txs.
	containerName := "state-actor-geth-boot-" + e2e.RandSuffix(8)
	hostPort := freeTCPPort(t)

	// Run geth as the test process's UID/GID so any files it writes to
	// the bind-mounted /data stay owned by the test user. Without this,
	// on native Linux (e.g. GHA runners), geth's default-root container
	// would write root-owned files into datadir, and t.TempDir's cleanup
	// (running as the test user) would fail with "permission denied".
	runArgs := append([]string{"run", "-d"}, e2e.DockerPlatformArgs("GETH_DOCKER_PLATFORM")...)
	runArgs = append(runArgs,
		"--name", containerName,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", datadir+":/data",
		"-p", fmt.Sprintf("127.0.0.1:%d:8545", hostPort),
		gethImageRef(),
		"--datadir", "/data",
		"--db.engine", "pebble",
		"--networkid", "1337",
		"--dev",
		"--dev.period", "1",
		"--dev.gaslimit", "60000000",
		"--http",
		"--http.addr", "0.0.0.0",
		"--http.port", "8545",
		"--http.api", "eth,net,web3,txpool",
		"--http.corsdomain", "*",
		"--http.vhosts", "*",
		"--verbosity", "3",
	)
	runOut, err := exec.Command("docker", runArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %s\n%v", runOut, err)
	}
	t.Logf("geth container started: %s", strings.TrimSpace(string(runOut)))

	t.Cleanup(func() {
		logs, _ := exec.Command("docker", "logs", containerName).CombinedOutput()
		t.Logf("geth container logs:\n%s", logs)
		exec.Command("docker", "stop", containerName).Run()    //nolint:errcheck
		exec.Command("docker", "rm", "-f", containerName).Run() //nolint:errcheck
	})

	rpcURL := fmt.Sprintf("http://127.0.0.1:%d", hostPort)
	t.Logf("geth JSON-RPC: %s", rpcURL)

	// Geth typically takes 5–10 s to come up; allow 60 s.
	if err := rpcprobe.WaitForRPC(rpcURL, 60*time.Second); err != nil {
		t.Fatalf("RPC never came up (logs captured in t.Cleanup): %v", err)
	}

	// Phases 3-7: shared via internal/e2e.RunSuitePhases.
	e2e.RunSuitePhases(t, e2e.SuitePhasesCfg{
		ClientName:      "geth",
		RPCURL:          rpcURL,
		EOAs:            eoas,
		Contracts:       contracts,
		GeneratorConfig: &cfg,
	})
}

// TestE2ESuiteSpec is the geth-driven end-to-end test for the --spec
// YAML feature: it loads examples/spec-ci-min.yaml, translates the
// spec into cfg.PreAlloc, runs Populate (which materializes PreAlloc
// into the legacy GenesisAccounts/Code/Storage maps via the
// back-compat shim and writes a real on-disk datadir), boots geth in
// --dev mode against that datadir, and runs the shared
// RunSuitePhases boot → RPC re-query → spamoor → re-query flow.
//
// This is the test that pins the user's stated bar: "run state-actor
// with this new feature prior to using spamoor and run the common-
// golden-hash checks etc with this feature."
//
// Tier 1 of plan Part 7: geth only. Tier 2 (besu/nethermind/reth) lands
// once the cross-client spec invariant aggregator is wired (v1.5).
//
// Build tag matches TestE2ESuite (oracle).
func TestE2ESuiteSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e spec suite skipped in short mode")
	}

	const seed = int64(42)

	g, err := stategenesis.BuildSynthetic("osaka", big.NewInt(1337), 60_000_000,
		1_700_000_000, []byte{0xde, 0xad, 0xbe, 0xef})
	if err != nil {
		t.Fatalf("BuildSynthetic: %v", err)
	}

	// Resolve the spec fixture path relative to the test file. Using
	// path/filepath here so the test works both locally and inside the
	// CI container where the working directory differs.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	specPath := filepath.Join(wd, "..", "..", "examples", "spec-ci-min.yaml")
	specDoc, err := stspec.ParseFile(specPath)
	if err != nil {
		t.Fatalf("spec.ParseFile %s: %v", specPath, err)
	}
	if _, err := specDoc.Validate(templates.UserVisibleNames()); err != nil {
		t.Fatalf("spec.Validate: %v", err)
	}

	// Use sizecal.NewFixed(64) so this test (and any future cross-client
	// variant) doesn't depend on the per-client calibration factors —
	// neutralizing the calibration-divergence risk Agent A flagged.
	sizer := sizecal.NewFixed(64)
	preAlloc, _, err := specbuild.Build(specDoc, specbuild.BuildOptions{
		Seed: seed, ClientName: "geth", Sizer: sizer,
	})
	if err != nil {
		t.Fatalf("specbuild.Build: %v", err)
	}

	datadir := t.TempDir()
	cfg := generator.Config{
		DBPath:          filepath.Join(datadir, "geth", "chaindata"),
		Seed:            seed,
		Verbose:         true,
		TrieMode:        generator.TrieModeMPT,
		Genesis:         g,
		InjectAddresses: []common.Address{oracle.SpamoorSenderAddr},
		PreAlloc:        preAlloc,
		// No --accounts/--contracts: the spec is the entirety of the state.
	}
	// Prague system contracts: required for cross-client genesis-root
	// invariant once Tier 2 ships. Geth tolerates their absence, but
	// adding them keeps this test forward-compatible with the multi-
	// client invariant.
	oracle.AddPragueSystemContracts(&cfg)

	if _, err := Populate(context.Background(), cfg, Options{}); err != nil {
		t.Fatalf("Populate: %v", err)
	}

	// Boot geth — same flags as TestE2ESuite. The container name suffix
	// differs so this test can run concurrently with TestE2ESuite under
	// `-parallel`.
	containerName := "state-actor-geth-spec-" + e2e.RandSuffix(8)
	hostPort := freeTCPPort(t)
	runArgs := append([]string{"run", "-d"}, e2e.DockerPlatformArgs("GETH_DOCKER_PLATFORM")...)
	runArgs = append(runArgs,
		"--name", containerName,
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", datadir+":/data",
		"-p", fmt.Sprintf("127.0.0.1:%d:8545", hostPort),
		gethImageRef(),
		"--datadir", "/data",
		"--db.engine", "pebble",
		"--networkid", "1337",
		"--dev",
		"--dev.period", "1",
		"--dev.gaslimit", "60000000",
		"--http",
		"--http.addr", "0.0.0.0",
		"--http.port", "8545",
		"--http.api", "eth,net,web3,txpool",
		"--http.corsdomain", "*",
		"--http.vhosts", "*",
		"--verbosity", "3",
	)
	runOut, err := exec.Command("docker", runArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %s\n%v", runOut, err)
	}
	t.Logf("geth container started: %s", strings.TrimSpace(string(runOut)))

	t.Cleanup(func() {
		logs, _ := exec.Command("docker", "logs", containerName).CombinedOutput()
		t.Logf("geth-spec container logs:\n%s", logs)
		exec.Command("docker", "stop", containerName).Run()    //nolint:errcheck
		exec.Command("docker", "rm", "-f", containerName).Run() //nolint:errcheck
	})

	rpcURL := fmt.Sprintf("http://127.0.0.1:%d", hostPort)
	t.Logf("geth-spec JSON-RPC: %s", rpcURL)

	if err := rpcprobe.WaitForRPC(rpcURL, 60*time.Second); err != nil {
		t.Fatalf("RPC never came up (logs captured in t.Cleanup): %v", err)
	}

	// Isolate this test's SuiteResult artifact so it doesn't overwrite
	// the synthetic test's geth-result.json (CI uploads only the
	// synthetic artifact for the cross-client-genesis-root aggregator;
	// the spec aggregator lands in v1.5). t.Setenv restores RESULT_PATH
	// to its prior value on test cleanup.
	t.Setenv("RESULT_PATH", filepath.Join(t.TempDir(), "geth-spec-result.json"))

	// Phases 3-7: shared via internal/e2e.RunSuitePhases.
	// EOAs/Contracts are nil — spec entities are checked by the same
	// CheckInjections path (which walks cfg.GenesisAccounts/Code via RPC).
	e2e.RunSuitePhases(t, e2e.SuitePhasesCfg{
		ClientName:      "geth-spec",
		RPCURL:          rpcURL,
		EOAs:            nil,
		Contracts:       nil,
		GeneratorConfig: &cfg,
	})
}
