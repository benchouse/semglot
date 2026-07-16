package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
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
					{Name: "net_revenue", Label: "Net revenue", Description: "Net booked revenue.",
						Def: ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_net_booked"}}},
					{Name: "orders", Label: "Orders",
						Def: ir.Agg{Func: "count_distinct", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_id"}}},
					{Name: "refunded_orders", Label: "Refunded orders",
						Def: ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Raw{SQL: "case when is_refunded then 1 else 0 end", Columns: []string{"is_refunded", "order_date", "order_id", "order_net_booked"}}}},
					{Name: "refund_rate", Label: "Refund rate",
						Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "refunded_orders"}, Right: ir.Ref{Metric: "orders"}}},
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
	} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}

	for _, want := range []string{
		"name: Net revenue",
		"description: Net booked revenue.",
		"type: sum",
		"key: ORDER_NET_BOOKED", // net_revenue simple metric aggregates the bare column
		"type: count_distinct",
		"key: ORDER_ID",
		// compound measure -> synthesized property.sql + a sum metric over it
		"sql: case when {is_refunded} then 1 else 0 end",
		"key: REFUNDED_ORDERS",
		// same-table ratio -> operations pipeline (terminal aggregation is sum over
		// the single whole-set-grouped row; supersimple validate rejects first+property)
		"operation: groupAggregate",
		"operation: deriveField",
		`expression: prop("_num") / prop("_den")`,
	} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}
	if strings.Contains(orders, "countDistinct") {
		t.Fatalf("aggregation type must be snake_case count_distinct:\n%s", orders)
	}

	// hasMany relation lives on the PARENT (dim_customer).
	cust := readFile(t, filepath.Join(dir, "DIM_CUSTOMER.yaml"))
	for _, want := range []string{"relations:", "type: hasMany", "model_id: FCT_ORDERS", "join_key: CUSTOMER_SK"} {
		if !strings.Contains(cust, want) {
			t.Fatalf("DIM_CUSTOMER.yaml missing %q:\n%s", want, cust)
		}
	}
}

func TestToPropertySQL(t *testing.T) {
	cols := map[string]bool{"is_refunded": true, "status": true, "name": true}
	got := toPropertySQL("case when is_refunded then 1 else 0 end", cols)
	if want := "case when {is_refunded} then 1 else 0 end"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// bare column wrapped; the string literal 'status' and keywords are not.
	got = toPropertySQL("case when status = 'status' then 1 else 0 end", cols)
	if want := "case when {status} = 'status' then 1 else 0 end"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// a doubled-quote escape inside a string must stay intact; only the real
	// column beside it is wrapped (exercises the lexer's escape handling end-to-end).
	got = toPropertySQL("case when name = 'O''Brien' then 1 else 0 end", cols)
	if want := "case when {name} = 'O''Brien' then 1 else 0 end"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A compound-measure metric whose name collides with a physical column must not
// clobber that column's property — it gets a distinct suffixed key.
func TestSupersimpleCompoundKeyNoClobber(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "t", PrimaryKey: []string{"id"},
		Dimensions: []ir.Field{
			{Name: "id", Expr: "id", DataType: "number"},
			{Name: "flag", Expr: "flag", DataType: "boolean"}, // physical -> property FLAG (Boolean)
		},
		Metrics: []ir.Metric{
			// compound metric named "flag" would synthesize key FLAG, colliding.
			{Name: "flag", Def: ir.Agg{Func: "sum", Table: "t", Arg: ir.Raw{SQL: "case when flag then 1 else 0 end", Columns: []string{"flag", "id"}}}},
		},
	}}}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatal(err)
	}
	out := readFile(t, filepath.Join(dir, "T.yaml"))
	if !strings.Contains(out, "type: Boolean") {
		t.Fatalf("physical FLAG property was clobbered (no Boolean type left):\n%s", out)
	}
	if !strings.Contains(out, "FLAG_EXPR") || !strings.Contains(out, "sql: case when {flag} then 1 else 0 end") {
		t.Fatalf("expected suffixed synthesized property FLAG_EXPR with rewritten sql:\n%s", out)
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

func TestFindParentRelation(t *testing.T) {
	m := &ir.Model{Relationships: []ir.Relationship{
		{Left: "fct_order_lines", Right: "fct_orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
	}}
	// either argument order finds the same parent/child/relKey.
	for _, pair := range [][2]string{{"fct_order_lines", "fct_orders"}, {"fct_orders", "fct_order_lines"}} {
		parent, relKey, child, ok := findParentRelation(m, pair[0], pair[1])
		if !ok || parent != "fct_orders" || child != "fct_order_lines" || relKey != "order_lines" {
			t.Fatalf("%v: got parent=%q relKey=%q child=%q ok=%v", pair, parent, relKey, child, ok)
		}
	}
	if _, _, _, ok := findParentRelation(m, "fct_orders", "dim_product"); ok {
		t.Fatal("unrelated tables should return ok=false")
	}
}

func TestCrossRatioMetric(t *testing.T) {
	// units_per_order = units_sold(child sum QUANTITY) / orders(base count_distinct ORDER_ID)
	sm := crossRatioMetric("FCT_ORDERS", "units_per_order", "order_lines", "Units per order", "u/o",
		crossOperand{onBase: false, aggType: "sum", key: "QUANTITY"},
		crossOperand{onBase: true, aggType: "count_distinct", key: "ORDER_ID"})
	b, err := yaml.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"model_id: FCT_ORDERS",
		"operation: relationAggregate",
		"key: order_lines", // relation key
		"key: QUANTITY",    // child operand pulled across the relation
		"operation: groupAggregate",
		"type: count_distinct", // parent operand direct
		"key: ORDER_ID",
		"operation: deriveField",
		`prop("_num") / prop("_den")`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("crossRatioMetric missing %q:\n%s", want, out)
		}
	}
}

