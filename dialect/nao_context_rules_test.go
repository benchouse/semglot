package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func emitContextRules(t *testing.T, m *ir.Model) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := (naoContextRules{}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "RULES.md"))
	if err != nil {
		t.Fatalf("read RULES.md: %v", err)
	}
	return string(b)
}

// TestContextRulesEmitsColumnDescriptionsAndSynonyms verifies the rules layer
// carries per-column descriptions + synonyms (parity with nao-yaml/cortex —
// nao's synced schema has only name+type), and omits columns that would add
// nothing beyond that.
func TestContextRulesEmitsColumnDescriptionsAndSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "dim_customer",
		Dimensions: []ir.Field{
			{Name: "customer_segment", Description: "Marketing segment.", Synonyms: []string{"segment", "customer_type"}},
			{Name: "email", Description: "Customer email."},
			{Name: "bare_col"}, // no description, no synonyms → omitted
		},
	}}}
	out := emitContextRules(t, m)

	if !strings.Contains(out, "## Columns") {
		t.Fatalf("missing Columns section:\n%s", out)
	}
	if !strings.Contains(out, "`dim_customer.customer_segment`: Marketing segment. Synonyms: segment, customer_type.") {
		t.Errorf("column description + synonyms not emitted:\n%s", out)
	}
	if !strings.Contains(out, "`dim_customer.email`: Customer email.") {
		t.Errorf("plain column description not emitted:\n%s", out)
	}
	if strings.Contains(out, "bare_col") {
		t.Errorf("column with no description/synonyms should be omitted (synced name+type suffices):\n%s", out)
	}
}

// TestContextRulesTableSectionRenamed verifies the table glossary is labelled
// "Table reference", not the misleading "Table traps".
func TestContextRulesTableSectionRenamed(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "fct_orders", Description: "Order-grain fact."}}}
	out := emitContextRules(t, m)
	if strings.Contains(out, "Table traps") {
		t.Errorf("section should be renamed away from 'Table traps':\n%s", out)
	}
	if !strings.Contains(out, "## Table reference") {
		t.Errorf("missing 'Table reference' section:\n%s", out)
	}
}
