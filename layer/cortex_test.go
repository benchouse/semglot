package layer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// sampleIR mirrors the dbt fixture's expected IR so emit is tested in isolation.
func sampleIR() *ir.Model {
	return &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", Description: "Order-grain finance fact. One row per order.",
				PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id"},
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "is_refunded", Expr: "is_refunded"},
				},
				TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date"}},
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
				Name: "dim_customer", Description: "Customer dimension.",
				PrimaryKey: []string{"customer_sk"},
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
}

func TestCortexEmit(t *testing.T) {
	dir := t.TempDir()
	e := cortex{Database: "ANALYTICS", Schema: "MAIN", ModelName: "eval_marts"}
	if err := e.Emit(sampleIR(), dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "semantic_model.yaml"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	golden := "testdata/cortex/semantic_model.golden.yaml"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("cortex output != golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
