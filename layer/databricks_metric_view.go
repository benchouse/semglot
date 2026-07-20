package layer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(databricksMetricView{}) }

// databricksMetricView emits Unity Catalog metric views — the YAML semantic
// layer Databricks AI/BI Genie grounds its answers on. It writes one
// <table>.yaml per IR table, rooted at that table with direct joins to the
// dimension tables it references — parity with sibling targets (cortex,
// snowflake-semantic-view, supersimple), which all emit every table. A table
// with metrics sources its measures from those metrics; a table with measures
// but no metrics (e.g. a wide OBT with dbt `measures:` that no metric
// references) renders its measures directly from the raw ir.Measure. A table
// left with zero measures either way (a pure dimension table, or one whose
// metrics all degraded) gets a synthesised row-count measure, since a metric
// view requires at least one. A table with zero dimensions is skipped
// entirely — a metric view also requires at least one, and there is no way to
// synthesise a meaningful one. Zero value is usable; the build command sets
// identity from flags. Emit does not mutate m. Database is the Unity Catalog
// catalog; Schema is the source-table schema.
type databricksMetricView struct{ Database, Schema, ModelName, Description string }

func (databricksMetricView) Name() string { return "databricks-metric-view" }

func (databricksMetricView) WithOptions(o Options) Emitter {
	return databricksMetricView{
		Database:    o.Database,
		Schema:      o.Schema,
		ModelName:   o.Name,
		Description: o.Description,
	}
}

// ---- metric-view YAML shapes ----

type dbxMetricView struct {
	Version  string       `yaml:"version"`
	Comment  string       `yaml:"comment,omitempty"`
	Source   string       `yaml:"source"`
	Joins    []dbxJoin    `yaml:"joins,omitempty"`
	Fields   []dbxField   `yaml:"fields,omitempty"`
	Measures []dbxMeasure `yaml:"measures,omitempty"`
}

type dbxField struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Comment     string   `yaml:"comment,omitempty"`
	DisplayName string   `yaml:"display_name,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

type dbxMeasure struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Comment     string   `yaml:"comment,omitempty"`
	DisplayName string   `yaml:"display_name,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

// dbxJoin marshals with a QUOTED "on" key. Databricks parses metric-view YAML
// as YAML 1.1, where a bare `on` key is the boolean true — which corrupts the
// join. yaml.v3 (YAML 1.2, where `on` is a plain string) would emit it bare, so
// build the mapping node explicitly and force the key's quote style.
type dbxJoin struct {
	Name   string
	Source string
	On     string
}

func (j dbxJoin) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	pairs := []struct {
		key, val string
		style    yaml.Style
	}{
		{"name", j.Name, 0},
		{"source", j.Source, 0},
		{"on", j.On, yaml.DoubleQuotedStyle},
	}
	for _, p := range pairs {
		kn := &yaml.Node{Kind: yaml.ScalarNode, Value: p.key, Style: p.style}
		vn := &yaml.Node{}
		if err := vn.Encode(p.val); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, kn, vn)
	}
	return n, nil
}

