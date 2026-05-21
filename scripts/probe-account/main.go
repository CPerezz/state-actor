// probe-account looks up a specific address in reth's PlainAccountState
// + HashedAccounts MDBX tables and prints what it finds. On v2 datadirs
// PlainAccountState is empty by design (HashedAccounts is the canonical
// state); the probe prints NOT FOUND for it and reads the row from
// HashedAccounts.
//
// Usage:
//
//	go build -tags=cgo_reth -buildvcs=false -o /tmp/probe-account ./scripts/probe-account
//	docker run --rm -v <datadir>:/data -v /tmp/probe-account:/p:ro debian:trixie-slim /p \
//	    -datadir /data -addr 0x7dd3d77f613a356170f87fc1ccb9adb57bce65ff

//go:build cgo_reth

package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/crypto"
)

func main() {
	datadir := flag.String("datadir", "", "reth datadir")
	addrHex := flag.String("addr", "", "20-byte hex address (with or without 0x prefix)")
	flag.Parse()
	if *datadir == "" || *addrHex == "" {
		log.Fatal("-datadir + -addr required")
	}
	s := *addrHex
	if len(s) >= 2 && (s[:2] == "0x" || s[:2] == "0X") {
		s = s[2:]
	}
	addr, err := hex.DecodeString(s)
	if err != nil || len(addr) != 20 {
		log.Fatalf("addr must be 20 hex bytes: %v len=%d", err, len(addr))
	}
	addrHash := crypto.Keccak256(addr)

	env, err := mdbx.NewEnv()
	if err != nil {
		log.Fatal(err)
	}
	defer env.Close()
	if err := env.SetOption(mdbx.OptMaxDB, 64); err != nil {
		log.Fatal(err)
	}
	if err := env.Open(filepath.Join(*datadir, "db"), mdbx.Readonly|mdbx.NoSubdir, 0o644); err != nil {
		log.Fatal(err)
	}

	if err := env.View(func(txn *mdbx.Txn) error {
		fmt.Printf("address:     0x%s\n", hex.EncodeToString(addr))
		fmt.Printf("addr_hash:   0x%s\n", hex.EncodeToString(addrHash))

		for _, table := range []string{"PlainAccountState", "HashedAccounts"} {
			dbi, err := txn.OpenDBISimple(table, 0)
			if err != nil {
				fmt.Printf("  %s: open failed: %v\n", table, err)
				continue
			}
			var key []byte
			if table == "PlainAccountState" {
				key = addr
			} else {
				key = addrHash
			}
			val, err := txn.Get(dbi, key)
			if err != nil {
				fmt.Printf("  %s[%s]: NOT FOUND (%v)\n", table, hex.EncodeToString(key), err)
				continue
			}
			fmt.Printf("  %s[%s]: %s (%d bytes)\n", table, hex.EncodeToString(key), hex.EncodeToString(val), len(val))
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}
}
