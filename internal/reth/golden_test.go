package reth

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

type fixture struct {
	Label    string `json:"label"`
	TypeName string `json:"type_name"`
	Hex      string `json:"hex"`
}

func loadFixtures(t *testing.T) map[string][]fixture {
	t.Helper()
	data, err := os.ReadFile("testdata/fixtures.json")
	if err != nil {
		t.Fatalf("read fixtures.json: %v (regenerate via testdata/gen/)", err)
	}
	var grouped map[string][]fixture
	if err := json.Unmarshal(data, &grouped); err != nil {
		t.Fatalf("parse fixtures.json: %v", err)
	}
	return grouped
}

func TestGoldenU64(t *testing.T) {
	for _, fx := range loadFixtures(t)["u64"] {
		hexNum := strings.TrimPrefix(fx.Label, "u64_")
		v, err := strconv.ParseUint(hexNum, 16, 64)
		if err != nil {
			t.Fatalf("parse label %q: %v", fx.Label, err)
		}
		var buf bytes.Buffer
		encodeU64Compact(&buf, v)
		got := hex.EncodeToString(buf.Bytes())
		if got != fx.Hex {
			t.Errorf("u64(%#x): go=%s rust=%s", v, got, fx.Hex)
		}
	}
}

func TestGoldenU256(t *testing.T) {
	for _, fx := range loadFixtures(t)["U256"] {
		hexNum := strings.TrimPrefix(fx.Label, "u256_")
		v, err := uint256.FromHex("0x" + hexNum)
		if err != nil {
			t.Fatalf("parse label %q: %v", fx.Label, err)
		}
		var buf bytes.Buffer
		encodeU256Compact(&buf, v)
		got := hex.EncodeToString(buf.Bytes())
		if got != fx.Hex {
			t.Errorf("U256(%s): go=%s rust=%s", hexNum, got, fx.Hex)
		}
	}
}

// TestGoldenIntegerList validates that Go's roaring64 produces bytes byte-compatible
// with Rust's RoaringTreemap (which backs reth's IntegerList).
func TestGoldenIntegerList(t *testing.T) {
	cases := loadFixtures(t)["IntegerList"]
	if len(cases) == 0 {
		t.Fatal("no IntegerList fixtures (regenerate via testdata/gen/)")
	}
	inputs := map[string][]uint64{
		"il_empty":  {},
		"il_single": {0},
		"il_small":  {0, 1, 2, 3},
		"il_sparse": {0, 100, 200, 0x12345678},
	}
	for _, fx := range cases {
		in, ok := inputs[fx.Label]
		if !ok {
			t.Fatalf("unknown fixture label %q — Rust and Go are out of sync", fx.Label)
		}
		var buf bytes.Buffer
		EncodeIntegerList(&buf, in)
		got := hex.EncodeToString(buf.Bytes())
		if got != fx.Hex {
			t.Errorf("IntegerList %s:\n  go   = %s\n  rust = %s", fx.Label, got, fx.Hex)
		}
	}
}

// TestGoldenStorageTrieEntry cross-validates Go StorageTrieEntry encoder output
// against Rust canonical hex (PackedStorageTrieEntry, storage v2, 33-byte subkey).
// If this fails, revise StorageTrieEntry.EncodeCompact in trie_format.go to match.
func TestGoldenStorageTrieEntry(t *testing.T) {
	cases := loadFixtures(t)["StorageTrieEntry"]
	if len(cases) == 0 {
		t.Fatal("no StorageTrieEntry fixtures (regenerate via testdata/gen/)")
	}
	h_a := common.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	h_b := common.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	inputs := map[string]StorageTrieEntry{
		"ste_minimal": {
			SubKey: StoredNibbles{Length: 0, Packed: common.Hash{}},
			Node:   BranchNodeCompact{StateMask: 0, TreeMask: 0, HashMask: 0, Hashes: nil, RootHash: nil},
		},
		"ste_basic": {
			SubKey: StoredNibbles{Length: 4, Packed: packNibbles([]byte{1, 2, 3, 4})},
			Node: BranchNodeCompact{
				StateMask: 0x0001, TreeMask: 0, HashMask: 0x0001,
				Hashes: []common.Hash{h_a}, RootHash: nil,
			},
		},
		"ste_with_root": {
			SubKey: StoredNibbles{Length: 8, Packed: packNibbles([]byte{1, 2, 3, 4, 5, 6, 7, 8})},
			Node: BranchNodeCompact{
				StateMask: 0x0003, TreeMask: 0x0002, HashMask: 0x0003,
				Hashes: []common.Hash{h_a, h_b}, RootHash: &h_b,
			},
		},
	}
	for _, fx := range cases {
		in, ok := inputs[fx.Label]
		if !ok {
			t.Fatalf("unknown fixture label %q — Rust and Go are out of sync", fx.Label)
		}
		var buf bytes.Buffer
		in.EncodeCompact(&buf)
		got := hex.EncodeToString(buf.Bytes())
		if got != fx.Hex {
			t.Errorf("StorageTrieEntry %s:\n  go   = %s\n  rust = %s", fx.Label, got, fx.Hex)
		}
	}
}

