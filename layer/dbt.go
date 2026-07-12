package layer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(dbt{}) }

// dbt parses a directory of dbt YAML. It merges two sources of truth: classic
// model properties (`models:` — table/column descriptions, data types, key and
// relationship constraints/tests) and the semantic layer (`semantic_models:` +
// `metrics:` — measures, aggregations, metrics). Either may be present alone.
type dbt struct{}

func (dbt) Name() string { return "dbt" }

// ---- raw YAML shapes ----

type dbtFile struct {
	Models         []dbtModel         `yaml:"models"`
	SemanticModels []dbtSemanticModel `yaml:"semantic_models"`
	Metrics        []dbtMetric        `yaml:"metrics"`
}

// classic model properties

type dbtModel struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	Constraints []dbtConstraint `yaml:"constraints"`
	Columns     []dbtColumn     `yaml:"columns"`
}

type dbtColumn struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	DataType    string          `yaml:"data_type"`
	Constraints []dbtConstraint `yaml:"constraints"`
	DataTests   []dbtTest       `yaml:"data_tests"`
	Tests       []dbtTest       `yaml:"tests"`
}

type dbtConstraint struct {
	Type      string   `yaml:"type"`       // primary_key, foreign_key, not_null, unique, ...
	Columns   []string `yaml:"columns"`    // model-level primary_key
	To        string   `yaml:"to"`         // foreign_key: ref('dim_x')
	ToColumns []string `yaml:"to_columns"` // foreign_key target columns
}

// dbtTest captures a column data test. Entries are either a bare string
// ("unique", "not_null") or a mapping ({relationships: {to, field}}); only the
// relationships form carries data we use.
type dbtTest struct {
	Relationships *dbtRelTest
}

type dbtRelTest struct {
	To    string `yaml:"to"`
	Field string `yaml:"field"`
}

func (t *dbtTest) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode { // "unique", "not_null" — ignored
		return nil
	}
	var m struct {
		Relationships *dbtRelTest `yaml:"relationships"`
	}
	if err := value.Decode(&m); err != nil {
		return err
	}
	t.Relationships = m.Relationships
	return nil
}

// semantic layer

type dbtSemanticModel struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Model       string `yaml:"model"`
	Defaults    struct {
		AggTimeDimension string `yaml:"agg_time_dimension"`
	} `yaml:"defaults"`
	Entities   []dbtEntity    `yaml:"entities"`
	Dimensions []dbtDimension `yaml:"dimensions"`
	Measures   []dbtMeasure   `yaml:"measures"`
}

type dbtEntity struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Expr string `yaml:"expr"`
}

type dbtDimension struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Expr string `yaml:"expr"`
}

type dbtMeasure struct {
	Name string `yaml:"name"`
	Agg  string `yaml:"agg"`
	Expr string `yaml:"expr"`
}

