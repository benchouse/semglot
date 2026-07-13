# supersimple cross-table ratios — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Emit one-hop cross-table ratios (e.g. `units_per_order`) as a supersimple metric `operations` pipeline on the parent table (via `relationAggregate`), instead of deferring them to `NOTES.md`.

**Architecture:** Add `findParentRelation` + `crossRatioMetric` helpers, then restructure `Emit` into three phases (build models + a global simple-metric registry → assign each metric to a file → write files) so cross-table operands resolve and the metric can re-home to the parent's file. Same-table/simple/compound output stays byte-identical.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (only dep). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; exported packages, no `internal/`; no new deps.
- Cross-dialect identifiers lowercase in IR, UPPERCASED on emit.
- **Cortex output unchanged** (`test/models/ecommerce/dbt/cortex/ecommerce.yaml` byte-identical).
- **Behavior-preserving for existing supersimple cases:** simple, compound-measure, and same-table ratio output must be byte-identical. The ONLY intended output change is `units_per_order` moving from `NOTES.md` into `FCT_ORDERS.yaml` (and `NOTES.md` no longer being written, since it becomes empty).
- The `relationAggregate` pipeline is **provisional** pending live-supersimple push validation — emit the best hypothesis; the golden is expected to change after validation. Only `crossRatioMetric` + the `units_per_order` golden change if so.
- Reuse existing helpers (`slug`, `prettify`, `upperAll`, `mapAgg`, `isIdent`, `ssType`, `toPropertySQL`, `ratioMetric`, `aggRef`); do not duplicate.
- `go build ./...`, `go test ./...`, `test -z "$(gofmt -l .)"`, `go vet ./...` clean per task.

## File Structure

- `layer/supersimple.go` — new structs (`ssRelationAggregateParams`, `ssRelationRef`), `findParentRelation`, `crossOperand`, `crossRatioMetric`, and the three-phase `Emit`.
- `layer/supersimple_test.go` — unit tests for the new helpers + an Emit-level cross-table test.
- `test/models/ecommerce/dbt/supersimple/*` — regenerated goldens (`FCT_ORDERS.yaml` gains `units_per_order`; `NOTES.md` deleted).

---

### Task 1: `findParentRelation`, `crossRatioMetric`, and relationAggregate structs

**Files:**
- Modify: `layer/supersimple.go`
- Modify: `layer/supersimple_test.go`

**Interfaces:**
- Produces: `ssRelationAggregateParams`, `ssRelationRef` (yaml types); `findParentRelation(m *ir.Model, a, b string) (parent, relKey, child string, ok bool)`; `crossOperand{onBase bool; aggType, key string}`; `crossRatioMetric(baseID, key, relKey, name, desc string, num, den crossOperand) ssMetric`.

- [ ] **Step 1: Add the relationAggregate structs (`layer/supersimple.go`, after `ssDeriveFieldParams`)**

```go
type ssRelationAggregateParams struct {
	Relation     ssRelationRef `yaml:"relation"`
	Aggregations []ssAggSpec   `yaml:"aggregations"`
}
type ssRelationRef struct {
	Key string `yaml:"key"`
}
```

- [ ] **Step 2: Add `findParentRelation` and `crossRatioMetric` (`layer/supersimple.go`, near `ratioMetric`)**

