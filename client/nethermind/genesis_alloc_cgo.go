//go:build cgo_neth

package nethermind

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/internal/neth"
	nethrlp "github.com/nerolation/state-actor/internal/neth/rlp"
	nethstorage "github.com/nerolation/state-actor/internal/neth/storage"
	nethtrie "github.com/nerolation/state-actor/internal/neth/trie"
)

// stateDBSink writes state-trie nodes to the State DB using HalfPath keys.
// This is the bridge between B4's trie.Builder (which emits OnTrieNode
// callbacks) and the State RocksDB Nethermind reads on boot.
type stateDBSink struct {
	db *grocksdb.DB
	wo *grocksdb.WriteOptions
}

func newStateDBSink(db *grocksdb.DB) *stateDBSink {
	return &stateDBSink{db: db, wo: grocksdb.NewDefaultWriteOptions()}
}

func (s *stateDBSink) close() {
	if s.wo != nil {
		s.wo.Destroy()
		s.wo = nil
	}
}

func (s *stateDBSink) SetStateNode(path []byte, pathLen int, keccak [32]byte, rlpBlob []byte) error {
	key := nethstorage.StateNodeKey(path, pathLen, keccak)
	return s.db.Put(s.wo, key, rlpBlob)
}

// SetStorageNode is unsupported in this minimal Phase B — the genesis-alloc
// path doesn't yet write per-account storage tries. Returning an error
// surfaces the gap loudly if a caller tries to use it.
func (s *stateDBSink) SetStorageNode(addrHash [32]byte, path []byte, pathLen int, keccak [32]byte, rlpBlob []byte) error {
	return errors.New("storage trie not supported in Phase B genesis-alloc path")
}

// writeGenesisAllocAccounts walks the genesis allocation, writes each
// account's leaf into the state trie via trie.Builder, and returns the
// computed state root. Code bytes (if any) go into the code DB keyed by
// keccak(code).
//
// Accounts MUST be processed in keccak(address) ascending order — that's
// what the StackTrie inside Builder requires. We sort them up-front.
//
// Storage slots in the genesis alloc are NOT written here; this is Phase B
// scaffolding that's enough to fund a few dev wallets so Nethermind's dev
// mode can mine txs. Full storage support arrives with the entitygen path.
func writeGenesisAllocAccounts(dbs *nethDBs, accounts map[common.Address]*types.StateAccount, codes map[common.Address][]byte) (common.Hash, error) {
	if len(accounts) == 0 {
		return common.Hash(neth.EmptyTreeHash), nil
	}

	sink := newStateDBSink(dbs.state)
	defer sink.close()

	builder := nethtrie.NewBuilder(sink)

	type addrEntry struct {
		addrHash [32]byte
		addr     common.Address
	}

	entries := make([]addrEntry, 0, len(accounts))
	for addr := range accounts {
		var ah [32]byte
		copy(ah[:], crypto.Keccak256(addr.Bytes()))
		entries = append(entries, addrEntry{addrHash: ah, addr: addr})
	}
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].addrHash[:], entries[j].addrHash[:]) < 0
	})

	codeWO := grocksdb.NewDefaultWriteOptions()
	defer codeWO.Destroy()

	for _, e := range entries {
		acc := accounts[e.addr]

		// Write code DB if the account has bytecode. Nethermind reads code
		// by keccak(code), so the key here mirrors what the EVM looks up.
		if code := codes[e.addr]; len(code) > 0 {
			codeHash := crypto.Keccak256Hash(code)
			if err := dbs.code.Put(codeWO, codeHash[:], code); err != nil {
				return common.Hash{}, fmt.Errorf("write code for %s: %w", e.addr.Hex(), err)
			}
			acc.CodeHash = codeHash[:]
		}

		accRLP, err := nethrlp.EncodeAccount(acc)
		if err != nil {
			return common.Hash{}, fmt.Errorf("encode account %s: %w", e.addr.Hex(), err)
		}
		if err := builder.AddAccount(e.addrHash, accRLP); err != nil {
			return common.Hash{}, fmt.Errorf("add account %s: %w", e.addr.Hex(), err)
		}
	}

	root, err := builder.FinalizeStateRoot()
	if err != nil {
		return common.Hash{}, fmt.Errorf("finalize state root: %w", err)
	}
	return common.Hash(root), nil
}
