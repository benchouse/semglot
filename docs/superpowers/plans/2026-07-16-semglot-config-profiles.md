# semglot config profiles Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace semglot's flat, flag-driven build config with named, self-contained profiles in `semglot.yaml`, selected by `--profile`.

**Architecture:** A new profiles loader in `cmd/semglot/config.go` parses `semglot.yaml`, selects the named profile, applies defaults, validates, and returns a `buildSpec`. `cmd/semglot/main.go`'s `buildCmd` drops all ad-hoc flags and drives the build entirely from that spec. Tests and README follow.

**Tech Stack:** Go 1.26, `gopkg.in/yaml.v3` (only dependency). No new deps.

## Global Constraints

- Module `github.com/benchouse/semglot`; Go 1.26; only dependency `gopkg.in/yaml.v3`; no new deps.
- Profiles-only CLI: `--profile <name>` is required; `--config <path>` defaults to `semglot.yaml`. All old flags (`--source`, `--target`, `--target-type`, `--source-type`, `--database`, `--schema`, `--name`, `--description`) are removed.
- Config field names: `source`, `source-dialect`, `target-dialect`, `output`, `database`, `schema`, `model-name`, `description`.
- Defaults for omitted optional fields: `source-dialect` = `dbt`, `schema` = `MAIN`, `model-name` = basename of the first source directory.
- Snowflake targets (`cortex`, `snowflake-semantic-view`) require a `database`.
- No em-dashes (`—`) anywhere in docs, comments, or copy.
- The transpile itself is unchanged: the Cortex golden (`layer/testdata/cortex/semantic_model.golden.yaml` and `test/models/ecommerce/dbt/cortex/ecommerce.yaml`) stays byte-identical.
- Per task: `go build ./...`, `go test ./...`, `test -z "$(gofmt -l .)"`, `go vet ./...` all clean.

## File Structure

- `cmd/semglot/config.go`: rewritten: `sourcePaths`, `profile`, `configFile`, `buildSpec` types; `snowflakeTargets`; `loadProfile`; `defaultModelName`. Replaces the old `identity`/`configFile`/`resolveIdentity`/`defaultName`.
- `cmd/semglot/main.go`: rewritten `buildCmd` (profiles-only) + `usage` + package doc comment. Removes `sourceList` and the `snowflakeTargets` var (moved to config.go).
- `cmd/semglot/config_test.go`: rewritten around `loadProfile`/`defaultModelName`.
- `cmd/semglot/main_test.go`: rewritten around `--profile`/`--config` and a `semglot.yaml` fixture.
- `test/integration_test.go`: the CLI-exec test writes a `semglot.yaml` and runs `--profile`.
- `README.md`: Usage and Configuration sections rewritten to the profile model.

The whole `cmd/semglot` package (config.go + main.go + both test files) changes atomically in Task 1, because the files reference each other and must compile together.

---

### Task 1: Profiles-only CLI (config loader + buildCmd)

**Files:**
- Modify (replace contents): `cmd/semglot/config.go`
- Modify (replace `buildCmd`, `usage`, doc comment; remove `sourceList`): `cmd/semglot/main.go`
- Modify (replace contents): `cmd/semglot/config_test.go`
- Modify (replace contents): `cmd/semglot/main_test.go`

**Interfaces:**
- Produces: `loadProfile(configPath, name string) (buildSpec, error)`; `defaultModelName(sources []string) string`; types `buildSpec{Sources []string; SourceDialect, TargetDialect, Output, Database, Schema, ModelName, Description string}`, `profile`, `configFile{Profiles map[string]profile}`, `sourcePaths []string`; var `snowflakeTargets map[string]bool`.
- Consumes: `layer.AsParser`, `layer.AsEmitter`, `layer.Configurable.WithOptions(database, schema, name, description string) Emitter`, `layer.CortexTypeGaps`, `layer.Names` (existing, unchanged).

- [ ] **Step 1: Replace `cmd/semglot/config.go`**

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// sourcePaths is a profile's `source`. It accepts either a single directory
// (a YAML scalar) or a list of directories (a YAML sequence).
type sourcePaths []string

func (s *sourcePaths) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*s = sourcePaths{node.Value}
		return nil
	}
	var list []string
	if err := node.Decode(&list); err != nil {
		return err
	}
	*s = list
	return nil
}

