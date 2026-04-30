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
