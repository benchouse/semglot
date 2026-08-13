package dialect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// ir.Table.Source is the physical address an ossie dataset declares. It is the
// one thing in an OSI document nothing downstream can reconstruct: every other
// physical reference semglot emits is rebuilt from the profile's
// database/schema, which is a guess about where the table lives, whereas
// `source` is the source dialect stating it.
//
// The rule this file pins, for EVERY registered emitter rather than the five
// that happened to be wired first: a declared source must either reach the
// emitted artifact, or be named in a returned warning. Silence is the one
// outcome ruled out — a dropped physical address that nobody is told about
// leaves a layer pointing at a table that may not exist, at exit code 0.
//
// The two shapes below are both legal OSI: `source` is specified as "either
// database_name.schema_name.table_name or query".

// emitAll runs every registered emitter over m and returns, per dialect name,
// the concatenated text of everything it wrote plus everything it warned.
func emitAll(t *testing.T, m *ir.Model) map[string]emitResult {
	t.Helper()
	out := map[string]emitResult{}
	for _, name := range Names() {
		// registry_test.go registers a do-nothing "fake-emitter" into the
		// same global registry, so whether it appears here depends on test
		// order. It is a registry fixture, not a dialect.
		if strings.HasPrefix(name, "fake-") {
			continue
		}
		e, err := AsEmitter(name)
		if err != nil {
			continue // parse-only dialect; nothing to check here
		}
		if c, ok := e.(Configurable); ok {
			e = c.WithOptions(Options{Database: "WAREHOUSE", Schema: "PUBLIC", Name: "m"})
		}
		dir := t.TempDir()
		warnings, err := e.Emit(m, dir)
		if err != nil {
			t.Fatalf("%s: emit: %v", name, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("%s: read output dir: %v", name, err)
		}
		var files strings.Builder
		for _, ent := range entries {
			b, err := os.ReadFile(filepath.Join(dir, ent.Name()))
			if err != nil {
				t.Fatalf("%s: read %s: %v", name, ent.Name(), err)
			}
			files.Write(b)
			files.WriteByte('\n')
		}
		out[name] = emitResult{files: files.String(), warnings: warnings}
	}
	if len(out) < 9 {
		t.Fatalf("expected at least the nine known emitters, got %d: %v", len(out), Names())
	}
	return out
}

type emitResult struct {
	files    string
	warnings []string
}

// warnsAbout reports whether some warning names both the table and its source,
// which is what makes a warning actionable rather than a shrug.
func (r emitResult) warnsAbout(table, source string) bool {
	for _, w := range r.warnings {
		if strings.Contains(w, table) && strings.Contains(w, source) {
			return true
		}
	}
	return false
}

// sourceModel is one table carrying a declared source, a dimension and a
// metric — enough for every emitter to produce a well-formed artifact
// (databricks-metric-view drops a table with no dimension, and a metric-less
// table takes lightdash's synthesised-row-count path).
func sourceModel(source string) *ir.Model {
	return &ir.Model{Tables: []ir.Table{{
		Name:       "orders",
		Source:     source,
		Dimensions: []ir.Field{{Name: "status", Expr: "status"}},
		Measures:   []ir.Measure{{Field: ir.Field{Name: "amount", Expr: "amount"}, Agg: "sum"}},
		Metrics: []ir.Metric{{
			Name: "revenue",
			Def:  ir.Agg{Func: "sum", Table: "orders", Arg: ir.Col{Table: "orders", Name: "amount"}},
		}},
	}}}
}

// TestEveryEmitterCarriesOrWarnsAboutDeclaredSource is the sweep. A dialect
// added later is covered automatically: Names() drives the loop.
func TestEveryEmitterCarriesOrWarnsAboutDeclaredSource(t *testing.T) {
	const table = "orders"

	t.Run("three-part reference", func(t *testing.T) {
		const source = "PROD.SALES.orders_v1"
		for name, res := range emitAll(t, sourceModel(source)) {
			// cortex is the one target that cannot hold the address as a
			// string: cortexBaseTable has separate database/schema/table
			// keys, so it emits the three parts rather than the dotted whole.
			// Every part must be present — a target that dropped the source
			// would show none of them, since neither the table name nor the
			// column names contain them.
			parts := strings.Split(source, ".")
			carried := strings.Contains(res.files, source)
			if !carried {
				carried = true
				for _, p := range parts {
					if !strings.Contains(res.files, p) {
						carried = false
					}
				}
			}
			if !carried && !res.warnsAbout(table, source) {
				t.Errorf("%s: declared source %q neither emitted nor warned about; warnings=%v\n--- files ---\n%s",
					name, source, res.warnings, res.files)
			}
		}
	})

	// A query is what makes dot-counting insufficient (see splitSource): the
	// canonical OSI query form has exactly the two dots a three-part address
	// has, so an emitter that splits without checking the shape produces a
	// wrong address instead of a missing one — worse, and just as silent.
	t.Run("query", func(t *testing.T) {
		const source = "SELECT * FROM prod.raw.orders WHERE deleted = false"
		for name, res := range emitAll(t, sourceModel(source)) {
			if strings.Contains(res.files, source) {
				continue // carried whole (prose targets, and ossie's own source:)
			}
			if !res.warnsAbout(table, source) {
				t.Errorf("%s: declared query source %q neither emitted nor warned about; warnings=%v\n--- files ---\n%s",
					name, source, res.warnings, res.files)
			}
			// Whatever it fell back to, it must not be the query masquerading
			// as an address — the failure mode the cortex fix closed.
			for _, frag := range []string{"database: SELECT", "source: SELECT", "table: SELECT", "as SELECT"} {
				if strings.Contains(res.files, frag) {
					t.Errorf("%s: pasted the query into an address slot (%q):\n%s", name, frag, res.files)
				}
			}
		}
	})
}

// TestDBTEmitWarnsOnDeclaredSource pins dbt's half of finding 2. dbt has no
// slot for it (see dbtSchemaSourceWarning), so the requirement is that the
// loss is reported, not that it lands somewhere.
func TestDBTEmitWarnsOnDeclaredSource(t *testing.T) {
	warnings, err := dbt{}.Emit(sourceModel("PROD.SALES.orders_v1"), t.TempDir())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, `"orders"`) && strings.Contains(w, "PROD.SALES.orders_v1") && strings.Contains(w, "dbt") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the table, its source and dbt; got %v", warnings)
	}
	// A model with no declared source must stay quiet: dbt -> dbt is the
	// round-trip TestDBTRoundTrip pins, and a warning per table there would be
	// noise about a loss that did not happen.
	quiet, err := dbt{}.Emit(sourceModel(""), t.TempDir())
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	for _, w := range quiet {
		if strings.Contains(w, "physical address") {
			t.Errorf("no source was declared, so nothing was lost; got %q", w)
		}
	}
}