// profile is one named build in semglot.yaml.
type profile struct {
	Source        sourcePaths `yaml:"source"`
	SourceDialect string      `yaml:"source-dialect"`
	TargetDialect string      `yaml:"target-dialect"`
	Output        string      `yaml:"output"`
	Database      string      `yaml:"database"`
	Schema        string      `yaml:"schema"`
	ModelName     string      `yaml:"model-name"`
	Description   string      `yaml:"description"`
}

// configFile is the top-level shape of semglot.yaml.
type configFile struct {
	Profiles map[string]profile `yaml:"profiles"`
}

// buildSpec is a fully-resolved build: a validated profile with defaults applied.
type buildSpec struct {
	Sources       []string
	SourceDialect string
	TargetDialect string
	Output        string
	Database      string
	Schema        string
	ModelName     string
	Description   string
}

// snowflakeTargets emit into a physical Snowflake database and therefore require
// a database; without one they'd emit invalid, unqualified DDL.
var snowflakeTargets = map[string]bool{"cortex": true, "snowflake-semantic-view": true}

// loadProfile reads configPath, selects the named profile, applies defaults, and
// validates it into a buildSpec.
func loadProfile(configPath, name string) (buildSpec, error) {
	b, err := os.ReadFile(configPath)
	if err != nil {
		return buildSpec{}, fmt.Errorf("read config %s: %w", configPath, err)
	}
	var cf configFile
	if err := yaml.Unmarshal(b, &cf); err != nil {
		return buildSpec{}, fmt.Errorf("parse config %s: %w", configPath, err)
	}
	p, ok := cf.Profiles[name]
	if !ok {
		return buildSpec{}, fmt.Errorf("profile %q not found in %s", name, configPath)
	}
	if len(p.Source) == 0 {
		return buildSpec{}, fmt.Errorf("profile %q: source is required", name)
	}
	if p.TargetDialect == "" {
		return buildSpec{}, fmt.Errorf("profile %q: target-dialect is required", name)
	}
	if p.Output == "" {
		return buildSpec{}, fmt.Errorf("profile %q: output is required", name)
	}
	spec := buildSpec{
		Sources:       []string(p.Source),
		SourceDialect: p.SourceDialect,
		TargetDialect: p.TargetDialect,
		Output:        p.Output,
		Database:      p.Database,
		Schema:        p.Schema,
		ModelName:     p.ModelName,
		Description:   p.Description,
	}
	if spec.SourceDialect == "" {
		spec.SourceDialect = "dbt"
	}
	if spec.Schema == "" {
		spec.Schema = "MAIN"
	}
	if spec.ModelName == "" {
		spec.ModelName = defaultModelName(spec.Sources)
	}
	if snowflakeTargets[spec.TargetDialect] && spec.Database == "" {
		return buildSpec{}, fmt.Errorf("profile %q: target-dialect %s requires a database", name, spec.TargetDialect)
	}
	return spec, nil
}

// defaultModelName derives the model name from the first source directory basename.
func defaultModelName(sources []string) string {
	base := filepath.Base(strings.TrimRight(sources[0], "/"))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "semantic_model"
	}
	return base
}
```

- [ ] **Step 2: Replace `buildCmd`, `usage`, `sourceList`, and the package doc comment in `cmd/semglot/main.go`**

The file becomes exactly this (the `main` function is unchanged from today; `sourceList` is deleted; `strings` is no longer imported):

```go
// Command semglot transpiles a source semantic-layer dialect into a target
// dialect through a neutral IR.
//
//	semglot build --profile <name> [--config semglot.yaml]
//
// Builds are configured with named profiles in semglot.yaml. Scoring
// (`semglot score`) is v2.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/benchouse/semglot/layer"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(buildCmd(os.Args[2:]))
	case "score":
		fmt.Fprintln(os.Stderr, "score is not implemented yet (v2)")
		os.Exit(1)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: semglot build --profile <name> [--config <file>]")
	fmt.Fprintln(os.Stderr, "profiles are defined in semglot.yaml (override the path with --config)")
}

