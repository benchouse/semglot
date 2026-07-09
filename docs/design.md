# semglot — semantic-layer transpiler & context fairness index — Design

**Date:** 2026-07-09
**Status:** Approved design, pending implementation plan
**Repo:** standalone, `github.com/benchouse/semglot` (MIT)

## Problem

Analytics tools each consume their own **semantic-layer dialect** — dbt semantic
models, Snowflake Cortex semantic YAML, and others — describing the same warehouse
in mutually incompatible formats. Two gaps:

1. **No transpiler.** A semantic layer authored once (e.g. as a dbt project) has to
   be re-authored by hand for every other tool. We want to **transpile** one source
   dialect into the others.
2. **No fairness measure.** When two systems are handed *different* dialect files
   for the same warehouse, there is no way to say whether they received *equivalent*
   information. A layer that silently drops a metric, or adds bonus synonyms, is not
   an equal-footing comparison. We want a **context fairness index**: how much more
   or less information a target layer carries versus a reference.

`semglot` is a Go CLI (in the `sqlglot` lineage — "-glot" = speaks many dialects)
that does both: **transpile** a source dialect → a target dialect, and **score** a
target dialect against a reference.

**Motivating use case (external):** an LLM-agent eval harness that grades agents
each given a different tool's context, and needs both to generate those contexts
from one source of truth and to prove they carry equivalent information. semglot
itself stays ignorant of that harness — see "Standalone" below.

## Settled decisions

- **Standalone & open-sourceable.** Its own repo/module (`github.com/benchouse/
  semglot`), MIT-licensed. **Zero knowledge of any consumer.** All inputs are
  `--flag` paths; fixtures are synthetic and public; no consumer-specific paths,
  formats, or dependencies. A consumer (e.g. an eval harness) integrates by
  **invoking the `semglot` binary**, not by semglot reaching into it.
- **Direction (v1):** one source dialect (dbt) → many target dialects. First real
  target: **Snowflake Cortex** semantic model.
- **Fairness index method:** **structural element coverage** — enumerate typed,
  normalized information elements on both sides and set-diff them. Deterministic,
  auditable, tells you exactly what is missing or extra. (Rejected: bit-counting —
  can't say *which* facts differ; LLM-judged equivalence — non-deterministic, hard
  to defend as "fairness".)
- **Internal representation (IR) is load-bearing, not optional.** Scoring must parse
  *both* reference and target into one shared model to diff them, so every dialect
  is **bidirectional**: `Parse` dialect→IR *and* `Emit` IR→dialect.
- **Every dialect is a `Layer`, including the source.** dbt is not special-cased; it
  is a `Layer` in the registry that implements `Parse` now (`Emit` deferred). This
  makes future **many→many** transpilation fall out for free (hub-and-spoke:
  `M + N` layers, never `M × N` converters) with no core rewrite — v1 just defaults
  `--from` to `dbt`.
- **The IR is a rich superset and it defines "information".** The IR is the union of
  dialect concepts, not a least-common-denominator. Anything outside the IR is, by
  definition, not counted by the fairness index — so the transpiler and the scorer
  share one source of truth for what a unit of information is.
- **Importable, not `internal/`.** Core logic lives in exported packages (`ir`,
  `layer`, `score`) so downstream Go code can embed semglot as a library in addition
  to shelling out to the binary.
- **House style:** subcommands via `flag.NewFlagSet`, no cobra; thin `cmd`, logic in
  the exported packages; no non-stdlib deps beyond a YAML parser
  (`gopkg.in/yaml.v3`).

## Architecture

```
                 ┌──────────────┐   Emit    ┌──────────────────┐
  dbt reference  │              │──────────▶│  ./cortex/*.yaml  │   (build)
  (Layer.Parse)  │  neutral IR   │           └──────────────────┘
        ─────────▶│  (pkg ir)    │
                 │              │◀──────────  cortex target (Layer.Parse)
                 └──────┬───────┘   Parse
                        │
                        ▼
                 element set-diff  ──▶  fairness index + missing/excess   (score)
```

### Packages

- `cmd/semglot/main.go` — subcommand dispatch (`build`, `score`), flag wiring only.
- `ir/` — the neutral model types + element enumeration (`package ir`).
- `layer/` — `Layer` interface + `registry` (`layer.go`), `dbt.go`, `cortex.go`.
- `score/` — element diff + fairness index (`package score`).
- `testdata/` — synthetic dbt + Cortex fixtures and goldens.

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
    Expr     string    // underlying column/expression, for cross-dialect identity
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

