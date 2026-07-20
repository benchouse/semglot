package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(okf{}) }

// okf emits an Open Knowledge Format bundle: a directory of markdown concepts
// with YAML frontmatter, per the OKF v0.1 spec
// (https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).
//
// Layout: one concept per table under tables/, one per metric under metrics/,
// the reserved index.md listing both, and notes.md for passthrough notes.
// Relationships become bundle-absolute markdown links, so the joins are part of
// the knowledge graph rather than prose only.
//
// Emit-only. OKF prescribes no type taxonomy and carries meaning in free prose,
// so a parser back into the IR would be a heuristic scraper, not a dialect.
//
// Zero value usable; the build command sets Database/Schema/Name from a profile.
type okf struct {
	Database  string
	Schema    string
	ModelName string
}

func (okf) Name() string { return "okf" }

// WithOptions lets the CLI pass the profile's warehouse identity (used to build
// each table concept's `resource` URI) and the bundle name.
func (okf) WithOptions(o Options) Emitter {
	return okf{Database: o.Database, Schema: o.Schema, ModelName: o.Name}
}

// okfFrontmatter is the concept header. Field order here is the emitted order.
// `timestamp` is deliberately absent: the spec only recommends it, and a clock
// would make bundles differ byte-for-byte between otherwise identical builds.
type okfFrontmatter struct {
	Type        string   `yaml:"type"`
	Title       string   `yaml:"title,omitempty"`
	Description string   `yaml:"description,omitempty"`
	Resource    string   `yaml:"resource,omitempty"`
	Tags        []string `yaml:"tags,omitempty"`
}

func (o okf) Emit(m *ir.Model, dir string) error {
	resolve := metricResolver(m)

	type conceptRef struct{ title, path, desc string }
	var tableRefs, metricRefs []conceptRef

	files := map[string][]byte{}

	for _, t := range m.Tables {
		path := "tables/" + t.Name + ".md"
		files[path] = o.tableConcept(m, t)
		tableRefs = append(tableRefs, conceptRef{title: t.Name, path: path, desc: t.Description})

		for _, mt := range t.Metrics {
			mpath := "metrics/" + mt.Name + ".md"
			files[mpath] = o.metricConcept(t, mt, resolve)
			title := mt.Label
			if title == "" {
				title = mt.Name
			}
			metricRefs = append(metricRefs, conceptRef{title: title, path: mpath, desc: mt.Description})
		}
	}

	if len(m.Notes) > 0 {
		var b bytes.Buffer
		writeFrontmatter(&b, okfFrontmatter{
			Type:        "Note",
			Title:       "Not transpiled",
			Description: "Source constructs that had no structural equivalent in the target layer.",
			Tags:        []string{"note"},
		})
		b.WriteString("# Not transpiled\n\n")
		for _, n := range m.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
		files["notes.md"] = b.Bytes()
	}

	// index.md is reserved: a plain listing, no frontmatter.
	var idx bytes.Buffer
	title := o.ModelName
	if title == "" {
		title = "Semantic layer"
	}
	fmt.Fprintf(&idx, "# %s\n", title)
	writeIndexSection(&idx, "Tables", func(yield func(title, path, desc string)) {
		for _, r := range tableRefs {
			yield(r.title, r.path, r.desc)
		}
	})
	writeIndexSection(&idx, "Metrics", func(yield func(title, path, desc string)) {
		for _, r := range metricRefs {
			yield(r.title, r.path, r.desc)
		}
	})
	if len(m.Notes) > 0 {
		idx.WriteString("\n## Notes\n\n- [Not transpiled](/notes.md)\n")
	}
	files["index.md"] = idx.Bytes()

	for path, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// tableConcept renders one table as a concept document.
func (o okf) tableConcept(m *ir.Model, t ir.Table) []byte {
	var b bytes.Buffer
	writeFrontmatter(&b, okfFrontmatter{
		Type:        "Table",
		Title:       t.Name,
		Description: t.Description,
		Resource:    o.resourceURI(t.Name),
		Tags:        []string{"table"},
	})
	fmt.Fprintf(&b, "# %s\n", t.Name)
	if t.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", t.Description)
	}
	if t.Grain != "" {
		fmt.Fprintf(&b, "\nGrain: `%s`.\n", t.Grain)
	}
	if len(t.PrimaryKey) > 0 {
		b.WriteString("\n## Primary key\n\n")
		for _, k := range t.PrimaryKey {
			fmt.Fprintf(&b, "- `%s`\n", k)
		}
	}
	writeFieldSection(&b, "Dimensions", t.Name, t.Dimensions)
	writeFieldSection(&b, "Time dimensions", t.Name, t.TimeDimensions)

	// Measures are the aggregatable columns behind the metrics. Without them the
	// bundle never names those columns: a reader would have to reverse them out
	// of a metric's definition SQL.
	if len(t.Measures) > 0 {
		b.WriteString("\n## Measures\n\n")
		for _, meas := range t.Measures {
			line := fmt.Sprintf("- `%s` (%s)", meas.Name, meas.Agg)
			if meas.Expr != "" && meas.Expr != meas.Name {
				line = fmt.Sprintf("- `%s` (%s of `%s`)", meas.Name, meas.Agg, meas.Expr)
			}
			if body := appendClause(strings.TrimSpace(meas.Description), synonymClause(meas.Synonyms)); body != "" {
				line += ": " + body
			}
			b.WriteString(line + "\n")
		}
	}

	var enums []string
	for _, f := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
		if c := enumClause(f.Enum); c != "" {
			enums = append(enums, fmt.Sprintf("- `%s`: %s", f.Name, strings.TrimPrefix(c, "Values: ")))
		}
	}
	if len(enums) > 0 {
		b.WriteString("\n## Allowed values\n\n")
		for _, e := range enums {
			b.WriteString(e + "\n")
		}
	}

	var joins []string
	for _, r := range m.Relationships {
		other := ""
		switch t.Name {
		case r.Left:
			other = r.Right
		case r.Right:
			other = r.Left
		default:
			continue
		}
		for _, c := range r.Columns {
			joins = append(joins, fmt.Sprintf("- [%s](/tables/%s.md): `%s.%s` = `%s.%s`",
				other, other, r.Left, c.Left, r.Right, c.Right))
		}
	}
	if len(joins) > 0 {
		b.WriteString("\n## Joins\n\n")
		for _, j := range joins {
			b.WriteString(j + "\n")
		}
	}

	if len(t.Metrics) > 0 {
		b.WriteString("\n## Metrics\n\n")
		for _, mt := range t.Metrics {
			label := mt.Label
			if label == "" {
				label = mt.Name
			}
			fmt.Fprintf(&b, "- [%s](/metrics/%s.md)\n", label, mt.Name)
		}
	}
	return b.Bytes()
}

