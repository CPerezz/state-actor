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
)

// erigonHash converts a geth-style 32-byte hash to Erigon's equivalent
// type. Both are `[32]byte`; this is a free byte-by-byte copy.
func erigonHash(h gethcommon.Hash) erigoncommon.Hash {
	var out erigoncommon.Hash
	copy(out[:], h[:])
	return out
}

// Account is the state-actor-facing input for one alloc entry. The
// commitment writer hashes (Nonce, Balance, StorageRoot, CodeHash) per
// the standard Ethereum account encoding; CodeHash is keccak256(Code)
// (or EmptyCodeHash if Code is empty), and storage is computed
// per-account via a sub-trie over (slot, value) pairs.
type Account struct {
	Address gethcommon.Address
	Nonce   uint64
	Balance *uint256.Int // may be nil → 0
	Code    []byte       // may be empty
	Storage map[gethcommon.Hash]gethcommon.Hash
}

// Result carries the outputs of a successful commitment computation.
type Result struct {
	// Root is the commitment trie's root hash — same value Erigon's
	// `erigon init` computes via `ComputeGenesisCommitment`.
	Root gethcommon.Hash

	// BranchNodes maps trie-prefix → branch-data bytes. Suitable for
	// emitting to a snap.Writer commitment-domain `.kv` file.
	BranchNodes map[string][]byte

	// HPHState is the raw output of HexPatriciaHashed.EncodeCurrentState
	// captured after Process(). Caller passes it to
	// EncodeKeyCommitmentStateValue (along with the txNum/blockNum it
	// wants pinned in the record header) to produce the value bytes for
	// the KeyCommitmentState record in commitment.0-N.kv.
	//
	// Typical length: ~683-815 bytes for a populated post-Process HPH.
	HPHState []byte
}

// ComputeGenesisRoot runs Erigon's HexPatriciaHashed against the
// supplied alloc and returns (root hash, branch nodes).
//
// The alloc is treated as a cold-start commitment: no prior branches,
// no prior history. Every account in `accounts` is touched once, with
// per-account storage slots touched as `(addr[20] || slot[32])`
// composite keys per Erigon's E3 layout
// (`execution/state/rw_v3.go:965`).
//
// Empty input returns the canonical empty-trie root.
func ComputeGenesisRoot(accounts []Account) (Result, error) {
	ctx := newGenesisCtx()
	plainKeys := make([][]byte, 0, len(accounts))
	updates := make([]erigoncommitment.Update, 0, len(accounts))

	for _, a := range accounts {
		// 1. Build the account-level Update.
		acctUpd := erigoncommitment.Update{
			Flags: erigoncommitment.NonceUpdate | erigoncommitment.BalanceUpdate,
			Nonce: a.Nonce,
		}
		if a.Balance != nil {
			acctUpd.Balance = *a.Balance
		}
		if len(a.Code) > 0 {
			h := crypto.Keccak256Hash(a.Code)
			acctUpd.CodeHash = erigonHash(h)
			acctUpd.Flags |= erigoncommitment.CodeUpdate
		} else {
			acctUpd.CodeHash = erigonHash(emptyCodeHash)
		}
		addrKey := append([]byte(nil), a.Address[:]...)
		plainKeys = append(plainKeys, addrKey)
		updates = append(updates, acctUpd)
		ctx.state[string(addrKey)] = acctUpd.Encode(nil, ctx.numBuf[:])

		// 2. Storage slots (one Touch per slot). Storage bytes must be
		// LEFT-aligned at Storage[0:StorageLen] — matching Erigon's
		// TouchStorage invariant at commitment.go:1746-1753 (the trie
		// reader at hex_patricia_hashed.go:946,1019,1033 unpacks the
		// value as Storage[0:StorageLen]).
		//
		// All-zero values are skipped: they're equivalent to "no entry"
		// in Erigon's storage domain, same as in geth's MPT (no leaf
		// for a never-set slot).
		for slot, value := range a.Storage {
			trimmed := trimLeadingZeros(value[:])
			if len(trimmed) == 0 {
				continue
			}
			storKey := make([]byte, 0, 52)
			storKey = append(storKey, a.Address[:]...)
			storKey = append(storKey, slot[:]...)

			storUpd := erigoncommitment.Update{
				Flags:      erigoncommitment.StorageUpdate,
				StorageLen: int8(len(trimmed)),
			}
			copy(storUpd.Storage[:], trimmed)
			plainKeys = append(plainKeys, storKey)
			updates = append(updates, storUpd)
			ctx.state[string(storKey)] = storUpd.Encode(nil, ctx.numBuf[:])
		}
	}

	// 3. Build the Updates tree using TouchPlainKeyDirect, which lets us
	// pass a pre-built *Update without going through TouchAccount /
	// TouchStorage / TouchCode (those would re-derive fields we've
	// already computed). The closure-based TouchPlainKey path used in
	// Erigon's test code reaches the unexported KeyUpdate fields,
	// which aren't accessible from outside the commitment package.
	upds := erigoncommitment.NewUpdates(
		erigoncommitment.ModeDirect,
		ctx.tmpDir,
		erigoncommitment.KeyToHexNibbleHash,
	)
	for i, key := range plainKeys {
		upds.TouchPlainKeyDirect(string(key), &updates[i])
	}

	// 4. Compute root via HexPatriciaHashed.
	hph := erigoncommitment.NewHexPatriciaHashed(20 /* accountKeyLen — Ethereum address */, ctx)
	rootBytes, err := hph.Process(context.Background(), upds, "state-actor-genesis", nil, erigoncommitment.WarmupConfig{})
	if err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: Process: %w", err)
	}
	if len(rootBytes) != 32 {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: unexpected root hash length %d", len(rootBytes))
	}
	var root gethcommon.Hash
	copy(root[:], rootBytes)

	// 5. Capture the HPH state for the KeyCommitmentState record we'll
	// write into commitment.0-N.kv. EncodeCurrentState(nil) is safe to
	// call post-Process and serializes the trie root cell + Depths +
	// TouchMap + AfterMap + branchBefore packing — exactly what the
	// daemon's first FCU needs to anchor commitment continuation.
	hphState, err := hph.EncodeCurrentState(nil)
	if err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: EncodeCurrentState: %w", err)
	}

	return Result{Root: root, BranchNodes: ctx.branches, HPHState: hphState}, nil
}

// genesisCtx implements erigoncommitment.PatriciaContext for a
// cold-start commitment over an in-memory alloc.
type genesisCtx struct {
	state    map[string][]byte // plainKey → erigoncommitment.Update.Encode bytes
	branches map[string][]byte // trie prefix → branch data
	numBuf   [binary.MaxVarintLen64]byte
	tmpDir   string
}

func newGenesisCtx() *genesisCtx {
	return &genesisCtx{
		state:    make(map[string][]byte),
		branches: make(map[string][]byte),
		tmpDir:   "",
	}
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
	enc, ok := c.state[string(plainKey)]
	if !ok {
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
	enc, ok := c.state[string(plainKey)]
	if !ok {
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
