package dialect

import (
	"fmt"
	"strings"

	"github.com/benchouse/semglot/ir"
)

// ---- naming the aggregates a metric inlines ----
//
// An ir.Binary over inlined ir.Agg leaves — SUM(a) / SUM(b), or SUM(a) - SUM(b)
// — is a legitimate IR shape: five of the nine emitters (cortex,
// databricks-metric-view, nao-yaml, nao-context-rules, ossie) render it
// correctly, because in those targets a metric's definition IS a SQL
// expression, so an anonymous aggregate needs no name.
//
// The other four cannot. dbt, snowflake-semantic-view, lightdash and
// supersimple all express a derived metric as arithmetic over metrics
// REFERENCED BY NAME (dbt's type_params.metrics/numerator/denominator,
// Snowflake's "a metric must directly refer to another aggregate-level
// expression … without an aggregate", Lightdash's ${metric} syntax,
// supersimple's operand lookup through its `global` map). An inlined aggregate
// has no name, so all four had to drop the whole metric.
//
// hoistInlineAggs gives those aggregates names. Each inlined ir.Agg leaf
// becomes a synthesised simple metric (plus, for dbt, the backing ir.Measure
// dbt's metric->measure indirection requires), and the arithmetic tree is
// rewritten to reference it as an ir.Ref. Every one of the four then runs its
// EXISTING Ref-handling path unchanged — nothing new is taught to any emitter
// about aggregates.
//
// It is deliberately emit-side. Synthesising in ossie's parser was considered
// and rejected: ossie_emit.go emits a field per measure, so an IR-resident
// synthesised measure would round-trip back out of OSI as a declared field
// aliasing the column, as though the source had written it. That changes the
// data model, not the plumbing.
//
// Only those four emitters call it, so no synthesised name reaches cortex,
// databricks-metric-view, nao-yaml, nao-context-rules or ossie output.
//
// Two shapes are deliberately left alone, because naming them does not help:
//   - a leaf whose aggregate argument is neither a column, a raw SQL fragment,
//     nor absent (COUNT(*)) — there is no measure expression to give it;
//   - a bare ir.Col/ir.Raw operand sitting directly in the arithmetic (not
//     wrapped in an aggregate), which is not an aggregate at all.
//
// Both stay inlined and the emitter degrades them loudly, exactly as before.

// aggHoist is the result of hoistInlineAggs: per-table replacement Metrics and
// Measures lists. Both are freshly built slices, so a caller assigning them
// onto its own copy of an ir.Table never writes through to the model.
//
// The Measures list is for dbt ALONE. A synthesised measure exists only
// because a dbt metric cannot name an aggregation directly — it must point at
// a measure — whereas snowflake-semantic-view, lightdash and supersimple each
// carry the aggregation on the metric itself. Handing them measures they do
// not need would add nothing to their output but a degrade note per measure
// they cannot express (a COUNT(*) measure's `1`, or a compound expression),
// duplicating a note the metric itself already produced.
type aggHoist struct {
	metrics  map[string][]ir.Metric
	measures map[string][]ir.Measure
}

// metricsFor returns t's metric list with inlined aggregates named, or t's own
// list unchanged when nothing on t needed it.
func (h aggHoist) metricsFor(t ir.Table) []ir.Metric {
	if ms, ok := h.metrics[t.Name]; ok {
		return ms
	}
	return t.Metrics
}

// measuresFor returns t's measure list extended with the backing measures the
// synthesised metrics need. dbt only — see aggHoist.
func (h aggHoist) measuresFor(t ir.Table) []ir.Measure {
	if ms, ok := h.measures[t.Name]; ok {
		return ms
	}
	return t.Measures
}

