package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/benchouse/semglot/dialect"
	"github.com/benchouse/semglot/ir"
)

// lossReport lists what a round-trip dropped or changed, as human-readable
// lines. It compares canonicalized models, so ordering differences never
// register as loss. This is the measurement ir/model.go's package comment
// anticipates when it calls the IR "the unit of the fairness index".
func lossReport(before, after *ir.Model) []string {
	b, a := *before, *after
	canonicalizeModel(&b)
	canonicalizeModel(&a)

	var out []string
	afterTables := map[string]ir.Table{}
	for _, t := range a.Tables {
		afterTables[t.Name] = t
	}
	for _, bt := range b.Tables {
		at, ok := afterTables[bt.Name]
		if !ok {
			out = append(out, "table "+bt.Name+": lost")
			continue
		}
		out = append(out, diffNames("table "+bt.Name+" dimensions", fieldNames(bt.Dimensions), fieldNames(at.Dimensions))...)
		out = append(out, diffNames("table "+bt.Name+" time dimensions", fieldNames(bt.TimeDimensions), fieldNames(at.TimeDimensions))...)
		out = append(out, diffNames("table "+bt.Name+" measures", measureNames(bt.Measures), measureNames(at.Measures))...)
		out = append(out, diffNames("table "+bt.Name+" metrics", metricNames(bt.Metrics), metricNames(at.Metrics))...)
		out = append(out, diffNames("table "+bt.Name+" primary key", bt.PrimaryKey, at.PrimaryKey)...)
		out = append(out, diffNames("table "+bt.Name+" synonyms", bt.Synonyms, at.Synonyms)...)
		if bt.Grain != at.Grain {
			out = append(out, fmt.Sprintf("table %s grain: %q -> %q", bt.Name, bt.Grain, at.Grain))
		}
	}
	if len(b.Relationships) != len(a.Relationships) {
		out = append(out, fmt.Sprintf("relationships: %d -> %d", len(b.Relationships), len(a.Relationships)))
	}
	sort.Strings(out)
	return out
}

func fieldNames(fs []ir.Field) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func measureNames(ms []ir.Measure) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

func metricNames(ms []ir.Metric) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Name)
	}
	sort.Strings(out)
	return out
}

// diffNames reports names present before but absent after, and vice versa.
func diffNames(label string, before, after []string) []string {
	have := map[string]bool{}
	for _, n := range after {
		have[n] = true
	}
	had := map[string]bool{}
	for _, n := range before {
		had[n] = true
	}
	var out []string
	for _, n := range before {
		if !have[n] {
			out = append(out, label+": lost "+n)
		}
	}
	for _, n := range after {
		if !had[n] {
			out = append(out, label+": gained "+n)
		}
	}
	return out
}

// allowedLoss is what dbt -> ossie -> dbt is EXPECTED to lose or gain, each
// entry a documented format limit from the design doc. A line the report
// produces that is not matched by one of these substrings is unplanned loss.
var allowedLoss = []string{
	// OSI has no measure concept. Every measure is emitted as a model-level
	// metric, so on the way back an unpublished measure returns as a PUBLISHED
	// metric: the metric list gains names it did not have.
	"metrics: gained",
	// Symmetrically, every OSI metric that is a plain aggregation synthesises a
	// measure on parse, so a dbt metric that had no measure of its own name
	// gains one. Both directions are the same missing distinction.
	"measures: gained",
	// A measure's column must be declared as a dataset field for OSI's
	// "fields are the operands of metric expressions" convention (the same
	// shape Ossie's own dbt converter produces, pinned by
	// TestOssieAgreesWithReferenceConverter). That field is named after the
	// MEASURE ("order_gross_amount"), not the physical column it wraps
	// ("order_gross") — the same measure/metric conflation "metrics: gained"
	// and "measures: gained" already describe, just visible from the fields
	// list instead. OSI's `fields:` has no discriminator marking a field as
	// "exists only to satisfy a metric operand" versus "a real business
	// dimension" (the only per-field flag is dimension.is_time), so on
	// reparse this field is indistinguishable from a genuine dimension and
	// resurfaces as one.
	"dimensions: gained",
	// The reverse case: a dbt measure whose expression is NOT a single bare
	// column (e.g. `case when is_refunded then 1 else 0 end`) still emits
	// correctly as an OSI metric, but OSI's Parse only synthesises a measure
	// back out of a metric whose top-level shape is a single, unfiltered
	// aggregate over a plain column (dialect/ossie.go's simpleAgg) — the
	// narrowest, unambiguous case. A compound-expression measure has no
	// structural marker distinguishing it from an equally-shaped metric that
	// was never a measure at all, so it round-trips as a metric only, and
	// its measure identity is lost. Same root cause as the two entries
	// above: OSI's one flat metrics list cannot carry the measure/metric
	// distinction except by narrow, best-effort inference.
	"measures: lost",
	// Table.Grain has no OSI slot; it folds into the dataset description and
	// does not come back structurally.
	"grain:",
}

