# Generalized metric model (expression AST) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat simple/ratio `ir.Metric` (+ rendered `Expr`) with a semantic expression AST (`ir.Expr`); each emitter lowers `Def` to its target and degrades unsupported shapes.

**Architecture:** Expand-contract refactor. Add the AST + `Metric.Def` *alongside* the existing fields (Task 1), migrate Cortex then supersimple to render/lower from `Def` (Tasks 2–3), remove the old fields (Task 4), then add the new metric kinds (Task 5). **Every task ends green with all existing goldens byte-identical** — the goldens are the correctness oracle for the migration.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (only dep). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; exported packages, no `internal/`; no new deps.
- **Byte-identical goldens through Tasks 1–4:** `test/models/ecommerce/dbt/cortex/ecommerce.yaml` and `test/models/ecommerce/dbt/supersimple/*` must not change; new capability (Task 5) is exercised only by NEW fixtures, never by editing an existing golden.
- Identifiers lowercase in IR, UPPERCASED on emit. `renderSQL` produces LOWERCASE SQL (Cortex uppercases it) matching the current `mt.Expr` exactly.
- Reuse `qualifyExpr`, `toPropertySQL`, `mapAgg`, `aggExpr`, `isIdent`, the ss* structs, `ratioMetric`/`crossRatioMetric`, `findParentRelation`.
- `go build ./...`, `go test ./...`, `test -z "$(gofmt -l .)"`, `go vet ./...` clean per task.

## File Structure

- `ir/expr.go` — new: the `Expr` AST (`Col/Raw/Ref/Lit/Agg/Binary/Window/Conversion`).
- `ir/model.go` — `Metric` gains `Def Expr`, `Grain`, `Dimensions` (Task 1); loses `Expr/Kind/Agg/Column/Numerator/Denominator` (Task 4).
- `layer/dbt.go` — build `Metric.Def` (Task 1); stop setting old fields (Task 4); new kinds (Task 5).
- `layer/render_sql.go` — new: `renderSQL(Expr) string` shared lowering (Task 1).
- `layer/cortex.go` — render metric from `Def` (Task 2).
- `layer/supersimple.go` — lower `Def` by pattern-match (Task 3).
- tests + new fixtures.

---

### Task 1: The AST + `renderSQL` + dbt builds `Def` (additive, nothing else changes)

**Files:** Create `ir/expr.go`, `layer/render_sql.go`, `layer/render_sql_test.go`; Modify `ir/model.go`, `layer/dbt.go`, `layer/dbt_test.go`.

**Interfaces:** Produces `ir.Expr` + node types; `ir.Metric.Def/Grain/Dimensions`; `renderSQL(ir.Expr) string`. `Metric.Expr` and the structured fields REMAIN (still populated) so emitters and goldens are untouched.

- [ ] **Step 1: Create `ir/expr.go`**
```go
package ir

// Expr is a node in a metric-definition expression tree.
type Expr interface{ isExpr() }

// Col is a physical column reference. Table may be "" when unqualified.
type Col struct{ Table, Name string }

// Raw is an opaque SQL fragment not decomposed (e.g. a CASE expression).
// Columns lists identifiers it references, for per-target qualification/wrapping.
type Raw struct {
	SQL     string
	Columns []string
}

// Ref references another metric by name.
type Ref struct{ Metric string }

// Lit is an already-rendered literal ("1", "0", "100.0", "'x'").
type Lit struct{ Value string }

// Agg is an aggregation over a sub-expression, optionally filtered. Table is the
// owning table, used to qualify a Raw arg's columns at render time.
type Agg struct {
	Func   string // sum | count | count_distinct | avg | min | max | median
	Table  string // owning table (qualifies a Raw arg's columns)
	Arg    Expr   // usually Col or Raw; nil for count(*)
	Filter Expr   // optional boolean Expr; nil if none
}

// Binary is an arithmetic/comparison/logical operation.
type Binary struct {
	Op          string // "+" "-" "*" "/" "=" "<" ">" "and" "or"
	Left, Right Expr
}

// Window wraps a base expression with a time window (cumulative/to-date).
type Window struct {
	Base   Expr
	Window string // e.g. "30 days"; "" = unbounded to-date
	Grain  string
}

// Conversion is a funnel between a base and conversion measure over a window.
type Conversion struct {
	Base, Conv Expr
	Entity     string
	Window     string
}

func (Col) isExpr()        {}
func (Raw) isExpr()        {}
func (Ref) isExpr()        {}
func (Lit) isExpr()        {}
func (Agg) isExpr()        {}
func (Binary) isExpr()     {}
func (Window) isExpr()     {}
func (Conversion) isExpr() {}
```

