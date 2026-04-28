// Package nethermind writes a fully-bootable Nethermind RocksDB database
// directly, bypassing Nethermind's chainspec loader.
//
// # Approach
//
// Run() opens the RocksDB instances directly under <datadir>/ that Nethermind
// expects (state, code, blocks, headers, blockNumbers, blockInfos,
// receipts), drives entitygen.Source → internal/neth/trie.Builder to
// produce HalfPath-keyed state-trie nodes, then assembles the genesis
// block tree (header / block / blockNumbers / blockInfos with
// WasProcessed=true / empty receipts at row 0). Booting nethermind
// against the produced datadir starts at block 0 ready — no init phase.
//
// # Build
//
// state-actor's Nethermind path is **Docker-only**. The cgo_neth build
// tag gates all grocksdb-importing files; vanilla `go build` (the local
// default) compiles the stub at run_stub.go which returns a clear error
// directing the user at the Dockerfile.
//
//	docker build -f Dockerfile.nethermind -t state-actor-nethermind .
//	docker run --rm -v $PWD/_artifacts:/data state-actor-nethermind \
//	  --client=nethermind --db=/data/neth --accounts=1000 --seed=42
//
// # Status — PR#3 stage 2 scaffold (this commit)
//
// run_cgo.go opens a smoke-test grocksdb instance to confirm the cgo
// linker resolves librocksdb correctly inside the Docker context, then
// returns a "wiring-only" error. The full 7-DB pipeline (state, code,
// blocks, headers, blockNumbers, blockInfos, receipts) lands in the
// next commit.
//
// # Pinned target
//
// Nethermind upstream/master at SHA `09bd5a2d` (2026-04-26). RLP shapes
// and HalfPath layouts mirror that exact commit; see internal/neth/.
package nethermind
