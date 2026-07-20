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
