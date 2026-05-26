// Command gen-bloatnet-spec emits a state-spec YAML exercising every
// spec parameter combination, sized to ~100 GB of state on disk
// (against sizecal.NewFixed(64)).
//
// Usage:
//
//	go run ./scripts/gen-bloatnet-spec -out examples/spec-bloatnet-100gb.yaml -seed 4242
//
// Deterministic: same -seed produces a byte-identical YAML.
package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
	mrand "math/rand"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// defaultBulkEOACount + defaultBulkContractCount: the baseline
	// (target-gb=100) entity counts. Scaled by targetScale = target-gb/100.
	defaultBulkEOACount      = 15_000
	defaultBulkContractCount = 200_000

	// Per-bulk-raw-contract storage size. Only HALF the bulk contracts
	// are raw (the other half are erc20 with negligible storage), so
	// raw-mean × bulkContractCount/2 = total raw bytes. To hit ~49 GB
	// from raws at target-gb=100 we set raw-mean = 490 KB → distribution
	// uniform on [0, 2× this].
	bulkContractAvgBytes = 490 * 1024

	// ERC-20 showcase #1: 20k explicit holders + 20k explicit allowances
	// are infeasible to hand-author (1M lines of YAML); we plant a small
	// explicit subset and rely on total_owners / total_allowances for
	// the bulk. Adjust if the user wants ALL explicit.
	bigERC20ExplicitOwners     = 5
	bigERC20ExplicitAllowances = 3
	bigERC20TotalOwners        = 20_000
	bigERC20TotalAllowances    = 20_000

	spamoorSenderAddr = "0x7e5f4552091a69125d5dfcb7b8c2659029395bdf"
	spamoorSenderBal  = "999999999000000000000000000" // 999_999_999 ETH

	// 1 ETH = 1e18 wei. Bulk EOA balances drawn from [1, 1000] ETH.
	weiPerEth = "1000000000000000000"
)

// scaledBulkCounts returns the (EOAs, contracts) counts scaled to the
// supplied target-gb. Min counts (250 EOAs, 1000 contracts) keep the
// bulk-verify samples non-empty even at very small targets.
func scaledBulkCounts(targetGB int) (int, int) {
	scale := float64(targetGB) / 100.0
	eoas := int(float64(defaultBulkEOACount) * scale)
	contracts := int(float64(defaultBulkContractCount) * scale)
	if eoas < 250 {
		eoas = 250
	}
	if contracts < 1000 {
		contracts = 1000
	}
	return eoas, contracts
}

// bloatedSpecForTarget picks the bloated-EOA set based on target-gb.
// For target=100 it returns the original 5-entity list (51 GB). For
// smaller targets it drops the largest entities. The verify script's
// expected addresses (bloat-100mb at 0x...0b0100, bloat-15gb at
// 0x...0b0f00) are preserved as long as target >= 15 GB.
func bloatedSpecForTarget(targetGB int) []bloatedSpec {
	const G = uint64(1024 * 1024 * 1024)
	all := []bloatedSpec{
		{name: "bloat-100mb", sizeBytes: 100 * 1024 * 1024, mode: "explicit",
			addr: "0x00000000000000000000000000000000000b0100", balance: "100" + weiPerEth, code: ""},
		{name: "bloat-1gb-deleg", sizeBytes: 1 * G, mode: "name",
			balance: "200" + weiPerEth, code: "0xef0100" + strings.Repeat("11", 20)},
		{name: "", sizeBytes: 5 * G, mode: "position",
			nonce: 42, balance: "5" + weiPerEth},
		{name: "bloat-15gb-explicit", sizeBytes: 15 * G, mode: "explicit",
			addr: "0x00000000000000000000000000000000000b0f00", balance: "15" + weiPerEth,
			code: "0xef0100" + strings.Repeat("22", 20), nonce: 7},
		{name: "bloat-30gb-named", sizeBytes: 30 * G, mode: "name",
			balance: "30" + weiPerEth, nonce: 1},
	}
	// Truncate so the cumulative bloated total stays under
	// ~30% of targetGB (leaves room for bulk + autofill).
	budget := uint64(float64(targetGB) * 1024 * 1024 * 1024 * 0.30)
	out := []bloatedSpec{}
	var acc uint64
	for _, b := range all {
		if acc+b.sizeBytes > budget {
			break
		}
		out = append(out, b)
		acc += b.sizeBytes
	}
	// Always keep bloat-100mb + bloat-15gb-explicit (verify expects these
	// at explicit addresses 0x...0b0100 / 0x...0b0f00). At target=25 only
	// the first 2-3 fit naturally; ensure 15gb survives.
	have100mb, have15gb := false, false
	for _, b := range out {
		if b.name == "bloat-100mb" {
			have100mb = true
		}
		if b.name == "bloat-15gb-explicit" {
			have15gb = true
		}
	}
	if !have100mb {
		out = append([]bloatedSpec{all[0]}, out...)
	}
	if !have15gb && targetGB >= 15 {
		// Substitute the smallest one we already kept with bloat-15gb so
		// the verify script's BLOAT_15GB address is present. Insert at
		// the end (order doesn't matter; spec processes by entity).
		out = append(out, all[3])
	}
	return out
}

