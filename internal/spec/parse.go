package spec

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// Parse decodes a Spec from a YAML document. Unknown fields in the YAML are
// rejected so that typos in user schemas surface at parse time rather than
// being silently ignored.
//
// Parse does NOT run schema validation — call (*Spec).Validate after.
func Parse(r io.Reader) (*Spec, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var s Spec
	if err := dec.Decode(&s); err != nil {
		// yaml.v3 errors already carry line/column for most failure modes.
		return nil, fmt.Errorf("parse spec: %w", err)
	}
	return &s, nil
}

// ParseFile opens the file at path and delegates to Parse. The file path is
// reported in the error context so a user with a typo'd path sees what the
// parser was looking at.
func ParseFile(path string) (*Spec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spec file %q: %w", path, err)
	}
	defer f.Close()
	s, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}
