package reth

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestBuildInjectedAccountTrieFields locks in the StateAccount field shape
// that buildInjectedAccount must produce. Root and CodeHash MUST be set to
// their canonical empty values (not zero-hash / nil) — otherwise the RLP
// encoding diverges from how reth's chainspec parser hashes the same alloc
// entry, producing a state-root mismatch on boot.
//
// History: an earlier version left both fields zero, which silently broke
// reth boot whenever --inject-accounts was used (smoke test on 2026-04-28).
func TestBuildInjectedAccountTrieFields(t *testing.T) {
	addr := common.HexToAddress("0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266")
	acc := buildInjectedAccount(addr)

	if acc.StateAccount.Root != types.EmptyRootHash {
		t.Errorf("Root: got %s, want EmptyRootHash %s",
			acc.StateAccount.Root.Hex(), types.EmptyRootHash.Hex())
	}
	if !bytes.Equal(acc.StateAccount.CodeHash, types.EmptyCodeHash.Bytes()) {
		t.Errorf("CodeHash: got %x, want EmptyCodeHash %x",
			acc.StateAccount.CodeHash, types.EmptyCodeHash.Bytes())
	}
	if acc.StateAccount.Nonce != 0 {
		t.Errorf("Nonce: got %d, want 0", acc.StateAccount.Nonce)
	}
	want := new(big.Int).Mul(big.NewInt(999_999_999), big.NewInt(1_000_000_000_000_000_000))
	if got := acc.StateAccount.Balance.ToBig(); got.Cmp(want) != 0 {
		t.Errorf("Balance: got %s, want %s", got, want)
	}
}
