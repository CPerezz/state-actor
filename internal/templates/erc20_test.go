package templates

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/nerolation/state-actor/internal/spec"
)

func TestERC20ValidateParameters(t *testing.T) {
	tmpl := erc20Template{}
	cases := []struct {
		name   string
		params map[string]any
		valid  bool
	}{
		{
			name:   "complete",
			params: map[string]any{"symbol": "USDC", "name": "USD Coin", "decimals": 6},
			valid:  true,
		},
		{
			name:   "missing-symbol",
			params: map[string]any{"name": "x", "decimals": 18},
		},
		{
			name:   "missing-name",
			params: map[string]any{"symbol": "x", "decimals": 18},
		},
		{
			name:   "missing-decimals",
			params: map[string]any{"symbol": "x", "name": "x"},
		},
		{
			name:   "decimals-out-of-range",
			params: map[string]any{"symbol": "x", "name": "x", "decimals": 999},
		},
		{
			name:   "decimals-wrong-type",
			params: map[string]any{"symbol": "x", "name": "x", "decimals": "18"},
		},
		{
			name:   "symbol-too-long",
			params: map[string]any{"symbol": "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567", "name": "x", "decimals": 18},
		},
		{
			name:   "name-too-long",
			params: map[string]any{"symbol": "x", "name": "ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567", "decimals": 18},
		},
		{
			name:   "unknown-key",
			params: map[string]any{"symbol": "x", "name": "x", "decimals": 18, "supply": 1_000_000},
		},
		{
			name:   "holders-int-ok",
			params: map[string]any{"symbol": "x", "name": "x", "decimals": 18, "holders": 1000},
			valid:  true,
		},
		{
			name:   "holders-bad-type",
			params: map[string]any{"symbol": "x", "name": "x", "decimals": 18, "holders": "1000"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tmpl.ValidateParameters(tc.params)
			if tc.valid && err != nil {
				t.Errorf("expected pass, got %v", err)
			}
			if !tc.valid && err == nil {
				t.Errorf("expected fail, got nil")
			}
		})
	}
}

func TestERC20StorageLayout(t *testing.T) {
	addr := common.HexToAddress("0x0000000000000000000000000000000000000006")
	ent := spec.Entity{
		Kind:     spec.KindContract,
		Template: "erc20",
		Parameters: map[string]any{
			"symbol": "TEST", "name": "TestToken", "decimals": 18,
			"holders": 3,
		},
	}
	ctx := Context{
		Seed: 1, ClientName: "geth",
		Sizer:           fixedSizer{bytesPerSlot: 64},
		ResolvedAddress: addr,
	}

	out, err := erc20Template{}.Expand(ctx, ent)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	storage := collectMap(out[0].Storage)

	// _name slot — short-string layout for "TestToken" (9 bytes → 0x12 at byte 31).
	nameSlot := storage[uint64SlotKey(erc20SlotName)]
	if nameSlot[31] != 18 { // 9 chars × 2 = 18
		t.Errorf("name slot length byte = 0x%02x, want 0x12", nameSlot[31])
	}
	if !bytes.HasPrefix(nameSlot[:], []byte("TestToken")) {
		t.Errorf("name slot prefix wrong: %x", nameSlot)
	}

	// _symbol slot — "TEST" (4 bytes → 0x08).
	symSlot := storage[uint64SlotKey(erc20SlotSymbol)]
	if symSlot[31] != 8 {
		t.Errorf("symbol slot length byte = 0x%02x, want 0x08", symSlot[31])
	}

	// _totalSupply slot — holderCount=3, balance=1/each → totalSupply=3.
	tsSlot := storage[uint64SlotKey(erc20SlotTotalSupply)]
	if tsSlot[31] != 3 {
		t.Errorf("totalSupply = %x, want 3", tsSlot)
	}

	// _balances mapping — 3 holder entries. Storage map has 3 explicit slots
	// (name/symbol/totalSupply) + 3 synthesized balance slots = 6 total.
	if len(storage) != 6 {
		t.Errorf("storage entry count = %d, want 6", len(storage))
	}
}

