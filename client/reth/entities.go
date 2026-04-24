package reth

import (
	"bytes"
	"math"
	mrand "math/rand"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/nerolation/state-actor/generator"
)

// accountData is the per-account work unit consumed by the JSONL dumper
// and the state-root builder. The RNG draws in entities.go must stay on
// one goroutine — math/rand.Rand is not thread-safe.
//
// This mirrors generator.accountData (unexported there) by design —
// client/reth/ is intentionally self-contained per the project's
// multi-client ADR.
type accountData struct {
	address  common.Address
	addrHash common.Hash
	account  *types.StateAccount
	code     []byte
	codeHash common.Hash
	storage  []storageSlot
}

type storageSlot struct {
	Key   common.Hash
	Value common.Hash
}

// entitySource produces a deterministic stream of accountData from a seed.
// It is a drop-in replacement for generator.go's inlined generation loops,
// ported here so client/reth stays independent of generator internals.
type entitySource struct {
	cfg          generator.Config
	rng          *mrand.Rand
	genesisAddrs map[common.Address]bool
}

func newEntitySource(cfg generator.Config) *entitySource {
	return &entitySource{
		cfg:          cfg,
		rng:          mrand.New(mrand.NewSource(cfg.Seed)),
		genesisAddrs: make(map[common.Address]bool, len(cfg.GenesisAccounts)),
	}
}

// genesisAccounts returns the accounts from genesis alloc in deterministic
// order (sorted by address). It populates s.genesisAddrs for collision
// avoidance during random generation.
func (s *entitySource) genesisAccounts() []*accountData {
	if len(s.cfg.GenesisAccounts) == 0 {
		return nil
	}
	addrs := make([]common.Address, 0, len(s.cfg.GenesisAccounts))
	for a := range s.cfg.GenesisAccounts {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})

	out := make([]*accountData, 0, len(addrs))
	for _, addr := range addrs {
		s.genesisAddrs[addr] = true
		acc := s.cfg.GenesisAccounts[addr]
		ad := &accountData{
			address:  addr,
			addrHash: crypto.Keccak256Hash(addr[:]),
			account:  acc,
		}
		if code, ok := s.cfg.GenesisCode[addr]; ok && len(code) > 0 {
			ad.code = code
			ad.codeHash = crypto.Keccak256Hash(code)
		}
		if slots, ok := s.cfg.GenesisStorage[addr]; ok && len(slots) > 0 {
			ad.storage = mapToSortedSlots(slots)
		}
		out = append(out, ad)
	}
	return out
}

// injectedAccounts returns the --inject-accounts entries with 999999999 ETH.
func (s *entitySource) injectedAccounts() []*accountData {
	if len(s.cfg.InjectAddresses) == 0 {
		return nil
	}
	bal := new(uint256.Int).Mul(uint256.NewInt(999999999), uint256.NewInt(1e18))
	out := make([]*accountData, 0, len(s.cfg.InjectAddresses))
	for _, addr := range s.cfg.InjectAddresses {
		if s.genesisAddrs[addr] {
			continue
		}
		s.genesisAddrs[addr] = true
		out = append(out, &accountData{
			address:  addr,
			addrHash: crypto.Keccak256Hash(addr[:]),
			account: &types.StateAccount{
				Nonce:    0,
				Balance:  bal,
				Root:     types.EmptyRootHash,
				CodeHash: types.EmptyCodeHash.Bytes(),
			},
		})
	}
	return out
}

// nextEOA generates one EOA. Matches the RNG draw sequence of
// generator.Generator.generateEOA at generator/generator.go:1237.
func (s *entitySource) nextEOA() *accountData {
	var addr common.Address
	s.rng.Read(addr[:])
	for s.genesisAddrs[addr] {
		s.rng.Read(addr[:])
	}

	bal := new(uint256.Int).Mul(
		uint256.NewInt(uint64(s.rng.Intn(1000))),
		uint256.NewInt(1e18),
	)
	return &accountData{
		address:  addr,
		addrHash: crypto.Keccak256Hash(addr[:]),
		account: &types.StateAccount{
			Nonce:    uint64(s.rng.Intn(1000)),
			Balance:  bal,
			Root:     types.EmptyRootHash,
			CodeHash: types.EmptyCodeHash.Bytes(),
		},
	}
}

