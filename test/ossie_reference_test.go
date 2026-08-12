package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/benchouse/semglot/dialect"
)

// Accepted divergences from Apache Ossie's reference dbt converter. Each entry
// is a deliberate decision recorded in the design doc, NOT a known bug. Adding
// an entry means deciding that semglot is right to differ; a NEW divergence
// showing up as a test failure is a regression signal.
//
//  1. Their converter emits a top-level `dialects:` key that the published
//     osi-schema.json rejects (additionalProperties: false permits only
//     `version` and `semantic_model`). semglot does not emit it, and the
//     comparison ignores it entirely by parsing into semglot's own structs.
//  2. Their ratios parenthesize aggressively — `(SUM(x)) / (SUM(y))` — while
//     renderOperand parenthesizes only compound operands. normalizeExpr strips
//     redundant parens around single terms before comparing.
//  3. Expression case and whitespace: they uppercase aggregate names, semglot
//     renders lowercase neutral SQL. normalizeExpr folds case.
//  4. COUNT desugaring: they rewrite a COUNT measure over column x as
//     `SUM(CASE WHEN x IS NOT NULL THEN 1 ELSE 0 END)`, presumably so every
//     metric expression is a SUM and stays trivially roll-up-able. semglot
//     emits plain `COUNT(x)`, and is right to: COUNT is the ANSI spelling of
//     exactly this aggregate, it is what every other semglot target emits for
//     a dbt `agg: count` measure, and it is better behaved at the edge — over
//     an empty input COUNT(x) is 0 while their SUM(CASE …) is NULL. Their form
//     is an internal representation choice, not a semantic requirement.
//     normalizeExpr folds the desugared form back to COUNT(x), so a genuine
//     arithmetic disagreement still fails.
const referenceDivergences = `dialects key; redundant parens; expression case; COUNT desugaring`

var (
	spaceRun     = regexp.MustCompile(`\s+`)
	parenWrapped = regexp.MustCompile(`\((\s*[A-Za-z_][A-Za-z0-9_.]*\s*\([^()]*\)\s*)\)`)
	// countDesugar matches the reference converter's expansion of COUNT(x):
	// `sum(case when x is not null then 1 else 0 end)`, capturing x. It is
	// deliberately anchored on the whole idiom (both the SUM wrapper and the
	// literal 1/0 arms) so it cannot collapse an unrelated conditional SUM.
	countDesugar = regexp.MustCompile(`sum\(\s*case when ([A-Za-z_][A-Za-z0-9_.]*) is not null then 1 else 0 end\s*\)`)
)

// normalizeExpr canonicalizes a SQL expression for comparison: case-folded,
// whitespace-collapsed, with the reference converter's COUNT desugaring folded
// back to COUNT(x), and with parens stripped from around a single call.
func normalizeExpr(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = spaceRun.ReplaceAllString(s, " ")
	s = countDesugar.ReplaceAllString(s, "count($1)")
	for {
		next := parenWrapped.ReplaceAllString(s, "$1")
		next = spaceRun.ReplaceAllString(strings.TrimSpace(next), " ")
		if next == s {
			return s
		}
		s = next
	}
}

// osiSummary is the semantic content compared between the two implementations:
// dataset -> sorted field names, and metric name -> normalized expression.
type osiSummary struct {
	fields  map[string][]string
	metrics map[string]string
}

// summarize reduces an OSI document to its comparable content. It deliberately
// ignores key ordering, descriptions, and ai_context — the reference converter
// carries none of the latter for these fixtures.
func summarize(t *testing.T, path string) osiSummary {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Parse into a loose shape so the reference file's extra top-level
	// `dialects:` key does not fail decoding.
	var doc struct {
		SemanticModel []struct {
			Datasets []struct {
				Name   string `yaml:"name"`
				Fields []struct {
					Name string `yaml:"name"`
				} `yaml:"fields"`
			} `yaml:"datasets"`
			Metrics []struct {
				Name       string `yaml:"name"`
				Expression struct {
					Dialects []struct {
						Dialect    string `yaml:"dialect"`
						Expression string `yaml:"expression"`
					} `yaml:"dialects"`
				} `yaml:"expression"`
			} `yaml:"metrics"`
		} `yaml:"semantic_model"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	s := osiSummary{fields: map[string][]string{}, metrics: map[string]string{}}
	for _, sm := range doc.SemanticModel {
		for _, ds := range sm.Datasets {
			var names []string
			for _, f := range ds.Fields {
				names = append(names, f.Name)
			}
			sort.Strings(names)
			s.fields[ds.Name] = names
		}
		for _, mt := range sm.Metrics {
			if len(mt.Expression.Dialects) == 0 {
				continue
			}
			s.metrics[mt.Name] = normalizeExpr(mt.Expression.Dialects[0].Expression)
		}
	}
	return s
}

// diffOSI reports every semantic difference between two summaries.
func diffOSI(want, got osiSummary) []string {
	var out []string
	for ds, wf := range want.fields {
		gf, ok := got.fields[ds]
		if !ok {
			out = append(out, fmt.Sprintf("dataset %q missing", ds))
			continue
		}
		if strings.Join(wf, ",") != strings.Join(gf, ",") {
			out = append(out, fmt.Sprintf("dataset %q fields: want %v, got %v", ds, wf, gf))
		}
	}
	for ds := range got.fields {
		if _, ok := want.fields[ds]; !ok {
			out = append(out, fmt.Sprintf("unexpected dataset %q", ds))
		}
	}
	for name, we := range want.metrics {
		ge, ok := got.metrics[name]
		if !ok {
			out = append(out, fmt.Sprintf("metric %q missing", name))
			continue
		}
		if we != ge {
			out = append(out, fmt.Sprintf("metric %q expression:\n    want %s\n     got %s", name, we, ge))
		}
	}
	for name := range got.metrics {
		if _, ok := want.metrics[name]; !ok {
			out = append(out, fmt.Sprintf("unexpected metric %q", name))
		}
	}
	sort.Strings(out)
	return out
}

// TestOssieAgreesWithReferenceConverter compares semglot's dbt -> ossie output
// against the output Apache Ossie's own dbt converter produces from an
// equivalent dbt project. Accepted divergences are listed in
// referenceDivergences and normalized away; anything else is a real
// disagreement worth understanding before it ships.
func TestOssieAgreesWithReferenceConverter(t *testing.T) {
	cases := []string{"derived_metric_nested", "ratio_metric_inlines"}
	p, err := dialect.AsParser("dbt")
	if err != nil {
		t.Fatal(err)
	}
	e, err := dialect.AsEmitter("ossie")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := p.Parse(filepath.Join("models/ossie/reference", name))
			if err != nil {
				t.Fatalf("parse dbt: %v", err)
			}
			out := t.TempDir()
			emitter := e
			if c, ok := e.(dialect.Configurable); ok {
				emitter = c.WithOptions(dialect.Options{Schema: "schema", Name: "semantic_model"})
			}
			if _, err := emitter.Emit(m, out); err != nil {
				t.Fatalf("emit ossie: %v", err)
			}
			want := summarize(t, filepath.Join(vendorDir, "reference", name+".yaml"))
			got := summarize(t, filepath.Join(out, "semantic_model.yaml"))
			if d := diffOSI(want, got); len(d) > 0 {
				t.Errorf("semglot disagrees with the reference converter (accepted divergences: %s):\n  %s",
					referenceDivergences, strings.Join(d, "\n  "))
			}
		})
	}
}
