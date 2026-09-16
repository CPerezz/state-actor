//go:build cgo_besu

package main

import "testing"

// Captured from rocksdb.aggregated-table-properties on a Besu Bonsai
// ACCOUNT_INFO_STATE CF; pins the key names the parser depends on.
const sample = "# data blocks=1233; # entries=354792873; # deletions=0; raw key size=11353371936; " +
	"raw average key size=32.000000; raw value size=28800000000; data block size=17441333066; " +
	"index block size (user-key? 1, delta-value? 1)=123; filter block size=443500673; " +
	"# entries for filter=354792873; (estimated) table size=1; filter policy name=N/A; "

func TestParseAggregated(t *testing.T) {
	g := parseAggregated(sample)
	want := geom{entries: 354792873, dataSize: 17441333066, rawKey: 11353371936, rawValue: 28800000000,
		dataBlocks: 1233, filterSize: 443500673, filterEnts: 354792873}
	if g != want {
		t.Fatalf("got %+v, want %+v", g, want)
	}
	if got := filterLabel(g); got != "10.00 bits" {
		t.Fatalf("filterLabel = %q, want \"10.00 bits\"", got)
	}
}
