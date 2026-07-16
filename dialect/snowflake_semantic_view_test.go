package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func emitSV(t *testing.T, e Emitter) string {
	t.Helper()
	dir := t.TempDir()
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "fct_orders",
		PrimaryKey: []string{"order_id"},
		Dimensions: []ir.Field{{Name: "order_date", Expr: "order_date"}},
	}}}
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "definition.md"))
	if err != nil {
		t.Fatalf("read definition.md: %v", err)
	}
	return string(b)
}

// TestSVViewSchemaSplit verifies the semantic-view OBJECT is created in
// ViewSchema (SEM) while its TABLES reference the source Schema (MAIN) — the two
// must be independently controllable so the view can live in a curated schema
// over base tables in another.
func TestSVViewSchemaSplit(t *testing.T) {
	e := snowflakeSemanticView{}.WithOptions(Options{
		Database:   "EVAL_MARTS",
		Schema:     "MAIN",
		ViewSchema: "SEM",
		Name:       "SV_ECOMM",
	})
	out := emitSV(t, e)

	if !strings.Contains(out, "create or replace semantic view EVAL_MARTS.SEM.SV_ECOMM") {
		t.Errorf("view object must be qualified in ViewSchema (EVAL_MARTS.SEM.SV_ECOMM):\n%s", out)
	}
	if !strings.Contains(out, "EVAL_MARTS.MAIN.FCT_ORDERS") {
		t.Errorf("table refs must use the source Schema (EVAL_MARTS.MAIN.FCT_ORDERS):\n%s", out)
	}
	if strings.Contains(out, "EVAL_MARTS.SEM.FCT_ORDERS") {
		t.Errorf("table refs must NOT be placed in ViewSchema:\n%s", out)
	}
}

// TestSVViewSchemaFallback verifies ViewSchema falls back to Schema when unset,
// so callers that don't need a split keep the single-schema behavior.
func TestSVViewSchemaFallback(t *testing.T) {
	e := snowflakeSemanticView{}.WithOptions(Options{
		Database: "EVAL_MARTS",
		Schema:   "MAIN",
		Name:     "SV_ECOMM",
	})
	out := emitSV(t, e)
	if !strings.Contains(out, "create or replace semantic view EVAL_MARTS.MAIN.SV_ECOMM") {
		t.Errorf("with no ViewSchema the view should fall back to Schema (EVAL_MARTS.MAIN.SV_ECOMM):\n%s", out)
	}
}

// TestSVDedupsDimensionMetricNameCollision verifies a dimension whose name
// collides with an emitted metric on the same table is dropped — Snowflake
// requires expression names unique across a semantic view's dimensions and
// metrics (e.g. a precomputed `roas` column alongside a computed ROAS metric).
func TestSVDedupsDimensionMetricNameCollision(t *testing.T) {
	dir := t.TempDir()
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "obt_marketing_daily",
		PrimaryKey: []string{"campaign_id"},
		Dimensions: []ir.Field{
			{Name: "platform", Expr: "platform"},
			{Name: "roas", Expr: "roas"}, // raw precomputed column
		},
		Metrics: []ir.Metric{
			// a metric named `roas` that IS emitted (simple aggregate) so it
			// collides with the raw `roas` dimension above
			{Name: "roas", Def: ir.Agg{Func: "sum", Table: "obt_marketing_daily", Arg: ir.Col{Table: "obt_marketing_daily", Name: "roas"}}},
		},
	}}}
	e := snowflakeSemanticView{}.WithOptions(Options{Database: "EVAL_MARTS", Schema: "MAIN", ViewSchema: "SEM", Name: "SV"})
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "definition.md"))
	out := string(b)
	if n := strings.Count(out, "OBT_MARKETING_DAILY.ROAS as"); n != 1 {
		t.Errorf("ROAS must be emitted exactly once (metric wins), got %d:\n%s", n, out)
	}
	// the surviving ROAS must be the computed metric, not the raw column
	if !strings.Contains(out, "OBT_MARKETING_DAILY.ROAS as SUM") {
		t.Errorf("surviving ROAS should be the computed metric:\n%s", out)
	}
}

// TestRenderSVMetricDef verifies simple aggregates render as-is, derived ratios
// render as references to their component metrics (never inlined aggregates —
// Snowflake rejects those), and a ratio that inlines an aggregate degrades.
func TestRenderSVMetricDef(t *testing.T) {
	tblOf := map[string]string{"net_revenue": "FCT_ORDERS", "orders": "FCT_ORDERS"}

	// simple aggregate
	agg := ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_net_booked"}}
	if got, ok := renderSVMetricDef(agg, tblOf); !ok || got != "SUM(FCT_ORDERS.ORDER_NET_BOOKED)" {
		t.Errorf("simple agg = %q,%v want SUM(FCT_ORDERS.ORDER_NET_BOOKED),true", got, ok)
	}

	// derived ratio over metric refs → qualified metric names
	ratio := ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "orders"}}
	if got, ok := renderSVMetricDef(ratio, tblOf); !ok || got != "FCT_ORDERS.NET_REVENUE / FCT_ORDERS.ORDERS" {
		t.Errorf("ratio = %q,%v want FCT_ORDERS.NET_REVENUE / FCT_ORDERS.ORDERS,true", got, ok)
	}

	// ratio inlining an aggregate → degrade (Snowflake rejects an aggregate in a derived metric)
	inlined := ir.Binary{Op: "/", Left: agg, Right: ir.Ref{Metric: "orders"}}
	if _, ok := renderSVMetricDef(inlined, tblOf); ok {
		t.Error("ratio with an inlined aggregate should degrade (ok=false)")
	}

	// ref to an unknown metric → degrade
	unknown := ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "mystery"}}
	if _, ok := renderSVMetricDef(unknown, tblOf); ok {
		t.Error("ratio referencing an unknown metric should degrade (ok=false)")
	}
}

// TestSVUnqualifiedWithoutDatabase verifies that with no database the view name
// stays unqualified (keeps zero-value output valid rather than emitting a
// leading-dot name).
func TestSVUnqualifiedWithoutDatabase(t *testing.T) {
	out := emitSV(t, snowflakeSemanticView{}.WithOptions(Options{Name: "SV_ECOMM"}))
	if !strings.Contains(out, "create or replace semantic view SV_ECOMM\n") {
		t.Errorf("with no database the view name should be unqualified:\n%s", out)
	}
}
