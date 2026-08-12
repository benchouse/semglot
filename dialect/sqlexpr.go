package dialect

import (
	"strings"

	"github.com/benchouse/semglot/ir"
)

// A shared recursive-descent parser over sqlTokens, used by every dialect that
// reads an expression string into an ir.Expr tree.
//
// The grammar (parens, then * /, then + -) is common to all callers. What
// differs is the LEAF rule: a dbt derived expression names other METRICS, while
// an ossie metric expression names COLUMNS and may wrap them in aggregate
// calls. Callers supply their own leaf rule and opt into call parsing, rather
// than sharing one permissive grammar — see parseDerivedExpr's comment for why
// that separation is load-bearing.

// exprParser is a minimal recursive-descent parser over sqlTokens.
type exprParser struct {
	toks []sqlToken
	pos  int
	err  bool
	// leaf builds the Expr for an identifier token. It is responsible for
	// advancing pos past everything it consumes.
	leaf func(p *exprParser) ir.Expr
	// calls enables SUM(...)/COUNT(...) parsing. Off for dbt: enabling it there
	// would silently reinterpret an expression that currently degrades to a note.
	calls bool
}

// tokenize splits expr into tokens with whitespace dropped.
func tokenize(expr string) []sqlToken {
	var toks []sqlToken
	for _, tk := range sqlTokens(expr) {
		if tk.typ == sqlOther && strings.TrimSpace(tk.val) == "" {
			continue
		}
		toks = append(toks, tk)
	}
	return toks
}

func (p *exprParser) peek() (sqlToken, bool) { return p.peekAt(0) }

func (p *exprParser) peekAt(n int) (sqlToken, bool) {
	if p.pos+n < len(p.toks) {
		return p.toks[p.pos+n], true
	}
	return sqlToken{}, false
}

func (p *exprParser) isOp(want ...string) (string, bool) {
	tk, ok := p.peek()
	if !ok || tk.typ != sqlOther {
		return "", false
	}
	for _, w := range want {
		if tk.val == w {
			return w, true
		}
	}
	return "", false
}

func (p *exprParser) parseAddSub() ir.Expr {
	left := p.parseMulDiv()
	for {
		op, ok := p.isOp("+", "-")
		if !ok {
			return left
		}
		p.pos++
		left = ir.Binary{Op: op, Left: left, Right: p.parseMulDiv()}
	}
}

func (p *exprParser) parseMulDiv() ir.Expr {
	left := p.parseFactor()
	for {
		op, ok := p.isOp("*", "/")
		if !ok {
			return left
		}
		p.pos++
		left = ir.Binary{Op: op, Left: left, Right: p.parseFactor()}
	}
}

func (p *exprParser) parseFactor() ir.Expr {
	tk, ok := p.peek()
	if !ok {
		p.err = true
		return nil
	}
	switch {
	case tk.typ == sqlOther && tk.val == "(":
		p.pos++
		e := p.parseAddSub()
		if _, ok := p.isOp(")"); !ok {
			p.err = true
			return nil
		}
		p.pos++
		return e
	case tk.typ == sqlIdent:
		return p.leaf(p)
	case tk.typ == sqlNumber:
		p.pos++
		return ir.Lit{Value: tk.val}
	default:
		p.err = true
		return nil
	}
}

// parseDerivedExpr parses a dbt derived-metric expression (arithmetic over
// metric names and numeric literals: + - * / with precedence and parens) into an
// ir.Binary/Ref/Lit tree. ok=false if the expression is not cleanly parseable as
// such (the caller then degrades it to a note).
//
// Its leaf rule maps a bare identifier to ir.Ref and it does NOT enable call
// parsing. Keeping that separate from ossie's leaf rule is deliberate: if this
// parser started accepting SUM(...), a dbt derived expression that today
// degrades to a note would instead parse into something else — a silent
// behaviour change in a shipped dialect.
func parseDerivedExpr(expr string) (ir.Expr, bool) {
	toks := tokenize(expr)
	if len(toks) == 0 {
		return nil, false
	}
	p := &exprParser{toks: toks, leaf: derivedLeaf}
	e := p.parseAddSub()
	if p.err || p.pos != len(p.toks) {
		return nil, false
	}
	return e, true
}

// derivedLeaf maps a bare identifier to a metric reference.
func derivedLeaf(p *exprParser) ir.Expr {
	tk, _ := p.peek()
	p.pos++
	return ir.Ref{Metric: tk.val}
}
