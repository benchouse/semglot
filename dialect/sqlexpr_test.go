package dialect

import (
	"reflect"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func TestParseAggExpr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want ir.Expr
	}{
		{
			name: "qualified sum",
			in:   "SUM(orders.amount)",
			want: ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "amount"}},
		},
		{
			name: "unqualified sum",
			in:   "SUM(o_totalprice)",
			want: ir.Agg{Func: "sum", Arg: ir.Col{Name: "o_totalprice"}},
		},
		{
			name: "count distinct",
			in:   "COUNT(DISTINCT customers.id)",
			want: ir.Agg{Func: "count_distinct", Arg: ir.Col{Table: "customers", Name: "id"}},
		},
		{
			name: "count star",
			in:   "COUNT(*)",
			want: ir.Agg{Func: "count", Arg: nil},
		},
		{
			name: "cross-dataset ratio",
			in:   "SUM(orders.amount) / COUNT(DISTINCT customers.id)",
			want: ir.Binary{
				Op:    "/",
				Left:  ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "amount"}},
				Right: ir.Agg{Func: "count_distinct", Arg: ir.Col{Table: "customers", Name: "id"}},
			},
		},
		{
			name: "nested arithmetic keeps precedence",
			in:   "SUM(orders.amount) - SUM(orders.cost_amount)",
			want: ir.Binary{
				Op:    "-",
				Left:  ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "amount"}},
				Right: ir.Agg{Func: "sum", Arg: ir.Col{Table: "orders", Name: "cost_amount"}},
			},
		},
		{
			name: "opaque call argument becomes Raw",
			in:   "SUM(CASE WHEN orders.order_id IS NOT NULL THEN 1 ELSE 0 END)",
			want: ir.Agg{Func: "sum", Arg: ir.Raw{SQL: "CASE WHEN orders.order_id IS NOT NULL THEN 1 ELSE 0 END"}},
		},
		{
			// Only COUNT(DISTINCT x) has a lossless ir.Agg.Func representation
			// (count_distinct). For every other aggregate the DISTINCT keyword
			// must be preserved verbatim via the Raw fallback rather than
			// silently dropped.
			name: "sum distinct becomes Raw",
			in:   "SUM(DISTINCT x)",
			want: ir.Agg{Func: "sum", Arg: ir.Raw{SQL: "DISTINCT x"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAggExpr(tc.in)
			if !ok {
				t.Fatalf("parseAggExpr(%q) returned ok=false", tc.in)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseAggExpr(%q)\n got: %#v\nwant: %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseAggExprRejects(t *testing.T) {
	// SUM(*) is not valid SQL -- only COUNT(*) is. It must fail to parse
	// rather than silently accept "*" as an ir.Raw argument.
	for _, in := range []string{"", "SUM(", "SUM(orders.amount", ")", "+ 1", "SUM(*)"} {
		if _, ok := parseAggExpr(in); ok {
			t.Errorf("parseAggExpr(%q) = ok, want failure", in)
		}
	}
}

// TestParseDerivedExprStillRejectsCalls guards the deliberate separation between
// the two leaf rules: enabling calls for ossie must not make dbt's derived
// parser accept them.
func TestParseDerivedExprStillRejectsCalls(t *testing.T) {
	if _, ok := parseDerivedExpr("SUM(revenue)"); ok {
		t.Error("parseDerivedExpr accepted a function call; dbt's grammar must stay call-free")
	}
}