func main() {
	out := flag.String("out", "spec.yaml", "output YAML path")
	seed := flag.Int64("seed", 4242, "RNG seed")
	targetGB := flag.Int("target-gb", 100, "approximate total spec size in GB (scales bulk + bloated entities). 100 = original 100 GB layout; 25 reduces bulk + drops the largest bloated entities while keeping the verify-tested addresses (bloat-100mb, bloat-15gb).")
	flag.Parse()
	bulkEOACount, bulkContractCount := scaledBulkCounts(*targetGB)
	bloated := bloatedSpecForTarget(*targetGB)

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("create %s: %v", *out, err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	rng := mrand.New(mrand.NewSource(*seed))
	codePool := genCodePool(rng, 20)

	fmt.Fprintf(w, "# state-spec for the %d GB bloatnet benchmark (seed=%d).\n", *targetGB, *seed)
	fmt.Fprintf(w, "# Generated by scripts/gen-bloatnet-spec --target-gb=%d — do not edit by hand.\n", *targetGB)
	fmt.Fprintf(w, "#\n")
	fmt.Fprintf(w, "# Spamoor sender at %s pre-funded with %s wei.\n", spamoorSenderAddr, spamoorSenderBal)
	fmt.Fprintf(w, "# Bulk: %d EOAs (half delegators) + %d contracts (half raw, half erc20).\n", bulkEOACount, bulkContractCount)
	fmt.Fprintf(w, "entities:\n")

	totalAccounts := 0
	totalSpecBytes := uint64(0)

	// 1. Spamoor sender
	writeSpamoorSender(w)
	totalAccounts++

	// 2. Bloated EOAs — preselected via bloatedSpecForTarget so the
	// largest entries get dropped at small --target-gb while the
	// verify-tested addresses (bloat-100mb, bloat-15gb-explicit) stay.
	for _, b := range bloated {
		writeBloatedEOA(w, b)
		totalAccounts++
		totalSpecBytes += b.sizeBytes
	}

	// 3. ERC-20 showcase
	totalSpecBytes += writeShowcaseERC20s(w, rng, &totalAccounts)

	// 4. Raw contracts (2 with custom code)
	totalSpecBytes += writeRawContracts(w, &totalAccounts)

	// 5. Demo EOA matrix (12 entities exercising every permutation)
	totalSpecBytes += writeDemoEOAMatrix(w, rng, &totalAccounts)

	// 6. Bulk EOAs (15k: half plain, half 7702-delegators)
	for i := range bulkEOACount {
		bal := bigInt(rng.Int63n(999)+1, weiPerEth)
		nonce := uint64(rng.Intn(100))
		if i < bulkEOACount/2 {
			// Plain EOA — name-derived
			fmt.Fprintf(w, "  - kind: eoa\n    name: eoa-plain-%d\n    balance: \"%s\"\n", i, bal)
			if nonce > 0 {
				fmt.Fprintf(w, "    nonce: %d\n", nonce)
			}
		} else {
			// 7702 delegator — name-derived, random target address
			target := deriveRandomAddr(rng, "deleg-target", i)
			fmt.Fprintf(w, "  - kind: eoa\n    name: eoa-deleg-%d\n    balance: \"%s\"\n    nonce: %d\n    code: \"0xef0100%s\"\n",
				i, bal, nonce, target)
		}
		totalAccounts++
	}

	// 7. Bulk contracts (200k: half raw with random code/size, half erc20 with random params)
	totalContractBytes := uint64(0)
	for i := range bulkContractCount {
		if i%2 == 0 {
			// Raw — random code from pool + random storage size
			codeIdx := rng.Intn(len(codePool))
			sizeBytes := uint64(rng.Intn(2 * bulkContractAvgBytes))
			fmt.Fprintf(w, "  - kind: contract\n    name: ct-raw-%d\n    code: \"%s\"\n    approximate_size_bytes: %d\n",
				i, codePool[codeIdx], sizeBytes)
			totalContractBytes += sizeBytes
		} else {
			// ERC-20 — random total_owners
			ownerCount := rng.Intn(50)
			fmt.Fprintf(w, "  - kind: contract\n    template: erc20\n    name: ct-erc20-%d\n    parameters:\n", i)
			fmt.Fprintf(w, "      symbol: T%d\n", i)
			fmt.Fprintf(w, "      name: BulkToken%d\n", i)
			fmt.Fprintf(w, "      decimals: 18\n")
			if ownerCount > 0 {
				fmt.Fprintf(w, "      total_owners: %d\n", ownerCount)
			}
		}
		totalAccounts++
	}
	totalSpecBytes += totalContractBytes

	// Footer comment in the YAML file (last entry already emitted).
	fmt.Fprintf(w, "# end-of-spec: %d entities, ~%s of state (calibration: 64 B/slot)\n",
		totalAccounts, humanBytes(totalSpecBytes))

	if err := w.Flush(); err != nil {
		log.Fatalf("flush: %v", err)
	}

	// Stats to stderr so callers can redirect just the YAML to a file.
	fmt.Fprintf(os.Stderr, "\nGenerated %s\n", *out)
	fmt.Fprintf(os.Stderr, "  Entities:       %d\n", totalAccounts)
	fmt.Fprintf(os.Stderr, "  Spec bytes (approx, 64 B/slot calibration):\n")
	fmt.Fprintf(os.Stderr, "    Bloated EOAs:    51.10 GB\n")
	fmt.Fprintf(os.Stderr, "    Bulk contracts:  %s\n", humanBytes(totalContractBytes))
	fmt.Fprintf(os.Stderr, "    Everything else: <1 GB\n")
	fmt.Fprintf(os.Stderr, "    TOTAL:           ~%s\n", humanBytes(totalSpecBytes))
}

type bloatedSpec struct {
	name      string // empty → position-derived
	addr      string // empty → name or position
	mode      string // "explicit", "name", "position" (for clarity)
	sizeBytes uint64
	balance   string
	code      string
	nonce     uint64
}

func writeSpamoorSender(w *bufio.Writer) {
	fmt.Fprintf(w, "  - kind: eoa\n")
	fmt.Fprintf(w, "    name: spamoor-sender\n")
	fmt.Fprintf(w, "    address: %s\n", spamoorSenderAddr)
	fmt.Fprintf(w, "    balance: \"%s\"\n", spamoorSenderBal)
	fmt.Fprintf(w, "    nonce: 0\n")
}

func writeBloatedEOA(w *bufio.Writer, b bloatedSpec) {
	fmt.Fprintf(w, "  - kind: eoa\n")
	if b.name != "" {
		fmt.Fprintf(w, "    name: %s\n", b.name)
	}
	if b.addr != "" {
		fmt.Fprintf(w, "    address: %s\n", b.addr)
	}
	if b.balance != "" {
		fmt.Fprintf(w, "    balance: \"%s\"\n", b.balance)
	}
	if b.nonce > 0 {
		fmt.Fprintf(w, "    nonce: %d\n", b.nonce)
	}
	if b.code != "" {
		fmt.Fprintf(w, "    code: \"%s\"\n", b.code)
	}
	fmt.Fprintf(w, "    approximate_size_bytes: %d\n", b.sizeBytes)
}

func writeShowcaseERC20s(w *bufio.Writer, rng *mrand.Rand, count *int) uint64 {
	// #1 big-showcase: explicit address, nonce=7, explicit owners + allowances + bulk
	fmt.Fprintf(w, "  - kind: contract\n    template: erc20\n    name: big-showcase\n")
	fmt.Fprintf(w, "    address: 0x0000000000000000000000000000000000c0ffee\n")
	fmt.Fprintf(w, "    nonce: 7\n    parameters:\n")
	fmt.Fprintf(w, "      symbol: BIG\n      name: BigShowcase\n      decimals: 18\n")
	fmt.Fprintf(w, "      owners:\n")
	for i := range bigERC20ExplicitOwners {
		addr := deriveRandomAddr(rng, "big-owner", i)
		bal := bigInt(int64(100+i*10), weiPerEth)
		fmt.Fprintf(w, "        - { address: \"0x%s\", balance: \"%s\" }\n", addr, bal)
	}
	fmt.Fprintf(w, "      allowances:\n")
	for i := range bigERC20ExplicitAllowances {
		owner := deriveRandomAddr(rng, "big-alw-owner", i)
		spender := deriveRandomAddr(rng, "big-alw-spender", i)
		amt := fmt.Sprintf("%d", 1000*(i+1))
		fmt.Fprintf(w, "        - { owner: \"0x%s\", spender: \"0x%s\", allowance: \"%s\" }\n", owner, spender, amt)
	}
	fmt.Fprintf(w, "      total_owners: %d\n", bigERC20TotalOwners)
	fmt.Fprintf(w, "      total_allowances: %d\n", bigERC20TotalAllowances)
	*count++

	// #2 USDC-style: name-derived
	fmt.Fprintf(w, "  - kind: contract\n    template: erc20\n    name: USDC\n    parameters:\n")
	fmt.Fprintf(w, "      symbol: USDC\n      name: USD Coin\n      decimals: 18\n      total_owners: 100\n")
	fmt.Fprintf(w, "      allowances:\n")
	fmt.Fprintf(w, "        - { owner: \"0x%s\", spender: \"0x%s\", allowance: \"1000000\" }\n",
		deriveRandomAddr(rng, "usdc-alw-owner", 0),
		deriveRandomAddr(rng, "usdc-alw-spender", 0))
	*count++

	// #3 position-derived (no name, no address)
	fmt.Fprintf(w, "  - kind: contract\n    template: erc20\n    parameters:\n")
	fmt.Fprintf(w, "      symbol: POS\n      name: PositionDerived\n      decimals: 18\n      total_owners: 50\n")
	*count++

	// #4–#7 parameter-combo coverage
	for i, p := range []struct {
		name, kind, body string
	}{
		{"combo-owners-only", "explicit",
			"      symbol: O1\n      name: OwnersOnly\n      decimals: 18\n      owners:\n" +
				"        - { address: \"0x" + deriveRandomAddr(rng, "owners-only", 0) + "\", balance: \"42\" }\n"},
		{"combo-allowances-only", "explicit",
			"      symbol: A1\n      name: AllowancesOnly\n      decimals: 18\n      allowances:\n" +
				"        - { owner: \"0x" + deriveRandomAddr(rng, "alw-only-owner", 0) +
				"\", spender: \"0x" + deriveRandomAddr(rng, "alw-only-spender", 0) + "\", allowance: \"100\" }\n"},
		{"combo-total-owners-only", "explicit",
			"      symbol: TO\n      name: TotalOwnersOnly\n      decimals: 18\n      total_owners: 200\n"},
		{"combo-total-allowances-only", "explicit",
			"      symbol: TA\n      name: TotalAllowancesOnly\n      decimals: 18\n      total_allowances: 200\n"},
	} {
		fmt.Fprintf(w, "  - kind: contract\n    template: erc20\n    name: %s\n    parameters:\n%s", p.name, p.body)
		*count++
		_ = i
	}

	// Rough storage: big ERC-20 ~2.5 MB; others <100 KB total.
	return 3 * 1024 * 1024 // approximation
}

func writeRawContracts(w *bufio.Writer, count *int) uint64 {
	// Raw #1: explicit addr + custom code + 10 MB storage
	fmt.Fprintf(w, "  - kind: contract\n    name: raw-fat\n    address: 0x000000000000000000000000000000000000ba51\n")
	fmt.Fprintf(w, "    code: \"0x6080604052348015600f57600080fd5b50603f80601d6000396000f3fe6080604052600080fdfea264\"\n")
	fmt.Fprintf(w, "    approximate_size_bytes: %d\n", 10*1024*1024)
	*count++
	// Raw #2: name-derived + custom code, no storage
	fmt.Fprintf(w, "  - kind: contract\n    name: raw-minimal\n")
	fmt.Fprintf(w, "    code: \"0x60606040526000357c0100000000000000000000000000000000000000000000000000000000900463\"\n")
	*count++
	return 10 * 1024 * 1024
}

func writeDemoEOAMatrix(w *bufio.Writer, rng *mrand.Rand, count *int) uint64 {
	// 12 permutations: 3 modes × 4 attribute combos
	type spec struct {
		mode    string // "explicit", "name", "position"
		balance string // ETH literal or ""
		nonce   uint64
		code    string // empty / 7702 marker / arbitrary
		size    uint64 // approximate_size_bytes; 0 = none
	}
	mat := []spec{
		// explicit address mode × 4 attribute combos
		{mode: "explicit", balance: "1"},
		{mode: "explicit", balance: "2", nonce: 99},
		{mode: "explicit", balance: "3", code: "0xef0100" + strings.Repeat("33", 20)},
		{mode: "explicit", balance: "4", code: "0xef0100" + strings.Repeat("44", 20), size: 10 * 1024, nonce: 5},
		// name-derived
		{mode: "name", balance: "5"},
		{mode: "name", balance: "6", nonce: 99},
		{mode: "name", balance: "7", code: "0xef0100" + strings.Repeat("55", 20)},
		{mode: "name", balance: "8", code: "0xef0100" + strings.Repeat("66", 20), size: 10 * 1024, nonce: 5},
		// position-derived
		{mode: "position", balance: "9"},
		{mode: "position", balance: "10", nonce: 99},
		{mode: "position", balance: "11", code: "0xef0100" + strings.Repeat("77", 20)},
		{mode: "position", balance: "12", code: "0xef0100" + strings.Repeat("88", 20), size: 10 * 1024, nonce: 5},
	}
	totalSize := uint64(0)
	for i, s := range mat {
		fmt.Fprintf(w, "  - kind: eoa\n")
		switch s.mode {
		case "explicit":
			fmt.Fprintf(w, "    address: 0x00000000000000000000000000000000d%07d\n", i)
		case "name":
			fmt.Fprintf(w, "    name: demo-mat-%d\n", i)
		case "position":
			// no name, no address
		}
		fmt.Fprintf(w, "    balance: \"%s\"\n", bigInt(int64(i+1), weiPerEth))
		if s.nonce > 0 {
			fmt.Fprintf(w, "    nonce: %d\n", s.nonce)
		}
		if s.code != "" {
			fmt.Fprintf(w, "    code: \"%s\"\n", s.code)
		}
		if s.size > 0 {
			fmt.Fprintf(w, "    approximate_size_bytes: %d\n", s.size)
			totalSize += s.size
		}
		*count++
		_ = rng
	}
	return totalSize
}

func genCodePool(rng *mrand.Rand, n int) []string {
	out := make([]string, n)
	for i := range n {
		// 50–500 byte random bytecode. Not valid EVM; doesn't matter
		// for state generation (the EVM only runs code on call).
		size := 50 + rng.Intn(450)
		buf := make([]byte, size)
		rng.Read(buf)
		out[i] = "0x" + hex.EncodeToString(buf)
	}
	return out
}

// deriveRandomAddr returns a 20-byte hex address derived from
// (label, index, rng-derived nonce). Pure function of the rng seed.
func deriveRandomAddr(rng *mrand.Rand, label string, idx int) string {
	salt := rng.Uint64()
	var buf [8 + 8 + 8]byte
	binary.BigEndian.PutUint64(buf[:8], salt)
	binary.BigEndian.PutUint64(buf[8:16], uint64(idx))
	copy(buf[16:], []byte(label))
	h := crypto.Keccak256(buf[:])
	return hex.EncodeToString(h[12:])
}

func bigInt(eth int64, weiPerEth string) string {
	w := new(big.Int)
	w.SetString(weiPerEth, 10)
	w.Mul(w, big.NewInt(eth))
	return w.String()
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