// unpackNibbles converts a byte slice to individual nibbles (each byte → 2 nibbles,
// high nibble first). Mirrors alloy_trie Nibbles::unpack.
func unpackNibbles(data []byte) []byte {
	nibbles := make([]byte, len(data)*2)
	for i, b := range data {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	return nibbles
}

// mustHexDecode decodes a hex string or fatals the test.
func mustHexDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("mustHexDecode(%q): %v", s, err)
	}
	return b
}

// hashBuilderLeaf is a (key, value) pair for HashBuilder input.
type hashBuilderLeaf struct{ key, value []byte }

// hashBuilderEmission is one (path, node) pair emitted by HashBuilder.
type hashBuilderEmission struct {
	path StoredNibbles
	node BranchNodeCompact
}

// hashBuilderLeavesByLabel returns the test-case leaves for the given fixture label.
// Definitions match the Rust harness cases byte-for-byte.
func hashBuilderLeavesByLabel(t *testing.T, label string) []hashBuilderLeaf {
	t.Helper()
	switch label {
	case "hb_single_root", "hb_single_emissions":
		return []hashBuilderLeaf{
			{key: bytes.Repeat([]byte{0xa0}, 32), value: bytes.Repeat([]byte{0x42}, 32)},
		}
	case "hb_two_shared_root", "hb_two_shared_emissions":
		k1 := bytes.Repeat([]byte{0xa0}, 32)
		k2 := bytes.Repeat([]byte{0xa0}, 32)
		k2[31] = 0xa1
		return []hashBuilderLeaf{
			{key: k1, value: []byte{0x01}},
			{key: k2, value: []byte{0x02}},
		}
	case "hb_three_top_root", "hb_three_top_emissions":
		k1 := make([]byte, 32)
		k2 := make([]byte, 32)
		k3 := make([]byte, 32)
		k1[0] = 0x10
		k2[0] = 0x20
		k3[0] = 0x30
		return []hashBuilderLeaf{
			{key: k1, value: []byte{0x01}},
			{key: k2, value: []byte{0x02}},
			{key: k3, value: []byte{0x03}},
		}
	case "hb_full_branch_root", "hb_full_branch_emissions":
		leaves := make([]hashBuilderLeaf, 16)
		for i := 0; i < 16; i++ {
			k := make([]byte, 32)
			k[0] = byte(i) << 4
			leaves[i] = hashBuilderLeaf{key: k, value: []byte{byte(i) + 1}}
		}
		return leaves
	default:
		t.Fatalf("hashBuilderLeavesByLabel: unknown label %q", label)
		return nil
	}
}

// encodeEmissionsForGoldenCompare encodes a slice of emissions into the same
// binary format the Rust harness writes into HashBuilderEmissions fixtures:
//
//	For each emission:
//	  path[33]: packed[32] || length[1]
//	  bnc_len[2]: big-endian u16
//	  bnc_bytes[bnc_len]: BranchNodeCompact compact encoding
func encodeEmissionsForGoldenCompare(emissions []hashBuilderEmission) []byte {
	var buf bytes.Buffer
	for _, e := range emissions {
		// Path: StoredNibbles wire form is packed[32] || length[1]
		buf.Write(e.path.Packed[:])
		buf.WriteByte(e.path.Length)
		var nodeBuf bytes.Buffer
		nLen := e.node.EncodeCompact(&nodeBuf)
		buf.WriteByte(byte(nLen >> 8))
		buf.WriteByte(byte(nLen))
		buf.Write(nodeBuf.Bytes())
	}
	return buf.Bytes()
}

