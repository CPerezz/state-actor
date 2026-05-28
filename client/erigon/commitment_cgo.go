//go:build cgo_erigon && cgo_erigon_commitment

package erigon

import (
	"encoding/binary"
	"fmt"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/erigon/commitment"
)

// MDBX env geometry — mirrors Erigon's kv_mdbx.go defaults so the
// daemon's compatibility check passes when it later reopens chaindata.
const (
	headerPatchPageSize    = 4096
	headerPatchMapSize     = 4 * 1024 * 1024 * 1024 * 1024
	headerPatchGrowthStep  = 4 * 1024 * 1024 * 1024
	headerPatchMaxDBs uint = 200

	bucketHeaders         = "Header"
	bucketHeaderCanonical = "CanonicalHeader"
)

// runCommitmentPhase computes the HexPatriciaHashed commitment root
// over the post-bloat alloc + storage. Returns (root, branches map,
// HPHState bytes, true, nil) on success.
//
// In the snapshot-tier architecture the branch nodes go into
// commitment.0-N.kv (NOT MDBX TblCommitmentVals). The caller is
// responsible for:
//
//   - Streaming the branches map + KeyCommitmentState record into a
//     snap.Writer commitment-domain pass (see internal/erigon/snap.WriteCommitment).
//   - Encoding HPHState into the KeyCommitmentState record value via
//     commitment.EncodeKeyCommitmentStateValue(txNum, blockNum, HPHState).
//   - Patching block 0's header.stateRoot via patchGenesisHeaderStateRoot
//     so the daemon's first-FCU root-validation passes.
//
// runCommitmentPhase does NOT patch the header itself — the orchestrator
// drives the snapshot write + header patch as the final step so a failed
// write doesn't leave the header pointing at a non-existent root.
func runCommitmentPhase(
	alloc map[common.Address]*allocAccount,
	storageMap map[[20]byte]map[[32]byte][32]byte,
) (common.Hash, map[string][]byte, []byte, bool, error) {
	accounts := buildCommitmentAccounts(alloc, storageMap)
	result, err := commitment.ComputeGenesisRoot(accounts)
	if err != nil {
		return common.Hash{}, nil, nil, false, fmt.Errorf("ComputeGenesisRoot: %w", err)
	}
	return result.Root, result.BranchNodes, result.HPHState, true, nil
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

// patchGenesisHeaderStateRoot rewrites block 0's header.stateRoot in the
// chaindata Headers table. Opens MDBX inline (no shared wrapper); closes
// before returning.
//
// Required because erigon init writes block 0 with the
// system-contracts-only state root, but our snapshot-tier writer fills
// in the bloat state — the daemon's first FCU validates by recomputing
// root over the visible state and comparing to this field.
//
// The Headers table is keyed by blockNum(u64-BE) || hash(32); block 0's
// hash is looked up via the CanonicalHeader table.
func patchGenesisHeaderStateRoot(dbPath string, root common.Hash) error {
	chaindataDir := filepath.Join(dbPath, "chaindata")

	env, err := mdbx.NewEnv(mdbx.Label("chaindata"))
	if err != nil {
		return fmt.Errorf("mdbx.NewEnv: %w", err)
	}
	defer env.Close()

	if err := env.SetOption(mdbx.OptMaxDB, uint64(headerPatchMaxDBs)); err != nil {
		return fmt.Errorf("mdbx.SetOption(MaxDB): %w", err)
	}
	if err := env.SetGeometry(-1, -1, headerPatchMapSize, headerPatchGrowthStep, -1, headerPatchPageSize); err != nil {
		return fmt.Errorf("mdbx.SetGeometry: %w", err)
	}
	if err := env.Open(chaindataDir, mdbx.Durable, 0o644); err != nil {
		return fmt.Errorf("mdbx.Open(%s): %w", chaindataDir, err)
	}

	return env.Update(func(txn *mdbx.Txn) error {
		canonicalDBI, err := txn.OpenDBI(bucketHeaderCanonical, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketHeaderCanonical, err)
		}
		headersDBI, err := txn.OpenDBI(bucketHeaders, 0, nil, nil)
		if err != nil {
			return fmt.Errorf("OpenDBI(%s): %w", bucketHeaders, err)
		}

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
