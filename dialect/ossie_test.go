package dialect

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeOSI writes an OSI document into a temp dir and returns the dir.
func writeOSI(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOssieRegistered(t *testing.T) {
	if _, err := AsParser("ossie"); err != nil {
		t.Errorf("AsParser(ossie): %v", err)
	}
	if _, err := AsEmitter("ossie"); err != nil {
		t.Errorf("AsEmitter(ossie): %v", err)
	}
}

func TestOssieParseDatasetsAndFields(t *testing.T) {
	dir := writeOSI(t, `
version: "0.2.0.dev0"
semantic_model:
  - name: sales
    description: Sales model.
    datasets:
      - name: orders
        source: sales.public.orders
        primary_key: [order_id, line_number]
        description: Customer orders.
        ai_context:
          synonyms: [purchases, sales]
        fields:
          - name: order_id
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_id
            datatype: Integer
            description: Order identifier.
          - name: order_date
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: order_date
            datatype: Date
            description: Order date.
          - name: created_at
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: created_at
            datatype: DateTime
            dimension:
              is_time: false
          - name: status
            expression:
              dialects:
                - dialect: ANSI_SQL
                  expression: status
            datatype: String
            ai_context:
              synonyms: [state]
`)
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tables) != 1 {
		t.Fatalf("want 1 table, got %d", len(m.Tables))
	}
	tbl := m.Tables[0]
	if tbl.Name != "orders" {
		t.Errorf("Name = %q, want orders", tbl.Name)
	}
	if tbl.Description != "Customer orders." {
		t.Errorf("Description = %q", tbl.Description)
	}
	if !reflect.DeepEqual(tbl.Synonyms, []string{"purchases", "sales"}) {
		t.Errorf("Synonyms = %v", tbl.Synonyms)
	}
	if !reflect.DeepEqual(tbl.PrimaryKey, []string{"order_id", "line_number"}) {
		t.Errorf("PrimaryKey = %v", tbl.PrimaryKey)
	}
	// order_date defaults to a time dimension via its Date datatype;
	// created_at opts out with an explicit is_time: false.
	var timeNames, dimNames []string
	for _, d := range tbl.TimeDimensions {
		timeNames = append(timeNames, d.Name)
	}
	for _, d := range tbl.Dimensions {
		dimNames = append(dimNames, d.Name)
	}
	if !reflect.DeepEqual(timeNames, []string{"order_date"}) {
		t.Errorf("TimeDimensions = %v, want [order_date]", timeNames)
	}
	if !reflect.DeepEqual(dimNames, []string{"order_id", "created_at", "status"}) {
		t.Errorf("Dimensions = %v", dimNames)
	}
	for _, d := range tbl.Dimensions {
		if d.Name == "order_id" && d.DataType != "integer" {
			t.Errorf("order_id DataType = %q, want integer", d.DataType)
		}
		if d.Name == "status" && !reflect.DeepEqual(d.Synonyms, []string{"state"}) {
			t.Errorf("status Synonyms = %v", d.Synonyms)
		}
	}
}

// TestOssieParseSkipsNonOSI ignores YAML files with no semantic_model key, so a
// mixed directory works.
func TestOssieParseSkipsNonOSI(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.yml"), []byte("models:\n  - name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := ossie{}.Parse(dir)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Tables) != 0 {
		t.Errorf("want 0 tables from a non-OSI file, got %d", len(m.Tables))
	}
}
