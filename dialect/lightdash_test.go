package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// emitLightdash runs the lightdash emitter (with opts) over m and returns the
// emitted schema.yml as a string.
func emitLightdash(t *testing.T, m *ir.Model, opts Options) string {
	t.Helper()
	// These tests assert WHAT is emitted, not which key path it nests under, and
	// ldDoc reads the `meta:` shape. Default them to the legacy path so a change
	// of default does not break every content test. Placement is covered by
	// TestLightdashConfigDbtMetaKeyPath; the golden covers the real default.
	if opts.DbtMetaKeyPath == "" {
		opts.DbtMetaKeyPath = "meta"
	}
	e := lightdash{}.WithOptions(opts)
	dir := t.TempDir()
	if _, err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatalf("read schema.yml: %v", err)
	}
	return string(b)
}

// ldDoc is the slice of the emitted schema the name-collision tests read: the
// column list plus both metric homes (column-level and model-level), which is
// where a collision moves a metric between.
type ldDoc struct {
	Models []ldDocModel `yaml:"models"`
}

type ldDocModel struct {
	Name string `yaml:"name"`
	Meta struct {
		PrimaryKey string              `yaml:"primary_key"`
		Metrics    map[string]ldMetric `yaml:"metrics"`
	} `yaml:"meta"`
	Columns []struct {
		Name string `yaml:"name"`
		Meta struct {
			Metrics map[string]ldMetric `yaml:"metrics"`
		} `yaml:"meta"`
	} `yaml:"columns"`
}

// column reports whether the model emits a column of that name (in Lightdash
// every emitted column is a dimension, so this is also the dimension check).
func (m ldDocModel) column(name string) (int, bool) {
	for i, c := range m.Columns {
		if c.Name == name {
			return i, true
		}
	}
	return 0, false
}

// columnMetric returns the type of the column-level metric `name` on `col`.
func (m ldDocModel) columnMetric(col, name string) (string, bool) {
	i, ok := m.column(col)
	if !ok {
		return "", false
	}
	mm, ok := m.Columns[i].Meta.Metrics[name]
	return mm.Type, ok
}

func parseLightdash(t *testing.T, s string) ldDoc {
	t.Helper()
	var doc ldDoc
	if err := yaml.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, s)
	}
	return doc
}

