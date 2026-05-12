package templates

import (
	"encoding/binary"
	"iter"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// SynthesizeSlots returns a streaming iterator over `count` deterministic
// (key, value) storage pairs derived from `seed`, `addr`, and a `domain`
// string. The domain lets multiple synthesis paths (e.g. ERC-20 _balances vs
// EOA storage bloat) coexist on the same address without collision.
//
// Determinism: for the same (seed, addr, domain, count), the iterator
// produces byte-identical (key, value) pairs across runs. The iterator can
// be consumed multiple times — it is a pure closure with no shared state.
//
// Streaming: no slice or map is allocated. Each iteration computes one
// keccak256 for the key and one for the value; memory footprint is O(1)
// regardless of count.
//
// Caller responsibility: writers that need MPT-sorted insertion must
// materialize the iterator into a sortable structure themselves. The
// iterator's emission order is the index sequence i = 0..count-1, NOT
// keccak-sorted.
func SynthesizeSlots(seed int64, addr common.Address, domain string, count int) iter.Seq2[common.Hash, common.Hash] {
	return func(yield func(common.Hash, common.Hash) bool) {
		// Pre-build the per-entity preamble so the inner loop only mutates
		// the index suffix.
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

// MapToSeq adapts an explicit storage map to the iter.Seq2 interface.
// Useful for templates that mix a small set of explicit slots (e.g. ERC-20
// _totalSupply) with a large set of synthesized slots — they can use this
// for the small set and Concat to combine.
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

// Concat returns an iter.Seq2 that yields from each input iterator in turn.
// Nil inputs are skipped. Useful for combining a small explicit-storage map
// with a large synthesized stream.
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

// CollectMap materializes an iter.Seq2 into a map. Convenience for tests
// and for writers that must operate on a sorted-or-mappable view of the
// storage. Returns nil for nil input.
//
// Memory: proportional to the iterator's yield count. Callers expecting
// 10M+-slot iterators must NOT use this and should iterate lazily instead.
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
