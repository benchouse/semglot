package dialect

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// writeOSI writes an OSI document into a temp dir and returns the dir.
func writeOSI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOssieRegistered(t *testing.T) {
	if _, err := AsParser("ossie"); err != nil {
		t.Errorf("AsParser(ossie): %v", err)
	}
	if _, err := AsEmitter("ossie"); err != nil {
		t.Errorf("AsEmitter(ossie): %v", err)
	}
}

func TestOssieParseDatasetsAndFields(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    description: Sales model.
    datasets:
      - name: orders
        source: sales.public.orders
        primary_key: [order_id, line_number]
        description: Customer orders.
        ai_context:
          synonyms: [purchases, sales]
        fields:
          - name: order_id
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_id
            datatype: Integer
            description: Order identifier.
          - name: order_date
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_date
            datatype: Date
            description: Order date.
          - name: created_at
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: created_at
            datatype: DateTime
            dimension:
              is_time: false
          - name: status
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: status
            datatype: String
            ai_context:
              synonyms: [state]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(m.Tables))
	}
	tbl := m.Tables[0]
	if tbl.Name != "orders" {
		t.Errorf("Name = %q, want orders", tbl.Name)
	}
	if tbl.Description != "Customer orders." {
		t.Errorf("Description = %q", tbl.Description)
	}
	if !reflect.DeepEqual(tbl.Synonyms, []string{"purchases", "sales"}) {
		t.Errorf("Synonyms = %v", tbl.Synonyms)
	}
	if !reflect.DeepEqual(tbl.PrimaryKey, []string{"order_id", "line_number"}) {
		t.Errorf("PrimaryKey = %v", tbl.PrimaryKey)
	}
	// order_date defaults to a time dimension via its Date datatype;
	// created_at opts out with an explicit is_time: false.
	var timeNames, dimNames []string
	for _, d := range tbl.TimeDimensions {
		timeNames = append(timeNames, d.Name)
	}
	for _, d := range tbl.Dimensions {
		dimNames = append(dimNames, d.Name)
	}
	if !reflect.DeepEqual(timeNames, []string{"order_date"}) {
		t.Errorf("TimeDimensions = %v, want [order_date]", timeNames)
	}
	if !reflect.DeepEqual(dimNames, []string{"order_id", "created_at", "status"}) {
		t.Errorf("Dimensions = %v", dimNames)
	}
	for _, d := range tbl.Dimensions {
		if d.Name == "order_id" && d.DataType != "integer" {
			t.Errorf("order_id DataType = %q, want integer", d.DataType)
		}
		if d.Name == "status" && !reflect.DeepEqual(d.Synonyms, []string{"state"}) {
			t.Errorf("status Synonyms = %v", d.Synonyms)
		}
	}
}

// TestOssieParseSkipsNonOSI ignores YAML files with no semantic_model key, so a
// mixed directory works.
func TestOssieParseSkipsNonOSI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.yml"), []byte("models:\n  - name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Tables) != 0 {
		t.Errorf("want 0 tables from a non-OSI file, got %d", len(m.Tables))
	}
}

// TestOssieParseDegradesUnrepresentable covers OSI constructs the IR has no
// slot for: a dataset's unique_keys, model-level ai_context.synonyms, and
// ai_context.examples at every level must each surface as a note rather than
// vanish; a field's label has no field-level IR slot either, but degrades
// into the field description instead of a note, so the information stays
// visible to any consumer.
func TestOssieParseDegradesUnrepresentable(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    ai_context:
      synonyms: [revenue]
      examples: ["What were total sales last quarter?"]
    datasets:
      - name: orders
        source: sales.public.orders
        unique_keys:
          - [order_number]
        ai_context:
          examples: ["Show me all orders."]
        fields:
          - name: order_id
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_id
            datatype: Integer
            label: Order ID
            description: Order identifier.
            ai_context:
              examples: ["What is the order id?"]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 || len(m.Tables[0].Dimensions) != 1 {
		t.Fatalf("want 1 table with 1 dimension, got %+v", m.Tables)
	}

	got := m.Tables[0].Dimensions[0].Description
	want := "Order identifier. Display name: Order ID."
	if got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}

	joined := strings.Join(m.Notes, "\n")
	for _, want := range []string{
		`dataset "orders" unique_keys`,
		`model "sales" ai_context.synonyms`,
		`model "sales" ai_context.examples`,
		`dataset "orders" ai_context.examples`,
		`field "order_id" on dataset "orders" ai_context.examples`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Notes = %v, want a note containing %q", m.Notes, want)
		}
	}
}

