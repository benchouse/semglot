# Generalized metric model (expression AST) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the flat simple/ratio `ir.Metric` (+ rendered `Expr`) with a semantic expression AST (`ir.Expr`) as the single source of truth; each emitter lowers `Def` to its target and degrades unsupported shapes. Add a dbt *emitter* and a dbt→dbt round-trip proving the AST is lossless.

**Architecture:** **Clean rewrite, no expand-contract.** In one atomic cutover (Task 1) the flat metric fields are deleted, the dbt parser builds `Def`, and both emitters lower from `Def` — there is no transitional period where old and new representations coexist. The Cortex + supersimple goldens are the correctness oracle: byte-identical before and after the cutover. Task 2 makes the dbt layer bidirectional (a dbt Emitter) and adds a dbt→dbt round-trip that re-parses emitted dbt and asserts the IR is unchanged — the losslessness proof for the AST. Task 3 adds the new metric kinds.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (only dep). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; exported packages, no `internal/`; no new deps.
- **Byte-identical goldens across the cutover:** `test/models/ecommerce/dbt/cortex/ecommerce.yaml` and `test/models/ecommerce/dbt/supersimple/*` MUST NOT change (Tasks 1–2). New capability (Task 3) is exercised only by NEW fixtures, never by editing an existing golden.
- **No legacy retained.** After Task 1, `ir.Metric` has exactly `{Name, Label, Description, Synonyms, Grain, Dimensions, Def}`. The fields `Expr/Kind/Agg/Table/Column/Numerator/Denominator` are gone, and nothing populates or reads them. No `renderSQL`-vs-legacy compatibility test survives.
- Identifiers lowercase in IR, UPPERCASED on emit. `renderSQL` produces LOWERCASE SQL (Cortex uppercases it) reproducing today's metric SQL exactly (so Cortex golden is byte-identical).
- Reuse `qualifyExpr`, `toPropertySQL`, `mapAgg`, `aggExpr`, `isIdent`, the ss* structs, `ratioMetric`/`crossRatioMetric`, `findParentRelation`.
- `go build ./...`, `go test ./...`, `test -z "$(gofmt -l .)"`, `go vet ./...` clean per task.

## File Structure

- `ir/expr.go` — **new**: the `Expr` AST (`Col/Raw/Ref/Lit/Agg/Binary/Window/Conversion`).
- `ir/model.go` — `Metric` reshaped to `{Name, Label, Description, Synonyms, Grain, Dimensions, Def}` (Task 1).
- `layer/render_sql.go` — **new**: `renderSQL(ir.Expr, resolve) string` + `colSet` shared lowering (Task 1).
- `layer/dbt.go` — parser builds `Def` (Task 1); new kinds (Task 3).
- `layer/dbt_emit.go` — **new**: dbt Emitter (`func (dbt) Emit`) (Task 2); new kinds (Task 3).
- `layer/cortex.go` — render metric from `Def` (Task 1).
- `layer/supersimple.go` — lower `Def` by pattern-match (Task 1).
- `layer/dbt_test.go` — assertions move from flat fields to `Def` (Task 1).
- `test/integration_test.go` — dbt→dbt round-trip + emitted golden (Task 2).
- `layer/testdata/dbt_derived/`, `layer/testdata/dbt_filtered/` — new-kind fixtures (Task 3).

---

### Task 1: Cutover — replace the flat metric with the expression AST

**Atomic.** Removing the flat fields breaks every consumer at once, so the AST, the parser, and both emitters all migrate in this single task. The build compiles and all goldens pass byte-identical only at the end of the task — that is the task's one review gate.

**Files:** Create `ir/expr.go`, `layer/render_sql.go`; Modify `ir/model.go`, `layer/dbt.go`, `layer/cortex.go`, `layer/supersimple.go`, `layer/dbt_test.go`.

**Interfaces:**
- Produces: `ir.Expr` + node types; `ir.Metric{Name,Label,Description,Synonyms,Grain,Dimensions,Def}`; `renderSQL(ir.Expr, func(string)(ir.Expr,bool)) string`; `colSet([]string) map[string]bool`.
- The dbt parser produces, per metric, a `Def`: simple → `ir.Agg`, ratio → `ir.Binary{Op:"/"}`. Cortex reads `Def` via `renderSQL`; supersimple reads `Def` via pattern-match.

