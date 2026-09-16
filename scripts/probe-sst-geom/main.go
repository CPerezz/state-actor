// probe-sst-geom: read-only RocksDB probe. Per-CF/per-level SST geometry from
// rocksdb.aggregated-table-properties(-at-level<N>) (numeric fields only, so
// the filter column is derived bits/key) plus sampled account/code composition
// (fixed-seed seeks; Besu Bonsai CFs 06/07 by default). RocksDB's LOG goes to
// the temp dir; nothing is written to the store.
//
//	go build -tags cgo_besu -buildvcs=false -o /tmp/probe-sst-geom ./scripts/probe-sst-geom
//	/tmp/probe-sst-geom -db /data/besu/database

//go:build cgo_besu

package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	mrand "math/rand/v2"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/linxGnu/grocksdb"
)

// Bonsai account value = RLP(nonce, balance, storageRoot, codeHash); substring
// match skips RLP parsing.
var (
	emptyCodeHash    = types.EmptyCodeHash.Bytes()
	emptyTrieRoot    = types.EmptyRootHash.Bytes()
	delegationPrefix = []byte{0xef, 0x01, 0x00} // EIP-7702: 0xef0100 || address (23 B)
)

type geom struct{ entries, ssts, dataSize, rawKey, rawValue, dataBlocks, filterSize, filterEnts uint64 }

func main() {
	dbPath := flag.String("db", "", "RocksDB directory (read-only)")
	seeks := flag.Int("seeks", 64, "pseudo-random seek points per sampled CF")
	perSeek := flag.Int("per-seek", 512, "consecutive records per seek point")
	acctCF := flag.String("acct-cf", "06", "hex name of the flat-account CF")
	codeCF := flag.String("code-cf", "07", "hex name of the code CF")
	flag.Parse()
	if *dbPath == "" {
		log.Fatal("-db is required")
	}

	names, err := grocksdb.ListColumnFamilies(grocksdb.NewDefaultOptions(), *dbPath)
	if err != nil {
		log.Fatal(err)
	}
	dbOpts := grocksdb.NewDefaultOptions()
	dbOpts.SetDbLogDir(os.TempDir())
	cfOpts := make([]*grocksdb.Options, len(names))
	for i := range cfOpts {
		cfOpts[i] = grocksdb.NewDefaultOptions()
	}
	db, handles, err := grocksdb.OpenDbForReadOnlyColumnFamilies(dbOpts, *dbPath, names, cfOpts, false)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	fmt.Printf("store: %s\ncolumn families: %d\n\n== SST geometry (RocksDB aggregated table properties)\n", *dbPath, len(names))
	fmt.Printf("%-8s %14s %6s %15s %9s %10s %9s %14s %10s\n",
		"cf", "entries", "ssts", "data_bytes", "rec_B", "blk_B", "phys/log", "filter_B", "filter")
	totals := make([]geom, len(names))
	var levels strings.Builder
	for i, h := range handles {
		g := parseAggregated(db.GetPropertyCF("rocksdb.aggregated-table-properties", h))
		for lvl := range 7 {
			l := parseAggregated(db.GetPropertyCF("rocksdb.aggregated-table-properties-at-level"+strconv.Itoa(lvl), h))
			l.ssts, _ = strconv.ParseUint(db.GetPropertyCF("rocksdb.num-files-at-level"+strconv.Itoa(lvl), h), 10, 64)
			g.ssts += l.ssts
			if l.ssts > 0 {
				fmt.Fprintf(&levels, "%-8s %5d %6d %14d %15d %14d\n", label(names[i]), lvl, l.ssts, l.entries, l.dataSize, l.filterSize)
			}
		}
		totals[i] = g
		if g.entries > 0 {
			fmt.Printf("%-8s %14d %6d %15d %9.1f %10.1f %9.3f %14d %10s\n",
				label(names[i]), g.entries, g.ssts, g.dataSize, div(g.rawKey+g.rawValue, g.entries),
				div(g.dataSize, g.dataBlocks), div(g.dataSize, g.rawKey+g.rawValue), g.filterSize, filterLabel(g))
		}
	}
	fmt.Printf("\n== LSM levels (non-empty only)\n%-8s %5s %6s %14s %15s %14s\n%s",
		"cf", "level", "ssts", "entries", "data_bytes", "filter_B", levels.String())

	var codedAccounts float64
	if i := slices.Index(names, cfName(*acctCF)); i >= 0 {
		codedAccounts = reportAccounts(db, handles[i], *acctCF, *seeks, *perSeek, totals[i])
	}
	if i := slices.Index(names, cfName(*codeCF)); i >= 0 {
		reportCode(db, handles[i], *codeCF, *seeks, *perSeek, totals[i], codedAccounts)
	}
}

