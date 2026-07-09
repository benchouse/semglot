package layer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

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
					{Name: "net_revenue", Description: "Net booked revenue.", Expr: "sum(fct_orders.order_net_booked)"},
					{Name: "orders", Expr: "count(distinct fct_orders.order_id)"},
					{Name: "aov", Description: "Net revenue / orders.", Expr: "sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)"},
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
	metrics := map[string]string{}
	for _, mt := range got.Tables[0].Metrics {
		metrics[mt.Name] = mt.Expr
	}
	if want := "sum(case when fct_orders.is_refunded then 1 else 0 end)"; metrics["refunded_orders"] != want {
		t.Fatalf("refunded_orders = %q, want %q", metrics["refunded_orders"], want)
	}
	// the bare `status` is qualified; the string literal 'status' is not.
	if want := "sum(case when fct_orders.status = 'status' then 1 else 0 end)"; metrics["status_match"] != want {
		t.Fatalf("status_match = %q, want %q", metrics["status_match"], want)
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
