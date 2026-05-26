package seg

import "io"

// bitWriter is a port of `db/seg/compress.go:770-809 BitWriter`. Encodes
// Huffman codes LSB-first into a byte-oriented io.ByteWriter; the trailing
// partial byte is emitted via flush() (which zero-pads the high bits).
//
// Bit-packing semantics (matches Erigon exactly):
//
//   - codes are written LSB-first, with bit 0 of `code` going into the
//     first bit position of the next-emitted byte.
//   - within each emitted byte, bits accumulate from LSB to MSB: a 3-bit
//     code `0b110` followed by a 2-bit code `0b01` produces byte
//     `0b00001110` (bit positions: 110_LSB || 01_NEXT, padded with zero).
//   - flush() writes the current partial byte (if any bits are set) and
//     resets the accumulator. Required between word boundaries: each
//     word's Huffman bits are byte-aligned independently so the raw
//     uncovered bytes that follow can be read directly without
//     bit-shifting.
//
// Caller responsibility: invoke flush() AFTER the trailing position code
// for each word (parallel_compress.go:744-747), then write the raw word
// bytes.
type bitWriter struct {
	w          io.ByteWriter
	outputBits int  // 0..7
	outputByte byte // accumulator
}

// encode appends the low `codeBits` bits of `code` to the bitstream.
// LSB of `code` is written first.
func (bw *bitWriter) encode(code uint64, codeBits int) error {
	for codeBits > 0 {
		var bitsUsed int
		if bw.outputBits+codeBits > 8 {
			bitsUsed = 8 - bw.outputBits
		} else {
			bitsUsed = codeBits
		}
		mask := (uint64(1) << bitsUsed) - 1
		bw.outputByte |= byte((code & mask) << bw.outputBits)
		code >>= bitsUsed
		codeBits -= bitsUsed
		bw.outputBits += bitsUsed
		if bw.outputBits == 8 {
			if err := bw.w.WriteByte(bw.outputByte); err != nil {
				return err
			}
			bw.outputBits = 0
			bw.outputByte = 0
		}
	}
	return nil
}

// flush emits the partial trailing byte (zero-padding the high bits) if
// any bits have been written since the last byte boundary. Idempotent.
func (bw *bitWriter) flush() error {
	if bw.outputBits > 0 {
		if err := bw.w.WriteByte(bw.outputByte); err != nil {
			return err
		}
		bw.outputBits = 0
		bw.outputByte = 0
	}
	return nil
}
