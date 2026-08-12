# ossie Dialect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `ossie` as semglot's second source dialect and eighth target, reading and writing the Apache Ossie Core Metadata Specification, and add `ir.Table.Synonyms` across every dialect.

**Architecture:** `dialect/ossie.go` (structs + `Parse`) and `dialect/ossie_emit.go` (`Emit` + `WithOptions`) register `ossie` through the existing `Parser`/`Emitter`/`Configurable` interfaces. The `derivedParser` in `dbt.go` is extracted to `dialect/sqlexpr.go` and parameterized so ossie can parse SQL aggregate expressions without changing dbt's behaviour. `ir.Table` gains `Synonyms`, wired structurally where a target has a slot and folded into descriptions where it does not.

**Tech Stack:** Go (version in `go.mod`), `gopkg.in/yaml.v3` — the project's only dependency. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-12-ossie-dialect-design.md`

## Global Constraints

- **One PR** on branch `feat/ossie-dialect`, based on `main`. Do not stack branches.
- **No new dependencies.** `gopkg.in/yaml.v3` is the only permitted import outside the standard library.
- **Every commit must pass:** `gofmt -l .` (prints nothing), `go vet ./...`, `go build ./...`, `go test ./...`. CI runs all four.
- **Golden files** regenerate with `UPDATE_GOLDEN=1 go test ./...`; always review the diff before committing.
- **OSI spec version** is the literal string `0.2.0.dev0`. It is a pinned const, never computed.
- **Upstream pin:** vendored Apache Ossie fixtures come from commit `88e0011148283302c9a04cd0287e00e0b9d87354` (2026-07-31). Apache Ossie is Apache-2.0; semglot is MIT. Vendored files keep their ASF headers.
- **Emitters must not mutate the model.** `Emit` takes `*ir.Model` read-only; warnings are returned, never appended to `m.Notes`.
- **Never drop silently.** Anything a target cannot express becomes a returned warning or a note.

---

### Task 1: `ir.Table.Synonyms` and the dbt round-trip

**Files:**
- Modify: `ir/model.go:18-27`
- Modify: `dialect/dbt.go:34-42` (`dbtModel`), `dialect/dbt.go:356-362` (table construction)
- Modify: `dialect/dbt_emit.go:33-39` (`dbtEmitModel`), `dialect/dbt_emit.go:221`
- Test: `dialect/dbt_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `ir.Table.Synonyms []string`. Every later task reads or writes it.

- [ ] **Step 1: Write the failing test**

Add to `dialect/dbt_test.go`:

```go
// TestParseModelSynonyms reads model-level meta.synonyms into ir.Table.Synonyms,
// mirroring the column-level meta.synonyms convention.
func TestParseModelSynonyms(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "schema.yml", `
models:
  - name: fct_orders
    description: Orders.
    meta:
      synonyms: [purchases, sales]
    columns:
      - name: order_id
        data_type: number
`)
	m, err := dbt{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(m.Tables))
	}
	got := m.Tables[0].Synonyms
	want := []string{"purchases", "sales"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Synonyms = %v, want %v", got, want)
	}
}

// TestEmitModelSynonyms round-trips table synonyms back out through dbt.
func TestEmitModelSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "fct_orders",
		Synonyms:   []string{"purchases", "sales"},
		Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id"}},
	}}}
	out := t.TempDir()
	if _, err := (dbt{}).Emit(m, out); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(out, "semantic_layer.yml"))
	for _, want := range []string{"synonyms:", "purchases", "sales"} {
		if !strings.Contains(got, want) {
			t.Errorf("emitted dbt missing %q in:\n%s", want, got)
		}
	}
}
```

If `writeFile` / `readFile` helpers do not already exist in `dialect/dbt_test.go`, check the existing test file for its fixture helpers and use those instead — do not add duplicates. Confirm the emitted dbt filename by reading `dialect/dbt_emit.go`'s `os.WriteFile` call and use the real name in `TestEmitModelSynonyms`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run 'TestParseModelSynonyms|TestEmitModelSynonyms' -v`
Expected: FAIL — `m.Tables[0].Synonyms` undefined (`ir.Table` has no field `Synonyms`).

- [ ] **Step 3: Add the IR field**

In `ir/model.go`, add to `Table` (after `Description`):

```go
	// Synonyms are alternative names for the entity itself, as distinct from a
	// field's synonyms. Sourced from a model-level meta.synonyms (dbt) or a
	// dataset's ai_context.synonyms (ossie). Emitters render it structurally
	// where the target has a table-level slot, else fold it into the table
	// description.
	Synonyms []string
```

- [ ] **Step 4: Wire the dbt parser**

In `dialect/dbt.go`, add a `Meta` field to `dbtModel` (reusing `dbtColumnMeta`, whose `Enum` key is simply unused at model level):

```go
type dbtModel struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Meta        dbtColumnMeta   `yaml:"meta"`
	Constraints []dbtConstraint `yaml:"constraints"`
	Columns     []dbtColumn     `yaml:"columns"`
	// TimeSpine, when present, marks a dbt MetricFlow date-spine model —
	// internal plumbing, not a business table. Presence alone is the signal.
	TimeSpine *dbtTimeSpine `yaml:"time_spine"`
}
```

Then at `dialect/dbt.go:356-362`, after the `t.Description` assignment, add:

```go
	t.Synonyms = md.Meta.Synonyms
```

Read the `meta:` key only, exactly as columns do. `Options.DbtMetaKeyPath` is a Lightdash emit concern (`dialect.go:44-47`) and does not apply to the dbt parser.

- [ ] **Step 5: Wire the dbt emitter**

In `dialect/dbt_emit.go`, add a `Meta` field to `dbtEmitModel`:

```go
type dbtEmitModel struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description,omitempty"`
	Meta        *dbtEmitMeta    `yaml:"meta,omitempty"`
	Columns     []dbtEmitColumn `yaml:"columns,omitempty"`
}
```

At `dialect/dbt_emit.go:221`, replace the construction:

```go
	em := dbtEmitModel{Name: t.Name, Description: t.Description}
	if len(t.Synonyms) > 0 {
		em.Meta = &dbtEmitMeta{Synonyms: t.Synonyms}
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./dialect/ -run 'TestParseModelSynonyms|TestEmitModelSynonyms' -v`
Expected: PASS

- [ ] **Step 7: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all tests pass, vet silent, `gofmt -l .` prints nothing.

- [ ] **Step 8: Commit**

```bash
git add ir/model.go dialect/dbt.go dialect/dbt_emit.go dialect/dbt_test.go
git commit -m "feat(ir): add Table.Synonyms, round-tripped through dbt

ir.Table had no synonyms field while ir.Field and ir.Metric both do,
so an entity's alternative names had nowhere structural to live. dbt
reads and writes them as a model-level meta.synonyms block, mirroring
the existing column-level convention."
```

---

### Task 2: Structural table synonyms for `snowflake-semantic-view` and `cortex`

**Files:**
- Modify: `dialect/snowflake_semantic_view.go:70-79`
- Modify: `dialect/cortex.go:50-58` (`cortexTable`), `dialect/cortex.go:125-129`
- Test: `dialect/snowflake_semantic_view_test.go`, `dialect/cortex_test.go`

**Interfaces:**
- Consumes: `ir.Table.Synonyms` from Task 1.
- Produces: nothing new; both emitters gain a structural synonyms slot.

- [ ] **Step 1: Verify Cortex supports table-level synonyms**

Before writing code, confirm against Snowflake's Cortex Analyst semantic-model YAML specification that a `tables[]` entry accepts a `synonyms:` key. `cortexCol` already carries one for dimensions, facts, and time dimensions.

If tables are **supported**, continue with this task as written.
If tables are **not supported**, skip every Cortex step below, and instead handle cortex in Task 3 with the prose-folding dialects — folding into `cortexTable.Description` via `appendClause(t.Description, synonymClause(t.Synonyms))`. Record which branch you took in the commit message.

- [ ] **Step 2: Write the failing tests**

Add to `dialect/snowflake_semantic_view_test.go`:

```go
// TestSemanticViewTableSynonyms emits table synonyms as a `with synonyms (...)`
// clause. Snowflake accepts the clause on tables as well as columns; svSynonyms
// already renders it.
func TestSemanticViewTableSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:        "fct_orders",
		Description: "Orders.",
		Synonyms:    []string{"purchases", "sales"},
		PrimaryKey:  []string{"order_id"},
		Dimensions:  []ir.Field{{Name: "order_id", Expr: "order_id"}},
	}}}
	out := t.TempDir()
	e := snowflakeSemanticView{}.WithOptions(Options{Database: "A", Schema: "M", Name: "v"})
	if _, err := e.Emit(m, out); err != nil {
		t.Fatal(err)
	}
	got := readGolden(t, filepath.Join(out, "definition.md"))
	want := "FCT_ORDERS as A.M.FCT_ORDERS primary key (ORDER_ID) with synonyms ('purchases', 'sales') comment='Orders.'"
	if !strings.Contains(got, want) {
		t.Errorf("definition.md missing %q in:\n%s", want, got)
	}
}
```

Add to `dialect/cortex_test.go`:

```go
// TestCortexTableSynonyms emits table-level synonyms structurally.
func TestCortexTableSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "fct_orders",
		Synonyms:   []string{"purchases", "sales"},
		Dimensions: []ir.Field{{Name: "order_id", Expr: "order_id"}},
	}}}
	out := t.TempDir()
	e := cortex{}.WithOptions(Options{Database: "A", Schema: "M", Name: "ecommerce"})
	if _, err := e.Emit(m, out); err != nil {
		t.Fatal(err)
	}
	got := readGolden(t, filepath.Join(out, "semantic_model.yaml"))
	for _, want := range []string{"synonyms:", "purchases", "sales"} {
		if !strings.Contains(got, want) {
			t.Errorf("semantic_model.yaml missing %q in:\n%s", want, got)
		}
	}
}
```

Use whatever file-reading helper the existing tests in each file use rather than inventing `readGolden`; check the top of each test file first.

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./dialect/ -run 'TestSemanticViewTableSynonyms|TestCortexTableSynonyms' -v`
Expected: FAIL — the synonyms clause / key is absent from both outputs.

- [ ] **Step 4: Wire snowflake-semantic-view**

At `dialect/snowflake_semantic_view.go:73-79`, insert the synonyms clause between the primary key and the comment, matching the ordering `svSynonyms`' own doc comment describes ("the clause sits before `comment`"):

```go
		if len(t.PrimaryKey) > 0 {
			line += fmt.Sprintf(" primary key (%s)", strings.Join(upperAll(t.PrimaryKey), ","))
		}
		if syn := svSynonyms(t.Synonyms); syn != "" {
			line += " " + syn
		}
		if t.Description != "" {
			line += fmt.Sprintf(" comment='%s'", sqlQuote(t.Description))
		}
```

- [ ] **Step 5: Wire cortex**

Add the field to `cortexTable` in `dialect/cortex.go`:

```go
type cortexTable struct {
	Name           string          `yaml:"name"`
	Description    string          `yaml:"description,omitempty"`
	Synonyms       []string        `yaml:"synonyms,omitempty"`
	BaseTable      cortexBaseTable `yaml:"base_table"`
	PrimaryKey     *cortexPK       `yaml:"primary_key,omitempty"`
	Dimensions     []cortexCol     `yaml:"dimensions,omitempty"`
	TimeDimensions []cortexCol     `yaml:"time_dimensions,omitempty"`
	Facts          []cortexCol     `yaml:"facts,omitempty"`
	Metrics        []cortexMetric  `yaml:"metrics,omitempty"`
}
```

And populate it at `dialect/cortex.go:125-129`:

```go
		ct := cortexTable{
			Name:        t.Name,
			Description: t.Description,
			Synonyms:    t.Synonyms,
			BaseTable:   cortexBaseTable{Database: c.Database, Schema: schema, Table: strings.ToUpper(t.Name)},
		}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./dialect/ -run 'TestSemanticViewTableSynonyms|TestCortexTableSynonyms' -v`
Expected: PASS

- [ ] **Step 7: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass. The ecommerce fixture declares no model-level synonyms yet, so no golden should change. If a golden does change, stop and investigate before regenerating it.

- [ ] **Step 8: Commit**

```bash
git add dialect/snowflake_semantic_view.go dialect/cortex.go dialect/snowflake_semantic_view_test.go dialect/cortex_test.go
git commit -m "feat(snowflake,cortex): emit table-level synonyms

Both formats have a table-level synonyms slot semglot was not filling.
svSynonyms already rendered the clause and documented that Snowflake
accepts it on tables; it was simply never called for one."
```

---

### Task 3: Prose table synonyms for the five slotless dialects

**Files:**
- Modify: `dialect/nao_yaml.go:101-129`, `dialect/nao_context_rules.go:87-92`, `dialect/lightdash.go:656`, `dialect/databricks_metric_view.go:374-383`, `dialect/supersimple.go:159-162`
- Modify: `dialect/README.md`
- Test: `dialect/nao_context_rules_test.go`, `dialect/supersimple_test.go`

**Interfaces:**
- Consumes: `ir.Table.Synonyms` (Task 1), `synonymClause` / `appendClause` (`dialect/enum.go:65,74`).
- Produces: nothing new.

- [ ] **Step 1: Write the failing tests**

Add to `dialect/supersimple_test.go`:

```go
// TestSupersimpleTableSynonyms folds table synonyms into the model description,
// since supersimple has no synonyms slot. This closes the gap dialect/README.md
// recorded under "Gaps vs. limits".
func TestSupersimpleTableSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:        "fct_orders",
		Description: "Orders.",
		Synonyms:    []string{"purchases", "sales"},
		Dimensions:  []ir.Field{{Name: "order_id", Expr: "order_id"}},
	}}}
	out := t.TempDir()
	if _, err := (supersimple{}).Emit(m, out); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, out)
	if !strings.Contains(got, "Synonyms: purchases, sales.") {
		t.Errorf("emitted supersimple missing folded synonyms in:\n%s", got)
	}
}
```

Add to `dialect/nao_context_rules_test.go`:

```go
// TestContextRulesTableSynonyms folds table synonyms into the Table reference
// entry, and emits the entry even when the table carries no description.
func TestContextRulesTableSynonyms(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:     "fct_orders",
		Synonyms: []string{"purchases", "sales"},
	}}}
	out := t.TempDir()
	if _, err := (naoContextRules{}).Emit(m, out); err != nil {
		t.Fatal(err)
	}
	got := readAll(t, filepath.Join(out, "RULES.md"))
	if !strings.Contains(got, "- **fct_orders**: Synonyms: purchases, sales.") {
		t.Errorf("RULES.md missing folded synonyms in:\n%s", got)
	}
}
```

Match the file-reading helper each test file already uses instead of `readAll`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run 'TestSupersimpleTableSynonyms|TestContextRulesTableSynonyms' -v`
Expected: FAIL — neither output contains the folded clause.

- [ ] **Step 3: Wire nao-context-rules**

At `dialect/nao_context_rules.go:87-92`, the Table reference section currently skips tables with no description. Fold synonyms in and emit when either is present:

```go
	var tables []string
	for _, t := range m.Tables {
		body := appendClause(strings.TrimSpace(t.Description), synonymClause(t.Synonyms))
		if body == "" {
			continue
		}
		tables = append(tables, fmt.Sprintf("- **%s**: %s", t.Name, body))
	}
```

- [ ] **Step 4: Wire nao-yaml**

nao-yaml has no per-table construct at all — its `dimensions`/`metrics` are model-global (documented as a limit in `dialect/README.md`). Fold table synonyms into the document's `notes:`, which is where nao-yaml already carries table-level prose. In `dialect/nao_yaml.go`, inside the `for _, t := range m.Tables` loop at line 101, before the dimension loops:

```go
		if c := synonymClause(t.Synonyms); c != "" {
			notes = append(notes, t.Name+": "+c)
		}
```

Do **not** append these to `own` — `own` is the returned warnings list for degraded metrics, and a folded synonym is not a degradation.

- [ ] **Step 5: Wire lightdash**

At `dialect/lightdash.go:656`, fold into the model description:

```go
		model := ldModel{
			Name:        t.Name,
			Description: appendClause(t.Description, synonymClause(t.Synonyms)),
			Columns:     cols.list(),
		}
```

- [ ] **Step 6: Wire databricks-metric-view**

At `dialect/databricks_metric_view.go:374-383`, the view comment is assembled from `parts`. Add the synonym clause to that assembly:

```go
	if t.Description != "" {
		parts = append(parts, t.Description)
	}
	if c := synonymClause(t.Synonyms); c != "" {
		parts = append(parts, c)
	}
```

Read the surrounding lines first — `parts` accumulates several pieces and the ordering matters for the golden. Place the synonym clause immediately after the description.

- [ ] **Step 7: Wire supersimple**

At `dialect/supersimple.go:159-162`, fold into the model description:

```go
			Description: appendClause(t.Description, synonymClause(t.Synonyms)),
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `go test ./dialect/ -run 'TestSupersimpleTableSynonyms|TestContextRulesTableSynonyms' -v`
Expected: PASS

- [ ] **Step 9: Add table synonyms to the ecommerce fixture and regenerate goldens**

Add a `meta.synonyms` block to one model in `test/models/ecommerce/dbt/marts/schema.yml` so every target's golden exercises the new path. Pick `fct_orders` and add, at model level:

```yaml
    meta:
      synonyms: [purchases, sales]
```

Then regenerate and review:

```bash
UPDATE_GOLDEN=1 go test ./...
git diff --stat
git diff
```

Expected changes: the dbt golden gains a `meta.synonyms` block; snowflake-semantic-view gains a `with synonyms (...)` clause on the `FCT_ORDERS` table line; cortex gains a `synonyms:` key (or a description suffix, if Task 2 Step 1 found tables unsupported); nao-yaml gains a `notes:` line; nao-context-rules, lightdash, databricks-metric-view, and supersimple gain a `Synonyms: purchases, sales.` description suffix. Any change that is **not** one of these means something else broke — investigate before continuing.

- [ ] **Step 10: Update `dialect/README.md`**

Two edits:

1. In the Mapping table, add a row directly below `Description`:

```
| Table synonyms | model `meta.synonyms` `<->` | `synonyms:` on the table | `with synonyms (...)` on the table | `text` (into the model description) | `text` (into `notes:`) | `text` (into the Table reference entry) | `text` (into the view `comment`) | `text` (into the model description) |
```

Adjust the `cortex` cell if Task 2 Step 1 found table-level synonyms unsupported there.

2. Under **Gaps vs. limits**, delete the `supersimple` synonyms bullet:

> - **`supersimple` synonyms.** A `synonymClause` helper exists but is not wired into the supersimple emitter (it would fold into a property description, as the nao dialects do).

and add it to the parenthetical that already records closed gaps, so the history stays visible:

> (`snowflake-semantic-view` synonyms, `nao-yaml` enum `values:`, and `supersimple` synonyms used to be gaps here; all three are emitted now.)

- [ ] **Step 11: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 12: Commit**

```bash
git add dialect/ test/models/
git commit -m "feat(dialects): fold table synonyms into descriptions where no slot exists

nao-yaml, nao-context-rules, lightdash, databricks-metric-view and
supersimple have no table-level synonyms slot, so they degrade to prose
via the existing synonymClause/appendClause helpers — the same way they
already degrade column synonyms. Closes the supersimple synonyms gap
dialect/README.md recorded."
```

---

### Task 4: Extract the shared SQL-expression parser

**Files:**
- Create: `dialect/sqlexpr.go`
- Modify: `dialect/dbt.go:684-784` (remove `parseDerivedExpr` and `derivedParser`)
- Test: existing `dialect/dbt_test.go` is the regression suite — no new tests.

**Interfaces:**
- Consumes: `sqlToken`, `sqlTokens`, `sqlIdent`, `sqlNumber`, `sqlOther` (`dialect/sqlex.go`).
- Produces: `exprParser` struct with fields `toks []sqlToken`, `pos int`, `err bool`, `leaf func(*exprParser) ir.Expr`, `calls bool`; methods `peek() (sqlToken, bool)`, `peekAt(int) (sqlToken, bool)`, `isOp(...string) (string, bool)`, `parseAddSub() ir.Expr`, `parseMulDiv() ir.Expr`, `parseFactor() ir.Expr`. Package-level `parseDerivedExpr(expr string) (ir.Expr, bool)` keeps its exact current signature and behaviour.

This task is a **pure move with no behaviour change**. The existing dbt tests are the proof.

- [ ] **Step 1: Confirm the baseline is green**

Run: `go test ./dialect/ -v -run 'TestDerived|TestDbt|TestParse'`
Expected: PASS. Note the count of passing tests; it must be identical at the end.

- [ ] **Step 2: Create `dialect/sqlexpr.go`**

```go
package dialect

import "github.com/benchouse/semglot/ir"

// A shared recursive-descent parser over sqlTokens, used by every dialect that
// reads an expression string into an ir.Expr tree.
//
// The grammar (parens, then * /, then + -) is common to all callers. What
// differs is the LEAF rule: a dbt derived expression names other METRICS, while
// an ossie metric expression names COLUMNS and may wrap them in aggregate
// calls. Callers supply their own leaf rule and opt into call parsing, rather
// than sharing one permissive grammar — see parseDerivedExpr's comment for why
// that separation is load-bearing.

// exprParser is a minimal recursive-descent parser over sqlTokens.
type exprParser struct {
	toks []sqlToken
	pos  int
	err  bool
	// leaf builds the Expr for an identifier token. It is responsible for
	// advancing pos past everything it consumes.
	leaf func(p *exprParser) ir.Expr
	// calls enables SUM(...)/COUNT(...) parsing. Off for dbt: enabling it there
	// would silently reinterpret an expression that currently degrades to a note.
	calls bool
}

// tokenize splits expr into tokens with whitespace dropped.
func tokenize(expr string) []sqlToken {
	var toks []sqlToken
	for _, tk := range sqlTokens(expr) {
		if tk.typ == sqlOther && strings.TrimSpace(tk.val) == "" {
			continue
		}
		toks = append(toks, tk)
	}
	return toks
}

func (p *exprParser) peek() (sqlToken, bool) { return p.peekAt(0) }

func (p *exprParser) peekAt(n int) (sqlToken, bool) {
	if p.pos+n < len(p.toks) {
		return p.toks[p.pos+n], true
	}
	return sqlToken{}, false
}

func (p *exprParser) isOp(want ...string) (string, bool) {
	tk, ok := p.peek()
	if !ok || tk.typ != sqlOther {
		return "", false
	}
	for _, w := range want {
		if tk.val == w {
			return w, true
		}
	}
	return "", false
}

func (p *exprParser) parseAddSub() ir.Expr {
	left := p.parseMulDiv()
	for {
		op, ok := p.isOp("+", "-")
		if !ok {
			return left
		}
		p.pos++
		left = ir.Binary{Op: op, Left: left, Right: p.parseMulDiv()}
	}
}

func (p *exprParser) parseMulDiv() ir.Expr {
	left := p.parseFactor()
	for {
		op, ok := p.isOp("*", "/")
		if !ok {
			return left
		}
		p.pos++
		left = ir.Binary{Op: op, Left: left, Right: p.parseFactor()}
	}
}

func (p *exprParser) parseFactor() ir.Expr {
	tk, ok := p.peek()
	if !ok {
		p.err = true
		return nil
	}
	switch {
	case tk.typ == sqlOther && tk.val == "(":
		p.pos++
		e := p.parseAddSub()
		if _, ok := p.isOp(")"); !ok {
			p.err = true
			return nil
		}
		p.pos++
		return e
	case tk.typ == sqlIdent:
		return p.leaf(p)
	case tk.typ == sqlNumber:
		p.pos++
		return ir.Lit{Value: tk.val}
	default:
		p.err = true
		return nil
	}
}

// parseDerivedExpr parses a dbt derived-metric expression (arithmetic over
// metric names and numeric literals: + - * / with precedence and parens) into an
// ir.Binary/Ref/Lit tree. ok=false if the expression is not cleanly parseable as
// such (the caller then degrades it to a note).
//
// Its leaf rule maps a bare identifier to ir.Ref and it does NOT enable call
// parsing. Keeping that separate from ossie's leaf rule is deliberate: if this
// parser started accepting SUM(...), a dbt derived expression that today
// degrades to a note would instead parse into something else — a silent
// behaviour change in a shipped dialect.
func parseDerivedExpr(expr string) (ir.Expr, bool) {
	toks := tokenize(expr)
	if len(toks) == 0 {
		return nil, false
	}
	p := &exprParser{toks: toks, leaf: derivedLeaf}
	e := p.parseAddSub()
	if p.err || p.pos != len(p.toks) {
		return nil, false
	}
	return e, true
}

// derivedLeaf maps a bare identifier to a metric reference.
func derivedLeaf(p *exprParser) ir.Expr {
	tk, _ := p.peek()
	p.pos++
	return ir.Ref{Metric: tk.val}
}
```

Add `"strings"` to the import block (`tokenize` uses it).

- [ ] **Step 3: Delete the originals from `dbt.go`**

Remove `parseDerivedExpr`, `derivedParser`, and its methods `peek`, `isOp`, `parseAddSub`, `parseMulDiv`, `parseFactor` — `dialect/dbt.go:684-784`. Leave `collectRefs` (line 787) in place; it is dbt-specific.

- [ ] **Step 4: Build and run the full suite**

Run: `go build ./... && go test ./... && go vet ./... && gofmt -l .`
Expected: all pass, with the same test count as Step 1. A behaviour change here means the extraction was not faithful — revert and redo rather than adjusting a test.

- [ ] **Step 5: Commit**

```bash
git add dialect/sqlexpr.go dialect/dbt.go
git commit -m "refactor(dialect): extract the SQL-expression parser to sqlexpr.go

Pure move, no behaviour change: derivedParser becomes exprParser with a
pluggable leaf rule, so ossie can reuse the grammar without dbt
inheriting a permissive one. The existing dbt tests are the proof."
```

---

### Task 5: `parseAggExpr` — aggregate calls and qualified columns

**Files:**
- Modify: `dialect/sqlexpr.go`
- Test: Create `dialect/sqlexpr_test.go`

**Interfaces:**
- Consumes: `exprParser` (Task 4).
- Produces: `parseAggExpr(expr string) (ir.Expr, bool)`. Task 7 calls it.

- [ ] **Step 1: Write the failing tests**

Create `dialect/sqlexpr_test.go`:

```go
package dialect

import (
	"reflect"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestParseAggExpr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ir.Expr
	}{
		{
			name: "qualified sum",
			in:   "SUM(orders.amount)",
			want: ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "amount"}},
		},
		{
			name: "unqualified sum",
			in:   "SUM(o_totalprice)",
			want: ir.Agg{Func: "sum", Arg: ir.Col{Name: "o_totalprice"}},
		},
		{
			name: "count distinct",
			in:   "COUNT(DISTINCT customers.id)",
			want: ir.Agg{Func: "count_distinct", Arg: ir.Col{Table: "customers", Name: "id"}},
		},
		{
			name: "count star",
			in:   "COUNT(*)",
			want: ir.Agg{Func: "count", Arg: nil},
		},
		{
			name: "cross-dataset ratio",
			in:   "SUM(orders.amount) / COUNT(DISTINCT customers.id)",
			want: ir.Binary{
				Op:    "/",
				Left:  ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "amount"}},
				Right: ir.Agg{Func: "count_distinct", Arg: ir.Col{Table: "customers", Name: "id"}},
			},
		},
		{
			name: "nested arithmetic keeps precedence",
			in:   "SUM(orders.amount) - SUM(orders.cost_amount)",
			want: ir.Binary{
				Op:    "-",
				Left:  ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "amount"}},
				Right: ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "cost_amount"}},
			},
		},
		{
			name: "opaque call argument becomes Raw",
			in:   "SUM(CASE WHEN orders.order_id IS NOT NULL THEN 1 ELSE 0 END)",
			want: ir.Agg{Func: "sum", Arg: ir.Raw{SQL: "CASE WHEN orders.order_id IS NOT NULL THEN 1 ELSE 0 END"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAggExpr(tc.in)
			if !ok {
				t.Fatalf("parseAggExpr(%q) returned ok=false", tc.in)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAggExpr(%q)\n got: %#v\nwant: %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseAggExprRejects(t *testing.T) {
	for _, in := range []string{"", "SUM(", "SUM(orders.amount", ")", "+ 1"} {
		if _, ok := parseAggExpr(in); ok {
			t.Errorf("parseAggExpr(%q) = ok, want failure", in)
		}
	}
}

// TestParseDerivedExprStillRejectsCalls guards the deliberate separation between
// the two leaf rules: enabling calls for ossie must not make dbt's derived
// parser accept them.
func TestParseDerivedExprStillRejectsCalls(t *testing.T) {
	if _, ok := parseDerivedExpr("SUM(revenue)"); ok {
		t.Error("parseDerivedExpr accepted a function call; dbt's grammar must stay call-free")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run 'TestParseAggExpr|TestParseDerivedExprStillRejectsCalls' -v`
Expected: FAIL — `parseAggExpr` undefined.

- [ ] **Step 3: Implement `parseAggExpr`**

Append to `dialect/sqlexpr.go`:

```go
// aggFuncs maps a recognised SQL aggregate name (lowercased) to the ir.Agg.Func
// value semglot uses. COUNT(DISTINCT x) is special-cased to count_distinct in
// parseCall.
var aggFuncs = map[string]string{
	"sum": "sum", "count": "count", "avg": "avg",
	"min": "min", "max": "max", "median": "median",
}

// parseAggExpr parses an OSI metric expression — aggregate calls over columns,
// combined with + - * / and parens — into an ir.Agg/Binary/Col/Lit tree.
// ok=false when the expression is not cleanly parseable, so the caller degrades
// it to a note rather than guessing.
//
// An aggregate's argument that is not a plain column becomes an ir.Raw carrying
// the verbatim source text (e.g. a CASE expression, which Ossie's own dbt
// converter emits for a filtered measure). Raw.Columns is left nil here; the
// caller fills it once it knows which table owns the metric.
func parseAggExpr(expr string) (ir.Expr, bool) {
	toks := tokenize(expr)
	if len(toks) == 0 {
		return nil, false
	}
	p := &exprParser{toks: toks, leaf: aggLeaf, calls: true}
	e := p.parseAddSub()
	if p.err || p.pos != len(p.toks) {
		return nil, false
	}
	return e, true
}

// aggLeaf maps an identifier to a column reference, consuming an optional
// `.column` qualifier. When calls are enabled and the identifier is followed by
// `(`, it is an aggregate call instead.
func aggLeaf(p *exprParser) ir.Expr {
	tk, _ := p.peek()
	if next, ok := p.peekAt(1); p.calls && ok && next.typ == sqlOther && next.val == "(" {
		return p.parseCall()
	}
	p.pos++
	// table.column
	if dot, ok := p.peek(); ok && dot.typ == sqlOther && dot.val == "." {
		if col, ok := p.peekAt(1); ok && col.typ == sqlIdent {
			p.pos += 2
			return ir.Col{Table: tk.val, Name: col.val}
		}
	}
	return ir.Col{Name: tk.val}
}

// parseCall parses `FUNC(...)` at the current position. Only known aggregates
// are accepted; anything else fails the parse so the caller degrades it.
func (p *exprParser) parseCall() ir.Expr {
	name, _ := p.peek()
	fn, known := aggFuncs[strings.ToLower(name.val)]
	if !known {
		p.err = true
		return nil
	}
	open := p.pos + 1
	close, ok := p.matchParen(open)
	if !ok {
		p.err = true
		return nil
	}
	inner := p.toks[open+1 : close]
	p.pos = close + 1

	// COUNT(*)
	if len(inner) == 1 && inner[0].typ == sqlOther && inner[0].val == "*" {
		return ir.Agg{Func: fn, Arg: nil}
	}
	// COUNT(DISTINCT x)
	if len(inner) > 1 && inner[0].typ == sqlIdent && strings.EqualFold(inner[0].val, "distinct") {
		if fn == "count" {
			fn = "count_distinct"
		}
		inner = inner[1:]
	}
	if arg, ok := parseColTokens(inner); ok {
		return ir.Agg{Func: fn, Arg: arg}
	}
	return ir.Agg{Func: fn, Arg: ir.Raw{SQL: joinTokens(inner)}}
}

// matchParen returns the index of the `)` closing the `(` at open, honouring
// nesting. ok=false when unbalanced.
func (p *exprParser) matchParen(open int) (int, bool) {
	if open >= len(p.toks) || p.toks[open].typ != sqlOther || p.toks[open].val != "(" {
		return 0, false
	}
	depth := 0
	for i := open; i < len(p.toks); i++ {
		tk := p.toks[i]
		if tk.typ != sqlOther {
			continue
		}
		switch tk.val {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// parseColTokens reads `column` or `table.column` from a complete token slice.
// ok=false if the slice is anything else, so the caller falls back to ir.Raw.
func parseColTokens(toks []sqlToken) (ir.Expr, bool) {
	switch len(toks) {
	case 1:
		if toks[0].typ == sqlIdent {
			return ir.Col{Name: toks[0].val}, true
		}
	case 3:
		if toks[0].typ == sqlIdent && toks[1].typ == sqlOther && toks[1].val == "." && toks[2].typ == sqlIdent {
			return ir.Col{Table: toks[0].val, Name: toks[2].val}, true
		}
	}
	return nil, false
}

// joinTokens reproduces the source text of a token span, separating identifiers
// and numbers with single spaces so a reconstructed SQL fragment stays readable
// and valid.
func joinTokens(toks []sqlToken) string {
	var sb strings.Builder
	for i, tk := range toks {
		if i > 0 && needsSpace(toks[i-1], tk) {
			sb.WriteByte(' ')
		}
		sb.WriteString(tk.val)
	}
	return sb.String()
}

// needsSpace reports whether two adjacent tokens must be separated to stay
// valid SQL: two word-ish tokens always, and never around punctuation.
func needsSpace(prev, cur sqlToken) bool {
	wordish := func(t sqlToken) bool {
		return t.typ == sqlIdent || t.typ == sqlNumber || t.typ == sqlString
	}
	if wordish(prev) && wordish(cur) {
		return true
	}
	// keep `x = 1` and `a > b` readable
	if prev.typ == sqlOther && strings.TrimSpace(prev.val) != "" && wordish(cur) && prev.val != "." && prev.val != "(" {
		return true
	}
	if wordish(prev) && cur.typ == sqlOther && cur.val != "." && cur.val != ")" && cur.val != "," {
		return true
	}
	return false
}
```

`close` shadows the builtin; rename it to `closeIdx` if `go vet` objects.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./dialect/ -run 'TestParseAggExpr|TestParseDerivedExprStillRejectsCalls' -v`
Expected: PASS. The `opaque call argument becomes Raw` case is the one most likely to need `joinTokens` spacing tweaks — adjust `needsSpace` until the reconstructed SQL matches, and prefer fixing the joiner over relaxing the test.

- [ ] **Step 5: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add dialect/sqlexpr.go dialect/sqlexpr_test.go
git commit -m "feat(dialect): parse SQL aggregate expressions into the IR

parseAggExpr reads OSI metric expressions - aggregate calls over
columns, combined arithmetically - into ir.Agg/Binary/Col trees. An
aggregate argument that is not a plain column becomes ir.Raw, which is
how Ossie's own converter renders a filtered measure."
```

---

### Task 6: ossie skeleton and dataset/field parsing

**Files:**
- Create: `dialect/ossie.go`
- Test: Create `dialect/ossie_test.go`

**Interfaces:**
- Consumes: `Register` (`dialect.go:58`), `ir.Model`, `ir.Table`, `ir.Field`.
- Produces: type `ossie struct{ Database, Schema, ModelName, Description string }` with `Name() string` returning `"ossie"` and `Parse(sources ...string) (*ir.Model, error)`. Named OSI YAML types `osiFile`, `osiModel`, `osiDataset`, `osiField`, `osiRelationship`, `osiMetric`, `osiExpression`, `osiDialectExpr`, `osiDimension`, `osiAIContext`. Helpers `osiToIRType(string) string`, `irToOSIType(string) string`, `pickExpression(osiExpression) (string, bool, string)`.

- [ ] **Step 1: Write the failing test**

Create `dialect/ossie_test.go`:

```go
package dialect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeOSI writes an OSI document into a temp dir and returns the dir.
func writeOSI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOssieRegistered(t *testing.T) {
	if _, err := AsParser("ossie"); err != nil {
		t.Errorf("AsParser(ossie): %v", err)
	}
	if _, err := AsEmitter("ossie"); err != nil {
		t.Errorf("AsEmitter(ossie): %v", err)
	}
}

func TestOssieParseDatasetsAndFields(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    description: Sales model.
    datasets:
      - name: orders
        source: sales.public.orders
        primary_key: [order_id, line_number]
        description: Customer orders.
        ai_context:
          synonyms: [purchases, sales]
        fields:
          - name: order_id
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_id
            datatype: Integer
            description: Order identifier.
          - name: order_date
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_date
            datatype: Date
            description: Order date.
          - name: created_at
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: created_at
            datatype: DateTime
            dimension:
              is_time: false
          - name: status
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: status
            datatype: String
            ai_context:
              synonyms: [state]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(m.Tables))
	}
	tbl := m.Tables[0]
	if tbl.Name != "orders" {
		t.Errorf("Name = %q, want orders", tbl.Name)
	}
	if tbl.Description != "Customer orders." {
		t.Errorf("Description = %q", tbl.Description)
	}
	if !reflect.DeepEqual(tbl.Synonyms, []string{"purchases", "sales"}) {
		t.Errorf("Synonyms = %v", tbl.Synonyms)
	}
	if !reflect.DeepEqual(tbl.PrimaryKey, []string{"order_id", "line_number"}) {
		t.Errorf("PrimaryKey = %v", tbl.PrimaryKey)
	}
	// order_date defaults to a time dimension via its Date datatype;
	// created_at opts out with an explicit is_time: false.
	var timeNames, dimNames []string
	for _, d := range tbl.TimeDimensions {
		timeNames = append(timeNames, d.Name)
	}
	for _, d := range tbl.Dimensions {
		dimNames = append(dimNames, d.Name)
	}
	if !reflect.DeepEqual(timeNames, []string{"order_date"}) {
		t.Errorf("TimeDimensions = %v, want [order_date]", timeNames)
	}
	if !reflect.DeepEqual(dimNames, []string{"order_id", "created_at", "status"}) {
		t.Errorf("Dimensions = %v", dimNames)
	}
	for _, d := range tbl.Dimensions {
		if d.Name == "order_id" && d.DataType != "integer" {
			t.Errorf("order_id DataType = %q, want integer", d.DataType)
		}
		if d.Name == "status" && !reflect.DeepEqual(d.Synonyms, []string{"state"}) {
			t.Errorf("status Synonyms = %v", d.Synonyms)
		}
	}
}

// TestOssieParseSkipsNonOSI ignores YAML files with no semantic_model key, so a
// mixed directory works.
func TestOssieParseSkipsNonOSI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.yml"), []byte("models:\n  - name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Tables) != 0 {
		t.Errorf("want 0 tables from a non-OSI file, got %d", len(m.Tables))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestOssie -v`
Expected: FAIL — `ossie` undefined.

- [ ] **Step 3: Create `dialect/ossie.go`**

```go
// Package-level note: ossie reads and writes the Apache Ossie Core Metadata
// Specification (github.com/apache/ossie, core-spec 0.2.0.dev0) — the
// semantic_model layer, not the separate ontology layer. The spec is a draft:
// "schema may change before 0.2.0 is released".
package dialect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(ossie{}) }

// osiVersion is the spec version emitted and expected. Pinned, never computed.
const osiVersion = "0.2.0.dev0"

// ossie reads and emits Apache Ossie semantic models. Zero value is usable;
// the build command sets identity from the profile. Emit does not mutate m.
type ossie struct{ Database, Schema, ModelName, Description string }

func (ossie) Name() string { return "ossie" }

// ---- OSI YAML shapes ----

type osiFile struct {
	Version       string     `yaml:"version"`
	SemanticModel []osiModel `yaml:"semantic_model"`
}

type osiModel struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description,omitempty"`
	AIContext     *osiAIContext     `yaml:"ai_context,omitempty"`
	Datasets      []osiDataset      `yaml:"datasets"`
	Relationships []osiRelationship `yaml:"relationships,omitempty"`
	Metrics       []osiMetric       `yaml:"metrics,omitempty"`
}

