// Package layer defines the dialect plugin surface (Parser/Emitter) and a
// registry mapping dialect names to implementations.
package layer

import (
	"fmt"
	"sort"

	"github.com/benchouse/semglot/ir"
)

// Layer is any registered semantic-layer dialect.
type Layer interface {
	Name() string
}

// Parser reads a dialect's files from dir into the neutral IR.
type Parser interface {
	Layer
	// Parse reads *.yml from each source directory (non-recursive) and merges
	// them into one IR model. Multiple sources let a dbt project's schema files
	// spread across folders (e.g. models/semantic + models/marts) be combined.
	Parse(sources ...string) (*ir.Model, error)
}

// Emitter writes the neutral IR out as a dialect's files under dir.
type Emitter interface {
	Layer
	Emit(m *ir.Model, dir string) error
}

// Configurable is an Emitter that accepts base_table/model identity options.
type Configurable interface {
	WithOptions(database, schema, name, description string) Emitter
}

var registry = map[string]Layer{}

// Register adds a layer to the global registry. Intended for use from init().
func Register(l Layer) { registry[l.Name()] = l }

// Get returns a registered layer by name.
func Get(name string) (Layer, bool) {
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
