package seg

import "testing"

// TestHuffmanCodeReconstruction verifies that codesFromDepths produces
// the same (code, codeBits) pairs that buildPositionHuffman assigned —
// i.e., the writer and the on-disk-dictionary decoder agree on which
// numerical code corresponds to which (depth, pos) entry.
//
// This is the critical invariant: if these diverge, the bitstream
// decoder will fail to match any code and Iterate returns ErrCorruptedFile.
func TestHuffmanCodeReconstruction(t *testing.T) {
	cases := []map[uint64]uint64{
		// Trivial single-entry — should produce a 1-bit code (Erigon's
		// builder always assigns a leaf code, never an empty tree).
		{0: 1, 2: 1},
		// Skewed: one length dominant, terminator equal-ish.
		{0: 100, 2: 50, 3: 50},
		// Larger, mixed.
		{0: 1000, 3: 200, 6: 200, 11: 100, 17: 50, 33: 25, 65: 10},
	}
	for _, posMap := range cases {
		want, _ := buildPositionHuffman(posMap)
		var raw []rawPos
		for _, p := range want {
			raw = append(raw, rawPos{depth: uint64(p.depth), pos: p.pos})
		}
		gotList, gotMap := codesFromDepths(raw)
		if len(gotList) != len(want) {
			t.Errorf("case %v: len mismatch got=%d want=%d", posMap, len(gotList), len(want))
			continue
		}
		for i, p := range want {
			g := gotList[i]
			if g.pos != p.pos || g.depth != p.depth ||
				g.code != p.code || g.codeBits != p.codeBits {
				t.Errorf("case %v entry %d: got (pos=%d depth=%d code=%d bits=%d), want (pos=%d depth=%d code=%d bits=%d)",
					posMap, i,
					g.pos, g.depth, g.code, g.codeBits,
					p.pos, p.depth, p.code, p.codeBits)
			}
			if gotMap[p.pos] != g {
				t.Errorf("case %v: gotMap[%d] != gotList[%d]", posMap, p.pos, i)
			}
		}
	}
}
