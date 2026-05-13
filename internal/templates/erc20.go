package templates

import (
	"encoding/binary"
	"fmt"
	"iter"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/spec"
)

func init() {
	Register(&erc20Template{})
}

// OpenZeppelin v5 ERC20 storage layout. The numbers here must match the
// reference contract's slot positions exactly; tested in erc20_test.go.
const (
	erc20SlotBalances    = 0 // mapping(address => uint256)
	erc20SlotAllowances  = 1 // mapping(address => mapping(address => uint256))
	erc20SlotTotalSupply = 2
	erc20SlotName        = 3 // string (short-string layout when ≤31 bytes)
	erc20SlotSymbol      = 4 // string (short-string layout when ≤31 bytes)
)

// erc20Template handles `kind: contract, template: erc20`.
//
// Parameters (all required):
//   - symbol (string, ≤31 bytes)
//   - name   (string, ≤31 bytes)
//   - decimals (uint8) — informational only; OZ v5 returns this from a
//     pure function, not from storage. Validated here so users surface
//     intent clearly; not used in the storage layout.
//
// Optional:
//   - holders (int): explicit holder count. If set AND approximate_size_bytes
//     is also set, holders wins. If neither is set, the contract has no
//     synthesized balance storage — useful for "skeleton" tokens.
type erc20Template struct{}

func (erc20Template) Name() string { return "erc20" }

// UserVisible reports true: users set `template: erc20` in YAML.
func (erc20Template) UserVisible() bool { return true }

func (erc20Template) ValidateParameters(params map[string]any) error {
	required := []string{"symbol", "name", "decimals"}
	for _, k := range required {
		v, ok := params[k]
		if !ok {
			return fmt.Errorf("erc20: missing required parameter %q", k)
		}
		if v == nil {
			return fmt.Errorf("erc20: parameter %q is null", k)
		}
	}
	if s, _ := params["symbol"].(string); len(s) > 31 {
		return fmt.Errorf("erc20: symbol %q exceeds 31 bytes (OZ v5 short-string limit)", s)
	}
	if s, _ := params["name"].(string); len(s) > 31 {
		return fmt.Errorf("erc20: name %q exceeds 31 bytes (OZ v5 short-string limit)", s)
	}
	switch d := params["decimals"].(type) {
	case int:
		if d < 0 || d > 255 {
			return fmt.Errorf("erc20: decimals %d out of range [0,255]", d)
		}
	case int64:
		if d < 0 || d > 255 {
			return fmt.Errorf("erc20: decimals %d out of range [0,255]", d)
		}
	default:
		return fmt.Errorf("erc20: decimals must be an integer (got %T)", params["decimals"])
	}
	// Allow but don't require `holders` — defaulted by sizing if absent.
	if h, has := params["holders"]; has {
		switch h.(type) {
		case int, int64:
			// ok
		default:
			return fmt.Errorf("erc20: holders must be an integer (got %T)", h)
		}
	}
	// Reject unknown parameter keys so typos like "symbool" surface loudly.
	for k := range params {
		switch k {
		case "symbol", "name", "decimals", "holders":
		default:
			return fmt.Errorf("erc20: unknown parameter %q", k)
		}
	}
	return nil
}

func (erc20Template) Expand(ctx Context, e spec.Entity) ([]PreAllocEntity, error) {
	symbol, _ := e.Parameters["symbol"].(string)
	name, _ := e.Parameters["name"].(string)

	balance := uint256.NewInt(0)
	if e.Balance != nil {
		balance = e.Balance.V
	}

	// Resolve the holder count. Explicit `parameters.holders` wins; otherwise
	// derive from approximate_size_bytes via the sizer.
	holderCount := 0
	if h, ok := e.Parameters["holders"]; ok {
		switch v := h.(type) {
		case int:
			holderCount = v
		case int64:
			holderCount = int(v)
		}
	} else if e.ApproximateSizeBytes > 0 {
		holderCount = ctx.Sizer.SlotsForBytes(ctx.ClientName, e.ApproximateSizeBytes)
	}

	// Build the explicit slot map (totalSupply + name + symbol). _balances
	// and _allowances are mappings — empty by default and populated lazily
	// by the synthesized iterator for balances.
	explicit := map[common.Hash]common.Hash{}

	// _name + _symbol use Solidity's short-string layout (≤31 bytes):
	// [bytes left-aligned] [zero padding] [length*2 at byte 31].
	explicit[uint64SlotKey(erc20SlotName)] = packShortString(name)
	explicit[uint64SlotKey(erc20SlotSymbol)] = packShortString(symbol)

	// _totalSupply = holderCount × per-holder balance (1 token unit, scaled
	// later if needed). For v1 we set 1 unit per holder; users wanting a
	// specific total-supply distribution can use the `raw` template.
	totalSupply := new(uint256.Int).SetUint64(uint64(holderCount))
	if holderCount > 0 {
		explicit[uint64SlotKey(erc20SlotTotalSupply)] = totalSupply.Bytes32()
	}

	// Compose explicit slots with the per-holder _balances synthesizer.
	storage := MapToSeq(explicit)
	if holderCount > 0 {
		balances := erc20BalancesIter(ctx.Seed, ctx.ResolvedAddress, holderCount)
		storage = Concat(storage, balances)
	}

	// Nonce: honor the user-supplied value; floor at 1 per EIP-161
	// (contracts have nonce>=1 after Spurious Dragon). This means users
	// who explicitly set `nonce: 0` get nonce=1 silently — that's
	// intentional and matches go-ethereum's genesis-alloc convention.
	// Documented in docs/SPEC.md.
	nonce := e.Nonce
	if nonce == 0 {
		nonce = 1
	}
	codeHash := crypto.Keccak256Hash(ERC20RuntimeBytecode)
	acc := &types.StateAccount{
		Nonce:    nonce,
		Balance:  balance,
		Root:     types.EmptyRootHash,
		CodeHash: codeHash.Bytes(),
	}

	return []PreAllocEntity{{
		Address: ctx.ResolvedAddress,
		Account: acc,
		Code:    ERC20RuntimeBytecode,
		Storage: storage,
	}}, nil
}

