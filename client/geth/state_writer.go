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

	for i, pe := range cfg.PreAlloc {
		if pe.Storage == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, err
		}
		addr := pe.Address
		addrHash := crypto.Keccak256Hash(addr[:])
		hb := newGethStorageHashBuilder(w, addrHash)
		streamSink := func(keyHash, _rawKey, value common.Hash) error {
			valueRLP, encErr := encodeStorageValue(value)
			if encErr != nil {
				return encErr
			}
			return w.WriteStorageRLP(addrHash, keyHash, valueRLP)
		}
		root, err := streamingtrie.StorageRoot("", pe.Storage, hb, streamSink)
		if err != nil {
			return common.Hash{}, nil, fmt.Errorf("geth: stream spec storage[%d] %s: %w", i, addr.Hex(), err)
		}
		if acc, ok := cfg.GenesisAccounts[addr]; ok && acc != nil {
			acc.Root = root
		}
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
