package layer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// dbt Emit makes the dbt layer bidirectional: it lowers the neutral IR back to a
// single dbt YAML file (models: + semantic_models: + metrics:) that Parse reads
// verbatim. The round-trip is IR-lossless, not byte-identical — see
// TestDBTRoundTrip. These emit-only YAML shapes are separate from the parse
// shapes on purpose: dbtTest has a custom unmarshaler and no marshaler, so we
// give these plain structs yaml tags matching the parser's field names exactly.

type dbtEmitFile struct {
	Models         []dbtEmitModel    `yaml:"models,omitempty"`
	SemanticModels []dbtEmitSemantic `yaml:"semantic_models,omitempty"`
	Metrics        []dbtEmitMetric   `yaml:"metrics,omitempty"`
}

type dbtEmitModel struct {
	Name        string          `yaml:"name"`
	Description string          `yaml:"description,omitempty"`
	Columns     []dbtEmitColumn `yaml:"columns,omitempty"`
}

type dbtEmitColumn struct {
	Name        string              `yaml:"name"`
	Description string              `yaml:"description,omitempty"`
	DataType    string              `yaml:"data_type,omitempty"`
	Constraints []dbtEmitConstraint `yaml:"constraints,omitempty"`
	DataTests   []dbtEmitTest       `yaml:"data_tests,omitempty"`
	Meta        *dbtEmitMeta        `yaml:"meta,omitempty"`
}

// dbtEmitMeta round-trips a column's synonyms and enum descriptions; the enum
// value list itself round-trips as an accepted_values test (see dbtEmitTest).
type dbtEmitMeta struct {
	Synonyms []string          `yaml:"synonyms,omitempty"`
	Enum     map[string]string `yaml:"enum,omitempty"`
}

type dbtEmitConstraint struct {
	Type string `yaml:"type"`
}

type dbtEmitTest struct {
	Relationships  *dbtEmitRelTest        `yaml:"relationships,omitempty"`
	AcceptedValues *dbtEmitAcceptedValues `yaml:"accepted_values,omitempty"`
}

type dbtEmitAcceptedValues struct {
	Arguments dbtEmitAVArgs `yaml:"arguments"`
}

type dbtEmitAVArgs struct {
	Values []string `yaml:"values,flow"`
}

type dbtEmitRelTest struct {
	To    string `yaml:"to"`
	Field string `yaml:"field"`
}

type dbtEmitSemantic struct {
	Name       string             `yaml:"name"`
	Model      string             `yaml:"model"`
	Defaults   *dbtEmitDefaults   `yaml:"defaults,omitempty"`
	Entities   []dbtEmitEntity    `yaml:"entities,omitempty"`
	Dimensions []dbtEmitDimension `yaml:"dimensions,omitempty"`
	Measures   []dbtEmitMeasure   `yaml:"measures,omitempty"`
}

type dbtEmitDefaults struct {
	AggTimeDimension string `yaml:"agg_time_dimension,omitempty"`
}

type dbtEmitEntity struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Expr string `yaml:"expr,omitempty"`
}

type dbtEmitDimension struct {
	Name string `yaml:"name"`
	Type string `yaml:"type"`
	Expr string `yaml:"expr,omitempty"`
}

type dbtEmitMeasure struct {
	Name string `yaml:"name"`
	Agg  string `yaml:"agg"`
	Expr string `yaml:"expr"`
}

type dbtEmitMetric struct {
	Name        string            `yaml:"name"`
	Label       string            `yaml:"label,omitempty"`
	Type        string            `yaml:"type"`
	Description string            `yaml:"description,omitempty"`
	Filter      string            `yaml:"filter,omitempty"`
	TypeParams  dbtEmitTypeParams `yaml:"type_params"`
}

type dbtEmitTypeParams struct {
	Measure              string                   `yaml:"measure,omitempty"`
	Numerator            string                   `yaml:"numerator,omitempty"`
	Denominator          string                   `yaml:"denominator,omitempty"`
	Expr                 string                   `yaml:"expr,omitempty"`
	Metrics              []dbtEmitMetricRef       `yaml:"metrics,omitempty"`
	Window               string                   `yaml:"window,omitempty"`
	Grain                string                   `yaml:"grain,omitempty"`
	ConversionTypeParams *dbtEmitConversionParams `yaml:"conversion_type_params,omitempty"`
}

type dbtEmitMetricRef struct {
	Name string `yaml:"name"`
}

