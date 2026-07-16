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
    view-schema: SEM_VIEW
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
		ViewSchema:    "SEM_VIEW",
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
