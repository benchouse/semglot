package layer

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// ---- helpers ----

// metricDefsByName returns a name->Def map across every table, plus a resolver.
func metricDefsByName(m *ir.Model) (map[string]ir.Expr, func(string) (ir.Expr, bool)) {
	defs := map[string]ir.Expr{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			defs[mt.Name] = mt.Def
		}
	}
	return defs, func(n string) (ir.Expr, bool) { e, ok := defs[n]; return e, ok }
}

// cortexMetricExprs emits Cortex and returns table->metric->expr for the emitted
// (non-degraded) metrics.
func cortexMetricExprs(t *testing.T, m *ir.Model) map[string]map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := (cortex{Database: "ANALYTICS", Schema: "MAIN", ModelName: "x"}).Emit(m, dir); err != nil {
		t.Fatalf("cortex emit: %v", err)
	}
	var cm struct {
		Tables []struct {
			Name    string `yaml:"name"`
			Metrics []struct {
				Name string `yaml:"name"`
				Expr string `yaml:"expr"`
			} `yaml:"metrics"`
		} `yaml:"tables"`
	}
	if err := yaml.Unmarshal([]byte(readFile(t, filepath.Join(dir, "semantic_model.yaml"))), &cm); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for _, tb := range cm.Tables {
		out[tb.Name] = map[string]string{}
		for _, mt := range tb.Metrics {
			out[tb.Name][mt.Name] = mt.Expr
		}
	}
	return out
}

// ---- derived ----

func TestDBTParseDerived(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_derived")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defs := map[string]ir.Expr{}
	for _, mt := range got.Tables[0].Metrics {
		defs[mt.Name] = mt.Def
	}
	wantGP := ir.Binary{Op: "-", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "cost"}}
	if !reflect.DeepEqual(defs["gross_profit"], ir.Expr(wantGP)) {
		t.Fatalf("gross_profit Def = %#v, want %#v", defs["gross_profit"], wantGP)
	}
	wantBP := ir.Binary{Op: "*",
		Left:  ir.Binary{Op: "-", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "cost"}},
		Right: ir.Lit{Value: "2"}}
	if !reflect.DeepEqual(defs["boosted_profit"], ir.Expr(wantBP)) {
		t.Fatalf("boosted_profit Def = %#v, want %#v", defs["boosted_profit"], wantBP)
	}
}

func TestDBTDerivedCortexRender(t *testing.T) {
	m, err := dbt{}.Parse("testdata/dbt_derived")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	exprs := cortexMetricExprs(t, m)["fct_orders"]
	if want := "SUM(FCT_ORDERS.REVENUE) - SUM(FCT_ORDERS.COST)"; exprs["gross_profit"] != want {
		t.Fatalf("gross_profit expr = %q, want %q", exprs["gross_profit"], want)
	}
	if want := "(SUM(FCT_ORDERS.REVENUE) - SUM(FCT_ORDERS.COST)) * 2"; exprs["boosted_profit"] != want {
		t.Fatalf("boosted_profit expr = %q, want %q", exprs["boosted_profit"], want)
	}
}

func TestDBTDerivedSupersimpleDegrades(t *testing.T) {
	m, err := dbt{}.Parse("testdata/dbt_derived")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	notes := readFile(t, filepath.Join(dir, "NOTES.md"))
	if !strings.Contains(notes, "gross_profit") || !strings.Contains(notes, "derived") {
		t.Fatalf("expected gross_profit derived degradation note:\n%s", notes)
	}
}

// ---- filter ----

func TestDBTParseFiltered(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_filtered")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defs := map[string]ir.Expr{}
	for _, mt := range got.Tables[0].Metrics {
		defs[mt.Name] = mt.Def
	}
	want := ir.Agg{Func: "sum", Table: "fct_orders",
		Arg:    ir.Col{Table: "fct_orders", Name: "amount"},
		Filter: ir.Col{Table: "fct_orders", Name: "is_completed"}}
	if !reflect.DeepEqual(defs["completed_revenue"], ir.Expr(want)) {
		t.Fatalf("completed_revenue Def = %#v, want %#v", defs["completed_revenue"], want)
	}
}

func TestDBTFilteredCortexRender(t *testing.T) {
	m, err := dbt{}.Parse("testdata/dbt_filtered")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	exprs := cortexMetricExprs(t, m)["fct_orders"]
	if want := "SUM(CASE WHEN FCT_ORDERS.IS_COMPLETED THEN FCT_ORDERS.AMOUNT END)"; exprs["completed_revenue"] != want {
		t.Fatalf("completed_revenue expr = %q, want %q", exprs["completed_revenue"], want)
	}
}

