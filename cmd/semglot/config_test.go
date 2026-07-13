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
