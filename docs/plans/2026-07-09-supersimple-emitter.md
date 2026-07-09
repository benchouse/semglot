# supersimple emitter + IR metric enrichment — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `supersimple` target dialect (dbt → per-table supersimple YAML), and enrich the neutral IR so metrics carry structure (not Cortex-shaped SQL).

**Architecture:** Enrich `ir.Metric` (structured fields + `Label`) and `ir.Table` (`Grain`); the dbt parser populates them; Cortex keeps rendering from `Expr` (unchanged); a new `supersimple` `Emitter` reads the structure. One output file per table.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3`, `github.com/DataDog/go-sqllexer` (already present). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; exported packages (`ir`, `layer`), no `internal/`.
- Cross-dialect identifiers lowercase in IR, UPPERCASED on emit.
- **Cortex output must not change** — `test/models/ecommerce/dbt/cortex/ecommerce.yaml` byte-identical after this work (regression guard).
- `go build ./...`, `go test ./...`, `gofmt -l .` (empty), `go vet ./...` all clean per task.
- Reuse existing helpers: `upperAll` (cortex.go), `isIdent` (dbt.go). Do not duplicate.

## File Structure

- `ir/model.go` — add `Table.Grain`; enrich `Metric`.
- `layer/dbt.go` — parse `label`/`defaults.agg_time_dimension`; populate structured metric fields + grain.
- `layer/dbt_test.go` — update `TestDBTParse`; add label/grain test.
- `layer/testdata/dbt_label_grain/model.yml` — new fixture.
- `layer/supersimple.go` — new `Emitter`.
- `layer/supersimple_test.go` — unit test.
- `cmd/semglot/main.go` — reorder Notes-print to after Emit; neutralize messages.
- `test/models/ecommerce/dbt/metrics.yml` — add `label:`s.
- `test/models/ecommerce/dbt/supersimple/*.yaml` — new multi-file golden.
- `test/integration_test.go` — add supersimple golden test.

---

### Task 1: Enrich the IR + dbt parser (structured metrics, Label, Grain)

**Files:**
- Modify: `ir/model.go`
- Modify: `layer/dbt.go`
- Modify: `layer/dbt_test.go`
- Create: `layer/testdata/dbt_label_grain/model.yml`

**Interfaces:**
- Produces: `ir.Metric{Label, Kind, Agg, Table, Column, Numerator, Denominator}` and `ir.Table.Grain`, populated by `dbt.Parse`. Consumed by the supersimple emitter (Task 2).

- [ ] **Step 1: Enrich `ir/model.go`**

Add `Grain` to `Table` (after `Metrics`):
```go
	Metrics        []Metric
	Grain          string // default time-dimension (dbt defaults.agg_time_dimension); "" if none
```
Replace the `Metric` struct with:
```go
// Metric is a named business calculation. Expr is the rendered SQL (used by
// SQL-shaped targets like Cortex); the structured fields let other targets
// (e.g. supersimple) build their own form without re-parsing SQL.
type Metric struct {
	Name        string
	Label       string // dbt metric label (display name); "" if none
	Description string
	Expr        string
	Synonyms    []string
	Kind        string // "simple" | "ratio"
	Agg         string // simple: sum | count | count_distinct | avg | min | max
	Table       string // owning table (model) name
	Column      string // simple: aggregated column (bare) or the raw expr if compound
	Numerator   string // ratio: numerator metric name
	Denominator string // ratio: denominator metric name
}
```

- [ ] **Step 2: Parse the new dbt fields (`layer/dbt.go`)**

Add `Label` to `dbtMetric`:
```go
type dbtMetric struct {
	Name        string `yaml:"name"`
	Label       string `yaml:"label"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	TypeParams  struct {
		Measure     string `yaml:"measure"`
		Numerator   string `yaml:"numerator"`
		Denominator string `yaml:"denominator"`
	} `yaml:"type_params"`
}
```
Add `Defaults` to `dbtSemanticModel`:
```go
type dbtSemanticModel struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Model       string         `yaml:"model"`
	Defaults    struct {
		AggTimeDimension string `yaml:"agg_time_dimension"`
	} `yaml:"defaults"`
	Entities    []dbtEntity    `yaml:"entities"`
	Dimensions  []dbtDimension `yaml:"dimensions"`
	Measures    []dbtMeasure   `yaml:"measures"`
}
```

- [ ] **Step 3: Populate grain + measure maps (`layer/dbt.go`)**

Near the other measure maps at the top of `Parse` (`measureTable`, `measureAggExpr`), add:
```go
	measureAgg := map[string]string{}
	measureCol := map[string]string{}
```
In the per-table loop, set grain right after the table description block:
```go
		t.Grain = sm.Defaults.AggTimeDimension
```
In the measures loop, add the two map writes:
```go
		for _, m := range sm.Measures {
			f := field(m.Name, m.Expr)
			if a := strings.ToLower(m.Agg); a == "count" || a == "count_distinct" {
				f.Description = ""
			}
			t.Measures = append(t.Measures, ir.Measure{Field: f, Agg: m.Agg})
			measureTable[m.Name] = name
			measureAgg[m.Name] = m.Agg
			measureCol[m.Name] = m.Expr
			measureAggExpr[m.Name] = aggExpr(m.Agg, qualifyExpr(name, cols, m.Expr))
		}
```

- [ ] **Step 4: Populate structured metric fields (`layer/dbt.go`)**

Replace the whole metrics section (the `attach` closure + the three loops) with:
```go
	metricExpr := map[string]string{}
	metricTable := map[string]string{}
	attach := func(table string, mt ir.Metric) {
		metricExpr[mt.Name] = mt.Expr
		metricTable[mt.Name] = table
		i := tableIdx[table]
		out.Tables[i].Metrics = append(out.Tables[i].Metrics, mt)
	}
	for _, m := range metrics {
		if m.Type != "simple" {
			continue
		}
		meas := m.TypeParams.Measure
		expr, table := measureAggExpr[meas], measureTable[meas]
		if expr == "" || table == "" {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("measure %q not found in the parsed semantic models", meas)))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Expr: expr,
			Kind: "simple", Agg: measureAgg[meas], Table: table, Column: measureCol[meas],
		})
	}
	for _, m := range metrics {
		if m.Type != "ratio" {
			continue
		}
		num, okN := metricExpr[m.TypeParams.Numerator]
		den, okD := metricExpr[m.TypeParams.Denominator]
		table, okT := metricTable[m.TypeParams.Numerator]
		if !okN || !okD || !okT {
			out.Notes = append(out.Notes, metricNote(m, "one or more ratio operands could not be resolved to a metric"))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Expr: num + " / " + den,
			Kind: "ratio", Table: table, Numerator: m.TypeParams.Numerator, Denominator: m.TypeParams.Denominator,
		})
	}
	for _, m := range metrics {
		switch m.Type {
		case "simple", "ratio":
			// handled above
		default:
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("unsupported metric type %q", m.Type)))
		}
	}

	return out, nil
}
```
(The `metricNote` helper and everything after are unchanged.)

- [ ] **Step 5: Update `TestDBTParse` expected metrics (`layer/dbt_test.go`)**

Replace the `Metrics: []ir.Metric{...}` block inside `TestDBTParse`'s `want.Tables[0]` with:
```go
				Metrics: []ir.Metric{
					{Name: "net_revenue", Description: "Net booked revenue.", Expr: "sum(fct_orders.order_net_booked)", Kind: "simple", Agg: "sum", Table: "fct_orders", Column: "order_net_booked"},
					{Name: "orders", Expr: "count(distinct fct_orders.order_id)", Kind: "simple", Agg: "count_distinct", Table: "fct_orders", Column: "order_id"},
					{Name: "aov", Description: "Net revenue / orders.", Expr: "sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)", Kind: "ratio", Table: "fct_orders", Numerator: "net_revenue", Denominator: "orders"},
				},
```

- [ ] **Step 6: Add the label/grain fixture**

Create `layer/testdata/dbt_label_grain/model.yml`:
```yaml
semantic_models:
  - name: fct_orders
    description: Orders.
    model: ref('fct_orders')
    defaults: {agg_time_dimension: order_date}
    entities:
      - {name: order, type: primary, expr: order_id}
    dimensions:
      - {name: order_date, type: time, type_params: {time_granularity: day}}
    measures:
      - {name: order_net_booked_amount, agg: sum, expr: order_net_booked}

metrics:
  - {name: net_revenue, label: Net revenue, type: simple, type_params: {measure: order_net_booked_amount}}
```

- [ ] **Step 7: Add the label/grain test (`layer/dbt_test.go`)**

```go
func TestDBTParseLabelAndGrain(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt_label_grain")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Tables[0].Grain != "order_date" {
		t.Fatalf("grain = %q, want order_date", got.Tables[0].Grain)
	}
	m := got.Tables[0].Metrics[0]
	if m.Label != "Net revenue" {
		t.Fatalf("label = %q, want 'Net revenue'", m.Label)
	}
	if m.Kind != "simple" || m.Agg != "sum" || m.Table != "fct_orders" || m.Column != "order_net_booked" {
		t.Fatalf("structured fields wrong: %+v", m)
	}
}
```

- [ ] **Step 8: Run tests — parser green AND Cortex golden unchanged**

Run: `go test ./layer/ ./cmd/... ./test/ -v -run 'DBTParse|Cortex'`
Expected: `TestDBTParse`, `TestDBTParseLabelAndGrain` PASS; `TestEcommerceCortexGolden` + `TestCortexEmit` PASS (Cortex reads `Expr`, which is unchanged). Then `go test ./...` all green, `gofmt -l .` empty, `go vet ./...` clean.

- [ ] **Step 9: Commit**

```bash
git add ir/model.go layer/dbt.go layer/dbt_test.go layer/testdata/dbt_label_grain
git commit -m "feat(ir): structured metrics + Metric.Label + Table.Grain"
```

---

### Task 2: supersimple emitter

**Files:**
- Create: `layer/supersimple.go`
- Create: `layer/supersimple_test.go`

**Interfaces:**
- Consumes: `ir.*` incl. the Task 1 fields; `upperAll` (cortex.go), `isIdent` (dbt.go).
- Produces: `supersimple{}` implementing `Emitter` + `Configurable`, registered `"supersimple"`. Writes `<dir>/<UPPER(table)>.yaml` per table; appends skip-notes to `m.Notes`.

- [ ] **Step 1: Write `layer/supersimple.go`**

```go
package layer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(supersimple{}) }

// supersimple emits one supersimple config YAML per model. Zero value usable;
// the build command sets Schema from --schema.
type supersimple struct {
	Schema string
}

func (supersimple) Name() string { return "supersimple" }

// WithOptions lets the CLI pass --schema (database/name/description are unused).
func (supersimple) WithOptions(database, schema, name, description string) Emitter {
	return supersimple{Schema: schema}
}

const ssHeader = "# yaml-language-server: $schema=https://assets.supersimple.io/configuration_schema/1.0.0/supersimple_configuration_schema.json\n"

type ssFile struct {
	Models  map[string]ssModel  `yaml:"models"`
	Metrics map[string]ssMetric `yaml:"metrics,omitempty"`
}
type ssModel struct {
	Name        string                `yaml:"name"`
	Table       string                `yaml:"table"`
	PrimaryKey  []string              `yaml:"primary_key,omitempty"`
	Description string                `yaml:"description,omitempty"`
	Properties  map[string]ssProperty `yaml:"properties,omitempty"`
	Relations   map[string]ssRelation `yaml:"relations,omitempty"`
}
type ssProperty struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
}
type ssRelation struct {
	Name         string         `yaml:"name"`
	Type         string         `yaml:"type"`
	ModelID      string         `yaml:"model_id"`
	JoinStrategy ssJoinStrategy `yaml:"join_strategy"`
}
type ssJoinStrategy struct {
	JoinKey string `yaml:"join_key"`
}
type ssMetric struct {
	Name        string        `yaml:"name"`
	ModelID     string        `yaml:"model_id"`
	Aggregation ssAggregation `yaml:"aggregation"`
}
type ssAggregation struct {
	Type string `yaml:"type"`
	Key  string `yaml:"key"`
}

func (s supersimple) Emit(m *ir.Model, dir string) error {
	schema := s.Schema
	if schema == "" {
		schema = "MAIN"
	}
	// relationships grouped by parent (Right) table
	relsByParent := map[string][]ir.Relationship{}
	for _, r := range m.Relationships {
		relsByParent[r.Right] = append(relsByParent[r.Right], r)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, t := range m.Tables {
		id := strings.ToUpper(t.Name)
		model := ssModel{
			Name:        prettify(t.Name),
			Table:       schema + "." + id,
			PrimaryKey:  upperAll(t.PrimaryKey),
			Description: t.Description,
			Properties:  map[string]ssProperty{},
		}
		addProp := func(f ir.Field, typ string) {
			col := strings.ToUpper(f.Expr)
			if _, ok := model.Properties[col]; ok {
				return
			}
			model.Properties[col] = ssProperty{Name: col, Type: typ, Description: f.Description}
		}
		for _, d := range t.Dimensions {
			addProp(d, ssType(d.DataType, d.Name, false))
		}
		for _, d := range t.TimeDimensions {
			addProp(d, ssType(d.DataType, d.Name, true))
		}
		for _, meas := range t.Measures {
			if !isIdent(meas.Expr) { // a compound expr is not a column
				continue
			}
			addProp(meas.Field, ssType(meas.DataType, meas.Name, false))
		}
		for _, r := range relsByParent[t.Name] {
			child := r.Left
			join := ""
			if len(r.Columns) > 0 {
				join = strings.ToUpper(r.Columns[0].Right)
			}
			if model.Relations == nil {
				model.Relations = map[string]ssRelation{}
			}
			model.Relations[slug(child)] = ssRelation{
				Name: prettify(child), Type: "hasMany", ModelID: strings.ToUpper(child),
				JoinStrategy: ssJoinStrategy{JoinKey: join},
			}
		}

		file := ssFile{Models: map[string]ssModel{id: model}}
		for _, mt := range t.Metrics {
			if mt.Kind != "simple" || !isIdent(mt.Column) {
				m.Notes = append(m.Notes, fmt.Sprintf("metric %q not representable in supersimple (only simple aggregations over a column) — omitted", mt.Name))
				continue
			}
			if file.Metrics == nil {
				file.Metrics = map[string]ssMetric{}
			}
			nm := mt.Label
			if nm == "" {
				nm = mt.Name
			}
			file.Metrics[mt.Name] = ssMetric{
				Name: nm, ModelID: strings.ToUpper(mt.Table),
				Aggregation: ssAggregation{Type: mapAgg(mt.Agg), Key: strings.ToUpper(mt.Column)},
			}
		}

		var buf bytes.Buffer
		buf.WriteString(ssHeader)
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(file); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, id+".yaml"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// prettify turns a model name into a display label: strip fct_/dim_/obt_/stg_
// prefix, spaces for underscores, capitalize. "fct_order_lines" -> "Order lines".
func prettify(name string) string {
	s := stripPrefix(name)
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// slug is the relation key: prefix-stripped, underscores kept. "fct_order_lines" -> "order_lines".
func slug(name string) string {
	if s := stripPrefix(name); s != "" {
		return s
	}
	return name
}

func stripPrefix(s string) string {
	for _, p := range []string{"fct_", "dim_", "obt_", "stg_"} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

// ssType maps to supersimple's property type vocabulary, preferring a real dbt
// data_type and falling back to a name/role heuristic. Enum/format not emitted.
func ssType(dbtType, name string, isTime bool) string {
	if dbtType != "" {
		return ssMapType(dbtType)
	}
	if isTime {
		return "Date"
	}
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "Boolean"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "Number"
	default:
		return "String"
	}
}

func ssMapType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "number", "int", "integer", "bigint", "smallint":
		return "Number"
	case "float", "double", "double precision", "real", "numeric", "decimal":
		return "Float"
	case "boolean", "bool":
		return "Boolean"
	case "date":
		return "Date"
	case "timestamp", "datetime", "timestamp_ntz", "timestamp_tz", "timestamp_ltz":
		return "Date"
	case "varchar", "text", "string", "char", "character varying":
		return "String"
	default:
		return "String"
	}
}

// mapAgg maps a dbt aggregation to supersimple's aggregation type.
func mapAgg(agg string) string {
	switch strings.ToLower(agg) {
	case "count_distinct":
		return "countDistinct"
	default:
		return strings.ToLower(agg) // sum, count, avg, min, max
	}
}
```

- [ ] **Step 2: Write the failing test `layer/supersimple_test.go`**

```go
package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestSupersimpleEmit(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", Description: "Orders.",
				PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{
					{Name: "order_id", Expr: "order_id", DataType: "number"},
					{Name: "is_refunded", Expr: "is_refunded", DataType: "boolean"},
				},
				TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date"}},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "order_net_booked_amount", Expr: "order_net_booked", DataType: "float"}, Agg: "sum"},
				},
				Metrics: []ir.Metric{
					{Name: "net_revenue", Label: "Net revenue", Kind: "simple", Agg: "sum", Table: "fct_orders", Column: "order_net_booked"},
					{Name: "refund_rate", Kind: "ratio", Table: "fct_orders", Numerator: "x", Denominator: "y"},
				},
			},
			{
				Name: "dim_customer", Description: "Customers.",
				PrimaryKey: []string{"customer_sk"},
				Dimensions: []ir.Field{{Name: "customer_sk", Expr: "customer_sk", DataType: "number"}},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_orders", Right: "dim_customer", Columns: []ir.ColumnPair{{Left: "customer_sk", Right: "customer_sk"}}},
		},
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	orders := readFile(t, filepath.Join(dir, "FCT_ORDERS.yaml"))
	for _, want := range []string{
		"# yaml-language-server:",
		"FCT_ORDERS:",
		"name: Orders",
		"table: MAIN.FCT_ORDERS",
		"type: Boolean",   // is_refunded
		"type: Date",      // order_date
		"type: Float",     // order_net_booked
		"type: Number",    // order_id
		"name: Net revenue",
		"type: sum",
		"key: ORDER_NET_BOOKED",
	} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}

	// hasMany relation lives on the PARENT (dim_customer).
	cust := readFile(t, filepath.Join(dir, "DIM_CUSTOMER.yaml"))
	for _, want := range []string{"relations:", "type: hasMany", "model_id: FCT_ORDERS", "join_key: CUSTOMER_SK"} {
		if !strings.Contains(cust, want) {
			t.Fatalf("DIM_CUSTOMER.yaml missing %q:\n%s", want, cust)
		}
	}

	// the ratio metric is omitted and reported via Notes.
	joined := strings.Join(m.Notes, "\n")
	if !strings.Contains(joined, "refund_rate") || !strings.Contains(joined, "not representable in supersimple") {
		t.Fatalf("expected a skip note for refund_rate, got: %v", m.Notes)
	}
	if strings.Contains(orders, "refund_rate") {
		t.Fatalf("ratio metric should not be emitted:\n%s", orders)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
```

- [ ] **Step 3: Run — expect FAIL then PASS**

Run: `go test ./layer/ -run TestSupersimpleEmit -v`
Expected: compiles (Step 1 saved) and PASSES. If it fails, the message names the missing snippet — fix the emitter, not the test.

- [ ] **Step 4: Full suite + hygiene**

Run: `go test ./... && gofmt -l . && go vet ./...`
Expected: all green, gofmt empty, vet clean. Cortex golden still unchanged.

- [ ] **Step 5: Commit**

```bash
git add layer/supersimple.go layer/supersimple_test.go
git commit -m "feat(layer): supersimple emitter (dbt -> per-table supersimple YAML)"
```

---

### Task 3: CLI wiring + ecommerce supersimple golden

**Files:**
- Modify: `cmd/semglot/main.go`
- Modify: `test/models/ecommerce/dbt/metrics.yml`
- Modify: `test/integration_test.go`
- Create: `test/models/ecommerce/dbt/supersimple/*.yaml` (generated)

**Interfaces:**
- Consumes: the `supersimple` emitter (Task 2, auto-registered).
- Produces: `build --layer supersimple` end-to-end; per-table goldens.

- [ ] **Step 1: Reorder Notes-print + neutralize messages (`cmd/semglot/main.go`)**

Move the `model.Notes` block to **after** `Emit`, and make both messages target-neutral. Replace:
```go
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d metric(s) not transpiled — passed through as custom_instructions:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	if err := emitter.Emit(model, *out); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	fmt.Printf("wrote %s/semantic_model.yaml (%s -> %s)\n", *out, *from, *target)
	return 0
```
with:
```go
	if err := emitter.Emit(model, *out); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d item(s) could not be fully transpiled:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	fmt.Printf("wrote to %s (%s -> %s)\n", *out, *from, *target)
	return 0
```

- [ ] **Step 2: Update the cmd end-to-end test's expected stdout (`cmd/semglot/main_test.go`)**

`TestBuildCmdEndToEnd` only checks the output file, not stdout — no change needed. Confirm it still reads `semantic_model.yaml` for the cortex case (it does). Run `go test ./cmd/... ` to confirm green.

- [ ] **Step 3: Add labels to the ecommerce metrics (`test/models/ecommerce/dbt/metrics.yml`)**

Add a `label:` to each metric so supersimple names read nicely. New file contents:
```yaml
metrics:
  - {name: gross_revenue, label: Gross revenue, type: simple, description: "Gross order revenue.", type_params: {measure: order_gross_amount}}
  - {name: net_revenue, label: Net revenue, type: simple, description: "Net booked revenue.", type_params: {measure: order_net_booked_amount}}
  - {name: orders, label: Orders, type: simple, type_params: {measure: orders_count}}
  - {name: refunded_orders, label: Refunded orders, type: simple, description: "Count of refunded orders.", type_params: {measure: refunded_orders_count}}
  - {name: units_sold, label: Units sold, type: simple, description: "Units sold.", type_params: {measure: quantity}}
  - {name: aov, label: Average order value, type: ratio, description: "Average order value (net revenue / orders).", type_params: {numerator: net_revenue, denominator: orders}}
  - {name: units_per_order, label: Units per order, type: ratio, description: "Units per order (cross-table).", type_params: {numerator: units_sold, denominator: orders}}
  - {name: refund_rate, label: Refund rate, type: ratio, description: "Refunded orders / all orders.", type_params: {numerator: refunded_orders, denominator: orders}}
```

- [ ] **Step 4: Verify the Cortex golden is UNCHANGED after adding labels**

Run: `go test ./test/ -run TestEcommerceCortexGolden -v`
Expected: PASS with no golden change (Cortex ignores `label`). If it fails, the emitter is wrongly reading `label` — stop and fix. Do NOT regenerate the cortex golden.

- [ ] **Step 5: Add the supersimple golden test (`test/integration_test.go`)**

Append:
```go
func TestEcommerceSupersimpleGolden(t *testing.T) {
	e, err := layer.AsEmitter("supersimple")
	if err != nil {
		t.Fatalf("AsEmitter: %v", err)
	}
	if c, ok := e.(layer.Configurable); ok {
		e = c.WithOptions("", "MAIN", "", "")
	}
	p, err := layer.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	model, err := p.Parse(projectDir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if err := e.Emit(model, out); err != nil {
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
}
```

- [ ] **Step 6: Generate the goldens and eyeball them**

Run: `UPDATE_GOLDEN=1 go test ./test/ -run TestEcommerceSupersimpleGolden`
Then: `ls test/models/ecommerce/dbt/supersimple/ && cat test/models/ecommerce/dbt/supersimple/FCT_ORDERS.yaml`
Expected: one file per table (`FCT_ORDERS.yaml`, `FCT_ORDER_LINES.yaml`, `DIM_CUSTOMER.yaml`, `DIM_PRODUCT.yaml`); FCT_ORDERS has typed properties, a `net_revenue`/etc. metrics block with `name: Net revenue`, and DIM_CUSTOMER/DIM_PRODUCT carry `hasMany` relations. Confirm no invented values.

- [ ] **Step 7: Full suite, hygiene, and manual smoke**

```bash
go test ./... && gofmt -l . && go vet ./...
go run ./cmd/semglot build --from dbt --reference test/models/ecommerce/dbt --layer supersimple --out /tmp/ss-smoke --schema MAIN
ls /tmp/ss-smoke
```
Expected: all green; the run prints a `warning:` line listing the omitted ratio metrics (aov, units_per_order, refund_rate) and `wrote to /tmp/ss-smoke (dbt -> supersimple)`; `/tmp/ss-smoke` has the per-table files.

- [ ] **Step 8: Commit**

```bash
git add cmd/semglot/main.go test/integration_test.go test/models/ecommerce/dbt/metrics.yml test/models/ecommerce/dbt/supersimple
git commit -m "feat(cmd,test): build --layer supersimple; ecommerce supersimple goldens"
```

---

## Self-Review

**1. Spec coverage:**
- IR enrichment (structured metrics, Label, Grain) → Task 1. ✅
- dbt parser populates them → Task 1. ✅
- Cortex unchanged / golden regression guard → Task 1 Step 8, Task 3 Step 4. ✅
- supersimple emitter: multi-file, properties (all cols typed), relations (hasMany-on-parent), simple metrics, ratio omitted+noted → Task 2. ✅
- CLI: `--layer supersimple`, Notes-after-Emit, neutral messages → Task 3. ✅
- Goldens + tests → Tasks 1–3. ✅
- Deferred (supersimple Parse, other emitters, Enum/format) → not planned, by design. ✅

**2. Placeholder scan:** No TBD/TODO; every code step shows full code; goldens generated by shown commands. ✅

**3. Type consistency:** `ir.Metric.{Label,Kind,Agg,Table,Column,Numerator,Denominator}`, `ir.Table.Grain`, `ssType/ssMapType/mapAgg/prettify/slug/stripPrefix`, `supersimple.WithOptions`, reused `upperAll`/`isIdent` — consistent across tasks. `mapAgg` is defined once (supersimple.go) and not used elsewhere; `prettify`/`slug` likewise. No collision with cortex.go (`mapDbtType`, `inferDataType`, `pickType` are distinct names). ✅
