# Snowflake semantic-view + nao emitters — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three registered `Emitter`s — `snowflake-semantic-view`, `nao-yaml`, `nao-context-rules` — transpiling the dbt IR into the eval's remaining context-layer formats, each verified against the ecommerce fixture with a byte-identical golden.

**Architecture:** Each emitter mirrors the existing `cortex`/`supersimple` pattern: a `struct{}` registered via `init()`, implementing `Emit(m *ir.Model, dir string) error`, writing ONE file. Metric SQL comes from the shared `renderSQL(mt.Def, resolve)`; the provisional cumulative/conversion kinds degrade via the shared `cortexDegrade`. `snowflake-semantic-view` and `nao-yaml` are `Configurable`. Design reference: `docs/design-context-layer-emitters.md`.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (only dep). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; exported packages, no `internal/`; no new deps.
- **Existing goldens untouched:** `test/models/ecommerce/dbt/{cortex,supersimple,dbt}/*` must not change. New capability is exercised only by NEW files under new `test/models/ecommerce/dbt/<target>/` dirs.
- **`Emit` is READ-ONLY over `m`.** Never append to `m.Notes`; accumulate degrade notes in a local slice (`append(slices.Clone(m.Notes), local...)`), matching the post-refactor cortex/supersimple.
- **Table aliases are physical names** (`FCT_ORDERS as DB.SCHEMA.FCT_ORDERS`).
- Reuse `renderSQL`, `cortexDegrade`, `upperAll`, the `Configurable` pattern, and the integration-test golden harness (`UPDATE_GOLDEN=1`). Do not reimplement lowering.
- Identifiers UPPERCASED on emit for Snowflake SQL; `renderSQL` output is lowercase and is uppercased by the semantic-view emitter (as cortex does), left as-is (lowercase) for nao formulas.
- `go build ./...`, `go test ./...`, `test -z "$(gofmt -l .)"`, `go vet ./...` clean per task.

## Fixture facts (for test assertions)

Tables: `fct_orders` (PK order_id), `fct_order_lines` (PK order_line_id), `dim_customer` (PK customer_sk), `dim_product` (PK product_id).
Relationships: `fct_orders→dim_customer` (customer_sk=customer_sk), `fct_order_lines→fct_orders` (order_id=order_id), `fct_order_lines→dim_product` (product_id=product_id).
Metrics and their `renderSQL(Def)` (lowercase; uppercased for Snowflake):
- `gross_revenue`/`net_revenue` (fct_orders): `sum(fct_orders.order_gross)` / `sum(fct_orders.order_net_booked)`
- `orders` (fct_orders): `count(distinct fct_orders.order_id)`
- `refunded_orders` (fct_orders, compound): `sum(case when fct_orders.is_refunded then 1 else 0 end)`
- `units_sold` (fct_order_lines): `sum(fct_order_lines.quantity)`
- `aov` (fct_orders): `sum(fct_orders.order_net_booked) / count(distinct fct_orders.order_id)`
- `units_per_order` (fct_order_lines, cross-table): `sum(fct_order_lines.quantity) / count(distinct fct_orders.order_id)`
- `refund_rate` (fct_orders): `sum(case when fct_orders.is_refunded then 1 else 0 end) / count(distinct fct_orders.order_id)`

All three tests use `WithOptions("ANALYTICS", "MAIN", "ecommerce", "")` (matching the cortex integration test), so the view name is `ECOMMERCE`, qualifier `ANALYTICS.MAIN`.

## File Structure

