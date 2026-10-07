//go:build cgo_erigon_commitment

package erigonseg

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/erigontech/erigon/db/datastruct/btindex"
	"github.com/erigontech/erigon/db/seg"
	"github.com/erigontech/erigon/db/state/statecfg"

	internalerigon "github.com/ethereum/state-actor/internal/erigon"
	saseg "github.com/ethereum/state-actor/internal/erigon/seg"
	"github.com/ethereum/state-actor/internal/erigon/snap"
)

// fixtureCode mirrors the benchmark's unique_jumpdest contracts: 24,576 bytes of JUMPDEST around
// the contract's own address, so no two are equal and a pattern dictionary collapses them all.
func fixtureCode(addr []byte) []byte {
	code := bytes.Repeat([]byte{0x5b}, 24576)
	copy(code, []byte{0x61, 0x5f, 0xff, 0x56})
	copy(code[44:], addr)
	return code
}

// TestCodecErigonReadsBack writes three domains through snap.Writer with the Erigon codec, then
// reads them back with nothing but Erigon's own code: the B-tree index state-actor built must find
// every key's value through Erigon's reader at the domain's schema compression, and the words that
// schema compresses must come out compressed. Every domain's keys and values carry 32-byte runs a
// dictionary can cover, so a wrong mask either misreads or leaves those words raw.
func TestCodecErigonReadsBack(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	w, err := snap.NewWriter(dir, snap.Settings{Seed: 42, Codec: Codec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	rnd := rand.New(rand.NewSource(1))
	run := bytes.Repeat([]byte{0xab, 0xcd, 0xef, 0x01}, 8) // a 32-byte run every value and slot shares
	const n = 600
	pairs := map[snap.Domain][]snap.DomainEntry{}
	for i := range n {
		addr := make([]byte, 20)
		binary.BigEndian.PutUint64(addr[12:], uint64(i+1))
		var code []byte
		if i%2 == 0 {
			code = fixtureCode(addr)
		} else {
			code = make([]byte, 1000+rnd.Intn(4000))
			rnd.Read(code)
		}
		pairs[snap.DomainCode] = append(pairs[snap.DomainCode], snap.DomainEntry{Key: addr, Value: code})
		acct := append(append([]byte{}, run...), byte(i))
		pairs[snap.DomainAccounts] = append(pairs[snap.DomainAccounts], snap.DomainEntry{Key: addr, Value: acct})
		for s := range 3 {
			key := append(append(append([]byte{}, addr...), run...), byte(s))
			val := append(append([]byte{}, run...), byte(s))
			pairs[snap.DomainStorage] = append(pairs[snap.DomainStorage], snap.DomainEntry{Key: key, Value: val})
		}
	}

	r := snap.StepRange{From: 0, To: 1}
	for d, ps := range pairs {
		if err := w.WriteDomain(ctx, d, r, uint64(len(ps)), func(yield func(snap.DomainEntry) bool) {
			for _, p := range ps {
				if !yield(p) {
					return
				}
			}
		}); err != nil {
			t.Fatalf("WriteDomain(%v): %v", d, err)
		}

		// The node reads each domain at its schema compression, whatever the writer chose.
		c := map[snap.Domain]seg.FileCompression{
			snap.DomainAccounts: statecfg.Schema.AccountsDomain.Compression,
			snap.DomainStorage:  statecfg.Schema.StorageDomain.Compression,
			snap.DomainCode:     statecfg.Schema.CodeDomain.Compression,
		}[d]
		dom := snap.DomainDir(dir)
		v := internalerigon.SnapshotFormatVersion
		path := snap.BuildDataFilename(dom, v, d, r)
		dec, bt, err := btindex.OpenBtreeIndexAndDataFile(snap.BuildBTreeFilename(dom, v, d, r), path, btindex.DefaultBtreeM, c, false)
		if err != nil {
			t.Fatalf("%v: Erigon can't open the file set: %v", d, err)
		}
		g := seg.NewReader(dec.MakeGetter(), c)
		for _, p := range ps {
			_, v, _, found, err := bt.Get(p.Key, g)
			if err != nil || !found || !bytes.Equal(v, p.Value) {
				t.Fatalf("%v: Erigon's B-tree lookup of %x: found=%v err=%v", d, p.Key, found, err)
			}
		}
		bt.Close()
		dec.Close()

		// Bytes on disk per word kind, from the key and value offsets Erigon reads at: a kind the
		// schema compresses must shrink, a kind it stores raw must not.
		rd, err := Codec{}.Open(d, path)
		if err != nil {
			t.Fatal(err)
		}
		var rawK, rawV, encK, encV uint64
		var prev *saseg.OffsetEntry
		for e, err := range rd.Iterate(ctx) {
			if err != nil {
				t.Fatal(err)
			}
			if prev != nil {
				encV += e.KeyOffset - prev.ValueOffset
			}
			encK += e.ValueOffset - e.KeyOffset
			rawK, rawV = rawK+uint64(len(e.Key)), rawV+uint64(len(e.Value))
			cur := e
			prev = &cur
		}
		rd.Close()
		rawV -= uint64(len(ps[len(ps)-1].Value)) // the last value has no next key to measure against
		for _, k := range []struct {
			name     string
			flag     seg.FileCompression
			raw, enc uint64
		}{{"keys", seg.CompressKeys, rawK, encK}, {"values", seg.CompressVals, rawV, encV}} {
			if compressed := k.enc < k.raw/2; compressed != c.Has(k.flag) {
				t.Errorf("%v %s: %d bytes on disk for %d raw, schema compresses them: %v", d, k.name, k.enc, k.raw, c.Has(k.flag))
			}
		}
	}
}
