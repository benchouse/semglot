package layer

import (
	"strings"

	"github.com/benchouse/semglot/ir"
)

// renderSQL lowers a metric-definition AST to a neutral, lowercase SQL string
// (Cortex uppercases it). resolve returns the Def of a referenced metric.
func renderSQL(e ir.Expr, resolve func(name string) (ir.Expr, bool)) string {
	switch n := e.(type) {
	case ir.Col:
		if n.Table != "" {
			return n.Table + "." + n.Name
		}
		return n.Name
	case ir.Raw:
		return n.SQL // unqualified; the enclosing Agg case qualifies it
	case ir.Lit:
		return n.Value
	case ir.Ref:
		if def, ok := resolve(n.Metric); ok {
			return renderSQL(def, resolve)
		}
		return n.Metric
	case ir.Agg:
		var arg string
		switch a := n.Arg.(type) {
		case ir.Raw: // qualify the raw fragment's columns with the owning table
			arg = qualifyExpr(n.Table, colSet(a.Columns), a.SQL)
		case nil:
			arg = ""
		default:
			arg = renderSQL(n.Arg, resolve)
		}
		if n.Filter != nil {
			var cond string
			// A Raw filter carries unqualified column refs; qualify them with the
			// owning table exactly like a Raw agg arg. Any other Expr (Col/Binary)
			// already renders qualified.
			if raw, ok := n.Filter.(ir.Raw); ok {
				cond = qualifyExpr(n.Table, colSet(raw.Columns), raw.SQL)
			} else {
				cond = renderSQL(n.Filter, resolve)
			}
			arg = "case when " + cond + " then " + arg + " end"
		}
		return aggExpr(n.Func, arg)
	case ir.Binary:
		return renderOperand(n.Left, resolve) + " " + n.Op + " " + renderOperand(n.Right, resolve)
	case ir.Window:
		return renderSQL(n.Base, resolve) // best-effort; Cortex window handled in Task 3
	case ir.Conversion:
		return "" // no SQL rendering; degraded by callers
	default:
		return ""
	}
}

// renderOperand renders a Binary operand, parenthesizing it when it is itself a
// compound (Binary) expression — directly or through a metric Ref — so operator
// precedence in the emitted SQL matches the AST's grouping.
func renderOperand(e ir.Expr, resolve func(name string) (ir.Expr, bool)) string {
	s := renderSQL(e, resolve)
	if isCompound(e, resolve) {
		return "(" + s + ")"
	}
	return s
}

func isCompound(e ir.Expr, resolve func(name string) (ir.Expr, bool)) bool {
	switch n := e.(type) {
	case ir.Binary:
		return true
	case ir.Ref:
		if def, ok := resolve(n.Metric); ok {
			return isCompound(def, resolve)
		}
	}
	return false
}

// colSet lowercases a column list into a set for qualifyExpr/toPropertySQL.
func colSet(cols []string) map[string]bool {
	m := make(map[string]bool, len(cols))
	for _, c := range cols {
		m[strings.ToLower(c)] = true
	}
	return m
}
