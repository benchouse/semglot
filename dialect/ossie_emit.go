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

// tableSource returns t's `source`: t.Source verbatim when the parsed model
// declared one — an OSI source is a fully-qualified physical address, and
// nothing downstream can recover it once discarded — else the profile-
// reconstructed reference osiSource built all along (the dbt path, which has
// no physical address of its own).
func (o ossie) tableSource(t ir.Table) string {
	if t.Source != "" {
		return t.Source
	}
	return o.osiSource(t.Name)
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

// qualifyMeasureExpr qualifies expr (a measure's underlying column or raw SQL
// fragment) with t's name for OSI's model-level flat metrics list, which
// (unlike a dataset's own dataset-scoped fields) has no implicit table scope.
// A plain identifier is qualified outright; anything else (e.g. a CASE
// expression) is qualified per-identifier via qualifyExpr, the same helper
// renderSQL uses to qualify a published metric's Raw aggregate argument — a
// naive whole-string "table.expr" prefix would corrupt a compound expression
// (e.g. "orders.case when x then 1 else 0 end", not valid SQL).
func qualifyMeasureExpr(t ir.Table, expr string) string {
	if isIdent(expr) {
		return t.Name + "." + expr
	}
	return qualifyExpr(t.Name, tableColumns(t), expr)
}

// tableColumns is the lowercased set of physical column identifiers t's own
// fields are known to reference: every dimension/time-dimension name and its
// Expr text when that Expr is a plain identifier, PLUS every OTHER measure's
// Expr when it is a plain identifier. qualifyMeasureExpr uses it to recognise
// which bare identifiers inside a measure's raw expression are real columns
// to qualify, as opposed to SQL keywords or literals.
//
// Without the measures pass, a measure whose raw expression references a
// column that exists ONLY as another measure's bare-identifier operand (e.g.
// `sum(case when is_refunded then order_gross else 0 end)`, where
// order_gross backs a measure and is not independently a dimension) would
// leave that column unqualified in OSI's cross-dataset metrics list — no
// warning, silently wrong SQL under the standing no-silent-wrong-result
// ruling. Mirrors dbt.go's own column-set construction (dbt.go:349-355),
// which folds in a measure's bare-identifier Expr for the same reason; only
// the measure's Expr counts here, not its Name, since the Name is the
// metric-list identity toOSIField's shadow field carries, not a physical
// column.
func tableColumns(t ir.Table) map[string]bool {
	cols := map[string]bool{}
	add := func(f ir.Field) {
		cols[strings.ToLower(f.Name)] = true
		if isIdent(f.Expr) {
			cols[strings.ToLower(f.Expr)] = true
		}
	}
	for _, f := range t.Dimensions {
		add(f)
	}
	for _, f := range t.TimeDimensions {
		add(f)
	}
	for _, ms := range t.Measures {
		if isIdent(ms.Expr) {
			cols[strings.ToLower(ms.Expr)] = true
		}
	}
	return cols
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
			Source:      o.tableSource(t),
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

	resolve := metricResolver(m)

	// OSI has one flat, model-level metrics list, so measures and metrics share
	// a namespace here even though they do not in the IR. Emit metrics first and
	// let them win a name clash - the metric is the published calculation, and
	// dialect/README.md records the same precedence for lightdash.
	seen := map[string]bool{}
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			if reason, degrade := cortexDegrade(mt.Def); degrade {
				warnings = append(warnings, fmt.Sprintf("metric %q not emitted to ossie: %s", mt.Name, reason))
				continue
			}
			// Two IR tables may each publish a metric of one name; OSI's single
			// flat list cannot hold both under the unique-name rule. First wins,
			// with a warning — emitting both would produce a document the spec
			// rejects, silently.
			if seen[mt.Name] {
				warnings = append(warnings, fmt.Sprintf(
					"metric %q on table %q not emitted: a metric of the same name is already in OSI's flat metric list", mt.Name, t.Name))
				continue
			}
			desc := mt.Description
			// Label, agg-time grain, and slice-by dimensions have no OSI slot.
			if mt.Label != "" {
				desc = appendClause(desc, "Display name: "+mt.Label+".")
			}
			if mt.Grain != "" {
				desc = appendClause(desc, "Agg-time grain: "+mt.Grain+".")
			}
			if len(mt.Dimensions) > 0 {
				desc = appendClause(desc, "Sliced by: "+strings.Join(mt.Dimensions, ", ")+".")
			}
			sm.Metrics = append(sm.Metrics, osiMetric{
				Name:        mt.Name,
				Expression:  ansi(renderSQL(mt.Def, resolve)),
				Description: desc,
				AIContext:   aiContext("", mt.Synonyms),
			})
			seen[mt.Name] = true
		}
	}
	for _, t := range m.Tables {
		for _, ms := range t.Measures {
			if seen[ms.Name] {
				warnings = append(warnings, fmt.Sprintf(
					"measure %q on table %q not emitted: a metric of the same name occupies OSI's flat metric list", ms.Name, t.Name))
				continue
			}
			sm.Metrics = append(sm.Metrics, osiMetric{
				Name:        ms.Name,
				Expression:  ansi(aggExpr(ms.Agg, qualifyMeasureExpr(t, ms.Expr))),
				Description: ms.Description,
				DataType:    irToOSIType(ms.DataType),
				AIContext:   aiContext("", ms.Synonyms),
			})
			seen[ms.Name] = true
		}
	}

	// OSI requires a unique relationship name and types it as a plain string, so
	// any non-empty declared name is legal here (valid == nil) and only a
	// collision can cost one. The generated fallback keeps relRoleSuffix, which
	// disambiguates a role-playing dimension (two FKs between the same pair)
	// exactly as the cortex, snowflake-semantic-view and databricks emitters do.
	relNames, relWarn := relationshipNames(m.Relationships, "ossie", relHasColumns,
		func(r ir.Relationship) string {
			name := r.Left + "_to_" + r.Right
			if suffix := relRoleSuffix(m.Relationships, r); suffix != "" {
				name += "_" + suffix
			}
			return name
		}, nil)
	for i, r := range m.Relationships {
		if len(r.Columns) == 0 {
			continue
		}
		if relWarn[i] != "" {
			warnings = append(warnings, relWarn[i])
		}
		rel := osiRelationship{
			Name: relNames[i], From: r.Left, To: r.Right,
			AIContext: aiContext("", r.Synonyms),
		}
		for _, cp := range r.Columns {
			rel.FromColumns = append(rel.FromColumns, cp.Left)
			rel.ToColumns = append(rel.ToColumns, cp.Right)
		}
		sm.Relationships = append(sm.Relationships, rel)
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
