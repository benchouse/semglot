package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// emitOKF runs the okf emitter over m and returns the bundle as a
// path -> content map (paths are slash-separated and bundle-relative).
func emitOKF(t *testing.T, e Emitter, m *ir.Model) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk bundle: %v", err)
	}
	return out
}

func okfModel() *ir.Model {
	return &ir.Model{
		Tables: []ir.Table{
			{
				Name:        "fct_orders",
				Description: "Order-grain fact.",
				PrimaryKey:  []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Description: "Order key.", DataType: "number"},
					{Name: "status", Description: "Order state.", Synonyms: []string{"state"},
						Enum: []ir.EnumValue{{Value: "shipped", Description: "Left the warehouse."}, {Value: "open"}}},
				},
				TimeDimensions: []ir.Field{{Name: "ordered_at", Description: "Order timestamp."}},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "amount", Expr: "amount"}, Agg: "sum"},
					{Field: ir.Field{Name: "net_amount", Expr: "amount_net"}, Agg: "sum"},
				},
				Metrics: []ir.Metric{{
					Name: "revenue", Label: "Revenue", Description: "Total order value.",
					Synonyms: []string{"sales"},
					Def:      ir.Agg{Func: "sum", Arg: ir.Col{Name: "amount"}},
				}},
			},
			{Name: "dim_customer", Description: "One row per customer."},
		},
		Relationships: []ir.Relationship{{
			Left: "fct_orders", Right: "dim_customer",
			Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}},
		}},
		Notes: []string{"metric `churn` was not transpiled."},
	}
}

// TestOKFTableConceptFrontmatter verifies a table becomes a conforming concept:
// non-empty `type`, plus the recommended title/description/resource. `timestamp`
// is deliberately never emitted so bundles stay byte-stable across runs.
func TestOKFTableConceptFrontmatter(t *testing.T) {
	e := okf{}.WithOptions(Options{Database: "ANALYTICS", Schema: "MAIN"})
	files := emitOKF(t, e, okfModel())

	doc, ok := files["tables/fct_orders.md"]
	if !ok {
		t.Fatalf("missing tables/fct_orders.md; got %v", keysOf(files))
	}
	for _, want := range []string{
		"---\n",
		"type: Table\n",
		"title: fct_orders\n",
		"description: Order-grain fact.\n",
		"resource: table://ANALYTICS/MAIN/FCT_ORDERS\n",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "timestamp:") {
		t.Errorf("timestamp must not be emitted (breaks byte-stable bundles):\n%s", doc)
	}
}

// TestOKFOmitsResourceWithoutDatabase verifies resource is dropped rather than
// emitted half-qualified when the profile carries no database.
func TestOKFOmitsResourceWithoutDatabase(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())
	doc := files["tables/fct_orders.md"]
	if strings.Contains(doc, "resource:") {
		t.Errorf("resource should be omitted without a database:\n%s", doc)
	}
	if !strings.Contains(doc, "type: Table") {
		t.Errorf("type is required even without a database:\n%s", doc)
	}
}

// TestOKFTableConceptBody verifies the prose body carries the columns, primary
// key, enums and synonyms that the IR holds.
func TestOKFTableConceptBody(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())
	doc := files["tables/fct_orders.md"]

	for _, want := range []string{
		"## Primary key",
		"`order_id`",
		"## Dimensions",
		"`order_id` (number): Order key.", // data type surfaced when the source records one
		"`status`: Order state. Synonyms: state.",
		"## Time dimensions",
		"`ordered_at`: Order timestamp.",
		"## Allowed values",
		"shipped = Left the warehouse.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("body missing %q:\n%s", want, doc)
		}
	}
}

