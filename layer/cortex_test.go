package layer

import (
	"os"
	"path/filepath"
	"strings"
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
					{Name: "net_revenue", Description: "Net booked revenue.",
						Def: ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_net_booked"}}},
					{Name: "orders",
						Def: ir.Agg{Func: "count_distinct", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_id"}}},
					{Name: "aov", Description: "Net revenue / orders.",
						Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "orders"}}},
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

// When the IR carries a real data type, the emitter uses it instead of the
// name heuristic — here a column the heuristic would call TEXT is BOOLEAN.
func TestCortexEmitPrefersRealDataType(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "dim_customer",
		PrimaryKey: []string{"customer_sk"},
		Dimensions: []ir.Field{
			{Name: "customer_sk", Expr: "customer_sk", DataType: "number"},
			{Name: "accepts_marketing", Expr: "accepts_marketing", DataType: "boolean", Description: "Opted in."},
		},
	}}}
	dir := t.TempDir()
	if err := (cortex{Database: "DB", Schema: "MAIN", ModelName: "m"}).Emit(m, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "data_type: BOOLEAN") {
		t.Fatalf("expected accepts_marketing -> BOOLEAN from real type, got:\n%s", out)
	}
	if !strings.Contains(out, "description: Opted in.") {
		t.Fatalf("expected column description to pass through, got:\n%s", out)
	}
}

// Columns without a source data_type are reported as gaps (with the type that
// was inferred), while columns carrying a real type are not — so the CLI can
// warn about exactly the columns whose Cortex type was guessed.
func TestCortexTypeGaps(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "fct_vendor_bills",
		Dimensions: []ir.Field{
			{Name: "bill_id", Expr: "bill_id"},                 // inferred NUMBER (_id)
			{Name: "bill_amount", Expr: "bill_amount"},         // inferred TEXT — the dangerous case
			{Name: "po_id", Expr: "po_id", DataType: "number"}, // real type: not a gap
		},
		TimeDimensions: []ir.Field{{Name: "bill_date", Expr: "bill_date"}},                                // inferred DATE
		Measures:       []ir.Measure{{Field: ir.Field{Name: "total_due", Expr: "total_due"}, Agg: "sum"}}, // inferred NUMBER
	}}}

	got := CortexTypeGaps(m)
	want := []string{
		"fct_vendor_bills.bill_id (inferred NUMBER)",
		"fct_vendor_bills.bill_amount (inferred TEXT)",
		"fct_vendor_bills.bill_date (inferred DATE)",
		"fct_vendor_bills.total_due (inferred NUMBER)",
	}
	if len(got) != len(want) {
		t.Fatalf("gap count = %d, want %d\ngot: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("gap[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// A column with a real data_type produces no gap at all.
func TestCortexTypeGapsNoneWhenTyped(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "dim_customer",
		Dimensions: []ir.Field{{Name: "customer_segment", Expr: "customer_segment", DataType: "varchar"}},
	}}}
	if gaps := CortexTypeGaps(m); len(gaps) != 0 {
		t.Fatalf("expected no gaps for fully-typed table, got: %v", gaps)
	}
}

// IR notes are surfaced as Cortex custom_instructions (free-text guidance).
func TestCortexEmitNotesAsCustomInstructions(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{{
			Name: "t", PrimaryKey: []string{"id"},
			Dimensions: []ir.Field{{Name: "id", Expr: "id"}},
		}},
		Notes: []string{`metric "growth" (derived): Orders, boosted. — not transpiled: unsupported metric type "derived"`},
	}
	dir := t.TempDir()
	if err := (cortex{}).Emit(m, dir); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "custom_instructions:") {
		t.Fatalf("expected custom_instructions in output:\n%s", out)
	}
	if !strings.Contains(out, `unsupported metric type`) {
		t.Fatalf("expected the note text to pass through:\n%s", out)
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