func TestOssieParseRelationshipsAndMetrics(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: sales.public.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
          - name: customer_id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: customer_id}]
      - name: customers
        source: sales.public.customers
        fields:
          - name: id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: id}]
    relationships:
      - name: orders_to_customers
        from: orders
        to: customers
        from_columns: [customer_id]
        to_columns: [id]
    metrics:
      - name: total_revenue
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
        description: Total revenue.
        ai_context:
          synonyms: [revenue]
      - name: arpu
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount) / COUNT(DISTINCT customers.id)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}

	wantRel := ir.Relationship{
		Left: "orders", Right: "customers",
		Columns: []ir.ColumnPair{{Left: "customer_id", Right: "id"}},
	}
	if len(m.Relationships) != 1 || !reflect.DeepEqual(m.Relationships[0], wantRel) {
		t.Errorf("Relationships = %#v, want %#v", m.Relationships, wantRel)
	}

	orders := tableByName(t, m, "orders")

	// A plain aggregation over one column yields BOTH a measure and a metric,
	// sharing the OSI metric's name - the shape dbt.Parse produces for a
	// type: simple metric, so every emitter behaves the same whatever the source.
	if len(orders.Measures) != 1 {
		t.Fatalf("want 1 measure on orders, got %d", len(orders.Measures))
	}
	ms := orders.Measures[0]
	if ms.Name != "total_revenue" || ms.Agg != "sum" || ms.Expr != "amount" {
		t.Errorf("measure = %+v, want {total_revenue sum amount}", ms)
	}
	if !reflect.DeepEqual(ms.Synonyms, []string{"revenue"}) {
		t.Errorf("measure Synonyms = %v", ms.Synonyms)
	}

	// Both metrics home on orders: total_revenue directly, arpu on its first
	// referenced dataset.
	var names []string
	for _, mt := range orders.Metrics {
		names = append(names, mt.Name)
	}
	if !reflect.DeepEqual(names, []string{"total_revenue", "arpu"}) {
		t.Errorf("orders.Metrics = %v, want [total_revenue arpu]", names)
	}
	wantDef := ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}
	if !reflect.DeepEqual(orders.Metrics[0].Def, wantDef) {
		t.Errorf("total_revenue Def = %#v, want %#v", orders.Metrics[0].Def, wantDef)
	}
}

// TestOssieParseUnparseableMetric notes a metric it cannot parse rather than
// guessing an AST for it.
func TestOssieParseUnparseableMetric(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
    metrics:
      - name: weird
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "NTILE(4) OVER (ORDER BY amount)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	if len(orders.Metrics) != 0 {
		t.Errorf("want the unparseable metric skipped, got %d", len(orders.Metrics))
	}
	var found bool
	for _, n := range m.Notes {
		if strings.Contains(n, "weird") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note naming the skipped metric, got %v", m.Notes)
	}
}

// TestOssieParseEmptyRelationshipColumns notes a relationship with no
// from_columns/to_columns rather than dropping it silently.
func TestOssieParseEmptyRelationshipColumns(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
      - name: customers
        source: s.p.customers
        fields:
          - name: id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: id}]
    relationships:
      - name: empty_rel
        from: orders
        to: customers
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Relationships) != 0 {
		t.Errorf("want the empty-column relationship skipped, got %v", m.Relationships)
	}
	var found bool
	for _, n := range m.Notes {
		if strings.Contains(n, "empty_rel") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note naming the skipped relationship, got %v", m.Notes)
	}
}

