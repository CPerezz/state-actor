package besu

import (
	"context"
	"errors"

	"github.com/nerolation/state-actor/generator"
)

// errNotImplemented is returned by the !cgo_besu build's runImpl. The
// stage-2 cgo+grocksdb wiring lives behind the cgo_besu build tag and is
// only available inside the Dockerfile.besu build context.
//
// Project decision: state-actor's Besu path is **Docker-only**. Local Go
// builds without the tag (the default) return this error so users don't
// accidentally think `--client=besu` works on their macOS / Linux dev
// machine without librocksdb installed. Build with
// `docker build -f Dockerfile.besu .` to use it.
var errNotImplemented = errors.New(
	"client/besu: requires the cgo_besu build tag and librocksdb. " +
		"--client=besu is Docker-only — build with `docker build -f Dockerfile.besu .`. " +
		"See client/besu/testdata/README.md for the reproducer (or `make smoke-besu`).",
)

// Run is the public entry point dispatched from main.go's `case "besu"` arm.
// It delegates to the build-tag-gated runImpl:
//
//   - Built with `-tags cgo_besu` (Docker only): runImpl in run_cgo.go opens
//     one grocksdb instance with 8 column families, drives entitygen →
//     trie.Builder → grocksdb writes, assembles the genesis block.
//   - Built without the tag (local default): runImpl in run_stub.go returns
//     errNotImplemented.
//
// The split keeps macOS/Linux dev builds free of cgo and librocksdb while
// the Docker image carries the real writer.
func Run(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	return runImpl(ctx, cfg, opts)
}

// GenesisFilePath / ChainIDOverride are package-level vars set from main.go
// before Run is called, mirroring the nethermind pattern. They surface the
// --genesis and --chain-id CLI flags into the package without threading
// them through the Config struct.
//
// Declared here (no build tag) so both cgo and stub builds see them.
// The cgo runImpl reads them; the stub runImpl ignores them.
//
// ChainIDOverride is warn-and-ignored at runtime: Besu reads chainId from
// --genesis-file at boot, not from the DB. We accept the flag so users
// scripting from the geth path don't get a hard rejection, but log a
// warning so the no-effect-here behavior is visible.
var (
	GenesisFilePath string
	ChainIDOverride int64
)
