package dialect

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
	if _, err := (naoYaml{}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "semantic.yaml"))
	if err != nil {
		t.Fatalf("read semantic.yaml: %v", err)
	}
	return string(b)
}

// TestNaoYamlWarningsExcludeModelNotes guards the double-print regression: the
// CLI already prints model.Notes separately, so an emitter's returned
// warnings must carry only ITS OWN degrade notes, never m.Notes verbatim.
func TestNaoYamlWarningsExcludeModelNotes(t *testing.T) {
	m := &ir.Model{
		Notes: []string{"a pre-existing model-level note"},
		Tables: []ir.Table{{
			Name:       "t",
			Dimensions: []ir.Field{{Name: "id", Expr: "id"}},
			Metrics: []ir.Metric{
				{Name: "cumulative_revenue", Def: ir.Window{Base: ir.Ref{Metric: "revenue"}, Window: "7 day"}},
			},
		}},
	}
	dir := t.TempDir()
	warnings, err := (naoYaml{}).Emit(m, dir)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	foundDegrade := false
	for _, w := range warnings {
		if strings.Contains(w, "cumulative_revenue") {
			foundDegrade = true
		}
		if strings.Contains(w, "a pre-existing model-level note") {
			t.Errorf("warnings must not include m.Notes (double-print regression), got: %v", warnings)
		}
	}
	if !foundDegrade {
		t.Errorf("expected a warning naming the degraded metric cumulative_revenue, got: %v", warnings)
	}
	// The artifact itself must still carry BOTH the model note and the degrade
	// note — only the returned warnings are scoped to this emitter's own.
	b, err := os.ReadFile(filepath.Join(dir, "semantic.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "a pre-existing model-level note") {
		t.Errorf("semantic.yaml must still carry the model note:\n%s", out)
	}
	if !strings.Contains(out, "cumulative_revenue") {
		t.Errorf("semantic.yaml must still carry the degrade note:\n%s", out)
	}
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

// TestNaoYamlEmitsEnumValues verifies a categorical enum becomes a structured
// `values:` list, while any per-value meanings fold into the description (nao's
// format has no per-value description slot).
func TestNaoYamlEmitsEnumValues(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "dim_customer",
		Dimensions: []ir.Field{{
			Name: "segment", Description: "Marketing segment.",
			Enum: []ir.EnumValue{{Value: "new", Description: "First order"}, {Value: "vip"}},
		}},
	}}}
	out := emitNaoYaml(t, m)
	for _, want := range []string{"values:", "- new", "- vip"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing structured value %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Values: new = First order") {
		t.Errorf("per-value meaning should fold into the description:\n%s", out)
	}
}

// TestNaoYamlDedupPrefersEnumVariant verifies that when a column name is shared
// across tables, the flat dimension dedup keeps the enum-bearing variant rather
// than a plainer first-seen duplicate — regardless of table order.
func TestNaoYamlDedupPrefersEnumVariant(t *testing.T) {
	enumField := ir.Field{Name: "status", Description: "Ticket status.",
		Enum: []ir.EnumValue{{Value: "open"}, {Value: "closed"}}}
	plainField := ir.Field{Name: "status", Description: "Product status."}

	// The enum variant carries a structured `values:` list; the plain one does not.
	hasEnumValues := func(out string) bool {
		return strings.Contains(out, "values:") && strings.Contains(out, "- open") && strings.Contains(out, "- closed")
	}

	// Plain seen first, enum second → enum must win (replace).
	out := emitNaoYaml(t, &ir.Model{Tables: []ir.Table{
		{Name: "dim_product", Dimensions: []ir.Field{plainField}},
		{Name: "fct_support_tickets", Dimensions: []ir.Field{enumField}},
	}})
	if !hasEnumValues(out) {
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
	if !hasEnumValues(out2) {
		t.Errorf("plain variant displaced the enum when seen second:\n%s", out2)
	}
}
