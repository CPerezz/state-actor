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
	// Seed drives deterministic name→address derivation and
	// synthesized storage generation.
	Seed int64
	// ClientName is "geth", "besu", "nethermind", or "reth".
	ClientName string
	// Sizer translates approximate_size_bytes → slot count.
	Sizer templates.SizeApproximator
}

// Diagnostics carries non-fatal warnings surfaced to the CLI.
type Diagnostics struct {
	Warnings []string
}

// Build translates a parsed Spec into the flat slice of PreAllocEntity
// records each writer consumes. Per entity: resolve address → pick
// template → validate parameters → Expand. Collisions across emitted
// addresses (including synthesized) are rejected.
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

	// Lower-case hex → first-emitting entity index.
	seenAddrs := make(map[string]int, len(s.Entities))

	for i, e := range s.Entities {
		tmpl, err := pickTemplate(e)
		if err != nil {
			return nil, diag, fmt.Errorf("entities[%d]: %w", i, err)
		}

		// Defense in depth for programmatic callers bypassing spec.Validate.
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

// pickTemplate maps a spec entity to its handling template.
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
		return nil, fmt.Errorf("kind=contract requires either template: or code:")
	default:
		return nil, fmt.Errorf("unknown kind %q", e.Kind)
	}
}

var _ = (*common.Address)(nil)
