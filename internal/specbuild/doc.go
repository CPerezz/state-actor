// Package specbuild translates a parsed *spec.Spec into the flat
// []templates.PreAllocEntity slice every client writer consumes.
//
// Responsibilities:
//   - Resolve each entity's address via one of three deterministic modes
//     (explicit, name-derived, position-derived). See derive.go.
//   - Dispatch each entity through the templates registry — `kind: eoa`
//     to the eoa template, `kind: contract, template: X` to template X,
//     `kind: contract, code: ...` to the raw template.
//   - Post-expansion validation — detect address collisions that involve
//     synthesized (derived) addresses, which the spec-time validator
//     cannot catch.
//
// This package is the seam between the *static* schema (spec/) and the
// *runtime-fanned* entities (templates/). It is the only place that
// imports both.
package specbuild
