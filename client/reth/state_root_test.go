package reth

import (
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/entitygen"
)

func TestComputeStateRootEmpty(t *testing.T) {
	got, err := ComputeStateRoot(nil)
	if err != nil {
		t.Fatalf("ComputeStateRoot(nil): %v", err)
	}
	want := common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if got != want {
		t.Errorf("empty root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

func TestComputeStateRootSingleEOA(t *testing.T) {
	acc := makeTestEOA(t, common.HexToAddress("0xaabb"), 100, 1)
	got, err := ComputeStateRoot([]*entitygen.Account{acc})
	if err != nil {
		t.Fatalf("ComputeStateRoot: %v", err)
	}

	// Cross-check against go-ethereum's StackTrie.
	st := trie.NewStackTrie(nil)
	rlpBytes := mustRLP(t, acc.StateAccount)
	if err := st.Update(acc.AddrHash[:], rlpBytes); err != nil {
		t.Fatalf("StackTrie.Update: %v", err)
	}
	want := st.Hash()

	if got != want {
		t.Errorf("single-EOA root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

func TestComputeStateRootManyEOAs(t *testing.T) {
	rng := rand.New(rand.NewSource(0xd00d))
	const n = 50
	accounts := make([]*entitygen.Account, n)
	for i := 0; i < n; i++ {
		accounts[i] = entitygen.GenerateEOA(rng)
	}

	got, err := ComputeStateRoot(accounts)
	if err != nil {
		t.Fatalf("ComputeStateRoot: %v", err)
	}

	// Reference: feed sorted-by-AddrHash directly into StackTrie.
	sorted := make([]*entitygen.Account, n)
	copy(sorted, accounts)
	sortAccountsByAddrHash(sorted)

	st := trie.NewStackTrie(nil)
	for _, a := range sorted {
		rlpBytes := mustRLP(t, a.StateAccount)
		if err := st.Update(a.AddrHash[:], rlpBytes); err != nil {
			t.Fatalf("StackTrie.Update: %v", err)
		}
	}
	want := st.Hash()

	if got != want {
		t.Errorf("50-EOA root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

// makeTestEOA creates a deterministic test EOA with the given address,
// balance in ETH, and nonce. Root and CodeHash are set to the empty values
// matching what GenerateEOA produces.
func makeTestEOA(t *testing.T, addr common.Address, balanceETH uint64, nonce uint64) *entitygen.Account {
	t.Helper()
	bal := new(uint256.Int).Mul(uint256.NewInt(balanceETH), uint256.NewInt(1e18))
	return &entitygen.Account{
		Address:  addr,
		AddrHash: crypto.Keccak256Hash(addr[:]),
		StateAccount: &types.StateAccount{
			Nonce:    nonce,
			Balance:  bal,
			Root:     types.EmptyRootHash,
			CodeHash: types.EmptyCodeHash.Bytes(),
		},
	}
}

// mustRLP encodes v using go-ethereum's RLP encoder or fails the test.
func mustRLP(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := rlp.EncodeToBytes(v)
	if err != nil {
		t.Fatalf("rlp.EncodeToBytes: %v", err)
	}
	return b
}
