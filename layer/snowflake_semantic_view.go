package layer

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
type snowflakeSemanticView struct{ Database, Schema, ModelName, Description string }

func (snowflakeSemanticView) Name() string { return "snowflake-semantic-view" }

func (snowflakeSemanticView) WithOptions(database, schema, name, description string) Emitter {
	return snowflakeSemanticView{database, schema, name, description}
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
	resolve := metricResolver(m)
	notes := slices.Clone(m.Notes)

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
		for _, d := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
			dims = append(dims, fmt.Sprintf("%s.%s as %s.%s", u, strings.ToUpper(d.Name), strings.ToLower(t.Name), strings.ToUpper(d.Expr)))
		}
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				notes = append(notes, fmt.Sprintf("metric %q: %s", mt.Name, reason))
				continue
			}
			ml := fmt.Sprintf("%s.%s as %s", u, strings.ToUpper(mt.Name), strings.ToUpper(renderSQL(mt.Def, resolve)))
			if mt.Description != "" {
				ml += fmt.Sprintf(" comment='%s'", sqlQuote(mt.Description))
			}
			metrics = append(metrics, ml)
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
	fmt.Fprintf(&b, "create or replace semantic view %s\n", view)
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
