package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// emitOssie emits m and returns the parsed semantic_model.yaml plus its raw text.
func emitOssie(t *testing.T, m *ir.Model, opts Options) (osiFile, string) {
	t.Helper()
	e := ossie{}.WithOptions(opts)
	out := t.TempDir()
	if _, err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	return f, string(b)
}

func TestOssieEmitDatasets(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:        "orders",
		Description: "Customer orders.",
		Synonyms:    []string{"purchases"},
		PrimaryKey:  []string{"order_id", "line_number"},
		Grain:       "order_date",
		Dimensions: []ir.Field{{
			Name: "status", Expr: "status", DataType: "varchar",
			Description: "Order status.",
			Synonyms:    []string{"state"},
			Enum:        []ir.EnumValue{{Value: "placed"}, {Value: "shipped", Description: "left the warehouse"}},
		}},
		TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date", DataType: "date"}},
	}}}

	f, raw := emitOssie(t, m, Options{Database: "ANALYTICS", Schema: "MAIN", Name: "sales", Description: "Sales."})

	if f.Version != "0.2.0.dev0" {
		t.Errorf("version = %q, want 0.2.0.dev0", f.Version)
	}
	if len(f.SemanticModel) != 1 {
		t.Fatalf("want 1 semantic_model entry, got %d", len(f.SemanticModel))
	}
	sm := f.SemanticModel[0]
	if sm.Name != "sales" || sm.Description != "Sales." {
		t.Errorf("model identity = %q / %q", sm.Name, sm.Description)
	}
	if len(sm.Datasets) != 1 {
		t.Fatalf("want 1 dataset, got %d", len(sm.Datasets))
	}
	ds := sm.Datasets[0]
	if ds.Source != "ANALYTICS.MAIN.orders" {
		t.Errorf("source = %q, want ANALYTICS.MAIN.orders", ds.Source)
	}
	if len(ds.PrimaryKey) != 2 {
		t.Errorf("primary_key = %v, want both columns", ds.PrimaryKey)
	}
	if ds.AIContext == nil || len(ds.AIContext.Synonyms) != 1 {
		t.Errorf("dataset ai_context.synonyms missing: %+v", ds.AIContext)
	}
	// Grain has no OSI slot and folds into the dataset description.
	if !strings.Contains(ds.Description, "order_date") {
		t.Errorf("dataset description missing the grain: %q", ds.Description)
	}

	byName := map[string]osiField{}
	for _, fl := range ds.Fields {
		byName[fl.Name] = fl
	}
	status, ok := byName["status"]
	if !ok {
		t.Fatalf("no status field in %v", ds.Fields)
	}
	if status.DataType != "String" {
		t.Errorf("status datatype = %q, want String", status.DataType)
	}
	if status.Dimension == nil || status.Dimension.IsTime == nil || *status.Dimension.IsTime {
		t.Errorf("status should be marked is_time: false, got %+v", status.Dimension)
	}
	// Enum has no OSI slot and folds into the field description.
	if !strings.Contains(status.Description, "placed") {
		t.Errorf("status description missing folded enum: %q", status.Description)
	}
	od, ok := byName["order_date"]
	if !ok {
		t.Fatalf("no order_date field")
	}
	if od.Dimension == nil || od.Dimension.IsTime == nil || !*od.Dimension.IsTime {
		t.Errorf("order_date should be marked is_time: true, got %+v", od.Dimension)
	}

	// The reference converter emits a top-level dialects: key that the published
	// osi-schema.json rejects (additionalProperties: false). semglot must not.
	if strings.Contains(raw, "\ndialects:") {
		t.Errorf("emitted a top-level dialects: key, which osi-schema.json rejects:\n%s", raw)
	}
}

// TestOssieEmitUnqualifiedSource degrades gracefully when no database is set.
func TestOssieEmitUnqualifiedSource(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "orders"}}}
	f, _ := emitOssie(t, m, Options{Name: "sales"})
	if got := f.SemanticModel[0].Datasets[0].Source; got != "orders" {
		t.Errorf("source = %q, want bare table name when no database is set", got)
	}
}

// TestOssieEmitPrefersDeclaredSource covers task 16's defect: an OSI model
// declaring distinct physical sources must emit those SAME sources on the way
// back out, not profile-reconstructed ones fabricated from Options{Database,
// Schema} + the table name. Two tables with different databases prove the
// profile's single Database/Schema pair is never consulted when Source is set.
func TestOssieEmitPrefersDeclaredSource(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{
		{Name: "orders", Source: "PROD.SALES.orders_v1"},
		{Name: "customer_master", Source: "OTHERDB.CRM.customer_master"},
	}}
	f, _ := emitOssie(t, m, Options{Database: "WAREHOUSE", Schema: "PUBLIC", Name: "sales"})
	byName := map[string]osiDataset{}
	for _, ds := range f.SemanticModel[0].Datasets {
		byName[ds.Name] = ds
	}
	if got := byName["orders"].Source; got != "PROD.SALES.orders_v1" {
		t.Errorf("orders source = %q, want the declared PROD.SALES.orders_v1, not a WAREHOUSE.PUBLIC reconstruction", got)
	}
	if got := byName["customer_master"].Source; got != "OTHERDB.CRM.customer_master" {
		t.Errorf("customer_master source = %q, want the declared OTHERDB.CRM.customer_master, not a WAREHOUSE.PUBLIC reconstruction", got)
	}
}

