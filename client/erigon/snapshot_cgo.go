//go:build cgo_erigon && cgo_erigon_commitment

package erigon

import (
	"context"
	"fmt"
	mrand "math/rand"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	internalerigon "github.com/nerolation/state-actor/internal/erigon"
	"github.com/nerolation/state-actor/internal/erigon/account"
	internalcommitment "github.com/nerolation/state-actor/internal/erigon/commitment"
	"github.com/nerolation/state-actor/internal/erigon/snap"
	"github.com/nerolation/state-actor/internal/streamsort"
)

// numRanges is the depth of the tiered LSM pyramid layout we emit.
// Four non-overlapping power-of-two-aligned step ranges (rangeIdx 0..3) —
// see ranges[] below. Spec entries pin to rangeIdx=0 (L4 deepest cold
// storage); autofill fills the pyramid TOP DOWN by byte-count
// milestones — rangeIdx=3 (L1, top) fills first; overflow past 11.5
// GiB spills DOWNWARD into L2, L3, and finally L4 alongside the spec.
const numRanges = 4

// ranges is the snapshot-file layout state-actor emits. Each range is
// power-of-two-aligned per upstream Erigon's merge invariant at
// db/state/merge.go::calculateMergeStartTxNum (a file's startTxNum
// MUST be a multiple of (endStep & -endStep)). Erigon's reader sorts
// visibleFiles ascending by endTxNum (db/state/dirty_files.go:208-213)
// then walks DESCENDING (db/state/domain.go:1318) — so the file with
// the HIGHEST endStep is probed FIRST:
//
//	rangeIdx=3 ([448, 449),   1 step ) — L1 TOP    — probed FIRST
//	                                     (1.5 GiB autofill + commitment branches)
//	rangeIdx=2 ([384, 448),  64 steps) — L2        — probed 2nd
//	                                     (3 GiB autofill)
//	rangeIdx=1 ([256, 384), 128 steps) — L3        — probed 3rd
//	                                     (7 GiB autofill)
//	rangeIdx=0 ([  0, 256), 256 steps) — L4 BOTTOM — probed LAST
//	                                     (13.5 GiB autofill overflow + ALL spec)
//
// A cold spec-key lookup forces 3 existence-filter probes on the upper
// files (L1/L2/L3, populated by autofill with ~1% FPR) + 1 BT walk on
// the deep rangeIdx=0 file. Autofill-key cost averages ~3.3 probes
// (geometric weighting). The size pyramid (1.5 / 3 / 7 / 13.5 GiB)
// matches mainnet Erigon's steady-state shape post-merger collapse to
// power-of-two-aligned super-blocks capped at StepsInFrozenFile
// (default 256 per db/config3/config3.go:34; clamp enforced at
// db/state/aggregator.go:1744 + db/state/merge.go:102).
var ranges = [numRanges]snap.StepRange{
	{From: 0, To: 256},   // rangeIdx=0 — L4 deepest (spec + autofill overflow)
	{From: 256, To: 384}, // rangeIdx=1 — L3
	{From: 384, To: 448}, // rangeIdx=2 — L2
	{From: 448, To: 449}, // rangeIdx=3 — L1 top (commitment branches live here)
}

// erigonWorkers is the size of the Phase 1 autofill encode-worker pool.
// Defaults to min(NumCPU, 8) to match the proven cap from reth, besu,
// and nethermind (client/reth/spec_storage_streaming_cgo.go:95-104,
// client/besu/state_writer_cgo.go:298, client/nethermind/phase0_cgo.go).
// Tests override via setErigonWorkers.
var erigonWorkers = func() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}()

// setErigonWorkers swaps erigonWorkers for the duration of a test.
// The returned function restores the previous value.
func setErigonWorkers(n int) (restore func()) {
	prev := erigonWorkers
	erigonWorkers = n
	return func() { erigonWorkers = prev }
}

// entityWork is one alloc entry queued for an encode-worker. The main
// goroutine fills these and sends them on entityCh; workers consume
// and call encodeEntity. rangeIdx is decided on the main thread BEFORE
// send so routing stays deterministic regardless of worker scheduling
// (cross-client invariance).
type entityWork struct {
	addr     common.Address
	entry    *allocAccount
	rangeIdx uint8
}

// domainWrite is one (key, value) tuple ready to be Put into a specific
// streamsort. Sent from encode workers to per-domain writer goroutines.
type domainWrite struct {
	key   []byte
	value []byte
}

