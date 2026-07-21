# semglot - DRY dialect emitters - Design

**Date:** 2026-07-21
**Status:** Proposed
**Builds on:** the `Emitter`/`Configurable` pattern (`dialect/dialect.go`), the expression-AST metric model (`ir.Expr`, `dialect/render_sql.go`), and the non-fatal `warnings []string` channel added in `0c47b08`.

## Goal

Remove the two kinds of duplication that have accumulated across the seven dialect emitters, without changing a single byte of emitted output.

1. **Plumbing duplication.** Every emitter repeats the same directory-creation and file-writing tail, and five of them repeat the same `WithOptions` field-copy. The identity-passing dance is repeated at every call site.
2. **SQL-rendering sprawl.** Three dialects render SQL from more than one function, so there is no single place to look to answer "what SQL does this target emit". Two `do not merge these` comments exist precisely because near-identical tree walks sit next to each other while needing to behave differently.

Non-goal: unifying the tree walks themselves. Each dialect keeps its own walk. The rule is one SQL function per dialect, not one SQL function overall.

## Part 1: Emit returns files, and takes Options

### The change

```go
// File is one emitted artifact, named relative to the output directory.
type File struct {
	Name string
	Data []byte
}

type Emitter interface {
	Dialect
	Emit(m *ir.Model, o Options) (files []File, warnings []string, err error)
}
```

`Configurable` and every `WithOptions` method are deleted. `Options` keeps its current fields (`Database`, `Schema`, `ViewSchema`, `Name`, `Description`).

A single writer, `dialect.WriteFiles(dir string, files []File) error`, performs the `os.MkdirAll(dir, 0o755)` and the per-file `os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644)`. `cmd/semglot` calls it once after `Emit` returns.

### What this removes

- 7 `os.MkdirAll` calls and 8 `os.WriteFile` calls, in `cortex.go`, `databricks_metric_view.go`, `dbt_emit.go`, `nao_context_rules.go`, `nao_yaml.go`, `snowflake_semantic_view.go` and `supersimple.go` (which writes both per-model files and `NOTES.md`).
- 5 `WithOptions` methods and the 5 duplicated identity field sets on the emitter structs.
- The `if c, ok := e.(Configurable); ok { e = c.WithOptions(...) }` assertion at 5 call sites: `cmd/semglot/main.go:68`, `test/context_layer_test.go:20`, and `test/integration_test.go` at 157, 418 and 490.

### Why this shape

Emitters become pure functions of `(model, options)`. Nothing in the emit path touches the filesystem, so a test asserting on emitted content no longer needs `t.TempDir()`, a write, a read-back and a YAML re-parse. Roughly 25 test sites simplify to indexing a `[]File` and asserting on bytes.

Emitter structs lose their identity fields entirely and become empty types again, which restores the property the registry assumes: `Register` stores one shared, stateless instance per dialect. Today that property holds only because `WithOptions` returns a copy.

### Migration notes

- Emitters that currently return early on a write error instead return the accumulated `files` and the error. No emitter needs partial-write semantics; today a mid-loop failure in supersimple already leaves a partial directory, and the new shape makes the write atomic-per-run at the call site instead.
- `dialect` is consumed by `cmd/semglot` and the tests only, so this breaking change is contained. `dialect/README.md` documents the interface and must be updated in the same change.
- Ordering of `files` is the emitter's existing write order, so `WriteFiles` reproduces today's on-disk result exactly.

## Part 2: One SQL-rendering function per dialect

### The rule

Each dialect has at most one function that turns IR into SQL. Its recursion lives in an internal closure rather than in sibling top-level helpers. It may call shared primitives (`renderANSI`, `aggExpr`, `qualifyExpr`, `sqlTokens`), but a reader looking for "what SQL does this dialect emit" has exactly one function to read.

### The shared signature

```go
// sqlRenderer turns one metric definition into target-specific SQL. Every
// dialect that renders metric SQL has exactly one. An error is a degrade
// reason, not a failure: the caller turns it into a warnings entry and drops
// the metric.
type sqlRenderer func(def ir.Expr, c sqlCtx) (sqlResult, error)

type sqlCtx struct {
	Resolve func(name string) (ir.Expr, bool) // metric lookup; nil means do not inline refs
	Table   string                            // owning table, for column qualification
	TableOf map[string]string                 // metric name -> table, for qualified metric refs
}

type sqlResult struct {
	SQL  string
	Refs []string // metric names left as bare references; only dbt consumes this
}
```