- [ ] **Step 2: Add `Def/Grain/Dimensions` to `ir.Metric` (`ir/model.go`), keeping existing fields**
Append to the `Metric` struct (do NOT remove any field yet):
```go
	// Def is the definition as an expression AST (the future single source of
	// truth; the fields above are being migrated away in a later step).
	Def        Expr
	Grain      string   // per-metric agg-time grain
	Dimensions []string // slice-by dimensions
```

- [ ] **Step 3: Create `layer/render_sql.go`**
`renderSQL` must reproduce the exact lowercase SQL the dbt parser currently puts in `mt.Expr`, so Cortex output is byte-identical after Task 2. `resolve` maps a metric name to its `Def` (for `Ref`).
```go
package layer

import (
	"strings"

	"github.com/benchouse/semglot/ir"
)

// renderSQL lowers a metric-definition AST to a neutral, lowercase SQL string
// (Cortex uppercases it). resolve returns the Def of a referenced metric.
func renderSQL(e ir.Expr, resolve func(name string) (ir.Expr, bool)) string {
	switch n := e.(type) {
	case ir.Col:
		if n.Table != "" {
			return n.Table + "." + n.Name
		}
		return n.Name
	case ir.Raw:
		return n.SQL // unqualified; qualified by the enclosing Agg case
	case ir.Lit:
		return n.Value
	case ir.Ref:
		if def, ok := resolve(n.Metric); ok {
			return renderSQL(def, resolve)
		}
		return n.Metric
	case ir.Agg:
		var arg string
		switch a := n.Arg.(type) {
		case ir.Raw: // qualify the raw fragment's columns with the owning table
			arg = qualifyExpr(n.Table, colSet(a.Columns), a.SQL)
		case nil:
			arg = ""
		default:
			arg = renderSQL(n.Arg, resolve)
		}
		if n.Filter != nil {
			arg = "case when " + renderSQL(n.Filter, resolve) + " then " + arg + " end"
		}
		return aggExpr(n.Func, arg)
	case ir.Binary:
		return renderSQL(n.Left, resolve) + " " + n.Op + " " + renderSQL(n.Right, resolve)
	case ir.Window:
		return renderSQL(n.Base, resolve) // best-effort; Cortex window handled later
	case ir.Conversion:
		return "" // no SQL rendering; degraded by callers
	default:
		return ""
	}
}

func colSet(cols []string) map[string]bool {
	m := make(map[string]bool, len(cols))
	for _, c := range cols {
		m[strings.ToLower(c)] = true
	}
	return m
}
```
Note: `aggExpr` already maps `count_distinct`→`count(distinct …)` etc., so
`Agg{"sum", Col{"fct_orders","order_net_booked"}}` → `sum(fct_orders.order_net_booked)`.