// TestOssieMetricRawArgQualifiedByOwnTable covers a cross-table metric whose
// non-home operand is a Raw fragment (a CASE expression): the Raw's Columns
// must come from the table the fragment actually references, not the
// metric's overall home table.
func TestOssieMetricRawArgQualifiedByOwnTable(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
      - name: customers
        source: s.p.customers
        fields:
          - name: active
            expression:
              dialects: [{dialect: ANSI_SQL, expression: active}]
    metrics:
      - name: active_rate
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount) / COUNT(CASE WHEN active THEN 1 END)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	if len(orders.Metrics) != 1 {
		t.Fatalf("want 1 metric on orders, got %d", len(orders.Metrics))
	}
	bin, ok := orders.Metrics[0].Def.(ir.Binary)
	if !ok {
		t.Fatalf("Def = %#v, want ir.Binary", orders.Metrics[0].Def)
	}
	left, ok := bin.Left.(ir.Agg)
	if !ok || left.Table != "orders" {
		t.Errorf("Left = %#v, want an ir.Agg homed on orders", bin.Left)
	}
	right, ok := bin.Right.(ir.Agg)
	if !ok {
		t.Fatalf("Right = %#v, want ir.Agg", bin.Right)
	}
	if right.Table != "customers" {
		t.Errorf("Right.Table = %q, want customers (the table `active` is declared on), not orders", right.Table)
	}
	raw, ok := right.Arg.(ir.Raw)
	if !ok {
		t.Fatalf("Right.Arg = %#v, want ir.Raw", right.Arg)
	}
	if !reflect.DeepEqual(raw.Columns, []string{"active"}) {
		t.Errorf("Raw.Columns = %v, want [active] (customers' columns, not orders')", raw.Columns)
	}
}

// TestOssieMetricUnattributableSubExpressionNoted covers a cross-table metric
// whose non-home operand references a column declared nowhere: it must be
// left unqualified (Table == "") and a note must name the metric and the
// unattributable sub-expression, rather than silently inheriting the metric's
// home table.
func TestOssieMetricUnattributableSubExpressionNoted(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
    metrics:
      - name: weird_ratio
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount) / COUNT(CASE WHEN mystery THEN 1 END)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	if len(orders.Metrics) != 1 {
		t.Fatalf("want 1 metric on orders, got %d", len(orders.Metrics))
	}
	bin, ok := orders.Metrics[0].Def.(ir.Binary)
	if !ok {
		t.Fatalf("Def = %#v, want ir.Binary", orders.Metrics[0].Def)
	}
	right, ok := bin.Right.(ir.Agg)
	if !ok {
		t.Fatalf("Right = %#v, want ir.Agg", bin.Right)
	}
	if right.Table != "" {
		t.Errorf("Right.Table = %q, want empty (unattributable), not silently inherited from the home table", right.Table)
	}
	var found bool
	for _, n := range m.Notes {
		if strings.Contains(n, "weird_ratio") && strings.Contains(n, "mystery") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note naming the metric and the unattributable sub-expression, got %v", m.Notes)
	}
}

// TestOssieMetricUnqualifiedColumnGetsTable covers Apache Ossie's own
// Databricks fixtures, which write an unqualified aggregate argument (e.g.
// SUM(o_totalprice)): once resolved unambiguously to a single dataset, the
// Col arg must have its Table filled in, matching the always-qualified Col
// invariant dbt.go maintains and databricks_metric_view.go relies on.
func TestOssieMetricUnqualifiedColumnGetsTable(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: o_totalprice
            expression:
              dialects: [{dialect: ANSI_SQL, expression: o_totalprice}]
    metrics:
      - name: total_price
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(o_totalprice)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	want := ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "o_totalprice"}}
	if len(orders.Metrics) != 1 || !reflect.DeepEqual(orders.Metrics[0].Def, want) {
		t.Errorf("Def = %#v, want %#v", orders.Metrics[0].Def, want)
	}
}