// metricConcept renders one metric as a concept document, linked back to the
// table it is defined on.
func (o okf) metricConcept(t ir.Table, mt ir.Metric, resolve func(string) (ir.Expr, bool)) []byte {
	title := mt.Label
	if title == "" {
		title = mt.Name
	}
	var b bytes.Buffer
	writeFrontmatter(&b, okfFrontmatter{
		Type:        "Metric",
		Title:       title,
		Description: mt.Description,
		Tags:        []string{"metric"},
	})
	fmt.Fprintf(&b, "# %s\n", title)
	if mt.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", mt.Description)
	}
	fmt.Fprintf(&b, "\n## Definition\n\n```sql\n%s\n```\n", renderSQL(mt.Def, resolve))
	fmt.Fprintf(&b, "\nDefined on [%s](/tables/%s.md).\n", t.Name, t.Name)
	if c := synonymClause(mt.Synonyms); c != "" {
		fmt.Fprintf(&b, "\n%s\n", c)
	}
	if mt.Grain != "" {
		fmt.Fprintf(&b, "\nTime grain: `%s`.\n", mt.Grain)
	}
	if len(mt.Dimensions) > 0 {
		b.WriteString("\n## Slice by\n\n")
		for _, d := range mt.Dimensions {
			fmt.Fprintf(&b, "- `%s`\n", d)
		}
	}
	return b.Bytes()
}

// resourceURI qualifies a table as table://DATABASE/SCHEMA/TABLE. Returns ""
// when no database is configured, so the field is dropped rather than emitted
// half-qualified.
func (o okf) resourceURI(table string) string {
	if o.Database == "" {
		return ""
	}
	schema := o.Schema
	if schema == "" {
		schema = "MAIN"
	}
	return fmt.Sprintf("table://%s/%s/%s", strings.ToUpper(o.Database), strings.ToUpper(schema), strings.ToUpper(table))
}

func writeFrontmatter(b *bytes.Buffer, fm okfFrontmatter) {
	b.WriteString("---\n")
	enc := yaml.NewEncoder(b)
	enc.SetIndent(2)
	// okfFrontmatter is a plain struct of scalars and strings; Encode cannot fail
	// on it, and Emit has no better recovery than writing a header-less concept.
	_ = enc.Encode(fm)
	_ = enc.Close()
	b.WriteString("---\n\n")
}

// writeFieldSection lists a table's fields as "`name`: description Synonyms: …".
// Fields carrying neither a description nor synonyms still appear: unlike the
// nao rules layer, an OKF bundle is not paired with a synced schema, so the
// column name is knowledge in itself.
func writeFieldSection(b *bytes.Buffer, heading, table string, fields []ir.Field) {
	if len(fields) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n", heading)
	for _, f := range fields {
		head := "`" + f.Name + "`"
		if f.DataType != "" {
			head += " (" + f.DataType + ")"
		}
		body := appendClause(strings.TrimSpace(f.Description), synonymClause(f.Synonyms))
		if body == "" {
			fmt.Fprintf(b, "- %s\n", head)
			continue
		}
		fmt.Fprintf(b, "- %s: %s\n", head, body)
	}
}

// writeIndexSection writes one grouped listing of the index, skipping the
// heading entirely when the group is empty.
func writeIndexSection(b *bytes.Buffer, heading string, each func(func(title, path, desc string))) {
	var lines []string
	each(func(title, path, desc string) {
		line := fmt.Sprintf("- [%s](/%s)", title, path)
		if desc != "" {
			line += ": " + desc
		}
		lines = append(lines, line)
	})
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n", heading)
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
}