type osiDataset struct {
	Name        string        `yaml:"name"`
	Source      string        `yaml:"source"`
	PrimaryKey  []string      `yaml:"primary_key,omitempty"`
	UniqueKeys  [][]string    `yaml:"unique_keys,omitempty"`
	Description string        `yaml:"description,omitempty"`
	AIContext   *osiAIContext `yaml:"ai_context,omitempty"`
	Fields      []osiField    `yaml:"fields,omitempty"`
}

type osiField struct {
	Name        string        `yaml:"name"`
	Expression  osiExpression `yaml:"expression"`
	Dimension   *osiDimension `yaml:"dimension,omitempty"`
	Label       string        `yaml:"label,omitempty"`
	Description string        `yaml:"description,omitempty"`
	DataType    string        `yaml:"datatype,omitempty"`
	AIContext   *osiAIContext `yaml:"ai_context,omitempty"`
}

type osiDimension struct {
	IsTime *bool `yaml:"is_time,omitempty"`
}

type osiRelationship struct {
	Name        string   `yaml:"name"`
	From        string   `yaml:"from"`
	To          string   `yaml:"to"`
	FromColumns []string `yaml:"from_columns"`
	ToColumns   []string `yaml:"to_columns"`
}

type osiMetric struct {
	Name        string        `yaml:"name"`
	Expression  osiExpression `yaml:"expression"`
	Description string        `yaml:"description,omitempty"`
	DataType    string        `yaml:"datatype,omitempty"`
	AIContext   *osiAIContext `yaml:"ai_context,omitempty"`
}

type osiExpression struct {
	Dialects []osiDialectExpr `yaml:"dialects"`
}

type osiDialectExpr struct {
	Dialect    string `yaml:"dialect"`
	Expression string `yaml:"expression"`
}

// osiAIContext is `string | object` in the schema. A bare string is treated as
// instructions.
type osiAIContext struct {
	Instructions string   `yaml:"instructions,omitempty"`
	Synonyms     []string `yaml:"synonyms,omitempty"`
	Examples     []string `yaml:"examples,omitempty"`
}

func (a *osiAIContext) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		a.Instructions = s
		return nil
	}
	type plain osiAIContext // avoid recursing into this method
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*a = osiAIContext(p)
	return nil
}

func (a *osiAIContext) synonyms() []string {
	if a == nil {
		return nil
	}
	return a.Synonyms
}

// ---- data types ----

// osiTypes maps OSI's logical DataType enum to the neutral SQL type string the
// IR carries (dialects like dbt record raw SQL types, and emitters such as
// cortex pass them straight through). Integer and Decimal map to distinct SQL
// names rather than collapsing to `number`, so the round-trip is stable.
var osiTypes = map[string]string{
	"String":     "varchar",
	"Integer":    "integer",
	"Decimal":    "decimal",
	"Float":      "float",
	"Boolean":    "boolean",
	"Date":       "date",
	"Time":       "time",
	"DateTime":   "timestamp",
	"DateTimeTz": "timestamp_tz",
}

// irTypes is the reverse map, extended with the SQL spellings other dialects
// emit for the same logical type.
var irTypes = map[string]string{
	"varchar": "String", "char": "String", "text": "String", "string": "String",
	"integer": "Integer", "int": "Integer", "bigint": "Integer", "smallint": "Integer",
	"decimal": "Decimal", "numeric": "Decimal",
	"float": "Float", "double": "Float", "real": "Float",
	"boolean": "Boolean", "bool": "Boolean",
	"date": "Date",
	"time": "Time",
	"timestamp": "DateTime", "datetime": "DateTime", "timestamp_ntz": "DateTime",
	"timestamp_tz": "DateTimeTz", "timestamptz": "DateTimeTz", "timestamp_ltz": "DateTimeTz",
}

// osiToIRType converts an OSI datatype to a SQL type string, or "" when the
// value is unknown or absent (including the deliberate Opaque case).
func osiToIRType(t string) string { return osiTypes[t] }

// irToOSIType converts a SQL type string to an OSI DataType, or "" when it is
// not in the portable vocabulary. The spec directs omitting datatype when
// unknown, so Opaque is never used as a catch-all.
func irToOSIType(t string) string {
	return irTypes[strings.ToLower(strings.TrimSpace(t))]
}

// osiTemporal reports whether an OSI datatype makes is_time default to true.
func osiTemporal(t string) bool {
	switch t {
	case "Date", "Time", "DateTime", "DateTimeTz":
		return true
	}
	return false
}

// isTime resolves a field's temporal role per spec: an explicit dimension.is_time
// always wins; when unset it defaults to true for a temporal datatype.
func (f osiField) isTime() bool {
	if f.Dimension != nil && f.Dimension.IsTime != nil {
		return *f.Dimension.IsTime
	}
	return osiTemporal(f.DataType)
}

// pickExpression selects the expression to read. ANSI_SQL wins when present.
// Otherwise a lone entry is used silently — Ossie's own Databricks fixtures are
// DATABRICKS-only, and noting every field there would be pure noise — while a
// choice among several non-ANSI dialects uses the first and returns a note.
func pickExpression(e osiExpression) (expr string, ok bool, note string) {
	if len(e.Dialects) == 0 {
		return "", false, ""
	}
	for _, d := range e.Dialects {
		if strings.EqualFold(d.Dialect, "ANSI_SQL") {
			return d.Expression, true, ""
		}
	}
	first := e.Dialects[0]
	if len(e.Dialects) == 1 {
		return first.Expression, true, ""
	}
	return first.Expression, true, fmt.Sprintf("no ANSI_SQL expression; read the %s dialect", first.Dialect)
}

// ---- Parse ----

// Parse reads *.yml and *.yaml from each source directory (non-recursive) and
// merges every semantic_model entry into one IR model. Files with no
// semantic_model key are skipped, so a mixed directory works.
func (o ossie) Parse(sources ...string) (*ir.Model, error) {
	out := &ir.Model{}
	for _, dir := range sources {
		var paths []string
		for _, pat := range []string{"*.yml", "*.yaml"} {
			matches, err := filepath.Glob(filepath.Join(dir, pat))
			if err != nil {
				return nil, err
			}
			paths = append(paths, matches...)
		}
		sort.Strings(paths) // deterministic merge order
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			var f osiFile
			if err := yaml.Unmarshal(b, &f); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if len(f.SemanticModel) == 0 {
				continue // not an OSI document
			}
			for _, sm := range f.SemanticModel {
				o.mergeModel(out, sm)
			}
		}
	}
	return out, nil
}

// mergeModel folds one OSI semantic_model entry into out.
func (o ossie) mergeModel(out *ir.Model, sm osiModel) {
	if sm.AIContext != nil && sm.AIContext.Instructions != "" {
		out.Notes = append(out.Notes, sm.AIContext.Instructions)
	}
	for _, ds := range sm.Datasets {
		t := ir.Table{
			Name:        ds.Name,
			Description: ds.Description,
			Synonyms:    ds.AIContext.synonyms(),
			PrimaryKey:  ds.PrimaryKey,
		}
		for _, f := range ds.Fields {
			expr, ok, note := pickExpression(f.Expression)
			if !ok {
				out.Notes = append(out.Notes,
					fmt.Sprintf("field %q on dataset %q has no expression; skipped", f.Name, ds.Name))
				continue
			}
			if note != "" {
				out.Notes = append(out.Notes, fmt.Sprintf("field %q on dataset %q: %s", f.Name, ds.Name, note))
			}
			fld := ir.Field{
				Name:        f.Name,
				Description: f.Description,
				DataType:    osiToIRType(f.DataType),
				Expr:        expr,
				Synonyms:    f.AIContext.synonyms(),
			}
			if f.isTime() {
				t.TimeDimensions = append(t.TimeDimensions, fld)
			} else {
				t.Dimensions = append(t.Dimensions, fld)
			}
		}
		out.Tables = append(out.Tables, t)
	}
}
```

- [ ] **Step 4: Add a stub `Emit` so the type satisfies `Emitter`**

`TestOssieRegistered` asserts `AsEmitter("ossie")` succeeds. Create `dialect/ossie_emit.go` with a stub that Task 8 replaces:

```go
package dialect

import "github.com/benchouse/semglot/ir"

// WithOptions returns an ossie emitter carrying the model identity and the
// database/schema used to qualify each dataset's `source`.
func (ossie) WithOptions(o Options) Emitter {
	return ossie{Database: o.Database, Schema: o.Schema, ModelName: o.Name, Description: o.Description}
}

func (o ossie) Emit(m *ir.Model, dir string) ([]string, error) {
	panic("ossie.Emit: implemented in a later task")
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./dialect/ -run TestOssie -v`
Expected: PASS

- [ ] **Step 6: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass. `TestRegistryCapabilities` and any test enumerating `Names()` may now see `ossie`; update expected lists if they are exhaustive.

- [ ] **Step 7: Commit**

```bash
git add dialect/ossie.go dialect/ossie_emit.go dialect/ossie_test.go
git commit -m "feat(ossie): parse OSI datasets and fields

Reads the Apache Ossie core-spec semantic_model layer into the IR:
datasets become tables, fields become dimensions or time dimensions per
the spec's is_time resolution rule, and ai_context.synonyms round-trips
structurally at both table and field level."
```

---

### Task 7: ossie relationships, metrics, and measure synthesis

**Files:**
- Modify: `dialect/ossie.go`
- Test: `dialect/ossie_test.go`

**Interfaces:**
- Consumes: `parseAggExpr` (Task 5), `mergeModel` (Task 6).
- Produces: helpers `metricHome(ir.Expr, map[string]string) (string, bool)` and `fillRawColumns(ir.Expr, string, []string) ir.Expr` on the ossie type or as package functions.

- [ ] **Step 1: Write the failing test**

Add to `dialect/ossie_test.go`:

```go
func TestOssieParseRelationshipsAndMetrics(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: sales.public.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
          - name: customer_id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: customer_id}]
      - name: customers
        source: sales.public.customers
        fields:
          - name: id
            expression:
              dialects: [{dialect: ANSI_SQL, expression: id}]
    relationships:
      - name: orders_to_customers
        from: orders
        to: customers
        from_columns: [customer_id]
        to_columns: [id]
    metrics:
      - name: total_revenue
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount)"}]
        description: Total revenue.
        ai_context:
          synonyms: [revenue]
      - name: arpu
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "SUM(orders.amount) / COUNT(DISTINCT customers.id)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}

	wantRel := ir.Relationship{
		Left: "orders", Right: "customers",
		Columns: []ir.ColumnPair{{Left: "customer_id", Right: "id"}},
	}
	if len(m.Relationships) != 1 || !reflect.DeepEqual(m.Relationships[0], wantRel) {
		t.Errorf("Relationships = %#v, want %#v", m.Relationships, wantRel)
	}

	orders := tableByName(t, m, "orders")

	// A plain aggregation over one column yields BOTH a measure and a metric,
	// sharing the OSI metric's name - the shape dbt.Parse produces for a
	// type: simple metric, so every emitter behaves the same whatever the source.
	if len(orders.Measures) != 1 {
		t.Fatalf("want 1 measure on orders, got %d", len(orders.Measures))
	}
	ms := orders.Measures[0]
	if ms.Name != "total_revenue" || ms.Agg != "sum" || ms.Expr != "amount" {
		t.Errorf("measure = %+v, want {total_revenue sum amount}", ms)
	}
	if !reflect.DeepEqual(ms.Synonyms, []string{"revenue"}) {
		t.Errorf("measure Synonyms = %v", ms.Synonyms)
	}

	// Both metrics home on orders: total_revenue directly, arpu on its first
	// referenced dataset.
	var names []string
	for _, mt := range orders.Metrics {
		names = append(names, mt.Name)
	}
	if !reflect.DeepEqual(names, []string{"total_revenue", "arpu"}) {
		t.Errorf("orders.Metrics = %v, want [total_revenue arpu]", names)
	}
	wantDef := ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}
	if !reflect.DeepEqual(orders.Metrics[0].Def, wantDef) {
		t.Errorf("total_revenue Def = %#v, want %#v", orders.Metrics[0].Def, wantDef)
	}
}

// TestOssieParseUnparseableMetric notes a metric it cannot parse rather than
// guessing an AST for it.
func TestOssieParseUnparseableMetric(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    datasets:
      - name: orders
        source: s.p.orders
        fields:
          - name: amount
            expression:
              dialects: [{dialect: ANSI_SQL, expression: amount}]
    metrics:
      - name: weird
        expression:
          dialects: [{dialect: ANSI_SQL, expression: "NTILE(4) OVER (ORDER BY amount)"}]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	orders := tableByName(t, m, "orders")
	if len(orders.Metrics) != 0 {
		t.Errorf("want the unparseable metric skipped, got %d", len(orders.Metrics))
	}
	var found bool
	for _, n := range m.Notes {
		if strings.Contains(n, "weird") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a note naming the skipped metric, got %v", m.Notes)
	}
}

