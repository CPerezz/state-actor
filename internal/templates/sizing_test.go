package templates

import (
	"iter"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestSynthesizeSlotsDeterministic(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	const seed = int64(42)
	const n = 100

	a := collectPairs(SynthesizeSlots(seed, addr, "test", n))
	b := collectPairs(SynthesizeSlots(seed, addr, "test", n))

	if len(a) != n {
		t.Fatalf("iterator produced %d slots, want %d", len(a), n)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("slot %d not deterministic: got %v vs %v", i, a[i], b[i])
		}
	}
}

func TestSynthesizeSlotsDomainSeparation(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	const seed = int64(42)
	const n = 10

	a := collectPairs(SynthesizeSlots(seed, addr, "raw", n))
	b := collectPairs(SynthesizeSlots(seed, addr, "eoa", n))

	if a[0] == b[0] {
		t.Errorf("different domains produced same first key — domain separation broken")
	}
}

func TestSynthesizeSlotsCountZero(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	out := collectPairs(SynthesizeSlots(42, addr, "test", 0))
	if len(out) != 0 {
		t.Errorf("count=0 produced %d slots", len(out))
	}
}

func TestSynthesizeSlotsKeysDistinct(t *testing.T) {
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	const n = 1000
	seen := make(map[common.Hash]struct{})
	for k := range collectMap(SynthesizeSlots(42, addr, "test", n)) {
		seen[k] = struct{}{}
	}
	if len(seen) != n {
		t.Errorf("got %d unique keys for %d slots — collision in synthesizer", len(seen), n)
	}
}

func TestMapToSeqNil(t *testing.T) {
	if MapToSeq(nil) != nil {
		t.Error("MapToSeq(nil) should return nil")
	}
	if MapToSeq(map[common.Hash]common.Hash{}) != nil {
		t.Error("MapToSeq(empty) should return nil")
	}
}

func TestMapToSeqRoundtrip(t *testing.T) {
	in := map[common.Hash]common.Hash{
		common.HexToHash("0x01"): common.HexToHash("0xaa"),
		common.HexToHash("0x02"): common.HexToHash("0xbb"),
	}
	got := CollectMap(MapToSeq(in))
	if len(got) != 2 || got[common.HexToHash("0x01")] != common.HexToHash("0xaa") {
		t.Errorf("roundtrip lost data: %v", got)
	}
}

func TestConcat(t *testing.T) {
	a := MapToSeq(map[common.Hash]common.Hash{common.HexToHash("0x01"): common.HexToHash("0xaa")})
	b := MapToSeq(map[common.Hash]common.Hash{common.HexToHash("0x02"): common.HexToHash("0xbb")})
	got := CollectMap(Concat(a, nil, b))
	if len(got) != 2 {
		t.Errorf("Concat lost entries: %v", got)
	}
}

// kvPair is the test view of a (key, value) iter.Seq2 emission.
type kvPair struct{ K, V common.Hash }

// collectPairs drains an iter.Seq2 into an ordered slice. Used for tests
// that need to assert order/determinism.
func collectPairs(seq iter.Seq2[common.Hash, common.Hash]) []kvPair {
	var out []kvPair
	if seq == nil {
		return out
	}
	seq(func(k, v common.Hash) bool {
		out = append(out, kvPair{k, v})
		return true
	})
	return out
}

// collectMap drains an iter.Seq2 into a map. Used for tests that need
// uniqueness/membership checks but not iteration order.
func collectMap(seq iter.Seq2[common.Hash, common.Hash]) map[common.Hash]common.Hash {
	out := map[common.Hash]common.Hash{}
	if seq == nil {
		return out
	}
	seq(func(k, v common.Hash) bool {
		out[k] = v
		return true
	})
	return out
}
