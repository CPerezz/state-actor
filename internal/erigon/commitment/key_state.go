//go:build cgo_erigon_commitment

package commitment

import (
	"encoding/binary"
	"fmt"
)

// EncodeKeyCommitmentStateValue produces the value bytes for the
// KeyCommitmentState record that lives in Erigon's commitment.0-N.kv
// snapshot file. Mirrors upstream
// `execution/commitment/commitmentdb/commitment_context.go::(*commitmentState).Encode`
// (line 930) on the bal-devnet-7 branch (= equivalent to v3.4.2's pin
// commit 7827ec7…; byte-identical encoder).
//
// On-disk layout, all big-endian:
//
//	[0:8]   txNum
//	[8:16]  blockNum
//	[16:18] uint16(len(trieState))
//	[18:]   trieState bytes (raw output of HexPatriciaHashed.EncodeCurrentState)
//
// Total length = 18 + len(trieState). trieState length is bounded by
// the HPH structure (typically 665-815 bytes); the u16 cap of 65535
// is therefore never approached in practice. Returns an error only if
// len(trieState) > math.MaxUint16, which would indicate upstream
// breakage rather than caller error.
//
// The daemon's first FCU reads this record via DecodeTxBlockNums (also
// in commitment_context.go, line 593) to extract (txNum, blockNum) and
// anchor commitment continuation; the full record is consumed when the
// HPH re-loads its trie state.
func EncodeKeyCommitmentStateValue(txNum, blockNum uint64, trieState []byte) ([]byte, error) {
	if len(trieState) > 0xFFFF {
		return nil, fmt.Errorf("commitment.EncodeKeyCommitmentStateValue: trieState too large (%d > %d)",
			len(trieState), 0xFFFF)
	}
	out := make([]byte, 18+len(trieState))
	binary.BigEndian.PutUint64(out[0:8], txNum)
	binary.BigEndian.PutUint64(out[8:16], blockNum)
	binary.BigEndian.PutUint16(out[16:18], uint16(len(trieState)))
	copy(out[18:], trieState)
	return out, nil
}
