package geth

import (
	"fmt"

	"github.com/nerolation/state-actor/generator"
)

// init registers NewWriterFactory as state-actor's default writer factory.
// Importing this package — even with a blank import:
//
//	import _ "github.com/nerolation/state-actor/client/geth"
//
// is enough to make generator.New(cfg) work without further wiring. This is
// how state-actor's main.go and e2e tests opt into the geth writer.
func init() {
	generator.RegisterDefaultWriterFactory(NewWriterFactory())
}

// NewWriterFactory returns a generator.WriterFactory that constructs a geth
// Writer for the given config. Useful for callers that want to pick the
// factory explicitly (e.g. via generator.NewWithWriter) rather than relying
// on the default registered by init().
func NewWriterFactory() generator.WriterFactory {
	return func(cfg generator.Config) (generator.Writer, error) {
		w, err := NewWriter(cfg.DBPath, cfg.BatchSize, cfg.Workers)
		if err != nil {
			return nil, fmt.Errorf("create geth writer: %w", err)
		}
		return w, nil
	}
}
