# semglot — semantic-layer transpiler & context fairness index — Design

**Date:** 2026-07-09
**Status:** Approved design, pending implementation plan
**Repo:** standalone, `github.com/benchouse/semglot` (MIT)
**Scope:** **v1 = transpiler** (`build`). **v2 = context fairness index** (`score`) —
designed here for continuity, but explicitly deferred.

## Problem

Analytics tools each consume their own **semantic-layer dialect** — dbt semantic
models, Snowflake Cortex semantic YAML, and others — describing the same warehouse
in mutually incompatible formats. Two gaps:

1. **No transpiler.** A semantic layer authored once (e.g. as a dbt project) has to
   be re-authored by hand for every other tool. We want to **transpile** one source
   dialect into the others. *(v1.)*
2. **No fairness measure.** When two systems are handed *different* dialect files
   for the same warehouse, there is no way to say whether they received *equivalent*
   information. We want a **context fairness index**: how much more or less
   information a target layer carries versus a reference. *(v2.)*

`semglot` is a Go CLI (in the `sqlglot` lineage — "-glot" = speaks many dialects).
v1 **transpiles** a source dialect → a target dialect; v2 adds **scoring** of a
target against a reference.

**Motivating use case (external):** an LLM-agent eval harness that grades agents
each given a different tool's context, and needs both to generate those contexts
from one source of truth and (later) to prove they carry equivalent information.
semglot itself stays ignorant of that harness — see "Standalone" below.

## Settled decisions

- **Standalone & open-sourceable.** Its own repo/module (`github.com/benchouse/
  semglot`), MIT-licensed. **Zero knowledge of any consumer.** All inputs are
  `--flag` paths; fixtures are synthetic and public; no consumer-specific paths,
  formats, or dependencies. A consumer integrates by **invoking the `semglot`
  binary**, not by semglot reaching into it.
- **Direction (v1):** one source dialect (dbt) → many target dialects. First (and
  only v1) target: **Snowflake Cortex** semantic model.
- **A neutral IR is the pivot.** Transpilation routes `source → IR → target`, not
  via direct per-pair converters. Justified even for a single v1 pair because it is
  the seam for **many→many** later (hub-and-spoke: `M + N` layers, never `M × N`
  converters) and because v2 scoring will diff two dialects on this shared model.
  The IR is a **rich superset** (union of dialect concepts), not a
  least-common-denominator — and in v2 it doubles as the definition of "what
  information means".
- **Every dialect is a `Layer`, and declares its capabilities.** Rather than one
  fat interface with unimplemented stubs, a `Layer` has a `Name()` plus optional
  **capability interfaces**: `Parser` (dialect→IR) and `Emitter` (IR→dialect). v1:
  **dbt implements `Parser`**, **cortex implements `Emitter`**. v2 adds a cortex
  `Parser` (for scoring) and, when wanted, a dbt `Emitter`. dbt is a registered
  Layer like any other, so `--from dbt` is not special-cased.
- **Fairness index method (v2):** structural element coverage — deterministic,
  auditable set-diff of typed information elements. (Rejected: bit-counting;
  LLM-judged equivalence.) Full v2 design retained below under "Deferred".
- **Importable, not `internal/`.** Core logic lives in exported packages so
  downstream Go code can embed semglot as a library in addition to shelling out.
- **House style:** subcommands via `flag.NewFlagSet`, no cobra; thin `cmd`, logic in
  exported packages. The **only** non-stdlib dependency is `gopkg.in/yaml.v3`.
  Column references inside compound expressions (`col` → `table.col` /
  `{col}`) are rewritten with a tiny in-repo SQL-expression lexer
  (`layer/sqllex.go`) — it splits an expression into identifier / string /
  number / other tokens and callers rewrite only identifiers that are known
  columns. It deliberately does **not** classify keywords (that removes the
  "incomplete keyword table" bug class — a word like `WHEN` is simply an
  identifier that is not a column). A full SQL parser was rejected as
  over-weight and dialect-mismatched (no pure-Go Snowflake parser exists), and
  the earlier third-party lexer was dropped in favor of this ~60-line,
  zero-dependency tokenizer.

