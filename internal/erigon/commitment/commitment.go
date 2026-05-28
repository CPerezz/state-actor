//go:build cgo_erigon_commitment

package commitment

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	gethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	erigoncommon "github.com/erigontech/erigon/common"
	erigonkv "github.com/erigontech/erigon/db/kv"
	erigoncommitment "github.com/erigontech/erigon/execution/commitment"

	"github.com/nerolation/state-actor/internal/streamsort"
)

// erigonHash converts a geth-style 32-byte hash to Erigon's equivalent
// type. Both are `[32]byte`; this is a free byte-by-byte copy.
func erigonHash(h gethcommon.Hash) erigoncommon.Hash {
	var out erigoncommon.Hash
	copy(out[:], h[:])
	return out
}

// Account is the state-actor-facing input shape for one alloc entry.
// Used by EncodeAccountUpdate to produce the bytes the orchestrator
// writes into the commitmentInputStore during the streaming autofill
// loop. NOT consumed directly by ComputeGenesisRoot anymore —
// ComputeGenesisRoot reads encoded Update bytes from the
// commitmentInputStore via streamsort.Get.
type Account struct {
	Address gethcommon.Address
	Nonce   uint64
	Balance *uint256.Int
	Code    []byte
	Storage map[gethcommon.Hash]gethcommon.Hash
}

// Result carries the outputs of a successful commitment computation.
type Result struct {
	Root        gethcommon.Hash
	BranchNodes map[string][]byte
	HPHState    []byte
}

// EncodeAccountUpdate returns the Update.Encode bytes for an account
// (nonce + balance + codeHash). Callers Put this into the
// commitmentInputStore keyed by plain 20-byte address. ctx.Account
// later Decodes it back into an erigoncommitment.Update.
//
// Splits cleanly from the snapshot SerialiseV3 encoding (Update.Encode
// is HPH's internal wire format; SerialiseV3 is Erigon's state-domain
// .kv value format — different shapes).
func EncodeAccountUpdate(nonce uint64, balance *uint256.Int, code []byte) []byte {
	upd := erigoncommitment.Update{
		Flags: erigoncommitment.NonceUpdate | erigoncommitment.BalanceUpdate,
		Nonce: nonce,
	}
	if balance != nil {
		upd.Balance = *balance
	}
	if len(code) > 0 {
		h := crypto.Keccak256Hash(code)
		upd.CodeHash = erigonHash(h)
		upd.Flags |= erigoncommitment.CodeUpdate
	} else {
		upd.CodeHash = erigonHash(emptyCodeHash)
	}
	var numBuf [binary.MaxVarintLen64]byte
	return upd.Encode(nil, numBuf[:])
}

// EncodeStorageUpdate returns the Update.Encode bytes for one storage
// slot value. The value is LEFT-aligned into Update.Storage[0:len]
// (matching Erigon's TouchStorage invariant at commitment.go:1746-1753).
//
// Callers Put this into commitmentInputStore keyed by addr(20)||slot(32).
// An all-zero value should NOT be encoded — caller filters out.
func EncodeStorageUpdate(value []byte) []byte {
	trimmed := trimLeadingZeros(value)
	upd := erigoncommitment.Update{
		Flags:      erigoncommitment.StorageUpdate,
		StorageLen: int8(len(trimmed)),
	}
	copy(upd.Storage[:], trimmed)
	var numBuf [binary.MaxVarintLen64]byte
	return upd.Encode(nil, numBuf[:])
}

// ComputeGenesisRoot runs Erigon's HexPatriciaHashed against the
// commitmentInputStore's encoded Update payloads.
//
// The caller is responsible for having populated commitmentInputStore
// during the buildAllocMap/writeSnapshots streaming loop: every alloc
// account writes one entry keyed by 20-byte addr; every non-zero
// storage slot writes one entry keyed by addr||slot. Encoding is done
// via EncodeAccountUpdate / EncodeStorageUpdate above.
//
// Memory profile: the in-memory `state map` of the prior implementation
// is GONE — Account/Storage lookups during the HPH walk hit Pebble via
// streamsort.Get. At 25 GB bench scale that's ~344 M Get calls; each
// is ~10 µs in the warm-cache case, ~100 µs cold — order of an hour
// of CPU time. The trade-off pays back the ~50 GB heap the old in-memory
// map would have needed at full bench scale.
//
// `ctx.branches` stays in memory: bounded by trie depth × entry count
// (~few hundred MB max even at 25 GB scale, since branches are O(N) not
// O(StorageSlots)).
func ComputeGenesisRoot(commitmentInputStore *streamsort.Store) (Result, error) {
	ctx := &genesisCtx{
		commitmentInputStore: commitmentInputStore,
		branches:             make(map[string][]byte),
	}

	upds := erigoncommitment.NewUpdates(
		erigoncommitment.ModeDirect,
		ctx.tmpDir,
		erigoncommitment.KeyToHexNibbleHash,
	)

	// Walker: iterate every entry in commitmentInputStore (which holds
	// addresses + addr||slot composite keys for the full alloc). Per
	// upstream commitment.go:1666-1681, ModeDirect's TouchPlainKeyDirect
	// discards the *Update arg — only the (hashedKey, plainKey) pair is
	// recorded in the etl.Collector. So we pass a placeholder; HPH will
	// re-fetch via ctx.Account/Storage during Process.
	var placeholder erigoncommitment.Update
	if err := commitmentInputStore.Iterate(func(plainKey, _ []byte) error {
		upds.TouchPlainKeyDirect(string(plainKey), &placeholder)
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: iterate commitmentInputStore: %w", err)
	}

	hph := erigoncommitment.NewHexPatriciaHashed(20 /* accountKeyLen */, ctx)
	rootBytes, err := hph.Process(context.Background(), upds, "state-actor-genesis", nil, erigoncommitment.WarmupConfig{})
	if err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: Process: %w", err)
	}
	if len(rootBytes) != 32 {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: unexpected root hash length %d", len(rootBytes))
	}
	var root gethcommon.Hash
	copy(root[:], rootBytes)

	hphState, err := hph.EncodeCurrentState(nil)
	if err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: EncodeCurrentState: %w", err)
	}

	return Result{Root: root, BranchNodes: ctx.branches, HPHState: hphState}, nil
}