```go
// findParentRelation returns the one-hop relationship connecting tables a and b
// (in either order): the parent (the Right/one side, which owns the hasMany
// relation), the relation key the emitter puts under the parent's relations
// (slug(child)), and the child (the Left/many side). ok=false if not directly related.
func findParentRelation(m *ir.Model, a, b string) (parent, relKey, child string, ok bool) {
	for _, r := range m.Relationships {
		if (r.Left == a && r.Right == b) || (r.Left == b && r.Right == a) {
			return r.Right, slug(r.Left), r.Left, true
		}
	}
	return "", "", "", false
}

// crossOperand describes one side of a cross-table ratio. onBase is true when the
// operand aggregates the parent (base) table directly; otherwise it aggregates the
// child table and must be pulled across the relation.
type crossOperand struct {
	onBase  bool
	aggType string
	key     string
}

// crossRatioMetric builds a cross-table ratio on the parent base: each child
// operand is pulled via relationAggregate (a per-parent value) then summed in the
// whole-set groupAggregate; each parent operand is aggregated directly there; the
// two named _num/_den columns are divided. Provisional pending live validation.
func crossRatioMetric(baseID, key, relKey, name, desc string, num, den crossOperand) ssMetric {
	var ops []ssOperation
	ga := ssGroupAggregateParams{Groups: []any{}}

	add := func(op crossOperand, propKey string) {
		if op.onBase {
			ga.Aggregations = append(ga.Aggregations, ssAggSpec{
				Type: op.aggType, Key: op.key, Property: ssPropRef{Key: propKey, Name: propKey},
			})
			return
		}
		rel := propKey + "_rel"
		ops = append(ops, ssOperation{Operation: "relationAggregate", Parameters: ssRelationAggregateParams{
			Relation:     ssRelationRef{Key: relKey},
			Aggregations: []ssAggSpec{{Type: op.aggType, Key: op.key, Property: ssPropRef{Key: rel, Name: rel}}},
		}})
		ga.Aggregations = append(ga.Aggregations, ssAggSpec{
			Type: "sum", Key: rel, Property: ssPropRef{Key: propKey, Name: propKey},
		})
	}
	add(num, "_num")
	add(den, "_den")
	ops = append(ops, ssOperation{Operation: "groupAggregate", Parameters: ga})
	ops = append(ops, ssOperation{Operation: "deriveField", Parameters: ssDeriveFieldParams{
		FieldName: name, Key: key, Value: ssExprValue{Expression: `prop("_num") / prop("_den")`, Version: "1"},
	}})
	return ssMetric{
		Name: name, ModelID: baseID, Description: desc, Operations: ops,
		Aggregation: ssAggregation{Type: "first", Key: key, Property: &ssPropRef{Key: key, Name: name}},
	}
}
```

- [ ] **Step 3: Write the failing tests (`layer/supersimple_test.go`)**

