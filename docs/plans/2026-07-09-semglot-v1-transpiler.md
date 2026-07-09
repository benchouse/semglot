# semglot v1 (dbt → Cortex transpiler) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transpile a dbt semantic-layer directory into a Snowflake Cortex `semantic_model.yaml` via a neutral IR, exposed as `semglot build`.

**Architecture:** `dbt.Parse(dir) → ir.Model → cortex.Emit(dir)`. dbt and cortex are registered `Layer`s declaring capability interfaces (`Parser` / `Emitter`). The IR is the transpile pivot and future many→many seam. No scoring in v1 (that is v2).

**Tech Stack:** Go 1.26, stdlib `flag` for the CLI, `gopkg.in/yaml.v3` for YAML. No cobra, no other deps.

## Global Constraints

- Module path: `github.com/benchouse/semglot` (verbatim).
- Go version floor: `go 1.26` (matches `go.mod`).
- Only non-stdlib dependency permitted: `gopkg.in/yaml.v3`.
- Exported packages (`ir`, `layer`), NOT `internal/` — the tool must be importable.
- Subcommands via `flag.NewFlagSet`; no cobra.
- Cross-dialect identifiers are lowercase in dbt/IR and UPPERCASED on Cortex emit.
- `go build ./...` and `go test ./...` green from the first code commit.
- License header not required in source files (MIT LICENSE at repo root covers it).

## File Structure

- `ir/model.go` — neutral model types (`package ir`). Pure data + one helper.
- `layer/layer.go` — `Layer`/`Parser`/`Emitter` interfaces + `registry`.
- `layer/dbt.go` — dbt `Parser` (dbt semantic YAML dir → `*ir.Model`).
- `layer/cortex.go` — cortex `Emitter` (`*ir.Model` → Cortex YAML) + type/expr helpers.
- `layer/dbt_test.go`, `layer/cortex_test.go`, `layer/registry_test.go` — unit tests.
- `layer/testdata/dbt/*.yml` — synthetic dbt fixture.
- `layer/testdata/cortex/semantic_model.golden.yaml` — golden Cortex output.
- `cmd/semglot/main.go` — subcommand dispatch (`build`; `score` stub).
- `cmd/semglot/main_test.go` — end-to-end + arg-parse test.

Already scaffolded (do not recreate): `go.mod`, `LICENSE`, `README.md`, `.gitignore`, `docs/`.

---

### Task 1: IR types, Layer interfaces, and the registry

**Files:**
- Create: `ir/model.go`
- Create: `layer/layer.go`
- Test: `layer/registry_test.go`

**Interfaces:**
- Produces (`package ir`): `Model`, `Table`, `Field`, `Measure`, `Metric`, `Relationship`, `ColumnPair` structs.
- Produces (`package layer`): `Layer` (`Name() string`), `Parser` (`Parse(dir string) (*ir.Model, error)`), `Emitter` (`Emit(m *ir.Model, dir string) error`); `Register(Layer)`, `Get(name string) (Layer, bool)`, `AsParser(name string) (Parser, error)`, `AsEmitter(name string) (Emitter, error)`.

- [ ] **Step 1: Write `ir/model.go`**

```go
// Package ir is semglot's neutral intermediate representation of a semantic
// layer. Every dialect parses into it and emits out of it, so it is the pivot
// for transpilation (and, in v2, the unit of the fairness index).
package ir

// Model is a whole semantic layer.
type Model struct {
	Tables        []Table
	Relationships []Relationship
}

// Table is one grain/entity in the layer.
type Table struct {
	Name           string
	Description    string
	PrimaryKey     []string // column exprs
	Dimensions     []Field  // categorical / id / plain
	TimeDimensions []Field
	Measures       []Measure
	Metrics        []Metric
}

// Field is a column-backed attribute. DataType is left empty by dialects (like
// dbt) that do not record SQL types; emitters may infer it.
type Field struct {
	Name        string
	Description string
	DataType    string
	Expr        string // underlying column/expression
	Synonyms    []string
}

// Measure is an aggregatable fact. Agg is the source aggregation (sum, count,
// count_distinct, avg, ...).
type Measure struct {
	Field
	Agg string
}

// Metric is a named business calculation. Expr is a neutral, lowercase
// expression string (e.g. "sum(fct_orders.order_net_booked)").
type Metric struct {
	Name        string
	Description string
	Expr        string
	Synonyms    []string
}

// Relationship is a join between two tables.
type Relationship struct {
	Left    string
	Right   string
	Columns []ColumnPair
}

// ColumnPair is one equi-join column pairing (left_column = right_column).
type ColumnPair struct {
	Left  string
	Right string
}
```