func (d databricksMetricView) Emit(m *ir.Model, dir string) error {
	catalog := strings.ToLower(d.Database)
	schema := strings.ToLower(d.Schema)
	if schema == "" {
		schema = "main"
	}
	resolve := metricResolver(m)

	// metricOwner maps each metric name to its owning table, so a derived metric
	// that references a metric on ANOTHER table (cross-grain) is detected and
	// degraded rather than inlined — inlining another grain's aggregate into this
	// view (over a fan-out join) would miscount.
	metricOwner := map[string]string{}
	tableByName := map[string]ir.Table{}
	for _, t := range m.Tables {
		tableByName[strings.ToLower(t.Name)] = t
		for _, mt := range t.Metrics {
			metricOwner[mt.Name] = t.Name
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, t := range m.Tables {
		mv := d.buildView(m, t, resolve, metricOwner, tableByName, catalog, schema)
		if len(mv.Fields) == 0 {
			continue // no dimensions: cannot form a valid metric view
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(mv); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		fname := strings.ToLower(t.Name) + ".yaml"
		if err := os.WriteFile(filepath.Join(dir, fname), buf.Bytes(), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (d databricksMetricView) buildView(m *ir.Model, t ir.Table, resolve func(string) (ir.Expr, bool), metricOwner map[string]string, tableByName map[string]ir.Table, catalog, schema string) dbxMetricView {
	mv := dbxMetricView{Version: "1.1", Source: dbxQualify(catalog, schema, t.Name)}
	var notes []string

	// Joins: relationships where this table is the LEFT (referencing) side.
	seenJoin := map[string]bool{}
	for _, r := range m.Relationships {
		if !strings.EqualFold(r.Left, t.Name) || len(r.Columns) == 0 {
			continue
		}
		joinName := strings.ToLower(r.Right)
		if seenJoin[joinName] {
			continue
		}
		seenJoin[joinName] = true
		var conds []string
		for _, cp := range r.Columns {
			conds = append(conds, "source."+strings.ToLower(cp.Left)+" = "+joinName+"."+strings.ToLower(cp.Right))
		}
		mv.Joins = append(mv.Joins, dbxJoin{
			Name:   joinName,
			Source: dbxQualify(catalog, schema, r.Right),
			On:     strings.Join(conds, " and "),
		})
	}

	// Measures: from metrics when the table has any (avoids emitting a
	// near-duplicate raw measure alongside its owning metric, e.g. orders_count
	// next to the orders metric). Degrade window/conversion and cross-grain
	// derived metrics to a note rather than emit SQL we cannot stand behind. A
	// table with measures but no metrics (e.g. a wide OBT with dbt `measures:`
	// that no metric references) renders its measures directly from the raw
	// ir.Measure, so it isn't dropped entirely.
	//
	// Built before fields (see the `seen` seeding below): Databricks requires
	// measure and dimension names to be unique across a metric view, and rejects
	// the view outright if they collide (METRIC_VIEW_INVALID_VIEW_DEFINITION:
	// "Measure and dimension names must be unique") — e.g. a source table with
	// both a precomputed `roas` column and a computed `roas` metric
	// (sum(attributed_revenue)/sum(spend)). Mirrors the identical fix in
	// snowflake-semantic-view (see its buildView), which resolves the same
	// collision by treating the computed metric as canonical.
	if len(t.Metrics) > 0 {
		for _, mt := range t.Metrics {
			if reason, degrade := dbxDegrade(mt); degrade {
				notes = append(notes, "metric "+mt.Name+": "+reason)
				continue
			}
			if dbxCrossGrain(mt.Def, metricOwner, t.Name) {
				notes = append(notes, "metric "+mt.Name+": references a measure on another table (cross-grain), not expressible as a single-grain metric-view measure")
				continue
			}
			mv.Measures = append(mv.Measures, dbxMeasure{
				Name: strings.ToLower(mt.Name), Expr: dbxStripSourceQualifier(renderSQL(mt.Def, resolve), t.Name),
				Comment: mt.Description, DisplayName: mt.Label, Synonyms: dbxCapSyn(mt.Synonyms),
			})
		}
	} else {
		for _, ms := range t.Measures {
			mv.Measures = append(mv.Measures, dbxMeasure{
				Name:     strings.ToLower(ms.Name),
				Expr:     aggExpr(ms.Agg, strings.ToLower(ms.Expr)),
				Comment:  ms.Description,
				Synonyms: dbxCapSyn(ms.Synonyms),
			})
		}
	}

	// A metric view must declare at least one measure. A table whose source
	// declares none (a pure dimension table), or whose metrics all degraded,
	// still carries useful dimensions, so give it a row count. count(1) asserts
	// no business semantics — synthesising SUM/AVG over a column would invent
	// meaning the source never declared. Run before the fields loop below so
	// row_count is also protected by the seen-seeding, in the unlikely case a
	// dimension is itself named row_count.
	if len(mv.Measures) == 0 {
		mv.Measures = append(mv.Measures, dbxMeasure{
			Name:    "row_count",
			Expr:    "count(1)",
			Comment: "Row count. Synthesised: the source declares no measures for this table.",
		})
	}

	// Fields: own dimensions (bare expr), then joined tables' dimensions
	// (prefixed expr). Names must be unique within a view; seed `seen` with the
	// measure names already emitted above so a dimension colliding with a
	// measure is dropped (the computed measure wins — see the comment on the
	// measures block). A joined field whose bare name collides is prefixed with
	// the join name, mirroring the snowflake-semantic-view dedup.
	seen := map[string]bool{}
	for _, ms := range mv.Measures {
		seen[strings.ToLower(ms.Name)] = true
	}
	for _, f := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
		name := strings.ToLower(f.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		mv.Fields = append(mv.Fields, dbxField{
			Name: name, Expr: strings.ToLower(f.Expr),
			Comment: dbxFieldComment(f), Synonyms: dbxCapSyn(f.Synonyms),
		})
	}
	for _, j := range mv.Joins {
		jt := tableByName[j.Name]
		for _, f := range append(append([]ir.Field{}, jt.Dimensions...), jt.TimeDimensions...) {
			name := strings.ToLower(f.Name)
			if seen[name] {
				name = j.Name + "_" + name
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			mv.Fields = append(mv.Fields, dbxField{
				Name: name, Expr: j.Name + "." + strings.ToLower(f.Expr),
				Comment: dbxFieldComment(f), Synonyms: dbxCapSyn(f.Synonyms),
			})
		}
	}

	var parts []string
	if t.Description != "" {
		parts = append(parts, t.Description)
	}
	if d.Description != "" {
		parts = append(parts, d.Description)
	}
	for _, n := range notes {
		parts = append(parts, "Note: "+n)
	}
	mv.Comment = strings.Join(parts, " ")
	return mv
}

// dbxStripSourceQualifier removes the source table's own name qualifier from a
// rendered measure expression. renderSQL qualifies a metric's columns with its
// owning table (e.g. "sum(fct_orders.order_gross)"), but in a metric view the
// source relation is the alias `source`, not its physical name — source columns
// are referenced bare, matching how fields are emitted. Cross-grain metrics
// (which reference another table) are degraded before rendering, so only the
// source qualifier can appear in a measure expr here.
func dbxStripSourceQualifier(expr, table string) string {
	return strings.ReplaceAll(expr, strings.ToLower(table)+".", "")
}

// dbxQualify builds a Unity Catalog table reference, three-part when a catalog
// is set, else two-part (keeps zero-value output well-formed).
func dbxQualify(catalog, schema, table string) string {
	t := strings.ToLower(table)
	if catalog == "" {
		return schema + "." + t
	}
	return catalog + "." + schema + "." + t
}

// dbxFieldComment folds a field's enum into its description, since a metric-view
// field has no per-value enum slot.
func dbxFieldComment(f ir.Field) string { return appendClause(f.Description, enumClause(f.Enum)) }

// dbxCapSyn caps synonyms at the metric-view limit of 10 per field/measure.
func dbxCapSyn(syn []string) []string {
	if len(syn) > 10 {
		return syn[:10]
	}
	return syn
}

// dbxDegrade reports metric kinds with no validated metric-view primitive
// (cumulative/conversion), matching the cortex/snowflake-semantic-view posture.
func dbxDegrade(mt ir.Metric) (string, bool) {
	switch mt.Def.(type) {
	case ir.Window:
		return "cumulative/windowed metric — no validated metric-view primitive (provisional)", true
	case ir.Conversion:
		return "conversion/funnel metric — no metric-view primitive (provisional)", true
	}
	return "", false
}

// dbxCrossGrain reports whether def references (directly or nested) a metric
// owned by a table other than self — i.e. a cross-grain derived metric that a
// single-source metric view cannot express without fan-out.
func dbxCrossGrain(def ir.Expr, owner map[string]string, self string) bool {
	found := false
	var walk func(ir.Expr)
	walk = func(e ir.Expr) {
		switch n := e.(type) {
		case ir.Ref:
			if o, ok := owner[n.Metric]; ok && !strings.EqualFold(o, self) {
				found = true
			}
		case ir.Binary:
			walk(n.Left)
			walk(n.Right)
		case ir.Agg:
			if n.Arg != nil {
				walk(n.Arg)
			}
			if n.Filter != nil {
				walk(n.Filter)
			}
		case ir.Window:
			walk(n.Base)
		case ir.Conversion:
			walk(n.Base)
			walk(n.Conv)
		}
	}
	walk(def)
	return found
}
