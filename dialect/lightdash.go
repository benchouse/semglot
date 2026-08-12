package dialect

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(lightdash{}) }

// lightdash emits a dbt schema.yml annotated with Lightdash meta: blocks, the
// form Lightdash Cloud ingests from a connected dbt project. Zero value is
// usable; the build command sets Name/Description/DbtMetaKeyPath from the profile.
// Emit does not mutate m.
type lightdash struct {
	ModelName   string
	Description string
	// DbtMetaKeyPath is the YAML key path the Lightdash meta block is written
	// under. Empty or "config.meta" writes `config.meta:`, dbt's preferred form
	// since 1.10 (2025-06-16); "meta" writes top-level `meta:`, which 1.10
	// deprecated and which only a project pinned to dbt 1.9 or earlier needs.
	//
	// This cannot be derived. semglot writes a file that someone else's dbt
	// parses later, so the emitting process never sees the dbt that will read
	// it — hence a config rather than detection.
	DbtMetaKeyPath string
}

func (lightdash) Name() string { return "lightdash" }
func (lightdash) WithOptions(o Options) Emitter {
	return lightdash{ModelName: o.Name, Description: o.Description, DbtMetaKeyPath: o.DbtMetaKeyPath}
}

// ---- Lightdash YAML shapes ----
//
// Each meta payload can hang under either `meta:` (dbt <=1.9) or `config.meta:`
// (dbt 1.10+/Fusion). The model/column structs carry BOTH a Meta and a Config
// field; the emitter sets exactly one based on DbtMetaKeyPath, so the struct tags
// stay static while placement varies.

type ldFile struct {
	Version int       `yaml:"version"`
	Models  []ldModel `yaml:"models"`
}

type ldModel struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description,omitempty"`
	Meta        *ldModelMeta `yaml:"meta,omitempty"`
	Config      *ldModelCfg  `yaml:"config,omitempty"`
	Columns     []ldColumn   `yaml:"columns,omitempty"`
}

type ldModelCfg struct {
	Meta *ldModelMeta `yaml:"meta,omitempty"`
}

type ldModelMeta struct {
	PrimaryKey string              `yaml:"primary_key,omitempty"`
	Joins      []ldJoin            `yaml:"joins,omitempty"`
	Metrics    map[string]ldMetric `yaml:"metrics,omitempty"`
}

func (m *ldModelMeta) empty() bool {
	return m.PrimaryKey == "" && len(m.Joins) == 0 && len(m.Metrics) == 0
}

type ldJoin struct {
	Join         string `yaml:"join"`
	SQLOn        string `yaml:"sql_on"`
	Relationship string `yaml:"relationship,omitempty"`
}

type ldColumn struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description,omitempty"`
	Meta        *ldColMeta `yaml:"meta,omitempty"`
	Config      *ldColCfg  `yaml:"config,omitempty"`
}

type ldColCfg struct {
	Meta *ldColMeta `yaml:"meta,omitempty"`
}

type ldColMeta struct {
	Dimension *ldDimension        `yaml:"dimension,omitempty"`
	Metrics   map[string]ldMetric `yaml:"metrics,omitempty"`
}

type ldDimension struct {
	Type  string `yaml:"type,omitempty"`
	Label string `yaml:"label,omitempty"`
}

// ldMetric is a Lightdash metric definition. Description is the metric's
// `description:` key, which Lightdash shows beside the metric in the explore.
// It is set only for a metric semglot MINTED (agg_names.go's hoist): such a
// metric appears in the layer under a name nobody authored, so without its
// synthesizedNote a reader has no way to tell it from a hand-written one, or
// to find the metric it was minted for.
type ldMetric struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description,omitempty"`
	SQL         string `yaml:"sql,omitempty"`
}

// ldColumnSet builds the ordered columns[] list, keyed by physical column name,
// so a dimension and a metric backed by the same column share one entry and a
// metric's backing column is created even when it is not itself a dimension.
type ldColumnSet struct {
	order  []string
	byName map[string]*ldColumn
}