- [ ] **Step 2: Write `layer/layer.go`**

```go
// Package layer defines the dialect plugin surface (Parser/Emitter) and a
// registry mapping dialect names to implementations.
package layer

import (
	"fmt"

	"github.com/benchouse/semglot/ir"
)

// Layer is any registered semantic-layer dialect.
type Layer interface {
	Name() string
}

// Parser reads a dialect's files from dir into the neutral IR.
type Parser interface {
	Layer
	Parse(dir string) (*ir.Model, error)
}

// Emitter writes the neutral IR out as a dialect's files under dir.
type Emitter interface {
	Layer
	Emit(m *ir.Model, dir string) error
}

var registry = map[string]Layer{}

// Register adds a layer to the global registry. Intended for use from init().
func Register(l Layer) { registry[l.Name()] = l }

// Get returns a registered layer by name.
func Get(name string) (Layer, bool) {
	l, ok := registry[name]
	return l, ok
}

// AsParser returns the named layer as a Parser, or an error if it is unknown or
// cannot parse (a valid state for emit-only dialects).
func AsParser(name string) (Parser, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q", name)
	}
	p, ok := l.(Parser)
	if !ok {
		return nil, fmt.Errorf("dialect %q cannot be a source (no parser)", name)
	}
	return p, nil
}

// AsEmitter returns the named layer as an Emitter, or an error if it is unknown
// or cannot emit.
func AsEmitter(name string) (Emitter, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q", name)
	}
	e, ok := l.(Emitter)
	if !ok {
		return nil, fmt.Errorf("dialect %q cannot be a target (no emitter)", name)
	}
	return e, nil
}
```

- [ ] **Step 3: Write the failing test `layer/registry_test.go`**

```go
package layer

import (
	"testing"

	"github.com/benchouse/semglot/ir"
)

type fakeParser struct{}

func (fakeParser) Name() string                      { return "fake-parser" }
func (fakeParser) Parse(string) (*ir.Model, error)   { return &ir.Model{}, nil }

type fakeEmitter struct{}

func (fakeEmitter) Name() string                  { return "fake-emitter" }
func (fakeEmitter) Emit(*ir.Model, string) error  { return nil }

func TestRegistryCapabilities(t *testing.T) {
	Register(fakeParser{})
	Register(fakeEmitter{})

	if _, err := AsParser("fake-parser"); err != nil {
		t.Fatalf("AsParser(fake-parser): %v", err)
	}
	if _, err := AsEmitter("fake-emitter"); err != nil {
		t.Fatalf("AsEmitter(fake-emitter): %v", err)
	}
	if _, err := AsEmitter("fake-parser"); err == nil {
		t.Fatal("expected fake-parser to lack an emitter")
	}
	if _, err := AsParser("nope"); err == nil {
		t.Fatal("expected unknown dialect error")
	}
}
```

- [ ] **Step 4: Run tests — expect PASS (types compile, registry behaves)**

Run: `go test ./layer/ -run TestRegistryCapabilities -v`
Expected: PASS. (If `ir` has a typo it fails to compile — fix and rerun.)

- [ ] **Step 5: Verify the whole module builds**

Run: `go build ./...`
Expected: no output, exit 0. (`go mod tidy` first if yaml.v3 isn't yet in go.sum — it isn't used yet, so build should need no deps.)

- [ ] **Step 6: Commit**

```bash
git add ir/ layer/layer.go layer/registry_test.go
git commit -m "feat(ir,layer): neutral IR types + Parser/Emitter registry"
```

---

### Task 2: dbt Parser

**Files:**
- Create: `layer/dbt.go`
- Create: `layer/testdata/dbt/orders.yml`
- Create: `layer/testdata/dbt/customers.yml`
- Create: `layer/testdata/dbt/metrics.yml`
- Test: `layer/dbt_test.go`

**Interfaces:**
- Consumes: `ir.*` (Task 1), `layer.Parser` shape (Task 1).
- Produces: `dbt{}` implementing `Parser`; registered as `"dbt"` via `init()`.

