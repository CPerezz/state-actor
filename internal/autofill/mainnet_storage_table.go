package autofill

// Mainnet storage calibration for --storage-model=mainnet, from a full walk of jochemnet's HashedStorages
// main tree (reth storage v2, block 24,410,463, 2026-10-09): 25,166,497 storage-bearing accounts, 1.49B
// slots total, after dropping the 335,545,082-slot EEST 10GB contract, which specs plant themselves. The
// 4 jochemnet accounts above mainnetMaxSlots are folded into the last knot.
//
// To regenerate: rebuild mainnetSmallSlotCDF, mainnetSlotTail and mainnetValueLenCDF from a fresh walk's
// empirical CDFs, keeping the last mainnetSlotTail knot at {1.0, mainnetMaxSlots}.

// mainnetSmallSlotCDF[k-1] = P(slots <= k), k = 1..28. Small counts are drawn exactly: they decide
// whether a contract's storage stays inline in the HashedStorages leaf or gets its own nested tree.
var mainnetSmallSlotCDF = [28]float64{
	0.549714368, 0.702195741, 0.732495508, 0.823180834, 0.845247672, 0.858925460, 0.873203927,
	0.883504566, 0.894329592, 0.907622781, 0.923790784, 0.934543135, 0.940194935, 0.945743939,
	0.948659283, 0.952209876, 0.955232069, 0.958837497, 0.961102493, 0.962333335, 0.963515026,
	0.964900638, 0.966081612, 0.967305025, 0.968445152, 0.969369356, 0.970296780, 0.971167501,
}

// mainnetMaxSlots caps one contract's storage draw, so the largest draw stays near 1 GB in memory. It is
// also the slot count of the last mainnetSlotTail knot below.
const mainnetMaxSlots = 16777216

// mainnetSlotTail are (P(slots <= n), n) knots above 28; draws are uniform between adjacent knots.
var mainnetSlotTail = []struct {
	p     float64
	slots uint64
}{
	{0.971167501, 28},
	{0.973782128, 32},
	{0.976067547, 37},
	{0.978122502, 43},
	{0.979784076, 49},
	{0.981341106, 56},
	{0.982897501, 65},
	{0.984174436, 74},
	{0.985526472, 86},
	{0.986715672, 99},
	{0.987797865, 113},
	{0.988859872, 130},
	{0.989855402, 150},
	{0.990752666, 172},
	{0.991554566, 198},
	{0.992310293, 228},
	{0.992973078, 262},
	{0.993557188, 301},
	{0.994115272, 347},
	{0.994612560, 398},
	{0.995072377, 458},
	{0.995500288, 527},
	{0.995888144, 606},
	{0.996246796, 697},
	{0.996574573, 802},
	{0.996876125, 922},
	{0.997162338, 1060},
	{0.997416089, 1219},
	{0.997645362, 1402},
	{0.997857548, 1612},
	{0.998048676, 1854},
	{0.998230584, 2132},
	{0.998392188, 2452},
	{0.998544493, 2820},
	{0.998686468, 3243},
	{0.998816641, 3729},
	{0.998936443, 4288},
	{0.999040232, 4931},
	{0.999140405, 5671},
	{0.999228776, 6522},
	{0.999311783, 7500},
	{0.999389387, 8625},
	{0.999458169, 9919},
	{0.999524924, 11407},
	{0.999581984, 13118},
	{0.999631971, 15086},
	{0.999677190, 17348},
	{0.999720700, 19951},
	{0.999756740, 22943},
	{0.999788091, 26385},
	{0.999815787, 30342},
	{0.999840661, 34894},
	{0.999860569, 40128},
	{0.999878688, 46147},
	{0.999898198, 53069},
	{0.999917430, 61029},
	{0.999932092, 70184},
	{0.999941947, 80711},
	{0.999950212, 92818},
	{0.999957483, 106741},
	{0.999963006, 122752},
	{0.999969126, 141165},
	{0.999973338, 162339},
	{0.999977867, 186690},
	{0.999980450, 214694},
	{0.999982795, 246898},
	{0.999985020, 283933},
	{0.999986848, 326523},
	{0.999988119, 375501},
	{0.999989430, 431826},
	{0.999991099, 496600},
	{0.999992490, 571090},
	{0.999993245, 656754},
	{0.999994199, 755267},
	{0.999994795, 868557},
	{0.999995430, 998840},
	{0.999995987, 1148666},
	{0.999996702, 1320966},
	{0.999996980, 1519111},
	{0.999997219, 1746978},
	{0.999997576, 2009025},
	{0.999998053, 2310378},
	{0.999998331, 2656935},
	{0.999998649, 3055475},
	{0.999998887, 3513797},
	{0.999998967, 4040866},
	{0.999999046, 4646996},
	{0.999999245, 5344045},
	{0.999999285, 6145652},
	{0.999999364, 7067500},
	{0.999999444, 8127625},
	{0.999999603, 9346769},
	{0.999999642, 10748784},
	{0.999999682, 12361102},
	{0.999999762, 14215267},
	{0.999999841, 16347557},
	{1.000000000, mainnetMaxSlots},
}

// mainnetMeanSlotsMilli is the knot-model mean of the slot-count distribution above, x1000, kept as an
// integer so the contract count derived from it is identical on every platform (cross-client root
// invariance).
const mainnetMeanSlotsMilli = 53821

// mainnetValueLenCDF[L-1] = P(len <= L): significant bytes of a slot value (sampled from 1% of
// jochemnet's HashedStorages leaves; 20 is an address, 32 a hash, 1 a flag or small counter).
var mainnetValueLenCDF = [32]float64{
	0.182985652, 0.203344962, 0.211982044, 0.236166935, 0.241669774, 0.245105233, 0.256044564, 0.276049580,
	0.287409552, 0.296504430, 0.307135910, 0.317005723, 0.321924707, 0.324479075, 0.327480623, 0.330096031,
	0.334382062, 0.340144982, 0.348605580, 0.846683501, 0.881748011, 0.891658959, 0.892910268, 0.895337250,
	0.897924791, 0.898492724, 0.898760766, 0.899586126, 0.900321253, 0.900601239, 0.901843259, 1.000000000,
}
