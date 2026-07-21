# Databricks metric-view emitter — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Erratum (2026-07-20):** This plan's fact-selection rule, one metric view per *fact* table (`len(t.Metrics) >= 1`), skipping pure-dimension tables, was superseded during implementation and is not what shipped.
> It emitted only 6 of 38 views on the real dataset.
> The shipped rule is one metric view per IR table (skipping only a table left with zero fields), with measures sourced from metrics plus any raw measure not already covered by name or expression.
> See `docs/design-databricks-metric-view.md` for the current, accurate description.
> This document is left otherwise unedited as a historical record of the plan as executed; lines below that describe the superseded rule are marked `[SUPERSEDED, see erratum above]` rather than rewritten.

**Goal:** Add a `databricks-metric-view` target that transpiles a dbt semantic layer into Unity Catalog Metric View YAML — the semantic layer Databricks AI/BI Genie grounds on — one `<fact>.yaml` per fact table. `[SUPERSEDED, see erratum above: ships one view per IR table, not per fact table]`

**Architecture:** A new `Emitter`/`Configurable` in `dialect/`, parallel to `snowflake_semantic_view.go`. It walks the IR: each table with ≥1 metric becomes a metric view rooted at that table (`source`), with direct IR relationships as `joins`, dimensions (own + joined) as `fields`, and metrics (lowered via the existing `renderSQL`) as `measures`. Cross-grain and window/conversion metrics degrade to a `comment` note. Output is raw YAML via `yaml.v3`. `[SUPERSEDED, see erratum above: every table gets a view, not only ones with a metric; measures also include uncovered raw ir.Measures]`

**Tech Stack:** Go, `gopkg.in/yaml.v3`, existing `ir` + `dialect` packages.

## Global Constraints

- Package `dialect` (renamed from the old `layer`).
- Target-dialect name is exactly `databricks-metric-view`.
- Reuse existing helpers — do NOT reimplement: `metricResolver(m)`, `renderSQL(e, resolve)`, `enumClause([]ir.EnumValue) string`, `appendClause(desc, clause string) string`. Signature of resolve is `func(name string) (ir.Expr, bool)`.
- `Options` fields: `Database` → Unity Catalog **catalog**; `Schema` → source-table schema (default `main`); `Name`/`ViewSchema` unused here; `Description` folded into each view `comment`.
- Metric-view `measures` come from IR **Metrics only** (not raw `Measures`). `[SUPERSEDED, see erratum above: raw Measures not already covered by name or expression are also emitted]`
- `on` join keys MUST be emitted double-quoted (`"on":`) — Databricks parses YAML 1.1 where bare `on` is boolean.
- `version` value is the string `"1.1"`.
- Table/column identifiers are lowercased in output (Unity Catalog convention); IR table names are already lowercase (e.g. `fct_orders`).
- `Emit` is read-only over `m` (accumulate degrade notes locally).
- Commit after each task. End commit messages with the `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer.
- Verify commands run from the worktree root (`.claude/worktrees/databricks-target`).

---

### Task 1: The `databricks-metric-view` emitter

**Files:**
- Create: `dialect/databricks_metric_view.go`
- Test: `dialect/databricks_metric_view_test.go`

**Interfaces:**
- Consumes: `ir.Model`, `ir.Table`, `ir.Field`, `ir.Measure`, `ir.Metric`, `ir.Relationship`, `ir.Expr` (and AST nodes `ir.Ref`, `ir.Binary`, `ir.Agg`, `ir.Window`, `ir.Conversion`); `metricResolver`, `renderSQL`, `enumClause`, `appendClause`; `dialect.Register`, `dialect.Options`, `dialect.Emitter`, `dialect.Configurable`.
- Produces: a registered dialect named `databricks-metric-view` implementing `Emitter` + `Configurable`. Emits `<lowercased-table-name>.yaml` per fact table into the output dir. `[SUPERSEDED, see erratum above: per IR table left with >=1 field, not per fact table]`

- [ ] **Step 1: Write the failing unit test**

Create `dialect/databricks_metric_view_test.go`:

```go
package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// dbxTestModel: one fact (orders) joined to a dimension (customers), with a
// simple aggregate metric, a same-grain derived ratio, and a cross-grain
// derived ratio that references a metric on another fact (lines).
func dbxTestModel() *ir.Model {
	orders := ir.Table{
		Name:       "orders",
		Dimensions: []ir.Field{{Name: "status", Expr: "status", Description: "Order status"}},
		Metrics: []ir.Metric{
			{Name: "revenue", Label: "Revenue", Description: "Gross revenue",
				Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
			{Name: "order_count",
				Def: ir.Agg{Func: "count_distinct", Table: "orders", Arg: ir.Col{Name: "order_id"}}},
			{Name: "aov", // same-grain derived: revenue / order_count
				Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "order_count"}}},
		},
	}
	customers := ir.Table{
		Name:       "customers",
		Dimensions: []ir.Field{{Name: "region", Expr: "region"}},
	}
	lines := ir.Table{
		Name: "lines",
		Metrics: []ir.Metric{
			{Name: "units", Def: ir.Agg{Func: "sum", Table: "lines", Arg: ir.Col{Name: "qty"}}},
			{Name: "units_per_order", // cross-grain: references orders' order_count
				Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "units"}, Right: ir.Ref{Metric: "order_count"}}},
		},
	}
	return &ir.Model{
		Tables: []ir.Table{orders, customers, lines},
		Relationships: []ir.Relationship{
			{Left: "orders", Right: "customers", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "customer_id"}}},
		},
	}
}

