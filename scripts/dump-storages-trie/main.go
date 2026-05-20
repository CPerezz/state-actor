// dump-storages-trie reads the StoragesTrie MDBX table and prints the
// first N rows' decoded BranchNodeCompact masks. Used to forensically
// verify our writer's output against what reth reads at decode time.
//
// Usage:  go run ./scripts/dump-storages-trie -datadir /path/to/reth-data -n 10
//
// Build tag matches the reth client (cgo_reth) so mdbx-go has its libmdbx
// cgo deps satisfied.

//go:build cgo_reth

package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"

	iReth "github.com/nerolation/state-actor/internal/reth"
)

func main() {
	datadir := flag.String("datadir", "", "reth datadir (contains db/mdbx.dat)")
	n := flag.Int("n", 10, "max rows to print")
	flag.Parse()
	if *datadir == "" {
		log.Fatal("-datadir required")
	}

	env, err := mdbx.NewEnv()
	if err != nil {
		log.Fatalf("NewEnv: %v", err)
	}
	defer env.Close()
	if err := env.SetOption(mdbx.OptMaxDB, 64); err != nil {
		log.Fatalf("SetOption maxdb: %v", err)
	}
	if err := env.Open(filepath.Join(*datadir, "db"), mdbx.Readonly|mdbx.NoSubdir, 0o644); err != nil {
		log.Fatalf("Open: %v", err)
	}

	if err := env.View(func(txn *mdbx.Txn) error {
		dbi, err := txn.OpenDBISimple("StoragesTrie", mdbx.DupSort)
		if err != nil {
			return fmt.Errorf("open StoragesTrie: %w", err)
		}
		cur, err := txn.OpenCursor(dbi)
		if err != nil {
			return fmt.Errorf("open cursor: %w", err)
		}
		defer cur.Close()

		shown := 0
		k, v, err := cur.Get(nil, nil, mdbx.First)
		for ; err == nil && shown < *n; k, v, err = cur.Get(nil, nil, mdbx.Next) {
			fmt.Printf("\n--- row %d ---\n", shown)
			fmt.Printf("  mainkey (keccak(addr), %d bytes): %s\n", len(k), hex.EncodeToString(k))
			fmt.Printf("  value (%d bytes): %s\n", len(v), hex.EncodeToString(v))

			if len(v) < 65 {
				fmt.Printf("  ERROR: value too short for SubKey (%d < 65)\n", len(v))
				shown++
				continue
			}

			// Decode SubKey (v1 legacy 65-byte form: nibbles[64] || length[1])
			var subKey iReth.StoredNibblesSubKey
			subKey.DecodeKey(v[:65])
			fmt.Printf("  subkey.Length: %d nibbles\n", subKey.Length)
			fmt.Printf("  subkey.Nibbles[:16]: %s\n", hex.EncodeToString(subKey.Nibbles[:16]))

			// Decode BNC from bytes after SubKey
			bncBytes := v[65:]
			fmt.Printf("  bnc-bytes (%d): %s\n", len(bncBytes), hex.EncodeToString(bncBytes))

			if len(bncBytes) < 6 {
				fmt.Printf("  ERROR: bnc-bytes too short for masks (%d < 6)\n", len(bncBytes))
				shown++
				continue
			}

			stateMask := uint16(bncBytes[0])<<8 | uint16(bncBytes[1])
			treeMask := uint16(bncBytes[2])<<8 | uint16(bncBytes[3])
			hashMask := uint16(bncBytes[4])<<8 | uint16(bncBytes[5])

			fmt.Printf("  state_mask: 0x%04x = %016b\n", stateMask, stateMask)
			fmt.Printf("  tree_mask:  0x%04x = %016b\n", treeMask, treeMask)
			fmt.Printf("  hash_mask:  0x%04x = %016b\n", hashMask, hashMask)

			treeLeaks := treeMask &^ stateMask
			hashLeaks := hashMask &^ stateMask
			if treeLeaks != 0 || hashLeaks != 0 {
				fmt.Printf("  *** INVARIANT VIOLATION: tree_mask leaks 0x%04x, hash_mask leaks 0x%04x ***\n",
					treeLeaks, hashLeaks)
			} else {
				fmt.Printf("  invariant OK (tree ⊆ state, hash ⊆ state)\n")
			}

			// Optional: roundtrip through our decoder to confirm
			var entry iReth.StorageTrieEntry
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("  Go decoder panic: %v\n", r)
				}
			}()
			entry.DecodeCompact(v, len(v))
			_ = entry
			_ = bytes.NewBuffer

			shown++
		}
		fmt.Printf("\nTotal rows inspected: %d\n", shown)
		return nil
	}); err != nil {
		log.Fatalf("view: %v", err)
	}
}
