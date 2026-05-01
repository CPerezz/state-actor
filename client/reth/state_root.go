package reth

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/nerolation/state-actor/internal/entitygen"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

// ComputeStateRoot returns the MPT state root over the supplied accounts.
//
// Each account's StateAccount is RLP-encoded and fed (in keccak-sorted-by-
// AddrHash order) to a HashBuilder. The returned root matches what
// go-ethereum's trie.StackTrie computes for the same inputs.
//
// Accounts must have AddrHash already populated; entitygen sets this
// when generating EOAs/contracts.
//
// Empty input returns the canonical empty-MPT hash (HashBuilder's empty case).
func ComputeStateRoot(accounts []*entitygen.Account) (common.Hash, error) {
	sorted := make([]*entitygen.Account, len(accounts))
	copy(sorted, accounts)
	sortAccountsByAddrHash(sorted)

	hb := iReth.NewHashBuilder(func(p iReth.StoredNibbles, n iReth.BranchNodeCompact) error {
		return nil // emissions go nowhere; we only want the root
	})

	for _, acc := range sorted {
		if acc.StateAccount == nil {
			return common.Hash{}, fmt.Errorf("ComputeStateRoot: account at addr %s has nil StateAccount", acc.Address.Hex())
		}
		rlpBytes, err := rlp.EncodeToBytes(acc.StateAccount)
		if err != nil {
			return common.Hash{}, fmt.Errorf("ComputeStateRoot: rlp encode %s: %w", acc.Address.Hex(), err)
		}
		nibbles := addrHashToNibbles(acc.AddrHash[:])
		if err := hb.AddLeaf(nibbles, rlpBytes); err != nil {
			return common.Hash{}, fmt.Errorf("ComputeStateRoot: AddLeaf %s: %w", acc.Address.Hex(), err)
		}
	}

	return hb.Root(), nil
}

// ComputeStateRootStreaming returns the MPT state root from a sorted-by-key
// stream of (addrHash, accountRLP) pairs. The supplied iter callback is
// invoked exactly once and is expected to call yield for each pair in
// ascending addrHash order — the HashBuilder enforces that invariant.
//
// This is the streaming counterpart of ComputeStateRoot used by RunCgo
// Phase 4 to drain a Sorter (Pebble auto-sorts on iterate) without holding
// every account in RAM. The resulting root is byte-identical to what
// ComputeStateRoot would produce over the same set, given the same
// sort-by-AddrHash order.
//
// Memory bound: O(trie depth * 33 bytes) ≈ 2 KB regardless of how many
// pairs the iterator emits. HashBuilder.AddLeaf copies its inputs, so the
// caller's slices (e.g. Pebble's iter.Key()/iter.Value() which alias
// internal buffers) can be safely reused after each yield call.
func ComputeStateRootStreaming(iter func(yield func(addrHash, accountRLP []byte) error) error) (common.Hash, error) {
	hb := iReth.NewHashBuilder(func(p iReth.StoredNibbles, n iReth.BranchNodeCompact) error {
		return nil // emissions go nowhere; we only want the root
	})

	err := iter(func(addrHash, accountRLP []byte) error {
		nibbles := addrHashToNibbles(addrHash)
		if err := hb.AddLeaf(nibbles, accountRLP); err != nil {
			return fmt.Errorf("AddLeaf: %w", err)
		}
		return nil
	})
	if err != nil {
		return common.Hash{}, fmt.Errorf("ComputeStateRootStreaming: iter: %w", err)
	}

	return hb.Root(), nil
}

// sortAccountsByAddrHash sorts in place by AddrHash ascending.
func sortAccountsByAddrHash(accounts []*entitygen.Account) {
	sort.Slice(accounts, func(i, j int) bool {
		return bytes.Compare(accounts[i].AddrHash[:], accounts[j].AddrHash[:]) < 0
	})
}

// addrHashToNibbles unpacks each byte to two nibbles, high then low.
// Mirrors internal/reth's unexported bytesToNibbles helper; duplicated
// here to avoid widening internal/reth's public surface.
func addrHashToNibbles(b []byte) []byte {
	out := make([]byte, 2*len(b))
	for i, c := range b {
		out[2*i] = c >> 4
		out[2*i+1] = c & 0x0f
	}
	return out
}
