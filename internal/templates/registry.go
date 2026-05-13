package templates

import (
	"fmt"
	"sort"
	"sync"
)

// registry is the process-level template lookup table. Populated by package
// init() in each template-implementing file (raw.go, eoa.go, erc20.go, ...).
//
// The mutex is defensive — Register is only called from init(), which is
// single-threaded, but exposing Register publicly means we may be called from
// test setup or future plugins.
var (
	registryMu sync.RWMutex
	registry   = make(map[string]Template)
)

// Register adds a template to the registry. Panics on duplicate name — that
// always indicates a programmer bug, never a runtime condition.
func Register(t Template) {
	registryMu.Lock()
	defer registryMu.Unlock()
	name := t.Name()
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("templates.Register: duplicate template name %q", name))
	}
	registry[name] = t
}

// Lookup returns the template registered under name, plus whether it exists.
func Lookup(name string) (Template, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	t, ok := registry[name]
	return t, ok
}

// Names returns every registered template name, sorted. Includes
// internal templates (raw, eoa) — used for diagnostics and tests.
// For the user-facing validator, use UserVisibleNames instead.
func Names() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// UserVisibleNames returns the subset of registered template names users
// may set in the YAML `template:` field. Internal templates (`raw`, `eoa`)
// are excluded — they're dispatched from `kind:` directly, and a user
// writing `template: raw` would bypass intended semantics. Used by
// spec.Validate to power the "unknown template" check.
func UserVisibleNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for n, t := range registry {
		if t.UserVisible() {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