- [ ] **Step 1: Create `ir/expr.go`**
```go
package ir

// Expr is a node in a metric-definition expression tree.
type Expr interface{ isExpr() }

// Col is a physical column reference. Table may be "" when unqualified.
type Col struct{ Table, Name string }

// Raw is an opaque SQL fragment not decomposed (e.g. a CASE expression).
// Columns lists the table's column names (lowercased, sorted) so a renderer can
// qualify (Cortex) or wrap (supersimple) the identifiers it references.
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

- [ ] **Step 2: Reshape `ir.Metric` (`ir/model.go`) — remove the flat fields**
Replace the whole `Metric` struct (delete `Expr/Kind/Agg/Table/Column/Numerator/Denominator`):
```go
// Metric is a named business calculation. Def is its definition as an expression
// AST — the single source of truth; each emitter lowers Def to its target form.
type Metric struct {
	Name        string
	Label       string   // dbt metric label (display name); "" if none
	Description string
	Synonyms    []string
	Grain       string   // per-metric agg-time grain (owning model's agg_time_dimension); "" if none
	Dimensions  []string // slice-by dimensions; nil if unspecified
	Def         Expr     // the definition AST
}
```

- [ ] **Step 3: Create `layer/render_sql.go`**
`renderSQL` reproduces the exact lowercase SQL the parser used to put in `mt.Expr`, so Cortex stays byte-identical. `resolve` maps a metric name to its `Def` (for `Ref` inlining).
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
		return n.SQL // unqualified; the enclosing Agg case qualifies it
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
		return renderSQL(n.Base, resolve) // best-effort; Cortex window handled in Task 3
	case ir.Conversion:
		return "" // no SQL rendering; degraded by callers
	default:
		return ""
	}
}

// colSet lowercases a column list into a set for qualifyExpr/toPropertySQL.
func colSet(cols []string) map[string]bool {
	m := make(map[string]bool, len(cols))
	for _, c := range cols {
		m[strings.ToLower(c)] = true
	}
	return m
}
```
Note: `aggExpr` already maps `count_distinct`→`count(distinct …)`, so `Agg{"sum", "fct_orders", Col{"fct_orders","order_net_booked"}, nil}` → `sum(fct_orders.order_net_booked)`.

- [ ] **Step 4: Parser builds `Def` (`layer/dbt.go`) — replace the flat-field metric loops**
Two changes.

(a) In the per-table loop (the `for _, name := range order` block), capture the grain and the table's column list next to where `cols`/`t.Grain` are computed. Declare these maps next to `measureTable` (~line 173):
```go
	grainByTable := map[string]string{}
	colsListByTable := map[string][]string{}
```
and just before `out.Tables = append(out.Tables, t)`:
```go
		grainByTable[name] = t.Grain
		colList := make([]string, 0, len(cols))
		for c := range cols {
			colList = append(colList, c)
		}
		sort.Strings(colList) // deterministic Raw.Columns
		colsListByTable[name] = colList
```
Also **delete** the now-unused `measureAggExpr` map (its declaration ~line 174 and its assignment `measureAggExpr[m.Name] = aggExpr(...)` in the measures loop) — `Def` needs the raw agg/column, not a pre-rendered string.

(b) Replace the three metric loops (the `metricExpr`/`metricTable`/`attach` block through the unsupported-type loop) with:
```go
	metricDefs := map[string]ir.Expr{}
	metricTable := map[string]string{}
	attach := func(table string, mt ir.Metric) {
		metricDefs[mt.Name] = mt.Def
		metricTable[mt.Name] = table
		i := tableIdx[table]
		out.Tables[i].Metrics = append(out.Tables[i].Metrics, mt)
	}
	for _, m := range metrics { // simple first, so ratios can reference them
		if m.Type != "simple" {
			continue
		}
		meas := m.TypeParams.Measure
		table := measureTable[meas]
		if table == "" {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("measure %q not found in the parsed semantic models", meas)))
			continue
		}
		col := measureCol[meas]
		var arg ir.Expr
		if isIdent(col) {
			arg = ir.Col{Table: table, Name: col}
		} else {
			// Raw stays UNQUALIFIED; the Agg (carrying Table) qualifies it at render
			// time, and supersimple wraps it via toPropertySQL. Columns is the table's
			// column list so both know what to qualify/wrap.
			arg = ir.Raw{SQL: col, Columns: colsListByTable[table]}
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description,
			Grain: grainByTable[table],
			Def:   ir.Agg{Func: measureAgg[meas], Table: table, Arg: arg},
		})
	}
	for _, m := range metrics { // ratio
		if m.Type != "ratio" {
			continue
		}
		_, okN := metricDefs[m.TypeParams.Numerator]
		_, okD := metricDefs[m.TypeParams.Denominator]
		table, okT := metricTable[m.TypeParams.Numerator]
		if !okN || !okD || !okT {
			out.Notes = append(out.Notes, metricNote(m, "one or more ratio operands could not be resolved to a metric"))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description,
			Grain: grainByTable[table],
			Def:   ir.Binary{Op: "/", Left: ir.Ref{Metric: m.TypeParams.Numerator}, Right: ir.Ref{Metric: m.TypeParams.Denominator}},
		})
	}
	for _, m := range metrics { // unsupported types -> notes (unchanged)
		switch m.Type {
		case "simple", "ratio":
		default:
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("unsupported metric type %q", m.Type)))
		}
	}
```

