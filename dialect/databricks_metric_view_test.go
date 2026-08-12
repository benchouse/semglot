package dialect

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
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
	// is no metric to source them from. gross_revenue and adjusted_revenue are a
	// pure raw-vs-raw pair: same agg+expr (sum(amount)), different names, no
	// metric involved — both must survive (Fix B: the metric-vs-raw expression
	// dedup must never apply raw-vs-raw, or distinct measures silently collapse
	// to one).
	obtWide := ir.Table{
		Name:       "obt_wide",
		Dimensions: []ir.Field{{Name: "segment", Expr: "segment"}},
		Measures: []ir.Measure{
			{Field: ir.Field{Name: "units_sold", Expr: "quantity", Description: "Units sold", Synonyms: []string{"qty"}}, Agg: "sum"},
			{Field: ir.Field{Name: "net_revenue", Expr: "net_revenue"}, Agg: "sum"},
			{Field: ir.Field{Name: "gross_revenue", Expr: "amount"}, Agg: "sum"},
			{Field: ir.Field{Name: "adjusted_revenue", Expr: "amount"}, Agg: "sum"},
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
	// caseDedup: a metric whose rendered expr keeps source case (ORDER_ID) beside
	// a raw measure over the same column lowercase (order_id). Fix C: the
	// expression dedup must compare on a normalised (lowercased) form, or the
	// near-duplicate slips through case-sensitively and both are emitted.
	caseDedup := ir.Table{
		Name:       "case_dedup",
		Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
		Metrics: []ir.Metric{
			{Name: "unique_orders",
				Def: ir.Agg{Func: "count_distinct", Table: "case_dedup", Arg: ir.Col{Name: "ORDER_ID"}}},
		},
		Measures: []ir.Measure{
			{Field: ir.Field{Name: "orders_count", Expr: "order_id"}, Agg: "count_distinct"},
		},
	}
	// orders2/dimCustomer2: Fix D. A joined dimension whose Expr is a compound
	// expression (not a bare column) must not be blindly prefixed with the join
	// name (dim_customer2.coalesce(region, 'unknown') is invalid SQL that fails
	// the whole view) — it must be skipped and noted. A bare-column joined
	// dimension (tier) must still be emitted prefixed as before.
	orders2 := ir.Table{
		Name:       "orders2",
		Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
	}
	dimCustomer2 := ir.Table{
		Name: "dim_customer2",
		Dimensions: []ir.Field{
			{Name: "tier", Expr: "tier"},
			{Name: "region_norm", Expr: "coalesce(region, 'unknown')"},
		},
	}
	// caseCollide: two metrics whose lowercased names collide (AOV vs aov). Fix
	// E: usedNames must be checked/populated INSIDE the metric loop so the
	// second metric is skipped and noted, rather than both emitting and
	// Databricks rejecting the whole view for a duplicate name.
	caseCollide := ir.Table{
		Name:       "case_collide",
		Dimensions: []ir.Field{{Name: "segment", Expr: "segment"}},
		Metrics: []ir.Metric{
			{Name: "AOV", Def: ir.Agg{Func: "sum", Table: "case_collide", Arg: ir.Col{Name: "amount"}}},
			{Name: "aov", Def: ir.Agg{Func: "avg", Table: "case_collide", Arg: ir.Col{Name: "amount"}}},
		},
	}
	return &ir.Model{
		Tables: []ir.Table{orders, customers, lines, obtWide, mixedCase, caseDedup, orders2, dimCustomer2, caseCollide},
		Relationships: []ir.Relationship{
			{Left: "orders", Right: "customers", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
			{Left: "orders2", Right: "dim_customer2", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
		},
	}
}

func emitDbx(t *testing.T, m *ir.Model) map[string]string {
	t.Helper()
	files, _ := emitDbxW(t, m)
	return files
}

// emitDbxW is emitDbx plus the returned warnings, for tests that need to
// assert on what the emitter reports rather than just what it writes.
func emitDbxW(t *testing.T, m *ir.Model) (map[string]string, []string) {
	t.Helper()
	e := databricksMetricView{}.WithOptions(Options{Database: "ANALYTICS", Schema: "MAIN"})
	dir := t.TempDir()
	warnings, err := e.Emit(m, dir)
	if err != nil {
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
	return out, warnings
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

// TestDatabricksMetricViewPrefersDeclaredSource covers task 16's defect: a
// declared ossie `source` is a fully-qualified physical address that nothing
// downstream can recover if it is discarded. `source:` must use it verbatim
// rather than relocating the table to the profile's catalog/schema under the
// IR's logical name.
func TestDatabricksMetricViewPrefersDeclaredSource(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Source:     "PROD.SALES.orders_v1",
		Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
	}}}
	files, warnings := emitDbxW(t, m)
	for _, w := range warnings {
		if strings.Contains(w, "orders") {
			t.Errorf("unexpected warning for a cleanly-splittable source: %q", w)
		}
	}
	got, ok := files["orders.yaml"]
	if !ok {
		t.Fatalf("expected orders.yaml, got files: %v", files)
	}
	if !strings.Contains(got, "source: PROD.SALES.orders_v1") {
		t.Errorf("source must be the declared PROD.SALES.orders_v1, not analytics.main.orders:\n%s", got)
	}
}

// TestDatabricksMetricViewAcceptsTwoPartSourceVerbatim covers fix round 1's
// correction: `source:` holds its reference as ONE string (unlike
// cortexBaseTable's separate Database/Schema/Table fields), so a two-part
// schema.table source — which resolves fine against the current catalog —
// must be used verbatim, with no warning, rather than rejected in favour of a
// same-shaped fabricated two-part address from the profile.
func TestDatabricksMetricViewAcceptsTwoPartSourceVerbatim(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Source:     "crm.orders",
		Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
	}}}
	files, warnings := emitDbxW(t, m)
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings for a usable two-part source: %v", warnings)
	}
	got, ok := files["orders.yaml"]
	if !ok {
		t.Fatalf("expected orders.yaml, got files: %v", files)
	}
	if !strings.Contains(got, "source: crm.orders") {
		t.Errorf("source must use the declared two-part crm.orders verbatim:\n%s", got)
	}
}

