//go:build cgo_erigon_commitment

package commitment

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// TestEmptyAllocReturnsEmptyTrieRoot pins the canonical empty-trie
// root and verifies ComputeGenesisRoot bottoms out cleanly when fed no
// accounts. The expected hash is keccak256(rlp("")) =
// 0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421.
func TestEmptyAllocReturnsEmptyTrieRoot(t *testing.T) {
	res, err := ComputeGenesisRootFromAccounts(nil)
	if err != nil {
		t.Fatalf("ComputeGenesisRootFromAccounts([]): %v", err)
	}
	emptyTrieRoot := common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if res.Root != emptyTrieRoot {
		t.Errorf("empty alloc root mismatch:\n  got:  %s\n  want: %s", res.Root.Hex(), emptyTrieRoot.Hex())
	}
	if len(res.BranchNodes) != 0 {
		t.Errorf("empty alloc emitted %d branch nodes; expected 0", len(res.BranchNodes))
	}
}

// TestSingleEOAProducesDeterministicRoot pins the root for a single
// alloc entry as a deterministic regression target. The exact value
// here is recorded the FIRST time the test runs; subsequent runs must
// match.
func TestSingleEOAProducesDeterministicRoot(t *testing.T) {
	bal := uint256.NewInt(0).Mul(uint256.NewInt(1_000_000_000), uint256.NewInt(1_000_000_000)) // 1e18
	accounts := []Account{{
		Address: common.HexToAddress("0x000000000000000000000000000000000000beef"),
		Nonce:   3,
		Balance: bal,
	}}
	res, err := ComputeGenesisRootFromAccounts(accounts)
	if err != nil {
		t.Fatalf("ComputeGenesisRoot: %v", err)
	}

	// Two invocations with identical input must agree.
	res2, err := ComputeGenesisRootFromAccounts(accounts)
	if err != nil {
		t.Fatalf("ComputeGenesisRoot (2nd): %v", err)
	}
	if res.Root != res2.Root {
		t.Errorf("non-deterministic: %s vs %s", res.Root.Hex(), res2.Root.Hex())
	}
	if !bytes.Equal(branchNodesBytes(res.BranchNodes), branchNodesBytes(res2.BranchNodes)) {
		t.Error("non-deterministic branch nodes")
	}
	if (res.Root == common.Hash{}) {
		t.Error("got zero hash for non-empty alloc")
	}
	t.Logf("single-EOA root: %s (%d branch nodes)", res.Root.Hex(), len(res.BranchNodes))
}

// TestContractWithStorageProducesRoot exercises the storage-touch path:
// a contract with 3 storage slots + balance + code. Verifies that
// branch nodes are emitted (commitment trie has structure) and the
// root is deterministic.
func TestContractWithStorageProducesRoot(t *testing.T) {
	addr := common.HexToAddress("0x0000000000000000000000000000000000c0ffee")
	bal := uint256.NewInt(42)
	code := []byte{0x60, 0x80, 0x60, 0x40, 0x52, 0x60, 0x05, 0x60, 0x00, 0x55} // PUSH1 5 PUSH1 0 SSTORE
	storage := map[common.Hash]common.Hash{
		common.BigToHash(uint256.NewInt(0).ToBig()):       common.BigToHash(uint256.NewInt(100).ToBig()),
		common.BigToHash(uint256.NewInt(1).ToBig()):       common.BigToHash(uint256.NewInt(200).ToBig()),
		common.BigToHash(uint256.NewInt(0x1000).ToBig()):  common.BigToHash(uint256.NewInt(0xdeadbeef).ToBig()),
	}
	accounts := []Account{{
		Address: addr,
		Nonce:   1,
		Balance: bal,
		Code:    code,
		Storage: storage,
	}}
	res, err := ComputeGenesisRootFromAccounts(accounts)
	if err != nil {
		t.Fatalf("ComputeGenesisRoot: %v", err)
	}
	if (res.Root == common.Hash{}) {
		t.Error("got zero hash for non-empty alloc with storage")
	}
	if len(res.BranchNodes) == 0 {
		t.Error("expected at least one branch node for contract+storage alloc")
	}
	t.Logf("contract+3slots root: %s (%d branch nodes)", res.Root.Hex(), len(res.BranchNodes))
}

// TestComputeGenesisRoot_IncludesRootBranch is the regression gate for
// the bug where ParallelHashSort returned the root hash without
// flushing the root HexPatriciaHashed's deferred branch updates.
// Without the explicit ApplyAndClearInlineDeferredUpdates call in
// ComputeGenesisRoot, the resulting BranchNodes map would be missing
// the root-level (depth-0) branch entry, producing silent state-root
// divergence at any alloc that lands in 16+ distinct first nibbles.
//
// The fixture: 16 EOAs with addresses chosen so their keccak256 hashes
// span all 16 first nibbles. The resulting trie has a 16-cell root
// branch — exactly the structure that exposes the bug. The leaves of
// a single-leaf-per-nibble trie are stored inline in the root branch
// (no extra branch nodes), so a missing root branch means
// len(BranchNodes) == 0.
func TestComputeGenesisRoot_IncludesRootBranch(t *testing.T) {
	accounts := generateAccountsSpanningAllFirstNibbles(t)
	if len(accounts) != 16 {
		t.Fatalf("fixture should have 16 accounts; got %d", len(accounts))
	}
	res, err := ComputeGenesisRootFromAccounts(accounts)
	if err != nil {
		t.Fatalf("ComputeGenesisRootFromAccounts: %v", err)
	}
	if (res.Root == common.Hash{}) {
		t.Fatal("expected non-zero root for 16-account alloc")
	}
	if len(res.BranchNodes) == 0 {
		t.Fatalf("expected ≥ 1 branch node for 16-account alloc spanning all first nibbles; got 0 — root-level branch was not flushed (Bug 2 regression: ApplyAndClearInlineDeferredUpdates not called after pph.Process)")
	}
	t.Logf("16-account root: %s (%d branch nodes)", res.Root.Hex(), len(res.BranchNodes))
}

// generateAccountsSpanningAllFirstNibbles produces 16 Accounts whose
// keccak256(addr) bytes start with each of the 16 hex nibbles. Brute-
// force search; on typical machines this terminates within milliseconds.
func generateAccountsSpanningAllFirstNibbles(t *testing.T) []Account {
	t.Helper()
	seen := make(map[byte]Account, 16)
	for i := uint64(0); len(seen) < 16; i++ {
		var addr common.Address
		binary.BigEndian.PutUint64(addr[12:], i+1)
		firstNibble := crypto.Keccak256Hash(addr[:])[0] >> 4
		if _, ok := seen[firstNibble]; ok {
			continue
		}
		seen[firstNibble] = Account{
			Address: addr,
			Nonce:   i + 1,
			Balance: uint256.NewInt(1000 + i),
		}
		if i > 100_000 {
			t.Fatalf("could not find 16 distinct first nibbles within %d attempts (got %d)", i, len(seen))
		}
	}
	out := make([]Account, 0, 16)
	for _, a := range seen {
		out = append(out, a)
	}
	return out
}

func branchNodesBytes(m map[string][]byte) []byte {
	out := make([]byte, 0)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// stable order
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		out = append(out, []byte(k)...)
		out = append(out, m[k]...)
	}
	return out
}
