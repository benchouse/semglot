package dialect

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(ossie{}) }

// osiVersion is the spec version emitted and expected. Pinned, never computed.
const osiVersion = "0.2.0.dev0"

// ossie reads and writes the Apache Ossie Core Metadata Specification
// (github.com/apache/ossie, core-spec 0.2.0.dev0) — the semantic_model layer,
// not the separate ontology layer. The spec is a draft: "schema may change
// before 0.2.0 is released".
//
// ossie reads and emits Apache Ossie semantic models. Zero value is usable;
// the build command sets identity from the profile. Emit does not mutate m.
type ossie struct{ Database, Schema, ModelName, Description string }

func (ossie) Name() string { return "ossie" }

// ---- OSI YAML shapes ----

type osiFile struct {
	Version       string     `yaml:"version"`
	SemanticModel []osiModel `yaml:"semantic_model"`
}

type osiModel struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description,omitempty"`
	AIContext     *osiAIContext     `yaml:"ai_context,omitempty"`
	Datasets      []osiDataset      `yaml:"datasets"`
	Relationships []osiRelationship `yaml:"relationships,omitempty"`
	Metrics       []osiMetric       `yaml:"metrics,omitempty"`
}

type osiDataset struct {
	Name        string        `yaml:"name"`
	Source      string        `yaml:"source"`
	PrimaryKey  []string      `yaml:"primary_key,omitempty"`
	UniqueKeys  [][]string    `yaml:"unique_keys,omitempty"`
	Description string        `yaml:"description,omitempty"`
	AIContext   *osiAIContext `yaml:"ai_context,omitempty"`
	Fields      []osiField    `yaml:"fields,omitempty"`
}

type osiField struct {
	Name        string        `yaml:"name"`
	Expression  osiExpression `yaml:"expression"`
	Dimension   *osiDimension `yaml:"dimension,omitempty"`
	Label       string        `yaml:"label,omitempty"`
	Description string        `yaml:"description,omitempty"`
	DataType    string        `yaml:"datatype,omitempty"`
	AIContext   *osiAIContext `yaml:"ai_context,omitempty"`
}

type osiDimension struct {
	IsTime *bool `yaml:"is_time,omitempty"`
}

type osiRelationship struct {
	Name        string   `yaml:"name"`
	From        string   `yaml:"from"`
	To          string   `yaml:"to"`
	FromColumns []string `yaml:"from_columns"`
	ToColumns   []string `yaml:"to_columns"`
}

type osiMetric struct {
	Name        string        `yaml:"name"`
	Expression  osiExpression `yaml:"expression"`
	Description string        `yaml:"description,omitempty"`
	DataType    string        `yaml:"datatype,omitempty"`
	AIContext   *osiAIContext `yaml:"ai_context,omitempty"`
}

type osiExpression struct {
	Dialects []osiDialectExpr `yaml:"dialects"`
}

type osiDialectExpr struct {
	Dialect    string `yaml:"dialect"`
	Expression string `yaml:"expression"`
}

// osiAIContext is `string | object` in the schema. A bare string is treated as
// instructions.
type osiAIContext struct {
	Instructions string   `yaml:"instructions,omitempty"`
	Synonyms     []string `yaml:"synonyms,omitempty"`
	Examples     []string `yaml:"examples,omitempty"`
}

func (a *osiAIContext) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		a.Instructions = s
		return nil
	}
	type plain osiAIContext // avoid recursing into this method
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*a = osiAIContext(p)
	return nil
}

func (a *osiAIContext) synonyms() []string {
	if a == nil {
		return nil
	}
	return a.Synonyms
}

// examplesNote returns a note reporting a's Examples, or "" when there are
// none. The IR has no examples slot at any level (model, dataset, or field),
// so this is the only way that data survives parsing. subject names what the
// ai_context belongs to, e.g. `dataset "orders"`.
func (a *osiAIContext) examplesNote(subject string) string {
	if a == nil || len(a.Examples) == 0 {
		return ""
	}
	return fmt.Sprintf("%s ai_context.examples %v: no examples slot in the IR; dropped", subject, a.Examples)
}

// ---- data types ----

// osiTypes maps OSI's logical DataType enum to the neutral SQL type string the
// IR carries (dialects like dbt record raw SQL types, and emitters such as
// cortex pass them straight through). Integer and Decimal map to distinct SQL
// names rather than collapsing to `number`, so the round-trip is stable.
var osiTypes = map[string]string{
	"String":     "varchar",
	"Integer":    "integer",
	"Decimal":    "decimal",
	"Float":      "float",
	"Boolean":    "boolean",
	"Date":       "date",
	"Time":       "time",
	"DateTime":   "timestamp",
	"DateTimeTz": "timestamp_tz",
}

// irTypes is the reverse map, extended with the SQL spellings other dialects
// emit for the same logical type.
var irTypes = map[string]string{
	"varchar": "String", "char": "String", "text": "String", "string": "String",
	"integer": "Integer", "int": "Integer", "bigint": "Integer", "smallint": "Integer",
	"decimal": "Decimal", "numeric": "Decimal",
	"float": "Float", "double": "Float", "real": "Float",
	"boolean": "Boolean", "bool": "Boolean",
	"date":      "Date",
	"time":      "Time",
	"timestamp": "DateTime", "datetime": "DateTime", "timestamp_ntz": "DateTime",
	"timestamp_tz": "DateTimeTz", "timestamptz": "DateTimeTz", "timestamp_ltz": "DateTimeTz",
}

// osiToIRType converts an OSI datatype to a SQL type string, or "" when the
// value is unknown or absent (including the deliberate Opaque case).
func osiToIRType(t string) string { return osiTypes[t] }

