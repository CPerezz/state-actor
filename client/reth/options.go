package reth

import "github.com/nerolation/state-actor/generator"

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
