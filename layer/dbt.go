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

// dbt parses a directory of dbt semantic-layer YAML (semantic_models + metrics).
type dbt struct{}

func (dbt) Name() string { return "dbt" }

// ---- raw YAML shapes ----

type dbtFile struct {
	SemanticModels []dbtSemanticModel `yaml:"semantic_models"`
	Metrics        []dbtMetric        `yaml:"metrics"`
}

type dbtSemanticModel struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Model       string         `yaml:"model"`
	Entities    []dbtEntity    `yaml:"entities"`
	Dimensions  []dbtDimension `yaml:"dimensions"`
	Measures    []dbtMeasure   `yaml:"measures"`
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

	var models []dbtSemanticModel
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
		models = append(models, df.SemanticModels...)
		metrics = append(metrics, df.Metrics...)
	}

	out := &ir.Model{}
	tableIdx := map[string]int{}          // table name -> index in out.Tables
	measureTable := map[string]string{}   // measure name -> owning table
	measureAggExpr := map[string]string{} // measure name -> "sum(table.col)" neutral expr
	primaryByEntity := map[string]struct{ table, col string }{}

	for _, sm := range models {
		t := ir.Table{Name: sm.Name, Description: sm.Description}
		for _, e := range sm.Entities {
			col := e.Expr
			if col == "" {
				col = e.Name
			}
			t.Dimensions = append(t.Dimensions, ir.Field{Name: col, Expr: col})
			if e.Type == "primary" {
				t.PrimaryKey = append(t.PrimaryKey, col)
				primaryByEntity[e.Name] = struct{ table, col string }{sm.Name, col}
			}
		}
		for _, d := range sm.Dimensions {
			col := d.Expr
			if col == "" {
				col = d.Name
			}
			f := ir.Field{Name: d.Name, Expr: col}
			if d.Type == "time" {
				t.TimeDimensions = append(t.TimeDimensions, f)
			} else {
				t.Dimensions = append(t.Dimensions, f)
			}
		}
		for _, m := range sm.Measures {
			t.Measures = append(t.Measures, ir.Measure{Field: ir.Field{Name: m.Name, Expr: m.Expr}, Agg: m.Agg})
			measureTable[m.Name] = sm.Name
			measureAggExpr[m.Name] = aggExpr(m.Agg, sm.Name+"."+m.Expr)
		}
		tableIdx[sm.Name] = len(out.Tables)
		out.Tables = append(out.Tables, t)
	}

	// Relationships: each foreign entity joins to the primary entity of the same name.
	for _, sm := range models {
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
				Left:    sm.Name,
				Right:   p.table,
				Columns: []ir.ColumnPair{{Left: col, Right: p.col}},
			})
		}
	}

	// Metrics: simple first (so ratios can reference their exprs), then ratios.
	metricExpr := map[string]string{}
	metricTable := map[string]string{}
	attach := func(name, desc, expr, table string) {
		metricExpr[name] = expr
		metricTable[name] = table
		i := tableIdx[table]
		out.Tables[i].Metrics = append(out.Tables[i].Metrics, ir.Metric{Name: name, Description: desc, Expr: expr})
	}
	for _, m := range metrics {
		if m.Type == "ratio" {
			continue
		}
		table := measureTable[m.TypeParams.Measure]
		attach(m.Name, m.Description, measureAggExpr[m.TypeParams.Measure], table)
	}
	for _, m := range metrics {
		if m.Type != "ratio" {
			continue
		}
		num := metricExpr[m.TypeParams.Numerator]
		den := metricExpr[m.TypeParams.Denominator]
		attach(m.Name, m.Description, num+" / "+den, metricTable[m.TypeParams.Numerator])
	}

	return out, nil
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
