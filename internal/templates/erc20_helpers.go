package templates

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"iter"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/spec"
)

// explicitOwner is one entry in the erc20 template's `owners` parameter:
//
//	owners:
//	  - { address: "0x...", balance: "1000000000000000000" }
type explicitOwner struct {
	Address common.Address
	Balance *uint256.Int
}

// explicitAllowance is one entry in the erc20 template's `allowances`:
//
//	allowances:
//	  - { owner: "0x...", spender: "0x...", allowance: "100" }
type explicitAllowance struct {
	Owner    common.Address
	Spender  common.Address
	Amount   *uint256.Int
}

// parseHexAddressParam decodes a parameter expected to be a `0x`-prefixed
// 20-byte hex string. Returns a structured error if the value is not a
// string or doesn't match the address shape. fieldLabel is interpolated
// into the error message so callers ("owner address", "spender address")
// stay readable.
func parseHexAddressParam(v any, fieldLabel string) (common.Address, error) {
	s, ok := v.(string)
	if !ok {
		return common.Address{}, fmt.Errorf("erc20: %s must be a quoted hex string (got %T)", fieldLabel, v)
	}
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return common.Address{}, fmt.Errorf("erc20: %s must have 0x prefix (got %q)", fieldLabel, s)
	}
	if len(s) != 2+2*common.AddressLength {
		return common.Address{}, fmt.Errorf("erc20: %s must be 20 bytes (42 hex chars including 0x), got %d chars in %q",
			fieldLabel, len(s), s)
	}
	raw, err := hex.DecodeString(s[2:])
	if err != nil {
		return common.Address{}, fmt.Errorf("erc20: %s decode %q: %w", fieldLabel, s, err)
	}
	var out common.Address
	copy(out[:], raw)
	return out, nil
}

// parseUint256Param decodes a parameter expected to be a quoted decimal
// or 0x-hex string representing a non-negative integer. Delegates to
// spec.ParseUint256 so the rules match the entity-level `balance:` field.
func parseUint256Param(v any, fieldLabel string) (*uint256.Int, error) {
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("erc20: %s must be a quoted decimal or 0x-hex string (got %T)", fieldLabel, v)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("erc20: %s is empty", fieldLabel)
	}
	u, err := spec.ParseUint256(s)
	if err != nil {
		return nil, fmt.Errorf("erc20: %s decode %q: %w", fieldLabel, s, err)
	}
	return u, nil
}

// parseNonNegIntParam decodes a parameter expected to be a non-negative
// integer. Accepts both `int` and `int64` because yaml.v3 may return
// either depending on magnitude.
func parseNonNegIntParam(v any, fieldLabel string) (int, error) {
	switch n := v.(type) {
	case int:
		if n < 0 {
			return 0, fmt.Errorf("erc20: %s must be >= 0 (got %d)", fieldLabel, n)
		}
		return n, nil
	case int64:
		if n < 0 {
			return 0, fmt.Errorf("erc20: %s must be >= 0 (got %d)", fieldLabel, n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("erc20: %s must be a non-negative integer (got %T)", fieldLabel, v)
	}
}

// parseExplicitOwners decodes the `owners` parameter into a typed
// slice. Each entry must be a map with `address` (0x-string) and
// `balance` (quoted decimal or hex). Duplicate addresses are rejected.
func parseExplicitOwners(v any) ([]explicitOwner, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("erc20: owners must be a list (got %T)", v)
	}
	out := make([]explicitOwner, 0, len(list))
	seen := make(map[common.Address]int, len(list))
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("erc20: owners[%d] must be a map with `address` and `balance` keys (got %T)", i, entry)
		}
		for k := range m {
			if k != "address" && k != "balance" {
				return nil, fmt.Errorf("erc20: owners[%d] has unknown key %q (expected `address`, `balance`)", i, k)
			}
		}
		addrAny, hasAddr := m["address"]
		balAny, hasBal := m["balance"]
		if !hasAddr {
			return nil, fmt.Errorf("erc20: owners[%d] missing `address`", i)
		}
		if !hasBal {
			return nil, fmt.Errorf("erc20: owners[%d] missing `balance`", i)
		}
		addr, err := parseHexAddressParam(addrAny, fmt.Sprintf("owners[%d].address", i))
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[addr]; dup {
			return nil, fmt.Errorf("erc20: owners[%d].address %s duplicates owners[%d]", i, addr.Hex(), prev)
		}
		bal, err := parseUint256Param(balAny, fmt.Sprintf("owners[%d].balance", i))
		if err != nil {
			return nil, err
		}
		seen[addr] = i
		out = append(out, explicitOwner{Address: addr, Balance: bal})
	}
	return out, nil
}

