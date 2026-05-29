//go:build cgo_erigon && cgo_erigon_commitment

package erigon

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	mrand "math/rand"

	"github.com/nerolation/state-actor/generator"
	internalerigon "github.com/nerolation/state-actor/internal/erigon"
	"github.com/nerolation/state-actor/internal/erigon/account"
	internalcommitment "github.com/nerolation/state-actor/internal/erigon/commitment"
	"github.com/nerolation/state-actor/internal/erigon/snap"
	"github.com/nerolation/state-actor/internal/streamsort"
)

// numRanges is the depth of the tail-pyramid LSM layout we emit. Five
// non-overlapping power-of-two-aligned step ranges (rangeIdx 0..4) —
// see ranges[] below. Spec entries pin to rangeIdx=0 (deepest cold
// storage); autofill fills rangeIdx=4 down to 1 by byte-count milestones.
const numRanges = 5

// ranges is the snapshot-file layout state-actor emits. Each range is
// power-of-two-aligned per upstream Erigon's merge invariant at
// db/state/merge.go::calculateMergeStartTxNum (a file's startTxNum
// MUST be a multiple of (endStep & -endStep)). Erigon's reader walks
// newest→oldest, so:
//
//   rangeIdx=4 ([30, 31), 1 step)  — probed FIRST  (autofill fills here)
//   rangeIdx=3 ([28, 30), 2 steps) — probed 2nd
//   rangeIdx=2 ([24, 28), 4 steps) — probed 3rd
//   rangeIdx=1 ([16, 24), 8 steps) — probed 4th
//   rangeIdx=0 ([0, 16), 16 steps) — probed LAST  (spec lives here)
//
// A cold spec-key lookup forces 4 existence-filter probes on the upper
// files (populated by autofill, ~1% FPR) + 1 BT walk on the deep
// rangeIdx=0 file — the realistic worst-case cold-key read path the
// bench is supposed to measure on a production-like Erigon node.
var ranges = [numRanges]snap.StepRange{
	{From: 0, To: 16},  // rangeIdx=0 — spec (deepest)
	{From: 16, To: 24}, // rangeIdx=1
	{From: 24, To: 28}, // rangeIdx=2
	{From: 28, To: 30}, // rangeIdx=3
	{From: 30, To: 31}, // rangeIdx=4 — autofill (shallowest, fresh)
}