- [ ] **Step 5: Cortex renders from `Def` (`layer/cortex.go`)**
Before the `for _, t := range m.Tables` loop, build a project-wide resolver:
```go
	metricDefs := map[string]ir.Expr{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			metricDefs[mt.Name] = mt.Def
		}
	}
	resolve := func(n string) (ir.Expr, bool) { e, ok := metricDefs[n]; return e, ok }
```
Change the metric emit (the `for _, mt := range t.Metrics` body) to:
```go
			ct.Metrics = append(ct.Metrics, cortexMetric{
				Name: mt.Name, Expr: strings.ToUpper(renderSQL(mt.Def, resolve)),
				Description: mt.Description, Synonyms: mt.Synonyms,
			})
```
(Add the `ir` import if not already present.)

- [ ] **Step 6: supersimple lowers from `Def` (`layer/supersimple.go`)**
Add a matcher:
```go
// asSimpleAgg reports whether def is a single unfiltered aggregation, returning
// the supersimple aggregation type and the aggregated arg (a Col or Raw).
func asSimpleAgg(def ir.Expr) (typ string, arg ir.Expr, ok bool) {
	a, ok := def.(ir.Agg)
	if !ok || a.Filter != nil {
		return "", nil, false
	}
	return mapAgg(a.Func), a.Arg, true
}
```
- **Phase 1 (registry):** replace the `mt.Kind != "simple"` gate + key derivation with: for each metric where `asSimpleAgg(mt.Def)` succeeds and `arg` is a `Col` or `Raw`, register `global[mt.Name]`. `Col{_, name}` → `key = UPPER(name)`. `Raw` → synthesize the `property.sql` via the clobber-guarded loop using `toPropertySQL(raw.SQL, colSet(raw.Columns))` (raw.SQL is unqualified) and `key = UPPER(mt.Name)`.
- **Phase 2 (ratios):** replace `mt.Kind == "ratio"` with `if bin, ok := mt.Def.(ir.Binary); ok && bin.Op == "/"`. Resolve `bin.Left`/`bin.Right` — each must be a `Ref` whose target is in `global`; then reuse the existing same-table `ratioMetric` / cross-table `crossRatioMetric` logic exactly as today.
- Any `Def` matching neither shape → the existing `NOTES.md` degradation.