func buildCmd(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	profileName := fs.String("profile", "", "profile name (required)")
	config := fs.String("config", "semglot.yaml", "path to the config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *profileName == "" {
		fmt.Fprintln(os.Stderr, "build: --profile is required")
		return 2
	}
	spec, err := loadProfile(*config, *profileName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}

	parser, err := layer.AsParser(spec.SourceDialect)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	emitter, err := layer.AsEmitter(spec.TargetDialect)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		return 1
	}
	if c, ok := emitter.(layer.Configurable); ok {
		emitter = c.WithOptions(spec.Database, spec.Schema, spec.ModelName, spec.Description)
	}

	model, err := parser.Parse(spec.Sources...)
	if err != nil {
		fmt.Fprintln(os.Stderr, "build: parse:", err)
		return 1
	}
	if err := emitter.Emit(model, spec.Output); err != nil {
		fmt.Fprintln(os.Stderr, "build: emit:", err)
		return 1
	}
	if len(model.Notes) > 0 {
		fmt.Fprintf(os.Stderr, "warning: %d item(s) could not be fully transpiled:\n", len(model.Notes))
		for _, n := range model.Notes {
			fmt.Fprintln(os.Stderr, "  - "+n)
		}
	}
	if spec.TargetDialect == "cortex" {
		if gaps := layer.CortexTypeGaps(model); len(gaps) > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d Cortex column(s) had no source data_type; inferred a type (add data_type in dbt to fix):\n", len(gaps))
			for _, g := range gaps {
				fmt.Fprintln(os.Stderr, "  - "+g)
			}
		}
	}
	fmt.Printf("wrote to %s (%s -> %s)\n", spec.Output, spec.SourceDialect, spec.TargetDialect)
	return 0
}
```

- [ ] **Step 3: Replace `cmd/semglot/config_test.go`**

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes a semglot.yaml with the given body into a temp dir and
// returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "semglot.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadProfileDefaults(t *testing.T) {
	cfg := writeConfig(t, `profiles:
  p:
    source: /x/ecommerce
    target-dialect: supersimple
    output: ./out
`)
	got, err := loadProfile(cfg, "p")
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceDialect != "dbt" {
		t.Errorf("source-dialect = %q, want dbt (default)", got.SourceDialect)
	}
	if got.Schema != "MAIN" {
		t.Errorf("schema = %q, want MAIN (default)", got.Schema)
	}
	if got.ModelName != "ecommerce" {
		t.Errorf("model-name = %q, want ecommerce (source basename)", got.ModelName)
	}
	if len(got.Sources) != 1 || got.Sources[0] != "/x/ecommerce" {
		t.Errorf("sources = %v, want [/x/ecommerce]", got.Sources)
	}
}

func TestLoadProfileExplicitValues(t *testing.T) {
	cfg := writeConfig(t, `profiles:
  p:
    source: /x/ecommerce
    source-dialect: dbt
    target-dialect: cortex
    output: ./out
    database: ANALYTICS
    schema: SEM
    model-name: catalog
    description: hi
`)
	got, err := loadProfile(cfg, "p")
	if err != nil {
		t.Fatal(err)
	}
	want := buildSpec{
		Sources:       []string{"/x/ecommerce"},
		SourceDialect: "dbt",
		TargetDialect: "cortex",
		Output:        "./out",
		Database:      "ANALYTICS",
		Schema:        "SEM",
		ModelName:     "catalog",
		Description:   "hi",
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadProfileSourceList(t *testing.T) {
	cfg := writeConfig(t, `profiles:
  p:
    source:
      - /x/semantic
      - /x/marts
    target-dialect: cortex
    output: ./out
    database: ANALYTICS
`)
	got, err := loadProfile(cfg, "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 2 || got.Sources[0] != "/x/semantic" || got.Sources[1] != "/x/marts" {
		t.Fatalf("sources = %v, want [/x/semantic /x/marts]", got.Sources)
	}
	if got.ModelName != "semantic" {
		t.Errorf("model-name = %q, want semantic (first source basename)", got.ModelName)
	}
}

func TestLoadProfileNotFound(t *testing.T) {
	cfg := writeConfig(t, `profiles:
  p:
    source: /x
    target-dialect: cortex
    output: ./out
    database: A
`)
	if _, err := loadProfile(cfg, "missing"); err == nil {
		t.Fatal("want error for a profile name not in the config")
	}
}

func TestLoadProfileMissingConfig(t *testing.T) {
	if _, err := loadProfile("/no/such/semglot.yaml", "p"); err == nil {
		t.Fatal("want error for a missing config file")
	}
}

func TestLoadProfileMissingRequired(t *testing.T) {
	cases := map[string]string{
		"no source": `profiles:
  p:
    target-dialect: cortex
    output: ./out
    database: A
`,
		"no target-dialect": `profiles:
  p:
    source: /x
    output: ./out
`,
		"no output": `profiles:
  p:
    source: /x
    target-dialect: cortex
    database: A
