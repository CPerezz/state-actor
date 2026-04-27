//go:build cgo_neth

// Real Run implementation behind the cgo_neth build tag. Only compiled
// inside the Dockerfile.nethermind build context where librocksdb-dev
// and grocksdb are available.
//
// This file is the integration point between:
//
//   - internal/entitygen (RNG-driven account/contract generation)
//   - internal/neth/trie.Builder (StackTrie wrapper with NodeStorage callback)
//   - internal/neth/storage (HalfPath / Hash-only key encoding)
//   - internal/neth/rlp (header/block/blockInfo/chainLevel encoders)
//   - github.com/linxGnu/grocksdb (cgo bindings to librocksdb)
//
// All the Nethermind-shape encoding work lives in internal/neth/; this file
// is plumbing only — it sequences the calls and routes their output into
// the 7 RocksDB instances Nethermind reads on boot.

package nethermind

import (
	"context"
	"errors"
	"fmt"

	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/generator"
)

// runImpl orchestrates a full Nethermind RocksDB write.
//
// Stage 2 scaffolding (this commit): proves the cgo linker resolves
// librocksdb, opens a single test RocksDB to confirm grocksdb works
// end-to-end inside the Docker context, then returns a "wiring-only"
// error so callers know we haven't yet emitted real Nethermind state.
//
// Stage 2 final (next commit on this branch): replace the body with the
// full pipeline — open 7 DBs, drive entitygen.Source → trie.Builder →
// grocksdb writes, assemble genesis block tree with WasProcessed=true,
// close and return generator.Stats with the computed state root.
func runImpl(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	_ = ctx
	_ = opts

	if cfg.TrieMode == generator.TrieModeBinary {
		return nil, errors.New("nethermind doesn't support binary trie (EIP-7864)")
	}
	if cfg.DBPath == "" {
		return nil, errors.New("--db is required for --client=nethermind")
	}

	// Cgo smoke: open a test RocksDB to confirm grocksdb + librocksdb are
	// linked correctly. If the Dockerfile is misconfigured (missing
	// librocksdb-dev, wrong header path, ABI mismatch), this is where we
	// surface it loudly. Once the next commit replaces this with the full
	// 7-DB pipeline, this line goes away.
	opts2 := grocksdb.NewDefaultOptions()
	defer opts2.Destroy()
	opts2.SetCreateIfMissing(true)

	smokePath := cfg.DBPath + "/.cgo-smoke"
	db, err := grocksdb.OpenDb(opts2, smokePath)
	if err != nil {
		return nil, fmt.Errorf("grocksdb smoke open: %w", err)
	}
	db.Close()

	return nil, errors.New(
		"client/nethermind: cgo linker resolved librocksdb successfully " +
			"(stage 2 scaffolding complete) — full 7-DB pipeline ships in the next commit",
	)
}