- [ ] **Step 7: Move `layer/dbt_test.go` assertions from flat fields to `Def`**
Rewrite the metric expectations. In `TestDBTParse` (~line 36–38), the `Metrics` slice becomes:
```go
				{Name: "net_revenue", Label: "Net revenue", Description: "Net booked revenue.", Grain: "order_date",
					Def: ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_net_booked"}}},
				{Name: "orders", Label: "Orders", Grain: "order_date",
					Def: ir.Agg{Func: "count_distinct", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "order_id"}}},
				{Name: "aov", Label: "Average order value", Description: "Net revenue / orders.", Grain: "order_date",
					Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "orders"}}},
```
(Use the actual `Grain` the fixture produces — read `semantic_models.yml`'s `defaults.agg_time_dimension` for `fct_orders`; if it is empty, drop the `Grain` field. Verify by running the test.)
In `TestDBTParseCaseExpr` (~line 149) — which built a `name → mt.Expr` map — build a resolver and assert `renderSQL(mt.Def, resolve)` equals the same expected SQL strings:
```go
	defs := map[string]ir.Expr{}
	for _, mt := range got.Tables[0].Metrics {
		defs[mt.Name] = mt.Def
	}
	resolve := func(n string) (ir.Expr, bool) { e, ok := defs[n]; return e, ok }
	metrics := map[string]string{}
	for _, mt := range got.Tables[0].Metrics {
		metrics[mt.Name] = renderSQL(mt.Def, resolve)
	}
```
In `TestDBTParseMerge`, `TestDBTParseLabelAndGrain`, `TestDBTParseUnresolvedMetrics`: replace any `Kind/Agg/Column/Numerator/Denominator/Expr` reference. `Label`/`Grain` now live on the reshaped struct (assert directly); a former `mt.Expr` check becomes `renderSQL(mt.Def, resolve)`.

- [ ] **Step 8: Build, test, verify goldens byte-identical**
Run: `go build ./... && go test ./... && test -z "$(gofmt -l .)" && go vet ./...`
Expected: all green. `git status` shows NO change to `test/models/ecommerce/dbt/cortex/ecommerce.yaml` or `test/models/ecommerce/dbt/supersimple/*`. If a golden differs, fix the lowering (`renderSQL`/supersimple), never the golden.

- [ ] **Step 9: Commit**
```bash
git add ir/ layer/render_sql.go layer/dbt.go layer/cortex.go layer/supersimple.go layer/dbt_test.go
git commit -m "feat(ir): expression-AST metric model; dbt/cortex/supersimple cut over to Def"
```

---

### Task 2: dbt emitter + dbt→dbt round-trip (losslessness proof)

Make the `dbt` layer bidirectional by adding `Emit`, then prove the AST/IR captures the source losslessly: parse → emit dbt → re-parse → the IR is unchanged (compared canonicalized — see below). Also pins an emitted-dbt golden so the generated docs are reviewable and regression-locked.

**Fidelity boundary (read first):** the IR intentionally discards dbt's *structural* distinctions (an entity vs a dimension vs a plain model column; the measure↔metric name link). So the round-trip guarantees **no content is lost** — every table, field, measure, metric, and relationship survives with identical `Name/Expr/Description/DataType/Def` — NOT that the emitted YAML matches the input's formatting, key order, or section placement. The comparison therefore canonicalizes (sorts) both IRs before `DeepEqual`. Unresolved-metric passthrough `Notes` are out of scope (they carry no structured metric to re-emit); the ecommerce fixture has none.

**Files:** Create `layer/dbt_emit.go`; Modify `test/integration_test.go`; Create golden `test/models/ecommerce/dbt/dbt/ecommerce.yml`.

**Interfaces:**
- Consumes: `ir.Model`, the AST, `renderSQL` (not needed — emit maps `Def` structurally), `aggExpr`.
- Produces: `dbt` now satisfies `layer.Emitter` (`Emit(*ir.Model, dir string) error`), writing `<dir>/ecommerce.yml`.

- [ ] **Step 1: Write the failing round-trip test (`test/integration_test.go`)**
```go
func canonicalizeModel(m *layer_ir.Model) {
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
		return a.Left+">"+a.Right < b.Left+">"+b.Right
	})
	sort.Strings(m.Notes)
}

func TestDBTRoundTrip(t *testing.T) {
	p, err := layer.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	m1, err := p.Parse(projectDir)
	if err != nil {
		t.Fatalf("parse source: %v", err)
	}
	e, err := layer.AsEmitter("dbt")
	if err != nil {
		t.Fatalf("dbt is not an Emitter: %v", err)
	}
	out := t.TempDir()
	if err := e.Emit(m1, out); err != nil {
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
```
Add imports `reflect`, `sort`, and import the `ir` package (alias it `layer_ir "github.com/benchouse/semglot/ir"` or just `ir` — match the file's style; the file currently imports only `layer`, so add the `ir` import and a small `sortFields` helper sorting `[]ir.Field` by `Name`). Run: `go test ./test/ -run TestDBTRoundTrip` → FAIL (`dbt is not an Emitter`).

- [ ] **Step 2: Implement `Emit` (`layer/dbt_emit.go`)**
Define emit-only YAML structs (do NOT reuse the parse structs — `dbtTest` has a custom unmarshaler and no marshaler) mirroring the parse shapes: `dbtEmitFile{Models,SemanticModels,Metrics}`, `dbtEmitModel{Name,Description,Columns}`, `dbtEmitColumn{Name,Description,DataType,DataTests}`, `dbtEmitRelTest{Relationships{To,Field}}`, `dbtEmitSemantic{Name,Model,Defaults{AggTimeDimension},Entities,Dimensions,Measures}`, `dbtEmitMetric{Name,Label,Type,Description,TypeParams{Measure,Numerator,Denominator}}` — all with `yaml:"...,omitempty"` tags matching the parser's field names exactly (so re-parse reads them).

Mapping rules (`Emit(m *ir.Model, dir string)`):
1. **models:** one `dbtEmitModel` per table. Emit a column for every field that carries a `Description` or `DataType` (dimensions, time dimensions, measures' backing columns, PK columns) so `colDesc`/`colType` reconstruct on re-parse. Attach PK via a column-level `primary_key` constraint (or model-level `constraints`) matching `pkFromModel`. For each relationship whose `Left` is this table, add a `data_tests: [{relationships: {to: "ref('<right>')", field: <rightCol>}}]` on the `<leftCol>` column — this alone reproduces `ir.Relationship` on re-parse (column names are all the IR holds; do NOT emit foreign *entities*).
2. **semantic_models:** one per table with `model: ref('<name>')`, `defaults.agg_time_dimension: <Table.Grain>`. Emit a **primary** entity per PK column (`name/expr = col`). Emit each non-PK `Dimension` as a semantic dimension (`type: categorical`, `expr` omitted when `== name`); each `TimeDimension` as `type: time`. Emit each `Measure` (`name`, `agg`, `expr`).
   - Skip re-emitting FK columns as entities — they round-trip as plain model columns (step 1) plus the relationship test.
3. **metrics:** for each `Table.Metric`:
   - `Def` is `ir.Agg{Func, Arg}` → find the measure on this table whose `Agg == Func` and whose expr matches `Arg` (`Col.Name` or `Raw.SQL`); emit `type: simple, type_params.measure: <that measure name>`. (Guaranteed to exist for dbt-sourced models — every simple metric came from a measure that is retained in `Table.Measures`.)
   - `Def` is `ir.Binary{Op:"/", Left: ir.Ref{a}, Right: ir.Ref{b}}` → emit `type: ratio, type_params: {numerator: a, denominator: b}`.
   - other `Def` shapes → skip in Task 2 (added in Task 3); the round-trip fixture has only simple/ratio.
   Reverse-match helper:
```go
func measureFor(t ir.Table, a ir.Agg) (string, bool) {
	want := ""
	switch arg := a.Arg.(type) {
	case ir.Col:
		want = arg.Name
	case ir.Raw:
		want = arg.SQL
	}
	for _, ms := range t.Measures {
		if strings.EqualFold(ms.Agg, a.Func) && ms.Expr == want {
			return ms.Name, true
		}
	}
	return "", false
}
```
Marshal the `dbtEmitFile` with `yaml.NewEncoder` + `SetIndent(2)` (match `cortex.Emit`), `MkdirAll(dir)`, write `filepath.Join(dir, "ecommerce.yml")`. Run: `go test ./test/ -run TestDBTRoundTrip` → PASS.

- [ ] **Step 3: Pin the emitted-dbt golden**
Add a golden test in `test/integration_test.go` that emits to a temp dir, reads `ecommerce.yml`, and compares to `test/models/ecommerce/dbt/dbt/ecommerce.yml` with the same `UPDATE_GOLDEN=1` create path as `TestEcommerceCortexGolden`. Create it:
```bash
UPDATE_GOLDEN=1 go test ./test/ -run TestDBTEmitGolden
```
Then eyeball `test/models/ecommerce/dbt/dbt/ecommerce.yml` — it is the generated dbt for the user to review. (Note: this subdir is NOT matched by `Parse`'s top-level `*.yml` glob, so it cannot pollute the source project.)

- [ ] **Step 4: Full suite + hygiene + commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add layer/dbt_emit.go test/integration_test.go test/models/ecommerce/dbt/dbt/ecommerce.yml
git commit -m "feat(dbt): emitter + dbt->dbt round-trip (IR-lossless)"
```

---

### Task 3: New capability — derived, filter, cumulative, conversion

**Files:** Modify `layer/dbt.go`, `layer/dbt_emit.go`, `layer/cortex.go`, `layer/supersimple.go`; new fixtures + tests under `layer/testdata/`.

- [ ] **Step 1: Parser builds the new nodes (`layer/dbt.go`)**
- `type: derived` → parse `type_params.expr` (arithmetic over metric names + literals) into a `Binary`/`Ref`/`Lit` tree via a small recursive-descent parser over `sqlTokens` (`+ - * /`, precedence, parens); unknown/complex → `Raw`.
- `type: cumulative` → `Window{Base: Ref{measure/metric}, Window: type_params.window, Grain: type_params.grain}`.
- `type: conversion` → `Conversion{Base, Conv, Entity, Window}` from its `type_params`.
- metric `filter:` (dbt) → set `Filter` on the built `Agg`.
Extend the raw `dbtMetric.TypeParams` struct with the new fields (`expr`, `window`, `grain`, `metrics`, conversion params) and add `Filter string` to `dbtMetric`.

- [ ] **Step 2: Cortex lowering (`layer/cortex.go` / `renderSQL`)**
`renderSQL` already handles `Binary` and `Agg.Filter`. Add: `Window` → Cortex windowed SQL if expressible, else omit the metric + append a model-level `NOTES.md`-style note. `Conversion` → omit + note (no Cortex primitive).

- [ ] **Step 3: supersimple degradation (`layer/supersimple.go`)**
`Binary` non-`/` (derived), `Window`, `Conversion`, or an `Agg` with `Filter` supersimple can't express → `NOTES.md` with a specific reason. A filtered simple agg over one table MAY map to a supersimple `filter` op + aggregation — implement if straightforward, else degrade.

- [ ] **Step 4: dbt emitter for the new kinds (`layer/dbt_emit.go`)**
Extend `Emit`'s metric mapping: `derived` → `type: derived` with `type_params.expr` (re-render the `Binary`/`Ref`/`Lit` tree) + `metrics:` list; `cumulative` → `type: cumulative`; `conversion` → `type: conversion`; `Agg.Filter` → the metric `filter:`. Extend the round-trip fixture coverage where a source fixture exists (derived/filtered).

- [ ] **Step 5: New fixtures + tests (do NOT touch existing goldens)**
Add `layer/testdata/dbt_derived/` (a `derived` metric `a - b`) and `layer/testdata/dbt_filtered/` (a filtered measure). Assert: dbt builds the expected `Def`; Cortex renders `A - B` / `SUM(CASE WHEN … )`; supersimple emits-or-notes; **dbt→dbt round-trips** for these two. cumulative/conversion: a parser unit test that the node is built + a Cortex/supersimple degradation test — **marked provisional** (no source fixture round-trip, no live validation).

- [ ] **Step 6: Full suite, hygiene, commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add -A && git commit -m "feat(metrics): derived/filter/cumulative/conversion via the AST (new kinds provisional)"
```

---

## Self-Review

**1. Spec coverage:** AST node set → Task 1 Step 1. `Metric{Def,Grain,Dimensions}` (flat fields removed) → Task 1 Steps 2/4. `renderSQL` lowering → Task 1 Steps 3/5. supersimple pattern-match + degrade → Task 1 Step 6 / Task 3 Step 3. Single source of truth (no `Expr` dual-source, no legacy retained) → Task 1 (Global Constraints). Byte-identical regression → Task 1 Step 8. dbt→dbt round-trip (losslessness) → Task 2. New kinds → Task 3. ✅

**2. Placeholder scan:** No TBD/TODO. The unqualified-`Raw` design (store `Raw.SQL` unqualified + `Agg.Table` to qualify at render time) is fully specified: `ir.Agg` carries `Table`, `renderSQL`'s Agg case qualifies a `Raw` arg via `qualifyExpr(n.Table, colSet(a.Columns), a.SQL)`, supersimple wraps the same unqualified `Raw.SQL` via `toPropertySQL`, and the dbt emitter reverse-matches on the same `Raw.SQL`. One stored representation, three derivations. ✅ The two places the implementer must confirm against the running fixture (not guess) are called out explicitly: the `Grain` value in `TestDBTParse` (Task 1 Step 7) and the emitted-dbt golden content (Task 2 Step 3). ✅

**3. Type consistency:** `ir.Expr` + node types (`Col`/`Raw`/`Ref`/`Lit`/`Agg{Func,Table,Arg,Filter}`/`Binary`/`Window`/`Conversion`), `renderSQL(Expr, resolve)`, `colSet`, `asSimpleAgg`, `measureFor`, `Metric.Def`, the `metricDefs`/`global` resolvers — consistent across tasks. `aggExpr`/`qualifyExpr`/`toPropertySQL`/`mapAgg`/`ratioMetric`/`crossRatioMetric` reused, not redefined. ✅
