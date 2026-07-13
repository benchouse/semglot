# semglot — Snowflake semantic-view + nao emitters — Design

**Date:** 2026-07-13
**Status:** Proposed design, pending review
**Builds on:** the expression-AST metric model (`ir.Expr`/`renderSQL`, merged to main) and the existing `Emitter`/`Configurable` pattern (cortex, supersimple).

## Goal

Add three new target emitters so semglot can transpile a dbt semantic layer into the eval's remaining context-layer formats:

1. **`snowflake-semantic-view`** — a Snowflake `CREATE SEMANTIC VIEW` DDL.
2. **`nao-yaml`** — nao's semantic-model YAML (`semantic.yaml`).
3. **`nao-context-rules`** — a prose `RULES.md`.

Each is a registered `Emitter` writing ONE semantic artifact to the output dir, verified against the `test/models/ecommerce/dbt` fixture with a byte-identical golden — exactly like the cortex and supersimple emitters.

## Scope boundary (settled)

**In:** the structural semantic layer derivable from the dbt model — tables, primary keys, relationships/joins, dimensions, measures, and metrics (via `renderSQL(Def)`). Prose slots (`comment=`, `notes:`, "Table traps") are filled ONLY from dbt `description:` fields and IR `Notes`.

**Out (not derivable from a dbt model):**
- The editorial "trap" knowledge (the eval's F1–F12 facts, e.g. *"obt_sales.net_revenue is line-level, not the P&L figure"*). Surfaces only if authored into dbt descriptions.
- The nao **directory scaffolding** (`databases/type=snowflake/database=…/schema=…/table=…/`) and **catalog files** (`columns.md`, `how_to_use.md`: row counts, usage stats, top-query logs) — nao's deploy layout + live warehouse introspection.
- `nao_config.yaml` — deploy plumbing (and holds secrets).

The eval's deploy step is responsible for placing semglot's emitted artifact into the nao layout; semglot just produces the artifact.

## Shared shape

- Each emitter is a `struct{}` registered via `init()` (`Register(...)`), implementing `Emit(m *ir.Model, dir string) error`, writing a single file.
- `snowflake-semantic-view` and `nao-yaml` are `Configurable` (`WithOptions(database, schema, name, description)`) — they need the database/schema qualifier and the model/view name, same as cortex. `nao-context-rules` needs only `name`/`description`.
- **Table aliasing (decided): physical names.** A table is emitted as `FCT_ORDERS as DB.SCHEMA.FCT_ORDERS` — no business-alias guessing.
- Metric SQL comes from `renderSQL(mt.Def, resolve)` (the single lowering path), uppercased for Snowflake. Build a project-wide `resolve` (name→`Def`) as cortex does, so `Ref`s inline.
- `Emit` is **read-only** over `m` (no writing back to `m.Notes`) — consistent with the post-refactor cortex/supersimple.

---

## 1. `snowflake-semantic-view` → `definition.md`

Closest sibling of cortex. Emits a markdown file wrapping the DDL (matching the eval's `.../semantic_view=<NAME>/definition.md`):

````
# <NAME>

This is a Snowflake **semantic view** — use this to understand the intended way to query and aggregate data.

<description>

## Definition

```sql
create or replace semantic view <NAME>
	tables ( <ALIAS as DB.SCHEMA.TABLE primary key (PK...) [comment='<desc>']> , ... )
	relationships ( <REL as LEFT(col) references RIGHT(col)> , ... )
	dimensions ( <TABLE.COL as table.COL> , ... )
	metrics ( <TABLE.NAME as <UPPER(renderSQL(Def))> [comment='<desc>']> , ... )
	[comment='<description>'];
```
````

Mapping from IR:
- **tables** — one per `ir.Table`: `UPPER(name) as <database>.<schema>.UPPER(name) primary key (UPPER(pk...))`, `comment='...'` from `Table.Description` (single-quote-escaped).
- **relationships** — one per `ir.Relationship`: `<LEFT>_<RIGHT> as UPPER(left)(UPPER(col)) references UPPER(right)(UPPER(col))`.
- **dimensions** — `Table.Dimensions` + `Table.TimeDimensions`: `UPPER(table).UPPER(col) as <table>.UPPER(col)`.
- **metrics** — `Table.Metrics`: `UPPER(table).UPPER(name) as UPPER(renderSQL(Def, resolve))`, `comment='...'` from `Metric.Description`.
- Model-level `comment=` from the `--description`.
- Metrics whose `Def` degrades (cumulative/conversion) are omitted; their reason is appended to the model `comment=` (like cortex's `custom_instructions`), reusing `cortexDegrade`-style logic.

Reuses `renderSQL`, an `upper`/escape helper, and the `Configurable` fields. No new lowering.

## 2. `nao-yaml` → `semantic.yaml`

```yaml
dimensions:
  - name: <dim name>
    type: <date | categorical>          # date for TimeDimensions, categorical otherwise
    description: <Field.Description>     # omitted if empty
metrics:
  - name: <metric name>
    definition: <Metric.Description>     # omitted if empty
    source: {table: <table>, column: <col>, aggregation: <AGG>}   # simple Agg over a Col
    grain: <Metric.Grain>               # omitted if ""
    # derived (Def is a Binary/Ref/Lit tree):
    type: derived
    source: {table: <owning table>}
    formula: <renderSQL(Def)>
```

Mapping / decisions:
- **dimensions** — the union of every table's `Dimensions` (→ `categorical`) and `TimeDimensions` (→ `date`), de-duplicated by name. (nao's dimension list is model-global, not per-table.)
- **simple metric** (`Def` = `Agg{Func, Table, Col}`) → `source: {table, column, aggregation: UPPER(Func)}`.
  - **`aggregation` enum:** emit the uppercased `Func`. `count_distinct` has no nao example; emit `COUNT_DISTINCT` and flag for confirmation — if nao rejects it, fall back to `COUNT` (a known follow-up, not a blocker for the fixture golden).
- **compound simple metric** (`Def` = `Agg{Func, Raw}`) → no clean `source:{column}`; emit as `type: derived` with `formula: renderSQL(Def)` and `source: {table}`.
- **derived / ratio** (`Def` = `Binary`) → `type: derived`, `formula: renderSQL(Def)`, `source: {table}`.
- **`grain`** from `Metric.Grain` (omit when empty).
- **per-metric `dimensions:`** — the dbt parser does not populate `Metric.Dimensions` (which dims slice a metric is editorial), so this key is **omitted**. Documented fidelity gap; can be revisited if a dbt source for it appears.
- **cumulative/conversion** → omitted from `metrics:`, reason appended as a top-level `# note` / or into a trailing `notes:` — degrade, never invent.

## 3. `nao-context-rules` → `RULES.md`

Prose markdown with generated sections:

```markdown
# Rules

## Key metrics reference
- **<Metric name / label>**: `<UPPER(renderSQL(Def))>`. <Metric.Description>
  ...

## Joins & routing
- Joins: `<left>.<col> → <right>.<col>`; ...   (one line per relationship, grouped)

## Table traps
- <Table.Description / column descriptions / IR Notes that read as cautions>
  (thin — only what the dbt model documents; empty section omitted)
```

Mapping:
- **Key metrics reference** — one bullet per metric: bolded name (label or name), its `renderSQL(Def)` formula in backticks, then `Description`.
- **Joins & routing** — from `ir.Relationships`, rendered as `left.col → right.col`.
- **Table traps** — best-effort: `Table.Description` and any `ir.Notes`. If nothing substantive, omit the section. This section is intentionally thin (the editorial traps are out of scope).

---

## Testing

Per emitter, mirroring the cortex/supersimple integration tests:
- A **golden** test: emit against `test/models/ecommerce/dbt` to a temp dir, compare the produced file to `test/models/ecommerce/dbt/<target>/<file>` byte-for-byte (with the `UPDATE_GOLDEN=1` create path).
- A **structure** test asserting the interesting behaviors directly (e.g. the semantic-view metric line for `aov` is `AOV as SUM(...)/COUNT(DISTINCT ...)`; nao-yaml `refund_rate` is `type: derived` with the CASE formula; context-rules lists every metric).
- The CLI end-to-end test (`go run ./cmd/semglot build --from dbt --layer <target> ...`) for at least the semantic-view target.
- New capability must not touch existing cortex/supersimple/dbt goldens.

## Out-of-scope / follow-ups (acknowledged)

- nao `aggregation: COUNT_DISTINCT` acceptance (confirm against nao's schema; fall back to `COUNT`).
- Per-metric `dimensions:` in nao-yaml (no dbt source today).
- Editorial trap prose and the nao catalog/scaffolding files (permanently out of scope for the transpiler).
- Live validation against a real Snowflake / nao deployment (a separate step, like the supersimple live push).
