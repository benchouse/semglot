package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/benchouse/semglot/dialect"
	"github.com/benchouse/semglot/ir"
)

const (
	// projectDir holds the emitted-golden subdirs (<target>/) for each dialect.
	projectDir = "models/ecommerce/dbt"
	goldenPath = "models/ecommerce/dbt/cortex/ecommerce.yaml"
	// dbtGoldenPath is the emitted-dbt golden, in a dbt/ SUBDIR that the source
	// globs below never read (which would pollute the source project).
	dbtGoldenPath = "models/ecommerce/dbt/dbt/ecommerce.yml"
)

// sourceDirs is the dbt input, split across folders like a real project
// (semantic_models + metrics under semantic/, classic models: schema under
// marts/), so the tests exercise multi-source (a `source:` list in the profile) parsing.
var sourceDirs = []string{
	"models/ecommerce/dbt/semantic",
	"models/ecommerce/dbt/marts",
}

// sortFields orders a []ir.Field slice by Name for canonical comparison.
func sortFields(fs []ir.Field) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name })
}

// canonicalizeModel sorts every order-insensitive slice in the IR so two models
// that are semantically identical but differently ordered compare equal. The
// dbt->dbt round-trip is IR-lossless, not byte-identical, so the round-trip test
// canonicalizes both sides before reflect.DeepEqual.
func canonicalizeModel(m *ir.Model) {
	sort.Slice(m.Tables, func(i, j int) bool { return m.Tables[i].Name < m.Tables[j].Name })
	for ti := range m.Tables {
		t := &m.Tables[ti]
		sort.Strings(t.PrimaryKey)
		sortFields(t.Dimensions)
		sortFields(t.TimeDimensions)
		sort.Slice(t.Measures, func(i, j int) bool { return t.Measures[i].Name < t.Measures[j].Name })
		sort.Slice(t.Metrics, func(i, j int) bool { return t.Metrics[i].Name < t.Metrics[j].Name })
	}
	sort.Slice(m.Relationships, func(i, j int) bool {
		a, b := m.Relationships[i], m.Relationships[j]
		return relSortKey(a) < relSortKey(b)
	})
	sort.Strings(m.Notes)
}

// relSortKey builds a deterministic sort key for a Relationship: the
// Left/Right endpoints followed by a discriminator built from its Columns, so
// two relationships sharing endpoints (or a multi-pair relationship) sort
// deterministically instead of comparing equal on Left+Right alone.
func relSortKey(r ir.Relationship) string {
	key := r.Left + ">" + r.Right
	for _, cp := range r.Columns {
		key += "|" + cp.Left + "=" + cp.Right
	}
	return key
}

