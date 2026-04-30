package reth

import (
	"bytes"

	"github.com/ethereum/go-ethereum/common"
)

// StoredNibbles is the v2 33-byte packed nibble path used as the
// AccountsTrie key. PR paradigmxyz/reth#22158 reduced this from 65 to 33 bytes.
//
// Wire (MDBX key, fixed 33 bytes):
//
//	packed (32 bytes; 2 nibbles per byte, high then low) || length (1 byte, 0..=64)
//
// When Length is odd, the final low nibble is zero-padded.
//
// Note: the length byte is at the END (byte[32]), not the beginning.
// This matches PackedStoredNibbles::to_compact in reth-trie-common (nibbles.rs
// lines 193-211) which writes packed[32] then puts the nibble count byte last.
type StoredNibbles struct {
	Length byte
	Packed common.Hash
}

func (s *StoredNibbles) EncodeKey(buf *bytes.Buffer) {
	buf.Write(s.Packed[:])
	buf.WriteByte(s.Length)
}

func (s *StoredNibbles) DecodeKey(b []byte) {
	if len(b) < 33 {
		panic("StoredNibbles: truncated key")
	}
	copy(s.Packed[:], b[0:32])
	s.Length = b[32]
}

// StoredNibblesSubKey is the StoragesTrie sub-key (DupSort sub-key after the
// 32-byte hashed-address main key). Same 33-byte layout as StoredNibbles.
type StoredNibblesSubKey = StoredNibbles

// BranchNodeCompact mirrors alloy_trie::BranchNodeCompact (alloy-trie 0.9.5,
// nodes/branch.rs). Used as the value of AccountsTrie / StoragesTrie.
//
// Wire format (cross-validated against Rust in golden_test.go; canonical
// source: reth-codecs 0.3.1 src/alloy/trie.rs lines 55-75):
//
//  1. state_mask: BE u16
//  2. tree_mask: BE u16
//  3. hash_mask: BE u16
//  4. root_hash: 32 bytes, present ONLY when RootHash != nil
//  5. hashes: popcount(hash_mask) × 32 bytes
//
// Note: there is NO bitflag header byte. root_hash presence is inferred from
// the total encoded length: if (totalLen-6) % 32 == 0 and the hash count
// equals popcount(hash_mask)+1, a root_hash precedes the child hashes.
type BranchNodeCompact struct {
	StateMask uint16
	TreeMask  uint16
	HashMask  uint16
	Hashes    []common.Hash
	RootHash  *common.Hash
}

func (b *BranchNodeCompact) EncodeCompact(buf *bytes.Buffer) int {
	if popcount16(b.HashMask) != len(b.Hashes) {
		panic("BranchNodeCompact: hash_mask popcount != len(hashes)")
	}
	if b.TreeMask&^b.StateMask != 0 || b.HashMask&^b.StateMask != 0 {
		panic("BranchNodeCompact: tree_mask/hash_mask must be subset of state_mask")
	}

	written := 0
	written += writeBEU16(buf, b.StateMask)
	written += writeBEU16(buf, b.TreeMask)
	written += writeBEU16(buf, b.HashMask)
	// root_hash comes BEFORE child hashes (matches Rust to_compact, line 65-68).
	if b.RootHash != nil {
		written += copy(bufWrite(buf, 32), b.RootHash[:])
	}
	for _, h := range b.Hashes {
		written += copy(bufWrite(buf, 32), h[:])
	}
	return written
}

func (b *BranchNodeCompact) DecodeCompact(data []byte, totalLen int) int {
	if totalLen < 6 {
		panic("BranchNodeCompact: totalLen < 6 (masks truncated)")
	}
	cursor := 0

	b.StateMask = readBEU16(data[cursor:])
	cursor += 2
	b.TreeMask = readBEU16(data[cursor:])
	cursor += 2
	b.HashMask = readBEU16(data[cursor:])
	cursor += 2

	// Determine how many 32-byte hash slots remain.
	remaining := totalLen - cursor
	if remaining%32 != 0 {
		panic("BranchNodeCompact: non-multiple-of-32 hash bytes")
	}
	numHashes := remaining / 32
	count := popcount16(b.HashMask)

	// If numHashes == count+1 the first slot is the root_hash.
	b.RootHash = nil
	if numHashes == count+1 {
		var h common.Hash
		copy(h[:], data[cursor:cursor+32])
		b.RootHash = &h
		cursor += 32
		numHashes--
	}
	if numHashes != count {
		panic("BranchNodeCompact: hash count mismatch")
	}

	b.Hashes = make([]common.Hash, count)
	for i := 0; i < count; i++ {
		copy(b.Hashes[i][:], data[cursor:cursor+32])
		cursor += 32
	}

	if cursor != totalLen {
		panic("BranchNodeCompact: cursor != totalLen")
	}
	return cursor
}

func popcount16(v uint16) int {
	v = v - ((v >> 1) & 0x5555)
	v = (v & 0x3333) + ((v >> 2) & 0x3333)
	v = (v + (v >> 4)) & 0x0f0f
	return int((v * 0x0101) >> 8)
}

func writeBEU16(buf *bytes.Buffer, v uint16) int {
	buf.WriteByte(byte(v >> 8))
	buf.WriteByte(byte(v))
	return 2
}

func readBEU16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

// StorageTrieEntry is the DupSort value of StoragesTrie. The 33-byte SubKey
// is also the MDBX DupSort sub-key prefix; reth re-encodes it inside the
// value for self-description.
//
// Wire:
//
//	33 bytes SubKey (PackedStoredNibblesSubKey) || BranchNodeCompact bytes
//
// SubKey layout matches PackedStoredNibblesSubKey::to_compact (reth-trie-common
// nibbles.rs): packed[32] (2 nibbles per byte, zero-padded right) || length[1].
// The length byte is at the END (byte[32]), not the beginning.
// Cross-validated against Rust canonical hex via TestGoldenStorageTrieEntry
// in golden_test.go.
type StorageTrieEntry struct {
	SubKey StoredNibblesSubKey
	Node   BranchNodeCompact
}

func (e *StorageTrieEntry) EncodeCompact(buf *bytes.Buffer) int {
	// SubKey wire: packed[32] || length[1] (length byte at end, matching
	// PackedStoredNibblesSubKey::to_compact in reth-trie-common nibbles.rs).
	written := 0
	written += copy(bufWrite(buf, 32), e.SubKey.Packed[:])
	written += copy(bufWrite(buf, 1), []byte{e.SubKey.Length})
	written += e.Node.EncodeCompact(buf)
	return written
}

func (e *StorageTrieEntry) DecodeCompact(data []byte, totalLen int) int {
	if totalLen < 33 {
		panic("StorageTrieEntry: totalLen < 33 (SubKey truncated)")
	}
	if len(data) < totalLen {
		panic("StorageTrieEntry: buffer shorter than totalLen")
	}
	// SubKey wire: packed[32] || length[1]
	copy(e.SubKey.Packed[:], data[0:32])
	e.SubKey.Length = data[32]
	nodeLen := totalLen - 33
	consumed := e.Node.DecodeCompact(data[33:], nodeLen)
	if consumed != nodeLen {
		panic("StorageTrieEntry: inner node consumed != totalLen-33")
	}
	return totalLen
}
