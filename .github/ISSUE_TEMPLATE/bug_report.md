---
name: Bug report
about: Something semglot got wrong
title: ""
labels: bug
---

**What happened**

<!-- What semglot did. -->

**What you expected**

**Minimal repro**

<!-- A small `semglot.yaml` profile and the source it points at, plus the command. -->

```yaml
# semglot.yaml
profiles:
  repro:
    source: ./models
    target-dialect: cortex
    output: ./out
    database: DB
```

```sh
semglot build --profile repro
```

**Output vs. expected**

<!-- The emitted file (or error), and what it should have been. -->

**Version**

<!-- semglot commit or release tag, and `go version`. -->
