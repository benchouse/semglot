package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/dialect"
)

// vendorDir holds fixtures copied verbatim from apache/ossie. See its README.md
// for provenance and licensing.
const vendorDir = "models/ossie/vendor"

// TestOssieParsesUpstreamFixtures parses every vendored Apache Ossie document
// and asserts semglot extracts real structure from each — the conformance
// signal is that the spec authors' own files round-trip into a usable IR.
func TestOssieParsesUpstreamFixtures(t *testing.T) {
	p, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(vendorDir)
	if err != nil {
		t.Fatal(err)
	}
	var files int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		files++
		t.Run(e.Name(), func(t *testing.T) {
			dir := t.TempDir()
			b, err := os.ReadFile(filepath.Join(vendorDir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o644); err != nil {
				t.Fatal(err)
			}
			m, err := p.Parse(dir)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(m.Tables) == 0 {
				t.Fatalf("no tables parsed from %s", e.Name())
			}
			var fields, metrics int
			for _, tb := range m.Tables {
				fields += len(tb.Dimensions) + len(tb.TimeDimensions)
				metrics += len(tb.Metrics)
			}
			if fields == 0 {
				t.Errorf("no fields parsed from %s", e.Name())
			}
			t.Logf("%s: %d tables, %d fields, %d metrics, %d relationships, %d notes",
				e.Name(), len(m.Tables), fields, metrics, len(m.Relationships), len(m.Notes))
			for _, n := range m.Notes {
				t.Logf("  note: %s", n)
			}
		})
	}
	if files == 0 {
		t.Fatal("no vendored fixtures found; run the vendoring step first")
	}
}

// TestOssieParsesTPCDSDetail pins the specifics of the spec's own headline
// example, so a regression in dataset, key, or metric handling is caught by
// name rather than by count.
func TestOssieParsesTPCDSDetail(t *testing.T) {
	p, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join(vendorDir, "tpcds_semantic_model.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tpcds.yaml"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := p.Parse(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tb := range m.Tables {
		if tb.Name != "store_sales" {
			continue
		}
		found = true
		// Composite primary key: [ss_item_sk, ss_ticket_number]
		if len(tb.PrimaryKey) != 2 {
			t.Errorf("store_sales primary_key = %v, want 2 columns", tb.PrimaryKey)
		}
		if len(tb.Synonyms) == 0 {
			t.Error("store_sales lost its dataset ai_context.synonyms")
		}
	}
	if !found {
		t.Fatalf("no store_sales dataset in %d tables", len(m.Tables))
	}
	var total bool
	for _, tb := range m.Tables {
		for _, mt := range tb.Metrics {
			if mt.Name == "total_sales" {
				total = true
			}
		}
	}
	if !total {
		t.Error("total_sales metric was not parsed")
	}
}