// writeSnapshots is the streaming multi-range snapshot orchestrator.
//
// Memory bounded by streamsort working set (~4 stores × ~256 MiB
// memtable = ~1 GB) regardless of --target-size. Disk usage scales
// with the autofill payload (~20 GB at 25 GB target), landing under
// <cfg.DBPath>/streamsort-<domain>/ on the bind-mounted filesystem,
// NOT the container overlay.
//
// Phases:
//   1. Open 4 streamsorts (accounts/storage/code/commitmentInputs)
//      under cfg.DBPath.
//   2. Process foundational.Spec into all 4 stores at rangeIdx=0.
//   3. Run AutoFill RNG loop (byte-for-byte with geth's draw
//      sequence — preserves cross-client invariance). Each draw:
//      pick rangeIdx via byte-count milestones; encode + write
//      directly to all 4 streamsorts. NO in-memory aggregation
//      for autofill — this is the OOM fix.
//   4. Run HPH commitment over the commitmentInputStore (disk-backed
//      ctx.Account/Storage callbacks via streamsort.Get).
//   5. Multi-range write loop — 5 ranges × 4 domains:
//      - Accounts/Storage/Code: per-range WriteDomain with
//        FromStreamsortRange.
//      - Commitment: branches + KeyCommitmentState in NEWEST range
//        only (commitment.30-31.kv); older 4 ranges get 1-entry
//        placeholder files (empty KeyCommitmentState) — satisfies the
//        integrity-checker's AddDependencyBtwnDomains(AccountsDomain,
//        CommitmentDomain) without duplicating branch data.
//
// Returns the HPH root; runImpl patches it into block-0 header.stateRoot.
func writeSnapshots(
	ctx context.Context,
	cfg generator.Config,
	foundational *FoundationalAlloc,
	stats *generator.Stats,
) (common.Hash, error) {
	// -- Step 1: open 4 streamsorts under cfg.DBPath (bind-mounted disk).
	accountsStore, err := streamsort.New(cfg.DBPath)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open accounts streamsort: %w", err)
	}
	defer accountsStore.Close()

	storageStore, err := streamsort.New(cfg.DBPath)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open storage streamsort: %w", err)
	}
	defer storageStore.Close()

	codeStore, err := streamsort.New(cfg.DBPath)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open code streamsort: %w", err)
	}
	defer codeStore.Close()

	commitmentInputStore, err := streamsort.New(cfg.DBPath)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open commitmentInput streamsort: %w", err)
	}
	defer commitmentInputStore.Close()

	// Per-(domain, rangeIdx) entry counts for the WriteDomain keyCount arg.
	var counts struct {
		Accounts [numRanges]uint64
		Storage  [numRanges]uint64
		Code     [numRanges]uint64
	}

	// Per-rangeIdx running byte counter for the autofill milestone routing.
	// Only the autofill loop reads/writes this — spec is pinned to 0.
	var bytesIn [numRanges]uint64

	// -- Step 2: drain foundational.Spec into all 4 stores at rangeIdx=0.
	// Also build genesisAddrs for the dedup-redraw loop (matches geth
	// byte-for-byte: dedup against spec only, never against other
	// autofill draws — see client/geth/state_writer.go:120-148).
	genesisAddrs := make(map[common.Address]struct{}, len(foundational.Spec))
	for addr, entry := range foundational.Spec {
		genesisAddrs[addr] = struct{}{}
		if err := putEntry(0, addr, entry, &counts.Accounts[0], &counts.Storage[0], &counts.Code[0],
			accountsStore, storageStore, codeStore, commitmentInputStore); err != nil {
			return common.Hash{}, err
		}
	}

	// -- Step 3: AutoFill RNG loop — STREAMING.
	//
	// The dedup-redraw loop MUST match client/geth/state_writer.go:116-148
	// byte-for-byte: geth burns RNG draws on collision until it finds a
	// non-colliding address, with no nil-checks. Any nil-check break here
	// would skip the assignment but still advance the RNG, desynchronizing
	// from geth's draw sequence and producing a different alloc + a
	// different cross-client genesis state-root.
	//
	// AutoFill.Draw{EOA,Contract} return non-nil for all valid plans (a
	// nil return would crash geth at the same point — we mirror that
	// contract rather than guard against it).
	if cfg.AutoFill != nil {
		rng := mrand.New(mrand.NewSource(cfg.Seed))
		milestones := computeAutofillMilestones(cfg.TargetSize)
		for i := 0; i < cfg.AutoFill.NumEOAs; i++ {
			acc := cfg.AutoFill.DrawEOA(rng)
			for _, dup := genesisAddrs[acc.Address]; dup; {
				acc = cfg.AutoFill.DrawEOA(rng)
				_, dup = genesisAddrs[acc.Address]
			}
			rangeIdx := pickAutofillRange(bytesIn, milestones)
			entry := &allocAccount{Nonce: acc.StateAccount.Nonce}
			if acc.StateAccount.Balance != nil {
				entry.Balance = acc.StateAccount.Balance.ToBig()
			}
			if err := putEntry(rangeIdx, acc.Address, entry,
				&counts.Accounts[rangeIdx], &counts.Storage[rangeIdx], &counts.Code[rangeIdx],
				accountsStore, storageStore, codeStore, commitmentInputStore); err != nil {
				return common.Hash{}, err
			}
			// Track encoded byte size for milestone routing — account
			// SerialiseV3 averages ~30 bytes; conservative estimate.
			bytesIn[rangeIdx] += 32
			stats.AccountsCreated++
		}
		for i := 0; i < cfg.AutoFill.NumContracts; i++ {
			c := cfg.AutoFill.DrawContract(rng)
			for _, dup := genesisAddrs[c.Address]; dup; {
				c = cfg.AutoFill.DrawContract(rng)
				_, dup = genesisAddrs[c.Address]
			}
			rangeIdx := pickAutofillRange(bytesIn, milestones)
			entry := &allocAccount{Nonce: c.StateAccount.Nonce}
			if c.StateAccount.Balance != nil {
				entry.Balance = c.StateAccount.Balance.ToBig()
			}
			if len(c.Code) > 0 {
				entry.Code = c.Code
				stats.CodeBytes += uint64(len(c.Code))
			}
			if err := putEntry(rangeIdx, c.Address, entry,
				&counts.Accounts[rangeIdx], &counts.Storage[rangeIdx], &counts.Code[rangeIdx],
				accountsStore, storageStore, codeStore, commitmentInputStore); err != nil {
				return common.Hash{}, err
			}
			// Stream storage slots inline — slice released after iteration.
			for _, s := range c.Storage {
				if err := putStorageSlot(rangeIdx, c.Address, s.Key, s.Value,
					&counts.Storage[rangeIdx], &bytesIn[rangeIdx],
					storageStore, commitmentInputStore); err != nil {
					return common.Hash{}, err
				}
			}
			stats.StorageSlotsCreated += len(c.Storage)
			stats.StorageBytes += uint64(len(c.Storage)) * 64
			bytesIn[rangeIdx] += uint64(len(entry.Code)) + 32
			stats.ContractsCreated++
		}
	}
	stats.TotalBytes = stats.StorageBytes + stats.CodeBytes

	// -- Step 4: HPH commitment walk over commitmentInputStore.
	// ctx.Account/Storage callbacks now read from streamsort.Get
	// (disk-backed). branches map stays in memory (bounded).
	result, err := internalcommitment.ComputeGenesisRoot(commitmentInputStore)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: ComputeGenesisRoot: %w", err)
	}
	keyStateValue, err := internalcommitment.EncodeKeyCommitmentStateValue(0, 0, result.HPHState)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: encode KeyCommitmentState: %w", err)
	}

	// -- Step 5a: marshal the global branches map into a streamsort
	// keyed by branch prefix (sorted for deterministic .kv output).
	branchesStore, err := streamsort.New(cfg.DBPath)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open branches streamsort: %w", err)
	}
	defer branchesStore.Close()
	var nBranches uint64
	for prefix, data := range result.BranchNodes {
		if err := branchesStore.Put([]byte(prefix), data); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: put branch %x: %w", []byte(prefix), err)
		}
		nBranches++
	}

	// -- Step 5b: snap.NewWriter + multi-range emit.
	settings := snap.Settings{
		Seed:              cfg.Seed,
		StepSize:          internalerigon.StepSize,
		StepsInFrozenFile: internalerigon.StepsInFrozenFile,
		SnapshotVersion:   internalerigon.SnapshotFormatVersion,
	}
	w, err := snap.NewWriter(cfg.DBPath, settings)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: snap.NewWriter: %w", err)
	}
	defer w.Close()

	for i := uint8(0); i < numRanges; i++ {
		r := ranges[i]
		if err := w.WriteDomain(ctx, snap.DomainAccounts, r, counts.Accounts[i],
			snap.FromStreamsortRange(accountsStore, i)); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: WriteDomain(Accounts, range=%v): %w", r, err)
		}
		if err := w.WriteDomain(ctx, snap.DomainStorage, r, counts.Storage[i],
			snap.FromStreamsortRange(storageStore, i)); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: WriteDomain(Storage, range=%v): %w", r, err)
		}
		if err := w.WriteDomain(ctx, snap.DomainCode, r, counts.Code[i],
			snap.FromStreamsortRange(codeStore, i)); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: WriteDomain(Code, range=%v): %w", r, err)
		}
	}

	// Commitment: branches + KeyCommitmentState in NEWEST range only.
	newestRange := ranges[numRanges-1]
	if err := snap.WriteCommitment(ctx, w, newestRange, keyStateValue, branchesStore, nBranches); err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: WriteCommitment(newest): %w", err)
	}
	// Older ranges get 1-entry placeholder files (empty trieState).
	// Daemon's first-FCU SeekCommitment reads ONLY the newest visible
	// commitment file's KeyCommitmentState per upstream's newest-wins
	// GetLatest path (db/state/domain.go:1290-1369), so older placeholder
	// records are inert — they only exist to satisfy the integrity
	// checker's AddDependencyBtwnDomains rule at every accounts range.
	emptyKeyState, err := internalcommitment.EncodeKeyCommitmentStateValue(0, 0, nil)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: encode empty KeyCommitmentState: %w", err)
	}
	for i := 0; i < numRanges-1; i++ {
		if err := snap.WriteCommitmentPlaceholder(ctx, w, ranges[i], emptyKeyState); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: WriteCommitmentPlaceholder(range=%v): %w", ranges[i], err)
		}
	}

	if cfg.Verbose {
		fmt.Printf("client/erigon: wrote snapshots: spec=%d autofill_accounts=%d contracts=%d storage_slots=%d branches=%d root=%s\n",
			len(foundational.Spec), stats.AccountsCreated, stats.ContractsCreated, stats.StorageSlotsCreated, nBranches, result.Root.Hex())
		fmt.Printf("client/erigon: per-range entry counts: accounts=%v storage=%v code=%v\n",
			counts.Accounts, counts.Storage, counts.Code)
	}

	return result.Root, nil
}

