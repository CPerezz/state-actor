package erigon

import (
	"context"
	"errors"

	"github.com/nerolation/state-actor/generator"
)

// errNotImplemented is returned by the !cgo_erigon build's runImpl.
// The real writer is gated behind the cgo_erigon build tag (which pulls
// in github.com/erigontech/mdbx-go) and is only compiled into the
// Dockerfile.erigon image. Local `go build` without the tag returns
// this error so users don't think `--client=erigon` silently works.
var errNotImplemented = errors.New(
	"client/erigon: requires the cgo_erigon build tag. " +
		"--client=erigon is Docker-only — build with `docker build -f Dockerfile.erigon .`.",
)

// Run is the public entry point dispatched from main.go's "erigon" arm.
// Built with `-tags cgo_erigon`: delegates to runImpl in run_cgo.go.
// Built without the tag: delegates to the stub in run_stub.go which
// returns errNotImplemented.
func Run(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return runImpl(ctx, cfg, opts)
}
