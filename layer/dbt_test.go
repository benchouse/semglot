package layer

import (
	"reflect"
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
