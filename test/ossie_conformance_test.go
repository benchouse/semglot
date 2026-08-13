package integration_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/benchouse/semglot/dialect"
)

// vendorDir holds fixtures copied verbatim from apache/ossie. See its README.md
// for provenance and licensing.
const vendorDir = "models/ossie/vendor"

// wantRelationships and wantMeasures pin the exact relationship and
// ir.Measure counts each vendored fixture must produce. Exact counts (rather
// than a bare "> 0" check) catch a partial regression that a nonzero check
// would miss — e.g. relationship extraction silently dropping one of several
// declared relationships, or measure synthesis silently stopping for some
// metrics but not others.
//
// Relationship counts were derived by counting `relationships:` entries in
// each upstream file directly (fixtureA_ossie.yaml: orders_to_customer;
// fixtureB_ossie.yaml: lineitem_to_orders; tpcds_ossie.yaml:
// store_sales_to_date_dim, store_sales_to_item, store_sales_to_customer;
// tpcds_semantic_model.yaml: store_sales_to_date, store_sales_to_customer,
// store_sales_to_item, store_sales_to_store).
//
// Measure counts were derived from simpleAgg's rule (dialect/ossie.go): a
// parsed metric becomes an ir.Measure only when its top-level expression is a
// single, unfiltered aggregate call over one column.
//
// fixtureA/fixtureB/tpcds_ossie's metrics reference physical columns no
// dataset declares under fields:, so metricHome's normal (qualified /
// colOwner) resolution fails — but each of these three fixtures has an
// unambiguous fact table (the dataset with no incoming relationship: orders,
// lineitem, store_sales respectively — Apache Ossie's own documented
// convention, see fixtureA_ossie.yaml's own comment), so task 14's
// metricHomeOrFact fallback now homes every one of them there instead of
// skipping them:
//   - fixtureA: total_revenue (SUM(o_totalprice), a plain Col arg) becomes a
//     measure; order_count (COUNT(*), Arg == nil) does not, since simpleAgg
//     requires a Col arg — 1 measure.
//   - fixtureB: revenue (SUM(l_extendedprice * (1 - l_discount)), a compound
//     Raw arg) does not become a measure; order_count
//     (COUNT(DISTINCT l_orderkey), a plain Col arg) does — 1 measure.
//   - tpcds_ossie: total_sales (SUM(ss_ext_sales_price)) and total_quantity
//     (SUM(ss_quantity)) are both plain single-column SUMs — 2 measures.
//
// tpcds_semantic_model.yaml is untouched by task 14: every metric there
// already references a fully qualified column (SUM(store_sales.ss_...)), so
// metricHome's normal resolution always succeeds and the fallback never
// fires. It parses 4 metrics: total_sales (SUM(store_sales.ss_ext_sales_price))
// and total_profit (SUM(store_sales.ss_net_profit)) and sales_by_brand
// (SUM(store_sales.ss_ext_sales_price)) are each a plain single-column SUM
// and become measures; customer_lifetime_value is a Binary of two Aggs
// (SUM(...) / COUNT(DISTINCT ...)) and is not a column-backed measure — 3
// measures total.
var (
	wantRelationships = map[string]int{
		"fixtureA_ossie.yaml":       1,
		"fixtureB_ossie.yaml":       1,
		"tpcds_ossie.yaml":          3,
		"tpcds_semantic_model.yaml": 4,
	}
	wantMeasures = map[string]int{
		"fixtureA_ossie.yaml":       1,
		"fixtureB_ossie.yaml":       1,
		"tpcds_ossie.yaml":          2,
		"tpcds_semantic_model.yaml": 3,
	}
)

