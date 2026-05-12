package specbuild

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/internal/spec"
	"github.com/nerolation/state-actor/internal/templates"
)

// BuildOptions carries the per-run inputs every Template.Expand needs.
type BuildOptions struct {
	// Seed is the RNG seed used for deterministic name→address derivation
	// AND for synthesized storage slot generation. Threaded through every
	// template invocation.
	Seed int64

	// ClientName picks the per-client byte-budget factor in the sizer.
	// One of "geth", "besu", "nethermind", "reth".
	ClientName string

	// Sizer translates approximate_size_bytes → slot count. Built outside
	// this package (in internal/sizecal/) so templates have no import
	// dependency on calibration code.
	Sizer templates.SizeApproximator
}

// Diagnostics carries non-fatal warnings the translator wants to surface
// to the CLI (which prints them before kickoff).
type Diagnostics struct {
	Warnings []string
}

// Build translates a parsed Spec into the flat slice of PreAllocEntity
// records each writer consumes.
//
// Steps per entity:
//  1. Resolve the address (explicit / name-derived / position-derived).
//  2. Pick the template — eoa for kind=eoa, the named template for
//     kind=contract with template:, or raw for kind=contract with code:.
//  3. Validate parameters (defense in depth — spec.Validate already ran).
//  4. Call Template.Expand. The result joins the output slice.
//
// Post-expansion, every emitted PreAllocEntity address is checked for
// collisions across the slice. The spec-time validator only catches
// explicit-address collisions; synthesized (derived) addresses may also
// collide and we must surface those before the writer is invoked.
func Build(s *spec.Spec, opts BuildOptions) ([]templates.PreAllocEntity, Diagnostics, error) {
	var (
		out  []templates.PreAllocEntity
		diag Diagnostics
	)

	if s == nil || len(s.Entities) == 0 {
		return nil, diag, fmt.Errorf("Build: spec has no entities")
	}
	if opts.Sizer == nil {
		return nil, diag, fmt.Errorf("Build: BuildOptions.Sizer is required")
	}

	// Track every emitted address so we can flag post-expansion collisions
	// in the same loop. Maps lower-case hex → first-emitting entity index.
	seenAddrs := make(map[string]int, len(s.Entities))

	for i, e := range s.Entities {
		tmpl, err := pickTemplate(e)
		if err != nil {
			return nil, diag, fmt.Errorf("entities[%d]: %w", i, err)
		}

		// Defense in depth: validate parameters again so a programmatic
		// caller that bypasses spec.Validate still sees the failure.
		if err := tmpl.ValidateParameters(e.Parameters); err != nil {
			return nil, diag, fmt.Errorf("entities[%d]: %w", i, err)
		}

		addr := ResolveAddress(opts.Seed, e, i)

		ctx := templates.Context{
			Seed:            opts.Seed,
			ClientName:      opts.ClientName,
			Sizer:           opts.Sizer,
			ResolvedAddress: addr,
			EntityIndex:     i,
		}

		expanded, err := tmpl.Expand(ctx, e)
		if err != nil {
			return nil, diag, fmt.Errorf("entities[%d] (%s.Expand): %w", i, tmpl.Name(), err)
		}

		for _, pe := range expanded {
			key := strings.ToLower(pe.Address.Hex())
			if prev, dup := seenAddrs[key]; dup {
				return nil, diag, fmt.Errorf(
					"entities[%d] (template %s) produced address %s that collides with entities[%d]",
					i, tmpl.Name(), pe.Address.Hex(), prev)
			}
			seenAddrs[key] = i
			out = append(out, pe)
		}
	}

	return out, diag, nil
}

// pickTemplate maps a spec entity to its handling template. Returns an
// error when the entity is well-formed at parse time but the registry
// doesn't have the requested template (which shouldn't happen if
// spec.Validate received templates.Names(), but defense-in-depth).
func pickTemplate(e spec.Entity) (templates.Template, error) {
	switch e.Kind {
	case spec.KindEOA:
		t, ok := templates.Lookup("eoa")
		if !ok {
			return nil, fmt.Errorf("registry missing required eoa template")
		}
		return t, nil
	case spec.KindContract:
		if e.Template != "" {
			t, ok := templates.Lookup(e.Template)
			if !ok {
				return nil, fmt.Errorf("template %q not registered", e.Template)
			}
			return t, nil
		}
		if len(e.Code) > 0 {
			t, ok := templates.Lookup("raw")
			if !ok {
				return nil, fmt.Errorf("registry missing required raw template")
			}
			return t, nil
		}
		// spec.Validate already catches this; defensive only.
		return nil, fmt.Errorf("kind=contract requires either template: or code:")
	default:
		return nil, fmt.Errorf("unknown kind %q", e.Kind)
	}
}

// Ensure common.Address is referenced — keeps the import non-stale if a
// future refactor drops the inline use. Cheap, zero runtime cost.
var _ = (*common.Address)(nil)
