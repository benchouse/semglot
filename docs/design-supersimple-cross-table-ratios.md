# semglot — supersimple cross-table ratios — Design

**Date:** 2026-07-12
**Status:** Approved design, pending implementation plan
**Builds on:** `docs/design-supersimple-rich-metrics.md` (compound + same-table ratios).

## Problem

A ratio whose two operands aggregate **different, directly-related** tables
(`units_per_order = units_sold[fct_order_lines] / orders[fct_orders]`) is
currently deferred to `NOTES.md`. supersimple can express it with a metric
`operations` pipeline that pulls the child operand across the parent's relation
(`relationAggregate`). This iteration emits **one-hop parent/child** cross-table
ratios; anything else stays in `NOTES.md`.

## Settled decisions

- **Base model = the parent** in the relationship joining the operand tables (the
  table that owns the `hasMany` relation to the other). We only emit `hasMany` on
  parents, so the parent is the only side with a relation to traverse. This is also
  the faithful grain (divide by the count of *all* parent rows).
- **Re-home the metric to the base (parent) file.** The IR attaches a ratio to the
  numerator's table; supersimple emits a cross-table ratio into the parent's file.
- **One-hop only.** If the two operand tables are not directly related (no
  relationship, multi-hop, or siblings), the ratio stays in `NOTES.md`.
- **Child operand aggregation must compose under an outer sum** (`sum`, `count`):
  `relationAggregate` yields a per-parent-row value, then `groupAggregate` sums it.
  `count_distinct`/`avg`/`min`/`max` on the *child* don't compose → `NOTES.md`.
  (The parent operand can be any aggregation — it's direct.)
- **Provisional, validate on push.** The `relationAggregate` shape and whether a
  `groupAggregate` can sum a relation-aggregated column are hypotheses; only the new
  `crossRatioMetric` + the `units_per_order` golden change if validation corrects them.
- **Behavior-preserving for existing cases.** Simple, compound, and same-table-ratio
  output must be byte-identical (except `units_per_order` moving from `NOTES.md` into
  `FCT_ORDERS.yaml`).

## Architecture — restructure `Emit` into phases (`layer/supersimple.go`)

Cross-table resolution needs *all* tables' simple metrics and the relationship
graph before any file is written, and the metric may land in a different file than
its IR table. So `Emit` moves from one per-table pass to three phases:

1. **Build models + global metric registry.** For each table: build its `ssModel`
   (properties incl. synthesized compound `property.sql`, relations) exactly as
   today, and record every simple metric in a **global** map:
   `metricName → {table, aggType, key}` (key = the column or synthesized property).
   Keep the models in an ordered per-table state map.
2. **Assign metrics to files.** For each metric:
   - **simple** → its own table's file.
   - **ratio, same-table** (both operands resolve to the same table) → that table's
     file, via the existing `ratioMetric` (unchanged).
   - **ratio, cross-table** → `findParentRelation(operandTableA, operandTableB)`;
     if a one-hop parent/child relation exists (and the child operand composes),
     emit `crossRatioMetric` into the **parent's** file; else append a `NOTES.md`
     entry.
3. **Write files** (per-table `<UPPER>.yaml`) then `NOTES.md`.

### `findParentRelation(m *ir.Model, a, b string) (parent, relationKey, child string, ok bool)`
Scan `m.Relationships` for one whose `{Left(child), Right(parent)}` equals `{a,b}`
in either order. Returns the parent, the relation key (`slug(child)`, matching what
the emitter puts under the parent's `relations`), and the child. `ok=false` if none.

### `crossRatioMetric(baseID, relationKey string, num, den crossOperand) ssMetric`
`crossOperand{onBase bool, aggType, key string}`. For the operand `onBase` (parent
table) → a direct `groupAggregate` aggregation; for the child operand → a
`relationAggregate` producing a per-parent column, summed in `groupAggregate`.
Produces:
```yaml
operations:
  - operation: relationAggregate            # child operand only
    parameters:
      relation: {key: <relationKey>}
      aggregations:
        - {type: <childAgg>, key: <childKey>, property: {key: _child, name: _child}}
  - operation: groupAggregate
    parameters:
      groups: []
      aggregations:
        - {type: sum, key: _child, property: {key: _num|_den, name: ...}}     # child, summed
        - {type: <parentAgg>, key: <parentKey>, property: {key: _den|_num, ...}}  # parent, direct
  - operation: deriveField
    parameters: {field_name: <name>, key: <metric>, value: {expression: 'prop("_num") / prop("_den")', version: "1"}}
aggregation: {type: first, key: <metric>, property: {key: <metric>, name: <name>}}
```
`_num`/`_den` are assigned by numerator/denominator identity so the division stays
`numerator / denominator` regardless of which side is the parent.

## Testing
- **Unit — `findParentRelation`:** returns the parent + relation key for a related
  pair (either argument order); `ok=false` for an unrelated pair.
- **Unit — cross-table ratio emit:** a hand-built IR (lines child sum + orders parent
  count_distinct, related) emits a `relationAggregate` on the parent, a
  `groupAggregate` with `_num`/`_den`, `deriveField` dividing them, terminal `first`,
  into the **parent's** file; and a non-composing child operand (`count_distinct`)
  → note, not emitted.
- **Golden — ecommerce:** regenerate; `units_per_order` now lands in
  **`FCT_ORDERS.yaml`** (the parent). It was the only `NOTES.md` entry, so `m.Notes`
  is now empty and **no `NOTES.md` is written** — the stale golden
  `test/models/ecommerce/dbt/supersimple/NOTES.md` must be **deleted** on
  regeneration (`git rm`), otherwise the golden set-equality check correctly fails
  (golden with no producer). Provisional pending push validation.
- **Regression:** simple/compound/same-table-ratio output byte-identical (the
  three-phase restructure must not change them); Cortex golden unchanged; full suite,
  gofmt, vet green.

## Scope
**In:** three-phase `Emit`; global metric registry; `findParentRelation`;
`crossRatioMetric`; one-hop cross-table ratios with composing child operands;
re-homing to the parent file; tests + provisional golden.
**Out (later / NOTES.md):** multi-hop / sibling joins; non-composing child operands;
unsupported dbt metric types; the same-table ratio shape (already provisional,
unchanged here).

## Open questions
None blocking. The `relationAggregate`/`groupAggregate` composition is the
push-validation item, isolated to `crossRatioMetric`.
