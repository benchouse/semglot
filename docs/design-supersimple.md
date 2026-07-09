# semglot — supersimple emitter + IR metric enrichment — Design

**Date:** 2026-07-09
**Status:** Approved design, pending implementation plan
**Builds on:** `docs/design.md` (v1 dbt → Cortex). This adds a second target dialect
and enriches the neutral IR so metrics are structured, not Cortex-shaped SQL.

## Problem

semglot has one emitter (Cortex). Adding **supersimple** as a second target both
delivers a new context layer and forces the IR to become a genuine neutral hub:
supersimple wants **structured** metrics (`aggregation: {type, key}`), but
`ir.Metric` currently holds only a pre-rendered SQL string (`sum(fct_orders.…)`),
which is Cortex-flavored. This design enriches the IR once, so this and future
emitters read structure rather than re-parsing SQL.

A survey of the other target formats (nao-yaml, nao-semviews, nao-rules/facts,
nao-local, cortex, supersimple) confirmed the IR **core is already sufficient**
(tables, typed columns + descriptions, PKs, relationships, dims vs measures,
simple + ratio metrics, synonyms, free-text guidance). The only faithfully
dbt-derivable gaps worth closing now are: **structured metrics**, the dbt metric
**`label`**, and the table **grain** (`agg_time_dimension`). Everything else future
formats add is either not derivable from dbt (row counts, query telemetry, sample
rows, Enum values, currency formats — these are *warehouse-profiling* outputs, not
dbt), emitter-side rendering (semantic-view DDL, markdown prose), or prose that
already rides in descriptions/`Notes`.

## Settled decisions

- **supersimple is the second target.** Emit-only in v1 (`Parse` deferred, like Cortex).
- **Enrich the IR now (the right thing), not a per-emitter hack.** Add structured
  metric fields + `Label` + table `Grain`. Cortex is **unchanged** (keeps rendering
  from `Expr`) and its golden must not move — a regression guard.
- **Multi-file output.** supersimple is one YAML file per model. `Emit(m, dir)`
  already takes a dir; no interface change.
- **Faithful-from-dbt only; document what dbt cannot provide.** `Enum`, currency
  `format`, row counts, telemetry, sample rows are omitted (not invented).
- **Honor "don't silently drop."** supersimple has no model-level instructions
  field, so metrics it cannot represent (ratio/derived, or simple metrics over a
  compound column) are **omitted from the file but reported on stderr** via
  `ir.Model.Notes` (the CLI prints Notes after `Emit`).

## IR changes (`ir/model.go`)

```go
type Table struct {
	// ...existing...
	Grain string // default time-dimension name (dbt defaults.agg_time_dimension); "" if none
}

type Metric struct {
	Name        string
	Label       string // dbt metric label (display name); "" if none
	Description string
	Expr        string // rendered SQL (Cortex renders from this)
	Synonyms    []string
	// Structured form, for emitters that want structure (supersimple, nao-yaml…):
	Kind        string // "simple" | "ratio"
	Agg         string // simple: sum | count | count_distinct | avg | min | max
	Table       string // owning table (model) name
	Column      string // simple: aggregated column (bare) or the raw expr if compound
	Numerator   string // ratio: numerator metric name
	Denominator string // ratio: denominator metric name
}
```

`Grain` is captured for nao-yaml later; supersimple/cortex ignore it. The structured
metric fields make each emitter render its own target form from one source of truth.

## dbt parser changes (`layer/dbt.go`)

- Parse `label:` on metrics and `defaults: {agg_time_dimension: …}` on semantic models:
  ```go
  type dbtMetric struct { /* ... */ Label string `yaml:"label"` }
  type dbtSemanticModel struct {
  	/* ... */
  	Defaults struct{ AggTimeDimension string `yaml:"agg_time_dimension"` } `yaml:"defaults"`
  }
  ```
- Set `Table.Grain = sm.Defaults.AggTimeDimension` in the per-table loop.
- Track each measure's `Agg` and bare column: `measureAgg[name]`, `measureCol[name]`
  (in addition to the existing `measureAggExpr`, `measureTable`).
- Populate the structured metric fields when attaching:
  - simple → `Kind:"simple", Agg:measureAgg[meas], Table:table, Column:measureCol[meas], Label:m.Label`
  - ratio  → `Kind:"ratio", Table:numeratorTable, Numerator:…, Denominator:…, Label:m.Label`
- **Cortex emitter unchanged** — still uses `Expr`.

## supersimple emitter (`layer/supersimple.go`, `Emitter` registered as `supersimple`)

One `<out>/<UPPER(name)>.yaml` per `ir.Table`:

