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
	src, err := filepath.Abs("../../dialect/testdata/dbt")
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
	want, err := os.ReadFile("../../dialect/testdata/cortex/semantic_model.golden.yaml")
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
	src, err := filepath.Abs("../../dialect/testdata/dbt")
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