func newColumnSet() *ldColumnSet { return &ldColumnSet{byName: map[string]*ldColumn{}} }

func (s *ldColumnSet) get(col string) *ldColumn {
	c, ok := s.byName[col]
	if !ok {
		c = &ldColumn{Name: col}
		s.byName[col] = c
		s.order = append(s.order, col)
	}
	return c
}

func (s *ldColumnSet) list() []ldColumn {
	out := make([]ldColumn, 0, len(s.order))
	for _, name := range s.order {
		out = append(out, *s.byName[name])
	}
	return out
}

// dimension records f as a Lightdash dimension on its backing column. Enum
// meanings and synonyms fold into the column description (Lightdash has no
// enum or synonym slot). type is set only when typ is non-empty; a label is
// set only when the IR dimension name differs from the physical column.
func (s *ldColumnSet) dimension(f ir.Field, typ string) {
	col := f.Expr
	if col == "" {
		col = f.Name
	}
	c := s.get(col)
	if c.Description == "" {
		desc := appendClause(f.Description, enumClause(f.Enum))
		desc = appendClause(desc, synonymClause(f.Synonyms))
		c.Description = desc
	}
	if typ != "" || (f.Name != "" && f.Name != col) {
		if c.Meta == nil {
			c.Meta = &ldColMeta{}
		}
		if c.Meta.Dimension == nil {
			c.Meta.Dimension = &ldDimension{}
		}
		if typ != "" {
			c.Meta.Dimension.Type = typ
		}
		if f.Name != "" && f.Name != col {
			c.Meta.Dimension.Label = f.Name
		}
	}
}

// ldDimensionType returns a Lightdash dimension type when a confident signal
// exists, else "" so the type is omitted (Lightdash auto-detects from the
// warehouse). Signals: an explicit DataType; a time dimension is timestamp; an
// is_/has_ prefix is boolean; an _id/_sk suffix (or bare "id") is number.
func ldDimensionType(f ir.Field, isTime bool) string {
	if f.DataType != "" {
		return ldMapType(f.DataType)
	}
	if isTime {
		return "timestamp"
	}
	n := strings.ToLower(f.Name)
	switch {
	case strings.HasPrefix(n, "is_"), strings.HasPrefix(n, "has_"):
		return "boolean"
	case strings.HasSuffix(n, "_id"), strings.HasSuffix(n, "_sk"), n == "id":
		return "number"
	}
	return ""
}

// ldMapType normalizes a warehouse column type to one of Lightdash's five
// dimension types. An unrecognized type returns "" so the type is omitted.
func ldMapType(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "number", "numeric", "decimal", "int", "integer", "bigint", "smallint",
		"float", "double", "double precision", "real":
		return "number"
	case "varchar", "text", "string", "char", "character varying":
		return "string"
	case "boolean", "bool":
		return "boolean"
	case "date":
		return "date"
	case "timestamp", "datetime", "timestamp_ntz", "timestamp_tz", "timestamp_ltz":
		return "timestamp"
	default:
		return ""
	}
}

// drop removes col from the set and returns the column it removed, so a
// dimension that cannot coexist with a same-named metric stops being emitted.
func (s *ldColumnSet) drop(col string) (*ldColumn, bool) {
	c, ok := s.byName[col]
	if !ok {
		return nil, false
	}
	delete(s.byName, col)
	order := make([]string, 0, len(s.order)-1) // a fresh slice: callers may hold s.order
	for _, n := range s.order {
		if n != col {
			order = append(order, n)
		}
	}
	s.order = order
	return c, true
}

// names returns the set of column names currently in the set. Lightdash turns
// every entry in columns[] into a dimension, including one the emitter created
// only to host a column-level metric, so this is exactly the set of dimension
// names the model will publish.
func (s *ldColumnSet) names() map[string]bool {
	out := make(map[string]bool, len(s.order))
	for _, n := range s.order {
		out[n] = true
	}
	return out
}

