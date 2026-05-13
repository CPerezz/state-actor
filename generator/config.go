package generator

import (
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/nerolation/state-actor/genesis"
	"github.com/nerolation/state-actor/internal/entitygen"
	"github.com/nerolation/state-actor/internal/templates"
)

// TrieMode represents the trie algorithm used for state root computation.
type TrieMode string

const (
	// TrieModeMPT uses the Merkle Patricia Trie (hexary, keccak256-based).
	TrieModeMPT TrieMode = "mpt"

	// TrieModeBinary uses the EIP-7864 binary trie.
	// Compatible with geth's --override.verkle=0 flag (=0 is the activation block number).
	TrieModeBinary TrieMode = "binary"
)

// Distribution selects how storage-slot counts vary across contracts.
// Re-exported from internal/entitygen so existing callers (CLI, tests)
// continue to work unchanged.
type Distribution = entitygen.Distribution

const (
	// PowerLaw distribution - most contracts have few slots, few have many.
	PowerLaw = entitygen.PowerLaw
	// Uniform distribution - all contracts have similar slot counts.
	Uniform = entitygen.Uniform
	// Exponential distribution - exponential decay in slot counts.
	Exponential = entitygen.Exponential
)

// ParseDistribution parses a distribution string.
func ParseDistribution(s string) Distribution {
	return entitygen.ParseDistribution(s)
}

// Config holds the configuration for state generation.
type Config struct {
	// DBPath is the path to the Pebble database directory.
	DBPath string

	// NumAccounts is the number of EOA accounts to create.
	NumAccounts int

	// NumContracts is the number of contract accounts to create.
	NumContracts int

	// MaxSlots is the maximum number of storage slots per contract.
	MaxSlots int

	// MinSlots is the minimum number of storage slots per contract.
	MinSlots int

	// Distribution is the storage slot distribution strategy.
	Distribution Distribution

	// Seed is the random seed for reproducible generation.
	Seed int64

	// CodeSize is the average contract code size in bytes.
	CodeSize int

	// Verbose enables verbose logging.
	Verbose bool

	// TrieMode selects the trie algorithm for state root computation.
	// Defaults to TrieModeMPT if empty.
	TrieMode TrieMode

	// Genesis is the in-memory genesis configuration that drives chainID,
	// fork selection, and the genesis-block header fields. Always non-nil
	// after main.go's BuildSynthetic call. Each client's writer reads
	// Genesis.Config (for IsLondon / IsShanghai / etc.) and Genesis's
	// header fields (GasLimit, Timestamp, ExtraData, ...).
	//
	// Genesis.Alloc is intentionally always empty in the production path:
	// pre-funded accounts come from --inject-accounts, not the genesis JSON.
	Genesis *genesis.Genesis

	// GenesisAccounts / GenesisStorage / GenesisCode carry pre-allocated
	// state — post-Merge EIP system contracts (deployed via
	// oracle.AddPragueSystemContracts in e2e suites) and any other
	// alloc entries tests need. All 4 client writers consume these
	// fields (geth/nethermind/besu/reth). Config.Validate() enforces
	// that GenesisStorage/GenesisCode have no orphan entries.
	// Production CLI populates these via the --spec YAML path (the
	// PreAlloc field, below, materializes into these maps at Validate
	// time).
	GenesisAccounts map[common.Address]*types.StateAccount
	GenesisStorage  map[common.Address]map[common.Hash]common.Hash
	GenesisCode     map[common.Address][]byte

	// CommitInterval is the number of accounts between binary trie commits
	// to disk. Only used in TrieModeBinary. 0 means no intermediate commits
	// (entire trie stays in memory). When set, the trie is periodically
	// committed to a temporary Pebble database and reopened, bounding memory
	// to the working set between commits (~1-2 GB) instead of the full trie.
	CommitInterval int

	// WriteTrieNodes enables writing serialized binary trie nodes to the
	// database during Phase 2 hash computation, making the database usable
	// by geth's PathDB. Automatically set when --genesis is provided.
	WriteTrieNodes bool

	// TargetSize is the target total database size on disk in bytes.
	// When set (> 0), this is the GOVERNING constraint: contracts are
	// generated until the projected on-disk size reaches this target.
	// NumContracts serves as a safety upper bound. 0 means no size limit.
	TargetSize uint64

	// GroupDepth is the binary trie group depth (1-8, default 8).
	// Controls how many trie levels are serialized per DB entry.
	GroupDepth int

	// PreAlloc carries entities produced by the --spec flag's YAML
	// translator (internal/specbuild/). Validate() materializes PreAlloc
	// entries into the GenesisAccounts/Code/Storage maps so every client
	// writer consumes a single unified shape.
	//
	// Validate() must be called before any writer consumes Config — that
	// is where the materialization happens.
	PreAlloc []templates.PreAllocEntity
}