// reportAccounts prints the plain-EOA share (both hashes empty; mainnet 0.808)
// and returns the estimated coded-account count.
func reportAccounts(db *grocksdb.DB, cf *grocksdb.ColumnFamilyHandle, name string, seeks, perSeek int, g geom) float64 {
	var sampled, bothEmpty, emptyRoot, coded int
	forEachSample(db, cf, seeks, perSeek, func(_, val []byte) {
		sampled++
		emptyCode, emptyRootHash := bytes.Contains(val, emptyCodeHash), bytes.Contains(val, emptyTrieRoot)
		if emptyRootHash {
			emptyRoot++
		}
		if emptyCode && emptyRootHash {
			bothEmpty++
		}
		if !emptyCode {
			coded++
		}
	})
	if sampled == 0 {
		return 0
	}
	share := func(n int) float64 { return float64(n) / float64(sampled) }
	fmt.Printf("\n== cf%s composition (sampled %d of %d)\n", name, sampled, g.entries)
	fmt.Printf("  both_empty_share   %6.3f   (plain EOA; mainnet 0.808)\n", share(bothEmpty))
	fmt.Printf("  empty_root_share   %6.3f   (mainnet 0.929)\n", share(emptyRoot))
	fmt.Printf("  coded_share        %6.3f   (mainnet 0.192)\n", share(coded))
	return share(coded) * float64(g.entries)
}

// reportCode prints the EIP-7702 designator share, value-length histogram and
// accounts per distinct bytecode; code keys are code hashes, so entries =
// distinct bytecodes.
func reportCode(db *grocksdb.DB, cf *grocksdb.ColumnFamilyHandle, name string, seeks, perSeek int, g geom, codedAccounts float64) {
	bounds := []int{32, 256, 1024, 4096, 8192, 16384, 24576}
	hist := make([]int, len(bounds)+1)
	var sampled, designators int
	forEachSample(db, cf, seeks, perSeek, func(_, val []byte) {
		sampled++
		if len(val) == 23 && bytes.HasPrefix(val, delegationPrefix) {
			designators++
		}
		hist[sort.SearchInts(bounds, len(val))]++
	})
	if sampled == 0 {
		return
	}
	fmt.Printf("\n== cf%s composition (sampled %d of %d)\n", name, sampled, g.entries)
	fmt.Printf("  designator_share   %6.3f   (23 B 0xef0100..; mainnet 0.042)\n", float64(designators)/float64(sampled))
	fmt.Printf("  distinct_bytecodes %6d\n", g.entries)
	fmt.Printf("  mean_value_B       %6.1f   (mainnet ~5265, fixture-corrected)\n", div(g.rawValue, g.entries))
	fmt.Printf("  accts_per_code     %6.1f   (coded accounts / distinct bytecodes; mainnet 28.2)\n", codedAccounts/float64(g.entries))
	fmt.Print("  value_len_hist    ")
	lo := 0
	for i, n := range hist {
		hi := "inf"
		if i < len(bounds) {
			hi = strconv.Itoa(bounds[i])
		}
		fmt.Printf(" [%d-%s]=%d", lo, hi, n)
		if i < len(bounds) {
			lo = bounds[i] + 1
		}
	}
	fmt.Println()
}

// forEachSample visits up to perSeek records at each of seeks fixed-seed seek
// points. Checksums and block-cache fill off: metadata probe.
func forEachSample(db *grocksdb.DB, cf *grocksdb.ColumnFamilyHandle, seeks, perSeek int, fn func(key, val []byte)) {
	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	ro.SetFillCache(false)
	ro.SetVerifyChecksums(false)
	it := db.NewIteratorCF(ro, cf)
	defer it.Close()
	rng := mrand.NewChaCha8([32]byte{42})
	var seek [32]byte
	for range seeks {
		rng.Read(seek[:])
		it.Seek(seek[:])
		for range perSeek {
			if !it.Valid() {
				break
			}
			fn(it.Key().Data(), it.Value().Data())
			it.Next()
		}
	}
	if err := it.Err(); err != nil {
		log.Fatal(err)
	}
}

// parseAggregated reads the key=value; pairs of TableProperties::ToString;
// numeric fields only.
func parseAggregated(s string) (g geom) {
	for _, kv := range strings.Split(s, ";") {
		k, v, _ := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(k) {
		case "# entries":
			g.entries = n
		case "# data blocks":
			g.dataBlocks = n
		case "raw key size":
			g.rawKey = n
		case "raw value size":
			g.rawValue = n
		case "data block size":
			g.dataSize = n
		case "filter block size":
			g.filterSize = n
		case "# entries for filter":
			g.filterEnts = n
		}
	}
	return g
}

func filterLabel(g geom) string {
	if g.filterSize == 0 {
		return "(none)"
	}
	return fmt.Sprintf("%.2f bits", 8*div(g.filterSize, g.filterEnts))
}

func div(a, b uint64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// label renders a CF name; Besu Bonsai names CFs by one literal byte.
func label(name string) string {
	if len(name) == 1 {
		return hex.EncodeToString([]byte(name))
	}
	return name
}

func cfName(hexName string) string {
	if b, err := hex.DecodeString(hexName); err == nil {
		return string(b)
	}
	return hexName
}
