# Clean CLI + config resolution — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Shrink `semglot build` to `--source/--target/--target-type` plus optional overrides, resolving model identity through `default < --config file < explicit flag`.

**Architecture:** All changes are in `cmd/semglot` plus one read-only helper in `layer`. Task 1 adds a pure, unit-tested resolver (`resolveIdentity` + config-file parsing). Task 2 rewires `buildCmd` to the new flags, wires the resolver via `fs.Visit`, adds `layer.Names()` for help, and updates the CLI end-to-end test. The `layer` emitters are otherwise untouched, so every emitter golden stays byte-identical. Design: `docs/design-clean-cli-config.md`.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (only dep). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; no new deps; no `internal/`.
- **Emitter goldens unchanged** — `test/models/ecommerce/dbt/*/` byte-identical (this is a CLI-layer change; `layer` behavior is untouched).
- **Precedence:** `default < --config file < explicit CLI flag`. "Explicit" = the flag was actually passed (via `flag.FlagSet.Visit`), NOT zero-value comparison — so `--database ""` beats a config value and a defaulted `--schema` does not outrank config.
- **Defaults:** `schema=MAIN`; `name` ← basename of `--source` (fallback `semantic_model`); `database`/`description` empty.
- A missing/invalid `--config` file is a fatal error (the user asked for it); absence of `--config` is fine.
- `go build ./...`, `go test ./...`, `test -z "$(gofmt -l .)"`, `go vet ./...` clean per task.

## File Structure

- `cmd/semglot/config.go` — new: `identity`, `configFile`, `resolveIdentity`, `defaultName` (Task 1).
- `cmd/semglot/config_test.go` — new: resolver precedence unit tests (Task 1).
- `cmd/semglot/main.go` — rewire `buildCmd`, `usage()`, doc comment (Task 2).
- `layer/layer.go` — add `Names() []string` (sorted registered dialects) (Task 2).
- `test/integration_test.go` — update `TestCLIBinaryEndToEnd` to new flags (Task 2).

---

### Task 1: The identity resolver (pure, TDD)

**Files:** Create `cmd/semglot/config.go`, `cmd/semglot/config_test.go`.

**Interfaces:**
- Produces: `type identity struct{ Database, Schema, Name, Description string }`; `func resolveIdentity(sourceDir, configPath string, set map[string]bool, flags identity) (identity, error)`; `func defaultName(sourceDir string) string`.
- `set` is the set of explicitly-passed flag names; `flags` carries the raw flag values.

- [ ] **Step 1: Write the failing tests (`cmd/semglot/config_test.go`)**
```go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveIdentityDefaults(t *testing.T) {
	got, err := resolveIdentity("/x/ecommerce", "", map[string]bool{}, identity{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Schema != "MAIN" {
		t.Errorf("schema = %q, want MAIN", got.Schema)
	}
	if got.Name != "ecommerce" {
		t.Errorf("name = %q, want ecommerce (source basename)", got.Name)
	}
	if got.Database != "" || got.Description != "" {
		t.Errorf("database/description should be empty, got %q/%q", got.Database, got.Description)
	}
}

func TestResolveIdentityConfigOverridesDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yml")
	if err := os.WriteFile(cfg, []byte("database: EVAL_MARTS\nschema: SEM\nname: SV_ECOMM\ndescription: hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveIdentity("/x/ecommerce", cfg, map[string]bool{}, identity{})
	if err != nil {
		t.Fatal(err)
	}
	if got != (identity{Database: "EVAL_MARTS", Schema: "SEM", Name: "SV_ECOMM", Description: "hi"}) {
		t.Errorf("got %+v", got)
	}
}

func TestResolveIdentityFlagOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yml")
	os.WriteFile(cfg, []byte("database: EVAL_MARTS\nschema: SEM\n"), 0o644)
	// explicit --database wins; --schema not passed so config's SEM stands
	got, err := resolveIdentity("/x/ecommerce", cfg,
		map[string]bool{"database": true}, identity{Database: "STAGING"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Database != "STAGING" {
		t.Errorf("database = %q, want STAGING (explicit flag)", got.Database)
	}
	if got.Schema != "SEM" {
		t.Errorf("schema = %q, want SEM (from config, flag not passed)", got.Schema)
	}
}

func TestResolveIdentityExplicitEmptyBeatsConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yml")
	os.WriteFile(cfg, []byte("database: EVAL_MARTS\n"), 0o644)
	got, err := resolveIdentity("/x/ecommerce", cfg,
		map[string]bool{"database": true}, identity{Database: ""})
	if err != nil {
		t.Fatal(err)
	}
	if got.Database != "" {
		t.Errorf("explicit --database \"\" should win, got %q", got.Database)
	}
}

func TestResolveIdentityMissingConfigErrors(t *testing.T) {
	if _, err := resolveIdentity("/x", "/no/such/file.yml", map[string]bool{}, identity{}); err == nil {
		t.Fatal("want error for missing --config file")
	}
}

func TestDefaultName(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b/ecommerce": "ecommerce",
		"ecommerce/":     "ecommerce",
		".":              "semantic_model",
		"":               "semantic_model",
	} {
		if got := defaultName(in); got != want {
			t.Errorf("defaultName(%q) = %q, want %q", in, got, want)
		}
	}
}
```
Run: `go test ./cmd/semglot/ -run 'ResolveIdentity|DefaultName'` → FAIL (undefined).

