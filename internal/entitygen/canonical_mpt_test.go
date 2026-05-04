package entitygen

import (
	"bytes"
	mrand "math/rand"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

// TestCanonicalEntitygenMPTRoot pins the canonical hexary-MPT state root for a
// known entitygen configuration, computed via go-ethereum's reference
// StackTrie implementation. This is the cross-client invariant every MPT-mode
// client adapter (nethermind, besu, reth — anything using entitygen + hexary MPT)
// MUST match.
//
// Same RNG draws → same accounts/codes/slots → same MPT root, regardless of
// on-disk node layout (geth flat snapshot vs nethermind HalfPath vs Besu Bonsai
// path-keyed vs reth MDBX). The state root is a function of state content + trie
// type, NOT storage format.
//
// If a client adapter produces a different hash for this seed/config, either
// its entity generation diverges from entitygen's RNG draw sequence, or its
// trie/RLP encoding has a bug. This test is the reference oracle.
//
// Note: the geth-MPT path in generator/generator.go currently uses inline RNG
// draws that don't match entitygen — its golden hash for the same config is a
// different value. That's a pre-existing generator inconsistency, tracked
// separately. The entitygen-pinned hash here is the value all entitygen-using
// adapters share.
func TestCanonicalEntitygenMPTRoot(t *testing.T) {
	const (
		seed         = int64(12345)
		numAccounts  = 10
		numContracts = 5
		minSlots     = 1
		maxSlots     = 100
		codeSize     = 256
	)
	const expected = "0xddbfa7c1941ff70fe5a692f7552149adc1ae29ebb2b5dc8bb3544c1368bcb0c3"

	rng := mrand.New(mrand.NewSource(seed))

	type acctEntry struct {
		addrHash common.Hash
		rlp      []byte
	}
	var accts []acctEntry

	for i := 0; i < numAccounts; i++ {
		acc := GenerateEOA(rng)
		buf, err := gethrlp.EncodeToBytes(acc.StateAccount)
		if err != nil {
			t.Fatalf("encode EOA %d: %v", i, err)
		}
		accts = append(accts, acctEntry{acc.AddrHash, buf})
	}

	for i := 0; i < numContracts; i++ {
		numSlots := GenerateSlotCount(rng, PowerLaw, minSlots, maxSlots)
		c := GenerateContract(rng, codeSize, numSlots)

		st := trie.NewStackTrie(nil)
		type kv struct {
			keyHash  common.Hash
			valueRLP []byte
		}
		slots := make([]kv, 0, len(c.Storage))
		for _, s := range c.Storage {
			val := s.Value
			raw := val[:]
			start := 0
			for start < len(raw) && raw[start] == 0x00 {
				start++
			}
			vrlp, err := gethrlp.EncodeToBytes(raw[start:])
			if err != nil {
				t.Fatalf("encode slot val: %v", err)
			}
			slots = append(slots, kv{
				keyHash:  crypto.Keccak256Hash(s.Key[:]),
				valueRLP: vrlp,
			})
		}
		sort.Slice(slots, func(i, j int) bool {
			return bytes.Compare(slots[i].keyHash[:], slots[j].keyHash[:]) < 0
		})
		for _, s := range slots {
			st.Update(s.keyHash[:], s.valueRLP)
		}
		c.StateAccount.Root = st.Hash()

		buf, err := gethrlp.EncodeToBytes(c.StateAccount)
		if err != nil {
			t.Fatalf("encode contract %d: %v", i, err)
		}
		accts = append(accts, acctEntry{c.AddrHash, buf})
	}

	sort.Slice(accts, func(i, j int) bool {
		return bytes.Compare(accts[i].addrHash[:], accts[j].addrHash[:]) < 0
	})

	acctTrie := trie.NewStackTrie(nil)
	for _, a := range accts {
		acctTrie.Update(a.addrHash[:], a.rlp)
	}
	got := acctTrie.Hash().Hex()
	if got != expected {
		t.Fatalf("canonical entitygen-MPT root mismatch:\n  got:  %s\n  want: %s\n  (any drift here means a coordinated update across all entitygen-using client adapters is required)",
			got, expected)
	}
}
