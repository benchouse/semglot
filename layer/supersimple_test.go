package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestSupersimpleEmit(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", Description: "Orders.",
				PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id", DataType: "number"},
					{Name: "is_refunded", Expr: "is_refunded", DataType: "boolean"},
				},
				TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date"}},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "order_net_booked_amount", Expr: "order_net_booked", DataType: "float"}, Agg: "sum"},
				},
				Metrics: []ir.Metric{
					{Name: "net_revenue", Label: "Net revenue", Description: "Net booked revenue.", Kind: "simple", Agg: "sum", Table: "fct_orders", Column: "order_net_booked"},
					{Name: "orders", Label: "Orders", Kind: "simple", Agg: "count_distinct", Table: "fct_orders", Column: "order_id"},
					{Name: "refund_rate", Kind: "ratio", Table: "fct_orders", Numerator: "x", Denominator: "y"},
				},
			},
			{
				Name: "dim_customer", Description: "Customers.",
				PrimaryKey: []string{"customer_sk"},
				Dimensions: []ir.Field{{Name: "customer_sk", Expr: "customer_sk", DataType: "number"}},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_sk", Right: "customer_sk"}}},
		},
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	orders := readFile(t, filepath.Join(dir, "FCT_ORDERS.yaml"))
	for _, want := range []string{
		"# yaml-language-server:",
		"FCT_ORDERS:",
		"name: Orders",
		"table: MAIN.FCT_ORDERS",
		"type: Boolean", // is_refunded
		"type: Date",    // order_date
		"type: Float",   // order_net_booked
		"type: Number",  // order_id
		"name: Net revenue",
		"description: Net booked revenue.", // metric description passes through
		"type: sum",
		"key: ORDER_NET_BOOKED",
		"type: count_distinct", // schema uses snake_case, not countDistinct
		"key: ORDER_ID",
	} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}

	// hasMany relation lives on the PARENT (dim_customer).
	cust := readFile(t, filepath.Join(dir, "DIM_CUSTOMER.yaml"))
	for _, want := range []string{"relations:", "type: hasMany", "model_id: FCT_ORDERS", "join_key: CUSTOMER_SK"} {
		if !strings.Contains(cust, want) {
			t.Fatalf("DIM_CUSTOMER.yaml missing %q:\n%s", want, cust)
		}
	}

	// the ratio metric is omitted and reported via Notes.
	joined := strings.Join(m.Notes, "\n")
	if !strings.Contains(joined, "refund_rate") || !strings.Contains(joined, "not representable in supersimple") {
		t.Fatalf("expected a skip note for refund_rate, got: %v", m.Notes)
	}
	if strings.Contains(orders, "countDistinct") {
		t.Fatalf("aggregation type must be snake_case count_distinct, not camelCase:\n%s", orders)
	}
	if strings.Contains(orders, "refund_rate") {
		t.Fatalf("ratio metric should not be emitted:\n%s", orders)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
