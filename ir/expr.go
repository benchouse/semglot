package ir

// Expr is a node in a metric-definition expression tree.
type Expr interface{ isExpr() }

// Col is a physical column reference. Table may be "" when unqualified.
type Col struct{ Table, Name string }

// Raw is an opaque SQL fragment not decomposed (e.g. a CASE expression).
// Columns lists the table's column names (lowercased, sorted) so a renderer can
// qualify (Cortex) or wrap (supersimple) the identifiers it references.
type Raw struct {
	SQL     string
	Columns []string
}

// Ref references another metric by name.
type Ref struct{ Metric string }

// Lit is an already-rendered literal ("1", "0", "100.0", "'x'").
type Lit struct{ Value string }

// Agg is an aggregation over a sub-expression, optionally filtered. Table is the
// owning table, used to qualify a Raw arg's columns at render time.
type Agg struct {
	Func   string // sum | count | count_distinct | avg | min | max | median
	Table  string // owning table (qualifies a Raw arg's columns)
	Arg    Expr   // usually Col or Raw; nil for count(*)
	Filter Expr   // optional boolean Expr; nil if none
}

// Binary is an arithmetic/comparison/logical operation.
type Binary struct {
	Op          string // "+" "-" "*" "/" "=" "<" ">" "and" "or"
	Left, Right Expr
}

// Window wraps a base expression with a time window (cumulative/to-date).
type Window struct {
	Base   Expr
	Window string // e.g. "30 days"; "" = unbounded to-date
	Grain  string
}

// Conversion is a funnel between a base and conversion measure over a window.
type Conversion struct {
	Base, Conv Expr
	Entity     string
	Window     string
}

func (Col) isExpr()        {}
func (Raw) isExpr()        {}
func (Ref) isExpr()        {}
func (Lit) isExpr()        {}
func (Agg) isExpr()        {}
func (Binary) isExpr()     {}
func (Window) isExpr()     {}
func (Conversion) isExpr() {}