// TestOKFListsMeasureColumns verifies the aggregatable columns are documented on
// the table concept. Without this the bundle never names them: a reader would
// have to reverse them out of a metric's definition SQL.
func TestOKFListsMeasureColumns(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())
	doc := files["tables/fct_orders.md"]

	if !strings.Contains(doc, "## Measures") {
		t.Fatalf("missing Measures section:\n%s", doc)
	}
	if !strings.Contains(doc, "- `amount` (sum)") {
		t.Errorf("measure column and aggregation not emitted:\n%s", doc)
	}
	// A measure named differently from the column it aggregates must name both,
	// or the bundle never reveals the column.
	if !strings.Contains(doc, "- `net_amount` (sum of `amount_net`)") {
		t.Errorf("measure expression not emitted:\n%s", doc)
	}
}

// TestOKFJoinsLinkToRelatedConcepts verifies relationships become real bundle
// links (the OKF knowledge graph), not just prose.
func TestOKFJoinsLinkToRelatedConcepts(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())
	doc := files["tables/fct_orders.md"]

	if !strings.Contains(doc, "## Joins") {
		t.Fatalf("missing Joins section:\n%s", doc)
	}
	if !strings.Contains(doc, "[dim_customer](/tables/dim_customer.md)") {
		t.Errorf("join should link to the related concept:\n%s", doc)
	}
	if !strings.Contains(doc, "`fct_orders.customer_id` = `dim_customer.customer_id`") {
		t.Errorf("join columns not emitted:\n%s", doc)
	}
}

// TestOKFMetricConcept verifies each metric is its own concept, typed as such,
// carrying its rendered definition and a link back to the table it lives on.
func TestOKFMetricConcept(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())

	doc, ok := files["metrics/revenue.md"]
	if !ok {
		t.Fatalf("missing metrics/revenue.md; got %v", keysOf(files))
	}
	for _, want := range []string{
		"type: Metric\n",
		"title: Revenue\n",
		"description: Total order value.\n",
		"sum(amount)",
		"[fct_orders](/tables/fct_orders.md)",
		"Synonyms: sales.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("metric concept missing %q:\n%s", want, doc)
		}
	}
}

// TestOKFIndexListsConcepts verifies the reserved index.md groups every concept
// with its description, for progressive disclosure.
func TestOKFIndexListsConcepts(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())

	idx, ok := files["index.md"]
	if !ok {
		t.Fatalf("missing index.md; got %v", keysOf(files))
	}
	for _, want := range []string{
		"## Tables",
		"[fct_orders](/tables/fct_orders.md): Order-grain fact.",
		"[dim_customer](/tables/dim_customer.md): One row per customer.",
		"## Metrics",
		"[Revenue](/metrics/revenue.md): Total order value.",
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index missing %q:\n%s", want, idx)
		}
	}
	if strings.Contains(idx, "---\n") {
		t.Errorf("index.md is reserved and takes no frontmatter:\n%s", idx)
	}
}

// TestOKFNotesConcept verifies passthrough notes survive as their own concept
// rather than being dropped.
func TestOKFNotesConcept(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())

	doc, ok := files["notes.md"]
	if !ok {
		t.Fatalf("missing notes.md; got %v", keysOf(files))
	}
	if !strings.Contains(doc, "type: Note\n") {
		t.Errorf("notes concept needs a type:\n%s", doc)
	}
	if !strings.Contains(doc, "metric `churn` was not transpiled.") {
		t.Errorf("note text not carried:\n%s", doc)
	}
}

// TestOKFNoNotesConceptWhenEmpty verifies an empty note list writes no file.
func TestOKFNoNotesConceptWhenEmpty(t *testing.T) {
	m := okfModel()
	m.Notes = nil
	files := emitOKF(t, okf{}, m)
	if _, ok := files["notes.md"]; ok {
		t.Errorf("notes.md should not be written when there are no notes")
	}
}

// TestOKFRegistered verifies the dialect is reachable as a target by name and
// is not offered as a source.
func TestOKFRegistered(t *testing.T) {
	if _, err := AsEmitter("okf"); err != nil {
		t.Fatalf("okf should be a registered target: %v", err)
	}
	if _, err := AsParser("okf"); err == nil {
		t.Errorf("okf is emit-only and must not register as a source")
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