// nextContract generates one contract with the requested number of storage
// slots. Matches generator.Generator.generateContract's RNG sequence.
func (s *entitySource) nextContract(numSlots int) *accountData {
	var addr common.Address
	s.rng.Read(addr[:])
	for s.genesisAddrs[addr] {
		s.rng.Read(addr[:])
	}

	codeSize := s.cfg.CodeSize + s.rng.Intn(s.cfg.CodeSize)
	code := make([]byte, codeSize)
	s.rng.Read(code)
	codeHash := crypto.Keccak256Hash(code)

	bal := new(uint256.Int).Mul(
		uint256.NewInt(uint64(s.rng.Intn(100))),
		uint256.NewInt(1e18),
	)

	storage := make([]storageSlot, 0, numSlots)
	for j := 0; j < numSlots; j++ {
		var key, value common.Hash
		s.rng.Read(key[:])
		s.rng.Read(value[:])
		if value == (common.Hash{}) {
			// Zero values are deletions in Ethereum state; lift to 1 for
			// determinism — matches generator/generator.go:1284.
			value[31] = 1
		}
		storage = append(storage, storageSlot{Key: key, Value: value})
	}
	sort.Slice(storage, func(i, j int) bool {
		return bytes.Compare(storage[i].Key[:], storage[j].Key[:]) < 0
	})

	return &accountData{
		address:  addr,
		addrHash: crypto.Keccak256Hash(addr[:]),
		account: &types.StateAccount{
			Nonce:    uint64(s.rng.Intn(1000)),
			Balance:  bal,
			Root:     types.EmptyRootHash, // filled in by statedump once storage root is known
			CodeHash: codeHash.Bytes(),
		},
		code:     code,
		codeHash: codeHash,
		storage:  storage,
	}
}

// nextSlotCount draws one slot count from the configured distribution.
// Matches generator.Generator.generateSlotCount (one rng.Float64 or one
// rng.Intn per call).
func (s *entitySource) nextSlotCount() int {
	switch s.cfg.Distribution {
	case generator.PowerLaw:
		u := s.rng.Float64()
		alpha := 1.5
		slots := float64(s.cfg.MinSlots) / math.Pow(1-u, 1/alpha)
		if slots > float64(s.cfg.MaxSlots) {
			slots = float64(s.cfg.MaxSlots)
		}
		return int(slots)
	case generator.Exponential:
		lambda := math.Log(2) / float64(s.cfg.MaxSlots/4)
		u := s.rng.Float64()
		slots := -math.Log(1-u) / lambda
		slots = math.Max(float64(s.cfg.MinSlots), math.Min(slots, float64(s.cfg.MaxSlots)))
		return int(slots)
	case generator.Uniform:
		return s.cfg.MinSlots + s.rng.Intn(s.cfg.MaxSlots-s.cfg.MinSlots+1)
	default:
		return s.cfg.MinSlots
	}
}

// mapToSortedSlots is the local version of generator.mapToSortedSlots,
// duplicated here so this package doesn't reach into generator internals.
// Sorting is by raw key (pre-keccak) for determinism in the JSONL dump;
// the state-root builder re-sorts by keccak256(key) before feeding StackTrie.
func mapToSortedSlots(m map[common.Hash]common.Hash) []storageSlot {
	slots := make([]storageSlot, 0, len(m))
	for k, v := range m {
		slots = append(slots, storageSlot{Key: k, Value: v})
	}
	sort.Slice(slots, func(i, j int) bool {
		return bytes.Compare(slots[i].Key[:], slots[j].Key[:]) < 0
	})
	return slots
}
