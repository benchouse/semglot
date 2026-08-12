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