// ComputeGenesisRootFromAccounts is a backward-compat wrapper for
// small in-memory inputs (tests + the H4 invariance proof). Materialises
// the slice into a temp streamsort + calls the streaming
// ComputeGenesisRoot. Not for production at bench scale.
func ComputeGenesisRootFromAccounts(accounts []Account) (Result, error) {
	store, err := streamsort.New("")
	if err != nil {
		return Result{}, fmt.Errorf("ComputeGenesisRootFromAccounts: streamsort.New: %w", err)
	}
	defer store.Close()

	for _, a := range accounts {
		// Account entry keyed by 20-byte address.
		var balance *uint256.Int
		if a.Balance != nil {
			balance = a.Balance
		}
		acctBytes := EncodeAccountUpdate(a.Nonce, balance, a.Code)
		if err := store.Put(a.Address[:], acctBytes); err != nil {
			return Result{}, fmt.Errorf("ComputeGenesisRootFromAccounts: put account %s: %w", a.Address.Hex(), err)
		}
		// Storage entries keyed by addr||slot. Skip all-zero values.
		for slot, val := range a.Storage {
			trimmed := trimLeadingZeros(val[:])
			if len(trimmed) == 0 {
				continue
			}
			composite := make([]byte, 0, 52)
			composite = append(composite, a.Address[:]...)
			composite = append(composite, slot[:]...)
			storBytes := EncodeStorageUpdate(val[:])
			if err := store.Put(composite, storBytes); err != nil {
				return Result{}, fmt.Errorf("ComputeGenesisRootFromAccounts: put storage %s/%s: %w", a.Address.Hex(), slot.Hex(), err)
			}
		}
	}
	return ComputeGenesisRoot(store)
}

// genesisCtx implements erigoncommitment.PatriciaContext over a
// streamsort-backed commitmentInputStore (random-access via Pebble).
// `branches` stays in memory.
type genesisCtx struct {
	commitmentInputStore *streamsort.Store
	branches             map[string][]byte
	tmpDir               string
}

func (c *genesisCtx) Branch(prefix []byte) ([]byte, erigonkv.Step, error) {
	if data, ok := c.branches[string(prefix)]; ok {
		return data, 0, nil
	}
	return nil, 0, nil
}

func (c *genesisCtx) PutBranch(prefix []byte, data []byte, prevData []byte) error {
	c.branches[string(prefix)] = append([]byte(nil), data...)
	return nil
}

func (c *genesisCtx) Account(plainKey []byte) (*erigoncommitment.Update, error) {
	enc, err := c.commitmentInputStore.Get(plainKey)
	if err != nil {
		return nil, fmt.Errorf("commitment.genesisCtx.Account: Get(%x): %w", plainKey, err)
	}
	if enc == nil {
		u := new(erigoncommitment.Update)
		u.Flags = erigoncommitment.DeleteUpdate
		return u, nil
	}
	var u erigoncommitment.Update
	pos, err := u.Decode(enc, 0)
	if err != nil {
		return nil, fmt.Errorf("commitment.genesisCtx.Account: decode plainKey=%x: %w", plainKey, err)
	}
	if pos != len(enc) {
		return nil, fmt.Errorf("commitment.genesisCtx.Account: trailing bytes after decode")
	}
	if u.Flags&erigoncommitment.StorageUpdate != 0 {
		return nil, errors.New("commitment.genesisCtx.Account: read storage entry via Account()")
	}
	return &u, nil
}

func (c *genesisCtx) Storage(plainKey []byte) (*erigoncommitment.Update, error) {
	enc, err := c.commitmentInputStore.Get(plainKey)
	if err != nil {
		return nil, fmt.Errorf("commitment.genesisCtx.Storage: Get(%x): %w", plainKey, err)
	}
	if enc == nil {
		u := new(erigoncommitment.Update)
		u.Flags = erigoncommitment.DeleteUpdate
		return u, nil
	}
	var u erigoncommitment.Update
	pos, err := u.Decode(enc, 0)
	if err != nil {
		return nil, fmt.Errorf("commitment.genesisCtx.Storage: decode plainKey=%x: %w", plainKey, err)
	}
	if pos != len(enc) {
		return nil, fmt.Errorf("commitment.genesisCtx.Storage: trailing bytes after decode")
	}
	return &u, nil
}

var emptyCodeHash = gethcommon.Hash{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

func trimLeadingZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	return b[i:]
}
