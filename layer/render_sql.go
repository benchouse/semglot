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
			arg = "case when " + renderSQL(n.Filter, resolve) + " then " + arg + " end"
		}
		return aggExpr(n.Func, arg)
	case ir.Binary:
		return renderSQL(n.Left, resolve) + " " + n.Op + " " + renderSQL(n.Right, resolve)
	case ir.Window:
		return renderSQL(n.Base, resolve) // best-effort; Cortex window handled in Task 3
	case ir.Conversion:
		return "" // no SQL rendering; degraded by callers
	default:
		return ""
	}
}

// colSet lowercases a column list into a set for qualifyExpr/toPropertySQL.
func colSet(cols []string) map[string]bool {
	m := make(map[string]bool, len(cols))
	for _, c := range cols {
		m[strings.ToLower(c)] = true
	}
	return m
}
