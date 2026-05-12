// Package spec defines the YAML schema state-actor's `--spec` flag accepts
// and a parser+validator for it.
//
// The schema describes concrete entities (EOAs + contracts) the user wants
// in the generated genesis state. Spec entities are written first by the
// client writers; the existing synthetic-fill loop then runs on top until
// --target-size is reached.
//
// This package is pure data: it owns the schema types, the YAML decode, and
// schema-time validation. It does NOT know about templates (resolved later
// in internal/templates/) or about per-client byte budgets (resolved later
// in internal/sizecal/). Template name lookups are done by the caller and
// passed to Validate via the knownTemplates argument so this package has no
// import cycle with internal/templates/.
//
// See docs/SPEC.md for the user-facing schema reference.
package spec
