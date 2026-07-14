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
	// TimeSpine, when present, marks a dbt MetricFlow date-spine model —
	// internal plumbing, not a business table. Presence alone is the signal.
	TimeSpine *dbtTimeSpine `yaml:"time_spine"`
}

type dbtTimeSpine struct {
	StandardGranularityColumn string `yaml:"standard_granularity_column"`
}

type dbtColumn struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description"`
	DataType    string          `yaml:"data_type"`
	Constraints []dbtConstraint `yaml:"constraints"`
	DataTests   []dbtTest       `yaml:"data_tests"`
	Tests       []dbtTest       `yaml:"tests"`
	Meta        dbtColumnMeta   `yaml:"meta"`
}

// dbtColumnMeta is a column's free-form dbt meta block. We read two keys:
// synonyms (alternate NL names) and enum (value→description for categoricals).
type dbtColumnMeta struct {
	Synonyms []string          `yaml:"synonyms"`
	Enum     map[string]string `yaml:"enum"`
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
	Name           string // scalar test name: "unique", "not_null", …
	Relationships  *dbtRelTest
	AcceptedValues *dbtAcceptedValues
}

// dbtAcceptedValues captures an accepted_values test. dbt 1.8+ nests args under
// `arguments:`; older projects put `values:` directly. Accept both.
type dbtAcceptedValues struct {
	Values    []string `yaml:"values"`
	Arguments *struct {
		Values []string `yaml:"values"`
	} `yaml:"arguments"`
}

// vals resolves the value list, preferring the nested `arguments:` form.
func (a *dbtAcceptedValues) vals() []string {
	if a.Arguments != nil && len(a.Arguments.Values) > 0 {
		return a.Arguments.Values
	}
	return a.Values
}

type dbtRelTest struct {
	To    string `yaml:"to"`
	Field string `yaml:"field"`
	// dbt 1.8+ nests a generic test's args under `arguments:`; older projects put
	// to/field directly. Accept both.
	Arguments *struct {
		To    string `yaml:"to"`
		Field string `yaml:"field"`
	} `yaml:"arguments"`
}

// relTo/relField resolve the target, preferring the nested `arguments:` form.
func (r *dbtRelTest) relTo() string {
	if r.Arguments != nil && r.Arguments.To != "" {
		return r.Arguments.To
	}
	return r.To
}

func (r *dbtRelTest) relField() string {
	if r.Arguments != nil && r.Arguments.Field != "" {
		return r.Arguments.Field
	}
	return r.Field
}

func (t *dbtTest) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode { // "unique", "not_null", …
		t.Name = value.Value
		return nil
	}
	var m struct {
		Relationships  *dbtRelTest        `yaml:"relationships"`
		AcceptedValues *dbtAcceptedValues `yaml:"accepted_values"`
	}
	if err := value.Decode(&m); err != nil {
		return err
	}
	t.Relationships = m.Relationships
	t.AcceptedValues = m.AcceptedValues
	return nil
}

// enumFromColumn computes a column's enum as the superset of its accepted_values
// test values and its meta.enum map, preserving accepted_values order first and
// appending any meta-only values sorted. Descriptions come from meta.enum.
// Returns nil when the column declares no categorical values either way.
func enumFromColumn(c dbtColumn) []ir.EnumValue {
	seen := map[string]bool{}
	var order []string
	for _, t := range append(append([]dbtTest{}, c.DataTests...), c.Tests...) {
		if t.AcceptedValues == nil {
			continue
		}
		for _, v := range t.AcceptedValues.vals() {
			if !seen[v] {
				seen[v] = true
				order = append(order, v)
			}
		}
	}
	metaOnly := make([]string, 0, len(c.Meta.Enum))
	for v := range c.Meta.Enum {
		if !seen[v] {
			seen[v] = true
			metaOnly = append(metaOnly, v)
		}
	}
	sort.Strings(metaOnly)
	order = append(order, metaOnly...)
	if len(order) == 0 {
		return nil
	}
	out := make([]ir.EnumValue, len(order))
	for i, v := range order {
		out[i] = ir.EnumValue{Value: v, Description: c.Meta.Enum[v]}
	}
	return out
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
	Name        string        `yaml:"name"`
	Label       string        `yaml:"label"`
	Type        string        `yaml:"type"`
	Description string        `yaml:"description"`
	Filter      string        `yaml:"filter"`
	TypeParams  dbtTypeParams `yaml:"type_params"`
}