type dbtEmitConversionParams struct {
	BaseMeasure       string `yaml:"base_measure"`
	ConversionMeasure string `yaml:"conversion_measure"`
	Entity            string `yaml:"entity,omitempty"`
	Window            string `yaml:"window,omitempty"`
}

// Emit writes the IR as a single dbt YAML file, <dir>/ecommerce.yml.
func (dbt) Emit(m *ir.Model, dir string) error {
	var f dbtEmitFile
	for _, t := range m.Tables {
		pk := stringSet(t.PrimaryKey)
		fk := fkColumns(m, t.Name)

		f.Models = append(f.Models, emitModel(m, t, pk, fk))
		f.SemanticModels = append(f.SemanticModels, emitSemantic(t, pk, fk))
		f.Metrics = append(f.Metrics, emitMetrics(t)...)
	}

	var buf bytes.Buffer
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
	return os.WriteFile(filepath.Join(dir, "ecommerce.yml"), buf.Bytes(), 0o644)
}

// emitModel builds the classic model-properties block: one column per field that
// carries a description or data_type (so colDesc/colType reconstruct on
// re-parse), a primary_key constraint on PK columns, and a relationships
// data_test on each FK column (which alone reproduces ir.Relationship).
func emitModel(m *ir.Model, t ir.Table, pk, fk map[string]bool) dbtEmitModel {
	// Collect physical columns in deterministic order, keyed by column name
	// (a field's Expr is the underlying column). Merge duplicates preferring
	// non-empty description/type — e.g. order_id is both a PK dimension
	// ("Order surrogate key.") and a count_distinct measure's backing column
	// (whose Field.Description parse forced to "").
	var order []string
	info := map[string]*dbtEmitColumn{}
	colEnum := map[string][]ir.EnumValue{}
	colSyn := map[string][]string{}
	add := func(col, desc, dtype string, enum []ir.EnumValue, syn []string) {
		if col == "" {
			return
		}
		c, ok := info[col]
		if !ok {
			c = &dbtEmitColumn{Name: col}
			info[col] = c
			order = append(order, col)
		}
		if c.Description == "" {
			c.Description = desc
		}
		if c.DataType == "" {
			c.DataType = dtype
		}
		if len(colEnum[col]) == 0 && len(enum) > 0 {
			colEnum[col] = enum
		}
		if len(colSyn[col]) == 0 && len(syn) > 0 {
			colSyn[col] = syn
		}
	}
	for _, d := range t.Dimensions {
		add(d.Expr, d.Description, d.DataType, d.Enum, d.Synonyms)
	}
	for _, d := range t.TimeDimensions {
		add(d.Expr, d.Description, d.DataType, d.Enum, d.Synonyms)
	}
	for _, ms := range t.Measures {
		// Only a bare-column measure has a physical backing column; a compound
		// expression (e.g. a CASE) is not a column and carries no doc to persist.
		if isIdent(ms.Expr) {
			add(ms.Expr, ms.Description, ms.DataType, ms.Enum, ms.Synonyms)
		}
	}
	// Ensure every FK column exists so its relationship test has a home, even if
	// it is not otherwise a documented dimension/measure column.
	for _, r := range m.Relationships {
		if r.Left != t.Name {
			continue
		}
		for _, cp := range r.Columns {
			add(cp.Left, "", "", nil, nil)
		}
	}

	em := dbtEmitModel{Name: t.Name, Description: t.Description}
	for _, col := range order {
		c := *info[col]
		// Enum round-trips as an accepted_values test (ordered value list) plus
		// meta.enum (per-value descriptions); synonyms round-trip via meta.
		if e := colEnum[col]; len(e) > 0 {
			vals := make([]string, len(e))
			descs := map[string]string{}
			for i, ev := range e {
				vals[i] = ev.Value
				if ev.Description != "" {
					descs[ev.Value] = ev.Description
				}
			}
			c.DataTests = append(c.DataTests, dbtEmitTest{
				AcceptedValues: &dbtEmitAcceptedValues{Arguments: dbtEmitAVArgs{Values: vals}}})
			if len(descs) > 0 {
				if c.Meta == nil {
					c.Meta = &dbtEmitMeta{}
				}
				c.Meta.Enum = descs
			}
		}
		if s := colSyn[col]; len(s) > 0 {
			if c.Meta == nil {
				c.Meta = &dbtEmitMeta{}
			}
			c.Meta.Synonyms = s
		}
		if pk[col] {
			c.Constraints = append(c.Constraints, dbtEmitConstraint{Type: "primary_key"})
		}
		if fk[col] {
			for _, r := range m.Relationships {
				if r.Left != t.Name {
					continue
				}
				for _, cp := range r.Columns {
					if cp.Left != col {
						continue
					}
					c.DataTests = append(c.DataTests, dbtEmitTest{Relationships: &dbtEmitRelTest{
						To:    "ref('" + r.Right + "')",
						Field: cp.Right,
					}})
				}
			}
		}
		em.Columns = append(em.Columns, c)
	}
	return em
}

