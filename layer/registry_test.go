package layer

import (
	"testing"

	"github.com/benchouse/semglot/ir"
)

type fakeParser struct{}

func (fakeParser) Name() string                       { return "fake-parser" }
func (fakeParser) Parse(...string) (*ir.Model, error) { return &ir.Model{}, nil }

type fakeEmitter struct{}

func (fakeEmitter) Name() string                 { return "fake-emitter" }
func (fakeEmitter) Emit(*ir.Model, string) error { return nil }

func TestRegistryCapabilities(t *testing.T) {
	Register(fakeParser{})
	Register(fakeEmitter{})

	if _, err := AsParser("fake-parser"); err != nil {
		t.Fatalf("AsParser(fake-parser): %v", err)
	}
	if _, err := AsEmitter("fake-emitter"); err != nil {
		t.Fatalf("AsEmitter(fake-emitter): %v", err)
	}
	if _, err := AsEmitter("fake-parser"); err == nil {
		t.Fatal("expected fake-parser to lack an emitter")
	}
	if _, err := AsParser("nope"); err == nil {
		t.Fatal("expected unknown dialect error")
	}
}