// TestDatabricksMetricViewQuerySourceFallsBackAndWarns covers the case that
// genuinely can't go in `source:`: the OSI spec permits `source` to be a
// query rather than a table reference, but a metric view's `source:` needs
// an address, not a subquery embedded as if it were one. That must fall back
// to the profile reconstruction AND warn.
func TestDatabricksMetricViewQuerySourceFallsBackAndWarns(t *testing.T) {
	source := "SELECT * FROM raw.orders WHERE deleted_at IS NULL"
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Source:     source,
		Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
	}}}
	files, warnings := emitDbxW(t, m)
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, `table "orders"`) && strings.Contains(w, source) && strings.Contains(w, "databricks-metric-view") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the table, the query source, and databricks-metric-view; got %v", warnings)
	}
	got, ok := files["orders.yaml"]
	if !ok {
		t.Fatalf("expected orders.yaml, got files: %v", files)
	}
	if !strings.Contains(got, "source: analytics.main.orders") {
		t.Errorf("must fall back to the profile-reconstructed reference, not paste the query into source:\n%s", got)
	}
}

// TestDatabricksMetricViewJoinPrefersDeclaredSource covers fix round 1's
// critical finding: a join's source was still built from the joining view's
// own catalog/schema, ignoring the JOINED table's own declared Source —
// producing a view whose own `source:` correctly pointed at its declared
// address while `joins[].source:` pointed at a fabricated, unrelated location
// for the very same entity (and orphaned any real declared source for it
// entirely). The joined table's Source must resolve the same way the view's
// own source does.
func TestDatabricksMetricViewJoinPrefersDeclaredSource(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name:       "orders",
				Source:     "PROD.SALES.orders_v1",
				Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
			},
			{
				Name:       "customer",
				Source:     "OTHERDB.CRM.customer_master",
				Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "orders", Right: "customer", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
		},
	}
	files, warnings := emitDbxW(t, m)
	for _, w := range warnings {
		if strings.Contains(w, "orders") || strings.Contains(w, "customer") {
			t.Errorf("unexpected warning for two cleanly-declared sources: %q", w)
		}
	}
	got, ok := files["orders.yaml"]
	if !ok {
		t.Fatalf("expected orders.yaml, got files: %v", files)
	}
	if !strings.Contains(got, "source: PROD.SALES.orders_v1") {
		t.Errorf("view's own source must be the declared PROD.SALES.orders_v1:\n%s", got)
	}
	if !strings.Contains(got, "source: OTHERDB.CRM.customer_master") {
		t.Errorf("join's source must be the JOINED table's declared OTHERDB.CRM.customer_master, not the view's own catalog/schema:\n%s", got)
	}
	if strings.Contains(got, "source: analytics.main.customer") {
		t.Errorf("join must not fall back to a fabricated analytics.main.customer when customer declares its own source:\n%s", got)
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

// TestDatabricksMetricViewMeasureNoExprDefaultsToName is Fix A's target-level
// guard: a dbt measure declared with no expr must parse to a defaulted
// column name (fixed in the parser, dialect/dbt.go) so the databricks target
// never renders an argument-less aggregate like sum() — Databricks rejects
// the entire view with WRONG_NUM_ARGS.WITHOUT_SUGGESTION for that.
func TestDatabricksMetricViewMeasureNoExprDefaultsToName(t *testing.T) {
	model, err := dbt{}.Parse("testdata/dbt_measure_no_expr")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	files := emitDbx(t, model)
	got, ok := files["fct_orders.yaml"]
	if !ok {
		t.Fatalf("expected fct_orders.yaml, got files: %v", keysOfDbx(files))
	}
	if strings.Contains(got, "sum()") {
		t.Fatalf("measure with no expr must never emit sum(), got:\n%s", got)
	}
	if !strings.Contains(got, "expr: sum(order_total)") {
		t.Errorf("expected expr: sum(order_total), got:\n%s", got)
	}
}

// TestDatabricksMetricViewRawVsRawNotDeduped is Fix B: the expression dedup
// exists only to suppress a raw measure that near-duplicates a METRIC. It
// must never apply raw-vs-raw — two distinct raw measures sharing an
// agg+expr (gross_revenue and adjusted_revenue, both sum(amount), no metric
// involved) must both survive; collapsing them silently drops a named fact
// with its own label/description/synonyms.
func TestDatabricksMetricViewRawVsRawNotDeduped(t *testing.T) {
	got := emitDbx(t, dbxTestModel())["obt_wide.yaml"]
	if !strings.Contains(got, "name: gross_revenue") {
		t.Errorf("distinct raw measure gross_revenue must be emitted:\n%s", got)
	}
	if !strings.Contains(got, "name: adjusted_revenue") {
		t.Errorf("distinct raw measure adjusted_revenue must be emitted (same expr as gross_revenue, different name):\n%s", got)
	}
	if n := strings.Count(got, "sum(amount)"); n != 2 {
		t.Errorf("expected sum(amount) to appear twice (once per distinct measure), got %d:\n%s", n, got)
	}
}

// TestDatabricksMetricViewExprDedupIsCaseInsensitive is Fix C: the metric side
// renders with source case preserved (count(distinct ORDER_ID)); the raw side
// always lowercases its column (count(distinct order_id)). The near-duplicate
// dedup must compare a normalised (lowercased) form, or a case difference lets
// both through.
func TestDatabricksMetricViewExprDedupIsCaseInsensitive(t *testing.T) {
	got, ok := emitDbx(t, dbxTestModel())["case_dedup.yaml"]
	if !ok {
		t.Fatalf("expected case_dedup.yaml")
	}
	if strings.Contains(got, "orders_count") {
		t.Errorf("raw measure orders_count duplicates the unique_orders metric's expr (case-insensitively); must not also be emitted:\n%s", got)
	}
	if n := strings.Count(got, "count(distinct"); n != 1 {
		t.Errorf("expected exactly one count(distinct ...) measure, got %d:\n%s", n, got)
	}
}

// TestDatabricksMetricViewJoinedCompoundExprSkipped is Fix D: a joined
// dimension whose Expr is not a bare column (coalesce(region, 'unknown'))
// must not be blindly prefixed with the join name — that emits invalid SQL
// (dim_customer2.coalesce(...)) and Databricks rejects the entire view. It
// must be skipped and noted in the comment instead. A bare-column joined
// dimension (tier) is unaffected and still emitted prefixed.
func TestDatabricksMetricViewJoinedCompoundExprSkipped(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got, ok := files["orders2.yaml"]
	if !ok {
		t.Fatalf("expected orders2.yaml, got files: %v", keysOfDbx(files))
	}
	if strings.Contains(got, "name: region_norm") {
		t.Errorf("compound joined dimension must not be emitted as a field:\n%s", got)
	}
	if strings.Contains(got, "dim_customer2.coalesce") {
		t.Errorf("compound joined dimension must never be emitted as invalid SQL:\n%s", got)
	}
	if !strings.Contains(got, "region_norm") {
		t.Errorf("skipped joined dimension must be noted in the view comment:\n%s", got)
	}
	if !strings.Contains(got, "expr: dim_customer2.tier") {
		t.Errorf("bare-column joined dimension must still be emitted prefixed:\n%s", got)
	}
}

// TestDatabricksMetricViewMetricNameCaseCollision is Fix E: two metrics on the
// same table whose lowercased names collide (AOV, aov) must not both emit —
// Databricks rejects the whole view for a duplicate name, the very failure
// mode the dedup logic exists to prevent. The second is skipped and noted.
func TestDatabricksMetricViewMetricNameCaseCollision(t *testing.T) {
	got, ok := emitDbx(t, dbxTestModel())["case_collide.yaml"]
	if !ok {
		t.Fatalf("expected case_collide.yaml")
	}
	if n := strings.Count(got, "name: aov"); n != 1 {
		t.Errorf("expected exactly one measure named aov (AOV/aov case-insensitive collision), got %d:\n%s", n, got)
	}
	if !strings.Contains(strings.ToLower(got), "collide") {
		t.Errorf("expected a note about the metric name collision:\n%s", got)
	}
}

// TestDatabricksMetricViewHostileAggsNeverReachYAML is Fix 1 + Fix 2: three
// review rounds in a row, the defect has been "an expression Databricks would
// reject reached the YAML" (unqualified names, compound joined dimensions,
// now an unvalidated agg). One invalid measure rejects the ENTIRE metric
// view, losing every other measure/dimension/join of that table, so this is
// a structural guard against the whole defect class rather than another
// single reproduction: it builds a table with a normal measure alongside
// every hostile shape named in the review (an agg with no Databricks
// equivalent, an agg omitted entirely, an expr omitted entirely, and the
// now-fixed sum_boolean), then asserts, generically and without hardcoding
// names, that every measures[].expr in every emitted view (this test's own
// hostile table AND the shared fixture) is built only from known-safe
// aggregate calls.
func TestDatabricksMetricViewHostileAggsNeverReachYAML(t *testing.T) {
	hostile := &ir.Model{
		Tables: []ir.Table{{
			Name:       "hostile",
			Dimensions: []ir.Field{{Name: "segment", Expr: "segment"}},
			Measures: []ir.Measure{
				{Field: ir.Field{Name: "normal_sum", Expr: "amount"}, Agg: "sum"},
				{Field: ir.Field{Name: "refunded_flag", Expr: "is_refunded"}, Agg: "sum_boolean"},
				{Field: ir.Field{Name: "p50", Expr: "order_total"}, Agg: "percentile"},
				{Field: ir.Field{Name: "no_agg", Expr: "shipping_cost"}, Agg: ""}, // Agg deliberately omitted
				{Field: ir.Field{Name: "no_expr"}, Agg: "sum"},                    // Expr deliberately omitted
			},
		}},
	}
	files := emitDbx(t, hostile)
	got, ok := files["hostile.yaml"]
	if !ok {
		t.Fatalf("expected hostile.yaml, got files: %v", keysOfDbx(files))
	}

	// The normal measure and the now-fixed sum_boolean measure must survive.
	for _, want := range []string{
		"name: normal_sum", "expr: sum(amount)",
		"name: refunded_flag", "expr: sum(case when is_refunded then 1 else 0 end)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hostile.yaml missing surviving measure %q:\n%s", want, got)
		}
	}
	// percentile (no Databricks equivalent), an omitted agg, and an omitted
	// expr must all be skipped and named in a note instead of emitted.
	for _, hostileName := range []string{"p50", "no_agg", "no_expr"} {
		if strings.Contains(got, "name: "+hostileName) {
			t.Errorf("hostile.yaml: measure %q has an unrenderable aggregation and must not be emitted:\n%s", hostileName, got)
		}
		if !strings.Contains(got, hostileName) {
			t.Errorf("hostile.yaml: skipped measure %q must be named in a note:\n%s", hostileName, got)
		}
	}

	// Structural invariant, generic across every view and measure: no matter
	// what a future model throws at the emitter, every surviving
	// measures[].expr must be built only from known-safe aggregate calls
	// (sum/count/avg/min/max/median), possibly combined by arithmetic. This
	// is the guard against the defect class itself, not just today's five
	// reproductions of it.
	for name, content := range files {
		assertMeasureExprsAreSafe(t, name, content)
	}
	for name, content := range emitDbx(t, dbxTestModel()) {
		assertMeasureExprsAreSafe(t, name, content)
	}
}

