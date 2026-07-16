# semglot config profiles: design

Date: 2026-07-16

## Goal

Replace semglot's flat, single-target configuration with **named profiles**. A
profile bundles a source, a target dialect, an output directory, and identity
settings, so one command builds one fully-specified output. Multiple targets and
multiple environments (staging vs production) are expressed as separate profiles.

## Motivation

Today a `semglot build` run takes ad-hoc flags (`--source`, `--target`,
`--target-type`, `--database`, `--schema`, `--name`, `--description`) plus an
optional flat `--config` file, resolved as `defaults < config < flags`. To build
three dialects across two environments you run six invocations, each with its own
flags or hand-maintained config. There is no single place that says "here are all
my outputs." Profiles fix that.

## Config file

Default path `./semglot.yaml`, overridable with `--config <path>`. Every field:

```yaml
# semglot.yaml
# One entry per named profile. Each profile is a complete, self-contained build.
# Build one with:  semglot build --profile <name>
profiles:

  view_prod:
    source: ./models                # REQUIRED. dbt source dir, or a list of dirs when
                                     #   schema files span folders: [./a, ./b]
    source-dialect: dbt             # optional. Dialect to parse. Default: dbt
    target-dialect: snowflake-semantic-view
                                     # REQUIRED. Output dialect. One of:
                                     #   dbt | cortex | snowflake-semantic-view |
                                     #   supersimple | nao-yaml | nao-context-rules
    output: ./out/view              # REQUIRED. Directory to write the emitted layer into
    database: ANALYTICS             # REQUIRED for Snowflake targets (cortex,
                                     #   snowflake-semantic-view). Ignored otherwise.
    schema: SEM                     # optional. Warehouse schema. Default: MAIN
    model-name: catalog             # optional. Name of the emitted model / view.
                                     #   Default: basename of the source dir
    description: Curated semantic view over orders.
                                     # optional. Free text written into the output

  cortex_staging:                   # staging is just another profile (different database)
    source: ./models
    target-dialect: cortex
    output: ./out/cortex-staging
    database: DEV
```

Rules:

- `profiles` is a map of profile name to profile.
- Each profile is fully self-contained. There is no shared/base block and no
  inheritance. Staging vs production are two profiles that repeat what they share.
- `source` accepts a single directory (string) or a list of directories, matching
  the current repeatable-source behavior.
- Fields: `source`, `source-dialect`, `target-dialect`, `output`, `database`,
  `schema`, `model-name`, `description`.

## CLI

```sh
semglot build --profile view_prod                              # reads ./semglot.yaml
semglot build --profile view_prod --config path/to/semglot.yaml
```

- `--profile <name>` is **required**; it is the one way to build.
- `--config <path>` is optional; defaults to `./semglot.yaml`.
- One profile per run. Building several means several invocations.

Removed flags (breaking change): `--source`, `--target`, `--target-type`,
`--source-type`, `--database`, `--schema`, `--name`, `--description`.

## Resolution and defaults

No flag layering remains: the profile specifies the build. Built-in defaults fill
only omitted optional fields:

- `source-dialect` defaults to `dbt`.
- `schema` defaults to `MAIN`.
- `model-name` defaults to the basename of the (first) source directory.

## Validation and errors

`build` fails with a clear message when:

- the config file does not exist or does not parse;
- `--profile <name>` is not present in the config;
- a profile omits a required field (`source`, `target-dialect`, or `output`);
- a Snowflake target (`cortex`, `snowflake-semantic-view`) has no `database`.

## Implementation impact

- `cmd/semglot/config.go`: replace the flat `configFile` struct and
  `resolveIdentity` with a profiles loader. It parses the config file, selects the
  named profile, applies defaults, validates, and returns a resolved build spec
  (source list, source-dialect, target-dialect, output, and identity).
- `cmd/semglot/main.go`: `build` parses only `--profile` and `--config`; the old
  flags are removed. It loads the spec, resolves parser and emitter by dialect, and
  runs the build. The Snowflake-database check moves to profile validation.
- Tests: `cmd/semglot/config_test.go` and `main_test.go` are rewritten around a
  `semglot.yaml` fixture; the integration test invokes the compiled CLI with
  `--profile`.
- README: the Usage example and Configuration section are rewritten to the profile
  model.

## Out of scope (YAGNI)

- Shared defaults or profile inheritance.
- Building multiple profiles in one run.
- Environment overlays as a first-class concept (environments are just profiles).
- New source or target dialects.
