//go:build cgo_erigon

package mdbx

import (
	"encoding/binary"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
)

// GenesisTxNum is the txNum Erigon assigns to genesis-state writes.
// Per genesis_write.go:397: "txNum := uint64(1) — 2 system txs in
// begin/end of block. Attribute state-writes to first, consensus
// state-changes to second." For block 0 (zero user txs), MaxTxNum[0]
// = 0 + 1 = 1 (Verifier A's Correction 7, confirmed by the schema
// investigation).
const GenesisTxNum uint64 = 1

// GenesisStep is txNum / DefaultStepSize. For txNum=1 and
// stepSize=390_625, step=0.
const GenesisStep uint64 = 0

// stepInvertedBE returns the big-endian 8-byte encoding of `^uint64(step)`.
// Erigon's Vals tables prefix every value with this stepInverted so that
// MDBX's cursor-Last returns the LATEST step first.
func stepInvertedBE(step uint64) [8]byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], ^step)
	return out
}

// txNumBE returns the big-endian 8-byte encoding of txNum (no inversion).
func txNumBE(txNum uint64) [8]byte {
	var out [8]byte
	binary.BigEndian.PutUint64(out[:], txNum)
	return out
}

// WriteStorageSlot writes one storage slot to the four tables Erigon's
// reader consults: TblStorageVals + TblStorageIdx + TblStorageHistoryKeys
// + TblStorageHistoryVals. Uses genesis-step semantics (txNum=1, step=0).
//
// Key layout (per the schema investigation):
//   - TblStorageVals (DupSort): key=address[20]||slot[32], value=stepInverted[8]||trimmedValue
//   - TblStorageIdx (DupSort):  key=address[20]||slot[32], value=txNum[8]
//   - TblStorageHistoryKeys:    key=txNum[8],              value=address[20]||slot[32]
//   - TblStorageHistoryVals:    key=address[20]||slot[32], value=txNum[8] (prevValue empty at genesis)
//
// `value` is the raw 32-byte slot value; leading zeros are trimmed
// before write. All-zero slots are silently skipped (Erigon's storage
// domain has no entry for "never set" slots — matches geth's MPT).
func WriteStorageSlot(txn *mdbx.Txn, env *Env, address [20]byte, slot [32]byte, value [32]byte) error {
	// Trim leading zeros — matches Erigon's uint256.Int.Bytes().
	trimmedStart := 0
	for trimmedStart < 32 && value[trimmedStart] == 0 {
		trimmedStart++
	}
	if trimmedStart == 32 {
		return nil // zero-value slots are not entries
	}
	trimmed := value[trimmedStart:]

	// Composite key: address || slot (52 bytes).
	composite := make([]byte, 52)
	copy(composite[:20], address[:])
	copy(composite[20:], slot[:])

	// TblStorageVals value: stepInverted[8] || trimmedValue.
	stepInv := stepInvertedBE(GenesisStep)
	val := make([]byte, 0, 8+len(trimmed))
	val = append(val, stepInv[:]...)
	val = append(val, trimmed...)
	if err := txn.Put(env.DBIs[TblStorageVals], composite, val, 0); err != nil {
		return fmt.Errorf("Put(TblStorageVals): %w", err)
	}

	// History layer — see schema investigation for the 3-table layout.
	// Idx: composite → txNum
	tx := txNumBE(GenesisTxNum)
	if err := txn.Put(env.DBIs[TblStorageIdx], composite, tx[:], 0); err != nil {
		return fmt.Errorf("Put(TblStorageIdx): %w", err)
	}
	// HistoryKeys: txNum → composite
	if err := txn.Put(env.DBIs[TblStorageHistoryKeys], tx[:], composite, 0); err != nil {
		return fmt.Errorf("Put(TblStorageHistoryKeys): %w", err)
	}
	// HistoryVals: composite → txNum (prevValue is empty at genesis)
	if err := txn.Put(env.DBIs[TblStorageHistoryVals], composite, tx[:], 0); err != nil {
		return fmt.Errorf("Put(TblStorageHistoryVals): %w", err)
	}
	return nil
}

// chunkSize is the number of storage slots we commit per MDBX
// transaction. A single transaction over millions of slots blows
// MDBX's dirty-page budget and triggers spill_slowpath (mdbx writes
// dirty pages to disk mid-transaction, ~50x slower than batched).
// 10_000 is small enough to stay under the default 64K dirty-page
// limit (assuming ~3 inserts per slot × 8-byte key + small value =
// well under 4 KiB per dirty page).
const chunkSize = 10_000

// WriteAlloc walks an alloc map and stream-writes every storage slot
// to MDBX via WriteStorageSlot. Chunks writes into transactions of
// `chunkSize` slots — single-transaction writes over millions of
// slots hit MDBX's spill_slowpath and become orders of magnitude
// slower than chunked.
//
// Returns the count of slots written (post-trim, post-zero-skip).
//
// `alloc` is keyed by address, mapping to a per-address storage map.
// Callers are expected to pre-build this from `cfg.PreAlloc[i].Storage`
// (via the streaming iter drain in the orchestrator).
func WriteAlloc(env *Env, alloc map[[20]byte]map[[32]byte][32]byte) (uint64, error) {
	// Flatten into a slice so we can chunk by count rather than per-
	// address (entities have wildly different storage sizes).
	type entry struct {
		addr  [20]byte
		slot  [32]byte
		value [32]byte
	}
	flat := make([]entry, 0, 1024)
	for addr, slots := range alloc {
		for slot, value := range slots {
			flat = append(flat, entry{addr, slot, value})
		}
	}

	var written uint64
	for start := 0; start < len(flat); start += chunkSize {
		end := start + chunkSize
		if end > len(flat) {
			end = len(flat)
		}
		batch := flat[start:end]
		if err := env.Env.Update(func(txn *mdbx.Txn) error {
			for _, e := range batch {
				if err := WriteStorageSlot(txn, env, e.addr, e.slot, e.value); err != nil {
					return fmt.Errorf("address=%x slot=%x: %w", e.addr[:], e.slot[:], err)
				}
				written++
			}
			return nil
		}); err != nil {
			return 0, err
		}
	}
	return written, nil
}