// putEntry encodes one alloc entry + writes to all 4 streamsorts.
//
// For accounts/storage/code stores: composite key = (rangeIdx:u8 || ...)
// — enables prefix-scan per range via FromStreamsortRange.
// For commitmentInputStore: plain key (no rangeIdx prefix) — ctx.Account
// looks up by plain key during the HPH walk, and key-uniqueness is
// global across ranges (every address shows up in exactly one range).
//
// Updates per-(domain, range) counters via the *uint64 pointers.
func putEntry(
	rangeIdx uint8,
	addr common.Address,
	entry *allocAccount,
	nAcct, nStor, nCode *uint64,
	accountsStore, storageStore, codeStore, commitmentInputStore *streamsort.Store,
) error {
	// Compose snap-store key: (rangeIdx || addr).
	addrComposite := make([]byte, 0, 1+20)
	addrComposite = append(addrComposite, rangeIdx)
	addrComposite = append(addrComposite, addr[:]...)

	// Accounts snapshot value: SerialiseV3.
	acct := account.Account{
		Nonce:    entry.Nonce,
		CodeHash: account.EmptyCodeHash,
	}
	if entry.Balance != nil {
		b, overflow := uint256.FromBig(entry.Balance)
		if overflow {
			return fmt.Errorf("putEntry: balance overflow for %s", addr.Hex())
		}
		acct.Balance = *b
	}
	if len(entry.Code) > 0 {
		h := crypto.Keccak256Hash(entry.Code)
		copy(acct.CodeHash[:], h[:])
	}
	if err := accountsStore.Put(addrComposite, account.SerialiseV3(acct)); err != nil {
		return fmt.Errorf("putEntry: put accounts[%s]: %w", addr.Hex(), err)
	}
	(*nAcct)++

	// Code snapshot value: raw bytecode keyed by addr.
	if len(entry.Code) > 0 {
		if err := codeStore.Put(addrComposite, entry.Code); err != nil {
			return fmt.Errorf("putEntry: put code[%s]: %w", addr.Hex(), err)
		}
		(*nCode)++
	}

	// Foundational entries may have inline Storage (PreAlloc /
	// GenesisStorage). Stream into storageStore + commitmentInputStore.
	for slot, value := range entry.Storage {
		if err := putStorageSlot(rangeIdx, addr, slot, value,
			nStor, nil, storageStore, commitmentInputStore); err != nil {
			return err
		}
	}

	// Commitment input value: Update.Encode keyed by plain addr.
	var balance *uint256.Int
	if entry.Balance != nil {
		b, overflow := uint256.FromBig(entry.Balance)
		if overflow {
			return fmt.Errorf("putEntry: balance overflow for commitment %s", addr.Hex())
		}
		balance = b
	}
	commitBytes := internalcommitment.EncodeAccountUpdate(entry.Nonce, balance, entry.Code)
	if err := commitmentInputStore.Put(addr[:], commitBytes); err != nil {
		return fmt.Errorf("putEntry: put commitmentInput[%s]: %w", addr.Hex(), err)
	}
	return nil
}

