package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// dbxTestModel: one fact (orders) joined to a dimension (customers), with a
// simple aggregate metric, a same-grain derived ratio, and a cross-grain
// derived ratio that references a metric on another fact (lines).
func dbxTestModel() *ir.Model {
	orders := ir.Table{
		Name: "orders",
		Dimensions: []ir.Field{
			{Name: "status", Expr: "status", Description: "Order status"},
			// aov: a precomputed physical column that collides with the aov metric
			// below (revenue / order_count). Reproduces the real Databricks failure
			// (METRIC_VIEW_INVALID_VIEW_DEFINITION: duplicate name) where a source
			// table has both a precomputed column and a computed metric of the same
			// name; the computed metric must win and the column must be dropped.
			{Name: "aov", Expr: "aov", Description: "Precomputed average order value"},
		},
		Metrics: []ir.Metric{
			{Name: "revenue", Label: "Revenue", Description: "Gross revenue",
				Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
			{Name: "order_count",
				Def: ir.Agg{Func: "count_distinct", Table: "orders", Arg: ir.Col{Name: "order_id"}}},
			{Name: "aov", // same-grain derived: revenue / order_count
				Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "order_count"}}},
			{Name: "refunded", Description: "Refunded orders",
				Def: ir.Agg{Func: "sum", Table: "orders",
					Arg: ir.Raw{SQL: "case when refunded then 1 else 0 end", Columns: []string{"refunded"}}}},
		},
		// Raw measures alongside metrics: orders_count near-duplicates the
		// order_count metric above (same expr, different name) and must NOT also
		// be emitted. avg_shipping_cost has no metric equivalent and must survive
		// alongside the metric-derived measures.
		Measures: []ir.Measure{
			{Field: ir.Field{Name: "orders_count", Expr: "order_id"}, Agg: "count_distinct"},
			{Field: ir.Field{Name: "avg_shipping_cost", Expr: "shipping_cost", Description: "Average shipping cost"}, Agg: "avg"},
		},
	}
	customers := ir.Table{
		Name:       "customers",
		Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
	}
	lines := ir.Table{
		Name:       "lines",
		Dimensions: []ir.Field{{Name: "sku", Expr: "sku"}},
		Metrics: []ir.Metric{
			{Name: "units", Def: ir.Agg{Func: "sum", Table: "lines", Arg: ir.Col{Name: "qty"}}},
			{Name: "units_per_order", // cross-grain: references orders' order_count
				Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "units"}, Right: ir.Ref{Metric: "order_count"}}},
		},
	}
	// obt_wide: measures but zero metrics. It must still get its own view, with
	// measures rendered directly from the raw ir.Measure (aggExpr), since there
	// is no metric to source them from.
	obtWide := ir.Table{
		Name:       "obt_wide",
		Dimensions: []ir.Field{{Name: "segment", Expr: "segment"}},
		Measures: []ir.Measure{
			{Field: ir.Field{Name: "units_sold", Expr: "quantity", Description: "Units sold", Synonyms: []string{"qty"}}, Agg: "sum"},
			{Field: ir.Field{Name: "net_revenue", Expr: "net_revenue"}, Agg: "sum"},
		},
	}
	// mixedCase: a table name carrying original dbt-YAML casing, reproducing the
	// real Databricks failure where a mixed-case semantic-model name (e.g.
	// FCT_Orders) survives into the rendered measure expr as a qualifier that
	// Databricks cannot resolve (the source relation is aliased `source`).
	mixedCase := ir.Table{
		Name:       "FCT_Orders",
		Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
		Metrics: []ir.Metric{
			{Name: "gross_amount",
				Def: ir.Agg{Func: "sum", Table: "FCT_Orders", Arg: ir.Col{Table: "FCT_Orders", Name: "amount"}}},
		},
	}
	return &ir.Model{
		Tables: []ir.Table{orders, customers, lines, obtWide, mixedCase},
		Relationships: []ir.Relationship{
			{Left: "orders", Right: "customers", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
		},
	}
}

