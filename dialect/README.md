# Dialects: how each one maps to and from the IR

Everything in semglot routes through the neutral IR (`ir/model.go`). A dialect is
a `Parser` (dialect files -> IR), an `Emitter` (IR -> dialect files), or both.
This page is the field-level map: for any IR concept, it tells you the construct
that concept becomes in each dialect, so you know exactly where to add or change
support (where synonyms live, where a primary key goes, and so on).

For a yes/no summary of what each dialect carries, see the **Dialect support**
table in the top-level [README](../README.md). This page is the mechanical detail
behind it. For what each IR field *means*, read the types in
[`ir/model.go`](../ir/model.go); that is the contract this page realizes.

## Direction

`dbt` is currently the only **source** (it parses to the IR), and it is also a
target, so its constructs are read and written (marked `<->` below). Every other
dialect is **emit-only** (IR -> dialect). So the "to IR" direction is a dbt-only
column today; the "from IR" direction is every dialect.

Each emitter writes:

| Dialect | Output |
|---|---|
| `dbt` | `<model-name>.yml` (models + semantic_models + metrics); the only source dialect |
| `cortex` | `semantic_model.yaml` |
| `snowflake-semantic-view` | `definition.md` (a `create or replace semantic view` block) |
| `supersimple` | one `<TABLE>.yaml` per model, plus `NOTES.md` for anything deferred |
| `nao-yaml` | `semantic.yaml` |
| `nao-context-rules` | `RULES.md` (prose) |
| `databricks-metric-view` | one `<table>.yaml` metric view per model table, with direct joins to referenced tables (requires Databricks Runtime 17.2+; `display_name`/`synonyms` require 17.3+) |
| `lightdash` | `schema.yml` — a dbt schema file carrying Lightdash `meta:` blocks, so Lightdash compiles it into explores. A `meta-style` profile option switches `meta:` (dbt <= 1.9) to `config.meta:` (dbt >= 1.10) |

## Mapping