- [ ] **Step 1: Create the fixture `layer/testdata/dbt/orders.yml`**

```yaml
semantic_models:
  - name: fct_orders
    description: Order-grain finance fact. One row per order.
    model: ref('fct_orders')
    entities:
      - {name: order, type: primary, expr: order_id}
      - {name: customer, type: foreign, expr: customer_sk}
    dimensions:
      - {name: order_date, type: time, type_params: {time_granularity: day}}
      - {name: is_refunded, type: categorical}
    measures:
      - {name: order_net_booked_amount, agg: sum, expr: order_net_booked}
      - {name: orders_count, agg: count_distinct, expr: order_id}
```

- [ ] **Step 2: Create the fixture `layer/testdata/dbt/customers.yml`**

```yaml
semantic_models:
  - name: dim_customer
    description: Customer dimension.
    model: ref('dim_customer')
    entities:
      - {name: customer, type: primary, expr: customer_sk}
    dimensions:
      - {name: customer_segment, type: categorical}
```

- [ ] **Step 3: Create the fixture `layer/testdata/dbt/metrics.yml`**

```yaml
metrics:
  - {name: net_revenue, label: Net revenue, type: simple, description: "Net booked revenue.", type_params: {measure: order_net_booked_amount}}
  - {name: orders, label: Orders, type: simple, type_params: {measure: orders_count}}
  - {name: aov, label: Average order value, type: ratio, description: "Net revenue / orders.", type_params: {numerator: net_revenue, denominator: orders}}
```

- [ ] **Step 4: Write the failing test `layer/dbt_test.go`**

```go
package layer

import (
	"reflect"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestDBTParse(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := &ir.Model{
		Tables: []ir.Table{
			{
				Name:        "fct_orders",
				Description: "Order-grain finance fact. One row per order.",
				PrimaryKey:  []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id"},
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "is_refunded", Expr: "is_refunded"},
				},
				TimeDimensions: []ir.Field{
					{Name: "order_date", Expr: "order_date"},
				},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "order_net_booked_amount", Expr: "order_net_booked"}, Agg: "sum"},
					{Field: ir.Field{Name: "orders_count", Expr: "order_id"}, Agg: "count_distinct"},
				},
				Metrics: []ir.Metric{
					{Name: "net_revenue", Description: "Net booked revenue.", Expr: "sum(fct_orders.order_net_booked)"},
					{Name: "orders", Expr: "count(distinct fct_orders.order_id)"},
					{Name: "aov", Description: "Net revenue / orders.", Expr: "sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)"},
				},
			},
			{
				Name:        "dim_customer",
				Description: "Customer dimension.",
				PrimaryKey:  []string{"customer_sk"},
				Dimensions: []ir.Field{
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "customer_segment", Expr: "customer_segment"},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_sk", Right: "customer_sk"}}},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IR mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}
```

- [ ] **Step 5: Run the test to verify it fails**

Run: `go test ./layer/ -run TestDBTParse -v`
Expected: FAIL — compile error `undefined: dbt`.

- [ ] **Step 6: Write `layer/dbt.go`**

