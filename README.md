# semglot

A semantic-layer transpiler. Where [`sqlglot`](https://github.com/tobymao/sqlglot)
translates across SQL dialects, **semglot** translates across **semantic-layer
dialects** — dbt semantic models, Snowflake Cortex, and more — through one neutral
intermediate representation.

It does two things:

- **`build`** — transpile a source semantic layer into a target dialect.
- **`score`** — compute a **context fairness index**: how much more or less
  information a target layer carries versus a reference, as an auditable
  element-level diff.

```sh
semglot build --from dbt --reference ./semantic --layer cortex --out ./cortex/
semglot score --from dbt --reference ./semantic --layer cortex --target ./cortex/
```

Status: early. v1 transpiles dbt → Cortex and scores any dialect against a
reference. The `Layer` interface (`Parse` dialect→IR, `Emit` IR→dialect) is the
seam for adding dialects; because every dialect is bidirectional, many→many
transpilation is a matter of registering more layers, not writing converters.

## Testing

```sh
go test ./...          # unit + integration (integration runs the compiled CLI)
go test -short ./...    # skips the process-exec integration test
```

Unit tests live beside each package (`layer/…_test.go`). End-to-end tests in
`test/` run a realistic dbt project through the transpiler and pin the emitted
output with golden files plus targeted assertions. Fixtures follow an
input/output layout — the source-dialect dir holds the inputs, and a nested
per-target subdir holds the expected output:

```
test/models/ecommerce/dbt/          # dbt source-dialect input
  schema.yml                        # model properties (descriptions, data types, keys, relationships)
  semantic_models.yml               # semantic layer (entities, dimensions, measures)
  metrics.yml                       # metrics
  cortex/ecommerce.yaml             # expected cortex target output
```

Regenerate goldens after an intentional change with `UPDATE_GOLDEN=1 go test ./...`.

## License

MIT — see [LICENSE](./LICENSE).
