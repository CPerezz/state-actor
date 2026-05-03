package trie

import (
	"bytes"
	"testing"
)

// TestRootLocation pins the root location as zero-length.
// Source: BonsaiWorldStateKeyValueStorage.java:275 (key=Bytes.EMPTY).
func TestRootLocation(t *testing.T) {
	if len(RootLocation) != 0 {
		t.Fatalf("RootLocation: len=%d, want 0", len(RootLocation))
	}
}

// TestAppendNibble pins single-nibble append behavior. Critical: nibble 0x0A
// is stored as byte 0x0A (one nibble per byte), NOT 0xA0 (packed).
// Source: CommitVisitor.java:51-54 — Bytes.of(i) for i in [0..15].
func TestAppendNibble(t *testing.T) {
	got := AppendNibble(RootLocation, 0x03)
	want := []byte{0x03}
	if !bytes.Equal(got, want) {
		t.Fatalf("AppendNibble([],0x03): got %x, want %x", got, want)
	}

	got = AppendNibble([]byte{0x03}, 0x0a)
	want = []byte{0x03, 0x0a}
	if !bytes.Equal(got, want) {
		t.Fatalf("AppendNibble([0x03],0x0a): got %x, want %x", got, want)
	}
}

// TestAppendNibble_Immutability ensures AppendNibble does not mutate input.
func TestAppendNibble_Immutability(t *testing.T) {
	base := []byte{0x05}
	_ = AppendNibble(base, 0x0a)
	if len(base) != 1 || base[0] != 0x05 {
		t.Fatalf("input mutated: got %x", base)
	}
}

// TestAppendPath verifies multi-nibble append used during ExtensionNode descent.
// The path argument is already nibble-per-byte; we just concatenate.
func TestAppendPath(t *testing.T) {
	got := AppendPath([]byte{0x01}, []byte{0x02, 0x03})
	want := []byte{0x01, 0x02, 0x03}
	if !bytes.Equal(got, want) {
		t.Fatalf("AppendPath([0x01],[0x02,0x03]): got %x, want %x", got, want)
	}
}

// TestStorageTrieKey verifies storage-trie key layout: accountHash(32) ++ location.
// Source: BonsaiWorldStateKeyValueStorage.java:306-309.
func TestStorageTrieKey(t *testing.T) {
	var ah [32]byte
	for i := range ah {
		ah[i] = 0xab
	}
	got := StorageTrieKey(ah, []byte{0x01, 0x02})
	if len(got) != 34 {
		t.Fatalf("StorageTrieKey length: got %d, want 34", len(got))
	}
	for i := 0; i < 32; i++ {
		if got[i] != 0xab {
			t.Fatalf("StorageTrieKey[%d]=%x, want 0xab", i, got[i])
		}
	}
	if got[32] != 0x01 || got[33] != 0x02 {
		t.Fatalf("StorageTrieKey suffix: got %x, want [0x01,0x02]", got[32:])
	}
}

// TestStorageTrieKey_RootLocation handles the storage-root case (location=[]).
// Result is just the 32-byte addrHash.
func TestStorageTrieKey_RootLocation(t *testing.T) {
	var ah [32]byte
	ah[0] = 0xff
	got := StorageTrieKey(ah, RootLocation)
	if len(got) != 32 {
		t.Fatalf("StorageTrieKey(ah, root): len=%d, want 32", len(got))
	}
	if got[0] != 0xff {
		t.Fatalf("StorageTrieKey(ah, root)[0]=%x, want 0xff", got[0])
	}
}

// TestNibbleRange verifies all locations stay in [0x00, 0x0F] range.
// This is the load-bearing invariant: nibbles are NOT packed.
func TestNibbleRange(t *testing.T) {
	for i := byte(0); i <= 15; i++ {
		got := AppendNibble(nil, i)
		if got[0] != i {
			t.Fatalf("AppendNibble(nil, %#x)[0]=%#x, want %#x", i, got[0], i)
		}
		if got[0] >= 16 {
			t.Fatalf("AppendNibble(nil, %#x) leaked into high nibble: %#x", i, got[0])
		}
	}
}
