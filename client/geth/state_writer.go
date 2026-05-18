package geth

import (
	"bytes"
	"context"
	"fmt"
	"log"
	mrand "math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
	"github.com/nerolation/state-actor/internal/sizecal"
	"github.com/nerolation/state-actor/internal/streamingtrie"
	"github.com/nerolation/state-actor/internal/streamsort"
)

const phase1FlushBytes = 64 * 1024 * 1024

// parallelKeccakThreshold is the slot count above which a contract's
// keccak hashing is parallelised across cores.
const parallelKeccakThreshold = 64

// maxPhase0Workers caps Phase 0's drain-and-compute parallelism. Matches
// reth's maxStreamSpecStorageWorkers — both clients use the same upper
// bound.
//
// Each worker owns a streamsort.Store with a 2 GiB MemTable cap plus
// Pebble's per-flush queue (MemTableStopWritesThreshold = 16). On the
// bloatnet workload at 32 workers, this OOM-killed state-actor at 127
// GiB anon-RSS on a 125 GiB box. 8 keeps the worst-case under ~32 GiB
// while still giving 5+ parallel bloated-EOA drains.
const maxPhase0Workers = 8

// scratchBatchFlushBytes is the per-worker batch flush threshold during
// Phase 0. Matches defaultFlushBytes in writer.go (64 MiB).
const scratchBatchFlushBytes = 64 * 1024 * 1024

