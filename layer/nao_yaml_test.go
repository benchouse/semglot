package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// emitNaoYaml emits m to a temp dir and returns the semantic.yaml text.
func emitNaoYaml(t *testing.T, m *ir.Model) string {
	t.Helper()
	dir := t.TempDir()
	if err := (naoYaml{}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "semantic.yaml"))
	if err != nil {
		t.Fatalf("read semantic.yaml: %v", err)
	}
	return string(b)
}

// TestNaoYamlFoldsSynonyms verifies a field's synonyms are folded into the
// dimension description (nao-yaml has no structured synonyms field).
func TestNaoYamlFoldsSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "dim_customer",
		Dimensions: []ir.Field{
			{Name: "customer_segment", Description: "Marketing segment.", Synonyms: []string{"segment", "customer_type"}},
		},
	}}}
	out := emitNaoYaml(t, m)
	if !strings.Contains(out, "Synonyms: segment, customer_type.") {
		t.Errorf("synonyms not folded into description:\n%s", out)
	}
}

// TestNaoYamlDedupPrefersEnumVariant verifies that when a column name is shared
// across tables, the flat dimension dedup keeps the enum-bearing variant rather
// than a plainer first-seen duplicate — regardless of table order.
func TestNaoYamlDedupPrefersEnumVariant(t *testing.T) {
	enumField := ir.Field{Name: "status", Description: "Ticket status.",
		Enum: []ir.EnumValue{{Value: "open"}, {Value: "closed"}}}
	plainField := ir.Field{Name: "status", Description: "Product status."}

	// Plain seen first, enum second → enum must win (replace).
	out := emitNaoYaml(t, &ir.Model{Tables: []ir.Table{
		{Name: "dim_product", Dimensions: []ir.Field{plainField}},
		{Name: "fct_support_tickets", Dimensions: []ir.Field{enumField}},
	}})
	if !strings.Contains(out, "Values: open, closed.") {
		t.Errorf("enum variant did not win when seen second:\n%s", out)
	}
	if strings.Count(out, "name: status") != 1 {
		t.Errorf("expected exactly one status dimension, got:\n%s", out)
	}

	// Enum seen first, plain second → plain must NOT displace the enum.
	out2 := emitNaoYaml(t, &ir.Model{Tables: []ir.Table{
		{Name: "fct_support_tickets", Dimensions: []ir.Field{enumField}},
		{Name: "dim_product", Dimensions: []ir.Field{plainField}},
	}})
	if !strings.Contains(out2, "Values: open, closed.") {
		t.Errorf("plain variant displaced the enum when seen second:\n%s", out2)
	}
}
