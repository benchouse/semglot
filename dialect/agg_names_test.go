package dialect

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// inlinedAggModel is the shape this file exists for: a metric whose definition
// inlines two aggregates instead of referencing named metrics. Five emitters
// render it directly; the four that reference metrics by name cannot, and
// hoistInlineAggs is what lets them. sum(amount) is ALSO published as the
// metric `revenue`, so the reuse path is exercised alongside the minting one.
func inlinedAggModel() *ir.Model {
	amount := ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}
	cost := ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "cost"}}
	return &ir.Model{
		Tables: []ir.Table{{
			Name:       "orders",
			PrimaryKey: []string{"order_id"},
			Dimensions: []ir.Field{
				{Name: "order_id", Expr: "order_id"},
				{Name: "amount", Expr: "amount"},
				{Name: "cost", Expr: "cost"},
			},
			Measures: []ir.Measure{{Field: ir.Field{Name: "revenue", Expr: "amount"}, Agg: "sum"}},
			Metrics: []ir.Metric{
				{Name: "revenue", Def: amount},
				{Name: "margin_rate", Def: ir.Binary{Op: "/", Left: amount, Right: cost}},
			},
		}},
	}
}

// TestHoistInlineAggsNamesLeaves pins the naming scheme and the reuse rule: an
// inlined aggregate the model already publishes under a name reuses that name,
// and one it does not gets <column>_<agg> plus a backing measure.
func TestHoistInlineAggsNamesLeaves(t *testing.T) {
	m := inlinedAggModel()
	h := hoistInlineAggs(m)
	got := h.metricsFor(m.Tables[0])

	want := ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "cost_sum"}}
	if !reflect.DeepEqual(got[1].Def, want) {
		t.Errorf("margin_rate def = %+v, want %+v", got[1].Def, want)
	}
	if len(got) != 3 || got[2].Name != "cost_sum" {
		t.Fatalf("metrics = %v, want revenue, margin_rate and a synthesised cost_sum", metricNamesOf(got))
	}
	if !strings.Contains(got[2].Description, "margin_rate") {
		t.Errorf("synthesised metric description = %q, want it to name the metric it was minted for", got[2].Description)
	}
	wantMeasures := []ir.Measure{
		{Field: ir.Field{Name: "revenue", Expr: "amount"}, Agg: "sum"},
		{Field: ir.Field{Name: "cost_sum", Expr: "cost"}, Agg: "sum"},
	}
	if ms := h.measuresFor(m.Tables[0]); !reflect.DeepEqual(ms, wantMeasures) {
		t.Errorf("measures = %+v, want %+v", ms, wantMeasures)
	}
}

// TestHoistInlineAggsDoesNotMutate guards the emitter contract: the hoist reads
// the model and builds new values, so an emitter calling it leaves the IR
// exactly as it found it.
func TestHoistInlineAggsDoesNotMutate(t *testing.T) {
	m := inlinedAggModel()
	before := inlinedAggModel()
	hoistInlineAggs(m)
	if !reflect.DeepEqual(m, before) {
		t.Errorf("hoistInlineAggs mutated the model:\n got %+v\nwant %+v", m, before)
	}
}

// TestHoistInlineAggsAvoidsNameCollisions covers the fallback. A dimension
// already called cost_sum would otherwise be shadowed by the minted metric —
// and in Lightdash, where dimensions and metrics share one namespace and the
// dimension wins, that would silently drop the metric being rescued.
func TestHoistInlineAggsAvoidsNameCollisions(t *testing.T) {
	for _, tc := range []struct {
		label string
		taken ir.Field
	}{
		// The IR name is what dbt and snowflake-semantic-view collide on.
		{"dimension name", ir.Field{Name: "cost_sum", Expr: "cost_total"}},
		// The physical column is what LIGHTDASH collides on: columns[] is keyed
		// by column and every entry becomes a dimension there.
		{"physical column", ir.Field{Name: "cost_total", Expr: "cost_sum"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			m := inlinedAggModel()
			m.Tables[0].Dimensions = append(m.Tables[0].Dimensions, tc.taken)
			got := hoistInlineAggs(m).metricsFor(m.Tables[0])
			if n := got[len(got)-1].Name; n != "cost_sum_2" {
				t.Errorf("minted name = %q, want cost_sum_2 (cost_sum is taken)", n)
			}
		})
	}
}

