# semglot — clean CLI + layered config resolution — Design

**Date:** 2026-07-13
**Status:** Proposed design, pending review
**Builds on:** the `build` subcommand + `Configurable` emitters (cortex, snowflake-semantic-view, nao-yaml).

## Goal

Shrink the `build` CLI to a clean core — `--source`, `--target`, `--target-type` — and resolve model-identity settings (`database`, `schema`, `name`, `description`) through a **layered precedence** instead of a wall of required flags. Model identity is *data about the project*, not per-invocation operation parameters, so it should come from the source or a config file, with flags as overrides.

## Findings that shape the scope

- **No `dbt_project.yml` in the fixture, and the harness feeds a *materialized* reference** that likely lacks one. So the dbt-source layer is best-effort, and the `--config` file carries the practical weight for `database`/`schema`.
- **dbt's physical location is not a clean field.** It's resolved across `profiles.yml` (target db + credentials) and model configs/macros. semglot must NOT parse those. The dbt-source layer therefore reads only what is unambiguous and safe: the project `name`, and a project-level `+database`/`+schema` *if declared* in `dbt_project.yml`.
- **The flag rename breaks the eval-harness deploy caller** (`internal/deploy/semglot.go` + `semglot_test.go`, on the unmerged `feature/semglot-deploy-integration` branch). Updating that caller is a **decoupled follow-up in the harness repo**, out of scope here (see Migration).

## CLI shape

```
semglot build --source <path> --target <dir> --target-type <dialect>
              [--config <file>]
              [--database <db>] [--schema <schema>] [--name <name>] [--description <text>]
              [--source-type <dialect>]   # default: dbt
```

- **Core (the mental model):** `--source`, `--target`, `--target-type`.
- **Renames:** `--reference`→`--source`, `--out`→`--target`, `--layer`→`--target-type`.
- **Removed:** `--from` → replaced by `--source-type` (optional, default `dbt`; only matters when a second source dialect exists).
- **Overrides (optional):** `--config`, `--database`, `--schema`, `--name`, `--description`.
- `--source`, `--target`, `--target-type` are the only required flags; a Snowflake target additionally requires `database`/`schema` to resolve from *some* layer (clear error if none provides them).

**`--target` is a directory** (emitters write their conventional filename into it — matching today; supersimple writes several files). True file-path/stdout output is deferred: it would require changing the `Emitter` interface (`Emit(m, dir)`) across all six emitters — a separate enhancement, not bundled here.

## Layered resolution

Each identity key (`database`, `schema`, `name`, `description`) is resolved independently, highest present layer wins:

```
default  <  dbt source  <  --config file  <  explicit CLI flag
```

- **default:** `schema=MAIN`; `name` ← basename of `--source` (fallback `semantic_model`); `database`/`description` empty.
- **dbt source:** if `<source>/dbt_project.yml` exists — `name` ← its `name:`; `database`/`schema` ← `models: <project>: +database/+schema` **if declared** (best-effort; absent → skip).
- **--config file:** a flat YAML (loaded only when `--config` is passed) — see below.
- **explicit CLI flag:** wins over everything. "Explicit" means the user actually passed it — detected via `flag.FlagSet.Visit` (which reports only set flags), NOT by comparing against the zero value (so `--database ""` is distinguishable from "not passed", and a defaulted `--schema MAIN` does not silently outrank a config value).

### Config file (`--config <file>`)

Flat YAML, loaded ONLY when `--config` is given (no directory discovery, no cascade — keeps it to one predictable file):

```yaml
database: EVAL_MARTS
schema: MAIN
name: SV_ECOMM
description: Curated semantic layer over EVAL_MARTS.MAIN.
```

Unknown keys are ignored (forward-compatible). Missing keys fall through to the lower layers. The multi-environment pattern is `--config prod.yml` vs `--config staging.yml`.

## Resolution implementation

A single resolver in `cmd/semglot` (identity is a CLI concern, not a `layer` concern):

```go
type identity struct{ Database, Schema, Name, Description string }

// resolveIdentity layers: defaults <- dbt source <- config file <- explicit flags.
func resolveIdentity(sourceDir, configPath string, set map[string]bool, flags identity) (identity, error)
```

- `set` is the set of explicitly-passed flag names (from `fs.Visit`).
- Reads `dbt_project.yml` (best-effort; a parse error on a present file is fatal, absence is fine).
- Reads the config file when `configPath != ""` (a missing/invalid `--config` file is fatal — the user asked for it).
- Applies precedence per key.
- Returns the resolved identity, passed to `Configurable.WithOptions(database, schema, name, description)` exactly as today. **The `layer` package is unchanged.**

## Scope

**In:** the flag rename; `--config` loader (flat YAML); `dbt_project.yml` reader (`name` + best-effort project-level `+database`/`+schema`); the layered resolver + `fs.Visit` explicit-flag detection; updated `usage()`/flag help (target-neutral, and the `-target-type` help lists the registered dialects); a `dbt_project.yml` added to the ecommerce fixture to exercise the source layer; the CLI end-to-end test updated to the new flags; unit tests for the resolver's precedence.

**Out:**
- File-path / stdout `--target` mode (needs an `Emitter` interface change — deferred).
- Directory discovery / cascading config (sqlfluff-style) — YAGNI for ~4 keys; explicit `--config` only.
- Parsing `profiles.yml`, per-model configs, or dbt macros for db/schema — semglot does not resolve dbt's full physical-location model.
- Updating the eval-harness deploy caller — decoupled follow-up (see Migration).
- Backward-compatible flag aliases — clean break (see Migration).

## Migration (breaking change)

The renamed flags break `eval-harness/internal/deploy/semglot.go` (`build --from dbt --reference … --layer … --out …`) and `internal/deploy/semglot_test.go` (asserts those flag strings). This is a **one-line update in the harness repo** (swap to `--source-type dbt --source … --target-type … --target …`), to be done when that unmerged branch is next touched. semglot stays decoupled; we do not add compatibility aliases (they would defeat the "clean" goal).

## Testing

- **Resolver unit tests** (`cmd/semglot`): precedence per key — default only; dbt-source overrides default; config overrides dbt-source; explicit flag overrides config; `--database ""` (explicit empty) beats a config value; defaulted `--schema` does NOT outrank config.
- **Config-file test:** a temp `semglot.yml` populates identity; unknown keys ignored; missing `--config` file errors.
- **dbt-source test:** a `dbt_project.yml` with `name` + `models: <proj>: +database/+schema` populates identity; absence is a no-op.
- **CLI end-to-end** (`TestCLIBinaryEndToEnd`): updated to the new flags; still asserts the emitted golden is byte-identical.
- Existing emitter goldens unchanged (this is a CLI-layer change; `layer` is untouched).
- `go build/test`, `gofmt -l`, `go vet` clean.
