package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/benchouse/semglot/ir"
)

func init() { Register(snowflakeSemanticView{}) }

// snowflakeSemanticView emits a Snowflake CREATE SEMANTIC VIEW DDL wrapped in a
// markdown definition.md. Zero value is usable; the build command sets identity
// from flags. Emit does not mutate m.
// ViewSchema is the schema the semantic-view OBJECT is created in (e.g. SEM);
// its underlying TABLES still reference Schema (e.g. MAIN). ViewSchema falls
// back to Schema when empty.
type snowflakeSemanticView struct{ Database, Schema, ViewSchema, ModelName, Description string }

func (snowflakeSemanticView) Name() string { return "snowflake-semantic-view" }

func (snowflakeSemanticView) WithOptions(o Options) Emitter {
	return snowflakeSemanticView{
		Database:    o.Database,
		Schema:      o.Schema,
		ViewSchema:  o.ViewSchema,
		ModelName:   o.Name,
		Description: o.Description,
	}
}

func (s snowflakeSemanticView) Emit(m *ir.Model, dir string) error {
	view := strings.ToUpper(s.ModelName)
	if view == "" {
		view = "SEMANTIC_VIEW"
	}
	schema := s.Schema
	if schema == "" {
		schema = "MAIN"
	}
	// The view OBJECT is created in ViewSchema (falls back to the table schema);
	// its TABLES keep referencing `schema`. Qualify the view name only when a
	// database is set (matches table-ref qualification; keeps zero-value output
	// valid unqualified DDL).
	viewSchema := s.ViewSchema
	if viewSchema == "" {
		viewSchema = schema
	}
	qualifiedView := view
	if s.Database != "" {
		qualifiedView = fmt.Sprintf("%s.%s.%s", strings.ToUpper(s.Database), strings.ToUpper(viewSchema), view)
	}
	notes := slices.Clone(m.Notes)

	// metricTableOf maps each metric name to its owning table (uppercased) so a
	// derived metric can reference its component metrics by qualified name.
	metricTableOf := map[string]string{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			metricTableOf[mt.Name] = strings.ToUpper(t.Name)
		}
	}

	var tables, rels, dims, metrics []string
	for _, t := range m.Tables {
		u := strings.ToUpper(t.Name)
		line := fmt.Sprintf("%s as %s.%s.%s", u, s.Database, schema, u)
		if len(t.PrimaryKey) > 0 {
			line += fmt.Sprintf(" primary key (%s)", strings.Join(upperAll(t.PrimaryKey), ","))
		}
		if t.Description != "" {
			line += fmt.Sprintf(" comment='%s'", sqlQuote(t.Description))
		}
		tables = append(tables, line)
		// Snowflake requires expression names to be unique across a semantic
		// view's dimensions and metrics for a given table. Emit metrics first and
		// track the names used, so a dimension whose name collides with an emitted
		// metric (e.g. a precomputed `roas` column that is also defined as a
		// computed ROAS metric) is dropped — the computed metric is canonical.
		// `seen` also guards against two dimensions sharing a name.
		seen := map[string]bool{}
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				notes = append(notes, fmt.Sprintf("metric %q: %s", mt.Name, reason))
				continue
			}
			expr, ok := renderSVMetricDef(mt.Def, metricTableOf)
			if !ok {
				// A derived metric that inlines aggregates (SUM(x)/SUM(y)) or
				// references an unknown metric can't be a Snowflake semantic-view
				// metric ("a metric must directly refer to another aggregate-level
				// expression … without an aggregate"). Degrade to a note.
				notes = append(notes, fmt.Sprintf("metric %q: derived ratio not expressible as a semantic-view metric", mt.Name))
				continue
			}
			name := strings.ToUpper(mt.Name)
			ml := fmt.Sprintf("%s.%s as %s", u, name, expr)
			if mt.Description != "" {
				ml += fmt.Sprintf(" comment='%s'", sqlQuote(mt.Description))
			}
			metrics = append(metrics, ml)
			seen[name] = true
		}
		for _, d := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
			name := strings.ToUpper(d.Name)
			if seen[name] {
				continue // name already emitted (as a metric or earlier dimension)
			}
			seen[name] = true
			dl := fmt.Sprintf("%s.%s as %s.%s", u, name, strings.ToLower(t.Name), strings.ToUpper(d.Expr))
			if c := appendClause(d.Description, enumClause(d.Enum)); c != "" {
				dl += fmt.Sprintf(" comment='%s'", sqlQuote(c))
			}
			dims = append(dims, dl)
		}
	}
	for _, r := range m.Relationships {
		if len(r.Columns) == 0 {
			continue
		}
		var leftCols, rightCols []string
		for _, cp := range r.Columns {
			leftCols = append(leftCols, strings.ToUpper(cp.Left))
			rightCols = append(rightCols, strings.ToUpper(cp.Right))
		}
		rels = append(rels, fmt.Sprintf("%s_%s as %s(%s) references %s(%s)",
			strings.ToUpper(r.Left), strings.ToUpper(r.Right),
			strings.ToUpper(r.Left), strings.Join(leftCols, ","),
			strings.ToUpper(r.Right), strings.Join(rightCols, ",")))
	}

	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n\n", view)
	b.WriteString("This is a Snowflake **semantic view** — use this to understand the intended way to query and aggregate data.\n\n")
	if s.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", s.Description)
	}
	b.WriteString("## Definition\n\n```sql\n")
	fmt.Fprintf(&b, "create or replace semantic view %s\n", qualifiedView)
	writeSection(&b, "tables", tables)
	writeSection(&b, "relationships", rels)
	writeSection(&b, "dimensions", dims)
	writeSection(&b, "metrics", metrics)
	var commentParts []string
	if s.Description != "" {
		commentParts = append(commentParts, s.Description)
	}
	commentParts = append(commentParts, notes...)
	if len(commentParts) > 0 {
		fmt.Fprintf(&b, "\tcomment='%s'", sqlQuote(strings.Join(commentParts, " ")))
	}
	b.WriteString(";\n```\n")

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "definition.md"), b.Bytes(), 0o644)
}

