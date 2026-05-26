//go:build cgo_erigon

// snapshots_cgo.go contains the optional snap.Writer pass that emits
// pure-Go cold-tier snapshot files alongside the `erigon init` MDBX
// chaindata. Enabled via Options.WriteSnapshots — default is FALSE
// (the bench's working path is `erigon init` alone). See options.go's
// doc on WriteSnapshots for the rationale.

package erigon

import (
	"bytes"
	"context"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"

	"github.com/nerolation/state-actor/internal/erigon/account"
	"github.com/nerolation/state-actor/internal/erigon/snap"
)

// writeSnapshots emits per-domain snapshot files into <dbPath>/snapshots/
// from the materialized alloc. The Accounts/Storage/Code domains are
// emitted; Commitment is deferred (Plan Task 72 — requires a
// HexPatriciaHashed walk over the same alloc).
//
// The output is byte-deterministic for a given (cfg.Seed, alloc) pair:
// salt is derived via snap.DeriveSaltFromSeed.
func writeSnapshots(ctx context.Context, dbPath string, seed int64, alloc map[common.Address]*allocAccount) error {
	w, err := snap.NewWriter(dbPath, snap.Settings{Seed: seed})
	if err != nil {
		return fmt.Errorf("snap.NewWriter: %w", err)
	}
	defer w.Close()

	r := snap.StepRange{From: 0, To: 256}

	// 1. Accounts domain.
	//    Key = address[20B] (Verifier B Correction 4: NO hashing, NO incarnation).
	//    Value = account.EncodeForStorage(...).
	addrs := make([]common.Address, 0, len(alloc))
	for addr := range alloc {
		addrs = append(addrs, addr)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})

	accountEntries := func(yield func(snap.DomainEntry) bool) {
		for _, addr := range addrs {
			a := alloc[addr]
			erigonAcct := account.Account{
				Nonce: a.Nonce,
			}
			if a.Balance != nil {
				erigonAcct.Balance.SetFromBig(a.Balance)
			}
			if len(a.Code) > 0 {
				h := sha3.NewLegacyKeccak256()
				_, _ = h.Write(a.Code)
				copy(erigonAcct.CodeHash[:], h.Sum(nil))
			} else {
				erigonAcct.CodeHash = account.EmptyCodeHash
			}
			val := account.AppendForStorage(erigonAcct, nil)
			if !yield(snap.DomainEntry{Key: append([]byte(nil), addr[:]...), Value: val}) {
				return
			}
		}
	}
	if err := w.WriteDomain(ctx, snap.DomainAccounts, r, uint64(len(addrs)), accountEntries); err != nil {
		return fmt.Errorf("WriteDomain(Accounts): %w", err)
	}

	// 2. Storage domain.
	//    Key = address[20B] || slot[32B] = 52B (Verifier B Correction 4).
	//    Value = trimmed leading-zero bytes (Erigon's storage compression).
	type storageEntry struct {
		Key   []byte // 52B composite
		Value []byte
	}
	var storage []storageEntry
	for _, addr := range addrs {
		a := alloc[addr]
		if len(a.Storage) == 0 {
			continue
		}
		slots := make([]common.Hash, 0, len(a.Storage))
		for slot := range a.Storage {
			slots = append(slots, slot)
		}
		sort.Slice(slots, func(i, j int) bool {
			return bytes.Compare(slots[i][:], slots[j][:]) < 0
		})
		for _, slot := range slots {
			val := a.Storage[slot]
			key := make([]byte, 52)
			copy(key[:20], addr[:])
			copy(key[20:], slot[:])
			storage = append(storage, storageEntry{Key: key, Value: trimLeadingZeros(val[:])})
		}
	}
	// Storage is already in (addr, slot) sort order from the address+slot loops above.
	storageStream := func(yield func(snap.DomainEntry) bool) {
		for _, e := range storage {
			if !yield(snap.DomainEntry{Key: e.Key, Value: e.Value}) {
				return
			}
		}
	}
	if err := w.WriteDomain(ctx, snap.DomainStorage, r, uint64(len(storage)), storageStream); err != nil {
		return fmt.Errorf("WriteDomain(Storage): %w", err)
	}

	// 3. Code domain.
	//    Key = codeHash[32B]. Deduplicated across addresses.
	//    Value = bytecode.
	codeMap := make(map[common.Hash][]byte)
	for _, addr := range addrs {
		a := alloc[addr]
		if len(a.Code) == 0 {
			continue
		}
		h := sha3.NewLegacyKeccak256()
		_, _ = h.Write(a.Code)
		var hash common.Hash
		copy(hash[:], h.Sum(nil))
		codeMap[hash] = a.Code
	}
	codeHashes := make([]common.Hash, 0, len(codeMap))
	for h := range codeMap {
		codeHashes = append(codeHashes, h)
	}
	sort.Slice(codeHashes, func(i, j int) bool {
		return bytes.Compare(codeHashes[i][:], codeHashes[j][:]) < 0
	})
	codeStream := func(yield func(snap.DomainEntry) bool) {
		for _, h := range codeHashes {
			if !yield(snap.DomainEntry{Key: append([]byte(nil), h[:]...), Value: codeMap[h]}) {
				return
			}
		}
	}
	if err := w.WriteDomain(ctx, snap.DomainCode, r, uint64(len(codeHashes)), codeStream); err != nil {
		return fmt.Errorf("WriteDomain(Code): %w", err)
	}

	// 4. Commitment domain: DEFERRED — requires HexPatriciaHashed walk
	// over the populated Accounts/Storage/Code state. Plan Task 72
	// covers the implementation. Without commitment files, an Erigon
	// daemon booting against THIS snapshot dir alone (no MDBX) would
	// fail; today we co-emit alongside `erigon init`'s MDBX so the
	// daemon's first-FCU stage cycle rebuilds commitment in memory.

	return nil
}

// trimLeadingZeros mirrors Erigon's storage-value compression at
// db/state/domain.go: leading zero bytes of the 32B slot value are
// stripped before encoding. A literal all-zero value encodes as the
// empty byte slice.
func trimLeadingZeros(v []byte) []byte {
	i := 0
	for i < len(v) && v[i] == 0 {
		i++
	}
	return v[i:]
}
