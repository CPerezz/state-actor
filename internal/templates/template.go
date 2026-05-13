package templates

import (
	"iter"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/nerolation/state-actor/internal/spec"
)

// PreAllocEntity is the unified post-expansion record every writer
// consumes. Each carries one address's account header (nonce/balance/
// codeHash/root), the deployed code (nil for plain EOAs), and the
// storage map.
//
// Storage iteration order is the responsibility of the consumer. For
// state-root determinism, writers iterate by sorted key. Templates are not
// required to emit storage in key-sorted order — they emit deterministically,
// and the writer sorts.
type PreAllocEntity struct {
	// Address is the on-chain address this entity occupies. Determined by
	// internal/specbuild/derive.go from one of three modes (explicit, name-
	// derived, position-derived).
	Address common.Address

	// Account is the StateAccount header. Templates set Nonce + Balance.
	// CodeHash is set by the template iff Code is non-nil. Root is left
	// as types.EmptyRootHash by templates — the writer computes it from
	// the Storage map.
	Account *types.StateAccount

	// Code is the deployed bytecode. nil for plain EOAs; non-nil for
	// contracts; can also be a 23-byte EIP-7702 0xef0100<addr> delegation
	// marker for EOAs.
	Code []byte

	// Storage yields (key, value) pairs for this entity. iter.Seq2 (Go 1.23+
	// range-over-function) so synthesized storage doesn't need to be
	// materialized up front — a 10 GB ERC-20 spec produces a closure that
	// computes slots on demand rather than a 10 GB Go-heap map. nil when
	// the entity has no storage. Iteration order is NOT keccak-sorted;
	// writers that need sorted insertion must materialize and sort
	// themselves (the storage write path uses a small per-entity buffer).
	//
	// Pure-function iterators may be re-iterated; callers should not
	// assume single-shot semantics.
	Storage iter.Seq2[common.Hash, common.Hash]
}

// Context carries the inputs a Template needs to deterministically expand
// one spec entity into one or more PreAllocEntity records.
type Context struct {
	// Seed is the run-wide RNG seed (typically from --seed). Every template
	// that synthesizes addresses or storage MUST derive from this seed so
	// re-running the same spec produces byte-identical state.
	Seed int64

	// ClientName identifies which writer (geth/besu/nethermind/reth) is
	// consuming the templates. The size approximator uses this to pick a
	// per-client bytes-per-slot calibration factor.
	ClientName string

	// Sizer translates `approximate_size_bytes` into a synthetic storage-
	// slot count. Per-client factor lives in internal/sizecal/.
	Sizer SizeApproximator

	// ResolvedAddress is the entity's address as decided by the translator
	// (internal/specbuild/derive.go). The template receives this rather
	// than re-deriving so address resolution stays in one place.
	ResolvedAddress common.Address

	// EntityIndex is the 0-based position of this entity in the spec.
	// Templates may include it in synthesis-key derivation so two entities
	// of the same template don't collide.
	EntityIndex int
}

// SizeApproximator converts a target byte budget to a synthetic slot count.
// Implemented by internal/sizecal/. The templates package depends only on
// this interface to avoid an import cycle.
type SizeApproximator interface {
	SlotsForBytes(client string, targetBytes uint64) int
}

// Template is the single extension point of this package. Adding a new
// template (ERC-721, UniswapV2, Aave, etc.) is one new file implementing
// this interface plus a Register() call in init().
//
// Determinism contract: for the same (ctx, e), Expand must return the same
// []PreAllocEntity byte-for-byte across runs and across machines.
type Template interface {
	// Name is the registry key.
	Name() string

	// UserVisible reports whether this template is exposed via the YAML
	// `template:` field. Internal-only templates (raw, eoa) return false
	// — they're dispatched from `kind:` directly and must not be picked
	// by user-supplied `template:` strings. UserVisibleNames() filters by
	// this.
	UserVisible() bool

	// ValidateParameters runs at parse time, before any Expand call, so
	// users see parameter errors early. Implementations should reject
	// unknown parameter keys (avoid silent typos).
	ValidateParameters(params map[string]any) error

	// Expand turns one spec entity into 1..N PreAllocEntity records. Most
	// templates emit one; multi-contract ecosystems (UniswapV2) emit many.
	Expand(ctx Context, e spec.Entity) ([]PreAllocEntity, error)
}
