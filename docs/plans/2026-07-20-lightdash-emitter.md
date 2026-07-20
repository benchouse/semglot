# Lightdash Emitter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `lightdash` target emitter that transpiles the neutral IR into a dbt `schema.yml` annotated with Lightdash `meta:` blocks, the form Lightdash Cloud ingests from a connected dbt project.

**Architecture:** A new registered `Emitter`/`Configurable` in `dialect/lightdash.go`, a sibling of the cortex and nao-yaml emitters. It writes one `schema.yml` (`version: 2`, a `models:` list). Dimensions become `columns[].meta.dimension`, simple aggregates become column-level `meta.metrics`, reference-only ratios become model-level `meta.metrics` (`type: number`), relationships become `meta.joins`, and constructs with no Lightdash primitive degrade to a `# semglot:` comment block. A `MetaStyle` option toggles `meta:` vs `config.meta:` nesting.

**Tech Stack:** Go, `gopkg.in/yaml.v3`. Reuses existing helpers `metricResolver`, `enumClause`, `synonymClause`, `appendClause`, `cortexDegrade`.

## Global Constraints

- Package for the emitter is `dialect` (files live in `dialect/`).
- The emitter must NOT mutate the `*ir.Model` passed to `Emit` (mirror cortex: accumulate degrade notes in a local slice).
- Lightdash is NOT a Snowflake target: do not add it to `snowflakeTargets`; it needs no `--database`.
- Prose in docs uses no em-dashes and one line per paragraph (repo convention).
- Run `gofmt` on every new/edited Go file before committing.
- Verify with `go test ./...`; regenerate goldens with `UPDATE_GOLDEN=1 go test ./...`.
- Dialect name string is exactly `lightdash`.

---

### Task 1: Emitter skeleton, registration, and the notes comment block

Registers the dialect, defines the Lightdash YAML shapes, and emits a minimal but valid `schema.yml`: `version: 2`, one model per IR table (name + description), and a leading `# semglot:` comment block carrying any passthrough `m.Notes`. Later tasks fill in columns, metrics, and joins.

**Files:**
- Create: `dialect/lightdash.go`
- Create: `dialect/lightdash_test.go`

**Interfaces:**
- Consumes: `ir.Model`, `ir.Table`, `Options` (from `dialect/dialect.go`), `Emitter`/`Configurable`, `Register`, `AsEmitter`.
- Produces:
  - `type lightdash struct { ModelName, Description, MetaStyle string }`
  - `func (lightdash) Name() string` returns `"lightdash"`
  - `func (lightdash) WithOptions(o Options) Emitter`
  - `func (l lightdash) Emit(m *ir.Model, dir string) error` writes `<dir>/schema.yml`
  - YAML shape types consumed by later tasks: `ldFile`, `ldModel`, `ldModelMeta`, `ldModelCfg`, `ldColumn`, `ldColMeta`, `ldColCfg`, `ldDimension`, `ldMetric`, `ldJoin`
  - `func (m *ldModelMeta) empty() bool`

- [ ] **Step 1: Write the failing test**

Create `dialect/lightdash_test.go`:

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

// emitLightdash runs the lightdash emitter (with opts) over m and returns the
// emitted schema.yml as a string.
func emitLightdash(t *testing.T, m *ir.Model, opts Options) string {
	t.Helper()
	e := lightdash{}.WithOptions(opts)
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatalf("read schema.yml: %v", err)
	}
	return string(b)
}