// TestOssieEmitDbtSourcedModelReconstructsFromProfile guards the fallback this
// task's safety net depends on: a table with no Source (the dbt path — a dbt
// model has no physical address of its own, since model: ref() is resolved by
// dbt itself) must still degrade to today's Options{Database,Schema}+name
// reconstruction, unchanged.
func TestOssieEmitDbtSourcedModelReconstructsFromProfile(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "orders"}}} // Source left zero-value, as dbt.Parse leaves it
	f, _ := emitOssie(t, m, Options{Database: "ANALYTICS", Schema: "MAIN", Name: "sales"})
	if got := f.SemanticModel[0].Datasets[0].Source; got != "ANALYTICS.MAIN.orders" {
		t.Errorf("source = %q, want the profile-reconstructed ANALYTICS.MAIN.orders when no Source is declared", got)
	}
}

// TestOssieEmitUnmappedDataType covers a SQL type the IR carries but that has
// no OSI portable-vocabulary equivalent (irToOSIType returns ""). Omitting
// datatype in that case is correct per spec guidance, but silently dropping
// the fact that the IR *did* have a type we couldn't map would violate the
// "nothing dropped silently" rule — so Emit must return a warning naming the
// field and the unmapped type, in addition to omitting datatype from the YAML.
func TestOssieEmitUnmappedDataType(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "events",
		Dimensions: []ir.Field{{
			Name: "payload", Expr: "payload", DataType: "variant",
		}},
	}}}

	e := ossie{}.WithOptions(Options{Name: "evt"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "payload") && strings.Contains(w, "variant") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming field %q and unmapped type %q", warnings, "payload", "variant")
	}

	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	fld := f.SemanticModel[0].Datasets[0].Fields[0]
	if fld.DataType != "" {
		t.Errorf("datatype = %q, want omitted for an unmapped SQL type", fld.DataType)
	}
}

// TestOssieEmitFieldDedupSameExpr covers a column that is both a plain
// dimension and a measure's operand: the same underlying column ("amount")
// declared under the same field name twice (once via Dimensions, once via
// Measures). OSI documents field name as unique within a dataset, so the
// second, identical declaration must be dropped — but silently, because
// nothing is lost: both occurrences describe the exact same column.
func TestOssieEmitFieldDedupSameExpr(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "amount", Expr: "amount"}},
		Measures:   []ir.Measure{{Field: ir.Field{Name: "amount", Expr: "amount"}, Agg: "sum"}},
	}}}

	e := ossie{}.WithOptions(Options{Name: "sales"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "amount") {
			t.Errorf("unexpected warning for a same-name/same-expression duplicate: %q", w)
		}
	}

	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	fields := f.SemanticModel[0].Datasets[0].Fields
	if len(fields) != 1 {
		t.Fatalf("want 1 deduplicated field, got %d: %+v", len(fields), fields)
	}
	if fields[0].Name != "amount" {
		t.Errorf("field name = %q, want amount", fields[0].Name)
	}
}

// TestOssieEmitFieldDedupDifferentExpr covers a genuine name collision: two
// different columns end up wanting to declare a field under the same name
// ("total"). OSI requires field names unique within a dataset, so only the
// first (Dimensions before Measures) can be kept; dropping the second is real
// information loss and must surface as a returned warning naming both
// expressions.
func TestOssieEmitFieldDedupDifferentExpr(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "total", Expr: "amount"}},
		Measures:   []ir.Measure{{Field: ir.Field{Name: "total", Expr: "amount_usd"}, Agg: "sum"}},
	}}}

	e := ossie{}.WithOptions(Options{Name: "sales"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "total") && strings.Contains(w, "amount") && strings.Contains(w, "amount_usd") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming field %q and both expressions %q / %q",
			warnings, "total", "amount", "amount_usd")
	}

	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	fields := f.SemanticModel[0].Datasets[0].Fields
	if len(fields) != 1 {
		t.Fatalf("want 1 field (first occurrence kept), got %d: %+v", len(fields), fields)
	}
	if fields[0].Name != "total" {
		t.Errorf("field name = %q, want total", fields[0].Name)
	}
	if got := fieldExprText(fields[0]); got != "amount" {
		t.Errorf("kept field expression = %q, want the first (Dimensions) occurrence %q", got, "amount")
	}
}