// TestGoldenHashBuilderRoot cross-validates Go HashBuilder root output against
// the alloy_trie::HashBuilder canonical root in the Rust fixtures.
// Tests will fail/panic on non-trivial cases until Slice B's algorithm is implemented.
func TestGoldenHashBuilderRoot(t *testing.T) {
	cases := loadFixtures(t)["HashBuilderRoot"]
	if len(cases) == 0 {
		t.Fatal("no HashBuilderRoot fixtures (regenerate via testdata/gen/)")
	}
	for _, fx := range cases {
		fx := fx
		t.Run(fx.Label, func(t *testing.T) {
			want := mustHexDecode(t, fx.Hex)
			leaves := hashBuilderLeavesByLabel(t, fx.Label)
			var emissions []hashBuilderEmission
			emit := func(path StoredNibbles, node BranchNodeCompact) error {
				emissions = append(emissions, hashBuilderEmission{path: path, node: node})
				return nil
			}
			hb := NewHashBuilder(emit)
			for _, leaf := range leaves {
				// key bytes → nibble-unpacked: each byte becomes 2 nibbles
				nibbles := unpackNibbles(leaf.key)
				if err := hb.AddLeaf(nibbles, leaf.value); err != nil {
					t.Fatalf("AddLeaf: %v", err)
				}
			}
			got := hb.Root()
			if !bytes.Equal(got[:], want) {
				t.Errorf("root mismatch:\n  go   = %x\n  rust = %x", got[:], want)
			}
		})
	}
}

// TestGoldenHashBuilderEmissions cross-validates Go HashBuilder emission output
// against the alloy_trie::HashBuilder canonical emissions in the Rust fixtures.
// Tests will fail/panic on non-trivial cases until Slice B's algorithm is implemented.
func TestGoldenHashBuilderEmissions(t *testing.T) {
	cases := loadFixtures(t)["HashBuilderEmissions"]
	if len(cases) == 0 {
		t.Fatal("no HashBuilderEmissions fixtures (regenerate via testdata/gen/)")
	}
	for _, fx := range cases {
		fx := fx
		t.Run(fx.Label, func(t *testing.T) {
			want := mustHexDecode(t, fx.Hex)
			leaves := hashBuilderLeavesByLabel(t, fx.Label)
			var emissions []hashBuilderEmission
			emit := func(path StoredNibbles, node BranchNodeCompact) error {
				emissions = append(emissions, hashBuilderEmission{path: path, node: node})
				return nil
			}
			hb := NewHashBuilder(emit)
			for _, leaf := range leaves {
				nibbles := unpackNibbles(leaf.key)
				if err := hb.AddLeaf(nibbles, leaf.value); err != nil {
					t.Fatalf("AddLeaf: %v", err)
				}
			}
			_ = hb.Root() // finalizes any pending emissions
			got := encodeEmissionsForGoldenCompare(emissions)
			if !bytes.Equal(got, want) {
				t.Errorf("emissions mismatch:\n  go   = %x\n  rust = %x", got, want)
			}
		})
	}
}

// TestGoldenBranchNodeCompact validates our BNC wire format against Rust's.
// If this fails, revise BranchNodeCompact.EncodeCompact in trie_format.go to match.
func TestGoldenBranchNodeCompact(t *testing.T) {
	cases := loadFixtures(t)["BranchNodeCompact"]
	if len(cases) == 0 {
		t.Skip("no BranchNodeCompact fixtures (regenerate via testdata/gen/)")
	}
	h1 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	h2 := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	inputs := map[string]BranchNodeCompact{
		"bnc_minimal": {StateMask: 0, TreeMask: 0, HashMask: 0, Hashes: nil, RootHash: nil},
		"bnc_one_child": {
			StateMask: 0x0001, TreeMask: 0, HashMask: 0x0001,
			Hashes: []common.Hash{h1}, RootHash: nil,
		},
		"bnc_two_children_with_root": {
			StateMask: 0x0003, TreeMask: 0x0002, HashMask: 0x0003,
			Hashes: []common.Hash{h1, h2}, RootHash: &h1,
		},
	}
	for _, fx := range cases {
		in, ok := inputs[fx.Label]
		if !ok {
			t.Fatalf("unknown fixture label %q — Rust and Go are out of sync", fx.Label)
		}
		var buf bytes.Buffer
		in.EncodeCompact(&buf)
		got := hex.EncodeToString(buf.Bytes())
		if got != fx.Hex {
			t.Errorf("BNC %s:\n  go   = %s\n  rust = %s", fx.Label, got, fx.Hex)
		}
	}
}