func TestLightdashRegisteredAndSkeleton(t *testing.T) {
	if _, err := AsEmitter("lightdash"); err != nil {
		t.Fatalf("AsEmitter(lightdash): %v", err)
	}
	m := &ir.Model{
		Tables: []ir.Table{{Name: "orders", Description: "One row per order."}},
		Notes:  []string{"a passthrough note"},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	if !strings.HasPrefix(got, "# semglot:") {
		t.Errorf("expected leading # semglot comment block, got:\n%s", got)
	}
	if !strings.Contains(got, "# - a passthrough note") {
		t.Errorf("passthrough note missing from comment block:\n%s", got)
	}

	var doc struct {
		Version int `yaml:"version"`
		Models  []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("version = %d, want 2", doc.Version)
	}
	if len(doc.Models) != 1 || doc.Models[0].Name != "orders" {
		t.Fatalf("models = %+v, want one named orders", doc.Models)
	}
	if doc.Models[0].Description != "One row per order." {
		t.Errorf("description = %q", doc.Models[0].Description)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestLightdashRegisteredAndSkeleton -v`
Expected: FAIL to compile (`undefined: lightdash`).

- [ ] **Step 3: Write minimal implementation**

Create `dialect/lightdash.go`:

```go
package dialect

import (
	"bytes"
	"os"
	"path/filepath"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(lightdash{}) }

// lightdash emits a dbt schema.yml annotated with Lightdash meta: blocks, the
// form Lightdash Cloud ingests from a connected dbt project. Zero value is
// usable; the build command sets Name/Description/MetaStyle from the profile.
// Emit does not mutate m.
type lightdash struct {
	ModelName   string
	Description string
	MetaStyle   string // "" or "meta" => meta:; "config.meta" => config.meta:
}

func (lightdash) Name() string { return "lightdash" }
func (lightdash) WithOptions(o Options) Emitter {
	return lightdash{ModelName: o.Name, Description: o.Description, MetaStyle: o.MetaStyle}
}

// ---- Lightdash YAML shapes ----
//
// Each meta payload can hang under either `meta:` (dbt <=1.9) or `config.meta:`
// (dbt 1.10+/Fusion). The model/column structs carry BOTH a Meta and a Config
// field; the emitter sets exactly one based on MetaStyle, so the struct tags
// stay static while placement varies.

type ldFile struct {
	Version int       `yaml:"version"`
	Models  []ldModel `yaml:"models"`
}

type ldModel struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Meta        *ldModelMeta `yaml:"meta,omitempty"`
	Config      *ldModelCfg  `yaml:"config,omitempty"`
	Columns     []ldColumn   `yaml:"columns,omitempty"`
}

type ldModelCfg struct {
	Meta *ldModelMeta `yaml:"meta,omitempty"`
}

type ldModelMeta struct {
	PrimaryKey string              `yaml:"primary_key,omitempty"`
	Joins      []ldJoin            `yaml:"joins,omitempty"`
	Metrics    map[string]ldMetric `yaml:"metrics,omitempty"`
}

func (m *ldModelMeta) empty() bool {
	return m.PrimaryKey == "" && len(m.Joins) == 0 && len(m.Metrics) == 0
}

type ldJoin struct {
	Join         string `yaml:"join"`
	SQLOn        string `yaml:"sql_on"`
	Relationship string `yaml:"relationship,omitempty"`
}

type ldColumn struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Meta        *ldColMeta `yaml:"meta,omitempty"`
	Config      *ldColCfg  `yaml:"config,omitempty"`
}

type ldColCfg struct {
	Meta *ldColMeta `yaml:"meta,omitempty"`
}

type ldColMeta struct {
	Dimension *ldDimension        `yaml:"dimension,omitempty"`
	Metrics   map[string]ldMetric `yaml:"metrics,omitempty"`
}

type ldDimension struct {
	Type  string `yaml:"type,omitempty"`
	Label string `yaml:"label,omitempty"`
}

type ldMetric struct {
	Type string `yaml:"type"`
	SQL  string `yaml:"sql,omitempty"`
}

// Emit writes the IR as one dbt schema.yml carrying Lightdash annotations. It
// does not mutate m: passthrough notes and degrade notes accumulate in a local
// slice and render as a leading # semglot: comment block.
func (l lightdash) Emit(m *ir.Model, dir string) error {
	var notes []string
	notes = append(notes, m.Notes...)

	f := ldFile{Version: 2}
	for _, t := range m.Tables {
		f.Models = append(f.Models, ldModel{Name: t.Name, Description: t.Description})
	}

	var buf bytes.Buffer
	if len(notes) > 0 {
		buf.WriteString("# semglot: some source constructs could not be transpiled to Lightdash:\n")
		for _, n := range notes {
			buf.WriteString("# - " + n + "\n")
		}
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "schema.yml"), buf.Bytes(), 0o644)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w dialect/lightdash.go dialect/lightdash_test.go && go test ./dialect/ -run TestLightdashRegisteredAndSkeleton -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add dialect/lightdash.go dialect/lightdash_test.go
git commit -m "feat(lightdash): emitter skeleton, registration, notes comment block"
```

---

### Task 2: Dimensions to columns with meta.dimension

Turns `Table.Dimensions` and `Table.TimeDimensions` into `columns[]` entries, folding enum values and synonyms into the column description and setting `meta.dimension.type` only when there is a confident type signal. Introduces the `ldColumnSet` builder (dedup by column, preserve first-seen order) that Task 3 reuses to attach column-level metrics.

**Files:**
- Modify: `dialect/lightdash.go`
- Modify: `dialect/lightdash_test.go`

**Interfaces:**
- Consumes: `ir.Field`, `enumClause`, `synonymClause`, `appendClause`.
- Produces:
  - `type ldColumnSet struct { order []string; byName map[string]*ldColumn }`
  - `func newColumnSet() *ldColumnSet`
  - `func (s *ldColumnSet) get(col string) *ldColumn`
  - `func (s *ldColumnSet) dimension(f ir.Field, typ string)`
  - `func (s *ldColumnSet) list() []ldColumn`
  - `func ldDimensionType(f ir.Field, isTime bool) string`
  - `func ldMapType(t string) string`

- [ ] **Step 1: Write the failing test**

Add to `dialect/lightdash_test.go`:

```go
func TestLightdashDimensions(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Dimensions: []ir.Field{
			{Name: "status", Expr: "status", Description: "Order state.",
				Enum: []ir.EnumValue{{Value: "paid"}, {Value: "refunded", Description: "money returned"}}},
			{Name: "is_first_order", Expr: "is_first_order"},
			{Name: "customer_sk", Expr: "customer_sk"},
			{Name: "region", Expr: "region", DataType: "varchar", Synonyms: []string{"area"}},
		},
		TimeDimensions: []ir.Field{
			{Name: "order_date", Expr: "order_date"},
		},
	}}}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Columns []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Meta        struct {
					Dimension struct {
						Type  string `yaml:"type"`
						Label string `yaml:"label"`
					} `yaml:"dimension"`
				} `yaml:"meta"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	cols := map[string]struct {
		desc, typ string
	}{}
	for _, c := range doc.Models[0].Columns {
		cols[c.Name] = struct{ desc, typ string }{c.Description, c.Meta.Dimension.Type}
	}

	// enum meanings fold into the description; bare values are dropped (no slot).
	if !strings.Contains(cols["status"].desc, "refunded = money returned") {
		t.Errorf("status description missing enum meaning: %q", cols["status"].desc)
	}
	// synonyms fold into the description.
	if !strings.Contains(cols["region"].desc, "Synonyms: area") {
		t.Errorf("region description missing synonyms: %q", cols["region"].desc)
	}
	// type from name/DataType signals; order_date is a time dimension.
	if cols["is_first_order"].typ != "boolean" {
		t.Errorf("is_first_order type = %q, want boolean", cols["is_first_order"].typ)
	}
	if cols["customer_sk"].typ != "number" {
		t.Errorf("customer_sk type = %q, want number", cols["customer_sk"].typ)
	}
	if cols["region"].typ != "string" {
		t.Errorf("region type = %q, want string", cols["region"].typ)
	}
	if cols["order_date"].typ != "timestamp" {
		t.Errorf("order_date type = %q, want timestamp", cols["order_date"].typ)
	}
	// no confident signal and no DataType => type omitted (empty).
	if cols["status"].typ != "" {
		t.Errorf("status type = %q, want empty (omitted)", cols["status"].typ)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestLightdashDimensions -v`
