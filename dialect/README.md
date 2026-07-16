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

## Mapping

Cells name the construct the IR concept becomes. Notation: `<->` dbt reads it
back into the IR too; `text` the value survives only as prose folded into a
description or comment; `--` not emitted (see [Gaps vs. limits](#gaps-vs-limits)).

| IR concept | `dbt` | `cortex` | `snowflake-semantic-view` | `supersimple` | `nao-yaml` | `nao-context-rules` |
|---|---|---|---|---|---|---|
| Table | `models:` + `semantic_models:` `<->` | `tables[].base_table` | `tables (...)` | one file per model | `--` | "Table reference" (if described) |
| Column / dimension | column + `dimensions type: categorical` `<->` | `dimensions[]` | `dimensions (...)` | `properties` | `dimensions[]` (deduped) | listed if described |
| Time dimension | `dimensions type: time` + `agg_time_dimension` `<->` | `time_dimensions[]` | plain dimension (not marked as time) | `properties` (Date) | `dimensions type: date` | with dimensions |
| Data type | column `data_type` `<->` | `data_type` | `--` | property `type` | `--` | `--` |
| Primary key | `primary_key` constraint + primary entity `<->` | `primary_key` | `primary key (...)` | `primary_key` | `--` | `--` |
| Relationship / join | `relationships` test on the FK column `<->` | `relationships[]` | `relationships (...) references` | `relations` (hasMany, join_key) | `--` | "Joins & routing" |
| Description | `description` `<->` | `description` | `comment='...'` | `description` | `description` (field/metric) | prose |
| Synonyms | `meta.synonyms` on the column `<->` | `synonyms:` | `--` (gap) | `--` (gap) | `text` (into description) | `text` (into description) |
| Enum / allowed values | `accepted_values` test + `meta.enum` `<->` | `sample_values` + `text` | `text` (into comment) | `text` (into description) | `text` (into description) | "Allowed values" |
| Simple metric (aggregation) | `measures` + `metrics type: simple` `<->` | `facts[]` | `metrics (...)` | metric aggregation | metric `source{table,column,aggregation}` | "Key metrics reference" |
| Ratio / derived metric | `type: ratio` / `type: derived` `<->` | `expr` (rendered SQL) | inline SQL in `metrics (...)` | division ratio -> pipeline; other arithmetic -> `NOTES.md` | `type: derived`, `formula` | rendered SQL |

## Gaps vs. limits

A `--` above is one of two very different things. When you extend a dialect, this
is the part to get right.

**Gaps (the target supports it, we do not emit it yet):**

- **`snowflake-semantic-view` synonyms.** Snowflake's `create semantic view`
  accepts `with synonyms ('...')` on dimensions and metrics
  ([docs](https://docs.snowflake.com/en/sql-reference/sql/create-semantic-view)),
  and the IR already carries `Field.Synonyms` / `Metric.Synonyms`. The emitter
  just does not write the clause yet. This is an easy win, not a limitation.
- **`supersimple` synonyms.** A `synonymClause` helper exists but is not wired
  into the supersimple emitter.

**Limits (the format has no place for it):**

- **`nao-yaml` tables and relationships.** As modeled here (`naoDoc` =
  `dimensions` + `metrics` + `notes`), nao-yaml is a flat, model-global document
  with no table grouping and no join field, so there is nowhere to put them. If
  nao's own spec turns out to support joins, adding them is a future enhancement
  rather than a fix. (nao-context-rules, being prose, does emit joins.)
- **Data types in `snowflake-semantic-view` and the nao dialects.** Omitted on
  purpose: these formats lean on the synced source-table schema for types rather
  than restating them in the semantic doc.

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
