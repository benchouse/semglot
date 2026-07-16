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
	ViewSchema  string
	Name        string
	Description string
}

// configFile is the flat YAML shape read from --config.
type configFile struct {
	Database    string `yaml:"database"`
	Schema      string `yaml:"schema"`
	ViewSchema  string `yaml:"view_schema"`
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
		if cf.ViewSchema != "" {
			id.ViewSchema = cf.ViewSchema
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
	if set["view-schema"] {
		id.ViewSchema = flags.ViewSchema
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
