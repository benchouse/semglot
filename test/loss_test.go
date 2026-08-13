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

// lossLine is one structured entry lossReport produces: a table-scoped
// field-list membership change (Table+Category+Dir+Name), a table's Grain
// changing (Table+Category="grain", Dir/Name unused), a whole table
// appearing/disappearing (Table+Category="table"), the relationship count
// changing (Category="relationships", Table unused), or a relationship's
// declared identity changing (Category="relationship name"/"relationship
// synonyms", with Table carrying the join's endpoints+columns key rather than
// a table name — a relationship belongs to a PAIR of tables, so there is no
// single one to scope it to).
//
// allowedLoss matches on these fields directly — the exact tuple a
// documented format limit is expected to produce for a specific table in a
// specific fixture — rather than matching a substring of the formatted
// text. Substring matching let a same-shaped-but-wrong regression hide
// behind an unrelated, already-expected line: "dimensions: gained" is a
// substring of "time dimensions: gained" (so a mutant that fabricated a
// spurious parsed time dimension went unnoticed), and "measures: lost"
// matched ANY lost measure regardless of which one (so a mutant that made
// EVERY measure fail to reconstruct also went unnoticed). Exact-tuple
// matching closes both holes: an unexpected Category or an unexpected Name
// fails to match any allowedLoss entry and is reported as unplanned loss.
type lossLine struct {
	Table    string
	Category string // "dimensions", "time dimensions", "measures", "metrics", "primary key", "synonyms", "grain", "source", "table", "relationships", "relationship name", "relationship synonyms"
	Dir      string // "gained" or "lost"; "" for grain/source/table/relationships
	Name     string // the item name gained/lost; "" for grain/source/table/relationships
	Text     string // human-readable rendering, for t.Logf and error messages
}

func (l lossLine) String() string { return l.Text }

// key is the tuple identity unexpected() matches on: everything but Text,
// which is derived and carries no independent information.
func (l lossLine) key() lossLine {
	return lossLine{Table: l.Table, Category: l.Category, Dir: l.Dir, Name: l.Name}
}