// writeSection writes a comma-separated CREATE SEMANTIC VIEW clause, or nothing
// when empty.
func writeSection(b *bytes.Buffer, name string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\t%s (\n", name)
	for i, it := range items {
		b.WriteString("\t\t")
		b.WriteString(it)
		if i < len(items)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("\t)\n")
}

// sqlQuote escapes single quotes for a Snowflake string literal.
func sqlQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

// svNoResolve keeps metric references intact when rendering a leaf aggregate —
// a simple metric has no refs, so it is never consulted, but passing it (rather
// than nil) guards against a nil-call if one ever appears.
func svNoResolve(string) (ir.Expr, bool) { return nil, false }

// renderSVMetricDef renders a metric definition for a Snowflake semantic view.
// A simple aggregate (SUM/COUNT/…) renders as-is. A DERIVED metric — arithmetic
// over other metrics, e.g. a ratio — must REFER to those metrics by their
// qualified name and must not contain an aggregate itself; Snowflake rejects
// "SUM(x)/SUM(y)" ("a metric must directly refer to another aggregate-level
// expression … without an aggregate"). Returns ok=false when the definition
// can't be expressed that way (an aggregate or column inlined inside the
// arithmetic, or a reference to an unknown metric), so the caller degrades it.
func renderSVMetricDef(def ir.Expr, tblOf map[string]string) (string, bool) {
	if _, isAgg := def.(ir.Agg); isAgg {
		return strings.ToUpper(renderSQL(def, svNoResolve)), true
	}
	return renderSVDerived(def, tblOf)
}

// renderSVDerived renders a derived-metric expression: metric references become
// qualified names (TABLE.METRIC), literals pass through, and nested arithmetic
// recurses. Any inlined aggregate/column (ir.Agg/ir.Col/ir.Raw) is invalid here
// and yields ok=false.
func renderSVDerived(e ir.Expr, tblOf map[string]string) (string, bool) {
	switch n := e.(type) {
	case ir.Ref:
		t, ok := tblOf[n.Metric]
		if !ok {
			return "", false
		}
		return t + "." + strings.ToUpper(n.Metric), true
	case ir.Lit:
		return n.Value, true
	case ir.Binary:
		l, lok := renderSVDerived(n.Left, tblOf)
		r, rok := renderSVDerived(n.Right, tblOf)
		if !lok || !rok {
			return "", false
		}
		if _, ok := n.Left.(ir.Binary); ok {
			l = "(" + l + ")"
		}
		if _, ok := n.Right.(ir.Binary); ok {
			r = "(" + r + ")"
		}
		return l + " " + n.Op + " " + r, true
	default:
		return "", false
	}
}