## Architecture (v1)

```
  dbt reference dir            ┌──────────────┐   Emit    ┌──────────────────┐
  (dbt.Parse: Parser) ───────▶│  neutral IR   │──────────▶│  ./cortex/*.yaml  │
                              │  (package ir) │  cortex   └──────────────────┘
                              └──────────────┘  (Emitter)
                    build = registry[from].Parse → registry[layer].Emit
```

### Packages (v1)

- `cmd/semglot/main.go` — subcommand dispatch (`build`; `score` is a v2 stub that
  prints "not implemented yet"), flag wiring only.
- `ir/` — neutral model types (`package ir`).
- `layer/` — `Layer`/`Parser`/`Emitter` interfaces + `registry` (`layer.go`),
  `dbt.go` (Parser), `cortex.go` (Emitter).
- `testdata/` — synthetic dbt fixtures + golden Cortex output.
- `score/` — **v2, not created in v1.**

### The neutral IR (`ir`)

```go
type Model struct {
    Tables        []Table
    Relationships []Relationship
}
type Table struct {
    Name, Description string
    Dimensions     []Field    // categorical / plain
    TimeDimensions []Field
    Measures       []Measure  // aggregatable facts
    Metrics        []Metric
}
type Field struct {
    Name, Description, DataType string
    Expr     string    // underlying column/expression (identity + emit)
    Synonyms []string
}
type Measure struct {
    Field
    Agg string          // sum / count / count_distinct, where the dialect records it
}
type Metric struct {
    Name, Description, Expr string   // e.g. SUM(...), ratio numerator/denominator
    Synonyms []string
}
type Relationship struct {
    Left, Right string
    Columns []ColumnPair
}
```

The IR carries descriptions, data types, synonyms, and underlying `Expr` even when a
dialect omits them — v1 uses them to emit the richest possible Cortex output; v2
uses the same fields as the units the fairness index measures.

### Interfaces (`layer`)

```go
type Layer interface { Name() string }

type Parser  interface { Layer; Parse(dir string) (*ir.Model, error) }  // dialect → IR
type Emitter interface { Layer; Emit(m *ir.Model, dir string) error }   // IR → dialect
```

- `dbt` (`Parser`): reads a directory of dbt YAML and **merges two sources of
  truth** into the IR:
  - **model properties** (`models:` with `columns:`) — table/column
    descriptions, real `data_type`, primary keys (`constraints`), and
    relationships (`relationships` data tests / foreign-key constraints);
  - **the semantic layer** (`semantic_models:` + `metrics:`) — measures,
    aggregations, entities, and metrics.

  Either may appear alone: a `models:`-only project (the common case — many orgs
  never adopt the semantic layer) yields tables, dimensions, keys and
  relationships (no facts/metrics); a `semantic_models:`-only project yields
  measures/metrics with inferred types. When both describe a model they merge by
  model/column name — model properties supply descriptions and **real data
  types** (so type inference is only a fallback), the semantic layer supplies
  roles (dimension vs measure) and aggregations.

  A metric resolves to a table via its measure's owning semantic model
  (`metric → measure → semantic_model → table`) and may combine measures across
  joinable tables (cross-table ratios). When a metric **can't** be resolved
  (references a measure no parsed semantic model defines) or uses a type we don't
  model (`derived`, `cumulative`, …), it is **not** guessed onto a table — it is
  flagged on stderr and passed through into the target's free-text guidance
  (Cortex `custom_instructions`) via `ir.Model.Notes`, so the information still
  reaches the downstream engine even when we can't structure it.