func TestLightdashRegisteredAndSkeleton(t *testing.T) {
	if _, err := AsEmitter("lightdash"); err != nil {
		t.Fatalf("AsEmitter(lightdash): %v", err)
	}
	m := &ir.Model{
		Tables: []ir.Table{{Name: "orders", Description: "One row per order."}},
		Notes:  []string{"a passthrough note"},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	if !strings.HasPrefix(got, "# semglot:") {
		t.Errorf("expected leading # semglot comment block, got:\n%s", got)
	}
	if !strings.Contains(got, "# - a passthrough note") {
		t.Errorf("passthrough note missing from comment block:\n%s", got)
	}

	var doc struct {
		Version int `yaml:"version"`
		Models  []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("version = %d, want 2", doc.Version)
	}
	if len(doc.Models) != 1 || doc.Models[0].Name != "orders" {
		t.Fatalf("models = %+v, want one named orders", doc.Models)
	}
	if doc.Models[0].Description != "One row per order." {
		t.Errorf("description = %q", doc.Models[0].Description)
	}
}

func TestLightdashMetrics(t *testing.T) {
	// net_revenue = sum(amount)           -> column metric on amount
	// orders      = count_distinct(order_id) -> column metric on order_id
	// aov         = net_revenue / orders   -> model metric ${net_revenue}/${orders}
	// refunded    = sum(case ...) (Raw arg) -> degrades to a note
	// refund_rate = refunded / orders      -> degrades (ref to degraded metric)
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Metrics: []ir.Metric{
			{Name: "net_revenue", Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
			{Name: "orders", Def: ir.Agg{Func: "count_distinct", Table: "orders", Arg: ir.Col{Name: "order_id"}}},
			{Name: "aov", Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "orders"}}},
			{Name: "refunded", Def: ir.Agg{Func: "sum", Table: "orders",
				Arg: ir.Raw{SQL: "case when is_refunded then 1 else 0 end", Columns: []string{"is_refunded"}}}},
			{Name: "refund_rate", Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "refunded"}, Right: ir.Ref{Metric: "orders"}}},
		},
	}}}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Meta struct {
				Metrics map[string]struct {
					Type string `yaml:"type"`
					SQL  string `yaml:"sql"`
				} `yaml:"metrics"`
			} `yaml:"meta"`
			Columns []struct {
				Name string `yaml:"name"`
				Meta struct {
					Metrics map[string]struct {
						Type string `yaml:"type"`
					} `yaml:"metrics"`
				} `yaml:"meta"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	colMetric := func(col, name string) (string, bool) {
		for _, c := range doc.Models[0].Columns {
			if c.Name == col {
				mm, ok := c.Meta.Metrics[name]
				return mm.Type, ok
			}
		}
		return "", false
	}
	if typ, ok := colMetric("amount", "net_revenue"); !ok || typ != "sum" {
		t.Errorf("net_revenue on amount: type=%q ok=%v, want sum true", typ, ok)
	}
	if typ, ok := colMetric("order_id", "orders"); !ok || typ != "count_distinct" {
		t.Errorf("orders on order_id: type=%q ok=%v, want count_distinct true", typ, ok)
	}
	aov, ok := doc.Models[0].Meta.Metrics["aov"]
	if !ok || aov.Type != "number" || aov.SQL != "${net_revenue} / ${orders}" {
		t.Errorf("aov = %+v, want type number sql ${net_revenue} / ${orders}", aov)
	}
	if _, ok := doc.Models[0].Meta.Metrics["refund_rate"]; ok {
		t.Errorf("refund_rate must NOT be emitted (references degraded metric)")
	}
	// both degraded metrics surface as notes.
	if !strings.Contains(got, "# - metric refunded not emitted") {
		t.Errorf("missing degrade note for refunded:\n%s", got)
	}
	if !strings.Contains(got, "# - metric refund_rate not emitted") {
		t.Errorf("missing degrade note for refund_rate:\n%s", got)
	}
}

// TestLightdashMetricNameCollidesWithColumn pins the fix for the single
// namespace Lightdash gives dimensions and metrics. Every entry in columns[]
// becomes a dimension, and when a metric carries the same name Lightdash keeps
// the dimension and drops the metric ("Skipped metric X because a dimension
// with the same name exists. Dimensions take priority."), visible only in
// deploy warnings. Both shapes seen in the wild are covered:
//
//	attributed_revenue = sum(attributed_revenue) -> the metric's own backing
//	    column is the colliding dimension (the column exists only to host it)
//	roas = ${attributed_revenue}/${ad_spend}     -> a model-level derived metric
//	    collides with a precomputed raw column that IS a declared dimension
//
// Both must survive as metrics; the colliding columns must be gone, and each
// drop must be noted.
func TestLightdashMetricNameCollidesWithColumn(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "obt_marketing_daily",
		Dimensions: []ir.Field{
			{Name: "platform", Expr: "platform", DataType: "varchar"},
			{Name: "roas", Expr: "roas", DataType: "double", Description: "Precomputed daily ROAS."},
		},
		Metrics: []ir.Metric{
			{Name: "attributed_revenue", Def: ir.Agg{Func: "sum", Table: "obt_marketing_daily",
				Arg: ir.Col{Name: "attributed_revenue"}}},
			{Name: "ad_spend", Def: ir.Agg{Func: "sum", Table: "obt_marketing_daily",
				Arg: ir.Col{Name: "spend"}}},
			{Name: "roas", Def: ir.Binary{Op: "/",
				Left: ir.Ref{Metric: "attributed_revenue"}, Right: ir.Ref{Metric: "ad_spend"}}},
		},
	}}}
	got := emitLightdash(t, m, Options{Name: "marketing"})
	doc := parseLightdash(t, got)

	// The two colliding columns are gone; the non-colliding ones stay.
	for _, tc := range []struct {
		col  string
		want bool
	}{
		{"platform", true},            // plain dimension, no metric of that name
		{"spend", true},               // hosts ad_spend, whose name differs
		{"attributed_revenue", false}, // dropped: metric of the same name wins
		{"roas", false},               // dropped: metric of the same name wins
	} {
		if _, ok := doc.Models[0].column(tc.col); ok != tc.want {
			t.Errorf("column %q present = %v, want %v\n%s", tc.col, ok, tc.want, got)
		}
	}

	// The metric a dropped column used to host is re-homed at model level with
	// an explicit ${TABLE}.col sql: the same aggregation, just spelled out.
	ar, ok := doc.Models[0].Meta.Metrics["attributed_revenue"]
	if !ok || ar.Type != "sum" || ar.SQL != "${TABLE}.attributed_revenue" {
		t.Errorf("attributed_revenue model metric = %+v ok=%v, want type sum sql ${TABLE}.attributed_revenue\n%s", ar, ok, got)
	}
	// The derived metric keeps its name and its references, which still resolve:
	// re-homing moves a metric, it never removes one.
	roas, ok := doc.Models[0].Meta.Metrics["roas"]
	if !ok || roas.Type != "number" || roas.SQL != "${attributed_revenue} / ${ad_spend}" {
		t.Errorf("roas model metric = %+v ok=%v, want type number sql ${attributed_revenue} / ${ad_spend}\n%s", roas, ok, got)
	}
	// ad_spend is untouched: no collision, so it stays column-level.
	if typ, ok := doc.Models[0].columnMetric("spend", "ad_spend"); !ok || typ != "sum" {
		t.Errorf("ad_spend on spend: type=%q ok=%v, want sum true\n%s", typ, ok, got)
	}
	// Neither drop is silent.
	for _, want := range []string{
		"# - table obt_marketing_daily: column attributed_revenue not emitted as a dimension",
		"# - table obt_marketing_daily: column roas not emitted as a dimension",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing collision note %q:\n%s", want, got)
		}
	}
}

// TestLightdashCollisionOnKeyColumnDegradesMetric pins the exception to the
// metric-wins rule: a column named by meta.primary_key or by a join's sql_on is
// resolved structurally, so dropping it would leave that reference pointing at a
// dimension that no longer exists and Lightdash rejects the whole explore. There
// the dimension stays and the metric degrades to a note instead.
func TestLightdashCollisionOnKeyColumnDegradesMetric(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{{
			Name:       "fct_orders",
			PrimaryKey: []string{"order_id"},
			Dimensions: []ir.Field{
				{Name: "order_id", Expr: "order_id"},
				{Name: "customer_id", Expr: "customer_id"},
			},
			Metrics: []ir.Metric{
				// collides with the primary key column
				{Name: "order_id", Def: ir.Agg{Func: "count_distinct", Table: "fct_orders",
					Arg: ir.Col{Name: "order_id"}}},
				// collides with a join key column
				{Name: "customer_id", Def: ir.Agg{Func: "count_distinct", Table: "fct_orders",
					Arg: ir.Col{Name: "customer_id"}}},
			},
		}},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
		},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})
	doc := parseLightdash(t, got)

	for _, col := range []string{"order_id", "customer_id"} {
		if _, ok := doc.Models[0].column(col); !ok {
			t.Errorf("column %q must be kept (primary/join key)\n%s", col, got)
		}
		if _, ok := doc.Models[0].Meta.Metrics[col]; ok {
			t.Errorf("metric %q must NOT be emitted (collides with a key column)\n%s", col, got)
		}
		if _, ok := doc.Models[0].columnMetric(col, col); ok {
			t.Errorf("metric %q must NOT be emitted column-level either\n%s", col, got)
		}
		if !strings.Contains(got, "# - metric "+col+" not emitted to Lightdash: its name collides with column "+col) {
			t.Errorf("missing degrade note for metric %q:\n%s", col, got)
		}
	}
	// The structural references still resolve.
	if doc.Models[0].Meta.PrimaryKey != "order_id" {
		t.Errorf("primary_key = %q, want order_id", doc.Models[0].Meta.PrimaryKey)
	}
}

func TestLightdashDimensions(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Dimensions: []ir.Field{
			{Name: "status", Expr: "status", Description: "Order state.",
				Enum: []ir.EnumValue{{Value: "paid"}, {Value: "refunded", Description: "money returned"}}},
			{Name: "is_first_order", Expr: "is_first_order"},
			{Name: "customer_sk", Expr: "customer_sk"},
			{Name: "region", Expr: "region", DataType: "varchar", Synonyms: []string{"area"}},
		},
		TimeDimensions: []ir.Field{
			{Name: "order_date", Expr: "order_date"},
		},
	}}}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Columns []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Meta        struct {
					Dimension struct {
						Type  string `yaml:"type"`
						Label string `yaml:"label"`
					} `yaml:"dimension"`
				} `yaml:"meta"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	cols := map[string]struct {
		desc, typ string
	}{}
	for _, c := range doc.Models[0].Columns {
		cols[c.Name] = struct{ desc, typ string }{c.Description, c.Meta.Dimension.Type}
	}

	// enum meanings fold into the description; bare values are dropped (no slot).
	if !strings.Contains(cols["status"].desc, "refunded = money returned") {
		t.Errorf("status description missing enum meaning: %q", cols["status"].desc)
	}
	// synonyms fold into the description.
	if !strings.Contains(cols["region"].desc, "Synonyms: area") {
		t.Errorf("region description missing synonyms: %q", cols["region"].desc)
	}
	// type from name/DataType signals; order_date is a time dimension.
	if cols["is_first_order"].typ != "boolean" {
		t.Errorf("is_first_order type = %q, want boolean", cols["is_first_order"].typ)
	}
	if cols["customer_sk"].typ != "number" {
		t.Errorf("customer_sk type = %q, want number", cols["customer_sk"].typ)
	}
	if cols["region"].typ != "string" {
		t.Errorf("region type = %q, want string", cols["region"].typ)
	}
	if cols["order_date"].typ != "timestamp" {
		t.Errorf("order_date type = %q, want timestamp", cols["order_date"].typ)
	}
	// no confident signal and no DataType => type omitted (empty).
	if cols["status"].typ != "" {
		t.Errorf("status type = %q, want empty (omitted)", cols["status"].typ)
	}
}

