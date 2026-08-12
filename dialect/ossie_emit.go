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

// WithOptions returns an ossie emitter carrying the model identity and the
// database/schema used to qualify each dataset's `source`.
func (ossie) WithOptions(o Options) Emitter {
	return ossie{Database: o.Database, Schema: o.Schema, ModelName: o.Name, Description: o.Description}
}

// osiSource builds a dataset's `source`. The spec says it "should be either
// database_name.schema_name.table_name or query", so an unset database degrades
// to a bare (still valid) table reference rather than emitting empty segments.
func (o ossie) osiSource(table string) string {
	switch {
	case o.Database != "" && o.Schema != "":
		return fmt.Sprintf("%s.%s.%s", o.Database, o.Schema, table)
	case o.Database != "":
		return fmt.Sprintf("%s.%s", o.Database, table)
	case o.Schema != "":
		return fmt.Sprintf("%s.%s", o.Schema, table)
	default:
		return table
	}
}

// ansi wraps a SQL string in the single-dialect expression object OSI requires.
func ansi(expr string) osiExpression {
	return osiExpression{Dialects: []osiDialectExpr{{Dialect: "ANSI_SQL", Expression: expr}}}
}

// aiContext builds an ai_context block, or nil when there is nothing to say.
func aiContext(instructions string, synonyms []string) *osiAIContext {
	if instructions == "" && len(synonyms) == 0 {
		return nil
	}
	return &osiAIContext{Instructions: instructions, Synonyms: synonyms}
}

// toOSIField renders one IR field. isTime is emitted explicitly in both
// directions rather than relying on the datatype default, so a reader never has
// to re-derive the role semglot already decided. When f.DataType is set but has
// no OSI portable-vocabulary equivalent (irToOSIType returns ""), datatype is
// correctly omitted per spec guidance, but the caller must still be told about
// it — omitting silently would drop the fact that the IR did carry a type.
func toOSIField(f ir.Field, isTime bool, table string, warnings *[]string) osiField {
	t := isTime
	dt := irToOSIType(f.DataType)
	if f.DataType != "" && dt == "" {
		*warnings = append(*warnings, fmt.Sprintf(
			"dataset %q field %q: SQL type %q has no OSI datatype equivalent; datatype omitted", table, f.Name, f.DataType))
	}
	return osiField{
		Name:        f.Name,
		Expression:  ansi(f.Expr),
		Dimension:   &osiDimension{IsTime: &t},
		Description: appendClause(f.Description, enumClause(f.Enum)),
		DataType:    dt,
		AIContext:   aiContext("", f.Synonyms),
	}
}

// addField appends f to ds.Fields, deduplicating by name: OSI documents field
// `name` as "Unique identifier for the field within the logical dataset", and
// Dimensions, TimeDimensions, and Measures can all resolve to a field sharing
// a name (e.g. a column that is both a plain dimension and a measure's
// operand). First occurrence wins, in emit order (Dimensions, then
// TimeDimensions, then Measures — the order the three loops run in).
//
// A later same-name/same-expression entry describes the same column twice;
// it is dropped with no warning because nothing is lost. A later same-name/
// different-expression entry is real information loss — the dataset can only
// keep one field under that name — so it is dropped WITH a returned warning
// naming both expressions, per this branch's no-silent-drop ruling.
func addField(ds *osiDataset, seen map[string]string, f osiField, warnings *[]string) {
	expr := fieldExprText(f)
	if prevExpr, dup := seen[f.Name]; dup {
		if prevExpr != expr {
			*warnings = append(*warnings, fmt.Sprintf(
				"dataset %q: field %q declared twice with different expressions (%q and %q); second occurrence dropped",
				ds.Name, f.Name, prevExpr, expr))
		}
		return
	}
	seen[f.Name] = expr
	ds.Fields = append(ds.Fields, f)
}

// fieldExprText extracts the ANSI_SQL expression text ansi() wrapped f's
// field in, for dedup comparison.
func fieldExprText(f osiField) string {
	if len(f.Expression.Dialects) == 0 {
		return ""
	}
	return f.Expression.Dialects[0].Expression
}

func (o ossie) Emit(m *ir.Model, dir string) ([]string, error) {
	name := o.ModelName
	if name == "" {
		name = "semantic_model"
	}
	var warnings []string

	sm := osiModel{Name: name, Description: o.Description}
	if len(m.Notes) > 0 {
		sm.AIContext = aiContext(strings.Join(m.Notes, " "), nil)
	}

	for _, t := range m.Tables {
		desc := t.Description
		// Grain has no OSI slot; fold it into the dataset description.
		if t.Grain != "" {
			desc = appendClause(desc, "Default time dimension: "+t.Grain+".")
		}
		ds := osiDataset{
			Name:        t.Name,
			Source:      o.osiSource(t.Name),
			PrimaryKey:  t.PrimaryKey,
			Description: desc,
			AIContext:   aiContext("", t.Synonyms),
		}
		seen := map[string]string{} // field name -> its emitted expression, for dedup
		for _, d := range t.Dimensions {
			addField(&ds, seen, toOSIField(d, false, t.Name, &warnings), &warnings)
		}
		for _, d := range t.TimeDimensions {
			addField(&ds, seen, toOSIField(d, true, t.Name, &warnings), &warnings)
		}
		// A measure's column must be a declared field: OSI defines fields as the
		// operands of metric expressions. Ossie's own dbt converter does the same.
		// Note: this field's `name` is the measure's name, not the underlying
		// column — Task 9's metric expressions reference the physical column
		// directly (e.g. `<table>.<column>`) rather than looking it up by this
		// field's name, so a dedup that drops a measure's field entry here does
		// not break that resolution path; it only removes a redundant (or, when
		// warned, conflicting) field declaration.
		for _, ms := range t.Measures {
			addField(&ds, seen, toOSIField(ms.Field, false, t.Name, &warnings), &warnings)
		}
		sm.Datasets = append(sm.Datasets, ds)
	}

	f := osiFile{Version: osiVersion, SemanticModel: []osiModel{sm}}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return warnings, err
	}
	if err := enc.Close(); err != nil {
		return warnings, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return warnings, err
	}
	return warnings, os.WriteFile(filepath.Join(dir, "semantic_model.yaml"), buf.Bytes(), 0o644)
}