Expected: FAIL (columns not emitted; `dimension` empty).

- [ ] **Step 3: Write minimal implementation**

Add to `dialect/lightdash.go` (add `"strings"` to the import block):

```go
// ldColumnSet builds the ordered columns[] list, keyed by physical column name,
// so a dimension and a metric backed by the same column share one entry and a
// metric's backing column is created even when it is not itself a dimension.
type ldColumnSet struct {
	order  []string
	byName map[string]*ldColumn
}

func newColumnSet() *ldColumnSet { return &ldColumnSet{byName: map[string]*ldColumn{}} }

func (s *ldColumnSet) get(col string) *ldColumn {
	c, ok := s.byName[col]
	if !ok {
		c = &ldColumn{Name: col}
		s.byName[col] = c
		s.order = append(s.order, col)
	}
	return c
}

func (s *ldColumnSet) list() []ldColumn {
	out := make([]ldColumn, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, *s.byName[name])
	}
	return out
}

// dimension records f as a Lightdash dimension on its backing column. Enum
// meanings and synonyms fold into the column description (Lightdash has no
// enum or synonym slot). type is set only when typ is non-empty; a label is
// set only when the IR dimension name differs from the physical column.
func (s *ldColumnSet) dimension(f ir.Field, typ string) {
	col := f.Expr
	if col == "" {
		col = f.Name
	}
	c := s.get(col)
	if c.Description == "" {
		desc := appendClause(f.Description, enumClause(f.Enum))
		desc = appendClause(desc, synonymClause(f.Synonyms))
		c.Description = desc
	}
	if typ != "" || (f.Name != "" && f.Name != col) {
		if c.Meta == nil {
			c.Meta = &ldColMeta{}
		}
		if c.Meta.Dimension == nil {
			c.Meta.Dimension = &ldDimension{}
		}
		if typ != "" {
			c.Meta.Dimension.Type = typ
		}
		if f.Name != "" && f.Name != col {
			c.Meta.Dimension.Label = f.Name
		}
	}
}

// ldDimensionType returns a Lightdash dimension type when a confident signal
// exists, else "" so the type is omitted (Lightdash auto-detects from the
// warehouse). Signals: an explicit DataType; a time dimension is timestamp; an
// is_/has_ prefix is boolean; an _id/_sk suffix (or bare "id") is number.
func ldDimensionType(f ir.Field, isTime bool) string {
	if f.DataType != "" {
		return ldMapType(f.DataType)
	}
	if isTime {
		return "timestamp"
	}
	n := strings.ToLower(f.Name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "boolean"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "number"
	}
	return ""
}

// ldMapType normalizes a warehouse column type to one of Lightdash's five
// dimension types. An unrecognized type returns "" so the type is omitted.
func ldMapType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "number", "numeric", "decimal", "int", "integer", "bigint", "smallint",
		"float", "double", "double precision", "real":
		return "number"
	case "varchar", "text", "string", "char", "character varying":
		return "string"
	case "boolean", "bool":
		return "boolean"
	case "date":
		return "date"
	case "timestamp", "datetime", "timestamp_ntz", "timestamp_tz", "timestamp_ltz":
		return "timestamp"
	default:
		return ""
	}
}
```

Then wire columns into `Emit` by replacing the table loop body:

```go
	for _, t := range m.Tables {
		cols := newColumnSet()
		for _, d := range t.Dimensions {
			cols.dimension(d, ldDimensionType(d, false))
		}
		for _, d := range t.TimeDimensions {
			cols.dimension(d, ldDimensionType(d, true))
		}
		f.Models = append(f.Models, ldModel{
			Name: t.Name, Description: t.Description, Columns: cols.list(),
		})
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w dialect/lightdash.go && go test ./dialect/ -run 'TestLightdash' -v`
Expected: PASS (skeleton and dimensions tests).