// irToOSIType converts a SQL type string to an OSI DataType, or "" when it is
// not in the portable vocabulary. The spec directs omitting datatype when
// unknown, so Opaque is never used as a catch-all.
func irToOSIType(t string) string {
	return irTypes[strings.ToLower(strings.TrimSpace(t))]
}

// osiTemporal reports whether an OSI datatype makes is_time default to true.
func osiTemporal(t string) bool {
	switch t {
	case "Date", "Time", "DateTime", "DateTimeTz":
		return true
	}
	return false
}

// isTime resolves a field's temporal role per spec: an explicit dimension.is_time
// always wins; when unset it defaults to true for a temporal datatype.
func (f osiField) isTime() bool {
	if f.Dimension != nil && f.Dimension.IsTime != nil {
		return *f.Dimension.IsTime
	}
	return osiTemporal(f.DataType)
}

// pickExpression selects the expression to read. ANSI_SQL wins when present.
// Otherwise a lone entry is used silently — Ossie's own Databricks fixtures are
// DATABRICKS-only, and noting every field there would be pure noise — while a
// choice among several non-ANSI dialects uses the first and returns a note.
func pickExpression(e osiExpression) (expr string, ok bool, note string) {
	if len(e.Dialects) == 0 {
		return "", false, ""
	}
	for _, d := range e.Dialects {
		if strings.EqualFold(d.Dialect, "ANSI_SQL") {
			return d.Expression, true, ""
		}
	}
	first := e.Dialects[0]
	if len(e.Dialects) == 1 {
		return first.Expression, true, ""
	}
	return first.Expression, true, fmt.Sprintf("no ANSI_SQL expression; read the %s dialect", first.Dialect)
}

// ---- Parse ----

// Parse reads *.yml and *.yaml from each source directory (non-recursive) and
// merges every semantic_model entry into one IR model. Files with no
// semantic_model key are skipped, so a mixed directory works.
func (o ossie) Parse(sources ...string) (*ir.Model, error) {
	out := &ir.Model{}
	for _, dir := range sources {
		var paths []string
		for _, pat := range []string{"*.yml", "*.yaml"} {
			matches, err := filepath.Glob(filepath.Join(dir, pat))
			if err != nil {
				return nil, err
			}
			paths = append(paths, matches...)
		}
		sort.Strings(paths) // deterministic merge order
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			var f osiFile
			if err := yaml.Unmarshal(b, &f); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if len(f.SemanticModel) == 0 {
				continue // not an OSI document
			}
			for _, sm := range f.SemanticModel {
				mergeModel(out, sm)
			}
		}
	}
	return out, nil
}

// mergeModel folds one OSI semantic_model entry into out.
func mergeModel(out *ir.Model, sm osiModel) {
	if sm.AIContext != nil && sm.AIContext.Instructions != "" {
		out.Notes = append(out.Notes, sm.AIContext.Instructions)
	}
	// Model-level ai_context.synonyms has no home in the IR (unlike dataset-
	// and field-level synonyms, which map onto ir.Table.Synonyms and
	// ir.Field.Synonyms): note it rather than drop it silently.
	if syn := sm.AIContext.synonyms(); len(syn) > 0 {
		out.Notes = append(out.Notes,
			fmt.Sprintf("model %q ai_context.synonyms %v: no model-level synonym slot in the IR; dropped", sm.Name, syn))
	}
	if note := sm.AIContext.examplesNote(fmt.Sprintf("model %q", sm.Name)); note != "" {
		out.Notes = append(out.Notes, note)
	}
	for _, ds := range sm.Datasets {
		t := ir.Table{
			Name:        ds.Name,
			Description: ds.Description,
			Synonyms:    ds.AIContext.synonyms(),
			PrimaryKey:  ds.PrimaryKey,
		}
		// unique_keys has no IR slot; note it so it isn't dropped silently.
		if len(ds.UniqueKeys) > 0 {
			out.Notes = append(out.Notes,
				fmt.Sprintf("dataset %q unique_keys %v: no unique-key slot in the IR; dropped", ds.Name, ds.UniqueKeys))
		}
		if note := ds.AIContext.examplesNote(fmt.Sprintf("dataset %q", ds.Name)); note != "" {
			out.Notes = append(out.Notes, note)
		}
		for _, f := range ds.Fields {
			expr, ok, note := pickExpression(f.Expression)
			if !ok {
				out.Notes = append(out.Notes,
					fmt.Sprintf("field %q on dataset %q has no expression; skipped", f.Name, ds.Name))
				continue
			}
			if note != "" {
				out.Notes = append(out.Notes, fmt.Sprintf("field %q on dataset %q: %s", f.Name, ds.Name, note))
			}
			if note := f.AIContext.examplesNote(fmt.Sprintf("field %q on dataset %q", f.Name, ds.Name)); note != "" {
				out.Notes = append(out.Notes, note)
			}
			// OSI's field-level label has no IR field-level slot, so it folds
			// into the description as visible prose rather than vanishing.
			desc := f.Description
			if f.Label != "" {
				desc = appendClause(desc, "Display name: "+f.Label+".")
			}
			fld := ir.Field{
				Name:        f.Name,
				Description: desc,
				DataType:    osiToIRType(f.DataType),
				Expr:        expr,
				Synonyms:    f.AIContext.synonyms(),
			}
			if f.isTime() {
				t.TimeDimensions = append(t.TimeDimensions, fld)
			} else {
				t.Dimensions = append(t.Dimensions, fld)
			}
		}
		out.Tables = append(out.Tables, t)
	}
}