type dbtTypeParams struct {
	Measure     string `yaml:"measure"`
	Numerator   string `yaml:"numerator"`
	Denominator string `yaml:"denominator"`
	// derived
	Expr    string         `yaml:"expr"`
	Metrics []dbtMetricRef `yaml:"metrics"`
	// cumulative
	Window string `yaml:"window"`
	Grain  string `yaml:"grain"`
	// conversion (nested, dbt-native shape)
	ConversionTypeParams *dbtConversionParams `yaml:"conversion_type_params"`
}

type dbtMetricRef struct {
	Name string `yaml:"name"`
}

type dbtConversionParams struct {
	BaseMeasure       string `yaml:"base_measure"`
	ConversionMeasure string `yaml:"conversion_measure"`
	Entity            string `yaml:"entity"`
	Window            string `yaml:"window"`
}

func (dbt) Parse(sources ...string) (*ir.Model, error) {
	var files []string
	for _, dir := range sources {
		matches, err := filepath.Glob(filepath.Join(dir, "*.yml"))
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
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

	// Drop dbt MetricFlow time-spine models — internal plumbing, not tables an
	// analyst queries — so they don't leak into the emitted context layers.
	kept := models[:0]
	for _, m := range models {
		if m.TimeSpine != nil {
			continue
		}
		kept = append(kept, m)
	}
	models = kept

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
	measureAgg := map[string]string{}
	measureCol := map[string]string{}
	grainByTable := map[string]string{}
	colsListByTable := map[string][]string{}
	// An entity name can be the primary entity of MORE THAN ONE table (e.g. both
	// dim_customer and fct_customer_ltv declare a primary "customer" on
	// customer_sk). Keep every owner so a foreign entity joins to all of them,
	// rather than silently dropping all but the last.
	primaryByEntity := map[string][]struct{ table, col string }{}

	for _, name := range order {
		md := modelByName[name]
		sm := semByName[name]

		colDesc := map[string]string{}
		colType := map[string]string{}
		colEnum := map[string][]ir.EnumValue{}
		colSyn := map[string][]string{}
		// cols is the set of this table's column names (lowercased), used to
		// qualify column references inside compound measure expressions.
		cols := map[string]bool{}
		for _, c := range md.Columns {
			colDesc[c.Name] = c.Description
			colType[c.Name] = c.DataType
			colEnum[c.Name] = enumFromColumn(c)
			colSyn[c.Name] = c.Meta.Synonyms
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
			return ir.Field{Name: fname, Expr: col, Description: colDesc[col], DataType: colType[col],
				Synonyms: colSyn[col], Enum: colEnum[col]}
		}

		for _, e := range sm.Entities {
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			t.Dimensions = append(t.Dimensions, field(col, col))
			if e.Type == "primary" {
				t.PrimaryKey = append(t.PrimaryKey, col)
				primaryByEntity[e.Name] = append(primaryByEntity[e.Name], struct{ table, col string }{name, col})
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
		}
		// Columns documented in models: but not surfaced by the semantic layer
		// become plain dimensions (this is the whole model for models:-only projects).
		for _, c := range md.Columns {
			if used[c.Name] {
				continue
			}
			t.Dimensions = append(t.Dimensions, ir.Field{Name: c.Name, Expr: c.Name, Description: c.Description, DataType: c.DataType,
				Synonyms: c.Meta.Synonyms, Enum: enumFromColumn(c)})
			used[c.Name] = true
		}

		for _, col := range pkFromModel(md) {
			if !contains(t.PrimaryKey, col) {
				t.PrimaryKey = append(t.PrimaryKey, col)
			}
		}

		// A column is often declared as BOTH an entity/dimension and the time
		// dimension — e.g. a foreign date key that is also the agg_time_dimension,
		// or a date-dimension's primary key that doubles as its time spine. Such a
		// column lands in t.Dimensions (via the entity/dimension loops) and again in
		// t.TimeDimensions, which emitters that flatten the two lists (e.g. the
		// semantic view) render twice. Keep it only as the time dimension; its
		// PK/FK/join role is preserved separately (PrimaryKey, Relationships).
		if len(t.TimeDimensions) > 0 {
			timeExpr := make(map[string]bool, len(t.TimeDimensions))
			for _, td := range t.TimeDimensions {
				timeExpr[strings.ToLower(td.Expr)] = true
			}
			kept := t.Dimensions[:0]
			for _, d := range t.Dimensions {
				if timeExpr[strings.ToLower(d.Expr)] {
					continue
				}
				kept = append(kept, d)
			}
			t.Dimensions = kept
		}

		grainByTable[name] = t.Grain
		colList := make([]string, 0, len(cols))
		for c := range cols {
			colList = append(colList, c)
		}
		sort.Strings(colList) // deterministic Raw.Columns
		colsListByTable[name] = colList

		tableIdx[name] = len(out.Tables)
		out.Tables = append(out.Tables, t)
	}

	// Relationships from semantic foreign entities...
	for _, sm := range semantic {
		for _, e := range sm.Entities {
			if e.Type != "foreign" {
				continue
			}
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			for _, p := range primaryByEntity[e.Name] {
				if p.table == sm.Name {
					continue
				}
				out.Relationships = append(out.Relationships, ir.Relationship{
					Left: sm.Name, Right: p.table, Columns: []ir.ColumnPair{{Left: col, Right: p.col}},
				})
			}
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
					Left: md.Name, Right: parseRef(test.Relationships.relTo()),
					Columns: []ir.ColumnPair{{Left: c.Name, Right: test.Relationships.relField()}},
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
	// Drop relationships whose endpoints aren't both tables in the model. An
	// empty right_table (e.g. an unparsed relationship test) or a target outside
	// the emitted set makes the whole semantic model invalid downstream — e.g.
	// Snowflake Cortex rejects the model with "Required field 'right_table'…".
	{
		kept := out.Relationships[:0]
		for _, r := range out.Relationships {
			_, lok := tableIdx[r.Left]
			ri, rok := tableIdx[r.Right]
			// Both endpoints must be tables in the model, and the referenced
			// (right) table must have a primary key — Snowflake Cortex rejects a
			// join to a PK-less table ("… has no primary key").
			if lok && rok && len(out.Tables[ri].PrimaryKey) > 0 {
				kept = append(kept, r)
			}
		}
		out.Relationships = kept
	}

	// Metrics: attach as structured Cortex metrics when we can resolve them to a
	// table; otherwise pass the metric through as a free-text note rather than
	// guessing a table. Simple metrics first so ratios can reference their exprs.
	metricDefs := map[string]ir.Expr{}
	metricTable := map[string]string{}
	attach := func(table string, mt ir.Metric) {
		metricDefs[mt.Name] = mt.Def
		metricTable[mt.Name] = table
		i := tableIdx[table]
		out.Tables[i].Metrics = append(out.Tables[i].Metrics, mt)
	}
	// tableForName resolves a metric or measure name to its owning table.
	tableForName := func(name string) (string, bool) {
		if t, ok := metricTable[name]; ok {
			return t, true
		}
		t, ok := measureTable[name]
		return t, ok
	}
	for _, m := range metrics { // simple first, so ratios can reference them
		if m.Type != "simple" {
			continue
		}
		meas := m.TypeParams.Measure
		table := measureTable[meas]
		if table == "" {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("measure %q not found in the parsed semantic models", meas)))
			continue
		}
		col := measureCol[meas]
		var arg ir.Expr
		if isIdent(col) {
			arg = ir.Col{Table: table, Name: col}
		} else {
			// Raw stays UNQUALIFIED; the Agg (carrying Table) qualifies it at render
			// time, and supersimple wraps it via toPropertySQL. Columns is the table's
			// column list so both know what to qualify/wrap.
			arg = ir.Raw{SQL: col, Columns: colsListByTable[table]}
		}
		agg := ir.Agg{Func: measureAgg[meas], Table: table, Arg: arg}
		if m.Filter != "" {
			// A dbt metric-level filter narrows the aggregation. Store it as the
			// Agg's Filter (a Col for a bare column, else an unqualified Raw the
			// renderer/emitter qualifies/wraps with this table's columns).
			agg.Filter = filterExpr(table, m.Filter, colsListByTable[table])
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description,
			Grain: grainByTable[table],
			Def:   agg,
		})
	}
	for _, m := range metrics { // ratio
		if m.Type != "ratio" {
			continue
		}
		_, okN := metricDefs[m.TypeParams.Numerator]
		_, okD := metricDefs[m.TypeParams.Denominator]
		table, okT := metricTable[m.TypeParams.Numerator]
		if !okN || !okD || !okT {
			out.Notes = append(out.Notes, metricNote(m, "one or more ratio operands could not be resolved to a metric"))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description,
			Grain: grainByTable[table],
			Def:   ir.Binary{Op: "/", Left: ir.Ref{Metric: m.TypeParams.Numerator}, Right: ir.Ref{Metric: m.TypeParams.Denominator}},
		})
	}
	for _, m := range metrics { // derived: arithmetic over metric refs + literals
		if m.Type != "derived" {
			continue
		}
		def, ok := parseDerivedExpr(m.TypeParams.Expr)
		if !ok {
			out.Notes = append(out.Notes, metricNote(m, "derived expression could not be parsed as arithmetic over metric refs"))
			continue
		}
		// Every referenced metric must resolve; the metric homes on the first
		// resolvable ref's table (dbt names are project-unique, so all refs share
		// a semantic space even if physically on different tables).
		table := ""
		resolved := true
		for _, r := range collectRefs(def) {
			if _, ok := metricDefs[r]; !ok {
				resolved = false
				break
			}
			if table == "" {
				table = metricTable[r]
			}
		}
		if !resolved || table == "" {
			out.Notes = append(out.Notes, metricNote(m, "one or more derived operands could not be resolved to a metric"))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description,
			Grain: grainByTable[table], Def: def,
		})
	}
	for _, m := range metrics { // cumulative -> Window (PROVISIONAL: no live target)
		if m.Type != "cumulative" {
			continue
		}
		base := m.TypeParams.Measure
		table, ok := tableForName(base)
		if !ok {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("cumulative base %q could not be resolved", base)))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Grain: grainByTable[table],
			Def: ir.Window{Base: ir.Ref{Metric: base}, Window: m.TypeParams.Window, Grain: m.TypeParams.Grain},
		})
	}
	for _, m := range metrics { // conversion -> Conversion (PROVISIONAL: no live target)
		if m.Type != "conversion" {
			continue
		}
		cp := m.TypeParams.ConversionTypeParams
		if cp == nil {
			out.Notes = append(out.Notes, metricNote(m, "conversion metric missing conversion_type_params"))
			continue
		}
		table, ok := tableForName(cp.BaseMeasure)
		if !ok {
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("conversion base %q could not be resolved", cp.BaseMeasure)))
			continue
		}
		attach(table, ir.Metric{
			Name: m.Name, Label: m.Label, Description: m.Description, Grain: grainByTable[table],
			Def: ir.Conversion{Base: ir.Ref{Metric: cp.BaseMeasure}, Conv: ir.Ref{Metric: cp.ConversionMeasure}, Entity: cp.Entity, Window: cp.Window},
		})
	}
	for _, m := range metrics { // genuinely unsupported types -> notes
		switch m.Type {
		case "simple", "ratio", "derived", "cumulative", "conversion":
		default:
			out.Notes = append(out.Notes, metricNote(m, fmt.Sprintf("unsupported metric type %q", m.Type)))
		}
	}

	return out, nil
}

