//go:build cgo_erigon && cgo_erigon_commitment

package erigon

import (
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/erigon/commitment"
	erigonmdbx "github.com/nerolation/state-actor/internal/erigon/mdbx"
)

// runCommitmentPhase computes the HexPatriciaHashed commitment root over
// the post-Phase-B state (alloc + storageMap), writes the resulting
// branch nodes to TblCommitmentVals (Phase C), and patches block 0's
// header.stateRoot in the Headers table (Phase D).
//
// Returns the HPH root, true (commitment was computed), and any error.
// Caller should override stats.StateRoot with the returned hash so the
// orchestrator surfaces the post-commitment root rather than the
// erigon-init partial root.
//
// With `!cgo_erigon_commitment` the stub variant returns zero-hash +
// false and the orchestrator keeps the existing stats.StateRoot.
func runCommitmentPhase(
	env *erigonmdbx.Env,
	alloc map[common.Address]*allocAccount,
	storageMap map[[20]byte]map[[32]byte][32]byte,
) (common.Hash, bool, error) {
	accounts := buildCommitmentAccounts(alloc, storageMap)
	result, err := commitment.ComputeGenesisRoot(accounts)
	if err != nil {
		return common.Hash{}, false, fmt.Errorf("ComputeGenesisRoot: %w", err)
	}
	if err := writeCommitmentBranches(env, result.BranchNodes); err != nil {
		return common.Hash{}, false, fmt.Errorf("writeCommitmentBranches: %w", err)
	}
	if err := patchGenesisHeaderStateRoot(env, result.Root); err != nil {
		return common.Hash{}, false, fmt.Errorf("patchGenesisHeaderStateRoot: %w", err)
	}
	return result.Root, true, nil
}

// buildCommitmentAccounts flattens (alloc, storageMap) into the shape
// the commitment package expects. Address ordering does not matter —
// ComputeGenesisRoot sorts internally by plain-key.
func buildCommitmentAccounts(
	alloc map[common.Address]*allocAccount,
	storageMap map[[20]byte]map[[32]byte][32]byte,
) []commitment.Account {
	accounts := make([]commitment.Account, 0, len(alloc))
	for addr, entry := range alloc {
		acct := commitment.Account{
			Address: addr,
			Nonce:   entry.Nonce,
		}
		if entry.Balance != nil {
			b, overflow := uint256.FromBig(entry.Balance)
			if overflow {
				continue
			}
			acct.Balance = b
		}
		if len(entry.Code) > 0 {
			acct.Code = entry.Code
		}
		var addrBytes [20]byte
		copy(addrBytes[:], addr[:])
		if storage, ok := storageMap[addrBytes]; ok && len(storage) > 0 {
			acct.Storage = make(map[common.Hash]common.Hash, len(storage))
			for slot, value := range storage {
				var k, v common.Hash
				copy(k[:], slot[:])
				copy(v[:], value[:])
				acct.Storage[k] = v
			}
		}
		accounts = append(accounts, acct)
	}
	return accounts
}

// writeCommitmentBranches writes branch-node entries to TblCommitmentVals.
// Single transaction is acceptable because branch count is bounded by
// trie depth × account count (typically << storage slot count) and
// stays well under MDBX's dirty-page budget.
func writeCommitmentBranches(env *erigonmdbx.Env, branches map[string][]byte) error {
	return env.Env.Update(func(txn *mdbx.Txn) error {
		dbi := env.DBIs[erigonmdbx.TblCommitmentVals]
		for prefix, data := range branches {
			if err := txn.Put(dbi, []byte(prefix), data, 0); err != nil {
				return fmt.Errorf("Put(TblCommitmentVals, prefix=%x): %w", prefix, err)
			}
		}
		return nil
	})
}

// patchGenesisHeaderStateRoot rewrites block 0's header RLP in the
// Headers table so the stateRoot field equals the HPH root we computed.
// The Headers table is keyed by blockNum(u64-BE) || hash(32); we look
// up block 0's hash via HeaderCanonical first.
//
// RLP is length-prefixed, so we decode→edit→re-encode rather than
// patching bytes in place.
func patchGenesisHeaderStateRoot(env *erigonmdbx.Env, root common.Hash) error {
	return env.Env.Update(func(txn *mdbx.Txn) error {
		canonicalDBI := env.DBIs[erigonmdbx.HeaderCanonical]
		headersDBI := env.DBIs[erigonmdbx.Headers]

		blockNumKey := make([]byte, 8)
		binary.BigEndian.PutUint64(blockNumKey, 0)

		hashBytes, err := txn.Get(canonicalDBI, blockNumKey)
		if err != nil {
			return fmt.Errorf("Get(HeaderCanonical, blockNum=0): %w", err)
		}
		if len(hashBytes) != 32 {
			return fmt.Errorf("HeaderCanonical[0] has len=%d, want 32", len(hashBytes))
		}

		headersKey := make([]byte, 0, 8+32)
		headersKey = append(headersKey, blockNumKey...)
		headersKey = append(headersKey, hashBytes...)

		headerRLP, err := txn.Get(headersDBI, headersKey)
		if err != nil {
			return fmt.Errorf("Get(Headers, blockNum=0||hash): %w", err)
		}
		var h types.Header
		if err := rlp.DecodeBytes(headerRLP, &h); err != nil {
			return fmt.Errorf("RLP decode block-0 header: %w", err)
		}
		h.Root = root
		newRLP, err := rlp.EncodeToBytes(&h)
		if err != nil {
			return fmt.Errorf("RLP encode patched block-0 header: %w", err)
		}
		if err := txn.Put(headersDBI, headersKey, newRLP, 0); err != nil {
			return fmt.Errorf("Put(Headers, blockNum=0||hash): %w", err)
		}
		return nil
	})
}
