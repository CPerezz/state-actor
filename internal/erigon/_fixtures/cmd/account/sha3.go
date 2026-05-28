//go:build erigon_gen

package main

import (
	"hash"

	"golang.org/x/crypto/sha3"
)

func sha3NewLegacyKeccak256() hash.Hash { return sha3.NewLegacyKeccak256() }
