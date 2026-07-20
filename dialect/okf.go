package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(okf{}) }

// okf emits an Open Knowledge Format bundle: a directory of markdown concepts
// with YAML frontmatter, per the OKF spec
// (https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md).
//
// Layout: one concept per table under tables/, one per metric under metrics/,
// notes.md for passthrough notes, and the reserved index.md in every directory.
// Relationships become markdown links, so the joins are part of the knowledge
// graph rather than prose only.
//
// The output targets the reference implementation, not just the prose spec: the
// two disagree, and the reference implementation is what actually reads bundles.
// Where they differ, see the notes on validate() and index.md below.
//
// Emit-only. OKF prescribes no type taxonomy and carries meaning in free prose,
// so a parser back into the IR would be a heuristic scraper, not a dialect.
//
// Zero value usable; the build command sets the fields from a profile.
type okf struct {
	Database  string
	Schema    string
	ModelName string
	Timestamp string
}

func (okf) Name() string { return "okf" }

// WithOptions lets the CLI pass the profile's warehouse identity (used to build
// each table concept's `resource` URI), the bundle name, and the timestamp to
// stamp on every concept.
func (okf) WithOptions(o Options) Emitter {
	return okf{Database: o.Database, Schema: o.Schema, ModelName: o.Name, Timestamp: o.Timestamp}
}

// okfFrontmatter is the concept header; field order here is the emitted order,
// matching the published reference bundles.
//
// SPEC.md requires only `type`, but the reference implementation's
// OKFDocument.validate() requires type, title, description AND timestamp to be
// non-empty (okf/src/reference_agent/bundle/document.py). We satisfy the
// stricter of the two: descriptions are synthesized when the IR has none, and
// the timestamp is caller-supplied (never a clock, so bundles stay
// byte-identical across builds of the same input).
type okfFrontmatter struct {
	Type        string   `yaml:"type"`
	Resource    string   `yaml:"resource,omitempty"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags,omitempty"`
	Timestamp   string   `yaml:"timestamp,omitempty"`
}

// okfConcept is one emitted document plus the index entry it produces.
type okfConcept struct {
	dir     string // bundle-relative directory ("" for the root)
	file    string // file name within dir
	typ     string // frontmatter type, the key index.md groups by
	title   string
	desc    string
	content []byte
}

func (o okf) Emit(m *ir.Model, dir string) error {
	resolve := metricResolver(m)
	var concepts []okfConcept

	for _, t := range m.Tables {
		concepts = append(concepts, o.tableConcept(m, t, resolve))
		for _, mt := range t.Metrics {
			concepts = append(concepts, o.metricConcept(t, mt, resolve))
		}
	}
	if len(m.Notes) > 0 {
		concepts = append(concepts, o.notesConcept(m.Notes))
	}

	for _, c := range concepts {
		full := filepath.Join(dir, filepath.FromSlash(c.dir), c.file)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, c.content, 0o644); err != nil {
			return err
		}
	}
	return writeIndexes(dir, concepts)
}

// tableConcept renders one table as a concept document.
func (o okf) tableConcept(m *ir.Model, t ir.Table, resolve func(string) (ir.Expr, bool)) okfConcept {
	desc := t.Description
	if desc == "" {
		desc = fmt.Sprintf("The %s table.", t.Name)
	}
	var b bytes.Buffer
	o.writeFrontmatter(&b, okfFrontmatter{
		Type:        "Table",
		Resource:    o.resourceURI(t.Name),
		Title:       t.Name,
		Description: desc,
		Tags:        []string{"table"},
	})
	fmt.Fprintf(&b, "# Overview\n\n%s\n", desc)
	if t.Grain != "" {
		fmt.Fprintf(&b, "\nGrain: `%s`.\n", t.Grain)
	}
	if len(t.PrimaryKey) > 0 {
		b.WriteString("\n# Primary key\n\n")
		for _, k := range t.PrimaryKey {
			fmt.Fprintf(&b, "- `%s`\n", k)
		}
	}
	writeFieldSection(&b, "Dimensions", t.Dimensions)
	writeFieldSection(&b, "Time dimensions", t.TimeDimensions)

	// Measures are the aggregatable columns behind the metrics. Without them the
	// bundle never names those columns: a reader would have to reverse them out
	// of a metric's definition SQL.
	if len(t.Measures) > 0 {
		b.WriteString("\n# Measures\n\n")
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
		b.WriteString("\n# Allowed values\n\n")
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
			joins = append(joins, fmt.Sprintf("- [%s](%s.md): `%s.%s` = `%s.%s`",
				other, other, r.Left, c.Left, r.Right, c.Right))
		}
	}
	if len(joins) > 0 {
		b.WriteString("\n# Joins\n\n")
		for _, j := range joins {
			b.WriteString(j + "\n")
		}
	}

	if len(t.Metrics) > 0 {
		b.WriteString("\n# Metrics\n\n")
		for _, mt := range t.Metrics {
			fmt.Fprintf(&b, "- [%s](../metrics/%s.md): %s\n",
				metricTitle(mt), mt.Name, metricDesc(mt, resolve))
		}
	}
	return okfConcept{dir: "tables", file: t.Name + ".md", typ: "Table", title: t.Name, desc: desc, content: b.Bytes()}
}

