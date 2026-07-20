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
	ViewSchema    string      `yaml:"view-schema"`
	ModelName     string      `yaml:"model-name"`
	Description   string      `yaml:"description"`
	// Timestamp pins the ISO 8601 instant stamped on emitted documents (okf).
	// Left empty, the build command derives one from the source's git history.
	Timestamp string `yaml:"timestamp"`
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
	ViewSchema    string
	ModelName     string
	Description   string
	Timestamp     string
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
		ViewSchema:    p.ViewSchema,
		ModelName:     p.ModelName,
		Description:   p.Description,
		Timestamp:     p.Timestamp,
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
