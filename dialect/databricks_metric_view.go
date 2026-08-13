package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(databricksMetricView{}) }

// databricksMetricView emits Unity Catalog metric views — the YAML semantic
// layer Databricks AI/BI Genie grounds its answers on. It writes one
// <table>.yaml per IR table, rooted at that table with direct joins to the
// dimension tables it references — parity with sibling targets (cortex,
// snowflake-semantic-view, supersimple), which all emit every table. Measures
// come from the table's metrics first, then any raw ir.Measure not already
// represented — by lowercased name or by rendered expression — so a table
// with metrics still surfaces raw measures no metric covers, while a raw
// measure that would just near-duplicate a metric (same expr, different name,
// e.g. orders_count beside an orders metric) is suppressed. A table left with
// zero measures either way (a pure dimension table, or one whose metrics all
// degraded) gets a synthesised row-count measure, since a metric view
// requires at least one. A table with zero dimensions is skipped entirely —
// a metric view also requires at least one, and there is no way to
// synthesise a meaningful one. Zero value is usable; the build command sets
// identity from flags. Emit does not mutate m. Database is the Unity Catalog
// catalog; Schema is the source-table schema.
type databricksMetricView struct{ Database, Schema, ModelName, Description string }

func (databricksMetricView) Name() string { return "databricks-metric-view" }

func (databricksMetricView) WithOptions(o Options) Emitter {
	return databricksMetricView{
		Database:    o.Database,
		Schema:      o.Schema,
		ModelName:   o.Name,
		Description: o.Description,
	}
}

// ---- metric-view YAML shapes ----

type dbxMetricView struct {
	Version  string       `yaml:"version"`
	Comment  string       `yaml:"comment,omitempty"`
	Source   string       `yaml:"source"`
	Joins    []dbxJoin    `yaml:"joins,omitempty"`
	Fields   []dbxField   `yaml:"fields,omitempty"`
	Measures []dbxMeasure `yaml:"measures,omitempty"`
}

type dbxField struct {
	Name     string   `yaml:"name"`
	Expr     string   `yaml:"expr"`
	Comment  string   `yaml:"comment,omitempty"`
	Synonyms []string `yaml:"synonyms,omitempty"`
}

type dbxMeasure struct {
	Name        string   `yaml:"name"`
	Expr        string   `yaml:"expr"`
	Comment     string   `yaml:"comment,omitempty"`
	DisplayName string   `yaml:"display_name,omitempty"`
	Synonyms    []string `yaml:"synonyms,omitempty"`
}

// dbxJoin marshals with a QUOTED "on" key. Databricks parses metric-view YAML
// as YAML 1.1, where a bare `on` key is the boolean true — which corrupts the
// join. yaml.v3 (YAML 1.2, where `on` is a plain string) would emit it bare, so
// build the mapping node explicitly and force the key's quote style.
type dbxJoin struct {
	Name   string
	Source string
	On     string
	// RightTable is the joined table's actual (lowercased) name in the model —
	// distinct from Name once a role-playing FK (two+ relationships to the same
	// Right table) has disambiguated Name. Never marshaled; it is the internal
	// key of this view's join set, used both to look up the joined table's
	// dimensions and, via dbxJoinAliases, to resolve an IR table qualifier in a
	// measure expression to the alias its join carries here.
	RightTable string
}

func (j dbxJoin) MarshalYAML() (any, error) {
	n := &yaml.Node{Kind: yaml.MappingNode}
	pairs := []struct {
		key, val string
		style    yaml.Style
	}{
		{"name", j.Name, 0},
		{"source", j.Source, 0},
		{"on", j.On, yaml.DoubleQuotedStyle},
	}
	for _, p := range pairs {
		kn := &yaml.Node{Kind: yaml.ScalarNode, Value: p.key, Style: p.style}
		vn := &yaml.Node{}
		if err := vn.Encode(p.val); err != nil {
			return nil, err
		}
		n.Content = append(n.Content, kn, vn)
	}
	return n, nil
}