// filterExpr builds the Expr for a dbt metric-level filter: a bare column
// becomes a Col (qualified at render time), anything compound an unqualified Raw
// carrying the owning table's columns so a renderer can qualify/wrap it.
func filterExpr(table, filter string, cols []string) ir.Expr {
	if isIdent(filter) {
		return ir.Col{Table: table, Name: filter}
	}
	return ir.Raw{SQL: filter, Columns: cols}
}

// parseDerivedExpr parses a dbt derived-metric expression (arithmetic over metric
// names and numeric literals: + - * / with precedence and parens) into an
// ir.Binary/Ref/Lit tree. ok=false if the expression is not cleanly parseable as
// such (the caller then degrades it to a note).
func parseDerivedExpr(expr string) (ir.Expr, bool) {
	var toks []sqlToken
	for _, tk := range sqlTokens(expr) {
		if tk.typ == sqlOther && strings.TrimSpace(tk.val) == "" {
			continue // drop whitespace
		}
		toks = append(toks, tk)
	}
	if len(toks) == 0 {
		return nil, false
	}
	p := &derivedParser{toks: toks}
	e := p.parseAddSub()
	if p.err || p.pos != len(p.toks) {
		return nil, false
	}
	return e, true
}

// derivedParser is a minimal recursive-descent parser over sqlTokens.
type derivedParser struct {
	toks []sqlToken
	pos  int
	err  bool
}

