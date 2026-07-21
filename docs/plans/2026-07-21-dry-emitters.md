# DRY Dialect Emitters Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the duplicated filesystem tail and identity plumbing from all seven dialect emitters, and collapse each dialect's SQL rendering into a single function, with byte-identical emitted output.

**Architecture:** Two independent parts. Part 1 changes `Emitter.Emit` to take `Options` and return `[]File` instead of writing to disk, with one shared `WriteFiles` performing the IO, and deletes the `Configurable` interface. It lands incrementally: each emitter first grows a private `emit(m, Options) ([]File, []string, error)` that the old `Emit` delegates to, so every commit compiles and passes; a final task flips the public interface and deletes the shims. Part 2 introduces an unexported `sqlRenderer` function type and consolidates the three dialects that render SQL from multiple functions down to one each.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3`, standard library `testing`. No new dependencies.

## Global Constraints

- **Emitted output must not change.** The golden fixtures under `test/models/ecommerce/dbt/*` are never regenerated during this work. If `go test ./...` reports a fixture diff, the refactor is wrong; fix the code, not the fixture.
- **File permissions stay exactly as today:** directories `0o755`, files `0o644`.
- **File write order stays as today**, because `WriteFiles` writes in slice order.
- Run the full suite with `go test ./...` from the repo root. Baseline is green on all four packages.
- Emitters must remain stateless after Part 1. `Register` stores one shared instance per dialect, so no emitter may keep per-run fields.
- No em-dashes in prose, comments or documentation added by this plan.
- Go doc comments on every new exported identifier, and on unexported ones whose purpose is not obvious from the name.

---

## File Structure

**Created:**
- `dialect/emit_files.go` - the `File` type and `WriteFiles`, the single place emitted bytes reach the filesystem.
- `dialect/emit_files_test.go` - unit tests for `WriteFiles`.
- `dialect/sql_render_test.go` - the shared renderer corpus test and the `sqlRenderers` registry check.

**Modified:**
- `dialect/dialect.go` - `Emitter` signature, deletion of `Configurable`.
- `dialect/render_sql.go` - `renderSQL` becomes `renderANSI` conforming to `sqlRenderer`; `renderOperand`/`isCompound` become closures inside it.
- `dialect/cortex.go`, `dialect/nao_yaml.go`, `dialect/nao_context_rules.go`, `dialect/snowflake_semantic_view.go`, `dialect/dbt_emit.go`, `dialect/supersimple.go`, `dialect/databricks_metric_view.go` - emit signature, removal of identity fields and `WithOptions`, renderer consolidation.
- `cmd/semglot/main.go:62-82` - call `Emit(model, opts)` then `WriteFiles`.
- `test/integration_test.go`, `test/context_layer_test.go`, `dialect/*_test.go` - call-site updates.
- `dialect/README.md` - documents the interface, must match.

**Unchanged and out of scope:** `dialect/dbt.go` (the parser), `dialect/sqllex.go`, `dialect/enum.go`, `ir/`.

---

## Part 1: Emit returns files

### Task 1: The File type and WriteFiles

**Files:**
- Create: `dialect/emit_files.go`
- Test: `dialect/emit_files_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `dialect.File{Name string; Data []byte}` and `dialect.WriteFiles(dir string, files []File) error`. Every later task uses both.

- [ ] **Step 1: Write the failing test**

Create `dialect/emit_files_test.go`:

```go
package dialect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFilesCreatesDirAndFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	files := []File{
		{Name: "a.yaml", Data: []byte("first")},
		{Name: "b.md", Data: []byte("second")},
	}
	if err := WriteFiles(dir, files); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	for _, f := range files {
		got, err := os.ReadFile(filepath.Join(dir, f.Name))
		if err != nil {
			t.Fatalf("ReadFile %s: %v", f.Name, err)
		}
		if string(got) != string(f.Data) {
			t.Errorf("%s = %q, want %q", f.Name, got, f.Data)
		}
	}
}

func TestWriteFilesPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	if err := WriteFiles(dir, []File{{Name: "a.yaml", Data: []byte("x")}}); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o755 {
		t.Errorf("dir mode = %o, want 755", got)
	}
	fi, err := os.Stat(filepath.Join(dir, "a.yaml"))
	if err != nil {
		t.Fatalf("Stat file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("file mode = %o, want 644", got)
	}
}

// An empty file list must still create the output directory, so a target that
// legitimately emits nothing leaves a usable, empty output dir rather than no
// dir at all (today every emitter calls MkdirAll before deciding what to write).
func TestWriteFilesEmptyStillCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	if err := WriteFiles(dir, nil); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("dir not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestWriteFiles -v`
Expected: FAIL to build with `undefined: File` and `undefined: WriteFiles`.

- [ ] **Step 3: Write minimal implementation**

Create `dialect/emit_files.go`:

```go
package dialect

import (
	"os"
	"path/filepath"
)

// File is one artifact produced by an Emitter, named relative to the output
// directory. Emitters return these instead of writing them, so emitting stays
// a pure function of (model, options) and the filesystem is touched in exactly
// one place.
type File struct {
	Name string // base name within the output dir, e.g. "semantic_model.yaml"
	Data []byte
}

// WriteFiles creates dir and writes files into it, in slice order. It is the
// only place in the dialect package that writes emitted output.
func WriteFiles(dir string, files []File) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./dialect/ -run TestWriteFiles -v`
Expected: PASS, three tests.

- [ ] **Step 5: Commit**

```bash
git add dialect/emit_files.go dialect/emit_files_test.go
git commit -m "feat(dialect): add File and WriteFiles, the single emit write path"
```

---

### Task 2: Cortex emits files

Cortex writes one file and is the simplest full example. The shape established here repeats in Tasks 3 to 7.

**Files:**
- Modify: `dialect/cortex.go:111-210`
- Test: `dialect/cortex_test.go`

**Interfaces:**
- Consumes: `File`, `WriteFiles` from Task 1.
- Produces: `func (c cortex) emit(m *ir.Model, o Options) ([]File, []string, error)`. Task 8 promotes this to the public `Emit`.

- [ ] **Step 1: Write the failing test**

Append to `dialect/cortex_test.go`:

```go
// emit returns the model YAML as bytes without touching the filesystem.
func TestCortexEmitReturnsFile(t *testing.T) {
	files, _, err := cortex{}.emit(sampleIR(), Options{Database: "DB", Schema: "MAIN", Name: "m"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1", len(files))
	}
	if files[0].Name != "semantic_model.yaml" {
		t.Errorf("Name = %q, want semantic_model.yaml", files[0].Name)
	}
	if len(files[0].Data) == 0 {
		t.Error("Data is empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestCortexEmitReturnsFile -v`
Expected: FAIL to build with `cortex.emit undefined`.

- [ ] **Step 3: Write minimal implementation**

In `dialect/cortex.go`, rename the existing method to `emit`, take `Options` instead of `dir`, read identity from `o` rather than from the receiver, and replace the tail. The method body is unchanged apart from these edits.

Header change:

```go
func (c cortex) emit(m *ir.Model, o Options) ([]File, []string, error) {
	name := o.Name
	if name == "" {
		name = "semantic_model"
	}
	schema := o.Schema
	if schema == "" {
		schema = "MAIN"
	}

	cm := cortexModel{Name: name, Description: o.Description}
```

Inside the table loop, the one other receiver read becomes `o.Database`:

```go
		BaseTable:   cortexBaseTable{Database: o.Database, Schema: schema, Table: strings.ToUpper(t.Name)},
```

Tail change, replacing the `os.MkdirAll` and `os.WriteFile` block:

```go
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cm); err != nil {
		return nil, warnings, err
	}
	if err := enc.Close(); err != nil {
		return nil, warnings, err
	}
	return []File{{Name: "semantic_model.yaml", Data: buf.Bytes()}}, warnings, nil
}

// Emit is the transitional shim: it writes what emit produces. Task 8 removes
// it in favour of emit becoming the interface method.
func (c cortex) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := c.emit(m, Options{
		Database: c.Database, Schema: c.Schema, Name: c.ModelName, Description: c.Description,
	})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

Remove the now-unused `os` and `path/filepath` imports from `dialect/cortex.go` if nothing else in the file uses them.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages. The golden fixture comparison in `test/integration_test.go` passing is the proof that output did not change.

- [ ] **Step 5: Commit**

```bash
git add dialect/cortex.go dialect/cortex_test.go
git commit -m "refactor(dialect): cortex emits []File, Emit becomes a write shim"
```

---

### Task 3: nao-yaml and nao-context-rules emit files

Both are single-file emitters with trivial tails, so they land together.

**Files:**
- Modify: `dialect/nao_yaml.go:69-147`, `dialect/nao_context_rules.go:19-106`
- Test: `dialect/nao_yaml_test.go`, `dialect/nao_context_rules_test.go`

**Interfaces:**
- Consumes: `File`, `WriteFiles`.
- Produces: `func (n naoYaml) emit(m *ir.Model, o Options) ([]File, []string, error)` and `func (naoContextRules) emit(m *ir.Model, o Options) ([]File, []string, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `dialect/nao_yaml_test.go`:

```go
func TestNaoYamlEmitReturnsFile(t *testing.T) {
	files, _, err := naoYaml{}.emit(sampleIR(), Options{})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) != 1 || files[0].Name != "semantic.yaml" {
		t.Fatalf("files = %+v, want one semantic.yaml", files)
	}
}
```

Append to `dialect/nao_context_rules_test.go`:

```go
func TestNaoContextRulesEmitReturnsFile(t *testing.T) {
	files, _, err := naoContextRules{}.emit(sampleIR(), Options{})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) != 1 || files[0].Name != "RULES.md" {
		t.Fatalf("files = %+v, want one RULES.md", files)
	}
}
```

If `sampleIR()` is not visible from these test files, use the model builder those files already use for their existing tests instead; both are in package `dialect`, so any helper in the package is available.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run 'TestNaoYamlEmitReturnsFile|TestNaoContextRulesEmitReturnsFile' -v`
Expected: FAIL to build with `naoYaml.emit undefined` and `naoContextRules.emit undefined`.

- [ ] **Step 3: Write minimal implementation**

In `dialect/nao_yaml.go`, rename `Emit` to `emit`, change the signature to `(m *ir.Model, o Options) ([]File, []string, error)`, replace receiver identity reads (`n.Database`, `n.Schema`, `n.ModelName`, `n.Description`) with the matching `o.` fields, change the two mid-function error returns to `return nil, own, err`, and replace the tail:

```go
	return []File{{Name: "semantic.yaml", Data: buf.Bytes()}}, own, nil
}

// Emit is the transitional shim removed in Task 8.
func (n naoYaml) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := n.emit(m, Options{
		Database: n.Database, Schema: n.Schema, Name: n.ModelName, Description: n.Description,
	})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

In `dialect/nao_context_rules.go`, do the same. It has no identity fields, so `o` is unused in the body; keep the parameter named `_ Options` to say so:

```go
func (naoContextRules) emit(m *ir.Model, _ Options) ([]File, []string, error) {
	// ... body unchanged ...
	return []File{{Name: "RULES.md", Data: b.Bytes()}}, nil, nil
}

// Emit is the transitional shim removed in Task 8.
func (n naoContextRules) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := n.emit(m, Options{})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

Drop the `os` and `path/filepath` imports from both files if unused.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages.

- [ ] **Step 5: Commit**

```bash
git add dialect/nao_yaml.go dialect/nao_yaml_test.go dialect/nao_context_rules.go dialect/nao_context_rules_test.go
git commit -m "refactor(dialect): nao emitters emit []File"
```

---

### Task 4: snowflake-semantic-view emits files

**Files:**
- Modify: `dialect/snowflake_semantic_view.go:36-182`
- Test: `dialect/snowflake_semantic_view_test.go`

**Interfaces:**
- Consumes: `File`, `WriteFiles`.
- Produces: `func (s snowflakeSemanticView) emit(m *ir.Model, o Options) ([]File, []string, error)`.

This is the first emitter reading `ViewSchema`, which falls back to `Schema` when empty. That fallback logic stays exactly where it is, reading `o.ViewSchema` and `o.Schema`.

- [ ] **Step 1: Write the failing test**

Append to `dialect/snowflake_semantic_view_test.go`:

```go
func TestSnowflakeSVEmitReturnsFile(t *testing.T) {
	files, _, err := snowflakeSemanticView{}.emit(sampleIR(), Options{
		Database: "DB", Schema: "MAIN", ViewSchema: "SEM", Name: "ecommerce",
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) != 1 || files[0].Name != "definition.md" {
		t.Fatalf("files = %+v, want one definition.md", files)
	}
	if !bytes.Contains(files[0].Data, []byte("DB.SEM.ECOMMERCE")) {
		t.Errorf("ViewSchema not applied; got:\n%s", files[0].Data)
	}
}
```

Add `"bytes"` to the test file's imports. If the qualified name in this codebase renders differently, run the existing `TestSnowflakeSemanticView*` tests first and copy the exact expected substring from their assertions rather than guessing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestSnowflakeSVEmitReturnsFile -v`
Expected: FAIL to build with `snowflakeSemanticView.emit undefined`.

- [ ] **Step 3: Write minimal implementation**

Rename `Emit` to `emit` with the new signature, replace `s.Database`, `s.Schema`, `s.ViewSchema`, `s.ModelName`, `s.Description` with the `o.` equivalents, change interior error returns to `return nil, own, err`, and replace the tail:

```go
	b.WriteString(";\n```\n")

	return []File{{Name: "definition.md", Data: b.Bytes()}}, own, nil
}

// Emit is the transitional shim removed in Task 8.
func (s snowflakeSemanticView) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := s.emit(m, Options{
		Database: s.Database, Schema: s.Schema, ViewSchema: s.ViewSchema,
		Name: s.ModelName, Description: s.Description,
	})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages.

- [ ] **Step 5: Commit**

```bash
git add dialect/snowflake_semantic_view.go dialect/snowflake_semantic_view_test.go
git commit -m "refactor(dialect): snowflake-semantic-view emits []File"
```

---

### Task 5: dbt emits files

**Files:**
- Modify: `dialect/dbt_emit.go:134-158`
- Test: `dialect/dbt_test.go`

**Interfaces:**
- Consumes: `File`, `WriteFiles`.
- Produces: `func (dbt) emit(m *ir.Model, o Options) ([]File, []string, error)`.

dbt has no identity fields and returns nil warnings, so this is the smallest change of the seven.

- [ ] **Step 1: Write the failing test**

Append to `dialect/dbt_test.go`:

```go
func TestDbtEmitReturnsFile(t *testing.T) {
	files, warnings, err := dbt{}.emit(sampleIR(), Options{})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) != 1 || files[0].Name != "ecommerce.yml" {
		t.Fatalf("files = %+v, want one ecommerce.yml", files)
	}
	if warnings != nil {
		t.Errorf("warnings = %v, want nil (dbt never degrades)", warnings)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestDbtEmitReturnsFile -v`
Expected: FAIL to build with `dbt.emit undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
// emit renders the IR as a single dbt YAML file, ecommerce.yml. dbt generates
// no degrade notes of its own; it always returns nil warnings.
func (dbt) emit(m *ir.Model, _ Options) ([]File, []string, error) {
	var f dbtEmitFile
	for _, t := range m.Tables {
		pk := stringSet(t.PrimaryKey)
		fk := fkColumns(m, t.Name)

		f.Models = append(f.Models, emitModel(m, t, pk, fk))
		f.SemanticModels = append(f.SemanticModels, emitSemantic(t, pk, fk))
		f.Metrics = append(f.Metrics, emitMetrics(t)...)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, nil, err
	}
	return []File{{Name: "ecommerce.yml", Data: buf.Bytes()}}, nil, nil
}

// Emit is the transitional shim removed in Task 8.
func (d dbt) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := d.emit(m, Options{})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages.

- [ ] **Step 5: Commit**

```bash
git add dialect/dbt_emit.go dialect/dbt_test.go
git commit -m "refactor(dialect): dbt emits []File"
```

---

### Task 6: supersimple emits files

Supersimple is the first multi-file emitter: one YAML per table in `order`, then a conditional `NOTES.md`.

**Files:**
- Modify: `dialect/supersimple.go:125-338`
- Test: `dialect/supersimple_test.go`

**Interfaces:**
- Consumes: `File`, `WriteFiles`.
- Produces: `func (s supersimple) emit(m *ir.Model, o Options) ([]File, []string, error)`.

- [ ] **Step 1: Write the failing test**

Append to `dialect/supersimple_test.go`:

```go
// File order must stay per-table-in-order followed by NOTES.md, because
// WriteFiles writes in slice order and the golden fixtures depend on the
// resulting directory contents.
func TestSupersimpleEmitReturnsFilesInOrder(t *testing.T) {
	m := sampleIR()
	m.Notes = []string{"something was not transpiled"}
	files, _, err := supersimple{}.emit(m, Options{Schema: "MAIN"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) < 2 {
		t.Fatalf("len(files) = %d, want at least one model plus NOTES.md", len(files))
	}
	last := files[len(files)-1]
	if last.Name != "NOTES.md" {
		t.Errorf("last file = %q, want NOTES.md", last.Name)
	}
	for _, f := range files[:len(files)-1] {
		if !strings.HasSuffix(f.Name, ".yaml") {
			t.Errorf("model file %q does not end in .yaml", f.Name)
		}
	}
}

// With no notes at all, NOTES.md must be absent rather than empty.
func TestSupersimpleEmitOmitsEmptyNotes(t *testing.T) {
	m := sampleIR()
	m.Notes = nil
	files, _, err := supersimple{}.emit(m, Options{Schema: "MAIN"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, f := range files {
		if f.Name == "NOTES.md" {
			t.Fatal("NOTES.md emitted with no notes")
		}
	}
}
```

Add `"strings"` to the test imports if absent. If `sampleIR()` produces degrade notes of its own, the second test will fail legitimately; in that case build a minimal model with one table and no degradable metrics instead, copying the builder style already used in `dialect/supersimple_test.go`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run 'TestSupersimpleEmit' -v`
Expected: FAIL to build with `supersimple.emit undefined`.

- [ ] **Step 3: Write minimal implementation**

Rename to `emit` with the new signature, read `o.Schema` in place of `s.Schema`, delete the `os.MkdirAll` block near the top, and replace the phase 3 write loop and the NOTES.md block with appends to a `files` slice:

```go
func (s supersimple) emit(m *ir.Model, o Options) ([]File, []string, error) {
	schema := o.Schema
	if schema == "" {
		schema = "MAIN"
	}
	// ... phases 1 and 2 unchanged, with the os.MkdirAll block deleted ...

	// Phase 3: per-table files (in table order), then NOTES.md.
	var files []File
	for _, name := range order {
		st := states[name]
		file := ssFile{Models: map[string]ssModel{st.id: st.model}}
		if len(st.metrics) > 0 {
			file.Metrics = st.metrics
		}
		var buf bytes.Buffer
		buf.WriteString(ssHeader)
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(file); err != nil {
			return nil, degradeNotes, err
		}
		if err := enc.Close(); err != nil {
			return nil, degradeNotes, err
		}
		files = append(files, File{Name: st.id + ".yaml", Data: buf.Bytes()})
	}
	allNotes := append(slices.Clone(m.Notes), degradeNotes...)
	if len(allNotes) > 0 {
		var sb strings.Builder
		sb.WriteString("# Not transpiled to supersimple\n\n")
		for _, n := range allNotes {
			sb.WriteString("- " + n + "\n")
		}
		files = append(files, File{Name: "NOTES.md", Data: []byte(sb.String())})
	}
	return files, degradeNotes, nil
}

// Emit is the transitional shim removed in Task 8.
func (s supersimple) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := s.emit(m, Options{Schema: s.Schema})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

Note the behaviour this preserves: today an emit with zero tables still creates the output directory because `MkdirAll` runs before the loop. `WriteFiles` also creates the directory before writing, including for an empty slice, which Task 1's third test pins.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages.

- [ ] **Step 5: Commit**

```bash
git add dialect/supersimple.go dialect/supersimple_test.go
git commit -m "refactor(dialect): supersimple emits []File"
```

---

### Task 7: databricks-metric-view emits files

The second multi-file emitter, and the one that skips tables mid-loop.

**Files:**
- Modify: `dialect/databricks_metric_view.go:110-160`
- Test: `dialect/databricks_metric_view_test.go`

**Interfaces:**
- Consumes: `File`, `WriteFiles`.
- Produces: `func (d databricksMetricView) emit(m *ir.Model, o Options) ([]File, []string, error)`.

- [ ] **Step 1: Write the failing test**

Append to `dialect/databricks_metric_view_test.go`:

```go
func TestDatabricksEmitReturnsOneFilePerTable(t *testing.T) {
	m := sampleIR()
	files, _, err := databricksMetricView{}.emit(m, Options{Database: "ANALYTICS", Schema: "MAIN"})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no files emitted")
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name, ".yaml") {
			t.Errorf("file %q does not end in .yaml", f.Name)
		}
		if strings.ToLower(f.Name) != f.Name {
			t.Errorf("file %q is not lowercased", f.Name)
		}
	}
}
```

Add `"strings"` to the test imports if absent.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestDatabricksEmitReturnsOneFilePerTable -v`
Expected: FAIL to build with `databricksMetricView.emit undefined`.

- [ ] **Step 3: Write minimal implementation**

Rename to `emit` with the new signature, read `o.Database` and `o.Schema` at the top, delete the `os.MkdirAll` block, and accumulate files. The zero-fields skip and its warning stay exactly as they are, including the `continue`, which is what makes the emitted file count differ from the table count.

```go
func (d databricksMetricView) emit(m *ir.Model, o Options) ([]File, []string, error) {
	catalog := strings.ToLower(o.Database)
	schema := strings.ToLower(o.Schema)
	if schema == "" {
		schema = "main"
	}
	// ... resolve, metricOwner, tableByName unchanged; MkdirAll block deleted ...

	var files []File
	var warnings []string
	for _, t := range m.Tables {
		mv, own := d.buildView(m, t, resolve, metricOwner, tableByName, catalog, schema)
		if len(mv.Fields) == 0 {
			warnings = append(warnings, fmt.Sprintf(
				"table %q skipped: all its dimensions collided with measure names, leaving none for a metric view (which requires at least one dimension)", t.Name))
			continue
		}
		warnings = append(warnings, own...)
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(mv); err != nil {
			return nil, warnings, err
		}
		if err := enc.Close(); err != nil {
			return nil, warnings, err
		}
		files = append(files, File{Name: strings.ToLower(t.Name) + ".yaml", Data: buf.Bytes()})
	}
	return files, warnings, nil
}

// Emit is the transitional shim removed in Task 8.
func (d databricksMetricView) Emit(m *ir.Model, dir string) ([]string, error) {
	files, warnings, err := d.emit(m, Options{
		Database: d.Database, Schema: d.Schema, Name: d.ModelName, Description: d.Description,
	})
	if err != nil {
		return warnings, err
	}
	return warnings, WriteFiles(dir, files)
}
```

`d.ModelName` and `d.Description` are currently read by `buildView` via the receiver. Leave those reads on the receiver for now; Task 8 threads `o` through `buildView` when the fields are deleted. If `buildView` reads no identity fields, skip that note.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages. `TestDatabricksZeroFieldsSkip` (pinned in `633286f`) must still pass, proving the skip survived.

- [ ] **Step 5: Commit**

```bash
git add dialect/databricks_metric_view.go dialect/databricks_metric_view_test.go
git commit -m "refactor(dialect): databricks-metric-view emits []File"
```

---

### Task 8: Flip the interface and delete Configurable

All seven emitters now have `emit`. This task makes `emit` the interface method, deletes the shims, the identity fields, `WithOptions` and `Configurable`, and updates every call site. It is one atomic commit because Go will not compile a partial version.

**Files:**
- Modify: `dialect/dialect.go:34-46`, all seven emitter files, `cmd/semglot/main.go:62-82`, `test/integration_test.go:85-501`, `test/context_layer_test.go:15-31`, `dialect/README.md`
- Test: existing suite plus the assertion below

**Interfaces:**
- Consumes: every `emit` from Tasks 2 to 7.
- Produces: `Emit(m *ir.Model, o Options) (files []File, warnings []string, err error)` on the `Emitter` interface. `Configurable`, `WithOptions` and all emitter identity fields no longer exist.

- [ ] **Step 1: Write the failing test**

Append to `dialect/registry_test.go`:

```go
// Every registered emitter must be usable straight from the registry with no
// configuration step, which is what deleting Configurable buys.
func TestEmittersAreStatelessAndTakeOptions(t *testing.T) {
	for _, name := range Names() {
		e, err := AsEmitter(name)
		if err != nil {
			continue // parse-only dialects are not emitters
		}
		files, _, err := e.Emit(sampleIR(), Options{Database: "DB", Schema: "MAIN", Name: "m"})
		if err != nil {
			t.Errorf("%s: Emit: %v", name, err)
			continue
		}
		if len(files) == 0 {
			t.Errorf("%s: emitted no files", name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestEmittersAreStatelessAndTakeOptions -v`
Expected: FAIL to build, because `Emit` still takes a `string`.

- [ ] **Step 3: Write the implementation**

3a. In `dialect/dialect.go`, replace the `Emitter` interface and delete `Configurable`:

```go
// Emitter renders the neutral IR as a dialect's files. It is stateless: the
// registry stores one shared instance per dialect and Options carries all
// per-run identity. Emitting does no IO; the caller passes the returned files
// to WriteFiles. warnings are non-fatal: source constructs the target could
// not represent and had to degrade or drop. They are returned rather than
// appended to ir.Model.Notes so Emit stays read-only over the model.
type Emitter interface {
	Dialect
	Emit(m *ir.Model, o Options) (files []File, warnings []string, err error)
}
```

Delete the `Configurable` interface entirely. Keep `Options` unchanged.

3b. In each of the seven emitter files: rename `emit` to `Emit`, delete the transitional `Emit` shim, and delete the identity fields from the struct so each becomes `type cortex struct{}`, `type naoYaml struct{}`, `type snowflakeSemanticView struct{}`, `type supersimple struct{}`, `type databricksMetricView struct{}`. `dbt` and `naoContextRules` already have no fields. Delete every `WithOptions` method.

For `databricksMetricView`, `buildView` loses whatever identity it read from the receiver; add the values it needs as parameters. If it reads `d.ModelName` and `d.Description`, its signature gains `modelName, description string` and the single call site passes `o.Name, o.Description`.

3c. In `cmd/semglot/main.go`, replace lines 62 to 82:

```go
	emitter, err := dialect.AsEmitter(spec.TargetDialect)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}

	model, err := parser.Parse(spec.Sources...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	files, warnings, err := emitter.Emit(model, dialect.Options{
		Database:    spec.Database,
		Schema:      spec.Schema,
		ViewSchema:  spec.ViewSchema,
		Name:        spec.ModelName,
		Description: spec.Description,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if err := dialect.WriteFiles(spec.Output, files); err != nil {
		fmt.Fprintln(os.Stderr, "build: write:", err)
		return 1
	}
```

The `model.Notes` and `warnings` printing blocks below are unchanged.

3d. In `test/integration_test.go` and `test/context_layer_test.go`, delete each `if c, ok := e.(dialect.Configurable); ok { e = c.WithOptions(...) }` block and move the `Options` into the `Emit` call, then write with `dialect.WriteFiles`. For example at `test/integration_test.go:152-164`:

```go
	e, err := dialect.AsEmitter("cortex")
	if err != nil {
		t.Fatalf("AsEmitter: %v", err)
	}
	files, _, err := e.Emit(m, dialect.Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce"})
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if err := dialect.WriteFiles(out, files); err != nil {
		t.Fatalf("WriteFiles: %v", err)
	}
```

Apply the same shape at `test/integration_test.go:85`, `:111`, `:413`, `:485` and `test/context_layer_test.go:15`. These tests compare the written directory against the golden fixtures, so they keep writing to disk.

3e. In `dialect/*_test.go`, update the remaining direct constructions. Calls like `(cortex{Database: "DB", Schema: "MAIN", ModelName: "m"}).Emit(m, dir)` become `cortex{}.Emit(m, Options{Database: "DB", Schema: "MAIN", Name: "m"})`, and assertions that read a file back from `dir` index `files` instead. The affected sites are `dialect/cortex_test.go:65,141,169,188`, `dialect/newkinds_test.go:31,99,156,181,241,267`, `dialect/nao_yaml_test.go:16,41`, `dialect/nao_context_rules_test.go:15`, `dialect/snowflake_semantic_view_test.go:20,35`, `dialect/databricks_metric_view_test.go:153`, plus the `emit` tests added in Tasks 2 to 7, which become `Emit` calls.

3f. Update `dialect/README.md` so its description of the emitter surface matches: `Emit` takes `Options` and returns files, `Configurable` and `WithOptions` no longer exist, and `WriteFiles` is the write path.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./...`
Expected: PASS on all four packages, with the golden fixtures unchanged.

Then confirm the deletions actually happened:

Run: `grep -rn 'Configurable\|WithOptions' --include='*.go' --include='*.md' .`
Expected: no output.

Run: `grep -rn 'os.MkdirAll\|os.WriteFile' dialect/*.go | grep -v _test`
Expected: only the two lines inside `dialect/emit_files.go`.

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(dialect): Emit takes Options and returns []File

Deletes Configurable, five WithOptions methods and the identity fields
they copied into, plus seven MkdirAll and eight WriteFile sites. The
filesystem is now touched only by WriteFiles, so emitters are pure
functions of (model, options) and the registry's one-shared-instance
assumption holds without copying."
```

---

## Part 2: One SQL renderer per dialect

### Task 9: The sqlRenderer type and renderANSI

**Files:**
- Modify: `dialect/render_sql.go`
- Modify: `dialect/cortex.go`, `dialect/nao_yaml.go`, `dialect/nao_context_rules.go`, `dialect/databricks_metric_view.go` (call sites)
- Test: `dialect/sql_render_test.go` (create)

**Interfaces:**
- Consumes: nothing from Part 1.
- Produces: `sqlRenderer`, `sqlCtx`, `sqlResult` and `renderANSI`, all used by Tasks 10 to 13.

- [ ] **Step 1: Write the failing test**

Create `dialect/sql_render_test.go`:

```go
package dialect

import (
	"testing"

	"github.com/benchouse/semglot/ir"
)

// renderANSI must satisfy sqlRenderer, so it is interchangeable with the
// per-dialect renderers in the corpus test added in Task 13.
var _ sqlRenderer = renderANSI

func TestRenderANSIParenthesizesCompoundOperands(t *testing.T) {
	// (a + b) * c must keep its grouping when printed.
	def := ir.Binary{
		Op:    "*",
		Left:  ir.Binary{Op: "+", Left: ir.Col{Name: "a"}, Right: ir.Col{Name: "b"}},
		Right: ir.Col{Name: "c"},
	}
	got, err := renderANSI(def, sqlCtx{})
	if err != nil {
		t.Fatalf("renderANSI: %v", err)
	}
	if got.SQL != "(a + b) * c" {
		t.Errorf("SQL = %q, want %q", got.SQL, "(a + b) * c")
	}
}

// A metric Ref is inlined, and stays parenthesized if what it resolves to is
// itself compound. This is the behaviour cortex depends on.
func TestRenderANSIInlinesCompoundRef(t *testing.T) {
	resolve := func(name string) (ir.Expr, bool) {
		if name == "ratio" {
			return ir.Binary{Op: "/", Left: ir.Col{Name: "x"}, Right: ir.Col{Name: "y"}}, true
		}
		return nil, false
	}
	def := ir.Binary{Op: "*", Left: ir.Ref{Metric: "ratio"}, Right: ir.Lit{Value: "100"}}
	got, err := renderANSI(def, sqlCtx{Resolve: resolve})
	if err != nil {
		t.Fatalf("renderANSI: %v", err)
	}
	if got.SQL != "(x / y) * 100" {
		t.Errorf("SQL = %q, want %q", got.SQL, "(x / y) * 100")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./dialect/ -run TestRenderANSI -v`
Expected: FAIL to build with `undefined: sqlRenderer`, `undefined: sqlCtx`, `undefined: renderANSI`.

- [ ] **Step 3: Write the implementation**

In `dialect/render_sql.go`, add the shared types above the renderer:

```go
// sqlRenderer turns one metric definition into target-specific SQL. Every
// dialect that renders metric SQL has exactly one, so there is a single place
// per target to read to know what SQL it produces. An error is a degrade
// reason, not a failure: the caller turns it into a warnings entry and drops
// the metric rather than emitting SQL it cannot stand behind.
type sqlRenderer func(def ir.Expr, c sqlCtx) (sqlResult, error)

// sqlCtx is everything a renderer may need about the surrounding model. Not
// every renderer reads every field.
type sqlCtx struct {
	Resolve func(name string) (ir.Expr, bool) // metric lookup; nil means do not inline refs
	Table   string                            // owning table, for column qualification
	TableOf map[string]string                 // metric name -> owning table, for qualified refs
	Filter  bool                              // rendering a measure filter fragment, not a metric def; read only by renderDBT
}

// sqlResult is a rendered expression plus anything the caller needs from the
// same pass.
type sqlResult struct {
	SQL  string
	Refs []string // metric names left as bare references; consumed only by the dbt emitter
}
```

Rename `renderSQL` to `renderANSI`, give it the `sqlRenderer` signature, and fold `renderOperand` and `isCompound` in as closures. Delete the two now-obsolete top-level functions and the long "do not dedupe" comment on `renderOperand`, replacing it with a short policy note on `renderANSI` itself:

```go
// renderANSI lowers a metric-definition AST to neutral, lowercase SQL. Ref
// policy: a referenced metric is INLINED, so an operand that resolves to a
// compound expression is parenthesized. Used as-is by cortex (uppercased at
// the call site), nao-yaml and nao-context-rules. The dbt and snowflake
// renderers deliberately do not share this walk because their ref policies
// differ; see renderDBT and renderSnowflakeSV.
func renderANSI(def ir.Expr, c sqlCtx) (sqlResult, error) {
	var isCompound func(ir.Expr) bool
	isCompound = func(e ir.Expr) bool {
		switch n := e.(type) {
		case ir.Binary:
			return true
		case ir.Ref:
			if c.Resolve != nil {
				if d, ok := c.Resolve(n.Metric); ok {
					return isCompound(d)
				}
			}
		}
		return false
	}
	var walk func(ir.Expr) string
	operand := func(e ir.Expr) string {
		s := walk(e)
		if isCompound(e) {
			return "(" + s + ")"
		}
		return s
	}
	walk = func(e ir.Expr) string {
		switch n := e.(type) {
		case ir.Col:
			if n.Table != "" {
				return n.Table + "." + n.Name
			}
			return n.Name
		case ir.Raw:
			return n.SQL // unqualified; the enclosing Agg case qualifies it
		case ir.Lit:
			return n.Value
		case ir.Ref:
			if c.Resolve != nil {
				if def, ok := c.Resolve(n.Metric); ok {
					return walk(def)
				}
			}
			return n.Metric
		case ir.Agg:
			var arg string
			switch a := n.Arg.(type) {
			case ir.Raw: // qualify the raw fragment's columns with the owning table
				arg = qualifyExpr(n.Table, colSet(a.Columns), a.SQL)
			case nil:
				arg = ""
			default:
				arg = walk(n.Arg)
			}
			if n.Filter != nil {
				var cond string
				// A Raw filter carries unqualified column refs; qualify them with
				// the owning table exactly like a Raw agg arg. Any other Expr
				// (Col/Binary) already renders qualified.
				if raw, ok := n.Filter.(ir.Raw); ok {
					cond = qualifyExpr(n.Table, colSet(raw.Columns), raw.SQL)
				} else {
					cond = walk(n.Filter)
				}
				arg = "case when " + cond + " then " + arg + " end"
			}
			return aggExpr(n.Func, arg)
		case ir.Binary:
			return operand(n.Left) + " " + n.Op + " " + operand(n.Right)
		case ir.Window:
			// Unreachable for shipped emitters: cortexDegrade/ssDegradeReason omit
			// Window metrics before any renderer sees them (no validated Cortex or
			// supersimple primitive, provisional). Kept only for completeness.
			return walk(n.Base) // best-effort
		case ir.Conversion:
			// Unreachable for shipped emitters, as above: no Cortex primitive.
			return "" // no SQL rendering; degraded by callers
		default:
			return ""
		}
	}
	return sqlResult{SQL: walk(def)}, nil
}
```

Two details that make this compile and behave: `operand` is declared before `walk` but calls it, which works because `walk` is a declared `var` assigned afterwards. The `ir.Ref` case now guards `c.Resolve != nil` and falls through to the bare metric name when there is no resolver, which is what today's callers get by passing a resolver that always returns false.

`renderANSI` returns a nil error today for every node kind, including `ir.Conversion`, which still returns the empty string. Callers already pre-filter Conversion and Window metrics through `cortexDegrade` and `ssDegradeReason`, so this preserves current behaviour exactly. Do not add an error return for those kinds in this task.

Update the four call sites to the new signature. Each currently ignores errors because there are none yet; be explicit rather than using `_`:

`dialect/cortex.go:160`:

```go
			rendered, err := renderANSI(mt.Def, sqlCtx{Resolve: resolve})
			if err != nil {
				degradeNotes = append(degradeNotes, fmt.Sprintf("metric %q not emitted to Cortex: %v", mt.Name, err))
				continue
			}
			ct.Metrics = append(ct.Metrics, cortexMetric{
				Name: mt.Name, Expr: strings.ToUpper(rendered.SQL),
				Description: mt.Description, Synonyms: mt.Synonyms,
			})
```

`dialect/nao_yaml.go:126`, `dialect/nao_context_rules.go:30` and `dialect/databricks_metric_view.go:252` take the same shape: call `renderANSI(def, sqlCtx{Resolve: resolve})`, handle the error as a warning where the function has a warnings slice, and use `.SQL`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages, golden fixtures unchanged.

- [ ] **Step 5: Commit**

```bash
git add dialect/render_sql.go dialect/sql_render_test.go dialect/cortex.go dialect/nao_yaml.go dialect/nao_context_rules.go dialect/databricks_metric_view.go
git commit -m "refactor(dialect): renderSQL becomes renderANSI, one sqlRenderer shape"
```

---

### Task 10: One renderer for snowflake-semantic-view

**Files:**
- Modify: `dialect/snowflake_semantic_view.go:222-269`
- Test: `dialect/snowflake_semantic_view_test.go`

**Interfaces:**
- Consumes: `sqlRenderer`, `sqlCtx`, `sqlResult`, `renderANSI` from Task 9.
- Produces: `func renderSnowflakeSV(def ir.Expr, c sqlCtx) (sqlResult, error)`. Task 13 registers it.

- [ ] **Step 1: Write the failing test**

Append to `dialect/snowflake_semantic_view_test.go`:

```go
var _ sqlRenderer = renderSnowflakeSV

// A simple aggregate renders as-is, uppercased.
func TestRenderSnowflakeSVAggregate(t *testing.T) {
	def := ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "amount"}}
	got, err := renderSnowflakeSV(def, sqlCtx{})
	if err != nil {
		t.Fatalf("renderSnowflakeSV: %v", err)
	}
	if got.SQL != "SUM(FCT_ORDERS.AMOUNT)" {
		t.Errorf("SQL = %q, want %q", got.SQL, "SUM(FCT_ORDERS.AMOUNT)")
	}
}

// A derived metric must refer to other metrics by qualified name.
func TestRenderSnowflakeSVDerivedUsesQualifiedRefs(t *testing.T) {
	def := ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "orders"}}
	c := sqlCtx{TableOf: map[string]string{"revenue": "FCT_ORDERS", "orders": "FCT_ORDERS"}}
	got, err := renderSnowflakeSV(def, c)
	if err != nil {
		t.Fatalf("renderSnowflakeSV: %v", err)
	}
	if got.SQL != "FCT_ORDERS.REVENUE / FCT_ORDERS.ORDERS" {
		t.Errorf("SQL = %q", got.SQL)
	}
}

// An aggregate inlined inside derived arithmetic is invalid for Snowflake and
// must degrade, not render.
func TestRenderSnowflakeSVRejectsInlinedAggregate(t *testing.T) {
	def := ir.Binary{
		Op:    "/",
		Left:  ir.Agg{Func: "sum", Table: "t", Arg: ir.Col{Table: "t", Name: "a"}},
		Right: ir.Ref{Metric: "orders"},
	}
	c := sqlCtx{TableOf: map[string]string{"orders": "T"}}
	if _, err := renderSnowflakeSV(def, c); err == nil {
		t.Fatal("want an error for an aggregate inlined in derived arithmetic")
	}
}

// An unknown metric reference must degrade too.
func TestRenderSnowflakeSVRejectsUnknownRef(t *testing.T) {
	def := ir.Binary{Op: "/", Left: ir.Ref{Metric: "nope"}, Right: ir.Lit{Value: "2"}}
	if _, err := renderSnowflakeSV(def, sqlCtx{TableOf: map[string]string{}}); err == nil {
		t.Fatal("want an error for an unknown metric ref")
	}
}
```

Add `"github.com/benchouse/semglot/ir"` to the test imports if absent. Verify the two expected SQL strings against the current `renderSVMetricDef` output before writing the implementation: run the existing snowflake tests and read `test/models/ecommerce/dbt/snowflake-semantic-view/definition.md`. If they differ, use the fixture's strings, since the fixture is authoritative.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run TestRenderSnowflakeSV -v`
Expected: FAIL to build with `undefined: renderSnowflakeSV`.

- [ ] **Step 3: Write the implementation**

Replace `renderSVMetricDef`, `renderSVDerived` and `svNoResolve` with one function. The recursion is an internal closure, and the two rejection paths become errors:

```go
// renderSnowflakeSV renders a metric definition for a Snowflake semantic view.
// Ref policy: a referenced metric renders as its QUALIFIED NAME (TABLE.METRIC)
// and is never inlined, because Snowflake requires a derived metric to refer to
// other aggregate-level expressions without containing an aggregate itself; it
// rejects "SUM(x)/SUM(y)" outright. A simple aggregate is the exception and
// renders through renderANSI as-is. An inlined aggregate or column inside
// derived arithmetic, or a reference to an unknown metric, is not expressible
// and returns an error so the caller degrades the metric.
func renderSnowflakeSV(def ir.Expr, c sqlCtx) (sqlResult, error) {
	if _, isAgg := def.(ir.Agg); isAgg {
		r, err := renderANSI(def, sqlCtx{})
		if err != nil {
			return sqlResult{}, err
		}
		return sqlResult{SQL: strings.ToUpper(r.SQL)}, nil
	}
	var refs []string
	var walk func(ir.Expr) (string, error)
	walk = func(e ir.Expr) (string, error) {
		switch n := e.(type) {
		case ir.Ref:
			t, ok := c.TableOf[n.Metric]
			if !ok {
				return "", fmt.Errorf("references unknown metric %q", n.Metric)
			}
			refs = append(refs, n.Metric)
			return t + "." + strings.ToUpper(n.Metric), nil
		case ir.Lit:
			return n.Value, nil
		case ir.Binary:
			l, err := walk(n.Left)
			if err != nil {
				return "", err
			}
			r, err := walk(n.Right)
			if err != nil {
				return "", err
			}
			if _, ok := n.Left.(ir.Binary); ok {
				l = "(" + l + ")"
			}
			if _, ok := n.Right.(ir.Binary); ok {
				r = "(" + r + ")"
			}
			return l + " " + n.Op + " " + r, nil
		default:
			return "", fmt.Errorf("a derived metric cannot contain %T; Snowflake requires it to refer to other metrics without an aggregate", e)
		}
	}
	s, err := walk(def)
	if err != nil {
		return sqlResult{}, err
	}
	return sqlResult{SQL: s, Refs: refs}, nil
}
```

Update the call site in `Emit` to handle `error` where it previously handled `ok=false`, appending the error text to the existing `own` warnings slice in the same wording as today, so the emitted `comment` text does not change. Read the current degrade wording at the call site and reuse it verbatim, formatting the error into it. Add `"fmt"` to the file imports if absent.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages, `test/models/ecommerce/dbt/snowflake-semantic-view/definition.md` unchanged.

- [ ] **Step 5: Commit**

```bash
git add dialect/snowflake_semantic_view.go dialect/snowflake_semantic_view_test.go
git commit -m "refactor(dialect): one SQL renderer for snowflake-semantic-view"
```

---

### Task 11: One renderer for dbt

dbt renders SQL from three functions today: `renderDerived` (metric definitions, refs kept bare), `parenIfBinary` (its paren rule) and `emitFilterSQL` (measure filter fragments). They collapse into `renderDBT`, with `sqlCtx.Filter` selecting the filter behaviour.

**Files:**
- Modify: `dialect/dbt_emit.go:385-432`
- Test: `dialect/dbt_test.go`

**Interfaces:**
- Consumes: `sqlRenderer`, `sqlCtx`, `sqlResult`, `renderANSI` from Task 9.
- Produces: `func renderDBT(def ir.Expr, c sqlCtx) (sqlResult, error)`, returning `Refs` for derived metrics.

- [ ] **Step 1: Write the failing test**

Append to `dialect/dbt_test.go`:

```go
var _ sqlRenderer = renderDBT

// Ref policy: metric names stay bare and are collected, never inlined.
func TestRenderDBTKeepsRefsBare(t *testing.T) {
	def := ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "orders"}}
	got, err := renderDBT(def, sqlCtx{})
	if err != nil {
		t.Fatalf("renderDBT: %v", err)
	}
	if got.SQL != "revenue / orders" {
		t.Errorf("SQL = %q, want %q", got.SQL, "revenue / orders")
	}
	if len(got.Refs) != 2 || got.Refs[0] != "revenue" || got.Refs[1] != "orders" {
		t.Errorf("Refs = %v, want [revenue orders]", got.Refs)
	}
}

// A Ref is never compound here, so only a literal Binary operand gets parens.
func TestRenderDBTParenthesizesOnlyBinaryOperands(t *testing.T) {
	def := ir.Binary{
		Op:    "*",
		Left:  ir.Binary{Op: "+", Left: ir.Ref{Metric: "a"}, Right: ir.Ref{Metric: "b"}},
		Right: ir.Ref{Metric: "c"},
	}
	got, err := renderDBT(def, sqlCtx{})
	if err != nil {
		t.Fatalf("renderDBT: %v", err)
	}
	if got.SQL != "(a + b) * c" {
		t.Errorf("SQL = %q, want %q", got.SQL, "(a + b) * c")
	}
}

// Duplicate refs are deduped, preserving first-seen order.
func TestRenderDBTDedupesRefs(t *testing.T) {
	def := ir.Binary{Op: "+", Left: ir.Ref{Metric: "a"}, Right: ir.Ref{Metric: "a"}}
	got, err := renderDBT(def, sqlCtx{})
	if err != nil {
		t.Fatalf("renderDBT: %v", err)
	}
	if len(got.Refs) != 1 || got.Refs[0] != "a" {
		t.Errorf("Refs = %v, want [a]", got.Refs)
	}
}

// Filter mode renders a bare column, matching today's emitFilterSQL.
func TestRenderDBTFilterModeRendersBareColumn(t *testing.T) {
	got, err := renderDBT(ir.Col{Table: "fct_orders", Name: "is_paid"}, sqlCtx{Filter: true})
	if err != nil {
		t.Fatalf("renderDBT: %v", err)
	}
	if got.SQL != "is_paid" {
		t.Errorf("SQL = %q, want %q", got.SQL, "is_paid")
	}
}

// Filter mode passes a raw fragment through untouched.
func TestRenderDBTFilterModeRendersRaw(t *testing.T) {
	got, err := renderDBT(ir.Raw{SQL: "status = 'paid'", Columns: []string{"status"}}, sqlCtx{Filter: true})
	if err != nil {
		t.Fatalf("renderDBT: %v", err)
	}
	if got.SQL != "status = 'paid'" {
		t.Errorf("SQL = %q, want %q", got.SQL, "status = 'paid'")
	}
}

// A non-Col, non-Raw filter falls back to neutral ANSI rendering with no metric
// inlining, exactly as emitFilterSQL does today.
func TestRenderDBTFilterModeFallsBackToANSI(t *testing.T) {
	def := ir.Binary{Op: ">", Left: ir.Col{Table: "t", Name: "amount"}, Right: ir.Lit{Value: "0"}}
	got, err := renderDBT(def, sqlCtx{Filter: true})
	if err != nil {
		t.Fatalf("renderDBT: %v", err)
	}
	if got.SQL != "t.amount > 0" {
		t.Errorf("SQL = %q, want %q", got.SQL, "t.amount > 0")
	}
}

// A metric definition containing anything other than Ref/Lit/Binary is not a
// valid dbt derived expression and must degrade.
func TestRenderDBTRejectsNonDerivedNode(t *testing.T) {
	def := ir.Binary{Op: "/", Left: ir.Col{Name: "x"}, Right: ir.Ref{Metric: "orders"}}
	if _, err := renderDBT(def, sqlCtx{}); err == nil {
		t.Fatal("want an error for a column inlined in a derived expression")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run TestRenderDBT -v`
Expected: FAIL to build with `undefined: renderDBT`.

- [ ] **Step 3: Write the implementation**

Replace `renderDerived`, `parenIfBinary` and `emitFilterSQL` with one function. `dedupeStrs` stays; it is a string helper, not a renderer.

```go
// renderDBT renders SQL for the dbt emitter, in two positions selected by
// c.Filter.
//
// Metric definitions (c.Filter false): a derived arithmetic tree over metric
// references. Ref policy: metric names are PRESERVED as bare references and
// collected into Refs, never inlined, because dbt's own metric layer resolves
// them. That is why a Ref is never compound here and only a literal Binary
// operand needs parens, unlike renderANSI which inlines and must look through
// a Ref. Anything other than Ref/Lit/Binary is not a valid derived operand and
// returns an error so the caller degrades the metric.
//
// Filter fragments (c.Filter true): a Col renders as its bare name and a Raw
// passes through verbatim, because a dbt measure filter is scoped to its own
// semantic model. Any other node falls back to renderANSI with no resolver.
func renderDBT(def ir.Expr, c sqlCtx) (sqlResult, error) {
	if c.Filter {
		switch f := def.(type) {
		case ir.Col:
			return sqlResult{SQL: f.Name}, nil
		case ir.Raw:
			return sqlResult{SQL: f.SQL}, nil
		default:
			return renderANSI(def, sqlCtx{})
		}
	}
	var refs []string
	var walk func(ir.Expr) (string, error)
	walk = func(e ir.Expr) (string, error) {
		switch n := e.(type) {
		case ir.Ref:
			refs = append(refs, n.Metric)
			return n.Metric, nil
		case ir.Lit:
			return n.Value, nil
		case ir.Binary:
			l, err := walk(n.Left)
			if err != nil {
				return "", err
			}
			r, err := walk(n.Right)
			if err != nil {
				return "", err
			}
			if _, ok := n.Left.(ir.Binary); ok {
				l = "(" + l + ")"
			}
			if _, ok := n.Right.(ir.Binary); ok {
				r = "(" + r + ")"
			}
			return l + " " + n.Op + " " + r, nil
		default:
			return "", fmt.Errorf("not a dbt derived operand: %T", e)
		}
	}
	s, err := walk(def)
	if err != nil {
		return sqlResult{}, err
	}
	return sqlResult{SQL: s, Refs: dedupeStrs(refs)}, nil
}
```

Update the two call sites in `dialect/dbt_emit.go`. The `emitFilterSQL(e)` call becomes `renderDBT(e, sqlCtx{Filter: true})`, and the `renderDerived(e)` call in `emitMetrics` becomes `renderDBT(e, sqlCtx{})`, switching its `ok` check to an `err != nil` check with identical control flow: on failure the metric is skipped exactly as it is today. Add `"fmt"` to the imports if absent.

Note that `emitMetrics` returns no warnings today. Do not add a warnings path in this task; a metric that fails to render is skipped as before. Part 1's `warnings` return on dbt stays nil.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages, `test/models/ecommerce/dbt/dbt/ecommerce.yml` unchanged.

- [ ] **Step 5: Commit**

```bash
git add dialect/dbt_emit.go dialect/dbt_test.go
git commit -m "refactor(dialect): one SQL renderer for dbt"
```

---

### Task 12: One renderer for databricks-metric-view

Databricks reaches SQL through `renderANSI`, then post-processes with `dbxStripSourceQualifier`, separately builds raw measures with `aggExpr`, and validates with `dbxValidMeasureExpr`. One function owns all of it.

**Files:**
- Modify: `dialect/databricks_metric_view.go:245-300, 388-475`
- Test: `dialect/databricks_metric_view_test.go`

**Interfaces:**
- Consumes: `sqlRenderer`, `sqlCtx`, `renderANSI` from Task 9, `aggExpr` from `dialect/dbt.go`.
- Produces: `func renderDatabricksSQL(def ir.Expr, c sqlCtx) (sqlResult, error)`.

- [ ] **Step 1: Write the failing test**

Append to `dialect/databricks_metric_view_test.go`:

```go
var _ sqlRenderer = renderDatabricksSQL

// The owning table's qualifier is stripped, because a metric view's measures
// are already scoped to source. The match is case-insensitive.
func TestRenderDatabricksStripsSourceQualifier(t *testing.T) {
	def := ir.Agg{Func: "sum", Table: "FCT_Orders", Arg: ir.Col{Table: "FCT_Orders", Name: "amount"}}
	got, err := renderDatabricksSQL(def, sqlCtx{Table: "fct_orders"})
	if err != nil {
		t.Fatalf("renderDatabricksSQL: %v", err)
	}
	if got.SQL != "sum(amount)" {
		t.Errorf("SQL = %q, want %q", got.SQL, "sum(amount)")
	}
}

// A joined table's qualifier is NOT stripped; only the owning table's is.
func TestRenderDatabricksKeepsJoinedQualifier(t *testing.T) {
	def := ir.Agg{Func: "sum", Table: "fct_orders", Arg: ir.Col{Table: "dim_customer", Name: "ltv"}}
	got, err := renderDatabricksSQL(def, sqlCtx{Table: "fct_orders"})
	if err != nil {
		t.Fatalf("renderDatabricksSQL: %v", err)
	}
	if got.SQL != "sum(dim_customer.ltv)" {
		t.Errorf("SQL = %q, want %q", got.SQL, "sum(dim_customer.ltv)")
	}
}

// An expression Databricks would reject must degrade rather than emit.
func TestRenderDatabricksRejectsInvalidMeasureExpr(t *testing.T) {
	def := ir.Agg{Func: "some_unknown_agg", Table: "fct_orders", Arg: ir.Col{Table: "fct_orders", Name: "amount"}}
	if _, err := renderDatabricksSQL(def, sqlCtx{Table: "fct_orders"}); err == nil {
		t.Fatal("want an error for an aggregate Databricks does not define")
	}
}
```

Before writing the implementation, read `dbxValidMeasureExpr` and `dbxKnownAggs` at `dialect/databricks_metric_view.go:433-475` and confirm the third test's aggregate name is genuinely rejected. If `dbxKnownAggs` admits it, pick one it does not.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run TestRenderDatabricks -v`
Expected: FAIL to build with `undefined: renderDatabricksSQL`.

- [ ] **Step 3: Write the implementation**

```go
// renderDatabricksSQL renders a metric definition as a metric-view measure
// expression. Ref policy: a referenced metric is INLINED, like renderANSI,
// which is safe only within one grain; the caller pre-checks cross-grain refs
// with dbxCrossGrain and degrades them before calling this. The owning table's
// qualifier is then stripped, because a measure is already scoped to source,
// while a joined table's qualifier is kept. The match is case-insensitive
// because renderANSI preserves the source table's original casing. Finally the
// result is validated, so an expression Databricks would reject degrades here
// rather than being written out.
func renderDatabricksSQL(def ir.Expr, c sqlCtx) (sqlResult, error) {
	r, err := renderANSI(def, c)
	if err != nil {
		return sqlResult{}, err
	}
	expr := dbxStripSourceQualifier(r.SQL, c.Table)
	if !dbxValidMeasureExpr(expr) {
		return sqlResult{}, fmt.Errorf("expression %q is not a valid Databricks measure expression", expr)
	}
	return sqlResult{SQL: expr}, nil
}
```

`dbxStripSourceQualifier` and `dbxValidMeasureExpr` become unexported helpers of this one renderer. Keeping them as separate small functions is consistent with the rule, which is about there being one entry point per dialect, not about inlining every helper. If a reviewer prefers them folded in as closures, that is acceptable; do not change their logic either way.

Update the `Emit` call site at `dialect/databricks_metric_view.go:252` to call `renderDatabricksSQL(mt.Def, sqlCtx{Resolve: resolve, Table: t.Name})` and treat the error as a degrade warning, reusing today's exact warning wording so the emitted output and the warning text both stay the same. The `dbxDegrade` and `dbxCrossGrain` pre-checks stay where they are and keep running before this call.

The raw-measure path at `dialect/databricks_metric_view.go:279`, which calls `aggExpr(ms.Agg, strings.ToLower(ms.Expr))`, is building an expression from a measure declaration rather than rendering an IR expression tree, so it stays as it is. Leave the existing comment there explaining the lowercasing asymmetry.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./...`
Expected: PASS on all four packages, every file under `test/models/ecommerce/dbt/databricks-metric-view/` unchanged.

- [ ] **Step 5: Commit**

```bash
git add dialect/databricks_metric_view.go dialect/databricks_metric_view_test.go
git commit -m "refactor(dialect): one SQL renderer for databricks-metric-view"
```

---

### Task 13: The renderer registry and shared corpus test

Every renderer now has the same signature, so one test can run them all over one corpus. The registry exists for this test, not for dispatch.

**Files:**
- Modify: `dialect/render_sql.go`, `dialect/sql_render_test.go`
- Modify: `dialect/README.md`

**Interfaces:**
- Consumes: `renderANSI`, `renderSnowflakeSV`, `renderDBT`, `renderDatabricksSQL`.
- Produces: `sqlRenderers map[string]sqlRenderer`.

- [ ] **Step 1: Write the failing test**

Append to `dialect/sql_render_test.go`:

```go
// Every emitter that renders metric SQL must have exactly one renderer, and it
// must be registered here. supersimple is the deliberate exception: it emits
// structured YAML and its only SQL work is toPropertySQL, which rewrites
// identifiers in a raw fragment rather than walking an expression tree.
var sqlRendererExceptions = map[string]bool{"supersimple": true}

func TestEverySQLEmitterHasARegisteredRenderer(t *testing.T) {
	for _, name := range Names() {
		if _, err := AsEmitter(name); err != nil {
			continue
		}
		if sqlRendererExceptions[name] {
			continue
		}
		if _, ok := sqlRenderers[name]; !ok {
			t.Errorf("dialect %q has no registered sqlRenderer", name)
		}
	}
}

// One corpus, every renderer, so the differences between targets are visible
// side by side and a change to one shows up as a diff here.
func TestSQLRendererCorpus(t *testing.T) {
	resolve := func(n string) (ir.Expr, bool) {
		if n == "ratio" {
			return ir.Binary{Op: "/", Left: ir.Col{Table: "t", Name: "x"}, Right: ir.Col{Table: "t", Name: "y"}}, true
		}
		return nil, false
	}
	cases := []struct {
		name string
		def  ir.Expr
	}{
		{"simple_agg", ir.Agg{Func: "sum", Table: "t", Arg: ir.Col{Table: "t", Name: "amount"}}},
		{"filtered_agg", ir.Agg{Func: "sum", Table: "t", Arg: ir.Col{Table: "t", Name: "amount"},
			Filter: ir.Raw{SQL: "status = 'paid'", Columns: []string{"status"}}}},
		{"ratio_of_refs", ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "orders"}}},
		{"nested_binary", ir.Binary{Op: "*",
			Left:  ir.Binary{Op: "+", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "orders"}},
			Right: ir.Lit{Value: "100"}}},
		{"inlined_compound_ref", ir.Binary{Op: "*", Left: ir.Ref{Metric: "ratio"}, Right: ir.Lit{Value: "100"}}},
		{"unknown_ref", ir.Binary{Op: "/", Left: ir.Ref{Metric: "nope"}, Right: ir.Lit{Value: "2"}}},
	}
	c := sqlCtx{
		Resolve: resolve,
		Table:   "t",
		TableOf: map[string]string{"revenue": "T", "orders": "T", "ratio": "T"},
	}
	for _, tc := range cases {
		for _, name := range sortedRendererNames() {
			got, err := sqlRenderers[name](tc.def, c)
			// Both outcomes are valid and interesting: a rendered string, or a
			// degrade reason. Log both so the table reads as documentation.
			if err != nil {
				t.Logf("%-22s %-24s degraded: %v", tc.name, name, err)
				continue
			}
			t.Logf("%-22s %-24s %s", tc.name, name, got.SQL)
		}
	}
}

func sortedRendererNames() []string {
	out := make([]string, 0, len(sqlRenderers))
	for n := range sqlRenderers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
```

Add `"sort"` to the test imports.

This corpus test logs rather than asserts, because its value is making the four policies visible in one place. Add assertions for any pair whose difference you want pinned; at minimum, pin that `ratio_of_refs` renders differently for dbt (bare names) and snowflake (qualified names):

```go
func TestCorpusPinsRefPolicyDifference(t *testing.T) {
	def := ir.Binary{Op: "/", Left: ir.Ref{Metric: "revenue"}, Right: ir.Ref{Metric: "orders"}}
	c := sqlCtx{TableOf: map[string]string{"revenue": "T", "orders": "T"}}
	dbtOut, err := sqlRenderers["dbt"](def, c)
	if err != nil {
		t.Fatalf("dbt: %v", err)
	}
	svOut, err := sqlRenderers["snowflake-semantic-view"](def, c)
	if err != nil {
		t.Fatalf("snowflake-semantic-view: %v", err)
	}
	if dbtOut.SQL != "revenue / orders" {
		t.Errorf("dbt SQL = %q, want bare refs", dbtOut.SQL)
	}
	if svOut.SQL != "T.REVENUE / T.ORDERS" {
		t.Errorf("snowflake SQL = %q, want qualified refs", svOut.SQL)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./dialect/ -run 'TestEverySQLEmitterHasARegisteredRenderer|TestSQLRendererCorpus|TestCorpusPinsRefPolicyDifference' -v`
Expected: FAIL to build with `undefined: sqlRenderers`.

- [ ] **Step 3: Write the implementation**

Add to `dialect/render_sql.go`:

```go
// sqlRenderers maps a dialect name to its single SQL renderer. Its only
// consumer is the corpus test, which runs every renderer over one set of
// expressions and fails when a dialect is added without one. Emitters call
// their own renderer directly; this map is never used for dispatch.
var sqlRenderers = map[string]sqlRenderer{
	"cortex":                  renderANSI,
	"nao-yaml":                renderANSI,
	"nao-context-rules":       renderANSI,
	"dbt":                     renderDBT,
	"snowflake-semantic-view": renderSnowflakeSV,
	"databricks-metric-view":  renderDatabricksSQL,
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./dialect/ -run 'TestEverySQLEmitterHasARegisteredRenderer|TestSQLRendererCorpus|TestCorpusPinsRefPolicyDifference' -v`
Expected: PASS. Read the corpus log output and sanity check each line against what you know each target requires.

Run: `go test ./...`
Expected: PASS on all four packages.

- [ ] **Step 5: Update the docs and commit**

In `dialect/README.md`, document the rule: each dialect has exactly one SQL-rendering function conforming to `sqlRenderer`, registered in `sqlRenderers`, with supersimple as the documented exception. State the three ref policies in one table, since that is the distinction a new emitter author will get wrong.

```bash
git add dialect/render_sql.go dialect/sql_render_test.go dialect/README.md
git commit -m "test(dialect): one corpus across every SQL renderer

Adds the sqlRenderers registry, whose only consumer is this test. It
fails when a dialect gains an emitter without a renderer, and pins the
ref-policy difference between dbt and snowflake that the old
'do not merge these' comments were guarding by hand."
```

---

## Final verification

- [ ] **Run the full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: clean build, no vet findings, PASS on all four packages.

- [ ] **Confirm no emitted output changed**

Run: `git diff main --stat -- test/models/`
Expected: no output. Any change here means the refactor altered emitted output and must be fixed before merging.

- [ ] **Confirm the duplication is actually gone**

Run: `grep -rn 'Configurable\|WithOptions' --include='*.go' .`
Expected: no output.

Run: `grep -rn 'os.MkdirAll\|os.WriteFile' dialect/*.go | grep -v _test`
Expected: exactly two lines, both in `dialect/emit_files.go`.

Run: `grep -rn 'renderSQL\|renderOperand\|isCompound\|renderDerived\|parenIfBinary\|emitFilterSQL\|renderSVDerived\|renderSVMetricDef\|svNoResolve' --include='*.go' .`
Expected: no output.

- [ ] **Run the CLI end to end**

Profiles live in `semglot.yaml` at the repo root, overridable with `--config`. Write a scratch config with one profile per target, pointing `sources` at `test/models/ecommerce/dbt/marts` and `output` at a temp directory, then for each target run:

```bash
go run ./cmd/semglot build --config /tmp/semglot-smoke.yaml --profile <target>
```

Expected: exits 0 and prints `wrote to <dir> (dbt -> <target>)`. If the profile's model degrades anything, the same `warning: N item(s) degraded or dropped by the <target> target:` block appears as before this work, because Part 1 did not change what goes into `warnings`. Copy the profile field names from `cmd/semglot/config.go` and `cmd/semglot/config_test.go`.
