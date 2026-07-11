package layer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DataDog/go-sqllexer"
	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(supersimple{}) }

// supersimple emits one supersimple config YAML per model. Zero value usable;
// the build command sets Schema from --schema.
type supersimple struct {
	Schema string
}

func (supersimple) Name() string { return "supersimple" }

// WithOptions lets the CLI pass --schema (database/name/description are unused).
func (supersimple) WithOptions(database, schema, name, description string) Emitter {
	return supersimple{Schema: schema}
}

const ssHeader = "# yaml-language-server: $schema=https://assets.supersimple.io/configuration_schema/1.0.0/supersimple_configuration_schema.json\n"

type ssFile struct {
	Models  map[string]ssModel  `yaml:"models"`
	Metrics map[string]ssMetric `yaml:"metrics,omitempty"`
}
type ssModel struct {
	Name        string                `yaml:"name"`
	Table       string                `yaml:"table"`
	PrimaryKey  []string              `yaml:"primary_key,omitempty"`
	Description string                `yaml:"description,omitempty"`
	Properties  map[string]ssProperty `yaml:"properties,omitempty"`
	Relations   map[string]ssRelation `yaml:"relations,omitempty"`
}
type ssProperty struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
	Sql         string `yaml:"sql,omitempty"`
}
type ssRelation struct {
	Name         string         `yaml:"name"`
	Type         string         `yaml:"type"`
	ModelID      string         `yaml:"model_id"`
	JoinStrategy ssJoinStrategy `yaml:"join_strategy"`
}
type ssJoinStrategy struct {
	JoinKey string `yaml:"join_key"`
}
type ssMetric struct {
	Name        string        `yaml:"name"`
	ModelID     string        `yaml:"model_id"`
	Description string        `yaml:"description,omitempty"`
	Operations  []ssOperation `yaml:"operations,omitempty"`
	Aggregation ssAggregation `yaml:"aggregation"`
}
type ssAggregation struct {
	Type     string     `yaml:"type"`
	Key      string     `yaml:"key,omitempty"`
	Property *ssPropRef `yaml:"property,omitempty"`
}
type ssPropRef struct {
	Key  string `yaml:"key"`
	Name string `yaml:"name"`
}
type ssOperation struct {
	Operation  string `yaml:"operation"`
	Parameters any    `yaml:"parameters"`
}
type ssGroupAggregateParams struct {
	Groups       []any       `yaml:"groups"`
	Aggregations []ssAggSpec `yaml:"aggregations"`
}
type ssAggSpec struct {
	Type     string    `yaml:"type"`
	Key      string    `yaml:"key,omitempty"`
	Property ssPropRef `yaml:"property"`
}
type ssDeriveFieldParams struct {
	FieldName string      `yaml:"field_name"`
	Key       string      `yaml:"key"`
	Value     ssExprValue `yaml:"value"`
}
type ssExprValue struct {
	Expression string `yaml:"expression"`
	Version    string `yaml:"version"`
}

