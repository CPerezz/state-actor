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

	"github.com/ethereum/state-actor/internal/streamsort"
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
	Root     gethcommon.Hash
	HPHState []byte
	// BranchCount is the number of distinct branch-node prefixes the walk
	// produced (== the old len(BranchNodes)). Production reads it as the
	// commitment-domain key count for WriteCommitment; the branch bytes
	// themselves are streamed straight into the caller-supplied branchesOut.
	BranchCount uint64
	// BranchNodes is populated ONLY by ComputeGenesisRootFromAccounts (the
	// small-input test / H4-invariance wrapper) for byte-level determinism
	// assertions. Production ComputeGenesisRoot leaves it nil — branches go
	// to branchesOut on disk, never a RAM map.
	BranchNodes map[string][]byte
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

// commitmentChunkKeys bounds how many keys each incremental commitment
// chunk Touches before a Process flush (A0). One chunk's Updates.keys dedup
// map + etl working set are ~O(commitmentChunkKeys) — independent of total
// alloc size. The default is large enough that typical allocs run as a
// SINGLE Process (the proven single-shot path); only bench-scale allocs
// chunk. setCommitmentChunkKeys overrides it for the chunked-vs-single test.
var commitmentChunkKeys = 50_000_000

// setCommitmentChunkKeys swaps commitmentChunkKeys for the duration of a
// test; the returned func restores the previous value.
func setCommitmentChunkKeys(n int) (restore func()) {
	prev := commitmentChunkKeys
	commitmentChunkKeys = n
	return func() { commitmentChunkKeys = prev }
}

