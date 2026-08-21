package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(supersimple{}) }

// supersimple emits one supersimple config YAML per model. Zero value usable;
// the build command sets Schema and TablePrefix from the profile.
type supersimple struct {
	Schema string
	// TablePrefix maps a logical table name to its PHYSICAL one. The IR carries
	// logical names (fct_orders); a warehouse may materialise them under a
	// prefix (ClickHouse marts__fct_orders), and `table:` must address the
	// physical object or every query fails to resolve.
	TablePrefix string
	// LowerCase emits identifiers as-is rather than upper-casing them. The
	// upper-case default suits Snowflake, where unquoted identifiers fold up;
	// ClickHouse is case-SENSITIVE, so MARTS__FCT_ORDERS does not exist.
	LowerCase bool
}

func (supersimple) Name() string { return "supersimple" }

// asSimpleAgg reports whether def is a single unfiltered aggregation, returning
// the supersimple aggregation type and the aggregated arg (a Col or Raw).
func asSimpleAgg(def ir.Expr) (typ string, arg ir.Expr, ok bool) {
	a, ok := def.(ir.Agg)
	if !ok || a.Filter != nil {
		return "", nil, false
	}
	return mapAgg(a.Func), a.Arg, true
}

// isRatioDef reports whether def is a division binary (numerator / denominator).
func isRatioDef(def ir.Expr) bool {
	bin, ok := def.(ir.Binary)
	return ok && bin.Op == "/"
}

// WithOptions lets the CLI pass the profile's schema (other identity fields are unused).
func (supersimple) WithOptions(o Options) Emitter {
	return supersimple{Schema: o.Schema, TablePrefix: o.TablePrefix, LowerCase: o.LowerCaseIdentifiers}
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

// ssJoinStrategy addresses a join. `join_key` is Supersimple's shorthand for
// "both sides share this column name"; when the foreign key and the primary key
// are named differently the two-sided form is REQUIRED, and the shorthand
// silently addresses the wrong column.
//
// The vendor's own discovery emits the two-sided form (dim_date joins
// fct_orders on join_key_on_base: DATE_DAY / join_key_on_related: ORDER_DATE).
// Emitting only the shorthand made `supersimple validate` reject three
// relations with "references unknown property 'date_day' on related model".
type ssJoinStrategy struct {
	JoinKey          string `yaml:"join_key,omitempty"`
	JoinKeyOnBase    string `yaml:"join_key_on_base,omitempty"`
	JoinKeyOnRelated string `yaml:"join_key_on_related,omitempty"`
}

// joinStrategy renders the shorthand when both sides agree and the explicit
// two-sided form when they differ.
func joinStrategy(base, related string) ssJoinStrategy {
	if base == related || related == "" {
		return ssJoinStrategy{JoinKey: base}
	}
	return ssJoinStrategy{JoinKeyOnBase: base, JoinKeyOnRelated: related}
}

type ssMetric struct {
	Name        string        `yaml:"name"`
	ModelID     string        `yaml:"model_id"`
	Description string        `yaml:"description,omitempty"`
	Operations  []ssOperation `yaml:"operations,omitempty"`
	Aggregation ssAggregation `yaml:"aggregation"`
}
type ssAggregation struct {
	Type string `yaml:"type"`
	Key  string `yaml:"key,omitempty"`
	// NOTE: the metric-level aggregation does NOT accept a `property` field
	// (supersimple validate rejects it) — that belongs only on the aggregations
	// inside groupAggregate/relationAggregate (ssAggSpec).
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

// Emit does not mutate m; it reads m.Notes and accumulates its own degrade
// notes locally before writing the combined text to NOTES.md.
// fold renders an identifier in the casing the target warehouse resolves.
// Snowflake folds unquoted identifiers to upper case, so upper is the safe
// default; ClickHouse is case-sensitive and MARTS__FCT_ORDERS does not exist.
func (s supersimple) fold(v string) string {
	if s.LowerCase {
		return v
	}
	return strings.ToUpper(v)
}

// foldAll is fold over a slice.
func (s supersimple) foldAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = s.fold(v)
	}
	return out
}

