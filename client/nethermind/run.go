package nethermind

import (
	"context"
	"errors"

	"github.com/nerolation/state-actor/generator"
)

// errNotImplemented is the placeholder Run returns until PR#3 stage 2
// (cgo + grocksdb wiring) lands. The text directs the user at the
// integration plan so they can see what's still ahead.
var errNotImplemented = errors.New(
	"client/nethermind: --client=nethermind is not yet implemented; " +
		"the writer scaffolding landed in PR#3 stage 1, the cgo+grocksdb " +
		"wiring ships in stage 2 — see ~/.claude/plans/do-you-recall-the-gleaming-fox.md " +
		"PR#3 / B5 for status",
)

// Run is the public entry point dispatched from main.go's
// `case "nethermind"` arm.
//
// Stage 1: returns errNotImplemented.
//
// Stage 2 (future): opens grocksdb instances under cfg.DBPath/db/, drives
// entitygen.Source → internal/neth/trie.Builder → grocksdb writes to fill
// state and code, then constructs the genesis block tree
// (headers / blocks / blockNumbers / blockInfos with WasProcessed=true /
// empty receipts at row 0). Returns generator.Stats populated with the
// computed state root for parity with the geth path.
func Run(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	_ = ctx
	_ = cfg
	_ = opts
	return nil, errNotImplemented
}