type dbtMetric struct {
	Name        string `yaml:"name"`
	Label       string `yaml:"label"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	TypeParams  struct {
		Measure     string `yaml:"measure"`
		Numerator   string `yaml:"numerator"`
		Denominator string `yaml:"denominator"`
	} `yaml:"type_params"`
}

func (dbt) Parse(dir string) (*ir.Model, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	var models []dbtModel
	var semantic []dbtSemanticModel
	var metrics []dbtMetric
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var df dbtFile
		if err := yaml.Unmarshal(b, &df); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		models = append(models, df.Models...)
		semantic = append(semantic, df.SemanticModels...)
		metrics = append(metrics, df.Metrics...)
	}

	// Ordered union of table names across both sources.
	var order []string
	seen := map[string]bool{}
	addName := func(n string) {
		if !seen[n] {
			seen[n] = true
			order = append(order, n)
		}
	}
	modelByName := map[string]dbtModel{}
	for _, m := range models {
		modelByName[m.Name] = m
		addName(m.Name)
	}
	semByName := map[string]dbtSemanticModel{}
	for _, s := range semantic {
		semByName[s.Name] = s
		addName(s.Name)
	}

	out := &ir.Model{}
	tableIdx := map[string]int{}
	measureTable := map[string]string{}
	measureAggExpr := map[string]string{}
	measureAgg := map[string]string{}
	measureCol := map[string]string{}
	primaryByEntity := map[string]struct{ table, col string }{}

	for _, name := range order {
		md := modelByName[name]
		sm := semByName[name]

		colDesc := map[string]string{}
		colType := map[string]string{}
		// cols is the set of this table's column names (lowercased), used to
		// qualify column references inside compound measure expressions.
		cols := map[string]bool{}
		for _, c := range md.Columns {
			colDesc[c.Name] = c.Description
			colType[c.Name] = c.DataType
			cols[strings.ToLower(c.Name)] = true
		}
		for _, e := range sm.Entities {
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			cols[strings.ToLower(col)] = true
		}
		for _, d := range sm.Dimensions {
			col := d.Expr
			if col == "" {
				col = d.Name
			}
			cols[strings.ToLower(col)] = true
		}
		// A measure defined directly over a bare column contributes that column
		// (it may not otherwise be an entity/dimension/documented column).
		for _, m := range sm.Measures {
			if isIdent(m.Expr) {
				cols[strings.ToLower(m.Expr)] = true
			}
		}

		t := ir.Table{Name: name}
		if md.Description != "" {
			t.Description = md.Description
		} else {
			t.Description = sm.Description
		}
		t.Grain = sm.Defaults.AggTimeDimension

		used := map[string]bool{}
		field := func(fname, col string) ir.Field {
			used[col] = true
			return ir.Field{Name: fname, Expr: col, Description: colDesc[col], DataType: colType[col]}
		}

		for _, e := range sm.Entities {
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			t.Dimensions = append(t.Dimensions, field(col, col))
			if e.Type == "primary" {
				t.PrimaryKey = append(t.PrimaryKey, col)
				primaryByEntity[e.Name] = struct{ table, col string }{name, col}
			}
		}
		for _, d := range sm.Dimensions {
			col := d.Expr
			if col == "" {
				col = d.Name
			}
			f := field(d.Name, col)
			if d.Type == "time" {
				t.TimeDimensions = append(t.TimeDimensions, f)
			} else {
				t.Dimensions = append(t.Dimensions, f)
			}
		}
		for _, m := range sm.Measures {
			f := field(m.Name, m.Expr)
			// A count aggregates cardinality, not the column's value, so the
			// underlying column's description would mislabel the fact — drop it.
			if a := strings.ToLower(m.Agg); a == "count" || a == "count_distinct" {
				f.Description = ""
			}
			t.Measures = append(t.Measures, ir.Measure{Field: f, Agg: m.Agg})
			measureTable[m.Name] = name
			measureAgg[m.Name] = m.Agg
			measureCol[m.Name] = m.Expr
			measureAggExpr[m.Name] = aggExpr(m.Agg, qualifyExpr(name, cols, m.Expr))
		}
		// Columns documented in models: but not surfaced by the semantic layer
		// become plain dimensions (this is the whole model for models:-only projects).
		for _, c := range md.Columns {
			if used[c.Name] {
				continue
			}
			t.Dimensions = append(t.Dimensions, ir.Field{Name: c.Name, Expr: c.Name, Description: c.Description, DataType: c.DataType})
			used[c.Name] = true
		}

		for _, col := range pkFromModel(md) {
			if !contains(t.PrimaryKey, col) {
				t.PrimaryKey = append(t.PrimaryKey, col)
			}
		}

		tableIdx[name] = len(out.Tables)
		out.Tables = append(out.Tables, t)
	}

	// Relationships from semantic foreign entities...
	for _, sm := range semantic {
		for _, e := range sm.Entities {
			if e.Type != "foreign" {
				continue
			}
			p, ok := primaryByEntity[e.Name]
			if !ok || p.table == sm.Name {
				continue
			}
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			out.Relationships = append(out.Relationships, ir.Relationship{
				Left: sm.Name, Right: p.table, Columns: []ir.ColumnPair{{Left: col, Right: p.col}},
			})
		}
	}
	// ...and from models: relationships tests / foreign-key constraints.
	for _, md := range models {
		for _, c := range md.Columns {
			for _, test := range append(append([]dbtTest{}, c.DataTests...), c.Tests...) {
				if test.Relationships == nil {
					continue
				}
				out.Relationships = append(out.Relationships, ir.Relationship{
					Left: md.Name, Right: parseRef(test.Relationships.To),
					Columns: []ir.ColumnPair{{Left: c.Name, Right: test.Relationships.Field}},
				})
			}
			for _, con := range c.Constraints {
				if con.Type != "foreign_key" || con.To == "" {
					continue
				}
				rightCol := c.Name
				if len(con.ToColumns) > 0 {
					rightCol = con.ToColumns[0]
				}
				out.Relationships = append(out.Relationships, ir.Relationship{
					Left: md.Name, Right: parseRef(con.To),
					Columns: []ir.ColumnPair{{Left: c.Name, Right: rightCol}},
				})
			}
		}
	}
	out.Relationships = dedupeRels(out.Relationships)

	// Metrics: attach as structured Cortex metrics when we can resolve them to a
	// table; otherwise pass the metric through as a free-text note rather than
	// guessing a table. Simple metrics first so ratios can reference their exprs.
	metricExpr := map[string]string{}
	metricTable := map[string]string{}
	attach := func(table string, mt ir.Metric) {
		metricExpr[mt.Name] = mt.Expr
		metricTable[mt.Name] = table
		i := tableIdx[table]
		out.Tables[i].Metrics = append(out.Tables[i].Metrics, mt)
	}
	for _, m := range metrics {
		if m.Type != "simple" {
			continue
		}
		meas := m.TypeParams.Measure
		expr, table := measureAggExpr[meas], measureTable[meas]
		if expr == "" || table == "" {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("measure %q not found in the parsed semantic models", meas)))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Expr: expr,
			Kind: "simple", Agg: measureAgg[meas], Table: table, Column: measureCol[meas],
		})
	}
	for _, m := range metrics {
		if m.Type != "ratio" {
			continue
		}
		num, okN := metricExpr[m.TypeParams.Numerator]
		den, okD := metricExpr[m.TypeParams.Denominator]
		table, okT := metricTable[m.TypeParams.Numerator]
		if !okN || !okD || !okT {
			out.Notes = append(out.Notes, metricNote(m, "one or more ratio operands could not be resolved to a metric"))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Expr: num + " / " + den,
			Kind: "ratio", Table: table, Numerator: m.TypeParams.Numerator, Denominator: m.TypeParams.Denominator,
		})
	}
	for _, m := range metrics {
		switch m.Type {
		case "simple", "ratio":
			// handled above
		default:
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("unsupported metric type %q", m.Type)))
		}
	}

	return out, nil
}

// metricNote renders a human/LLM-readable description of a dbt metric that could
// not be transpiled to a structured target metric, for passthrough into the
// target's free-text guidance.
func metricNote(m dbtMetric, reason string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "metric %q", m.Name)
	if m.Type != "" {
		fmt.Fprintf(&sb, " (%s)", m.Type)
	}
	if m.Description != "" {
		fmt.Fprintf(&sb, ": %s", m.Description)
	}
	switch m.Type {
	case "simple":
		fmt.Fprintf(&sb, " [measure: %s]", m.TypeParams.Measure)
	case "ratio":
		fmt.Fprintf(&sb, " [numerator: %s, denominator: %s]", m.TypeParams.Numerator, m.TypeParams.Denominator)
	}
	fmt.Fprintf(&sb, " — not transpiled: %s", reason)
	return sb.String()
}

// isIdent reports whether s is a single bare SQL identifier.
func isIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// qualifyExpr prefixes column references in a measure expression with the
// table name (col -> table.col), so the emitted metric SQL is unambiguous.
// It lexes the expression and rewrites only IDENT tokens that name a real
// column of this (single) table — string literals, numbers, SQL keywords and
// function names are left untouched. A bare column becomes table.column; a
// compound expression like "case when is_refunded then 1 else 0 end" becomes
// "case when table.is_refunded then 1 else 0 end".
func qualifyExpr(table string, cols map[string]bool, expr string) string {
	var b strings.Builder
	for _, tok := range sqlTokens(expr) {
		if tok.typ == sqlIdent && cols[strings.ToLower(tok.val)] {
			b.WriteString(table)
			b.WriteByte('.')
			b.WriteString(tok.val)
		} else {
			b.WriteString(tok.val)
		}
	}
	return b.String()
}

// aggExpr renders a neutral, lowercase aggregate expression over a qualified col.
func aggExpr(agg, col string) string {
	switch strings.ToLower(agg) {
	case "sum":
		return "sum(" + col + ")"
	case "count":
		return "count(" + col + ")"
	case "count_distinct":
		return "count(distinct " + col + ")"
	case "avg", "average":
		return "avg(" + col + ")"
	case "min":
		return "min(" + col + ")"
	case "max":
		return "max(" + col + ")"
	default:
		return strings.ToLower(agg) + "(" + col + ")"
	}
}

// pkFromModel collects primary-key columns from model-level and column-level
// contract constraints.
func pkFromModel(md dbtModel) []string {
	var pk []string
	for _, con := range md.Constraints {
		if con.Type == "primary_key" {
			pk = append(pk, con.Columns...)
		}
	}
	for _, c := range md.Columns {
		for _, con := range c.Constraints {
			if con.Type == "primary_key" {
				pk = append(pk, c.Name)
			}
		}
	}
	return pk
}

// parseRef extracts the model name from a dbt ref, e.g. ref('dim_customer') ->
// dim_customer. A plain name is returned unchanged.
func parseRef(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i >= 0 {
		s = s[i+1:]
		s = strings.TrimSuffix(strings.TrimSpace(s), ")")
	}
	return strings.Trim(strings.TrimSpace(s), "'\"")
}

func dedupeRels(rels []ir.Relationship) []ir.Relationship {
	seen := map[string]bool{}
	var out []ir.Relationship
	for _, r := range rels {
		key := r.Left + ">" + r.Right
		for _, c := range r.Columns {
			key += ":" + c.Left + "=" + c.Right
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
