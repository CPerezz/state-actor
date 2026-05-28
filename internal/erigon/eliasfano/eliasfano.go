package eliasfano

import (
	"encoding/binary"
	"fmt"
	"unsafe"
)

// init asserts the runtime is little-endian. Erigon's AppendBytes (line 766)
// uses `unsafe.Slice` to reinterpret []uint64 as []byte — host byte order on
// the writer side, host byte order assumed on the reader side. State-actor
// only ships on little-endian targets (linux/amd64, linux/arm64, darwin/arm64);
// a big-endian platform would silently produce an Erigon-unreadable file.
func init() {
	var x uint64 = 1
	if *(*byte)(unsafe.Pointer(&x)) != 1 {
		panic("eliasfano: big-endian platform not supported (Erigon's wire format is host-byte-order LE)")
	}
}

// Builder constructs an Elias-Fano encoding of a monotone non-decreasing
// uint64 sequence. Single-use: AddOffset N times, Build, then read via
// AppendBytes/Serialize/Bytes.
//
// Fields mirror `db/recsplit/eliasfano32.EliasFano` (elias_fano.go:54-67).
// We keep them unexported so callers go through the API.
type Builder struct {
	data           []uint64 // backing buffer; partitioned into lower/upper/jump views
	lowerBits      []uint64 // |--- view into data ---|
	upperBits      []uint64 // |
	jump           []uint64 //
	lowerBitsMask  uint64
	count          uint64 // declared count - 1 (Erigon convention; see NewEliasFano elias_fano.go:103)
	u              uint64 // maxOffset + 1
	l              uint64 // bits per element in the lower half
	maxOffset      uint64
	i              uint64 // next offset index (0..count)
	wordsUpperBits int
	built          bool
	lastAdded      uint64 // monotonicity check (not in Erigon — added for error returns)
}

// New constructs a Builder for a sequence of `count` monotone offsets each
// ≤ `maxOffset`. Mirrors `NewEliasFano(count, maxOffset)` at elias_fano.go:97-109.
//
// Returns an error rather than panicking on count==0 (Erigon panics; we
// prefer error returns to match state-actor conventions).
func New(count, maxOffset uint64) (*Builder, error) {
	if count == 0 {
		return nil, fmt.Errorf("eliasfano: count must be > 0")
	}
	b := &Builder{
		count:     count - 1,
		maxOffset: maxOffset,
	}
	b.u = maxOffset + 1
	b.wordsUpperBits = b.deriveFields()
	return b, nil
}

// Bytes returns a freshly-allocated COPY of the serialized form. Safe to
// retain past the Builder's lifetime. Use Bytes when the result will be
// embedded inside another file (e.g., RecSplit's `.kvi` footer); use
// AppendBytes when feeding an existing buffer.
func (b *Builder) Bytes() []byte {
	out := make([]byte, 0, 16+len(b.data)*uint64Size)
	return b.AppendBytes(out)
}

// AppendBytes serializes the builder's state into `buf` and returns the
// extended slice. Output layout (matches elias_fano.go:760-769):
//
//	[8B BE: count] [8B BE: u] [N×8B host-order: data]
//
// Where `data` is the concatenated lowerBits || upperBits || jump slices
// (each is just a view into the same uint64 backing array, so a single
// emission covers all three).
//
// CRITICAL: Build() must have been called first. Calling on an unfinished
// builder panics, matching Erigon's tighter contract.
func (b *Builder) AppendBytes(buf []byte) []byte {
	if !b.built {
		panic("eliasfano: AppendBytes called before Build")
	}
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], b.count)
	buf = append(buf, numBuf[:]...)
	binary.BigEndian.PutUint64(numBuf[:], b.u)
	buf = append(buf, numBuf[:]...)
	// Emit data in host (little-endian) byte order. Erigon does this via
	// `unsafe.Slice((*byte)(unsafe.Pointer(&data[0])), len(data)*8)` —
	// we mirror with binary.LittleEndian to keep the code unsafe-free.
	// On LE platforms the resulting bytes are bit-identical.
	scratch := make([]byte, 8*len(b.data))
	for i, w := range b.data {
		binary.LittleEndian.PutUint64(scratch[i*8:(i+1)*8], w)
	}
	buf = append(buf, scratch...)
	return buf
}

// Serialize writes the builder's state to an io.Writer. Equivalent to
// AppendBytes(nil) + a single Write, but avoids the intermediate copy
// when the writer is the final sink (e.g., a snapshot file).
func (b *Builder) Serialize(w interface{ Write([]byte) (int, error) }) error {
	if !b.built {
		panic("eliasfano: Serialize called before Build")
	}
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], b.count)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	binary.BigEndian.PutUint64(numBuf[:], b.u)
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	// Same host-byte-order emission as AppendBytes.
	scratch := make([]byte, 8*len(b.data))
	for i, x := range b.data {
		binary.LittleEndian.PutUint64(scratch[i*8:(i+1)*8], x)
	}
	if _, err := w.Write(scratch); err != nil {
		return err
	}
	return nil
}