// hoistInlineAggs names every aggregate a metric inlines inside arithmetic, and
// gives every top-level aggregate a backing measure. It does not mutate m.
//
// A synthesised metric is homed on the table its aggregate belongs to
// (ir.Agg.Table when that names a real table, else the table owning the metric
// being rewritten), so a cross-table ratio's two halves land on their own
// tables and each emitter's qualification (Snowflake's TABLE.METRIC,
// supersimple's cross-table relation walk) sees the truth. Lightdash's
// same-model-only restriction on ${metric} references then legitimately
// degrades such a metric, which is a Lightdash limit and not something this
// rewrite may paper over.
//
// The second job is the mirror image of the first: a metric whose definition is
// a top-level ir.Agg needs no naming (it already has one — its own) but DOES
// need a backing ir.Measure for dbt to point at, and measureFor finds none when
// the aggregate is a COUNT(*) or wraps a compound expression. One is
// synthesised under the metric's own name, exactly as ossie's parser already
// does for the simple aggregations it can resolve.
func hoistInlineAggs(m *ir.Model) aggHoist {
	h := aggHoist{
		metrics:  make(map[string][]ir.Metric, len(m.Tables)),
		measures: make(map[string][]ir.Measure, len(m.Tables)),
	}
	st := &hoistState{
		known:      make(map[string]bool, len(m.Tables)),
		minters:    make(map[string]*nameMinter, len(m.Tables)),
		byLeaf:     make(map[string]map[string]string, len(m.Tables)),
		newMetrics: map[string][]ir.Metric{},
		newMeasure: map[string][]ir.Measure{},
	}
	for _, t := range m.Tables {
		st.known[t.Name] = true
		st.minters[t.Name] = newNameMinter(t)
		// Seed the leaf index with the table's OWN simple metrics, so an
		// inlined aggregate the source has already published under a name
		// reuses that name instead of minting a synonym beside it: TPC-DS's
		// customer_lifetime_value inlines SUM(store_sales.ss_ext_sales_price),
		// which the same model already publishes as total_sales. First
		// declaration wins, so the choice does not depend on map iteration.
		leaves := map[string]string{}
		for _, mt := range t.Metrics {
			agg, ok := mt.Def.(ir.Agg)
			if !ok {
				continue
			}
			if key := renderSQL(agg, noMetricResolve); leaves[key] == "" {
				leaves[key] = mt.Name
			}
		}
		st.byLeaf[t.Name] = leaves
	}

	kept := make(map[string][]ir.Metric, len(m.Tables))
	for _, t := range m.Tables {
		out := make([]ir.Metric, 0, len(t.Metrics))
		for _, mt := range t.Metrics {
			switch def := mt.Def.(type) {
			case ir.Binary:
				rewritten := mt
				rewritten.Def = st.rewrite(def, mt, t)
				out = append(out, rewritten)
			case ir.Agg:
				st.backTopLevel(mt, def, t)
				out = append(out, mt)
			default:
				out = append(out, mt)
			}
		}
		kept[t.Name] = out
	}

	for _, t := range m.Tables {
		h.metrics[t.Name] = concatMetrics(kept[t.Name], st.newMetrics[t.Name])
		h.measures[t.Name] = concatMeasures(t.Measures, st.newMeasure[t.Name])
	}
	return h
}

// hoistState is hoistInlineAggs' working state: the model's table names, one
// name minter and one leaf-dedup index per table, and the metrics/measures
// synthesised onto each table so far.
type hoistState struct {
	known      map[string]bool
	minters    map[string]*nameMinter
	byLeaf     map[string]map[string]string // table -> rendered leaf SQL -> minted name
	newMetrics map[string][]ir.Metric
	newMeasure map[string][]ir.Measure
}

// rewrite replaces every nameable ir.Agg leaf of an arithmetic tree with an
// ir.Ref to a synthesised metric. It returns a new tree; e is not modified.
func (st *hoistState) rewrite(e ir.Expr, mt ir.Metric, owner ir.Table) ir.Expr {
	switch n := e.(type) {
	case ir.Binary:
		n.Left = st.rewrite(n.Left, mt, owner)
		n.Right = st.rewrite(n.Right, mt, owner)
		return n
	case ir.Agg:
		name, ok := st.nameLeaf(n, mt, owner)
		if !ok {
			return n // left inlined; the emitter degrades it loudly
		}
		return ir.Ref{Metric: name}
	default:
		return e
	}
}