func TestERC20BalancesSlotComputationMatchesSolidity(t *testing.T) {
	// Solidity rule: slot(mapping[k]) = keccak256(abi.encode(k, mappingSlot))
	// where k is the address (left-padded to 32 bytes) and mappingSlot is
	// the slot index (left-padded to 32 bytes).
	//
	// Verify our erc20BalancesIter produces a key for the first synthesized
	// holder that matches what Solidity would compute for that holder.
	addr := common.HexToAddress("0x0000000000000000000000000000000000000007")
	const seed = int64(99)
	const count = 1
	pairs := collectPairs(erc20BalancesIter(seed, addr, count))
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}

	// Reconstruct the holder address the iterator would have used.
	var preimage [8 + common.AddressLength + 8]byte
	preimage[0] = 0
	preimage[1] = 0
	preimage[2] = 0
	preimage[3] = 0
	preimage[4] = 0
	preimage[5] = 0
	preimage[6] = 0
	preimage[7] = byte(seed) // seed=99 fits in one byte
	copy(preimage[8:8+common.AddressLength], addr[:])
	// last 8 bytes are zero for i=0
	holderHashFull := crypto.Keccak256(preimage[:])

	// Compute the expected mapping slot key the Solidity way.
	var expected [64]byte
	copy(expected[12:32], holderHashFull[12:]) // address left-padded
	// slot index 0 already zero-filled at 32..63
	expectedKey := crypto.Keccak256Hash(expected[:])

	if pairs[0].K != expectedKey {
		t.Errorf("balance slot key:\n got  %s\n want %s", pairs[0].K.Hex(), expectedKey.Hex())
	}
}

// TestERC20BalancesSlotComputationManyHolders extends the single-holder
// Solidity-equivalence check to multiple holders so a buffer-reuse bug
// or off-by-one in the iterator's index mutation (erc20.go:185) would
// surface. Each holder's _balances[h] slot must equal Solidity's
// keccak256(pad32(h) || pad32(0)) rule.
func TestERC20BalancesSlotComputationManyHolders(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000beef")
	const seed = int64(99)
	const count = 25

	pairs := collectPairs(erc20BalancesIter(seed, addr, count))
	if len(pairs) != count {
		t.Fatalf("got %d pairs, want %d", len(pairs), count)
	}

	// Per-iteration: re-derive the expected slot key from scratch using
	// the iterator's input recipe.
	var preimage [8 + common.AddressLength + 8]byte
	preimage[7] = byte(seed) // seed=99 fits in one byte
	copy(preimage[8:8+common.AddressLength], addr[:])

	var mapKey [64]byte // leftPad32(holder) || leftPad32(slot=0)
	for i := 0; i < count; i++ {
		// Index in the trailing 8 bytes of preimage.
		for j := 0; j < 7; j++ {
			preimage[8+common.AddressLength+j] = 0
		}
		preimage[8+common.AddressLength+7] = byte(i)
		holderHash := crypto.Keccak256(preimage[:])

		// Build mapKey: clear holder bytes then copy holder address (right-12).
		for j := 0; j < 32; j++ {
			mapKey[j] = 0
		}
		copy(mapKey[12:32], holderHash[12:])
		expected := crypto.Keccak256Hash(mapKey[:])

		if pairs[i].K != expected {
			t.Errorf("holder %d slot mismatch:\n got  %s\n want %s",
				i, pairs[i].K.Hex(), expected.Hex())
		}
	}
}

// TestERC20NonceHonorsUserValue pins the v1 contract about nonce:
// user-supplied nonce wins, but nonce=0 (the unset YAML default) floors
// to nonce=1 per EIP-161.
func TestERC20NonceHonorsUserValue(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000aaaa")
	ctx := Context{Sizer: fixedSizer{bytesPerSlot: 64}, ResolvedAddress: addr}

	cases := []struct {
		name     string
		setNonce uint64
		want     uint64
	}{
		{"unset (default 1)", 0, 1},
		{"explicit-1", 1, 1},
		{"explicit-42", 42, 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ent := spec.Entity{
				Kind:     spec.KindContract,
				Template: "erc20",
				Nonce:    tc.setNonce,
				Parameters: map[string]any{
					"symbol": "X", "name": "X", "decimals": 18,
				},
			}
			out, err := erc20Template{}.Expand(ctx, ent)
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if got := out[0].Account.Nonce; got != tc.want {
				t.Errorf("nonce: got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestERC20RuntimeBytecodePinned(t *testing.T) {
	// v1 ships a stub. When the bytecode is swapped to real OZ v5, this
	// test's expected hash will change — that's intentional, the swap is
	// a known event.
	want := "bc36789e7a1e281436464229828f817d6612f7b477d66591ff96a9e064bcc98a" // keccak256(0x00) — v1 stub
	got := hex.EncodeToString(crypto.Keccak256(ERC20RuntimeBytecode))
	if got != want {
		t.Errorf("ERC20RuntimeBytecode keccak256 changed:\n got  %s\n want %s\n(intentional bytecode swap? update this test)", got, want)
	}
}
