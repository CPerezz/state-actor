// strip-shallow-bncs deletes AccountsTrie rows with Length < threshold.
// Diagnostic tool — used to test whether shallow BNCs trigger reth's
// stack overflow at block-time state-root computation.
//
// Usage: go run ./scripts/strip-shallow-bncs -datadir /path/to/reth-data -min-depth 4

//go:build cgo_reth

package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/erigontech/mdbx-go/mdbx"
)

func main() {
	datadir := flag.String("datadir", "", "reth datadir (contains db/mdbx.dat)")
	minDepth := flag.Int("min-depth", 4, "delete rows with Length < min-depth")
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
	if err := env.SetGeometry(-1, -1, -1, -1, -1, -1); err != nil {
		log.Fatalf("SetGeometry: %v", err)
	}
	if err := env.Open(filepath.Join(*datadir, "db"), mdbx.NoSubdir, 0o644); err != nil {
		log.Fatalf("Open: %v", err)
	}

	deleted := 0
	if err := env.Update(func(txn *mdbx.Txn) error {
		dbi, err := txn.OpenDBISimple("AccountsTrie", 0)
		if err != nil {
			return fmt.Errorf("open AccountsTrie: %w", err)
		}
		cur, err := txn.OpenCursor(dbi)
		if err != nil {
			return fmt.Errorf("open cursor: %w", err)
		}
		defer cur.Close()

		k, _, err := cur.Get(nil, nil, mdbx.First)
		for ; err == nil; k, _, err = cur.Get(nil, nil, mdbx.Next) {
			if len(k) < 65 {
				continue
			}
			depth := int(k[64])
			if depth < *minDepth {
				if dErr := cur.Del(0); dErr != nil {
					return fmt.Errorf("delete depth=%d row: %w", depth, dErr)
				}
				deleted++
			}
		}
		return nil
	}); err != nil {
		log.Fatalf("update: %v", err)
	}
	fmt.Printf("Deleted %d AccountsTrie rows with Length < %d\n", deleted, *minDepth)
}