// putStorageSlot writes one (addr, slot, value) tuple to storageStore +
// commitmentInputStore. Skip on all-zero value (Erigon's StorageDomain
// treats absent ≡ zero; storing zero is wrong).
//
// bytesAcc may be nil if caller doesn't track byte counts for this slot
// (e.g. foundational storage isn't milestone-routed).
func putStorageSlot(
	rangeIdx uint8,
	addr common.Address,
	slotKey common.Hash,
	slotValue common.Hash,
	nStor *uint64,
	bytesAcc *uint64,
	storageStore, commitmentInputStore *streamsort.Store,
) error {
	trimmed := trimLeadingZeros(slotValue[:])
	if len(trimmed) == 0 {
		return nil
	}
	// Composite key for snapshot: (rangeIdx || addr || slot) = 53 bytes.
	composite := make([]byte, 0, 1+20+32)
	composite = append(composite, rangeIdx)
	composite = append(composite, addr[:]...)
	composite = append(composite, slotKey[:]...)
	if err := storageStore.Put(composite, trimmed); err != nil {
		return fmt.Errorf("putStorageSlot: put storage[%x|%x]: %w", addr, slotKey, err)
	}
	(*nStor)++

	// Plain key for commitment: addr || slot = 52 bytes (no rangeIdx).
	plainKey := make([]byte, 0, 20+32)
	plainKey = append(plainKey, addr[:]...)
	plainKey = append(plainKey, slotKey[:]...)
	commitBytes := internalcommitment.EncodeStorageUpdate(slotValue[:])
	if err := commitmentInputStore.Put(plainKey, commitBytes); err != nil {
		return fmt.Errorf("putStorageSlot: put commitmentInput storage[%x|%x]: %w", addr, slotKey, err)
	}
	if bytesAcc != nil {
		*bytesAcc += uint64(len(trimmed)) + 64
	}
	return nil
}