- [ ] **Step 2: Implement (`cmd/semglot/config.go`)**
```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// identity is the model-identity settings a Configurable emitter needs.
type identity struct {
	Database    string
	Schema      string
	Name        string
	Description string
}

// configFile is the flat YAML shape read from --config.
type configFile struct {
	Database    string `yaml:"database"`
	Schema      string `yaml:"schema"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// resolveIdentity layers defaults < config file < explicit flags. set holds the
// names of flags the user actually passed (from flag.FlagSet.Visit); flags holds
// their raw values. A non-empty configPath must exist and parse.
func resolveIdentity(sourceDir, configPath string, set map[string]bool, flags identity) (identity, error) {
	id := identity{Schema: "MAIN", Name: defaultName(sourceDir)}

	if configPath != "" {
		b, err := os.ReadFile(configPath)
		if err != nil {
			return identity{}, fmt.Errorf("read --config: %w", err)
		}
		var cf configFile
		if err := yaml.Unmarshal(b, &cf); err != nil {
			return identity{}, fmt.Errorf("parse --config %s: %w", configPath, err)
		}
		if cf.Database != "" {
			id.Database = cf.Database
		}
		if cf.Schema != "" {
			id.Schema = cf.Schema
		}
		if cf.Name != "" {
			id.Name = cf.Name
		}
		if cf.Description != "" {
			id.Description = cf.Description
		}
	}

	if set["database"] {
		id.Database = flags.Database
	}
	if set["schema"] {
		id.Schema = flags.Schema
	}
	if set["name"] {
		id.Name = flags.Name
	}
	if set["description"] {
		id.Description = flags.Description
	}
	return id, nil
}

