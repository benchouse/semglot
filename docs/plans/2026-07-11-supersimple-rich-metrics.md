# supersimple rich metrics (compound + same-table ratios) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recover supersimple metrics the simple `{aggregation}` shape can't hold — compound-measure metrics via `property.sql`, and same-table ratios via a metric `operations` pipeline — with a `NOTES.md` sidecar for what's still deferred.

**Architecture:** All changes in `layer/supersimple.go`. A per-table pass resolves every simple metric to its supersimple `(type, key)` (synthesizing a computed `property.sql` for compound measures), then emits simple metrics directly and same-table ratios as a `groupAggregate → deriveField → first` pipeline. Cross-table ratios and unsupported kinds go to `NOTES.md`. No IR change.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3`, `github.com/DataDog/go-sqllexer` (already a dep). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; exported packages, no `internal/`; no new deps.
- Cross-dialect identifiers lowercase in IR, UPPERCASED on emit.
- **Cortex output unchanged** (`test/models/ecommerce/dbt/cortex/ecommerce.yaml` byte-identical).
- Reuse existing package-`layer` helpers (`upperAll`, `isIdent`, `mapAgg`, `ssType`, `prettify`, `slug`); do not duplicate.
- The ratio `operations` pipeline is a **provisional hypothesis** pending live-supersimple push validation — emit it, but the ecommerce ratio golden is expected to change after validation.
- `go build ./...`, `go test ./...`, `gofmt -l .` (empty), `go vet ./...` clean per task.

## File Structure

- `layer/supersimple.go` — struct additions, `toPropertySQL`, rewritten metric emit, `ratioMetric`, `NOTES.md`.
- `layer/supersimple_test.go` — updated + new unit tests.
- `test/integration_test.go` — existing supersimple golden test already iterates emitted files (picks up new metrics + `NOTES.md`).
- `test/models/ecommerce/dbt/supersimple/*` — regenerated goldens (provisional).

---

### Task 1: Struct additions + expression compiler (`toPropertySQL`)

**Files:**
- Modify: `layer/supersimple.go`
- Modify: `layer/supersimple_test.go`

**Interfaces:**
- Produces: `ssProperty.Sql`; `ssMetric.Operations`; `ssAggregation.Property`; new types `ssPropRef, ssOperation, ssGroupAggregateParams, ssAggSpec, ssDeriveFieldParams, ssExprValue`; func `toPropertySQL(expr string) string`.

- [ ] **Step 1: Add the `sqllexer` import (`layer/supersimple.go`)**

Change the import block to include the lexer (already used in `dbt.go`):
```go
import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DataDog/go-sqllexer"
	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)
```

- [ ] **Step 2: Extend the output structs (`layer/supersimple.go`)**

Replace `ssProperty`, `ssMetric`, and `ssAggregation` with these, and add the new pipeline types after `ssAggregation`:
```go
type ssProperty struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
	Sql         string `yaml:"sql,omitempty"`
}
type ssMetric struct {
	Name        string        `yaml:"name"`
	ModelID     string        `yaml:"model_id"`
	Description string        `yaml:"description,omitempty"`
	Operations  []ssOperation `yaml:"operations,omitempty"`
	Aggregation ssAggregation `yaml:"aggregation"`
}
type ssAggregation struct {
	Type     string     `yaml:"type"`
	Key      string     `yaml:"key,omitempty"`
	Property *ssPropRef `yaml:"property,omitempty"`
}
type ssPropRef struct {
	Key  string `yaml:"key"`
	Name string `yaml:"name"`
}
type ssOperation struct {
	Operation  string `yaml:"operation"`
	Parameters any    `yaml:"parameters"`
}
type ssGroupAggregateParams struct {
	Groups       []any       `yaml:"groups"`
	Aggregations []ssAggSpec `yaml:"aggregations"`
}
type ssAggSpec struct {
	Type     string    `yaml:"type"`
	Key      string    `yaml:"key,omitempty"`
	Property ssPropRef `yaml:"property"`
}
type ssDeriveFieldParams struct {
	FieldName string      `yaml:"field_name"`
	Key       string      `yaml:"key"`
	Value     ssExprValue `yaml:"value"`
}
type ssExprValue struct {
	Expression string `yaml:"expression"`
	Version    string `yaml:"version"`
}
```

- [ ] **Step 3: Add `toPropertySQL` (`layer/supersimple.go`, near the other helpers)**

Wrap only identifiers that are real columns (`cols`), the same membership
approach `qualifyExpr` uses — the lexer's keyword table is unreliable (it does
not classify `when`/`then`), so IDENT-vs-KEYWORD alone would wrongly wrap `when`.
```go
// toPropertySQL rewrites a compound measure expression into supersimple's
// property.sql form: each column identifier (a member of cols, lowercased) is
// wrapped in {braces}; keywords, numbers, string literals and functions are
// left untouched.
// e.g. "case when is_refunded then 1 else 0 end" (cols={is_refunded}) ->
//      "case when {is_refunded} then 1 else 0 end".
func toPropertySQL(expr string, cols map[string]bool) string {
	lx := sqllexer.New(expr)
	var b strings.Builder
	for {
		tok := lx.Scan()
		if tok.Type == sqllexer.EOF || tok.Type == sqllexer.ERROR {
			break
		}
		if tok.Type == sqllexer.IDENT && cols[strings.ToLower(tok.Value)] {
			b.WriteByte('{')
			b.WriteString(tok.Value)
			b.WriteByte('}')
		} else {
			b.WriteString(tok.Value)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Write the failing test (`layer/supersimple_test.go`)**

```go
func TestToPropertySQL(t *testing.T) {
	cols := map[string]bool{"is_refunded": true, "status": true}
	got := toPropertySQL("case when is_refunded then 1 else 0 end", cols)
	if want := "case when {is_refunded} then 1 else 0 end"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// bare column wrapped; the string literal 'status' and keywords are not.
	got = toPropertySQL("case when status = 'status' then 1 else 0 end", cols)
	if want := "case when {status} = 'status' then 1 else 0 end"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 5: Run — expect PASS (structs compile; helper works)**

Run: `go test ./layer/ -run 'TestToPropertySQL|TestSupersimpleEmit'`
Expected: PASS. The struct additions are all `omitempty`/new, so the existing `TestSupersimpleEmit` and ecommerce golden are unchanged. Then `go build ./...`, `gofmt -l .` (empty), `go vet ./...`.

- [ ] **Step 6: Commit**

```bash
git add layer/supersimple.go layer/supersimple_test.go
git commit -m "feat(supersimple): pipeline structs + property.sql expression compiler"
```

---

### Task 2: Rich metric emit (compound property + same-table ratio pipeline)

**Files:**
- Modify: `layer/supersimple.go`
- Modify: `layer/supersimple_test.go`

**Interfaces:**
- Consumes: Task 1 structs + `toPropertySQL`; `ir.Metric.{Kind,Agg,Column,Table,Numerator,Denominator,Label,Description}`.
- Produces: `ratioMetric(...)` helper; rewritten metric-emit block.

- [ ] **Step 1: Add the `ratioMetric` helper (`layer/supersimple.go`)**

```go
// aggRef is a resolved supersimple aggregation for a simple metric.
type aggRef struct{ typ, key string }

// ratioMetric builds a same-table ratio as a groupAggregate -> deriveField ->
// first pipeline. NOTE: the whole-set groupAggregate shape and the deriveField
// expression grammar are provisional pending live-supersimple validation.
func ratioMetric(modelID, key, name, desc string, num, den aggRef) ssMetric {
	return ssMetric{
		Name: name, ModelID: modelID, Description: desc,
		Operations: []ssOperation{
			{Operation: "groupAggregate", Parameters: ssGroupAggregateParams{
				Groups: []any{},
				Aggregations: []ssAggSpec{
					{Type: num.typ, Key: num.key, Property: ssPropRef{Key: "_num", Name: "_num"}},
					{Type: den.typ, Key: den.key, Property: ssPropRef{Key: "_den", Name: "_den"}},
				},
			}},
			{Operation: "deriveField", Parameters: ssDeriveFieldParams{
				FieldName: name, Key: key,
				Value: ssExprValue{Expression: `prop("_num") / prop("_den")`, Version: "1"},
			}},
		},
		Aggregation: ssAggregation{Type: "first", Key: key, Property: &ssPropRef{Key: key, Name: name}},
	}
}
```

- [ ] **Step 2: Replace the metric-emit block (`layer/supersimple.go`)**

Replace the whole block from `file := ssFile{Models: map[string]ssModel{id: model}}` through the end of the `for _, mt := range t.Metrics { ... }` loop (the simple-only emit) with:
```go
		// cols is this table's column set (lowercased), used to wrap column
		// references in a compound measure's property.sql.
		cols := map[string]bool{}
		for _, d := range t.Dimensions {
			cols[strings.ToLower(d.Expr)] = true
		}
		for _, d := range t.TimeDimensions {
			cols[strings.ToLower(d.Expr)] = true
		}
		for _, meas := range t.Measures {
			if isIdent(meas.Expr) {
				cols[strings.ToLower(meas.Expr)] = true
			}
		}

		// Resolve every simple metric to its supersimple (type,key), synthesizing
		// a computed property.sql for compound-measure metrics. Do this before
		// creating the file so the synthesized properties are on the model, and
		// before emitting ratios so operands resolve.
		simpleAgg := map[string]aggRef{}
		for _, mt := range t.Metrics {
			if mt.Kind != "simple" {
				continue
			}
			key := strings.ToUpper(mt.Column)
			if !isIdent(mt.Column) { // compound measure -> synthesized sql property
				key = strings.ToUpper(mt.Name)
				model.Properties[key] = ssProperty{Name: key, Type: "Number", Sql: toPropertySQL(mt.Column, cols)}
			}
			simpleAgg[mt.Name] = aggRef{typ: mapAgg(mt.Agg), key: key}
		}

		file := ssFile{Models: map[string]ssModel{id: model}}
		metricName := func(mt ir.Metric) string {
			if mt.Label != "" {
				return mt.Label
			}
			return mt.Name
		}
		addMetric := func(name string, sm ssMetric) {
			if file.Metrics == nil {
				file.Metrics = map[string]ssMetric{}
			}
			file.Metrics[name] = sm
		}
		for _, mt := range t.Metrics {
			switch {
			case mt.Kind == "simple":
				ar := simpleAgg[mt.Name]
				addMetric(mt.Name, ssMetric{
					Name: metricName(mt), ModelID: id, Description: mt.Description,
					Aggregation: ssAggregation{Type: ar.typ, Key: ar.key},
				})
			case mt.Kind == "ratio":
				num, okN := simpleAgg[mt.Numerator]
				den, okD := simpleAgg[mt.Denominator]
				if !okN || !okD { // operands not both same-table simple metrics
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: operands span tables or are not simple aggregations — deferred to a later iteration", mt.Name))
					continue
				}
				addMetric(mt.Name, ratioMetric(id, mt.Name, metricName(mt), mt.Description, num, den))
			default:
				m.Notes = append(m.Notes, fmt.Sprintf("metric %q not emitted: unsupported kind %q", mt.Name, mt.Kind))
			}
		}
```

- [ ] **Step 3: Update `TestSupersimpleEmit` to exercise the new paths (`layer/supersimple_test.go`)**

Change the model's `Metrics` so `refund_rate` is a real same-table ratio over the two simple metrics, and add a compound-measure metric. Replace the `Metrics: []ir.Metric{...}` block (inside `fct_orders`) with:
```go
				Metrics: []ir.Metric{
					{Name: "net_revenue", Label: "Net revenue", Description: "Net booked revenue.", Kind: "simple", Agg: "sum", Table: "fct_orders", Column: "order_net_booked"},
					{Name: "orders", Label: "Orders", Kind: "simple", Agg: "count_distinct", Table: "fct_orders", Column: "order_id"},
					{Name: "refunded_orders", Label: "Refunded orders", Kind: "simple", Agg: "sum", Table: "fct_orders", Column: "case when is_refunded then 1 else 0 end"},
					{Name: "refund_rate", Label: "Refund rate", Kind: "ratio", Table: "fct_orders", Numerator: "refunded_orders", Denominator: "orders"},
				},
```
Then replace the metric-related assertions (the `type: sum` / `count_distinct` / `refund_rate`-skip section) with:
```go
	for _, want := range []string{
		"name: Net revenue",
		"description: Net booked revenue.",
		"type: sum",
		"type: count_distinct",
		"key: ORDER_ID",
		// compound measure -> synthesized property.sql + a sum metric over it
		"sql: case when {is_refunded} then 1 else 0 end",
		"key: REFUNDED_ORDERS",
		// same-table ratio -> operations pipeline
		"operation: groupAggregate",
		"operation: deriveField",
		`expression: prop("_num") / prop("_den")`,
		"type: first",
	} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}
	if strings.Contains(orders, "countDistinct") {
		t.Fatalf("aggregation type must be snake_case count_distinct:\n%s", orders)
	}
```
(Delete the old `m.Notes` "refund_rate not representable" assertion — `refund_rate` is now emitted. Keep the DIM_CUSTOMER `hasMany` relation assertions unchanged.)

- [ ] **Step 4: Run the unit tests**

Run: `go test ./layer/ -run TestSupersimpleEmit -v`
Expected: PASS. If a snippet is missing, the failure names it — fix the emitter.

- [ ] **Step 5: Full suite + hygiene**

Run: `go test ./... && gofmt -l . && go vet ./...`
Expected: `./layer` green; `./test` (ecommerce supersimple golden) will now FAIL because the emitted metrics changed — that's expected and fixed in Task 3 (do not regenerate goldens here). Confirm the ONLY failure is `TestEcommerceSupersimpleGolden`; `./cmd/semglot`, `TestEcommerceCortexGolden`, and `./layer` are green.

- [ ] **Step 6: Commit**

```bash
git add layer/supersimple.go layer/supersimple_test.go
git commit -m "feat(supersimple): compound-measure property.sql metrics + same-table ratio pipeline"
```

---

### Task 3: `NOTES.md` sidecar + regenerate ecommerce goldens

**Files:**
- Modify: `layer/supersimple.go`
- Regenerate: `test/models/ecommerce/dbt/supersimple/*`

**Interfaces:**
- Consumes: `m.Notes` (parser notes + emitter-appended deferrals).
- Produces: `<out>/NOTES.md` when any metric is deferred.

- [ ] **Step 1: Write `NOTES.md` at the end of `Emit` (`layer/supersimple.go`)**

Immediately before the final `return nil` of `Emit` (after the `for _, t := range m.Tables` loop closes):
```go
	if len(m.Notes) > 0 {
		var sb strings.Builder
		sb.WriteString("# Not transpiled to supersimple\n\n")
		for _, n := range m.Notes {
			sb.WriteString("- " + n + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte(sb.String()), 0o644); err != nil {
			return err
		}
	}
	return nil
```

- [ ] **Step 2: Regenerate the ecommerce supersimple goldens**

Run: `UPDATE_GOLDEN=1 go test ./test/ -run TestEcommerceSupersimpleGolden`
Then inspect:
```bash
ls test/models/ecommerce/dbt/supersimple/
cat test/models/ecommerce/dbt/supersimple/FCT_ORDERS.yaml
cat test/models/ecommerce/dbt/supersimple/NOTES.md
```
Expected:
- `FCT_ORDERS.yaml`: a synthesized `REFUNDED_ORDERS` property with `sql: case when {is_refunded} then 1 else 0 end`; metrics `gross_revenue`, `net_revenue`, `orders`, `refunded_orders` (simple), and `aov` + `refund_rate` as `operations` pipelines.
- `FCT_ORDER_LINES.yaml`: `units_sold` simple metric (only).
- `NOTES.md`: one entry — `units_per_order` (ratio) deferred (cross-table).
Verify no invented values; every property/metric traces to the dbt source.

- [ ] **Step 3: Confirm the golden test passes + Cortex unchanged**

Run: `go test ./test/ -run 'Supersimple|Cortex' -v`
Expected: `TestEcommerceSupersimpleGolden` PASS; `TestEcommerceCortexGolden` PASS (byte-identical).

- [ ] **Step 4: Full suite, hygiene, smoke**

```bash
go test ./... && gofmt -l . && go vet ./...
go run ./cmd/semglot build --from dbt --reference test/models/ecommerce/dbt --layer supersimple --out /tmp/ss-rich --schema MAIN
ls /tmp/ss-rich && cat /tmp/ss-rich/NOTES.md
```
Expected: all green; stderr warns about `units_per_order` deferred; `/tmp/ss-rich` has the per-table files + `NOTES.md`.

- [ ] **Step 5: Commit**

```bash
git add layer/supersimple.go test/models/ecommerce/dbt/supersimple
git commit -m "feat(supersimple): NOTES.md sidecar; regenerate ecommerce goldens (provisional ratios)"
```

---

## Self-Review

**1. Spec coverage:**
- Compound-measure metrics via `property.sql` → Task 1 (`toPropertySQL`) + Task 2 (synthesis). ✅
- Same-table ratios via `operations` pipeline → Task 2 (`ratioMetric`). ✅
- Expression compiler (property.sql `{col}` target) → Task 1. (Formula `prop("KEY")` is built structurally in `ratioMetric`, not from raw SQL — correct per spec.) ✅
- Measure→property resolver → Task 2 (`simpleAgg` pass). ✅
- `NOTES.md` sidecar → Task 3. ✅
- Cross-table ratio (`units_per_order`) deferred to sidecar → Task 2 (operand-not-found → note) + Task 3 golden. ✅
- No IR change → confirmed; operands resolve within the table's metric list. ✅
- Cortex unchanged / provisional ratio golden → Task 2 Step 5, Task 3 Steps 2–3. ✅

**2. Placeholder scan:** No TBD/TODO. The `Groups: []any{}` whole-set grouping is the spec's provisional item #1, deliberately explicit (not a placeholder) and flagged for push validation. ✅

**3. Type consistency:** `aggRef`, `ratioMetric(modelID,key,name,desc,num,den)`, `ssMetric.Operations`, `ssAggregation.Property`, `ssPropRef`, `ssGroupAggregateParams`, `ssAggSpec`, `ssDeriveFieldParams`, `ssExprValue`, `toPropertySQL` — used with identical signatures across tasks. Reuses `mapAgg`/`isIdent`/`upperAll`; no collisions with existing names. ✅

**Provisional note for the reviewer/validator:** the same-table ratio YAML (`groups: []`, `prop("_num") / prop("_den")`, terminal `first`) is a hypothesis. It is expected to be revised after the emitted config is pushed to a live supersimple project; only `ratioMetric` + the ratio golden change when it is.