- `layer/snowflake_semantic_view.go` — Task 1.
- `layer/nao_yaml.go` — Task 2.
- `layer/nao_context_rules.go` — Task 3.
- `test/context_layer_test.go` — new: golden + structure tests for all three (package `integration_test`, reuses the file's existing helpers `assertStr`/`assertEqual`).
- `test/models/ecommerce/dbt/snowflake-semantic-view/definition.md`, `.../nao-yaml/semantic.yaml`, `.../nao-context-rules/RULES.md` — new goldens.

---

### Task 1: `snowflake-semantic-view` emitter

Closest sibling of `cortex`. Emits a markdown `definition.md` wrapping a `CREATE SEMANTIC VIEW` DDL.

**Files:** Create `layer/snowflake_semantic_view.go`; Create/append `test/context_layer_test.go`; Create golden `test/models/ecommerce/dbt/snowflake-semantic-view/definition.md`.

**Interfaces:**
- Consumes: `ir.Model`, `renderSQL`, `cortexDegrade`, `upperAll`.
- Produces: layer named `snowflake-semantic-view`, `Configurable`, writes `definition.md`.

- [ ] **Step 1: Write the structure test (`test/context_layer_test.go`)**
```go
package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/layer"
)

func emitTarget(t *testing.T, target, file string) string {
	t.Helper()
	e, err := layer.AsEmitter(target)
	if err != nil {
		t.Fatalf("AsEmitter(%s): %v", target, err)
	}
	if c, ok := e.(layer.Configurable); ok {
		e = c.WithOptions("ANALYTICS", "MAIN", "ecommerce", "")
	}
	p, err := layer.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.Parse(projectDir)
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
		"create or replace semantic view ECOMMERCE",
		"FCT_ORDERS as ANALYTICS.MAIN.FCT_ORDERS primary key (ORDER_ID)",
		"FCT_ORDER_LINES_FCT_ORDERS as FCT_ORDER_LINES(ORDER_ID) references FCT_ORDERS(ORDER_ID)",
		"FCT_ORDERS.AOV as SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)",
		"FCT_ORDER_LINES.UNITS_PER_ORDER as SUM(FCT_ORDER_LINES.QUANTITY) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)",
		"FCT_ORDERS.REFUND_RATE as SUM(CASE WHEN FCT_ORDERS.IS_REFUNDED THEN 1 ELSE 0 END) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("definition.md missing %q", want)
		}
	}
}
```
Run: `go test ./test/ -run TestSemanticViewStructure` → FAIL (`unknown dialect "snowflake-semantic-view"`).

- [ ] **Step 2: Implement the emitter (`layer/snowflake_semantic_view.go`)**
Mirror `cortex.go`'s struct + `WithOptions`. Build a project-wide `resolve` (name→`Def`) as cortex does. Assemble four `\t\t`-indented, comma-joined section bodies. Uppercase table/column/metric identifiers; escape single quotes in comments (`strings.ReplaceAll(s, "'", "''")`).
```go
package layer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/benchouse/semglot/ir"
)

func init() { Register(snowflakeSemanticView{}) }

// snowflakeSemanticView emits a Snowflake CREATE SEMANTIC VIEW DDL wrapped in a
// markdown definition.md. Zero value is usable; the build command sets identity
// from flags. Emit does not mutate m.
type snowflakeSemanticView struct{ Database, Schema, ModelName, Description string }

func (snowflakeSemanticView) Name() string { return "snowflake-semantic-view" }

func (snowflakeSemanticView) WithOptions(database, schema, name, description string) Emitter {
	return snowflakeSemanticView{database, schema, name, description}
}

func (s snowflakeSemanticView) Emit(m *ir.Model, dir string) error {
	view := strings.ToUpper(s.ModelName)
	if view == "" {
		view = "SEMANTIC_VIEW"
	}
	schema := s.Schema
	if schema == "" {
		schema = "MAIN"
	}
	defs := map[string]ir.Expr{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			defs[mt.Name] = mt.Def
		}
	}
	resolve := func(n string) (ir.Expr, bool) { e, ok := defs[n]; return e, ok }
	notes := slices.Clone(m.Notes)

	var tables, rels, dims, metrics []string
	for _, t := range m.Tables {
		u := strings.ToUpper(t.Name)
		line := fmt.Sprintf("%s as %s.%s.%s", u, s.Database, schema, u)
		if len(t.PrimaryKey) > 0 {
			line += fmt.Sprintf(" primary key (%s)", strings.Join(upperAll(t.PrimaryKey), ","))
		}
		if t.Description != "" {
			line += fmt.Sprintf(" comment='%s'", sqlQuote(t.Description))
		}
		tables = append(tables, line)
		for _, d := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
			dims = append(dims, fmt.Sprintf("%s.%s as %s.%s", u, strings.ToUpper(d.Expr), strings.ToLower(t.Name), strings.ToUpper(d.Expr)))
		}
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				notes = append(notes, fmt.Sprintf("metric %q: %s", mt.Name, reason))
				continue
			}
			ml := fmt.Sprintf("%s.%s as %s", u, strings.ToUpper(mt.Name), strings.ToUpper(renderSQL(mt.Def, resolve)))
			if mt.Description != "" {
				ml += fmt.Sprintf(" comment='%s'", sqlQuote(mt.Description))
			}
			metrics = append(metrics, ml)
		}
	}
	for _, r := range m.Relationships {
		if len(r.Columns) == 0 {
			continue
		}
		rels = append(rels, fmt.Sprintf("%s_%s as %s(%s) references %s(%s)",
			strings.ToUpper(r.Left), strings.ToUpper(r.Right),
			strings.ToUpper(r.Left), strings.ToUpper(r.Columns[0].Left),
			strings.ToUpper(r.Right), strings.ToUpper(r.Columns[0].Right)))
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n\n", view)
	b.WriteString("This is a Snowflake **semantic view** — use this to understand the intended way to query and aggregate data.\n\n")
	if s.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", s.Description)
	}
	b.WriteString("## Definition\n\n```sql\n")
	fmt.Fprintf(&b, "create or replace semantic view %s\n", view)
	writeSection(&b, "tables", tables)
	writeSection(&b, "relationships", rels)
	writeSection(&b, "dimensions", dims)
	writeSection(&b, "metrics", metrics)
	if len(notes) > 0 {
		fmt.Fprintf(&b, "\tcomment='%s'", sqlQuote(strings.Join(notes, " ")))
	}
	b.WriteString(";\n```\n")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "definition.md"), b.Bytes(), 0o644)
}

