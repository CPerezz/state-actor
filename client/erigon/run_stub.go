//go:build !cgo_erigon

// runImpl stub for the !cgo_erigon build. Returns errNotImplemented so
// `--client=erigon` fails fast with a clear Docker-pointing message
// when the build tag is not set.

package erigon

import (
	"context"

	"github.com/nerolation/state-actor/generator"
)

func runImpl(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	_ = ctx
	_ = cfg
	_ = opts
	return nil, errNotImplemented
}