// wantNotes pins the EXACT set of ir.Model.Notes each vendored fixture must
// produce. Notes are how this branch honours its no-silent-drop ruling — every
// construct the IR cannot represent has to leave one — so they are the
// conformance signal, not debug output, and are asserted rather than only
// logged.
//
// Pinning the exact set catches a regression in both directions: a fix that
// stops noting something (the drop goes silent again) and a change that starts
// noting things it should not (the notes stop being a reviewable triage list
// and become noise). Every note below was triaged against the upstream YAML:
//
//   - "attributed to dataset ... because it is the model's fact table" —
//     fixtureA, fixtureB and tpcds_ossie write metric arguments over columns
//     (o_totalprice, l_extendedprice, l_discount, l_orderkey,
//     ss_ext_sales_price, ss_quantity) that no dataset declares under
//     fields:, so metricHome's normal (qualified / colOwner) resolution
//     fails. Each fixture has exactly one dataset with no incoming
//     relationship (orders, lineitem, store_sales respectively — Apache
//     Ossie's own documented fact-table convention), so task 14's
//     metricHomeOrFact fallback homes the metric there instead of skipping
//     it, and notes that the attribution is inferred rather than read from
//     the file. This is why those three fixtures have measures above instead
//     of 0.
//   - "custom_extensions" — real vendor payloads (DATABRICKS formatting and
//     join hints; a SALESFORCE Tableau/CRM block and a DBT block on the TPC-DS
//     model). Per the design's "Out of scope", semglot degrades these to prose
//     rather than round-tripping them.
//   - "unique_keys" — OSI carries non-primary uniqueness; the IR has only
//     PrimaryKey.
//   - the bare TPC-DS paragraph — a model-level ai_context.instructions, which
//     maps ONTO ir.Model.Notes structurally (the one ai_context level that has
//     an IR home), so it appears verbatim rather than wrapped in a "dropped"
//     message.
//   - "datatype ... ir.Metric has no datatype slot" —
//     customer_lifetime_value is a ratio, not a column-backed measure, so
//     there is no ir.Measure.DataType to carry its Decimal.
//   - "could not be parsed as an aggregate expression" — store_productivity
//     uses NULLIF, a scalar function outside parseAggExpr's closed aggregate
//     allowlist.
var wantNotes = map[string][]string{
	"fixtureA_ossie.yaml": {
		`metric "order_count" attributed to dataset "orders" because it is the model's fact table (no incoming relationship); inferred, not declared in the file`,
		`metric "total_revenue" attributed to dataset "orders" because it is the model's fact table (no incoming relationship); inferred, not declared in the file`,
	},
	"fixtureB_ossie.yaml": {
		`dataset "orders" unique_keys [[o_orderkey]]: no unique-key slot in the IR; dropped`,
		`field "line_number" on dataset "lineitem" custom_extensions [DATABRICKS]: vendor extensions are not transpiled; dropped`,
		`metric "order_count" attributed to dataset "lineitem" because it is the model's fact table (no incoming relationship); inferred, not declared in the file`,
		`metric "revenue" custom_extensions [DATABRICKS]: vendor extensions are not transpiled; dropped`,
		`metric "revenue" attributed to dataset "lineitem" because it is the model's fact table (no incoming relationship); inferred, not declared in the file`,
		`model "lineitem" custom_extensions [DATABRICKS]: vendor extensions are not transpiled; dropped`,
		`relationship "lineitem_to_orders" custom_extensions [DATABRICKS]: vendor extensions are not transpiled; dropped`,
	},
	"tpcds_ossie.yaml": {
		`metric "total_quantity" attributed to dataset "store_sales" because it is the model's fact table (no incoming relationship); inferred, not declared in the file`,
		`metric "total_sales" custom_extensions [DATABRICKS]: vendor extensions are not transpiled; dropped`,
		`metric "total_sales" attributed to dataset "store_sales" because it is the model's fact table (no incoming relationship); inferred, not declared in the file`,
		`model "tpcds_store_sales" custom_extensions [DATABRICKS]: vendor extensions are not transpiled; dropped`,
	},
	"tpcds_semantic_model.yaml": {
		"Use this semantic model for retail analytics. It provides comprehensive sales, customer, product, and store data from the TPC-DS benchmark. The model supports time-based analysis, customer segmentation, product performance, and store operations metrics.",
		`dataset "customer" unique_keys [[c_customer_sk]]: no unique-key slot in the IR; dropped`,
		`dataset "date_dim" unique_keys [[d_date_sk]]: no unique-key slot in the IR; dropped`,
		`dataset "item" unique_keys [[i_item_sk]]: no unique-key slot in the IR; dropped`,
		`dataset "store" unique_keys [[s_store_id]]: no unique-key slot in the IR; dropped`,
		`dataset "store_sales" unique_keys [[ss_item_sk ss_ticket_number]]: no unique-key slot in the IR; dropped`,
		`metric "customer_lifetime_value" datatype "Decimal": ir.Metric has no datatype slot (only a column-backed measure carries one); dropped`,
		`metric "store_productivity" not transpiled: expression "SUM(store_sales.ss_ext_sales_price) / NULLIF(SUM(store.s_number_employees), 0)" could not be parsed as an aggregate expression`,
		`model "tpcds_retail_model" custom_extensions [SALESFORCE DBT]: vendor extensions are not transpiled; dropped`,
	},
}

// assertNotes compares got against the pinned set for fixture, sorted so note
// ORDER (an implementation detail of the parse walk) is not pinned while the
// content is.
func assertNotes(t *testing.T, fixture string, got []string) {
	t.Helper()
	want, ok := wantNotes[fixture]
	if !ok {
		t.Fatalf("no expected note set recorded for %s; triage its notes and add them to wantNotes", fixture)
	}
	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if len(gotSorted) != len(wantSorted) {
		t.Errorf("%s: %d notes, want %d\n got: %s\nwant: %s",
			fixture, len(gotSorted), len(wantSorted), strings.Join(gotSorted, "\n      "), strings.Join(wantSorted, "\n      "))
		return
	}
	for i := range gotSorted {
		if gotSorted[i] != wantSorted[i] {
			t.Errorf("%s note %d:\n got: %s\nwant: %s", fixture, i, gotSorted[i], wantSorted[i])
		}
	}
}

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
			var fields, metrics, measures int
			for _, tb := range m.Tables {
				fields += len(tb.Dimensions) + len(tb.TimeDimensions)
				metrics += len(tb.Metrics)
				measures += len(tb.Measures)
			}
			if fields == 0 {
				t.Errorf("no fields parsed from %s", e.Name())
			}
			t.Logf("%s: %d tables, %d fields, %d metrics, %d measures, %d relationships, %d notes",
				e.Name(), len(m.Tables), fields, metrics, measures, len(m.Relationships), len(m.Notes))
			for _, n := range m.Notes {
				t.Logf("  note: %s", n)
			}
			wantRel, ok := wantRelationships[e.Name()]
			if !ok {
				t.Fatalf("no expected relationship count recorded for %s; add one to wantRelationships", e.Name())
			}
			if len(m.Relationships) != wantRel {
				t.Errorf("%s: relationships = %d, want %d", e.Name(), len(m.Relationships), wantRel)
			}
			wantMeas, ok := wantMeasures[e.Name()]
			if !ok {
				t.Fatalf("no expected measure count recorded for %s; add one to wantMeasures", e.Name())
			}
			if measures != wantMeas {
				t.Errorf("%s: measures = %d, want %d", e.Name(), measures, wantMeas)
			}
			assertNotes(t, e.Name(), m.Notes)
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
