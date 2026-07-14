package layer

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(naoYaml{}) }

// naoYaml emits nao's semantic-model YAML (a model-global dimensions + metrics
// document). Zero value is usable; the build command sets identity from flags.
// Emit does not mutate m.
type naoYaml struct{ Database, Schema, ModelName, Description string }

func (naoYaml) Name() string { return "nao-yaml" }
func (naoYaml) WithOptions(database, schema, name, description string) Emitter {
	return naoYaml{database, schema, name, description}
}

type naoDoc struct {
	Dimensions []naoDim    `yaml:"dimensions,omitempty"`
	Metrics    []naoMetric `yaml:"metrics,omitempty"`
	Notes      string      `yaml:"notes,omitempty"`
}
type naoDim struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
}
type naoMetric struct {
	Name       string     `yaml:"name"`
	Definition string     `yaml:"definition,omitempty"`
	Type       string     `yaml:"type,omitempty"`
	Source     *naoSource `yaml:"source,omitempty"`
	Formula    string     `yaml:"formula,omitempty"`
	Grain      string     `yaml:"grain,omitempty"`
}
type naoSource struct {
	Table       string `yaml:"table"`
	Column      string `yaml:"column,omitempty"`
	Aggregation string `yaml:"aggregation,omitempty"`
}

func (n naoYaml) Emit(m *ir.Model, dir string) error {
	resolve := metricResolver(m)

	var doc naoDoc
	seen := map[string]bool{}
	addDim := func(f ir.Field, typ string) {
		if seen[f.Name] {
			return
		}
		seen[f.Name] = true
		doc.Dimensions = append(doc.Dimensions, naoDim{Name: f.Name, Type: typ,
			Description: appendClause(f.Description, enumClause(f.Enum))})
	}
	notes := slices.Clone(m.Notes)
	for _, t := range m.Tables {
		for _, d := range t.Dimensions {
			addDim(d, "categorical")
		}
		for _, d := range t.TimeDimensions {
			addDim(d, "date")
		}
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				notes = append(notes, mt.Name+": "+reason)
				continue
			}
			nm := naoMetric{Name: mt.Name, Definition: mt.Description, Grain: mt.Grain}
			if agg, ok := mt.Def.(ir.Agg); ok {
				if col, ok := agg.Arg.(ir.Col); ok && agg.Filter == nil {
					nm.Source = &naoSource{Table: agg.Table, Column: col.Name, Aggregation: strings.ToUpper(agg.Func)}
					doc.Metrics = append(doc.Metrics, nm)
					continue
				}
			}
			// compound agg, ratio, derived, filtered → a derived formula
			nm.Type = "derived"
			nm.Source = &naoSource{Table: t.Name}
			nm.Formula = renderSQL(mt.Def, resolve)
			doc.Metrics = append(doc.Metrics, nm)
		}
	}
	if len(notes) > 0 {
		doc.Notes = strings.Join(notes, "\n")
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "semantic.yaml"), buf.Bytes(), 0o644)
}