// hasMetric reports whether any column carries a metric.
func (s *ldColumnSet) hasMetric() bool {
	for _, c := range s.byName {
		if c.Meta != nil && len(c.Meta.Metrics) > 0 {
			return true
		}
	}
	return false
}

// metric records a column-level Lightdash metric on col.
func (s *ldColumnSet) metric(col, name string, met ldMetric) {
	c := s.get(col)
	if c.Meta == nil {
		c.Meta = &ldColMeta{}
	}
	if c.Meta.Metrics == nil {
		c.Meta.Metrics = map[string]ldMetric{}
	}
	c.Meta.Metrics[name] = met
}

// simpleColumnMetric reports whether mt is a plain, unfiltered aggregate over a
// single column, and if so returns the backing column and the column-level
// Lightdash metric. A filtered agg, a count(*) (nil arg), a Raw arg, or an
// unmapped aggregate returns ok=false (the caller degrades it).
func simpleColumnMetric(mt ir.Metric) (string, ldMetric, bool) {
	agg, ok := mt.Def.(ir.Agg)
	if !ok || agg.Filter != nil {
		return "", ldMetric{}, false
	}
	typ, ok := ldAggType(agg.Func)
	if !ok {
		return "", ldMetric{}, false
	}
	col, ok := agg.Arg.(ir.Col)
	if !ok {
		return "", ldMetric{}, false
	}
	return col.Name, ldMetric{Type: typ}, true
}

// ldAggType maps an IR aggregation function to a Lightdash column-metric type.
func ldAggType(fn string) (string, bool) {
	switch strings.ToLower(fn) {
	case "sum":
		return "sum", true
	case "avg", "average":
		return "average", true
	case "count":
		return "count", true
	case "count_distinct":
		return "count_distinct", true
	case "min":
		return "min", true
	case "max":
		return "max", true
	case "median":
		return "median", true
	}
	return "", false
}

// renderLightdash renders a reference-only derived tree to Lightdash ${metric}
// syntax. Lightdash type: number metrics may reference only other metrics (and
// literals), so any node that is not a Ref/Lit/Binary makes the whole metric
// non-representable (ok=false) and the caller degrades it. Kept separate from
// renderSQL and renderDerived: each target has its own reference discipline
// (Cortex inlines SQL, dbt keeps bare measure refs, Lightdash uses ${...}).
func renderLightdash(e ir.Expr) (string, bool) {
	switch n := e.(type) {
	case ir.Ref:
		return "${" + n.Metric + "}", true
	case ir.Lit:
		return n.Value, true
	case ir.Binary:
		l, lok := renderLightdashOperand(n.Left)
		r, rok := renderLightdashOperand(n.Right)
		if !lok || !rok {
			return "", false
		}
		return l + " " + n.Op + " " + r, true
	default:
		return "", false
	}
}

// renderLightdashOperand renders a Binary operand, parenthesizing a nested
// Binary so operator grouping in the emitted SQL matches the AST.
func renderLightdashOperand(e ir.Expr) (string, bool) {
	s, ok := renderLightdash(e)
	if !ok {
		return "", false
	}
	if _, isBin := e.(ir.Binary); isBin {
		return "(" + s + ")", true
	}
	return s, true
}

// metricRefs returns the metric names a reference-only tree references.
func metricRefs(e ir.Expr) []string {
	switch n := e.(type) {
	case ir.Ref:
		return []string{n.Metric}
	case ir.Binary:
		return append(metricRefs(n.Left), metricRefs(n.Right)...)
	}
	return nil
}

// degradeNote explains why a metric was not emitted to Lightdash. Window and
// conversion metrics reuse cortexDegrade's wording; other misses are filtered
// or compound aggregates, or references that did not resolve to emitted
// same-table simple metrics.
func degradeNote(mt ir.Metric) string {
	if reason, ok := cortexDegrade(mt.Def); ok {
		return "metric " + mt.Name + " not emitted to Lightdash: " + reason
	}
	return "metric " + mt.Name + " not emitted to Lightdash: no Lightdash primitive (filtered/compound aggregate or unresolved reference)"
}

