# semglot — generalized metric model (expression AST) — Design

**Date:** 2026-07-13
**Status:** Approved design, pending implementation plan
**Builds on / reshapes:** the flat `ir.Metric` (`Kind/Agg/Column/Numerator/Denominator` + rendered `Expr`).

## Problem

`ir.Metric` is a flat struct hardcoded for exactly two shapes (`simple`, `ratio`),
with most fields nil for any given metric, and a **dual source of truth** (structured
fields *and* a pre-rendered `Expr`). It cannot represent **derived** (arbitrary
formula over metrics), **cumulative** (windowed/to-date), **conversion** (funnel), or
**filtered** metrics — those fall through to `NOTES.md`. Adding each new kind as more
nil-able fields does not scale.

Replace the metric *definition* with a small **semantic expression AST**: a metric's
definition is a tree of typed nodes. This represents any metric shape uniformly; each
**emitter lowers** the tree into its target form and **degrades** shapes it cannot
express. It also removes the `Expr` dual-source: the AST is the single source of
truth, and Cortex/supersimple render from it.

## Settled decisions

- **Representation: a typed expression AST** (`ir.Expr` interface + concrete node
  types). Not a fat struct, not a stringly-typed SQL blob. The IR is in-memory Go —
  no serialization concern with an interface.
- **Implement all kinds' node types** (simple/ratio/derived/filter/cumulative/
  conversion). The dbt parser builds them where the source provides them; emitters
  lower the shapes they support and **degrade the rest to `NOTES.md`** (supersimple)
  or best-effort SQL (Cortex).
- **Drop the rendered `Expr` field.** Each emitter renders from `Def`. The only raw
  SQL that survives is a **leaf** (`Raw` node) for arbitrary column expressions
  (`case when …`) that no structure decomposes.
- **Behavior-preserving.** Existing Cortex + supersimple goldens must be
  **byte-identical** after the refactor. This is the regression guard and the proof
  the AST + lowering reproduce today's output for simple/ratio/compound.
- **Honest scope limit:** cumulative/conversion are designed and emitted best-effort
  but have **no source fixture and no target validation** — explicitly provisional.

## The AST (`ir/expr.go`, `package ir`)

```go
// Expr is a node in a metric-definition expression tree.
type Expr interface{ isExpr() }

// Col is a physical column reference (Table may be "" when unqualified).
type Col struct{ Table, Name string }

// Raw is an opaque SQL fragment we do not decompose (e.g. a CASE expression).
// Columns lists the identifiers it references, for per-target qualification/wrapping.
type Raw struct{ SQL string; Columns []string }

// Ref references another metric by name (operand of a ratio/derived metric).
type Ref struct{ Metric string }

// Lit is a literal constant, already rendered ("1", "0", "100.0", "'x'").
type Lit struct{ Value string }

// Agg is an aggregation over a sub-expression, optionally filtered.
type Agg struct {
	Func   string // sum | count | count_distinct | avg | min | max | median | ...
	Arg    Expr   // usually a Col or Raw; nil for count(*)
	Filter Expr   // optional boolean Expr (a WHERE); nil if none
}

// Binary is an arithmetic/comparison/logical operation.
type Binary struct {
	Op          string // "+" "-" "*" "/" "=" "<" ">" "and" "or" ...
	Left, Right Expr
}

// Window wraps a base expression with a time window (cumulative / to-date).
type Window struct {
	Base   Expr
	Window string // e.g. "30 days"; "" means unbounded (to-date)
	Grain  string // time grain (day/week/month/…)
}

// Conversion is a funnel between a base and a conversion measure over a window.
type Conversion struct {
	Base, Conv Expr
	Entity     string // the converting entity
	Window     string
}
```

`isExpr()` is implemented (empty) on each concrete type. Node set is closed but
adding a new node is additive.

### Metric becomes metadata + a Def
```go
type Metric struct {
	Name        string
	Label       string
	Description string
	Synonyms    []string
	Grain       string   // per-metric agg-time grain (from the owning model's agg_time_dimension)
	Dimensions  []string // slice-by dimensions (nao-yaml); "" if unspecified
	Def         Expr     // the definition AST — replaces Expr/Kind/Agg/Column/Numerator/Denominator
}
```
(`Table.Grain` stays; `Metric.Grain` is the per-metric value.)

### How each shape maps
- **simple** `net_revenue` → `Agg{Func:"sum", Arg: Col{"fct_orders","order_net_booked"}}`
- **simple, count** `orders` → `Agg{Func:"count_distinct", Arg: Col{"fct_orders","order_id"}}`
- **compound** `refunded_orders` → `Agg{Func:"sum", Arg: Raw{"case when is_refunded then 1 else 0 end", ["is_refunded"]}}`
- **ratio** `aov` → `Binary{"/", Ref{"net_revenue"}, Ref{"orders"}}`
- **derived** → any `Binary` tree over `Ref`/`Lit`/`Agg`, e.g. `Binary{"-", Ref{"revenue"}, Ref{"cost"}}`
- **filtered** → `Agg{Func:"sum", Arg: Col{…}, Filter: Binary{"=", Col{…"is_refunded"}, Lit{"false"}}}`
- **cumulative** → `Window{Base: Ref{"revenue"}, Window:"30 days", Grain:"day"}`
- **conversion** → `Conversion{Base:…, Conv:…, Entity:"customer", Window:"7 days"}`

## Lowering (each emitter walks `Def`)

