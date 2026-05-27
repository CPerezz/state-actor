//go:build cgo_erigon_commitment

// Package commitment is a thin adapter over Erigon's
// `execution/commitment` HexPatriciaHashed trie engine. State-actor
// uses it to compute the genesis stateRoot that Erigon's daemon will
// also compute on first boot — i.e., the same value Erigon reports via
// `eth_getBlockByNumber(0).stateRoot` after `erigon init`.
//
// Why we vendor Erigon instead of porting HexPatriciaHashed:
//
// Three Opus reviewer agents investigated the trade-offs:
//
//  1. API surface — STANDALONE_OK: HexPatriciaHashed is driven entirely
//     via the `PatriciaContext` interface (4 methods: Branch, PutBranch,
//     Account, Storage). Erigon's own MockState test driver proves the
//     trie can be exercised against `map[string][]byte` state — no DB
//     required.
//  2. Write-back coupling — H_CALLBACK: every branch-node persist
//     routes through `ctx.PutBranch(prefix, data, prevData)`. The
//     concrete domain-writer (`commitmentdb/TrieContext.PutBranch →
//     SharedDomains.DomainPut`) lives in a separate sub-package we do
//     NOT vendor; state-actor implements its own `PatriciaContext` and
//     routes branch bytes wherever it chooses.
//  3. Dependency-graph pollution — POLLUTION_RISK_HIGH but mitigable:
//     vendoring transitively pulls ~300-480 modules (libp2p,
//     charm.land, gqlgen, chromedp, etc.). The user accepted this
//     trade-off with the documented mitigation set:
//     a. Build-tag gate (this file's `//go:build cgo_erigon_commitment`)
//        so default `go build ./...` skips the Erigon import path
//        entirely.
//     b. Pinned Erigon tag (not local replace) — TODO; using local
//        replace during initial iteration.
//     c. CI guard for unexpected go.sum growth — TODO.
//     d. Document the bloat in AGENTS.md — TODO.
//     e. Dependabot/Renovate ignore-list for the transitive trash —
//        TODO.
//
// Architect B invariant note: this package is the ONLY place in
// state-actor that imports `github.com/erigontech/erigon`. The build
// tag prevents accidental pollution of untagged builds. To preserve
// the invariant, do NOT import this package from a non-build-tagged
// file.
package commitment