// TestDBTRoundTrip proves the AST/IR captures the dbt source losslessly: parse
// the fixture -> emit dbt -> re-parse -> the (canonicalized) IR is unchanged.
func TestDBTRoundTrip(t *testing.T) {
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	m1, err := p.Parse(sourceDirs...)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	e, err := dialect.AsEmitter("dbt")
	if err != nil {
		t.Fatalf("dbt is not an Emitter: %v", err)
	}
	out := t.TempDir()
	if _, err := e.Emit(m1, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	m2, err := p.Parse(out)
	if err != nil {
		t.Fatalf("re-parse emitted dbt: %v", err)
	}
	canonicalizeModel(m1)
	canonicalizeModel(m2)
	if !reflect.DeepEqual(m1, m2) {
		t.Fatalf("round-trip changed the IR:\n--- source ---\n%+v\n--- re-parsed ---\n%+v", m1, m2)
	}
}

// TestDBTEmitGolden pins the emitted-dbt document so the generated dbt is
// reviewable and regression-locked. Run with UPDATE_GOLDEN=1 to (re)create it.
func TestDBTEmitGolden(t *testing.T) {
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	e, err := dialect.AsEmitter("dbt")
	if err != nil {
		t.Fatalf("dbt is not an Emitter: %v", err)
	}
	m, err := p.Parse(sourceDirs...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if _, err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(out, "ecommerce.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(dbtGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dbtGoldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(dbtGoldenPath)
	if err != nil {
		t.Fatalf("read dbt golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("dbt output != golden:\n--- got ---\n%s", got)
	}
}

// emit runs the real dbt -> cortex pipeline through the public layer API and
// returns the emitted Cortex YAML.
func emit(t *testing.T) []byte {
	t.Helper()
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatalf("AsParser: %v", err)
	}
	e, err := dialect.AsEmitter("cortex")
	if err != nil {
		t.Fatalf("AsEmitter: %v", err)
	}
	if c, ok := e.(dialect.Configurable); ok {
		e = c.WithOptions(dialect.Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce"})
	}
	m, err := p.Parse(sourceDirs...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if _, err := e.Emit(m, out); err != nil {
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
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
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
	Name        string `yaml:"name"`
	Expr        string `yaml:"expr"`
	DataType    string `yaml:"data_type"`
	Description string `yaml:"description"`
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
	assertEqual(t, "tables", tables, []string{"fct_orders", "fct_order_lines", "dim_customer", "dim_product", "obt_sales", "dim_channel"})

	// Every foreign entity becomes a relationship, including fact->fact.
	var rels []string
	for _, r := range m.Relationships {
		rels = append(rels, r.Name)
	}
	assertEqual(t, "relationships", rels, []string{
		// fct_orders references dim_customer twice (a role-playing dimension:
		// ship-to via customer_sk, bill-to via billing_customer_sk), so BOTH are
		// kept and disambiguated by their left column. Pairs with a single
		// relationship keep the plain <left>_to_<right> name.
		"fct_orders_to_dim_customer_customer_sk",
		"fct_order_lines_to_fct_orders",
		"fct_order_lines_to_dim_product",
		"obt_sales_to_fct_orders", // measures-only OBT, foreign entity to fct_orders
		"fct_orders_to_dim_customer_billing_customer_sk",
		"fct_orders_to_dim_channel", // classic-only target, PK via unique+not_null, arguments: rel form
	})

	// Same-table ratio metric expands to qualified, UPPERCASED SQL.
	assertStr(t, "aov expr", metricExpr(t, m, "fct_orders", "aov"),
		"SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)")

	// Cross-table ratio: owned by the numerator's table, references both tables.
	assertStr(t, "units_per_order expr", metricExpr(t, m, "fct_order_lines", "units_per_order"),
		"SUM(FCT_ORDER_LINES.QUANTITY) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)")

	// Compound measure expr: the column inside the CASE is qualified (via the
	// SQL lexer), the SQL keywords and literal are not.
	assertStr(t, "refund_rate expr", metricExpr(t, m, "fct_orders", "refund_rate"),
		"SUM(CASE WHEN FCT_ORDERS.IS_REFUNDED THEN 1 ELSE 0 END) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)")

	// Real data types come from dbt model properties (data_type), not the
	// name heuristic: accepts_marketing is BOOLEAN even though its name has no
	// is_/has_ prefix the heuristic would have called TEXT.
	assertStr(t, "accepts_marketing data_type", dimType(t, m, "dim_customer", "accepts_marketing"), "BOOLEAN")
	assertStr(t, "is_refunded data_type", dimType(t, m, "fct_orders", "is_refunded"), "BOOLEAN")
	assertStr(t, "order_id data_type", dimType(t, m, "fct_orders", "order_id"), "NUMBER")
	assertStr(t, "customer_segment data_type", dimType(t, m, "dim_customer", "customer_segment"), "TEXT")

	// Column descriptions from model properties flow through to the target.
	assertStr(t, "order_id description", dimDesc(t, m, "fct_orders", "order_id"), "Order surrogate key.")
	assertStr(t, "accepts_marketing description", dimDesc(t, m, "dim_customer", "accepts_marketing"),
		"Whether the customer opted in to marketing.")
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
	semantic, err := filepath.Abs(sourceDirs[0])
	if err != nil {
		t.Fatal(err)
	}
	marts, err := filepath.Abs(sourceDirs[1])
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "semglot.yaml")
	cfgBody := fmt.Sprintf(`profiles:
  ecommerce:
    source:
      - %s
      - %s
    target-dialect: cortex
    output: %s
    database: ANALYTICS
    model-name: ecommerce
`, semantic, marts, out)
	if err := os.WriteFile(cfg, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/semglot", "build",
		"--profile", "ecommerce", "--config", cfg)
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

func dimDesc(t *testing.T, m cModel, table, dim string) string {
	t.Helper()
	for _, tb := range m.Tables {
		if tb.Name != table {
			continue
		}
		for _, d := range tb.Dimensions {
			if d.Name == dim {
				return d.Description
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

func TestEcommerceSupersimpleGolden(t *testing.T) {
	e, err := dialect.AsEmitter("supersimple")
	if err != nil {
		t.Fatalf("AsEmitter: %v", err)
	}
	if c, ok := e.(dialect.Configurable); ok {
		e = c.WithOptions(dialect.Options{Schema: "MAIN"})
	}
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	model, err := p.Parse(sourceDirs...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if _, err := e.Emit(model, out); err != nil {
		t.Fatalf("emit: %v", err)
	}

	goldenDir := "models/ecommerce/dbt/supersimple"
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.MkdirAll(goldenDir, 0o755)
	}
	for _, ent := range entries {
		got, err := os.ReadFile(filepath.Join(out, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		gpath := filepath.Join(goldenDir, ent.Name())
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(gpath, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(gpath)
		if err != nil {
			t.Fatalf("read golden %s (UPDATE_GOLDEN=1 to create): %v", gpath, err)
		}
		if string(got) != string(want) {
			t.Fatalf("%s != golden:\n--- got ---\n%s", ent.Name(), got)
		}
	}

	// Reverse direction: every golden must have been produced, so a regression
	// that stops emitting a file (e.g. NOTES.md or a whole table) is caught;
	// the loop above only checks produced->golden.
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		goldens, err := os.ReadDir(goldenDir)
		if err != nil {
			t.Fatal(err)
		}
		produced := map[string]bool{}
		for _, ent := range entries {
			produced[ent.Name()] = true
		}
		for _, g := range goldens {
			if !produced[g.Name()] {
				t.Fatalf("golden %q was not produced by Emit (stopped emitting or renamed?)", g.Name())
			}
		}
	}
}

// emitDatabricks runs dbt -> databricks-metric-view over the ecommerce fixture
// and returns every emitted file keyed by name.
func emitDatabricks(t *testing.T) map[string]string {
	t.Helper()
	e, err := dialect.AsEmitter("databricks-metric-view")
	if err != nil {
		t.Fatalf("AsEmitter: %v", err)
	}
	if c, ok := e.(dialect.Configurable); ok {
		e = c.WithOptions(dialect.Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce"})
	}
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.Parse(sourceDirs...)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if _, err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	files := map[string]string{}
	ents, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(out, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[ent.Name()] = string(b)
	}
	return files
}

func TestDatabricksMetricViewStructure(t *testing.T) {
	files := emitDatabricks(t)
	orders, ok := files["fct_orders.yaml"]
	if !ok {
		t.Fatalf("expected fct_orders.yaml; got %v keys", keysOf(files))
	}
	for _, want := range []string{
		`version: "1.1"`,
		"source: analytics.main.fct_orders",
		`"on":`, // a quoted join key
	} {
		if !strings.Contains(orders, want) {
			t.Errorf("fct_orders.yaml missing %q\n--- got ---\n%s", want, orders)
		}
	}
	// dim_customer is a pure dimension — it now gets its own view with a
	// synthesised row-count measure, so every IR table is emitted (parity with
	// the sibling targets, which all emit one file per table).
	dimCustomer, ok := files["dim_customer.yaml"]
	if !ok {
		t.Fatal("expected dim_customer.yaml (dimension-only tables must still get a view with a synthesised row count)")
	}
	if !strings.Contains(dimCustomer, "expr: count(1)") || !strings.Contains(dimCustomer, "name: row_count") {
		t.Errorf("dim_customer.yaml missing synthesised row-count measure\n--- got ---\n%s", dimCustomer)
	}
	// fct_order_lines has a metric (units_sold) AND a raw measure the metric
	// doesn't cover (net_line_revenue) — it must not be silently dropped just
	// because the table also has metrics.
	orderLines, ok := files["fct_order_lines.yaml"]
	if !ok {
		t.Fatal("expected fct_order_lines.yaml")
	}
	if !strings.Contains(orderLines, "name: net_line_revenue") {
		t.Errorf("fct_order_lines.yaml: raw measure net_line_revenue (uncovered by any metric) must survive alongside metric-derived measures\n--- got ---\n%s", orderLines)
	}
}

func keysOf(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// databricksGoldenDir is the pinned databricks-metric-view output, one yaml per
// fact table. Regenerate with UPDATE_GOLDEN=1 and eyeball for valid YAML.
const databricksGoldenDir = "models/ecommerce/dbt/databricks-metric-view"

func TestDatabricksMetricViewGolden(t *testing.T) {
	files := emitDatabricks(t)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.MkdirAll(databricksGoldenDir, 0o755)
	}
	for name, got := range files {
		gpath := filepath.Join(databricksGoldenDir, name)
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(gpath, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(gpath)
		if err != nil {
			t.Fatalf("read golden %s (UPDATE_GOLDEN=1 to create): %v", gpath, err)
		}
		if got != string(want) {
			t.Fatalf("%s != golden:\n--- got ---\n%s", name, got)
		}
	}
	// Reverse: every golden must still be produced (catch a dropped file).
	if os.Getenv("UPDATE_GOLDEN") != "1" {
		goldens, err := os.ReadDir(databricksGoldenDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range goldens {
			if _, ok := files[g.Name()]; !ok {
				t.Errorf("golden %s was not produced by emit", g.Name())
			}
		}
	}
}

// TestRolePlayingDimensionAllTargets pins the role-playing dimension shape
// across every target that names relationships. fct_orders references
// dim_customer twice (ship-to via customer_sk, bill-to via
// billing_customer_sk), which is ordinary star-schema modelling. Each target
// keyed its relationships by the related table alone, so the second FK was
// either silently dropped (databricks-metric-view, supersimple) or emitted
// with a duplicate name that the consumer rejects (cortex,
// snowflake-semantic-view). Both FKs must survive, under distinct names.
func TestRolePlayingDimensionAllTargets(t *testing.T) {
	t.Run("cortex", func(t *testing.T) {
		got := string(emit(t))
		for _, want := range []string{
			"name: fct_orders_to_dim_customer_customer_sk",
			"name: fct_orders_to_dim_customer_billing_customer_sk",
			"left_column: CUSTOMER_SK",
			"left_column: BILLING_CUSTOMER_SK",
			// A pair with a single relationship keeps its plain name.
			"name: fct_orders_to_dim_channel",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("cortex missing %q", want)
			}
		}
	})

	t.Run("snowflake-semantic-view", func(t *testing.T) {
		got := emitTarget(t, "snowflake-semantic-view", "definition.md")
		for _, want := range []string{
			"FCT_ORDERS_DIM_CUSTOMER_CUSTOMER_SK as FCT_ORDERS(CUSTOMER_SK) references DIM_CUSTOMER(CUSTOMER_SK)",
			"FCT_ORDERS_DIM_CUSTOMER_BILLING_CUSTOMER_SK as FCT_ORDERS(BILLING_CUSTOMER_SK) references DIM_CUSTOMER(CUSTOMER_SK)",
			"FCT_ORDERS_DIM_CHANNEL as FCT_ORDERS(CHANNEL_ID) references DIM_CHANNEL(CHANNEL_ID)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("snowflake-semantic-view missing %q", want)
			}
		}
	})

	t.Run("databricks-metric-view", func(t *testing.T) {
		got := emitDatabricks(t)["fct_orders.yaml"]
		for _, want := range []string{
			"name: dim_customer_customer_sk",
			`"on": source.customer_sk = dim_customer_customer_sk.customer_sk`,
			"name: dim_customer_billing_customer_sk",
			`"on": source.billing_customer_sk = dim_customer_billing_customer_sk.customer_sk`,
			"name: dim_channel",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("databricks fct_orders.yaml missing %q\n--- got ---\n%s", want, got)
			}
		}
	})

	t.Run("supersimple", func(t *testing.T) {
		got := emitTarget(t, "supersimple", "DIM_CUSTOMER.yaml")
		for _, want := range []string{"orders_customer_sk:", "orders_billing_customer_sk:"} {
			if !strings.Contains(got, want) {
				t.Errorf("supersimple DIM_CUSTOMER.yaml missing %q\n--- got ---\n%s", want, got)
			}
		}
	})

	t.Run("nao-context-rules", func(t *testing.T) {
		got := emitTarget(t, "nao-context-rules", "RULES.md")
		for _, want := range []string{
			"`fct_orders.customer_sk → dim_customer.customer_sk`",
			"`fct_orders.billing_customer_sk → dim_customer.customer_sk`",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("nao-context-rules RULES.md missing %q", want)
			}
		}
	})
}