func (s supersimple) Emit(m *ir.Model, dir string) ([]string, error) {
	// An EXPLICIT empty schema is honoured (ClickHouse has a two-part namespace,
	// so "MAIN.marts__fct_orders" resolves to nothing); only an unset one
	// defaults.
	schema := s.Schema
	// relationships grouped by parent (Right) table
	relsByParent := map[string][]ir.Relationship{}
	for _, r := range m.Relationships {
		relsByParent[r.Right] = append(relsByParent[r.Right], r)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	type tableState struct {
		id      string
		model   ssModel
		metrics map[string]ssMetric
	}
	states := map[string]*tableState{}
	var order []string

	// metric name -> its resolved simple aggregation + owning table (global so
	// ratio operands resolve across tables). Keyed by metric name, which dbt
	// enforces to be project-unique, so cross-table lookups are unambiguous.
	type simpleInfo struct{ table, typ, key string }
	global := map[string]simpleInfo{}
	var degradeNotes []string

	// A supersimple relation is written on the PARENT (Right) model, from
	// inside the table loop below, so a relationship whose parent names no
	// table in the model is iterated by nothing: no relation, and with it no
	// relNameWarning and no relSynonymsWarning, since both are reported from
	// that same loop. Reported here instead — the mirror image of the
	// left-endpoint case dbt, lightdash and databricks-metric-view report,
	// which supersimple does not have because it hangs the relation off the
	// other end.
	models := map[string]bool{}
	for _, t := range m.Tables {
		models[t.Name] = true
	}
	for _, r := range m.Relationships {
		if !models[r.Right] {
			degradeNotes = append(degradeNotes, relNotEmittedWarning("supersimple", r, relEndpointMissing(r.Right,
				"a supersimple relation is written on the parent model the join points at")))
		}
	}

	// A supersimple ratio resolves each operand through `global`, which only
	// holds metrics it registered as simple aggregations, so an aggregate
	// inlined in the arithmetic cannot be an operand. Naming those aggregates
	// registers them like any other simple metric and the ratio path below runs
	// unchanged. Its non-division restriction still applies and still degrades
	// loudly: ssDegradeReason has no construct for a non-division Binary.
	hoist := hoistInlineAggs(m)

	// Phase 1: build each model (properties incl. synthesized compound property.sql,
	// and relations) and register its simple metrics.
	for _, t := range m.Tables {
		t.Metrics = hoist.metricsFor(t) // t is the range's own copy; m is untouched
		id := s.fold(t.Name)
		// Prefer the source dialect's own declared physical address. `table:`
		// holds ONE opaque string, unlike cortexBaseTable's separate
		// Database/Schema/Table fields, so a genuine table reference is used
		// verbatim regardless of its dot-part count (a two-part schema.table
		// resolves fine against the connection's default database). Only a
		// source that is a QUERY rather than a reference (which the OSI spec
		// permits) can't go here as-is — that falls back to the profile
		// reconstruction with a warning rather than pasting a query into
		// `table:` as if it were an address.
		table := s.TablePrefix + id
		if schema != "" {
			table = schema + "." + table
		}
		if t.Source != "" {
			if looksLikeQuery(t.Source) {
				degradeNotes = append(degradeNotes, querySourceWarning("supersimple", t.Name, t.Source))
			} else {
				table = t.Source
			}
		}
		model := ssModel{
			Name:        prettify(t.Name),
			Table:       table,
			PrimaryKey:  s.foldAll(t.PrimaryKey),
			Description: appendClause(t.Description, synonymClause(t.Synonyms)),
			Properties:  map[string]ssProperty{},
		}
		addProp := func(f ir.Field, typ string) {
			col := s.fold(f.Expr)
			if _, ok := model.Properties[col]; ok {
				return
			}
			model.Properties[col] = ssProperty{Name: col, Type: typ,
				Description: appendClause(f.Description, enumClause(f.Enum))}
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
			// r.Right is the PARENT (this model, the base); r.Left is the child.
			// Column pairs are (Left=child column, Right=parent column), so the
			// base key is the parent's and the related key is the child's.
			var baseKey, relatedKey string
			if len(r.Columns) > 0 {
				baseKey = s.fold(r.Columns[0].Right)
				relatedKey = s.fold(r.Columns[0].Left)
			}
			if model.Relations == nil {
				model.Relations = map[string]ssRelation{}
			}
			// Relations is a map keyed by relation slug, so a role-playing
			// dimension (two FKs from the same child, e.g. ship-to and bill-to
			// customer) would collide on slug(child) and silently lose one.
			// Disambiguate by the child's own left column(s), as the other
			// emitters do, leaving single-relationship keys unchanged.
			key, label := slug(child), prettify(child)
			if suffix := relRoleSuffix(m.Relationships, r); suffix != "" {
				key += "_" + slug(suffix)
				label += " (" + prettify(suffix) + ")"
			}
			// A supersimple relation identifies itself by the CHILD model it
			// pulls in: its key is a slug of that model's name and its Name is
			// that model's display label, both of which a consumer resolves back
			// to the model. The join's own declared name is a different thing
			// and has no slot here, so it (and its synonyms) are reported rather
			// than silently replaced by the child's name.
			for _, w := range []string{relNameWarning("supersimple", r), relSynonymsWarning("supersimple", r.Name, r)} {
				if w != "" {
					degradeNotes = append(degradeNotes, w)
				}
			}
			model.Relations[key] = ssRelation{
				Name: label, Type: "hasMany", ModelID: s.fold(child),
				JoinStrategy: joinStrategy(baseKey, relatedKey),
			}
		}

		for _, mt := range t.Metrics {
			typ, arg, ok := asSimpleAgg(mt.Def)
			if !ok {
				continue
			}
			var key string
			switch a := arg.(type) {
			case ir.Col:
				key = s.fold(a.Name)
			case ir.Raw:
				// raw.SQL is unqualified; wrap its columns and synthesize a
				// property keyed by the metric name, guarding against clobbering
				// a physical column that already owns that key.
				key = s.fold(mt.Name)
				for {
					if _, taken := model.Properties[key]; !taken {
						break
					}
					key += "_EXPR"
				}
				model.Properties[key] = ssProperty{Name: key, Type: "Number", Sql: toPropertySQL(a.SQL, colSet(a.Columns))}
			default:
				continue // arg is neither Col nor Raw (e.g. count(*)); not registerable
			}
			global[mt.Name] = simpleInfo{table: t.Name, typ: typ, key: key}
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
		t.Metrics = hoist.metricsFor(t) // as in phase 1; t is the range's own copy
		for _, mt := range t.Metrics {
			_, registered := global[mt.Name]
			switch {
			case registered:
				si := global[mt.Name]
				st := states[si.table]
				st.metrics[mt.Name] = ssMetric{
					Name: metricName(mt), ModelID: st.id, Description: mt.Description,
					Aggregation: ssAggregation{Type: si.typ, Key: si.key},
				}
			case isRatioDef(mt.Def):
				bin := mt.Def.(ir.Binary)
				numRef, okNR := bin.Left.(ir.Ref)
				denRef, okDR := bin.Right.(ir.Ref)
				var num, den simpleInfo
				var okN, okD bool
				if okNR {
					num, okN = global[numRef.Metric]
				}
				if okDR {
					den, okD = global[denRef.Metric]
				}
				if !okN || !okD {
					degradeNotes = append(degradeNotes, fmt.Sprintf("metric %q (ratio) not emitted: operand(s) are not a simple aggregation", mt.Name))
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
					degradeNotes = append(degradeNotes, fmt.Sprintf("metric %q (ratio) not emitted: operand tables %q and %q are not directly related", mt.Name, num.table, den.table))
					continue
				}
				childInfo := num
				if den.table == child {
					childInfo = den
				}
				if childInfo.typ != "sum" && childInfo.typ != "count" {
					degradeNotes = append(degradeNotes, fmt.Sprintf("metric %q (ratio) not emitted: child operand aggregation %q does not compose across the relation", mt.Name, childInfo.typ))
					continue
				}
				states[parent].metrics[mt.Name] = crossRatioMetric(states[parent].id, mt.Name, relKey, metricName(mt), mt.Description,
					crossOperand{onBase: num.table == parent, aggType: num.typ, key: num.key},
					crossOperand{onBase: den.table == parent, aggType: den.typ, key: den.key})
			default:
				degradeNotes = append(degradeNotes, fmt.Sprintf("metric %q not emitted: %s", mt.Name, ssDegradeReason(mt.Def)))
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
			return degradeNotes, err
		}
		if err := enc.Close(); err != nil {
			return degradeNotes, err
		}
		if err := os.WriteFile(filepath.Join(dir, st.id+".yaml"), buf.Bytes(), 0o644); err != nil {
			return degradeNotes, err
		}
	}
	allNotes := append(slices.Clone(m.Notes), degradeNotes...)
	if len(allNotes) > 0 {
		var sb strings.Builder
		sb.WriteString("# Not transpiled to supersimple\n\n")
		for _, n := range allNotes {
			sb.WriteString("- " + n + "\n")
		}
		if err := os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte(sb.String()), 0o644); err != nil {
			return degradeNotes, err
		}
	}
	return degradeNotes, nil
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
// data_type and falling back to a name/role heuristic. supersimple has no
// structured enum type, so enum values are folded into the property description.
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

// ssDegradeReason explains why a metric definition cannot be expressed in
// supersimple, for a specific NOTES.md line. The cumulative (Window) and
// conversion (Conversion) kinds are PROVISIONAL degradations (no live target).
func ssDegradeReason(def ir.Expr) string {
	switch d := def.(type) {
	case ir.Agg:
		if d.Filter != nil {
			return "filtered aggregation is not expressible in supersimple"
		}
		return "aggregation is not expressible in supersimple"
	case ir.Window:
		return "cumulative/windowed metric is not expressible in supersimple (provisional)"
	case ir.Conversion:
		return "conversion/funnel metric is not expressible in supersimple (provisional)"
	case ir.Binary:
		return "derived arithmetic metric is not expressible in supersimple"
	default:
		return "definition is neither a simple aggregation nor a ratio"
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
		Aggregation: ssAggregation{Type: "sum", Key: key},
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
		Aggregation: ssAggregation{Type: "sum", Key: key},
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