// perDomainChans is the 4 per-domain channels — one dedicated writer
// goroutine drains each. The 4096 buffer absorbs a single
// 615-slot-contract burst without blocking the encoder.
type perDomainChans struct {
	accounts chan domainWrite
	storage  chan domainWrite
	code     chan domainWrite
	commitIn chan domainWrite
}

// rangeCounts tracks per-(domain, rangeIdx) entry counts. Workers
// increment via atomic.AddUint64 — multiple workers may emit for the
// same range concurrently. Phase 5 reads via plain access AFTER all
// workers have drained (sync.WaitGroup.Wait provides the
// happens-before barrier).
type rangeCounts struct {
	accounts [numRanges]uint64
	storage  [numRanges]uint64
	code     [numRanges]uint64
}

// writeSnapshots is the multi-range streaming snapshot orchestrator
// with a Phase 1 channel-pipeline encode-worker pool and Phase 3
// WaitGroup fan-out across the 12 (range, domain) tuples.
//
// Memory bounded by streamsort working set (~4 stores × ~256 MiB
// memtable = ~1 GB) regardless of --target-size. Disk usage scales
// with the autofill payload (~20 GB at 25 GB target), landing under
// <cfg.DBPath>/streamsort-<domain>/ on the bind-mounted filesystem.
//
// Phases:
//  1. Open 4 streamsorts (accounts/storage/code/commitmentInputs)
//     under cfg.DBPath.
//  2. Spawn N encode workers (N = erigonWorkers) + 4 per-domain
//     writer goroutines (one per streamsort).
//  3. Main goroutine drains foundational.Spec + runs the AutoFill
//     RNG loop, sending entityWork records to entityCh. Workers
//     consume, encode SerialiseV3 + commitment-update, emit
//     (key, value) tuples to per-domain channels. Writer goroutines
//     drain channels into streamsort.Put. CPU encode and disk I/O
//     overlap; RNG order stays on main thread for cross-client
//     invariance.
//  4. Run HPH commitment over the commitmentInputStore (disk-backed
//     ctx.Account/Storage callbacks via streamsort.Get).
//  5a. Marshal branches map into branchesStore (sequential — small).
//  5b. Multi-range write loop — 4 ranges × 3 domains FAN OUT into
//      goroutines (semaphore-bounded at NumCPU). Commitment phase
//      stays serial (small + shared branchesStore).
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

	// commitmentInputStore takes the heavy random-read workload: the
	// 16 ConcurrentPatriciaHashed workers call subtreeCtx.Storage /
	// Account on every leaf (~344M Get calls at SPEC_TARGET_GB=1 for
	// a 12 GiB store, ~1.7B at 25 GiB). With the default 8 MiB block
	// cache the LSM SSTs miss on most reads. Bump the cache here so
	// the Pebble block cache holds a non-trivial fraction of the
	// working set; benchmarking showed 50+ min Phase 2 wall at default.
	// Tunable via the environment-derived future Options if needed;
	// 4 GiB is the floor that the bench host (240 GiB RAM) can spare.
	commitmentInputStore, err := streamsort.NewWithOptions(cfg.DBPath, streamsort.Options{
		BlockCacheBytes: 4 << 30,
	})
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: open commitmentInput streamsort: %w", err)
	}
	defer commitmentInputStore.Close()

	// Per-rangeIdx running byte counter for autofill milestone routing.
	// Main-goroutine-only — never touched by workers.
	var bytesIn [numRanges]uint64

	// counts: atomic per-(domain, rangeIdx) increments by workers.
	var counts rangeCounts

	// -- Step 2: spawn worker pool + per-domain writer goroutines.
	pipelineCtx, cancelPipeline := context.WithCancel(ctx)
	defer cancelPipeline()

	chans := &perDomainChans{
		accounts: make(chan domainWrite, 4096),
		storage:  make(chan domainWrite, 4096),
		code:     make(chan domainWrite, 4096),
		commitIn: make(chan domainWrite, 4096),
	}

	N := erigonWorkers
	entityCh := make(chan entityWork, 2*N)
	encodeErrCh := make(chan error, N)
	writerErrCh := make(chan error, 4)

	var encodeWg sync.WaitGroup
	encodeWg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer encodeWg.Done()
			if err := autofillEncodeWorker(pipelineCtx, entityCh, chans, &counts); err != nil {
				select {
				case encodeErrCh <- err:
				default:
				}
				cancelPipeline()
			}
		}()
	}

	var writerWg sync.WaitGroup
	writerWg.Add(4)
	go runDomainWriter(pipelineCtx, &writerWg, writerErrCh, cancelPipeline, chans.accounts, accountsStore, "accounts")
	go runDomainWriter(pipelineCtx, &writerWg, writerErrCh, cancelPipeline, chans.storage, storageStore, "storage")
	go runDomainWriter(pipelineCtx, &writerWg, writerErrCh, cancelPipeline, chans.code, codeStore, "code")
	go runDomainWriter(pipelineCtx, &writerWg, writerErrCh, cancelPipeline, chans.commitIn, commitmentInputStore, "commitmentInput")

	// sendEntity exits early if the pipeline already cancelled (a worker
	// or writer failed mid-stream).
	sendEntity := func(ew entityWork) error {
		select {
		case entityCh <- ew:
			return nil
		case <-pipelineCtx.Done():
			return pipelineCtx.Err()
		}
	}

	// -- Step 3a: drain foundational.Spec → entityCh at rangeIdx=0.
	// Also build genesisAddrs for the dedup-redraw loop (matches geth
	// byte-for-byte: dedup against spec only, never against other
	// autofill draws — see client/geth/state_writer.go:120-148).
	genesisAddrs := make(map[common.Address]struct{}, len(foundational.Spec))
	var pipelineErr error
	for addr, entry := range foundational.Spec {
		genesisAddrs[addr] = struct{}{}
		if err := sendEntity(entityWork{addr: addr, entry: entry, rangeIdx: 0}); err != nil {
			pipelineErr = err
			break
		}
	}

	// -- Step 3b: AutoFill RNG loop — STREAMING (main thread only).
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
	if pipelineErr == nil && cfg.AutoFill != nil {
		rng := mrand.New(mrand.NewSource(cfg.Seed))
		milestones := computeAutofillMilestones(cfg.TargetSize)
		for i := 0; i < cfg.AutoFill.NumEOAs && pipelineErr == nil; i++ {
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
			if err := sendEntity(entityWork{addr: acc.Address, entry: entry, rangeIdx: rangeIdx}); err != nil {
				pipelineErr = err
				break
			}
			// Conservative per-entity estimate — SerialiseV3 averages ~30 B for an EOA.
			bytesIn[rangeIdx] += 32
			stats.AccountsCreated++
		}
		for i := 0; i < cfg.AutoFill.NumContracts && pipelineErr == nil; i++ {
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
			if len(c.Storage) > 0 {
				entry.Storage = make(map[common.Hash]common.Hash, len(c.Storage))
				for _, s := range c.Storage {
					entry.Storage[s.Key] = s.Value
				}
			}
			if err := sendEntity(entityWork{addr: c.Address, entry: entry, rangeIdx: rangeIdx}); err != nil {
				pipelineErr = err
				break
			}
			stats.StorageSlotsCreated += len(c.Storage)
			stats.StorageBytes += uint64(len(c.Storage)) * 64
			// Conservative per-entity estimate covering account + code + slots.
			bytesIn[rangeIdx] += uint64(len(entry.Code)) + 32 + uint64(len(c.Storage))*64
			stats.ContractsCreated++
		}
	}
	stats.TotalBytes = stats.StorageBytes + stats.CodeBytes

	// Close entityCh → workers drain remaining items then exit.
	close(entityCh)
	encodeWg.Wait()

	// Close per-domain channels → writers drain then exit.
	close(chans.accounts)
	close(chans.storage)
	close(chans.code)
	close(chans.commitIn)
	writerWg.Wait()

	// Surface the first encountered error. Worker errors take priority
	// over the local pipelineErr (which is just ctx.Canceled in the
	// cancel path; the real cause is in encodeErrCh or writerErrCh).
	select {
	case err := <-encodeErrCh:
		return common.Hash{}, fmt.Errorf("writeSnapshots: encode worker: %w", err)
	default:
	}
	select {
	case err := <-writerErrCh:
		return common.Hash{}, fmt.Errorf("writeSnapshots: domain writer: %w", err)
	default:
	}
	if pipelineErr != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: pipeline: %w", pipelineErr)
	}

	// Finalize the 4 streamsorts. After this they enter their FINALIZED
	// state and Get/Iterate become safe for concurrent callers — Phase 2
	// (ConcurrentPatriciaHashed, 16 workers) does concurrent
	// commitmentInputStore.Get; Phase 5b (12-way WriteDomain fan-out)
	// does concurrent accountsStore/storageStore/codeStore.Iterate.
	// Without Finalize, the post-Phase-1 batch flush would race against
	// the next call's batch.Commit and trigger pebble: batch already
	// committing. Finalize moves the flush to a one-shot mutex-serialized
	// transition so the read path is lock-free.
	for _, fz := range []struct {
		name  string
		store *streamsort.Store
	}{
		{"accounts", accountsStore},
		{"storage", storageStore},
		{"code", codeStore},
		{"commitmentInput", commitmentInputStore},
	} {
		if err := fz.store.Finalize(); err != nil {
			return common.Hash{}, fmt.Errorf("writeSnapshots: Finalize %s streamsort: %w", fz.name, err)
		}
	}

	// -- Step 4: HPH commitment walk over commitmentInputStore.
	// ctx.Account/Storage callbacks read from streamsort.Get
	// (disk-backed). branches map stays in memory (bounded).
	result, err := internalcommitment.ComputeGenesisRoot(commitmentInputStore)
	if err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: ComputeGenesisRoot: %w", err)
	}
	// KeyCommitmentState's encoded txNum MUST match the newest commitment
	// file's startStep, or Erigon's SeekCommitment treats the snapshot
	// commitment state as out-of-date and refuses to build blocks:
	//
	//   [2/3 BuilderExecution] seek commitment failed:
	//     "commitment" state out of date: step 0, expected step 448
	//
	// Erigon decodes step = txNum / stepSize from the encoded value
	// (commitmentdb/commitment_context.go::DecodeTxBlockNums), then
	// cross-checks against the file's startStep (= ranges[numRanges-1].From).
	// We were encoding txNum=0 → decoded step=0, mismatching the
	// newest file's step 448 (= ranges[3].From with stepSize=390625).
	//
	// Fix: encode the first txNum of the newest file's step range.
	// blockNum stays 0 — we're still at genesis.
	newestStartTxNum := uint64(ranges[numRanges-1].From) * internalerigon.StepSize
	keyStateValue, err := internalcommitment.EncodeKeyCommitmentStateValue(newestStartTxNum, 0, result.HPHState)
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

	// -- Step 5b: snap.NewWriter + parallel multi-range emit.
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

	// Fan out 12 independent WriteDomain calls (4 ranges × 3 domains).
	// Each tuple has its own .kv + accessors output, its own streamsort
	// prefix-scan input, and no shared mutable state — safe to run in
	// parallel. Semaphore-bound at NumCPU keeps seg.Compressor pressure
	// realistic on small hosts.
	type domainSpec struct {
		domain snap.Domain
		store  *streamsort.Store
		counts *[numRanges]uint64
	}
	domainSpecs := []domainSpec{
		{snap.DomainAccounts, accountsStore, &counts.accounts},
		{snap.DomainStorage, storageStore, &counts.storage},
		{snap.DomainCode, codeStore, &counts.code},
	}
	emitErrCh := make(chan error, numRanges*len(domainSpecs))
	var emitWg sync.WaitGroup
	sem := make(chan struct{}, runtime.NumCPU())
	for r := uint8(0); r < numRanges; r++ {
		for _, ds := range domainSpecs {
			sem <- struct{}{}
			emitWg.Add(1)
			go func(r uint8, ds domainSpec) {
				defer func() { <-sem; emitWg.Done() }()
				count := ds.counts[r]
				if err := w.WriteDomain(ctx, ds.domain, ranges[r], count,
					snap.FromStreamsortRange(ds.store, r)); err != nil {
					select {
					case emitErrCh <- fmt.Errorf("WriteDomain(%v, range=%v): %w", ds.domain, ranges[r], err):
					default:
					}
				}
			}(r, ds)
		}
	}
	emitWg.Wait()
	select {
	case err := <-emitErrCh:
		return common.Hash{}, fmt.Errorf("writeSnapshots: %w", err)
	default:
	}

	// Commitment: branches + KeyCommitmentState in NEWEST range only.
	// Serial after the fan-out — commitment is small (~200 MB branches
	// at full bench scale) and the placeholders share branchesStore-
	// derived emptyKeyState, so parallelization gain is negligible.
	newestRange := ranges[numRanges-1]
	if err := snap.WriteCommitment(ctx, w, newestRange, keyStateValue, branchesStore, nBranches); err != nil {
		return common.Hash{}, fmt.Errorf("writeSnapshots: WriteCommitment(newest): %w", err)
	}
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
		fmt.Printf("client/erigon: wrote snapshots: spec=%d autofill_accounts=%d contracts=%d storage_slots=%d branches=%d workers=%d root=%s\n",
			len(foundational.Spec), stats.AccountsCreated, stats.ContractsCreated, stats.StorageSlotsCreated, nBranches, N, result.Root.Hex())
		fmt.Printf("client/erigon: per-range entry counts: accounts=%v storage=%v code=%v\n",
			counts.accounts, counts.storage, counts.code)
	}

	return result.Root, nil
}

