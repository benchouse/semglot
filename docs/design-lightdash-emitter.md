# semglot: Lightdash emitter: Design

**Date:** 2026-07-20
**Status:** Proposed design, pending review
**Builds on:** the expression-AST metric model (`ir.Expr`/`renderSQL`) and the existing `Emitter`/`Configurable` pattern (cortex, nao-yaml, snowflake-semantic-view).

## Goal

Add a new target emitter, `lightdash`, so semglot can transpile a dbt semantic layer into the config Lightdash Cloud ingests.

Lightdash has no separate upload API for semantics. It connects to a dbt project and reads standard dbt `schema.yml` files augmented with Lightdash `meta:` blocks. The emitter therefore writes one dbt schema file (`version: 2`, a `models:` list) carrying Lightdash annotations.

Target-only (emit). Lightdash is not a source dialect in v1.

## How Lightdash differs from the existing dbt target

Both emit dbt YAML, but they serve different consumers with non-overlapping semantic schemas that only share the outer `models:`/`columns:` skeleton.

The dbt target emits the dbt Semantic Layer (MetricFlow) spec: `semantic_models:` (entities, dimensions, measures) plus a top-level `metrics:` block, and it is bidirectional (its output re-parses through the dbt `Parser`, round-trip lossless).

Lightdash reads none of `semantic_models:`/`metrics:`. It reads the same concepts under different keys:

| Concept | dbt target (MetricFlow) | Lightdash |
| --- | --- | --- |
| dimension | `semantic_models[].dimensions[]` | `columns[].meta.dimension` (singular) |
| metric | top-level `metrics[]` + `measures[]` | `columns[].meta.metrics` + `models[].meta.metrics` |
| metric SQL | `type_params` (measure refs) | `${metric}` / `${TABLE}.col` strings |
| join | `data_tests: relationships` + entities | `models[].meta.joins[]` with `sql_on` |
| primary key | `constraints: [primary_key]` + entity | `meta.primary_key` |
| enum | `accepted_values` test + `meta.enum` | folded into description |

Structural overlap is only the physical `models:` + `columns:` (name, description) shell, and the dbt emitter's shell is itself loaded with MetricFlow round-trip machinery (constraints, relationship tests, `meta.enum`) that Lightdash does not want. Lightdash is a new sibling emitter in the mold of cortex/nao-yaml, not a flavor of the dbt target. It reuses shared helpers, not the dbt emitter's code.

## Output

One file per Emit, `<dir>/schema.yml`, shaped:

```yaml
version: 2
models:
  - name: orders                     # explore = dbt model
    description: "..."
    meta:
      primary_key: order_id
      joins:
        - join: customers
          sql_on: ${orders.customer_id} = ${customers.id}
          relationship: many-to-one
      metrics:                       # model-level: derived / ratio metrics
        revenue_per_order:
          type: number
          sql: ${total_revenue} / ${order_count}
    columns:
      - name: status
        description: "..."
        meta:
          dimension:                 # singular; customizes the auto dimension
            type: string
      - name: amount
        meta:
          metrics:                   # column-level: simple aggregates
            total_revenue:
              type: sum
```

## IR to Lightdash mapping

| IR | Lightdash |
| --- | --- |
| `Table` | a `models[]` entry |
| `Table.Description` | `models[].description` |
| `Table.PrimaryKey` (single column) | `models[].meta.primary_key` |
| `Table.PrimaryKey` (composite) | omitted; surfaced as a note (Lightdash `primary_key` is a single column) |
| `Dimensions` | `columns[]` with `meta.dimension.type` in {string, number, timestamp, date, boolean} |
| `TimeDimensions` | same, typed `timestamp`/`date` (Lightdash auto-generates the interval breakdown; the IR carries no interval list, so `time_intervals` is not emitted in v1) |
| `Field.Enum` | folded into the column `description` via `enumValues`/`appendClause` (Lightdash has no enum type) |
| `Field.Synonyms` | folded into the column `description` via `synonymClause` (no native synonym slot) |
| simple `Measure`/`Metric` | column-level `meta.metrics.<name>` on the backing column |
| derived `Metric` | model-level `meta.metrics.<name>`, `type: number`, `sql` in `${metric}` syntax |
| `Relationship` | `models[].meta.joins[]`: `join`, `sql_on`, `relationship` |

### Dimension type mapping

The IR does not always carry a SQL type. Lightdash auto-detects types from the warehouse, so the emitter only sets `type` when it has a confident signal and omits it otherwise rather than guessing wrongly. The signals, in order: `Field.DataType` when present (reusing the same normalization the cortex emitter applies); a time dimension is `timestamp` (or `date` when the IR/name indicates a pure date); an `is_`/`has_` name prefix is `boolean`; an `_id`/`_sk` name suffix is `number`. With no signal, `type` is omitted.

