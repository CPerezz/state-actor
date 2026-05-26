package erigon

import (
	"context"
	"errors"

	"github.com/nerolation/state-actor/generator"
)

// errNotImplemented is returned by the !cgo_erigon build's runImpl. The
// real writer pipeline (Phase A primitives + Phase B SnapshotWriter +
// Phase C orchestrator) lives behind the cgo_erigon build tag and is
// only available inside the Dockerfile.erigon build context.
//
// Project decision: state-actor's Erigon path follows the same Docker-only
// posture as besu/nethermind/reth. Local Go builds without the tag (the
// default) return this error so users don't accidentally think
// `--client=erigon` works on their macOS / Linux dev machine without the
// reimplemented file-format primitives compiled in.
//
// Build with `docker build -f Dockerfile.erigon .` to use it.
var errNotImplemented = errors.New(
	"client/erigon: requires the cgo_erigon build tag. " +
		"--client=erigon is Docker-only — build with `docker build -f Dockerfile.erigon .`. " +
		"See client/erigon/testdata/README.md for the reproducer (or `make smoke-erigon`).",
)

// Run is the public entry point dispatched from main.go's `case "erigon"` arm.
// It delegates to the build-tag-gated runImpl:
//
//   - Built with `-tags cgo_erigon` (Docker only): runImpl in run_cgo.go opens
//     internal/erigon/mdbx.Env (chaindata), drives entitygen → internal/erigon/snap.Writer
//     → seg.Compressor + btindex + existence pipelines, assembles the
//     genesis block via internal/erigon/mdbx.WriteMinimumChaindata.
//   - Built without the tag (local default): runImpl in run_stub.go returns
//     errNotImplemented.
//
// The split keeps macOS/Linux dev builds free of the reimplemented
// primitives' transitive surface while the Docker image carries the real
// writer.
func Run(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return runImpl(ctx, cfg, opts)
}
