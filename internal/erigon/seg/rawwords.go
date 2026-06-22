package seg

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// rawWordsFile is the temporary uncompressed intermediate file the
// Compressor writes during AddWord/AddUncompressedWord. Mirrors
// `db/seg/compress.go:946-1044 RawWordsFile`.
//
// Wire format (one record per word, no header):
//
//	varint(2*len(v) [+ 1 if uncompressed]): length prefix; bottom bit
//	                                        encodes the "uncompressed" flag
//	len(v) bytes:                            the raw word
//
// "Uncompressed" means: this word should bypass pattern-cover encoding
// at Compress() time and be written as a length-prefixed plain byte
// run. "Compressed" means: pattern-cover encoding may apply (in the
// no-pattern fast path, both classes collapse to the same output).
type rawWordsFile struct {
	f        *os.File
	w        *bufio.Writer
	filePath string
	buf      []byte
	count    uint64
}

func newRawWordsFile(filePath string) (*rawWordsFile, error) {
	// Explicit 0o644 (vs os.Create's mode 0o666 & ~umask): the downstream
	// Erigon daemon runs in a separate container as a different uid, and
	// inside state-actor's docker container umask defaults to 077 — so
	// os.Create here used to produce 0o600 files unreadable by the daemon.
	// See feedback_snapshot_file_perms in the project memory.
	f, err := os.OpenFile(filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &rawWordsFile{
		filePath: filePath,
		f:        f,
		w:        bufio.NewWriterSize(f, 256*1024),
		buf:      make([]byte, binary.MaxVarintLen64),
	}, nil
}

// Append writes a "compressed" word (length prefix low bit = 0).
func (rf *rawWordsFile) Append(v []byte) error {
	rf.count++
	n := binary.PutUvarint(rf.buf, 2*uint64(len(v)))
	if _, e := rf.w.Write(rf.buf[:n]); e != nil {
		return e
	}
	if len(v) > 0 {
		if _, e := rf.w.Write(v); e != nil {
			return e
		}
	}
	return nil
}

// AppendUncompressed writes an "uncompressed" word (length prefix low bit = 1).
func (rf *rawWordsFile) AppendUncompressed(v []byte) error {
	rf.count++
	n := binary.PutUvarint(rf.buf, 2*uint64(len(v))+1)
	if _, e := rf.w.Write(rf.buf[:n]); e != nil {
		return e
	}
	if len(v) > 0 {
		if _, e := rf.w.Write(v); e != nil {
			return e
		}
	}
	return nil
}

// Flush flushes the bufio.Writer to the OS.
func (rf *rawWordsFile) Flush() error {
	return rf.w.Flush()
}

// Close flushes and closes the OS file (file remains on disk).
func (rf *rawWordsFile) Close() error {
	if rf.f == nil {
		return nil
	}
	if err := rf.w.Flush(); err != nil {
		_ = rf.f.Close()
		rf.f = nil
		return err
	}
	err := rf.f.Close()
	rf.f = nil
	return err
}

// CloseAndRemove closes the file and deletes it. Used after Compress
// succeeds — the .idt temp file is no longer needed.
func (rf *rawWordsFile) CloseAndRemove() {
	if rf.f != nil {
		_ = rf.w.Flush()
		_ = rf.f.Close()
		rf.f = nil
	}
	_ = os.Remove(rf.filePath)
}

// ForEach walks every record in the file, calling walker(v, compressed)
// for each one. Resets the file offset to 0 before reading so it may be
// called multiple times.
func (rf *rawWordsFile) ForEach(walker func(v []byte, compressed bool) error) error {
	if _, err := rf.f.Seek(0, 0); err != nil {
		return err
	}
	r := bufio.NewReaderSize(rf.f, 256*1024)
	buf := make([]byte, 16*1024)
	for {
		l, e := binary.ReadUvarint(r)
		if e != nil {
			if errors.Is(e, io.EOF) {
				return nil
			}
			return e
		}
		compressed := (l & 1) == 0
		l >>= 1
		if uint64(cap(buf)) < l {
			buf = make([]byte, l)
		} else {
			buf = buf[:l]
		}
		if l > 0 {
			if _, e := io.ReadFull(r, buf); e != nil {
				return e
			}
		}
		if err := walker(buf[:l], compressed); err != nil {
			return err
		}
	}
}
