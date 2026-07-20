# semglot — Databricks metric-view emitter — Design

**Date:** 2026-07-20
**Status:** Approved design, pending implementation
**Builds on:** the `Emitter`/`Configurable` pattern (`layer/cortex.go`, `layer/snowflake_semantic_view.go`), the expression-AST metric model (`ir.Expr`/`renderSQL`, `metricResolver`), and the `Options` identity struct (Database/Schema/ViewSchema/Name/Description).

## Goal

Add a `databricks-metric-view` target so semglot can transpile a dbt semantic layer into **Unity Catalog Metric Views** — the YAML-defined semantic layer that Databricks **AI/BI Genie** grounds its answers on. Genie resolves metrics from metric views (a single source of truth) rather than inferring them, and auto-imports their agent metadata (display names, synonyms) for field discovery. Emitting metric views is therefore the Databricks analog of the existing `cortex` (Snowflake Cortex Analyst) target.

## What a metric view is (target shape)

A metric view is one YAML document per view, created over a **single `source` table with `joins`** — structurally unlike Cortex/`snowflake-semantic-view`, which model many tables + relationships in one artifact. Reference shape (`version: "1.1"`):

```yaml
version: "1.1"
comment: "Orders, joined to customer."
source: catalog.schema.fct_orders
joins:
  - name: dim_customer
    source: catalog.schema.dim_customer
    "on": source.customer_id = dim_customer.customer_id
fields:                      # dimensions / attributes
  - name: order_date
    expr: order_date
    comment: "Date the order was placed"
  - name: customer_region
    expr: dim_customer.region
measures:                    # aggregate expressions
  - name: net_revenue
    expr: SUM(net_revenue)
    display_name: "Net revenue"
  - name: aov
    expr: SUM(net_revenue) / COUNT(DISTINCT order_id)
```

Key facts driving the design:
- `source` is a three-part Unity Catalog name `catalog.schema.table`.
- Joined-table columns are referenced by the join `name` as prefix (`dim_customer.region`); base-table columns are bare, or `source.`-prefixed inside a join `on`.
- `on` must be quoted (`"on":`) — a YAML 1.1 parser reads bare `on` as boolean.
- `fields` = dimensions; `measures` = aggregate expressions. Databricks **allows** inlined aggregate arithmetic in a measure (`SUM(x)/SUM(y)`), unlike Snowflake semantic views.
- Agent metadata for Genie: `comment`, `display_name` (≤255 chars), `synonyms` (≤10). `format` exists but has no IR signal — skipped in v1.

## Scope

**In:** structural semantic layer derivable from the dbt model — source table, direct joins (from IR relationships), dimensions + time dimensions (as `fields`), measures and metrics (as `measures`), and Genie agent metadata (comment/display_name/synonyms) sourced from dbt descriptions, labels, and synonyms.

**Out (v1):**
- `format` specs (no data-type→format signal in the IR worth guessing).
- `filter`, `parameters`, `materialization`, `window measures` — no IR source.
- Transitive/snowflake-schema join nesting — only **direct** joins from each fact are emitted; a dimension that itself references a further dimension is not chained (noted, not expanded).
- Joined dimensions whose `Expr` is a compound expression (e.g. `lower(region)`) rather than a bare column: v1 prefixes joined-field exprs unconditionally as `<join>.<expr>`, which is correct for the bare-column case the fixtures exercise but would mis-wrap a derived expression (`dim_customer.lower(region)`). Same latent limitation as the snowflake-semantic-view emitter; source-table dimensions are unaffected (emitted bare). Harden with a token-aware qualifier if derived joined dimensions become a real input.
- The `CREATE VIEW … WITH METRICS` DDL wrapper — v1 emits raw YAML (decided; parallels `cortex`). A DDL-wrapped variant can be added later, reusing `ViewSchema`.

## Identity mapping (reuses `Options`, no new flags)

| `Options` field | Databricks meaning |
|---|---|
| `Database` | Unity Catalog **catalog** (three-part `source` prefix). **Required** — a metric view's `source` needs it. |
| `Schema` | schema of the **source tables** (default `MAIN`). |
| `ViewSchema` | unused in v1 (raw YAML carries no destination). Reserved for a future DDL variant. |
| `Name` | unused for file identity (one file per fact table, named by table). Reserved. |
| `Description` | folded into each view `comment` alongside the table description. |

`databricks-metric-view` joins the required-catalog check in `cmd/semglot/main.go`: rename the `snowflakeTargets` map to `warehouseTargets` and add `"databricks-metric-view": true`, so building without `--database`/`--config` fails fast instead of emitting an unqualified `source`.

## Mapping N IR tables → metric views (one per fact table)

