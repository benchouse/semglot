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
	if _, err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
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
	if _, err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
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

// TestSupersimpleTableSynonyms folds table synonyms into the model description,
// since supersimple has no synonyms slot. This closes the gap dialect/README.md
// recorded under "Gaps vs. limits".
func TestSupersimpleTableSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:        "fct_orders",
		Description: "Orders.",
		Synonyms:    []string{"purchases", "sales"},
		Dimensions:  []ir.Field{{Name: "order_id", Expr: "order_id"}},
	}}}
	out := t.TempDir()
	if _, err := (supersimple{}).Emit(m, out); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(out, "FCT_ORDERS.yaml"))
	if !strings.Contains(got, "Synonyms: purchases, sales.") {
		t.Errorf("emitted supersimple missing folded synonyms in:\n%s", got)
	}
}

// TestSupersimpleEmitPrefersDeclaredSource covers task 16's defect: a declared
// ossie `source` is a fully-qualified physical address that nothing
// downstream can recover if it is discarded. `table:` must use it verbatim
// rather than relocating the model to the profile's schema under the IR's
// logical name.
func TestSupersimpleEmitPrefersDeclaredSource(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "orders", Source: "PROD.SALES.orders_v1"}}}
	dir := t.TempDir()
	warnings, err := (supersimple{Schema: "PUBLIC"}).Emit(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "orders") {
			t.Errorf("unexpected warning for a cleanly-splittable source: %q", w)
		}
	}
	got := readFile(t, filepath.Join(dir, "ORDERS.yaml"))
	if !strings.Contains(got, "table: PROD.SALES.orders_v1") {
		t.Errorf("table must reference the declared source PROD.SALES.orders_v1, not PUBLIC.ORDERS:\n%s", got)
	}
}

// TestSupersimpleEmitAcceptsTwoPartSourceVerbatim covers fix round 1's
// correction: `table:` holds its reference as ONE string (unlike
// cortexBaseTable's separate Database/Schema/Table fields), so a two-part
// schema.table source — which resolves fine against the connection's default
// database — must be used verbatim, with no warning, rather than rejected in
// favour of a same-shaped fabricated two-part address from the profile. This
// is the exact regression the fix-round review caught: given
// `source: CRM.customer_master`, the previous 3-part gate warned it "could
// not be expressed as supersimple's database/schema/table shape" and then
// fell back to a same-shaped-but-fabricated PUBLIC.CUSTOMER_MASTER.
func TestSupersimpleEmitAcceptsTwoPartSourceVerbatim(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "customer_master", Source: "CRM.customer_master"}}}
	dir := t.TempDir()
	warnings, err := (supersimple{Schema: "PUBLIC"}).Emit(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings for a usable two-part source: %v", warnings)
	}
	got := readFile(t, filepath.Join(dir, "CUSTOMER_MASTER.yaml"))
	if !strings.Contains(got, "table: CRM.customer_master") {
		t.Errorf("table must use the declared two-part CRM.customer_master verbatim:\n%s", got)
	}
	if strings.Contains(got, "table: PUBLIC.CUSTOMER_MASTER") {
		t.Errorf("must not fall back to a fabricated PUBLIC.CUSTOMER_MASTER for a usable source:\n%s", got)
	}
}

// TestSupersimpleEmitQuerySourceFallsBackAndWarns covers the case that
// genuinely can't go in `table:`: the OSI spec permits `source` to be a
// query rather than a table reference, but supersimple's `table:` needs an
// address, not a subquery embedded as if it were one. That must fall back to
// the profile reconstruction AND warn.
func TestSupersimpleEmitQuerySourceFallsBackAndWarns(t *testing.T) {
	source := "SELECT * FROM raw.orders WHERE deleted_at IS NULL"
	m := &ir.Model{Tables: []ir.Table{{Name: "orders", Source: source}}}
	dir := t.TempDir()
	warnings, err := (supersimple{Schema: "PUBLIC"}).Emit(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, `table "orders"`) && strings.Contains(w, source) && strings.Contains(w, "supersimple") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the table, the query source, and supersimple; got %v", warnings)
	}
	got := readFile(t, filepath.Join(dir, "ORDERS.yaml"))
	if !strings.Contains(got, "table: PUBLIC.ORDERS") {
		t.Errorf("must fall back to the profile-reconstructed reference, not paste the query into table:\n%s", got)
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
	if _, err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
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
	if _, err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
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

// TestSupersimpleTwoSidedJoinKey pins the two-sided join form. `join_key` is
// Supersimple's shorthand for "both sides share this column name"; when the FK
// and PK differ it addresses the wrong column, and `supersimple validate`
// rejected three dim_date relations with "references unknown property
// 'date_day' on related model" because of exactly this.
func TestSupersimpleTwoSidedJoinKey(t *testing.T) {
	if got := joinStrategy("date_day", "order_date"); got.JoinKeyOnBase != "date_day" ||
		got.JoinKeyOnRelated != "order_date" || got.JoinKey != "" {
		t.Errorf("differing keys must use the two-sided form; got %+v", got)
	}
	// Matching keys keep the shorthand, which is what the vendor emits and what
	// the other 20 relations already validated with.
	if got := joinStrategy("order_id", "order_id"); got.JoinKey != "order_id" ||
		got.JoinKeyOnBase != "" {
		t.Errorf("matching keys should use the shorthand; got %+v", got)
	}
}
