package layer

import "testing"

func TestSQLTokensFaithfulRoundTrip(t *testing.T) {
	for _, expr := range []string{
		"case when is_refunded then 1 else 0 end",
		"case when status = 'status' then 1 else 0 end",
		"sum(order_gross) / count(distinct order_id)",
		"coalesce(refund_total, 0) * 100.0",
		`"Order Id"`,
		"",
	} {
		var got string
		for _, tk := range sqlTokens(expr) {
			got += tk.val
		}
		if got != expr {
			t.Fatalf("round-trip mismatch: got %q want %q", got, expr)
		}
	}
}

func TestSQLTokensClassification(t *testing.T) {
	// A string literal whose text equals a column name must be a string token,
	// not an identifier — so callers never rewrite inside it.
	toks := sqlTokens("case when status = 'status' then 1 else 0 end")
	var idents, strings []string
	for _, tk := range toks {
		switch tk.typ {
		case sqlIdent:
			idents = append(idents, tk.val)
		case sqlString:
			strings = append(strings, tk.val)
		}
	}
	// bare words (incl. keywords, which are just identifiers here) are IDENTs
	wantIdents := map[string]bool{"case": true, "when": true, "status": true, "then": true, "else": true, "end": true}
	for _, id := range idents {
		if !wantIdents[id] {
			t.Fatalf("unexpected ident %q", id)
		}
	}
	if len(strings) != 1 || strings[0] != "'status'" {
		t.Fatalf("string literal not isolated: %v", strings)
	}
	// the literal 'status' must NOT appear as an ident.
	for _, id := range idents {
		if id == "'status'" {
			t.Fatal("string literal leaked into idents")
		}
	}
}