Cells name the construct the IR concept becomes. Notation: `<->` dbt reads it
back into the IR too; `text` the value survives only as prose folded into a
description or comment; `--` not emitted (see [Gaps vs. limits](#gaps-vs-limits)).

| IR concept | `dbt` | `cortex` | `snowflake-semantic-view` | `supersimple` | `nao-yaml` | `nao-context-rules` | `databricks-metric-view` | `lightdash` |
|---|---|---|---|---|---|---|---|---|
| Table | `models:` + `semantic_models:` `<->` | `tables[].base_table` | `tables (...)` | one file per model | `--` | "Table reference" (if described) | `source` (+ `joins[].source` for referenced tables) | `models[]` entry |
| Column / dimension | column + `dimensions type: categorical` `<->` | `dimensions[]` | `dimensions (...)` | `properties` | `dimensions[]` (deduped) | listed if described | `fields[]` | column + `meta.dimension` |
| Time dimension | `dimensions type: time` + `agg_time_dimension` `<->` | `time_dimensions[]` | plain dimension (not marked as time) | `properties` (Date) | `dimensions type: date` | with dimensions | plain `fields[]` entry (not marked as time) | column + `meta.dimension type: date/timestamp` |
| Data type | column `data_type` `<->` | `data_type` | `--` | property `type` | `--` | `--` | `--` | `meta.dimension.type` only where confidently inferable, else omitted |
| Primary key | `primary_key` constraint + primary entity `<->` | `primary_key` | `primary key (...)` | `primary_key` | `--` | `--` | `--` | `meta.primary_key` (single column only; composite degrades) |
| Relationship / join | `relationships` test on the FK column `<->` | `relationships[]` | `relationships (...) references` | `relations` (hasMany, join_key) | `--` | "Joins & routing" | `joins[]` (quoted `"on":` condition) | `meta.joins[]` (`sql_on` with `${table.col}` refs) |
| Description | `description` `<->` | `description` | `comment='...'` | `description` | `description` (field/metric) | prose | `comment` (field/measure/view) | `description` |
| Synonyms | `meta.synonyms` on the column `<->` | `synonyms:` | `with synonyms (...)` | `--` (gap) | `text` (into description) | `text` (into description) | `synonyms:` (capped at 10) | `text` (into the column description) |
| Enum / allowed values | `accepted_values` test + `meta.enum` `<->` | `sample_values` + `text` | `text` (into comment) | `text` (into description) | `values:` | "Allowed values" | `text` (into comment) | `text` (into the column description) |
| Simple metric (aggregation) | `measures` + `metrics type: simple` `<->` | `facts[]` | `metrics (...)` | metric aggregation | metric `source{table,column,aggregation}` | "Key metrics reference" | `measures[]` | column-level `meta.metrics` |
| Ratio / derived metric | `type: ratio` / `type: derived` `<->` | `expr` (rendered SQL) | inline SQL in `metrics (...)` | division ratio -> pipeline; other arithmetic -> `NOTES.md` | `type: derived`, `formula` | rendered SQL | inline SQL in `measures[].expr` | model-level `meta.metrics` `type: number` (reference-only; filtered/compound aggregates and cross-table refs degrade) |

## Gaps vs. limits

A `--` above is one of two very different things. When you extend a dialect, this
is the part to get right.

**Gaps (the target supports it, we do not emit it yet):**

- **`supersimple` synonyms.** A `synonymClause` helper exists but is not wired
  into the supersimple emitter (it would fold into a property description, as the
  nao dialects do).
- **`nao-yaml` metric `filters:`, per-metric `dimensions:`, and `notes:`.** nao's
  metric supports a filter list, a slice-by `dimensions:` list, and per-metric
  `notes:`. semglot omits them: which dimensions slice a metric and the editorial
  notes are not derivable from a dbt model, and a filtered aggregation currently
  renders as a derived `formula` rather than a structured `filters:`.

(`snowflake-semantic-view` synonyms and `nao-yaml` enum `values:` used to be gaps
here; both are emitted now. Snowflake supports `with synonyms ('...')`
([docs](https://docs.snowflake.com/en/sql-reference/sql/create-semantic-view)),
and nao dimensions carry a structured `values:` list
([nao docs](https://docs.getnao.io/nao-agent/context-engineering/skills)).)

**Limits (the format has no place for it):**

- **`nao-yaml` tables and relationships.** nao's format is a flat, model-global
  list of `dimensions:` and `metrics:` with no table grouping and no
  relationships/joins section; joins are written as prose inside descriptions and
  notes. Verified against nao's own docs and the eval's real `semantic.yaml`, so
  there is nowhere structured to emit tables or relationships. (nao-context-rules,
  being prose, does emit joins.)
- **Data types in `snowflake-semantic-view`, the nao dialects, and
  `databricks-metric-view`.** Omitted on purpose: these formats lean on the
  synced source-table schema for types rather than restating them in the
  semantic doc. A metric view's `fields`/`measures` carry no `data_type` key at
  all ([docs](https://docs.databricks.com/en/metric-views/index.html)).
- **Primary keys in `nao-yaml`, `nao-context-rules`, and
  `databricks-metric-view`.** None of these formats has a primary-key slot: the
  nao dialects have no per-table construct to hang one on, and a Databricks
  metric view declares `source`/`joins`/`fields`/`measures` only, no
  primary-key key; Unity Catalog constraints on the underlying table are the
  authority there instead.
- **Composite primary keys in `lightdash`.** `meta.primary_key` takes a single
  column name, so a multi-column key has nowhere to go and degrades to a note.
- **Synonyms and enum values in `lightdash`.** Lightdash has no synonym or
  allowed-values slot on a dimension, so both fold into the column description
  (`≈`), as they do for the nao dialects.
- **Filtered and compound aggregates in `lightdash`.** A Lightdash metric is
  either an aggregation of one column or a `type: number` expression over other
  metrics by name. There is no place for a filtered aggregate
  (`sum(case when ... end)`), and a `type: number` expression can only reference
  metrics on the SAME model, so a cross-table derived metric degrades too.

### A note the `--` notation cannot express: name collisions

Lightdash puts dimensions and metrics in ONE namespace per model, and resolves a
clash in favour of the dimension — **silently dropping the metric**. So a metric
named after a column is not a gap or a limit, it is a correctness trap: the
layer deploys cleanly and quietly lacks the metric it exists to publish.

The emitter treats the computed metric as canonical and does not emit the
colliding column as a dimension, matching `snowflake_semantic_view.go` and
`databricks_metric_view.go` so semglot's answer to a name like `roas` is the
same across targets. The exception is a column that `meta.primary_key` or a
join's `sql_on` names: those are resolved structurally and Lightdash rejects the
whole explore if the dimension is missing, so there the dimension wins and the
metric degrades to a note instead. Either way a note is emitted; the one thing
that must never happen is losing the metric silently.

## Adding or changing a mapping

1. Find the IR concept in the table above and the dialect column you are touching.
2. If the concept is missing from the IR entirely, add it to `ir/model.go` first
   (and document what it means there).
3. Wire the emitter (and, for dbt, the parser) to the construct named in the cell.
   Reuse the shared helpers where they apply: `renderSQL` (metric AST -> SQL),
   `enumClause` / `appendClause` (fold text into a description), `upperAll`,
   `metricResolver`.
4. Update this table and the top-level Dialect support summary, and add or refresh
   the golden fixtures under `dialect/testdata/` and `test/models/`.