`,
	}
	for name, body := range cases {
		cfg := writeConfig(t, body)
		if _, err := loadProfile(cfg, "p"); err == nil {
			t.Errorf("%s: want a validation error", name)
		}
	}
}

func TestLoadProfileSnowflakeRequiresDatabase(t *testing.T) {
	cfg := writeConfig(t, `profiles:
  p:
    source: /x
    target-dialect: snowflake-semantic-view
    output: ./out
`)
	_, err := loadProfile(cfg, "p")
	if err == nil {
		t.Fatal("want error: snowflake target with no database")
	}
}

func TestDefaultModelName(t *testing.T) {
	for in, want := range map[string]string{
		"/a/b/ecommerce": "ecommerce",
		"ecommerce/":     "ecommerce",
		".":              "semantic_model",
		"":               "semantic_model",
	} {
		if got := defaultModelName([]string{in}); got != want {
			t.Errorf("defaultModelName([%q]) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 4: Replace `cmd/semglot/main_test.go`**

```go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCmdEndToEnd(t *testing.T) {
	out := t.TempDir()
	src, err := filepath.Abs("../../layer/testdata/dbt")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "semglot.yaml")
	body := fmt.Sprintf(`profiles:
  ecom:
    source: %s
    target-dialect: cortex
    output: %s
    database: ANALYTICS
    schema: MAIN
    model-name: eval_marts
`, src, out)
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := buildCmd([]string{"--profile", "ecom", "--config", cfg}); code != 0 {
		t.Fatalf("buildCmd exit code = %d, want 0", code)
	}

	got, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	want, err := os.ReadFile("../../layer/testdata/cortex/semantic_model.golden.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("build output != golden:\n--- got ---\n%s", got)
	}
}

func TestBuildCmdMissingProfile(t *testing.T) {
	if code := buildCmd([]string{}); code != 2 {
		t.Fatalf("missing --profile should exit 2, got %d", code)
	}
}

func TestBuildCmdSourceWithoutParser(t *testing.T) {
	// cortex is emit-only in v1; using it as source-dialect must fail clearly.
	cfg := filepath.Join(t.TempDir(), "semglot.yaml")
	os.WriteFile(cfg, []byte(`profiles:
  p:
    source: x
    source-dialect: cortex
    target-dialect: cortex
    output: y
    database: A
`), 0o644)
	if code := buildCmd([]string{"--profile", "p", "--config", cfg}); code != 1 {
		t.Fatalf("cortex-as-source should exit 1, got %d", code)
	}
}

// TestBuildCmdSnowflakeTargetRequiresDatabase proves a Snowflake-family target
// fails clearly (mentioning "database") instead of emitting invalid DDL when no
// database is set in the profile.
func TestBuildCmdSnowflakeTargetRequiresDatabase(t *testing.T) {
	src, err := filepath.Abs("../../layer/testdata/dbt")
	if err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "semglot.yaml")
	os.WriteFile(cfg, []byte(fmt.Sprintf(`profiles:
  p:
    source: %s
    target-dialect: snowflake-semantic-view
    output: %s
`, src, t.TempDir())), 0o644)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStderr := os.Stderr
	os.Stderr = w
	code := buildCmd([]string{"--profile", "p", "--config", cfg})
	w.Close()
	os.Stderr = origStderr
	stderr, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if code == 0 {
		t.Fatalf("buildCmd exit code = 0, want non-zero (missing database)")
	}
	if !strings.Contains(strings.ToLower(string(stderr)), "database") {
		t.Fatalf("stderr = %q, want a message mentioning \"database\"", stderr)
	}
}
```

- [ ] **Step 5: Build, run the package tests, and hygiene**

Run:
```sh
go build ./... && go test ./cmd/semglot/ -v 2>&1 | tail -30
test -z "$(gofmt -l .)" && echo fmt-ok && go vet ./...
```
Expected: `go build` clean; every `./cmd/semglot` test PASSes (`TestBuildCmdEndToEnd` matches the unchanged Cortex golden); `fmt-ok`; `go vet` silent.

- [ ] **Step 6: Commit**

```bash
git add cmd/semglot/config.go cmd/semglot/main.go cmd/semglot/config_test.go cmd/semglot/main_test.go
git commit -m "feat(cli): profiles-only build; --profile selects a semglot.yaml profile"
```

---

### Task 2: Point the integration test at a profile

**Files:**
- Modify: `test/integration_test.go` (the `exec.Command` block near line 299; add `fmt` and `os`/`filepath` imports if not already present)

**Interfaces:**
- Consumes: the profiles-only CLI from Task 1 (`semglot build --profile <name> --config <file>`).

- [ ] **Step 1: Replace the CLI invocation block**

Find this block (currently around lines 298-303):

```go
	out := t.TempDir()
	cmd := exec.Command("go", "run", "./cmd/semglot", "build",
		"--source", semantic, "--source", marts, // repeatable --source
		"--target-type", "cortex", "--target", out,
		"--database", "ANALYTICS", "--name", "ecommerce")
	cmd.Dir = moduleRoot