func TestLightdashJoinsAndPrimaryKey(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{Name: "order_lines", PrimaryKey: []string{"order_line_id"}},
			{Name: "orders", PrimaryKey: []string{"order_id", "tenant_id"}}, // composite -> note
		},
		Relationships: []ir.Relationship{
			{Left: "order_lines", Right: "orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
		},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Name string `yaml:"name"`
			Meta struct {
				PrimaryKey string `yaml:"primary_key"`
				Joins      []struct {
					Join         string `yaml:"join"`
					SQLOn        string `yaml:"sql_on"`
					Relationship string `yaml:"relationship"`
				} `yaml:"joins"`
			} `yaml:"meta"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	byName := map[string]int{}
	for i, mdl := range doc.Models {
		byName[mdl.Name] = i
	}
	ol := doc.Models[byName["order_lines"]]
	if ol.Meta.PrimaryKey != "order_line_id" {
		t.Errorf("order_lines primary_key = %q, want order_line_id", ol.Meta.PrimaryKey)
	}
	if len(ol.Meta.Joins) != 1 {
		t.Fatalf("order_lines joins = %d, want 1", len(ol.Meta.Joins))
	}
	j := ol.Meta.Joins[0]
	if j.Join != "orders" || j.Relationship != "many-to-one" ||
		j.SQLOn != "${order_lines.order_id} = ${orders.order_id}" {
		t.Errorf("join = %+v", j)
	}
	// composite PK is not emitted and surfaces as a note.
	if doc.Models[byName["orders"]].Meta.PrimaryKey != "" {
		t.Errorf("composite PK must not be emitted")
	}
	if !strings.Contains(got, "composite primary key") {
		t.Errorf("missing composite-PK note:\n%s", got)
	}
}

func TestLightdashConfigDbtMetaKeyPath(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		PrimaryKey: []string{"order_id"},
		Dimensions: []ir.Field{{Name: "status", Expr: "status", DataType: "varchar"}},
		Metrics: []ir.Metric{
			{Name: "net_revenue", Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
		},
	}}}

	// Default (an unset DbtMetaKeyPath) is config.meta, dbt's preferred form
	// since 1.10. Built directly, not via emitLightdash, which overrides the
	// default for the content tests.
	dir := t.TempDir()
	if _, err := (lightdash{}.WithOptions(Options{Name: "ecommerce"})).Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatalf("read schema.yml: %v", err)
	}
	if def := string(b); !strings.Contains(def, "config:") {
		t.Errorf("default should nest under config.meta:, got:\n%s", def)
	}

	// Explicit "meta" opts back into top-level meta, for dbt 1.9 and earlier.
	legacy := emitLightdash(t, m, Options{Name: "ecommerce", DbtMetaKeyPath: "meta"})
	if !strings.Contains(legacy, "\n    meta:\n") {
		t.Errorf("DbtMetaKeyPath=meta should nest under meta:, got:\n%s", legacy)
	}
	if strings.Contains(legacy, "config:") {
		t.Errorf("DbtMetaKeyPath=meta must not emit config:, got:\n%s", legacy)
	}

	// config.meta style: meta nested under config.
	got := emitLightdash(t, m, Options{Name: "ecommerce", DbtMetaKeyPath: "config.meta"})
	var doc struct {
		Models []struct {
			Config struct {
				Meta struct {
					PrimaryKey string `yaml:"primary_key"`
				} `yaml:"meta"`
			} `yaml:"config"`
			Meta struct {
				PrimaryKey string `yaml:"primary_key"`
			} `yaml:"meta"`
			Columns []struct {
				Config struct {
					Meta struct {
						Dimension struct {
							Type string `yaml:"type"`
						} `yaml:"dimension"`
					} `yaml:"meta"`
				} `yaml:"config"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if doc.Models[0].Config.Meta.PrimaryKey != "order_id" {
		t.Errorf("config.meta model primary_key = %q, want order_id\n%s", doc.Models[0].Config.Meta.PrimaryKey, got)
	}
	if doc.Models[0].Meta.PrimaryKey != "" {
		t.Errorf("config.meta style must not also emit top-level meta")
	}
	if doc.Models[0].Columns[0].Config.Meta.Dimension.Type != "string" {
		t.Errorf("config.meta column dimension type = %q, want string\n%s",
			doc.Models[0].Columns[0].Config.Meta.Dimension.Type, got)
	}
}