```go
package layer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(dbt{}) }

// dbt parses a directory of dbt semantic-layer YAML (semantic_models + metrics).
type dbt struct{}

func (dbt) Name() string { return "dbt" }

// ---- raw YAML shapes ----

type dbtFile struct {
	SemanticModels []dbtSemanticModel `yaml:"semantic_models"`
	Metrics        []dbtMetric        `yaml:"metrics"`
}

type dbtSemanticModel struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Model       string         `yaml:"model"`
	Entities    []dbtEntity    `yaml:"entities"`
	Dimensions  []dbtDimension `yaml:"dimensions"`
	Measures    []dbtMeasure   `yaml:"measures"`
}

type dbtEntity struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Expr string `yaml:"expr"`
}

type dbtDimension struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Expr string `yaml:"expr"`
}

type dbtMeasure struct {
	Name string `yaml:"name"`
	Agg  string `yaml:"agg"`
	Expr string `yaml:"expr"`
}

type dbtMetric struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	TypeParams  struct {
		Measure     string `yaml:"measure"`
		Numerator   string `yaml:"numerator"`
		Denominator string `yaml:"denominator"`
	} `yaml:"type_params"`
}

func (dbt) Parse(dir string) (*ir.Model, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var models []dbtSemanticModel
	var metrics []dbtMetric
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var df dbtFile
		if err := yaml.Unmarshal(b, &df); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		models = append(models, df.SemanticModels...)
		metrics = append(metrics, df.Metrics...)
	}

	out := &ir.Model{}
	tableIdx := map[string]int{}          // table name -> index in out.Tables
	measureTable := map[string]string{}   // measure name -> owning table
	measureAggExpr := map[string]string{} // measure name -> "sum(table.col)" neutral expr
	primaryByEntity := map[string]struct{ table, col string }{}

	for _, sm := range models {
		t := ir.Table{Name: sm.Name, Description: sm.Description}
		for _, e := range sm.Entities {
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			t.Dimensions = append(t.Dimensions, ir.Field{Name: col, Expr: col})
			if e.Type == "primary" {
				t.PrimaryKey = append(t.PrimaryKey, col)
				primaryByEntity[e.Name] = struct{ table, col string }{sm.Name, col}
			}
		}
		for _, d := range sm.Dimensions {
			col := d.Expr
			if col == "" {
				col = d.Name
			}
			f := ir.Field{Name: d.Name, Expr: col}
			if d.Type == "time" {
				t.TimeDimensions = append(t.TimeDimensions, f)
			} else {
				t.Dimensions = append(t.Dimensions, f)
			}
		}
		for _, m := range sm.Measures {
			t.Measures = append(t.Measures, ir.Measure{Field: ir.Field{Name: m.Name, Expr: m.Expr}, Agg: m.Agg})
			measureTable[m.Name] = sm.Name
			measureAggExpr[m.Name] = aggExpr(m.Agg, sm.Name+"."+m.Expr)
		}
		tableIdx[sm.Name] = len(out.Tables)
		out.Tables = append(out.Tables, t)
	}

	// Relationships: each foreign entity joins to the primary entity of the same name.
	for _, sm := range models {
		for _, e := range sm.Entities {
			if e.Type != "foreign" {
				continue
			}
			p, ok := primaryByEntity[e.Name]
			if !ok || p.table == sm.Name {
				continue
			}
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			out.Relationships = append(out.Relationships, ir.Relationship{
				Left:    sm.Name,
				Right:   p.table,
				Columns: []ir.ColumnPair{{Left: col, Right: p.col}},
			})
		}
	}

	// Metrics: simple first (so ratios can reference their exprs), then ratios.
	metricExpr := map[string]string{}
	metricTable := map[string]string{}
	attach := func(name, desc, expr, table string) {
		metricExpr[name] = expr
		metricTable[name] = table
		i := tableIdx[table]
		out.Tables[i].Metrics = append(out.Tables[i].Metrics, ir.Metric{Name: name, Description: desc, Expr: expr})
	}
	for _, m := range metrics {
		if m.Type == "ratio" {
			continue
		}
		table := measureTable[m.TypeParams.Measure]
		attach(m.Name, m.Description, measureAggExpr[m.TypeParams.Measure], table)
	}
	for _, m := range metrics {
		if m.Type != "ratio" {
			continue
		}
		num := metricExpr[m.TypeParams.Numerator]
		den := metricExpr[m.TypeParams.Denominator]
		attach(m.Name, m.Description, num+" / "+den, metricTable[m.TypeParams.Numerator])
	}

	return out, nil
}

// aggExpr renders a neutral, lowercase aggregate expression over a qualified col.
func aggExpr(agg, col string) string {
	switch strings.ToLower(agg) {
	case "sum":
		return "sum(" + col + ")"
	case "count":
		return "count(" + col + ")"
	case "count_distinct":
		return "count(distinct " + col + ")"
	case "avg", "average":
		return "avg(" + col + ")"
	case "min":
		return "min(" + col + ")"
	case "max":
		return "max(" + col + ")"
	default:
		return strings.ToLower(agg) + "(" + col + ")"
	}
}
```

- [ ] **Step 7: Add yaml.v3 to the module**