// defaultName derives the default model name from the source directory basename.
func defaultName(sourceDir string) string {
	base := filepath.Base(strings.TrimRight(sourceDir, "/"))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "semantic_model"
	}
	return base
}
```
Run: `go test ./cmd/semglot/ -run 'ResolveIdentity|DefaultName'` → PASS.

- [ ] **Step 3: Full suite + commit**
```bash
go test ./... && test -z "$(gofmt -l .)" && go vet ./...
git add cmd/semglot/config.go cmd/semglot/config_test.go
git commit -m "feat(cli): identity resolver — defaults < config file < explicit flags"
```

---

### Task 2: Rewire the CLI to the clean flags

**Files:** Modify `cmd/semglot/main.go`, `layer/layer.go`, `test/integration_test.go`.

**Interfaces:**
- Consumes: `resolveIdentity`/`identity`/`defaultName` (Task 1); `layer.AsParser`/`AsEmitter`/`Configurable`.
- Produces: `layer.Names() []string` (sorted registered dialect names).

- [ ] **Step 1: Add `layer.Names()` (`layer/layer.go`)**
```go
// Names returns the registered dialect names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
```
Add `"sort"` to `layer/layer.go`'s imports.

- [ ] **Step 2: Rewire `buildCmd` + `usage()` + doc comment (`cmd/semglot/main.go`)**
Replace the `buildCmd` flag block and identity wiring:
```go
func buildCmd(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	sourceType := fs.String("source-type", "dbt", "source dialect")
	source := fs.String("source", "", "source directory (required)")
	targetType := fs.String("target-type", "", "target dialect (required); one of: "+strings.Join(layer.Names(), ", "))
	target := fs.String("target", "", "output directory (required)")
	config := fs.String("config", "", "path to a config file (optional)")
	database := fs.String("database", "", "warehouse database (Snowflake targets)")
	schema := fs.String("schema", "", "warehouse schema (Snowflake targets; default MAIN)")
	name := fs.String("name", "", "model/view name (default: source basename)")
	description := fs.String("description", "", "model description")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *source == "" || *targetType == "" || *target == "" {
		fmt.Fprintln(os.Stderr, "build: --source, --target and --target-type are required")
		return 2
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	parser, err := layer.AsParser(*sourceType)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	emitter, err := layer.AsEmitter(*targetType)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	if c, ok := emitter.(layer.Configurable); ok {
		id, err := resolveIdentity(*source, *config, set,
			identity{Database: *database, Schema: *schema, Name: *name, Description: *description})
		if err != nil {
			fmt.Fprintln(os.Stderr, "build:", err)
			return 1
		}
		emitter = c.WithOptions(id.Database, id.Schema, id.Name, id.Description)
	}

	model, err := parser.Parse(*source)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	if err := emitter.Emit(model, *target); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d item(s) could not be fully transpiled:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	fmt.Printf("wrote to %s (%s -> %s)\n", *target, *sourceType, *targetType)
	return 0
}
```
Update `usage()`:
```go
func usage() {
	fmt.Fprintln(os.Stderr, "usage: semglot build --source <dir> --target <dir> --target-type <dialect> [--config <file>] [--database --schema --name --description]")
	fmt.Fprintln(os.Stderr, "target-type is one of: "+strings.Join(layer.Names(), ", "))
}
```
Add `"strings"` to `main.go` imports. Update the top-of-file doc comment example to the new flags.

- [ ] **Step 3: Update the CLI end-to-end test (`test/integration_test.go`)**
In `TestCLIBinaryEndToEnd`, replace the args:
```go
	cmd := exec.Command("go", "run", "./cmd/semglot", "build",
		"--source", ref, "--target-type", "cortex", "--target", out,
		"--database", "ANALYTICS", "--name", "ecommerce")
```
(Drop `--from`; `--source-type` defaults to dbt. The golden comparison is unchanged — output must stay byte-identical.)

- [ ] **Step 4: Verify + commit**
Run:
```bash
go build ./... && go test ./... && test -z "$(gofmt -l .)" && go vet ./...
go run ./cmd/semglot build --source test/models/ecommerce/dbt --target /tmp/sv --target-type snowflake-semantic-view --database ANALYTICS
go run ./cmd/semglot build --help
```
Expected: full suite green; the manual runs succeed and `--help` shows the new flags + the dialect list; emitter goldens unchanged (`git status`). Then:
```bash
git add cmd/semglot/main.go layer/layer.go test/integration_test.go
git commit -m "feat(cli): clean build flags (--source/--target/--target-type) + --config resolution"
```

---

## Self-Review

**1. Spec coverage:** flag rename (`--source/--target/--target-type`, `--source-type` default dbt, drop `--from`) → Task 2. `--config` loader + flat YAML → Task 1. Layered resolver (default < config < explicit, via `fs.Visit`) → Task 1 wired in Task 2. `name`←source basename default → Task 1. Target-neutral help + dialect list (`layer.Names()`) → Task 2. CLI e2e test updated, goldens unchanged → Task 2. ✅

**2. Placeholder scan:** No TBD/TODO. Resolver code + precedence tests are complete; the `set` map from `fs.Visit` is the explicit-flag mechanism, exercised by `TestResolveIdentityFlagOverridesConfig`/`ExplicitEmptyBeatsConfig`. Out-of-scope items (dbt-source, file/stdout target, harness caller) are named in the design, not silently dropped. ✅

**3. Type consistency:** `identity{Database,Schema,Name,Description}` and `resolveIdentity(sourceDir, configPath string, set map[string]bool, flags identity)` are used identically in Task 1 (defined + tested) and Task 2 (called). `layer.Names() []string` defined in Task 2 Step 1, consumed in Steps 2. `WithOptions(database, schema, name, description)` matches the existing `Configurable` signature. ✅
