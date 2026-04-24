package reth

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/nerolation/state-actor/generator"
)

// writeDump streams every generated account to accountsOut as one JSONL line
// per account. In parallel, it feeds each account's (addrHash, RLP(account))
// pair to an in-memory slice; after all accounts are written, that slice is
// sorted by addrHash and fed to a hexary StackTrie to compute the authoritative
// MPT state root.
//
// The caller is responsible for prepending a {"root":"0x.."} header line to
// the final dump file — `reth init-state` requires that the dump start with
// the state root.
//
// Memory profile:
//   - Storage slots for ONE contract at a time (bounded by cfg.MaxSlots).
//   - All (addrHash, RLP) pairs for ALL accounts: ~100 bytes/acct. At 24M
//     accounts this is ~2.4 GB — well within the 32 GB reth init footprint.
//
// Entity order:
//  1. genesis alloc (from cfg.GenesisAccounts, sorted by addr)
//  2. --inject-accounts entries (flag order)
//  3. --accounts EOAs
//  4. --contracts contract accounts
func writeDump(cfg generator.Config, accountsOut io.Writer) (common.Hash, generator.Stats, error) {
	var stats generator.Stats
	src := newEntitySource(cfg)
	bw := bufio.NewWriterSize(accountsOut, 1<<20)

	type acctEntry struct {
		AddrHash common.Hash
		FullRLP  []byte
	}
	est := cfg.NumAccounts + cfg.NumContracts + len(cfg.GenesisAccounts) + len(cfg.InjectAddresses)
	entries := make([]acctEntry, 0, est)

	appendAccount := func(ad *accountData) error {
		if err := writeAccountJSON(bw, ad); err != nil {
			return fmt.Errorf("write account %s: %w", ad.address.Hex(), err)
		}
		rlpBytes, err := rlp.EncodeToBytes(ad.account)
		if err != nil {
			return fmt.Errorf("rlp encode %s: %w", ad.address.Hex(), err)
		}
		entries = append(entries, acctEntry{AddrHash: ad.addrHash, FullRLP: rlpBytes})
		return nil
	}

	for _, ad := range src.genesisAccounts() {
		if err := finalizeStorageRoot(ad); err != nil {
			return common.Hash{}, stats, err
		}
		if err := appendAccount(ad); err != nil {
			return common.Hash{}, stats, err
		}
		if len(ad.code) > 0 || len(ad.storage) > 0 {
			stats.ContractsCreated++
		} else {
			stats.AccountsCreated++
		}
		stats.StorageSlotsCreated += len(ad.storage)
	}
	for _, ad := range src.injectedAccounts() {
		if err := appendAccount(ad); err != nil {
			return common.Hash{}, stats, err
		}
		stats.AccountsCreated++
	}
	for i := 0; i < cfg.NumAccounts; i++ {
		ad := src.nextEOA()
		if err := appendAccount(ad); err != nil {
			return common.Hash{}, stats, err
		}
		stats.AccountsCreated++
		if len(stats.SampleEOAs) < 3 {
			stats.SampleEOAs = append(stats.SampleEOAs, ad.address)
		}
	}
	for i := 0; i < cfg.NumContracts; i++ {
		numSlots := src.nextSlotCount()
		ad := src.nextContract(numSlots)
		if err := finalizeStorageRoot(ad); err != nil {
			return common.Hash{}, stats, err
		}
		if err := appendAccount(ad); err != nil {
			return common.Hash{}, stats, err
		}
		stats.ContractsCreated++
		stats.StorageSlotsCreated += numSlots
		if len(stats.SampleContracts) < 3 {
			stats.SampleContracts = append(stats.SampleContracts, ad.address)
		}
	}

	if err := bw.Flush(); err != nil {
		return common.Hash{}, stats, fmt.Errorf("flush dump: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].AddrHash[:], entries[j].AddrHash[:]) < 0
	})
	acctTrie := trie.NewStackTrie(nil)
	for _, e := range entries {
		acctTrie.Update(e.AddrHash[:], e.FullRLP)
	}
	root := acctTrie.Hash()
	stats.StateRoot = root
	return root, stats, nil
}