func unexpected(report []string) []string {
	var out []string
	for _, line := range report {
		ok := false
		for _, allow := range allowedLoss {
			if strings.Contains(line, allow) {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, line)
		}
	}
	return out
}

// TestDBTToOssieLoss measures what a dbt -> ossie -> dbt round-trip costs, and
// fails on any loss the design does not document.
func TestDBTToOssieLoss(t *testing.T) {
	dbtP, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	before, err := dbtP.Parse(sourceDirs...)
	if err != nil {
		t.Fatal(err)
	}

	ossieE, err := dialect.AsEmitter("ossie")
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := ossieE.(dialect.Configurable); ok {
		ossieE = c.WithOptions(dialect.Options{Database: "ANALYTICS", Schema: "MAIN", Name: "ecommerce"})
	}
	mid := t.TempDir()
	if _, err := ossieE.Emit(before, mid); err != nil {
		t.Fatal(err)
	}

	ossieP, err := dialect.AsParser("ossie")
	if err != nil {
		t.Fatal(err)
	}
	after, err := ossieP.Parse(mid)
	if err != nil {
		t.Fatal(err)
	}

	report := lossReport(before, after)
	for _, line := range report {
		t.Logf("loss: %s", line)
	}
	if bad := unexpected(report); len(bad) > 0 {
		t.Errorf("undocumented information loss in dbt -> ossie -> ossie-parse:\n  %s", strings.Join(bad, "\n  "))
	}
}

// TestOssieRoundTrip measures ossie -> ossie stability: parse each vendored
// upstream document on its own, emit it, re-parse, and diff. It is NOT
// asserted fully lossless — see the comment above unexpected(report) below
// for the one documented gap it does tolerate — only that nothing UNDOCUMENTED
// leaks through, the same bar TestDBTToOssieLoss holds.
//
// Each fixture is parsed on its own, rather than merging vendorDir's whole
// directory into one model the way TestOssieParsesUpstreamFixtures's single
// wantRelationships/wantMeasures pass does: several fixtures declare datasets
// under the same name (e.g. "orders", "customer", "store_sales", "date_dim"),
// and merging them would let one fixture's table silently stand in for
// another's in lossReport's by-name comparison, corrupting the result.
//
// Separately, three of the four vendored fixtures (fixtureA_ossie.yaml,
// fixtureB_ossie.yaml, tpcds_ossie.yaml) write their metrics' aggregate
// argument as a column that is never declared under `fields:` on any
// dataset — e.g. fixtureA's `SUM(o_totalprice)` when the `orders` dataset
// only declares o_orderkey and o_orderdate. dialect/ossie.go's metricHome
// cannot attribute such a column to a dataset and skips the metric with a
// note (see task-10-report.md's note triage), so those metrics never enter
// the "before" IR in the first place — this test's before/after diff has
// nothing to say about them one way or the other; the loss already happened
// between the vendored file and "before", outside what lossReport measures.
// Only tpcds_semantic_model.yaml declares its metrics' columns as real
// fields, so it is the only fixture here that actually exercises a
// metric/measure round-trip.
func TestOssieRoundTrip(t *testing.T) {
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
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			b, err := os.ReadFile(filepath.Join(vendorDir, name))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
				t.Fatal(err)
			}

			before, err := p.Parse(dir)
			if err != nil {
				t.Fatal(err)
			}
			e, err := dialect.AsEmitter("ossie")
			if err != nil {
				t.Fatal(err)
			}
			if c, ok := e.(dialect.Configurable); ok {
				e = c.WithOptions(dialect.Options{Database: "A", Schema: "M", Name: "rt"})
			}
			out := t.TempDir()
			if _, err := e.Emit(before, out); err != nil {
				t.Fatal(err)
			}
			after, err := p.Parse(out)
			if err != nil {
				t.Fatal(err)
			}
			report := lossReport(before, after)
			for _, line := range report {
				t.Logf("loss: %s", line)
			}
			// ossie -> ossie is not asserted fully lossless: emitting a
			// measure re-declares its column as a dataset field named after
			// the MEASURE (OSI's "fields are the operands of metric
			// expressions" convention), and OSI's fields[] has no
			// discriminator marking that field as metric-operand-only rather
			// than a real dimension, so reparsing it resurfaces it as a
			// dimension. That is the same allowedLoss-documented gap
			// TestDBTToOssieLoss tolerates, not a defect specific to this
			// test, so the same allowlist applies here.
			if bad := unexpected(report); len(bad) > 0 {
				t.Errorf("ossie -> ossie has undocumented information loss:\n  %s", strings.Join(bad, "\n  "))
			}
		})
	}
	if files == 0 {
		t.Fatal("no vendored fixtures found; run the vendoring step first")
	}
}