// writeSection writes a comma-separated CREATE SEMANTIC VIEW clause, or nothing
// when empty.
func writeSection(b *bytes.Buffer, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\t%s (\n", name)
	for i, it := range items {
		b.WriteString("\t\t")
		b.WriteString(it)
		if i < len(items)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("\t)\n")
}

// sqlQuote escapes single quotes for a Snowflake string literal.
func sqlQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
```
Run: `go test ./test/ -run TestSemanticViewStructure` → PASS.

- [ ] **Step 3: Generate + eyeball + pin the golden**
```bash
UPDATE_GOLDEN=1 go test ./test/ -run TestSemanticViewGolden
```
Add `TestSemanticViewGolden` to `test/context_layer_test.go` following the exact shape of `TestEcommerceCortexGolden` (emit to temp dir, read `definition.md`, `UPDATE_GOLDEN=1` writes `test/models/ecommerce/dbt/snowflake-semantic-view/definition.md`, else compare byte-for-byte). Read the generated golden and confirm it is well-formed DDL (tables/relationships/dimensions/metrics clauses present, commas correct, comment escaping sane).

- [ ] **Step 4: Full suite + hygiene + commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add layer/snowflake_semantic_view.go test/context_layer_test.go test/models/ecommerce/dbt/snowflake-semantic-view/
git commit -m "feat(layer): snowflake-semantic-view emitter (CREATE SEMANTIC VIEW DDL)"
```

---

### Task 2: `nao-yaml` emitter

Emits nao's `semantic.yaml` (model-global dimensions + metrics). Uses `yaml.Marshal` like cortex.

**Files:** Create `layer/nao_yaml.go`; append to `test/context_layer_test.go`; Create golden `test/models/ecommerce/dbt/nao-yaml/semantic.yaml`.

**Interfaces:**
- Consumes: `ir.Model`, `renderSQL`, `cortexDegrade`.
- Produces: layer named `nao-yaml`, `Configurable`, writes `semantic.yaml`.

- [ ] **Step 1: Write the structure test (append to `test/context_layer_test.go`)**
Unmarshal the emitted YAML into a local subset struct and assert:
```go
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
```
Add `import "gopkg.in/yaml.v3"` to the test file. Run → FAIL (`unknown dialect "nao-yaml"`).

- [ ] **Step 2: Implement the emitter (`layer/nao_yaml.go`)**
Define emit YAML structs with `omitempty`. Dimensions = deduped union of every table's `Dimensions` (→ `categorical`) and `TimeDimensions` (→ `date`). Metric mapping:
- `Def` is `ir.Agg{Func, Table, Arg}` with `Arg` an `ir.Col` → `source: {table, column: Col.Name, aggregation: UPPER(Func)}`, `definition` from `Description`, `grain` from `Grain` (omit if "").
- `Def` is `ir.Agg{…, Arg: ir.Raw}` (compound) OR `ir.Binary` (ratio/derived) → `type: derived`, `source: {table}`, `formula: renderSQL(Def, resolve)`.
- `cortexDegrade(Def)` true (cumulative/conversion) → skip the metric, append reason to a trailing `notes:` (a top-level string field) built from a local `slices.Clone(m.Notes)`.
```go
package layer

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(naoYaml{}) }

type naoYaml struct{ Database, Schema, ModelName, Description string }

func (naoYaml) Name() string { return "nao-yaml" }
func (naoYaml) WithOptions(database, schema, name, description string) Emitter {
	return naoYaml{database, schema, name, description}
}

type naoDoc struct {
	Dimensions []naoDim    `yaml:"dimensions,omitempty"`
	Metrics    []naoMetric `yaml:"metrics,omitempty"`
	Notes      string      `yaml:"notes,omitempty"`
}
type naoDim struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
}
type naoMetric struct {
	Name       string     `yaml:"name"`
	Definition string     `yaml:"definition,omitempty"`
	Type       string     `yaml:"type,omitempty"`
	Source     *naoSource `yaml:"source,omitempty"`
	Formula    string     `yaml:"formula,omitempty"`
	Grain      string     `yaml:"grain,omitempty"`
}
type naoSource struct {
	Table       string `yaml:"table"`
	Column      string `yaml:"column,omitempty"`
	Aggregation string `yaml:"aggregation,omitempty"`
}

func (n naoYaml) Emit(m *ir.Model, dir string) error {
	defs := map[string]ir.Expr{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			defs[mt.Name] = mt.Def
		}
	}
	resolve := func(s string) (ir.Expr, bool) { e, ok := defs[s]; return e, ok }

	var doc naoDoc
	seen := map[string]bool{}
	addDim := func(f ir.Field, typ string) {
		if seen[f.Name] {
			return
		}
		seen[f.Name] = true
		doc.Dimensions = append(doc.Dimensions, naoDim{Name: f.Name, Type: typ, Description: f.Description})
	}
	notes := slices.Clone(m.Notes)
	for _, t := range m.Tables {
		for _, d := range t.Dimensions {
			addDim(d, "categorical")
		}
		for _, d := range t.TimeDimensions {
			addDim(d, "date")
		}
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				notes = append(notes, mt.Name+": "+reason)
				continue
			}
			nm := naoMetric{Name: mt.Name, Definition: mt.Description, Grain: mt.Grain}
			if agg, ok := mt.Def.(ir.Agg); ok {
				if col, ok := agg.Arg.(ir.Col); ok && agg.Filter == nil {
					nm.Source = &naoSource{Table: agg.Table, Column: col.Name, Aggregation: strings.ToUpper(agg.Func)}
					doc.Metrics = append(doc.Metrics, nm)
					continue
				}
			}
			// compound agg, ratio, derived, filtered → a derived formula
			nm.Type = "derived"
			nm.Source = &naoSource{Table: metricTableOf(m, mt.Name)}
			nm.Formula = renderSQL(mt.Def, resolve)
			doc.Metrics = append(doc.Metrics, nm)
		}
	}
	if len(notes) > 0 {
		doc.Notes = strings.Join(notes, "\n")
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	_ = enc.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "semantic.yaml"), buf.Bytes(), 0o644)
}

// metricTableOf returns the table that owns the named metric.
func metricTableOf(m *ir.Model, name string) string {
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			if mt.Name == name {
				return t.Name
			}
		}
	}
	return ""
}
```
Run → PASS.

- [ ] **Step 3: Generate + eyeball + pin the golden**
Add `TestNaoYamlGolden` (same shape as the cortex golden test, file `semantic.yaml`, golden dir `test/models/ecommerce/dbt/nao-yaml`). `UPDATE_GOLDEN=1 go test ./test/ -run TestNaoYamlGolden`. Read the golden: confirm dimensions are deduped, `orders` shows `aggregation: COUNT_DISTINCT`, ratios/compound are `type: derived` with a formula. **Note for reviewer/user:** `COUNT_DISTINCT` is emitted verbatim; if nao's schema rejects it, the follow-up is to map it to `COUNT` (design doc, out-of-scope list).

- [ ] **Step 4: Full suite + hygiene + commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add layer/nao_yaml.go test/context_layer_test.go test/models/ecommerce/dbt/nao-yaml/
git commit -m "feat(layer): nao-yaml emitter (semantic.yaml)"
```

---

### Task 3: `nao-context-rules` emitter

Emits a prose `RULES.md` with generated "Key metrics reference" and "Joins & routing" sections, and a thin best-effort "Table traps".

**Files:** Create `layer/nao_context_rules.go`; append to `test/context_layer_test.go`; Create golden `test/models/ecommerce/dbt/nao-context-rules/RULES.md`.

**Interfaces:**
- Consumes: `ir.Model`, `renderSQL`.
- Produces: layer named `nao-context-rules`, writes `RULES.md`. NOT `Configurable` (needs no db/schema); a plain `struct{}`.

- [ ] **Step 1: Write the structure test (append to `test/context_layer_test.go`)**
```go
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
```
`emitTarget` calls `WithOptions` only when the emitter is `Configurable`; `nao-context-rules` is not, so it is emitted as its zero value — fine. Run → FAIL (`unknown dialect`).

- [ ] **Step 2: Implement the emitter (`layer/nao_context_rules.go`)**
```go
package layer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/benchouse/semglot/ir"
)

func init() { Register(naoContextRules{}) }

type naoContextRules struct{}

func (naoContextRules) Name() string { return "nao-context-rules" }

func (naoContextRules) Emit(m *ir.Model, dir string) error {
	defs := map[string]ir.Expr{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			defs[mt.Name] = mt.Def
		}
	}
	resolve := func(s string) (ir.Expr, bool) { e, ok := defs[s]; return e, ok }

	var b bytes.Buffer
	b.WriteString("# Rules\n\n## Key metrics reference\n\n")
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			name := mt.Label
			if name == "" {
				name = mt.Name
			}
			fmt.Fprintf(&b, "- **%s**: `%s`.", name, renderSQL(mt.Def, resolve))
			if mt.Description != "" {
				fmt.Fprintf(&b, " %s", mt.Description)
			}
			b.WriteByte('\n')
		}
	}
	if len(m.Relationships) > 0 {
		b.WriteString("\n## Joins & routing\n\n")
		for _, r := range m.Relationships {
			for _, c := range r.Columns {
				fmt.Fprintf(&b, "- `%s.%s → %s.%s`\n", r.Left, c.Left, r.Right, c.Right)
			}
		}
	}
	// Table traps: best-effort, only what the model documents.
	var traps []string
	for _, t := range m.Tables {
		if t.Description != "" {
			traps = append(traps, fmt.Sprintf("- **%s**: %s", t.Name, t.Description))
		}
	}
	traps = append(traps, notesToBullets(m.Notes)...)
	if len(traps) > 0 {
		b.WriteString("\n## Table traps\n\n")
		for _, tr := range traps {
			b.WriteString(tr)
			b.WriteByte('\n')
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "RULES.md"), b.Bytes(), 0o644)
}

