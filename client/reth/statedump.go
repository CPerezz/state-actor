package reth

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/nerolation/state-actor/generator"
)

// writeDump streams every account in s to accountsOut as a single JSONL line
// and returns the MPT state root computed from the same accounts. The caller
// is responsible for prefixing the final output file with {"root":"<hex>"}
// on line 1 — reth init-state rejects dumps that don't start with the root.
//
// Entity generation order:
//  1. genesis alloc accounts (sorted by address, from cfg.GenesisAccounts)
//  2. --inject-accounts entries (in flag order)
//  3. --accounts EOAs
//  4. --contracts contract accounts
//
// The state root is computed via go-ethereum's hexary StackTrie, fed in
// keccak256(address) order. Storage roots are computed inline per contract.
// This mirrors the MPT pipeline at generator/generator.go:101 so the
// per-account trie encoding is identical to what Reth expects.
//
// Returns the state root, count of accounts written, count of storage slots
// written.
func writeDump(cfg generator.Config, accountsOut io.Writer) (common.Hash, generator.Stats, error) {
	var stats generator.Stats
	src := newEntitySource(cfg)
	bw := bufio.NewWriterSize(accountsOut, 1<<20) // 1 MB buffer

	// Collect (addrHash, fullAccountRLP) for the account trie. Full RLP (not
	// SlimAccountRLP) is required here because StackTrie leaf values are
	// decoded as types.StateAccount with a fixed 32-byte Root field. See
	// generator/generator.go:209-218 for the same invariant.
	type acctEntry struct {
		AddrHash common.Hash
		FullRLP  []byte
	}
	// Preallocate optimistically for the typical sum; we accept slop when the
	// genesis alloc isn't empty.
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

	// 1. Genesis alloc.
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

	// 2. Injected addresses (e.g. Anvil's default account).
	for _, ad := range src.injectedAccounts() {
		if err := appendAccount(ad); err != nil {
			return common.Hash{}, stats, err
		}
		stats.AccountsCreated++
	}

	// 3. EOAs.
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

	// 4. Contracts.
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
		return common.Hash{}, stats, fmt.Errorf("flush accounts dump: %w", err)
	}

	// Account trie — feed entries sorted by addrHash.
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].AddrHash[:], entries[j].AddrHash[:]) < 0
	})
	acctTrie := trie.NewStackTrie(nil)
	for _, e := range entries {
		acctTrie.Update(e.AddrHash[:], e.FullRLP)
	}
	root := acctTrie.Hash()
	stats.StateRoot = root

	if cfg.Verbose {
		log.Printf("[reth] state root: %s (accounts=%d contracts=%d slots=%d)",
			root.Hex(), stats.AccountsCreated, stats.ContractsCreated, stats.StorageSlotsCreated)
	}

	return root, stats, nil
}

// finalizeStorageRoot computes the storage root for ad.storage and writes it
// into ad.account.Root. Also re-sorts storage slots by keccak256(key) as
// required by the MPT StackTrie. No-op when storage is empty.
func finalizeStorageRoot(ad *accountData) error {
	if len(ad.storage) == 0 {
		ad.account.Root = types.EmptyRootHash
		return nil
	}
	type hashedSlot struct {
		rawKey  common.Hash
		keyHash common.Hash
		value   common.Hash
	}
	sorted := make([]hashedSlot, len(ad.storage))
	for i, s := range ad.storage {
		sorted[i] = hashedSlot{
			rawKey:  s.Key,
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

// encodeStorageValue RLP-encodes a storage value with leading zero bytes
// trimmed. Zero values return nil bytes (caller must skip insertion).
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

// writeAccountJSON writes one GenesisAccountWithAddress-shaped JSON object
// followed by "\n" — the format reth init-state expects. Matches
// alloy_genesis::GenesisAccount's serde layout:
//
//	{"address":"0x..","balance":"0x..","nonce":"0x..","code":"0x..","storage":{...}}
//
// Fields omitted when zero/empty (matches serde's skip_serializing_if).
func writeAccountJSON(w *bufio.Writer, ad *accountData) error {
	if _, err := w.WriteString(`{"address":"0x`); err != nil {
		return err
	}
	if _, err := w.WriteString(hex.EncodeToString(ad.address[:])); err != nil {
		return err
	}
	// Balance — always present per alloy_genesis::GenesisAccount.
	if _, err := w.WriteString(`","balance":"`); err != nil {
		return err
	}
	// uint256.Int.Hex() returns "0x0" for zero or "0x..." trimmed
	// (no leading zeros beyond one). alloy's U256 deserializer accepts both.
	if _, err := w.WriteString(ad.account.Balance.Hex()); err != nil {
		return err
	}
	if _, err := w.WriteString(`"`); err != nil {
		return err
	}
	// Nonce — omitted when zero (serde Option<u64> skip_if).
	if ad.account.Nonce > 0 {
		if _, err := fmt.Fprintf(w, `,"nonce":"0x%x"`, ad.account.Nonce); err != nil {
			return err
		}
	}
	// Code — omitted when empty.
	if len(ad.code) > 0 {
		if _, err := w.WriteString(`,"code":"0x`); err != nil {
			return err
		}
		if _, err := w.WriteString(hex.EncodeToString(ad.code)); err != nil {
			return err
		}
		if _, err := w.WriteString(`"`); err != nil {
			return err
		}
	}
	// Storage — omitted when empty.
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
			if _, err := w.WriteString(`"`); err != nil {
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

// writeFinalDump concatenates the root header line and the accountsTempPath
// into outPath. Uses io.Copy to avoid loading the full dump into memory.
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
