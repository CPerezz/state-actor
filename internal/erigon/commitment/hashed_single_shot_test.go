//go:build cgo_erigon_commitment

package commitment

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// keccakSpanFixture draws n accounts whose keccak first-nibbles span all 16
// parts with multi-level branch structure (same shape as FirstChunkShrink's
// fixture) — half of them carrying storage so the KeyingHashed
// account+storage co-location is exercised too.
func keccakSpanFixture(n int) []Account {
	accounts := make([]Account, 0, n)
	for i := 0; i < n; i++ {
		var addr common.Address
		binary.BigEndian.PutUint64(addr[12:], uint64(i+1))
		a := Account{
			Address: addr,
			Nonce:   uint64(i + 1),
			Balance: uint256.NewInt(uint64(i + 1)),
		}
		if i%2 == 0 {
			a.Storage = map[common.Hash]common.Hash{}
			for j := 0; j < 2; j++ {
				var k, v common.Hash
				k[0], k[31] = byte(i), byte(j+1)
				v[31] = byte(j + 7)
				a.Storage[k] = v
			}
		}
		accounts = append(accounts, a)
	}
	return accounts
}

// TestComputeGenesisRoot_HashedVsPlainSingleShot is the M1b safety gate: in
// SINGLE-SHOT mode (chunkKeys=0), building the same alloc through the
// KeyingHashed layout (hashed-keyed sub-stores + reused SeekGE Getter reads +
// value-carried plain keys) must yield the byte-identical root, branch count,
// AND branch bytes as the KeyingPlain layout. The keying only changes how
// rows are stored and fetched — never the keccak order or the fold.
func TestComputeGenesisRoot_HashedVsPlainSingleShot(t *testing.T) {
	restore := setCommitmentChunkKeys(0) // single-shot — the only mode KeyingHashed permits
	defer restore()

	accounts := keccakSpanFixture(2048)

	plain, err := ComputeGenesisRootFromAccountsKeyed(accounts, KeyingPlain)
	if err != nil {
		t.Fatalf("KeyingPlain: %v", err)
	}
	hashed, err := ComputeGenesisRootFromAccountsKeyed(accounts, KeyingHashed)
	if err != nil {
		t.Fatalf("KeyingHashed: %v", err)
	}

	if plain.Root != hashed.Root {
		t.Fatalf("ROOT DIVERGED: plain=%s hashed=%s — input keying changed the fold",
			plain.Root.Hex(), hashed.Root.Hex())
	}
	if plain.BranchCount != hashed.BranchCount {
		t.Errorf("branch count differs: plain=%d hashed=%d", plain.BranchCount, hashed.BranchCount)
	}
	if !bytes.Equal(branchNodesBytes(plain.BranchNodes), branchNodesBytes(hashed.BranchNodes)) {
		t.Errorf("branch BYTES differ between plain and hashed keyings")
	}
	t.Logf("hashed == plain: root %s, %d branches", plain.Root.Hex(), plain.BranchCount)
}

// TestComputeGenesisRoot_HashedRejectsChunking pins the guard: the
// hashed+chunked combination (the 209af4a empty-branch bug class) must be
// REJECTED at entry, not silently run.
func TestComputeGenesisRoot_HashedRejectsChunking(t *testing.T) {
	restore := setCommitmentChunkKeys(3)
	defer restore()

	_, err := ComputeGenesisRootFromAccountsKeyed(keccakSpanFixture(16), KeyingHashed)
	if err == nil {
		t.Fatal("hashed keying under chunking was ACCEPTED — the guard is gone")
	}
	t.Logf("guard fired as expected: %v", err)
}
