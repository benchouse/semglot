<!--
Thanks for contributing to semglot!
CI runs gofmt, go vet, go build, and go test ./... on this PR (and go test fails
on stale goldens), so you don't need to attest to those here.
-->

## What and why

<!-- What does this change do, and why? -->

## Checklist

- [ ] This PR is based on `main` (not stacked on another unmerged branch)
- [ ] If it changes emitted output, I regenerated goldens (`UPDATE_GOLDEN=1 go test ./...`) and **reviewed the diff** (don't bless output you haven't read)
- [ ] Docs updated if behavior or a dialect mapping changed (`README.md`, `dialect/README.md`)
