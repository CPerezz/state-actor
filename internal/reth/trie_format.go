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