// dbxSafeCallRe recognizes one known-safe aggregate call (sum/count/avg/min/
// max/median) with a non-empty, non-nested argument. Deliberately
// reimplemented independently of dbxValidMeasureExpr (rather than calling
// it), so this test catches a regression in either function, not only in
// whichever one happens to run first.
var dbxSafeCallRe = regexp.MustCompile(`(?i)\b(sum|count|avg|min|max|median)\([^()]+\)`)

// assertMeasureExprsAreSafe parses every measures[].expr out of a rendered
// metric-view YAML file and asserts each is a known-aggregate call, or a
// derived arithmetic expression composed only of such calls, literals and
// operators: strip every recognized call out of the expr and require nothing
// but arithmetic/parens/whitespace to remain, which catches an unknown
// function call (e.g. percentile(x)) hiding alongside a legitimate one.
func assertMeasureExprsAreSafe(t *testing.T, file, content string) {
	t.Helper()
	var doc struct {
		Measures []struct {
			Name string `yaml:"name"`
			Expr string `yaml:"expr"`
		} `yaml:"measures"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("%s: unmarshal: %v", file, err)
	}
	if len(doc.Measures) == 0 {
		t.Errorf("%s: expected at least one measure (a metric view requires >=1)", file)
	}
	for _, ms := range doc.Measures {
		if !dbxSafeCallRe.MatchString(ms.Expr) {
			t.Errorf("%s: measure %q has expr %q with no known-aggregate call", file, ms.Name, ms.Expr)
			continue
		}
		rest := dbxSafeCallRe.ReplaceAllString(ms.Expr, "")
		if regexp.MustCompile(`[A-Za-z_]`).MatchString(rest) {
			t.Errorf("%s: measure %q expr %q contains something other than known aggregate calls (leftover %q)", file, ms.Name, ms.Expr, rest)
		}
	}
}

// TestDatabricksMetricViewModelNotesReachEveryView is Fix 3: buildView reads
// only m.Relationships from the model, dropping m.Notes entirely. Every
// sibling target (cortex via custom_instructions, supersimple, snowflake-
// semantic-view, nao-yaml, nao-context-rules) folds m.Notes into its emitted
// artifact, so a model-level note semglot could not transpile must reach
// Genie the same way it reaches Cortex Analyst. Every emitted view carries
// every model-level note (rather than one view per note by table-name
// mention): simpler, and it means a note is never silently absent from the
// one view a user happens to open.
func TestDatabricksMetricViewModelNotesReachEveryView(t *testing.T) {
	m := dbxTestModel()
	m.Notes = []string{`measure "bogus" not found in the parsed semantic models`}
	files := emitDbx(t, m)
	if len(files) == 0 {
		t.Fatal("expected at least one emitted view")
	}
	for name, content := range files {
		if !strings.Contains(content, "bogus") {
			t.Errorf("%s: model-level note must be folded into the view comment, got:\n%s", name, content)
		}
	}
}

func keysOfDbx(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestDatabricksMetricViewZeroFieldsSkipped pins the one path that drops a
// whole table. Measures are built before fields and their names seed the
// field-dedup set, because Databricks rejects a view whose measure and
// dimension names collide. A table whose every dimension is suppressed that
// way ends up with no fields, and a metric view requires at least one
// dimension, so no view can be formed and no file is written.
//
// The drop used to be silent: notes reach a view's `comment`, and here there
// is no view to carry one. It is now surfaced as an Emit warning instead —
// this test pins both the missing file and the warning naming the table.
func TestDatabricksMetricViewZeroFieldsSkipped(t *testing.T) {
	// pings: its only dimension collides with its only measure, so the view
	// cannot be formed. orders: an ordinary table, emitted as usual, proving
	// the skip is scoped to the offending table and not the whole model.
	m := &ir.Model{Tables: []ir.Table{
		{
			Name:       "pings",
			Dimensions: []ir.Field{{Name: "pings", Expr: "pings"}},
			Metrics: []ir.Metric{
				{Name: "pings", Def: ir.Agg{Func: "count", Table: "pings", Arg: ir.Col{Name: "ping_id"}}},
			},
		},
		{
			Name:       "orders",
			Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
			Metrics: []ir.Metric{
				{Name: "orders", Def: ir.Agg{Func: "count", Table: "orders", Arg: ir.Col{Name: "order_id"}}},
			},
		},
	}}
	files, warnings := emitDbxW(t, m)
	if _, ok := files["pings.yaml"]; ok {
		t.Errorf("pings has no emittable dimension, so no valid metric view exists; got:\n%s", files["pings.yaml"])
	}
	if _, ok := files["orders.yaml"]; !ok {
		t.Errorf("the skip must be scoped to the offending table; orders.yaml missing, got %v", keysOfDbx(files))
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "pings") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a warning naming the skipped table %q, got: %v", "pings", warnings)
	}
}

// TestDatabricksMetricViewFieldSurvivesWhenNamesDiffer is the control for the
// test above: the same shape, with the dimension renamed so it no longer
// collides, must produce a view. Without this, that test would still pass if
// the emitter started dropping every table for an unrelated reason.
func TestDatabricksMetricViewFieldSurvivesWhenNamesDiffer(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "pings",
		Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
		Metrics: []ir.Metric{
			{Name: "pings", Def: ir.Agg{Func: "count", Table: "pings", Arg: ir.Col{Name: "ping_id"}}},
		},
	}}}
	got, ok := emitDbx(t, m)["pings.yaml"]
	if !ok {
		t.Fatal("pings.yaml should be emitted when the dimension does not collide with a measure")
	}
	for _, want := range []string{"name: region", "name: pings", "expr: count(ping_id)"} {
		if !strings.Contains(got, want) {
			t.Errorf("pings.yaml missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestDatabricksMetricViewQuerySourceWarnedOnce pins the dedup. Every view
// resolves the physical source of every table it joins as well as its own, so
// one unusable source is reached once per referencing view (and twice more for
// a role-playing dimension, which joins the same table twice). The warning
// names the table and the source, so repeats carry nothing new — the CLI must
// print it once.
func TestDatabricksMetricViewQuerySourceWarnedOnce(t *testing.T) {
	source := "SELECT * FROM prod.raw.customer WHERE deleted = false"
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name:       "orders",
				Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
			},
			{
				Name:       "customer",
				Source:     source,
				Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
			},
		},
		Relationships: []ir.Relationship{
			// A role-playing dimension: orders references customer twice, so
			// buildView(orders) resolves customer's source twice on its own,
			// and buildView(customer) resolves it a third time.
			{Left: "orders", Right: "customer", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
			{Left: "orders", Right: "customer", Columns: []ir.ColumnPair{{Left: "billing_customer_id", Right: "customer_id"}}},
		},
	}
	files, warnings := emitDbxW(t, m)
	n := 0
	for _, w := range warnings {
		if strings.Contains(w, source) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("query-source warning emitted %d times, want exactly 1: %v", n, warnings)
	}
	// Each artifact still carries it, since a reader of one file must see why
	// that file's source was reconstructed — and once per file, not twice.
	for _, name := range []string{"orders.yaml", "customer.yaml"} {
		got, ok := files[name]
		if !ok {
			t.Fatalf("expected %s, got %v", name, keysOfDbx(files))
		}
		if c := strings.Count(got, source); c != 1 {
			t.Errorf("%s carries the query-source note %d times, want 1:\n%s", name, c, got)
		}
	}
}

// A metric view's `joins:` and its `measures:` must agree about what each
// joined relation is CALLED. renderSQL qualifies a cross-table column with the
// IR TABLE name ("count(distinct customer.c_customer_sk)"); the view's only
// relations are `source` and the join aliases. That reconciliation used to be
// an accident — the alias was strings.ToLower(r.Right), byte-identical to the
// qualifier — and preferring a DECLARED relationship name broke it silently:
// the join became `store_sales_to_customer` while the measure still said
// `customer.…`, and Databricks rejects the ENTIRE view for the dangling
// relation. The tests below pin the coupling from both ends.

// dbxMeasureQualifiers returns every `<relation>.` qualifier appearing in a
// view's measure expressions, and the view's join names, so a test can assert
// the first is a subset of the second.
func dbxMeasureQualifiers(t *testing.T, file string) (quals, joins []string) {
	t.Helper()
	var v struct {
		Joins []struct {
			Name string `yaml:"name"`
		} `yaml:"joins"`
		Measures []struct {
			Expr string `yaml:"expr"`
		} `yaml:"measures"`
	}
	if err := yaml.Unmarshal([]byte(file), &v); err != nil {
		t.Fatalf("parse emitted view: %v\n%s", err, file)
	}
	for _, j := range v.Joins {
		joins = append(joins, j.Name)
	}
	re := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*)\.[A-Za-z_]`)
	for _, ms := range v.Measures {
		for _, m := range re.FindAllStringSubmatch(ms.Expr, -1) {
			quals = append(quals, m[1])
		}
	}
	return quals, joins
}

