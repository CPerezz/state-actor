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