- `cortex` (`Emitter`): writes a Cortex `semantic_model.yaml` from the IR.
- `registry` maps a dialect name → `Layer`. `build` looks up `--from` and asserts it
  is a `Parser`, looks up `--layer` and asserts it is an `Emitter`; a clear error if
  a requested capability is absent (e.g. "cortex cannot be a --from source in v1").

`build` = `registry[from].(Parser).Parse(reference) → registry[layer].(Emitter).Emit(out)`.

## CLI (v1)

```
semglot build --from dbt --reference <dir> --layer cortex --out <dir>
    Transpile the source dialect at --reference into --layer, writing to --out.
    --from defaults to dbt.

semglot score ...    # v2 — prints "score is not implemented yet (v2)" and exits non-zero
```

`--reference` and `--out` are plain paths the caller supplies; semglot resolves
nothing implicitly and reads no consumer-specific config. Missing/invalid input
fails fast with a clear message.

## Testing (v1, TDD)

- **Unit — dbt.Parse:** a synthetic dbt semantic YAML fixture parses into an
  expected IR (`ir.Model` golden), covering tables, dimensions, time dimensions,
  measures (incl. agg), metrics (simple + ratio), and relationships.
- **Unit — cortex.Emit:** a hand-built IR emits byte-for-byte expected Cortex YAML
  (golden file), covering descriptions, data types, synonyms, facts, metrics,
  relationships.
- **End-to-end — build:** `dbt fixture dir → build → cortex YAML` equals the golden
  Cortex output; run via the `layer` packages (and once through `cmd` arg parsing).
- `go test ./...` and `go build ./...` green from the first code commit.

## Scope

**In (v1):** repo scaffolding (done: go.mod, LICENSE, README, .gitignore); the `ir`
and `layer` packages; dbt `Parser`; cortex `Emitter`; the `build` command; a `score`
stub that exits with a "v2" message; the tests above.

**Out (v2 and beyond):** the `score` package and everything under "Deferred" below
(cortex `Parser`, the fairness index, `--json`, weights, round-trip tests); nao /
supersimple / semviews / prose-rules Layers; dbt `Emitter`; full many→many; CI /
publishing. Consumer integration (an eval harness invoking the binary) lives in that
consumer, not here.

---

## Deferred — v2: the context fairness index (`score`)

Retained so v1's IR/interface choices stay coherent with where this is going. **Not
built in v1.**

To score, cortex gains a `Parser` (Cortex YAML → IR), so both reference and target
resolve to the IR and can be diffed. Both IR models flatten into a **set of typed,
normalized element keys**:

```
table:fct_orders
description:table:fct_orders                    # a description is present
dimension:fct_orders.is_refunded
timedimension:fct_orders.order_date
measure:fct_orders.order_net_booked
metric:net_revenue
metric.expr:net_revenue                         # the metric's math is present
synonym:fct_orders.order_net_booked:"net revenue"
datatype:fct_orders.order_id:NUMBER
relationship:fct_order_lines->fct_orders
```

Let **R** = reference set, **T** = target set.

- **coverage (recall)** = |R ∩ T| / |R|.
- **excess** = |T \ R|.
- **fairness index** = |R ∩ T| / |R ∪ T| — Jaccard headline in `[0,1]`; `1.0` = exact
  information parity. Penalizes both loss and addition. Direction ("more or less")
  comes from the printed **missing** (`R \ T`) and **excess** (`T \ R`) lists.

**Weighting:** per-category weights, equal (1.0) by default. **Cross-dialect element
identity** is the main risk: normalize keys (lowercase, snake/camel-fold, strip agg
suffixes like `_amount`/`_count`), match on underlying `Expr`/column first, then
name; `score --json` emits the raw matched/missing/excess sets so every
classification is auditable.

**v2 CLI:**
```
semglot score --from dbt --reference <dir> --layer cortex --target <dir> \
              [--json] [--weights <file>]
```

## Open questions

None blocking v1. (v2: weight defaults and the measure-suffix list, tuned against
realistic fixtures.)
