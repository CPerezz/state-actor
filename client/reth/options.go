package reth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
)

// Options carries optional knobs for RunCgo. Reserved for future use; the
// zero value is the supported default.
type Options struct {
	// (No options exposed yet. Future candidates: bytecode-LRU capacity,
	// commit-threshold override, scratch directory, etc.)
}

// GenesisFilePath and ChainIDOverride are set by main.go when --client=reth
// is selected, threading the --genesis and --chain-id flag values into
// RunCgo without expanding generator.Config.
var (
	GenesisFilePath string
	ChainIDOverride int64
)

// genesisPathFromCfg and chainIDFromCfg are package-internal trampolines.
// They ignore cfg today (the values come from the package globals above);
// the cfg parameter is reserved so a future Config field can replace the
// globals without changing call sites.
func genesisPathFromCfg(_ generator.Config) string { return GenesisFilePath }
func chainIDFromCfg(_ generator.Config) int64      { return ChainIDOverride }

// buildInjectedAccount returns an entitygen.Account for a pre-funded EOA at
// the supplied address. Used by RunCgo to honour cfg.InjectAddresses (e.g.
// the Anvil dev account 0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266).
//
// Balance: 999_999_999 ETH (same as the legacy JSONL path so spamoor and
// other test harnesses keep working). Nonce 0, no code, no storage.
func buildInjectedAccount(addr common.Address) *entitygen.Account {
	balance := new(uint256.Int).Mul(
		uint256.NewInt(999_999_999),
		uint256.NewInt(1e18),
	)
	return &entitygen.Account{
		Address:  addr,
		AddrHash: crypto.Keccak256Hash(addr[:]),
		StateAccount: &types.StateAccount{
			Nonce:   0,
			Balance: balance,
		},
	}
}
