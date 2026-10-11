package reth

import (
	"bytes"
	"math/rand/v2"
	"testing"

	iReth "github.com/ethereum/state-actor/internal/reth"
)

// TestTrieGroupWriter_AscendingOutput feeds a real HashBuilder over a few
// thousand random keys and checks the group writer turns its post-order
// emission into ascending Puts (ancestor rows aside) without dropping or
// duplicating rows.
func TestTrieGroupWriter_AscendingOutput(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))

	const n = 4000
	seen := map[[32]byte]bool{}
	keys := make([][32]byte, 0, n)
	for len(keys) < n {
		var k [32]byte
		for i := range k {
			k[i] = byte(rng.IntN(256))
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	sortBy32(keys)

	// Unsorted emission set, captured independently for the equality check.
	wantRows := map[string][]byte{}
	hbRef := iReth.NewHashBuilder(func(path iReth.StoredNibbles, node iReth.BranchNodeCompact) error {
		if path.Length == 0 {
			return nil
		}
		var keyBuf, valBuf bytes.Buffer
		path.EncodePackedAccountKey(&keyBuf)
		node.EncodeCompact(&valBuf)
		wantRows[keyBuf.String()] = append([]byte(nil), valBuf.Bytes()...)
		return nil
	})
	for _, k := range keys {
		if err := hbRef.AddLeaf(addrHashToNibbles(k[:]), k[:]); err != nil {
			t.Fatalf("AddLeaf (reference): %v", err)
		}
	}
	hbRef.Root()

	var puts [][]byte // key bytes, in Put call order
	gw := &trieGroupWriter{put: func(rows []trieRow) error {
		for _, r := range rows {
			puts = append(puts, r.key)
		}
		return nil
	}}
	hb := iReth.NewHashBuilder(func(path iReth.StoredNibbles, node iReth.BranchNodeCompact) error {
		if path.Length == 0 {
			return nil
		}
		var keyBuf, valBuf bytes.Buffer
		path.EncodePackedAccountKey(&keyBuf)
		node.EncodeCompact(&valBuf)
		return gw.add(path, keyBuf.Bytes(), valBuf.Bytes())
	})
	for _, k := range keys {
		if err := hb.AddLeaf(addrHashToNibbles(k[:]), k[:]); err != nil {
			t.Fatalf("AddLeaf: %v", err)
		}
	}
	hb.Root()
	if err := gw.flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if len(puts) != len(wantRows) {
		t.Fatalf("row count mismatch: got %d puts, reference emitted %d rows", len(puts), len(wantRows))
	}
	gotRows := map[string]bool{}
	for _, k := range puts {
		gotRows[string(k)] = true
	}
	for k := range wantRows {
		if !gotRows[k] {
			t.Fatalf("reference row %x missing from group-writer output", k)
		}
	}

	// Ascending except rows with path length < 2 (<= 16 bytes[32]==0xNN
	// encoded as a 33rd length byte < 2) — those are ancestors, Put
	// immediately and may precede a later, smaller sibling-group key.
	for i := 1; i < len(puts); i++ {
		prevLen, curLen := puts[i-1][32], puts[i][32]
		if curLen < 2 || prevLen < 2 {
			continue
		}
		if bytes.Compare(puts[i-1], puts[i]) >= 0 {
			t.Fatalf("non-ascending Put at %d: %x >= %x", i, puts[i-1], puts[i])
		}
	}
}

func sortBy32(keys [][32]byte) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && bytes.Compare(keys[j-1][:], keys[j][:]) > 0; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}