- [ ] **Step 5: Commit**

```bash
git add dialect/lightdash.go dialect/lightdash_test.go
git commit -m "feat(lightdash): emit dimensions as columns with meta.dimension"
```

---

### Task 3: Metrics (column-level simple, model-level derived, degrade)

Classifies each `Table.Metric`: a same-table simple aggregate over a `Col` becomes a column-level `meta.metrics` entry; a reference-only ratio over emitted same-table simple metrics becomes a model-level `type: number` metric with `${...}` SQL; everything else degrades to a note.

**Files:**
- Modify: `dialect/lightdash.go`
- Modify: `dialect/lightdash_test.go`

**Interfaces:**
- Consumes: `ir.Metric`, `ir.Agg`, `ir.Col`, `ir.Binary`, `ir.Ref`, `ir.Lit`, `ir.Expr`, `cortexDegrade`.
- Produces:
  - `func (s *ldColumnSet) metric(col, name string, met ldMetric)`
  - `func simpleColumnMetric(mt ir.Metric) (col string, met ldMetric, ok bool)`
  - `func ldAggType(fn string) (string, bool)`
  - `func renderLightdash(e ir.Expr) (string, bool)`
  - `func renderLightdashOperand(e ir.Expr) (string, bool)`
  - `func metricRefs(e ir.Expr) []string`
  - `func degradeNote(mt ir.Metric) string`

- [ ] **Step 1: Write the failing test**

Add to `dialect/lightdash_test.go`:

