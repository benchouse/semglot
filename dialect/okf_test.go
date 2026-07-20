package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// okfTimestamp is a fixed ISO 8601 instant. The emitter never reads a clock, so
// the caller supplies this; see TestOKFTimestampIsCallerSupplied.
const okfTimestamp = "2026-07-20T00:00:00+00:00"

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

// okfEmitter is the emitter under test, configured as the CLI configures it.
func okfEmitter() Emitter {
	return okf{}.WithOptions(Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce", Timestamp: okfTimestamp})
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
				Metrics: []ir.Metric{
					{
						Name: "revenue", Label: "Revenue", Description: "Total order value.",
						Synonyms: []string{"sales"},
						Def:      ir.Agg{Func: "sum", Arg: ir.Col{Name: "amount"}},
					},
					// No description: the reference implementation requires a
					// non-empty one, so the emitter must synthesize it.
					{Name: "order_count", Def: ir.Agg{Func: "count", Arg: ir.Col{Name: "order_id"}}},
				},
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

// TestOKFTableConceptFrontmatter verifies a table becomes a concept carrying
// every key the reference implementation's OKFDocument.validate() requires
// (type, title, description, timestamp), plus the resource URI.
func TestOKFTableConceptFrontmatter(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

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
		okfTimestamp,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("frontmatter missing %q:\n%s", want, doc)
		}
	}
}

// TestOKFTimestampIsCallerSupplied verifies the emitter never invents a
// timestamp. A clock would make two builds of the same input differ
// byte-for-byte, which the golden fixtures rely on.
func TestOKFTimestampIsCallerSupplied(t *testing.T) {
	files := emitOKF(t, okf{}, okfModel())
	if strings.Contains(files["tables/fct_orders.md"], "timestamp:") {
		t.Errorf("emitter must not synthesize a timestamp:\n%s", files["tables/fct_orders.md"])
	}

	a := emitOKF(t, okfEmitter(), okfModel())
	b := emitOKF(t, okfEmitter(), okfModel())
	for path, content := range a {
		if b[path] != content {
			t.Errorf("%s differs between two builds of the same input", path)
		}
	}
}

// TestOKFSynthesizesMissingDescriptions verifies every concept carries a
// non-empty description, which the reference implementation requires but the
// IR does not guarantee.
func TestOKFSynthesizesMissingDescriptions(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

	// A metric with no description falls back to its definition.
	if !strings.Contains(files["metrics/order_count.md"], "description: count(order_id)") {
		t.Errorf("metric description not synthesized from its definition:\n%s", files["metrics/order_count.md"])
	}
	// A table with no description still gets one.
	if !strings.Contains(files["tables/dim_customer.md"], "description: One row per customer.") {
		t.Errorf("table description missing:\n%s", files["tables/dim_customer.md"])
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
// key, enums and synonyms that the IR holds. Sections are `#` headings, as in
// the published reference bundles (the title lives in the frontmatter).
func TestOKFTableConceptBody(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())
	doc := files["tables/fct_orders.md"]

	for _, want := range []string{
		"# Overview",
		"# Primary key",
		"`order_id`",
		"# Dimensions",
		"`order_id` (number): Order key.", // data type surfaced when the source records one
		"`status`: Order state. Synonyms: state.",
		"# Time dimensions",
		"`ordered_at`: Order timestamp.",
		"# Allowed values",
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
	files := emitOKF(t, okfEmitter(), okfModel())
	doc := files["tables/fct_orders.md"]

	if !strings.Contains(doc, "# Measures") {
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

// TestOKFLinksAreRelative verifies concept links are directory-relative, as in
// the published reference bundles: the reference viewer resolves them that way,
// and index.md links must be relative for regenerate_indexes to be a no-op.
func TestOKFLinksAreRelative(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

	orders := files["tables/fct_orders.md"]
	if !strings.Contains(orders, "# Joins") {
		t.Fatalf("missing Joins section:\n%s", orders)
	}
	// Same directory, so a bare filename.
	if !strings.Contains(orders, "[dim_customer](dim_customer.md): `fct_orders.customer_id` = `dim_customer.customer_id`") {
		t.Errorf("join link should be relative:\n%s", orders)
	}
	// Across directories, so a ../ hop.
	if !strings.Contains(orders, "[Revenue](../metrics/revenue.md)") {
		t.Errorf("metric link should be relative:\n%s", orders)
	}
	if strings.Contains(orders, "](/") {
		t.Errorf("no link should be bundle-absolute:\n%s", orders)
	}
}

// TestOKFMetricConcept verifies each metric is its own concept, typed as such,
// carrying its rendered definition and a link back to the table it lives on.
func TestOKFMetricConcept(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

	doc, ok := files["metrics/revenue.md"]
	if !ok {
		t.Fatalf("missing metrics/revenue.md; got %v", keysOf(files))
	}
	for _, want := range []string{
		"type: Metric\n",
		"title: Revenue\n",
		"description: Total order value.\n",
		"sum(amount)",
		"[fct_orders](../tables/fct_orders.md)",
		"Synonyms: sales.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("metric concept missing %q:\n%s", want, doc)
		}
	}
}

// TestOKFIndexPerDirectory verifies every directory gets the reserved index.md
// in the exact shape reference_agent.bundle.index.regenerate_indexes writes:
// grouped by concept type under a `#` heading, entries as
// "* [Title](relative-link) - description", sorted by title.
func TestOKFIndexPerDirectory(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

	tables, ok := files["tables/index.md"]
	if !ok {
		t.Fatalf("missing tables/index.md; got %v", keysOf(files))
	}
	wantTables := "# Table\n\n" +
		"* [dim_customer](dim_customer.md) - One row per customer.\n" +
		"* [fct_orders](fct_orders.md) - Order-grain fact.\n"
	if tables != wantTables {
		t.Errorf("tables/index.md != reference format:\n--- got ---\n%s\n--- want ---\n%s", tables, wantTables)
	}

	metrics, ok := files["metrics/index.md"]
	if !ok {
		t.Fatalf("missing metrics/index.md")
	}
	// Sorted by title, case-insensitively: "count(order_id)" then "Revenue".
	wantMetrics := "# Metric\n\n" +
		"* [order_count](order_count.md) - count(order_id)\n" +
		"* [Revenue](revenue.md) - Total order value.\n"
	if metrics != wantMetrics {
		t.Errorf("metrics/index.md != reference format:\n--- got ---\n%s\n--- want ---\n%s", metrics, wantMetrics)
	}
}

// TestOKFRootIndexListsSubdirectories verifies the root index links each
// subdirectory's own index, the way the reference implementation does.
func TestOKFRootIndexListsSubdirectories(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

	idx, ok := files["index.md"]
	if !ok {
		t.Fatalf("missing index.md; got %v", keysOf(files))
	}
	for _, want := range []string{
		"# Subdirectories",
		"* [metrics](metrics/index.md) - ",
		"* [tables](tables/index.md) - ",
		"# Note",
		"* [Not transpiled](notes.md) - ",
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("root index missing %q:\n%s", want, idx)
		}
	}
	if strings.HasPrefix(idx, "---") {
		t.Errorf("index.md is reserved and takes no frontmatter:\n%s", idx)
	}
}

// TestOKFNotesConcept verifies passthrough notes survive as their own concept
// rather than being dropped.
func TestOKFNotesConcept(t *testing.T) {
	files := emitOKF(t, okfEmitter(), okfModel())

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
	files := emitOKF(t, okfEmitter(), m)
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