// assertMeasureQualifiersResolve is the invariant itself: every relation a
// measure expression names must be one this view actually declares. `source`
// is the base relation's own alias and is always valid.
func assertMeasureQualifiersResolve(t *testing.T, name, file string) {
	t.Helper()
	quals, joins := dbxMeasureQualifiers(t, file)
	known := map[string]bool{"source": true}
	for _, j := range joins {
		known[j] = true
	}
	for _, q := range quals {
		if !known[q] {
			t.Errorf("%s: measure expression references relation %q, but the view declares only %v; Databricks rejects the whole view for this:\n%s",
				name, q, append([]string{"source"}, joins...), file)
		}
	}
}

// dbxCrossTableModel is a fact with a metric whose denominator counts a column
// on the JOINED table — the shape of TPC-DS's customer_lifetime_value, which is
// where this regression surfaced.
func dbxCrossTableModel(relName string) *ir.Model {
	return &ir.Model{
		Tables: []ir.Table{
			{
				Name:       "store_sales",
				Dimensions: []ir.Field{{Name: "ss_customer_sk", Expr: "ss_customer_sk"}},
				Metrics: []ir.Metric{{
					Name: "clv",
					Def: ir.Binary{
						Op:   "/",
						Left: ir.Agg{Func: "sum", Table: "store_sales", Arg: ir.Col{Table: "store_sales", Name: "ss_ext_sales_price"}},
						Right: ir.Agg{Func: "count_distinct", Table: "customer",
							Arg: ir.Col{Table: "customer", Name: "c_customer_sk"}},
					},
				}},
			},
			{
				Name:       "customer",
				Dimensions: []ir.Field{{Name: "c_customer_sk", Expr: "c_customer_sk"}},
			},
		},
		Relationships: []ir.Relationship{{
			Name: relName, Left: "store_sales", Right: "customer",
			Columns: []ir.ColumnPair{{Left: "ss_customer_sk", Right: "c_customer_sk"}},
		}},
	}
}