func (d databricksMetricView) Emit(m *ir.Model, dir string) ([]string, error) {
	catalog := strings.ToLower(d.Database)
	schema := strings.ToLower(d.Schema)
	if schema == "" {
		schema = "main"
	}
	resolve := metricResolver(m)

	// metricOwner maps each metric name to its owning table, so a derived metric
	// that references a metric on ANOTHER table (cross-grain) is detected and
	// degraded rather than inlined — inlining another grain's aggregate into this
	// view (over a fan-out join) would miscount.
	metricOwner := map[string]string{}
	tableByName := map[string]ir.Table{}
	for _, t := range m.Tables {
		tableByName[strings.ToLower(t.Name)] = t
		for _, mt := range t.Metrics {
			metricOwner[mt.Name] = t.Name
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var warnings []string
	// Every view resolves the physical source of every table it joins, so a
	// joined table with an unusable source is reported by its own view AND by
	// each view that joins it — the same sentence about the same table, once
	// per referencing view. Each view's artifact comment still carries it (a
	// reader of one file must see why its join source was reconstructed), but
	// the CLI's warning list is per BUILD, so it is emitted once there.
	seenWarning := map[string]bool{}
	addWarning := func(w string) {
		if seenWarning[w] {
			return
		}
		seenWarning[w] = true
		warnings = append(warnings, w)
	}

	// A metric view is rooted at ONE table and carries only the joins that
	// LEAVE it, so the joins loop in buildView is gated on r.Left == t.Name —
	// and a relationship whose Left endpoint names no table in the model is
	// consequently iterated by no view at all. Nothing in buildView can report
	// it (it is the only place relationshipNames' warnings and
	// relSynonymsWarning are consulted), so the relationship, its declared name
	// and its synonyms would leave no trace anywhere: not a join, not a
	// warning, not a comment. Detected here instead, once per build, and
	// threaded into EVERY view's comment below so a reader of any artifact
	// sees that the model declared a join this target could not place.
	//
	// A column-less relationship is a separate, deliberate skip (relHasColumns,
	// the emits predicate) shared with ossie and snowflake-semantic-view, and
	// is left exactly as it was.
	var orphanRels []string
	for _, r := range m.Relationships {
		if _, ok := tableByName[strings.ToLower(r.Left)]; ok || !relHasColumns(r) {
			continue
		}
		orphanRels = append(orphanRels, relNotEmittedWarning("databricks-metric-view", r,
			relEndpointMissing(r.Left, "a metric view carries only the joins that leave the table it is rooted at")))
	}
	for _, w := range orphanRels {
		addWarning(w)
	}

	for _, t := range m.Tables {
		mv, own := d.buildView(m, t, resolve, metricOwner, tableByName, catalog, schema, orphanRels)
		if len(mv.Fields) == 0 {
			// no dimensions: cannot form a valid metric view. Every dimension was
			// suppressed by colliding with a measure name (see the seen-seeding in
			// buildView); a metric view requires at least one dimension, so the
			// whole table is dropped rather than emitted invalid or dimension-less.
			addWarning(fmt.Sprintf(
				"table %q skipped: all its dimensions collided with measure names, leaving none for a metric view (which requires at least one dimension)", t.Name))
			// The view this table's notes would have hung in is not written,
			// so they reach the reader only here. Kept rather than discarded
			// with the view: each names a further source construct that did
			// not survive, which the one-line skip warning does not restate.
			for _, w := range own {
				addWarning(w)
			}
			continue
		}
		for _, w := range own {
			addWarning(w)
		}
		var buf bytes.Buffer
		enc := yaml.NewEncoder(&buf)
		enc.SetIndent(2)
		if err := enc.Encode(mv); err != nil {
			return warnings, err
		}
		if err := enc.Close(); err != nil {
			return warnings, err
		}
		fname := strings.ToLower(t.Name) + ".yaml"
		if err := os.WriteFile(filepath.Join(dir, fname), buf.Bytes(), 0o644); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// buildView returns the constructed view and this table's OWN degrade notes
// (not m.Notes, which is folded into mv.Comment but never returned as a
// warning — the CLI already prints m.Notes separately, and double-printing it
// per emitted view would be wrong).
// modelNotes are build-level notes every view carries in its comment but which
// no single view owns (Emit has already returned them as warnings, so they are
// deliberately NOT added to own — see addNote).
func (d databricksMetricView) buildView(m *ir.Model, t ir.Table, resolve func(string) (ir.Expr, bool), metricOwner map[string]string, tableByName map[string]ir.Table, catalog, schema string, modelNotes []string) (dbxMetricView, []string) {
	// Seeded with m.Notes (cloned; Emit is read-only over m), the same
	// passthrough annotations every sibling target (cortex, supersimple,
	// snowflake-semantic-view, nao-yaml, nao-context-rules) folds into its
	// emitted artifact. Every emitted view carries them, rather than picking
	// one view per note by table-name mention: simpler, and correct for the
	// project's context-fairness index, which counts a note as "carried" per
	// target, not per table. modelNotes (build-level losses no one view owns,
	// e.g. a relationship no view can root — see Emit) ride along for the same
	// reason.
	notes := append(slices.Clone(m.Notes), modelNotes...)
	var own []string
	// addNote records a degrade note both in the artifact's comment text
	// (notes) and in this table's own returned warnings (own), keeping the two
	// in lockstep without duplicating every call site.
	//
	// Repeats are collapsed. One view resolves a physical source more than
	// once — its own, plus every joined table's — and a role-playing dimension
	// joins the same table twice, so an unusable source would otherwise be
	// reported once per resolution. The text names the table and the source it
	// is about, so a second copy carries no information the first does not.
	// (Emit collapses the same text ACROSS views, where the same joined table
	// is resolved again by each view that joins it.)
	seenNote := map[string]bool{}
	addNote := func(n string) {
		if seenNote[n] {
			return
		}
		seenNote[n] = true
		notes = append(notes, n)
		own = append(own, n)
	}
	mv := dbxMetricView{Version: "1.1", Source: dbxSourceFor(t, catalog, schema, addNote)}

	// Joins: relationships where this table is the LEFT (referencing) side. A
	// (Left, Right) pair with more than one relationship is a role-playing
	// dimension (e.g. ship-to vs bill-to customer) — every relationship in the
	// pair is kept and disambiguated by its own left column(s), rather than
	// dropping the second FK on a right-table-name collision. A pair with
	// exactly one relationship keeps today's plain lowercased-right-table-name
	// join, unchanged.
	//
	// The join's NAME prefers the one the source dialect declared, since that is
	// the author's own identifier for this join; it falls back to the generated
	// name (with a note) when there is none, when it collides, or when it cannot
	// serve as the alias a metric view's join actually is — see dbxJoinName.
	// Names are resolved against the WHOLE model, not just this view's joins, so
	// two views cannot disagree about what one relationship is called.
	//
	// The emits predicate also requires the LEFT table to exist: a relationship
	// leaving a table this model does not declare is written by no view (Emit
	// reports it), so it occupies no join name here and must not push a real
	// relationship's declared name onto the fallback path over a clash with
	// something never written — the same reasoning relHasColumns encodes.
	relNames, relWarn := relationshipNames(m.Relationships, "databricks-metric-view",
		func(r ir.Relationship) bool {
			if !relHasColumns(r) {
				return false
			}
			_, ok := tableByName[strings.ToLower(r.Left)]
			return ok
		},
		func(r ir.Relationship) string {
			name := strings.ToLower(r.Right)
			if suffix := relRoleSuffix(m.Relationships, r); suffix != "" {
				name += "_" + strings.ToLower(suffix)
			}
			return name
		}, dbxJoinName)
	// THE ALIAS IS SETTLED HERE. joinName is not just the `name:` key of the
	// join: it is the relation alias every ON condition and every joined
	// dimension's expr is qualified with, and — via dbxJoinAliases, built from
	// mv.Joins right after this loop — the alias a measure expression's IR
	// table qualifier is rewritten to by dbxRewriteQualifiers. Nothing else may
	// recompute it; see dbxJoinAliases for what broke when two sites did.
	for i, r := range m.Relationships {
		if !strings.EqualFold(r.Left, t.Name) || len(r.Columns) == 0 {
			continue
		}
		joinName := strings.ToLower(relNames[i])
		if relWarn[i] != "" {
			addNote(relWarn[i])
		}
		if w := relSynonymsWarning("databricks-metric-view", joinName, r); w != "" {
			addNote(w)
		}
		var conds []string
		for _, cp := range r.Columns {
			conds = append(conds, "source."+strings.ToLower(cp.Left)+" = "+joinName+"."+strings.ToLower(cp.Right))
		}
		// The joined table's OWN declared source, not the joining table's:
		// resolving via dbxQualify(catalog, schema, r.Right) alone would
		// paste this view's catalog/schema onto a table that may declare a
		// different physical address entirely (fixed as part of task 16's
		// fix round — a same-named-but-relocated join is exactly the defect
		// this task exists to close, left live here previously).
		joinSource := dbxQualify(catalog, schema, r.Right)
		if rt, ok := tableByName[strings.ToLower(r.Right)]; ok {
			joinSource = dbxSourceFor(rt, catalog, schema, addNote)
		} else {
			// The model declares no such table, so there is no declared source
			// to resolve and the profile reconstruction is a GUESS: a
			// well-formed three-part address for a table this build has never
			// seen. That is exactly the "plausible, wrong, unwarned" shape
			// splitSource's doc comment forbids, so it is written (dropping
			// the join would lose a relationship the source really declared)
			// but never silently.
			addNote(fmt.Sprintf(
				"join %q: the model declares no table %q, so its source was reconstructed from the profile as %q, which may not be where that table lives",
				joinName, r.Right, joinSource))
		}
		mv.Joins = append(mv.Joins, dbxJoin{
			Name:       joinName,
			Source:     joinSource,
			On:         strings.Join(conds, " and "),
			RightTable: strings.ToLower(r.Right),
		})
	}

	// Measures: metric-derived measures first (avoids emitting a near-duplicate
	// raw measure alongside its owning metric, e.g. orders_count next to the
	// orders metric — same expr, different name). Degrade window/conversion and
	// cross-grain derived metrics to a note rather than emit SQL we cannot stand
	// behind. Then append raw ir.Measures not already represented — by lowercased
	// name OR by rendered expression — so a table with >=1 metric still surfaces
	// measures no metric covers (e.g. a fact with one metric and a second raw
	// measure the model never turned into a metric), instead of silently
	// dropping every raw measure just because the table also has metrics.
	//
	// Built before fields (see the `seen` seeding below): Databricks requires
	// measure and dimension names to be unique across a metric view, and rejects
	// the view outright if they collide (METRIC_VIEW_INVALID_VIEW_DEFINITION:
	// "Measure and dimension names must be unique") — e.g. a source table with
	// both a precomputed `roas` column and a computed `roas` metric
	// (sum(attributed_revenue)/sum(spend)). Mirrors the identical fix in
	// snowflake-semantic-view (see its buildView), which resolves the same
	// collision by treating the computed metric as canonical.
	// Built from mv.Joins, above, so a measure expression and the join it names
	// can never disagree: whatever alias the joins loop settled on — generated
	// or declared — is the alias a qualifier is rewritten to. See
	// dbxJoinAliases for why this reconciliation has to be explicit.
	aliases := dbxJoinAliases(mv.Joins)
	// measureExpr resolves one rendered expression against this view's
	// relations, reporting the measure as dropped when a qualifier names
	// something the view does not join. Shared by the metric-derived and raw
	// passes below: both write into the same `measures:` list, so both have to
	// satisfy the same invariant.
	measureExpr := func(kind, name, expr string) (string, bool) {
		out, unresolved, ambiguous, ok := dbxRewriteQualifiers(expr, t.Name, aliases, tableByName)
		if !ok {
			addNote(kind + " " + name + ": expression references " + unresolved +
				", which this view does not join exactly once (" + expr + "), skipped")
			return "", false
		}
		// Each ambiguous qualifier is a fresh `a.b` dbxRewriteQualifiers could not
		// resolve to self, a join, or any table of the model — it went out as a
		// nested column access, which is the only reading available, but is
		// exactly as plausible a guess as the dropped case above when it was
		// really a table reference the model never declared (e.g. a phantom join
		// qualifier). Unlike that case, this one cannot be dropped: refusing it
		// would also refuse a legitimate struct/map field access, which the
		// SAME view's fields: already emit verbatim. So it is reported instead
		// — named, not silently resolved either way — mirroring the posture
		// dbxSourceFor's "the model declares no table" note already takes for a
		// join address it had to guess.
		for _, q := range ambiguous {
			addNote(fmt.Sprintf(
				"%s %q: qualifier %q names no table of the model; kept as a nested column access, which is wrong if it was meant as a table",
				kind, name, q))
		}
		return out, true
	}
	usedNames := map[string]bool{}
	for _, mt := range t.Metrics {
		if reason, degrade := dbxDegrade(mt); degrade {
			addNote("metric " + mt.Name + ": " + reason)
			continue
		}
		if dbxCrossGrain(mt.Def, metricOwner, t.Name) {
			addNote("metric " + mt.Name + ": references a measure on another table (cross-grain), not expressible as a single-grain metric-view measure")
			continue
		}
		name := strings.ToLower(mt.Name)
		// Checked/populated HERE, inside the loop, not after it: two metrics on
		// the same table whose lowercased names collide (e.g. AOV and aov) would
		// otherwise both emit, and Databricks rejects the entire view for a
		// duplicate name — the exact failure this dedup exists to prevent
		// elsewhere (field/measure collisions). The first metric wins.
		if usedNames[name] {
			addNote("metric " + mt.Name + ": skipped, its name collides with another metric on this table (case-insensitive)")
			continue
		}
		expr, ok := measureExpr("metric", mt.Name, renderSQL(mt.Def, resolve))
		if !ok {
			continue
		}
		// A simple metric renders through aggExpr exactly like a raw measure
		// (see dbxValidMeasureExpr's doc comment): an agg outside the set
		// aggExpr can render safely produces SQL Databricks rejects, taking the
		// whole view down with it. Skip and note rather than emit it.
		if !dbxValidMeasureExpr(expr) {
			addNote("metric " + mt.Name + ": aggregation cannot be rendered as valid Databricks SQL (" + expr + "), skipped")
			continue
		}
		usedNames[name] = true
		mv.Measures = append(mv.Measures, dbxMeasure{
			Name: name, Expr: expr,
			Comment: mt.Description, DisplayName: mt.Label, Synonyms: dbxCapSyn(mt.Synonyms),
		})
	}
	usedExprs := map[string]bool{}
	for _, ms := range mv.Measures {
		// Compared lowercased: renderSQL preserves the source column's original
		// case (e.g. "count(distinct ORDER_ID)" for an uppercase dbt column),
		// while the raw side below always lowercases its column via aggExpr. A
		// case-sensitive comparison would let a near-duplicate through purely
		// because of casing. This normalises the LOOKUP key only — the expr
		// actually emitted to YAML (ms.Expr) keeps its original case.
		usedExprs[strings.ToLower(ms.Expr)] = true
	}
	for _, ms := range t.Measures {
		name := strings.ToLower(ms.Name)
		// Resolved against the view's relations exactly like a metric's
		// expression above: a raw measure's Expr is USUALLY a bare column, but
		// nothing guarantees it (a source dialect may write a qualified or
		// compound one), and a qualifier that names no relation takes the whole
		// view down whichever pass emitted it.
		expr, ok := measureExpr("measure", ms.Name, aggExpr(ms.Agg, strings.ToLower(ms.Expr)))
		if !ok {
			continue
		}
		if usedNames[name] || usedExprs[strings.ToLower(expr)] {
			continue
		}
		// A raw measure whose Agg is empty, unknown to aggExpr, or missing an
		// argument (no Expr) renders SQL Databricks rejects. See
		// dbxValidMeasureExpr's doc comment for why that must never reach the
		// YAML.
		if !dbxValidMeasureExpr(expr) {
			addNote("measure " + ms.Name + ": aggregation " + ms.Agg + " cannot be rendered as valid Databricks SQL, skipped")
			continue
		}
		usedNames[name] = true
		// Deliberately NOT usedExprs[expr] = true here: the expression dedup
		// exists only to suppress a raw measure that near-duplicates a METRIC
		// (same expr, different name — e.g. orders_count beside an orders
		// metric). Extending it to raw-vs-raw would silently drop a second,
		// equally legitimate raw measure that happens to share an agg+column
		// with another (e.g. revenue and net_revenue both sum(amount)), even
		// though the two carry distinct names, labels, and descriptions. Raw-vs-
		// raw is deduped by name only; raw-vs-metric by name or expression.
		mv.Measures = append(mv.Measures, dbxMeasure{
			Name:     name,
			Expr:     expr,
			Comment:  ms.Description,
			Synonyms: dbxCapSyn(ms.Synonyms),
		})
	}

	// A metric view must declare at least one measure. A table whose source
	// declares none (a pure dimension table), or whose metrics all degraded,
	// still carries useful dimensions, so give it a row count. count(1) asserts
	// no business semantics — synthesising SUM/AVG over a column would invent
	// meaning the source never declared. Run before the fields loop below so
	// row_count is also protected by the seen-seeding, in the unlikely case a
	// dimension is itself named row_count.
	if len(mv.Measures) == 0 {
		mv.Measures = append(mv.Measures, dbxMeasure{
			Name:    "row_count",
			Expr:    "count(1)",
			Comment: "Row count. Synthesised: the source declares no measures for this table.",
		})
	}

	// Fields: own dimensions (bare expr), then joined tables' dimensions
	// (prefixed expr). Names must be unique within a view; seed `seen` with the
	// measure names already emitted above so a dimension colliding with a
	// measure is dropped (the computed measure wins — see the comment on the
	// measures block). A joined field whose bare name collides is prefixed with
	// the join name, mirroring the snowflake-semantic-view dedup.
	seen := map[string]bool{}
	for _, ms := range mv.Measures {
		seen[strings.ToLower(ms.Name)] = true
	}
	for _, f := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
		name := strings.ToLower(f.Name)
		if seen[name] {
			continue
		}
		seen[name] = true
		mv.Fields = append(mv.Fields, dbxField{
			Name: name, Expr: strings.ToLower(f.Expr),
			Comment: dbxFieldComment(f), Synonyms: dbxCapSyn(f.Synonyms),
		})
	}
	for _, j := range mv.Joins {
		jt := tableByName[j.RightTable]
		for _, f := range append(append([]ir.Field{}, jt.Dimensions...), jt.TimeDimensions...) {
			// A joined field is prefixed with the join name (<join>.<expr>) to
			// qualify it against the source alias. That's only well-formed when
			// Expr is a bare column — a compound expression (e.g. coalesce(region,
			// 'unknown'), date_trunc('month', ordered_at)) would become invalid SQL
			// like "dim_customer.coalesce(region, 'unknown')" and Databricks rejects
			// the entire view for it. Skip it and note it instead of guessing how to
			// qualify a compound expression.
			if !isIdent(f.Expr) {
				addNote("joined dimension " + j.Name + "." + f.Name + " (expr " + f.Expr + "): skipped, a compound expression cannot be safely qualified with the join name")
				continue
			}
			name := strings.ToLower(f.Name)
			if seen[name] {
				name = j.Name + "_" + name
			}
			if seen[name] {
				continue
			}
			seen[name] = true
			mv.Fields = append(mv.Fields, dbxField{
				Name: name, Expr: j.Name + "." + strings.ToLower(f.Expr),
				Comment: dbxFieldComment(f), Synonyms: dbxCapSyn(f.Synonyms),
			})
		}
	}

	var parts []string
	if t.Description != "" {
		parts = append(parts, t.Description)
	}
	if c := synonymClause(t.Synonyms); c != "" {
		parts = append(parts, c)
	}
	if d.Description != "" {
		parts = append(parts, d.Description)
	}
	for _, n := range notes {
		parts = append(parts, "Note: "+n)
	}
	mv.Comment = strings.Join(parts, " ")
	return mv, own
}

// dbxJoinAliases maps each joined table's lowercased IR name to the alias its
// join carries in this view, or to "" when the view joins that table more than
// once (a role-playing dimension: a bare `<table>.<column>` qualifier names no
// one of the two in particular, so it cannot be resolved).
//
// THIS IS THE COUPLING between a view's `joins:` and its `measures:`. A metric
// expression arrives qualified with IR TABLE names (renderSQL writes
// "count(distinct customer.c_customer_sk)"), while inside the view the only
// relations that exist are `source` and the join ALIASES. The two therefore
// have to be reconciled, and both sides must read the alias from the same
// place — dbxJoin.Name, via this function — rather than each recomputing it.
//
// They did not, until now. The join alias used to be strings.ToLower(r.Right),
// which happened to equal the qualifier renderSQL emits, so the reconciliation
// was a no-op that nobody had to write down. Preferring a DECLARED
// relationship name broke that accident instantly and silently: the view's
// joins became store_sales_to_customer while the measure still said
// customer.c_customer_sk, and Databricks rejects the whole view for the
// dangling relation. See dbxRewriteQualifiers, and the joins loop in buildView
// which is the other half of this pair.
func dbxJoinAliases(joins []dbxJoin) map[string]string {
	aliases := make(map[string]string, len(joins))
	count := map[string]int{}
	for _, j := range joins {
		count[j.RightTable]++
		aliases[j.RightTable] = j.Name
	}
	for table, n := range count {
		if n > 1 {
			aliases[table] = ""
		}
	}
	return aliases
}

// dbxRewriteQualifiers rewrites every `<table>.<column>` qualifier in a
// rendered measure expression to the relation that actually exists in this
// view: self (the view's own base table) is STRIPPED, because the base
// relation is aliased `source` and its columns are referenced bare, matching
// how fields are emitted; any other table becomes the alias of the join that
// pulls it in (see dbxJoinAliases, which is where that alias comes from).
//
// ok is false, with unresolved naming the offending qualifier, when a
// qualifier NAMES A TABLE OF THE MODEL (tables) that is neither self nor joined
// by this view exactly once. The caller must then DROP the measure with a note:
// emitting it would reference a relation the view does not declare, and
// Databricks rejects the entire view for that — every other measure, dimension
// and join of the file goes with it. dbxCrossGrain does not catch this case; it
// looks at metric REFERENCES (ir.Ref), not at bare column qualifiers, which is
// why a metric like `SUM(store_sales.x) / COUNT(DISTINCT customer.y)` reaches
// this function at all.
//
// A qualifier that names NO table of the model is not necessarily a table
// qualifier at all — it may be a struct or map field access (`payload.amount`,
// `attributes['k'].v`), which Databricks supports and which this view's
// `fields:` already emit verbatim (`expr: payload.amount`). It is passed
// through untouched rather than dropped, so a measure over a nested column
// survives exactly like the same field expression does in `fields:`. But
// `ir.Col.Table` is assigned purely syntactically wherever a metric expression
// is parsed (see aggLeaf): a lone, unqualified `a.b` carries no signal telling
// this function whether "a" was meant as a table or as a struct's own field
// name, and a table the model's author meant but never declared reads exactly
// like a struct root. Silently guessing "struct" is how a phantom qualifier
// used to escape unwarned and take Databricks' whole-file validation down with
// it (every other measure, dimension and join in the same view rejected for
// one dangling relation): unlike a name that IS a table of the model (handled
// above, where dropping is the only safe move), there is no drop that helps
// here — refusing every unresolved `a.b` would also refuse the legitimate
// struct case this passthrough exists for. So each one is returned in
// ambiguous, named but not resolved either way, and the caller reports it
// rather than staying quiet. Only the FIRST identifier of a dotted run is ever
// ambiguous this way: once a run's leading segment is known to name a real
// relation (self, stripped, or a join alias), every further dotted segment in
// the SAME run is unambiguously a nested column on that relation's row — not a
// fresh qualifier candidate — and is written through with no report, which is
// what lets a genuinely nested `SUM(store_sales.payload.amount)` still emit as
// `sum(payload.amount)` without noise.
//
// Tokenised rather than pattern-matched: the previous regex-based strip could
// not tell a qualifier from the same text inside a string literal, and could
// only handle the ONE self qualifier. sqlTokens round-trips faithfully, so
// rebuilding from the token stream preserves the expression exactly apart from
// the qualifiers rewritten here, and a literal containing "<table>." is a
// sqlString token that is never inspected.
func dbxRewriteQualifiers(expr, self string, aliases map[string]string, tables map[string]ir.Table) (out, unresolved string, ambiguous []string, ok bool) {
	toks := sqlTokens(expr)
	var b strings.Builder
	// chainContinuation is true for as long as the current run of dotted
	// identifiers descends from a segment already resolved to a real relation
	// (self or a join alias) or already reported as ambiguous — see the
	// "Only the FIRST identifier" paragraph above. It survives a "." token
	// untouched (connecting tissue within the same run) and is cleared by
	// anything else: an operator, paren, comma, or the run simply ending.
	chainContinuation := false
	for i := 0; i < len(toks); i++ {
		tk := toks[i]
		qualified := tk.typ == sqlIdent && i+2 < len(toks) &&
			toks[i+1].typ == sqlOther && toks[i+1].val == "." && toks[i+2].typ == sqlIdent
		if !qualified {
			b.WriteString(tk.val)
			if tk.val != "." {
				chainContinuation = false
			}
			continue
		}
		// Self first: a self-join (Left == Right) would also appear in aliases,
		// and stripping is what this view did for its own table before joins
		// existed at all. The column token itself is written by the next
		// iteration in both branches.
		if strings.EqualFold(tk.val, self) {
			i++ // skip the "."; the qualifier is dropped with it
			chainContinuation = true
			continue
		}
		key := strings.ToLower(tk.val)
		if alias, joined := aliases[key]; joined {
			if alias == "" {
				return "", tk.val, nil, false // role-playing: joined more than once, so ambiguous
			}
			b.WriteString(alias)
			chainContinuation = true
			continue
		}
		if _, isTable := tables[key]; isTable {
			return "", tk.val, nil, false // a table of the model this view does not join
		}
		// Not self, not a join, not a table of the model: written through as a
		// struct/map field access, with the "." and the field written by the
		// next iterations, exactly as the fields: pass emits a nested column.
		// Reported only when it is NOT already a continuation of a resolved
		// run (see chainContinuation's doc above) — a fresh, unresolved head
		// is where the table/struct ambiguity actually lives.
		if !chainContinuation {
			ambiguous = append(ambiguous, tk.val)
		}
		b.WriteString(tk.val)
		chainContinuation = true
	}
	return b.String(), "", ambiguous, true
}

// dbxJoinName reports whether name can be used as a metric view's join name.
// A join name here is not decoration: it is the relation ALIAS every one of
// that join's ON conditions and every joined dimension's expr is qualified
// with (`<join>.<column>`), so it has to be a bare identifier — anything else
// produces SQL Databricks rejects, taking the whole view down with it.
//
// `source` is refused as well. It is the alias the metric view's own base
// relation already carries (`source.<column>` on the left of every join
// condition), so a join claiming it would either duplicate the alias or
// silently re-point the base table's own columns at the joined table.
func dbxJoinName(name string) bool {
	return isIdent(name) && !strings.EqualFold(name, "source")
}

// dbxQualify builds a Unity Catalog table reference, three-part when a catalog
// is set, else two-part (keeps zero-value output well-formed).
func dbxQualify(catalog, schema, table string) string {
	t := strings.ToLower(table)
	if catalog == "" {
		return schema + "." + t
	}
	return catalog + "." + schema + "." + t
}

// dbxSourceFor resolves t's physical reference for a `source:` field —
// either a metric view's own `source:` or a `joins[].source:`, which share
// the exact same shape. `source:` holds ONE opaque string, unlike
// cortexBaseTable's separate Database/Schema/Table fields, so a genuine table
// reference is used verbatim regardless of its dot-part count (a two-part
// schema.table resolves fine against the current catalog). Only a source
// that is a QUERY rather than a reference (which the OSI spec permits) can't
// go here as-is; addNote records the resulting warning and the profile
// reconstruction is used instead.
func dbxSourceFor(t ir.Table, catalog, schema string, addNote func(string)) string {
	if t.Source != "" {
		if looksLikeQuery(t.Source) {
			addNote(querySourceWarning("databricks-metric-view", t.Name, t.Source))
		} else {
			return t.Source
		}
	}
	return dbxQualify(catalog, schema, t.Name)
}

// dbxFieldComment folds a field's enum into its description, since a metric-view
// field has no per-value enum slot.
func dbxFieldComment(f ir.Field) string { return appendClause(f.Description, enumClause(f.Enum)) }

// dbxCapSyn caps synonyms at the metric-view limit of 10 per field/measure.
func dbxCapSyn(syn []string) []string {
	if len(syn) > 10 {
		return syn[:10]
	}
	return syn
}

// dbxKnownAggs are the aggregate function names aggExpr renders explicitly:
// sum, count/count_distinct, avg/average, min, max, median, sum_boolean.
// count_distinct and sum_boolean both lower to a call whose own function name
// is "count"/"sum" respectively, so they need no separate entry here. Any
// other agg falls through aggExpr's default passthrough, which other targets
// rely on but which dbxValidMeasureExpr below must reject.
var dbxKnownAggs = map[string]bool{
	"sum": true, "count": true, "avg": true, "min": true, "max": true, "median": true,
}

// dbxValidMeasureExpr is the single choke point every rendered measure
// expression (metric-derived, via renderSQL, and raw, via aggExpr) passes
// through before being written to a view's `measures`. It reports whether
// expr is composed only of known-safe aggregate calls (dbxKnownAggs) with a
// non-empty argument, joined by arithmetic, literals and parens: the shape a
// simple or same-grain-derived metric-view measure can legally take.
//
// This exists because Databricks rejects the ENTIRE metric view when a
// single measure's expr is not valid SQL, not just that measure (verified
// live: one measure with an unsupported agg like percentile, an agg with no
// Databricks equivalent, or an aggregate call with no argument, e.g. sum()
// from a measure with no expr, loses every other measure, dimension and join
// of that view). An aggregation that cannot be rendered safely must
// therefore never reach the YAML; it is skipped and noted instead.
func dbxValidMeasureExpr(expr string) bool {
	toks := sqlTokens(expr)
	calls := 0
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].typ != sqlIdent || toks[i+1].val != "(" {
			continue // not a function call: a bare identifier, keyword, or operand
		}
		if !dbxKnownAggs[strings.ToLower(toks[i].val)] {
			return false // unsupported aggregate (e.g. percentile, sum_boolean's old passthrough)
		}
		if i+2 >= len(toks) || toks[i+2].val == ")" {
			return false // empty argument list, e.g. sum() from a measure with no expr
		}
		calls++
	}
	return calls > 0 // must contain at least one known aggregate call
}

// dbxDegrade reports metric kinds with no validated metric-view primitive
// (cumulative/conversion), matching the cortex/snowflake-semantic-view posture.
func dbxDegrade(mt ir.Metric) (string, bool) {
	switch mt.Def.(type) {
	case ir.Window:
		return "cumulative/windowed metric, no validated metric-view primitive (provisional)", true
	case ir.Conversion:
		return "conversion/funnel metric, no metric-view primitive (provisional)", true
	}
	return "", false
}

// dbxCrossGrain reports whether def references (directly or nested) a metric
// owned by a table other than self — i.e. a cross-grain derived metric that a
// single-source metric view cannot express without fan-out.
func dbxCrossGrain(def ir.Expr, owner map[string]string, self string) bool {
	found := false
	var walk func(ir.Expr)
	walk = func(e ir.Expr) {
		switch n := e.(type) {
		case ir.Ref:
			if o, ok := owner[n.Metric]; ok && !strings.EqualFold(o, self) {
				found = true
			}
		case ir.Binary:
			walk(n.Left)
			walk(n.Right)
		case ir.Agg:
			if n.Arg != nil {
				walk(n.Arg)
			}
			if n.Filter != nil {
				walk(n.Filter)
			}
		case ir.Window:
			walk(n.Base)
		case ir.Conversion:
			walk(n.Base)
			walk(n.Conv)
		}
	}
	walk(def)
	return found
}