func notesToBullets(notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, "- "+n)
	}
	return out
}
```
Run → PASS.

- [ ] **Step 3: Generate + eyeball + pin the golden**
Add `TestContextRulesGolden` (file `RULES.md`, golden dir `test/models/ecommerce/dbt/nao-context-rules`). `UPDATE_GOLDEN=1 go test ./test/ -run TestContextRulesGolden`. Read the golden: confirm every fixture metric appears with its formula, join lines are correct, and the traps section carries the table descriptions (thin, as designed).

- [ ] **Step 4: Full suite + hygiene + commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add layer/nao_context_rules.go test/context_layer_test.go test/models/ecommerce/dbt/nao-context-rules/
git commit -m "feat(layer): nao-context-rules emitter (RULES.md prose)"
```

---

## Self-Review

**1. Spec coverage:** snowflake-semantic-view → Task 1; nao-yaml → Task 2; nao-context-rules → Task 3. Each: registered `Emitter`, `renderSQL`-based metric SQL, `cortexDegrade` for provisional kinds, one artifact, byte-identical golden + structure test. `Configurable` on the two that need db/schema; physical-name aliases; `Emit` read-only over `m` (`slices.Clone`). Fidelity gaps (nao `COUNT_DISTINCT`, per-metric dimensions omitted, thin traps) surfaced in the design doc and Task 2/3 notes. ✅

**2. Placeholder scan:** No TBD/TODO. Each golden is generated via `UPDATE_GOLDEN=1` and eyeballed (the exact bytes are produced by the emitter, not hand-written); the structure tests pin the load-bearing content with exact strings derived from the fixture. The CLI needs no change — `AsEmitter` + `Configurable` resolve new layers automatically. ✅

**3. Type consistency:** All three reuse `renderSQL(ir.Expr, resolve)`, `cortexDegrade(ir.Expr)`, `upperAll([]string)`, and the `Emitter`/`Configurable` interfaces exactly as `cortex` declares them. New helpers (`writeSection`, `sqlQuote`, `metricTableOf`, `notesToBullets`) are package-level and single-purpose. `naoDoc`/`naoMetric` mirror the cortex-YAML struct-with-`omitempty` convention. ✅