// nameLeaf returns the name of the metric standing for leaf: an existing simple
// metric on the home table with the identical definition when there is one,
// otherwise a synthesised metric (and its backing measure) created on first
// sight. Two identical leaves on one table therefore share one metric. The
// index key is the leaf's rendered SQL, so it distinguishes a filtered
// aggregate from an unfiltered one over the same column, which the minted name
// alone would not.
func (st *hoistState) nameLeaf(leaf ir.Agg, mt ir.Metric, owner ir.Table) (string, bool) {
	home := owner.Name
	if leaf.Table != "" && st.known[leaf.Table] {
		home = leaf.Table
	}
	key := renderSQL(leaf, noMetricResolve)
	if name, ok := st.byLeaf[home][key]; ok {
		return name, true
	}
	expr, ok := measureExprOf(leaf)
	if !ok {
		return "", false
	}
	name := st.minters[home].mint(aggBaseName(leaf, mt.Name))
	st.newMetrics[home] = append(st.newMetrics[home], ir.Metric{
		Name:        name,
		Description: synthesizedNote(mt.Name),
		Def:         leaf,
	})
	// The measure carries NO description on purpose. dbt's measure block has no
	// description field, so the only place one would surface is emitModel's
	// columns[] — where it would document the PHYSICAL COLUMN as "component
	// aggregate of metric x", which is false about the column. The synthesised
	// metric carries the note instead, which is where it is true.
	st.newMeasure[home] = append(st.newMeasure[home], ir.Measure{
		Field: ir.Field{Name: name, Expr: expr},
		Agg:   leaf.Func,
	})
	st.byLeaf[home][key] = name
	return name, true
}

// backTopLevel gives a metric whose whole definition is one aggregate the
// backing measure dbt needs, when the table declares none that measureFor can
// match. The measure takes the metric's own name (dbt keeps measures and
// metrics in separate namespaces, and ossie's parser already pairs them like
// this); a clash with an existing measure or dimension name is settled by the
// minter.
//
// An aggregate stamped with a DIFFERENT table than the metric's own is skipped:
// dbt looks measures up on the metric's own semantic model, so a measure
// planted there over another table's column would be a plausible, wrong,
// unwarned definition. The emitter warns about it instead.
func (st *hoistState) backTopLevel(mt ir.Metric, def ir.Agg, owner ir.Table) {
	if def.Table != "" && def.Table != owner.Name {
		return
	}
	all := concatMeasures(owner.Measures, st.newMeasure[owner.Name])
	if _, ok := measureNameFor(all, def); ok {
		return
	}
	expr, ok := measureExprOf(def)
	if !ok {
		return
	}
	name := st.minters[owner.Name].mintMeasure(mt.Name)
	st.newMeasure[owner.Name] = append(st.newMeasure[owner.Name], ir.Measure{
		Field: ir.Field{Name: name, Description: mt.Description, Expr: expr},
		Agg:   def.Func,
	})
}

// measureExprOf returns the measure expression backing an aggregate: the bare
// column for a Col arg, the SQL text for a Raw one, and `1` for the absent arg
// of a COUNT(*) — dbt spells a row count as `agg: count` with `expr: 1`, and
// measureNameFor looks for exactly that. ok=false for anything else (a literal
// or nested arithmetic argument), which has no measure form.
func measureExprOf(a ir.Agg) (string, bool) {
	switch arg := a.Arg.(type) {
	case ir.Col:
		return arg.Name, true
	case ir.Raw:
		return arg.SQL, true
	case nil:
		if strings.EqualFold(a.Func, "count") {
			return "1", true
		}
		return "", false
	default:
		return "", false
	}
}

// aggBaseName is the naming scheme for a synthesised metric, before the
// minter's collision check:
//
//	<column>_<agg>   when the aggregate is over one bare column
//	                 — sum(o_totalprice) -> o_totalprice_sum
//	<metric>_<agg>   otherwise (a compound Raw argument, or COUNT(*), neither of
//	                 which names a column) — the owning metric supplies the stem
//	                 instead, e.g. revenue_sum, order_count_count
//
// Keying the common case on the COLUMN rather than the metric is deliberate:
// two metrics that both inline sum(amount) then agree on one name and share one
// synthesised metric, instead of minting a near-duplicate each.
func aggBaseName(a ir.Agg, metric string) string {
	agg := strings.ToLower(a.Func)
	if agg == "" {
		agg = "agg"
	}
	if c, ok := a.Arg.(ir.Col); ok && isIdent(c.Name) {
		return strings.ToLower(c.Name) + "_" + agg
	}
	return strings.ToLower(metric) + "_" + agg
}