// Validate rejects malformed Config combinations at command start, before
// any DB write happens. Centralized so all 4 client writers enforce the
// same invariants.
//
// Validate also materializes PreAlloc entries into the
// GenesisAccounts/Code/Storage maps. Writers should call Validate as
// their first non-trivial step.
//
// Rejects:
//   - PreAlloc addresses collide with programmatic GenesisAccounts entries
//     (e.g. oracle.AddPragueSystemContracts).
//   - GenesisCode address not in GenesisAccounts — orphan code.
//   - GenesisStorage address not in GenesisAccounts — orphan storage.
//   - Spec storage estimate exceeds --target-size.
//
// Called by client writers as the first non-trivial step of
// Run() / Populate() / runImpl().
func (c *Config) Validate() error {
	// Step 1: materialize PreAlloc. After this point the checks treat
	// both YAML-spec entries and programmatic alloc entries uniformly.
	if err := c.materializePreAlloc(); err != nil {
		return err
	}

	for a := range c.GenesisCode {
		if _, ok := c.GenesisAccounts[a]; !ok {
			return fmt.Errorf("Config: GenesisCode[%s] has no corresponding GenesisAccounts entry (orphan code)", a.Hex())
		}
	}
	for a := range c.GenesisStorage {
		if _, ok := c.GenesisAccounts[a]; !ok {
			return fmt.Errorf("Config: GenesisStorage[%s] has no corresponding GenesisAccounts entry (orphan storage)", a.Hex())
		}
	}

	// Target-size budget enforcement: if --target-size is set, the spec
	// alone must not exceed it. Conservative per-slot estimate (80 B/slot
	// — the heaviest of the per-client calibration factors in
	// internal/sizecal/factors.json) means we under-report and never
	// false-reject; users can always raise --target-size if the warning
	// fires spuriously.
	if c.TargetSize > 0 {
		const bytesPerSlot uint64 = 80
		var totalSlots uint64
		for _, slots := range c.GenesisStorage {
			totalSlots += uint64(len(slots))
		}
		estimated := totalSlots * bytesPerSlot
		if estimated > c.TargetSize {
			return fmt.Errorf(
				"Config: spec entities require an estimated %d bytes (%d slots × %d B/slot conservative estimate) "+
					"which exceeds --target-size=%d. Raise --target-size or reduce approximate_size_bytes on spec entities.",
				estimated, totalSlots, bytesPerSlot, c.TargetSize,
			)
		}
	}
	return nil
}

// materializePreAlloc folds Config.PreAlloc into the
// GenesisAccounts/Code/Storage maps. Each PreAllocEntity becomes one
// GenesisAccounts entry plus optional GenesisCode/GenesisStorage entries.
// Storage iter.Seq2 is drained into a map — RAM is proportional to the
// total slot count across all entities.
//
// After PreAlloc is materialized, the field is left as-is so callers can
// still inspect it (e.g. for diagnostics) but writers should not iterate
// it directly.
//
// Idempotent: calling Validate twice does not double-materialize because
// the function refuses to overwrite an existing key in GenesisAccounts
// (returns an error instead, surfacing the collision).
func (c *Config) materializePreAlloc() error {
	if len(c.PreAlloc) == 0 {
		return nil
	}
	if c.GenesisAccounts == nil {
		c.GenesisAccounts = make(map[common.Address]*types.StateAccount, len(c.PreAlloc))
	}
	if c.GenesisCode == nil {
		c.GenesisCode = make(map[common.Address][]byte)
	}
	if c.GenesisStorage == nil {
		c.GenesisStorage = make(map[common.Address]map[common.Hash]common.Hash)
	}

	for i, pe := range c.PreAlloc {
		if _, dup := c.GenesisAccounts[pe.Address]; dup {
			return fmt.Errorf("Config.PreAlloc[%d]: address %s already present in GenesisAccounts (programmatic-alloc + spec-alloc collision)", i, pe.Address.Hex())
		}
		c.GenesisAccounts[pe.Address] = pe.Account
		if len(pe.Code) > 0 {
			c.GenesisCode[pe.Address] = pe.Code
		}
		if pe.Storage != nil {
			storage := make(map[common.Hash]common.Hash)
			pe.Storage(func(k, v common.Hash) bool {
				storage[k] = v
				return true
			})
			if len(storage) > 0 {
				c.GenesisStorage[pe.Address] = storage
			}
		}
	}
	// PreAlloc has been consumed; clearing it makes Validate idempotent
	// across multiple calls (some test harnesses validate twice).
	c.PreAlloc = nil
	return nil
}

// Stats holds statistics about the generation process.
type Stats struct {
	// AccountsCreated is the number of accounts created.
	AccountsCreated int

	// ContractsCreated is the number of contracts created.
	ContractsCreated int

	// StorageSlotsCreated is the total number of storage slots created.
	StorageSlotsCreated int

	// TotalBytes is the total number of bytes written.
	TotalBytes uint64

	// AccountBytes is the number of bytes for account data.
	AccountBytes uint64

	// StorageBytes is the number of bytes for storage data.
	StorageBytes uint64

	// CodeBytes is the number of bytes for contract code.
	CodeBytes uint64

	// TrieNodeBytes is the number of bytes written for trie nodes (Phase 2).
	// Only populated when WriteTrieNodes is true.
	TrieNodeBytes uint64

	// StemBlobBytes is the number of bytes written for bintrie flat-state
	// stem blobs (Phase 2). Only populated in binary trie mode.
	StemBlobBytes uint64

	// StateRoot is the computed state root hash.
	StateRoot common.Hash

	// SampleEOAs holds a few sample EOA addresses for post-generation verification.
	SampleEOAs []common.Address

	// SampleContracts holds a few sample contract addresses for post-generation verification.
	SampleContracts []common.Address

	// DBWriteTime is the time spent writing to the database.
	DBWriteTime time.Duration

	// GenerationTime is the time spent generating data.
	GenerationTime time.Duration
}
