package specbuild

import (
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/nerolation/state-actor/internal/spec"
)

// ResolveAddress picks an address for a spec entity using three deterministic
// modes:
//
//  1. Explicit: e.Address != nil → return that.
//  2. Name-derived: e.Address == nil, e.Name != "" → keccak256(seed||name)[12:].
//  3. Position-derived: both empty → keccak256(seed||"anon-N")[12:] where N
//     is the entity's 0-based position in the spec.
//
// All three modes are pure functions of (seed, spec). Same inputs always
// produce the same address — critical for the cross-client state-root
// invariant in CI.
//
// Note about mode 3: reordering entities in the YAML changes their derived
// addresses (because the index changes). Modes 1 and 2 are stable across
// reorderings. The user documentation calls this out.
func ResolveAddress(seed int64, e spec.Entity, index int) common.Address {
	if e.Address != nil {
		return e.Address.Address()
	}
	var seedBytes [8]byte
	binary.BigEndian.PutUint64(seedBytes[:], uint64(seed))

	var key string
	if e.Name != "" {
		key = e.Name
	} else {
		key = fmt.Sprintf("anon-%d", index)
	}
	hash := crypto.Keccak256(seedBytes[:], []byte(key))
	return common.BytesToAddress(hash[12:])
}
