package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

// emitLightdash runs the lightdash emitter (with opts) over m and returns the
// emitted schema.yml as a string.
func emitLightdash(t *testing.T, m *ir.Model, opts Options) string {
	t.Helper()
	e := lightdash{}.WithOptions(opts)
	dir := t.TempDir()
	if err := e.Emit(m, dir); err != nil {
		t.Fatalf("emit: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatalf("read schema.yml: %v", err)
	}
	return string(b)
}

func TestLightdashRegisteredAndSkeleton(t *testing.T) {
	if _, err := AsEmitter("lightdash"); err != nil {
		t.Fatalf("AsEmitter(lightdash): %v", err)
	}
	m := &ir.Model{
		Tables: []ir.Table{{Name: "orders", Description: "One row per order."}},
		Notes:  []string{"a passthrough note"},
	}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	if !strings.HasPrefix(got, "# semglot:") {
		t.Errorf("expected leading # semglot comment block, got:\n%s", got)
	}
	if !strings.Contains(got, "# - a passthrough note") {
		t.Errorf("passthrough note missing from comment block:\n%s", got)
	}

	var doc struct {
		Version int `yaml:"version"`
		Models  []struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Version != 2 {
		t.Errorf("version = %d, want 2", doc.Version)
	}
	if len(doc.Models) != 1 || doc.Models[0].Name != "orders" {
		t.Fatalf("models = %+v, want one named orders", doc.Models)
	}
	if doc.Models[0].Description != "One row per order." {
		t.Errorf("description = %q", doc.Models[0].Description)
	}
}

func TestLightdashDimensions(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{
		Name: "orders",
		Dimensions: []ir.Field{
			{Name: "status", Expr: "status", Description: "Order state.",
				Enum: []ir.EnumValue{{Value: "paid"}, {Value: "refunded", Description: "money returned"}}},
			{Name: "is_first_order", Expr: "is_first_order"},
			{Name: "customer_sk", Expr: "customer_sk"},
			{Name: "region", Expr: "region", DataType: "varchar", Synonyms: []string{"area"}},
		},
		TimeDimensions: []ir.Field{
			{Name: "order_date", Expr: "order_date"},
		},
	}}}
	got := emitLightdash(t, m, Options{Name: "ecommerce"})

	var doc struct {
		Models []struct {
			Columns []struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
				Meta        struct {
					Dimension struct {
						Type  string `yaml:"type"`
						Label string `yaml:"label"`
					} `yaml:"dimension"`
				} `yaml:"meta"`
			} `yaml:"columns"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, got)
	}
	cols := map[string]struct {
		desc, typ string
	}{}
	for _, c := range doc.Models[0].Columns {
		cols[c.Name] = struct{ desc, typ string }{c.Description, c.Meta.Dimension.Type}
	}

	// enum meanings fold into the description; bare values are dropped (no slot).
	if !strings.Contains(cols["status"].desc, "refunded = money returned") {
		t.Errorf("status description missing enum meaning: %q", cols["status"].desc)
	}
	// synonyms fold into the description.
	if !strings.Contains(cols["region"].desc, "Synonyms: area") {
		t.Errorf("region description missing synonyms: %q", cols["region"].desc)
	}
	// type from name/DataType signals; order_date is a time dimension.
	if cols["is_first_order"].typ != "boolean" {
		t.Errorf("is_first_order type = %q, want boolean", cols["is_first_order"].typ)
	}
	if cols["customer_sk"].typ != "number" {
		t.Errorf("customer_sk type = %q, want number", cols["customer_sk"].typ)
	}
	if cols["region"].typ != "string" {
		t.Errorf("region type = %q, want string", cols["region"].typ)
	}
	if cols["order_date"].typ != "timestamp" {
		t.Errorf("order_date type = %q, want timestamp", cols["order_date"].typ)
	}
	// no confident signal and no DataType => type omitted (empty).
	if cols["status"].typ != "" {
		t.Errorf("status type = %q, want empty (omitted)", cols["status"].typ)
	}
}
