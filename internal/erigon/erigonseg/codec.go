//go:build cgo_erigon_commitment

// Package erigonseg writes and re-reads domain .kv files with Erigon's own compressor, at the
// compression Erigon's schema gives each domain: code values and storage and commitment keys
// against a pattern dictionary, accounts raw. state-actor's own seg stores every word raw, so a
// 24,576-byte test contract cost 7 pages per read on a generated store against 1 on a node's
// own file (Erigon state-DB study, round 14).
//
// Like internal/erigon/commitment, this package imports github.com/erigontech/erigon, so it
// must only be imported from files under the cgo_erigon_commitment build tag.
package erigonseg

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/seg"
	"github.com/erigontech/erigon/db/state/statecfg"

	saseg "github.com/ethereum/state-actor/internal/erigon/seg"
	"github.com/ethereum/state-actor/internal/erigon/snap"
)

// Codec implements snap.KVCodec. It compresses with statecfg.DomainCompressCfg, whose single
// worker keeps the dictionary, and so the file, deterministic for a given input.
type Codec struct{}

var _ snap.KVCodec = Codec{}

func compression(d snap.Domain) (seg.FileCompression, error) {
	switch d {
	case snap.DomainAccounts:
		return statecfg.Schema.AccountsDomain.Compression, nil
	case snap.DomainStorage:
		return statecfg.Schema.StorageDomain.Compression, nil
	case snap.DomainCode:
		return statecfg.Schema.CodeDomain.Compression, nil
	case snap.DomainCommitment:
		return statecfg.Schema.CommitmentDomain.Compression, nil
	}
	return 0, fmt.Errorf("erigonseg: no Erigon compression for domain %v", d)
}

func (Codec) Write(ctx context.Context, d snap.Domain, path, tmpDir string, entries func(yield func(snap.DomainEntry) bool)) error {
	c, err := compression(d)
	if err != nil {
		return err
	}
	comp, err := seg.NewCompressor(ctx, "state-actor "+d.Tag(), path, tmpDir, statecfg.DomainCompressCfg, log.LvlDebug, log.New())
	if err != nil {
		return err
	}
	defer comp.Close()
	w := seg.NewWriter(comp, c)
	var werr error
	entries(func(e snap.DomainEntry) bool {
		if _, werr = w.Write(e.Key); werr != nil {
			return false
		}
		_, werr = w.Write(e.Value)
		return werr == nil
	})
	if werr != nil {
		return werr
	}
	return comp.Compress()
}

func (Codec) Open(d snap.Domain, path string) (snap.KVReader, error) {
	c, err := compression(d)
	if err != nil {
		return nil, err
	}
	dec, err := seg.NewDecompressor(path)
	if err != nil {
		return nil, err
	}
	return &reader{dec: dec, c: c}, nil
}

type reader struct {
	dec *seg.Decompressor
	c   seg.FileCompression
}

// Iterate yields each pair with the offset of its key's first word, the position Erigon's own
// accessor builders index (btindex.BuildBtreeIndexWithDecompressor, buildHashMapAccessorAt).
// Getter offsets count from the start of the words section, as saseg.OffsetEntry does.
func (r *reader) Iterate(ctx context.Context) iter.Seq2[saseg.OffsetEntry, error] {
	return func(yield func(saseg.OffsetEntry, error) bool) {
		g := seg.NewReader(r.dec.MakeGetter(), r.c)
		var key, val []byte
		var keyOff uint64
		for g.HasNext() {
			if err := ctx.Err(); err != nil {
				yield(saseg.OffsetEntry{}, err)
				return
			}
			var valOff, next uint64
			key, valOff = g.Next(key[:0])
			if !g.HasNext() {
				yield(saseg.OffsetEntry{}, errors.New("erigonseg: key without a value"))
				return
			}
			val, next = g.Next(val[:0])
			if !yield(saseg.OffsetEntry{Key: key, Value: val, KeyOffset: keyOff, ValueOffset: valOff}, nil) {
				return
			}
			keyOff = next
		}
	}
}

func (r *reader) Close() error {
	r.dec.Close()
	return nil
}
