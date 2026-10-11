package reth

import (
	"bytes"
	"math/rand/v2"
	"sort"
	"testing"
)

// refBranch is one true divergence point in the nibble trie built from a
// sorted, deduplicated set of fixed-length keys (independent of HashBuilder):
// a prefix where two or more keys disagree on the next nibble.
type refBranch struct {
	hasLeafChild bool // at least one child slot holds exactly one key
	stored       bool // storeInDbTrie per the alloy_trie rule (see below)
}

// refAnalyze walks (ks, depth) — ks all sharing a `depth`-nibble prefix —
// and returns whether this position (after collapsing any single-child
// "funnel" down to the real divergence point) is a branch, plus its
// storeInDbTrie value.
//
// storeInDbTrie(branch) is true iff either:
//   - it has a "direct" (zero-gap) child branch: the child slot's keys
//     diverge again at the very next nibble, no shared run in between. The
//     branch RLP is always referenced by hash at this size, so alloy_trie
//     sets the parent's hash_mask bit for that slot unconditionally.
//   - it has a child branch reached through a run of shared nibbles (an
//     extension) whose own storeInDbTrie is true — propagated into the
//     parent's tree_mask. If the gapped child itself has nothing stored
//     below it, nothing propagates: caching that path saves nothing.
//
// This mirrors alloy_trie::HashBuilder::store_branch_node / build_extension,
// derived independently from the key set rather than by re-deriving
// hash_builder.go's stack bookkeeping, and is cross-checked against the
// real HashBuilder by TestHashBuilderEmissions_MatchesIndependentRecount
// over random and extension-heavy key sets.
func refAnalyze(ks [][]byte, depth int) (isBranch bool, info refBranch) {
	if len(ks) < 2 {
		return false, refBranch{}
	}
	groups := map[byte][][]byte{}
	var order []byte
	for _, k := range ks {
		c := k[depth]
		if _, ok := groups[c]; !ok {
			order = append(order, c)
		}
		groups[c] = append(groups[c], k)
	}
	if len(groups) == 1 {
		// Shared nibble, not yet a divergence: funnel one level deeper.
		return refAnalyze(groups[order[0]], depth+1)
	}
	for _, c := range order {
		g := groups[c]
		if len(g) < 2 {
			info.hasLeafChild = true
			continue
		}
		sub := map[byte]bool{}
		for _, k := range g {
			sub[k[depth+1]] = true
		}
		directlyDiverges := len(sub) >= 2
		if directlyDiverges {
			info.stored = true
		} else if _, childInfo := refAnalyze(g, depth+1); childInfo.stored {
			info.stored = true
		}
	}
	return true, info
}

// referenceBranches returns, for every true branch prefix (nibble path,
// root excluded), its refBranch info.
func referenceBranches(nibKeys [][]byte) map[string]refBranch {
	out := map[string]refBranch{}
	var rec func(ks [][]byte, depth int)
	rec = func(ks [][]byte, depth int) {
		if len(ks) < 2 {
			return
		}
		groups := map[byte][][]byte{}
		var order []byte
		for _, k := range ks {
			c := k[depth]
			if _, ok := groups[c]; !ok {
				order = append(order, c)
			}
			groups[c] = append(groups[c], k)
		}
		if len(groups) >= 2 {
			_, info := refAnalyze(ks, depth)
			if depth > 0 {
				out[string(ks[0][:depth])] = info
			}
		}
		for _, c := range order {
			rec(groups[c], depth+1)
		}
	}
	rec(nibKeys, 0)
	return out
}

// randomKeys returns n distinct sorted 32-byte keys from a seeded PCG.
func randomKeys(seed uint64, n int) [][]byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	keys := make([][]byte, n)
	for i := range keys {
		k := make([]byte, 32)
		for b := range k {
			k[b] = byte(r.IntN(256))
		}
		keys[i] = k
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	dedup := keys[:0]
	for i, k := range keys {
		if i == 0 || !bytes.Equal(k, keys[i-1]) {
			dedup = append(dedup, k)
		}
	}
	return dedup
}