### Metric lowering and honest degrade

Following the repository's established rule (never emit a definition the target cannot stand behind, as `cortexDegrade` does), metrics lower in three tiers:

1. Simple aggregate: `Agg` with a `Col` arg and no filter. Emits a column-level metric on the backing column, `type` mapped (`avg` to `average`, `count_distinct` kept, and `sum`/`count`/`min`/`max`/`median` passed through). No `sql` needed; Lightdash auto-references the column. A `count(*)` (nil arg) has no backing column and degrades to a note in v1.
2. Reference-only derived: a `Binary` tree whose leaves are all `Lit` or `Ref` to a same-table simple metric that is itself emitted (tier 1). Emits a model-level metric, `type: number`, `sql` rendered in Lightdash `${metric}` reference form by a new small `renderLightdash`. Requiring the refs to resolve to emitted same-table simple metrics is what prevents a dangling `${...}`: a derived metric whose operand degraded (for example a ratio over a filtered aggregate) degrades in turn rather than referencing a metric that was never written. Cross-table refs are out of scope for v1 and degrade.
3. Everything else: filtered aggregates, `count(*)`, ratios referencing degraded or cross-table metrics, `Raw`, `Window`, `Conversion`. Not emitted as a metric. Degraded to a note. `Window`/`Conversion` reuse `cortexDegrade`.

Notes have no `custom_instructions` equivalent in a Lightdash schema file. They surface two ways: as a leading `# semglot:` comment block prepended to the emitted file, and through the existing CLI `warning:` output driven by `model.Notes`.

Filtered aggregates degrade to notes in v1 rather than attempting to translate an arbitrary boolean `Expr` into Lightdash's `filters:` list (only trivial `dim = value` cases would be safe). Mapping simple equality/in filters is a possible follow-up.

## Config: meta placement toggle

dbt moved where Lightdash's `meta` lives. dbt 1.9 and earlier nest under `meta:`; dbt 1.10+ and Fusion nest under `config.meta:`. The emitter defaults to `meta:` and exposes a toggle to `config.meta:`.

Plumbing mirrors `ViewSchema` exactly (the existing per-target option carried through shared config):

- `dialect.Options` gains `MetaStyle string` (`""`/`meta` default, `config.meta` alternate).
- `configFile` gains `meta_style:` and `identity` gains `MetaStyle`, layered defaults < config < flag in `resolveIdentity`.
- `cmd/semglot` adds a `--lightdash-meta-style` flag.
- Under `config.meta`, each `meta:` block on a model and a column is nested one level under a `config:` key. The `meta` content is identical; only its parent wrapper differs.

Lightdash is not a Snowflake target, so it is absent from `snowflakeTargets` and does not require `--database`.

## Components and boundaries

- `dialect/lightdash.go`: the emitter. Owns the Lightdash YAML shapes (`ldModel`, `ldColumn`, `ldDimension`, `ldMetric`, `ldJoin`), the IR-to-Lightdash lowering, and file writing. Reuses `metricResolver`, `enumValues`, `appendClause`, `synonymClause`, `fkColumns`, `dedupeStrs`, and the cortex data-type normalization.
- `renderLightdash(e ir.Expr, resolve) (sql string, ok bool)`: renders a reference-only derived tree to `${metric}` form, returning ok=false for any node that is not a `Ref`/`Lit`/`Binary`. Kept separate from `renderSQL` and `renderDerived` for the same reason those two are separate: each target has a distinct reference discipline.
- `WithOptions` carries `Name`, `Description`, and `MetaStyle`. Database/Schema are unused by Lightdash.

## Testing

Unit tests in `dialect/lightdash_test.go`:

- a dimension of each mapped type, including a time dimension typed `timestamp`
- a simple aggregate emitting a column-level metric, with agg-name mapping
- a ratio emitting a model-level `type: number` metric with `${metric}` sql
- an enum-bearing dimension folding values into the description
- a relationship emitting a `meta.joins` entry with correct `sql_on` and cardinality
- both meta styles (`meta` and `config.meta`) over the same model
- a filtered aggregate and a `Window` metric degrading to notes, asserting the note text and that no metric is emitted

End-to-end: a golden fixture `test/models/ecommerce/dbt/lightdash/ecommerce.yml` wired into the existing runner, regenerated with `UPDATE_GOLDEN=1`.

## Out of scope for v1

- Lightdash as a source (parser).
- `colors:` maps for categorical dimensions.
- Mapping metric filters to Lightdash `filters:`.
- Lightdash YAML (the flat, non-`meta` file variant) and `lightdash.config.yml`.
- Per-field `format`/`compact`/`round` formatting, `urls`, `groups`, `tags`.