// derivedModelMetric reports whether mt is a reference-only ratio/derived metric
// whose every referenced metric is an emitted same-table simple metric, and if
// so returns its model-level Lightdash form (type: number, ${...} sql).
func derivedModelMetric(mt ir.Metric, simple map[string]bool) (ldMetric, bool) {
	if _, ok := mt.Def.(ir.Binary); !ok {
		return ldMetric{}, false
	}
	sql, ok := renderLightdash(mt.Def)
	if !ok {
		return ldMetric{}, false
	}
	for _, r := range metricRefs(mt.Def) {
		if !simple[r] {
			return ldMetric{}, false
		}
	}
	return ldMetric{Type: "number", SQL: sql}, true
}

// derivedRepresentable reports whether mt would emit as a model-level derived
// metric, separating a metric Lightdash could have carried (whose loss to a name
// collision is worth its own note) from one that degrades on its own terms and
// is already covered by degradeNote.
func derivedRepresentable(mt ir.Metric, simple map[string]bool) bool {
	_, ok := derivedModelMetric(mt, simple)
	return ok
}

// joinSQLOn renders a relationship's equi-join columns as a Lightdash sql_on
// expression: ${left.col} = ${right.col}, ANDed for a composite key.
func joinSQLOn(r ir.Relationship) string {
	parts := make([]string, len(r.Columns))
	for i, cp := range r.Columns {
		parts[i] = "${" + r.Left + "." + cp.Left + "} = ${" + r.Right + "." + cp.Right + "}"
	}
	return strings.Join(parts, " and ")
}

// ldKeyColumns returns the columns of t that Lightdash resolves structurally:
// the single-column primary_key it emits, and every join key naming t on either
// side of a relationship. These columns cannot be dropped to settle a name
// collision: meta.primary_key and a join's sql_on both point at a dimension by
// name, and Lightdash rejects the explore when that dimension is missing, so a
// metric colliding with one of them degrades instead (see resolveNameCollisions).
func ldKeyColumns(t ir.Table, rels []ir.Relationship) map[string]bool {
	keys := map[string]bool{}
	if len(t.PrimaryKey) == 1 { // a composite PK is not emitted, so it pins nothing
		keys[t.PrimaryKey[0]] = true
	}
	for _, r := range rels {
		for _, cp := range r.Columns {
			if r.Left == t.Name {
				keys[cp.Left] = true
			}
			if r.Right == t.Name {
				keys[cp.Right] = true
			}
		}
	}
	return keys
}

// keyCollisionNote explains a metric dropped because its name is a key column.
func keyCollisionNote(metric, table string) string {
	return "metric " + metric + " not emitted to Lightdash: its name collides with column " + metric +
		" on table " + table + ", which is a primary key or join key and must stay a dimension"
}

// resolveNameCollisions settles the single namespace Lightdash gives dimensions
// and metrics. Every entry in columns[] becomes a dimension, and when a metric
// carries the same name Lightdash keeps the dimension and silently skips the
// metric ("Skipped metric X because a dimension with the same name exists.
// Dimensions take priority."), a warning in the deploy log and nothing in the
// emitted YAML, so downstream it just looks like the metric was never defined.
//
// The colliding column is dropped and the computed metric wins, matching how
// snowflake-semantic-view and databricks-metric-view resolve the same clash
// (both seed their dedup with measure names so a colliding field is the one that
// goes). A dropped column may itself have hosted metrics. The common shape is
// sum(attributed_revenue) named attributed_revenue, where the metric's own
// backing column is the collision, so those are re-homed at model level with an
// explicit ${TABLE}.col sql: the same aggregation, spelled out instead of
// inferred from the column it hung on. Re-homing moves metrics and never removes
// one, so a derived ${ref} to a re-homed metric still resolves.
//
// Key columns are handled by the caller and never reach here.
func (s *ldColumnSet) resolveNameCollisions(mm *ldModelMeta, table string) []string {
	metricNames := map[string]bool{}
	for name := range mm.Metrics {
		metricNames[name] = true
	}
	for _, c := range s.byName {
		if c.Meta == nil {
			continue
		}
		for name := range c.Meta.Metrics {
			metricNames[name] = true
		}
	}
	var notes []string
	for _, col := range append([]string{}, s.order...) { // s.order is mutated by drop
		if !metricNames[col] {
			continue
		}
		c, _ := s.drop(col)
		if c.Meta != nil {
			for name, met := range c.Meta.Metrics {
				if mm.Metrics == nil {
					mm.Metrics = map[string]ldMetric{}
				}
				met.SQL = "${TABLE}." + col
				mm.Metrics[name] = met
			}
		}
		notes = append(notes, "table "+table+": column "+col+
			" not emitted as a dimension: its name collides with metric "+col+
			", and Lightdash resolves that clash in favour of the dimension, silently dropping the metric")
	}
	return notes
}

