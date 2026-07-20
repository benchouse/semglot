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
	e := lightdash{}.WithOptions(opts)
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatalf("read schema.yml: %v", err)
	}
	return string(b)
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
