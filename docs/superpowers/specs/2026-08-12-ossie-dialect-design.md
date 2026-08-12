# ossie dialect (Apache Ossie / Open Semantic Interchange): design

Date: 2026-08-12

## Goal

Add `ossie` as semglot's second **source** dialect and its eighth **target**,
reading and writing the [Apache Ossie](https://github.com/apache/ossie) Core
Metadata Specification. Ossie is a vendor-neutral interchange format for semantic
models, so supporting both directions turns every existing emit-only dialect into
a destination reachable from an OSI file, and makes semglot's IR reachable from
any tool that already exports OSI.

## Scope: which Ossie spec

Apache Ossie publishes more than one layer. This work targets the **Core Metadata
Specification** only:

- `core-spec/osi-schema.json` and `core-spec/spec.yaml`, version `0.2.0.dev0` —
  the `semantic_model` / `datasets` / `fields` / `relationships` / `metrics`
  shape.
- **Not** the ontology layer (`examples/flights.yaml`), which is a different
  `concept` / `ValueType` / `requires` vocabulary with no IR counterpart.

The spec is explicitly a draft ("schema may change before 0.2.0 is released").
The version string is a pinned const in the emitter, not a computed value.

## Files

| File | Contents |
|---|---|
| `dialect/ossie.go` | OSI YAML structs, `Name`, `Parse` |
| `dialect/ossie_emit.go` | `Emit`, `WithOptions` |
| `dialect/sqlexpr.go` | shared SQL-expression parser (extracted from `dbt.go`) |
| `dialect/ossie_test.go`, `dialect/ossie_emit_test.go` | unit + golden tests |
| `test/models/ossie/` | fixtures, including vendored upstream files |
| `ir/model.go` | `Table.Synonyms` (see IR change) |
| every `dialect/*.go` emitter | table synonyms, structural or prose |

Registered via `init()` as `ossie`, implementing `Parser`, `Emitter`, and
`Configurable`.

Emit writes one `semantic_model.yaml`. Parse globs `*.yml` **and** `*.yaml` from
each source directory (non-recursive) — every existing parser globs `*.yml` only,
but Ossie ships `.yaml` — and skips any file with no top-level `semantic_model:`
key, so mixed directories work.

Multiple `semantic_model[]` entries, and multiple files, merge into the single
`ir.Model` the IR provides.

## Mapping

| IR concept | OSI construct |
|---|---|
| `Table` | `datasets[]`. `source` is `Database.Schema.Name` on emit; dropped on parse (the IR has no source-table field) |
| `Field` (dimension) | `fields[]` with `dimension.is_time: false` |
| `Field` (time dimension) | `fields[]` with `dimension.is_time: true` |
| `Measure` | a `fields[]` entry for its column **and** a model-level `metrics[]` entry |
| `Metric` | model-level `metrics[]`, `expression` = `renderSQL(Def)` |
| `Table.PrimaryKey` | `primary_key: []` (composite supported natively) |
| `Relationship` | `relationships[]`: `from`/`to` = `Left`/`Right`, zipped `from_columns`/`to_columns` (composite supported natively) |
| `Table.Synonyms` (new), `Field.Synonyms`, `Metric.Synonyms` | `ai_context.synonyms` (round-trips structurally) |
| `Description` | `description` |
| `Model.Notes` | model-level `ai_context.instructions` |
| `Field.Enum`, `Metric.Label`, `Metric.Dimensions` | folded into the field's / metric's own `description` |
| `Table.Grain` | folded into the dataset's `description` |

Emit writes exactly one `semantic_model[]` entry, named from `Options.Name` with
`Options.Description`, since the IR holds one model.

`ai_context` is `string | object` in the schema. Parse accepts both: an object
reads `synonyms` and `instructions`; a bare string is treated as `instructions`.

## IR change: `Table.Synonyms`

Ossie carries `ai_context.synonyms` on datasets — its own TPC-DS example leans on
them heavily (`"sales transactions"`, `"POS data"`) — and `ir.Table` has no
synonyms field, only `ir.Field` and `ir.Metric` do. Rather than degrade
dataset-level synonyms to prose on parse, add them to the IR:

```go
// Table is one grain/entity in the layer.
type Table struct {
    // ...
    Synonyms []string // alternative names for the table/entity
}
```

This is a genuine pre-existing gap, not an ossie quirk: two shipped targets have
a table-level synonym slot semglot does not currently fill. Wire it across every
dialect, structurally where a slot exists and as prose where none does.

| Dialect | Table synonyms land in |
|---|---|
| `ossie` | `ai_context.synonyms` on the dataset (parse + emit) |
| `dbt` | model-level `meta.synonyms` (parse + emit) |
| `snowflake-semantic-view` | `with synonyms (...)` on the table (emit) |
| `cortex` | `synonyms:` on the table (emit) |
| `nao-yaml`, `nao-context-rules`, `lightdash`, `databricks-metric-view`, `supersimple` | folded into the table/model description via `appendClause` |

Notes on the mechanics:

- `svSynonyms` (`snowflake_semantic_view.go:205`) already documents that Snowflake
  accepts synonyms "on tables, dimensions, facts, and metrics" and renders the
  clause; the table case is just not called yet.
- `cortexTable` has no `synonyms` field today while `cortexCol` does. Confirm
  table-level support against Snowflake's Cortex Analyst YAML spec before adding
  the key; if unsupported, `cortex` degrades to prose with the rest.
- `dbtModel` has no `Meta` field at all — only `dbtColumn` does. Add one carrying
  `synonyms`, mirroring the existing column convention, and honour the
  `DbtMetaKeyPath` option (`meta:` vs `config.meta:`) exactly as the column path
  does. Without this, `ossie → dbt → ossie` drops table synonyms.
- The prose fold reuses `synonymClause` / `appendClause` (`enum.go:65`), which is
  how these five dialects already degrade *column* synonyms.
- This closes the **`supersimple` synonyms** gap `dialect/README.md` currently
  records under "Gaps vs. limits", since the helper finally gets wired there.

All golden fixtures are regenerated (`UPDATE_GOLDEN=1 go test ./...`) and the diff
reviewed, per `CONTRIBUTING.md`.

### Measures emit as both a field and a metric

OSI defines `fields` as the operands of metric expressions and has no measure
concept, so a measure needs both halves. This matches what Ossie's own
`converters/dbt/msi_to_osi.py` produces: a dbt measure `revenue`
(`agg: sum, expr: amount`) becomes a field `revenue` with expression `amount`
*and* a metric `revenue` with expression `SUM(orders.amount)`.

The same name in both is correct and not a collision — `fields` are
dataset-scoped, `metrics` are model-scoped.

Metric expressions reference the **physical column** (`SUM(orders.amount)`), not
the field name (`SUM(orders.revenue)`). This follows the reference converter, and
is what `renderSQL` already produces from an `ir.Col`. A measure over a raw
expression becomes a field carrying that expression, named after the measure.

### Data types

Bidirectional against OSI's logical enum:

| IR (SQL type string) | OSI |
|---|---|
| `varchar`, `char`, `text` | `String` |
| `integer` | `Integer` |
| `decimal`, `numeric` | `Decimal` |
| `float`, `double` | `Float` |
| `boolean` | `Boolean` |
| `date` | `Date` |
| `time` | `Time` |
| `timestamp`, `datetime` | `DateTime` |
| `timestamp_tz`, `timestamptz` | `DateTimeTz` |

An unrecognised type **omits** `datatype` entirely, as the spec directs. `Opaque`
is never used as a catch-all. `Integer` and `Decimal` map back to `integer` and
`decimal` rather than collapsing both to `number`, so the round-trip is stable.

### Time dimensions

On parse, `is_time` resolves per spec: an explicit value always wins; when unset
it defaults to `true` for `Date`, `Time`, `DateTime`, and `DateTimeTz`, and
`false` otherwise. A field resolving to `true` lands in `Table.TimeDimensions`.

### Relationships

`from` is the many side and `to` is the one side, which is exactly the IR's
`Left`/`Right` convention (`dbt.go:489`). Columns zip pairwise; a length mismatch
between `from_columns` and `to_columns` is a parse note, not a silent truncation.

OSI requires a unique relationship `name`, so emit derives it from the table pair
plus the existing `relRoleSuffix` helper (`dialect.go:115`) — the same
role-playing-dimension disambiguator cortex, snowflake-semantic-view, and
databricks-metric-view already share, so all four name a role-played join alike.

### Expression dialects

Emit writes `ANSI_SQL` only, and does **not** emit the top-level `dialects:` key
their converter produces (see Divergences).

Parse prefers the `ANSI_SQL` entry. When absent, it falls back silently if
exactly one dialect is present (Ossie's own Databricks fixtures are
`DATABRICKS`-only, and noting every field there would be pure noise), and falls
back to the first entry **with a note** when several non-ANSI dialects are
offered.

## SQL expression parsing

OSI metrics are SQL strings, so `Parse` needs SQL → `ir.Expr`. `dbt.go` already
has `derivedParser`: precedence-climbing recursive descent over `sqlTokens`,
handling `+ - * /` and parens. It is the right engine but its leaf rule maps a
bare identifier to `ir.Ref` and it has no function-call rule.

Extract it to `dialect/sqlexpr.go` and parameterize the leaf handling, giving two
entry points over one shared parser:

- `parseDerivedExpr` — **unchanged** dbt behaviour: bare ident → `Ref`, no calls.
- `parseAggExpr` — new, for ossie: `table.column` → `Col`, `SUM`/`COUNT`/`AVG`/
  `MIN`/`MAX`/`MEDIAN` → `Agg`, `COUNT(DISTINCT x)` → `count_distinct`,
  `COUNT(*)` → `Agg{Arg: nil}`.

The leaf rules stay separate deliberately. If dbt's parser started accepting
calls, a dbt derived expression that today degrades to a note would instead parse
into something else — a silent behaviour change in a shipped dialect.

A parsed metric homes on the table of its first referenced dataset, the rule
`dbt.go:604` already uses for cross-table derived metrics. An expression that
does not parse cleanly becomes a `Model.Notes` entry and is skipped; semglot does
not guess an AST.

Per the accepted design decision, an OSI metric that is a plain aggregation over
a single column additionally synthesises an `ir.Measure` on the owning table,
sharing the metric's name — the same shape `dbt.Parse` produces for a
`type: simple` metric, so every emitter behaves identically regardless of source.

## Degradations

**Emit warnings:**

- `Window` and `Conversion` metrics have no OSI primitive. Omit and warn, the
  same judgement `cortexDegrade` applies.
- A measure and a metric sharing a name both land in the single flat `metrics:`
  list. The metric wins, the measure degrades to a note — the precedent already
  documented in `dialect/README.md`.

**Parse notes:**

- Metric expressions that do not parse.
- Expression available only under several non-ANSI dialects.
- `from_columns` / `to_columns` length mismatch.

**Format limits** (documented in `dialect/README.md` as limits, not gaps):

- No measure concept, so a measure no metric publishes returns from a round-trip
  as a published metric. `dbt → ossie → dbt` is therefore lossy in a way
  `dbt → dbt` is not.
- No per-table grain, metric label, field label, or enum-value slot.
- No source-table identity on parse.

## Divergences from the reference converters

Recorded because the differential tests below must tolerate them:

- Ossie's dbt converter emits a top-level `dialects: [ANSI_SQL]` key that
  **`osi-schema.json` rejects** (`additionalProperties: false` permits only
  `version` and `semantic_model`). semglot does not emit it.
- Their ratios parenthesize aggressively (`(x) / (y)`); `renderOperand`
  parenthesizes only compounds. Semantically identical, textually different.
- They uppercase aggregate function names (`SUM(...)`); `renderSQL` emits neutral
  lowercase SQL. Textual only.
- They desugar a `COUNT` measure over column `x` into
  `SUM(CASE WHEN x IS NOT NULL THEN 1 ELSE 0 END)` — both in the metric
  expression and in the dataset field the measure contributes. semglot emits
  plain `COUNT(x)`: it is the ANSI spelling of the same aggregate, it is what
  every other semglot target emits for a dbt `agg: count`, and it is better
  behaved over an empty input (`COUNT(x)` is 0, their `SUM(CASE …)` is NULL).
- Their Databricks converter picks the fact table as `source` and folds the rest
  in as `joins`, while semglot's `databricks-metric-view` emitter writes one view
  per table. Their paired `*_metric_view.yaml` files are therefore **not** valid
  expected output for semglot — they are parse inputs only.

Filtered measures and inlined ratios already agree: they flatten a filter to
`SUM(CASE WHEN … THEN … END)`, the identical shape `renderSQL:59` builds, and
they inline referenced metrics' SQL as `metricResolver` does.

## Testing

### A. Vendored parse conformance

Upstream OSI documents as parse inputs, asserting the resulting IR and that no
unexpected notes are produced:

- `examples/tpcds_semantic_model.yaml`
- `converters/databricks/tests/fixtures/*_ossie.yaml`
- the OSI documents embedded in
  `converters/dbt/tests/__snapshots__/test_msi_to_osi.ambr`

Between them these cover multi-dialect expressions, flattened filters, nested
derived metrics, composite primary and foreign keys, and three-way relationships.

### B. Cross-implementation agreement on `dbt → ossie`

Their snapshots pair a dbt input with the OSI output *their* converter produces.
Vendor both, run semglot's `dbt → ossie`, and compare **semantically** — parse
both sides into the OSI structs, normalize key order and expression whitespace,
and diff.

The comparison carries an explicit allowlist of accepted divergences (the
Divergences section above). That allowlist is the real artifact: it documents
exactly where semglot and the reference implementation disagree, and any new
entry appearing is a regression signal.

### C. Round-trip information-loss report

A differ over two `*ir.Model` values returning a structured loss report, driving
assertions for `dbt → ossie → dbt` and `ossie → dbt → ossie` against a documented
allowlist (unpublished measures becoming published, enum structure, grain, field
labels).

This generalizes beyond ossie: `ir/model.go`'s package comment already
anticipates the IR being "the unit of the fairness index", and this is that
measurement.

### Vendoring and licensing

Apache Ossie is Apache-2.0; semglot is MIT. Vendoring their fixtures is permitted
with attribution. Vendored files keep their ASF headers, and
`test/models/ossie/vendor/README.md` records upstream provenance, the pinned
commit `88e0011148283302c9a04cd0287e00e0b9d87354` (2026-07-31), and the license.

## Documentation

- Top-level `README.md`: add an `ossie` row with both Source and Target ticked.
  The surrounding prose currently states dbt is "the only source" and that
  `dbt` → `dbt` is *the* lossless round-trip; both need rewriting now that a
  second source exists.
- `dialect/README.md`: add an `ossie` column to the mapping table, update the
  **Direction** section (which asserts dbt is the only source), and record the
  format limits above under **Gaps vs. limits**. The **Synonyms** row gains a
  table-level counterpart across every column, and the `supersimple` synonyms
  entry moves out of "Gaps" because this change closes it.

## Out of scope (YAGNI)

- The Ossie ontology layer (`concept` / `ValueType` / `requires`).
- `custom_extensions` in either direction — the accepted decision is prose-only
  degradation, so no `vendor_name: SEMGLOT` block is written or read.
- A live differential harness invoking Ossie's Python converters. It would catch
  upstream drift, but semglot's CI is Go-only with one dependency and this puts
  Python and `uv` on the path.
- JSON-schema validation of emitted output, which would add a dependency.
- Collapsing `ir.Measure` into `ir.Metric`. Considered and rejected: measure and
  metric names are independent and both meaningful, measures exist that no metric
  publishes, and targets have separate slots for the two (cortex `facts[]` vs
  `metrics[]`, lightdash column-level vs model-level). A unified type would need
  a `Published` flag and a second name field — `Measure` again, renamed.
