package entitygen

import "strings"

// Distribution selects how storage-slot counts vary across contracts.
type Distribution int

const (
	// PowerLaw — Pareto distribution, 80/20 rule. Most contracts have few
	// slots; a few have many. Matches real Ethereum state where a handful of
	// contracts (Uniswap, etc.) hold millions of slots.
	PowerLaw Distribution = iota

	// Uniform — all contracts have similar slot counts.
	Uniform

	// Exponential — exponential decay in slot counts.
	Exponential
)

// ParseDistribution parses a human-readable distribution name. Unknown values
// fall back to PowerLaw to preserve historical CLI behavior.
func ParseDistribution(s string) Distribution {
	switch strings.ToLower(s) {
	case "uniform":
		return Uniform
	case "exponential":
		return Exponential
	default:
		return PowerLaw
	}
}
