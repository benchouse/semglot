// Package dialect defines the dialect plugin surface (Parser/Emitter) and a
// registry mapping dialect names to implementations.
package dialect

import (
	"fmt"
	"sort"

	"github.com/benchouse/semglot/ir"
)

// Dialect is any registered semantic-layer dialect.
type Dialect interface {
	Name() string
}

// Parser reads a dialect's files from dir into the neutral IR.
type Parser interface {
	Dialect
	// Parse reads *.yml from each source directory (non-recursive) and merges
	// them into one IR model. Multiple sources let a dbt project's schema files
	// spread across folders (e.g. models/semantic + models/marts) be combined.
	Parse(sources ...string) (*ir.Model, error)
}

// Emitter writes the neutral IR out as a dialect's files under dir.
type Emitter interface {
	Dialect
	Emit(m *ir.Model, dir string) error
}

// Options carries the model/view identity a Configurable emitter needs.
type Options struct {
	Database    string // warehouse database (e.g. EVAL_MARTS)
	Schema      string // schema of the SOURCE tables the model reads (e.g. MAIN)
	ViewSchema  string // schema where the emitted view OBJECT is created (Snowflake semantic view); falls back to Schema when empty
	Name        string
	Description string
	// Timestamp is an ISO 8601 instant stamped onto emitted documents by targets
	// that require one (okf). It is supplied by the caller rather than read from
	// a clock, so the same input always produces the same output.
	Timestamp string
}

// Configurable is an Emitter that accepts model/view identity options.
type Configurable interface {
	WithOptions(Options) Emitter
}

var registry = map[string]Dialect{}

// Register adds a dialect to the global registry. Intended for use from init().
func Register(l Dialect) { registry[l.Name()] = l }

// Get returns a registered dialect by name.
func Get(name string) (Dialect, bool) {
	l, ok := registry[name]
	return l, ok
}

// AsParser returns the named layer as a Parser, or an error if it is unknown or
// cannot parse (a valid state for emit-only dialects).
func AsParser(name string) (Parser, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q", name)
	}
	p, ok := l.(Parser)
	if !ok {
		return nil, fmt.Errorf("dialect %q cannot be a source (no parser)", name)
	}
	return p, nil
}

// AsEmitter returns the named layer as an Emitter, or an error if it is unknown
// or cannot emit.
func AsEmitter(name string) (Emitter, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q", name)
	}
	e, ok := l.(Emitter)
	if !ok {
		return nil, fmt.Errorf("dialect %q cannot be a target (no emitter)", name)
	}
	return e, nil
}

// Names returns the registered dialect names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