func (p *derivedParser) peek() (sqlToken, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return sqlToken{}, false
}

func (p *derivedParser) isOp(want ...string) (string, bool) {
	tk, ok := p.peek()
	if !ok || tk.typ != sqlOther {
		return "", false
	}
	for _, w := range want {
		if tk.val == w {
			return w, true
		}
	}
	return "", false
}

func (p *derivedParser) parseAddSub() ir.Expr {
	left := p.parseMulDiv()
	for {
		op, ok := p.isOp("+", "-")
		if !ok {
			return left
		}
		p.pos++
		left = ir.Binary{Op: op, Left: left, Right: p.parseMulDiv()}
	}
}

func (p *derivedParser) parseMulDiv() ir.Expr {
	left := p.parseFactor()
	for {
		op, ok := p.isOp("*", "/")
		if !ok {
			return left
		}
		p.pos++
		left = ir.Binary{Op: op, Left: left, Right: p.parseFactor()}
	}
}

func (p *derivedParser) parseFactor() ir.Expr {
	tk, ok := p.peek()
	if !ok {
		p.err = true
		return nil
	}
	switch {
	case tk.typ == sqlOther && tk.val == "(":
		p.pos++
		e := p.parseAddSub()
		if _, ok := p.isOp(")"); !ok {
			p.err = true
			return nil
		}
		p.pos++
		return e
	case tk.typ == sqlIdent:
		p.pos++
		return ir.Ref{Metric: tk.val}
	case tk.typ == sqlNumber:
		p.pos++
		return ir.Lit{Value: tk.val}
	default:
		p.err = true
		return nil
	}
}

// collectRefs returns the metric names referenced by a derived expression tree.
func collectRefs(e ir.Expr) []string {
	switch n := e.(type) {
	case ir.Ref:
		return []string{n.Metric}
	case ir.Binary:
		return append(collectRefs(n.Left), collectRefs(n.Right)...)
	default:
		return nil
	}
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
		// dbt idiom: a column tested both unique AND not_null is the primary key.
		var uniq, notNull bool
		for _, t := range append(append([]dbtTest{}, c.DataTests...), c.Tests...) {
			switch t.Name {
			case "unique":
				uniq = true
			case "not_null":
				notNull = true
			}
		}
		if uniq && notNull && !contains(pk, c.Name) {
			pk = append(pk, c.Name)
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