// TestDatabricksCrossTableQualifierFollowsJoinAlias: whatever the join ends up
// called, the measure expression must name that same alias. Run with a
// declared name (the case that broke) and without one (the case that used to
// work by accident), so the test fails if the two ever diverge again.
func TestDatabricksCrossTableQualifierFollowsJoinAlias(t *testing.T) {
	for _, tc := range []struct {
		declared  string
		wantAlias string
	}{
		{"store_sales_to_customer", "store_sales_to_customer"},
		{"", "customer"},
	} {
		name := tc.declared
		if name == "" {
			name = "(anonymous)"
		}
		t.Run(name, func(t *testing.T) {
			files := emitDbx(t, dbxCrossTableModel(tc.declared))
			got, ok := files["store_sales.yaml"]
			if !ok {
				t.Fatalf("expected store_sales.yaml, got %v", keysOfDbx(files))
			}
			assertMeasureQualifiersResolve(t, "store_sales.yaml", got)
			if !strings.Contains(got, "name: "+tc.wantAlias+"\n") {
				t.Errorf("want join named %q:\n%s", tc.wantAlias, got)
			}
			if !strings.Contains(got, "count(distinct "+tc.wantAlias+".c_customer_sk)") {
				t.Errorf("measure must qualify the joined column with the join alias %q:\n%s", tc.wantAlias, got)
			}
			// The IR table name must not survive as a qualifier once it differs
			// from the alias — that is exactly the dangling relation.
			if tc.wantAlias != "customer" && strings.Contains(got, "customer.c_customer_sk)") &&
				!strings.Contains(got, tc.wantAlias+".c_customer_sk)") {
				t.Errorf("measure still qualifies with the IR table name:\n%s", got)
			}
		})
	}
}