// parseExplicitAllowances decodes the `allowances` parameter into a
// typed slice. Each entry must be a map with `owner`, `spender`, and
// `allowance`. Duplicate (owner, spender) pairs are rejected; an
// allowance owner does NOT need to have a balance entry.
func parseExplicitAllowances(v any) ([]explicitAllowance, error) {
	if v == nil {
		return nil, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("erc20: allowances must be a list (got %T)", v)
	}
	out := make([]explicitAllowance, 0, len(list))
	type ownerSpender struct{ owner, spender common.Address }
	seen := make(map[ownerSpender]int, len(list))
	for i, entry := range list {
		m, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("erc20: allowances[%d] must be a map with `owner`, `spender`, `allowance` (got %T)", i, entry)
		}
		for k := range m {
			if k != "owner" && k != "spender" && k != "allowance" {
				return nil, fmt.Errorf("erc20: allowances[%d] has unknown key %q (expected `owner`, `spender`, `allowance`)", i, k)
			}
		}
		ownerAny, hasO := m["owner"]
		spenderAny, hasS := m["spender"]
		amtAny, hasA := m["allowance"]
		if !hasO {
			return nil, fmt.Errorf("erc20: allowances[%d] missing `owner`", i)
		}
		if !hasS {
			return nil, fmt.Errorf("erc20: allowances[%d] missing `spender`", i)
		}
		if !hasA {
			return nil, fmt.Errorf("erc20: allowances[%d] missing `allowance`", i)
		}
		owner, err := parseHexAddressParam(ownerAny, fmt.Sprintf("allowances[%d].owner", i))
		if err != nil {
			return nil, err
		}
		spender, err := parseHexAddressParam(spenderAny, fmt.Sprintf("allowances[%d].spender", i))
		if err != nil {
			return nil, err
		}
		key := ownerSpender{owner, spender}
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("erc20: allowances[%d] (owner=%s spender=%s) duplicates allowances[%d]",
				i, owner.Hex(), spender.Hex(), prev)
		}
		amt, err := parseUint256Param(amtAny, fmt.Sprintf("allowances[%d].allowance", i))
		if err != nil {
			return nil, err
		}
		seen[key] = i
		out = append(out, explicitAllowance{Owner: owner, Spender: spender, Amount: amt})
	}
	return out, nil
}

// balanceSlotKey computes the storage slot key for `_balances[holder]`
// using Solidity's mapping rule: keccak256(leftPad32(holder) || leftPad32(0)).
func balanceSlotKey(holder common.Address) common.Hash {
	var buf [64]byte
	copy(buf[12:32], holder[:])
	// buf[32..63] zero — slot index 0 left-padded
	return crypto.Keccak256Hash(buf[:])
}

// allowanceSlotKey computes the storage slot key for
// `_allowances[owner][spender]` using Solidity's nested-mapping rule:
//
//	inner = keccak256(leftPad32(owner)   || leftPad32(1))   // slot of _allowances[owner]
//	slot  = keccak256(leftPad32(spender) || inner)
func allowanceSlotKey(owner, spender common.Address) common.Hash {
	var innerBuf [64]byte
	copy(innerBuf[12:32], owner[:])
	binary.BigEndian.PutUint64(innerBuf[56:64], erc20SlotAllowances)
	inner := crypto.Keccak256(innerBuf[:])

	var outerBuf [64]byte
	copy(outerBuf[12:32], spender[:])
	copy(outerBuf[32:64], inner)
	return crypto.Keccak256Hash(outerBuf[:])
}

