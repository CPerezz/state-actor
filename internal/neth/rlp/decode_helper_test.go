package rlp

import (
	"github.com/ethereum/go-ethereum/core/types"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// decodeHeaderForTest is a test-only round-trip helper. Lives in its own
// file so the import sits next to its single user.
func decodeHeaderForTest(b []byte, h *types.Header) error {
	return gethrlp.DecodeBytes(b, h)
}

// decodeBlockForTest mirrors decodeHeaderForTest for *types.Block.
func decodeBlockForTest(b []byte, blk *types.Block) error {
	return gethrlp.DecodeBytes(b, blk)
}