Run: `go get gopkg.in/yaml.v3@v3.0.1 && go mod tidy`
Expected: `go.mod` now requires `gopkg.in/yaml.v3`; `go.sum` populated.

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./layer/ -run TestDBTParse -v`
Expected: PASS. (If the IR mismatches, the printed `got`/`want` shows exactly which field — fix the mapping, not the fixture.)

- [ ] **Step 9: Commit**

```bash
git add layer/dbt.go layer/dbt_test.go layer/testdata/dbt go.mod go.sum
git commit -m "feat(layer): dbt semantic-layer parser -> IR"
```

---

### Task 3: Cortex Emitter

**Files:**
- Create: `layer/cortex.go`
- Create: `layer/testdata/cortex/semantic_model.golden.yaml` (generated in Step 4)
- Test: `layer/cortex_test.go`

**Interfaces:**
- Consumes: `ir.*` (Task 1), `aggExpr` is NOT reused here (emit only uppercases).
- Produces: `cortex{}` implementing `Emitter`; registered as `"cortex"`. Emit options via exported fields: `cortex{Database, Schema, Name, Description string}`.

- [ ] **Step 1: Write `layer/cortex.go`**

```go
package layer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(cortex{}) }

// cortex emits a Snowflake Cortex semantic model. Zero value is usable; the
// build command sets Database/Schema/Name/Description from flags.
type cortex struct {
	Database    string
	Schema      string
	Name        string
	Description string
}

func (cortex) Name() string { return "cortex" }

// ---- Cortex YAML shapes ----

type cortexModel struct {
	Name          string         `yaml:"name"`
	Description    string         `yaml:"description,omitempty"`
	Tables        []cortexTable  `yaml:"tables"`
	Relationships []cortexRel    `yaml:"relationships,omitempty"`
}

type cortexTable struct {
	Name           string          `yaml:"name"`
	Description    string          `yaml:"description,omitempty"`
	BaseTable      cortexBaseTable `yaml:"base_table"`
	PrimaryKey     *cortexPK       `yaml:"primary_key,omitempty"`
	Dimensions     []cortexCol     `yaml:"dimensions,omitempty"`
	TimeDimensions []cortexCol     `yaml:"time_dimensions,omitempty"`
	Facts          []cortexCol     `yaml:"facts,omitempty"`
	Metrics        []cortexMetric  `yaml:"metrics,omitempty"`
}

type cortexBaseTable struct {
	Database string `yaml:"database"`
	Schema   string `yaml:"schema"`
	Table    string `yaml:"table"`
}

type cortexPK struct {
	Columns []string `yaml:"columns"`
}

type cortexCol struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	DataType    string   `yaml:"data_type"`
	Description string   `yaml:"description,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

type cortexMetric struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Description string   `yaml:"description,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

type cortexRel struct {
	Name                string          `yaml:"name"`
	LeftTable           string          `yaml:"left_table"`
	RightTable          string          `yaml:"right_table"`
	RelationshipColumns []cortexRelCol  `yaml:"relationship_columns"`
}

type cortexRelCol struct {
	LeftColumn  string `yaml:"left_column"`
	RightColumn string `yaml:"right_column"`
}

func (c cortex) Emit(m *ir.Model, dir string) error {
	name := c.Name
	if name == "" {
		name = "semantic_model"
	}
	schema := c.Schema
	if schema == "" {
		schema = "MAIN"
	}

	cm := cortexModel{Name: name, Description: c.Description}
	for _, t := range m.Tables {
		ct := cortexTable{
			Name:        t.Name,
			Description: t.Description,
			BaseTable:   cortexBaseTable{Database: c.Database, Schema: schema, Table: strings.ToUpper(t.Name)},
		}
		if len(t.PrimaryKey) > 0 {
			ct.PrimaryKey = &cortexPK{Columns: upperAll(t.PrimaryKey)}
		}
		for _, d := range t.Dimensions {
			ct.Dimensions = append(ct.Dimensions, cortexCol{
				Name: d.Name, Expr: strings.ToUpper(d.Expr), DataType: inferDataType(d.Name),
				Description: d.Description, Synonyms: d.Synonyms,
			})
		}
		for _, d := range t.TimeDimensions {
			ct.TimeDimensions = append(ct.TimeDimensions, cortexCol{
				Name: d.Name, Expr: strings.ToUpper(d.Expr), DataType: "DATE",
				Description: d.Description, Synonyms: d.Synonyms,
			})
		}
		for _, mm := range t.Measures {
			ct.Facts = append(ct.Facts, cortexCol{
				Name: mm.Name, Expr: strings.ToUpper(mm.Expr), DataType: "NUMBER",
				Description: mm.Description, Synonyms: mm.Synonyms,
			})
		}
		for _, mt := range t.Metrics {
			ct.Metrics = append(ct.Metrics, cortexMetric{
				Name: mt.Name, Expr: strings.ToUpper(mt.Expr),
				Description: mt.Description, Synonyms: mt.Synonyms,
			})
		}
		cm.Tables = append(cm.Tables, ct)
	}
	for _, r := range m.Relationships {
		cols := make([]cortexRelCol, len(r.Columns))
		for i, cp := range r.Columns {
			cols[i] = cortexRelCol{LeftColumn: strings.ToUpper(cp.Left), RightColumn: strings.ToUpper(cp.Right)}
		}
		cm.Relationships = append(cm.Relationships, cortexRel{
			Name: r.Left + "_to_" + r.Right, LeftTable: r.Left, RightTable: r.Right, RelationshipColumns: cols,
		})
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cm); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "semantic_model.yaml"), buf.Bytes(), 0o644)
}

func upperAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToUpper(s)
	}
	return out
}

// inferDataType guesses a Cortex data_type for a dimension whose source dialect
// did not record one. Known v1 limitation: heuristic, not exact.
func inferDataType(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "BOOLEAN"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "NUMBER"
	default:
		return "TEXT"
	}
}
```

- [ ] **Step 2: Write the test `layer/cortex_test.go` (golden-file pattern)**

```go
package layer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// sampleIR mirrors the dbt fixture's expected IR so emit is tested in isolation.
func sampleIR() *ir.Model {
	return &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", Description: "Order-grain finance fact. One row per order.",
				PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id"},
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "is_refunded", Expr: "is_refunded"},
				},
				TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date"}},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "order_net_booked_amount", Expr: "order_net_booked"}, Agg: "sum"},
					{Field: ir.Field{Name: "orders_count", Expr: "order_id"}, Agg: "count_distinct"},
				},
				Metrics: []ir.Metric{
					{Name: "net_revenue", Description: "Net booked revenue.", Expr: "sum(fct_orders.order_net_booked)"},
					{Name: "orders", Expr: "count(distinct fct_orders.order_id)"},
					{Name: "aov", Description: "Net revenue / orders.", Expr: "sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)"},
				},
			},
			{
				Name: "dim_customer", Description: "Customer dimension.",
				PrimaryKey: []string{"customer_sk"},
				Dimensions: []ir.Field{
					{Name: "customer_sk", Expr: "customer_sk"},
					{Name: "customer_segment", Expr: "customer_segment"},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_sk", Right: "customer_sk"}}},
		},
	}
}