// TestHoistInlineAggsHomesLeavesOnTheirOwnTable pins that a cross-table
// operand's synthesised metric lands on the table its column belongs to, not on
// the table owning the metric being rewritten: a measure planted on the wrong
// semantic model would be a plausible, wrong definition.
func TestHoistInlineAggsHomesLeavesOnTheirOwnTable(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{
		{
			Name:       "orders",
			Dimensions: []ir.Field{{Name: "amount", Expr: "amount"}},
			Metrics: []ir.Metric{{Name: "per_customer", Def: ir.Binary{Op: "/",
				Left:  ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}},
				Right: ir.Agg{Func: "count_distinct", Table: "customers", Arg: ir.Col{Table: "customers", Name: "customer_id"}},
			}}},
		},
		{Name: "customers", Dimensions: []ir.Field{{Name: "customer_id", Expr: "customer_id"}}},
	}}
	h := hoistInlineAggs(m)
	if got := metricNamesOf(h.metricsFor(m.Tables[1])); !reflect.DeepEqual(got, []string{"customer_id_count_distinct"}) {
		t.Errorf("customers metrics = %v, want the count_distinct operand homed there", got)
	}
	if got := metricNamesOf(h.metricsFor(m.Tables[0])); !reflect.DeepEqual(got, []string{"per_customer", "amount_sum"}) {
		t.Errorf("orders metrics = %v, want only the sum operand homed there", got)
	}
}

// TestDBTEmitInlinedAggsRoundTrip is the end-to-end claim: a metric dbt used to
// drop silently now emits as a ratio over named metrics, and re-parses.
func TestDBTEmitInlinedAggsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	warnings, err := dbt{}.Emit(inlinedAggModel(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (every metric is expressible)", warnings)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ecommerce.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"numerator: revenue",
		"denominator: cost_sum",
		"- name: cost_sum\n        agg: sum\n        expr: cost",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("emitted dbt missing %q:\n%s", want, b)
		}
	}
	back, err := dbt{}.Parse(dir)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if got := metricNamesOf(back.Tables[0].Metrics); !reflect.DeepEqual(got, []string{"revenue", "cost_sum", "margin_rate"}) {
		t.Errorf("re-parsed metrics = %v", got)
	}
}

// TestDBTEmitCountStarGetsAMeasure covers the second silent drop: COUNT(*) has
// no column, so measureFor matched nothing and the metric vanished. dbt spells a
// row count as agg: count over expr: 1.
func TestDBTEmitCountStarGetsAMeasure(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id"}},
		Metrics: []ir.Metric{
			{Name: "order_count", Def: ir.Agg{Func: "count", Table: "orders", Arg: nil}},
		},
	}}}
	dir := t.TempDir()
	warnings, err := dbt{}.Emit(m, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ecommerce.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- name: order_count\n        agg: count\n        expr: \"1\"", "measure: order_count"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("emitted dbt missing %q:\n%s", want, b)
		}
	}
}

// TestDBTEmitWarnsOnUnexpressibleMetric is the merge-blocking half: what naming
// cannot rescue must still be reported, never dropped with exit code 0. A bare
// column operand is not an aggregate, so no name can be minted for it.
func TestDBTEmitWarnsOnUnexpressibleMetric(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "amount", Expr: "amount"}, {Name: "cost", Expr: "cost"}},
		Metrics: []ir.Metric{{Name: "raw_ratio", Def: ir.Binary{Op: "/",
			Left:  ir.Col{Table: "orders", Name: "amount"},
			Right: ir.Col{Table: "orders", Name: "cost"},
		}}},
	}}}
	warnings, err := dbt{}.Emit(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "raw_ratio") {
		t.Fatalf("warnings = %v, want one naming raw_ratio", warnings)
	}
}

// TestDBTEmitWarnsOnUnbackableAggregate is the same guarantee for the other
// site: an aggregate over an argument with no measure form cannot be given a
// backing measure, so it must be reported rather than dropped.
func TestDBTEmitWarnsOnUnbackableAggregate(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "amount", Expr: "amount"}},
		Metrics: []ir.Metric{{Name: "odd", Def: ir.Agg{Func: "sum", Table: "orders",
			Arg: ir.Binary{Op: "+", Left: ir.Col{Name: "amount"}, Right: ir.Lit{Value: "1"}}}}},
	}}}
	warnings, err := dbt{}.Emit(m, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "odd") {
		t.Fatalf("warnings = %v, want one naming odd", warnings)
	}
}

func metricNamesOf(ms []ir.Metric) []string {
	out := make([]string, len(ms))
	for i, mt := range ms {
		out[i] = mt.Name
	}
	return out
}
