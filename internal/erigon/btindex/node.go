package btindex

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Wire format for the node-list tail is a direct port of
// `encodeListNodes` (bps_tree.go:149-162) followed by per-node
// `Node.Encode` (bps_tree.go:178-184):
//
//	[8B BE: nodeCount]
//	per node:
//	  [8B BE: di — key ordinal]
//	  [2B BE: keyLen]
//	  [keyLen bytes: key]
//
// The reader side decodes via `decodeListNodes` (bps_tree.go:164-176)
// — we mirror only the encode path here. A round-trip decoder
// (`decodeListNodes`) is provided for tests but kept unexported.

const nodeHeaderLen = 10 // 8B di + 2B keyLen

// encodeListNodes writes the node tail to w. Mirrors `encodeListNodes`
// at bps_tree.go:149-162.
//
// Erigon allocates a fresh 8-byte numBuf, writes the count, then for
// each node writes the result of `Node.Encode()` (a freshly-allocated
// per-node buffer). We mirror the byte layout exactly but avoid the
// per-node allocation by writing the di + keyLen header into a stack
// buffer and the key bytes directly via a separate Write.
func encodeListNodes(nodes []node, w io.Writer) error {
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], uint64(len(nodes)))
	if _, err := w.Write(numBuf[:]); err != nil {
		return err
	}
	var hdr [nodeHeaderLen]byte
	for i := range nodes {
		n := &nodes[i]
		// Erigon caps keyLen at uint16 — our caller should never
		// exceed this for the Account/Storage/Code domains (32-byte
		// keys typical). Reject explicitly to match Erigon's truncating
		// cast (`uint16(len(n.key))`) being load-bearing.
		if len(n.key) > 0xFFFF {
			return fmt.Errorf("btindex: node key length %d exceeds uint16 max", len(n.key))
		}
		binary.BigEndian.PutUint64(hdr[:8], n.di)
		binary.BigEndian.PutUint16(hdr[8:10], uint16(len(n.key)))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if len(n.key) > 0 {
			if _, err := w.Write(n.key); err != nil {
				return err
			}
		}
	}
	return nil
}

// decodeListNodes parses the node tail back into a slice. Mirrors
// `decodeListNodes` at bps_tree.go:164-176. Unexported because it
// only exists for round-trip tests in this package — production
// callers read .bt files via Erigon's reader, never ours.
//
// data must be a slice starting at the node-list header (i.e., after
// the EliasFano block). Returns the parsed nodes and the number of
// bytes consumed.
func decodeListNodes(data []byte) ([]node, int, error) {
	if len(data) < 8 {
		return nil, 0, errors.New("btindex: node list too short for count header")
	}
	count := binary.BigEndian.Uint64(data[:8])
	pos := 8
	nodes := make([]node, count)
	for i := uint64(0); i < count; i++ {
		if len(data[pos:]) < nodeHeaderLen {
			return nil, 0, fmt.Errorf("btindex: node %d header truncated", i)
		}
		di := binary.BigEndian.Uint64(data[pos : pos+8])
		l := int(binary.BigEndian.Uint16(data[pos+8 : pos+10]))
		pos += nodeHeaderLen
		if len(data[pos:]) < l {
			return nil, 0, fmt.Errorf("btindex: node %d key truncated", i)
		}
		// Erigon zero-copies into the mmap; we copy out because the
		// caller's data slice has unknown lifetime.
		key := make([]byte, l)
		copy(key, data[pos:pos+l])
		pos += l
		nodes[i] = node{key: key, di: di}
	}
	return nodes, pos, nil
}