```

Replace it with:

```go
	out := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "semglot.yaml")
	cfgBody := fmt.Sprintf(`profiles:
  ecommerce:
    source:
      - %s
      - %s
    target-dialect: cortex
    output: %s
    database: ANALYTICS
    model-name: ecommerce
`, semantic, marts, out)
	if err := os.WriteFile(cfg, []byte(cfgBody), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./cmd/semglot", "build",
		"--profile", "ecommerce", "--config", cfg)
	cmd.Dir = moduleRoot
```

- [ ] **Step 2: Ensure imports**

Confirm `test/integration_test.go`'s import block includes `fmt`, `os`, and `path/filepath` (the block already uses `os`/`filepath` elsewhere; add `fmt` if missing). If the comment near line 28 mentions the old `--source a --source b` form, reword it to "multi-source (a `source:` list in the profile)".

Run: `go build ./...` and `goimports`-style check via `go vet ./test/`.
Expected: compiles; no unused-import error.

- [ ] **Step 3: Run the integration test**

Run: `go test ./test/ -run TestEcommerceCortexGolden -v 2>&1 | tail -20`
Expected: PASS. The CLI now builds from the profile and its output still matches the Cortex golden.

- [ ] **Step 4: Full suite + hygiene**

Run: `go test ./... && test -z "$(gofmt -l .)" && echo fmt-ok && go vet ./...`
Expected: all packages green; `fmt-ok`; vet silent.

- [ ] **Step 5: Commit**

```bash
git add test/integration_test.go
git commit -m "test: drive the CLI integration test from a semglot.yaml profile"
```

---

### Task 3: Rewrite README Usage and Configuration

**Files:**
- Modify: `README.md` (the `## Usage` section including its `### Options` subsection, and the `## Configuration` section)

**Interfaces:**
- Consumes: the profiles-only CLI. No code interfaces.

- [ ] **Step 1: Replace the `## Usage` section**

Replace everything from the `## Usage` heading up to (but not including) the `## Configuration` heading with:

````markdown
## Usage

`build` transpiles a source semantic layer into a target dialect. Builds are
configured with named **profiles** in `semglot.yaml`:

```yaml
# semglot.yaml
profiles:
  catalog:
    source: ./models
    target-dialect: snowflake-semantic-view
    output: ./out
    database: ANALYTICS
    model-name: catalog
```

Given a small dbt table:

```yaml
# models/schema.yml
models:
  - name: dim_product
    description: Product dimension.
    columns:
      - name: product_id
        description: Product surrogate key.
        data_type: number
        constraints:
          - type: primary_key
      - name: category
        description: Product category.
        data_type: varchar
      - name: title
        description: Product title.
        data_type: varchar
```

run the profile:

```sh
semglot build --profile catalog
```

semglot writes `out/definition.md` with the create statement:

```sql
create or replace semantic view CATALOG
	tables (
		DIM_PRODUCT as ANALYTICS.MAIN.DIM_PRODUCT primary key (PRODUCT_ID) comment='Product dimension.'
	)
	dimensions (
		DIM_PRODUCT.PRODUCT_ID as dim_product.PRODUCT_ID comment='Product surrogate key.',
		DIM_PRODUCT.CATEGORY as dim_product.CATEGORY comment='Product category.',
		DIM_PRODUCT.TITLE as dim_product.TITLE comment='Product title.'
	)
;
```

### Options

- `--profile <name>` selects a profile from the config. Required.
- `--config <path>` points at the config file. Defaults to `./semglot.yaml`.

