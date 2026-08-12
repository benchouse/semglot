package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// emitOssie emits m and returns the parsed semantic_model.yaml plus its raw text.
func emitOssie(t *testing.T, m *ir.Model, opts Options) (osiFile, string) {
	t.Helper()
	e := ossie{}.WithOptions(opts)
	out := t.TempDir()
	if _, err := e.Emit(m, out); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	return f, string(b)
}

func TestOssieEmitDatasets(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name:        "orders",
		Description: "Customer orders.",
		Synonyms:    []string{"purchases"},
		PrimaryKey:  []string{"order_id", "line_number"},
		Grain:       "order_date",
		Dimensions: []ir.Field{{
			Name: "status", Expr: "status", DataType: "varchar",
			Description: "Order status.",
			Synonyms:    []string{"state"},
			Enum:        []ir.EnumValue{{Value: "placed"}, {Value: "shipped", Description: "left the warehouse"}},
		}},
		TimeDimensions: []ir.Field{{Name: "order_date", Expr: "order_date", DataType: "date"}},
	}}}

	f, raw := emitOssie(t, m, Options{Database: "ANALYTICS", Schema: "MAIN", Name: "sales", Description: "Sales."})

	if f.Version != "0.2.0.dev0" {
		t.Errorf("version = %q, want 0.2.0.dev0", f.Version)
	}
	if len(f.SemanticModel) != 1 {
		t.Fatalf("want 1 semantic_model entry, got %d", len(f.SemanticModel))
	}
	sm := f.SemanticModel[0]
	if sm.Name != "sales" || sm.Description != "Sales." {
		t.Errorf("model identity = %q / %q", sm.Name, sm.Description)
	}
	if len(sm.Datasets) != 1 {
		t.Fatalf("want 1 dataset, got %d", len(sm.Datasets))
	}
	ds := sm.Datasets[0]
	if ds.Source != "ANALYTICS.MAIN.orders" {
		t.Errorf("source = %q, want ANALYTICS.MAIN.orders", ds.Source)
	}
	if len(ds.PrimaryKey) != 2 {
		t.Errorf("primary_key = %v, want both columns", ds.PrimaryKey)
	}
	if ds.AIContext == nil || len(ds.AIContext.Synonyms) != 1 {
		t.Errorf("dataset ai_context.synonyms missing: %+v", ds.AIContext)
	}
	// Grain has no OSI slot and folds into the dataset description.
	if !strings.Contains(ds.Description, "order_date") {
		t.Errorf("dataset description missing the grain: %q", ds.Description)
	}

	byName := map[string]osiField{}
	for _, fl := range ds.Fields {
		byName[fl.Name] = fl
	}
	status, ok := byName["status"]
	if !ok {
		t.Fatalf("no status field in %v", ds.Fields)
	}
	if status.DataType != "String" {
		t.Errorf("status datatype = %q, want String", status.DataType)
	}
	if status.Dimension == nil || status.Dimension.IsTime == nil || *status.Dimension.IsTime {
		t.Errorf("status should be marked is_time: false, got %+v", status.Dimension)
	}
	// Enum has no OSI slot and folds into the field description.
	if !strings.Contains(status.Description, "placed") {
		t.Errorf("status description missing folded enum: %q", status.Description)
	}
	od, ok := byName["order_date"]
	if !ok {
		t.Fatalf("no order_date field")
	}
	if od.Dimension == nil || od.Dimension.IsTime == nil || !*od.Dimension.IsTime {
		t.Errorf("order_date should be marked is_time: true, got %+v", od.Dimension)
	}

	// The reference converter emits a top-level dialects: key that the published
	// osi-schema.json rejects (additionalProperties: false). semglot must not.
	if strings.Contains(raw, "\ndialects:") {
		t.Errorf("emitted a top-level dialects: key, which osi-schema.json rejects:\n%s", raw)
	}
}

// TestOssieEmitUnqualifiedSource degrades gracefully when no database is set.
func TestOssieEmitUnqualifiedSource(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "orders"}}}
	f, _ := emitOssie(t, m, Options{Name: "sales"})
	if got := f.SemanticModel[0].Datasets[0].Source; got != "orders" {
		t.Errorf("source = %q, want bare table name when no database is set", got)
	}
}

// TestOssieEmitUnmappedDataType covers a SQL type the IR carries but that has
// no OSI portable-vocabulary equivalent (irToOSIType returns ""). Omitting
// datatype in that case is correct per spec guidance, but silently dropping
// the fact that the IR *did* have a type we couldn't map would violate the
// "nothing dropped silently" rule — so Emit must return a warning naming the
// field and the unmapped type, in addition to omitting datatype from the YAML.
func TestOssieEmitUnmappedDataType(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "events",
		Dimensions: []ir.Field{{
			Name: "payload", Expr: "payload", DataType: "variant",
		}},
	}}}

	e := ossie{}.WithOptions(Options{Name: "evt"})
	out := t.TempDir()
	warnings, err := e.Emit(m, out)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}

	found := false
	for _, w := range warnings {
		if strings.Contains(w, "payload") && strings.Contains(w, "variant") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %v, want one naming field %q and unmapped type %q", warnings, "payload", "variant")
	}

	b, err := os.ReadFile(filepath.Join(out, "semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var f osiFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		t.Fatalf("emitted YAML does not parse: %v", err)
	}
	fld := f.SemanticModel[0].Datasets[0].Fields[0]
	if fld.DataType != "" {
		t.Errorf("datatype = %q, want omitted for an unmapped SQL type", fld.DataType)
	}
}
