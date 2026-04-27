package entitygen

import (
	"math"
	mrand "math/rand"
)

// GenerateSlotDistribution returns a slot-count per contract for n contracts,
// drawn from the chosen Distribution.
//
// Each Distribution branch uses a fixed RNG draw count per contract:
//   - PowerLaw    : 1 × Float64
//   - Exponential : 1 × Float64
//   - Uniform     : 1 × Intn
func GenerateSlotDistribution(rng *mrand.Rand, dist Distribution, minSlots, maxSlots, n int) []int {
	distribution := make([]int, n)

	switch dist {
	case PowerLaw:
		// Power-law distribution (Pareto) - 80/20 rule
		// Most contracts have few slots, few contracts have many
		alpha := 1.5 // Shape parameter
		for i := range distribution {
			// Inverse CDF of Pareto distribution
			u := rng.Float64()
			slots := float64(minSlots) / math.Pow(1-u, 1/alpha)
			if slots > float64(maxSlots) {
				slots = float64(maxSlots)
			}
			distribution[i] = int(slots)
		}

	case Exponential:
		// Exponential decay
		lambda := math.Log(2) / float64(maxSlots/4)
		for i := range distribution {
			u := rng.Float64()
			slots := -math.Log(1-u) / lambda
			slots = math.Max(float64(minSlots), math.Min(slots, float64(maxSlots)))
			distribution[i] = int(slots)
		}

	case Uniform:
		// Uniform distribution
		for i := range distribution {
			distribution[i] = minSlots + rng.Intn(maxSlots-minSlots+1)
		}
	}

	return distribution
}

// GenerateSlotCount returns a single slot count drawn from the chosen
// Distribution, consuming the same per-contract RNG draws as
// GenerateSlotDistribution. Used by the bintrie producer goroutine which
// generates contracts one at a time and would otherwise waste an
// O(NumContracts) array allocation.
func GenerateSlotCount(rng *mrand.Rand, dist Distribution, minSlots, maxSlots int) int {
	switch dist {
	case PowerLaw:
		alpha := 1.5
		u := rng.Float64()
		slots := float64(minSlots) / math.Pow(1-u, 1/alpha)
		if slots > float64(maxSlots) {
			slots = float64(maxSlots)
		}
		return int(slots)
	case Exponential:
		lambda := math.Log(2) / float64(maxSlots/4)
		u := rng.Float64()
		slots := -math.Log(1-u) / lambda
		slots = math.Max(float64(minSlots), math.Min(slots, float64(maxSlots)))
		return int(slots)
	case Uniform:
		return minSlots + rng.Intn(maxSlots-minSlots+1)
	}
	// Default fallback (should never hit if Distribution is valid)
	return minSlots
}
