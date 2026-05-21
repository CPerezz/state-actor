// probe-tablesizes prints row counts + byte sizes for every reth MDBX table.
//
//go:build cgo_reth

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/erigontech/mdbx-go/mdbx"
)

// Same table list as internal/reth/tables.go; duplicated here so the probe
// has no module-tree dependency.
var tables = []string{
	"CanonicalHeaders", "HeaderTerminalDifficulties", "HeaderNumbers", "Headers",
	"BlockBodyIndices", "BlockOmmers", "BlockWithdrawals", "Transactions",
	"TransactionHashNumbers", "TransactionBlocks", "Receipts", "Bytecodes",
	"PlainAccountState", "PlainStorageState", "AccountsHistory", "StoragesHistory",
	"AccountChangeSets", "StorageChangeSets", "HashedAccounts", "HashedStorages",
	"AccountsTrie", "StoragesTrie", "TransactionSenders", "StageCheckpoints",
	"StageCheckpointProgresses", "PruneCheckpoints", "VersionHistory", "ChainState",
	"Metadata",
}

func main() {
	datadir := flag.String("datadir", "", "reth datadir")
	flag.Parse()
	if *datadir == "" {
		log.Fatal("-datadir required")
	}

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

	type tableInfo struct {
		name       string
		entries    uint64
		leafPages  uint64
		branchPgs  uint64
		overflowPg uint64
		pageSize   uint64
	}
	var infos []tableInfo
	var totalLeafPages, totalBranch, totalOverflow uint64

	if err := env.View(func(txn *mdbx.Txn) error {
		for _, t := range tables {
			dbi, err := txn.OpenDBISimple(t, 0)
			if err != nil {
				fmt.Printf("%-30s OPEN FAILED: %v\n", t, err)
				continue
			}
			stat, err := txn.StatDBI(dbi)
			if err != nil {
				fmt.Printf("%-30s STAT FAILED: %v\n", t, err)
				continue
			}
			infos = append(infos, tableInfo{
				name:       t,
				entries:    stat.Entries,
				leafPages:  stat.LeafPages,
				branchPgs:  stat.BranchPages,
				overflowPg: stat.OverflowPages,
				pageSize:   uint64(stat.PSize),
			})
			totalLeafPages += stat.LeafPages
			totalBranch += stat.BranchPages
			totalOverflow += stat.OverflowPages
		}
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	// Sort by total bytes descending so the biggest tables show first.
	sort.Slice(infos, func(i, j int) bool {
		bi := (infos[i].leafPages + infos[i].branchPgs + infos[i].overflowPg) * infos[i].pageSize
		bj := (infos[j].leafPages + infos[j].branchPgs + infos[j].overflowPg) * infos[j].pageSize
		return bi > bj
	})

	fmt.Printf("%-28s %14s %14s %16s\n", "table", "entries", "pages", "bytes")
	fmt.Println("------------------------------------------------------------------------------")
	for _, ti := range infos {
		pages := ti.leafPages + ti.branchPgs + ti.overflowPg
		bytes := pages * ti.pageSize
		fmt.Printf("%-28s %14d %14d %16s\n", ti.name, ti.entries, pages, humanBytes(bytes))
	}
	fmt.Println("------------------------------------------------------------------------------")
	pageSize := uint64(0)
	if len(infos) > 0 {
		pageSize = infos[0].pageSize
	}
	totalBytes := (totalLeafPages + totalBranch + totalOverflow) * pageSize
	fmt.Printf("%-28s %14s %14d %16s\n", "TOTAL", "", totalLeafPages+totalBranch+totalOverflow, humanBytes(totalBytes))

	// v2-aware sanity check. Read Metadata["storage_settings"]; if it
	// decodes as {"storage_v2":true} the Plain* tables MUST be empty and
	// the Hashed* tables MUST be populated. A non-empty Plain* on a v2
	// datadir means a writer has re-introduced the dropped rows; an empty
	// Hashed* on v2 means the canonical state is missing.
	v2, ok := readStorageV2Flag(env)
	if !ok {
		return
	}
	fmt.Println()
	if v2 {
		fmt.Println("storage_settings: v2 (HashedAccounts/HashedStorages canonical)")
	} else {
		fmt.Println("storage_settings: v1 (PlainAccountState/PlainStorageState canonical)")
		return
	}
	entries := map[string]uint64{}
	for _, ti := range infos {
		entries[ti.name] = ti.entries
	}
	var problems []string
	if entries["PlainAccountState"] > 0 {
		problems = append(problems, fmt.Sprintf("PlainAccountState=%d (must be 0 on v2)", entries["PlainAccountState"]))
	}
	if entries["PlainStorageState"] > 0 {
		problems = append(problems, fmt.Sprintf("PlainStorageState=%d (must be 0 on v2)", entries["PlainStorageState"]))
	}
	if entries["HashedAccounts"] == 0 {
		problems = append(problems, "HashedAccounts=0 (canonical state empty on v2)")
	}
	if entries["HashedStorages"] == 0 {
		problems = append(problems, "HashedStorages=0 (canonical state empty on v2)")
	}
	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "v2 invariant violations:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  "+p)
		}
		os.Exit(2)
	}
	fmt.Println("v2 invariants OK")
}

// readStorageV2Flag opens the Metadata table and decodes the storage_settings
// row. Returns (storage_v2, found).
func readStorageV2Flag(env *mdbx.Env) (bool, bool) {
	var raw []byte
	err := env.View(func(txn *mdbx.Txn) error {
		dbi, err := txn.OpenDBISimple("Metadata", 0)
		if err != nil {
			return err
		}
		val, err := txn.Get(dbi, []byte("storage_settings"))
		if err != nil {
			return err
		}
		raw = append(raw, val...)
		return nil
	})
	if err != nil {
		return false, false
	}
	var s struct {
		StorageV2 bool `json:"storage_v2"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return false, false
	}
	return s.StorageV2, true
}

func humanBytes(n uint64) string {
	const k = 1024
	switch {
	case n >= k*k*k*k:
		return fmt.Sprintf("%.2f TiB", float64(n)/float64(k*k*k*k))
	case n >= k*k*k:
		return fmt.Sprintf("%.2f GiB", float64(n)/float64(k*k*k))
	case n >= k*k:
		return fmt.Sprintf("%.2f MiB", float64(n)/float64(k*k))
	case n >= k:
		return fmt.Sprintf("%.2f KiB", float64(n)/float64(k))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
