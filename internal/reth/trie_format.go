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
//	length (1 byte, 0..=64) || packed (32 bytes; 2 nibbles per byte, high then low)
//
// When Length is odd, the final low nibble is zero-padded.
type StoredNibbles struct {
	Length byte
	Packed common.Hash
}

func (s *StoredNibbles) EncodeKey(buf *bytes.Buffer) {
	buf.WriteByte(s.Length)
	buf.Write(s.Packed[:])
}

func (s *StoredNibbles) DecodeKey(b []byte) {
	if len(b) < 33 {
		panic("StoredNibbles: truncated key")
	}
	s.Length = b[0]
	copy(s.Packed[:], b[1:33])
}

// StoredNibblesSubKey is the StoragesTrie sub-key (DupSort sub-key after the
// 32-byte hashed-address main key). Same 33-byte layout as StoredNibbles.
type StoredNibblesSubKey = StoredNibbles

// BranchNodeCompact mirrors alloy_trie::BranchNodeCompact (alloy-trie 0.9.5,
// nodes/branch.rs:262-285). Used as the value of AccountsTrie / StoragesTrie.
//
// Wire format (best-effort hand-derived; Task 16 cross-validates against Rust):
//
//  1. 1-byte bitflag: root_hash_present(1) | padding(7)
//  2. state_mask: BE u16
//  3. tree_mask: BE u16
//  4. hash_mask: BE u16
//  5. hashes: popcount(hash_mask) × 32 bytes
//  6. root_hash: 32 bytes if present
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

	var bb bitflagBuilder
	bb.PutBool(b.RootHash != nil)
	header := bb.Finalize(1)

	written := 0
	written += copy(bufWrite(buf, len(header)), header)
	written += writeBEU16(buf, b.StateMask)
	written += writeBEU16(buf, b.TreeMask)
	written += writeBEU16(buf, b.HashMask)
	for _, h := range b.Hashes {
		written += copy(bufWrite(buf, 32), h[:])
	}
	if b.RootHash != nil {
		written += copy(bufWrite(buf, 32), b.RootHash[:])
	}
	return written
}

func (b *BranchNodeCompact) DecodeCompact(data []byte, totalLen int) int {
	cursor := 0
	if len(data) < 1 {
		panic("BranchNodeCompact: header truncated")
	}
	header := data[:1]
	cursor++

	var br bitflagReader
	br.Init(header, 1)
	hasRoot := br.GetBool()

	b.StateMask = readBEU16(data[cursor:])
	cursor += 2
	b.TreeMask = readBEU16(data[cursor:])
	cursor += 2
	b.HashMask = readBEU16(data[cursor:])
	cursor += 2

	count := popcount16(b.HashMask)
	b.Hashes = make([]common.Hash, count)
	for i := 0; i < count; i++ {
		copy(b.Hashes[i][:], data[cursor:cursor+32])
		cursor += 32
	}

	if hasRoot {
		var h common.Hash
		copy(h[:], data[cursor:cursor+32])
		b.RootHash = &h
		cursor += 32
	} else {
		b.RootHash = nil
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