// TestDatabricksUnjoinableQualifierDegrades: a metric qualifying a table this
// view does not join has no valid rendering at all. It must be dropped and
// noted, never emitted with a relation the view does not declare — one bad
// measure takes every other measure, dimension and join of the file with it.
func TestDatabricksUnjoinableQualifierDegrades(t *testing.T) {
	m := dbxCrossTableModel("store_sales_to_customer")
	m.Relationships = nil // the metric still references customer; nothing joins it now
	files, warnings := emitDbxW(t, m)
	got := files["store_sales.yaml"]
	// assertMeasureQualifiersResolve is the check that matters here: it looks
	// at the measure EXPRESSIONS only. The note itself legitimately quotes the
	// rejected expression (that is how a reader knows what was dropped), so a
	// whole-file substring check would match its own explanation.
	assertMeasureQualifiersResolve(t, "store_sales.yaml", got)
	if strings.Contains(got, "expr: sum(store_sales.ss_ext_sales_price)") {
		t.Errorf("the degraded metric must not be emitted as a measure at all:\n%s", got)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "clv") && strings.Contains(w, "customer") && strings.Contains(w, "does not join") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note naming the metric and the unjoinable relation; got %v", warnings)
	}
	// The view still stands: the degraded metric leaves it measure-less, so the
	// synthesised row count keeps it valid rather than dropping the table.
	if !strings.Contains(got, "name: row_count") {
		t.Errorf("want the synthesised row-count measure:\n%s", got)
	}
}