// lossReport lists what a round-trip dropped or changed, as structured
// entries. It compares canonicalized models, so ordering differences never
// register as loss. This is the measurement ir/model.go's package comment
// anticipates when it calls the IR "the unit of the fairness index".
func lossReport(before, after *ir.Model) []lossLine {
	b, a := *before, *after
	canonicalizeModel(&b)
	canonicalizeModel(&a)

	var out []lossLine
	afterTables := map[string]ir.Table{}
	for _, t := range a.Tables {
		afterTables[t.Name] = t
	}
	for _, bt := range b.Tables {
		at, ok := afterTables[bt.Name]
		if !ok {
			out = append(out, lossLine{Table: bt.Name, Category: "table", Dir: "lost", Text: "table " + bt.Name + ": lost"})
			continue
		}
		out = append(out, diffNames(bt.Name, "dimensions", fieldNames(bt.Dimensions), fieldNames(at.Dimensions))...)
		out = append(out, diffNames(bt.Name, "time dimensions", fieldNames(bt.TimeDimensions), fieldNames(at.TimeDimensions))...)
		out = append(out, diffNames(bt.Name, "measures", measureNames(bt.Measures), measureNames(at.Measures))...)
		out = append(out, diffNames(bt.Name, "metrics", metricNames(bt.Metrics), metricNames(at.Metrics))...)
		out = append(out, diffNames(bt.Name, "primary key", bt.PrimaryKey, at.PrimaryKey)...)
		out = append(out, diffNames(bt.Name, "synonyms", bt.Synonyms, at.Synonyms)...)
		if bt.Grain != at.Grain {
			out = append(out, lossLine{
				Table: bt.Name, Category: "grain",
				Text: fmt.Sprintf("table %s grain: %q -> %q", bt.Name, bt.Grain, at.Grain),
			})
		}
		// Only flagged when the BEFORE model actually declared a source
		// (bt.Source != ""): a dbt-sourced model has no physical source of
		// its own (dbt's model: ref() is resolved by dbt itself), so every
		// emitter reconstructs one from the profile — an empty-before,
		// populated-after pair there is the documented, pre-existing
		// fallback this task's brief guards (see ir.Table.Source), not new
		// loss. What this comparison exists to catch is an ossie-declared
		// source failing to survive a round-trip unchanged.
		if bt.Source != "" && bt.Source != at.Source {
			out = append(out, lossLine{
				Table: bt.Name, Category: "source",
				Text: fmt.Sprintf("table %s source: %q -> %q", bt.Name, bt.Source, at.Source),
			})
		}
	}
	if len(b.Relationships) != len(a.Relationships) {
		out = append(out, lossLine{
			Category: "relationships",
			Text:     fmt.Sprintf("relationships: %d -> %d", len(b.Relationships), len(a.Relationships)),
		})
	}
	// A join's declared identity, matched by endpoints+columns (relSortKey —
	// the same key canonicalizeModel sorts by, and the only part of a
	// relationship that is structural rather than nominal). The count line
	// above cannot see this: a round-trip that renames every relationship
	// keeps the count exactly.
	//
	// As with source above, only flagged when the BEFORE model actually
	// declared one. A dbt relationship is anonymous — a `relationships` test
	// names nothing — so every emitter mints a name from the endpoints, and an
	// empty-before/populated-after pair is that documented fallback rather
	// than loss. What this catches is an ossie-declared name or ai_context
	// synonym list failing to survive a round-trip unchanged.
	afterRels := map[string]ir.Relationship{}
	for _, r := range a.Relationships {
		afterRels[relSortKey(r)] = r
	}
	for _, br := range b.Relationships {
		ar, ok := afterRels[relSortKey(br)]
		if !ok {
			continue // the count line above reports a join that vanished
		}
		if br.Name != "" && br.Name != ar.Name {
			out = append(out, lossLine{
				Table: relSortKey(br), Category: "relationship name", Name: br.Name,
				Text: fmt.Sprintf("relationship %s name: %q -> %q", relSortKey(br), br.Name, ar.Name),
			})
		}
		if len(br.Synonyms) > 0 {
			// diffNames does the set comparison; only its Text is rewritten,
			// since "table <endpoints>" would misname a join as a table. Text
			// is derived and carries no identity — key() ignores it.
			for _, l := range diffNames(relSortKey(br), "relationship synonyms", br.Synonyms, ar.Synonyms) {
				l.Text = fmt.Sprintf("relationship %s synonyms: %s %s", relSortKey(br), l.Dir, l.Name)
				out = append(out, l)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Text < out[j].Text })
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

// diffNames reports names present before but absent after, and vice versa,
// as lossLine entries scoped to table/category.
func diffNames(table, category string, before, after []string) []lossLine {
	have := map[string]bool{}
	for _, n := range after {
		have[n] = true
	}
	had := map[string]bool{}
	for _, n := range before {
		had[n] = true
	}
	var out []lossLine
	for _, n := range before {
		if !have[n] {
			out = append(out, lossLine{
				Table: table, Category: category, Dir: "lost", Name: n,
				Text: fmt.Sprintf("table %s %s: lost %s", table, category, n),
			})
		}
	}
	for _, n := range after {
		if !had[n] {
			out = append(out, lossLine{
				Table: table, Category: category, Dir: "gained", Name: n,
				Text: fmt.Sprintf("table %s %s: gained %s", table, category, n),
			})
		}
	}
	return out
}

// allowedLoss is what a round-trip is EXPECTED to lose or gain: the exact
// (table, category, direction, name) tuple a documented format limit
// produces for the fixtures TestDBTToOssieLoss and TestOssieRoundTrip use.
// A reported lossLine whose key() does not exactly match one of these is
// unplanned loss. Grouped by the format limit each traces to; every limit
// here is a face of the SAME root cause — OSI has one flat, model-level
// `metrics:` list and no separate measure concept, recorded in
// dialect/README.md's "No measure concept in ossie" and the design doc's
// "Format limits" — not four independent gaps.
var allowedLoss = []lossLine{
	// OSI has no measure concept: every measure is emitted as a model-level
	// metric, so on the way back an unpublished measure returns as a
	// PUBLISHED metric — the metric list gains a name it did not have.
	{Table: "fct_order_lines", Category: "metrics", Dir: "gained", Name: "net_line_revenue"},
	{Table: "fct_order_lines", Category: "metrics", Dir: "gained", Name: "quantity"},
	{Table: "fct_orders", Category: "metrics", Dir: "gained", Name: "order_gross_amount"},
	{Table: "fct_orders", Category: "metrics", Dir: "gained", Name: "order_net_booked_amount"},
	{Table: "fct_orders", Category: "metrics", Dir: "gained", Name: "orders_count"},
	{Table: "fct_orders", Category: "metrics", Dir: "gained", Name: "refunded_orders_count"},
	{Table: "obt_sales", Category: "metrics", Dir: "gained", Name: "obt_net_revenue_line"},
	{Table: "obt_sales", Category: "metrics", Dir: "gained", Name: "obt_units_sold"},
	// Symmetrically, an OSI metric that is a plain aggregation synthesises a
	// same-named measure on parse, so a dbt metric that had no measure of its
	// own name gains one.
	{Table: "fct_order_lines", Category: "measures", Dir: "gained", Name: "units_sold"},
	{Table: "fct_orders", Category: "measures", Dir: "gained", Name: "gross_revenue"},
	{Table: "fct_orders", Category: "measures", Dir: "gained", Name: "net_revenue"},
	{Table: "fct_orders", Category: "measures", Dir: "gained", Name: "orders"},
	// A measure's column must be declared as a dataset field for OSI's
	// "fields are the operands of metric expressions" convention (the same
	// shape Ossie's own dbt converter produces, pinned by
	// TestOssieAgreesWithReferenceConverter). That field is named after the
	// MEASURE ("order_gross_amount"), not the physical column it wraps
	// ("order_gross") — the same measure/metric conflation the two groups
	// above already describe, just visible from the fields list instead.
	// OSI's `fields:` has no discriminator marking a field as "exists only
	// to satisfy a metric operand" versus "a real business dimension" (the
	// only per-field flag is dimension.is_time), so on reparse this field is
	// indistinguishable from a genuine dimension and resurfaces as one.
	{Table: "fct_order_lines", Category: "dimensions", Dir: "gained", Name: "net_line_revenue"},
	{Table: "fct_order_lines", Category: "dimensions", Dir: "gained", Name: "quantity"},
	{Table: "fct_orders", Category: "dimensions", Dir: "gained", Name: "order_gross_amount"},
	{Table: "fct_orders", Category: "dimensions", Dir: "gained", Name: "order_net_booked_amount"},
	{Table: "fct_orders", Category: "dimensions", Dir: "gained", Name: "orders_count"},
	{Table: "fct_orders", Category: "dimensions", Dir: "gained", Name: "refunded_orders_count"},
	{Table: "obt_sales", Category: "dimensions", Dir: "gained", Name: "obt_net_revenue_line"},
	{Table: "obt_sales", Category: "dimensions", Dir: "gained", Name: "obt_units_sold"},
	{Table: "store_sales", Category: "dimensions", Dir: "gained", Name: "sales_by_brand"},
	{Table: "store_sales", Category: "dimensions", Dir: "gained", Name: "total_profit"},
	{Table: "store_sales", Category: "dimensions", Dir: "gained", Name: "total_sales"},
	// Task 14 gives fixtureA_ossie.yaml, fixtureB_ossie.yaml and
	// tpcds_ossie.yaml's own metrics a home via the fact-table fallback
	// (dialect/ossie.go's metricHomeOrFact), where before they were skipped
	// entirely and so never reached the "before" IR that TestOssieRoundTrip
	// diffs (see the comment above TestOssieRoundTrip). Now that
	// total_revenue (fixtureA), order_count (fixtureB) and total_quantity
	// (tpcds_ossie) parse into simple, column-backed measures, they hit the
	// SAME format limit the entries above already document: OSI's `fields:`
	// has no discriminator marking a field as metric-operand-only, so
	// re-emitting the measure's synthesised field (named after the MEASURE,
	// not its underlying physical column) and reparsing it resurfaces it as
	// a plain dimension. tpcds_ossie's total_sales measure hits the exact
	// same tuple already listed above for tpcds_semantic_model.yaml's own
	// store_sales dataset (allowedLossSet matches on the tuple alone, not
	// which fixture produced it), so it needs no separate entry here.
	{Table: "orders", Category: "dimensions", Dir: "gained", Name: "total_revenue"},
	{Table: "lineitem", Category: "dimensions", Dir: "gained", Name: "order_count"},
	{Table: "store_sales", Category: "dimensions", Dir: "gained", Name: "total_quantity"},
	// The reverse case: a dbt measure whose expression is NOT a single bare
	// column (`case when is_refunded then 1 else 0 end`) still emits
	// correctly as an OSI metric, but OSI's Parse only synthesises a measure
	// back out of a metric whose top-level shape is a single, unfiltered
	// aggregate over a plain column (dialect/ossie.go's simpleAgg) — the
	// narrowest, unambiguous case. A compound-expression measure has no
	// structural marker distinguishing it from an equally-shaped metric that
	// was never a measure at all, so it round-trips as a metric only, and
	// its measure identity is lost.
	{Table: "fct_orders", Category: "measures", Dir: "lost", Name: "refunded_orders_count"},
	// Table.Grain has no OSI slot; it folds into the dataset description and
	// does not come back structurally.
	{Table: "obt_sales", Category: "grain"},
}

var allowedLossSet = func() map[lossLine]bool {
	m := make(map[lossLine]bool, len(allowedLoss))
	for _, a := range allowedLoss {
		m[a.key()] = true
	}
	return m
}()

func unexpected(report []lossLine) []lossLine {
	var out []lossLine
	for _, line := range report {
		if !allowedLossSet[line.key()] {
			out = append(out, line)
		}
	}
	return out
}

func joinLines(lines []lossLine) string {
	s := make([]string, len(lines))
	for i, l := range lines {
		s[i] = l.Text
	}
	return strings.Join(s, "\n  ")
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
		t.Errorf("undocumented information loss in dbt -> ossie -> ossie-parse:\n  %s", joinLines(bad))
	}
}

// TestOssieRoundTrip measures ossie -> ossie stability: parse each vendored
// upstream document on its own, emit it, re-parse, and diff. It is NOT
// asserted fully lossless — see the comment above unexpected(report) below
// for the one documented gap it does tolerate — only that nothing UNDOCUMENTED
// leaks through, the same bar TestDBTToOssieLoss holds.
//
// Each fixture is parsed on its own, rather than pointing Parse at vendorDir
// as a whole. Same-named datasets across the fixtures no longer collide
// silently — dialect/ossie.go's mergeTable unions them by name and notes every
// disagreement — but merging them is still the wrong thing to MEASURE here.
// Several fixtures declare a dataset under a name another fixture also uses
// while modelling something else entirely (fixtureA's TPCH "customer", keyed
// c_custkey, versus tpcds_semantic_model's TPC-DS "customer", keyed
// c_customer_sk; likewise "orders", "store_sales", "date_dim"). Merging four
// unrelated upstream documents produces one chimeric model, and a round-trip
// of the chimera says nothing about whether any single upstream document
// survives. Per-fixture subtests also attribute a failure to the file that
// caused it.
//
// Separately, three of the four vendored fixtures (fixtureA_ossie.yaml,
// fixtureB_ossie.yaml, tpcds_ossie.yaml) write their metrics' aggregate
// argument as a column that is never declared under `fields:` on any
// dataset — e.g. fixtureA's `SUM(o_totalprice)` when the `orders` dataset
// only declares o_orderkey and o_orderdate. dialect/ossie.go's metricHome
// cannot attribute such a column via a qualified reference or colOwner, but
// each of the three has an unambiguous fact table (the dataset with no
// incoming relationship — Apache Ossie's own documented convention), so
// task 14's metricHomeOrFact fallback homes the metric there anyway (see
// task-14-report.md). Those metrics DO now enter the "before" IR — and, when
// their aggregate argument is a plain column, synthesise a measure too — so
// this test's before/after diff exercises them exactly like
// tpcds_semantic_model.yaml's metrics. The one gap that fallback attribution
// opens (a measure's synthesised field re-parsing as a plain dimension) is
// the SAME documented format limit tpcds_semantic_model.yaml's store_sales
// entries already cover in allowedLoss above; task 14 adds three more exact
// tuples for it (orders/total_revenue, lineitem/order_count,
// store_sales/total_quantity) rather than loosening the check.
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
				t.Errorf("ossie -> ossie has undocumented information loss:\n  %s", joinLines(bad))
			}
		})
	}
	if files == 0 {
		t.Fatal("no vendored fixtures found; run the vendoring step first")
	}
}