// tableByName fetches a table by name or fails the test.
func tableByName(t *testing.T, m *ir.Model, name string) ir.Table {
	t.Helper()
	for _, tb := range m.Tables {
		if tb.Name == name {
			return tb
		}
	}
	t.Fatalf("no table %q in %v", name, m.Tables)
	return ir.Table{}
}
```

Add `"strings"` and the `ir` import to `dialect/ossie_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run 'TestOssieParseRelationships|TestOssieParseUnparseable' -v`
Expected: FAIL — no relationships or metrics are produced.

- [ ] **Step 3: Implement relationships**

In `mergeModel`, after the dataset loop:

```go
	for _, r := range sm.Relationships {
		if len(r.FromColumns) != len(r.ToColumns) {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"relationship %q: from_columns (%d) and to_columns (%d) differ in length; skipped",
				r.Name, len(r.FromColumns), len(r.ToColumns)))
			continue
		}
		if len(r.FromColumns) == 0 {
			continue
		}
		rel := ir.Relationship{Left: r.From, Right: r.To}
		for i := range r.FromColumns {
			rel.Columns = append(rel.Columns, ir.ColumnPair{Left: r.FromColumns[i], Right: r.ToColumns[i]})
		}
		out.Relationships = append(out.Relationships, rel)
	}
```

`from` is the many side and `to` the one side, which is exactly the IR's `Left`/`Right` convention (`dbt.go:489`).

- [ ] **Step 4: Implement metric parsing**

Add to `dialect/ossie.go`:

```go
// colOwner indexes which dataset declares a given column, so an unqualified
// aggregate (Ossie's Databricks fixtures write `SUM(o_totalprice)`) can still
// be homed. A column declared by more than one dataset maps to "" — ambiguous,
// and the metric is noted rather than guessed at.
func colOwner(sm osiModel) map[string]string {
	owner := map[string]string{}
	for _, ds := range sm.Datasets {
		for _, f := range ds.Fields {
			key := strings.ToLower(f.Name)
			if prev, seen := owner[key]; seen && prev != ds.Name {
				owner[key] = ""
				continue
			}
			owner[key] = ds.Name
		}
	}
	return owner
}

// metricHome returns the table a parsed metric belongs to: the first qualified
// column's table, else the sole dataset declaring the first unqualified column.
// Mirrors dbt.go's rule that a cross-table metric homes on its first resolvable
// operand's table.
func metricHome(e ir.Expr, owner map[string]string) (string, bool) {
	switch n := e.(type) {
	case ir.Col:
		if n.Table != "" {
			return n.Table, true
		}
		if t := owner[strings.ToLower(n.Name)]; t != "" {
			return t, true
		}
		return "", false
	case ir.Agg:
		if n.Arg == nil {
			return "", false // COUNT(*) names no column
		}
		return metricHome(n.Arg, owner)
	case ir.Raw:
		for _, tk := range tokenize(n.SQL) {
			if tk.typ != sqlIdent {
				continue
			}
			if t := owner[strings.ToLower(tk.val)]; t != "" {
				return t, true
			}
		}
		return "", false
	case ir.Binary:
		if t, ok := metricHome(n.Left, owner); ok {
			return t, true
		}
		return metricHome(n.Right, owner)
	}
	return "", false
}

// setAggTable stamps the owning table onto every Agg node and fills a Raw arg's
// Columns, which renderSQL needs to qualify the fragment at emit time.
func setAggTable(e ir.Expr, table string, cols []string) ir.Expr {
	switch n := e.(type) {
	case ir.Agg:
		n.Table = table
		if raw, ok := n.Arg.(ir.Raw); ok {
			raw.Columns = cols
			n.Arg = raw
		} else if n.Arg != nil {
			n.Arg = setAggTable(n.Arg, table, cols)
		}
		return n
	case ir.Binary:
		n.Left = setAggTable(n.Left, table, cols)
		n.Right = setAggTable(n.Right, table, cols)
		return n
	}
	return e
}

// simpleAgg reports the measure a plain aggregation over one column implies:
// its aggregate and the column expression. ok=false for anything compound,
// filtered, or COUNT(*), none of which is a column-backed measure.
func simpleAgg(e ir.Expr) (agg, col string, ok bool) {
	a, isAgg := e.(ir.Agg)
	if !isAgg || a.Filter != nil {
		return "", "", false
	}
	c, isCol := a.Arg.(ir.Col)
	if !isCol {
		return "", "", false
	}
	return a.Func, c.Name, true
}
```

Then in `mergeModel`, after relationships, add the metric loop. It needs the table index built from the datasets just appended:

```go
	owner := colOwner(sm)
	colsByTable := map[string][]string{}
	for _, ds := range sm.Datasets {
		cols := make([]string, 0, len(ds.Fields))
		for _, f := range ds.Fields {
			cols = append(cols, strings.ToLower(f.Name))
		}
		sort.Strings(cols) // deterministic Raw.Columns
		colsByTable[ds.Name] = cols
	}
	for _, mt := range sm.Metrics {
		expr, ok, note := pickExpression(mt.Expression)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf("metric %q has no expression; skipped", mt.Name))
			continue
		}
		if note != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("metric %q: %s", mt.Name, note))
		}
		def, ok := parseAggExpr(expr)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q not transpiled: expression %q could not be parsed as an aggregate expression", mt.Name, expr))
			continue
		}
		home, ok := metricHome(def, owner)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q not transpiled: could not determine the dataset its expression belongs to", mt.Name))
			continue
		}
		idx := tableIndex(out, home)
		if idx < 0 {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q references unknown dataset %q; skipped", mt.Name, home))
			continue
		}
		def = setAggTable(def, home, colsByTable[home])
		// A plain aggregation over one column is also a column-backed measure.
		if agg, col, isSimple := simpleAgg(def); isSimple {
			out.Tables[idx].Measures = append(out.Tables[idx].Measures, ir.Measure{
				Field: ir.Field{
					Name:        mt.Name,
					Description: mt.Description,
					DataType:    osiToIRType(mt.DataType),
					Expr:        col,
					Synonyms:    mt.AIContext.synonyms(),
				},
				Agg: agg,
			})
		}
		out.Tables[idx].Metrics = append(out.Tables[idx].Metrics, ir.Metric{
			Name:        mt.Name,
			Description: mt.Description,
			Synonyms:    mt.AIContext.synonyms(),
			Def:         def,
		})
	}
```

And the index helper:

```go
// tableIndex returns the position of the named table in m, or -1.
func tableIndex(m *ir.Model, name string) int {
	for i := range m.Tables {
		if m.Tables[i].Name == name {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./dialect/ -run TestOssie -v`
Expected: PASS

- [ ] **Step 6: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add dialect/ossie.go dialect/ossie_test.go
git commit -m "feat(ossie): parse relationships and metrics

OSI's flat metric list distributes onto the IR's per-table metrics by
homing each expression on its first referenced dataset, the same rule
dbt uses for cross-table derived metrics. A plain aggregation over one
column additionally synthesises an ir.Measure, matching what dbt.Parse
produces for a type: simple metric."
```

---

### Task 8: ossie `Emit` — datasets and fields

**Files:**
- Modify: `dialect/ossie_emit.go`
- Test: Create `dialect/ossie_emit_test.go`

**Interfaces:**
- Consumes: `irToOSIType` (Task 6), `enumClause` / `synonymClause` / `appendClause` (`dialect/enum.go`).
- Produces: `ossie.Emit` writing `semantic_model.yaml`. Task 9 extends the same function.

- [ ] **Step 1: Write the failing test**

Create `dialect/ossie_emit_test.go`:

```go
package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// emitOssie emits m and returns the parsed semantic_model.yaml plus its raw text.
func emitOssie(t *testing.T, m *ir.Model, opts Options) (osiFile, string) {
	t.Helper()
	e := ossie{}.WithOptions(opts)
	out := t.TempDir()
	if _, err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	return f, string(b)
}

func TestOssieEmitDatasets(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:        "orders",
		Description: "Customer orders.",
		Synonyms:    []string{"purchases"},
		PrimaryKey:  []string{"order_id", "line_number"},
		Grain:       "order_date",
		Dimensions: []ir.Field{{
			Name: "status", Expr: "status", DataType: "varchar",
			Description: "Order status.",
			Synonyms:    []string{"state"},
			Enum:        []ir.EnumValue{{Value: "placed"}, {Value: "shipped", Description: "left the warehouse"}},
		}},
		TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date", DataType: "date"}},
	}}}

	f, raw := emitOssie(t, m, Options{Database: "ANALYTICS", Schema: "MAIN", Name: "sales", Description: "Sales."})

	if f.Version != "0.2.0.dev0" {
		t.Errorf("version = %q, want 0.2.0.dev0", f.Version)
	}
	if len(f.SemanticModel) != 1 {
		t.Fatalf("want 1 semantic_model entry, got %d", len(f.SemanticModel))
	}
	sm := f.SemanticModel[0]
	if sm.Name != "sales" || sm.Description != "Sales." {
		t.Errorf("model identity = %q / %q", sm.Name, sm.Description)
	}
	if len(sm.Datasets) != 1 {
		t.Fatalf("want 1 dataset, got %d", len(sm.Datasets))
	}
	ds := sm.Datasets[0]
	if ds.Source != "ANALYTICS.MAIN.orders" {
		t.Errorf("source = %q, want ANALYTICS.MAIN.orders", ds.Source)
	}
	if len(ds.PrimaryKey) != 2 {
		t.Errorf("primary_key = %v, want both columns", ds.PrimaryKey)
	}
	if ds.AIContext == nil || len(ds.AIContext.Synonyms) != 1 {
		t.Errorf("dataset ai_context.synonyms missing: %+v", ds.AIContext)
	}
	// Grain has no OSI slot and folds into the dataset description.
	if !strings.Contains(ds.Description, "order_date") {
		t.Errorf("dataset description missing the grain: %q", ds.Description)
	}

	byName := map[string]osiField{}
	for _, fl := range ds.Fields {
		byName[fl.Name] = fl
	}
	status, ok := byName["status"]
	if !ok {
		t.Fatalf("no status field in %v", ds.Fields)
	}
	if status.DataType != "String" {
		t.Errorf("status datatype = %q, want String", status.DataType)
	}
	if status.Dimension == nil || status.Dimension.IsTime == nil || *status.Dimension.IsTime {
		t.Errorf("status should be marked is_time: false, got %+v", status.Dimension)
	}
	// Enum has no OSI slot and folds into the field description.
	if !strings.Contains(status.Description, "placed") {
		t.Errorf("status description missing folded enum: %q", status.Description)
	}
	od, ok := byName["order_date"]
	if !ok {
		t.Fatalf("no order_date field")
	}
	if od.Dimension == nil || od.Dimension.IsTime == nil || !*od.Dimension.IsTime {
		t.Errorf("order_date should be marked is_time: true, got %+v", od.Dimension)
	}

	// The reference converter emits a top-level dialects: key that the published
	// osi-schema.json rejects (additionalProperties: false). semglot must not.
	if strings.Contains(raw, "\ndialects:") {
		t.Errorf("emitted a top-level dialects: key, which osi-schema.json rejects:\n%s", raw)
	}
}

// TestOssieEmitUnqualifiedSource degrades gracefully when no database is set.
func TestOssieEmitUnqualifiedSource(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "orders"}}}
	f, _ := emitOssie(t, m, Options{Name: "sales"})
	if got := f.SemanticModel[0].Datasets[0].Source; got != "orders" {
		t.Errorf("source = %q, want bare table name when no database is set", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestOssieEmit -v`
Expected: FAIL — `Emit` panics with "implemented in a later task".

- [ ] **Step 3: Implement dataset and field emit**

Replace the stub in `dialect/ossie_emit.go`:

```go
package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// WithOptions returns an ossie emitter carrying the model identity and the
// database/schema used to qualify each dataset's `source`.
func (ossie) WithOptions(o Options) Emitter {
	return ossie{Database: o.Database, Schema: o.Schema, ModelName: o.Name, Description: o.Description}
}

// osiSource builds a dataset's `source`. The spec says it "should be either
// database_name.schema_name.table_name or query", so an unset database degrades
// to a bare (still valid) table reference rather than emitting empty segments.
func (o ossie) osiSource(table string) string {
	switch {
	case o.Database != "" && o.Schema != "":
		return fmt.Sprintf("%s.%s.%s", o.Database, o.Schema, table)
	case o.Database != "":
		return fmt.Sprintf("%s.%s", o.Database, table)
	case o.Schema != "":
		return fmt.Sprintf("%s.%s", o.Schema, table)
	default:
		return table
	}
}

// ansi wraps a SQL string in the single-dialect expression object OSI requires.
func ansi(expr string) osiExpression {
	return osiExpression{Dialects: []osiDialectExpr{{Dialect: "ANSI_SQL", Expression: expr}}}
}

// aiContext builds an ai_context block, or nil when there is nothing to say.
func aiContext(instructions string, synonyms []string) *osiAIContext {
	if instructions == "" && len(synonyms) == 0 {
		return nil
	}
	return &osiAIContext{Instructions: instructions, Synonyms: synonyms}
}

// osiField renders one IR field. isTime is emitted explicitly in both
// directions rather than relying on the datatype default, so a reader never has
// to re-derive the role semglot already decided.
func toOSIField(f ir.Field, isTime bool) osiField {
	t := isTime
	return osiField{
		Name:        f.Name,
		Expression:  ansi(f.Expr),
		Dimension:   &osiDimension{IsTime: &t},
		Description: appendClause(f.Description, enumClause(f.Enum)),
		DataType:    irToOSIType(f.DataType),
		AIContext:   aiContext("", f.Synonyms),
	}
}

func (o ossie) Emit(m *ir.Model, dir string) ([]string, error) {
	name := o.ModelName
	if name == "" {
		name = "semantic_model"
	}
	var warnings []string

	sm := osiModel{Name: name, Description: o.Description}
	if len(m.Notes) > 0 {
		sm.AIContext = aiContext(strings.Join(m.Notes, " "), nil)
	}

	for _, t := range m.Tables {
		desc := t.Description
		// Grain has no OSI slot; fold it into the dataset description.
		if t.Grain != "" {
			desc = appendClause(desc, "Default time dimension: "+t.Grain+".")
		}
		ds := osiDataset{
			Name:        t.Name,
			Source:      o.osiSource(t.Name),
			PrimaryKey:  t.PrimaryKey,
			Description: desc,
			AIContext:   aiContext("", t.Synonyms),
		}
		for _, d := range t.Dimensions {
			ds.Fields = append(ds.Fields, toOSIField(d, false))
		}
		for _, d := range t.TimeDimensions {
			ds.Fields = append(ds.Fields, toOSIField(d, true))
		}
		// A measure's column must be a declared field: OSI defines fields as the
		// operands of metric expressions. Ossie's own dbt converter does the same.
		for _, ms := range t.Measures {
			ds.Fields = append(ds.Fields, toOSIField(ms.Field, false))
		}
		sm.Datasets = append(sm.Datasets, ds)
	}

	f := osiFile{Version: osiVersion, SemanticModel: []osiModel{sm}}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return warnings, err
	}
	if err := enc.Close(); err != nil {
		return warnings, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return warnings, err
	}
	return warnings, os.WriteFile(filepath.Join(dir, "semantic_model.yaml"), buf.Bytes(), 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./dialect/ -run TestOssieEmit -v`
Expected: PASS

- [ ] **Step 5: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add dialect/ossie_emit.go dialect/ossie_emit_test.go
git commit -m "feat(ossie): emit datasets and fields

Measure columns are emitted as fields because OSI defines fields as the
operands of metric expressions - the same shape Ossie's own dbt
converter produces. Enum values and table grain have no OSI slot and
fold into descriptions."
```

---

### Task 9: ossie `Emit` — metrics, relationships, and degradations

**Files:**
- Modify: `dialect/ossie_emit.go`
- Modify: `README.md`
- Test: `dialect/ossie_emit_test.go`

**Interfaces:**
- Consumes: `renderSQL`, `metricResolver` (`dialect/render_sql.go`), `relRoleSuffix` (`dialect.go:115`), `cortexDegrade`.
- Produces: complete `ossie.Emit`. Tasks 10-12 test it end to end.

- [ ] **Step 1: Write the failing test**

Add to `dialect/ossie_emit_test.go`:

```go
func TestOssieEmitMetricsAndRelationships(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{
				Name:       "orders",
				Dimensions: []ir.Field{{Name: "customer_id", Expr: "customer_id"}},
				Measures: []ir.Measure{
					{Field: ir.Field{Name: "revenue", Expr: "amount", Description: "Revenue."}, Agg: "sum"},
				},
				Metrics: []ir.Metric{
					{Name: "revenue", Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}},
					{Name: "aov", Label: "Average order value", Def: ir.Binary{
						Op:    "/",
						Left:  ir.Ref{Metric: "revenue"},
						Right: ir.Agg{Func: "count", Table: "orders", Arg: nil},
					}},
				},
			},
			{Name: "customers", Dimensions: []ir.Field{{Name: "id", Expr: "id"}}},
		},
		Relationships: []ir.Relationship{
			{Left: "orders", Right: "customers", Columns: []ir.ColumnPair{{Left: "customer_id", Right: "id"}}},
		},
	}

	f, _ := emitOssie(t, m, Options{Database: "A", Schema: "M", Name: "sales"})
	sm := f.SemanticModel[0]

	if len(sm.Relationships) != 1 {
		t.Fatalf("want 1 relationship, got %d", len(sm.Relationships))
	}
	r := sm.Relationships[0]
	if r.From != "orders" || r.To != "customers" || r.Name == "" {
		t.Errorf("relationship = %+v; from must be the many side and name must be set", r)
	}

	// The measure and the metric share the name "revenue"; only one can occupy
	// the flat metrics list. The metric is canonical.
	byName := map[string]osiMetric{}
	for _, mt := range sm.Metrics {
		if _, dup := byName[mt.Name]; dup {
			t.Errorf("duplicate metric name %q in the flat list", mt.Name)
		}
		byName[mt.Name] = mt
	}
	if len(byName) != 2 {
		t.Errorf("want 2 metrics (revenue, aov), got %v", sm.Metrics)
	}
	rev, ok := byName["revenue"]
	if !ok {
		t.Fatal("no revenue metric")
	}
	if len(rev.Expression.Dialects) != 1 || rev.Expression.Dialects[0].Dialect != "ANSI_SQL" {
		t.Errorf("expression = %+v, want a single ANSI_SQL entry", rev.Expression)
	}
	if !strings.Contains(rev.Expression.Dialects[0].Expression, "orders.amount") {
		t.Errorf("revenue expression = %q, want a physical column reference", rev.Expression.Dialects[0].Expression)
	}
	// Label has no OSI slot and folds into the description.
	if !strings.Contains(byName["aov"].Description, "Average order value") {
		t.Errorf("aov description missing folded label: %q", byName["aov"].Description)
	}
}