func emitDbx(t *testing.T, m *ir.Model) map[string]string {
	t.Helper()
	e := databricksMetricView{}.WithOptions(Options{Database: "ANALYTICS", Schema: "MAIN"})
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	out := map[string]string{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[ent.Name()] = string(b)
	}
	return out
}

func TestDatabricksMetricViewOrders(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got, ok := files["orders.yaml"]
	if !ok {
		t.Fatalf("expected orders.yaml, got files: %v", files)
	}
	for _, want := range []string{
		`version: "1.1"`,
		"source: analytics.main.orders",
		`"on": source.customer_id = customers.customer_id`,
		"source: analytics.main.customers", // the join source
		"expr: customers.region",           // joined dimension, prefixed
		"expr: SUM(amount)",                 // simple metric lowered (renderSQL uppercases agg)
		"SUM(amount) / COUNT(DISTINCT order_id)", // same-grain derived, inlined
	} {
		if !strings.Contains(got, want) {
			t.Errorf("orders.yaml missing %q\n--- got ---\n%s", want, got)
		}
	}
}

func TestDatabricksMetricViewNoDimensionFile(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	if _, ok := files["customers.yaml"]; ok {
		t.Error("customers is a pure dimension (no metrics); should not get its own view")
	}
}

func TestDatabricksMetricViewCrossGrainDegrades(t *testing.T) {
	files := emitDbx(t, dbxTestModel())
	got := files["lines.yaml"]
	if strings.Contains(got, "units_per_order\n") || strings.Contains(got, "name: units_per_order") {
		t.Errorf("cross-grain metric units_per_order should not be an emitted measure\n%s", got)
	}
	if !strings.Contains(got, "units_per_order") || !strings.Contains(strings.ToLower(got), "cross-grain") {
		t.Errorf("cross-grain metric should be noted in the comment\n%s", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./dialect/ -run TestDatabricksMetricView 2>&1 | tail -20`
Expected: FAIL — compile error `undefined: databricksMetricView`.

- [ ] **Step 3: Write the emitter**

Create `dialect/databricks_metric_view.go`:

```go
package dialect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(databricksMetricView{}) }

// databricksMetricView emits Unity Catalog metric views — the YAML semantic
// layer Databricks AI/BI Genie grounds its answers on. It writes one
// <fact>.yaml per fact table (a table with >=1 metric), rooted at that table
// with direct joins to the dimension tables it references. Zero value is
// usable; the build command sets identity from flags. Emit does not mutate m.
// Database is the Unity Catalog catalog; Schema is the source-table schema.
type databricksMetricView struct{ Database, Schema, ModelName, Description string }

func (databricksMetricView) Name() string { return "databricks-metric-view" }

func (databricksMetricView) WithOptions(o Options) Emitter {
	return databricksMetricView{
		Database:    o.Database,
		Schema:      o.Schema,
		ModelName:   o.Name,
		Description: o.Description,
	}
}

// ---- metric-view YAML shapes ----

type dbxMetricView struct {
	Version  string       `yaml:"version"`
	Comment  string       `yaml:"comment,omitempty"`
	Source   string       `yaml:"source"`
	Joins    []dbxJoin    `yaml:"joins,omitempty"`
	Fields   []dbxField   `yaml:"fields,omitempty"`
	Measures []dbxMeasure `yaml:"measures,omitempty"`
}

type dbxField struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Comment     string   `yaml:"comment,omitempty"`
	DisplayName string   `yaml:"display_name,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

type dbxMeasure struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Comment     string   `yaml:"comment,omitempty"`
	DisplayName string   `yaml:"display_name,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

// dbxJoin marshals with a QUOTED "on" key. Databricks parses metric-view YAML
// as YAML 1.1, where a bare `on` key is the boolean true — which corrupts the
// join. yaml.v3 (YAML 1.2, where `on` is a plain string) would emit it bare, so
// build the mapping node explicitly and force the key's quote style.
type dbxJoin struct {
	Name   string
	Source string
	On     string
}

func (j dbxJoin) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	pairs := []struct {
		key, val string
		style    yaml.Style
	}{
		{"name", j.Name, 0},
		{"source", j.Source, 0},
		{"on", j.On, yaml.DoubleQuotedStyle},
	}
	for _, p := range pairs {
		kn := &yaml.Node{Kind: yaml.ScalarNode, Value: p.key, Style: p.style}
		vn := &yaml.Node{}
		if err := vn.Encode(p.val); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, kn, vn)
	}
	return n, nil
}

