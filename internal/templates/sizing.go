package templates

import (
	"encoding/binary"
	"iter"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// SynthesizeSlots returns an iter.Seq2 over `count` deterministic
// (key, value) storage pairs derived from (seed, addr, domain). The
// domain disambiguates synthesis paths sharing an address (e.g. ERC-20
// _balances vs EOA storage bloat). Pure closure — O(1) memory, safe
// to consume multiple times. Emission order is i=0..count-1, not
// keccak-sorted.
func SynthesizeSlots(seed int64, addr common.Address, domain string, count int) iter.Seq2[common.Hash, common.Hash] {
	return func(yield func(common.Hash, common.Hash) bool) {
		// Layout: [8B seed BE] [20B addr] [domain bytes] [8B index BE]
		preamble := make([]byte, 8+common.AddressLength+len(domain))
		binary.BigEndian.PutUint64(preamble[:8], uint64(seed))
		copy(preamble[8:8+common.AddressLength], addr[:])
		copy(preamble[8+common.AddressLength:], domain)

		keyBuf := make([]byte, len(preamble)+8)
		valBuf := make([]byte, len(preamble)+8+1)
		copy(keyBuf, preamble)
		copy(valBuf, preamble)
		valBuf[len(preamble)+8] = 0x76 // 'v' — ensures keys and values hash differently

		for i := range count {
			binary.BigEndian.PutUint64(keyBuf[len(preamble):], uint64(i))
			binary.BigEndian.PutUint64(valBuf[len(preamble):len(preamble)+8], uint64(i))

			key := crypto.Keccak256Hash(keyBuf)
			val := crypto.Keccak256Hash(valBuf)
			if !yield(key, val) {
				return
			}
		}
	}
}

// MapToSeq adapts a storage map to iter.Seq2.
func MapToSeq(m map[common.Hash]common.Hash) iter.Seq2[common.Hash, common.Hash] {
	if len(m) == 0 {
		return nil
	}
	return func(yield func(common.Hash, common.Hash) bool) {
		for k, v := range m {
			if !yield(k, v) {
				return
			}
		}
	}
}

// Concat yields from each input iterator in turn. Nil inputs are skipped.
func Concat(seqs ...iter.Seq2[common.Hash, common.Hash]) iter.Seq2[common.Hash, common.Hash] {
	return func(yield func(common.Hash, common.Hash) bool) {
		for _, s := range seqs {
			if s == nil {
				continue
			}
			s(func(k, v common.Hash) bool {
				return yield(k, v)
			})
		}
	}
}

// CollectMap materializes an iter.Seq2 into a map. O(N) memory; do
// not use for multi-million-slot iterators — iterate lazily instead.
func CollectMap(seq iter.Seq2[common.Hash, common.Hash]) map[common.Hash]common.Hash {
	if seq == nil {
		return nil
	}
	out := make(map[common.Hash]common.Hash)
	seq(func(k, v common.Hash) bool {
		out[k] = v
		return true
	})
	return out
}