This is a function type, not an interface method. The renderer is an implementation detail of `Emit`, and no consumer outside the dialect package calls it, so it does not belong on `Emitter`. Keeping it a function type still gives the uniformity the interface was wanted for: the compiler enforces the signature, the implementations are interchangeable, and one table test can run all of them over a shared IR corpus.

### Per-dialect state, before and after

| dialect | today | after |
| --- | --- | --- |
| cortex | `renderSQL` call plus `ToUpper` | `renderANSI`, uppercased at the call site |
| nao-yaml | `renderSQL` call | `renderANSI` |
| nao-context-rules | `renderSQL` call | `renderANSI` |
| snowflake-sv | `renderSVMetricDef`, `renderSVDerived`, `svNoResolve` | `renderSnowflakeSV` |
| dbt | `renderDerived`, `parenIfBinary`, `emitFilterSQL` | `renderDBT` |
| databricks | `renderSQL`, `dbxStripSourceQualifier`, `aggExpr`, `dbxValidMeasureExpr` | `renderDatabricksSQL` |
| supersimple | `toPropertySQL` | `toPropertySQL`, unchanged |

`render_sql.go` survives as the home of the shared primitives. `renderSQL` is renamed `renderANSI` to say what it is, and `renderOperand`/`isCompound` are folded into it as closures. `metricResolver`, `colSet`, `qualifyExpr` and `aggExpr` stay shared, because `qualifyExpr` and `aggExpr` are also used by the dbt *parser* (`dialect/dbt.go`), which is not part of this refactor.

### Supersimple is a deliberate exception

Supersimple emits structured YAML, not metric SQL. Its only SQL work is `toPropertySQL`, which rewrites identifiers inside a raw fragment rather than walking a tree, so it has a different signature and no `sqlRenderer`. Forcing it into the shared shape would mean a return type loose enough to stop the compiler from helping.

### The two "do not merge" comments

`dialect/render_sql.go:79` and `dialect/dbt_emit.go:421` currently warn against deduplicating `renderOperand`/`isCompound` with `parenIfBinary`. They are correct that the behaviors must differ: cortex inlines a referenced metric's SQL, so a `Ref` may resolve to a compound expression needing parens, while dbt preserves metric names as bare refs, so a `Ref` is never compound. After this change the two walks live in separate, separately named functions rather than as neighbours that look mergeable, so the warning is no longer load-bearing. Both comments are replaced by a one-line note on each renderer stating its own ref policy.

### Degrade unification

`ok=false` returns, `cortexDegrade`, `dbxDegrade` and `ssDegradeReason` currently express the same idea in four shapes: this target cannot represent this metric, so drop it and tell the user. The `error` return from `sqlRenderer` becomes the single channel, and its message becomes the `warnings` entry. Callers keep their existing pre-checks where those decisions are made before rendering is attempted (for example the Window and Conversion metric kinds, which are omitted before any renderer sees them).

## Testing

The refactor must not change emitted output. Two layers guard that.

1. **Golden output, unchanged.** The existing integration tests in `test/integration_test.go` compare emitted artifacts for all targets against `test/models/ecommerce/dbt/*`. These fixtures are not regenerated. They pass byte-identical before and after, which is the primary safety net for the whole change.
2. **New shared renderer test.** An unexported `sqlRenderers` map keyed by dialect name, consumed only by a table test that runs every registered renderer over one IR corpus (a simple agg, a filtered agg, a ratio of two metric refs, a nested binary needing parens, a raw fragment, an unknown metric ref) and golden-files the results side by side. The map also fails the test when a dialect is added without a renderer. It is not used for dispatch.

Unit tests in `dialect/*_test.go` move off tempdirs onto the returned `[]File` as they are touched, not as a separate sweep.

## Implementation order

Two independent commits, each green on its own.

1. Part 1, the `Emit` signature, `File`, `WriteFiles`, and the removal of `Configurable`. Mechanical, touches every emitter and every call site.
2. Part 2, one renderer per dialect, starting with snowflake-sv (smallest), then dbt, then databricks.

## Risks

- **Silent output drift** in the databricks renderer, which is the most tangled today because `dbxStripSourceQualifier` post-processes a rendered string. Folding it into the renderer must preserve the case-insensitive table-prefix match documented at `dialect/databricks_metric_view.go:396`. The golden fixtures cover this.
- **Scope creep into the dbt parser.** `dialect/dbt.go` is 975 lines and shares `qualifyExpr`/`aggExpr` with the emit path. It is out of scope. Those two helpers stay where they are.
