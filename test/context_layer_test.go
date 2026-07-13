package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/layer"
)

func emitTarget(t *testing.T, target, file string) string {
	t.Helper()
	e, err := layer.AsEmitter(target)
	if err != nil {
		t.Fatalf("AsEmitter(%s): %v", target, err)
	}
	if c, ok := e.(layer.Configurable); ok {
		e = c.WithOptions("ANALYTICS", "MAIN", "ecommerce", "")
	}
	p, err := layer.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.Parse(projectDir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := t.TempDir()
	if err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSemanticViewStructure(t *testing.T) {
	got := emitTarget(t, "snowflake-semantic-view", "definition.md")
	for _, want := range []string{
		"create or replace semantic view ECOMMERCE",
		"FCT_ORDERS as ANALYTICS.MAIN.FCT_ORDERS primary key (ORDER_ID)",
		"FCT_ORDER_LINES_FCT_ORDERS as FCT_ORDER_LINES(ORDER_ID) references FCT_ORDERS(ORDER_ID)",
		"FCT_ORDERS.AOV as SUM(FCT_ORDERS.ORDER_NET_BOOKED) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)",
		"FCT_ORDER_LINES.UNITS_PER_ORDER as SUM(FCT_ORDER_LINES.QUANTITY) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)",
		"FCT_ORDERS.REFUND_RATE as SUM(CASE WHEN FCT_ORDERS.IS_REFUNDED THEN 1 ELSE 0 END) / COUNT(DISTINCT FCT_ORDERS.ORDER_ID)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("definition.md missing %q", want)
		}
	}
}

// semanticViewGoldenPath is the pinned snowflake-semantic-view definition.md,
// generated with UPDATE_GOLDEN=1 and eyeballed for well-formed DDL.
const semanticViewGoldenPath = "models/ecommerce/dbt/snowflake-semantic-view/definition.md"

// TestSemanticViewGolden pins the full emitted definition.md, mirroring
// TestEcommerceCortexGolden's shape.
func TestSemanticViewGolden(t *testing.T) {
	got := emitTarget(t, "snowflake-semantic-view", "definition.md")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(semanticViewGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(semanticViewGoldenPath, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(semanticViewGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create it): %v", err)
	}
	if got != string(want) {
		t.Fatalf("definition.md output != golden:\n--- got ---\n%s", got)
	}
}
