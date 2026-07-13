package layer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestDBTParseSkipsTimeSpine(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_scout")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tb := range got.Tables {
		if strings.EqualFold(tb.Name, "metricflow_time_spine") {
			t.Fatalf("time-spine model must be skipped, but was emitted as table %q", tb.Name)
		}
	}
}

func TestDBTParseSharedPrimaryEntityJoinsAll(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_scout")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// fct_orders' foreign "customer" entity must join to BOTH tables that declare
	// a primary "customer" (dim_customer AND fct_customer_ltv) — not just the last.
	targets := map[string]bool{}
	for _, r := range got.Relationships {
		if r.Left == "fct_orders" {
			targets[r.Right] = true
		}
	}
	for _, want := range []string{"dim_customer", "fct_customer_ltv"} {
		if !targets[want] {
			t.Errorf("missing fct_orders -> %s relationship (got %v)", want, targets)
		}
	}
}

func TestDBTParseTimeDimEntityDedup(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_timedim_entity")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(got.Tables))
	}
	tb := got.Tables[0]
	// event_date is declared as BOTH a foreign entity and the time dimension; it
	// must land only in TimeDimensions, never duplicated into Dimensions.
	var inTime, inDim int
	for _, d := range tb.TimeDimensions {
		if d.Expr == "event_date" {
			inTime++
		}
	}
	for _, d := range tb.Dimensions {
		if d.Expr == "event_date" {
			inDim++
		}
	}
	if inTime != 1 {
		t.Errorf("event_date in TimeDimensions = %d, want 1", inTime)
	}
	if inDim != 0 {
		t.Errorf("event_date leaked into Dimensions %d time(s), want 0", inDim)
	}
}

