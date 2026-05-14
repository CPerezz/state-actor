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

// erc20FixedDecimals is the only value accepted for the `decimals`
// parameter. OZ v5's base ERC20 hardcodes `decimals()` to return 18 from
// a pure function; planting a different value in storage doesn't change
// the RPC return. Users wanting non-18 tokens use the `raw` template.
const erc20FixedDecimals = 18

// erc20Template handles `kind: contract, template: erc20`.
//
// Required parameters:
//   - symbol (string, ≤31 bytes)
//   - name   (string, ≤31 bytes)
//   - decimals (int) — must equal 18 (OZ v5 base default).
//
// Optional parameters:
//   - owners: list of `{address, balance}` entries. Each entry plants
//     `_balances[address] = balance` in storage; balances are quoted
//     strings (decimal or 0x-hex). Duplicate addresses are rejected.
//   - allowances: list of `{owner, spender, allowance}` entries. Each
//     entry plants `_allowances[owner][spender] = allowance`. Duplicate
//     `(owner, spender)` pairs are rejected; an allowance owner need
//     not have a balance entry (standard ERC-20 semantics).
//   - total_owners: target total holder count. `total_owners - len(owners)`
//     additional random holders are synthesized with deterministic
//     varied balances in `[1, 10^18]` wei. Must satisfy
//     `total_owners >= len(owners)`.
//   - total_allowances: same shape, applied to the allowances mapping.
//
// `_totalSupply` is auto-summed from all planted balances (explicit +
// synthesized random). Users cannot override it — the ERC-20
// conservation invariant is preserved by construction.
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

	// `holders` was renamed to `total_owners`. Catch the old key
	// explicitly so users get a clear migration message instead of
	// "unknown parameter".
	if _, has := params["holders"]; has {
		return fmt.Errorf("erc20: `holders` was renamed to `total_owners` " +
			"(also accepts an `owners` list for granular entries). " +
			"See docs/SPEC.md for the new schema.")
	}

	if s, _ := params["symbol"].(string); len(s) > 31 {
		return fmt.Errorf("erc20: symbol %q exceeds 31 bytes (OZ v5 short-string limit)", s)
	}
	if s, _ := params["name"].(string); len(s) > 31 {
		return fmt.Errorf("erc20: name %q exceeds 31 bytes (OZ v5 short-string limit)", s)
	}

	// decimals must equal 18 (OZ v5 base ERC20 returns 18 from a pure
	// function). Loud rejection beats silent ignore.
	var dec int
	switch d := params["decimals"].(type) {
	case int:
		dec = d
	case int64:
		dec = int(d)
	default:
		return fmt.Errorf("erc20: decimals must be an integer (got %T)", params["decimals"])
	}
	if dec != erc20FixedDecimals {
		return fmt.Errorf("erc20: decimals must equal %d (OZ v5 base default); "+
			"use the `raw` template for non-%d tokens (got %d)",
			erc20FixedDecimals, erc20FixedDecimals, dec)
	}

	// Reject unknown parameter keys so typos like "symbool" surface loudly.
	for k := range params {
		switch k {
		case "symbol", "name", "decimals",
			"owners", "allowances", "total_owners", "total_allowances":
		default:
			return fmt.Errorf("erc20: unknown parameter %q", k)
		}
	}

	// Structural validation of the optional list parameters. Reuses the
	// same parsers Expand uses so validation and expansion stay in sync.
	owners, err := ParseExplicitOwners(params["owners"])
	if err != nil {
		return err
	}
	allowances, err := ParseExplicitAllowances(params["allowances"])
	if err != nil {
		return err
	}

	totalOwners := 0
	if v, has := params["total_owners"]; has {
		n, err := ParseNonNegIntParam(v, "total_owners")
		if err != nil {
			return err
		}
		totalOwners = n
	}
	if len(owners) > totalOwners && totalOwners > 0 {
		return fmt.Errorf("erc20: len(owners)=%d > total_owners=%d (set total_owners >= %d or remove explicit owners)",
			len(owners), totalOwners, len(owners))
	}

	totalAllowances := 0
	if v, has := params["total_allowances"]; has {
		n, err := ParseNonNegIntParam(v, "total_allowances")
		if err != nil {
			return err
		}
		totalAllowances = n
	}
	if len(allowances) > totalAllowances && totalAllowances > 0 {
		return fmt.Errorf("erc20: len(allowances)=%d > total_allowances=%d (set total_allowances >= %d or remove explicit allowances)",
			len(allowances), totalAllowances, len(allowances))
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

	owners, err := ParseExplicitOwners(e.Parameters["owners"])
	if err != nil {
		return nil, err
	}
	allowances, err := ParseExplicitAllowances(e.Parameters["allowances"])
	if err != nil {
		return nil, err
	}

	totalOwners := len(owners)
	if v, has := e.Parameters["total_owners"]; has {
		n, err := ParseNonNegIntParam(v, "total_owners")
		if err != nil {
			return nil, err
		}
		totalOwners = n
	}
	totalAllowances := len(allowances)
	if v, has := e.Parameters["total_allowances"]; has {
		n, err := ParseNonNegIntParam(v, "total_allowances")
		if err != nil {
			return nil, err
		}
		totalAllowances = n
	}
	randomOwnerCount := totalOwners - len(owners)
	randomAllowanceCount := totalAllowances - len(allowances)

	// Build the explicit slot map up-front: name, symbol, explicit
	// balances, explicit allowances. _totalSupply is added below after
	// the random-balance sum is computed.
	explicit := map[common.Hash]common.Hash{}

	explicit[uint64SlotKey(erc20SlotName)] = packShortString(name)
	explicit[uint64SlotKey(erc20SlotSymbol)] = packShortString(symbol)

	// Auto-summed _totalSupply: explicit balances + random balances.
	totalSupply := new(uint256.Int)

	for _, o := range owners {
		explicit[balanceSlotKey(o.Address)] = o.Balance.Bytes32()
		totalSupply.Add(totalSupply, o.Balance)
	}
	for _, a := range allowances {
		explicit[allowanceSlotKey(a.Owner, a.Spender)] = a.Amount.Bytes32()
	}

	// Sum the random-balance contribution. The random-balances iter is
	// a pure function — we iterate it once here for the sum, and again
	// below when composing the storage iter. Each yields the same
	// (key, value) pairs.
	for i := 0; i < randomOwnerCount; i++ {
		v := DeterministicRandomBalance(ctx.Seed, ctx.ResolvedAddress, i)
		totalSupply.Add(totalSupply, new(uint256.Int).SetBytes(v[:]))
	}

	if !totalSupply.IsZero() {
		explicit[uint64SlotKey(erc20SlotTotalSupply)] = totalSupply.Bytes32()
	}

	// Compose the final storage iter:
	//   explicit map -> random _balances synthesizer -> random _allowances synthesizer.
	storage := MapToSeq(explicit)
	if randomOwnerCount > 0 {
		storage = Concat(storage, erc20BalancesIter(ctx.Seed, ctx.ResolvedAddress, randomOwnerCount))
	}
	if randomAllowanceCount > 0 {
		storage = Concat(storage, erc20RandomAllowancesIter(ctx.Seed, ctx.ResolvedAddress, randomAllowanceCount))
	}

	// Nonce: honor the user-supplied value; floor at 1 per EIP-161
	// (contracts have nonce>=1 after Spurious Dragon).
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

// erc20BalancesIter emits a deterministic stream of synthesized
// `_balances[holder]` storage entries for the random-fill portion of an
// erc20 contract. The holder address is `keccak256(seed||token||index)[12:]`
// — pure function of `(seed, tokenAddr, index)`. Balance values come from
// DeterministicRandomBalance, also a pure function of the same inputs.
//
// Each slot key is computed using Solidity's mapping rule:
//
//	slot(_balances[h]) = keccak256(abi.encode(h, uint256(0)))
//
// which is keccak256(leftPad32(h) || leftPad32(0)).
//
// Re-iteration is safe: every call to the returned iter yields the
// same sequence.
func erc20BalancesIter(seed int64, tokenAddr common.Address, count int) iter.Seq2[common.Hash, common.Hash] {
	return func(yield func(common.Hash, common.Hash) bool) {
		// Reusable buffers across iterations to avoid per-slot allocation.
		var holderBuf [8 + common.AddressLength + 8]byte
		binary.BigEndian.PutUint64(holderBuf[:8], uint64(seed))
		copy(holderBuf[8:8+common.AddressLength], tokenAddr[:])

		var mapKeyBuf [64]byte // leftPad32(holder) || leftPad32(slot=0)

		for i := range count {
			binary.BigEndian.PutUint64(holderBuf[8+common.AddressLength:], uint64(i))
			holderHash := crypto.Keccak256(holderBuf[:])
			// holderAddr is right-12-bytes of keccak256(...). For slot-key
			// computation, the LEFT-padded ADDRESS lives in bytes 12..31 of
			// the mapKey buffer's first 32 bytes.
			copy(mapKeyBuf[12:32], holderHash[12:])
			slotKey := crypto.Keccak256Hash(mapKeyBuf[:])
			val := DeterministicRandomBalance(seed, tokenAddr, i)
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

// ERC20RuntimeBytecode is defined in erc20_bytecode.go — it embeds the
// vendored OpenZeppelin v5.6.1 ERC20 deployed runtime bytecode.