// TestOssieEmitDegradesWindowMetrics omits a metric with no OSI primitive and
// returns a warning rather than emitting SQL semglot cannot stand behind.
func TestOssieEmitDegradesWindowMetrics(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Metrics: []ir.Metric{{
			Name: "rolling_revenue",
			Def:  ir.Window{Base: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}}, Window: "30 days"},
		}},
	}}}
	e := ossie{}.WithOptions(Options{Name: "sales"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, "rolling_revenue") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the degraded metric, got %v", warnings)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run 'TestOssieEmitMetrics|TestOssieEmitDegrades' -v`
Expected: FAIL — no metrics or relationships are emitted.

- [ ] **Step 3: Implement metric and relationship emit**

In `dialect/ossie_emit.go`, inside `Emit`, after the table loop and before building `f`:

```go
	resolve := metricResolver(m)

	// OSI has one flat, model-level metrics list, so measures and metrics share
	// a namespace here even though they do not in the IR. Emit metrics first and
	// let them win a name clash - the metric is the published calculation, and
	// dialect/README.md records the same precedence for lightdash.
	seen := map[string]bool{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				warnings = append(warnings, fmt.Sprintf("metric %q not emitted to ossie: %s", mt.Name, reason))
				continue
			}
			desc := mt.Description
			// Label and slice-by dimensions have no OSI slot.
			if mt.Label != "" {
				desc = appendClause(desc, "Display name: "+mt.Label+".")
			}
			if len(mt.Dimensions) > 0 {
				desc = appendClause(desc, "Sliced by: "+strings.Join(mt.Dimensions, ", ")+".")
			}
			sm.Metrics = append(sm.Metrics, osiMetric{
				Name:        mt.Name,
				Expression:  ansi(renderSQL(mt.Def, resolve)),
				Description: desc,
				AIContext:   aiContext("", mt.Synonyms),
			})
			seen[mt.Name] = true
		}
	}
	for _, t := range m.Tables {
		for _, ms := range t.Measures {
			if seen[ms.Name] {
				warnings = append(warnings, fmt.Sprintf(
					"measure %q on table %q not emitted: a metric of the same name occupies OSI's flat metric list", ms.Name, t.Name))
				continue
			}
			sm.Metrics = append(sm.Metrics, osiMetric{
				Name:        ms.Name,
				Expression:  ansi(aggExpr(ms.Agg, t.Name+"."+ms.Expr)),
				Description: ms.Description,
				DataType:    irToOSIType(ms.DataType),
				AIContext:   aiContext("", ms.Synonyms),
			})
			seen[ms.Name] = true
		}
	}

	for _, r := range m.Relationships {
		if len(r.Columns) == 0 {
			continue
		}
		// OSI requires a unique relationship name. relRoleSuffix disambiguates a
		// role-playing dimension (two FKs between the same pair) exactly as the
		// cortex, snowflake-semantic-view and databricks emitters do.
		relName := r.Left + "_to_" + r.Right
		if suffix := relRoleSuffix(m.Relationships, r); suffix != "" {
			relName += "_" + suffix
		}
		rel := osiRelationship{Name: relName, From: r.Left, To: r.Right}
		for _, cp := range r.Columns {
			rel.FromColumns = append(rel.FromColumns, cp.Left)
			rel.ToColumns = append(rel.ToColumns, cp.Right)
		}
		sm.Relationships = append(sm.Relationships, rel)
	}
```

`aggExpr(agg, col string) string` already exists at `dialect/dbt.go:858`; confirm its exact rendering (particularly for `count_distinct`) and use it rather than hand-building the SQL.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./dialect/ -run TestOssieEmit -v`
Expected: PASS

- [ ] **Step 5: Update the top-level README**

Two edits to `README.md`:

1. Add a row to the Dialect support table, keeping the existing column alignment:

```
| `ossie`                   |   ✓    |   ✓    |
```

2. The prose below the table states dbt is "both, so `dbt` to `dbt` is a lossless round-trip", and the intro paragraph names the supported formats. Rewrite the sentence beginning "A **source** is read into the IR" so it no longer implies dbt is the only source:

> A **source** is read into the IR; a **target** is written from it. `dbt` and
> `ossie` are both, so `dbt` to `dbt` and `ossie` to `ossie` are round-trips.

Also add Apache Ossie to the list of formats in the opening description, after "Databricks metric views".

- [ ] **Step 6: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add dialect/ossie_emit.go dialect/ossie_emit_test.go README.md
git commit -m "feat(ossie): emit metrics and relationships

Measures and metrics both land in OSI's single flat metrics list; a name
clash resolves in the metric's favour with a warning, matching the
precedent dialect/README.md records for lightdash. Relationship names
use relRoleSuffix so role-playing dimensions stay unique, as OSI
requires."
```

---

### Task 10: Test layer A — vendored parse conformance

**Files:**
- Create: `test/models/ossie/vendor/README.md`
- Create: `test/models/ossie/vendor/tpcds_semantic_model.yaml`, `fixtureA_ossie.yaml`, `fixtureB_ossie.yaml`, `tpcds_ossie.yaml`
- Create: `test/ossie_conformance_test.go`

**Interfaces:**
- Consumes: `dialect.AsParser("ossie")`.
- Produces: nothing consumed later.

- [ ] **Step 1: Vendor the upstream fixtures**

```bash
mkdir -p test/models/ossie/vendor
BASE=https://raw.githubusercontent.com/apache/ossie/88e0011148283302c9a04cd0287e00e0b9d87354
curl -sSf "$BASE/examples/tpcds_semantic_model.yaml" -o test/models/ossie/vendor/tpcds_semantic_model.yaml
for f in fixtureA_ossie fixtureB_ossie tpcds_ossie; do
  curl -sSf "$BASE/converters/databricks/tests/fixtures/$f.yaml" -o "test/models/ossie/vendor/$f.yaml"
done
head -20 test/models/ossie/vendor/tpcds_semantic_model.yaml
```

Confirm each downloaded file retains its ASF licence header. Do not edit the vendored files.

- [ ] **Step 2: Write the provenance record**

Create `test/models/ossie/vendor/README.md`:

```markdown
# Vendored Apache Ossie fixtures

These files are copied verbatim from [apache/ossie](https://github.com/apache/ossie)
at commit `88e0011148283302c9a04cd0287e00e0b9d87354` (2026-07-31), and are used
as parse-conformance inputs for semglot's `ossie` dialect.

| File | Upstream path |
|---|---|
| `tpcds_semantic_model.yaml` | `examples/tpcds_semantic_model.yaml` |
| `fixtureA_ossie.yaml` | `converters/databricks/tests/fixtures/fixtureA_ossie.yaml` |
| `fixtureB_ossie.yaml` | `converters/databricks/tests/fixtures/fixtureB_ossie.yaml` |
| `tpcds_ossie.yaml` | `converters/databricks/tests/fixtures/tpcds_ossie.yaml` |

Apache Ossie is licensed under the Apache License 2.0; semglot is MIT. The ASF
headers in these files are retained deliberately — do not strip them, and do not
edit the files. To refresh them, re-copy from a newer upstream commit and update
the SHA above.
```

- [ ] **Step 3: Write the conformance test**

Create `test/ossie_conformance_test.go`:

```go
package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/dialect"
)

// vendorDir holds fixtures copied verbatim from apache/ossie. See its README.md
// for provenance and licensing.
const vendorDir = "models/ossie/vendor"

// TestOssieParsesUpstreamFixtures parses every vendored Apache Ossie document
// and asserts semglot extracts real structure from each — the conformance
// signal is that the spec authors' own files round-trip into a usable IR.
func TestOssieParsesUpstreamFixtures(t *testing.T) {
	p, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(vendorDir)
	if err != nil {
		t.Fatal(err)
	}
	var files int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		files++
		t.Run(e.Name(), func(t *testing.T) {
			dir := t.TempDir()
			b, err := os.ReadFile(filepath.Join(vendorDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
			m, err := p.Parse(dir)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(m.Tables) == 0 {
				t.Fatalf("no tables parsed from %s", e.Name())
			}
			var fields, metrics int
			for _, tb := range m.Tables {
				fields += len(tb.Dimensions) + len(tb.TimeDimensions)
				metrics += len(tb.Metrics)
			}
			if fields == 0 {
				t.Errorf("no fields parsed from %s", e.Name())
			}
			t.Logf("%s: %d tables, %d fields, %d metrics, %d relationships, %d notes",
				e.Name(), len(m.Tables), fields, metrics, len(m.Relationships), len(m.Notes))
			for _, n := range m.Notes {
				t.Logf("  note: %s", n)
			}
		})
	}
	if files == 0 {
		t.Fatal("no vendored fixtures found; run the vendoring step first")
	}
}

// TestOssieParsesTPCDSDetail pins the specifics of the spec's own headline
// example, so a regression in dataset, key, or metric handling is caught by
// name rather than by count.
func TestOssieParsesTPCDSDetail(t *testing.T) {
	p, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join(vendorDir, "tpcds_semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tpcds.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := p.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tb := range m.Tables {
		if tb.Name != "store_sales" {
			continue
		}
		found = true
		// Composite primary key: [ss_item_sk, ss_ticket_number]
		if len(tb.PrimaryKey) != 2 {
			t.Errorf("store_sales primary_key = %v, want 2 columns", tb.PrimaryKey)
		}
		if len(tb.Synonyms) == 0 {
			t.Error("store_sales lost its dataset ai_context.synonyms")
		}
	}
	if !found {
		t.Fatalf("no store_sales dataset in %d tables", len(m.Tables))
	}
	var total bool
	for _, tb := range m.Tables {
		for _, mt := range tb.Metrics {
			if mt.Name == "total_sales" {
				total = true
			}
		}
	}
	if !total {
		t.Error("total_sales metric was not parsed")
	}
}
```

- [ ] **Step 4: Run the conformance tests**

Run: `go test ./test/ -run TestOssieParses -v`
Expected: PASS. Read the logged note lines carefully — each note is something semglot could not represent. Notes for genuinely unsupported constructs are fine; a note on a construct the design says is supported (composite keys, `DATABRICKS`-only expressions, filtered aggregates) is a bug to fix now, in `dialect/ossie.go`, before moving on.

- [ ] **Step 5: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add test/models/ossie/ test/ossie_conformance_test.go
git commit -m "test(ossie): parse conformance against vendored upstream fixtures

Parses Apache Ossie's own example and converter fixtures, covering
multi-dialect expressions, composite keys, dataset synonyms and
cross-dataset metrics. Fixtures are copied verbatim at a pinned commit
with their ASF headers retained; see the vendor README for provenance."
```

---

### Task 11: Test layer B — cross-implementation agreement on `dbt → ossie`

**Files:**
- Create: `test/models/ossie/vendor/reference/derived_metric_nested.yaml`, `ratio_metric_inlines.yaml`
- Create: `test/models/ossie/reference/derived_metric_nested/schema.yml`, `ratio_metric_inlines/schema.yml`
- Create: `test/ossie_reference_test.go`
- Modify: `test/models/ossie/vendor/README.md`

**Interfaces:**
- Consumes: `dialect.AsParser("dbt")`, `dialect.AsEmitter("ossie")`.
- Produces: `normalizeOSI` and `diffOSI` helpers, used only by this test file.

- [ ] **Step 1: Extract the reference outputs**

Open `converters/dbt/tests/__snapshots__/test_msi_to_osi.ambr` upstream:

```bash
curl -sSf "https://raw.githubusercontent.com/apache/ossie/88e0011148283302c9a04cd0287e00e0b9d87354/converters/dbt/tests/__snapshots__/test_msi_to_osi.ambr" -o /tmp/msi_to_osi.ambr
grep -n '^# name:' /tmp/msi_to_osi.ambr
```

The file is a syrupy snapshot: each case is `# name: <TestClass.test_name>`, then a `'''`-delimited block of YAML indented by two spaces. Extract the two cases named `TestMetricConversion.test_derived_metric_nested` and `TestMetricConversion.test_ratio_metric_inlines_sub_expressions`, strip the two-space indent, and save them as `test/models/ossie/vendor/reference/derived_metric_nested.yaml` and `ratio_metric_inlines.yaml`.

Add a section to `test/models/ossie/vendor/README.md`:

```markdown
## Reference converter outputs

`reference/` holds OSI documents produced by Ossie's own dbt converter,
extracted from `converters/dbt/tests/__snapshots__/test_msi_to_osi.ambr` at the
same pinned commit and de-indented from the syrupy snapshot format. They are the
expected output in `test/ossie_reference_test.go`, which compares them against
what semglot's `dbt -> ossie` produces from an equivalent dbt input.
```

- [ ] **Step 2: Write the equivalent dbt inputs**

Read each extracted reference output and hand-write the dbt project that produces it. For `derived_metric_nested.yaml` — whose datasets declare fields `revenue`/`cost`/`expenses` over columns `amount`/`cost_amount`/`expense_amount`, and metrics `revenue`, `cost`, `expenses`, `gross_profit`, `net_profit` — create `test/models/ossie/reference/derived_metric_nested/schema.yml`:

```yaml
semantic_models:
  - name: orders
    model: ref('orders')
    measures:
      - {name: revenue, agg: sum, expr: amount}
      - {name: cost, agg: sum, expr: cost_amount}
      - {name: expenses, agg: sum, expr: expense_amount}

metrics:
  - name: revenue
    type: simple
    type_params: {measure: revenue}
  - name: cost
    type: simple
    type_params: {measure: cost}
  - name: expenses
    type: simple
    type_params: {measure: expenses}
  - name: gross_profit
    type: derived
    type_params: {expr: "revenue - cost"}
  - name: net_profit
    type: derived
    type_params: {expr: "gross_profit - expenses"}
```

Do the same for `ratio_metric_inlines.yaml`, whose reference output declares fields `revenue` (over `amount`) and `order_count` (over a `CASE WHEN order_id IS NOT NULL THEN 1 ELSE 0 END` expression), and metrics `revenue`, `order_count`, and `arpu` as their ratio. Read the extracted file and mirror it exactly — do not guess.

- [ ] **Step 3: Write the failing test**

Create `test/ossie_reference_test.go`:

```go
package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/benchouse/semglot/dialect"
)

// Accepted divergences from Apache Ossie's reference dbt converter. Each entry
// is a deliberate decision recorded in the design doc, NOT a known bug. Adding
// an entry means deciding that semglot is right to differ; a NEW divergence
// showing up as a test failure is a regression signal.
//
//  1. Their converter emits a top-level `dialects:` key that the published
//     osi-schema.json rejects (additionalProperties: false permits only
//     `version` and `semantic_model`). semglot does not emit it, and the
//     comparison ignores it entirely by parsing into semglot's own structs.
//  2. Their ratios parenthesize aggressively — `(SUM(x)) / (SUM(y))` — while
//     renderOperand parenthesizes only compound operands. normalizeExpr strips
//     redundant parens around single terms before comparing.
//  3. Expression case and whitespace: they uppercase aggregate names, semglot
//     renders lowercase neutral SQL. normalizeExpr folds case.
const referenceDivergences = `dialects key; redundant parens; expression case`

var (
	spaceRun     = regexp.MustCompile(`\s+`)
	parenWrapped = regexp.MustCompile(`\((\s*[A-Za-z_][A-Za-z0-9_.]*\s*\([^()]*\)\s*)\)`)
)

// normalizeExpr canonicalizes a SQL expression for comparison: case-folded,
// whitespace-collapsed, and with parens stripped from around a single call.
func normalizeExpr(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = spaceRun.ReplaceAllString(s, " ")
	for {
		next := parenWrapped.ReplaceAllString(s, "$1")
		next = spaceRun.ReplaceAllString(strings.TrimSpace(next), " ")
		if next == s {
			return s
		}
		s = next
	}
}

// osiSummary is the semantic content compared between the two implementations:
// dataset -> sorted field names, and metric name -> normalized expression.
type osiSummary struct {
	fields  map[string][]string
	metrics map[string]string
}

// summarize reduces an OSI document to its comparable content. It deliberately
// ignores key ordering, descriptions, and ai_context — the reference converter
// carries none of the latter for these fixtures.
func summarize(t *testing.T, path string) osiSummary {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Parse into a loose shape so the reference file's extra top-level
	// `dialects:` key does not fail decoding.
	var doc struct {
		SemanticModel []struct {
			Datasets []struct {
				Name   string `yaml:"name"`
				Fields []struct {
					Name string `yaml:"name"`
				} `yaml:"fields"`
			} `yaml:"datasets"`
			Metrics []struct {
				Name       string `yaml:"name"`
				Expression struct {
					Dialects []struct {
						Dialect    string `yaml:"dialect"`
						Expression string `yaml:"expression"`
					} `yaml:"dialects"`
				} `yaml:"expression"`
			} `yaml:"metrics"`
		} `yaml:"semantic_model"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	s := osiSummary{fields: map[string][]string{}, metrics: map[string]string{}}
	for _, sm := range doc.SemanticModel {
		for _, ds := range sm.Datasets {
			var names []string
			for _, f := range ds.Fields {
				names = append(names, f.Name)
			}
			sort.Strings(names)
			s.fields[ds.Name] = names
		}
		for _, mt := range sm.Metrics {
			if len(mt.Expression.Dialects) == 0 {
				continue
			}
			s.metrics[mt.Name] = normalizeExpr(mt.Expression.Dialects[0].Expression)
		}
	}
	return s
}