// TestOssieMetricBareColumnQualified covers an OSI metric whose expression is
// a bare column, with no aggregate wrapper: `amount` parses to an ir.Col with
// no Table, and OSI's metrics list is MODEL-scoped, so emitting it unqualified
// names nothing in particular. resolveAggTables must fill the Table in — it
// used to descend only Agg and Binary, leaving this case silently wrong.
func TestOssieMetricBareColumnQualified(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
          - name: tax
            expression:
              dialects: [{dialect: ANSI_SQL, expression: tax}]
    metrics:
      - name: raw_amount
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "amount"}]
      - name: amount_plus_tax
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "amount + tax"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	if len(orders.Metrics) != 2 {
		t.Fatalf("want 2 metrics on orders, got %d", len(orders.Metrics))
	}
	if want := (ir.Col{Table: "orders", Name: "amount"}); !reflect.DeepEqual(orders.Metrics[0].Def, want) {
		t.Errorf("raw_amount Def = %#v, want %#v", orders.Metrics[0].Def, want)
	}
	// A bare Col reached through a Binary must be qualified too.
	want := ir.Binary{Op: "+", Left: ir.Col{Table: "orders", Name: "amount"}, Right: ir.Col{Table: "orders", Name: "tax"}}
	if !reflect.DeepEqual(orders.Metrics[1].Def, want) {
		t.Errorf("amount_plus_tax Def = %#v, want %#v", orders.Metrics[1].Def, want)
	}

	// The observable defect: the emitted, model-scoped metric expression.
	f, _ := emitOssie(t, m, Options{Database: "A", Schema: "M", Name: "sales"})
	got := map[string]string{}
	for _, mt := range f.SemanticModel[0].Metrics {
		got[mt.Name] = fieldExprText(osiField{Expression: mt.Expression})
	}
	if got["raw_amount"] != "orders.amount" {
		t.Errorf("emitted raw_amount = %q, want orders.amount", got["raw_amount"])
	}
	if got["amount_plus_tax"] != "orders.amount + orders.tax" {
		t.Errorf("emitted amount_plus_tax = %q, want %q", got["amount_plus_tax"], "orders.amount + orders.tax")
	}
}

// TestOssieMetricBareColumnUnattributableNoted covers the other half of the
// case above: a bare column no dataset declares stays unqualified and is
// noted, rather than being attributed to the metric's home table by default.
func TestOssieMetricBareColumnUnattributableNoted(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
    metrics:
      - name: amount_plus_mystery
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "amount + mystery"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	bin, ok := orders.Metrics[0].Def.(ir.Binary)
	if !ok {
		t.Fatalf("Def = %#v, want ir.Binary", orders.Metrics[0].Def)
	}
	if right := bin.Right.(ir.Col); right.Table != "" {
		t.Errorf("Right.Table = %q, want empty (unattributable), not inherited from the home table", right.Table)
	}
	var found bool
	for _, n := range m.Notes {
		if strings.Contains(n, "amount_plus_mystery") && strings.Contains(n, "mystery") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note naming the metric and the unattributable column, got %v", m.Notes)
	}
}

// TestOssieParseMergesDatasetsByName covers two semantic_model entries — in
// two separate files, so file-level merging is exercised too — that each
// declare `orders`. They must fold into ONE ir.Table: two same-named tables
// would make tableIndex home every metric on the first and make Emit write two
// `datasets:` entries under one name, which OSI forbids. dbt.Parse merges by
// name the same way (TestDBTParseMerge).
func TestOssieParseMergesDatasetsByName(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", `
version: "0.2.0.dev0"
semantic_model:
  - name: sales_a
    datasets:
      - name: orders
        source: s.p.orders
        primary_key: [order_id]
        description: Customer orders.
        ai_context:
          synonyms: [purchases]
        fields:
          - name: order_id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: order_id}]
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
    metrics:
      - name: total_amount
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
`)
	write("b.yaml", `
version: "0.2.0.dev0"
semantic_model:
  - name: sales_b
    datasets:
      - name: orders
        source: s.p.orders
        ai_context:
          synonyms: [sales]
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
          - name: tax
            expression:
              dialects: [{dialect: ANSI_SQL, expression: tax}]
    metrics:
      - name: total_tax
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.tax)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 {
		names := make([]string, len(m.Tables))
		for i, tb := range m.Tables {
			names[i] = tb.Name
		}
		t.Fatalf("want the two `orders` declarations merged into 1 table, got %d: %v", len(m.Tables), names)
	}
	orders := m.Tables[0]
	if !reflect.DeepEqual(orders.PrimaryKey, []string{"order_id"}) {
		t.Errorf("PrimaryKey = %v, want [order_id] (from the declaration that had one)", orders.PrimaryKey)
	}
	if orders.Description != "Customer orders." {
		t.Errorf("Description = %q", orders.Description)
	}
	if !reflect.DeepEqual(orders.Synonyms, []string{"purchases", "sales"}) {
		t.Errorf("Synonyms = %v, want the union [purchases sales]", orders.Synonyms)
	}
	var dims []string
	for _, d := range orders.Dimensions {
		dims = append(dims, d.Name)
	}
	// `amount` is declared by both and must appear once.
	if !reflect.DeepEqual(dims, []string{"order_id", "amount", "tax"}) {
		t.Errorf("Dimensions = %v, want [order_id amount tax] (unioned, `amount` not duplicated)", dims)
	}
	var metrics []string
	for _, mt := range orders.Metrics {
		metrics = append(metrics, mt.Name)
	}
	if !reflect.DeepEqual(metrics, []string{"total_amount", "total_tax"}) {
		t.Errorf("Metrics = %v, want both entries' metrics homed on the merged table", metrics)
	}
	// An identical redeclaration loses nothing, so it must not produce a note.
	if len(m.Notes) != 0 {
		t.Errorf("want no notes for a conflict-free merge, got %v", m.Notes)
	}
}

