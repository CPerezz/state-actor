//go:build cgo_erigon_commitment

package commitment

import (
	"hash"

	"golang.org/x/crypto/sha3"
)

func newKeccak() hash.Hash {
	return sha3.NewLegacyKeccak256()
}
