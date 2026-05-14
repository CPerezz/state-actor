package spec

import (
	"fmt"
	"strings"
)

// approxSizeWarnThreshold flags suspicious approximate_size_bytes
// values (likely unit mistakes).
const approxSizeWarnThreshold uint64 = 1 << 40 // 1 TiB

// MaxCodeSize is EIP-170's contract-code ceiling (24,576 bytes).
const MaxCodeSize = 24576

// ValidateResult carries non-fatal warnings; hard rules return an error.
type ValidateResult struct {
	Warnings []string
}

// Validate applies the schema rules to s. knownTemplates is the set of
// registered template names; pass an empty slice to skip that check.
// Rules are documented in docs/SPEC.md.
func (s *Spec) Validate(knownTemplates []string) (ValidateResult, error) {
	result := ValidateResult{}

	knownTemplateSet := make(map[string]struct{}, len(knownTemplates))
	for _, t := range knownTemplates {
		knownTemplateSet[t] = struct{}{}
	}

	if len(s.Entities) == 0 {
		return result, fmt.Errorf("spec has no entities; at least one is required")
	}

	seenAddrs := make(map[string]int) // lowercased address hex → first index it appeared at

	for i, e := range s.Entities {
		anchor := fmt.Sprintf("entities[%d]", i)
		if e.Name != "" {
			anchor = fmt.Sprintf("entities[%d] (name=%q)", i, e.Name)
		}

		switch e.Kind {
		case KindContract:
			hasTemplate := e.Template != ""
			hasCode := len(e.Code) > 0
			if !hasTemplate && !hasCode {
				return result, fmt.Errorf("%s: kind=contract requires either `template` or `code`", anchor)
			}
			if hasTemplate && hasCode {
				return result, fmt.Errorf("%s: kind=contract must NOT set both `template` and `code`", anchor)
			}
			if hasTemplate && len(knownTemplateSet) > 0 {
				if _, ok := knownTemplateSet[e.Template]; !ok {
					return result, fmt.Errorf("%s: unknown template %q (known: %v)", anchor, e.Template, knownTemplates)
				}
			}
			if !hasTemplate && len(e.Parameters) > 0 {
				return result, fmt.Errorf("%s: `parameters` is only valid with `template`", anchor)
			}
		case KindEOA:
			if e.Template != "" {
				return result, fmt.Errorf("%s: kind=eoa must NOT set `template`", anchor)
			}
			if len(e.Parameters) > 0 {
				return result, fmt.Errorf("%s: kind=eoa must NOT set `parameters`", anchor)
			}
		case "":
			return result, fmt.Errorf("%s: missing required field `kind`", anchor)
		default:
			return result, fmt.Errorf("%s: unknown kind %q (expected %q or %q)", anchor, e.Kind, KindContract, KindEOA)
		}

		// Derived-address collisions are caught later in specbuild.Build.
		if e.Address != nil {
			key := strings.ToLower(e.Address.Address().Hex())
			if prev, dup := seenAddrs[key]; dup {
				return result, fmt.Errorf("%s: duplicate address %s (first appeared at entities[%d])", anchor, key, prev)
			}
			seenAddrs[key] = i
		}

		if e.ApproximateSizeBytes > approxSizeWarnThreshold {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s: approximate_size_bytes=%d exceeds %d (1 TiB); likely a unit mistake",
					anchor, e.ApproximateSizeBytes, approxSizeWarnThreshold))
		}

		if len(e.Code) > MaxCodeSize {
			return result, fmt.Errorf("%s: code length %d exceeds EIP-170 limit (%d bytes); contracts with oversize code are unmineable on mainnet-rules forks",
				anchor, len(e.Code), MaxCodeSize)
		}
	}

	return result, nil
}
