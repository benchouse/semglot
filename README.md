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

## License

MIT — see [LICENSE](./LICENSE).