// TestOssieParseMergeConflictsNoted covers a merge whose two declarations
// DISAGREE: the first wins (mirroring ossie_emit.go's addField in the emit
// direction) and every discarded value is named in a note.
func TestOssieParseMergeConflictsNoted(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: first
    datasets:
      - name: orders
        source: s.p.orders
        primary_key: [order_id]
        description: The first description.
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
    metrics:
      - name: total
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
  - name: second
    datasets:
      - name: orders
        source: s.p.orders
        primary_key: [order_key]
        description: The second description.
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: gross_amount}]
    metrics:
      - name: total
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("want 1 merged table, got %d", len(m.Tables))
	}
	orders := m.Tables[0]
	if orders.Description != "The first description." {
		t.Errorf("Description = %q, want the first declaration's", orders.Description)
	}
	if !reflect.DeepEqual(orders.PrimaryKey, []string{"order_id"}) {
		t.Errorf("PrimaryKey = %v, want the first declaration's [order_id], not a union", orders.PrimaryKey)
	}
	if len(orders.Dimensions) != 1 || orders.Dimensions[0].Expr != "amount" {
		t.Errorf("Dimensions = %+v, want the first `amount` only", orders.Dimensions)
	}
	if len(orders.Metrics) != 1 {
		t.Errorf("want the duplicate metric dropped, got %d", len(orders.Metrics))
	}
	joined := strings.Join(m.Notes, "\n")
	for _, want := range []string{
		"different descriptions",
		"different primary keys",
		`field "amount" on dataset "orders" is declared more than once`,
		`metric "total" on dataset "orders" is declared more than once`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Notes = %v, want one containing %q", m.Notes, want)
		}
	}
}

// TestOssieParseNotesNonModelAIContextAndExtensions covers the constructs that
// used to parse into the OSI structs and then vanish: ai_context.instructions
// anywhere below the model level (in both its object and bare-string forms),
// metric-level ai_context.examples, and custom_extensions at every level the
// spec allows one. None has an IR slot, so each must leave a note.
func TestOssieParseNotesNonModelAIContextAndExtensions(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        ai_context:
          instructions: Orders are restated nightly.
        custom_extensions:
          - vendor_name: ACME
            data: '{"x": 1}'
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
            ai_context: Amounts are in cents.
            custom_extensions:
              - vendor_name: DATABRICKS
                data: '{"format": "currency"}'
      - name: customers
        source: s.p.customers
        fields:
          - name: id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: id}]
    relationships:
      - name: orders_to_customers
        from: orders
        to: customers
        from_columns: [amount]
        to_columns: [id]
        custom_extensions:
          - vendor_name: DATABRICKS
    metrics:
      - name: total
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
        ai_context:
          instructions: Always filter to shipped orders.
          examples: ["What was the total last month?"]
        custom_extensions:
          - vendor_name: SALESFORCE
    custom_extensions:
      - vendor_name: ACME
      - vendor_name: DBT
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(m.Notes, "\n")
	for _, want := range []string{
		// ai_context.instructions below the model level (object form).
		`dataset "orders" ai_context.instructions "Orders are restated nightly."`,
		`metric "total" ai_context.instructions "Always filter to shipped orders."`,
		// ...and the bare-string form osiAIContext.UnmarshalYAML accepts.
		`field "amount" on dataset "orders" ai_context.instructions "Amounts are in cents."`,
		// metric-level ai_context.examples.
		`metric "total" ai_context.examples [What was the total last month?]`,
		// custom_extensions at every level, naming the vendor(s).
		`model "sales" custom_extensions [ACME DBT]`,
		`dataset "orders" custom_extensions [ACME]`,
		`field "amount" on dataset "orders" custom_extensions [DATABRICKS]`,
		`relationship "orders_to_customers" custom_extensions [DATABRICKS]`,
		`metric "total" custom_extensions [SALESFORCE]`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Notes = %v,\nwant one containing %q", m.Notes, want)
		}
	}
}

