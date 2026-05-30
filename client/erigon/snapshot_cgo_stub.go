//go:build cgo_erigon && !cgo_erigon_commitment

package erigon

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
)

// writeSnapshots stub for the !cgo_erigon_commitment build. The
// snapshot writer needs the vendored HexPatriciaHashed to compute the
// genesis commitment root + branch nodes (commitment.0-N.kv), and HPH
// is gated behind cgo_erigon_commitment because it pulls in
// github.com/erigontech/erigon. Without the tag the snapshot orchestrator
// returns a clear error so the user sees the missing build flag at
// runtime rather than silently emitting an inconsistent datadir.
func writeSnapshots(
	_ context.Context,
	_ generator.Config,
	_ *FoundationalAlloc,
	_ *generator.Stats,
) (common.Hash, error) {
	return common.Hash{}, errors.New(
		"client/erigon: writeSnapshots requires the cgo_erigon_commitment build tag " +
			"(the snapshot commitment writer depends on Erigon's vendored HexPatriciaHashed)")
}

// patchGenesisHeaderStateRoot's real implementation lives in the
// untagged client/erigon/genesis_patch.go (it has no cgo dependency).
// No build-tag-gated stub needed — the function is unconditionally
// available regardless of cgo_erigon_commitment.