func (d databricksMetricView) Emit(m *ir.Model, dir string) error {
	catalog := strings.ToLower(d.Database)
	schema := strings.ToLower(d.Schema)
	if schema == "" {
		schema = "main"
	}
	resolve := metricResolver(m)

	// metricOwner maps each metric name to its owning table, so a derived metric
	// that references a metric on ANOTHER table (cross-grain) is detected and
	// degraded rather than inlined — inlining another grain's aggregate into this
	// view (over a fan-out join) would miscount.
	metricOwner := map[string]string{}
	tableByName := map[string]ir.Table{}
	for _, t := range m.Tables {
		tableByName[strings.ToLower(t.Name)] = t
		for _, mt := range t.Metrics {
			metricOwner[mt.Name] = t.Name
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, t := range m.Tables {
		if len(t.Metrics) == 0 {
			continue // not a fact; surfaces only as a join on other views
		}
		mv := d.buildView(m, t, resolve, metricOwner, tableByName, catalog, schema)
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(mv); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		fname := strings.ToLower(t.Name) + ".yaml"
		if err := os.WriteFile(filepath.Join(dir, fname), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (d databricksMetricView) buildView(m *ir.Model, t ir.Table, resolve func(string) (ir.Expr, bool), metricOwner map[string]string, tableByName map[string]ir.Table, catalog, schema string) dbxMetricView {
	mv := dbxMetricView{Version: "1.1", Source: dbxQualify(catalog, schema, t.Name)}
	var notes []string

	// Joins: relationships where this table is the LEFT (referencing) side.
	seenJoin := map[string]bool{}
	for _, r := range m.Relationships {
		if !strings.EqualFold(r.Left, t.Name) || len(r.Columns) == 0 {
			continue
		}
		joinName := strings.ToLower(r.Right)
		if seenJoin[joinName] {
			continue
		}
		seenJoin[joinName] = true
		var conds []string
		for _, cp := range r.Columns {
			conds = append(conds, "source."+strings.ToLower(cp.Left)+" = "+joinName+"."+strings.ToLower(cp.Right))
		}
		mv.Joins = append(mv.Joins, dbxJoin{
			Name:   joinName,
			Source: dbxQualify(catalog, schema, r.Right),
			On:     strings.Join(conds, " and "),
		})
	}

	// Fields: own dimensions (bare expr), then joined tables' dimensions
	// (prefixed expr). Names must be unique within a view; a joined field whose
	// bare name collides is prefixed with the join name, mirroring the
	// snowflake-semantic-view dedup.
	seen := map[string]bool{}
	for _, f := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
		name := strings.ToLower(f.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		mv.Fields = append(mv.Fields, dbxField{
			Name: name, Expr: strings.ToLower(f.Expr),
			Comment: dbxFieldComment(f), Synonyms: dbxCapSyn(f.Synonyms),
		})
	}
	for _, j := range mv.Joins {
		jt := tableByName[j.Name]
		for _, f := range append(append([]ir.Field{}, jt.Dimensions...), jt.TimeDimensions...) {
			name := strings.ToLower(f.Name)
			if seen[name] {
				name = j.Name + "_" + name
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			mv.Fields = append(mv.Fields, dbxField{
				Name: name, Expr: j.Name + "." + strings.ToLower(f.Expr),
				Comment: dbxFieldComment(f), Synonyms: dbxCapSyn(f.Synonyms),
			})
		}
	}

	// Measures: from metrics only. Degrade window/conversion and cross-grain
	// derived metrics to a note rather than emit SQL we cannot stand behind.
	for _, mt := range t.Metrics {
		if reason, degrade := dbxDegrade(mt); degrade {
			notes = append(notes, "metric "+mt.Name+": "+reason)
			continue
		}
		if dbxCrossGrain(mt.Def, metricOwner, t.Name) {
			notes = append(notes, "metric "+mt.Name+": references a measure on another table (cross-grain), not expressible as a single-grain metric-view measure")
			continue
		}
		mv.Measures = append(mv.Measures, dbxMeasure{
			Name: strings.ToLower(mt.Name), Expr: renderSQL(mt.Def, resolve),
			Comment: mt.Description, DisplayName: mt.Label, Synonyms: dbxCapSyn(mt.Synonyms),
		})
	}

	var parts []string
	if t.Description != "" {
		parts = append(parts, t.Description)
	}
	if d.Description != "" {
		parts = append(parts, d.Description)
	}
	for _, n := range notes {
		parts = append(parts, "Note: "+n)
	}
	mv.Comment = strings.Join(parts, " ")
	return mv
}

// dbxQualify builds a Unity Catalog table reference, three-part when a catalog
// is set, else two-part (keeps zero-value output well-formed).
func dbxQualify(catalog, schema, table string) string {
	t := strings.ToLower(table)
	if catalog == "" {
		return schema + "." + t
	}
	return catalog + "." + schema + "." + t
}

// dbxFieldComment folds a field's enum into its description, since a metric-view
// field has no per-value enum slot.
func dbxFieldComment(f ir.Field) string { return appendClause(f.Description, enumClause(f.Enum)) }

// dbxCapSyn caps synonyms at the metric-view limit of 10 per field/measure.
func dbxCapSyn(syn []string) []string {
	if len(syn) > 10 {
		return syn[:10]
	}
	return syn
}

// dbxDegrade reports metric kinds with no validated metric-view primitive
// (cumulative/conversion), matching the cortex/snowflake-semantic-view posture.
func dbxDegrade(mt ir.Metric) (string, bool) {
	switch mt.Def.(type) {
	case ir.Window:
		return "cumulative/windowed metric — no validated metric-view primitive (provisional)", true
	case ir.Conversion:
		return "conversion/funnel metric — no metric-view primitive (provisional)", true
	}
	return "", false
}

// dbxCrossGrain reports whether def references (directly or nested) a metric
// owned by a table other than self — i.e. a cross-grain derived metric that a
// single-source metric view cannot express without fan-out.
func dbxCrossGrain(def ir.Expr, owner map[string]string, self string) bool {
	found := false
	var walk func(ir.Expr)
	walk = func(e ir.Expr) {
		switch n := e.(type) {
		case ir.Ref:
			if o, ok := owner[n.Metric]; ok && !strings.EqualFold(o, self) {
				found = true
			}
		case ir.Binary:
			walk(n.Left)
			walk(n.Right)
		case ir.Agg:
			if n.Arg != nil {
				walk(n.Arg)
			}
			if n.Filter != nil {
				walk(n.Filter)
			}
		case ir.Window:
			walk(n.Base)
		case ir.Conversion:
			walk(n.Base)
			walk(n.Conv)
		}
	}
	walk(def)
	return found
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./dialect/ -run TestDatabricksMetricView -v 2>&1 | tail -25`
Expected: PASS for `TestDatabricksMetricViewOrders`, `TestDatabricksMetricViewNoDimensionFile`, `TestDatabricksMetricViewCrossGrainDegrades`.

- [ ] **Step 5: Run the full dialect package to check nothing else broke**

Run: `go test ./dialect/ 2>&1 | tail -5`
Expected: `ok  github.com/benchouse/semglot/dialect`

- [ ] **Step 6: Commit**

```bash
git add dialect/databricks_metric_view.go dialect/databricks_metric_view_test.go
git commit -m "feat(databricks): add databricks-metric-view emitter

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: CLI wiring — require a catalog for the new target

**Files:**
- Modify: `cmd/semglot/config.go` (the `snowflakeTargets` var and its use inside `loadProfile`)

**Interfaces:**
- Consumes: the registered `databricks-metric-view` dialect from Task 1.
- Produces: a profile with `target-dialect: databricks-metric-view` requires `database`; the target auto-appears in `dialect.Names()` usage output.

- [ ] **Step 1: Write the failing test**

Add to `cmd/semglot/config_test.go` (append; keep existing package clause):

```go
func TestLoadProfileDatabricksRequiresDatabase(t *testing.T) {
	cfg := writeConfig(t, `profiles:
  p:
    source: /x
    target-dialect: databricks-metric-view
    output: ./out
`)
	_, err := loadProfile(cfg, "p")
	if err == nil {
		t.Fatal("want error: databricks-metric-view target with no database")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./cmd/semglot/ -run TestLoadProfileDatabricksRequiresDatabase -v 2>&1 | tail -15`
Expected: FAIL — the target is not yet in the required-catalog set, so `loadProfile` returns no error.

- [ ] **Step 3: Add the target to the required-catalog set**

In `cmd/semglot/config.go`, replace the `snowflakeTargets` declaration:

```go
// snowflakeTargets emit into a physical Snowflake database and therefore require
// a database; without one they'd emit invalid, unqualified DDL.
var snowflakeTargets = map[string]bool{"cortex": true, "snowflake-semantic-view": true}
```

with:

```go
// warehouseTargets emit into a physical warehouse (Snowflake, or a Databricks
// Unity Catalog) and therefore require a resolved database/catalog; without one
// they'd emit invalid, unqualified DDL.
var warehouseTargets = map[string]bool{"cortex": true, "snowflake-semantic-view": true, "databricks-metric-view": true}
```

Then update the single use site (in `loadProfile`) from:

```go
	if snowflakeTargets[spec.TargetDialect] && spec.Database == "" {
```

to:

```go
	if warehouseTargets[spec.TargetDialect] && spec.Database == "" {
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/semglot/ -run TestLoadProfileDatabricksRequiresDatabase -v 2>&1 | tail -15`
Expected: PASS.

- [ ] **Step 5: Build and confirm no stale references to the old var name**

Run: `go build ./... && grep -rn "snowflakeTargets" cmd/ || echo "no stale refs"`
Expected: build succeeds; prints `no stale refs`.

- [ ] **Step 6: Commit**

```bash
git add cmd/semglot/config.go cmd/semglot/config_test.go
git commit -m "feat(cli): require a catalog for databricks-metric-view

Rename snowflakeTargets -> warehouseTargets and add the Databricks target
so a profile without a database fails fast instead of emitting an
unqualified source reference.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Integration — golden fixtures + structure assertions

**Files:**
- Modify: `test/integration_test.go` (add two tests, mirroring `TestEcommerceSupersimpleGolden` and `TestSemanticViewStructure`)
- Create (via `UPDATE_GOLDEN=1`): `test/models/ecommerce/dbt/databricks-metric-view/*.yaml`

**Interfaces:**
- Consumes: `sourceDirs` (already defined in the test package), `dialect.AsEmitter`, `dialect.AsParser`, `dialect.Configurable`, `dialect.Options`.
- Produces: pinned goldens for the ecommerce fixture and a structure test.

- [ ] **Step 1: Write the structure + golden tests (they will fail until goldens exist)**

Append to `test/integration_test.go`:

```go
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
	if err := e.Emit(m, out); err != nil {
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
	// dim_customer is a pure dimension — it must NOT get its own view.
	if _, ok := files["dim_customer.yaml"]; ok {
		t.Error("dim_customer has no metrics; should not be emitted as its own metric view")
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
```

Note: `os`, `path/filepath`, `sort`, `strings`, and `dialect` are already imported in `test/integration_test.go` (verify; add any missing to the import block).

- [ ] **Step 2: Run the structure test (no golden dependency) to verify emit works**

Run: `go test ./test/ -run TestDatabricksMetricViewStructure -v 2>&1 | tail -25`
Expected: PASS. If it fails on a missing join `"on":`, inspect the printed YAML — confirm relationships resolved for `fct_orders`.

- [ ] **Step 3: Generate the goldens and eyeball them**

Run: `UPDATE_GOLDEN=1 go test ./test/ -run TestDatabricksMetricViewGolden && ls test/models/ecommerce/dbt/databricks-metric-view/`
Expected: creates `fct_orders.yaml` and `fct_order_lines.yaml` (the two fact tables). Then Read each file and confirm: three-part `source`, quoted `"on":` joins, joined dimensions prefixed with the join name, derived measures inlined (e.g. `AOV` as `... / ...`), and `units_per_order` degraded into `fct_order_lines.yaml`'s `comment` (not a measure).

- [ ] **Step 4: Re-run the golden test without UPDATE_GOLDEN to confirm it is stable**

Run: `go test ./test/ -run TestDatabricksMetricView -v 2>&1 | tail -20`
Expected: PASS for both `Structure` and `Golden`.

- [ ] **Step 5: Commit**

```bash
git add test/integration_test.go test/models/ecommerce/dbt/databricks-metric-view/
git commit -m "test(databricks): golden + structure tests for metric-view target

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Docs + full-suite verification

**Files:**
- Modify: `README.md` (dialect/target mention)

- [ ] **Step 1: Add the target to the README dialect list**

Read `README.md`, find where targets/dialects are listed (the intro paragraph names "dbt semantic models, Snowflake Cortex, and more", and the Status line mentions transpile targets). Add a concise mention that `databricks-metric-view` emits Unity Catalog Metric View YAML for Databricks AI/BI Genie. Keep the existing prose style; no em-dashes are required but the repo uses them — match surrounding text. Example addition to the intro list: change "Snowflake Cortex, and more" context so Databricks metric views appear among supported targets, and if there is a per-target list, add:

```
- `databricks-metric-view` — Unity Catalog Metric View YAML (the semantic layer Databricks AI/BI Genie grounds on); one file per fact table. `[SUPERSEDED, see erratum above: one file per IR table]`
```

- [ ] **Step 2: Run the entire test suite**

Run: `go test ./... 2>&1 | tail -10`
Expected: all packages `ok` (`cmd/semglot`, `dialect`, `test`; `ir` has no tests).

- [ ] **Step 3: Vet and build**

Run: `go vet ./... && go build ./...`
Expected: no output (clean).

- [ ] **Step 4: End-to-end CLI smoke test**

The CLI is profiles-only: write a `semglot.yaml` with a profile naming the two source dirs, `target-dialect: databricks-metric-view`, and a `database`, then run:

```bash
go build -o /tmp/semglot ./cmd/semglot && \
cat > /tmp/semglot.yaml <<'EOF'
profiles:
  dbx:
    source:
      - test/models/ecommerce/dbt/semantic
      - test/models/ecommerce/dbt/marts
    target-dialect: databricks-metric-view
    output: /tmp/dbx-out
    database: ANALYTICS
EOF
/tmp/semglot build --profile dbx --config /tmp/semglot.yaml && \
ls /tmp/dbx-out && head -20 /tmp/dbx-out/fct_orders.yaml
```
Expected: lists the per-table yaml files, and shows a valid metric-view header (`version: "1.1"`, `source: analytics.main.fct_orders`).

- [ ] **Step 5: Commit**

```bash
git add README.md
git commit -m "docs(readme): mention the databricks-metric-view target

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- New emitter file + registration → Task 1. ✓
- Identity mapping (Database→catalog, Schema→schema, default `main`) → Task 1 (`Emit`) + Task 2 (required catalog). ✓
- One view per fact table; pure-dimension tables skipped → Task 1 (`len(t.Metrics)==0` skip) + tests. ✓ `[SUPERSEDED, see erratum above: this rule shipped a fact-only view set (6 of 38 on the real dataset) and was replaced with one view per IR table before merge]`
- Joins from IR relationships, quoted `on`, `source.`/join-name prefixes, multi-col `AND` → Task 1 (`dbxJoin.MarshalYAML`, join loop). ✓
- Fields: own + joined dims, dedup with prefix, enum fold → Task 1 (field loops, `dbxFieldComment`). ✓
- Measures from Metrics via `renderSQL`; display_name/synonyms(cap 10)/comment → Task 1 (measure loop, `dbxCapSyn`). ✓
- Degrade window/conversion + cross-grain to `comment` note → Task 1 (`dbxDegrade`, `dbxCrossGrain`). ✓
- `version: "1.1"`, yaml.v3 `SetIndent(2)`, `os.MkdirAll` → Task 1. ✓
- Testing: unit + golden(multi-file) + structure + CLI required-catalog → Tasks 1–4. ✓
- README mention → Task 4. ✓

**Placeholder scan:** No TBD/TODO; all code blocks complete; README step gives an exact line to add and defers only the insertion point to a Read (unavoidable — house prose varies).

**Type consistency:** `databricksMetricView` struct, `dbxMetricView/dbxField/dbxMeasure/dbxJoin`, helpers `dbxQualify/dbxFieldComment/dbxCapSyn/dbxDegrade/dbxCrossGrain`, and `buildView(...)` signature are referenced consistently across steps. `resolve` typed `func(string) (ir.Expr, bool)` matches `metricResolver`/`renderSQL`. Test helpers (`emitDbx`, `emitDatabricks`, `keysOf`) are self-contained.