// extensionForcingKeys returns keys engineered to share long common nibble
// runs before diverging, forcing extension nodes between branches.
func extensionForcingKeys(seed uint64) [][]byte {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var keys [][]byte
	prefix := make([]byte, 20)
	for i := range prefix {
		prefix[i] = 0xAB
	}
	for i := range 10 {
		k := make([]byte, 32)
		copy(k, prefix)
		k[20] = byte(i)
		for b := 21; b < 32; b++ {
			k[b] = byte(r.IntN(256))
		}
		keys = append(keys, k)
	}
	// Second cluster: two-level nested extensions (share 10 bytes, branch,
	// share another run, branch again).
	for i := range 5 {
		for j := range 5 {
			k := make([]byte, 32)
			for b := range k {
				k[b] = 0x11
			}
			k[10] = byte(i)
			k[25] = byte(j)
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}

// emission records one (path, BranchNodeCompact) pair from a build.
type emission struct {
	path StoredNibbles
	node BranchNodeCompact
}

// TestHashBuilderEmissions_MatchesIndependentRecount pins the alloy_trie
// store_branch_node rule (tree_mask | hash_mask != 0) against an
// independent reconstruction of the trie's branch structure from the
// sorted key set, over random pure-leaf builds of varying size plus
// extension-heavy structured key sets. Also checks (d) the state_mask
// superset invariants and (e) that persisting rows doesn't change the root.
func TestHashBuilderEmissions_MatchesIndependentRecount(t *testing.T) {
	cases := map[string][][]byte{
		"n=1":        randomKeys(1, 1),
		"n=2":        randomKeys(2, 2),
		"n=17":       randomKeys(3, 17),
		"n=1000":     randomKeys(4, 1000),
		"n=50000":    randomKeys(5, 50000),
		"extensions": extensionForcingKeys(6),
	}

	for name, keys := range cases {
		t.Run(name, func(t *testing.T) {
			nibKeys := make([][]byte, len(keys))
			for i, k := range keys {
				nibKeys[i] = unpackNibbles(k)
			}
			ref := referenceBranches(nibKeys)

			var emissions []emission
			hb := NewHashBuilder(func(p StoredNibbles, n BranchNodeCompact) error {
				emissions = append(emissions, emission{p, n})
				return nil
			})
			for _, k := range keys {
				if err := hb.AddLeaf(unpackNibbles(k), k); err != nil {
					t.Fatalf("AddLeaf: %v", err)
				}
			}
			root := hb.Root()

			emitted := map[string]BranchNodeCompact{}
			for _, e := range emissions {
				path := string(e.path.Nibbles[:e.path.Length])
				emitted[path] = e.node

				// (a) every emitted row has tree_mask | hash_mask != 0.
				if e.node.TreeMask == 0 && e.node.HashMask == 0 {
					t.Errorf("path %x: emitted with tree_mask=hash_mask=0", path)
				}
				// (d) mask subset invariants + Hashes length.
				if leftover := e.node.TreeMask &^ e.node.StateMask; leftover != 0 {
					t.Errorf("path %x: tree_mask not subset of state_mask: leftover=%04x", path, leftover)
				}
				if leftover := e.node.HashMask &^ e.node.StateMask; leftover != 0 {
					t.Errorf("path %x: hash_mask not subset of state_mask: leftover=%04x", path, leftover)
				}
				if got, want := len(e.node.Hashes), popcount16(e.node.HashMask); got != want {
					t.Errorf("path %x: len(Hashes)=%d, want popcount(hash_mask)=%d", path, got, want)
				}
			}

			mismatches := 0
			report := func(format string, args ...any) {
				mismatches++
				if mismatches <= 10 {
					t.Errorf(format, args...)
				}
			}
			for path, info := range ref {
				node, isEmitted := emitted[path]
				if info.stored != isEmitted {
					report("path %x: reference stored=%v, emitted=%v", path, info.stored, isEmitted)
					continue
				}
				// (b) no emitted row is a bottom branch (all children
				// leaves): a bottom branch is exactly !info.stored, so any
				// emitted (stored) row must have tree_mask|hash_mask set —
				// re-assert directly against the node to catch drift
				// between info.stored and the actual masks.
				if isEmitted && node.TreeMask == 0 && node.HashMask == 0 {
					t.Errorf("path %x: emitted bottom branch (all children leaves)", path)
				}
			}
			for path := range emitted {
				if path == "" {
					continue // root is always emitted; excluded from ref by convention
				}
				if _, ok := ref[path]; !ok {
					report("path %x: emitted but not a branch per independent reconstruction", path)
				}
			}
			if mismatches > 0 {
				t.Errorf("%s: %d path mismatches (branches=%d, emitted=%d)", name, mismatches, len(ref), len(emitted))
			}

			// (e) compute-only (nil-emit) build reaches the same root.
			noop := NewHashBuilder(func(StoredNibbles, BranchNodeCompact) error { return nil })
			for _, k := range keys {
				if err := noop.AddLeaf(unpackNibbles(k), k); err != nil {
					t.Fatalf("AddLeaf (noop): %v", err)
				}
			}
			if gotRoot := noop.Root(); gotRoot != root {
				t.Errorf("%s: compute-only root %s != persisting root %s", name, gotRoot.Hex(), root.Hex())
			}
		})
	}
}

// TestHashBuilderEmissions_PostOrder pins HashBuilder's actual emission
// order: a branch is only emitted after every emission in its own subtree
// (descendant-before-ancestor), and the root (path Length 0) is always
// last. This is NOT a global lexicographic sort by StoredNibbles path —
// completing a child subtree and returning to a shallower ancestor is a
// "jump" to a smaller key — which is why callers persist rows with
// per-key MDBX Put rather than relying on cursor.Append's sorted-only
// fast path (see HashBuilder's "Path emission order" doc).
func TestHashBuilderEmissions_PostOrder(t *testing.T) {
	keys := randomKeys(7, 2000)
	var paths []StoredNibbles
	hb := NewHashBuilder(func(p StoredNibbles, _ BranchNodeCompact) error {
		paths = append(paths, p)
		return nil
	})
	for _, k := range keys {
		if err := hb.AddLeaf(unpackNibbles(k), k); err != nil {
			t.Fatalf("AddLeaf: %v", err)
		}
	}
	hb.Root()

	if len(paths) == 0 {
		t.Fatal("expected at least one emission for a 2000-key build")
	}
	nibbles := func(p StoredNibbles) []byte { return p.Nibbles[:p.Length] }
	for i, a := range paths {
		for _, b := range paths[i+1:] {
			if bytes.HasPrefix(nibbles(b), nibbles(a)) && b.Length > a.Length {
				t.Errorf("ancestor %x emitted before its descendant %x", nibbles(a), nibbles(b))
			}
		}
	}
	last := paths[len(paths)-1]
	if last.Length != 0 {
		t.Errorf("final emission's path Length = %d, want 0 (root)", last.Length)
	}
}
