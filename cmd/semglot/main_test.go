package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCmdEndToEnd(t *testing.T) {
	out := t.TempDir()
	code := buildCmd([]string{
		"--from", "dbt",
		"--reference", "../../layer/testdata/dbt",
		"--layer", "cortex",
		"--out", out,
		"--database", "ANALYTICS",
		"--schema", "MAIN",
		"--name", "eval_marts",
	})
	if code != 0 {
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

func TestBuildCmdMissingFlags(t *testing.T) {
	if code := buildCmd([]string{"--layer", "cortex"}); code != 2 {
		t.Fatalf("missing --reference/--out should exit 2, got %d", code)
	}
}

func TestBuildCmdSourceWithoutParser(t *testing.T) {
	// cortex is emit-only in v1; using it as --from must fail clearly.
	code := buildCmd([]string{"--from", "cortex", "--reference", "x", "--layer", "cortex", "--out", "y"})
	if code != 1 {
		t.Fatalf("cortex-as-source should exit 1, got %d", code)
	}
}