// TestDatabricksRolePlayingQualifierIsAmbiguous: two joins to the same table
// (ship-to vs bill-to) mean a bare `customer.x` qualifier names neither. The
// old alias-equals-table-name accident did not hold here either — both joins
// are suffixed — so this closes a case that was already broken, silently.
func TestDatabricksRolePlayingQualifierIsAmbiguous(t *testing.T) {
	m := dbxCrossTableModel("")
	m.Relationships = append(m.Relationships, ir.Relationship{
		Left: "store_sales", Right: "customer",
		Columns: []ir.ColumnPair{{Left: "ss_bill_customer_sk", Right: "c_customer_sk"}},
	})
	files, warnings := emitDbxW(t, m)
	assertMeasureQualifiersResolve(t, "store_sales.yaml", files["store_sales.yaml"])
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "clv") && strings.Contains(w, "exactly once") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note that the qualifier cannot be resolved to one join; got %v", warnings)
	}
}

// TestDatabricksEveryEmittedViewResolvesItsQualifiers sweeps the shared test
// model, so any future change that reintroduces a dangling qualifier anywhere
// in the fixture fails here rather than in a Databricks deploy log.
func TestDatabricksEveryEmittedViewResolvesItsQualifiers(t *testing.T) {
	for name, file := range emitDbx(t, dbxTestModel()) {
		assertMeasureQualifiersResolve(t, name, file)
	}
}