// metricConcept renders one metric as a concept document, linked back to the
// table it is defined on.
func (o okf) metricConcept(t ir.Table, mt ir.Metric, resolve func(string) (ir.Expr, bool)) okfConcept {
	title, desc := metricTitle(mt), metricDesc(mt, resolve)
	var b bytes.Buffer
	o.writeFrontmatter(&b, okfFrontmatter{
		Type:        "Metric",
		Title:       title,
		Description: desc,
		Tags:        []string{"metric"},
	})
	fmt.Fprintf(&b, "# Overview\n\n%s\n", desc)
	fmt.Fprintf(&b, "\n# Definition\n\n```sql\n%s\n```\n", renderSQL(mt.Def, resolve))
	fmt.Fprintf(&b, "\nDefined on [%s](../tables/%s.md).\n", t.Name, t.Name)
	if c := synonymClause(mt.Synonyms); c != "" {
		fmt.Fprintf(&b, "\n%s\n", c)
	}
	if mt.Grain != "" {
		fmt.Fprintf(&b, "\nTime grain: `%s`.\n", mt.Grain)
	}
	if len(mt.Dimensions) > 0 {
		b.WriteString("\n# Slice by\n\n")
		for _, d := range mt.Dimensions {
			fmt.Fprintf(&b, "- `%s`\n", d)
		}
	}
	return okfConcept{dir: "metrics", file: mt.Name + ".md", typ: "Metric", title: title, desc: desc, content: b.Bytes()}
}

func (o okf) notesConcept(notes []string) okfConcept {
	const desc = "Source constructs that had no structural equivalent in the target layer."
	var b bytes.Buffer
	o.writeFrontmatter(&b, okfFrontmatter{
		Type:        "Note",
		Title:       "Not transpiled",
		Description: desc,
		Tags:        []string{"note"},
	})
	b.WriteString("# Not transpiled\n\n")
	for _, n := range notes {
		fmt.Fprintf(&b, "- %s\n", n)
	}
	return okfConcept{file: "notes.md", typ: "Note", title: "Not transpiled", desc: desc, content: b.Bytes()}
}

func metricTitle(mt ir.Metric) string {
	if mt.Label != "" {
		return mt.Label
	}
	return mt.Name
}

// metricDesc falls back to the metric's rendered definition when the source
// documents none, so every concept satisfies the reference implementation's
// non-empty `description` requirement.
func metricDesc(mt ir.Metric, resolve func(string) (ir.Expr, bool)) string {
	if mt.Description != "" {
		return mt.Description
	}
	return renderSQL(mt.Def, resolve)
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

func (o okf) writeFrontmatter(b *bytes.Buffer, fm okfFrontmatter) {
	fm.Timestamp = o.Timestamp
	b.WriteString("---\n")
	enc := yaml.NewEncoder(b)
	enc.SetIndent(2)
	// okfFrontmatter is a plain struct of strings; Encode cannot fail on it, and
	// Emit has no better recovery than writing a header-less concept.
	_ = enc.Encode(fm)
	_ = enc.Close()
	b.WriteString("---\n\n")
}

// writeIndexes writes the reserved index.md into every bundle directory, in the
// exact shape reference_agent.bundle.index.regenerate_indexes produces: one `#`
// section per concept type (sections sorted by type, entries by title,
// case-insensitively), links relative to the directory, and subdirectories
// grouped under "Subdirectories". Matching it byte-for-byte means running their
// regenerate_indexes over our bundle is a no-op instead of a diff.
func writeIndexes(root string, concepts []okfConcept) error {
	type entry struct{ typ, title, link, desc string }
	byDir := map[string][]entry{}
	for _, c := range concepts {
		byDir[c.dir] = append(byDir[c.dir], entry{c.typ, c.title, c.file, c.desc})
	}

	// Subdirectories appear in the root index, described by what they hold.
	var subdirs []string
	for d := range byDir {
		if d != "" {
			subdirs = append(subdirs, d)
		}
	}
	sort.Strings(subdirs)
	for _, d := range subdirs {
		byDir[""] = append(byDir[""], entry{"Subdirectories", d, d + "/index.md", subdirDesc(d)})
	}

	for dir, entries := range byDir {
		grouped := map[string][]entry{}
		for _, e := range entries {
			grouped[e.typ] = append(grouped[e.typ], e)
		}
		types := make([]string, 0, len(grouped))
		for typ := range grouped {
			types = append(types, typ)
		}
		sort.Strings(types)

		var sections []string
		for _, typ := range types {
			es := grouped[typ]
			sort.Slice(es, func(i, j int) bool {
				return strings.ToLower(es[i].title) < strings.ToLower(es[j].title)
			})
			lines := []string{"# " + typ, ""}
			for _, e := range es {
				line := fmt.Sprintf("* [%s](%s)", e.title, e.link)
				if e.desc != "" {
					line += " - " + e.desc
				}
				lines = append(lines, line)
			}
			sections = append(sections, strings.Join(lines, "\n"))
		}
		out := strings.Join(sections, "\n\n") + "\n"
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(dir), "index.md"), []byte(out), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func subdirDesc(dir string) string {
	switch dir {
	case "tables":
		return "One concept per table in the semantic layer, with its columns, joins and measures."
	case "metrics":
		return "One concept per metric, with its definition and the table it is defined on."
	default:
		return ""
	}
}

// writeFieldSection lists a table's fields as "`name` (type): description
// Synonyms: …". Fields carrying neither a description nor synonyms still
// appear: unlike the nao rules layer, an OKF bundle is not paired with a synced
// schema, so the column name is knowledge in itself.
func writeFieldSection(b *bytes.Buffer, heading string, fields []ir.Field) {
	if len(fields) == 0 {
		return
	}
	fmt.Fprintf(b, "\n# %s\n\n", heading)
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
