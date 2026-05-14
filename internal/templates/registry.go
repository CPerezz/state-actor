package templates

import (
	"fmt"
	"sort"
	"sync"
)

// Process-level template lookup table populated by package init() in
// each template-implementing file.
var (
	registryMu sync.RWMutex
	registry   = make(map[string]Template)
)

// Register adds a template to the registry. Panics on duplicate name.
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

// Names returns every registered template name, sorted (includes
// internal templates). Use UserVisibleNames for the user-facing set.
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

// UserVisibleNames returns the subset of registered template names
// users may set in the YAML `template:` field (excludes raw, eoa,
// which are dispatched from `kind:` directly).
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
