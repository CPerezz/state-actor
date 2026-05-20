package reth

import (
	"bytes"
	"testing"
)

// TestPruneCheckpointEncodingGroundTruth pins the byte-for-byte Compact
// encoding of PruneCheckpoint{Some(0), None, Before(1)} that reth's pruner
// would produce. The expected bytes are derived by hand-tracing reth-codecs
// 0.3.1's derive macro through reth-prune-types' PruneCheckpoint struct;
// the test catches future format drift in either crate.
//
// Wire breakdown (4 bytes):
//
//	0x01 — PruneCheckpoint flag byte: bit 0 set (block_number is Some), bit 1
//	       clear (tx_number is None), bits 2-7 padding.
//	0x00 — varuint(0): block_number's inner u64.to_compact() returns 0 bytes
//	       for value 0 (all leading zeros stripped), so the varuint-prefixed
//	       inner length is 0.
//	0x02 — PruneMode flag byte: variant index 2 = Before.
//	0x01 — inner u64.to_compact() for value 1 = stripped-BE [0x01].
func TestPruneCheckpointEncodingGroundTruth(t *testing.T) {
	ckpt := PruneCheckpoint{
		BlockNumber: U64Ptr(0),
		TxNumber:    nil,
		PruneMode:   PruneMode{Kind: PruneModeBefore, Value: 1},
	}
	var buf bytes.Buffer
	n := ckpt.EncodeCompact(&buf)

	want := []byte{0x01, 0x00, 0x02, 0x01}
	got := buf.Bytes()
	if !bytes.Equal(got, want) {
		t.Errorf("PruneCheckpoint(Some(0), None, Before(1)) encode = %x, want %x", got, want)
	}
	if n != len(want) {
		t.Errorf("written count = %d, want %d", n, len(want))
	}
}

// TestPruneCheckpointEncodingNoneNone covers the all-None variant to confirm
// the flag byte cleanly distinguishes "field absent" from "field=0".
func TestPruneCheckpointEncodingNoneNone(t *testing.T) {
	ckpt := PruneCheckpoint{
		BlockNumber: nil,
		TxNumber:    nil,
		PruneMode:   PruneMode{Kind: PruneModeFull},
	}
	var buf bytes.Buffer
	ckpt.EncodeCompact(&buf)

	// 0x00 flag byte (both Options None) + 0x00 prune_mode (variant=Full, no payload).
	want := []byte{0x00, 0x00}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("PruneCheckpoint(None, None, Full) encode = %x, want %x", buf.Bytes(), want)
	}
}

// TestPruneCheckpointEncodingDistance covers Distance variant + a Some(N)
// where N requires multi-byte stripping.
func TestPruneCheckpointEncodingDistance(t *testing.T) {
	ckpt := PruneCheckpoint{
		BlockNumber: U64Ptr(0x1234), // 2 stripped-BE bytes
		TxNumber:    U64Ptr(0xFF),   // 1 stripped-BE byte
		PruneMode:   PruneMode{Kind: PruneModeDistance, Value: 100},
	}
	var buf bytes.Buffer
	ckpt.EncodeCompact(&buf)

	// Flag byte: bit 0 + bit 1 set = 0x03.
	// block_number: varuint(2) || stripped_be(0x1234) = 0x02 0x12 0x34.
	// tx_number:    varuint(1) || stripped_be(0xFF)   = 0x01 0xFF.
	// prune_mode:   0x01 (Distance variant) || stripped_be(100) = 0x01 0x64.
	want := []byte{0x03, 0x02, 0x12, 0x34, 0x01, 0xFF, 0x01, 0x64}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("PruneCheckpoint(Some(0x1234), Some(0xFF), Distance(100)) encode = %x, want %x",
			buf.Bytes(), want)
	}
}

// TestEncodePruneSegmentKey pins the 1-byte enum-discriminant encoding
// reth uses as the PruneCheckpoints table key.
func TestEncodePruneSegmentKey(t *testing.T) {
	cases := []struct {
		segment uint8
		want    []byte
	}{
		{PruneSegmentSenderRecovery, []byte{0x00}},
		{PruneSegmentTransactionLookup, []byte{0x01}},
		{PruneSegmentReceipts, []byte{0x02}},
		{PruneSegmentContractLogs, []byte{0x03}},
		{PruneSegmentAccountHistory, []byte{0x04}},
		{PruneSegmentStorageHistory, []byte{0x05}},
		{PruneSegmentBodies, []byte{0x09}},
	}
	for _, c := range cases {
		got := EncodePruneSegmentKey(c.segment)
		if !bytes.Equal(got, c.want) {
			t.Errorf("EncodePruneSegmentKey(%d) = %x, want %x", c.segment, got, c.want)
		}
	}
}