The IR is deliberately a **superset**: it carries descriptions, data types,
synonyms, and underlying `Expr` even when a given dialect omits them, because those
are exactly the information elements the fairness index measures.

### The Layer interface (`layer`)

```go
type Layer interface {
    Name() string
    Parse(dir string) (*ir.Model, error)  // dialect files → IR   (needed for score)
    Emit(m *ir.Model, dir string) error   // IR → dialect files    (needed for build)
}
```

- `dbt` Layer: `Parse` reads a directory of dbt semantic YAML — `semantic_models`
  blocks (`*.yml`) plus a `metrics` file. `Emit` returns `ErrNotImplemented` in v1.
- `cortex` Layer: `Parse` reads a Cortex `semantic_model.yaml`; `Emit` writes it.
- A `registry` maps a dialect name string to a `Layer`. dbt is registered too, so
  `--from dbt` resolves through the same path as any other dialect.

`build` = `registry[from].Parse(reference) → registry[layer].Emit(out)`.
`score` = diff( `registry[from].Parse(reference)`, `registry[layer].Parse(target)` ).

## The context fairness index (`score`)

Both IR models are flattened into a **set of typed, normalized element keys**:

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

Let **R** = reference element set, **T** = target element set.

- **coverage (recall)** = |R ∩ T| / |R| — how much reference information the layer keeps.
- **excess** = |T \ R| — information the target adds beyond the reference.
- **fairness index** = |R ∩ T| / |R ∪ T| — **Jaccard**, the headline in `[0,1]`.
  `1.0` = exact information parity (nothing missing, nothing extra). It penalizes
  **both** loss and addition — precisely "fair", i.e. every layer conveys equivalent
  information. The **direction** ("more or less") is reported by the explicit
  **missing** (`R \ T`) and **excess** (`T \ R`) lists.

**Weighting:** element categories are **equal-weight by default**; a per-category
weights map is configurable (so descriptions can be down-weighted relative to
metrics). The weighted index sums weights instead of counting set members.

### Cross-dialect element identity — the main risk

Reference `order_net_booked_amount` (a measure) must be recognized as the same
element as Cortex `order_net_booked` (a fact) / `net_revenue` (a metric on it).
Normalization of an element's identity key:

1. lowercase; snake/camel-fold.
2. strip aggregation suffixes (`_amount`, `_count`, `_total`) on measures.
3. when both sides record an underlying `Expr`/column, match on that first; fall
   back to normalized name.

Imperfect matches are the known limitation. Mitigation: `score --json` emits the raw
**matched / missing / excess** sets so every classification is auditable, and the
normalization rules live in one testable function.

## CLI

```
semglot build --from dbt --reference <dir> --layer cortex --out <dir>
    Transpile the source dialect at --reference into --layer, writing to --out.
    --from defaults to dbt.

semglot score --from dbt --reference <dir> --layer cortex --target <dir> \
              [--json] [--weights <file>]
    Diff the target dialect against the reference and print the fairness index.
    --json emits { index, coverage, excess, missing[], extra[], byCategory{} }.
```

`--reference` and `--target` are plain directories the caller supplies; semglot
resolves nothing implicitly and reads no consumer-specific config. Missing/invalid
input fails fast with a clear message.

## Testing (TDD)

- **Unit — normalization:** the element-key normalizer against a table of
  reference/cortex name pairs that must and must not match.
- **Unit — IR round-trip:** `cortex.Emit` then `cortex.Parse` reproduces the IR
  (idempotence of the Cortex dialect through the hub).
- **Golden — synthetic fixtures:** a hand-built dbt + Cortex pair under `testdata/`
  (a small e-commerce-style model), with a golden IR and an expected fairness index;
  include a deliberately **lossy** target to assert coverage < 1 and a non-empty
  missing list, and an **excess** target to assert extra > 0.
- `go test ./...` and `go build ./...` green from the first commit.

## Scope

**In (v1):** repo scaffolding (go.mod, LICENSE, README, .gitignore); the `ir`,
`layer`, `score` packages; dbt reference `Parse`; Cortex `Parse`+`Emit`; the scorer
(Jaccard index + coverage + missing/excess + `--json` + weights); `build` and
`score` commands; the tests above.

**Out (deferred, unlocked by the same seams):** nao / supersimple / semviews /
prose-rules Layers; dbt `Emit`; full many→many via additional registered layers;
GitHub Actions CI; publishing/tagging. Consumer integration (an eval harness
invoking the binary) lives entirely in that consumer, not here.

## Open questions

None blocking. Weight defaults (all 1.0) and the exact suffix list for measure
normalization may be tuned once the golden tests run against realistic fixtures.