// TestLightdashEmitsRawMeasures pins the pass that keeps measures no metric
// covers. Without it the emitter silently dropped them AND their backing
// columns: the benchmark's source had 22 measures behind 14 metrics, so clicks
// and impressions vanished entirely and 34 of 38 explores ended up with no
// metric at all.
func TestLightdashEmitsRawMeasures(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "obt_marketing_daily",
		PrimaryKey: []string{"report_date"},
		Dimensions: []ir.Field{{Name: "platform", Expr: "platform", DataType: "varchar"}},
		Measures: []ir.Measure{
			{Field: ir.Field{Name: "total_clicks", Expr: "clicks"}, Agg: "sum"},
			// Not a plain column: must degrade rather than become a column name.
			{Field: ir.Field{Name: "refunded_orders", Expr: "case when is_refunded then 1 else 0 end"}, Agg: "sum"},
		},
		Metrics: []ir.Metric{
			{Name: "ad_spend", Def: ir.Agg{Func: "sum", Table: "obt_marketing_daily", Arg: ir.Col{Name: "spend"}}},
		},
	}}}

	out := emitLightdash(t, m, Options{Name: "ecommerce"})
	doc := parseLightdash(t, out)

	// The measure survives, and its backing column is created for it.
	if got, ok := doc.Models[0].columnMetric("clicks", "total_clicks"); !ok || got != "sum" {
		t.Errorf("total_clicks on clicks = %q (ok=%v), want sum\n%s", got, ok, out)
	}
	// The metric-backed measure is untouched.
	if _, ok := doc.Models[0].columnMetric("spend", "ad_spend"); !ok {
		t.Errorf("ad_spend should still be emitted\n%s", out)
	}
	// A compound expression is never used as a column name.
	for _, c := range doc.Models[0].Columns {
		if strings.Contains(c.Name, " ") {
			t.Errorf("emitted a compound expression as a column name: %q", c.Name)
		}
	}
	if !strings.Contains(out, "not a plain column") {
		t.Errorf("dropping the compound measure should be noted\n%s", out)
	}
}