// diffOSI reports every semantic difference between two summaries.
func diffOSI(want, got osiSummary) []string {
	var out []string
	for ds, wf := range want.fields {
		gf, ok := got.fields[ds]
		if !ok {
			out = append(out, fmt.Sprintf("dataset %q missing", ds))
			continue
		}
		if strings.Join(wf, ",") != strings.Join(gf, ",") {
			out = append(out, fmt.Sprintf("dataset %q fields: want %v, got %v", ds, wf, gf))
		}
	}
	for ds := range got.fields {
		if _, ok := want.fields[ds]; !ok {
			out = append(out, fmt.Sprintf("unexpected dataset %q", ds))
		}
	}
	for name, we := range want.metrics {
		ge, ok := got.metrics[name]
		if !ok {
			out = append(out, fmt.Sprintf("metric %q missing", name))
			continue
		}
		if we != ge {
			out = append(out, fmt.Sprintf("metric %q expression:\n    want %s\n     got %s", name, we, ge))
		}
	}
	for name := range got.metrics {
		if _, ok := want.metrics[name]; !ok {
			out = append(out, fmt.Sprintf("unexpected metric %q", name))
		}
	}
	sort.Strings(out)
	return out
}

// TestOssieAgreesWithReferenceConverter compares semglot's dbt -> ossie output
// against the output Apache Ossie's own dbt converter produces from an
// equivalent dbt project. Accepted divergences are listed in
// referenceDivergences and normalized away; anything else is a real
// disagreement worth understanding before it ships.
func TestOssieAgreesWithReferenceConverter(t *testing.T) {
	cases := []string{"derived_metric_nested", "ratio_metric_inlines"}
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	e, err := dialect.AsEmitter("ossie")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := p.Parse(filepath.Join("models/ossie/reference", name))
			if err != nil {
				t.Fatalf("parse dbt: %v", err)
			}
			out := t.TempDir()
			emitter := e
			if c, ok := e.(dialect.Configurable); ok {
				emitter = c.WithOptions(dialect.Options{Schema: "schema", Name: "semantic_model"})
			}
			if _, err := emitter.Emit(m, out); err != nil {
				t.Fatalf("emit ossie: %v", err)
			}
			want := summarize(t, filepath.Join(vendorDir, "reference", name+".yaml"))
			got := summarize(t, filepath.Join(out, "semantic_model.yaml"))
			if d := diffOSI(want, got); len(d) > 0 {
				t.Errorf("semglot disagrees with the reference converter (accepted divergences: %s):\n  %s",
					referenceDivergences, strings.Join(d, "\n  "))
			}
		})
	}
}
```

- [ ] **Step 4: Run the test and triage every difference**

Run: `go test ./test/ -run TestOssieAgreesWithReference -v`

The first run will almost certainly report differences. Triage each one:

- **A real semglot bug** → fix `dialect/ossie.go` or `dialect/ossie_emit.go`.
- **A legitimate design difference** → add it to `referenceDivergences` with a one-line justification, extend `normalizeExpr` or `summarize` to neutralize it, and add the same entry to the Divergences section of the design doc.
- **A mismatched hand-written dbt input** → fix the fixture in `test/models/ossie/reference/`.

Do not silence a difference by deleting the assertion. Iterate until the test passes.

- [ ] **Step 5: Run the full suite**

Run: `go test ./... && go vet ./... && gofmt -l .`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add test/models/ossie/ test/ossie_reference_test.go
git commit -m "test(ossie): agreement with Apache Ossie's reference dbt converter

Compares semglot's dbt -> ossie output against the OSI documents Ossie's
own converter produces, semantically rather than textually. The accepted
divergences are enumerated in referenceDivergences; a new one appearing
is a regression signal."
```

---

### Task 12: Test layer C — round-trip information-loss report, and docs

**Files:**
- Create: `test/loss_test.go`
- Modify: `dialect/README.md`
- Modify: `docs/superpowers/specs/2026-08-12-ossie-dialect-design.md` (only if Task 11 added divergences)

**Interfaces:**
- Consumes: `canonicalizeModel` (`test/integration_test.go:44`), both dialects.
- Produces: `lossReport(before, after *ir.Model) []string`.

- [ ] **Step 1: Write the failing test**

Create `test/loss_test.go`:

```go
package integration_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/benchouse/semglot/dialect"
	"github.com/benchouse/semglot/ir"
)

// lossReport lists what a round-trip dropped or changed, as human-readable
// lines. It compares canonicalized models, so ordering differences never
// register as loss. This is the measurement ir/model.go's package comment
// anticipates when it calls the IR "the unit of the fairness index".
func lossReport(before, after *ir.Model) []string {
	b, a := *before, *after
	canonicalizeModel(&b)
	canonicalizeModel(&a)

	var out []string
	afterTables := map[string]ir.Table{}
	for _, t := range a.Tables {
		afterTables[t.Name] = t
	}
	for _, bt := range b.Tables {
		at, ok := afterTables[bt.Name]
		if !ok {
			out = append(out, "table "+bt.Name+": lost")
			continue
		}
		out = append(out, diffNames("table "+bt.Name+" dimensions", fieldNames(bt.Dimensions), fieldNames(at.Dimensions))...)
		out = append(out, diffNames("table "+bt.Name+" time dimensions", fieldNames(bt.TimeDimensions), fieldNames(at.TimeDimensions))...)
		out = append(out, diffNames("table "+bt.Name+" measures", measureNames(bt.Measures), measureNames(at.Measures))...)
		out = append(out, diffNames("table "+bt.Name+" metrics", metricNames(bt.Metrics), metricNames(at.Metrics))...)
		out = append(out, diffNames("table "+bt.Name+" primary key", bt.PrimaryKey, at.PrimaryKey)...)
		out = append(out, diffNames("table "+bt.Name+" synonyms", bt.Synonyms, at.Synonyms)...)
		if bt.Grain != at.Grain {
			out = append(out, fmt.Sprintf("table %s grain: %q -> %q", bt.Name, bt.Grain, at.Grain))
		}
	}
	if len(b.Relationships) != len(a.Relationships) {
		out = append(out, fmt.Sprintf("relationships: %d -> %d", len(b.Relationships), len(a.Relationships)))
	}
	sort.Strings(out)
	return out
}

func fieldNames(fs []ir.Field) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func measureNames(ms []ir.Measure) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

func metricNames(ms []ir.Metric) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

// diffNames reports names present before but absent after, and vice versa.
func diffNames(label string, before, after []string) []string {
	have := map[string]bool{}
	for _, n := range after {
		have[n] = true
	}
	had := map[string]bool{}
	for _, n := range before {
		had[n] = true
	}
	var out []string
	for _, n := range before {
		if !have[n] {
			out = append(out, label+": lost "+n)
		}
	}
	for _, n := range after {
		if !had[n] {
			out = append(out, label+": gained "+n)
		}
	}
	return out
}

// allowedLoss is what dbt -> ossie -> dbt is EXPECTED to lose or gain, each
// entry a documented format limit from the design doc. A line the report
// produces that is not matched by one of these substrings is unplanned loss.
var allowedLoss = []string{
	// OSI has no measure concept. Every measure is emitted as a model-level
	// metric, so on the way back an unpublished measure returns as a PUBLISHED
	// metric: the metric list gains names it did not have.
	"metrics: gained",
	// Symmetrically, every OSI metric that is a plain aggregation synthesises a
	// measure on parse, so a dbt metric that had no measure of its own name
	// gains one. Both directions are the same missing distinction.
	"measures: gained",
	// Table.Grain has no OSI slot; it folds into the dataset description and
	// does not come back structurally.
	"grain:",
}

func unexpected(report []string) []string {
	var out []string
	for _, line := range report {
		ok := false
		for _, allow := range allowedLoss {
			if strings.Contains(line, allow) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, line)
		}
	}
	return out
}

// TestDBTToOssieLoss measures what a dbt -> ossie -> dbt round-trip costs, and
// fails on any loss the design does not document.
func TestDBTToOssieLoss(t *testing.T) {
	dbtP, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	before, err := dbtP.Parse(sourceDirs...)
	if err != nil {
		t.Fatal(err)
	}

	ossieE, err := dialect.AsEmitter("ossie")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := ossieE.(dialect.Configurable); ok {
		ossieE = c.WithOptions(dialect.Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce"})
	}
	mid := t.TempDir()
	if _, err := ossieE.Emit(before, mid); err != nil {
		t.Fatal(err)
	}

	ossieP, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	after, err := ossieP.Parse(mid)
	if err != nil {
		t.Fatal(err)
	}

	report := lossReport(before, after)
	for _, line := range report {
		t.Logf("loss: %s", line)
	}
	if bad := unexpected(report); len(bad) > 0 {
		t.Errorf("undocumented information loss in dbt -> ossie -> ossie-parse:\n  %s", strings.Join(bad, "\n  "))
	}
}

// TestOssieRoundTrip proves ossie -> ossie is stable: parse a vendored upstream
// document, emit it, re-parse, and expect no loss at all.
func TestOssieRoundTrip(t *testing.T) {
	p, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.Parse(vendorDir)
	if err != nil {
		t.Fatal(err)
	}
	e, err := dialect.AsEmitter("ossie")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := e.(dialect.Configurable); ok {
		e = c.WithOptions(dialect.Options{Database: "A", Schema: "M", Name: "rt"})
	}
	out := t.TempDir()
	if _, err := e.Emit(before, out); err != nil {
		t.Fatal(err)
	}
	after, err := p.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if report := lossReport(before, after); len(report) > 0 {
		t.Errorf("ossie -> ossie is not stable:\n  %s", strings.Join(report, "\n  "))
	}
}
```

- [ ] **Step 2: Run the tests and triage**

Run: `go test ./test/ -run 'TestDBTToOssieLoss|TestOssieRoundTrip' -v`

Read every logged `loss:` line. For each:

- **Matches a documented format limit** → confirm `allowedLoss` covers it, and that the design doc's "Format limits" section names it.
- **Unplanned** → fix the dialect. Do not widen `allowedLoss` to make a real bug pass; each entry there is a claim that the loss is inherent to OSI's format.

`TestOssieRoundTrip` parses every vendored fixture into one merged model, so dataset names must be unique across those files — if two fixtures share a dataset name, split the test to run per-file the way `TestOssieParsesUpstreamFixtures` does.

- [ ] **Step 3: Update `dialect/README.md`**

Four edits:

1. **Direction section** — it currently opens "`dbt` is currently the only **source** (it parses to the IR)". Rewrite:

> `dbt` and `ossie` are **sources** (they parse to the IR) and also targets, so
> their constructs are read and written (marked `<->` below). Every other
> dialect is **emit-only** (IR -> dialect).

2. **Output table** — add a row:

```
| `ossie` | `semantic_model.yaml` (Apache Ossie core-spec 0.2.0.dev0); a source dialect as well as a target |
```

3. **Mapping table** — add an `ossie` column with a cell per row:

| IR concept | `ossie` |
|---|---|
| Table | `datasets[]` `<->` |
| Column / dimension | `fields[]` with `dimension.is_time: false` `<->` |
| Time dimension | `fields[]` with `dimension.is_time: true` `<->` |
| Data type | `datatype` (logical enum) `<->` |
| Primary key | `primary_key: []` (composite supported) `<->` |
| Relationship / join | `relationships[]` (`from`/`to`, composite supported) `<->` |
| Description | `description` `<->` |
| Synonyms | `ai_context.synonyms` `<->` |
| Table synonyms | `ai_context.synonyms` on the dataset `<->` |
| Enum / allowed values | `text` (into the field description) |
| Simple metric (aggregation) | model-level `metrics[]` + a `fields[]` entry for the column `<->` |
| Ratio / derived metric | model-level `metrics[]` (rendered SQL) `<->` |

4. **Gaps vs. limits** — add under **Limits**:

> - **No measure concept in `ossie`.** OSI has one flat, model-level `metrics:`
>   list and no separate measure construct, so a measure that no metric
>   publishes comes back from a round-trip as a published metric. `dbt` →
>   `ossie` → `dbt` is therefore lossy in a way `dbt` → `dbt` is not.
> - **No table grain, metric label, field label, or enum slot in `ossie`.** All
>   four fold into the nearest `description`.
> - **No source-table identity in `ossie` on parse.** A dataset's `source`
>   (`db.schema.table`) has no IR counterpart; emit reconstructs it from the
>   profile's `database`/`schema`.

- [ ] **Step 4: Reconcile the design doc**

If Task 11 added entries to `referenceDivergences`, add the same entries to the **Divergences from the reference converters** section of `docs/superpowers/specs/2026-08-12-ossie-dialect-design.md`, so the spec and the test agree. If Task 2 Step 1 found Cortex does not support table-level synonyms, correct the IR change section's table there too.

- [ ] **Step 5: Run the full verification**

```bash
gofmt -l .
go vet ./...
go build ./...
go test ./...
```

Expected: `gofmt -l .` prints nothing; the other three pass. This is exactly what CI runs.

- [ ] **Step 6: Verify the CLI end to end**

```bash
go build -o /tmp/semglot ./cmd/semglot
mkdir -p /tmp/ossie-out
cat > /tmp/semglot.yaml <<'YAML'
profiles:
  to_ossie:
    source: ./test/models/ecommerce/dbt/semantic
    target-dialect: ossie
    output: /tmp/ossie-out
    database: ANALYTICS
    schema: MAIN
    model-name: ecommerce
  from_ossie:
    source: /tmp/ossie-out
    source-dialect: ossie
    target-dialect: cortex
    output: /tmp/ossie-cortex
    database: ANALYTICS
    model-name: ecommerce
YAML
/tmp/semglot build --profile to_ossie --config /tmp/semglot.yaml
/tmp/semglot build --profile from_ossie --config /tmp/semglot.yaml
head -40 /tmp/ossie-out/semantic_model.yaml
head -40 /tmp/ossie-cortex/semantic_model.yaml
```

Expected: both builds succeed, `semantic_model.yaml` is a well-formed OSI document, and the cortex output built *from* it carries the same tables and metrics. This proves ossie works as both a source and a target through the real CLI. If `from_ossie` fails on a missing `source-dialect` option, check `cmd/semglot/config.go` accepts it — the profile field already exists per the config spec.

- [ ] **Step 7: Commit**

```bash
git add test/loss_test.go dialect/README.md docs/
git commit -m "test(ossie): measure round-trip information loss

lossReport diffs two IR models and lists what a round-trip dropped or
gained, asserted against an allowlist of documented format limits.
dbt -> ossie -> dbt loses unpublished measures because OSI has no
measure concept; ossie -> ossie must be lossless.

Documents the ossie mapping and its limits in dialect/README.md."
```

- [ ] **Step 8: Open the PR**

```bash
git push -u origin feat/ossie-dialect
gh pr create --base main --title "feat(ossie): Apache Ossie source and target dialect" --body "$(cat <<'EOF'
Adds `ossie`, semglot's second source dialect and eighth target, reading and
writing the Apache Ossie Core Metadata Specification (core-spec `0.2.0.dev0`).

Because everything routes through the IR, this makes every existing emit-only
dialect reachable from an OSI file, and makes semglot's IR reachable from any
tool that already exports OSI.

## Also in this PR

`ir.Table.Synonyms`. Ossie carries `ai_context.synonyms` on datasets and the IR
had nowhere structural to put them — only `ir.Field` and `ir.Metric` had a
synonyms field. It turned out to be a pre-existing gap rather than an ossie
quirk: Snowflake semantic views accept table synonyms and `svSynonyms` already
rendered the clause, it was simply never called for a table. Wired across every
dialect, structurally where a slot exists and as prose where none does, which
closes the `supersimple` synonyms gap `dialect/README.md` recorded.

## Testing

Beyond unit tests, three layers built on Apache Ossie's own fixtures:

- **Parse conformance** against their example and converter fixtures, vendored
  at a pinned commit with ASF headers retained.
- **Cross-implementation agreement**: semglot's `dbt -> ossie` output compared
  semantically against what Ossie's own dbt converter produces, with an
  enumerated allowlist of accepted divergences.
- **Information-loss reporting**: an IR differ asserting `dbt -> ossie -> dbt`
  loses only what OSI's format inherently cannot carry, and that
  `ossie -> ossie` is lossless.

Design: `docs/superpowers/specs/2026-08-12-ossie-dialect-design.md`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review notes

Checked against `docs/superpowers/specs/2026-08-12-ossie-dialect-design.md`:

| Spec section | Task |
|---|---|
| Scope: core-spec only, pinned version | 6 (`osiVersion` const, package comment) |
| Files / registration / `.yml`+`.yaml` glob | 6 |
| Mapping table (all rows) | 6, 7, 8, 9 |
| Measures emit as field + metric | 8 (field), 9 (metric) |
| Data types, bidirectional | 6 (`osiTypes` / `irTypes`) |
| Time dimensions, `is_time` default | 6 (`osiField.isTime`) |
| Relationships, `relRoleSuffix` naming | 7 (parse), 9 (emit) |
| Expression dialects, ANSI preference | 6 (`pickExpression`) |
| SQL expression parsing, two leaf rules | 4, 5 |
| `IR change: Table.Synonyms` (all 9 dialects) | 1, 2, 3 |
| Degradations (emit warnings, parse notes, limits) | 7, 9, 12 |
| Divergences from reference converters | 8 (no `dialects:` key), 11 (allowlist) |
| Testing A / B / C | 10, 11, 12 |
| Vendoring and licensing | 10 |
| Documentation (README, dialect/README) | 3, 9, 12 |
| Out of scope | not implemented anywhere — correct |

Type consistency verified: `osiFile`/`osiModel`/`osiDataset`/`osiField`/`osiMetric`/`osiRelationship`/`osiExpression`/`osiDialectExpr`/`osiDimension`/`osiAIContext` are defined once in Task 6 and used unchanged in Tasks 8, 9, 11. `parseAggExpr` is defined in Task 5 and called in Task 7. `exprParser` is defined in Task 4 and extended in Task 5. `lossReport` is defined and used only in Task 12. `emitOssie` is defined in Task 8 and reused in Task 9.

Two places deliberately defer to the implementer rather than guessing, each with an explicit decision rule and both branches specified:

- **Task 2 Step 1** — whether Cortex accepts table-level `synonyms`. Verify against Snowflake's docs; the fallback (fold to prose in Task 3) is written out.
- **Task 11 Steps 2 and 4** — the hand-written dbt inputs must mirror the extracted reference outputs exactly, and each reported difference needs triage into one of three named categories.