// Emit writes the IR as one dbt schema.yml carrying Lightdash annotations. It
// does not mutate m: passthrough notes and degrade notes accumulate in a local
// slice and render as a leading # semglot: comment block.
// Emit writes schema.yml and returns its degrade notes as warnings.
//
// The notes are ALSO written into the file as a leading `# semglot:` comment
// block, deliberately: the emitted schema.yml is committed and deployed by
// `lightdash deploy`, so a reader of the artifact should see what was dropped
// without going back to the build log. Returning them as well lets the CLI
// surface them at build time, which is the channel main added.
func (l lightdash) Emit(m *ir.Model, dir string) ([]string, error) {
	var notes []string
	notes = append(notes, m.Notes...)

	// Default to config.meta: it has been dbt's preferred form since 1.10
	// (2025-06-16) and top-level meta is deprecated there. Only an explicit
	// "meta" opts back into the legacy form, for a project pinned to dbt 1.9
	// or earlier.
	configMeta := l.DbtMetaKeyPath != "meta"
	// A Lightdash type: number metric may reference only other metrics, so an
	// aggregate inlined in the arithmetic makes the whole metric
	// non-representable. Naming those aggregates lets derivedModelMetric run on
	// the result unchanged. Its same-model restriction still applies and still
	// degrades loudly: a component homed on another table is a Lightdash limit,
	// not something this rewrite may paper over.
	hoist := hoistInlineAggs(m)
	// A minted component exists only so the metric it was hoisted out of can
	// be expressed. When that metric degrades anyway (classically Lightdash's
	// same-model restriction on ${metric} references), the component is still
	// a valid aggregate and is still emitted — dropping it would lose an
	// aggregate the source really declared, and on a dimension-only table it
	// is the difference between one real metric and none. But it is then an
	// orphan: a metric under a name nobody authored that nothing references.
	// Emitted WITH its synthesizedNote description and reported below, so the
	// artifact explains itself either way. Recorded in declaration order, not
	// in a map, so the note order is deterministic.
	type mintedComponent struct{ name, table string }
	var mintedEmitted []mintedComponent
	referenced := map[string]bool{} // minted names an EMITTED metric refers to
	f := ldFile{Version: 2}
	for _, t := range m.Tables {
		t.Metrics = hoist.metricsFor(t) // t is the range's own copy; m is untouched
		// ir.Table.Source — the physical address an ossie dataset declares —
		// has no Lightdash home. The artifact is a dbt schema.yml, and
		// Lightdash takes each model's relation from dbt's compiled manifest;
		// its model meta has no address key (see dbtSchemaSourceWarning).
		// Reported rather than dropped in silence.
		if t.Source != "" {
			notes = append(notes, dbtSchemaSourceWarning("lightdash", t.Name, t.Source))
		}
		cols := newColumnSet()
		for _, d := range t.Dimensions {
			cols.dimension(d, ldDimensionType(d, false))
		}
		for _, d := range t.TimeDimensions {
			cols.dimension(d, ldDimensionType(d, true))
		}

		// Pass 1: which metrics become column-level simple metrics. Their names
		// are the only references a derived metric may use, so a ratio over a
		// degraded or cross-table metric degrades rather than dangling.
		//
		// A metric whose name is a key column is excluded here as well as below:
		// it will not be emitted (dropping the key column to make room for it
		// would dangle meta.primary_key or a join's sql_on, see ldKeyColumns),
		// and leaving it in `simple` would let a derived metric reference it.
		keys := ldKeyColumns(t, m.Relationships)
		// The dimension names this model will publish: the dimension columns
		// above plus the backing column of every simple metric, since cols.metric
		// creates an entry for a backing column that is not itself a dimension
		// and Lightdash makes a dimension of that too.
		emitted := cols.names()
		for _, mt := range t.Metrics {
			if col, _, ok := simpleColumnMetric(mt); ok {
				emitted[col] = true
			}
		}
		simple := map[string]bool{}
		for _, mt := range t.Metrics {
			if _, _, ok := simpleColumnMetric(mt); ok && !(emitted[mt.Name] && keys[mt.Name]) {
				simple[mt.Name] = true
			}
		}

		mm := &ldModelMeta{}
		if len(t.PrimaryKey) == 1 {
			mm.PrimaryKey = t.PrimaryKey[0]
		} else if len(t.PrimaryKey) > 1 {
			notes = append(notes, fmt.Sprintf(
				"table %s: composite primary key %v not emitted (Lightdash primary_key is a single column)",
				t.Name, t.PrimaryKey))
		}
		for _, r := range m.Relationships {
			if r.Left != t.Name {
				continue
			}
			mm.Joins = append(mm.Joins, ldJoin{
				Join: r.Right, SQLOn: joinSQLOn(r), Relationship: "many-to-one",
			})
		}
		for _, mt := range t.Metrics {
			col, met, isSimple := simpleColumnMetric(mt)
			// A metric named after a key column cannot be emitted at all: the
			// dimension has to stay, and Lightdash would drop the metric anyway.
			// Noted rather than left to the deploy log.
			if (isSimple || derivedRepresentable(mt, simple)) && emitted[mt.Name] && keys[mt.Name] {
				notes = append(notes, keyCollisionNote(mt.Name, t.Name))
				continue
			}
			if isSimple {
				if hoist.synthesisedName(mt.Name) {
					met.Description = mt.Description // synthesizedNote
					mintedEmitted = append(mintedEmitted, mintedComponent{mt.Name, t.Name})
				}
				cols.metric(col, mt.Name, met)
				continue
			}
			if met, ok := derivedModelMetric(mt, simple); ok {
				if mm.Metrics == nil {
					mm.Metrics = map[string]ldMetric{}
				}
				// Every ref this metric resolves through is now carried by an
				// emitted metric, so a minted component among them is not an
				// orphan.
				for _, r := range metricRefs(mt.Def) {
					referenced[r] = true
				}
				mm.Metrics[mt.Name] = met
				continue
			}
			notes = append(notes, degradeNote(mt))
		}

		// Raw measures no metric covers. Without this the emitter drops them
		// silently: the source's 22 measures became 9 metrics, so clicks,
		// impressions, order_lines_count and shipments_count vanished along with
		// their backing columns, leaving 34 of 38 explores with no metric at
		// all. databricks_metric_view.go already does this pass; not doing it
		// here made the same IR produce a materially thinner layer for one
		// target than another, which is the opposite of the point of an IR.
		//
		// Dedup follows the databricks precedent exactly: raw-vs-metric by name
		// OR by rendered aggregate (so orders_count is suppressed beside an
		// orders metric with the same agg+column), raw-vs-raw by name only (so
		// revenue and net_revenue both survive even if both are sum(amount)).
		usedExprs := map[string]bool{}
		for _, c := range cols.list() {
			if c.Meta == nil {
				continue
			}
			for _, met := range c.Meta.Metrics {
				usedExprs[strings.ToLower(met.Type+"("+c.Name+")")] = true
			}
		}
		for _, ms := range t.Measures {
			typ, ok := ldAggType(ms.Agg)
			if !ok {
				notes = append(notes, "measure "+ms.Name+" on "+t.Name+
					" not emitted: aggregation "+ms.Agg+" has no Lightdash metric type")
				continue
			}
			// A column metric hangs off a real column, so the measure's expr
			// must be a bare identifier. A compound expr (e.g.
			// "case when is_refunded then 1 else 0 end") would otherwise be
			// emitted as a column NAME, and Lightdash would look for a column
			// literally called that. Same isIdent guard the joined-dimension
			// path uses.
			col := ms.Expr
			if !isIdent(col) {
				notes = append(notes, "measure "+ms.Name+" on "+t.Name+
					" not emitted: its expression is not a plain column ("+col+"), "+
					"and a Lightdash column metric must hang off one")
				continue
			}
			key := strings.ToLower(typ + "(" + col + ")")
			if emitted[ms.Name] || usedExprs[key] {
				continue
			}
			// A measure named after a key column would be dropped by Lightdash
			// in favour of that dimension, exactly as a metric would.
			if keys[ms.Name] {
				notes = append(notes, keyCollisionNote(ms.Name, t.Name))
				continue
			}
			cols.metric(col, ms.Name, ldMetric{Type: typ})
			emitted[ms.Name] = true
			usedExprs[key] = true
		}

		// Synthesise a row count when the table would otherwise expose no metric
		// at all. Lightdash does NOT require one — ad-hoc custom metrics work
		// against a dimension-only explore. But its AI agent, given nothing
		// selectable, invents a metric id; Lightdash then drops the unresolvable
		// field and compiles the rest, yielding `SELECT\n\nFROM t` which strict
		// warehouses reject outright (ClickHouse Code 62). A real countable
		// metric removes the incentive. databricks_metric_view.go synthesises
		// the same thing, for a different reason: that format requires one.
		if !cols.hasMetric() && len(mm.Metrics) == 0 && len(cols.order) > 0 {
			countCol := cols.order[0]
			if len(t.PrimaryKey) == 1 {
				countCol = t.PrimaryKey[0]
			}
			name := t.Name + "_count"
			if !emitted[name] && !keys[name] {
				cols.metric(countCol, name, ldMetric{Type: "count"})
				emitted[name] = true
				notes = append(notes, "table "+t.Name+
					": no metric was derivable from the source, so a row-count metric "+
					name+" was synthesised over "+countCol)
			}
		}

		// Every emitted metric is placed by now, so the dimension/metric
		// namespaces can be reconciled against each other in one pass: a
		// remaining collision is over a droppable (non-key) column.
		notes = append(notes, cols.resolveNameCollisions(mm, t.Name)...)

		model := ldModel{
			Name:        t.Name,
			Description: appendClause(t.Description, synonymClause(t.Synonyms)),
			Columns:     cols.list(),
		}
		if !mm.empty() {
			model.Meta = mm
		}
		if configMeta {
			if model.Meta != nil {
				model.Config = &ldModelCfg{Meta: model.Meta}
				model.Meta = nil
			}
			for i := range model.Columns {
				if model.Columns[i].Meta != nil {
					model.Columns[i].Config = &ldColCfg{Meta: model.Columns[i].Meta}
					model.Columns[i].Meta = nil
				}
			}
		}
		f.Models = append(f.Models, model)
	}

	// Minted components left without a referencing metric (see mintedEmitted).
	for _, mc := range mintedEmitted {
		if referenced[mc.name] {
			continue
		}
		notes = append(notes, "metric "+mc.name+" on "+mc.table+
			" is a component semglot minted for a derived metric Lightdash could not emit; "+
			"it is emitted on its own so the aggregate is not lost, but no metric references it")
	}

	var buf bytes.Buffer
	if len(notes) > 0 {
		buf.WriteString("# semglot: some source constructs could not be transpiled to Lightdash:\n")
		for _, n := range notes {
			buf.WriteString("# - " + n + "\n")
		}
	}
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return notes, err
	}
	if err := enc.Close(); err != nil {
		return notes, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return notes, err
	}
	return notes, os.WriteFile(filepath.Join(dir, "schema.yml"), buf.Bytes(), 0o644)
}