// runDomainWriter is the goroutine entry-point for one per-domain
// writer. Drains `in` into `store` until either the channel closes
// (clean shutdown) or ctx cancels (error path). On error, pushes to
// errCh and triggers cancel.
func runDomainWriter(
	ctx context.Context,
	wg *sync.WaitGroup,
	errCh chan<- error,
	cancel context.CancelFunc,
	in <-chan domainWrite,
	store *streamsort.Store,
	label string,
) {
	defer wg.Done()
	if err := domainWriter(ctx, in, store, label); err != nil {
		select {
		case errCh <- err:
		default:
		}
		cancel()
	}
}

// autofillEncodeWorker consumes entityWork from entityCh, encodes
// each entry on a worker goroutine, and emits (key, value) tuples to
// the per-domain channels. Exits cleanly on entityCh close OR ctx
// cancellation.
func autofillEncodeWorker(
	ctx context.Context,
	in <-chan entityWork,
	out *perDomainChans,
	counts *rangeCounts,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ew, ok := <-in:
			if !ok {
				return nil
			}
			if err := encodeEntity(ctx, ew, out, counts); err != nil {
				return err
			}
		}
	}
}

// encodeEntity is the worker-side encode for one alloc entry. Emits:
//   - 1 account row → accounts channel (always)
//   - 1 code row → code channel (only if entity has bytecode)
//   - len(entry.Storage) storage rows → storage + commitIn channels
//   - 1 commitment-input row → commitIn channel (account-level update)
//
// All composite keys + encoded values are computed here (CPU-bound).
// The per-domain writer goroutines absorb the disk-bound Put.
func encodeEntity(
	ctx context.Context,
	ew entityWork,
	out *perDomainChans,
	counts *rangeCounts,
) error {
	addrComposite := make([]byte, 0, 1+20)
	addrComposite = append(addrComposite, ew.rangeIdx)
	addrComposite = append(addrComposite, ew.addr[:]...)

	// Accounts snapshot value: SerialiseV3.
	acct := account.Account{
		Nonce:    ew.entry.Nonce,
		CodeHash: account.EmptyCodeHash,
	}
	var balance *uint256.Int
	if ew.entry.Balance != nil {
		b, overflow := uint256.FromBig(ew.entry.Balance)
		if overflow {
			return fmt.Errorf("encodeEntity: balance overflow for %s", ew.addr.Hex())
		}
		acct.Balance = *b
		balance = b
	}
	if len(ew.entry.Code) > 0 {
		h := crypto.Keccak256Hash(ew.entry.Code)
		copy(acct.CodeHash[:], h[:])
	}
	if err := sendDomainWrite(ctx, out.accounts, domainWrite{key: addrComposite, value: account.SerialiseV3(acct)}); err != nil {
		return err
	}
	atomic.AddUint64(&counts.accounts[ew.rangeIdx], 1)

	// Code snapshot value: raw bytecode keyed by addr.
	if len(ew.entry.Code) > 0 {
		if err := sendDomainWrite(ctx, out.code, domainWrite{key: addrComposite, value: ew.entry.Code}); err != nil {
			return err
		}
		atomic.AddUint64(&counts.code[ew.rangeIdx], 1)
	}

	// Inline storage (foundational PreAlloc + contract autofill).
	for slot, value := range ew.entry.Storage {
		if err := encodeStorageSlot(ctx, ew.rangeIdx, ew.addr, slot, value, out, counts); err != nil {
			return err
		}
	}

	// Commitment input: account-level Update keyed by plain addr.
	commitBytes := internalcommitment.EncodeAccountUpdate(ew.entry.Nonce, balance, ew.entry.Code)
	addrKey := make([]byte, 20)
	copy(addrKey, ew.addr[:])
	return sendDomainWrite(ctx, out.commitIn, domainWrite{key: addrKey, value: commitBytes})
}

