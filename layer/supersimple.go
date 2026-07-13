package layer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
type ssRelationAggregateParams struct {
	Relation     ssRelationRef `yaml:"relation"`
	Aggregations []ssAggSpec   `yaml:"aggregations"`
}
type ssRelationRef struct {
	Key string `yaml:"key"`
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

	type tableState struct {
		id      string
		model   ssModel
		metrics map[string]ssMetric
	}
	states := map[string]*tableState{}
	var order []string

	// metric name -> its resolved simple aggregation + owning table (global so
	// ratio operands resolve across tables).
	type simpleInfo struct{ table, typ, key string }
	global := map[string]simpleInfo{}

	// Phase 1: build each model (properties incl. synthesized compound property.sql,
	// and relations) and register its simple metrics.
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
			if !isIdent(meas.Expr) {
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
		for _, mt := range t.Metrics {
			if mt.Kind != "simple" {
				continue
			}
			key := strings.ToUpper(mt.Column)
			if !isIdent(mt.Column) {
				key = strings.ToUpper(mt.Name)
				for {
					if _, taken := model.Properties[key]; !taken {
						break
					}
					key += "_EXPR"
				}
				model.Properties[key] = ssProperty{Name: key, Type: "Number", Sql: toPropertySQL(mt.Column, cols)}
			}
			global[mt.Name] = simpleInfo{table: t.Name, typ: mapAgg(mt.Agg), key: key}
		}

		states[t.Name] = &tableState{id: id, model: model, metrics: map[string]ssMetric{}}
		order = append(order, t.Name)
	}

	// Phase 2: assign every metric to a file.
	metricName := func(mt ir.Metric) string {
		if mt.Label != "" {
			return mt.Label
		}
		return mt.Name
	}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			switch {
			case mt.Kind == "simple":
				si := global[mt.Name]
				st := states[si.table]
				st.metrics[mt.Name] = ssMetric{
					Name: metricName(mt), ModelID: st.id, Description: mt.Description,
					Aggregation: ssAggregation{Type: si.typ, Key: si.key},
				}
			case mt.Kind == "ratio":
				num, okN := global[mt.Numerator]
				den, okD := global[mt.Denominator]
				if !okN || !okD {
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: operand(s) are not a simple aggregation", mt.Name))
					continue
				}
				if num.table == den.table {
					st := states[num.table]
					st.metrics[mt.Name] = ratioMetric(st.id, mt.Name, metricName(mt), mt.Description,
						aggRef{typ: num.typ, key: num.key}, aggRef{typ: den.typ, key: den.key})
					continue
				}
				parent, relKey, child, ok := findParentRelation(m, num.table, den.table)
				if !ok {
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: operand tables %q and %q are not directly related", mt.Name, num.table, den.table))
					continue
				}
				childInfo := num
				if den.table == child {
					childInfo = den
				}
				if childInfo.typ != "sum" && childInfo.typ != "count" {
					m.Notes = append(m.Notes, fmt.Sprintf("metric %q (ratio) not emitted: child operand aggregation %q does not compose across the relation", mt.Name, childInfo.typ))
					continue
				}
				states[parent].metrics[mt.Name] = crossRatioMetric(states[parent].id, mt.Name, relKey, metricName(mt), mt.Description,
					crossOperand{onBase: num.table == parent, aggType: num.typ, key: num.key},
					crossOperand{onBase: den.table == parent, aggType: den.typ, key: den.key})
			default:
				m.Notes = append(m.Notes, fmt.Sprintf("metric %q not emitted: unsupported kind %q", mt.Name, mt.Kind))
			}
		}
	}

	// Phase 3: write per-table files (in table order), then NOTES.md.
	for _, name := range order {
		st := states[name]
		file := ssFile{Models: map[string]ssModel{st.id: st.model}}
		if len(st.metrics) > 0 {
			file.Metrics = st.metrics
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
		if err := os.WriteFile(filepath.Join(dir, st.id+".yaml"), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	if len(m.Notes) > 0 {
		var sb strings.Builder
		sb.WriteString("# Not transpiled to supersimple\n\n")
		for _, n := range m.Notes {
			sb.WriteString("- " + n + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte(sb.String()), 0o644); err != nil {
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

// findParentRelation returns the one-hop relationship connecting tables a and b
// (in either order): the parent (the Right/one side, which owns the hasMany
// relation), the relation key the emitter puts under the parent's relations
// (slug(child)), and the child (the Left/many side). ok=false if not directly related.
func findParentRelation(m *ir.Model, a, b string) (parent, relKey, child string, ok bool) {
	for _, r := range m.Relationships {
		if (r.Left == a && r.Right == b) || (r.Left == b && r.Right == a) {
			return r.Right, slug(r.Left), r.Left, true
		}
	}
	return "", "", "", false
}

// crossOperand describes one side of a cross-table ratio. onBase is true when the
// operand aggregates the parent (base) table directly; otherwise it aggregates the
// child table and must be pulled across the relation.
type crossOperand struct {
	onBase  bool
	aggType string
	key     string
}

// crossRatioMetric builds a cross-table ratio on the parent base: each child
// operand is pulled via relationAggregate (a per-parent value) then summed in the
// whole-set groupAggregate; each parent operand is aggregated directly there; the
// two named _num/_den columns are divided. Provisional pending live validation.
func crossRatioMetric(baseID, key, relKey, name, desc string, num, den crossOperand) ssMetric {
	var ops []ssOperation
	ga := ssGroupAggregateParams{Groups: []any{}}

	add := func(op crossOperand, propKey string) {
		if op.onBase {
			ga.Aggregations = append(ga.Aggregations, ssAggSpec{
				Type: op.aggType, Key: op.key, Property: ssPropRef{Key: propKey, Name: propKey},
			})
			return
		}
		rel := propKey + "_rel"
		ops = append(ops, ssOperation{Operation: "relationAggregate", Parameters: ssRelationAggregateParams{
			Relation:     ssRelationRef{Key: relKey},
			Aggregations: []ssAggSpec{{Type: op.aggType, Key: op.key, Property: ssPropRef{Key: rel, Name: rel}}},
		}})
		ga.Aggregations = append(ga.Aggregations, ssAggSpec{
			Type: "sum", Key: rel, Property: ssPropRef{Key: propKey, Name: propKey},
		})
	}
	add(num, "_num")
	add(den, "_den")
	ops = append(ops, ssOperation{Operation: "groupAggregate", Parameters: ga})
	ops = append(ops, ssOperation{Operation: "deriveField", Parameters: ssDeriveFieldParams{
		FieldName: name, Key: key, Value: ssExprValue{Expression: `prop("_num") / prop("_den")`, Version: "1"},
	}})
	return ssMetric{
		Name: name, ModelID: baseID, Description: desc, Operations: ops,
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
	var b strings.Builder
	for _, tok := range sqlTokens(expr) {
		if tok.typ == sqlIdent && cols[strings.ToLower(tok.val)] {
			b.WriteByte('{')
			b.WriteString(tok.val)
			b.WriteByte('}')
		} else {
			b.WriteString(tok.val)
		}
	}
	return b.String()
}
