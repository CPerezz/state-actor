package reth

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestHashBuilderFullEmissions_PureLeafProducesEmissions guards the
// genesis-trie persistence contract: state-actor's reth writer requires
// NewHashBuilderFullEmissions to emit at least one BranchNodeCompact for
// a pure-leaf input (no add_branch). The default NewHashBuilder uses
// alloy_trie's incremental-update semantics and emits zero rows in this
// scenario; that's verified by the existing TestGoldenHashBuilderEmissions
// fixtures (which intentionally pin to the Rust reference behavior).
//
// Without this emission contract, reth's payload builder falls back to a
// full HashedAccounts walk on every block and hits MDBX's 300 s read-txn
// timeout on bloatnet-scale DBs.
func TestHashBuilderFullEmissions_PureLeafProducesEmissions(t *testing.T) {
	// Build a tiny synthetic trie: enough leaves spread across nibble
	// branches that internal branches end up ≥ 32 bytes (i.e., hashed).
	// 16 leaves at different first-nibble paths force a fan-out branch
	// at depth 0 which encodes well past 32 bytes.
	leaves := make([]struct {
		key   []byte
		value []byte
	}, 0, 16)
	for i := 0; i < 16; i++ {
		// 32-byte key starting with a distinct top nibble.
		key := make([]byte, 32)
		key[0] = byte(i << 4)
		for j := 1; j < 32; j++ {
			key[j] = byte(i + j)
		}
		// 32-byte value to guarantee leaf+branch encodings exceed 32 bytes.
		val := make([]byte, 32)
		for j := range val {
			val[j] = byte(0xa0 + i + j)
		}
		leaves = append(leaves, struct {
			key   []byte
			value []byte
		}{key: key, value: val})
	}

	type emission struct {
		path StoredNibbles
		node BranchNodeCompact
	}
	var emissions []emission
	emit := func(path StoredNibbles, node BranchNodeCompact) error {
		emissions = append(emissions, emission{path: path, node: node})
		return nil
	}

	hb := NewHashBuilderFullEmissions(emit)
	for _, l := range leaves {
		nibbles := unpackNibbles(l.key)
		if err := hb.AddLeaf(nibbles, l.value); err != nil {
			t.Fatalf("AddLeaf: %v", err)
		}
	}
	root := hb.Root()

	if len(emissions) == 0 {
		t.Fatal("NewHashBuilderFullEmissions emitted zero BranchNodeCompact rows for a 16-leaf pure-leaf input — fullEmissions mode is not active")
	}

	// The last emission must be the root (Length == 0). Sanity-check its
	// RootHash matches what Root() returned.
	last := emissions[len(emissions)-1]
	if last.path.Length != 0 {
		t.Errorf("final emission's path Length = %d, want 0 (root)", last.path.Length)
	}
	if last.node.RootHash == nil {
		t.Errorf("final emission has nil RootHash; want %s", root.Hex())
	} else if *last.node.RootHash != root {
		t.Errorf("final emission RootHash = %s, want %s", last.node.RootHash.Hex(), root.Hex())
	}
}

// TestHashBuilderFullEmissions_PathsInLexicographicOrder pins that
// emissions arrive in strictly ascending StoredNibbles path order, which
// is a load-bearing invariant for MDBX cursor.append fast-path writes
// into AccountsTrie / StoragesTrie.
func TestHashBuilderFullEmissions_PathsInLexicographicOrder(t *testing.T) {
	// Same synthetic 16-leaf trie as above.
	var paths []StoredNibbles
	emit := func(path StoredNibbles, node BranchNodeCompact) error {
		// StoredNibbles is a value type (Length byte + 32-byte Packed
		// hash); appending the value copies it, no separate defensive
		// copy needed.
		paths = append(paths, path)
		return nil
	}
	hb := NewHashBuilderFullEmissions(emit)
	for i := 0; i < 16; i++ {
		key := make([]byte, 32)
		key[0] = byte(i << 4)
		for j := 1; j < 32; j++ {
			key[j] = byte(i + j)
		}
		val := make([]byte, 32)
		for j := range val {
			val[j] = byte(0xa0 + i + j)
		}
		nibbles := unpackNibbles(key)
		if err := hb.AddLeaf(nibbles, val); err != nil {
			t.Fatalf("AddLeaf %d: %v", i, err)
		}
	}
	hb.Root()

	// Lexicographic order on (Length, Packed[:Length nibbles]). For our
	// 16-leaf input, all branch paths have Length <= 1 and are uniquely
	// ordered by Length then by their first nibble.
	prevKey := []byte{}
	for i := range paths {
		var buf bytes.Buffer
		paths[i].EncodeKey(&buf)
		curKey := buf.Bytes()
		if i > 0 && bytes.Compare(prevKey, curKey) >= 0 {
			t.Errorf("emissions out of order at i=%d: prev=%x, cur=%x", i, prevKey, curKey)
		}
		// Save a copy of curKey since buf will be reused next iteration.
		prevKey = append([]byte(nil), curKey...)
	}
}

var _ = common.Hash{} // keep import alive if pruned