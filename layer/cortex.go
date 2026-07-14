package layer

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

// cortexDegrade reports whether a metric definition has no validated Cortex
// primitive and must be degraded to a note. The cumulative (Window) and
// conversion (Conversion) kinds are PROVISIONAL — deliberately not lowered to
// Cortex SQL until a live target validates them.
func cortexDegrade(def ir.Expr) (reason string, degrade bool) {
	switch def.(type) {
	case ir.Window:
		return "cumulative/windowed metric — no validated Cortex primitive (provisional)", true
	case ir.Conversion:
		return "conversion/funnel metric — no Cortex primitive (provisional)", true
	}
	return "", false
}

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
	Name               string        `yaml:"name"`
	Description        string        `yaml:"description,omitempty"`
	Tables             []cortexTable `yaml:"tables"`
	Relationships      []cortexRel   `yaml:"relationships,omitempty"`
	CustomInstructions string        `yaml:"custom_instructions,omitempty"`
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
	Name         string   `yaml:"name"`
	Expr         string   `yaml:"expr"`
	DataType     string   `yaml:"data_type"`
	Description  string   `yaml:"description,omitempty"`
	Synonyms     []string `yaml:"synonyms,omitempty"`
	SampleValues []string `yaml:"sample_values,omitempty"`
}

// cortexEnum renders a field's enum for Cortex, which has no per-value
// description field: the values become sample_values, and any documented values
// are folded into the column description as a "Values: v = meaning; …" clause.
func cortexEnum(desc string, enum []ir.EnumValue) (string, []string) {
	if len(enum) == 0 {
		return desc, nil
	}
	vals := make([]string, len(enum))
	hasDesc := false
	for i, e := range enum {
		vals[i] = e.Value
		if e.Description != "" {
			hasDesc = true
		}
	}
	// sample_values already carries the bare values, so only fold the text
	// clause in when it adds per-value meaning.
	if hasDesc {
		desc = appendClause(desc, enumClause(enum))
	}
	return desc, vals
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

// Emit does not mutate m; it reads m.Notes and accumulates its own degrade
// notes locally before writing the combined text to custom_instructions.
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
	resolve := metricResolver(m)
	var degradeNotes []string
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
			desc, sampleVals := cortexEnum(d.Description, d.Enum)
			ct.Dimensions = append(ct.Dimensions, cortexCol{
				Name: d.Name, Expr: strings.ToUpper(d.Expr), DataType: pickType(d.DataType, inferDataType(d.Name)),
				Description: desc, Synonyms: d.Synonyms, SampleValues: sampleVals,
			})
		}
		for _, d := range t.TimeDimensions {
			ct.TimeDimensions = append(ct.TimeDimensions, cortexCol{
				Name: d.Name, Expr: strings.ToUpper(d.Expr), DataType: pickType(d.DataType, "DATE"),
				Description: d.Description, Synonyms: d.Synonyms,
			})
		}
		for _, mm := range t.Measures {
			ct.Facts = append(ct.Facts, cortexCol{
				Name: mm.Name, Expr: strings.ToUpper(mm.Expr), DataType: pickType(mm.DataType, "NUMBER"),
				Description: mm.Description, Synonyms: mm.Synonyms,
			})
		}
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				// No validated Cortex primitive for this kind: omit the metric and
				// surface it as guidance rather than emit SQL we cannot stand behind.
				degradeNotes = append(degradeNotes, fmt.Sprintf("metric %q not emitted to Cortex: %s", mt.Name, reason))
				continue
			}
			ct.Metrics = append(ct.Metrics, cortexMetric{
				Name: mt.Name, Expr: strings.ToUpper(renderSQL(mt.Def, resolve)),
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

	allNotes := append(slices.Clone(m.Notes), degradeNotes...)
	if len(allNotes) > 0 {
		var sb strings.Builder
		sb.WriteString("Some dbt metrics could not be transpiled to Cortex metrics; treat the following as guidance:")
		for _, n := range allNotes {
			sb.WriteString("\n- ")
			sb.WriteString(n)
		}
		cm.CustomInstructions = sb.String()
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

// pickType returns the mapped real data type when the IR carries one (from dbt
// model properties), otherwise the caller's fallback (an inference or a role
// default like DATE/NUMBER).
func pickType(dbtType, fallback string) string {
	if strings.TrimSpace(dbtType) == "" {
		return fallback
	}
	return mapDbtType(dbtType)
}

// mapDbtType normalizes a dbt/warehouse column data_type to a Cortex data_type.
// Unknown types are passed through uppercased.
func mapDbtType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "number", "numeric", "decimal", "int", "integer", "bigint", "smallint":
		return "NUMBER"
	case "float", "double", "double precision", "real":
		return "FLOAT"
	case "varchar", "text", "string", "char", "character varying":
		return "TEXT"
	case "boolean", "bool":
		return "BOOLEAN"
	case "date":
		return "DATE"
	case "timestamp", "datetime", "timestamp_ntz", "timestamp_tz", "timestamp_ltz":
		return "TIMESTAMP"
	default:
		return strings.ToUpper(strings.TrimSpace(t))
	}
}

// inferDataType guesses a Cortex data_type for a dimension whose source dialect
// did not record one. Heuristic fallback used only when no real type is known.
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

// CortexTypeGaps returns, in table/column order, the columns whose Cortex
// data_type had to be inferred because the source model declared no data_type.
// Cortex requires a data_type per column, so a wrong guess (classically a
// numeric amount inferred as TEXT, which makes Cortex emit string-concatenating
// SQL) silently corrupts answers. Each entry names the column and the type that
// was inferred, so the source dbt model can be backfilled with real types. The
// CLI prints these for the cortex target.
func CortexTypeGaps(m *ir.Model) []string {
	var gaps []string
	add := func(table, col, inferred string, dt string) {
		if strings.TrimSpace(dt) == "" {
			gaps = append(gaps, fmt.Sprintf("%s.%s (inferred %s)", table, col, inferred))
		}
	}
	for _, t := range m.Tables {
		for _, d := range t.Dimensions {
			add(t.Name, d.Name, inferDataType(d.Name), d.DataType)
		}
		for _, d := range t.TimeDimensions {
			add(t.Name, d.Name, "DATE", d.DataType)
		}
		for _, mm := range t.Measures {
			add(t.Name, mm.Name, "NUMBER", mm.DataType)
		}
	}
	return gaps
}