// pickAutofillRange returns the rangeIdx for the next autofill entry,
// shallowest-first (rangeIdx=numRanges-1 fills first; spills DOWNWARD
// to rangeIdx=1 as each upper range hits its milestone). rangeIdx=0
// is reserved for spec.
//
// Per the plan's tail-pyramid layout: autofill represents "fresh"
// data that lives in the upper LSM layers (queried first by the
// daemon's newest→oldest reader walk). Spec data is "cold" — pinned
// to the deepest layer (rangeIdx=0), only reached after the reader's
// existence-filter probes miss on all upper files.
//
// milestones[i] is the cumulative byte threshold AFTER which range
// (numRanges-1-i) is considered full and we spill to (numRanges-2-i).
func pickAutofillRange(bytesIn [numRanges]uint64, milestones [numRanges - 1]uint64) uint8 {
	totalAuto := uint64(0)
	for i := uint8(1); i < numRanges; i++ {
		totalAuto += bytesIn[i]
	}
	for i := 0; i < numRanges-1; i++ {
		if totalAuto < milestones[i] {
			return uint8(numRanges - 1 - i)
		}
	}
	return 1 // overflow past targetSize — keep packing the lowest autofill range
}

// computeAutofillMilestones derives the per-step byte thresholds from
// the runtime target-size cap. Geometric pyramid: 50% / 25% / 12.5% /
// 12.5% of target into ranges 4, 3, 2, 1 respectively.
func computeAutofillMilestones(targetSize uint64) [numRanges - 1]uint64 {
	// Fallback if cfg.TargetSize wasn't set (parsed defaults).
	if targetSize == 0 {
		targetSize = 25 * 1024 * 1024 * 1024 // 25 GiB
	}
	return [numRanges - 1]uint64{
		targetSize / 2,                  // rangeIdx=4 fills until 50%
		targetSize / 2 + targetSize / 4, // rangeIdx=3 fills 50%-75%
		targetSize / 2 + targetSize / 4 + targetSize / 8, // rangeIdx=2 fills 75%-87.5%
		targetSize, // rangeIdx=1 fills 87.5%-100%
	}
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