func TestDBTParse(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := &ir.Model{
		Tables: []ir.Table{
			{
				Name:        "fct_orders",
				Description: "Order-grain finance fact. One row per order.",
				PrimaryKey:  []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id"},
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "is_refunded", Expr: "is_refunded"},
				},
				TimeDimensions: []ir.Field{
					{Name: "order_date", Expr: "order_date"},
				},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "order_net_booked_amount", Expr: "order_net_booked"}, Agg: "sum"},
					{Field: ir.Field{Name: "orders_count", Expr: "order_id"}, Agg: "count_distinct"},
				},
				Metrics: []ir.Metric{
					{Name: "net_revenue", Label: "Net revenue", Description: "Net booked revenue.",
						Def: ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_net_booked"}}},
					{Name: "orders", Label: "Orders",
						Def: ir.Agg{Func: "count_distinct", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_id"}}},
					{Name: "aov", Label: "Average order value", Description: "Net revenue / orders.",
						Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "orders"}}},
				},
			},
			{
				Name:        "dim_customer",
				Description: "Customer dimension.",
				PrimaryKey:  []string{"customer_sk"},
				Dimensions: []ir.Field{
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "customer_segment", Expr: "customer_segment"},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_sk", Right: "customer_sk"}}},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IR mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// A project with only classic model properties (no semantic layer): every
// documented column becomes a dimension carrying its real type and description,
// primary keys come from constraints, relationships from tests.
func TestDBTParseModelsOnly(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_models_only")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := &ir.Model{
		Tables: []ir.Table{
			{
				Name:        "dim_customer",
				Description: "Customer dimension.",
				PrimaryKey:  []string{"customer_sk"},
				Dimensions: []ir.Field{
					{Name: "customer_sk", Expr: "customer_sk", Description: "Surrogate key.", DataType: "number"},
					{Name: "customer_segment", Expr: "customer_segment", Description: "Marketing segment.", DataType: "varchar"},
					{Name: "accepts_marketing", Expr: "accepts_marketing", Description: "Opted in to marketing.", DataType: "boolean"},
				},
			},
			{
				Name:        "fct_orders",
				Description: "Orders fact.",
				PrimaryKey:  []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id", Description: "Order id.", DataType: "number"},
					{Name: "customer_sk", Expr: "customer_sk", Description: "Customer FK.", DataType: "number"},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_sk", Right: "customer_sk"}}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IR mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// A value measure (sum) inherits its column's description; a count measure does
// not, because the column description describes the value, not the cardinality.
func TestDBTParseCountMeasureDropsColumnDescription(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_count_desc")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	desc := map[string]string{}
	for _, m := range got.Tables[0].Measures {
		desc[m.Name] = m.Description
	}
	if desc["order_gross_amount"] != "Gross revenue." {
		t.Fatalf("order_gross_amount description = %q, want %q", desc["order_gross_amount"], "Gross revenue.")
	}
	if desc["orders_count"] != "" {
		t.Fatalf("orders_count description = %q, want empty (count must not inherit the id column's description)", desc["orders_count"])
	}
}

// A metric referencing an unknown measure, or an unsupported metric type, is
// NOT silently attached to a table — it becomes a passthrough note.
func TestDBTParseUnresolvedMetrics(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_unresolved")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got.Tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(got.Tables))
	}
	var attached []string
	for _, mt := range got.Tables[0].Metrics {
		attached = append(attached, mt.Name)
	}
	if len(attached) != 1 || attached[0] != "orders" {
		t.Fatalf("attached metrics = %v, want [orders] (nothing mis-attached to Tables[0])", attached)
	}
	if len(got.Notes) != 2 {
		t.Fatalf("notes = %d %v, want 2", len(got.Notes), got.Notes)
	}
	joined := strings.Join(got.Notes, "\n")
	for _, want := range []string{"mystery", "not_a_measure", "growth", "derived"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("notes missing %q:\n%s", want, joined)
		}
	}
}

// Compound measure expressions get their column references qualified via the
// SQL lexer; string literals that happen to equal a column name are left alone.
func TestDBTParseCaseExpr(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_case_expr")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defs := map[string]ir.Expr{}
	for _, mt := range got.Tables[0].Metrics {
		defs[mt.Name] = mt.Def
	}
	resolve := func(n string) (ir.Expr, bool) { e, ok := defs[n]; return e, ok }
	metrics := map[string]string{}
	for _, mt := range got.Tables[0].Metrics {
		metrics[mt.Name] = renderSQL(mt.Def, resolve)
	}
	if want := "sum(case when fct_orders.is_refunded then 1 else 0 end)"; metrics["refunded_orders"] != want {
		t.Fatalf("refunded_orders = %q, want %q", metrics["refunded_orders"], want)
	}
	// the bare `status` is qualified; the string literal 'status' is not.
	if want := "sum(case when fct_orders.status = 'status' then 1 else 0 end)"; metrics["status_match"] != want {
		t.Fatalf("status_match = %q, want %q", metrics["status_match"], want)
	}
}

// qualifyExpr prefixes only known-column identifiers with the table; keywords,
// non-column identifiers, and string literals (even ones equal to a column name)
// are left alone.
func TestQualifyExpr(t *testing.T) {
	cols := map[string]bool{"is_refunded": true, "order_id": true, "status": true}
	if got := qualifyExpr("fct_orders", cols, "order_id"); got != "fct_orders.order_id" {
		t.Fatalf("bare column: got %q", got)
	}
	if got := qualifyExpr("fct_orders", cols, "case when is_refunded then 1 else 0 end"); got != "case when fct_orders.is_refunded then 1 else 0 end" {
		t.Fatalf("case expr: got %q", got)
	}
	// 'status' literal untouched; column status qualified; non-column x and the
	// function name coalesce left alone.
	got := qualifyExpr("fct_orders", cols, "case when status = 'status' then coalesce(x, 0) else 0 end")
	if want := "case when fct_orders.status = 'status' then coalesce(x, 0) else 0 end"; got != want {
		t.Fatalf("mixed: got %q, want %q", got, want)
	}
}

// When both sources describe the same model, model properties supply the table
// description, column descriptions and real data types; the semantic layer
// supplies roles (dimension vs measure) and aggregations.
func TestDBTParseMerge(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_merge")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := &ir.Model{
		Tables: []ir.Table{
			{
				Name:        "fct_orders",
				Description: "Orders (from model properties).", // models: wins over semantic
				PrimaryKey:  []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id", Description: "Order surrogate key.", DataType: "number"},
					{Name: "is_refunded", Expr: "is_refunded", Description: "Whether refunded.", DataType: "boolean"},
				},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "order_gross_amount", Expr: "order_gross", Description: "Gross revenue.", DataType: "number"}, Agg: "sum"},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IR mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

// A metric's dbt label surfaces as ir.Metric.Label, and a semantic model's
// defaults.agg_time_dimension surfaces as ir.Table.Grain.
func TestDBTParseLabelAndGrain(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_label_grain")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tables[0].Grain != "order_date" {
		t.Fatalf("grain = %q, want order_date", got.Tables[0].Grain)
	}
	m := got.Tables[0].Metrics[0]
	if m.Label != "Net revenue" {
		t.Fatalf("label = %q, want 'Net revenue'", m.Label)
	}
	if m.Grain != "order_date" {
		t.Fatalf("metric grain = %q, want order_date", m.Grain)
	}
	wantDef := ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_net_booked"}}
	if !reflect.DeepEqual(m.Def, ir.Expr(wantDef)) {
		t.Fatalf("Def = %#v, want %#v", m.Def, wantDef)
	}
}