// writeStateAndCollectRoot drives the two-phase MPT pipeline.
//
// Phase 0 streams each spec-PreAlloc entity's storage iter through
// streamingtrie, persisting per-slot snapshot rows + storage-trie nodes
// and splicing the computed root into cfg.GenesisAccounts[addr].Root.
//
// Phase 1 streams entitygen output (genesis-alloc, synthetic EOAs +
// contracts) into a streamsort.Store keyed by addrHash for Phase 2 to
// consume sorted. Phase 2 builds per-account storage tries, writes the
// production Pebble in keccak order, and feeds the outer account trie.
//
// Memory is bounded by O(max storage slots in any single contract).
func writeStateAndCollectRoot(
	ctx context.Context,
	cfg generator.Config,
	w *Writer,
) (common.Hash, *generator.Stats, error) {
	stats := &generator.Stats{}
	start := time.Now()

	sorter, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, nil, fmt.Errorf("geth: streamsort.New: %w", err)
	}
	defer sorter.Close()

	// hashToAddr lets Phase 2 look up cfg.GenesisAccounts[addr].Root
	// (set by Phase 0) when encoding spec-entity account leaves.
	hashToAddr := make(map[common.Hash]common.Address, len(cfg.GenesisAccounts))
	for addr := range cfg.GenesisAccounts {
		hashToAddr[crypto.Keccak256Hash(addr[:])] = addr
	}

	if err := runPhase0(ctx, cfg, w); err != nil {
		return common.Hash{}, nil, err
	}

	rng := mrand.New(mrand.NewSource(int64(cfg.Seed)))

	// Origin-scoped target-size: accumulate projected on-disk trie bytes
	// per entity using the same sizecal constants internal/specbuild uses.
	// Stops emission once projection reaches cfg.TargetSize, so Phase 2
	// processes every entity Phase 1 emits — no orphaned account-trie
	// state. Replaces the prior 5x raw-bytes safety cap; the dirSize
	// sampling that used to live in Phase 2 is gone.
	bAcct := sizecal.BytesPerAccount("")
	bSlot := sizecal.BytesPerSlot("")
	var projectedTrieBytes uint64
	targetReached := false
	addProjection := func(slotCount int) bool {
		projectedTrieBytes += bAcct + bSlot*uint64(slotCount)
		if cfg.TargetSize > 0 && projectedTrieBytes >= cfg.TargetSize {
			if cfg.Verbose && !targetReached {
				log.Printf("geth MPT Phase 1: projected trie %d MiB >= target %d MiB — stopping entity emission",
					projectedTrieBytes>>20, cfg.TargetSize>>20)
			}
			targetReached = true
			return true
		}
		return false
	}

	// genesisAddrs prevents synthetic RNG addresses from colliding with
	// pre-allocated genesis addresses.
	genesisAddrs := make(map[common.Address]struct{}, len(cfg.GenesisAccounts))

	for addr, acc := range cfg.GenesisAccounts {
		genesisAddrs[addr] = struct{}{}
		addrHash := crypto.Keccak256Hash(addr[:])

		var code []byte
		if c, ok := cfg.GenesisCode[addr]; ok {
			code = c
		}
		var slots []entityBlobSlot
		if storage, ok := cfg.GenesisStorage[addr]; ok {
			slots = make([]entityBlobSlot, 0, len(storage))
			for k, v := range storage {
				slots = append(slots, entityBlobSlot{Key: k, Value: v})
			}
			stats.StorageSlotsCreated += len(storage)
		}

		var blob []byte
		if len(code) == 0 && len(slots) == 0 {
			blob = encodeEntityEOA(acc.Nonce, acc.Balance)
			stats.AccountsCreated++
		} else {
			blob = encodeEntityContract(acc.Nonce, acc.Balance, code, slots)
			stats.ContractsCreated++
		}
		if err := sorter.Put(addrHash[:], blob); err != nil {
			return common.Hash{}, nil, fmt.Errorf("phase1 genesis alloc: %w", err)
		}
		addProjection(len(slots))
	}

	for i := 0; i < cfg.NumAccounts && !targetReached; i++ {
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, err
		}
		acc := entitygen.GenerateEOA(rng)
		for _, dup := genesisAddrs[acc.Address]; dup; {
			acc = entitygen.GenerateEOA(rng)
			_, dup = genesisAddrs[acc.Address]
		}
		blob := encodeEntityEOA(acc.StateAccount.Nonce, acc.StateAccount.Balance)
		if err := sorter.Put(acc.AddrHash[:], blob); err != nil {
			return common.Hash{}, nil, fmt.Errorf("phase1 EOA #%d: %w", i, err)
		}
		stats.AccountsCreated++
		if len(stats.SampleEOAs) < 3 {
			stats.SampleEOAs = append(stats.SampleEOAs, acc.Address)
		}
		addProjection(0)
	}

	codeSize := cfg.CodeSize
	if codeSize <= 0 {
		codeSize = 1024
	}
	for i := 0; i < cfg.NumContracts && !targetReached; i++ {
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, err
		}
		contract := entitygen.GenerateContractRoll(rng, cfg.Distribution, codeSize, cfg.MinSlots, cfg.MaxSlots)
		for _, dup := genesisAddrs[contract.Address]; dup; {
			contract = entitygen.GenerateContractRoll(rng, cfg.Distribution, codeSize, cfg.MinSlots, cfg.MaxSlots)
			_, dup = genesisAddrs[contract.Address]
		}

		// contract.Storage is sorted by raw Key; Phase 2 re-sorts by keccak(Key).
		slots := make([]entityBlobSlot, len(contract.Storage))
		for j, s := range contract.Storage {
			slots[j] = entityBlobSlot{Key: s.Key, Value: s.Value}
		}
		blob := encodeEntityContract(
			contract.StateAccount.Nonce,
			contract.StateAccount.Balance,
			contract.Code,
			slots,
		)
		if err := sorter.Put(contract.AddrHash[:], blob); err != nil {
			return common.Hash{}, nil, fmt.Errorf("phase1 contract #%d: %w", i, err)
		}
		stats.ContractsCreated++
		stats.StorageSlotsCreated += len(contract.Storage)
		if len(stats.SampleContracts) < 3 {
			stats.SampleContracts = append(stats.SampleContracts, contract.Address)
		}
		addProjection(len(contract.Storage))
	}

	stats.GenerationTime = time.Since(start)
	if cfg.Verbose {
		log.Printf("[geth MPT Phase 1] complete: %d accounts, %d contracts, %d slots in %v",
			stats.AccountsCreated, stats.ContractsCreated,
			stats.StorageSlotsCreated, stats.GenerationTime.Round(time.Millisecond))
	}

	phase2Start := time.Now()

	// Outer account trie nodes are persisted under TrieNodeAccountPrefix.
	// Geth's PathDB boot derives the trie root via
	// keccak256(rawdb.ReadAccountTrieNode(db, nil)); a missing root node
	// short-circuits to EmptyRootHash and fails snapshot consistency.
	var accountTrieErr error
	accountCb := func(path []byte, hash common.Hash, blob []byte) {
		if accountTrieErr != nil {
			return
		}
		// StackTrie's path/blob slices are volatile — copy before queuing.
		p := append([]byte(nil), path...)
		b := append([]byte(nil), blob...)
		key := append([]byte{}, rawdb.TrieNodeAccountPrefix...)
		key = append(key, p...)
		if err := w.PutTrieNode(key, b); err != nil {
			accountTrieErr = fmt.Errorf("geth: write account trie node: %w", err)
		}
	}
	accountTrie := trie.NewStackTrie(accountCb)

	codeSeen := make(map[common.Hash]struct{}, cfg.NumContracts)

	count := 0
	iterErr := sorter.Iterate(func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var addrHash common.Hash
		copy(addrHash[:], key)

		ent, err := decodeEntityBlob(value)
		if err != nil {
			return fmt.Errorf("phase2 decode at #%d: %w", count, err)
		}

		// Spec entities stream their storage in Phase 0 (ent.slots is
		// empty for them); pick up the pre-computed Root from
		// cfg.GenesisAccounts instead of recomputing.
		storageRoot, sortedSlotEntries, err := buildStorageTrie(w, addrHash, ent.slots)
		if err != nil {
			return fmt.Errorf("phase2 storage trie at #%d: %w", count, err)
		}
		if len(ent.slots) == 0 {
			if specAddr, ok := hashToAddr[addrHash]; ok {
				if acc := cfg.GenesisAccounts[specAddr]; acc != nil &&
					acc.Root != (common.Hash{}) {
					storageRoot = acc.Root
				}
			}
		}

		acc := types.StateAccount{
			Nonce:    ent.nonce,
			Balance:  ent.balance,
			Root:     storageRoot,
			CodeHash: types.EmptyCodeHash.Bytes(),
		}
		var codeHash common.Hash
		if len(ent.code) > 0 {
			codeHash = crypto.Keccak256Hash(ent.code)
			acc.CodeHash = codeHash.Bytes()
		}
		if err := w.WriteAccount(common.Address{}, addrHash, &acc, 0); err != nil {
			return fmt.Errorf("phase2 write account at #%d: %w", count, err)
		}
		for _, s := range sortedSlotEntries {
			if err := w.WriteStorageRLP(addrHash, s.slotHash, s.valueRLP); err != nil {
				return fmt.Errorf("phase2 write slot at #%d: %w", count, err)
			}
		}
		if len(ent.code) > 0 {
			if _, dup := codeSeen[codeHash]; !dup {
				if err := w.WriteCode(codeHash, ent.code); err != nil {
					return fmt.Errorf("phase2 write code at #%d: %w", count, err)
				}
				codeSeen[codeHash] = struct{}{}
			}
		}

		// MUST be full StateAccount RLP (not SlimAccountRLP) — geth's
		// trie reader expects a fixed 32-byte Root field.
		fullRLP, err := rlp.EncodeToBytes(&acc)
		if err != nil {
			return fmt.Errorf("phase2 encode account RLP at #%d: %w", count, err)
		}
		if err := accountTrie.Update(addrHash[:], fullRLP); err != nil {
			return fmt.Errorf("phase2 account trie update at #%d: %w", count, err)
		}

		count++
		return nil
	})
	if iterErr != nil {
		return common.Hash{}, nil, iterErr
	}
	if accountTrieErr != nil {
		return common.Hash{}, nil, accountTrieErr
	}

	stateRoot := accountTrie.Hash()
	// accountTrie.Hash() may emit final nodes through the callback.
	if accountTrieErr != nil {
		return common.Hash{}, nil, accountTrieErr
	}
	stats.StateRoot = stateRoot
	stats.DBWriteTime = time.Since(phase2Start)
	if cfg.Verbose {
		log.Printf("[geth MPT Phase 2] %d entities → root %s in %v",
			count, stateRoot.Hex(), stats.DBWriteTime.Round(time.Millisecond))
	}

	writerStats := w.Stats()
	stats.AccountBytes = writerStats.AccountBytes
	stats.StorageBytes = writerStats.StorageBytes
	stats.CodeBytes = writerStats.CodeBytes
	stats.TotalBytes = stats.AccountBytes + stats.StorageBytes + stats.CodeBytes

	return stateRoot, stats, nil
}