// emitSemantic builds the semantic_models block: a primary entity per PK column,
// every non-PK/non-FK dimension as a semantic dimension (FK columns round-trip
// as plain model columns + the relationship test, so they are NOT re-emitted as
// entities), and every measure.
func emitSemantic(t ir.Table, pk, fk map[string]bool) dbtEmitSemantic {
	sm := dbtEmitSemantic{Name: t.Name, Model: "ref('" + t.Name + "')"}
	if t.Grain != "" {
		sm.Defaults = &dbtEmitDefaults{AggTimeDimension: t.Grain}
	}
	for _, col := range t.PrimaryKey {
		sm.Entities = append(sm.Entities, dbtEmitEntity{Name: col, Type: "primary"})
	}
	for _, d := range t.Dimensions {
		if pk[d.Expr] || fk[d.Expr] {
			continue
		}
		sm.Dimensions = append(sm.Dimensions, dbtEmitDimension{
			Name: d.Name, Type: "categorical", Expr: exprIfDiffers(d.Name, d.Expr),
		})
	}
	for _, d := range t.TimeDimensions {
		sm.Dimensions = append(sm.Dimensions, dbtEmitDimension{
			Name: d.Name, Type: "time", Expr: exprIfDiffers(d.Name, d.Expr),
		})
	}
	for _, ms := range t.Measures {
		sm.Measures = append(sm.Measures, dbtEmitMeasure{Name: ms.Name, Agg: ms.Agg, Expr: ms.Expr})
	}
	return sm
}

// emitMetrics reverse-maps each Table.Metric to its dbt metric form: a simple
// metric points at the backing measure (found by matching Agg + expr), carrying
// its filter when present; a ratio carries numerator/denominator; a derived
// metric re-renders its arithmetic tree; cumulative/conversion re-emit their
// (provisional) params.
func emitMetrics(t ir.Table) []dbtEmitMetric {
	var out []dbtEmitMetric
	for _, mt := range t.Metrics {
		switch def := mt.Def.(type) {
		case ir.Agg:
			meas, ok := measureFor(t, def)
			if !ok {
				// Intentional drop: measureFor is guaranteed to find a backing
				// measure for dbt-sourced IR; a genuine miss here would be a bug
				// caught by TestDBTRoundTrip, not an expected case to handle.
				continue
			}
			em := dbtEmitMetric{
				Name: mt.Name, Label: mt.Label, Type: "simple", Description: mt.Description,
				TypeParams: dbtEmitTypeParams{Measure: meas},
			}
			if def.Filter != nil {
				em.Filter = emitFilterSQL(def.Filter)
			}
			out = append(out, em)
		case ir.Binary:
			// A plain ratio (Ref / Ref) round-trips as type: ratio; any other
			// arithmetic tree is a derived metric.
			if def.Op == "/" {
				if l, lok := def.Left.(ir.Ref); lok {
					if r, rok := def.Right.(ir.Ref); rok {
						out = append(out, dbtEmitMetric{
							Name: mt.Name, Label: mt.Label, Type: "ratio", Description: mt.Description,
							TypeParams: dbtEmitTypeParams{Numerator: l.Metric, Denominator: r.Metric},
						})
						continue
					}
				}
			}
			expr, refs, ok := renderDerived(def)
			if !ok {
				// Intentional drop: every derived operand originates from a
				// Ref/Lit/Binary tree built from dbt-sourced IR, so renderDerived
				// is expected to succeed; a genuine miss is caught by
				// TestDBTRoundTrip, not an expected case to handle.
				continue
			}
			var metrics []dbtEmitMetricRef
			for _, r := range refs {
				metrics = append(metrics, dbtEmitMetricRef{Name: r})
			}
			out = append(out, dbtEmitMetric{
				Name: mt.Name, Label: mt.Label, Type: "derived", Description: mt.Description,
				TypeParams: dbtEmitTypeParams{Expr: expr, Metrics: metrics},
			})
		case ir.Window: // PROVISIONAL
			base := ""
			if r, ok := def.Base.(ir.Ref); ok {
				base = r.Metric
			}
			out = append(out, dbtEmitMetric{
				Name: mt.Name, Label: mt.Label, Type: "cumulative", Description: mt.Description,
				TypeParams: dbtEmitTypeParams{Measure: base, Window: def.Window, Grain: def.Grain},
			})
		case ir.Conversion: // PROVISIONAL
			base, _ := def.Base.(ir.Ref)
			conv, _ := def.Conv.(ir.Ref)
			out = append(out, dbtEmitMetric{
				Name: mt.Name, Label: mt.Label, Type: "conversion", Description: mt.Description,
				TypeParams: dbtEmitTypeParams{ConversionTypeParams: &dbtEmitConversionParams{
					BaseMeasure: base.Metric, ConversionMeasure: conv.Metric, Entity: def.Entity, Window: def.Window,
				}},
			})
		}
	}
	return out
}