func TestOssieEmitMetricsAndRelationships(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name:       "orders",
				Dimensions: []ir.Field{{Name: "customer_id", Expr: "customer_id"}},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "revenue", Expr: "amount", Description: "Revenue."}, Agg: "sum"},
				},
				Metrics: []ir.Metric{
					{Name: "revenue", Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}},
					{Name: "aov", Label: "Average order value", Def: ir.Binary{
						Op:    "/",
						Left:  ir.Ref{Metric: "revenue"},
						Right: ir.Agg{Func: "count", Table: "orders", Arg: nil},
					}},
				},
			},
			{Name: "customers", Dimensions: []ir.Field{{Name: "id", Expr: "id"}}},
		},
		Relationships: []ir.Relationship{
			{Left: "orders", Right: "customers", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "id"}}},
		},
	}

	f, _ := emitOssie(t, m, Options{Database: "A", Schema: "M", Name: "sales"})
	sm := f.SemanticModel[0]

	if len(sm.Relationships) != 1 {
		t.Fatalf("want 1 relationship, got %d", len(sm.Relationships))
	}
	r := sm.Relationships[0]
	if r.From != "orders" || r.To != "customers" || r.Name == "" {
		t.Errorf("relationship = %+v; from must be the many side and name must be set", r)
	}

	// The measure and the metric share the name "revenue"; only one can occupy
	// the flat metrics list. The metric is canonical.
	byName := map[string]osiMetric{}
	for _, mt := range sm.Metrics {
		if _, dup := byName[mt.Name]; dup {
			t.Errorf("duplicate metric name %q in the flat list", mt.Name)
		}
		byName[mt.Name] = mt
	}
	if len(byName) != 2 {
		t.Errorf("want 2 metrics (revenue, aov), got %v", sm.Metrics)
	}
	rev, ok := byName["revenue"]
	if !ok {
		t.Fatal("no revenue metric")
	}
	if len(rev.Expression.Dialects) != 1 || rev.Expression.Dialects[0].Dialect != "ANSI_SQL" {
		t.Errorf("expression = %+v, want a single ANSI_SQL entry", rev.Expression)
	}
	if !strings.Contains(rev.Expression.Dialects[0].Expression, "orders.amount") {
		t.Errorf("revenue expression = %q, want a physical column reference", rev.Expression.Dialects[0].Expression)
	}
	// Label has no OSI slot and folds into the description.
	if !strings.Contains(byName["aov"].Description, "Average order value") {
		t.Errorf("aov description missing folded label: %q", byName["aov"].Description)
	}
}

// TestOssieEmitDegradesWindowMetrics omits a metric with no OSI primitive and
// returns a warning rather than emitting SQL semglot cannot stand behind.
func TestOssieEmitDegradesWindowMetrics(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Metrics: []ir.Metric{{
			Name: "rolling_revenue",
			Def:  ir.Window{Base: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}, Window: "30 days"},
		}},
	}}}
	e := ossie{}.WithOptions(Options{Name: "sales"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "rolling_revenue") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the degraded metric, got %v", warnings)
	}
}

// TestOssieEmitMetricGrainFoldsIntoDescription covers ir.Metric.Grain, which
// has no OSI slot. Grain is the metric's agg-time grain (the owning model's
// agg_time_dimension) — omitting it would silently drop information the IR
// carries, so it folds into the metric's description as visible prose, the
// same way Label and Dimensions already do.
func TestOssieEmitMetricGrainFoldsIntoDescription(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Metrics: []ir.Metric{{
			Name:  "revenue",
			Grain: "month",
			Def:   ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}},
		}},
	}}}
	f, _ := emitOssie(t, m, Options{Name: "sales"})
	sm := f.SemanticModel[0]
	if len(sm.Metrics) != 1 {
		t.Fatalf("want 1 metric, got %d", len(sm.Metrics))
	}
	if !strings.Contains(sm.Metrics[0].Description, "month") {
		t.Errorf("revenue description missing folded grain: %q", sm.Metrics[0].Description)
	}
}

// TestOssieEmitDuplicateMetricNameWarns covers two IR tables each publishing a
// metric of the same name. OSI has ONE flat, model-level metrics list and
// requires a unique metric name, so both cannot be written; the first wins and
// the second is reported. Emitting both would produce a document the spec
// rejects, with nothing to say it had happened.
func TestOssieEmitDuplicateMetricNameWarns(t *testing.T) {
	agg := func(table, col string) ir.Expr {
		return ir.Agg{Func: "sum", Table: table, Arg: ir.Col{Table: table, Name: col}}
	}
	m := &ir.Model{Tables: []ir.Table{
		{Name: "orders", Metrics: []ir.Metric{{Name: "revenue", Def: agg("orders", "amount")}}},
		{Name: "refunds", Metrics: []ir.Metric{{Name: "revenue", Def: agg("refunds", "amount")}}},
	}}
	e := ossie{}.WithOptions(Options{Name: "sales"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := emitOssie(t, m, Options{Name: "sales"})
	if len(f.SemanticModel[0].Metrics) != 1 {
		t.Errorf("want 1 metric under the unique-name rule, got %d", len(f.SemanticModel[0].Metrics))
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, `metric "revenue" on table "refunds" not emitted`) {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the dropped duplicate, got %v", warnings)
	}
}