```yaml
# yaml-language-server: $schema=https://assets.supersimple.io/configuration_schema/1.0.0/supersimple_configuration_schema.json
models:
  FCT_ORDERS:
    name: Orders                       # prettify(name): strip fct_/dim_/obt_ prefix, capitalize; fallback = name
    table: MAIN.FCT_ORDERS             # <schema>.<UPPER(name)>  (--schema, default MAIN; no database)
    primary_key: [ORDER_ID]
    description: <table description>
    properties:                        # every column, deduped
      ORDER_ID: {name: ORDER_ID, type: Number, description: <col desc>}
      ...
    relations:                         # on the PARENT side (hasMany), from IR relationships
      order_lines:
        name: Order lines
        type: hasMany
        model_id: FCT_ORDER_LINES
        join_strategy: {join_key: ORDER_ID}
metrics:                               # only this model's emittable metrics; omitted if none
  net_revenue:
    name: Net revenue                  # metric.Label, fallback metric.Name
    model_id: FCT_ORDERS
    aggregation: {type: sum, key: ORDER_NET_BOOKED}
```

**Mapping details:**
- **properties** = union of the table's `Dimensions`, `TimeDimensions`, and measure
  columns *that are bare identifiers*, deduped by column name (entity/dimension
  columns already cover most). A compound measure expr (e.g. `case when …`) is not a
  column and is not emitted as a property; its referenced columns are already present
  as dimensions.
- **property type** (supersimple vocabulary) from the IR `DataType` when present,
  else inferred by name/role:
  `Date` (time dims / dbt date), `Boolean` (dbt boolean / `is_`/`has_`),
  `Number` (dbt number/int), `Float` (dbt float/decimal/numeric),
  `String` (dbt varchar/text/categorical, and the default). `Enum` and `format` are
  never emitted (not derivable from dbt) — documented.
- **relations**: for each IR `Relationship{Left:child, Right:parent, Columns}`, add a
  relation to the **parent's** file: key = `prettify-slug(child)` (strip prefix,
  lowercase → `order_lines`), `name = prettify(child)`, `type: hasMany`,
  `model_id: UPPER(child)`, `join_strategy.join_key: UPPER(join column)`. (hasMany-on-
  parent matches the observed supersimple example and is semantically correct.)
- **metrics**: for each `ir.Table.Metrics` entry with `Kind=="simple"` and a
  bare-identifier `Column`, emit `{name: Label|Name, model_id: UPPER(Table),
  aggregation: {type: mapAgg(Agg), key: UPPER(Column)}}` into the file's top-level
  `metrics:` block. `mapAgg`: `sum→sum, count→count, count_distinct→countDistinct,
  avg→avg, min→min, max→max`. Any metric that is `ratio`/other, or simple-over-
  compound-column, is **omitted and appended to `m.Notes`** (→ stderr) with the reason.

**CLI change:** print `model.Notes` to stderr **after** `Emit` (so emitter-appended
skips are included), not before.

## Cortex: unchanged
No changes to `cortex.go` behavior. Its golden (`.../dbt/cortex/ecommerce.yaml`) must
be byte-identical after this work — an explicit regression check.

## Known limitations (documented)
`Enum`/currency `format` not derivable from dbt (omitted); `Number` vs `Float`
follows the dbt `data_type` (a money column typed `number` emits `Number`); relation
`join_key` assumes matching column names; friendly `name`/pluralization is heuristic
("Customer", not "Customers"); ratio/derived and compound-column metrics are not
representable in supersimple (omitted, reported on stderr). Row counts, telemetry,
sample rows, and Enum values are warehouse-profiling outputs, out of scope for a
schema transpiler.

## Testing (TDD)
- **Unit — IR enrichment:** update `TestDBTParse` expected metrics to include the new
  structured fields (`Kind/Agg/Table/Column`, `Label` where present); add a fixture
  metric with a `label:` to assert `Metric.Label` capture; assert `Table.Grain` from
  a `defaults.agg_time_dimension`.
- **Unit — supersimple emit:** hand-built IR → assert per-file bytes (golden), covering
  properties/types, a relation on the parent, a simple metric, and that a ratio metric
  is omitted + noted.
- **Golden — ecommerce:** emit the existing ecommerce dbt project to supersimple; goldens
  under `test/models/ecommerce/dbt/supersimple/*.yaml` (one per table). Add metric
  `label:`s to `metrics.yml` so names read nicely (this must NOT change the Cortex golden).
- **Regression — Cortex golden unchanged:** existing `TestEcommerceCortexGolden` stays green.
- **CLI:** `build --layer supersimple` end-to-end writes the per-table files; stderr lists
  omitted ratio metrics.

## Scope
**In:** IR metric enrichment + `Label` + `Grain`; dbt parser populating them; the
`supersimple` `Emitter` + registry; multi-file goldens + tests; CLI note-order change.
**Out (deferred):** supersimple `Parse`; nao-yaml/semviews/rules/facts/local emitters;
`Enum`/format/cardinality-from-`unique`-entity; anything requiring warehouse profiling.

## Open questions
None blocking. supersimple's exact `aggregation.type` vocabulary (e.g. `countDistinct`
vs `count_distinct`) and `relations` type keyword are taken from the observed example
and the vendor schema URL; verify against the live schema when convenient.
