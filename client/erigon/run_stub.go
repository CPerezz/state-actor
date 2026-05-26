//go:build !cgo_erigon

// Build without the cgo_erigon tag: no internal/erigon/* primitives
// imported, no SnapshotWriter wired up. runImpl returns the canned
// errNotImplemented error so `--client=erigon` fails fast with a clear
// message pointing at Docker.
//
// This file is the only one in client/erigon/ that compiles without the tag;
// everything else under client/erigon/ that touches the Erigon writer
// pipeline is gated behind `//go:build cgo_erigon` and excluded from the
// build entirely.

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
