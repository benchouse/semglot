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
	Name string
	// Source is the physical table this entity reads, as the source dialect
	// declared it (e.g. "PROD.SALES.orders_v1"). Empty when the source has no
	// physical address of its own — dbt models resolve theirs through ref() —
	// in which case emitters reconstruct it from the profile's database/schema.
	Source      string
	Description string
	// Synonyms are alternative names for the entity itself, as distinct from a
	// field's synonyms. Sourced from a model-level meta.synonyms (dbt) or a
	// dataset's ai_context.synonyms (ossie). Emitters render it structurally
	// where the target has a table-level slot, else fold it into the table
	// description.
	Synonyms       []string
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
	// Enum lists the field's allowed categorical values, optionally documented.
	// Sourced from a superset of the value list (e.g. dbt accepted_values) and a
	// value→description map (e.g. dbt meta.enum). Emitters render it structurally
	// where the target supports per-value enums, else fold it into Description.
	Enum []EnumValue
}

// EnumValue is one allowed value of a categorical field, with an optional
// human description of what the value means.
type EnumValue struct {
	Value       string
	Description string
}

// Measure is an aggregatable fact. Agg is the source aggregation (sum, count,
// count_distinct, avg, ...).
type Measure struct {
	Field
	Agg string
}

// Metric is a named business calculation. Def is its definition as an expression
// AST — the single source of truth; each emitter lowers Def to its target form.
type Metric struct {
	Name        string
	Label       string // dbt metric label (display name); "" if none
	Description string
	Synonyms    []string
	Grain       string   // per-metric agg-time grain (owning model's agg_time_dimension); "" if none
	Dimensions  []string // slice-by dimensions; nil if unspecified
	Def         Expr     // the definition AST
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
