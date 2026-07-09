// Package ir is semglot's neutral intermediate representation of a semantic
// layer. Every dialect parses into it and emits out of it, so it is the pivot
// for transpilation (and, in v2, the unit of the fairness index).
package ir

// Model is a whole semantic layer.
type Model struct {
	Tables        []Table
	Relationships []Relationship
	// Notes are free-text passthrough annotations for source constructs that
	// could not be represented structurally (e.g. a metric referencing an
	// unknown measure, or an unsupported metric type). Emitters may surface
	// them as target-native guidance (e.g. Cortex custom_instructions).
	Notes []string
}

// Table is one grain/entity in the layer.
type Table struct {
	Name           string
	Description    string
	PrimaryKey     []string // column exprs
	Dimensions     []Field  // categorical / id / plain
	TimeDimensions []Field
	Measures       []Measure
	Metrics        []Metric
	Grain          string // default time-dimension (dbt defaults.agg_time_dimension); "" if none
}

// Field is a column-backed attribute. DataType is left empty by dialects (like
// dbt) that do not record SQL types; emitters may infer it.
type Field struct {
	Name        string
	Description string
	DataType    string
	Expr        string // underlying column/expression
	Synonyms    []string
}

// Measure is an aggregatable fact. Agg is the source aggregation (sum, count,
// count_distinct, avg, ...).
type Measure struct {
	Field
	Agg string
}

// Metric is a named business calculation. Expr is the rendered SQL (used by
// SQL-shaped targets like Cortex); the structured fields let other targets
// (e.g. supersimple) build their own form without re-parsing SQL.
type Metric struct {
	Name        string
	Label       string // dbt metric label (display name); "" if none
	Description string
	Expr        string
	Synonyms    []string
	Kind        string // "simple" | "ratio"
	Agg         string // simple: sum | count | count_distinct | avg | min | max
	Table       string // owning table (model) name
	Column      string // simple: aggregated column (bare) or the raw expr if compound
	Numerator   string // ratio: numerator metric name
	Denominator string // ratio: denominator metric name
}

// Relationship is a join between two tables.
type Relationship struct {
	Left    string
	Right   string
	Columns []ColumnPair
}

// ColumnPair is one equi-join column pairing (left_column = right_column).
type ColumnPair struct {
	Left  string
	Right string
}