// synthesizedNote is the description a synthesised metric and its measure
// carry, so a reader of the emitted artifact can tell a name semglot minted
// from one the source declared, and knows which metric it was minted for.
func synthesizedNote(metric string) string {
	return "Component aggregate of metric " + metric +
		", named by semglot so a derived metric can reference it."
}

// nameMinter mints collision-free names on one table. The two namespaces are
// tracked separately because a synthesised name occupies both: dbt keeps metric
// names and semantic-model member names (entities, dimensions, measures) apart,
// so one minted name can serve both the simple metric and its backing measure —
// which is also what ossie's parser does — while a measure minted on its own
// (backTopLevel) only needs to clear the member namespace.
//
// `fields` deliberately holds BOTH the IR name and the physical expression of
// every dimension and measure. dbt and snowflake-semantic-view collide on the
// IR name; Lightdash's columns[] is keyed by the physical COLUMN and it turns
// every entry into a dimension, resolving a dimension/metric clash in favour of
// the dimension and SILENTLY DROPPING the metric (see dialect/README.md's name
// collisions section). Reserving both spellings means a minted name cannot
// delete the very metric it was minted to rescue.
//
// Matching is case-insensitive: snowflake-semantic-view uppercases every name
// it emits, so two names differing only in case are one name there.
type nameMinter struct {
	metrics map[string]bool
	fields  map[string]bool
}

func newNameMinter(t ir.Table) *nameMinter {
	n := &nameMinter{metrics: map[string]bool{}, fields: map[string]bool{}}
	for _, mt := range t.Metrics {
		n.metrics[minterKey(mt.Name)] = true
	}
	reserve := func(fs ...ir.Field) {
		for _, f := range fs {
			n.fields[minterKey(f.Name)] = true
			n.fields[minterKey(f.Expr)] = true
		}
	}
	reserve(t.Dimensions...)
	reserve(t.TimeDimensions...)
	for _, ms := range t.Measures {
		reserve(ms.Field)
	}
	for _, col := range t.PrimaryKey {
		n.fields[minterKey(col)] = true
	}
	return n
}

func minterKey(s string) string { return strings.ToLower(s) }

// mint reserves base in BOTH namespaces, for a name that will be used as a
// metric and as its backing measure. The fallback on a collision is a numeric
// suffix — base_2, base_3, … — so the result stays a pure function of the
// table's own contents and the order the model declares its metrics in.
func (n *nameMinter) mint(base string) string {
	name := base
	for i := 2; n.metrics[minterKey(name)] || n.fields[minterKey(name)]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	n.metrics[minterKey(name)] = true
	n.fields[minterKey(name)] = true
	return name
}

// mintMeasure reserves base in the semantic-model member namespace only, for a
// measure that deliberately shares its name with an existing metric.
func (n *nameMinter) mintMeasure(base string) string {
	name := base
	for i := 2; n.fields[minterKey(name)]; i++ {
		name = fmt.Sprintf("%s_%d", base, i)
	}
	n.fields[minterKey(name)] = true
	return name
}

// noMetricResolve renders a metric Ref as its bare name instead of inlining the
// referenced definition — the hoist compares leaf aggregates textually and must
// not follow references while doing it.
func noMetricResolve(string) (ir.Expr, bool) { return nil, false }

// concatMetrics / concatMeasures build a fresh slice from two, so an emitter
// assigning the result onto its own copy of an ir.Table can never append
// through into the model's backing array.
func concatMetrics(a, b []ir.Metric) []ir.Metric {
	out := make([]ir.Metric, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}

func concatMeasures(a, b []ir.Measure) []ir.Measure {
	out := make([]ir.Measure, 0, len(a)+len(b))
	out = append(out, a...)
	return append(out, b...)
}
