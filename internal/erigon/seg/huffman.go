package seg

import (
	"cmp"
	"container/heap"
	"math/bits"
	"slices"
)

// position represents one entry in the position Huffman tree —
// a (length-or-terminator value, usage count) pair. Mirrors
// `db/seg/compress.go:659-665 Position`.
type position struct {
	uses     uint64
	pos      uint64 // the (length+1) value, or 0 for terminator
	code     uint64 // numeric Huffman code (final after build)
	codeBits int    // number of bits in code
	depth    int    // Huffman tree depth (== codeBits for canonical codes)
}

// positionHuff is an interior node of the position Huffman tree.
// Mirrors `db/seg/compress.go:667-673 PositionHuff`.
type positionHuff struct {
	p0         *position
	p1         *position
	h0         *positionHuff
	h1         *positionHuff
	uses       uint64
	tieBreaker uint64
}

func (h *positionHuff) addZero() {
	if h.p0 != nil {
		h.p0.code <<= 1
		h.p0.codeBits++
	} else {
		h.h0.addZero()
	}
	if h.p1 != nil {
		h.p1.code <<= 1
		h.p1.codeBits++
	} else {
		h.h1.addZero()
	}
}

func (h *positionHuff) addOne() {
	if h.p0 != nil {
		h.p0.code <<= 1
		h.p0.code++
		h.p0.codeBits++
	} else {
		h.h0.addOne()
	}
	if h.p1 != nil {
		h.p1.code <<= 1
		h.p1.code++
		h.p1.codeBits++
	} else {
		h.h1.addOne()
	}
}

func (h *positionHuff) setDepth(depth int) {
	if h.p0 != nil {
		h.p0.depth = depth + 1
		h.p0.uses = 0
	}
	if h.p1 != nil {
		h.p1.depth = depth + 1
		h.p1.uses = 0
	}
	if h.h0 != nil {
		h.h0.setDepth(depth + 1)
	}
	if h.h1 != nil {
		h.h1.setDepth(depth + 1)
	}
}

// positionListCmp matches `db/seg/compress.go:729-734 positionListCmp`.
// Ordering: ascending uses, ties broken by bit-reversed code (so the
// resulting Huffman code is canonical and deterministic across runs).
func positionListCmp(i, j *position) int {
	if i.uses == j.uses {
		return cmp.Compare(bits.Reverse64(i.code), bits.Reverse64(j.code))
	}
	return cmp.Compare(i.uses, j.uses)
}

// positionHeap is the priority queue used during Huffman tree
// construction. Matches `db/seg/compress.go:736-768`. Lowest uses wins;
// tieBreaker is monotonically increased on every push to keep ordering
// deterministic when two subtrees have equal aggregate uses.
type positionHeap []*positionHuff

func (ph positionHeap) Len() int { return len(ph) }
func (ph positionHeap) Less(i, j int) bool {
	if ph[i].uses == ph[j].uses {
		return ph[i].tieBreaker < ph[j].tieBreaker
	}
	return ph[i].uses < ph[j].uses
}
func (ph *positionHeap) Swap(i, j int) { (*ph)[i], (*ph)[j] = (*ph)[j], (*ph)[i] }
func (ph *positionHeap) Push(x any) {
	*ph = append(*ph, x.(*positionHuff))
}
func (ph *positionHeap) Pop() any {
	old := *ph
	n := len(old)
	x := old[n-1]
	old[n-1] = nil
	*ph = old[0 : n-1]
	return x
}

// buildPositionHuffman takes a posMap[(length+1 or 0 terminator)] = uses
// and returns the sorted, code-assigned position list. Mirrors the inner
// loop of `db/seg/parallel_compress.go:758-825 buildAndWritePosDict`
// EXCEPT for the file-writing — emission is handled by the Compressor.
//
// The returned slice's order is the FINAL serialization order (sorted by
// (uses, bits.Reverse64(code)) AFTER Huffman codes are assigned). Each
// position has its final `.depth`, `.code`, `.codeBits` set so the
// caller can directly Huffman-encode words and serialize the dict.
//
// pos2code maps the position value back to its position struct for
// O(1) lookup during the per-word encode pass.
func buildPositionHuffman(posMap map[uint64]uint64) (positionList []*position, pos2code map[uint64]*position) {
	positionList = make([]*position, 0, len(posMap))
	pos2code = make(map[uint64]*position, len(posMap))
	for pos, uses := range posMap {
		p := &position{pos: pos, uses: uses, code: pos, codeBits: 0}
		positionList = append(positionList, p)
		pos2code[pos] = p
	}
	// FIRST sort: (uses, bits.Reverse64(code)) where code initially == pos.
	// Erigon does this at parallel_compress.go:767.
	slices.SortFunc(positionList, positionListCmp)

	// Build Huffman tree exactly as parallel_compress.go:769-802.
	var ph positionHeap
	heap.Init(&ph)
	i := 0
	tieBreaker := uint64(0)
	for ph.Len()+(len(positionList)-i) > 1 {
		h := &positionHuff{tieBreaker: tieBreaker}
		// Pick left child: prefer the heap top if its uses are strictly
		// less than the next-in-list (matches Erigon's tie-breaking).
		if ph.Len() > 0 && (i >= len(positionList) || ph[0].uses < positionList[i].uses) {
			h.h0 = heap.Pop(&ph).(*positionHuff)
			h.h0.addZero()
			h.uses += h.h0.uses
		} else {
			h.p0 = positionList[i]
			h.p0.code = 0
			h.p0.codeBits = 1
			h.uses += h.p0.uses
			i++
		}
		// Pick right child.
		if ph.Len() > 0 && (i >= len(positionList) || ph[0].uses < positionList[i].uses) {
			h.h1 = heap.Pop(&ph).(*positionHuff)
			h.h1.addOne()
			h.uses += h.h1.uses
		} else {
			h.p1 = positionList[i]
			h.p1.code = 1
			h.p1.codeBits = 1
			h.uses += h.p1.uses
			i++
		}
		tieBreaker++
		heap.Push(&ph, h)
	}
	if ph.Len() > 0 {
		root := heap.Pop(&ph).(*positionHuff)
		root.setDepth(0)
	}
	// SECOND sort happens at parallel_compress.go:813. After the build,
	// each position has its final code/depth, so the list re-sorts by
	// (uses=0 after setDepth, bits.Reverse64(code)) — i.e. canonical
	// ordering by reverse-bit code. Note that setDepth zeroes uses so
	// this is effectively a sort by bits.Reverse64(code).
	slices.SortFunc(positionList, positionListCmp)
	return positionList, pos2code
}