// A compound (Raw) filter has its column references qualified at render time,
// exactly like a Raw aggregation arg.
func TestRenderFilteredRawQualifies(t *testing.T) {
	def := ir.Agg{Func: "sum", Table: "fct_orders",
		Arg:    ir.Col{Table: "fct_orders", Name: "amount"},
		Filter: ir.Raw{SQL: "status = 'completed'", Columns: []string{"amount", "status"}}}
	got := renderSQL(def, func(string) (ir.Expr, bool) { return nil, false })
	if want := "sum(case when fct_orders.status = 'completed' then fct_orders.amount end)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDBTFilteredSupersimpleDegrades(t *testing.T) {
	m, err := dbt{}.Parse("testdata/dbt_filtered")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	notes := readFile(t, filepath.Join(dir, "NOTES.md"))
	if !strings.Contains(notes, "completed_revenue") || !strings.Contains(notes, "filter") {
		t.Fatalf("expected completed_revenue filtered degradation note:\n%s", notes)
	}
	// the unfiltered peer still emits normally
	orders := readFile(t, filepath.Join(dir, "FCT_ORDERS.yaml"))
	if !strings.Contains(orders, "name: revenue") && !strings.Contains(orders, "revenue") {
		t.Fatalf("unfiltered revenue metric should still emit:\n%s", orders)
	}
}

// ---- round-trip (derived + filtered) ----

// roundTripDefs parses fixtureDir, emits dbt, re-parses, and returns source and
// re-parsed name->Def maps for comparison.
func roundTripDefs(t *testing.T, fixtureDir string) (src, rt map[string]ir.Expr) {
	t.Helper()
	m1, err := dbt{}.Parse(fixtureDir)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	out := t.TempDir()
	if err := (dbt{}).Emit(m1, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	m2, err := dbt{}.Parse(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	src, _ = metricDefsByName(m1)
	rt, _ = metricDefsByName(m2)
	return src, rt
}

func TestDBTDerivedRoundTrip(t *testing.T) {
	src, rt := roundTripDefs(t, "testdata/dbt_derived")
	for _, name := range []string{"revenue", "cost", "gross_profit", "boosted_profit"} {
		if !reflect.DeepEqual(src[name], rt[name]) {
			t.Fatalf("%s Def changed on round-trip:\n src: %#v\n  rt: %#v", name, src[name], rt[name])
		}
	}
}

func TestDBTFilteredRoundTrip(t *testing.T) {
	src, rt := roundTripDefs(t, "testdata/dbt_filtered")
	for _, name := range []string{"revenue", "completed_revenue"} {
		if !reflect.DeepEqual(src[name], rt[name]) {
			t.Fatalf("%s Def changed on round-trip:\n src: %#v\n  rt: %#v", name, src[name], rt[name])
		}
	}
}

// ---- cumulative + conversion (PROVISIONAL) ----

// PROVISIONAL: no live target, no source-fixture round-trip. We assert only that
// the parser builds the node and that both target emitters degrade to a note.
func TestDBTParseCumulativeConversionProvisional(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_cumulative_conversion")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	defs := map[string]ir.Expr{}
	for _, mt := range got.Tables[0].Metrics {
		defs[mt.Name] = mt.Def
	}
	wantWin := ir.Window{Base: ir.Ref{Metric: "revenue"}, Window: "30 days", Grain: "month"}
	if !reflect.DeepEqual(defs["cumulative_revenue"], ir.Expr(wantWin)) {
		t.Fatalf("cumulative_revenue Def = %#v, want %#v", defs["cumulative_revenue"], wantWin)
	}
	wantConv := ir.Conversion{Base: ir.Ref{Metric: "revenue"}, Conv: ir.Ref{Metric: "orders"},
		Entity: "customer", Window: "7 days"}
	if !reflect.DeepEqual(defs["order_conversion"], ir.Expr(wantConv)) {
		t.Fatalf("order_conversion Def = %#v, want %#v", defs["order_conversion"], wantConv)
	}
}

func TestCumulativeConversionCortexDegrades(t *testing.T) {
	m, err := dbt{}.Parse("testdata/dbt_cumulative_conversion")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dir := t.TempDir()
	if err := (cortex{Database: "ANALYTICS", Schema: "MAIN", ModelName: "x"}).Emit(m, dir); err != nil {
		t.Fatalf("cortex emit: %v", err)
	}
	out := readFile(t, filepath.Join(dir, "semantic_model.yaml"))
	// degraded metrics are omitted from the metrics list...
	exprs := cortexMetricExprs(t, m)["fct_orders"]
	if _, ok := exprs["cumulative_revenue"]; ok {
		t.Fatalf("cumulative_revenue should be omitted from Cortex metrics")
	}
	if _, ok := exprs["order_conversion"]; ok {
		t.Fatalf("order_conversion should be omitted from Cortex metrics")
	}
	// ...and surfaced as custom_instructions notes.
	for _, want := range []string{"cumulative_revenue", "order_conversion"} {
		if !strings.Contains(out, want) {
			t.Fatalf("cortex custom_instructions missing %q:\n%s", want, out)
		}
	}
}

func TestCumulativeConversionSupersimpleDegrades(t *testing.T) {
	m, err := dbt{}.Parse("testdata/dbt_cumulative_conversion")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	notes := readFile(t, filepath.Join(dir, "NOTES.md"))
	for _, want := range []string{"cumulative_revenue", "cumulative", "order_conversion", "conversion"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("supersimple NOTES.md missing %q:\n%s", want, notes)
		}
	}
}
