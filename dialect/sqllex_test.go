package dialect

import "testing"

func TestSQLTokensFaithfulRoundTrip(t *testing.T) {
	for _, expr := range []string{
		"case when is_refunded then 1 else 0 end",
		"case when status = 'status' then 1 else 0 end",
		"sum(order_gross) / count(distinct order_id)",
		"coalesce(refund_total, 0) * 100.0",
		`"Order Id"`,
		"case when name = 'O''Brien' then 1 else 0 end", // doubled-quote escape
		`select "a""b" from t`,                          // doubled double-quote
		"'unterminated",                                 // unterminated string
		"a.b.c",
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

func TestSQLTokensTable(t *testing.T) {
	type tk struct {
		typ sqlTokenType
		val string
	}
	cases := []struct {
		name string
		in   string
		want []tk
	}{
		{"bare ident", "order_id", []tk{{sqlIdent, "order_id"}}},
		{"ident with digits", "col2", []tk{{sqlIdent, "col2"}}},
		{"integer", "100", []tk{{sqlNumber, "100"}}},
		{"decimal", "100.0", []tk{{sqlNumber, "100.0"}}},
		{"single-quoted string", "'status'", []tk{{sqlString, "'status'"}}},
		{"double-quoted ident", `"Order Id"`, []tk{{sqlString, `"Order Id"`}}},
		{"doubled single-quote escape", "'it''s'", []tk{{sqlString, "'it''s'"}}},
		{"doubled double-quote escape", `"a""b"`, []tk{{sqlString, `"a""b"`}}},
		{"unterminated string to EOF", "'abc", []tk{{sqlString, "'abc"}}},
		{"member access splits on dot", "a.b", []tk{{sqlIdent, "a"}, {sqlOther, "."}, {sqlIdent, "b"}}},
		{"operator between idents", "a/b", []tk{{sqlIdent, "a"}, {sqlOther, "/"}, {sqlIdent, "b"}}},
	}
	for _, c := range cases {
		got := sqlTokens(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %d tokens %+v, want %d", c.name, len(got), got, len(c.want))
		}
		for i := range got {
			if got[i].typ != c.want[i].typ || got[i].val != c.want[i].val {
				t.Fatalf("%s tok %d: got {typ=%d %q}, want {typ=%d %q}", c.name, i, got[i].typ, got[i].val, c.want[i].typ, c.want[i].val)
			}
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