// TestOssieEmitWritesNoCustomExtensions pins the emit half of the decision
// above: semglot degrades custom_extensions to prose and writes none of its
// own, so no `vendor_name: SEMGLOT` block (or any other) may appear.
func TestOssieEmitWritesNoCustomExtensions(t *testing.T) {
	m := &ir.Model{
		Notes: []string{`model "sales" custom_extensions [ACME]: vendor extensions are not transpiled; dropped`},
		Tables: []ir.Table{{
			Name:       "orders",
			Dimensions: []ir.Field{{Name: "amount", Expr: "amount"}},
		}},
	}
	f, raw := emitOssie(t, m, Options{Database: "A", Schema: "M", Name: "sales"})
	// No custom_extensions KEY anywhere. Checked on the raw text by key prefix
	// rather than by substring, because the prose degradation legitimately puts
	// the words "custom_extensions" inside ai_context.instructions — which is
	// the whole point, and must keep working.
	for _, line := range strings.Split(raw, "\n") {
		if l := strings.TrimLeft(line, " -"); strings.HasPrefix(l, "custom_extensions:") {
			t.Errorf("emitted document must not declare custom_extensions:\n%s", raw)
			break
		}
	}
	sm := f.SemanticModel[0]
	if sm.CustomExtensions != nil || sm.Datasets[0].CustomExtensions != nil ||
		sm.Datasets[0].Fields[0].CustomExtensions != nil {
		t.Errorf("emitted document round-trips a custom_extensions block:\n%s", raw)
	}
	// The prose degradation itself must survive.
	if sm.AIContext == nil || !strings.Contains(sm.AIContext.Instructions, "custom_extensions [ACME]") {
		t.Errorf("the custom_extensions note must survive as prose, got %+v", sm.AIContext)
	}
}

// TestOssieParseNotesMetricDataType covers a metric that is NOT a plain
// aggregation over one column: it synthesises no ir.Measure, and ir.Metric has
// no DataType field, so its OSI `datatype` has nowhere to go and must be noted.
func TestOssieParseNotesMetricDataType(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
          - name: order_id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: order_id}]
    metrics:
      - name: total_amount
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
        datatype: Decimal
      - name: aov
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount) / COUNT(DISTINCT orders.order_id)"}]
        datatype: Decimal
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	// The simple aggregation keeps its datatype: it synthesises a measure, and
	// ir.Measure embeds ir.Field, which has a DataType slot.
	if len(orders.Measures) != 1 || orders.Measures[0].DataType != "decimal" {
		t.Errorf("measures = %+v, want total_amount carrying DataType decimal", orders.Measures)
	}
	// The ratio does not, so it must be noted.
	joined := strings.Join(m.Notes, "\n")
	if !strings.Contains(joined, `metric "aov" datatype "Decimal"`) {
		t.Errorf("Notes = %v, want one reporting aov's dropped datatype", m.Notes)
	}
	if strings.Contains(joined, `metric "total_amount" datatype`) {
		t.Errorf("Notes = %v, must NOT report a datatype the synthesised measure carries", m.Notes)
	}
}

// TestOssieIrToOSITypeNumber covers dbt/Snowflake's default numeric spelling,
// "number", which is an exact synonym of "numeric" and must map to Decimal
// the same way rather than falling through to no datatype at all.
func TestOssieIrToOSITypeNumber(t *testing.T) {
	if got := irToOSIType("number"); got != "Decimal" {
		t.Errorf(`irToOSIType("number") = %q, want "Decimal"`, got)
	}
	// The reverse direction must stay untouched: Decimal still spells back to
	// "decimal", never "number".
	if got := osiToIRType("Decimal"); got != "decimal" {
		t.Errorf(`osiToIRType("Decimal") = %q, want "decimal"`, got)
	}
}

// tableByName fetches a table by name or fails the test.
func tableByName(t *testing.T, m *ir.Model, name string) ir.Table {
	t.Helper()
	for _, tb := range m.Tables {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("no table %q in %v", name, m.Tables)
	return ir.Table{}
}
