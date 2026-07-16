package dialect

// A minimal SQL-expression lexer. It is NOT a full SQL parser — just enough to
// split an expression into a faithful token stream so callers can rewrite
// column references without touching string literals, numbers, or punctuation.
//
// Keyword status is deliberately NOT tracked: callers decide what to rewrite by
// column-set membership (see qualifyExpr / toPropertySQL), not by keyword
// classification. That removes the entire class of "incomplete keyword table"
// bugs — a word like WHEN is simply an identifier that is not a known column.

type sqlTokenType int

const (
	sqlEOF    sqlTokenType = iota
	sqlIdent               // bare identifier: [A-Za-z_][A-Za-z0-9_]*
	sqlString              // 'single' or "double" quoted span (literal / quoted ident)
	sqlNumber              // numeric literal
	sqlOther               // whitespace, operators, punctuation — anything else
)

type sqlToken struct {
	typ sqlTokenType
	val string
}

// sqlTokens splits expr into tokens whose values, concatenated in order,
// reproduce expr exactly (a faithful round-trip).
func sqlTokens(expr string) []sqlToken {
	rs := []rune(expr)
	n := len(rs)
	var toks []sqlToken
	for i := 0; i < n; {
		c := rs[i]
		switch {
		case c == '\'' || c == '"':
			// Quoted span; a doubled quote ('' or "") is an escape, not a close.
			quote := c
			j := i + 1
			for j < n {
				if rs[j] == quote {
					if j+1 < n && rs[j+1] == quote {
						j += 2
						continue
					}
					j++ // include the closing quote
					break
				}
				j++
			}
			toks = append(toks, sqlToken{sqlString, string(rs[i:j])})
			i = j
		case isIdentStartRune(c):
			j := i + 1
			for j < n && isIdentPartRune(rs[j]) {
				j++
			}
			toks = append(toks, sqlToken{sqlIdent, string(rs[i:j])})
			i = j
		case c >= '0' && c <= '9':
			j := i + 1
			for j < n && (rs[j] >= '0' && rs[j] <= '9' || rs[j] == '.') {
				j++
			}
			toks = append(toks, sqlToken{sqlNumber, string(rs[i:j])})
			i = j
		default:
			toks = append(toks, sqlToken{sqlOther, string(c)})
			i++
		}
	}
	return toks
}

func isIdentStartRune(c rune) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPartRune(c rune) bool {
	return isIdentStartRune(c) || (c >= '0' && c <= '9')
}