// emitFilterSQL renders a metric filter Expr back to the dbt filter: string form
// (unqualified, exactly as Parse read it).
func emitFilterSQL(e ir.Expr) string {
	switch f := e.(type) {
	case ir.Col:
		return f.Name
	case ir.Raw:
		return f.SQL
	default:
		return renderSQL(e, func(string) (ir.Expr, bool) { return nil, false })
	}
}

// renderDerived re-renders a derived arithmetic tree to a dbt expr string and the
// distinct metric names it references. Binary operands are parenthesized so the
// re-parse reconstructs the same grouping. ok=false if a node is not a
// Ref/Lit/Binary (nothing else is a valid derived operand).
func renderDerived(e ir.Expr) (expr string, refs []string, ok bool) {
	switch n := e.(type) {
	case ir.Ref:
		return n.Metric, []string{n.Metric}, true
	case ir.Lit:
		return n.Value, nil, true
	case ir.Binary:
		ls, lr, lok := renderDerived(n.Left)
		rs, rr, rok := renderDerived(n.Right)
		if !lok || !rok {
			return "", nil, false
		}
		return parenIfBinary(n.Left, ls) + " " + n.Op + " " + parenIfBinary(n.Right, rs),
			dedupeStrs(append(lr, rr...)), true
	default:
		return "", nil, false
	}
}

// parenIfBinary decides whether a derived operand needs parens when re-emitted
// as a dbt expr string. Deliberately separate from render_sql.go's
// renderOperand/isCompound: this emitter preserves metric names as bare refs
// (nothing is inlined, so a Ref is never compound), while Cortex inlines
// referenced metrics' SQL in place (so a Ref may resolve to a compound
// expression). Do not merge these — doing so would either start dbt inlining
// metric names or stop Cortex from inlining them, breaking derived-metric
// emit for one of the two targets.
func parenIfBinary(e ir.Expr, s string) string {
	if _, ok := e.(ir.Binary); ok {
		return "(" + s + ")"
	}
	return s
}

func dedupeStrs(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// measureFor finds the table measure backing a simple metric's aggregation: the
// measure whose Agg matches Func and whose expr matches the Agg's Arg (a Col's
// Name or a Raw's SQL). Guaranteed to exist for dbt-sourced models.
func measureFor(t ir.Table, a ir.Agg) (string, bool) {
	want := ""
	switch arg := a.Arg.(type) {
	case ir.Col:
		want = arg.Name
	case ir.Raw:
		want = arg.SQL
	}
	for _, ms := range t.Measures {
		if strings.EqualFold(ms.Agg, a.Func) && ms.Expr == want {
			return ms.Name, true
		}
	}
	return "", false
}

// fkColumns returns the set of left-side columns of every relationship whose
// Left is table — the foreign-key columns of this table.
func fkColumns(m *ir.Model, table string) map[string]bool {
	fk := map[string]bool{}
	for _, r := range m.Relationships {
		if r.Left != table {
			continue
		}
		for _, cp := range r.Columns {
			fk[cp.Left] = true
		}
	}
	return fk
}

func stringSet(ss []string) map[string]bool {
	s := make(map[string]bool, len(ss))
	for _, x := range ss {
		s[x] = true
	}
	return s
}

// exprIfDiffers returns expr when it differs from name (so the parser's
// expr-defaults-to-name rule reconstructs it), otherwise "" to omit it.
func exprIfDiffers(name, expr string) string {
	if expr == name {
		return ""
	}
	return expr
}
