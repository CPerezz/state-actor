package snap

import (
	"context"
	"iter"

	"github.com/ethereum/state-actor/internal/erigon/seg"
)

// KVCodec writes a domain's .kv and reads it back for the accessor pass. The default is
// state-actor's own seg, which has no pattern dictionary, so every word is stored raw. Erigon
// compresses some domains' keys or values against one (statecfg.Schema), and a generated store
// read through raw files pays more pages per read than a node's own: 7 against 1 for a
// 24,576-byte contract. The cgo Erigon build injects internal/erigon/erigonseg, which uses
// Erigon's own compressor; this package stays free of the erigontech/erigon import.
type KVCodec interface {
	// Write encodes entries as alternating key and value words into the .kv at path.
	// entries yields ascending keys and is repeatable.
	Write(ctx context.Context, d Domain, path, tmpDir string, entries func(yield func(DomainEntry) bool)) error
	// Open re-reads the .kv at path, yielding each pair with its key offset.
	Open(d Domain, path string) (KVReader, error)
}

// KVReader iterates a written .kv. Offsets are relative to the start of the words section,
// as Erigon's Getter counts them; see seg.OffsetEntry.
type KVReader interface {
	Iterate(ctx context.Context) iter.Seq2[seg.OffsetEntry, error]
	Close() error
}

// rawCodec is state-actor's pure-Go seg: no pattern dictionary for any domain.
type rawCodec struct{}

func (rawCodec) Write(_ context.Context, _ Domain, path, tmpDir string, entries func(yield func(DomainEntry) bool)) error {
	comp, err := seg.NewCompressor(path, tmpDir, seg.DefaultConfig())
	if err != nil {
		return err
	}
	// CompressFromSource drives the (repeatable) source twice, count then encode, skipping the
	// .idt intermediate that used to cost a full extra write and read of the domain.
	src := seg.WordSource(func(yield func(word []byte) bool) {
		entries(func(e DomainEntry) bool {
			return yield(e.Key) && yield(e.Value)
		})
	})
	if err := comp.CompressFromSource(src); err != nil {
		_ = comp.Close()
		return err
	}
	return comp.Close()
}

func (rawCodec) Open(_ Domain, path string) (KVReader, error) {
	return seg.NewDecompressor(path)
}
