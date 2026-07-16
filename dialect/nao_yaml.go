package dialect

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
func (naoYaml) WithOptions(o Options) Emitter {
	return naoYaml{o.Database, o.Schema, o.Name, o.Description}
}

type naoDoc struct {
	Dimensions []naoDim    `yaml:"dimensions,omitempty"`
	Metrics    []naoMetric `yaml:"metrics,omitempty"`
	Notes      string      `yaml:"notes,omitempty"`
}
type naoDim struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Description string   `yaml:"description,omitempty"`
	Values      []string `yaml:"values,omitempty"`
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

// dimRichness scores how much semantic detail a field carries, so the flat
// nao-yaml dimension dedup can keep the most informative variant of a
// name shared across tables. Enums dominate (they change what the agent can
// filter on), then synonyms, then a description.
func dimRichness(f ir.Field) int {
	r := 0
	if len(f.Enum) > 0 {
		r += 4
	}
	if len(f.Synonyms) > 0 {
		r += 2
	}
	if strings.TrimSpace(f.Description) != "" {
		r++
	}
	return r
}

func (n naoYaml) Emit(m *ir.Model, dir string) error {
	resolve := metricResolver(m)

	var doc naoDoc
	// nao-yaml is a model-global, flat dimension list, so a column name shared
	// across tables collapses to one dimension. Dedup by name, but keep the
	// RICHEST variant (enum > synonyms > description) rather than first-seen, so
	// an enum/synonym-bearing column is never displaced by a plainer duplicate.
	// (A truly ambiguous case — the same name with DIFFERENT enums in different
	// tables — still keeps only one; that is inherent to the flat format.)
	type dimSlot struct{ idx, rich int }
	seen := map[string]dimSlot{}
	addDim := func(f ir.Field, typ string) {
		// nao dimensions carry a structured `values:` list for categoricals; any
		// per-value meanings and the synonyms fold into the description, which nao
		// has no separate slot for.
		desc, values := enumValues(f.Description, f.Enum)
		desc = appendClause(desc, synonymClause(f.Synonyms))
		nd := naoDim{Name: f.Name, Type: typ, Description: desc, Values: values}
		rich := dimRichness(f)
		if slot, ok := seen[f.Name]; ok {
			if rich > slot.rich {
				doc.Dimensions[slot.idx] = nd
				seen[f.Name] = dimSlot{idx: slot.idx, rich: rich}
			}
			return
		}
		seen[f.Name] = dimSlot{idx: len(doc.Dimensions), rich: rich}
		doc.Dimensions = append(doc.Dimensions, nd)
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