```go
func TestLightdashMetrics(t *testing.T) {
	// net_revenue = sum(amount)           -> column metric on amount
	// orders      = count_distinct(order_id) -> column metric on order_id
	// aov         = net_revenue / orders   -> model metric ${net_revenue}/${orders}
	// refunded    = sum(case ...) (Raw arg) -> degrades to a note
	// refund_rate = refunded / orders      -> degrades (ref to degraded metric)
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Metrics: []ir.Metric{
			{Name: "net_revenue", Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
			{Name: "orders", Def: ir.Agg{Func: "count_distinct", Table: "orders", Arg: ir.Col{Name: "order_id"}}},
			{Name: "aov", Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "net_revenue"}, Right: ir.Ref{Metric: "orders"}}},
			{Name: "refunded", Def: ir.Agg{Func: "sum", Table: "orders",
				Arg: ir.Raw{SQL: "case when is_refunded then 1 else 0 end", Columns: []string{"is_refunded"}}}},
			{Name: "refund_rate", Def: ir.Binary{Op: "/", Left: ir.Ref{Metric: "refunded"}, Right: ir.Ref{Metric: "orders"}}},
		},
	}}}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Meta struct {
				Metrics map[string]struct {
					Type string `yaml:"type"`
					SQL  string `yaml:"sql"`
				} `yaml:"metrics"`
			} `yaml:"meta"`
			Columns []struct {
				Name string `yaml:"name"`
				Meta struct {
					Metrics map[string]struct {
						Type string `yaml:"type"`
					} `yaml:"metrics"`
				} `yaml:"meta"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	colMetric := func(col, name string) (string, bool) {
		for _, c := range doc.Models[0].Columns {
			if c.Name == col {
				mm, ok := c.Meta.Metrics[name]
				return mm.Type, ok
			}
		}
		return "", false
	}
	if typ, ok := colMetric("amount", "net_revenue"); !ok || typ != "sum" {
		t.Errorf("net_revenue on amount: type=%q ok=%v, want sum true", typ, ok)
	}
	if typ, ok := colMetric("order_id", "orders"); !ok || typ != "count_distinct" {
		t.Errorf("orders on order_id: type=%q ok=%v, want count_distinct true", typ, ok)
	}
	aov, ok := doc.Models[0].Meta.Metrics["aov"]
	if !ok || aov.Type != "number" || aov.SQL != "${net_revenue} / ${orders}" {
		t.Errorf("aov = %+v, want type number sql ${net_revenue} / ${orders}", aov)
	}
	if _, ok := doc.Models[0].Meta.Metrics["refund_rate"]; ok {
		t.Errorf("refund_rate must NOT be emitted (references degraded metric)")
	}
	// both degraded metrics surface as notes.
	if !strings.Contains(got, "# - metric refunded not emitted") {
		t.Errorf("missing degrade note for refunded:\n%s", got)
	}
	if !strings.Contains(got, "# - metric refund_rate not emitted") {
		t.Errorf("missing degrade note for refund_rate:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestLightdashMetrics -v`
Expected: FAIL (no metrics emitted; no notes).

- [ ] **Step 3: Write minimal implementation**

Add to `dialect/lightdash.go`:

```go
// metric records a column-level Lightdash metric on col.
func (s *ldColumnSet) metric(col, name string, met ldMetric) {
	c := s.get(col)
	if c.Meta == nil {
		c.Meta = &ldColMeta{}
	}
	if c.Meta.Metrics == nil {
		c.Meta.Metrics = map[string]ldMetric{}
	}
	c.Meta.Metrics[name] = met
}

// simpleColumnMetric reports whether mt is a plain, unfiltered aggregate over a
// single column, and if so returns the backing column and the column-level
// Lightdash metric. A filtered agg, a count(*) (nil arg), a Raw arg, or an
// unmapped aggregate returns ok=false (the caller degrades it).
func simpleColumnMetric(mt ir.Metric) (string, ldMetric, bool) {
	agg, ok := mt.Def.(ir.Agg)
	if !ok || agg.Filter != nil {
		return "", ldMetric{}, false
	}
	typ, ok := ldAggType(agg.Func)
	if !ok {
		return "", ldMetric{}, false
	}
	col, ok := agg.Arg.(ir.Col)
	if !ok {
		return "", ldMetric{}, false
	}
	return col.Name, ldMetric{Type: typ}, true
}

// ldAggType maps an IR aggregation function to a Lightdash column-metric type.
func ldAggType(fn string) (string, bool) {
	switch strings.ToLower(fn) {
	case "sum":
		return "sum", true
	case "avg", "average":
		return "average", true
	case "count":
		return "count", true
	case "count_distinct":
		return "count_distinct", true
	case "min":
		return "min", true
	case "max":
		return "max", true
	case "median":
		return "median", true
	}
	return "", false
}

// renderLightdash renders a reference-only derived tree to Lightdash ${metric}
// syntax. Lightdash type: number metrics may reference only other metrics (and
// literals), so any node that is not a Ref/Lit/Binary makes the whole metric
// non-representable (ok=false) and the caller degrades it. Kept separate from
// renderSQL and renderDerived: each target has its own reference discipline
// (Cortex inlines SQL, dbt keeps bare measure refs, Lightdash uses ${...}).
func renderLightdash(e ir.Expr) (string, bool) {
	switch n := e.(type) {
	case ir.Ref:
		return "${" + n.Metric + "}", true
	case ir.Lit:
		return n.Value, true
	case ir.Binary:
		l, lok := renderLightdashOperand(n.Left)
		r, rok := renderLightdashOperand(n.Right)
		if !lok || !rok {
			return "", false
		}
		return l + " " + n.Op + " " + r, true
	default:
		return "", false
	}
}

// renderLightdashOperand renders a Binary operand, parenthesizing a nested
// Binary so operator grouping in the emitted SQL matches the AST.
func renderLightdashOperand(e ir.Expr) (string, bool) {
	s, ok := renderLightdash(e)
	if !ok {
		return "", false
	}
	if _, isBin := e.(ir.Binary); isBin {
		return "(" + s + ")", true
	}
	return s, true
}

// metricRefs returns the metric names a reference-only tree references.
func metricRefs(e ir.Expr) []string {
	switch n := e.(type) {
	case ir.Ref:
		return []string{n.Metric}
	case ir.Binary:
		return append(metricRefs(n.Left), metricRefs(n.Right)...)
	}
	return nil
}

// degradeNote explains why a metric was not emitted to Lightdash. Window and
// conversion metrics reuse cortexDegrade's wording; other misses are filtered
// or compound aggregates, or references that did not resolve to emitted
// same-table simple metrics.
func degradeNote(mt ir.Metric) string {
	if reason, ok := cortexDegrade(mt.Def); ok {
		return "metric " + mt.Name + " not emitted to Lightdash: " + reason
	}
	return "metric " + mt.Name + " not emitted to Lightdash: no Lightdash primitive (filtered/compound aggregate or unresolved reference)"
}
```

Now integrate metric classification into `Emit`'s table loop. Replace the table loop body from Task 2 with:

```go
	for _, t := range m.Tables {
		cols := newColumnSet()
		for _, d := range t.Dimensions {
			cols.dimension(d, ldDimensionType(d, false))
		}
		for _, d := range t.TimeDimensions {
			cols.dimension(d, ldDimensionType(d, true))
		}

		// Pass 1: which metrics become column-level simple metrics. Their names
		// are the only references a derived metric may use, so a ratio over a
		// degraded or cross-table metric degrades rather than dangling.
		simple := map[string]bool{}
		for _, mt := range t.Metrics {
			if _, _, ok := simpleColumnMetric(mt); ok {
				simple[mt.Name] = true
			}
		}

		mm := &ldModelMeta{}
		for _, mt := range t.Metrics {
			if col, met, ok := simpleColumnMetric(mt); ok {
				cols.metric(col, mt.Name, met)
				continue
			}
			if met, ok := derivedModelMetric(mt, simple); ok {
				if mm.Metrics == nil {
					mm.Metrics = map[string]ldMetric{}
				}
				mm.Metrics[mt.Name] = met
				continue
			}
			notes = append(notes, degradeNote(mt))
		}

		model := ldModel{Name: t.Name, Description: t.Description, Columns: cols.list()}
		if !mm.empty() {
			model.Meta = mm
		}
		f.Models = append(f.Models, model)
	}
```

Add the derived helper:

```go
// derivedModelMetric reports whether mt is a reference-only ratio/derived metric
// whose every referenced metric is an emitted same-table simple metric, and if
// so returns its model-level Lightdash form (type: number, ${...} sql).
func derivedModelMetric(mt ir.Metric, simple map[string]bool) (ldMetric, bool) {
	if _, ok := mt.Def.(ir.Binary); !ok {
		return ldMetric{}, false
	}
	sql, ok := renderLightdash(mt.Def)
	if !ok {
		return ldMetric{}, false
	}
	for _, r := range metricRefs(mt.Def) {
		if !simple[r] {
			return ldMetric{}, false
		}
	}
	return ldMetric{Type: "number", SQL: sql}, true
}
```

Note: `Emit` already declares `notes` before the loop (Task 1), so `notes = append(...)` inside the loop compiles. Confirm the comment-block writer (Task 1) still runs after the loop.

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w dialect/lightdash.go && go test ./dialect/ -run 'TestLightdash' -v`
Expected: PASS (skeleton, dimensions, metrics).

- [ ] **Step 5: Commit**

```bash
git add dialect/lightdash.go dialect/lightdash_test.go
git commit -m "feat(lightdash): column-level simple + model-level derived metrics, degrade to notes"
```

---

### Task 4: Joins and primary key

Emits `Table.PrimaryKey` (single column) as `meta.primary_key` and each `Relationship` owned by the table as a `meta.joins` entry. A composite primary key degrades to a note.

**Files:**
- Modify: `dialect/lightdash.go`
- Modify: `dialect/lightdash_test.go`

**Interfaces:**
- Consumes: `ir.Relationship`, `ir.ColumnPair`, `ir.Table.PrimaryKey`.
- Produces:
  - `func joinSQLOn(r ir.Relationship) string`

- [ ] **Step 1: Write the failing test**

Add to `dialect/lightdash_test.go` (also add `"fmt"` import only if not present; the test below does not need it):

```go
func TestLightdashJoinsAndPrimaryKey(t *testing.T) {
	m := &ir.Model{
		Tables: []ir.Table{
			{Name: "order_lines", PrimaryKey: []string{"order_line_id"}},
			{Name: "orders", PrimaryKey: []string{"order_id", "tenant_id"}}, // composite -> note
		},
		Relationships: []ir.Relationship{
			{Left: "order_lines", Right: "orders", Columns: []ir.ColumnPair{{Left: "order_id", Right: "order_id"}}},
		},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Name string `yaml:"name"`
			Meta struct {
				PrimaryKey string `yaml:"primary_key"`
				Joins      []struct {
					Join         string `yaml:"join"`
					SQLOn        string `yaml:"sql_on"`
					Relationship string `yaml:"relationship"`
				} `yaml:"joins"`
			} `yaml:"meta"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	byName := map[string]int{}
	for i, mdl := range doc.Models {
		byName[mdl.Name] = i
	}
	ol := doc.Models[byName["order_lines"]]
	if ol.Meta.PrimaryKey != "order_line_id" {
		t.Errorf("order_lines primary_key = %q, want order_line_id", ol.Meta.PrimaryKey)
	}
	if len(ol.Meta.Joins) != 1 {
		t.Fatalf("order_lines joins = %d, want 1", len(ol.Meta.Joins))
	}
	j := ol.Meta.Joins[0]
	if j.Join != "orders" || j.Relationship != "many-to-one" ||
		j.SQLOn != "${order_lines.order_id} = ${orders.order_id}" {
		t.Errorf("join = %+v", j)
	}
	// composite PK is not emitted and surfaces as a note.
	if doc.Models[byName["orders"]].Meta.PrimaryKey != "" {
		t.Errorf("composite PK must not be emitted")
	}
	if !strings.Contains(got, "composite primary key") {
		t.Errorf("missing composite-PK note:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestLightdashJoinsAndPrimaryKey -v`
Expected: FAIL (no primary_key/joins; no note).

- [ ] **Step 3: Write minimal implementation**

Add to `dialect/lightdash.go` (add `"fmt"` to imports):

```go
// joinSQLOn renders a relationship's equi-join columns as a Lightdash sql_on
// expression: ${left.col} = ${right.col}, ANDed for a composite key.
func joinSQLOn(r ir.Relationship) string {
	parts := make([]string, len(r.Columns))
	for i, cp := range r.Columns {
		parts[i] = "${" + r.Left + "." + cp.Left + "} = ${" + r.Right + "." + cp.Right + "}"
	}
	return strings.Join(parts, " and ")
}
```

In `Emit`, populate `mm` with the primary key and joins at the top of the per-table block, right after `mm := &ldModelMeta{}` is created. Move the `mm` creation above the metric loop if needed and add:

```go
		mm := &ldModelMeta{}
		if len(t.PrimaryKey) == 1 {
			mm.PrimaryKey = t.PrimaryKey[0]
		} else if len(t.PrimaryKey) > 1 {
			notes = append(notes, fmt.Sprintf(
				"table %s: composite primary key %v not emitted (Lightdash primary_key is a single column)",
				t.Name, t.PrimaryKey))
		}
		for _, r := range m.Relationships {
			if r.Left != t.Name {
				continue
			}
			mm.Joins = append(mm.Joins, ldJoin{
				Join: r.Right, SQLOn: joinSQLOn(r), Relationship: "many-to-one",
			})
		}
```

(The metric loop that also appends to `mm.Metrics` stays below this block; `mm.empty()` now correctly accounts for primary key and joins.)

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w dialect/lightdash.go && go test ./dialect/ -run 'TestLightdash' -v`
Expected: PASS (all four Lightdash unit tests).

- [ ] **Step 5: Commit**

```bash
git add dialect/lightdash.go dialect/lightdash_test.go
git commit -m "feat(lightdash): emit meta.joins and single-column primary_key"
```

---

### Task 5: meta.style toggle (meta vs config.meta) and profile plumbing

Threads a `MetaStyle` option from the `semglot.yaml` profile through to the emitter, and wraps each model/column `meta:` block under `config:` when `MetaStyle == "config.meta"`.

**Files:**
- Modify: `dialect/dialect.go` (add `MetaStyle` to `Options`)
- Modify: `cmd/semglot/config.go` (add to `profile`, `buildSpec`, `loadProfile`)
- Modify: `cmd/semglot/main.go` (pass through `WithOptions`)
- Modify: `dialect/lightdash.go` (wrap meta under config when configMeta)
- Modify: `dialect/lightdash_test.go`

**Interfaces:**
- Consumes: `Options`, `buildSpec`, `profile`.
- Produces: `Options.MetaStyle string`; `profile.MetaStyle` (`yaml:"meta-style"`); `buildSpec.MetaStyle`.

- [ ] **Step 1: Write the failing test**

Add to `dialect/lightdash_test.go`:

```go
func TestLightdashConfigMetaStyle(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		PrimaryKey: []string{"order_id"},
		Dimensions: []ir.Field{{Name: "status", Expr: "status", DataType: "varchar"}},
		Metrics: []ir.Metric{
			{Name: "net_revenue", Def: ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Name: "amount"}}},
		},
	}}}

	// default style: meta directly under model and column.
	def := emitLightdash(t, m, Options{Name: "ecommerce"})
	if !strings.Contains(def, "\n    meta:\n") {
		t.Errorf("default style should nest under meta:, got:\n%s", def)
	}
	if strings.Contains(def, "config:") {
		t.Errorf("default style must not emit config:, got:\n%s", def)
	}

	// config.meta style: meta nested under config.
	got := emitLightdash(t, m, Options{Name: "ecommerce", MetaStyle: "config.meta"})
	var doc struct {
		Models []struct {
			Config struct {
				Meta struct {
					PrimaryKey string `yaml:"primary_key"`
				} `yaml:"meta"`
			} `yaml:"config"`
			Meta struct {
				PrimaryKey string `yaml:"primary_key"`
			} `yaml:"meta"`
			Columns []struct {
				Config struct {
					Meta struct {
						Dimension struct {
							Type string `yaml:"type"`
						} `yaml:"dimension"`
					} `yaml:"meta"`
				} `yaml:"config"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	if doc.Models[0].Config.Meta.PrimaryKey != "order_id" {
		t.Errorf("config.meta model primary_key = %q, want order_id\n%s", doc.Models[0].Config.Meta.PrimaryKey, got)
	}
	if doc.Models[0].Meta.PrimaryKey != "" {
		t.Errorf("config.meta style must not also emit top-level meta")
	}
	if doc.Models[0].Columns[0].Config.Meta.Dimension.Type != "string" {
		t.Errorf("config.meta column dimension type = %q, want string\n%s",
			doc.Models[0].Columns[0].Config.Meta.Dimension.Type, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestLightdashConfigMetaStyle -v`
Expected: FAIL (config.meta not produced; `Options` has no `MetaStyle` so it will not compile).

- [ ] **Step 3: Write minimal implementation**

In `dialect/dialect.go`, add a field to `Options` (after `Description`):

```go
	// MetaStyle selects where Lightdash meta lives: "" / "meta" nests under
	// meta: (dbt <=1.9); "config.meta" nests under config.meta: (dbt 1.10+).
	MetaStyle string
```

In `dialect/lightdash.go` `Emit`, after building each `model` (and before appending to `f.Models`), rewrap meta under config when requested. Compute `configMeta := l.MetaStyle == "config.meta"` once near the top of `Emit`, and replace `model.Meta = mm` / column assembly with:

```go
		model := ldModel{Name: t.Name, Description: t.Description, Columns: cols.list()}
		if !mm.empty() {
			model.Meta = mm
		}
		if configMeta {
			if model.Meta != nil {
				model.Config = &ldModelCfg{Meta: model.Meta}
				model.Meta = nil
			}
			for i := range model.Columns {
				if model.Columns[i].Meta != nil {
					model.Columns[i].Config = &ldColCfg{Meta: model.Columns[i].Meta}
					model.Columns[i].Meta = nil
				}
			}
		}
		f.Models = append(f.Models, model)
```

In `cmd/semglot/config.go`, add `MetaStyle string \`yaml:"meta-style"\`` to `profile` (after `Description`), add `MetaStyle string` to `buildSpec`, and copy it in `loadProfile`'s `spec := buildSpec{...}` literal:

```go
		MetaStyle:     p.MetaStyle,
```

In `cmd/semglot/main.go`, add to the `WithOptions` call:

```go
			MetaStyle:   spec.MetaStyle,
```

- [ ] **Step 4: Run test to verify it passes**

Run: `gofmt -w dialect/lightdash.go dialect/dialect.go cmd/semglot/config.go cmd/semglot/main.go && go test ./... `
Expected: PASS (all packages, including the new config.meta test).

- [ ] **Step 5: Commit**

```bash
git add dialect/lightdash.go dialect/dialect.go cmd/semglot/config.go cmd/semglot/main.go dialect/lightdash_test.go
git commit -m "feat(lightdash): meta-style profile option toggling meta vs config.meta"
```

---

### Task 6: End-to-end golden over the ecommerce fixture

Wires the `lightdash` target into the integration runner with a structure test and a pinned golden `schema.yml`, exactly like the nao-yaml and snowflake-semantic-view goldens.

**Files:**
- Modify: `test/context_layer_test.go`
- Create (via UPDATE_GOLDEN): `test/models/ecommerce/dbt/lightdash/schema.yml`

**Interfaces:**
- Consumes: `emitTarget` helper, `sourceDirs` (both already in the `integration_test` package).

- [ ] **Step 1: Write the failing test**

Append to `test/context_layer_test.go`:

```go
func TestLightdashStructure(t *testing.T) {
	got := emitTarget(t, "lightdash", "schema.yml")
	for _, want := range []string{
		"version: 2",
		"- name: fct_orders",
		"net_revenue:",         // a column-level simple metric name
		"type: sum",            // its aggregation
		"aov:",                 // a model-level derived metric
		"${net_revenue} / ${orders}",
		"joins:",
		"${fct_order_lines.order_id} = ${fct_orders.order_id}",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("schema.yml missing %q", want)
		}
	}
}

// lightdashGoldenPath is the pinned lightdash schema.yml, generated with
// UPDATE_GOLDEN=1 and eyeballed for a well-formed Lightdash dbt schema.
const lightdashGoldenPath = "models/ecommerce/dbt/lightdash/schema.yml"

// TestLightdashGolden pins the full emitted schema.yml, mirroring
// TestNaoYamlGolden's shape.
func TestLightdashGolden(t *testing.T) {
	got := emitTarget(t, "lightdash", "schema.yml")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(lightdashGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lightdashGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(lightdashGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("schema.yml output != golden:\n--- got ---\n%s", got)
	}
}
```

- [ ] **Step 2: Run structure test to verify it fails**

Run: `go test ./test/ -run TestLightdashStructure -v`
Expected: FAIL. If any `want` substring is absent, first confirm it against a manual emit (next step generates the golden) and adjust the substring to match the real fixture metric/table names (the fixture's canonical names are `fct_orders`, `fct_order_lines`, `net_revenue`, `orders`, `aov`, per the existing nao/SV tests). Do NOT weaken the test to pass trivially.

- [ ] **Step 3: Generate the golden and eyeball it**

Run: `UPDATE_GOLDEN=1 go test ./test/ -run TestLightdashGolden`
Then Read `test/models/ecommerce/dbt/lightdash/schema.yml` and verify:
- `version: 2` header, one model per fact table.
- Column-level metrics sit under the correct backing column.
- `aov` is a model-level `type: number` with `sql: ${net_revenue} / ${orders}`.
- Cross-table / filtered metrics (e.g. `units_per_order`, `refunded_orders`, `refund_rate`) are absent from `metrics:` and each appears as a `# - metric ... not emitted` note.
- No dangling `${...}` reference points at a metric that is not defined in the file.

If the output is malformed, fix `dialect/lightdash.go` and regenerate before proceeding.

- [ ] **Step 4: Run the full suite to verify it passes**

Run: `go test ./...`
Expected: PASS (structure + golden + all prior tests). Also run `go vet ./...` and `gofmt -l dialect cmd test` (expect no files listed).

- [ ] **Step 5: Commit**

```bash
git add test/context_layer_test.go test/models/ecommerce/dbt/lightdash/schema.yml
git commit -m "test(lightdash): end-to-end structure + golden over ecommerce fixture"
```

---

### Task 7: README dialect note

Adds Lightdash to the README's supported-target list so the CLI's advertised dialects match reality.

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Locate the target list**

Run: `grep -n "cortex\|target-type\|dialect" README.md`
Read the surrounding lines to find where existing targets are listed.

- [ ] **Step 2: Add Lightdash**

Add `lightdash` to the enumerated targets with a one-line description, matching the existing style, for example: "lightdash: dbt schema.yml with Lightdash meta: blocks (dimensions, metrics, joins)." Use no em-dashes; keep one line per paragraph.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: list the lightdash target in the README"
```

---

## Self-Review Notes

- Spec coverage: target-only emit (Task 1), output `schema.yml` version 2 (Task 1), dimensions with type mapping + enum/synonym fold (Task 2), simple + derived metrics with honest degrade (Task 3), joins + single/composite primary key (Task 4), meta-style toggle plumbed like `ViewSchema` (Task 5), unit + e2e golden tests (Tasks 1-6), out-of-scope items (colors, filters, Lightdash flat YAML, formatting) intentionally omitted.
- The dangling-reference risk (a derived metric referencing a degraded metric) is handled by the `simple` set gate in Task 3's `derivedModelMetric`.
- `time_intervals` is intentionally not emitted (the IR carries no interval list; Lightdash auto-generates them).
- Types/signatures are consistent across tasks: `ldColumnSet`/`get`/`dimension`/`metric`/`list`, `simpleColumnMetric`, `derivedModelMetric`, `renderLightdash`, `ldModelMeta.empty`, `Options.MetaStyle`.
