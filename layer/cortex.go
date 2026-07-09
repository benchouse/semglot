package layer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(cortex{}) }

// cortex emits a Snowflake Cortex semantic model. Zero value is usable; the
// build command sets Database/Schema/Name/Description from flags.
type cortex struct {
	Database    string
	Schema      string
	ModelName   string
	Description string
}

func (cortex) Name() string { return "cortex" }

// WithOptions returns a cortex emitter carrying the given base_table and model
// identity. Used by the CLI to pass --database/--schema/--name/--description.
func (cortex) WithOptions(database, schema, name, description string) Emitter {
	return cortex{Database: database, Schema: schema, ModelName: name, Description: description}
}

// ---- Cortex YAML shapes ----

type cortexModel struct {
	Name          string        `yaml:"name"`
	Description   string        `yaml:"description,omitempty"`
	Tables        []cortexTable `yaml:"tables"`
	Relationships []cortexRel   `yaml:"relationships,omitempty"`
}

type cortexTable struct {
	Name           string          `yaml:"name"`
	Description    string          `yaml:"description,omitempty"`
	BaseTable      cortexBaseTable `yaml:"base_table"`
	PrimaryKey     *cortexPK       `yaml:"primary_key,omitempty"`
	Dimensions     []cortexCol     `yaml:"dimensions,omitempty"`
	TimeDimensions []cortexCol     `yaml:"time_dimensions,omitempty"`
	Facts          []cortexCol     `yaml:"facts,omitempty"`
	Metrics        []cortexMetric  `yaml:"metrics,omitempty"`
}

type cortexBaseTable struct {
	Database string `yaml:"database"`
	Schema   string `yaml:"schema"`
	Table    string `yaml:"table"`
}

type cortexPK struct {
	Columns []string `yaml:"columns"`
}

type cortexCol struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	DataType    string   `yaml:"data_type"`
	Description string   `yaml:"description,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

type cortexMetric struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Description string   `yaml:"description,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

type cortexRel struct {
	Name                string         `yaml:"name"`
	LeftTable           string         `yaml:"left_table"`
	RightTable          string         `yaml:"right_table"`
	RelationshipColumns []cortexRelCol `yaml:"relationship_columns"`
}

type cortexRelCol struct {
	LeftColumn  string `yaml:"left_column"`
	RightColumn string `yaml:"right_column"`
}

func (c cortex) Emit(m *ir.Model, dir string) error {
	name := c.ModelName
	if name == "" {
		name = "semantic_model"
	}
	schema := c.Schema
	if schema == "" {
		schema = "MAIN"
	}

	cm := cortexModel{Name: name, Description: c.Description}
	for _, t := range m.Tables {
		ct := cortexTable{
			Name:        t.Name,
			Description: t.Description,
			BaseTable:   cortexBaseTable{Database: c.Database, Schema: schema, Table: strings.ToUpper(t.Name)},
		}
		if len(t.PrimaryKey) > 0 {
			ct.PrimaryKey = &cortexPK{Columns: upperAll(t.PrimaryKey)}
		}
		for _, d := range t.Dimensions {
			ct.Dimensions = append(ct.Dimensions, cortexCol{
				Name: d.Name, Expr: strings.ToUpper(d.Expr), DataType: inferDataType(d.Name),
				Description: d.Description, Synonyms: d.Synonyms,
			})
		}
		for _, d := range t.TimeDimensions {
			ct.TimeDimensions = append(ct.TimeDimensions, cortexCol{
				Name: d.Name, Expr: strings.ToUpper(d.Expr), DataType: "DATE",
				Description: d.Description, Synonyms: d.Synonyms,
			})
		}
		for _, mm := range t.Measures {
			ct.Facts = append(ct.Facts, cortexCol{
				Name: mm.Name, Expr: strings.ToUpper(mm.Expr), DataType: "NUMBER",
				Description: mm.Description, Synonyms: mm.Synonyms,
			})
		}
		for _, mt := range t.Metrics {
			ct.Metrics = append(ct.Metrics, cortexMetric{
				Name: mt.Name, Expr: strings.ToUpper(mt.Expr),
				Description: mt.Description, Synonyms: mt.Synonyms,
			})
		}
		cm.Tables = append(cm.Tables, ct)
	}
	for _, r := range m.Relationships {
		cols := make([]cortexRelCol, len(r.Columns))
		for i, cp := range r.Columns {
			cols[i] = cortexRelCol{LeftColumn: strings.ToUpper(cp.Left), RightColumn: strings.ToUpper(cp.Right)}
		}
		cm.Relationships = append(cm.Relationships, cortexRel{
			Name: r.Left + "_to_" + r.Right, LeftTable: r.Left, RightTable: r.Right, RelationshipColumns: cols,
		})
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(cm); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "semantic_model.yaml"), buf.Bytes(), 0o644)
}

func upperAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = strings.ToUpper(s)
	}
	return out
}

// inferDataType guesses a Cortex data_type for a dimension whose source dialect
// did not record one. Known v1 limitation: heuristic, not exact.
func inferDataType(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "BOOLEAN"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "NUMBER"
	default:
		return "TEXT"
	}
}
