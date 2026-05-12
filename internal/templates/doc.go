// Package templates hosts the registry of named state-actor spec templates
// (`erc20`, etc.) and exports the PreAllocEntity record type that every
// writer consumes.
//
// The package is structured around a single Template interface and a
// process-level registry populated via init() functions. Adding a new
// template is one new file in this package: implement Template, call
// Register(yourTemplate) in init().
//
// What lives here:
//
//   - template.go — the Template interface, Context, and PreAllocEntity.
//   - registry.go — Register / Lookup / All.
//   - raw.go      — kind: contract, code: ... (no template field).
//   - eoa.go      — kind: eoa (with optional 7702 code, storage bloat).
//   - erc20.go    — kind: contract, template: erc20.
//   - sizing.go   — shared streaming storage-slot synthesizer.
//
// What does NOT live here:
//   - The YAML schema and parser (internal/spec/).
//   - The Spec→entities translator (internal/specbuild/).
//   - Per-client byte-budget calibration (internal/sizecal/).
//
// Determinism contract: every Template.Expand call must be byte-identical
// across runs for the same input. Address derivation, slot synthesis, and
// storage values are all pure functions of the spec entity + the seed.
package templates
