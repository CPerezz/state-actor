package specbuild

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/internal/spec"
	"github.com/nerolation/state-actor/internal/templates"
)

// fixedSizer is a SizeApproximator stub for tests. The real one lives in
// internal/sizecal/ (Part 5), but specbuild tests deliberately don't depend
// on it.
type fixedSizer struct{ bytesPerSlot uint64 }

func (s fixedSizer) SlotsForBytes(client string, bytes uint64) int {
	if s.bytesPerSlot == 0 {
		return 0
	}
	return int(bytes / s.bytesPerSlot)
}

var defaultOpts = BuildOptions{
	Seed:       42,
	ClientName: "geth",
	Sizer:      fixedSizer{bytesPerSlot: 64},
}

func parseSpec(t *testing.T, src string) *spec.Spec {
	t.Helper()
	s, err := spec.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func TestBuildStory1(t *testing.T) {
	// Story 1: three ERC-20s of decreasing size + five 7702 EOAs.
	s, err := spec.ParseFile("../spec/testdata/valid-story1.yaml")
	if err != nil {
		t.Fatalf("load story 1: %v", err)
	}
	if _, err := s.Validate(templates.Names()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	pre, _, err := Build(s, defaultOpts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := len(pre), 8; got != want {
		t.Fatalf("entity count: got %d, want %d", got, want)
	}
	// First three are ERC-20s — should carry non-empty Code (the runtime
	// bytecode stub) and a non-nil Storage iterator.
	for i := 0; i < 3; i++ {
		if len(pre[i].Code) == 0 {
			t.Errorf("entity[%d] (erc20): Code is empty", i)
		}
		if pre[i].Storage == nil {
			t.Errorf("entity[%d] (erc20): Storage is nil", i)
		}
	}
	// Last five are 7702 EOAs — Code is the 23-byte delegation marker.
	for i := 3; i < 8; i++ {
		if len(pre[i].Code) != 23 {
			t.Errorf("entity[%d] (eoa): Code length = %d, want 23", i, len(pre[i].Code))
		}
	}
}

func TestBuildStory2(t *testing.T) {
	// Story 2: three bloated 7702 EOAs.
	s, err := spec.ParseFile("../spec/testdata/valid-story2.yaml")
	if err != nil {
		t.Fatalf("load story 2: %v", err)
	}
	if _, err := s.Validate(templates.Names()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	pre, _, err := Build(s, defaultOpts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got, want := len(pre), 3; got != want {
		t.Fatalf("entity count: got %d, want %d", got, want)
	}
	for i, e := range pre {
		if e.Storage == nil {
			t.Errorf("entity[%d] (bloated eoa): Storage is nil — bloat slots missing", i)
		}
	}
}

func TestBuildAllFeatures(t *testing.T) {
	// Mixed-spec fixture exercising every v1 feature.
	s, err := spec.ParseFile("../spec/testdata/valid-all-features.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := s.Validate(templates.Names()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	pre, _, err := Build(s, defaultOpts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(pre) != 7 {
		t.Fatalf("entity count: got %d, want 7", len(pre))
	}

	// entities[0]: explicit address 0x...aaaa
	wantExplicit := common.HexToAddress("0x000000000000000000000000000000000000aaaa")
	if pre[0].Address != wantExplicit {
		t.Errorf("entity[0] (explicit) address: got %v, want %v", pre[0].Address, wantExplicit)
	}
	// entities[1]: name-derived (named-token). Address must be stable.
	addr1Run1 := pre[1].Address
	pre2, _, _ := Build(s, defaultOpts)
	if pre2[1].Address != addr1Run1 {
		t.Errorf("entity[1] (name-derived) must be stable across runs: %v vs %v",
			addr1Run1, pre2[1].Address)
	}
}

func TestBuildPositionDerivedReordering(t *testing.T) {
	// Two NAMED entities at positions 0 and 1. Swapping them: the
	// name-derived address follows the entity's `name:` field — NOT its
	// position — so entity "alpha" lands at the same address in both
	// specs. (Top-level `name:` is what derive.go uses; the `name:` field
	// inside `parameters:` is template-specific metadata, distinct.)
	yamlA := `entities:
  - kind: contract
    template: erc20
    name: alpha
    parameters:
      symbol: A
      name: A
      decimals: 18
  - kind: contract
    template: erc20
    name: beta
    parameters:
      symbol: B
      name: B
      decimals: 18`

	yamlB := `entities:
  - kind: contract
    template: erc20
    name: beta
    parameters:
      symbol: B
      name: B
      decimals: 18
  - kind: contract
    template: erc20
    name: alpha
    parameters:
      symbol: A
      name: A
      decimals: 18`

	specA := parseSpec(t, yamlA)
	specB := parseSpec(t, yamlB)

	// Both have name-derived addresses (name: "A" and name: "B"). Swap
	// position → addresses must follow the names, not the position,
	// because name-derivation wins over position-derivation. So entity A
	// is at the same address in both specs.
	preA, _, _ := Build(specA, defaultOpts)
	preB, _, _ := Build(specB, defaultOpts)
	// In specA, entity 0 = name "alpha". In specB, entity 1 = name "alpha".
	if preA[0].Address != preB[1].Address {
		t.Errorf("named entity 'alpha' should match across reorderings: %v vs %v",
			preA[0].Address, preB[1].Address)
	}
}

func TestBuildPositionDerivedDependsOnIndex(t *testing.T) {
	// Two TRULY anonymous entities (no name, no address). Reordering
	// changes their derived addresses.
	yamlA := `entities:
  - kind: contract
    code: "0x01"
  - kind: contract
    code: "0x02"`

	yamlB := `entities:
  - kind: contract
    code: "0x02"
  - kind: contract
    code: "0x01"`

	preA, _, _ := Build(parseSpec(t, yamlA), defaultOpts)
	preB, _, _ := Build(parseSpec(t, yamlB), defaultOpts)

	// In specA, entity 0 has code 0x01. In specB, entity 1 has code 0x01.
	// Their *addresses* are both position-derived from indices 0 vs 1, so
	// they should DIFFER even though the entity content is the same.
	if preA[0].Address == preB[1].Address {
		t.Errorf("position-derived addresses must depend on index, not content")
	}
}

func TestBuildDetectsCrossEntityAddressCollision(t *testing.T) {
	// Two entities with different names but engineered (here, just same
	// name) to produce the same derived address. spec.Validate doesn't
	// catch this because explicit-address dup check only covers explicit
	// addresses; the post-expansion check in Build does.
	yamlSrc := `entities:
  - kind: contract
    template: erc20
    name: collider
    parameters:
      symbol: A
      name: A
      decimals: 18
  - kind: contract
    template: erc20
    name: collider
    parameters:
      symbol: B
      name: B
      decimals: 18`

	s := parseSpec(t, yamlSrc)
	if _, err := s.Validate(templates.Names()); err != nil {
		t.Fatalf("Validate should pass (same name is permitted at parse time): %v", err)
	}
	_, _, err := Build(s, defaultOpts)
	if err == nil {
		t.Fatal("Build should detect collision between same-named entities")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("expected collision error, got %q", err.Error())
	}
}

func TestBuildRejectsNilSizer(t *testing.T) {
	s := parseSpec(t, `entities:
  - kind: eoa
    address: "0x1111111111111111111111111111111111111111"`)
	_, _, err := Build(s, BuildOptions{Seed: 0, ClientName: "geth"})
	if err == nil {
		t.Fatal("expected Sizer-required error")
	}
}

func TestBuildRejectsEmptySpec(t *testing.T) {
	if _, _, err := Build(&spec.Spec{}, defaultOpts); err == nil {
		t.Fatal("expected error for empty spec")
	}
}