// ComputeGenesisRoot runs Erigon's ConcurrentPatriciaHashed (16-nibble
// subtree-parallel HPH) against the commitmentInputStore's encoded Update
// payloads.
//
// The caller is responsible for having populated commitmentInputStore
// during the buildAllocMap/writeSnapshots streaming loop: every alloc
// account writes one entry keyed by 20-byte addr; every non-zero
// storage slot writes one entry keyed by addr||slot. Encoding is done
// via EncodeAccountUpdate / EncodeStorageUpdate above.
//
// Parallelism (Phase 2 worker pool):
//   - upstream's NewUpdates with SetConcurrentCommitment(true) routes
//     Touch...Direct into 16 per-nibble etl.Collector instances (sharded
//     by hashedKey[0]).
//   - NewConcurrentPatriciaHashed spawns 16 SubTrie HPH instances, one
//     per first-nibble, all mounted under a single root HPH.
//   - Process dispatches to ParallelHashSort which runs 16 worker
//     goroutines via errgroup. Each worker drains its nibble's
//     collector, calls followAndUpdate on its SubTrie, then foldNibble
//     merges the subtree's final cell into root.grid[0][nib] under
//     rootMu. After all 16 finish, root.fold() produces the final hash
//     from the 16 child cells via the standard foldBranch path.
//   - Subtree branch keys are disjoint by first nibble — workers write
//     PutBranch into the SHARED live branch store (a temp Pebble KV);
//     concurrent Set on disjoint prefixes has no overwrite hazard.
//
// Memory profile: Account/Storage lookups during the HPH walk hit Pebble
// via streamsort.Get (thread-safe; 16 concurrent readers fine). Branches
// no longer accumulate in a RAM map — the old mergedBranches was
// Θ(total-keys) (storage slots are hashed into the same unified trie), so
// at bench scale it was tens of GB. They now stream into the live
// branchStore (bounded by its Pebble memtable+cache) and are dumped into
// the caller's write-once branchesOut at the end.
//
// branchesOut is the write-once streamsort WriteCommitment will consume;
// ComputeGenesisRoot Puts the sorted branches into it but does NOT
// Finalize (WriteCommitment appends KeyCommitmentState then Finalizes).
//
// tmpDir is the on-disk scratch directory for the upstream etl.Collector
// spill (the per-nibble (hashedKey, plainKey) runs). It MUST point at real
// bind-mounted disk (e.g. cfg.DBPath): the previous "" passed os.TempDir(),
// which on bench hosts is tmpfs (RAM-backed), so the ~28 GB of touched-key
// spill was actually resident memory. The path is pure scratch — it cannot
// affect the sort order, the hashing, or the computed root.
func ComputeGenesisRoot(commitmentInputStore, branchesOut *streamsort.Store, tmpDir string) (Result, error) {
	// Live read-write branch sink shared by all 16 nibble-disjoint workers
	// + the root ctx. Replaces the in-memory mergedBranches map. Created
	// under tmpDir (real disk).
	branches, err := newBranchStore(tmpDir)
	if err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: branch store: %w", err)
	}
	defer branches.close()

	// factory yields a subtreeCtx bound to the SHARED live branch store.
	// Workers write disjoint first-nibble prefixes, so concurrent PutBranch
	// (pebble Set) has no key-overwrite hazard and needs no per-worker merge.
	factory := func() (erigoncommitment.PatriciaContext, func()) {
		return &subtreeCtx{commitmentInputStore: commitmentInputStore, branches: branches}, func() {}
	}

	// rootCtx shares the same store; the root-level PutBranch from foldBranch
	// (and the ApplyAndClearInlineDeferredUpdates flush below) lands in it.
	rootCtx, _ := factory()

	// A0 — chunked Touch+Process. Build the trie INCREMENTALLY in bounded
	// chunks of commitmentChunkKeys. Each chunk gets a FRESH Updates, so the
	// upstream Updates.keys dedup map (~100 B/unique-key) AND the per-nibble
	// etl working set stay bounded by the chunk size instead of growing to
	// O(total-keys) — tens of GB across a 100 GB alloc if done one-shot. The
	// pph is REUSED across chunks (no Reset), so the trie accumulates;
	// ctx.Branch (the live branchStore) feeds each chunk the branches written
	// by earlier chunks. This is upstream's incremental per-block model —
	// CanDoConcurrentNext (hex_concurrent_patricia_hashed.go:330) exists
	// precisely because Process is meant to be called repeatedly.
	//
	// SAFETY DEFAULT: commitmentChunkKeys is large, so any alloc at or below
	// it runs as a SINGLE Process — byte-identical to the proven single-shot
	// path (the existing golden/H4 tests exercise exactly this). Multi-chunk
	// only engages at bench scale; TestComputeGenesisRoot_ChunkedVsSingle
	// pins that a forced-small chunk size yields the SAME root as one shot.
	//
	// VALIDATION PENDING ON BENCH (cgo tests don't link on macOS, see plan
	// Tier 0/3): the incremental-concurrent multi-chunk path must be
	// confirmed against golden roots + cross-verify before it is relied on.
	// Single-chunk (the production default for sub-chunk allocs) is the
	// already-validated A2 behaviour.
	hph := erigoncommitment.NewHexPatriciaHashed(20 /* accountKeyLen */, rootCtx)
	pph := erigoncommitment.NewConcurrentPatriciaHashed(hph, rootCtx)
	defer pph.Close()

	var (
		rootBytes    []byte
		upds         *erigoncommitment.Updates
		chunkKeys    int
		processedAny bool
		placeholder  erigoncommitment.Update
	)
	newChunk := func() {
		// ModeDirect + concurrent up-front, as the single-shot path always
		// did (ParallelHashSort only requires mode==ModeDirect &&
		// sortPerNibble==true; the CanDoConcurrentNext gate is a hint).
		upds = erigoncommitment.NewUpdates(erigoncommitment.ModeDirect, tmpDir, erigoncommitment.KeyToHexNibbleHash)
		upds.SetConcurrentCommitment(true)
		chunkKeys = 0
	}
	processChunk := func() error {
		if upds == nil {
			return nil
		}
		// Skip an empty TRAILING chunk (total keys a multiple of the chunk
		// size), but DO process an empty FIRST chunk so an empty alloc still
		// yields the empty-trie root.
		if chunkKeys == 0 && processedAny {
			upds = nil
			return nil
		}
		rb, err := pph.Process(context.Background(), upds, "state-actor-genesis", nil,
			erigoncommitment.WarmupConfig{CtxFactory: factory})
		if err != nil {
			return fmt.Errorf("Process: %w", err)
		}
		rootBytes = rb
		// Flush this chunk's root-level deferred branch updates into the live
		// store. ParallelHashSort returns rootHash WITHOUT flushing the root
		// HPH's deferred branch (SpawnSubTrie disables defer only for the
		// per-nibble mounts, not the root), so without this the root-level
		// (empty-prefix, compact-key 0x10) branch is missing → divergent root
		// + a daemon FCU panic on SeekCommitment. Doing it per chunk also
		// makes the root branch available to the next chunk's reads, and for
		// chunkKeys==1-chunk it is exactly the single-shot flush.
		if err := pph.RootTrie().ApplyAndClearInlineDeferredUpdates(); err != nil {
			return fmt.Errorf("ApplyAndClearInlineDeferredUpdates: %w", err)
		}
		processedAny = true
		upds = nil
		return nil
	}

	// Walker: ModeDirect TouchPlainKeyDirect records (hashedKey, plainKey)
	// into the per-nibble etl collector + the dedup map; the *Update arg is
	// discarded (HPH re-fetches via ctx.Account/Storage during Process).
	newChunk()
	if err := commitmentInputStore.Iterate(func(plainKey, _ []byte) error {
		upds.TouchPlainKeyDirect(string(plainKey), &placeholder)
		chunkKeys++
		if chunkKeys >= commitmentChunkKeys {
			if err := processChunk(); err != nil {
				return err
			}
			newChunk()
		}
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: iterate commitmentInputStore: %w", err)
	}
	if err := processChunk(); err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: %w", err)
	}

	if len(rootBytes) != 32 {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: unexpected root hash length %d", len(rootBytes))
	}
	var root gethcommon.Hash
	copy(root[:], rootBytes)

	// HPHState comes from the root trie after all subtrees have folded
	// back into root.grid[0]. RootTrie() returns the same root HPH we
	// constructed above.
	hphState, err := pph.RootTrie().EncodeCurrentState(nil)
	if err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: EncodeCurrentState: %w", err)
	}

	// Dump the branches (ascending prefix order) into the caller's
	// write-once commitment .kv streamsort, counting distinct prefixes for
	// WriteCommitment's keyCount. branchesOut stays WRITING.
	var branchCount uint64
	if err := branches.iterate(func(prefix, data []byte) error {
		branchCount++
		return branchesOut.Put(prefix, data)
	}); err != nil {
		return Result{}, fmt.Errorf("commitment.ComputeGenesisRoot: dump branches: %w", err)
	}

	return Result{Root: root, HPHState: hphState, BranchCount: branchCount}, nil
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
	// ComputeGenesisRoot requires its input streamsort to be Finalized
	// — Iterate and Get on the store both gate on the Finalize state
	// transition. The wrapper Puts everything here, so we Finalize
	// before delegating.
	if err := store.Finalize(); err != nil {
		return Result{}, fmt.Errorf("ComputeGenesisRootFromAccounts: Finalize: %w", err)
	}
	// Tests/H4-invariance use tiny inputs; "" (os.TempDir) is fine here.
	branchesOut, err := streamsort.New("")
	if err != nil {
		return Result{}, fmt.Errorf("ComputeGenesisRootFromAccounts: branchesOut streamsort.New: %w", err)
	}
	defer branchesOut.Close()

	res, err := ComputeGenesisRoot(store, branchesOut, "")
	if err != nil {
		return Result{}, err
	}

	// Collect the streamed branches into a map so tests can make byte-level
	// determinism + count assertions (production never does this — branches
	// stay on disk). Iterate auto-finalizes the WRITING branchesOut.
	branches := make(map[string][]byte)
	if err := branchesOut.Iterate(func(k, v []byte) error {
		branches[string(k)] = append([]byte(nil), v...)
		return nil
	}); err != nil {
		return Result{}, fmt.Errorf("ComputeGenesisRootFromAccounts: collect branches: %w", err)
	}
	res.BranchNodes = branches
	return res, nil
}

