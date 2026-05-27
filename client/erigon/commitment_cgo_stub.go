//go:build cgo_erigon && !cgo_erigon_commitment

package erigon

import (
	"github.com/ethereum/go-ethereum/common"

	erigonmdbx "github.com/nerolation/state-actor/internal/erigon/mdbx"
)

// runCommitmentPhase stub for the !cgo_erigon_commitment build.
// Returns zero-hash + false so the caller leaves stats.StateRoot
// unchanged (the daemon will recompute commitment on first FCU).
func runCommitmentPhase(
	_ *erigonmdbx.Env,
	_ map[common.Address]*allocAccount,
	_ map[[20]byte]map[[32]byte][32]byte,
) (common.Hash, bool, error) {
	return common.Hash{}, false, nil
}