// deterministicRandomBalance produces the synthesized balance value for
// the i-th random holder of an ERC-20 contract. The value is in
// `[1, 10^18]` wei, derived from `keccak256(seed || tokenAddr || "BAL" || i)`.
// Deterministic across machines (cross-client invariant relies on this).
func deterministicRandomBalance(seed int64, tokenAddr common.Address, i int) common.Hash {
	const tag = "BAL"
	var preimage [8 + common.AddressLength + len(tag) + 8]byte
	binary.BigEndian.PutUint64(preimage[:8], uint64(seed))
	copy(preimage[8:8+common.AddressLength], tokenAddr[:])
	copy(preimage[8+common.AddressLength:8+common.AddressLength+len(tag)], tag)
	binary.BigEndian.PutUint64(preimage[8+common.AddressLength+len(tag):], uint64(i))
	h := crypto.Keccak256(preimage[:])
	u := binary.BigEndian.Uint64(h[24:32]) % 1_000_000_000_000_000_000
	if u == 0 {
		u = 1
	}
	return uint256.NewInt(u).Bytes32()
}

// deterministicRandomAlwAddress produces the synthesized owner or
// spender address for the i-th random allowance entry. tag is "AOW"
// (owner) or "ASP" (spender) so owner and spender derive from disjoint
// preimage spaces — a random owner won't collide with a random spender
// at the same index.
func deterministicRandomAlwAddress(seed int64, tokenAddr common.Address, tag string, i int) common.Address {
	if len(tag) != 3 {
		panic(fmt.Sprintf("deterministicRandomAlwAddress: tag must be 3 bytes (got %q)", tag))
	}
	var preimage [8 + common.AddressLength + 3 + 8]byte
	binary.BigEndian.PutUint64(preimage[:8], uint64(seed))
	copy(preimage[8:8+common.AddressLength], tokenAddr[:])
	copy(preimage[8+common.AddressLength:8+common.AddressLength+3], tag)
	binary.BigEndian.PutUint64(preimage[8+common.AddressLength+3:], uint64(i))
	h := crypto.Keccak256(preimage[:])
	var addr common.Address
	copy(addr[:], h[12:])
	return addr
}

// deterministicRandomAllowanceAmount: keccak256(seed||token||"AAM"||i),
// mod 10^18, clamped to [1, 10^18].
func deterministicRandomAllowanceAmount(seed int64, tokenAddr common.Address, i int) common.Hash {
	const tag = "AAM"
	var preimage [8 + common.AddressLength + len(tag) + 8]byte
	binary.BigEndian.PutUint64(preimage[:8], uint64(seed))
	copy(preimage[8:8+common.AddressLength], tokenAddr[:])
	copy(preimage[8+common.AddressLength:8+common.AddressLength+len(tag)], tag)
	binary.BigEndian.PutUint64(preimage[8+common.AddressLength+len(tag):], uint64(i))
	h := crypto.Keccak256(preimage[:])
	u := binary.BigEndian.Uint64(h[24:32]) % 1_000_000_000_000_000_000
	if u == 0 {
		u = 1
	}
	return uint256.NewInt(u).Bytes32()
}

// erc20RandomAllowancesIter emits a deterministic stream of synthesized
// `_allowances[owner][spender]` entries. Owner and spender are derived
// independently per index so they're effectively random and disjoint.
// Re-iteration is safe (pure function of seed/tokenAddr/count).
func erc20RandomAllowancesIter(seed int64, tokenAddr common.Address, count int) iter.Seq2[common.Hash, common.Hash] {
	return func(yield func(common.Hash, common.Hash) bool) {
		for i := range count {
			owner := deterministicRandomAlwAddress(seed, tokenAddr, "AOW", i)
			spender := deterministicRandomAlwAddress(seed, tokenAddr, "ASP", i)
			amount := deterministicRandomAllowanceAmount(seed, tokenAddr, i)
			if !yield(allowanceSlotKey(owner, spender), amount) {
				return
			}
		}
	}
}
