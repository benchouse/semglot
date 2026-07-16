package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/layer"
	"gopkg.in/yaml.v3"
)

func emitTarget(t *testing.T, target, file string) string {
	t.Helper()
	e, err := layer.AsEmitter(target)
	if err != nil {
		t.Fatalf("AsEmitter(%s): %v", target, err)
	}
	if c, ok := e.(layer.Configurable); ok {
		e = c.WithOptions(layer.Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce"})
	}
	p, err := layer.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.Parse(sourceDirs...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSemanticViewStructure(t *testing.T) {
	got := emitTarget(t, "snowflake-semantic-view", "definition.md")
	for _, want := range []string{
		"create or replace semantic view ANALYTICS.MAIN.ECOMMERCE",
		"FCT_ORDERS as ANALYTICS.MAIN.FCT_ORDERS primary key (ORDER_ID)",
		"FCT_ORDER_LINES_FCT_ORDERS as FCT_ORDER_LINES(ORDER_ID) references FCT_ORDERS(ORDER_ID)",
		// Derived metrics reference their component metrics by qualified name
		// (Snowflake rejects inlined aggregates in a derived metric).
		"FCT_ORDERS.AOV as FCT_ORDERS.NET_REVENUE / FCT_ORDERS.ORDERS",
		"FCT_ORDER_LINES.UNITS_PER_ORDER as FCT_ORDER_LINES.UNITS_SOLD / FCT_ORDERS.ORDERS",
		"FCT_ORDERS.REFUND_RATE as FCT_ORDERS.REFUNDED_ORDERS / FCT_ORDERS.ORDERS",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("definition.md missing %q", want)
		}
	}
}

// semanticViewGoldenPath is the pinned snowflake-semantic-view definition.md,
// generated with UPDATE_GOLDEN=1 and eyeballed for well-formed DDL.
const semanticViewGoldenPath = "models/ecommerce/dbt/snowflake-semantic-view/definition.md"

// TestSemanticViewGolden pins the full emitted definition.md, mirroring
// TestEcommerceCortexGolden's shape.
func TestSemanticViewGolden(t *testing.T) {
	got := emitTarget(t, "snowflake-semantic-view", "definition.md")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(semanticViewGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(semanticViewGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(semanticViewGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("definition.md output != golden:\n--- got ---\n%s", got)
	}
}

func TestNaoYamlStructure(t *testing.T) {
	var doc struct {
		Dimensions []struct {
			Name, Type string
		} `yaml:"dimensions"`
		Metrics []struct {
			Name, Type, Formula string
			Source              struct {
				Table, Column, Aggregation string
			} `yaml:"source"`
		} `yaml:"metrics"`
	}
	if err := yaml.Unmarshal([]byte(emitTarget(t, "nao-yaml", "semantic.yaml")), &doc); err != nil {
		t.Fatal(err)
	}
	metric := func(name string) (typ, formula, table, col, agg string) {
		for _, m := range doc.Metrics {
			if m.Name == name {
				return m.Type, m.Formula, m.Source.Table, m.Source.Column, m.Source.Aggregation
			}
		}
		t.Fatalf("metric %q not found", name)
		return
	}
	_, _, table, col, agg := metric("net_revenue")
	assertStr(t, "net_revenue source", table+"/"+col+"/"+agg, "fct_orders/order_net_booked/SUM")
	_, _, _, _, ordersAgg := metric("orders")
	assertStr(t, "orders aggregation", ordersAgg, "COUNT_DISTINCT")
	typ, formula, _, _, _ := metric("refund_rate")
	assertStr(t, "refund_rate type", typ, "derived")
	if !strings.Contains(formula, "count(distinct fct_orders.order_id)") {
		t.Errorf("refund_rate formula = %q", formula)
	}
	// compound simple metric degrades to a derived formula (no clean source column)
	rtyp, rformula, _, _, _ := metric("refunded_orders")
	assertStr(t, "refunded_orders type", rtyp, "derived")
	if !strings.Contains(rformula, "case when fct_orders.is_refunded then 1 else 0 end") {
		t.Errorf("refunded_orders formula = %q", rformula)
	}
	hasDim := func(name, typ string) bool {
		for _, d := range doc.Dimensions {
			if d.Name == name && d.Type == typ {
				return true
			}
		}
		return false
	}
	if !hasDim("order_date", "date") {
		t.Error("missing dimension order_date:date")
	}
	if !hasDim("customer_segment", "categorical") {
		t.Error("missing dimension customer_segment:categorical")
	}
}

// naoYamlGoldenPath is the pinned nao-yaml semantic.yaml, generated with
// UPDATE_GOLDEN=1 and eyeballed for well-formed, deduped YAML.
const naoYamlGoldenPath = "models/ecommerce/dbt/nao-yaml/semantic.yaml"

// TestNaoYamlGolden pins the full emitted semantic.yaml, mirroring
// TestSemanticViewGolden's shape.
func TestNaoYamlGolden(t *testing.T) {
	got := emitTarget(t, "nao-yaml", "semantic.yaml")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(naoYamlGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(naoYamlGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(naoYamlGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("semantic.yaml output != golden:\n--- got ---\n%s", got)
	}
}

func TestContextRulesStructure(t *testing.T) {
	got := emitTarget(t, "nao-context-rules", "RULES.md")
	for _, want := range []string{
		"## Key metrics reference",
		"## Joins & routing",
		"**Average order value**",
		"`sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)`",
		"fct_order_lines.order_id → fct_orders.order_id",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RULES.md missing %q", want)
		}
	}
}

// contextRulesGoldenPath is the pinned nao-context-rules RULES.md, generated
// with UPDATE_GOLDEN=1 and eyeballed for well-formed prose.
const contextRulesGoldenPath = "models/ecommerce/dbt/nao-context-rules/RULES.md"

// TestContextRulesGolden pins the full emitted RULES.md, mirroring
// TestSemanticViewGolden's shape.
func TestContextRulesGolden(t *testing.T) {
	got := emitTarget(t, "nao-context-rules", "RULES.md")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(contextRulesGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(contextRulesGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(contextRulesGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("RULES.md output != golden:\n--- got ---\n%s", got)
	}
}
