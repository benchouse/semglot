# semglot — supersimple rich metrics (compound + same-table ratios) — Design

**Date:** 2026-07-11
**Status:** Approved design, pending implementation plan
**Builds on:** `docs/design-supersimple.md` (simple-aggregation supersimple emitter).

## Problem

The supersimple emitter currently emits only **simple** metrics (`{aggregation:
{type, key}}`) and omits everything else — reported on stderr but absent from the
artifact. Investigation of the supersimple JSON schema + docs showed the omitted
metrics **are** expressible:

- **Compound-measure metrics** (a measure whose expr is a SQL expression, e.g.
  `refunded_orders_count = sum(CASE WHEN is_refunded …)`) via a computed
  **`property.sql`** column + a simple aggregation over it. **Deterministic** —
  `property.sql` is documented with verbatim examples.
- **Ratio metrics** (`N / D`) via a metric **`operations` pipeline**
  (`groupAggregate` → `deriveField` division → terminal `first` aggregation).
  **Best-effort** — the schema leaves the formula `expression` grammar and the
  whole-set `groupAggregate` shape unspecified, so the emitted pipeline is a
  hypothesis to be **validated against a live supersimple project**.

**Scope of this iteration:** compound-measure metrics **and same-table ratios**
(both operands aggregate the same base model — `aov`, `refund_rate`).
**Cross-table ratios** (`units_per_order`, operands on different models) are
**deferred** to a follow-up after same-table is validated; until then they go to
the sidecar. Anything still unmappable also goes to the sidecar — never silently
dropped.

## Settled decisions

- **Validation is an external push loop.** semglot emits its best-effort config;
  the user pushes it to a live supersimple project and reports errors; we iterate.
  The ratio golden is therefore **provisional** until validated.
- **Keep the simple fast path.** Bare-column simple metrics stay
  `{aggregation: {type, key}}` with no `operations` — no regression, clean output.
- **No IR change needed.** The IR already carries structured metrics
  (`Kind/Agg/Table/Column/Numerator/Denominator`). Ratio operands are metric
  names resolved to their simple metrics within the same table.
- **Sidecar is the honest catch-all.** `NOTES.md` in the output dir lists every
  metric not emitted structurally (cross-table ratios this iteration, unsupported
  dbt types), with the reason. Replaces the current stderr-only reporting for
  supersimple (stderr warning stays too).

## Architecture (all in `layer/supersimple.go`)

### 1. Measure → property-key resolver
Every measure maps to a supersimple **aggregation key** (a property on the model):
- **bare-column measure** (`isIdent(expr)`) → key = `UPPER(expr)` (already a physical property).
- **compound measure** (expr is an expression) → synthesize a computed property
  `key = UPPER(measure.Name)`, `type: Integer|Number`, `sql = <expr → {col} form>`.
  Add it to the model's `properties`. The metric then aggregates this key.

A metric's aggregation `key` and `type` come from its owning measure via this resolver.

### 2. Expression compiler (two render targets, lexer-based)
Reuse the `go-sqllexer` pass already in `dbt.go`:
- **`property.sql`** target: rewrite each bare column identifier `is_refunded` →
  `{is_refunded}` (supersimple column-ref syntax); keep keywords/literals/operators.
  `case when is_refunded then 1 else 0 end` → `case when {is_refunded} then 1 else 0 end`.
- **formula `expression`** target: `prop("KEY_A") / prop("KEY_B")` for the
  deriveField division (built structurally from operand keys, not from raw SQL).

### 3. Metric emit by shape
- **simple, bare column:** unchanged fast path.
- **simple, compound measure:** ensure the synthesized `property.sql` exists, emit
  `{aggregation: {type: mapAgg(agg), key: <property>}}`.
- **same-table ratio** (`Kind=="ratio"`, both operands resolve to the base table):
  resolve numerator/denominator metrics → their `(agg, key)`; emit
  ```yaml
  <ratio>:
    name: <label|name>
    model_id: <TABLE>
    description: <desc>
    operations:
      - operation: groupAggregate
        parameters:
          groups: [ ... ]            # whole-set grouping (exact shape = validation item #1)
          aggregations:
            - {type: <numAgg>, key: <numKey>, property: {key: _num, name: _num}}
            - {type: <denAgg>, key: <denKey>, property: {key: _den, name: _den}}
      - operation: deriveField
        parameters:
          field_name: <name>
          key: <ratio>
          value: {expression: 'prop("_num") / prop("_den")', version: "1"}
    aggregation:
      type: first
      key: <ratio>
      property: {key: <ratio>, name: <name>}
  ```
- **cross-table ratio / unsupported:** not emitted → `NOTES.md` entry + stderr.

### 4. Sidecar
`NOTES.md` written to `--out` when any metric is un-emitted:
```
# Not transpiled to supersimple
- metric "units_per_order" (ratio): cross-table (units over fct_order_lines / orders over fct_orders) — deferred to a later iteration.
```

## Provisional / validation items (the hypotheses to confirm on push)
1. Whole-set `groupAggregate` shape — is a group required? (fallback: group by a
   `deriveField` constant, then aggregate.)
2. Does `deriveField.expression` support `/` and reference preceding aggregation
   keys via `prop("_num")`? (schema: `expression` is a free string.)
3. Is terminal `aggregation: {type: first}` the right way to surface a single
   derived value? (alternative: `type: custom` with the expression.)

The design isolates these to the ratio path; if validation forces changes, only
the same-table-ratio emit + its golden change.

## Testing
- **Unit — compound property:** a compound measure yields a `property` with the
  rewritten `sql` (`{is_refunded}`), and its metric aggregates that property key.
- **Unit — expression rewrite:** `case when is_refunded then 1 else 0 end` →
  `case when {is_refunded} then 1 else 0 end`; a string literal is not rewritten.
- **Unit — same-table ratio:** `aov` emits the `operations` pipeline with the two
  operand aggregations and a `deriveField` dividing them.
- **Unit — sidecar:** a cross-table ratio produces a `NOTES.md` entry, not a metric.
- **Golden — ecommerce supersimple:** regenerate; `refunded_orders`, `aov`,
  `refund_rate` now present; `units_per_order` in `NOTES.md`. Marked provisional
  pending push validation.
- **Regression:** Cortex + existing simple-metric goldens unchanged elsewhere;
  full suite + gofmt + vet green.

## Validation loop (external, post-build)
1. `semglot build --layer supersimple` → push the output to the live supersimple project.
2. User reports which metrics load/run and which error (with messages).
3. Fix the ratio emit (items 1–3 above), regenerate goldens, repeat until clean.
4. Then a follow-up iteration adds cross-table ratios (`relationAggregate`).

## Scope
**In:** compound-measure metrics (property.sql); same-table ratios (operations
pipeline); the expression compiler; the measure→property resolver; `NOTES.md`
sidecar; tests + provisional goldens.
**Out (next iterations):** cross-table ratios (`relationAggregate`); unsupported
dbt metric types (`derived`/`cumulative`); Enum/format (not derivable).

## Open questions
None blocking the build. The three provisional items are resolved by the push
validation, by design.