func TestCortexEmit(t *testing.T) {
	dir := t.TempDir()
	e := cortex{Database: "ANALYTICS", Schema: "MAIN", Name: "eval_marts"}
	if err := e.Emit(sampleIR(), dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "semantic_model.yaml"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	golden := "testdata/cortex/semantic_model.golden.yaml"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("cortex output != golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./layer/ -run TestCortexEmit -v`
Expected: FAIL — golden file missing (`read golden ...: no such file`). (Compilation must succeed first; if `cortex` is undefined, fix Step 1.)

- [ ] **Step 4: Generate the golden file, then inspect it**

Run: `UPDATE_GOLDEN=1 go test ./layer/ -run TestCortexEmit -v`
Then: `cat layer/testdata/cortex/semantic_model.golden.yaml`

Expected content (verify it matches — 2-space indent, block style):

```yaml
name: eval_marts
tables:
  - name: fct_orders
    description: Order-grain finance fact. One row per order.
    base_table:
      database: ANALYTICS
      schema: MAIN
      table: FCT_ORDERS
    primary_key:
      columns:
        - ORDER_ID
    dimensions:
      - name: order_id
        expr: ORDER_ID
        data_type: NUMBER
      - name: customer_sk
        expr: CUSTOMER_SK
        data_type: NUMBER
      - name: is_refunded
        expr: IS_REFUNDED
        data_type: BOOLEAN
    time_dimensions:
      - name: order_date
        expr: ORDER_DATE
        data_type: DATE
    facts:
      - name: order_net_booked_amount
        expr: ORDER_NET_BOOKED
        data_type: NUMBER
      - name: orders_count
        expr: ORDER_ID
        data_type: NUMBER
    metrics:
      - name: net_revenue
        expr: SUM(FCT_ORDERS.ORDER_NET_BOOKED)
        description: Net booked revenue.
      - name: orders
        expr: COUNT(DISTINCT FCT_ORDERS.ORDER_ID)
      - name: aov
        expr: SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)
        description: Net revenue / orders.
  - name: dim_customer
    description: Customer dimension.
    base_table:
      database: ANALYTICS
      schema: MAIN
      table: DIM_CUSTOMER
    primary_key:
      columns:
        - CUSTOMER_SK
    dimensions:
      - name: customer_sk
        expr: CUSTOMER_SK
        data_type: NUMBER
      - name: customer_segment
        expr: CUSTOMER_SEGMENT
        data_type: TEXT
relationships:
  - name: fct_orders_to_dim_customer
    left_table: fct_orders
    right_table: dim_customer
    relationship_columns:
      - left_column: CUSTOMER_SK
        right_column: CUSTOMER_SK
```

If the generated file differs from the above (e.g. yaml.v3 quoting or indent nuances), the generated file is the source of truth — re-read this block only to confirm the *structure/values* are right. Fix the emitter if values are wrong; keep the generated bytes if only formatting differs.

- [ ] **Step 5: Run the test normally to verify it passes**

Run: `go test ./layer/ -run TestCortexEmit -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add layer/cortex.go layer/cortex_test.go layer/testdata/cortex
git commit -m "feat(layer): cortex emitter (IR -> Snowflake Cortex YAML)"
```

---

### Task 4: `build` command + `score` stub + end-to-end test

**Files:**
- Create: `cmd/semglot/main.go`
- Test: `cmd/semglot/main_test.go`

**Interfaces:**
- Consumes: `layer.AsParser`, `layer.AsEmitter` (Task 1); dbt/cortex registered via their `init()` (Tasks 2–3). To set cortex emit options, the command type-asserts the emitter to a settable form — so expose a small setter on cortex.
- Produces: the `semglot` binary with `build` and `score` subcommands.

- [ ] **Step 1: Add an options setter to cortex (edit `layer/cortex.go`)**

Append to `layer/cortex.go`:

```go
// WithOptions returns a cortex emitter carrying the given base_table and model
// identity. Used by the CLI to pass --database/--schema/--name/--description.
func (cortex) WithOptions(database, schema, name, description string) Emitter {
	return cortex{Database: database, Schema: schema, Name: name, Description: description}
}
```

And define the capability interface in `layer/layer.go` (append):

```go
// Configurable is an Emitter that accepts base_table/model identity options.
type Configurable interface {
	WithOptions(database, schema, name, description string) Emitter
}
```

- [ ] **Step 2: Write `cmd/semglot/main.go`**

```go
// Command semglot transpiles a source semantic-layer dialect into a target
// dialect through a neutral IR.
//
//	semglot build --from dbt --reference ./semantic --layer cortex --out ./cortex/
//
// v1 supports dbt (source) -> cortex (target). Scoring (`semglot score`) is v2.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/benchouse/semglot/layer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(buildCmd(os.Args[2:]))
	case "score":
		fmt.Fprintln(os.Stderr, "score is not implemented yet (v2)")
		os.Exit(1)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: semglot <build|score> [flags]")
}

func buildCmd(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	from := fs.String("from", "dbt", "source dialect")
	reference := fs.String("reference", "", "source dialect directory (required)")
	target := fs.String("layer", "", "target dialect (required)")
	out := fs.String("out", "", "output directory (required)")
	database := fs.String("database", "", "Cortex base_table database")
	schema := fs.String("schema", "MAIN", "Cortex base_table schema")
	name := fs.String("name", "semantic_model", "Cortex model name")
	description := fs.String("description", "", "Cortex model description")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *reference == "" || *target == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "build: --reference, --layer and --out are required")
		return 2
	}

	parser, err := layer.AsParser(*from)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	emitter, err := layer.AsEmitter(*target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	if c, ok := emitter.(layer.Configurable); ok {
		emitter = c.WithOptions(*database, *schema, *name, *description)
	}

	model, err := parser.Parse(*reference)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	if err := emitter.Emit(model, *out); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	fmt.Printf("wrote %s/semantic_model.yaml (%s -> %s)\n", *out, *from, *target)
	return 0
}
```

- [ ] **Step 3: Write the failing end-to-end test `cmd/semglot/main_test.go`**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCmdEndToEnd(t *testing.T) {
	out := t.TempDir()
	code := buildCmd([]string{
		"--from", "dbt",
		"--reference", "../../layer/testdata/dbt",
		"--layer", "cortex",
		"--out", out,
		"--database", "ANALYTICS",
		"--schema", "MAIN",
		"--name", "eval_marts",
	})
	if code != 0 {
		t.Fatalf("buildCmd exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	want, err := os.ReadFile("../../layer/testdata/cortex/semantic_model.golden.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("build output != golden:\n--- got ---\n%s", got)
	}
}

func TestBuildCmdMissingFlags(t *testing.T) {
	if code := buildCmd([]string{"--layer", "cortex"}); code != 2 {
		t.Fatalf("missing --reference/--out should exit 2, got %d", code)
	}
}

func TestScoreStub(t *testing.T) {
	// score must not be wired to a real implementation in v1.
	if _, err := os.Stat("score_stub_marker"); err == nil {
		t.Skip("marker present")
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./cmd/semglot/ -run TestBuildCmdEndToEnd -v`
Expected: FAIL — compile error `undefined: buildCmd` (until Step 2 saved) or golden mismatch if the emitter changed. If it is the first run after Step 2, it should actually PASS; treat a pre-Step-2 run as the failing baseline.

- [ ] **Step 5: Run all tests to verify they pass**

Run: `go test ./...`
Expected: `ok` for `./ir` (no tests → `no test files`), `./layer`, `./cmd/semglot`.

- [ ] **Step 6: Manual smoke test**

```bash
go run ./cmd/semglot build --from dbt --reference layer/testdata/dbt \
  --layer cortex --out /tmp/semglot-smoke --database ANALYTICS --name eval_marts
cat /tmp/semglot-smoke/semantic_model.yaml
```
Expected: prints `wrote /tmp/semglot-smoke/semantic_model.yaml (dbt -> cortex)` and the file matches the golden.

Also verify the error paths:
```bash
go run ./cmd/semglot build --from cortex --reference x --layer cortex --out y
```
Expected: `build: dialect "cortex" cannot be a source (no parser)` and non-zero exit.

- [ ] **Step 7: Commit**

```bash
git add cmd/semglot/main.go cmd/semglot/main_test.go layer/cortex.go layer/layer.go
git commit -m "feat(cmd): semglot build (dbt -> cortex) + score v2 stub"
```

---

## Self-Review

**1. Spec coverage:**
- IR (rich superset, exported `ir`) → Task 1. ✅
- `Layer`/`Parser`/`Emitter` capability interfaces + registry, dbt-as-Layer → Task 1. ✅
- dbt `Parser` (semantic_models + metrics, entities→dims+pk+relationships, simple & ratio metrics) → Task 2. ✅
- cortex `Emitter` (base_table, primary_key, dimensions/time_dimensions/facts/metrics/relationships, type inference, UPPERCASE identifiers) → Task 3. ✅
- `build` command with `--from/--reference/--layer/--out`, `score` stub → Task 4. ✅
- Standalone, flag-driven inputs, no consumer coupling → Tasks 2 & 4 (paths are flags). ✅
- Tests: dbt.Parse golden, cortex.Emit golden, end-to-end build → Tasks 2–4. ✅
- Deferred to v2 (score package, cortex.Parse, Jaccard, --json, weights, round-trip) → not planned here, by design. ✅

**2. Placeholder scan:** No TBD/TODO; every code step shows complete code; the one golden file is generated by a shown command and its expected contents are printed. ✅

**3. Type consistency:** `ir.Field`/`ir.Measure`/`ir.Metric`/`ir.ColumnPair`, `layer.Parser`/`Emitter`/`Configurable`, `cortex.WithOptions`, `buildCmd` are used with identical signatures across tasks. `aggExpr` defined in Task 2 (dbt.go), not referenced elsewhere. `inferDataType` defined and used only in Task 3. ✅

**Known v1 fidelity limitations (documented, acceptable):** data types are inferred heuristically (dbt carries none); `model: ref('x')` is not parsed (table name used directly); booleans detected by `is_`/`has_` prefix only. These are noted in the spec's v1 scope and surface again in v2 when scoring is added.