// subtreeCtx implements erigoncommitment.PatriciaContext over a
// streamsort-backed commitmentInputStore (random-access via Pebble,
// thread-safe read path) plus the SHARED live branch store.
//
// Lifecycle: one subtreeCtx per ConcurrentPatriciaHashed worker (16 of
// them inside ParallelHashSort) plus one for the root HPH. All of them
// point at the same *branchStore. Workers write disjoint first-nibble
// prefixes, so concurrent PutBranch (pebble Set) and Branch (pebble Get)
// are safe with no overwrite ambiguity — pebble's Set/Get are goroutine-
// safe. Branch reads back prior writes, which is a no-op for a single
// from-empty genesis Process (sorted single-pass never re-descends a
// folded prefix) and the read path that makes incremental/chunked
// commitment work.
type subtreeCtx struct {
	commitmentInputStore *streamsort.Store
	branches             *branchStore
}

func (c *subtreeCtx) Branch(prefix []byte) ([]byte, erigonkv.Step, error) {
	data, err := c.branches.get(prefix)
	if err != nil {
		return nil, 0, err
	}
	return data, 0, nil
}

func (c *subtreeCtx) PutBranch(prefix []byte, data []byte, prevData []byte) error {
	// pebble copies key+value internally, so no manual copy is needed.
	return c.branches.set(prefix, data)
}

func (c *subtreeCtx) Account(plainKey []byte) (*erigoncommitment.Update, error) {
	enc, err := c.commitmentInputStore.Get(plainKey)
	if err != nil {
		return nil, fmt.Errorf("commitment.subtreeCtx.Account: Get(%x): %w", plainKey, err)
	}
	if enc == nil {
		u := new(erigoncommitment.Update)
		u.Flags = erigoncommitment.DeleteUpdate
		return u, nil
	}
	var u erigoncommitment.Update
	pos, err := u.Decode(enc, 0)
	if err != nil {
		return nil, fmt.Errorf("commitment.subtreeCtx.Account: decode plainKey=%x: %w", plainKey, err)
	}
	if pos != len(enc) {
		return nil, fmt.Errorf("commitment.subtreeCtx.Account: trailing bytes after decode")
	}
	if u.Flags&erigoncommitment.StorageUpdate != 0 {
		return nil, errors.New("commitment.subtreeCtx.Account: read storage entry via Account()")
	}
	return &u, nil
}

func (c *subtreeCtx) Storage(plainKey []byte) (*erigoncommitment.Update, error) {
	enc, err := c.commitmentInputStore.Get(plainKey)
	if err != nil {
		return nil, fmt.Errorf("commitment.subtreeCtx.Storage: Get(%x): %w", plainKey, err)
	}
	if enc == nil {
		u := new(erigoncommitment.Update)
		u.Flags = erigoncommitment.DeleteUpdate
		return u, nil
	}
	var u erigoncommitment.Update
	pos, err := u.Decode(enc, 0)
	if err != nil {
		return nil, fmt.Errorf("commitment.subtreeCtx.Storage: decode plainKey=%x: %w", plainKey, err)
	}
	if pos != len(enc) {
		return nil, fmt.Errorf("commitment.subtreeCtx.Storage: trailing bytes after decode")
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