// dirSize returns the total bytes used by all regular files under
// path. Returns 0 + nil if path doesn't exist yet.
func dirSize(path string) (uint64, error) {
	var total uint64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

// sortedSlot is a (keccak(slotKey), RLP value) pair sorted by slotHash.
type sortedSlot struct {
	slotHash common.Hash
	valueRLP []byte
}

// buildStorageTrie hashes + sorts the slots, builds the per-account
// storage StackTrie (always emitting trie nodes via w.PutTrieNode —
// geth's PathDB requires them at boot), and returns (root, sortedEntries)
// for the caller to write the snapshot in keccak order. For ≥
// parallelKeccakThreshold slots, keccak hashing is parallelised.
func buildStorageTrie(
	w *Writer,
	accountHash common.Hash,
	slots []entityBlobSlot,
) (common.Hash, []sortedSlot, error) {
	if len(slots) == 0 {
		return types.EmptyRootHash, nil, nil
	}

	type kv struct {
		Key   common.Hash
		Hash  common.Hash
		Value common.Hash
	}
	hashed := make([]kv, len(slots))
	if len(slots) >= parallelKeccakThreshold {
		numWorkers := runtime.GOMAXPROCS(0)
		chunk := (len(slots) + numWorkers - 1) / numWorkers
		var wg sync.WaitGroup
		for w := 0; w < numWorkers; w++ {
			s := w * chunk
			e := s + chunk
			if s >= len(slots) {
				break
			}
			if e > len(slots) {
				e = len(slots)
			}
			wg.Add(1)
			go func(s, e int) {
				defer wg.Done()
				for i := s; i < e; i++ {
					hashed[i] = kv{
						Key:   slots[i].Key,
						Hash:  crypto.Keccak256Hash(slots[i].Key[:]),
						Value: slots[i].Value,
					}
				}
			}(s, e)
		}
		wg.Wait()
	} else {
		for i, s := range slots {
			hashed[i] = kv{
				Key:   s.Key,
				Hash:  crypto.Keccak256Hash(s.Key[:]),
				Value: s.Value,
			}
		}
	}
	sort.Slice(hashed, func(i, j int) bool {
		return bytes.Compare(hashed[i].Hash[:], hashed[j].Hash[:]) < 0
	})

	acctHash := accountHash // capture for closure
	var storageTrieErr error
	storageCb := func(path []byte, hash common.Hash, blob []byte) {
		if storageTrieErr != nil {
			return
		}
		p := append([]byte(nil), path...)
		b := append([]byte(nil), blob...)
		key := make([]byte, 0, len(rawdb.TrieNodeStoragePrefix)+common.HashLength+len(p))
		key = append(key, rawdb.TrieNodeStoragePrefix...)
		key = append(key, acctHash[:]...)
		key = append(key, p...)
		if err := w.PutTrieNode(key, b); err != nil {
			storageTrieErr = fmt.Errorf("geth: write storage trie node: %w", err)
		}
	}
	storageTrie := trie.NewStackTrie(storageCb)
	out := make([]sortedSlot, 0, len(hashed))
	for _, h := range hashed {
		valueRLP, err := encodeStorageValue(h.Value)
		if err != nil {
			return common.Hash{}, nil, err
		}
		if err := storageTrie.Update(h.Hash[:], valueRLP); err != nil {
			return common.Hash{}, nil, err
		}
		out = append(out, sortedSlot{slotHash: h.Hash, valueRLP: valueRLP})
	}
	root := storageTrie.Hash()
	if storageTrieErr != nil {
		return common.Hash{}, nil, storageTrieErr
	}
	return root, out, nil
}

// gethStorageHashBuilder adapts trie.StackTrie to
// streamingtrie.HashBuilder. The StackTrie callback persists each
// storage-trie node under TrieNodeStoragePrefix + addrHash + path
// (required by geth's PathDB). PutTrieNode failures are captured in
// err (sticky) and surfaced via AddLeaf / Root.
type gethStorageHashBuilder struct {
	t        *trie.StackTrie
	w        *Writer
	addrHash common.Hash
	err      error
}

func newGethStorageHashBuilder(w *Writer, addrHash common.Hash) *gethStorageHashBuilder {
	hb := &gethStorageHashBuilder{w: w, addrHash: addrHash}
	cb := func(path []byte, hash common.Hash, blob []byte) {
		if hb.err != nil {
			return
		}
		p := append([]byte(nil), path...)
		b := append([]byte(nil), blob...)
		key := make([]byte, 0, len(rawdb.TrieNodeStoragePrefix)+common.HashLength+len(p))
		key = append(key, rawdb.TrieNodeStoragePrefix...)
		key = append(key, hb.addrHash[:]...)
		key = append(key, p...)
		if err := hb.w.PutTrieNode(key, b); err != nil {
			hb.err = fmt.Errorf("geth: write storage trie node for %s: %w", hb.addrHash.Hex(), err)
		}
	}
	hb.t = trie.NewStackTrie(cb)
	return hb
}

// newScratchGethStorageHashBuilder builds a HashBuilder that writes
// storage-trie nodes through a caller-supplied *pebble.Batch instead of
// through the shared w.batch+batchMu hot path. Used by Phase 0's worker
// pool so each worker writes without contending on the shared mutex.
//
// The returned builder reuses gethStorageHashBuilder; only the callback
// differs. err remains per-instance (per-worker).
func newScratchGethStorageHashBuilder(batch *pebble.Batch, addrHash common.Hash) *gethStorageHashBuilder {
	hb := &gethStorageHashBuilder{addrHash: addrHash}
	cb := func(path []byte, hash common.Hash, blob []byte) {
		if hb.err != nil {
			return
		}
		p := append([]byte(nil), path...)
		b := append([]byte(nil), blob...)
		key := make([]byte, 0, len(rawdb.TrieNodeStoragePrefix)+common.HashLength+len(p))
		key = append(key, rawdb.TrieNodeStoragePrefix...)
		key = append(key, hb.addrHash[:]...)
		key = append(key, p...)
		if err := batch.Set(key, b, nil); err != nil {
			hb.err = fmt.Errorf("geth: scratch trie node Set for %s: %w", hb.addrHash.Hex(), err)
		}
	}
	hb.t = trie.NewStackTrie(cb)
	return hb
}

func (g *gethStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	if g.err != nil {
		return g.err
	}
	return g.t.Update(keyHash[:], valueRLP)
}

func (g *gethStorageHashBuilder) Root() (common.Hash, error) {
	root := g.t.Hash() // triggers final node-completion callbacks
	if g.err != nil {
		return common.Hash{}, g.err
	}
	return root, nil
}

// runPhase0 drives the spec-PreAlloc streaming-trie phase in parallel.
//
// For every cfg.PreAlloc entity with pe.Storage != nil, a worker:
//   1. Drains the entity's storage iter into a per-call streamsort.Store
//      (rooted under cfg.DBPath so the temp lives on the production
//      filesystem, not the docker container's /tmp overlay).
//   2. Iterates the sorted store, building a per-account storage MPT
//      via a scratch-batch HashBuilder. Per-slot snapshot rows and
//      per-storage-trie-node writes go to the worker's own *pebble.Batch
//      — no contention on the shared w.batch+batchMu hot path.
//   3. Flushes its batch via w.CommitScratchBatch when it crosses
//      scratchBatchFlushBytes, and one final flush at worker exit.
//   4. Reports the computed storage root through preparedCh to the main
//      goroutine, which assigns it to cfg.GenesisAccounts[addr].Root.
//
// Root assignment is single-writer on the main goroutine; workers
// never touch cfg.GenesisAccounts. Across-entity Pebble keyspaces are
// disjoint (every key is addrHash-prefixed), so parallel db.Apply
// calls don't conflict at the storage layer; they coalesce in
// Pebble's WAL pipeline.
func runPhase0(ctx context.Context, cfg generator.Config, w *Writer) error {
	indices := make([]int, 0, len(cfg.PreAlloc))
	for i := range cfg.PreAlloc {
		if cfg.PreAlloc[i].Storage != nil {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return nil
	}

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > maxPhase0Workers {
		workers = maxPhase0Workers
	}
	if workers > len(indices) {
		workers = len(indices)
	}

	type preparedEntity struct {
		idx  int
		addr common.Address
		root common.Hash
	}

	drainCtx, cancelDrain := context.WithCancelCause(ctx)
	defer cancelDrain(nil)

	drainCh := make(chan int, workers*2)
	preparedCh := make(chan preparedEntity, workers*4)

	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			scratch := w.NewScratchBatch()
			defer func() {
				// Final flush on exit (success or cancel). Errors during
				// final flush surface through drainCtx — if we got here on
				// cancel they're best-effort. Sync=false matches the
				// per-entity path.
				if scratch.Len() > 0 {
					if err := w.CommitScratchBatch(scratch); err != nil {
						cancelDrain(fmt.Errorf("geth phase0: final flush: %w", err))
					}
				} else {
					_ = scratch.Close()
				}
			}()

			for i := range drainCh {
				if drainCtx.Err() != nil {
					return
				}
				pe := &cfg.PreAlloc[i]
				addr := pe.Address
				addrHash := crypto.Keccak256Hash(addr[:])

				d, err := streamingtrie.Drain(cfg.DBPath, pe.Storage)
				if err != nil {
					cancelDrain(fmt.Errorf("geth: drain spec storage[%d] %s: %w", i, addr.Hex(), err))
					return
				}
				sink := func(keyHash, _rawKey, value common.Hash) error {
					valueRLP, encErr := encodeStorageValue(value)
					if encErr != nil {
						return encErr
					}
					key := storageSnapshotKey(addrHash, keyHash)
					return scratch.Set(key, valueRLP, nil)
				}
				hb := newScratchGethStorageHashBuilder(scratch, addrHash)
				root, err := d.IterateRoot(hb, sink)
				d.Close()
				if err != nil {
					cancelDrain(fmt.Errorf("geth: iterate spec storage[%d] %s: %w", i, addr.Hex(), err))
					return
				}
				// Flush mid-stream to bound RAM. Crossing the threshold
				// produces an Apply + fresh Batch; on commit failure the
				// worker bails and signals via drainCtx.
				if scratch.Len() >= scratchBatchFlushBytes {
					if err := w.CommitScratchBatch(scratch); err != nil {
						cancelDrain(fmt.Errorf("geth phase0: scratch flush: %w", err))
						return
					}
					scratch = w.NewScratchBatch()
				}
				select {
				case preparedCh <- preparedEntity{idx: i, addr: addr, root: root}:
				case <-drainCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(drainCh)
		for _, i := range indices {
			select {
			case drainCh <- i:
			case <-drainCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(preparedCh)
	}()

	for entry := range preparedCh {
		if acc, ok := cfg.GenesisAccounts[entry.addr]; ok && acc != nil {
			acc.Root = entry.root
		}
	}
	if cause := context.Cause(drainCtx); cause != nil && cause != context.Canceled {
		return cause
	}
	return nil
}
