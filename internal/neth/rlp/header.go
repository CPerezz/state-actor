package rlp

import (
	"github.com/ethereum/go-ethereum/core/types"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// EncodeHeader returns the RLP encoding of a block header in the byte
// shape Nethermind's HeaderDecoder expects.
//
// Implementation strategy: wrap go-ethereum's `*types.Header`. Both
// clients follow standard Ethereum RLP (EIP-1559, Shanghai, Cancun,
// Prague), and go-ethereum's Header struct already declares the
// EIP-conditional fields with `rlp:"optional"` tags — the same
// "trailing-optional cascade" Nethermind enforces in its
// `HeaderDecoder.cs:requiredItems[]` backward-propagation loop. If field N
// is non-nil, all earlier optionals are emitted (with zero defaults if
// they were nil), so the wire layout stays in lockstep across clients.
//
// Limitations of this wrapper:
//
//   - Post-Electra fields Nethermind-master adds beyond go-ethereum
//     (BlockAccessListHash, SlotNumber) are not currently representable.
//     state-actor's genesis writer does not activate those forks at
//     block 0, so this is not a blocker for B5; if it becomes one, this
//     encoder grows a Nethermind-specific extended Header type.
//
// Pinned bytes are caught by tests further down the pipeline (B6's Tier 2
// differential oracle), not here — this encoder's correctness is
// equivalent to the geth path's, which is already exercised in CI.
func EncodeHeader(h *types.Header) ([]byte, error) {
	return gethrlp.EncodeToBytes(h)
}