// The other operand arrangement: numerator on the base (parent), denominator on
// the child — the denominator is the one pulled via relationAggregate (_den_rel),
// and the division stays numerator/denominator.
func TestCrossRatioMetricBaseNumerator(t *testing.T) {
	sm := crossRatioMetric("FCT_ORDERS", "m", "order_lines", "M", "",
		crossOperand{onBase: true, aggType: "count_distinct", key: "ORDER_ID"}, // numerator, base
		crossOperand{onBase: false, aggType: "sum", key: "QUANTITY"})           // denominator, child
	b, err := yaml.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{"operation: relationAggregate", "_den_rel", "key: QUANTITY", `prop("_num") / prop("_den")`} {
		if !strings.Contains(out, want) {
			t.Fatalf("base-numerator arrangement missing %q:\n%s", want, out)
		}
	}
}

// A cross-table ratio whose CHILD operand does not compose under an outer sum
// (count_distinct) is deferred to NOTES.md, not emitted.
func TestSupersimpleCrossTableNonComposingChildDeferred(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id", DataType: "number"}},
				Metrics:    []ir.Metric{{Name: "orders", Def: ir.Agg{Func: "count_distinct", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_id"}}}},
			},
			{
				Name: "fct_order_lines", PrimaryKey: []string{"line_id"},
				Dimensions: []ir.Field{{Name: "line_id", Expr: "line_id", DataType: "number"}, {Name: "product_id", Expr: "product_id", DataType: "number"}},
				Metrics: []ir.Metric{
					{Name: "distinct_products", Def: ir.Agg{Func: "count_distinct", Table: "fct_order_lines", Arg: ir.Col{Table: "fct_order_lines", Name: "product_id"}}},
					{Name: "products_per_order", Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "distinct_products"}, Right: ir.Ref{Metric: "orders"}}},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_order_lines", Right: "fct_orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
		},
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatal(err)
	}
	notes := readFile(t, filepath.Join(dir, "NOTES.md"))
	if !strings.Contains(notes, "products_per_order") || !strings.Contains(notes, "does not compose") {
		t.Fatalf("expected products_per_order deferral note:\n%s", notes)
	}
	for _, f := range []string{"FCT_ORDERS.yaml", "FCT_ORDER_LINES.yaml"} {
		if strings.Contains(readFile(t, filepath.Join(dir, f)), "products_per_order") {
			t.Fatalf("products_per_order should not be emitted, found in %s", f)
		}
	}
}

func TestSupersimpleCrossTableRatioEmit(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id", DataType: "number"}},
				Metrics: []ir.Metric{
					{Name: "orders", Def: ir.Agg{Func: "count_distinct", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_id"}}},
				},
			},
			{
				Name: "fct_order_lines", PrimaryKey: []string{"line_id"},
				Dimensions: []ir.Field{{Name: "line_id", Expr: "line_id", DataType: "number"}},
				Measures:   []ir.Measure{{Field: ir.Field{Name: "units_sold", Expr: "quantity", DataType: "number"}, Agg: "sum"}},
				Metrics: []ir.Metric{
					{Name: "units_sold", Def: ir.Agg{Func: "sum", Table: "fct_order_lines", Arg: ir.Col{Table: "fct_order_lines", Name: "quantity"}}},
					{Name: "units_per_order", Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "units_sold"}, Right: ir.Ref{Metric: "orders"}}},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_order_lines", Right: "fct_orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
		},
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatal(err)
	}
	orders := readFile(t, filepath.Join(dir, "FCT_ORDERS.yaml"))
	// units_per_order re-homes to the parent (fct_orders) with a relationAggregate pipeline.
	for _, want := range []string{"units_per_order", "operation: relationAggregate", "key: order_lines", "key: QUANTITY", `prop("_num") / prop("_den")`} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}
	lines := readFile(t, filepath.Join(dir, "FCT_ORDER_LINES.yaml"))
	if strings.Contains(lines, "units_per_order") {
		t.Fatalf("units_per_order must not be in the child file:\n%s", lines)
	}
	// no deferral note was produced -> no NOTES.md
	if _, err := os.Stat(filepath.Join(dir, "NOTES.md")); err == nil {
		t.Fatal("NOTES.md should not exist when nothing is deferred")
	}
}