// TestLightdashWarnsOnDeclaredSource is the same for lightdash, whose artifact
// is also a dbt schema file. Its notes are written into the artifact as well,
// so both channels are checked.
func TestLightdashWarnsOnDeclaredSource(t *testing.T) {
	m := sourceModel("PROD.SALES.orders_v1")
	dir := t.TempDir()
	warnings, err := lightdash{}.Emit(m, dir)
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, `"orders"`) && strings.Contains(w, "PROD.SALES.orders_v1") && strings.Contains(w, "lightdash") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a warning naming the table, its source and lightdash; got %v", warnings)
	}
	b, err := os.ReadFile(filepath.Join(dir, "schema.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "PROD.SALES.orders_v1") {
		t.Errorf("the emitted schema.yml must carry the note too:\n%s", b)
	}
}

// TestNaoYamlFoldsDeclaredSourceIntoNotes pins nao-yaml's prose fold: nao's
// metric `source.table` selects a table inside the database/schema its
// directory scaffolding pins, so a qualified address does not belong there;
// notes: carries it whole instead, exactly as table synonyms are carried.
func TestNaoYamlFoldsDeclaredSourceIntoNotes(t *testing.T) {
	for _, source := range []string{
		"PROD.SALES.orders_v1",
		"SELECT * FROM prod.raw.orders WHERE deleted = false",
	} {
		out := emitNaoYaml(t, sourceModel(source))
		if !strings.Contains(out, "orders: Physical source: "+source+".") {
			t.Errorf("semantic.yaml must carry the declared source %q in notes:\n%s", source, out)
		}
		// The structural slot keeps the logical table name: writing the
		// address there would restate or contradict nao's scaffolding.
		if !strings.Contains(out, "table: orders\n") {
			t.Errorf("metric source.table must stay the logical table name:\n%s", out)
		}
	}
}

// TestContextRulesCarriesDeclaredSource pins the prose target's advantage: it
// can carry either source shape verbatim, with no degradation at all.
func TestContextRulesCarriesDeclaredSource(t *testing.T) {
	for _, source := range []string{
		"PROD.SALES.orders_v1",
		"SELECT * FROM prod.raw.orders WHERE deleted = false",
	} {
		out := emitContextRules(t, sourceModel(source))
		if !strings.Contains(out, "- **orders**: Physical source: "+source+".") {
			t.Errorf("RULES.md must carry the declared source %q:\n%s", source, out)
		}
	}
}

// TestContextRulesSourceGivesUndocumentedTableAnEntry: the source is folded
// into the entry BODY, so a table that documents nothing else still appears —
// it has something to say now.
func TestContextRulesSourceGivesUndocumentedTableAnEntry(t *testing.T) {
	m := &ir.Model{Tables: []ir.Table{{Name: "orders", Source: "PROD.SALES.orders_v1"}}}
	out := emitContextRules(t, m)
	if !strings.Contains(out, "## Table reference") {
		t.Errorf("a table with only a source must still get a Table reference entry:\n%s", out)
	}
}
