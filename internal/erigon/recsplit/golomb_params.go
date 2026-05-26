package recsplit

import "math"

// bijMemo is Erigon's hardcoded table of optimal Golomb-Rice parameters
// for leaves of size 0..MaxLeafSize. Verbatim copy of golomb_rice.go:29.
var bijMemo = []uint32{0, 0, 0, 1, 3, 4, 5, 7, 8, 10, 11, 12, 14, 15, 16, 18, 19, 21, 22, 23, 25, 26, 28, 29, 30}

// MaxLeafSize must equal Erigon's recsplit.MaxLeafSize (= 24, recsplit.go:55).
// Leaves larger than this are rejected at New().
const MaxLeafSize = 24

// golombBaseLog2 = -log((sqrt(5)+1)/2) is the natural log of 1/phi, used
// in computeGolombRice to derive the bit-length of each non-leaf golomb
// code. Defined at recsplit.go:477.
var golombBaseLog2 = -math.Log((math.Sqrt(5) + 1.0) / 2.0)

// splitParams returns (fanout, unit) for splitting a bucket of size m.
// Mirrors recsplit.go:463-475 — for m above secondaryAggrBound use a
// 2-way (binary) split; below primaryAggrBound use a leafSize-way split.
//
// Returns:
//   - fanout: number of sub-partitions
//   - unit:   size of each sub-partition (last sub-partition may be smaller)
func splitParams(m, leafSize, primaryAggrBound, secondaryAggrBound uint16) (fanout, unit uint16) {
	if m > secondaryAggrBound { // High-level aggregation (fanout 2)
		unit = secondaryAggrBound * (((m+1)/2 + secondaryAggrBound - 1) / secondaryAggrBound)
		fanout = 2
	} else if m > primaryAggrBound { // Second-level aggregation
		unit = primaryAggrBound
		fanout = (m + primaryAggrBound - 1) / primaryAggrBound
	} else { // First-level aggregation
		unit = leafSize
		fanout = (m + leafSize - 1) / leafSize
	}
	return
}

// computeGolombRice fills in `table[m]` with the encoded (length << 27 |
// nodes << 16 | accumulatedLength) packed value, recursively descending
// the sub-tree. Caller MUST have already filled in `table[k]` for all
// k < m (the function reads only smaller indices).
//
// Mirrors recsplit.go:479-512.
func computeGolombRice(m uint16, table []uint32, leafSize, primaryAggrBound, secondaryAggrBound uint16) {
	fanout, unit := splitParams(m, leafSize, primaryAggrBound, secondaryAggrBound)
	k := make([]uint16, fanout)
	k[fanout-1] = m
	for i := uint16(0); i < fanout-1; i++ {
		k[i] = unit
		k[fanout-1] -= k[i]
	}
	sqrtProd := float64(1)
	for i := uint16(0); i < fanout; i++ {
		sqrtProd *= math.Sqrt(float64(k[i]))
	}
	p := math.Sqrt(float64(m)) / (math.Pow(2*math.Pi, (float64(fanout)-1.)/2.0) * sqrtProd)
	golombRiceLength := uint32(math.Ceil(math.Log2(golombBaseLog2 / math.Log1p(-p))))
	if golombRiceLength > 0x1F {
		panic("golombRiceLength > 0x1F")
	}
	table[m] = golombRiceLength << 27
	for i := uint16(0); i < fanout; i++ {
		golombRiceLength += table[k[i]] & 0xFFFF
	}
	if golombRiceLength > 0xFFFF {
		panic("golombRiceLength > 0xFFFF")
	}
	table[m] |= golombRiceLength
	nodes := uint32(1)
	for i := uint16(0); i < fanout; i++ {
		nodes += (table[k[i]] >> 16) & 0x7FF
	}
	if leafSize >= 3 && nodes > 0x7FF {
		panic("rs.leafSize >= 3 && nodes > 0x7FF")
	}
	table[m] |= nodes << 16
}

// golombParamCache lazily extends a golomb-rice table and returns the
// `length` half (top 5 bits) for bucket-size m. Mirrors
// recsplitScratch.golombParam at recsplit.go:360-372.
type golombParamCache struct {
	table              []uint32
	leafSize           uint16
	primaryAggrBound   uint16
	secondaryAggrBound uint16
}

// param returns the bit-length to use as log2(golombParam) when encoding
// a salt for a sub-bucket of size m. Extends the cache table on first
// access for each m.
func (c *golombParamCache) param(m uint16) int {
	for s := uint16(len(c.table)); m >= s; s++ {
		c.table = append(c.table, 0)
		if s == 0 {
			c.table[0] = (bijMemo[0] << 27) | bijMemo[0]
		} else if s <= c.leafSize {
			c.table[s] = (bijMemo[s] << 27) | (uint32(1) << 16) | bijMemo[s]
		} else {
			computeGolombRice(s, c.table, c.leafSize, c.primaryAggrBound, c.secondaryAggrBound)
		}
	}
	return int(c.table[m] >> 27)
}

// computeAggrBounds returns the (primaryAggrBound, secondaryAggrBound)
// pair for a given leafSize. Mirrors recsplit.go:318-324.
func computeAggrBounds(leafSize uint16) (primary, secondary uint16) {
	primary = leafSize * uint16(math.Max(2, math.Ceil(0.35*float64(leafSize)+1./2.)))
	if leafSize < 7 {
		secondary = primary * 2
	} else {
		secondary = primary * uint16(math.Ceil(0.21*float64(leafSize)+9./10.))
	}
	return
}
