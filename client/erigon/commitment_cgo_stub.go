//go:build cgo_erigon && !cgo_erigon_commitment

package erigon

import (
	"github.com/ethereum/go-ethereum/common"
)

// runCommitmentPhase stub for the !cgo_erigon_commitment build.
// Returns zero values + false so the caller leaves stats.StateRoot
// unchanged (the daemon will recompute commitment on first FCU — but
// in the snapshot-tier model this is a degenerate state because no
// commitment.0-N.kv file is emitted, so the integrity checker will
// hide the value-domain files. The stub build is for unit-test-only
// regression coverage; production runs require cgo_erigon_commitment).
func runCommitmentPhase(
	_ map[common.Address]*allocAccount,
	_ map[[20]byte]map[[32]byte][32]byte,
) (common.Hash, map[string][]byte, []byte, bool, error) {
	return common.Hash{}, nil, nil, false, nil
}