Anything a target dialect can't express is reported rather than dropped silently
(e.g. a `NOTES.md` sidecar listing metrics that don't map).
````

- [ ] **Step 2: Replace the `## Configuration` section**

Replace everything from the `## Configuration` heading up to (but not including) the `## Dialect support` heading with:

````markdown
## Configuration

A profile is a complete, self-contained build. Every field:

```yaml
# semglot.yaml
profiles:
  view_prod:
    source: ./models              # required. dbt source dir, or a list of dirs
    source-dialect: dbt           # optional. default: dbt
    target-dialect: snowflake-semantic-view   # required
    output: ./out/view            # required. directory to write into
    database: ANALYTICS           # required for Snowflake targets (cortex, snowflake-semantic-view)
    schema: SEM                   # optional. default: MAIN
    model-name: catalog           # optional. default: source dir name
    description: Curated view.     # optional
```

- Each profile is independent: there is no shared or inherited config. Staging and
  production are two profiles that differ only in `database` and `output`.
- Omitted optional fields take defaults: `source-dialect` is `dbt`, `schema` is
  `MAIN`, and `model-name` is the source directory name.
- `build` fails clearly when the config is missing or unparseable, the `--profile`
  is not found, a required field (`source`, `target-dialect`, `output`) is absent,
  or a Snowflake target has no `database`.
````

- [ ] **Step 3: Verify the README example against the real tool**

Run:
```sh
mkdir -p /tmp/ss-readme/models && cd /tmp/ss-readme
cat > models/schema.yml <<'YML'
models:
  - name: dim_product
    description: Product dimension.
    columns:
      - name: product_id
        description: Product surrogate key.
        data_type: number
        constraints:
          - type: primary_key
      - name: category
        description: Product category.
        data_type: varchar
      - name: title
        description: Product title.
        data_type: varchar
YML
cat > semglot.yaml <<'YML'
profiles:
  catalog:
    source: ./models
    target-dialect: snowflake-semantic-view
    output: ./out
    database: ANALYTICS
    model-name: catalog
YML
go run github.com/benchouse/semglot/cmd/semglot build --profile catalog 2>&1  # or: go run <path>/cmd/semglot ...
cat out/definition.md
```
Expected: the emitted `create or replace semantic view CATALOG ...` block matches the SQL shown in the README Usage section (tables + dimensions for `dim_product`). Adjust the README if the real output differs. Then `cd` back to the repo.

- [ ] **Step 4: Confirm no em-dashes and commit**

Run: `test "$(grep -c '—' README.md)" = "0" && echo no-em-dash`
Expected: `no-em-dash`.

```bash
git add README.md
git commit -m "docs: rewrite README Usage and Configuration for profiles"
```

---

## Self-Review

**1. Spec coverage:**
- Config file with `profiles` map, self-contained profiles -> Task 1 (`configFile`, `profile`). ✅
- Field set `source`/`source-dialect`/`target-dialect`/`output`/`database`/`schema`/`model-name`/`description` -> Task 1 (`profile` struct tags). ✅
- `source` accepts scalar or list -> Task 1 (`sourcePaths.UnmarshalYAML`) + `TestLoadProfileSourceList`. ✅
- CLI `--profile` required, `--config` default `semglot.yaml` -> Task 1 (`buildCmd`) + `TestBuildCmdMissingProfile`. ✅
- Old flags removed -> Task 1 (rewritten `buildCmd`, no old flags; `sourceList` deleted). ✅
- Defaults (`source-dialect`=dbt, `schema`=MAIN, `model-name`=basename) -> Task 1 (`loadProfile`) + `TestLoadProfileDefaults`. ✅
- Validation (missing config/profile/required field, snowflake-needs-database) -> Task 1 (`loadProfile`) + `TestLoadProfileMissing*`, `TestLoadProfileSnowflakeRequiresDatabase`, `TestBuildCmdSnowflakeTargetRequiresDatabase`. ✅
- Integration test uses a profile -> Task 2. ✅
- README Usage + Configuration rewritten -> Task 3. ✅
- Cortex output unchanged -> Task 1 `TestBuildCmdEndToEnd` and Task 2 golden compare (byte-identical). ✅

**2. Placeholder scan:** No TBD/TODO. Every code step shows complete code; every run step shows the command and expected result. ✅

**3. Type consistency:** `buildSpec` fields (`Sources`, `SourceDialect`, `TargetDialect`, `Output`, `Database`, `Schema`, `ModelName`, `Description`), `loadProfile(configPath, name) (buildSpec, error)`, `defaultModelName([]string) string`, `profile`/`configFile`/`sourcePaths`, and `snowflakeTargets` are used identically across config.go, main.go, and both test files. `WithOptions(database, schema, name, description)` matches the current positional signature in `layer` (the `layer.Options` struct refactor is on an unmerged branch and is not used here). ✅