Emitters resolve `Ref` via a metric-name→`Metric` map (supersimple already builds a
global registry; Cortex builds one too).

### Cortex — `Def → SQL` (`renderSQL(Expr) string`)
A recursive lowering to a Snowflake SQL string (what Cortex metrics want):
- `Col{t,n}` → `T.N` (uppercased, qualified); `Raw` → its SQL with columns qualified
  (reuse `qualifyExpr`); `Lit` → value; `Ref{m}` → recursively render the referenced
  metric's `Def` (inline); `Agg{f,a,filter}` → `F(render(a))` or
  `F(CASE WHEN render(filter) THEN render(a) END)`; `Binary{op,l,r}` →
  `render(l) op render(r)`; `Window` → Cortex windowed SQL if expressible else
  degrade; `Conversion` → degrade (a `Notes` entry; Cortex has no funnel primitive).
- **Regression:** for today's simple/ratio/compound metrics this must reproduce the
  existing Cortex golden byte-for-byte (e.g. `SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)`).

### supersimple — `Def → operations` (pattern-match + degrade)
Recognize the shapes supersimple can express; degrade the rest to `NOTES.md`:
- `Agg{f, Col}` → `{aggregation:{type,key}}` (simple).
- `Agg{f, Raw}` → synthesized `property.sql` (via `toPropertySQL`) + `{aggregation:{type,key}}`.
- `Agg{…, Filter}` → a `filter` operation before the aggregation (best-effort) or degrade.
- `Binary{"/", A, B}` where A,B resolve to `Agg`s → the ratio `operations` pipeline
  (same-table or cross-table via `relationAggregate`, exactly as today).
- `Binary` (non-ratio) / `Window` / `Conversion` / anything else → **`NOTES.md`** with a reason.
- **Regression:** simple/compound/same-table-ratio/cross-table-ratio output must stay
  byte-identical to today's goldens.

Lowering is the emitter's job; the AST does not know about targets.

## dbt parser (`layer/dbt.go`)
Build `Metric.Def` from dbt metrics instead of the flat fields:
- `type: simple` → `Agg{measure.agg, Col|Raw(measure.expr), Filter?}` (a bare-column
  measure → `Col`; a compound expr → `Raw` with its referenced columns).
- `type: ratio` → `Binary{"/", Ref{numerator}, Ref{denominator}}`.
- `type: derived` → parse `type_params.expr` + `metrics:` into a `Binary`/`Ref`/`Lit`
  tree (a small formula parser; reuse the SQL lexer for tokens).
- `type: cumulative` → `Window{Ref{measure/metric}, window, grain}`.
- `type: conversion` → `Conversion{…}`.
- Populate `Grain` (from `defaults.agg_time_dimension`) and `Dimensions` on the metric.
- Metric filters (dbt `filter:`) → the `Filter` on the relevant `Agg`.

Unresolvable/unsupported still append to `ir.Model.Notes` (unchanged behavior).

## Migration & regression strategy
This touches the IR, dbt parser, both emitters, and all metric-bearing tests/goldens.
- Land it as a behavior-preserving refactor: after each step, `go test ./...` green and
  **all goldens byte-identical** (Cortex + supersimple). New capability (derived/filter/
  cumulative/conversion) is exercised by *new* fixtures/tests, never by changing an
  existing golden.
- A shared `renderSQL(Expr)` (in `layer`, used by Cortex and by supersimple's Raw/leaf
  handling) avoids duplicated lowering.

## Testing
- **Unit — AST construction:** dbt parser builds the expected `Def` tree for simple,
  compound, ratio (deep-equal on the tree).
- **Unit — renderSQL:** each node lowers to the expected SQL; a ratio/compound tree
  reproduces today's Cortex metric strings.
- **Unit — supersimple lowering:** `Agg{sum,Col}`→simple; `Binary{"/",…}`→pipeline;
  `Window`/`Conversion`→note (degradation).
- **New-capability fixtures:** a `derived` metric (`Binary{"-",Ref,Ref}`) → Cortex SQL
  `A - B`, supersimple → note; a `filter`ed metric → Cortex `SUM(CASE WHEN … )`,
  supersimple filter-op-or-note. cumulative/conversion: parser builds the node + a
  Cortex best-effort/degrade test (marked provisional, no live validation).
- **Regression:** all existing Cortex + supersimple goldens unchanged; full suite,
  gofmt, vet green.

## Scope
**In:** the `ir.Expr` AST + all node types; `Metric` reshaped to `{metadata, Def}`;
`Metric.Grain`/`Dimensions`; dbt parser building the AST for all dbt metric types;
Cortex `renderSQL` lowering; supersimple pattern-match lowering with degradation; the
`Expr` field removed; new-capability + regression tests.
**Out:** validating cumulative/conversion against a live target (no source/target for
them yet — provisional); non-metric IR changes.

## Open questions / risks (acknowledged)
- **supersimple lowering of arbitrary trees** is pattern-match-and-degrade — anything
  beyond agg/ratio/filter goes to `NOTES.md`. Accepted (the AST over-represents what
  any single target renders; degradation is the contract).
- **cumulative/conversion are unvalidated** (no fixture, no live round-trip). Emitted
  best-effort, marked provisional; correctness is a future item.
- **Derived-formula parsing** (dbt `derived.expr`) needs a small formula→tree parser;
  scope it to arithmetic over metric refs + literals, degrade exotic SQL to `Raw`.
