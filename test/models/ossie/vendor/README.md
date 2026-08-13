# Vendored Apache Ossie fixtures

These files are copied verbatim from [apache/ossie](https://github.com/apache/ossie)
at commit `88e0011148283302c9a04cd0287e00e0b9d87354` (2026-07-31), and are used
as parse-conformance inputs for semglot's `ossie` dialect.

| File | Upstream path |
|---|---|
| `tpcds_semantic_model.yaml` | `examples/tpcds_semantic_model.yaml` |
| `fixtureA_ossie.yaml` | `converters/databricks/tests/fixtures/fixtureA_ossie.yaml` |
| `fixtureB_ossie.yaml` | `converters/databricks/tests/fixtures/fixtureB_ossie.yaml` |
| `tpcds_ossie.yaml` | `converters/databricks/tests/fixtures/tpcds_ossie.yaml` |
| `osi-schema.json` | `core-spec/osi-schema.json` |

Apache Ossie is licensed under the Apache License 2.0; semglot is MIT. The ASF
headers in these files are retained deliberately — do not strip them, and do not
edit the files. (`osi-schema.json` carries no header: JSON has no comment
syntax, and upstream ships it without an embedded licence field. It is
nonetheless the same Apache-2.0 work as the rest of this directory.) To refresh
them, re-copy from a newer upstream commit and update the SHA above.

`osi-schema.json` is the spec's own property list, and is what
`dialect/ossie_schema_test.go` walks to prove every property OSI declares has a
yaml tag on the matching `osi*` Go struct. Refreshing it to a newer commit is
therefore how a new spec property surfaces: the test fails, naming the property
and the struct that lacks it.

## Reference converter outputs

`reference/` holds OSI documents produced by Ossie's own dbt converter,
extracted from `converters/dbt/tests/__snapshots__/test_msi_to_osi.ambr` at the
same pinned commit and de-indented from the syrupy snapshot format. They are the
expected output in `test/ossie_reference_test.go`, which compares them against
what semglot's `dbt -> ossie` produces from an equivalent dbt input.

| File | Upstream snapshot |
|---|---|
| `reference/derived_metric_nested.yaml` | `TestMetricConversion.test_derived_metric_nested` |
| `reference/ratio_metric_inlines.yaml` | `TestMetricConversion.test_ratio_metric_inlines_sub_expressions` |

The only edits made to the snapshot payloads are removing the uniform two-space
syrupy indent and prepending the ASF header that heads the `.ambr` file, so each
extracted document carries the licence notice of the work it derives from.
