# Contributing to semglot

Thanks for your interest. semglot is a semantic-layer transpiler: it reads a source dialect into one neutral IR and writes it back out in a target dialect. Contributions that add dialects, fix mappings, or improve fidelity are very welcome.

## Development

Requires Go (the version in [`go.mod`](./go.mod)). No other dependencies beyond `gopkg.in/yaml.v3`.

```sh
go build ./...          # build
go test ./...           # unit + integration (integration runs the compiled CLI)
go test -short ./...    # skip the process-exec integration test
go vet ./...            # vet
gofmt -l .              # must print nothing (run `gofmt -w .` to fix)
```

End-to-end tests pin emitted output with golden files. After an intentional change to what a dialect emits, regenerate them and review the diff:

```sh
UPDATE_GOLDEN=1 go test ./...
```

CI runs `gofmt`, `go vet`, `go build`, and `go test ./...` on every pull request; all four must pass.

## Pull requests

- **Base every PR on `main`.** Do not stack a PR on another unmerged branch; if your work depends on something unmerged, wait for it to land or fold both into one PR. (Stacked PRs silently strand changes when merged out of order.)
- Keep a PR focused on one thing. Update goldens and docs in the same PR as the change that requires them.
- `main` is protected: changes land through PRs, and CI must be green.

## Adding a dialect

A dialect is a `Parser` (dialect files to IR), an `Emitter` (IR to dialect files), or both, implementing the interfaces in [`dialect/dialect.go`](./dialect/dialect.go) and registering itself via `init()`.

1. Read [`dialect/README.md`](./dialect/README.md): the field-level map of how every IR concept lands in each dialect. It tells you the construct to touch (where synonyms go, where a primary key goes, and so on).
2. Read the IR contract in [`ir/model.go`](./ir/model.go) for what each field means. If a concept is missing from the IR, add it there first.
3. Implement `Emit` (and, if it is a source, `Parse`). Reuse the shared helpers (`renderSQL`, `enumValues`, `enumClause`/`appendClause`, `upperAll`, `metricResolver`) rather than duplicating them.
4. Add golden fixtures under `dialect/testdata/` and `test/models/`, and update the mapping table in `dialect/README.md` and the support table in the README.

Anything a target cannot express should be reported (a note, a `NOTES.md` sidecar), never silently dropped.

## License

By contributing you agree that your contributions are licensed under the project's [MIT License](./LICENSE).
