package besu

import (
	"bytes"
	"math/big"
	"sort"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"
)

// TestCanonicalGenesisNonceBlockHash builds the genesisNonce.json genesis
// header by hand using go-ethereum's standard types.Header + trie.StackTrie
// and asserts the block hash matches Besu's GenesisStateTest.java:157-159
// pinned value 0x36750291f1a8429aeb553a790dc2d149d04dbba0ca4cfc7fd5eb12d478117c9f.
//
// This is the reference oracle that lets us bisect a header-encoding bug
// without depending on cgo/grocksdb. If this test passes, we know:
//   - The fixture values map cleanly to types.Header fields
//   - go-ethereum's RLP encoding of *types.Header matches Besu's BlockHeader RLP
//   - The expected stateRoot is what we compute for the alloc
//
// If THIS passes but client/besu's writer fails, the bug is in our writer's
// header construction (mis-set field). If THIS fails, the bug is in the
// canonical computation itself (wrong stateRoot, wrong field interpretation).
func TestCanonicalGenesisNonceBlockHash(t *testing.T) {
	// --- Build alloc state root ---
	type alloc struct {
		addr     common.Address
		nonce    uint64
		balance  *big.Int
		code     []byte
		storages [][2]common.Hash
	}
	allocs := []alloc{
		{
			addr:    common.HexToAddress("0xa94f5374fce5edbc8e2a8697c15331677e6ebf0b"),
			nonce:   0,
			balance: hexBig("0x0de0b6b3a7640000"),
		},
		{
			addr:    common.HexToAddress("0xec0e71ad0a90ffe1909d27dac207f7680abba42d"),
			nonce:   3,
			balance: hexBig("0x01"),
			code:    mustDecode("0x6010ff"),
		},
	}

	type acctEntry struct {
		addrHash common.Hash
		rlp      []byte
	}
	var accts []acctEntry
	for _, a := range allocs {
		balU := uint256.MustFromBig(a.balance)
		var codeHash common.Hash
		if len(a.code) == 0 {
			codeHash = types.EmptyCodeHash
		} else {
			codeHash = crypto.Keccak256Hash(a.code)
		}
		acc := &types.StateAccount{
			Nonce:    a.nonce,
			Balance:  balU,
			Root:     types.EmptyRootHash,
			CodeHash: codeHash.Bytes(),
		}
		buf, err := gethrlp.EncodeToBytes(acc)
		if err != nil {
			t.Fatalf("encode acct %s: %v", a.addr.Hex(), err)
		}
		accts = append(accts, acctEntry{
			addrHash: crypto.Keccak256Hash(a.addr[:]),
			rlp:      buf,
		})
	}
	sort.Slice(accts, func(i, j int) bool {
		return bytes.Compare(accts[i].addrHash[:], accts[j].addrHash[:]) < 0
	})
	st := trie.NewStackTrie(nil)
	for _, a := range accts {
		st.Update(a.addrHash[:], a.rlp)
	}
	stateRoot := st.Hash()
	t.Logf("canonical stateRoot for genesisNonce alloc: %s", stateRoot.Hex())

	// --- Build header from fixture values ---
	header := &types.Header{
		ParentHash:  common.Hash{},
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.HexToAddress("0x8888f1f195afa192cfee860698584c030f4c9db1"),
		Root:        stateRoot,
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Bloom:       types.Bloom{},
		Difficulty:  hexBig("0x020000"),
		Number:      big.NewInt(0),
		GasLimit:    0x2fefd8,
		GasUsed:     0,
		Time:        0x54c98c81,
		Extra:       []byte{0x42},
		MixDigest:   common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		Nonce:       types.EncodeNonce(0x0102030405060708),
	}

	got := header.Hash()
	const want = "0x36750291f1a8429aeb553a790dc2d149d04dbba0ca4cfc7fd5eb12d478117c9f"
	if got.Hex() != want {
		t.Fatalf("canonical genesisNonce blockHash mismatch:\n  got:  %s\n  want: %s\n  stateRoot: %s\n  TxHash:%s\n  ReceiptHash:%s\n  UncleHash:%s",
			got.Hex(), want, stateRoot.Hex(),
			header.TxHash.Hex(), header.ReceiptHash.Hex(), header.UncleHash.Hex())
	}
}

func hexBig(s string) *big.Int {
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	v, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("hexBig: bad input " + s)
	}
	return v
}

func mustDecode(s string) []byte {
	b, err := hexutil.Decode(s)
	if err != nil {
		panic(err)
	}
	return b
}
