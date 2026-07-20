package dialect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(lightdash{}) }

// lightdash emits a dbt schema.yml annotated with Lightdash meta: blocks, the
// form Lightdash Cloud ingests from a connected dbt project. Zero value is
// usable; the build command sets Name/Description/MetaStyle from the profile.
// Emit does not mutate m.
type lightdash struct {
	ModelName   string
	Description string
	MetaStyle   string // "" or "meta" => meta:; "config.meta" => config.meta:
}

func (lightdash) Name() string { return "lightdash" }
func (lightdash) WithOptions(o Options) Emitter {
	return lightdash{ModelName: o.Name, Description: o.Description}
}

// ---- Lightdash YAML shapes ----
//
// Each meta payload can hang under either `meta:` (dbt <=1.9) or `config.meta:`
// (dbt 1.10+/Fusion). The model/column structs carry BOTH a Meta and a Config
// field; the emitter sets exactly one based on MetaStyle, so the struct tags
// stay static while placement varies.

type ldFile struct {
	Version int       `yaml:"version"`
	Models  []ldModel `yaml:"models"`
}

type ldModel struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Meta        *ldModelMeta `yaml:"meta,omitempty"`
	Config      *ldModelCfg  `yaml:"config,omitempty"`
	Columns     []ldColumn   `yaml:"columns,omitempty"`
}

type ldModelCfg struct {
	Meta *ldModelMeta `yaml:"meta,omitempty"`
}

type ldModelMeta struct {
	PrimaryKey string              `yaml:"primary_key,omitempty"`
	Joins      []ldJoin            `yaml:"joins,omitempty"`
	Metrics    map[string]ldMetric `yaml:"metrics,omitempty"`
}

func (m *ldModelMeta) empty() bool {
	return m.PrimaryKey == "" && len(m.Joins) == 0 && len(m.Metrics) == 0
}

type ldJoin struct {
	Join         string `yaml:"join"`
	SQLOn        string `yaml:"sql_on"`
	Relationship string `yaml:"relationship,omitempty"`
}

type ldColumn struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Meta        *ldColMeta `yaml:"meta,omitempty"`
	Config      *ldColCfg  `yaml:"config,omitempty"`
}

type ldColCfg struct {
	Meta *ldColMeta `yaml:"meta,omitempty"`
}

type ldColMeta struct {
	Dimension *ldDimension        `yaml:"dimension,omitempty"`
	Metrics   map[string]ldMetric `yaml:"metrics,omitempty"`
}

type ldDimension struct {
	Type  string `yaml:"type,omitempty"`
	Label string `yaml:"label,omitempty"`
}

type ldMetric struct {
	Type string `yaml:"type"`
	SQL  string `yaml:"sql,omitempty"`
}

// ldColumnSet builds the ordered columns[] list, keyed by physical column name,
// so a dimension and a metric backed by the same column share one entry and a
// metric's backing column is created even when it is not itself a dimension.
type ldColumnSet struct {
	order  []string
	byName map[string]*ldColumn
}

func newColumnSet() *ldColumnSet { return &ldColumnSet{byName: map[string]*ldColumn{}} }

func (s *ldColumnSet) get(col string) *ldColumn {
	c, ok := s.byName[col]
	if !ok {
		c = &ldColumn{Name: col}
		s.byName[col] = c
		s.order = append(s.order, col)
	}
	return c
}

func (s *ldColumnSet) list() []ldColumn {
	out := make([]ldColumn, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, *s.byName[name])
	}
	return out
}

// dimension records f as a Lightdash dimension on its backing column. Enum
// meanings and synonyms fold into the column description (Lightdash has no
// enum or synonym slot). type is set only when typ is non-empty; a label is
// set only when the IR dimension name differs from the physical column.
func (s *ldColumnSet) dimension(f ir.Field, typ string) {
	col := f.Expr
	if col == "" {
		col = f.Name
	}
	c := s.get(col)
	if c.Description == "" {
		desc := appendClause(f.Description, enumClause(f.Enum))
		desc = appendClause(desc, synonymClause(f.Synonyms))
		c.Description = desc
	}
	if typ != "" || (f.Name != "" && f.Name != col) {
		if c.Meta == nil {
			c.Meta = &ldColMeta{}
		}
		if c.Meta.Dimension == nil {
			c.Meta.Dimension = &ldDimension{}
		}
		if typ != "" {
			c.Meta.Dimension.Type = typ
		}
		if f.Name != "" && f.Name != col {
			c.Meta.Dimension.Label = f.Name
		}
	}
}

// ldDimensionType returns a Lightdash dimension type when a confident signal
// exists, else "" so the type is omitted (Lightdash auto-detects from the
// warehouse). Signals: an explicit DataType; a time dimension is timestamp; an
// is_/has_ prefix is boolean; an _id/_sk suffix (or bare "id") is number.
func ldDimensionType(f ir.Field, isTime bool) string {
	if f.DataType != "" {
		return ldMapType(f.DataType)
	}
	if isTime {
		return "timestamp"
	}
	n := strings.ToLower(f.Name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "boolean"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "number"
	}
	return ""
}

// ldMapType normalizes a warehouse column type to one of Lightdash's five
// dimension types. An unrecognized type returns "" so the type is omitted.
func ldMapType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "number", "numeric", "decimal", "int", "integer", "bigint", "smallint",
		"float", "double", "double precision", "real":
		return "number"
	case "varchar", "text", "string", "char", "character varying":
		return "string"
	case "boolean", "bool":
		return "boolean"
	case "date":
		return "date"
	case "timestamp", "datetime", "timestamp_ntz", "timestamp_tz", "timestamp_ltz":
		return "timestamp"
	default:
		return ""
	}
}

// Emit writes the IR as one dbt schema.yml carrying Lightdash annotations. It
// does not mutate m: passthrough notes and degrade notes accumulate in a local
// slice and render as a leading # semglot: comment block.
func (l lightdash) Emit(m *ir.Model, dir string) error {
	var notes []string
	notes = append(notes, m.Notes...)

	f := ldFile{Version: 2}
	for _, t := range m.Tables {
		cols := newColumnSet()
		for _, d := range t.Dimensions {
			cols.dimension(d, ldDimensionType(d, false))
		}
		for _, d := range t.TimeDimensions {
			cols.dimension(d, ldDimensionType(d, true))
		}
		f.Models = append(f.Models, ldModel{
			Name: t.Name, Description: t.Description, Columns: cols.list(),
		})
	}

	var buf bytes.Buffer
	if len(notes) > 0 {
		buf.WriteString("# semglot: some source constructs could not be transpiled to Lightdash:\n")
		for _, n := range notes {
			buf.WriteString("# - " + n + "\n")
		}
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "schema.yml"), buf.Bytes(), 0o644)
}