// writeFinalDump prepends the {"root":"0x.."} header line to accountsTempPath
// and writes the combined output to outPath. Uses io.Copy so the full dump
// never sits in memory (important at 400 GB+ scale).
func writeFinalDump(outPath, accountsTempPath string, root common.Hash) error {
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create dump file: %w", err)
	}
	defer out.Close()
	bw := bufio.NewWriterSize(out, 1<<20)
	if _, err := fmt.Fprintf(bw, `{"root":"0x%s"}`+"\n", hex.EncodeToString(root[:])); err != nil {
		return fmt.Errorf("write root header: %w", err)
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	in, err := os.Open(accountsTempPath)
	if err != nil {
		return fmt.Errorf("open accounts temp: %w", err)
	}
	defer in.Close()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy accounts: %w", err)
	}
	return nil
}

// writeAccountJSON emits one JSONL line in alloy_genesis::GenesisAccount
// serde shape: {"address","balance","nonce","code","storage"}. Optional
// fields are omitted when zero/empty (matches serde's skip_serializing_if).
func writeAccountJSON(w *bufio.Writer, ad *accountData) error {
	if _, err := w.WriteString(`{"address":"0x`); err != nil {
		return err
	}
	if _, err := w.WriteString(hex.EncodeToString(ad.address[:])); err != nil {
		return err
	}
	if _, err := w.WriteString(`","balance":"`); err != nil {
		return err
	}
	if _, err := w.WriteString(ad.account.Balance.Hex()); err != nil {
		return err
	}
	if err := w.WriteByte('"'); err != nil {
		return err
	}
	if ad.account.Nonce > 0 {
		if _, err := fmt.Fprintf(w, `,"nonce":"0x%x"`, ad.account.Nonce); err != nil {
			return err
		}
	}
	if len(ad.code) > 0 {
		if _, err := w.WriteString(`,"code":"0x`); err != nil {
			return err
		}
		if _, err := w.WriteString(hex.EncodeToString(ad.code)); err != nil {
			return err
		}
		if err := w.WriteByte('"'); err != nil {
			return err
		}
	}
	if len(ad.storage) > 0 {
		if _, err := w.WriteString(`,"storage":{`); err != nil {
			return err
		}
		for i, s := range ad.storage {
			if i > 0 {
				if err := w.WriteByte(','); err != nil {
					return err
				}
			}
			if _, err := w.WriteString(`"0x`); err != nil {
				return err
			}
			if _, err := w.WriteString(hex.EncodeToString(s.Key[:])); err != nil {
				return err
			}
			if _, err := w.WriteString(`":"0x`); err != nil {
				return err
			}
			if _, err := w.WriteString(hex.EncodeToString(s.Value[:])); err != nil {
				return err
			}
			if err := w.WriteByte('"'); err != nil {
				return err
			}
		}
		if err := w.WriteByte('}'); err != nil {
			return err
		}
	}
	if _, err := w.WriteString("}\n"); err != nil {
		return err
	}
	return nil
}

// --- Helpers shared with the chainspec alloc path -----------------------
// (finalizeStorageRoot, encodeStorageValue, trimLeftZeroes are kept here so
// the storage-root computation stays local to statedump.go)

// finalizeStorageRoot computes the storage root for ad.storage and writes it
// into ad.account.Root. Re-sorts storage slots by keccak256(key) as required
// by the MPT StackTrie. No-op when storage is empty.
func finalizeStorageRoot(ad *accountData) error {
	if len(ad.storage) == 0 {
		ad.account.Root = types.EmptyRootHash
		return nil
	}
	type hashedSlot struct {
		keyHash common.Hash
		value   common.Hash
	}
	sorted := make([]hashedSlot, len(ad.storage))
	for i, s := range ad.storage {
		sorted[i] = hashedSlot{
			keyHash: crypto.Keccak256Hash(s.Key[:]),
			value:   s.Value,
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].keyHash[:], sorted[j].keyHash[:]) < 0
	})
	st := trie.NewStackTrie(nil)
	for _, s := range sorted {
		rlpVal, err := encodeStorageValue(s.value)
		if err != nil {
			return err
		}
		if len(rlpVal) > 0 {
			st.Update(s.keyHash[:], rlpVal)
		}
	}
	ad.account.Root = st.Hash()
	return nil
}

func encodeStorageValue(v common.Hash) ([]byte, error) {
	trimmed := trimLeftZeroes(v[:])
	if len(trimmed) == 0 {
		return nil, nil
	}
	return rlp.EncodeToBytes(trimmed)
}

func trimLeftZeroes(s []byte) []byte {
	for i, v := range s {
		if v != 0 {
			return s[i:]
		}
	}
	return nil
}