- [ ] **Step 4: Build `Def` in the dbt parser (`layer/dbt.go`), alongside the existing fields**
In the simple-metric loop, construct the arg (Col for a bare column, Raw holding the compound expr UNQUALIFIED — the enclosing `Agg.Table` qualifies it at render time), and set `Def`:
```go
		meas := m.TypeParams.Measure
		expr, table := measureAggExpr[meas], measureTable[meas]
		if expr == "" || table == "" {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("measure %q not found in the parsed semantic models", meas)))
			continue
		}
		var arg ir.Expr
		col := measureCol[meas]
		if isIdent(col) {
			arg = ir.Col{Table: table, Name: col}
		} else {
			// Raw stays UNQUALIFIED; the Agg (which carries Table) qualifies it at
			// render time, and supersimple wraps it via toPropertySQL. Columns is
			// the owning table's column list so both know what to qualify/wrap.
			arg = ir.Raw{SQL: col, Columns: colsListByTable[table]}
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Expr: expr,
			Kind: "simple", Agg: measureAgg[meas], Table: table, Column: col,
			Def:   ir.Agg{Func: measureAgg[meas], Table: table, Arg: arg},
			Grain: tableGrain[table],
		})
```
In the ratio loop, set `Def: ir.Binary{Op:"/", Left: ir.Ref{Metric: m.TypeParams.Numerator}, Right: ir.Ref{Metric: m.TypeParams.Denominator}}`.
(Capture two maps during the per-table loop where `cols`/`sm.Defaults.AggTimeDimension` are already computed: `colsListByTable[t.Name]` = the table's column names as a `[]string`, and `tableGrain[t.Name]` = the agg-time-dimension. Declare them next to `measureAgg`/`measureCol`.)

- [ ] **Step 5: Write the failing test `layer/render_sql_test.go`**
```go
package layer

import (
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestRenderSQLMatchesLegacyExpr(t *testing.T) {
	got, err := dbt{}.Parse("testdata/dbt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	byName := map[string]ir.Expr{}
	for _, tb := range got.Tables {
		for _, mt := range tb.Metrics {
			byName[mt.Name] = mt.Def
		}
	}
	resolve := func(n string) (ir.Expr, bool) { e, ok := byName[n]; return e, ok }
	for _, tb := range got.Tables {
		for _, mt := range tb.Metrics {
			if r := renderSQL(mt.Def, resolve); r != mt.Expr {
				t.Fatalf("metric %q: renderSQL(Def)=%q, legacy Expr=%q", mt.Name, r, mt.Expr)
			}
		}
	}
}
```

- [ ] **Step 6: Run**
Run: `go test ./layer/ -run 'RenderSQL|DBTParse' && go test ./...`
Expected: `renderSQL(Def)` equals the legacy `Expr` for every metric (net_revenue, orders, aov, refund_rate, units_sold, refunded_orders, units_per_order, gross_revenue). All goldens unchanged (emitters still use old fields). `gofmt`/`vet` clean.

- [ ] **Step 7: Commit**
```bash
git add ir/expr.go ir/model.go layer/render_sql.go layer/render_sql_test.go layer/dbt.go layer/dbt_test.go
git commit -m "feat(ir): expression AST + Metric.Def; dbt builds it; renderSQL matches legacy Expr"
```

---

### Task 2: Cortex renders from `Def`

**Files:** Modify `layer/cortex.go`.

- [ ] **Step 1: Build a metric resolver and render from `Def`**
Cortex's metric loop currently does `Expr: strings.ToUpper(mt.Expr)`. Before the table loop, build a project-wide resolver:
```go
	metricDefs := map[string]ir.Expr{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			metricDefs[mt.Name] = mt.Def
		}
	}
	resolve := func(n string) (ir.Expr, bool) { e, ok := metricDefs[n]; return e, ok }
```
Change the metric emit to:
```go
			ct.Metrics = append(ct.Metrics, cortexMetric{
				Name: mt.Name, Expr: strings.ToUpper(renderSQL(mt.Def, resolve)),
				Description: mt.Description, Synonyms: mt.Synonyms,
			})
```

- [ ] **Step 2: Verify byte-identical Cortex golden**
Run: `go test ./test/ -run TestEcommerceCortex -v`
Expected: PASS with NO change to `test/models/ecommerce/dbt/cortex/ecommerce.yaml` (`git status` clean for it). If it differs, `renderSQL` doesn't match `Expr` — fix `renderSQL`, not the golden.

- [ ] **Step 3: Full suite + commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add layer/cortex.go && git commit -m "refactor(cortex): render metric SQL from Def (byte-identical)"
```

---

### Task 3: supersimple lowers from `Def` (pattern-match)

**Files:** Modify `layer/supersimple.go`.

**Interfaces:** Replace the Phase-1 `global` registration and Phase-2 ratio resolution (which read `mt.Kind/Agg/Column/Numerator/Denominator`) with pattern-matching on `mt.Def`.

- [ ] **Step 1: Pattern-match `Def` into the existing supersimple shapes**
Add a helper that recognizes a simple aggregation and its `(func, key, isRaw)`:
```go
// asSimpleAgg reports whether def is a single aggregation over a column or a raw
// expression, returning the supersimple aggregation type and the column/expr.
func asSimpleAgg(def ir.Expr) (typ string, arg ir.Expr, ok bool) {
	a, ok := def.(ir.Agg)
	if !ok || a.Filter != nil {
		return "", nil, false
	}
	return mapAgg(a.Func), a.Arg, true
}
```
In **Phase 1** registration, replace the `mt.Kind != "simple"` gate + key derivation with: for each metric whose `Def` `asSimpleAgg` succeeds and whose `arg` is a `Col` or `Raw`, register `global[mt.Name]`. For a `Col{_, name}` → `key = UPPER(name)`; for a `Raw` → synthesize the `property.sql` (via the clobber-guarded loop) using `toPropertySQL(raw.SQL, colSet(raw.Columns))` and `key = UPPER(mt.Name)`. `raw.SQL` is already unqualified (Task 1 stores it that way).
In **Phase 2**, replace `mt.Kind == "ratio"` with: `if bin, ok := mt.Def.(ir.Binary); ok && bin.Op == "/"`, resolve `bin.Left`/`bin.Right` — each must be a `Ref` whose target is in `global` (or an inline `Agg`); then reuse the existing same-table `ratioMetric` / cross-table `crossRatioMetric` logic exactly as today. Any `Def` that matches neither a simple agg nor a `Binary{"/"}` of resolvable operands → the existing `NOTES.md` degradation.

- [ ] **Step 2: supersimple compound-measure `property.sql` from the Raw arg**
For a metric whose `Def` is `Agg{Arg: ir.Raw}`, synthesize the property with
`toPropertySQL(raw.SQL, colSet(raw.Columns))` — `raw.SQL` is already the unqualified
form and `raw.Columns` is the table's column list (built that way in Task 1), so this
reproduces today's `sql: case when {is_refunded} then 1 else 0 end` exactly.

- [ ] **Step 3: Verify byte-identical supersimple goldens + full suite**
Run: `go test ./... -run Supersimple && go test ./...`
Expected: `test/models/ecommerce/dbt/supersimple/*` unchanged (`git status` clean); all green. Fix lowering to match, never the golden.

- [ ] **Step 4: Commit**
```bash
test -z "$(gofmt -l .)" && go vet ./...
git add layer/supersimple.go layer/dbt.go ir/expr.go layer/render_sql.go layer/render_sql_test.go && git commit -m "refactor(supersimple): lower metrics from Def by pattern-match (byte-identical)"
```

---

### Task 4: Contract — remove the legacy metric fields

**Files:** Modify `ir/model.go`, `layer/dbt.go`, tests referencing the old fields.

- [ ] **Step 1: Remove `Expr/Kind/Agg/Table/Column/Numerator/Denominator` from `ir.Metric`**
Leave `Name/Label/Description/Synonyms/Def/Grain/Dimensions`.

- [ ] **Step 2: Stop populating them in `layer/dbt.go`**
Delete the removed-field assignments (keep `Def`). Delete now-unused locals (`measureAggExpr` may still feed `metricExpr` used for ratio-resolution ordering — keep only what `Def` building needs; the `metricExpr`/`metricTable` maps stay for resolving which table a ratio attaches to).

- [ ] **Step 3: Update tests that assert old fields**
`TestDBTParse`, `TestDBTParseMerge`, `TestDBTParseLabelAndGrain`, `TestDBTParseCaseExpr`, `TestDBTParseUnresolvedMetrics` — replace `Metric{Kind/Agg/…}` expectations with `Metric{Def: …}` deep-equal (the AST the parser now builds). Where a test checked `mt.Expr`, assert `renderSQL(mt.Def, resolve)` instead.

- [ ] **Step 4: Full suite + goldens unchanged + commit**
Run: `go build ./... && go test ./... && test -z "$(gofmt -l .)" && go vet ./...`
Expected: all green; Cortex + supersimple goldens still byte-identical (nothing renders differently — the old fields were already unused after Tasks 2–3).
```bash
git add -A && git commit -m "refactor(ir): remove legacy metric fields; Def is the single source of truth"
```

---

### Task 5: New capability — derived, filter, cumulative, conversion

**Files:** Modify `layer/dbt.go`, `layer/cortex.go`, `layer/supersimple.go`; new fixtures + tests under `layer/testdata/`.

- [ ] **Step 1: dbt parser builds the new nodes**
- `type: derived` → parse `type_params.expr` (an arithmetic formula over metric names + literals) into a `Binary`/`Ref`/`Lit` tree with a small lexer-based formula parser (`sqlTokens` for tokens; recursive-descent over `+ - * /` with precedence and parens); unknown/complex → `Raw`.
- `type: cumulative` → `Window{Base: Ref{measure/metric}, Window: type_params.window, Grain: type_params.grain}`.
- `type: conversion` → `Conversion{Base, Conv, Entity, Window}` from its `type_params`.
- metric `filter:` (dbt) → set `Filter` on the built `Agg`.

- [ ] **Step 2: Cortex lowering for the new nodes**
`renderSQL` already handles `Binary`/`Agg.Filter`. Add: `Window` → a Cortex windowed SQL if expressible, else return "" and record a `NOTES.md`-style note (Cortex has no custom_instructions for this per metric — degrade by omitting + a model-level note). `Conversion` → omit + note (no Cortex primitive).

- [ ] **Step 3: supersimple degradation**
`Def` that is a `Binary` non-`/` (derived), `Window`, `Conversion`, or an `Agg` with `Filter` supersimple can't express as a `filter` op → `NOTES.md` with a specific reason. A `filter`ed simple agg over one table MAY map to a supersimple `filter` operation + aggregation — implement if straightforward, else degrade.

- [ ] **Step 4: New fixtures + tests (do NOT touch existing goldens)**
Add `layer/testdata/dbt_derived/` (a `derived` metric `a - b`) and `layer/testdata/dbt_filtered/` (a filtered measure). Assert: dbt builds the expected `Def`; Cortex renders `A - B` / `SUM(CASE WHEN … )`; supersimple emits or notes appropriately. cumulative/conversion: a parser unit test that the node is built + a Cortex/supersimple degradation test — **marked provisional** (no live validation).

- [ ] **Step 5: Full suite, hygiene, commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add -A && git commit -m "feat(metrics): derived/filter/cumulative/conversion via the AST (new kinds provisional)"
```

---

## Self-Review

**1. Spec coverage:** AST node set → Task 1. `Metric{Def,Grain,Dimensions}` → Task 1/4. `renderSQL` lowering → Task 1–2. supersimple pattern-match + degrade → Task 3/5. Drop `Expr` dual-source → Task 4. Byte-identical regression → Tasks 1–4 gates. New kinds (derived/filter/cumulative/conversion) → Task 5. ✅

**2. Placeholder scan:** No TBD/TODO. The unqualified-`Raw` design (store `Raw.SQL` unqualified + `Agg.Table` to qualify at render time) is fully specified in Task 1 — `ir.Agg` carries `Table`, `renderSQL`'s Agg case qualifies a `Raw` arg via `qualifyExpr(n.Table, colSet(a.Columns), a.SQL)`, and supersimple (Task 3) wraps the same unqualified `Raw.SQL` via `toPropertySQL`. Cortex and supersimple derive their two forms from one stored representation; no dual source. ✅

**3. Type consistency:** `ir.Expr` + node types (`Col`/`Raw`/`Ref`/`Lit`/`Agg{Func,Table,Arg,Filter}`/`Binary`/`Window`/`Conversion`), `renderSQL(Expr, resolve)`, `colSet`, `asSimpleAgg`, `Metric.Def`, the `metricDefs`/`global` resolvers — consistent across tasks. `aggExpr`/`qualifyExpr`/`toPropertySQL` reused. ✅