// TestLightdashSynthesisesRowCount covers a dimension-only table. Lightdash does
// not require a metric, but its AI agent invents one when given nothing
// selectable, and Lightdash then compiles the unresolvable field away into
// `SELECT\n\nFROM t`, which ClickHouse rejects outright.
func TestLightdashSynthesisesRowCount(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "dim_carrier",
		PrimaryKey: []string{"carrier_code"},
		Dimensions: []ir.Field{
			{Name: "carrier_code", Expr: "carrier_code", DataType: "varchar"},
			{Name: "carrier_name", Expr: "carrier_name", DataType: "varchar"},
		},
	}}}

	out := emitLightdash(t, m, Options{Name: "ecommerce"})
	doc := parseLightdash(t, out)

	// Synthesised over the primary key, not an arbitrary column.
	if got, ok := doc.Models[0].columnMetric("carrier_code", "dim_carrier_count"); !ok || got != "count" {
		t.Errorf("dim_carrier_count on carrier_code = %q (ok=%v), want count\n%s", got, ok, out)
	}
	if !strings.Contains(out, "row-count metric") {
		t.Errorf("synthesising a row count should be noted\n%s", out)
	}
}

// TestLightdashNoRowCountWhenMetricsExist guards the other direction: a table
// that already exposes a metric must not gain a synthetic one.
func TestLightdashNoRowCountWhenMetricsExist(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "fct_orders",
		PrimaryKey: []string{"order_id"},
		Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id", DataType: "number"}},
		Metrics: []ir.Metric{
			{Name: "net_revenue", Def: ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Name: "amount"}}},
		},
	}}}

	out := emitLightdash(t, m, Options{Name: "ecommerce"})
	if strings.Contains(out, "fct_orders_count") {
		t.Errorf("must not synthesise a row count when a metric exists\n%s", out)
	}
}
