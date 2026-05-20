package reth

import "bytes"

// PruneSegment matches reth's PruneSegment enum at
// crates/prune/types/src/segment.rs. Variant indexes are load-bearing — they
// are encoded directly as the MDBX key in the PruneCheckpoints table.
const (
	PruneSegmentSenderRecovery    uint8 = 0
	PruneSegmentTransactionLookup uint8 = 1
	PruneSegmentReceipts          uint8 = 2
	PruneSegmentContractLogs      uint8 = 3
	PruneSegmentAccountHistory    uint8 = 4
	PruneSegmentStorageHistory    uint8 = 5
	// Variants 6/7/8 are deprecated (Headers/Transactions/MerkleChangeSets).
	PruneSegmentBodies uint8 = 9
)

// EncodePruneSegmentKey returns the MDBX key for the PruneCheckpoints table.
// Reth's PruneSegment derives Compact as an enum: 1-byte flag struct holding
// the variant index, no payload. For unit variants this is the entire encoding.
func EncodePruneSegmentKey(segment uint8) []byte {
	return []byte{segment}
}

// PruneModeKind matches reth's PruneMode enum discriminant.
const (
	PruneModeFull     uint8 = 0
	PruneModeDistance uint8 = 1
	PruneModeBefore   uint8 = 2
)

// PruneMode is a Go-side mirror of reth's PruneMode enum at
// crates/prune/types/src/mode.rs.
type PruneMode struct {
	Kind  uint8  // PruneModeFull/Distance/Before
	Value uint64 // unused for Full
}

// EncodeCompact appends the Compact-encoded PruneMode bytes to buf.
//
// Wire format (per reth-codecs 0.3.1's enum derive):
//   - 1-byte flag struct: variant index (B8)
//   - payload (only for Distance/Before): u64.to_compact = stripped-BE bytes
//
// For Full: just the 1-byte flags = [0x00].
// For Distance(N) / Before(N): [variant_byte, stripped_be(N)...]
func (m *PruneMode) EncodeCompact(buf *bytes.Buffer) int {
	var payload bytes.Buffer
	var payloadLen int
	switch m.Kind {
	case PruneModeFull:
		// no payload
	case PruneModeDistance, PruneModeBefore:
		payloadLen = encodeU64Compact(&payload, m.Value)
	}

	// 1-byte flag struct (variant index).
	buf.WriteByte(m.Kind)
	if payloadLen > 0 {
		buf.Write(payload.Bytes())
	}
	return 1 + payloadLen
}

// PruneCheckpoint is a Go-side mirror of reth's PruneCheckpoint at
// crates/prune/types/src/checkpoint.rs:
//
//	struct PruneCheckpoint {
//	    block_number: Option<BlockNumber>,  // BlockNumber = u64
//	    tx_number:    Option<TxNumber>,     // TxNumber    = u64
//	    prune_mode:   PruneMode,
//	}
//
// Wire format (per reth-codecs 0.3.1 + derive 0.3.1):
//  1. 1-byte struct flag header (LSB-first):
//     bit 0: block_number is_some (1) / is_none (0)
//     bit 1: tx_number    is_some (1) / is_none (0)
//     bits 2-7: padding (unused)
//  2. If block_number is Some: varuint(N) || stripped_be(value)
//     where N = stripped_be_byte_count(value). For Some(0): varuint(0)=0x00, 0 value bytes.
//  3. If tx_number is Some: varuint(N) || stripped_be(value)
//  4. prune_mode: 1-byte variant || optional u64 payload (see PruneMode.EncodeCompact)
//
// PruneMode is NOT a flag-type for the parent struct (is_flag_type returns
// false for "PruneMode"), so the parent doesn't reserve a length field for it.
// Reth's reader consumes prune_mode from "rest of buffer" at decode time.
type PruneCheckpoint struct {
	BlockNumber *uint64 // nil = None
	TxNumber    *uint64 // nil = None
	PruneMode   PruneMode
}

// EncodeCompact appends the Compact-encoded PruneCheckpoint bytes to buf.
// Returns the total number of bytes written.
func (c *PruneCheckpoint) EncodeCompact(buf *bytes.Buffer) int {
	// 1) Encode Option<u64> fields into a temporary buffer.
	// Per reth-codecs Option<T> impl: Some => varuint(inner_compact_len) || inner.to_compact bytes; None => nothing.
	var optBuf bytes.Buffer
	var blockFlag uint8
	if c.BlockNumber != nil {
		var inner bytes.Buffer
		innerLen := encodeU64Compact(&inner, *c.BlockNumber)
		encodeVarUint(&optBuf, uint64(innerLen))
		optBuf.Write(inner.Bytes())
		blockFlag = 1
	}
	var txFlag uint8
	if c.TxNumber != nil {
		var inner bytes.Buffer
		innerLen := encodeU64Compact(&inner, *c.TxNumber)
		encodeVarUint(&optBuf, uint64(innerLen))
		optBuf.Write(inner.Bytes())
		txFlag = 1
	}

	// 2) Encode prune_mode after the Option fields (in struct declaration order).
	var pmBuf bytes.Buffer
	c.PruneMode.EncodeCompact(&pmBuf)

	// 3) Assemble the 1-byte struct flag header (LSB-first bit order).
	flagByte := blockFlag | (txFlag << 1)

	// 4) Emit: flag header || option fields || prune_mode.
	written := 0
	buf.WriteByte(flagByte)
	written++
	written += copy(bufWrite(buf, optBuf.Len()), optBuf.Bytes())
	written += copy(bufWrite(buf, pmBuf.Len()), pmBuf.Bytes())
	return written
}

// U64Ptr returns a pointer to the given uint64 (helper for constructing
// PruneCheckpoint values).
func U64Ptr(v uint64) *uint64 { return &v }
