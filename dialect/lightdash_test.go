package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// emitLightdash runs the lightdash emitter (with opts) over m and returns the
// emitted schema.yml as a string.
func emitLightdash(t *testing.T, m *ir.Model, opts Options) string {
	t.Helper()
	e := lightdash{}.WithOptions(opts)
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatalf("read schema.yml: %v", err)
	}
	return string(b)
}

func TestLightdashRegisteredAndSkeleton(t *testing.T) {
	if _, err := AsEmitter("lightdash"); err != nil {
		t.Fatalf("AsEmitter(lightdash): %v", err)
	}
	m := &ir.Model{
		Tables: []ir.Table{{Name: "orders", Description: "One row per order."}},
		Notes:  []string{"a passthrough note"},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	if !strings.HasPrefix(got, "# semglot:") {
		t.Errorf("expected leading # semglot comment block, got:\n%s", got)
	}
	if !strings.Contains(got, "# - a passthrough note") {
		t.Errorf("passthrough note missing from comment block:\n%s", got)
	}

	var doc struct {
		Version int `yaml:"version"`
		Models  []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("version = %d, want 2", doc.Version)
	}
	if len(doc.Models) != 1 || doc.Models[0].Name != "orders" {
		t.Fatalf("models = %+v, want one named orders", doc.Models)
	}
	if doc.Models[0].Description != "One row per order." {
		t.Errorf("description = %q", doc.Models[0].Description)
	}
}