```go
func TestFindParentRelation(t *testing.T) {
	m := &ir.Model{Relationships: []ir.Relationship{
		{Left: "fct_order_lines", Right: "fct_orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
	}}
	// either argument order finds the same parent/child/relKey.
	for _, pair := range [][2]string{{"fct_order_lines", "fct_orders"}, {"fct_orders", "fct_order_lines"}} {
		parent, relKey, child, ok := findParentRelation(m, pair[0], pair[1])
		if !ok || parent != "fct_orders" || child != "fct_order_lines" || relKey != "order_lines" {
			t.Fatalf("%v: got parent=%q relKey=%q child=%q ok=%v", pair, parent, relKey, child, ok)
		}
	}
	if _, _, _, ok := findParentRelation(m, "fct_orders", "dim_product"); ok {
		t.Fatal("unrelated tables should return ok=false")
	}
}

func TestCrossRatioMetric(t *testing.T) {
	// units_per_order = units_sold(child sum QUANTITY) / orders(base count_distinct ORDER_ID)
	sm := crossRatioMetric("FCT_ORDERS", "units_per_order", "order_lines", "Units per order", "u/o",
		crossOperand{onBase: false, aggType: "sum", key: "QUANTITY"},
		crossOperand{onBase: true, aggType: "count_distinct", key: "ORDER_ID"})
	b, err := yaml.Marshal(sm)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"model_id: FCT_ORDERS",
		"operation: relationAggregate",
		"key: order_lines",     // relation key
		"key: QUANTITY",        // child operand pulled across the relation
		"operation: groupAggregate",
		"type: count_distinct", // parent operand direct
		"key: ORDER_ID",
		"operation: deriveField",
		`prop("_num") / prop("_den")`,
		"type: first",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("crossRatioMetric missing %q:\n%s", want, out)
		}
	}
}
```
(These need `yaml` imported in the test file — add `"gopkg.in/yaml.v3"` to `layer/supersimple_test.go`'s imports.)

- [ ] **Step 4: Run the tests**

Run: `go test ./layer/ -run 'FindParentRelation|CrossRatioMetric' -v`
Expected: PASS. Then `go build ./...`, `go test ./...` (existing tests unaffected — the helpers aren't wired into `Emit` yet), `test -z "$(gofmt -l .)"`, `go vet ./...`.

- [ ] **Step 5: Commit**

```bash
git add layer/supersimple.go layer/supersimple_test.go
git commit -m "feat(supersimple): findParentRelation + crossRatioMetric (relationAggregate)"
```

---

### Task 2: Restructure `Emit` into three phases + wire cross-table

**Files:**
- Modify: `layer/supersimple.go`
- Modify: `layer/supersimple_test.go`

**Interfaces:**
- Consumes: Task 1 helpers; existing `ratioMetric`, `aggRef`, property/relation build logic.
- Produces: a three-phase `Emit` that resolves ratio operands globally and re-homes cross-table ratios to the parent file.

- [ ] **Step 1: Replace the body of `Emit` (`layer/supersimple.go`)**

Replace everything from `// relationships grouped by parent (Right) table` down to the final `return nil` of `Emit` with:
```go
	// relationships grouped by parent (Right) table
	relsByParent := map[string][]ir.Relationship{}
	for _, r := range m.Relationships {
		relsByParent[r.Right] = append(relsByParent[r.Right], r)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	type tableState struct {
		id      string
		model   ssModel
		metrics map[string]ssMetric
	}
	states := map[string]*tableState{}
	var order []string

	// metric name -> its resolved simple aggregation + owning table (global so
	// ratio operands resolve across tables).
	type simpleInfo struct{ table, typ, key string }
	global := map[string]simpleInfo{}

	// Phase 1: build each model (properties incl. synthesized compound property.sql,
	// and relations) and register its simple metrics.
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
			if !isIdent(meas.Expr) {
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
		for _, mt := range t.Metrics {
			if mt.Kind != "simple" {
				continue
			}
			key := strings.ToUpper(mt.Column)
			if !isIdent(mt.Column) {
				key = strings.ToUpper(mt.Name)
				for {
					if _, taken := model.Properties[key]; !taken {
						break
					}
					key += "_EXPR"
				}
				model.Properties[key] = ssProperty{Name: key, Type: "Number", Sql: toPropertySQL(mt.Column, cols)}
			}
			global[mt.Name] = simpleInfo{table: t.Name, typ: mapAgg(mt.Agg), key: key}
		}

		states[t.Name] = &tableState{id: id, model: model, metrics: map[string]ssMetric{}}
		order = append(order, t.Name)
	}

	// Phase 2: assign every metric to a file.
	metricName := func(mt ir.Metric) string {
		if mt.Label != "" {
			return mt.Label
		}
		return mt.Name
	}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			switch {
			case mt.Kind == "simple":
				si := global[mt.Name]
				st := states[si.table]
				st.metrics[mt.Name] = ssMetric{
					Name: metricName(mt), ModelID: st.id, Description: mt.Description,
					Aggregation: ssAggregation{Type: si.typ, Key: si.key},
				}
			case mt.Kind == "ratio":
				num, okN := global[mt.Numerator]
				den, okD := global[mt.Denominator]
				if !okN || !okD {
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: operand(s) are not a simple aggregation", mt.Name))
					continue
				}
				if num.table == den.table {
					st := states[num.table]
					st.metrics[mt.Name] = ratioMetric(st.id, mt.Name, metricName(mt), mt.Description,
						aggRef{typ: num.typ, key: num.key}, aggRef{typ: den.typ, key: den.key})
					continue
				}
				parent, relKey, child, ok := findParentRelation(m, num.table, den.table)
				if !ok {
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: operand tables %q and %q are not directly related", mt.Name, num.table, den.table))
					continue
				}
				childInfo := num
				if den.table == child {
					childInfo = den
				}
				if childInfo.typ != "sum" && childInfo.typ != "count" {
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: child operand aggregation %q does not compose across the relation", mt.Name, childInfo.typ))
					continue
				}
				states[parent].metrics[mt.Name] = crossRatioMetric(states[parent].id, mt.Name, relKey, metricName(mt), mt.Description,
					crossOperand{onBase: num.table == parent, aggType: num.typ, key: num.key},
					crossOperand{onBase: den.table == parent, aggType: den.typ, key: den.key})
			default:
				m.Notes = append(m.Notes, fmt.Sprintf("metric %q not emitted: unsupported kind %q", mt.Name, mt.Kind))
			}
		}
	}

	// Phase 3: write per-table files (in table order), then NOTES.md.
	for _, name := range order {
		st := states[name]
		file := ssFile{Models: map[string]ssModel{st.id: st.model}}
		if len(st.metrics) > 0 {
			file.Metrics = st.metrics
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
		if err := os.WriteFile(filepath.Join(dir, st.id+".yaml"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
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

- [ ] **Step 2: Add an Emit-level cross-table test (`layer/supersimple_test.go`)**

```go
func TestSupersimpleCrossTableRatioEmit(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name: "fct_orders", PrimaryKey: []string{"order_id"},
				Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id", DataType: "number"}},
				Metrics: []ir.Metric{
					{Name: "orders", Kind: "simple", Agg: "count_distinct", Table: "fct_orders", Column: "order_id"},
				},
			},
			{
				Name: "fct_order_lines", PrimaryKey: []string{"line_id"},
				Dimensions: []ir.Field{{Name: "line_id", Expr: "line_id", DataType: "number"}},
				Measures:   []ir.Measure{{Field: ir.Field{Name: "units_sold", Expr: "quantity", DataType: "number"}, Agg: "sum"}},
				Metrics: []ir.Metric{
					{Name: "units_sold", Kind: "simple", Agg: "sum", Table: "fct_order_lines", Column: "quantity"},
					{Name: "units_per_order", Kind: "ratio", Table: "fct_order_lines", Numerator: "units_sold", Denominator: "orders"},
				},
			},
		},
		Relationships: []ir.Relationship{
			{Left: "fct_order_lines", Right: "fct_orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
		},
	}
	dir := t.TempDir()
	if err := (supersimple{Schema: "MAIN"}).Emit(m, dir); err != nil {
		t.Fatal(err)
	}
	orders := readFile(t, filepath.Join(dir, "FCT_ORDERS.yaml"))
	// units_per_order re-homes to the parent (fct_orders) with a relationAggregate pipeline.
	for _, want := range []string{"units_per_order", "operation: relationAggregate", "key: order_lines", "key: QUANTITY", `prop("_num") / prop("_den")`} {
		if !strings.Contains(orders, want) {
			t.Fatalf("FCT_ORDERS.yaml missing %q:\n%s", want, orders)
		}
	}
	lines := readFile(t, filepath.Join(dir, "FCT_ORDER_LINES.yaml"))
	if strings.Contains(lines, "units_per_order") {
		t.Fatalf("units_per_order must not be in the child file:\n%s", lines)
	}
	// no deferral note was produced -> no NOTES.md
	if _, err := os.Stat(filepath.Join(dir, "NOTES.md")); err == nil {
		t.Fatal("NOTES.md should not exist when nothing is deferred")
	}
}
```

- [ ] **Step 3: Run unit tests — existing behavior preserved + new cross-table test**

Run: `go test ./layer/ -v 2>&1 | grep -E "FAIL|ok|PASS: TestSupersimple"`
Expected: all `./layer` tests PASS, including the unchanged `TestSupersimpleEmit`/`TestSupersimpleCompoundKeyNoClobber` (behavior-preserving) and the new `TestSupersimpleCrossTableRatioEmit`.

- [ ] **Step 4: Confirm only the ecommerce golden is now red**

Run: `go test ./... 2>&1 | tail -6`
Expected: `./layer` and `./cmd/semglot` green; `TestEcommerceCortexGolden` green; the ONLY failure is `./test`'s `TestEcommerceSupersimpleGolden` (units_per_order re-homed + NOTES.md removed) — fixed in Task 3. Do NOT regenerate goldens here. `test -z "$(gofmt -l .)"`, `go vet ./...` clean.

- [ ] **Step 5: Commit**

```bash
git add layer/supersimple.go layer/supersimple_test.go
git commit -m "feat(supersimple): three-phase Emit; wire cross-table ratios to the parent"
```

---

### Task 3: Regenerate ecommerce goldens (units_per_order re-homed; NOTES.md removed)

**Files:**
- Regenerate: `test/models/ecommerce/dbt/supersimple/FCT_ORDERS.yaml`
- Delete: `test/models/ecommerce/dbt/supersimple/NOTES.md`

- [ ] **Step 1: Regenerate + delete the now-stale NOTES.md**

```bash
UPDATE_GOLDEN=1 go test ./test/ -run TestEcommerceSupersimpleGolden
git rm test/models/ecommerce/dbt/supersimple/NOTES.md
```
(The regeneration writes the produced files but does NOT delete an orphaned golden; `units_per_order` is now emitted so `m.Notes` is empty and no `NOTES.md` is produced — the stale golden must be removed by hand, else the golden set-equality check fails.)

- [ ] **Step 2: Inspect the regenerated golden**

```bash
cat test/models/ecommerce/dbt/supersimple/FCT_ORDERS.yaml
ls test/models/ecommerce/dbt/supersimple/
```
Expected: `FCT_ORDERS.yaml` now contains a `units_per_order` metric with a `relationAggregate` on the `order_lines` relation pulling `sum(QUANTITY)`, a `groupAggregate` producing `_num`/`_den` (with `count_distinct` on `ORDER_ID`), a `deriveField` dividing them, and terminal `first`. No `NOTES.md`. Every value traces to the dbt source.

- [ ] **Step 3: Full suite + hygiene + smoke**

```bash
go test ./... && test -z "$(gofmt -l .)" && echo fmt-ok && go vet ./...
go run ./cmd/semglot build --from dbt --reference test/models/ecommerce/dbt --layer supersimple --out /tmp/ss-cross --schema MAIN
ls /tmp/ss-cross   # expect NO NOTES.md; FCT_ORDERS.yaml has units_per_order
```
Expected: all green (incl. `TestEcommerceSupersimpleGolden` and its set-equality check); the build prints no `warning:` line (nothing deferred) and writes no `NOTES.md`.

- [ ] **Step 4: Commit**

```bash
git add test/models/ecommerce/dbt/supersimple
git commit -m "feat(supersimple): ecommerce golden — units_per_order cross-table (provisional); drop NOTES.md"
```

---

## Self-Review

**1. Spec coverage:**
- `findParentRelation` (one-hop, either order) → Task 1. ✅
- `crossRatioMetric` (relationAggregate child → groupAggregate → deriveField → first; _num/_den by identity) → Task 1. ✅
- Three-phase `Emit` + global registry + re-home to parent → Task 2. ✅
- Same-table/simple/compound behavior-preserving → Task 2 Step 3 (existing tests green). ✅
- Child-operand composability guard (`sum`/`count` only) → Task 2 Step 1. ✅
- One-hop-only; unrelated → NOTES.md → Task 2 Step 1. ✅
- Golden regenerate + delete stale NOTES.md → Task 3. ✅
- Cortex unchanged → Task 2 Step 4, Task 3 Step 3. ✅

**2. Placeholder scan:** No TBD/TODO; every step shows full code. `Groups: []any{}` (whole-set) is the documented provisional item, not a placeholder. ✅

**3. Type consistency:** `simpleInfo`, `tableState`, `crossOperand`, `crossRatioMetric(baseID,key,relKey,name,desc,num,den)`, `findParentRelation(...)→(parent,relKey,child,ok)`, `ssRelationAggregateParams`/`ssRelationRef`, reused `ratioMetric`/`aggRef` — consistent across tasks. `global`/`states` keyed by table name; `id` is `UPPER(name)`. ✅

**Provisional note for reviewer/validator:** the `relationAggregate` + `groupAggregate(sum of _rel)` shape is a hypothesis validated by pushing the emitted config to a live supersimple project; if it needs changes only `crossRatioMetric` and the `units_per_order` golden move.
