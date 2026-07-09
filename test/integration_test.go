package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/benchouse/semglot/layer"
)

const (
	projectDir = "testdata/ecommerce"
	goldenPath = "testdata/ecommerce.cortex.golden.yaml"
)

// emit runs the real dbt -> cortex pipeline through the public layer API and
// returns the emitted Cortex YAML.
func emit(t *testing.T) []byte {
	t.Helper()
	p, err := layer.AsParser("dbt")
	if err != nil {
		t.Fatalf("AsParser: %v", err)
	}
	e, err := layer.AsEmitter("cortex")
	if err != nil {
		t.Fatalf("AsEmitter: %v", err)
	}
	if c, ok := e.(layer.Configurable); ok {
		e = c.WithOptions("ANALYTICS", "MAIN", "ecommerce", "")
	}
	m, err := p.Parse(projectDir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEcommerceCortexGolden pins the full emitted Cortex document.
func TestEcommerceCortexGolden(t *testing.T) {
	got := emit(t)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("cortex output != golden:\n--- got ---\n%s", got)
	}
}

// ---- subset of the Cortex shape, for targeted structural assertions ----

type cModel struct {
	Name          string   `yaml:"name"`
	Tables        []cTable `yaml:"tables"`
	Relationships []cRel   `yaml:"relationships"`
}

type cTable struct {
	Name       string    `yaml:"name"`
	Dimensions []cCol    `yaml:"dimensions"`
	Metrics    []cMetric `yaml:"metrics"`
}

type cCol struct {
	Name     string `yaml:"name"`
	Expr     string `yaml:"expr"`
	DataType string `yaml:"data_type"`
}

type cMetric struct {
	Name string `yaml:"name"`
	Expr string `yaml:"expr"`
}

type cRel struct {
	Name       string `yaml:"name"`
	LeftTable  string `yaml:"left_table"`
	RightTable string `yaml:"right_table"`
}

// TestEcommerceCortexStructure asserts the interesting transpilation behaviors
// directly, so a failure names the specific rule that broke.
func TestEcommerceCortexStructure(t *testing.T) {
	var m cModel
	if err := yaml.Unmarshal(emit(t), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "ecommerce" {
		t.Fatalf("model name = %q, want ecommerce", m.Name)
	}

	// Table order follows source order (facts before dimensions).
	var tables []string
	for _, tb := range m.Tables {
		tables = append(tables, tb.Name)
	}
	assertEqual(t, "tables", tables, []string{"fct_orders", "fct_order_lines", "dim_customer", "dim_product"})

	// Every foreign entity becomes a relationship, including fact->fact.
	var rels []string
	for _, r := range m.Relationships {
		rels = append(rels, r.Name)
	}
	assertEqual(t, "relationships", rels, []string{
		"fct_orders_to_dim_customer",
		"fct_order_lines_to_fct_orders",
		"fct_order_lines_to_dim_product",
	})

	// Same-table ratio metric expands to qualified, UPPERCASED SQL.
	assertStr(t, "aov expr", metricExpr(t, m, "fct_orders", "aov"),
		"SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)")

	// Cross-table ratio: owned by the numerator's table, references both tables.
	assertStr(t, "units_per_order expr", metricExpr(t, m, "fct_order_lines", "units_per_order"),
		"SUM(FCT_ORDER_LINES.QUANTITY) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)")

	// Data-type inference heuristics.
	assertStr(t, "is_refunded data_type", dimType(t, m, "fct_orders", "is_refunded"), "BOOLEAN")
	assertStr(t, "order_id data_type", dimType(t, m, "fct_orders", "order_id"), "NUMBER")
	assertStr(t, "customer_sk data_type", dimType(t, m, "fct_orders", "customer_sk"), "NUMBER")
	assertStr(t, "customer_segment data_type", dimType(t, m, "dim_customer", "customer_segment"), "TEXT")
}

// TestCLIBinaryEndToEnd exercises the actual compiled command, not just the API.
func TestCLIBinaryEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skips process exec under -short")
	}
	moduleRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := filepath.Abs(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	cmd := exec.Command("go", "run", "./cmd/semglot", "build",
		"--from", "dbt", "--reference", ref, "--layer", "cortex", "--out", out,
		"--database", "ANALYTICS", "--name", "ecommerce")
	cmd.Dir = moduleRoot
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cli run: %v\n%s", err, b)
	}
	got, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("cli output != golden:\n--- got ---\n%s", got)
	}
}

// ---- helpers ----

func metricExpr(t *testing.T, m cModel, table, metric string) string {
	t.Helper()
	for _, tb := range m.Tables {
		if tb.Name != table {
			continue
		}
		for _, mt := range tb.Metrics {
			if mt.Name == metric {
				return mt.Expr
			}
		}
		t.Fatalf("metric %q not found on table %q", metric, table)
	}
	t.Fatalf("table %q not found", table)
	return ""
}

func dimType(t *testing.T, m cModel, table, dim string) string {
	t.Helper()
	for _, tb := range m.Tables {
		if tb.Name != table {
			continue
		}
		for _, d := range tb.Dimensions {
			if d.Name == dim {
				return d.DataType
			}
		}
		t.Fatalf("dimension %q not found on table %q", dim, table)
	}
	t.Fatalf("table %q not found", table)
	return ""
}

func assertEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

func assertStr(t *testing.T, label, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", label, got, want)
	}
}
