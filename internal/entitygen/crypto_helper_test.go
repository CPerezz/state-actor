package entitygen

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// crypto256Direct is the test-only keccak helper used by entitygen_test.go.
// Lives in its own file so the import lives next to the only function that
// uses it.
func crypto256Direct(b []byte) common.Hash {
	return crypto.Keccak256Hash(b)
}