// encodeStorageSlot encodes one (addr, slot, value) tuple. Skip on
// all-zero value (Erigon's StorageDomain treats absent ≡ zero;
// storing zero is wrong).
func encodeStorageSlot(
	ctx context.Context,
	rangeIdx uint8,
	addr common.Address,
	slotKey common.Hash,
	slotValue common.Hash,
	out *perDomainChans,
	counts *rangeCounts,
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
	if err := sendDomainWrite(ctx, out.storage, domainWrite{key: composite, value: trimmed}); err != nil {
		return err
	}
	atomic.AddUint64(&counts.storage[rangeIdx], 1)

	// Plain key for commitment: addr || slot = 52 bytes (no rangeIdx).
	plainKey := make([]byte, 0, 20+32)
	plainKey = append(plainKey, addr[:]...)
	plainKey = append(plainKey, slotKey[:]...)
	commitBytes := internalcommitment.EncodeStorageUpdate(slotValue[:])
	return sendDomainWrite(ctx, out.commitIn, domainWrite{key: plainKey, value: commitBytes})
}

// sendDomainWrite is the cancel-aware channel send used by encoders.
func sendDomainWrite(ctx context.Context, ch chan<- domainWrite, dw domainWrite) error {
	select {
	case ch <- dw:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// domainWriter drains a single per-domain channel into the
// corresponding streamsort. streamsort is single-writer by design
// (internal/streamsort/streamsort.go) — having exactly one writer
// goroutine per domain is what makes the worker pool safe without a
// mutex on the streamsort itself.
func domainWriter(
	ctx context.Context,
	in <-chan domainWrite,
	store *streamsort.Store,
	label string,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case dw, ok := <-in:
			if !ok {
				return nil
			}
			if err := store.Put(dw.key, dw.value); err != nil {
				return fmt.Errorf("domainWriter[%s]: %w", label, err)
			}
		}
	}
}

