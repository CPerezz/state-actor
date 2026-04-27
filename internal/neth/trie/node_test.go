package trie

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// hashRef builds a ChildRef pointing at a fixed 32-byte hash for tests.
func hashRef(b byte) ChildRef {
	var h [32]byte
	for i := range h {
		h[i] = b
	}
	return ChildRef{Keccak: &h}
}

// TestEncodeLeaf_EmptyPathEmptyValue pins the smallest possible leaf:
// path=[], value=[].
//
// Inner content:
//
//	HP(empty, isLeaf=true)        → [0x20]                   = 1 byte payload
//	value=[]                       → 0x80                     = 1 byte payload
//	Each wrapped in RLP byte string:
//	  HP=[0x20] (single byte ≤ 0x7f → emitted as-is)          = 1 byte
//	  value=empty byte string                                 = 1 byte (0x80)
//	List content length = 2 → list header = 0xc2.
func TestEncodeLeaf_EmptyPathEmptyValue(t *testing.T) {
	got := EncodeLeaf([]byte{}, []byte{})
	want, _ := hex.DecodeString("c2" + "20" + "80")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeLeaf empty:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeLeaf_SmallValue confirms the value is RLP-byte-string-wrapped.
// path=[0x1, 0x2, 0x3] (odd), value=[0xab]:
//
//	HP([1,2,3], true) = [0x31, 0x23] (2 bytes)
//	  → RLP: 0x82 0x31 0x23 (byte string of length 2)
//	value [0xab] (single byte > 0x7f) → 0x81 0xab
//	Inner = 3 + 2 = 5 bytes → list header 0xc5.
func TestEncodeLeaf_SmallValue(t *testing.T) {
	got := EncodeLeaf([]byte{0x1, 0x2, 0x3}, []byte{0xab})
	want, _ := hex.DecodeString("c5" + "823123" + "81ab")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeLeaf small:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeExtension_HashChild covers the canonical case: short path,
// child is a 32-byte keccak hash.
//
//	HP([0xa, 0xb], false) = [0x00, 0xab] (2 bytes)
//	  → RLP: 0x82 0x00 0xab
//	hash child (32 bytes of 0x55) → 0xa0 || 32 bytes
//	  → already RLP-shaped, copied as RawValue (33 bytes total)
//	Inner = 3 + 33 = 36 bytes ≤ 55 → list header = 0xc0 + 36 = 0xe4.
func TestEncodeExtension_HashChild(t *testing.T) {
	var hash [32]byte
	for i := range hash {
		hash[i] = 0x55
	}

	got := EncodeExtension([]byte{0xa, 0xb}, ChildRef{Keccak: &hash})

	want, _ := hex.DecodeString(
		"e4" + // list header (36 bytes content)
			"8200ab" + // HP(extension, [0xa, 0xb])
			"a0" + "5555555555555555555555555555555555555555555555555555555555555555",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeExtension hash:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeExtension_InlineChild covers the rare-but-critical case
// (CCD-flagged): child's RLP is < 32 bytes, parent embeds it verbatim.
//
// We construct a ~10-byte synthetic inline RLP and confirm it appears
// untouched in the parent's payload.
//
//	HP([0x1], false) = [0x11]                               = 1 byte
//	  → RLP: 0x11 (single byte ≤ 0x7f, emitted as-is)
//	inline child RLP = [0xc4, 0x80, 0x80, 0x80, 0x80]      (5 bytes)
//	  → RawValue, copied verbatim
//	Inner = 1 + 5 = 6 bytes → list header = 0xc6.
func TestEncodeExtension_InlineChild(t *testing.T) {
	inline := []byte{0xc4, 0x80, 0x80, 0x80, 0x80} // arbitrary < 32-byte RLP

	got := EncodeExtension([]byte{0x1}, ChildRef{InlineRLP: inline})

	want, _ := hex.DecodeString("c6" + "11" + "c480808080")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeExtension inline:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBranch_AllNullEmptyValue: 17 empty byte strings.
//
//	17 × 0x80 = 17 bytes content → list header 0xc0+17 = 0xd1.
func TestEncodeBranch_AllNullEmptyValue(t *testing.T) {
	var children [16]ChildRef // all zero → all null

	got := EncodeBranch(children, []byte{})

	want, _ := hex.DecodeString("d1" + "80808080808080808080808080808080" + "80")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBranch all-null:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBranch_OneHashChild_Index3 confirms the hash child sits at
// the correct slot.
//
//	children[3] = hash(0x77) → 33 bytes
//	other 15 children = null → 15 × 1 = 15 bytes
//	value = empty → 1 byte
//	Inner = 33 + 15 + 1 = 49 bytes ≤ 55 → list header 0xc0+49 = 0xf1.
func TestEncodeBranch_OneHashChild_Index3(t *testing.T) {
	var children [16]ChildRef
	children[3] = hashRef(0x77)

	got := EncodeBranch(children, nil)

	want, _ := hex.DecodeString(
		"f1" + // list header (49 bytes content)
			"808080" + // children[0..2] null
			"a0" + "7777777777777777777777777777777777777777777777777777777777777777" +
			"808080808080808080808080" + // children[4..15] null
			"80", // value empty
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBranch hash@3:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBranch_TwoInlineChildren confirms inline RLP is pasted at
// the right slots verbatim.
func TestEncodeBranch_TwoInlineChildren(t *testing.T) {
	var children [16]ChildRef
	children[0] = ChildRef{InlineRLP: []byte{0xc4, 0x80, 0x80, 0x80, 0x80}} // 5 bytes
	children[5] = ChildRef{InlineRLP: []byte{0xc3, 0x80, 0x80, 0x80}}      // 4 bytes

	got := EncodeBranch(children, nil)

	// Content: 5 (children[0]) + 4 (1..4 nulls) + 4 (children[5]) +
	//          10 (6..15 nulls) + 1 (value) = 24
	want, _ := hex.DecodeString(
		"d8" + // list header (24 bytes content)
			"c480808080" + // children[0] inline
			"80808080" + // children[1..4] null
			"c3808080" + // children[5] inline
			"80808080808080808080" + // children[6..15] null (10 nulls)
			"80", // value
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBranch two-inline:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBranch_WithNonEmptyValue exercises the value slot. State /
// storage tries never use this in practice, but the encoder must support
// it for round-trip testing.
func TestEncodeBranch_WithNonEmptyValue(t *testing.T) {
	var children [16]ChildRef
	got := EncodeBranch(children, []byte{0xab})

	// 16 nulls × 1 = 16, value [0xab] (single byte > 0x7f) → 0x81 0xab = 2.
	// Total 18 → list header 0xd2.
	want, _ := hex.DecodeString("d2" + "80808080808080808080808080808080" + "81ab")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBranch with-value:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeLeaf_LargeValue covers the boundary where the leaf's outer
// list header crosses the 55-byte threshold (0xf7+) — the case for state
// trie leaves carrying full account RLPs (~70 bytes).
//
//	HP([] for an arbitrary 64-nibble path, true): zeroed 32+1 byte header.
//	value: 70 bytes → RLP: 0xb8 0x46 || 70 bytes = 72 bytes
//	HP(64-nibbles): 33 bytes → RLP: 0xa0 || 33 bytes = 34 bytes
//	Inner: 34 + 72 = 106 bytes > 55 → list header 0xf8 0x6a (2 bytes).
func TestEncodeLeaf_LargeValue(t *testing.T) {
	// 64-nibble path of all zeros.
	path := make([]byte, 64)
	// 70-byte value (matches typical empty-account RLP length).
	value := make([]byte, 70)
	for i := range value {
		value[i] = byte(i + 1)
	}

	got := EncodeLeaf(path, value)

	if got[0] != 0xf8 || got[1] != 0x6a {
		t.Fatalf("expected list header 0xf8 0x6a (106-byte content), got 0x%02x 0x%02x", got[0], got[1])
	}
	// Total length = 2 (header) + 106 (content) = 108.
	if len(got) != 108 {
		t.Fatalf("output length: got %d, want 108", len(got))
	}
}