func emitDbx(t *testing.T, m *ir.Model) map[string]string {
	t.Helper()
	e := databricksMetricView{}.WithOptions(Options{Database: "ANALYTICS", Schema: "MAIN"})
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[ent.Name()] = string(b)
	}
	return out
}

func TestDatabricksMetricViewOrders(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got, ok := files["orders.yaml"]
	if !ok {
		t.Fatalf("expected orders.yaml, got files: %v", files)
	}
	for _, want := range []string{
		`version: "1.1"`,
		"source: analytics.main.orders",
		`"on": source.customer_id = customers.customer_id`,
		"source: analytics.main.customers",       // the join source
		"expr: customers.region",                 // joined dimension, prefixed
		"expr: sum(amount)",                      // simple metric lowered (renderSQL is lowercase)
		"sum(amount) / count(distinct order_id)", // same-grain derived, inlined
	} {
		if !strings.Contains(got, want) {
			t.Errorf("orders.yaml missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestDatabricksMetricViewNoDimensionFile: a pure dimension table (no metrics,
// no measures) still gets its own metric view — a Databricks metric view
// requires >=1 measure, so one is synthesised as a row count. This mirrors the
// sibling targets (cortex, snowflake-semantic-view, supersimple), which all
// emit every IR table rather than dropping dimension-only ones.
func TestDatabricksMetricViewNoDimensionFile(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got, ok := files["customers.yaml"]
	if !ok {
		t.Fatalf("customers is dimension-only but must still get a view with a synthesised row count; got files: %v", keysOfDbx(files))
	}
	if !strings.Contains(got, "expr: count(1)") {
		t.Errorf("expected synthesised row-count measure expr, got:\n%s", got)
	}
	if !strings.Contains(got, "name: row_count") {
		t.Errorf("expected synthesised row-count measure name, got:\n%s", got)
	}
}

func TestDatabricksMetricViewMeasuresBareColumns(t *testing.T) {
	got := emitDbx(t, dbxTestModel())["orders.yaml"]
	if !strings.Contains(got, "sum(case when refunded then 1 else 0 end)") {
		t.Errorf("expected bare filtered sum, got:\n%s", got)
	}
	if strings.Contains(got, "orders.refunded") || strings.Contains(got, "sum(orders.") {
		t.Errorf("measure expr must not carry the source-table qualifier:\n%s", got)
	}
}

func TestDatabricksMetricViewCrossGrainDegrades(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got := files["lines.yaml"]
	if strings.Contains(got, "units_per_order\n") || strings.Contains(got, "name: units_per_order") {
		t.Errorf("cross-grain metric units_per_order should not be an emitted measure\n%s", got)
	}
	if !strings.Contains(got, "units_per_order") || !strings.Contains(strings.ToLower(got), "cross-grain") {
		t.Errorf("cross-grain metric should be noted in the comment\n%s", got)
	}
}

// TestDatabricksMetricViewMeasuresOnlyTable covers Filter 1 and Filter 2: a
// table with measures but zero metrics must not be dropped, and its measures
// must be rendered from the raw ir.Measure (via aggExpr), since there is no
// metric to source them from.
func TestDatabricksMetricViewMeasuresOnlyTable(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got, ok := files["obt_wide.yaml"]
	if !ok {
		t.Fatalf("expected obt_wide.yaml (measures-only table must still get a view); got files: %v", keysOfDbx(files))
	}
	for _, want := range []string{
		"name: units_sold",
		"expr: sum(quantity)",
		"name: net_revenue",
		"expr: sum(net_revenue)",
		"comment: Units sold",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("obt_wide.yaml missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestDatabricksMetricViewMetricsWinOverRawMeasures guards against a
// duplication regression: when a table HAS metrics, its measures must come
// from metrics only — raw ir.Measure entries on the same table (orders_count)
// must not also be emitted, since that would near-duplicate the order_count
// metric.
func TestDatabricksMetricViewMetricsWinOverRawMeasures(t *testing.T) {
	got := emitDbx(t, dbxTestModel())["orders.yaml"]
	if strings.Contains(got, "orders_count") {
		t.Errorf("orders.yaml has metrics; raw measure orders_count must not also be emitted\n%s", got)
	}
}

// TestDatabricksMetricViewNoSpuriousRowCount guards the fallback's precision:
// a table whose measures list ends up non-empty (from metrics, here) must not
// also get a synthesised row_count alongside its real measures.
func TestDatabricksMetricViewNoSpuriousRowCount(t *testing.T) {
	got := emitDbx(t, dbxTestModel())["orders.yaml"]
	if strings.Contains(got, "row_count") {
		t.Errorf("orders.yaml has real measures from metrics; must not also get a synthesised row_count\n%s", got)
	}
}

// TestDatabricksMetricViewMeasureDimensionCollision reproduces a live Databricks
// deploy failure: METRIC_VIEW_INVALID_VIEW_DEFINITION, "Measure and dimension
// names must be unique. Duplicate names: roas". orders has both a computed aov
// metric and a precomputed aov dimension column (see dbxTestModel). Databricks
// requires field and measure names to be disjoint, so the column must be
// dropped and the computed metric must win — mirroring the established
// snowflake-semantic-view precedent for the same collision.
func TestDatabricksMetricViewMeasureDimensionCollision(t *testing.T) {
	got := emitDbx(t, dbxTestModel())["orders.yaml"]
	if n := strings.Count(got, "name: aov"); n != 1 {
		t.Errorf("expected aov to appear exactly once (as the measure), got %d occurrences:\n%s", n, got)
	}
	if strings.Contains(got, "expr: aov") {
		t.Errorf("precomputed aov column must be dropped as a field, not emitted:\n%s", got)
	}
	if !strings.Contains(got, "sum(amount) / count(distinct order_id)") {
		t.Errorf("computed aov metric must still be emitted as a measure:\n%s", got)
	}
}

// TestDatabricksMetricViewUncoveredRawMeasureSurvives is Fix 1: a table with
// metrics must not drop raw measures no metric covers. orders has metrics AND
// a raw measure (avg_shipping_cost) whose expression no metric produces, so it
// must still be emitted. orders_count, whose expr duplicates the order_count
// metric, must still be suppressed (guards the near-duplicate regression the
// original all-or-nothing design existed to prevent).
func TestDatabricksMetricViewUncoveredRawMeasureSurvives(t *testing.T) {
	got := emitDbx(t, dbxTestModel())["orders.yaml"]
	for _, want := range []string{
		"name: avg_shipping_cost",
		"expr: avg(shipping_cost)",
		"comment: Average shipping cost",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("orders.yaml missing uncovered raw measure %q\n--- got ---\n%s", want, got)
		}
	}
	if strings.Contains(got, "orders_count") {
		t.Errorf("orders.yaml: raw measure orders_count duplicates order_count metric's expr; must not be emitted\n%s", got)
	}
}

// TestDatabricksMetricViewMixedCaseTableStripsQualifier is Fix 2: a table name
// carrying dbt-YAML case (FCT_Orders) must not leak that qualifier into a
// rendered measure expr — the source relation in a metric view is aliased
// `source`, and Databricks cannot resolve `FCT_Orders.amount`.
func TestDatabricksMetricViewMixedCaseTableStripsQualifier(t *testing.T) {
	got, ok := emitDbx(t, dbxTestModel())["fct_orders.yaml"]
	if !ok {
		t.Fatalf("expected fct_orders.yaml (from table FCT_Orders)")
	}
	if !strings.Contains(got, "expr: sum(amount)") {
		t.Errorf("expected bare sum(amount), got:\n%s", got)
	}
	if strings.Contains(got, "FCT_Orders.") {
		t.Errorf("measure expr must not carry the mixed-case source-table qualifier:\n%s", got)
	}
}

func keysOfDbx(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