// pickAutofillRange returns the rangeIdx for the next autofill entry.
// TOP first (rangeIdx=numRanges-1 = L1 fills first; spills DOWNWARD
// to L2, L3, and finally L4 as each upper range hits its milestone).
//
// Per the plan's tiered LSM pyramid (Layout C+): autofill represents
// "fresh" data that lives in the upper LSM layers (queried first by
// the daemon's newest→oldest reader walk). Spec data is "cold" —
// pinned to L4 (rangeIdx=0, deepest), reached only after existence-
// filter probes miss on L1/L2/L3. Once autofill exceeds the L3
// milestone (11.5 GiB cumulative across upper tiers), all remaining
// autofill OVERFLOW also routes to rangeIdx=0 — L4 holds spec PLUS
// the autofill tail.
//
// milestones[i] is the cumulative byte threshold AFTER which range
// (numRanges-1-i) is considered full and we spill to (numRanges-2-i).
// totalAuto sums bytesIn[1..numRanges-1] (excluding rangeIdx=0) so
// spec bytes never count against autofill thresholds, AND the overflow
// path (return 0) stays sticky once the L3 milestone is exceeded.
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
	return 0 // overflow past 11.5 GiB — pack into L4 alongside spec
}

// computeAutofillMilestones returns the absolute cumulative byte
// thresholds for the tiered LSM pyramid. Values are independent of
// --target-size: the tier SHAPE must match real-world Erigon's
// pyramid regardless of how much state the bench instructs us to
// generate.
//
//	milestones[0] =  1.5 GiB — L1 (rangeIdx=3) fills 0    → 1.5  GiB
//	milestones[1] =  4.5 GiB — L2 (rangeIdx=2) fills 1.5  → 4.5  GiB
//	milestones[2] = 11.5 GiB — L3 (rangeIdx=1) fills 4.5  → 11.5 GiB
//	overflow      → L4 (rangeIdx=0) — alongside spec
//
// For SPEC_TARGET_GB=1, only L1 fills (1 GB < 1.5 GiB cap) and
// L2/L3/L4 receive empty placeholder files. For SPEC_TARGET_GB=25,
// all four tiers fill in the 1.5 / 3 / 7 / 13.5 GiB pattern
// documented in plans/so-what-we-have-enumerated-lantern.md.
// The targetSize parameter is preserved for callsite signature
// stability but is intentionally unused.
func computeAutofillMilestones(targetSize uint64) [numRanges - 1]uint64 {
	_ = targetSize
	const GiB = uint64(1024) * 1024 * 1024
	return [numRanges - 1]uint64{
		3 * GiB / 2,  //  1.5 GiB — L1 cap
		9 * GiB / 2,  //  4.5 GiB — L2 cap
		23 * GiB / 2, // 11.5 GiB — L3 cap (overflow → L4)
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
