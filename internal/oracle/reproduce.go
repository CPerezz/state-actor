// Package oracle provides shared helpers for the per-client oracle/boot/e2e
// tests that boot a real Ethereum node against a state-actor-generated
// datadir and assert via JSON-RPC.
//
// The first export is Reproduce: re-derives the exact (EOAs, contracts)
// stream a client adapter wrote during Phase 1, by replaying entitygen's
// canonical RNG draw order. Lets the oracle side know the expected
// balances / code / storage without exposing them through the writer
// API. Lives here (not in internal/entitygen) so it can depend on the
// per-client generator.Config wiring without forcing entitygen itself
// to import generator.
package oracle

import (
	mrand "math/rand"

	"github.com/nerolation/state-actor/internal/entitygen"
)

// ReproduceCfg controls the entity stream Reproduce regenerates. Mirrors
// the writer-side knobs in generator.Config that affect entitygen draws:
// the seed, EOA count, contract count, code size per contract, and the
// slot-count distribution + bounds. Anything else (DB path, batch size,
// workers) doesn't affect the entity stream and isn't replicated here.
type ReproduceCfg struct {
	Seed         int64
	NumAccounts  int
	NumContracts int
	CodeSize     int
	MinSlots     int
	MaxSlots     int
	Distribution entitygen.Distribution
}

// Reproduce returns the (EOAs, contracts) pair a state-actor writer would
// have produced during Phase 1 for the supplied config. The draw order
// — N EOAs first, then M contracts via GenerateContractRoll (which in
// turn calls GenerateSlotCount then GenerateContract) — is the canonical
// "single source of truth" RNG sequence every MPT-mode writer follows.
//
// Identical inputs → identical outputs across calls. Use this from
// oracle / boot / e2e tests to compute expected balances / code /
// storage values to compare against eth_get* RPC results.
//
// Caveat: writers that pass --inject-accounts (geth's collision-retry
// loop in particular) advance the RNG further on a draw collision with
// the inject set. Reproduce assumes no inject-accounts, matching the
// canonical-MPT invariant configuration. If a caller's writer was
// invoked with inject addresses, the reproduced stream will silently
// drift on collisions. Tests using this helper should construct their
// generator.Config with InjectAddresses left nil (today's boot tests
// already do this).
func Reproduce(cfg ReproduceCfg) (eoas, contracts []*entitygen.Account) {
	rng := mrand.New(mrand.NewSource(cfg.Seed))
	eoas = make([]*entitygen.Account, cfg.NumAccounts)
	for i := 0; i < cfg.NumAccounts; i++ {
		eoas[i] = entitygen.GenerateEOA(rng)
	}
	contracts = make([]*entitygen.Account, cfg.NumContracts)
	for i := 0; i < cfg.NumContracts; i++ {
		contracts[i] = entitygen.GenerateContractRoll(
			rng, cfg.Distribution, cfg.CodeSize, cfg.MinSlots, cfg.MaxSlots,
		)
	}
	return eoas, contracts
}
