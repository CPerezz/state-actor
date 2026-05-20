// dump-trie inspects the AccountsTrie + HashedAccounts + StoragesTrie tables
// for the v1 legacy 65-byte SubKey wire format. With -key set, it probes for
// duplicates / structural issues around one specific hashed_address.
//
// Build tag matches the reth client (cgo_reth) so mdbx-go has its libmdbx
// cgo deps satisfied.

//go:build cgo_reth

package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
)

func main() {
	datadir := flag.String("datadir", "", "reth datadir (contains db/mdbx.dat)")
	n := flag.Int("n", 5, "max rows to print per table")
	target := flag.String("key", "", "32-byte hex hashed_address to probe (e.g. 0x0000ac12...)")
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

	var targetBytes []byte
	if *target != "" {
		s := *target
		if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
			s = s[2:]
		}
		var err error
		targetBytes, err = hex.DecodeString(s)
		if err != nil || len(targetBytes) != 32 {
			log.Fatalf("-key must be 32 bytes hex, got %q (decode err=%v len=%d)", *target, err, len(targetBytes))
		}
	}

	if err := env.View(func(txn *mdbx.Txn) error {
		// Count rows in each table.
		for _, table := range []string{"HashedAccounts", "AccountsTrie", "StoragesTrie", "PlainAccountState", "PlainStorageState"} {
			dbi, err := txn.OpenDBISimple(table, 0)
			if err != nil {
				fmt.Printf("%s: open failed: %v\n", table, err)
				continue
			}
			stat, err := txn.StatDBI(dbi)
			if err != nil {
				fmt.Printf("%s: stat failed: %v\n", table, err)
				continue
			}
			fmt.Printf("%s: %d entries\n", table, stat.Entries)
		}

		if targetBytes != nil {
			fmt.Printf("\n=== Probing for hashed_address %s ===\n", hex.EncodeToString(targetBytes))

			// 1. Is the key in HashedAccounts?
			dbi, err := txn.OpenDBISimple("HashedAccounts", 0)
			if err != nil {
				return fmt.Errorf("open HashedAccounts: %w", err)
			}
			val, err := txn.Get(dbi, targetBytes)
			if err == nil {
				fmt.Printf("HashedAccounts[%s] = %s (%d bytes)\n",
					hex.EncodeToString(targetBytes), hex.EncodeToString(val), len(val))
			} else {
				fmt.Printf("HashedAccounts[%s] = NOT FOUND (%v)\n", hex.EncodeToString(targetBytes), err)
			}

			// 2. AccountsTrie rows that are prefixes of unpack(target).
			// A prefix-of-unpacked-target key is a 65-byte StoredNibbles
			// where Length ∈ [0, 64) and Nibbles[:Length] match the
			// corresponding nibbles of targetBytes.
			//
			// Easiest implementation: iterate ALL AccountsTrie rows, check.
			fmt.Printf("\nAccountsTrie rows whose nibble path is a prefix of the target:\n")
			adbi, err := txn.OpenDBISimple("AccountsTrie", 0)
			if err != nil {
				return fmt.Errorf("open AccountsTrie: %w", err)
			}
			acur, err := txn.OpenCursor(adbi)
			if err != nil {
				return fmt.Errorf("open AccountsTrie cursor: %w", err)
			}
			defer acur.Close()
			targetNibbles := make([]byte, 64)
			for i, b := range targetBytes {
				targetNibbles[i*2] = b >> 4
				targetNibbles[i*2+1] = b & 0x0f
			}
			k, v, err := acur.Get(nil, nil, mdbx.First)
			matches := 0
			for ; err == nil; k, v, err = acur.Get(nil, nil, mdbx.Next) {
				if len(k) < 65 {
					continue
				}
				length := int(k[64])
				if length > 64 {
					fmt.Printf("  WARNING: AccountsTrie key has Length=%d > 64: %s\n", length, hex.EncodeToString(k))
					continue
				}
				// Compare k[:length] against targetNibbles[:length]
				match := true
				for i := 0; i < length; i++ {
					if k[i] != targetNibbles[i] {
						match = false
						break
					}
				}
				if match {
					fmt.Printf("  PREFIX MATCH at depth=%d: nibbles[:%d]=%s value_len=%d\n",
						length, length, hex.EncodeToString(k[:length]), len(v))
					if matches < 4 {
						fmt.Printf("    value=%s\n", hex.EncodeToString(v))
					}
					matches++
				}
			}
			fmt.Printf("Total AccountsTrie prefix matches: %d\n", matches)
		} else {
			// Print first N rows of AccountsTrie to inspect structure
			fmt.Printf("\nFirst %d AccountsTrie rows:\n", *n)
			adbi, err := txn.OpenDBISimple("AccountsTrie", 0)
			if err == nil {
				acur, _ := txn.OpenCursor(adbi)
				defer acur.Close()
				k, v, err := acur.Get(nil, nil, mdbx.First)
				for i := 0; err == nil && i < *n; i, _ = i+1, 0 {
					length := byte(0)
					if len(k) >= 65 {
						length = k[64]
					}
					fmt.Printf("  [%d] key_len=%d nibbles[:%d]=%s value_len=%d value=%s\n",
						i, len(k), length, hex.EncodeToString(k[:min(int(length), len(k))]),
						len(v), hex.EncodeToString(v[:min(20, len(v))]))
					k, v, err = acur.Get(nil, nil, mdbx.Next)
				}
			}
		}

		return nil
	}); err != nil {
		log.Fatalf("view: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
