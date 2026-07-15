package layer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

func emitSV(t *testing.T, e Emitter) string {
	t.Helper()
	dir := t.TempDir()
	m := &ir.Model{Tables: []ir.Table{{
		Name:       "fct_orders",
		PrimaryKey: []string{"order_id"},
		Dimensions: []ir.Field{{Name: "order_date", Expr: "order_date"}},
	}}}
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "definition.md"))
	if err != nil {
		t.Fatalf("read definition.md: %v", err)
	}
	return string(b)
}

// TestSVViewSchemaSplit verifies the semantic-view OBJECT is created in
// ViewSchema (SEM) while its TABLES reference the source Schema (MAIN) — the two
// must be independently controllable so the view can live in a curated schema
// over base tables in another.
func TestSVViewSchemaSplit(t *testing.T) {
	e := snowflakeSemanticView{}.WithOptions(Options{
		Database:   "EVAL_MARTS",
		Schema:     "MAIN",
		ViewSchema: "SEM",
		Name:       "SV_ECOMM",
	})
	out := emitSV(t, e)

	if !strings.Contains(out, "create or replace semantic view EVAL_MARTS.SEM.SV_ECOMM") {
		t.Errorf("view object must be qualified in ViewSchema (EVAL_MARTS.SEM.SV_ECOMM):\n%s", out)
	}
	if !strings.Contains(out, "EVAL_MARTS.MAIN.FCT_ORDERS") {
		t.Errorf("table refs must use the source Schema (EVAL_MARTS.MAIN.FCT_ORDERS):\n%s", out)
	}
	if strings.Contains(out, "EVAL_MARTS.SEM.FCT_ORDERS") {
		t.Errorf("table refs must NOT be placed in ViewSchema:\n%s", out)
	}
}

// TestSVViewSchemaFallback verifies ViewSchema falls back to Schema when unset,
// so callers that don't need a split keep the single-schema behavior.
func TestSVViewSchemaFallback(t *testing.T) {
	e := snowflakeSemanticView{}.WithOptions(Options{
		Database: "EVAL_MARTS",
		Schema:   "MAIN",
		Name:     "SV_ECOMM",
	})
	out := emitSV(t, e)
	if !strings.Contains(out, "create or replace semantic view EVAL_MARTS.MAIN.SV_ECOMM") {
		t.Errorf("with no ViewSchema the view should fall back to Schema (EVAL_MARTS.MAIN.SV_ECOMM):\n%s", out)
	}
}

// TestSVUnqualifiedWithoutDatabase verifies that with no database the view name
// stays unqualified (keeps zero-value output valid rather than emitting a
// leading-dot name).
func TestSVUnqualifiedWithoutDatabase(t *testing.T) {
	out := emitSV(t, snowflakeSemanticView{}.WithOptions(Options{Name: "SV_ECOMM"}))
	if !strings.Contains(out, "create or replace semantic view SV_ECOMM\n") {
		t.Errorf("with no database the view name should be unqualified:\n%s", out)
	}
}
