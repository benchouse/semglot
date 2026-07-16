<!-- Thanks for contributing to semglot! -->

## What and why

<!-- What does this change do, and why? -->

## Checklist

- [ ] This PR is based on `main` (not stacked on another unmerged branch)
- [ ] `go build ./...`, `go test ./...`, `go vet ./...` pass
- [ ] `gofmt -l .` prints nothing
- [ ] Goldens regenerated (`UPDATE_GOLDEN=1 go test ./...`) if emitted output changed, and the diff reviewed
- [ ] Docs updated if behavior or a dialect mapping changed (`README.md`, `dialect/README.md`)
