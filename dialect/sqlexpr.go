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

// aggFuncs maps a recognised SQL aggregate name (lowercased) to the ir.Agg.Func
// value semglot uses. COUNT(DISTINCT x) is special-cased to count_distinct in
// parseCall.
var aggFuncs = map[string]string{
	"sum": "sum", "count": "count", "avg": "avg",
	"min": "min", "max": "max", "median": "median",
}

// parseAggExpr parses an OSI metric expression — aggregate calls over columns,
// combined with + - * / and parens — into an ir.Agg/Binary/Col/Lit tree.
// ok=false when the expression is not cleanly parseable, so the caller degrades
// it to a note rather than guessing.
//
// An aggregate's argument that is not a plain column becomes an ir.Raw carrying
// the verbatim source text (e.g. a CASE expression, which Ossie's own dbt
// converter emits for a filtered measure). Raw.Columns is left nil here; the
// caller fills it once it knows which table owns the metric.
func parseAggExpr(expr string) (ir.Expr, bool) {
	toks := tokenize(expr)
	if len(toks) == 0 {
		return nil, false
	}
	p := &exprParser{toks: toks, leaf: aggLeaf, calls: true}
	e := p.parseAddSub()
	if p.err || p.pos != len(p.toks) {
		return nil, false
	}
	return e, true
}

// aggLeaf maps an identifier to a column reference, consuming an optional
// `.column` qualifier. When calls are enabled and the identifier is followed by
// `(`, it is an aggregate call instead.
func aggLeaf(p *exprParser) ir.Expr {
	tk, _ := p.peek()
	if next, ok := p.peekAt(1); p.calls && ok && next.typ == sqlOther && next.val == "(" {
		return p.parseCall()
	}
	p.pos++
	// table.column
	if dot, ok := p.peek(); ok && dot.typ == sqlOther && dot.val == "." {
		if col, ok := p.peekAt(1); ok && col.typ == sqlIdent {
			p.pos += 2
			return ir.Col{Table: tk.val, Name: col.val}
		}
	}
	return ir.Col{Name: tk.val}
}

// parseCall parses `FUNC(...)` at the current position. Only known aggregates
// are accepted; anything else fails the parse so the caller degrades it.
func (p *exprParser) parseCall() ir.Expr {
	name, _ := p.peek()
	fn, known := aggFuncs[strings.ToLower(name.val)]
	if !known {
		p.err = true
		return nil
	}
	open := p.pos + 1
	closeIdx, ok := p.matchParen(open)
	if !ok {
		p.err = true
		return nil
	}
	inner := p.toks[open+1 : closeIdx]
	p.pos = closeIdx + 1

	// COUNT(*) -- only valid for count. SUM(*) etc. is not valid SQL, so reject
	// it outright rather than let it fall through to the Raw fallback below,
	// which would otherwise accept it as Agg{Arg: Raw{SQL: "*"}}.
	if len(inner) == 1 && inner[0].typ == sqlOther && inner[0].val == "*" {
		if fn != "count" {
			p.err = true
			return nil
		}
		return ir.Agg{Func: fn, Arg: nil}
	}
	// COUNT(DISTINCT x). For any other aggregate, ir.Agg.Func has no distinct
	// flag, so there is no lossless way to drop the keyword here: leave it in
	// inner and let the Raw fallback below preserve it verbatim instead of
	// silently discarding it.
	if fn == "count" && len(inner) > 1 && inner[0].typ == sqlIdent && strings.EqualFold(inner[0].val, "distinct") {
		fn = "count_distinct"
		inner = inner[1:]
	}
	if arg, ok := parseColTokens(inner); ok {
		return ir.Agg{Func: fn, Arg: arg}
	}
	return ir.Agg{Func: fn, Arg: ir.Raw{SQL: joinTokens(inner)}}
}

// matchParen returns the index of the `)` closing the `(` at open, honouring
// nesting. ok=false when unbalanced.
func (p *exprParser) matchParen(open int) (int, bool) {
	if open >= len(p.toks) || p.toks[open].typ != sqlOther || p.toks[open].val != "(" {
		return 0, false
	}
	depth := 0
	for i := open; i < len(p.toks); i++ {
		tk := p.toks[i]
		if tk.typ != sqlOther {
			continue
		}
		switch tk.val {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// parseColTokens reads `column` or `table.column` from a complete token slice.
// ok=false if the slice is anything else, so the caller falls back to ir.Raw.
func parseColTokens(toks []sqlToken) (ir.Expr, bool) {
	switch len(toks) {
	case 1:
		if toks[0].typ == sqlIdent {
			return ir.Col{Name: toks[0].val}, true
		}
	case 3:
		if toks[0].typ == sqlIdent && toks[1].typ == sqlOther && toks[1].val == "." && toks[2].typ == sqlIdent {
			return ir.Col{Table: toks[0].val, Name: toks[2].val}, true
		}
	}
	return nil, false
}

// joinTokens reproduces the source text of a token span, separating identifiers
// and numbers with single spaces so a reconstructed SQL fragment stays readable
// and valid.
func joinTokens(toks []sqlToken) string {
	var sb strings.Builder
	for i, tk := range toks {
		if i > 0 && needsSpace(toks[i-1], tk) {
			sb.WriteByte(' ')
		}
		sb.WriteString(tk.val)
	}
	return sb.String()
}

// needsSpace reports whether two adjacent tokens must be separated to stay
// valid SQL: two word-ish tokens always, and never around punctuation.
func needsSpace(prev, cur sqlToken) bool {
	wordish := func(t sqlToken) bool {
		return t.typ == sqlIdent || t.typ == sqlNumber || t.typ == sqlString
	}
	if wordish(prev) && wordish(cur) {
		return true
	}
	// keep `x = 1` and `a > b` readable
	if prev.typ == sqlOther && strings.TrimSpace(prev.val) != "" && wordish(cur) && prev.val != "." && prev.val != "(" {
		return true
	}
	if wordish(prev) && cur.typ == sqlOther && cur.val != "." && cur.val != ")" && cur.val != "," {
		return true
	}
	return false
}
