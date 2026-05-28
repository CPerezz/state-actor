//go:build cgo_erigon && !cgo_erigon_commitment

package erigon

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
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
	_ string,
	_ int64,
	_ map[common.Address]*allocAccount,
	_ map[common.Address]*allocAccount,
	_ []autofillContractStorage,
	_ bool,
) (common.Hash, error) {
	return common.Hash{}, errors.New(
		"client/erigon: writeSnapshots requires the cgo_erigon_commitment build tag " +
			"(the snapshot commitment writer depends on Erigon's vendored HexPatriciaHashed)")
}

// patchGenesisHeaderStateRoot stub for the !cgo_erigon_commitment build.
// patchGenesisHeaderStateRoot proper lives in commitment_cgo.go (which
// the cgo_erigon_commitment build provides). Under the no-commitment
// build the header is left untouched — fine, because writeSnapshots
// already returned an error above.
func patchGenesisHeaderStateRoot(_ string, _ common.Hash) error {
	return errors.New(
		"client/erigon: patchGenesisHeaderStateRoot requires the cgo_erigon_commitment build tag")
}
