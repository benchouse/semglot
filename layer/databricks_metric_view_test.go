package layer

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
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "status", Expr: "status", Description: "Order status"}},
		Metrics: []ir.Metric{
			{Name: "revenue", Label: "Revenue", Description: "Gross revenue",
				Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
			{Name: "order_count",
				Def: ir.Agg{Func: "count_distinct", Table: "orders", Arg: ir.Col{Name: "order_id"}}},
			{Name: "aov", // same-grain derived: revenue / order_count
				Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "order_count"}}},
		},
	}
	customers := ir.Table{
		Name:       "customers",
		Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
	}
	lines := ir.Table{
		Name: "lines",
		Metrics: []ir.Metric{
			{Name: "units", Def: ir.Agg{Func: "sum", Table: "lines", Arg: ir.Col{Name: "qty"}}},
			{Name: "units_per_order", // cross-grain: references orders' order_count
				Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "units"}, Right: ir.Ref{Metric: "order_count"}}},
		},
	}
	return &ir.Model{
		Tables: []ir.Table{orders, customers, lines},
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
		"source: analytics.main.customers", // the join source
		"expr: customers.region",           // joined dimension, prefixed
		"expr: sum(amount)",                 // simple metric lowered (renderSQL is lowercase)
		"sum(amount) / count(distinct order_id)", // same-grain derived, inlined
	} {
		if !strings.Contains(got, want) {
			t.Errorf("orders.yaml missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestDatabricksMetricViewNoDimensionFile(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	if _, ok := files["customers.yaml"]; ok {
		t.Error("customers is a pure dimension (no metrics); should not get its own view")
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
