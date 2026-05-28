//go:build cgo_erigon && cgo_erigon_commitment

package erigon

import (
	"context"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	internalerigon "github.com/nerolation/state-actor/internal/erigon"
	"github.com/nerolation/state-actor/internal/erigon/account"
	internalcommitment "github.com/nerolation/state-actor/internal/erigon/commitment"
	"github.com/nerolation/state-actor/internal/erigon/snap"
	"github.com/nerolation/state-actor/internal/streamsort"
)

// writeSnapshots is the streaming snapshot orchestrator (plan PART 5).
//
// Builds four streamsort.Store instances (accounts / storage / code /
// commitment-branches), feeds them from the in-memory alloc +
// autofillStor side-channel, runs HexPatriciaHashed over the same
// inputs (in-memory; produces root + branches map + HPHState), then
// emits the per-domain snapshot file sets via snap.Writer:
//
//	v1.0-accounts.0-1.kv    + .bt   + .kvei
//	v1.0-storage.0-1.kv     + .bt   + .kvei
//	v1.0-code.0-1.kv        + .bt   + .kvei
//	v1.0-commitment.0-1.kv  + .kvi  + .kvei
//
// snap.NewWriter additionally writes <dbPath>/snapshots/salt-state.txt +
// erigondb.toml during construction (FS preconditions, plan PART 7).
//
// Returns the HPH root so the caller can patch block 0's header.stateRoot.
// Errors short-circuit; partial output is left on disk (caller's
// responsibility to clean up the datadir on failure).
//
// Memory profile: streamsort stores cap peak resident memory at
// ~1.3 GiB (4 × 256 MiB memtables + 4 × 8 MiB block caches). The
// in-memory `alloc` + `autofillStor` inputs add their own footprint
// (~2-7 GiB at 25 GB bloat scale). True end-to-end streaming where
// alloc itself spills is a v2 optimization.
func writeSnapshots(
	ctx context.Context,
	dbPath string,
	seed int64,
	alloc map[common.Address]*allocAccount,
	autofillStor []autofillContractStorage,
	verbose bool,
) (common.Hash, error) {
	// Step 1: rebuild storageMap from autofillStor for both feeding the
	// streamsort AND passing into runCommitmentPhase. PreAlloc storage
	// is already in alloc[].Storage and is handled by the iteration
	// over alloc below.
	storageMap := make(map[[20]byte]map[[32]byte][32]byte, len(autofillStor))
	for _, ent := range autofillStor {
		var addr [20]byte
		copy(addr[:], ent.addr[:])
		if _, ok := storageMap[addr]; !ok {
			storageMap[addr] = make(map[[32]byte][32]byte, len(ent.slots))
		}
		for _, s := range ent.slots {
			var sk, sv [32]byte
			copy(sk[:], s.Key[:])
			copy(sv[:], s.Value[:])
			storageMap[addr][sk] = sv
		}
	}
	// Also fold alloc[].Storage (PreAlloc / GenesisStorage) into storageMap
	// so the HPH walker and the storage streamsort see the same dataset.
	for addr, entry := range alloc {
		if len(entry.Storage) == 0 {
			continue
		}
		var addrBytes [20]byte
		copy(addrBytes[:], addr[:])
		if _, ok := storageMap[addrBytes]; !ok {
			storageMap[addrBytes] = make(map[[32]byte][32]byte, len(entry.Storage))
		}
		for k, v := range entry.Storage {
			var sk, sv [32]byte
			copy(sk[:], k[:])
			copy(sv[:], v[:])
			storageMap[addrBytes][sk] = sv
		}
	}

	// Step 2: spin up the four streamsort.Stores.
	accountsStore, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open accounts streamsort: %w", err)
	}
	defer accountsStore.Close()

	storageStore, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open storage streamsort: %w", err)
	}
	defer storageStore.Close()

	codeStore, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open code streamsort: %w", err)
	}
	defer codeStore.Close()

	branchesStore, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open commitment-branches streamsort: %w", err)
	}
	defer branchesStore.Close()

	// Step 3: feed accounts / storage / code from alloc + storageMap.
	var nAccounts, nStorage, nCode uint64
	for addr, entry := range alloc {
		// Account record. Erigon's Accounts domain key = raw 20-byte
		// address; value = accounts.SerialiseV3(...).
		acct := account.Account{
			Nonce:    entry.Nonce,
			CodeHash: account.EmptyCodeHash,
		}
		if entry.Balance != nil {
			b, overflow := uint256.FromBig(entry.Balance)
			if overflow {
				return common.Hash{}, fmt.Errorf("writeSnapshots: balance overflow for %s", addr.Hex())
			}
			acct.Balance = *b
		}
		if len(entry.Code) > 0 {
			h := crypto.Keccak256Hash(entry.Code)
			copy(acct.CodeHash[:], h[:])
		}
		val := account.SerialiseV3(acct)
		if err := accountsStore.Put(addr[:], val); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: put accounts[%s]: %w", addr.Hex(), err)
		}
		nAccounts++

		// Code record (only if contract). Erigon's Code domain key = raw
		// 20-byte address (NOT codeHash); value = raw bytecode.
		if len(entry.Code) > 0 {
			if err := codeStore.Put(addr[:], entry.Code); err != nil {
				return common.Hash{}, fmt.Errorf("writeSnapshots: put code[%s]: %w", addr.Hex(), err)
			}
			nCode++
		}
	}
	// Storage records — iterate the merged storageMap.
	for addr, slots := range storageMap {
		for slot, value := range slots {
			trimmed := trimLeadingZeros(value[:])
			if len(trimmed) == 0 {
				continue // Erigon stores absent rather than zero
			}
			// Composite key = address(20) || slot(32) = 52 bytes raw.
			key := make([]byte, 0, 52)
			key = append(key, addr[:]...)
			key = append(key, slot[:]...)
			if err := storageStore.Put(key, trimmed); err != nil {
				return common.Hash{}, fmt.Errorf("writeSnapshots: put storage[%x|%x]: %w", addr, slot, err)
			}
			nStorage++
		}
	}

	// Step 4: HPH commitment. In-memory pass produces root + branches
	// map + HPHState. The branches map is then drained into branchesStore
	// for the snapshot WriteCommitment pass.
	root, branches, hphState, computed, err := runCommitmentPhase(alloc, storageMap)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: runCommitmentPhase: %w", err)
	}
	if !computed {
		return common.Hash{}, fmt.Errorf("writeSnapshots: runCommitmentPhase returned !computed " +
			"(missing cgo_erigon_commitment build tag?)")
	}

	// Sort branch keys before Put so iteration order through streamsort
	// is deterministic for byte-stable .kv output between runs (Pebble
	// already sorts internally, but sorting at Put time avoids any
	// surprises from map-iteration nondeterminism).
	branchKeys := make([]string, 0, len(branches))
	for k := range branches {
		branchKeys = append(branchKeys, k)
	}
	sort.Strings(branchKeys)
	var nBranches uint64
	for _, k := range branchKeys {
		if err := branchesStore.Put([]byte(k), branches[k]); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: put branch %x: %w", []byte(k), err)
		}
		nBranches++
	}

	keyStateValue, err := internalcommitment.EncodeKeyCommitmentStateValue(0, 0, hphState)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: encode KeyCommitmentState: %w", err)
	}

	// Step 5: snap.NewWriter — creates snapshots/ layout, writes
	// salt-state.txt + erigondb.toml.
	settings := snap.Settings{
		Seed:              seed,
		StepSize:          internalerigon.StepSize,
		StepsInFrozenFile: internalerigon.StepsInFrozenFile,
		SnapshotVersion:   internalerigon.SnapshotFormatVersion,
	}
	w, err := snap.NewWriter(dbPath, settings)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: snap.NewWriter: %w", err)
	}
	defer w.Close()

	// Step 6: emit the four value-domain snapshot file sets. Step range
	// is [0, 1) for v1 — single-step file, the smallest valid range.
	stepRange := snap.StepRange{From: 0, To: 1}

	if err := w.WriteDomain(ctx, snap.DomainAccounts, stepRange, nAccounts,
		snap.FromStreamsort(accountsStore)); err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: WriteDomain(Accounts, %d entries): %w", nAccounts, err)
	}
	if err := w.WriteDomain(ctx, snap.DomainStorage, stepRange, nStorage,
		snap.FromStreamsort(storageStore)); err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: WriteDomain(Storage, %d entries): %w", nStorage, err)
	}
	if err := w.WriteDomain(ctx, snap.DomainCode, stepRange, nCode,
		snap.FromStreamsort(codeStore)); err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: WriteDomain(Code, %d entries): %w", nCode, err)
	}
	if err := snap.WriteCommitment(ctx, w, stepRange, keyStateValue, branchesStore, nBranches); err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: WriteCommitment(%d branches): %w", nBranches, err)
	}

	if verbose {
		fmt.Printf("client/erigon: wrote snapshots: accounts=%d storage=%d code=%d commitment-branches=%d root=%s\n",
			nAccounts, nStorage, nCode, nBranches, root.Hex())
	}

	return root, nil
}

// trimLeadingZeros returns the suffix of b after the longest run of
// leading zero bytes. Returns an empty slice for an all-zero input
// (matches Erigon's storage-domain "absent = zero" semantics).
func trimLeadingZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	return b[i:]
}