**Fact selection.** A table is a *fact* (gets its own `<table>.yaml`) iff it has ≥1 **Metric**. Databricks `measures` are sourced from IR **Metrics** only — they carry a `Def` AST (rendered cleanly via `renderSQL`) plus label/synonyms/description, exactly as `snowflake_semantic_view.go` does; raw IR **Measures** are the lower-level dbt building blocks that metrics inline, and are not emitted separately (avoids near-duplicate `orders_count` measure vs `ORDERS` metric and any Measure→SQL reconstruction). Pure dimension tables (no metrics) surface only as joins on the facts that reference them; a dimension-only table referenced by nothing is skipped with a `model.Notes`-style note.

For each fact table `F`:

1. **`source`**: `<catalog>.<schema>.<F>` (lowercased table name; catalog/schema as configured).
2. **`joins`**: for each `Relationship` where `F` is the `Left` side, emit `{name: <Right>, source: <catalog>.<schema>.<Right>, "on": "source.<lcol> = <Right>.<rcol>"}`. Multiple `ColumnPair`s join with ` AND `. Only direct relationships from `F`; `F` as a `Right` side (i.e. F being referenced) does not pull in a join.
3. **`fields`**: `F`'s `Dimensions` + `TimeDimensions` as `{name, expr, comment}` with bare `expr`. Then, for each joined dimension table, its `Dimensions` + `TimeDimensions` with `expr: <join>.<col>`. Field **names must be unique within a view** — dedup with a `seen` set (mirroring `snowflake_semantic_view.go`): a joined field whose bare name collides is prefixed with the join name (`dim_customer_region`). Enum values fold into `comment` (metric views have no per-value enum), reusing the existing `enumClause`/`appendClause` helpers.
4. **`measures`** (from `F`'s `Metrics`; raw `Measures` are not emitted):
   - Each `Metric` → `{name, expr}` where `expr = renderSQL(mt.Def, resolve)` and `resolve` is `metricResolver(m)` (inlines same-model measure/metric refs to aggregates — `AOV → SUM(net_revenue) / COUNT(DISTINCT order_id)`).
   - `display_name` ← `Metric.Label` (omit if empty); `synonyms` ← `Synonyms` (capped at 10); `comment` ← `Description`.

**Degrade to a note** (folded into that view's `comment` as a trailing "Note: …"), never emitting SQL we cannot stand behind:
- `ir.Window` (cumulative) and `ir.Conversion` (funnel) metrics — no validated metric-view primitive (same posture as `cortexDegrade`).
- **Cross-grain derived metrics**: a metric whose `Def` references (`ir.Ref`) a metric/measure owned by a *different* table than `F`. Inlining another grain's aggregate into `F`'s view would fan out and miscount (e.g. `fct_order_lines.units_per_order = units_sold / fct_orders.orders`). Detect via a `metricTableOf`/measure-owner map (as `snowflake_semantic_view.go` builds) and degrade.

`Emit` is **read-only** over `m` (accumulates degrade notes locally), consistent with cortex/supersimple. Existing `model.Notes` continue to print on CLI stderr.

## Output

One `<facttable>.yaml` per fact table, written with `yaml.v3` at the same encoder settings as cortex (`SetIndent(2)`). `os.MkdirAll(dir)` first. The `on` key is emitted quoted.

## Testing

- **Unit** — `layer/databricks_metric_view_test.go`: a small in-memory `ir.Model` (a fact with a dimension join, a simple aggregate metric, a same-grain derived metric, and a cross-grain derived metric) asserting: `source` three-part name; a quoted `"on":` join line with `source.`/join-name prefixes; a joined dimension rendered `expr: <join>.<col>`; an inlined derived measure (`SUM(...) / COUNT(...)`); the cross-grain metric absent from `measures` and present as a `comment` note; `version: "1.1"`.
- **Golden** — `test/models/ecommerce/dbt/databricks-metric-view/` with one yaml per fact (`fct_orders.yaml`, `fct_order_lines.yaml`), pinned via `UPDATE_GOLDEN=1` and eyeballed for valid metric-view YAML. Add a `databricks-metric-view` case to `test/integration_test.go`; since this target emits **multiple** files, extend the harness to read a named file from the output dir (the existing `emitTarget` already takes a `file` arg — pass `fct_orders.yaml`) and add a golden-dir comparison over all emitted files.
- CLI: building the fixture with `--target-type databricks-metric-view --database ANALYTICS` succeeds; omitting `--database` fails with the required-catalog error.

## Files

- `layer/databricks_metric_view.go` — new emitter (`init()` `Register`; `Name() "databricks-metric-view"`; `WithOptions`; `Emit`).
- `layer/databricks_metric_view_test.go` — unit tests.
- `cmd/semglot/main.go` — rename `snowflakeTargets` → `warehouseTargets`; add the new target.
- `test/integration_test.go` — golden + structure test; multi-file read helper.
- `test/models/ecommerce/dbt/databricks-metric-view/*.yaml` — goldens.
- `README.md` — mention the new target in the dialect list.