// erc20BalancesIter emits a deterministic stream of `_balances[holder]`
// storage entries. The holder address is `keccak256(seed||token||index)[12:]`
// — pure function of `(seed, tokenAddr, index)`. Balance values are 1 token
// unit (the contract's _totalSupply is set to holderCount in Expand).
//
// Each emitted slot key is computed using Solidity's mapping rule:
//
//	slot(_balances[h]) = keccak256(abi.encode(h, uint256(0)))
//
// which is keccak256(leftPad32(h) || leftPad32(0)).
func erc20BalancesIter(seed int64, tokenAddr common.Address, count int) iter.Seq2[common.Hash, common.Hash] {
	return func(yield func(common.Hash, common.Hash) bool) {
		// Reusable buffers across iterations to avoid per-slot allocation.
		var holderBuf [8 + common.AddressLength + 8]byte
		binary.BigEndian.PutUint64(holderBuf[:8], uint64(seed))
		copy(holderBuf[8:8+common.AddressLength], tokenAddr[:])
		// last 8 bytes: index — set per iteration.

		var mapKeyBuf [64]byte // leftPad32(holder) || leftPad32(slot=0)
		// mapKeyBuf[44..63] is zero (slot 0 padded to 32 bytes).

		// Each holder gets exactly 1 unit of token.
		val := uint256.NewInt(1).Bytes32()

		for i := range count {
			binary.BigEndian.PutUint64(holderBuf[8+common.AddressLength:], uint64(i))
			holderHash := crypto.Keccak256(holderBuf[:])
			// holderAddr is right-12-bytes of keccak256(...). For slot-key
			// computation, the LEFT-padded ADDRESS lives in bytes 12..31 of
			// the mapKey buffer's first 32 bytes.
			copy(mapKeyBuf[12:32], holderHash[12:])
			slotKey := crypto.Keccak256Hash(mapKeyBuf[:])
			if !yield(slotKey, val) {
				return
			}
		}
	}
}

// uint64SlotKey turns a small slot index into its 32-byte big-endian
// representation — used for top-level slots (not mappings).
func uint64SlotKey(slot uint64) common.Hash {
	var h common.Hash
	binary.BigEndian.PutUint64(h[24:32], slot)
	return h
}

// packShortString packs a ≤31-byte string into a Solidity-format short-string
// storage slot: [bytes left-aligned (positions 0..len-1)] [zero padding
// (positions len..30)] [length*2 (position 31)].
//
// Panics for inputs > 31 bytes — ValidateParameters rejects those earlier.
func packShortString(s string) common.Hash {
	if len(s) > 31 {
		panic(fmt.Sprintf("packShortString: input too long (%d bytes); callers must validate", len(s)))
	}
	var h common.Hash
	copy(h[:len(s)], s)
	h[31] = byte(len(s) * 2)
	return h
}

// ERC20RuntimeBytecode is the deployed bytecode for ERC-20 template
// instances. This is a v1 STUB: the bytes form a non-reverting STOP
// opcode so genesis state-root computation succeeds and JSON-RPC
// eth_call to the contract returns empty bytes (which decode to zero for
// any uint256/bool return type — so balanceOf and totalSupply return 0
// regardless of stored values).
//
// **v1 limitation**: this bytecode does NOT implement the ERC-20
// interface. The storage layout (_balances mapping at slot 0,
// _totalSupply at slot 2, _name/_symbol at slots 3/4 in short-string
// format) IS correct — Story 1's "10 GB ERC-20" produces real 10 GB of
// on-disk state-trie data, but the contract is not callable via RPC.
//
// **v1.5 follow-up**: replace these bytes with audited OpenZeppelin v5
// ERC20.sol runtime bytecode, compiled with solc 0.8.20 and
// --optimize-runs=200. The slot layout above is intentionally aligned
// with OZ v5 to make this a single-file swap with no test rewrites.
var ERC20RuntimeBytecode = []byte{0x00}
