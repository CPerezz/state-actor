# Erigon Fixture-Capture Submodule

Single Go submodule that imports `github.com/erigontech/erigon` and emits
byte-equality golden fixtures for every `internal/erigon/*` primitive.
Keeping the Erigon dependency in this submodule means state-actor's main
`go.mod` stays Erigon-free.

## Why a separate submodule

state-actor's main module deliberately does NOT import
`github.com/erigontech/erigon` (Architect B's "long-term-maintainability"
choice — see `/Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md`
Checkpoint 2 + Critical Verifier Corrections). The Erigon source tree at
`/Users/random_anon/dev/clients/erigon/` is treated as a SPECIFICATION,
not a runtime dependency.

The byte-equality discipline still needs SOMEONE to run Erigon's writers
on identical inputs and capture the result. This submodule is that someone.
Each subcommand under `cmd/<primitive>/main.go` is a build-tagged
(`//go:build erigon_gen`) Go program that produces one type of fixture
into `internal/erigon/<primitive>/testdata/`.

## Layout

```
internal/erigon/_fixtures/
├── go.mod                      # imports github.com/erigontech/erigon
├── go.sum
├── README.md                   # this file
├── shared/
│   └── manifest.go             # writes MANIFEST.txt with Erigon SHA + cmd args
└── cmd/
    ├── eliasfano/main.go       # generates EliasFano builder fixtures
    ├── seg/main.go             # (Day 9-10) generates seg.Compressor fixtures
    ├── recsplit/main.go        # (Day 11+) generates .kvi fixtures
    ├── btindex/main.go         # generates .bt fixtures
    ├── existence/main.go       # generates .kvei (bloom) fixtures
    ├── account/main.go         # generates EncodeForStorage byte vectors
    └── erigon-init/main.go     # runs `erigon init` in Docker; dumps chaindata buckets
```

## Regenerating fixtures

From the state-actor repo root:

```bash
make regen-erigon-fixtures
```

(The Makefile target chains all subcommands; bumping `PinnedErigonCommit`
in `internal/erigon/constants.go` triggers a re-run.)

For a single primitive during development:

```bash
cd internal/erigon/_fixtures
go run -tags erigon_gen ./cmd/eliasfano \
    --out=../eliasfano/testdata
```

Each subcommand writes a `MANIFEST.txt` next to the fixture files
recording the exact Erigon commit + CLI args used. CI's
`cross-verify-erigon` job re-runs the regen and asserts
`git diff --exit-code internal/erigon/*/testdata/` — drift fails loudly.

## Erigon dependency pin

The submodule pins `github.com/erigontech/erigon v3.4.2` (matching
`internal/erigon/constants.go PinnedErigonRelease`). Bumping the pin
requires:

1. `cd internal/erigon/_fixtures && go get github.com/erigontech/erigon@vX.Y.Z`
2. `go mod tidy`
3. `make regen-erigon-fixtures` (from repo root)
4. Review the resulting diff in `internal/erigon/*/testdata/` — it IS the
   format-drift surface
5. Update `PinnedErigonCommit` in `internal/erigon/constants.go`
6. Re-run `make test-erigon-suite`

## NOTE on the local Erigon reference checkout

There is a clone of Erigon at `/Users/random_anon/dev/clients/erigon/`
used as a HUMAN reference (for reading source lines from the plan).
This submodule's `go.mod` does NOT use a `replace` directive against
that checkout — fixtures must reproduce against the public release tag
so anyone running `make regen-erigon-fixtures` gets the same result.
