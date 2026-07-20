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
| `okf` | a bundle directory: `tables/<model>.md`, `metrics/<metric>.md`, `notes.md`, plus the reserved `index.md` in every directory |

## Mapping

Cells name the construct the IR concept becomes. Notation: `<->` dbt reads it
back into the IR too; `text` the value survives only as prose folded into a
description or comment; `--` not emitted (see [Gaps vs. limits](#gaps-vs-limits)).

| IR concept | `dbt` | `cortex` | `snowflake-semantic-view` | `supersimple` | `nao-yaml` | `nao-context-rules` | `okf` |
|---|---|---|---|---|---|---|---|
| Table | `models:` + `semantic_models:` `<->` | `tables[].base_table` | `tables (...)` | one file per model | `--` | "Table reference" (if described) | one `type: Table` concept per model |
| Column / dimension | column + `dimensions type: categorical` `<->` | `dimensions[]` | `dimensions (...)` | `properties` | `dimensions[]` (deduped) | listed if described | "Dimensions" bullet |
| Time dimension | `dimensions type: time` + `agg_time_dimension` `<->` | `time_dimensions[]` | plain dimension (not marked as time) | `properties` (Date) | `dimensions type: date` | with dimensions | "Time dimensions" section |
| Data type | column `data_type` `<->` | `data_type` | `--` | property `type` | `--` | `--` | parenthesized after the column name |
| Primary key | `primary_key` constraint + primary entity `<->` | `primary_key` | `primary key (...)` | `primary_key` | `--` | `--` | "Primary key" section |
| Relationship / join | `relationships` test on the FK column `<->` | `relationships[]` | `relationships (...) references` | `relations` (hasMany, join_key) | `--` | "Joins & routing" | "Joins", as a relative link to the other concept |
| Description | `description` `<->` | `description` | `comment='...'` | `description` | `description` (field/metric) | prose | frontmatter `description` + body prose |
| Synonyms | `meta.synonyms` on the column `<->` | `synonyms:` | `with synonyms (...)` | `--` (gap) | `text` (into description) | `text` (into description) | `text` (into the bullet) |
| Enum / allowed values | `accepted_values` test + `meta.enum` `<->` | `sample_values` + `text` | `text` (into comment) | `text` (into description) | `values:` | "Allowed values" | "Allowed values" section |
| Simple metric (aggregation) | `measures` + `metrics type: simple` `<->` | `facts[]` | `metrics (...)` | metric aggregation | metric `source{table,column,aggregation}` | "Key metrics reference" | one `type: Metric` concept |
| Ratio / derived metric | `type: ratio` / `type: derived` `<->` | `expr` (rendered SQL) | inline SQL in `metrics (...)` | division ratio -> pipeline; other arithmetic -> `NOTES.md` | `type: derived`, `formula` | rendered SQL | rendered SQL in a fenced block |

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
- **Data types in `snowflake-semantic-view` and the nao dialects.** Omitted on
  purpose: these formats lean on the synced source-table schema for types rather
  than restating them in the semantic doc.
- **`okf` is emit-only.** The spec prescribes no type taxonomy and puts meaning in
  free prose, so reading a bundle back into the IR would be a heuristic markdown
  scraper rather than a parser.

### `okf`: the spec and the reference implementation disagree

`okf/SPEC.md` says exactly one frontmatter field is required, `type`. The
reference implementation is stricter: `OKFDocument.validate()` in
`okf/src/reference_agent/bundle/document.py` requires **type, title, description
and timestamp** to all be non-empty. The reference implementation is what
actually reads bundles, so semglot satisfies the stricter of the two. What that
means in practice:

- **Descriptions are synthesized when the IR has none.** A metric falls back to
  its rendered definition, a table to "The `<name>` table." Neither is guaranteed
  by the IR, and an empty one fails validation.
- **`timestamp` is caller-supplied, never a clock.** It comes from `Options`
  (profile `timestamp` field, else the source's last commit date). A clock would
  make two builds of the same checkout differ byte-for-byte, which the goldens
  rely on. With no timestamp available the field is omitted, and the bundle is
  spec-conformant but fails the reference validator.
- **`index.md` matches `regenerate_indexes` byte-for-byte.** index.md is reserved
  and machine-generated upstream: one `#` section per concept type, sections
  sorted by type and entries by title, links relative to the directory,
  subdirectories under a "Subdirectories" section. Matching it means their
  tooling is a no-op on our bundles instead of rewriting them.
- **Links are directory-relative**, as in the published reference bundles
  (`../metrics/aov.md`), not bundle-absolute. The spec recommends absolute for
  stability, but stability is moot for a generated bundle, and relative is what
  the reference viewer resolves.
- **`resource` reuses the profile's `database`/`schema`** and renders as
  `table://DATABASE/SCHEMA/TABLE`. Without a `database` the field is dropped
  rather than emitted half-qualified.

### Testing `okf` against upstream

Three layers, in the order they catch things:

1. `test/okf_conformance_test.go` re-implements the reference rules in Go
   (frontmatter parses, required keys non-empty, every link resolves). Runs in CI
   with no extra toolchain.
2. `test/okf_contract_test.py` runs the *real* `OKFDocument.validate()` and
   `regenerate_indexes()` over the golden bundle. Not in CI (it needs a Python
   3.11+ venv and a clone of upstream); run it by hand when touching the emitter
   or when upstream moves. Setup instructions are in the file's docstring.
3. `python -m reference_agent visualize --bundle <dir>` renders the bundle as the
   reference viewer draws it. The edge count is the useful signal: it is how many
   of our links the viewer actually resolved.

Last verified against `knowledge-catalog @ main` on 2026-07-20: all concepts
validate, `regenerate_indexes` is a no-op, and the viewer renders 13 concepts
with 24 edges.

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