func (s supersimple) Emit(m *ir.Model, dir string) error {
	schema := s.Schema
	if schema == "" {
		schema = "MAIN"
	}
	// relationships grouped by parent (Right) table
	relsByParent := map[string][]ir.Relationship{}
	for _, r := range m.Relationships {
		relsByParent[r.Right] = append(relsByParent[r.Right], r)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	for _, t := range m.Tables {
		id := strings.ToUpper(t.Name)
		model := ssModel{
			Name:        prettify(t.Name),
			Table:       schema + "." + id,
			PrimaryKey:  upperAll(t.PrimaryKey),
			Description: t.Description,
			Properties:  map[string]ssProperty{},
		}
		addProp := func(f ir.Field, typ string) {
			col := strings.ToUpper(f.Expr)
			if _, ok := model.Properties[col]; ok {
				return
			}
			model.Properties[col] = ssProperty{Name: col, Type: typ, Description: f.Description}
		}
		for _, d := range t.Dimensions {
			addProp(d, ssType(d.DataType, d.Name, false))
		}
		for _, d := range t.TimeDimensions {
			addProp(d, ssType(d.DataType, d.Name, true))
		}
		for _, meas := range t.Measures {
			if !isIdent(meas.Expr) { // a compound expr is not a column
				continue
			}
			addProp(meas.Field, ssType(meas.DataType, meas.Name, false))
		}
		for _, r := range relsByParent[t.Name] {
			child := r.Left
			join := ""
			if len(r.Columns) > 0 {
				join = strings.ToUpper(r.Columns[0].Right)
			}
			if model.Relations == nil {
				model.Relations = map[string]ssRelation{}
			}
			model.Relations[slug(child)] = ssRelation{
				Name: prettify(child), Type: "hasMany", ModelID: strings.ToUpper(child),
				JoinStrategy: ssJoinStrategy{JoinKey: join},
			}
		}

		// cols is this table's column set (lowercased), used to wrap column
		// references in a compound measure's property.sql.
		cols := map[string]bool{}
		for _, d := range t.Dimensions {
			cols[strings.ToLower(d.Expr)] = true
		}
		for _, d := range t.TimeDimensions {
			cols[strings.ToLower(d.Expr)] = true
		}
		for _, meas := range t.Measures {
			if isIdent(meas.Expr) {
				cols[strings.ToLower(meas.Expr)] = true
			}
		}

		// Resolve every simple metric to its supersimple (type,key), synthesizing
		// a computed property.sql for compound-measure metrics. Do this before
		// creating the file so the synthesized properties are on the model, and
		// before emitting ratios so operands resolve.
		simpleAgg := map[string]aggRef{}
		for _, mt := range t.Metrics {
			if mt.Kind != "simple" {
				continue
			}
			key := strings.ToUpper(mt.Column)
			if !isIdent(mt.Column) { // compound measure -> synthesized sql property
				key = strings.ToUpper(mt.Name)
				model.Properties[key] = ssProperty{Name: key, Type: "Number", Sql: toPropertySQL(mt.Column, cols)}
			}
			simpleAgg[mt.Name] = aggRef{typ: mapAgg(mt.Agg), key: key}
		}

		file := ssFile{Models: map[string]ssModel{id: model}}
		metricName := func(mt ir.Metric) string {
			if mt.Label != "" {
				return mt.Label
			}
			return mt.Name
		}
		addMetric := func(name string, sm ssMetric) {
			if file.Metrics == nil {
				file.Metrics = map[string]ssMetric{}
			}
			file.Metrics[name] = sm
		}
		for _, mt := range t.Metrics {
			switch {
			case mt.Kind == "simple":
				ar := simpleAgg[mt.Name]
				addMetric(mt.Name, ssMetric{
					Name: metricName(mt), ModelID: id, Description: mt.Description,
					Aggregation: ssAggregation{Type: ar.typ, Key: ar.key},
				})
			case mt.Kind == "ratio":
				num, okN := simpleAgg[mt.Numerator]
				den, okD := simpleAgg[mt.Denominator]
				if !okN || !okD { // operands not both same-table simple metrics
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: operands span tables or are not simple aggregations — deferred to a later iteration", mt.Name))
					continue
				}
				addMetric(mt.Name, ratioMetric(id, mt.Name, metricName(mt), mt.Description, num, den))
			default:
				m.Notes = append(m.Notes, fmt.Sprintf("metric %q not emitted: unsupported kind %q", mt.Name, mt.Kind))
			}
		}

		var buf bytes.Buffer
		buf.WriteString(ssHeader)
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(file); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, id+".yaml"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// prettify turns a model name into a display label: strip fct_/dim_/obt_/stg_
// prefix, spaces for underscores, capitalize. "fct_order_lines" -> "Order lines".
func prettify(name string) string {
	s := stripPrefix(name)
	s = strings.ReplaceAll(s, "_", " ")
	if s == "" {
		return name
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// slug is the relation key: prefix-stripped, underscores kept. "fct_order_lines" -> "order_lines".
func slug(name string) string {
	if s := stripPrefix(name); s != "" {
		return s
	}
	return name
}

func stripPrefix(s string) string {
	for _, p := range []string{"fct_", "dim_", "obt_", "stg_"} {
		if strings.HasPrefix(s, p) {
			return strings.TrimPrefix(s, p)
		}
	}
	return s
}

// ssType maps to supersimple's property type vocabulary, preferring a real dbt
// data_type and falling back to a name/role heuristic. Enum/format not emitted.
func ssType(dbtType, name string, isTime bool) string {
	if dbtType != "" {
		return ssMapType(dbtType)
	}
	if isTime {
		return "Date"
	}
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "Boolean"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "Number"
	default:
		return "String"
	}
}

func ssMapType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "int", "integer", "bigint", "smallint":
		return "Integer"
	case "number":
		return "Number"
	case "float", "double", "double precision", "real", "numeric", "decimal":
		return "Float"
	case "boolean", "bool":
		return "Boolean"
	case "date":
		return "Date"
	case "timestamp", "datetime", "timestamp_ntz", "timestamp_tz", "timestamp_ltz":
		return "Date"
	case "varchar", "text", "string", "char", "character varying":
		return "String"
	default:
		return "String"
	}
}

// mapAgg maps a dbt aggregation to supersimple's aggregation type. dbt and
// supersimple share the same names (sum, count, count_distinct, avg, min, max);
// dbt's "average" is the only alias to normalize.
func mapAgg(agg string) string {
	a := strings.ToLower(agg)
	if a == "average" {
		return "avg"
	}
	return a
}

// aggRef is a resolved supersimple aggregation for a simple metric.
type aggRef struct{ typ, key string }

// ratioMetric builds a same-table ratio as a groupAggregate -> deriveField ->
// first pipeline. NOTE: the whole-set groupAggregate shape and the deriveField
// expression grammar are provisional pending live-supersimple validation.
func ratioMetric(modelID, key, name, desc string, num, den aggRef) ssMetric {
	return ssMetric{
		Name: name, ModelID: modelID, Description: desc,
		Operations: []ssOperation{
			{Operation: "groupAggregate", Parameters: ssGroupAggregateParams{
				Groups: []any{},
				Aggregations: []ssAggSpec{
					{Type: num.typ, Key: num.key, Property: ssPropRef{Key: "_num", Name: "_num"}},
					{Type: den.typ, Key: den.key, Property: ssPropRef{Key: "_den", Name: "_den"}},
				},
			}},
			{Operation: "deriveField", Parameters: ssDeriveFieldParams{
				FieldName: name, Key: key,
				Value: ssExprValue{Expression: `prop("_num") / prop("_den")`, Version: "1"},
			}},
		},
		Aggregation: ssAggregation{Type: "first", Key: key, Property: &ssPropRef{Key: key, Name: name}},
	}
}

// toPropertySQL rewrites a compound measure expression into supersimple's
// property.sql form: each column identifier (a member of cols, lowercased) is
// wrapped in {braces}; keywords, numbers, string literals and functions are
// left untouched.
// e.g. "case when is_refunded then 1 else 0 end" (cols={is_refunded}) ->
//
//	"case when {is_refunded} then 1 else 0 end".
func toPropertySQL(expr string, cols map[string]bool) string {
	lx := sqllexer.New(expr)
	var b strings.Builder
	for {
		tok := lx.Scan()
		if tok.Type == sqllexer.EOF || tok.Type == sqllexer.ERROR {
			break
		}
		if tok.Type == sqllexer.IDENT && cols[strings.ToLower(tok.Value)] {
			b.WriteByte('{')
			b.WriteString(tok.Value)
			b.WriteByte('}')
		} else {
			b.WriteString(tok.Value)
		}
	}
	return b.String()
}
