package dialect

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
	"gopkg.in/yaml.v3"
)

func init() { Register(ossie{}) }

// osiVersion is the spec version emitted and expected. Pinned, never computed.
const osiVersion = "0.2.0.dev0"

// ossie reads and writes the Apache Ossie Core Metadata Specification
// (github.com/apache/ossie, core-spec 0.2.0.dev0) — the semantic_model layer,
// not the separate ontology layer. The spec is a draft: "schema may change
// before 0.2.0 is released".
//
// ossie reads and emits Apache Ossie semantic models. Zero value is usable;
// the build command sets identity from the profile. Emit does not mutate m.
type ossie struct{ Database, Schema, ModelName, Description string }

func (ossie) Name() string { return "ossie" }

// ---- OSI YAML shapes ----

type osiFile struct {
	Version       string     `yaml:"version"`
	SemanticModel []osiModel `yaml:"semantic_model"`
}

type osiModel struct {
	Name             string               `yaml:"name"`
	Description      string               `yaml:"description,omitempty"`
	AIContext        *osiAIContext        `yaml:"ai_context,omitempty"`
	Datasets         []osiDataset         `yaml:"datasets"`
	Relationships    []osiRelationship    `yaml:"relationships,omitempty"`
	Metrics          []osiMetric          `yaml:"metrics,omitempty"`
	CustomExtensions []osiCustomExtension `yaml:"custom_extensions,omitempty"`
}

type osiDataset struct {
	Name             string               `yaml:"name"`
	Source           string               `yaml:"source"`
	PrimaryKey       []string             `yaml:"primary_key,omitempty"`
	UniqueKeys       [][]string           `yaml:"unique_keys,omitempty"`
	Description      string               `yaml:"description,omitempty"`
	AIContext        *osiAIContext        `yaml:"ai_context,omitempty"`
	Fields           []osiField           `yaml:"fields,omitempty"`
	CustomExtensions []osiCustomExtension `yaml:"custom_extensions,omitempty"`
}

type osiField struct {
	Name             string               `yaml:"name"`
	Expression       osiExpression        `yaml:"expression"`
	Dimension        *osiDimension        `yaml:"dimension,omitempty"`
	Label            string               `yaml:"label,omitempty"`
	Description      string               `yaml:"description,omitempty"`
	DataType         string               `yaml:"datatype,omitempty"`
	AIContext        *osiAIContext        `yaml:"ai_context,omitempty"`
	CustomExtensions []osiCustomExtension `yaml:"custom_extensions,omitempty"`
}

type osiDimension struct {
	IsTime *bool `yaml:"is_time,omitempty"`
}

type osiRelationship struct {
	Name             string               `yaml:"name"`
	From             string               `yaml:"from"`
	To               string               `yaml:"to"`
	FromColumns      []string             `yaml:"from_columns"`
	ToColumns        []string             `yaml:"to_columns"`
	CustomExtensions []osiCustomExtension `yaml:"custom_extensions,omitempty"`
}

type osiMetric struct {
	Name             string               `yaml:"name"`
	Expression       osiExpression        `yaml:"expression"`
	Description      string               `yaml:"description,omitempty"`
	DataType         string               `yaml:"datatype,omitempty"`
	AIContext        *osiAIContext        `yaml:"ai_context,omitempty"`
	CustomExtensions []osiCustomExtension `yaml:"custom_extensions,omitempty"`
}

// osiCustomExtension is one vendor-private extension block. The spec allows
// them on the model, a dataset, a field, a relationship and a metric, and real
// vendor payloads exist in Apache Ossie's own fixtures (DATABRICKS formatting
// hints, a SALESFORCE Tableau/CRM block).
//
// The design's "Out of scope" section decided on prose-only degradation: their
// contents are vendor-private, semglot neither interprets them nor writes a
// `vendor_name: SEMGLOT` block of its own, so only vendor_name is decoded and
// nothing is ever emitted (every emit path leaves the slice nil, and omitempty
// keeps the key out of the document). Out of scope for round-tripping is not
// the same as invisible, though: parse notes each block's owner and vendor,
// per this branch's no-silent-drop ruling.
type osiCustomExtension struct {
	VendorName string `yaml:"vendor_name,omitempty"`
}

type osiExpression struct {
	Dialects []osiDialectExpr `yaml:"dialects"`
}

type osiDialectExpr struct {
	Dialect    string `yaml:"dialect"`
	Expression string `yaml:"expression"`
}

// osiAIContext is `string | object` in the schema. A bare string is treated as
// instructions.
type osiAIContext struct {
	Instructions string   `yaml:"instructions,omitempty"`
	Synonyms     []string `yaml:"synonyms,omitempty"`
	Examples     []string `yaml:"examples,omitempty"`
}

func (a *osiAIContext) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		a.Instructions = s
		return nil
	}
	type plain osiAIContext // avoid recursing into this method
	var p plain
	if err := value.Decode(&p); err != nil {
		return err
	}
	*a = osiAIContext(p)
	return nil
}

func (a *osiAIContext) synonyms() []string {
	if a == nil {
		return nil
	}
	return a.Synonyms
}

// examplesNote returns a note reporting a's Examples, or "" when there are
// none. The IR has no examples slot at any level (model, dataset, field, or
// metric), so this is the only way that data survives parsing. subject names
// what the ai_context belongs to, e.g. `dataset "orders"`. Every level that
// can carry an ai_context must call it — a level left out is a silent drop.
func (a *osiAIContext) examplesNote(subject string) string {
	if a == nil || len(a.Examples) == 0 {
		return ""
	}
	return fmt.Sprintf("%s ai_context.examples %v: no examples slot in the IR; dropped", subject, a.Examples)
}

// instructionsNote returns a note reporting a's Instructions, or "" when there
// are none. Only the MODEL level has an IR home for instructions
// (ir.Model.Notes, which mergeModel appends the text to directly); a dataset's,
// field's, or metric's instructions have no slot at all, so they surface as a
// note naming the level and the owner rather than vanishing. Both the object
// form and the bare-string form osiAIContext.UnmarshalYAML accepts reach here,
// since both land in the same Instructions field.
func (a *osiAIContext) instructionsNote(subject string) string {
	if a == nil || a.Instructions == "" {
		return ""
	}
	return fmt.Sprintf("%s ai_context.instructions %q: no instructions slot in the IR below the model level; dropped",
		subject, a.Instructions)
}

// customExtensionsNote returns a note naming subject's vendor extension blocks,
// or "" when there are none. See osiCustomExtension for why the payload itself
// is deliberately not carried across.
func customExtensionsNote(subject string, ext []osiCustomExtension) string {
	if len(ext) == 0 {
		return ""
	}
	vendors := make([]string, 0, len(ext))
	for _, e := range ext {
		if e.VendorName == "" {
			vendors = append(vendors, "(unnamed)")
			continue
		}
		vendors = append(vendors, e.VendorName)
	}
	return fmt.Sprintf("%s custom_extensions %v: vendor extensions are not transpiled; dropped", subject, vendors)
}

// addNote appends note to *notes unless it is empty, so a caller can hand it
// the result of a *Note helper without repeating the emptiness check.
func addNote(notes *[]string, note string) {
	if note != "" {
		*notes = append(*notes, note)
	}
}

// ---- data types ----

// osiTypes maps OSI's logical DataType enum to the neutral SQL type string the
// IR carries (dialects like dbt record raw SQL types, and emitters such as
// cortex pass them straight through). Integer and Decimal map to distinct SQL
// names rather than collapsing to `number`, so the round-trip is stable.
var osiTypes = map[string]string{
	"String":     "varchar",
	"Integer":    "integer",
	"Decimal":    "decimal",
	"Float":      "float",
	"Boolean":    "boolean",
	"Date":       "date",
	"Time":       "time",
	"DateTime":   "timestamp",
	"DateTimeTz": "timestamp_tz",
}

// irTypes is the reverse map, extended with the SQL spellings other dialects
// emit for the same logical type.
var irTypes = map[string]string{
	"varchar": "String", "char": "String", "text": "String", "string": "String",
	"integer": "Integer", "int": "Integer", "bigint": "Integer", "smallint": "Integer",
	// "number" is Snowflake/dbt's default numeric spelling (an exact synonym of
	// "numeric") and maps to Decimal rather than Integer. That slightly
	// overshoots for surrogate-key columns typed "number" with no declared
	// scale, but an imprecise logical type beats emitting none at all.
	"decimal": "Decimal", "numeric": "Decimal", "number": "Decimal",
	"float": "Float", "double": "Float", "real": "Float",
	"boolean": "Boolean", "bool": "Boolean",
	"date":      "Date",
	"time":      "Time",
	"timestamp": "DateTime", "datetime": "DateTime", "timestamp_ntz": "DateTime",
	"timestamp_tz": "DateTimeTz", "timestamptz": "DateTimeTz", "timestamp_ltz": "DateTimeTz",
}

// osiToIRType converts an OSI datatype to a SQL type string, or "" when the
// value is unknown or absent (including the deliberate Opaque case).
func osiToIRType(t string) string { return osiTypes[t] }

// irToOSIType converts a SQL type string to an OSI DataType, or "" when it is
// not in the portable vocabulary. The spec directs omitting datatype when
// unknown, so Opaque is never used as a catch-all.
func irToOSIType(t string) string {
	return irTypes[strings.ToLower(strings.TrimSpace(t))]
}

// osiTemporal reports whether an OSI datatype makes is_time default to true.
func osiTemporal(t string) bool {
	switch t {
	case "Date", "Time", "DateTime", "DateTimeTz":
		return true
	}
	return false
}

// isTime resolves a field's temporal role per spec: an explicit dimension.is_time
// always wins; when unset it defaults to true for a temporal datatype.
func (f osiField) isTime() bool {
	if f.Dimension != nil && f.Dimension.IsTime != nil {
		return *f.Dimension.IsTime
	}
	return osiTemporal(f.DataType)
}

// pickExpression selects the expression to read. ANSI_SQL wins when present.
// Otherwise a lone entry is used silently — Ossie's own Databricks fixtures are
// DATABRICKS-only, and noting every field there would be pure noise — while a
// choice among several non-ANSI dialects uses the first and returns a note.
func pickExpression(e osiExpression) (expr string, ok bool, note string) {
	if len(e.Dialects) == 0 {
		return "", false, ""
	}
	for _, d := range e.Dialects {
		if strings.EqualFold(d.Dialect, "ANSI_SQL") {
			return d.Expression, true, ""
		}
	}
	first := e.Dialects[0]
	if len(e.Dialects) == 1 {
		return first.Expression, true, ""
	}
	return first.Expression, true, fmt.Sprintf("no ANSI_SQL expression; read the %s dialect", first.Dialect)
}

// ---- Parse ----

// Parse reads *.yml and *.yaml from each source directory (non-recursive) and
// merges every semantic_model entry into one IR model. Files with no
// semantic_model key are skipped, so a mixed directory works.
func (o ossie) Parse(sources ...string) (*ir.Model, error) {
	out := &ir.Model{}
	for _, dir := range sources {
		var paths []string
		for _, pat := range []string{"*.yml", "*.yaml"} {
			matches, err := filepath.Glob(filepath.Join(dir, pat))
			if err != nil {
				return nil, err
			}
			paths = append(paths, matches...)
		}
		sort.Strings(paths) // deterministic merge order
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			var f osiFile
			if err := yaml.Unmarshal(b, &f); err != nil {
				return nil, fmt.Errorf("%s: %w", p, err)
			}
			if len(f.SemanticModel) == 0 {
				continue // not an OSI document
			}
			for _, sm := range f.SemanticModel {
				mergeModel(out, sm)
			}
		}
	}
	return out, nil
}

// mergeModel folds one OSI semantic_model entry into out.
//
// A dataset whose name is already in out is MERGED into the existing ir.Table
// rather than appended as a second same-named one — see mergeTable. Multiple
// semantic_model entries and multiple files are documented to "merge into the
// single ir.Model", and dbt.Parse merges its models: / semantic_models: by name
// the same way (TestDBTParseMerge). Appending a duplicate would also make
// tableIndex home every metric on the first of the pair and make Emit write two
// `datasets:` entries under one name, which OSI's "unique identifier for the
// dataset" rule forbids.
func mergeModel(out *ir.Model, sm osiModel) {
	modelSubject := fmt.Sprintf("model %q", sm.Name)
	if sm.AIContext != nil && sm.AIContext.Instructions != "" {
		out.Notes = append(out.Notes, sm.AIContext.Instructions)
	}
	// Model-level ai_context.synonyms has no home in the IR (unlike dataset-
	// and field-level synonyms, which map onto ir.Table.Synonyms and
	// ir.Field.Synonyms): note it rather than drop it silently.
	if syn := sm.AIContext.synonyms(); len(syn) > 0 {
		out.Notes = append(out.Notes,
			fmt.Sprintf("model %q ai_context.synonyms %v: no model-level synonym slot in the IR; dropped", sm.Name, syn))
	}
	addNote(&out.Notes, sm.AIContext.examplesNote(modelSubject))
	addNote(&out.Notes, customExtensionsNote(modelSubject, sm.CustomExtensions))

	for _, ds := range sm.Datasets {
		dsSubject := fmt.Sprintf("dataset %q", ds.Name)
		t := ir.Table{
			Name:        ds.Name,
			Description: ds.Description,
			Synonyms:    ds.AIContext.synonyms(),
			PrimaryKey:  ds.PrimaryKey,
		}
		// unique_keys has no IR slot; note it so it isn't dropped silently.
		if len(ds.UniqueKeys) > 0 {
			out.Notes = append(out.Notes,
				fmt.Sprintf("dataset %q unique_keys %v: no unique-key slot in the IR; dropped", ds.Name, ds.UniqueKeys))
		}
		addNote(&out.Notes, ds.AIContext.instructionsNote(dsSubject))
		addNote(&out.Notes, ds.AIContext.examplesNote(dsSubject))
		addNote(&out.Notes, customExtensionsNote(dsSubject, ds.CustomExtensions))
		for _, f := range ds.Fields {
			fSubject := fmt.Sprintf("field %q on dataset %q", f.Name, ds.Name)
			addNote(&out.Notes, f.AIContext.instructionsNote(fSubject))
			addNote(&out.Notes, f.AIContext.examplesNote(fSubject))
			addNote(&out.Notes, customExtensionsNote(fSubject, f.CustomExtensions))
			expr, ok, note := pickExpression(f.Expression)
			if !ok {
				out.Notes = append(out.Notes,
					fmt.Sprintf("field %q on dataset %q has no expression; skipped", f.Name, ds.Name))
				continue
			}
			if note != "" {
				out.Notes = append(out.Notes, fmt.Sprintf("%s: %s", fSubject, note))
			}
			// OSI's field-level label has no IR field-level slot, so it folds
			// into the description as visible prose rather than vanishing.
			desc := f.Description
			if f.Label != "" {
				desc = appendClause(desc, "Display name: "+f.Label+".")
			}
			fld := ir.Field{
				Name:        f.Name,
				Description: desc,
				DataType:    osiToIRType(f.DataType),
				Expr:        expr,
				Synonyms:    f.AIContext.synonyms(),
			}
			if f.isTime() {
				t.TimeDimensions = append(t.TimeDimensions, fld)
			} else {
				t.Dimensions = append(t.Dimensions, fld)
			}
		}
		if i := tableIndex(out, t.Name); i >= 0 {
			mergeTable(&out.Tables[i], t, &out.Notes)
			continue
		}
		out.Tables = append(out.Tables, t)
	}

	for _, r := range sm.Relationships {
		addNote(&out.Notes, customExtensionsNote(fmt.Sprintf("relationship %q", r.Name), r.CustomExtensions))
		if len(r.FromColumns) != len(r.ToColumns) {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"relationship %q: from_columns (%d) and to_columns (%d) differ in length; skipped",
				r.Name, len(r.FromColumns), len(r.ToColumns)))
			continue
		}
		if len(r.FromColumns) == 0 {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"relationship %q: from_columns/to_columns is empty; skipped", r.Name))
			continue
		}
		rel := ir.Relationship{Left: r.From, Right: r.To}
		for i := range r.FromColumns {
			rel.Columns = append(rel.Columns, ir.ColumnPair{Left: r.FromColumns[i], Right: r.ToColumns[i]})
		}
		out.Relationships = append(out.Relationships, rel)
	}

	owner := colOwner(sm)
	tables := datasetNames(sm)
	fact, _ := factTable(sm)
	colsByTable := map[string][]string{}
	for _, ds := range sm.Datasets {
		cols := make([]string, 0, len(ds.Fields))
		for _, f := range ds.Fields {
			cols = append(cols, strings.ToLower(f.Name))
		}
		sort.Strings(cols) // deterministic Raw.Columns
		colsByTable[ds.Name] = cols
	}
	for _, mt := range sm.Metrics {
		mtSubject := fmt.Sprintf("metric %q", mt.Name)
		// Reported before the skip checks below, so a metric that never reaches
		// the IR still surfaces what its ai_context carried.
		addNote(&out.Notes, mt.AIContext.instructionsNote(mtSubject))
		addNote(&out.Notes, mt.AIContext.examplesNote(mtSubject))
		addNote(&out.Notes, customExtensionsNote(mtSubject, mt.CustomExtensions))
		expr, ok, note := pickExpression(mt.Expression)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf("metric %q has no expression; skipped", mt.Name))
			continue
		}
		if note != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("metric %q: %s", mt.Name, note))
		}
		def, ok := parseAggExpr(expr)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q not transpiled: expression %q could not be parsed as an aggregate expression", mt.Name, expr))
			continue
		}
		home, ok, viaFact := metricHomeOrFact(def, owner, tables, fact)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q not transpiled: could not determine the dataset its expression belongs to", mt.Name))
			continue
		}
		if viaFact {
			// This is an inference, not something the file stated: say so, per
			// this branch's no-silent-drop ruling extended to no-silent-WRONG —
			// an inferred attribution presented as unqualified fact would be
			// exactly that.
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q attributed to dataset %q because it is the model's fact table (no incoming relationship); inferred, not declared in the file",
				mt.Name, home))
		}
		idx := tableIndex(out, home)
		if idx < 0 {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q references unknown dataset %q; skipped", mt.Name, home))
			continue
		}
		if metricIndex(out.Tables[idx].Metrics, mt.Name) >= 0 {
			// Two semantic_model entries (or two files) publishing the same
			// metric name onto the same dataset. Keeping both would put two
			// same-named entries in OSI's one flat, model-level metrics list on
			// the way back out, which the spec's unique-name rule forbids.
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q on dataset %q is declared more than once; the later declaration was dropped", mt.Name, home))
			continue
		}
		// factHome is passed only when the metric's OWN top-level home decision
		// already required the fallback (viaFact) — in that case metricHome's
		// whole-tree descent has already proven every node fails NORMAL
		// resolution too (a Binary/Agg's descent tries every branch before
		// giving up), so defaulting an unresolvable node to factHome silently
		// completes an inference already disclosed above, rather than making a
		// NEW, undisclosed one. When viaFact is false, some other branch
		// resolved normally and a DIFFERENT node failing independently (e.g. a
		// column no dataset declares in a cross-table expression) is real,
		// undisclosed ambiguity — factHome stays "" and resolveAggTables keeps
		// its original behaviour of leaving it unqualified with a note.
		factHome := ""
		if viaFact {
			factHome = home
		}
		def = resolveAggTables(def, mt.Name, owner, tables, colsByTable, factHome, &out.Notes)
		// A plain aggregation over one column is also a column-backed measure.
		if agg, col, isSimple := simpleAgg(def); isSimple {
			out.Tables[idx].Measures = append(out.Tables[idx].Measures, ir.Measure{
				Field: ir.Field{
					Name:        mt.Name,
					Description: mt.Description,
					DataType:    osiToIRType(mt.DataType),
					Expr:        col,
					Synonyms:    mt.AIContext.synonyms(),
				},
				Agg: agg,
			})
		} else if mt.DataType != "" {
			// Only a column-backed measure has a DataType slot to carry the
			// metric's datatype (the branch above). ir.Metric has none at all,
			// so a ratio's or derived metric's datatype would otherwise vanish.
			out.Notes = append(out.Notes, fmt.Sprintf(
				"metric %q datatype %q: ir.Metric has no datatype slot (only a column-backed measure carries one); dropped",
				mt.Name, mt.DataType))
		}
		out.Tables[idx].Metrics = append(out.Tables[idx].Metrics, ir.Metric{
			Name:        mt.Name,
			Description: mt.Description,
			Synonyms:    mt.AIContext.synonyms(),
			Def:         def,
		})
	}
}

// mergeTable folds src into dst, both being the same OSI dataset declared
// twice (two semantic_model entries, or two files in one source directory).
//
// Fields are unioned by name — OSI documents a field's name as "unique
// identifier for the field within the logical dataset", so two declarations of
// one name describe one column. An identical redeclaration is absorbed
// silently because nothing is lost; a conflicting one keeps the FIRST
// declaration and notes the conflict, the same first-wins-plus-warn rule
// ossie_emit.go's addField applies to the emit direction. Scalars (description,
// primary key) fill in when the first declaration left them empty and are
// otherwise kept with a note on disagreement, so a merge never invents a
// composite key or a spliced description out of two conflicting sources.
func mergeTable(dst *ir.Table, src ir.Table, notes *[]string) {
	switch {
	case dst.Description == "":
		dst.Description = src.Description
	case src.Description != "" && src.Description != dst.Description:
		*notes = append(*notes, fmt.Sprintf(
			"dataset %q is declared more than once with different descriptions; kept %q and dropped %q",
			dst.Name, dst.Description, src.Description))
	}
	for _, s := range src.Synonyms {
		if !contains(dst.Synonyms, s) {
			dst.Synonyms = append(dst.Synonyms, s)
		}
	}
	switch {
	case len(dst.PrimaryKey) == 0:
		dst.PrimaryKey = src.PrimaryKey
	case len(src.PrimaryKey) > 0 && !reflect.DeepEqual(dst.PrimaryKey, src.PrimaryKey):
		*notes = append(*notes, fmt.Sprintf(
			"dataset %q is declared more than once with different primary keys; kept %v and dropped %v",
			dst.Name, dst.PrimaryKey, src.PrimaryKey))
	}
	mergeFields(dst, src.Dimensions, false, notes)
	mergeFields(dst, src.TimeDimensions, true, notes)
}

// mergeFields adds each of src's fields to dst's dimension or time-dimension
// list, skipping a name dst already declares. A skipped field that differs from
// the one already there (a different expression, or a different temporal role)
// is real information loss and is noted; an identical one is not.
func mergeFields(dst *ir.Table, src []ir.Field, isTime bool, notes *[]string) {
	for _, f := range src {
		if prev, prevTime, found := findField(dst, f.Name); found {
			if prevTime != isTime || !reflect.DeepEqual(prev, f) {
				*notes = append(*notes, fmt.Sprintf(
					"field %q on dataset %q is declared more than once with different definitions; kept the first (expression %q) and dropped the second (expression %q)",
					f.Name, dst.Name, prev.Expr, f.Expr))
			}
			continue
		}
		if isTime {
			dst.TimeDimensions = append(dst.TimeDimensions, f)
		} else {
			dst.Dimensions = append(dst.Dimensions, f)
		}
	}
}

// findField looks name up across both of t's field lists, reporting whether it
// was found and whether it is a time dimension.
func findField(t *ir.Table, name string) (f ir.Field, isTime, found bool) {
	for _, d := range t.Dimensions {
		if d.Name == name {
			return d, false, true
		}
	}
	for _, d := range t.TimeDimensions {
		if d.Name == name {
			return d, true, true
		}
	}
	return ir.Field{}, false, false
}

// metricIndex returns the position of the named metric in ms, or -1.
func metricIndex(ms []ir.Metric, name string) int {
	for i := range ms {
		if ms[i].Name == name {
			return i
		}
	}
	return -1
}

// colOwner indexes which dataset declares a given column, so an unqualified
// aggregate (Ossie's Databricks fixtures write `SUM(o_totalprice)`) can still
// be homed. A column declared by more than one dataset maps to "" — ambiguous,
// and the metric is noted rather than guessed at.
func colOwner(sm osiModel) map[string]string {
	owner := map[string]string{}
	for _, ds := range sm.Datasets {
		for _, f := range ds.Fields {
			key := strings.ToLower(f.Name)
			if prev, seen := owner[key]; seen && prev != ds.Name {
				owner[key] = ""
				continue
			}
			owner[key] = ds.Name
		}
	}
	return owner
}

// datasetNames indexes sm's dataset names by lowercased name, so a Raw SQL
// fragment's `table.column` qualifier can be recognised as naming a dataset
// directly (see metricHome's ir.Raw case) rather than only ever falling back
// to colOwner's column-name lookup, which is ambiguous whenever two datasets
// declare a same-named column (e.g. an is_refunded copied onto a wide/OBT
// table by a join) even though the SQL text already disambiguates it.
func datasetNames(sm osiModel) map[string]string {
	names := map[string]string{}
	for _, ds := range sm.Datasets {
		names[strings.ToLower(ds.Name)] = ds.Name
	}
	return names
}

// factTable identifies sm's fact dataset per Apache Ossie's own documented
// convention (see fixtureA_ossie.yaml's own comment, and their Databricks
// converter, which uses the same rule to choose a metric view's source
// table): the dataset with no incoming relationship, i.e. one that never
// appears as a relationship's `to`. OSI's fields: is a curated list of
// group/filter-able attributes, not a complete column index, so a metric's
// aggregate argument routinely names a physical column no dataset declares —
// exactly the case metricHomeOrFact's fallback exists for.
//
// Exactly one such dataset is unambiguous. Two or more (including the
// degenerate case of a relationship cycle where every dataset has an
// incoming edge, leaving zero candidates) is genuinely ambiguous, and so is a
// model with no relationships at all and more than one dataset — every
// dataset trivially has "no incoming relationship" then, which is not the
// same as any one of them being identifiably the fact. A model with no
// relationships and exactly ONE dataset is the unambiguous edge case: that
// lone dataset is trivially the fact table.
func factTable(sm osiModel) (string, bool) {
	incoming := map[string]bool{}
	for _, r := range sm.Relationships {
		incoming[r.To] = true
	}
	var candidates []string
	for _, ds := range sm.Datasets {
		if !incoming[ds.Name] {
			candidates = append(candidates, ds.Name)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return "", false
}

// metricHomeOrFact resolves e via metricHome first — a qualified reference or
// a column resolvable through colOwner always wins — and only when that
// fails outright falls back to fact, the model's fact table (empty when
// factTable found none or an ambiguous set). viaFallback reports whether the
// fallback fired, so the caller can note the attribution as inferred rather
// than presenting it as read from the file. Requirement: the fallback must
// never override information the file actually stated, so it is consulted
// only after metricHome itself has exhausted every qualified/colOwner path.
func metricHomeOrFact(e ir.Expr, owner map[string]string, tables map[string]string, fact string) (table string, ok bool, viaFallback bool) {
	if t, ok := metricHome(e, owner, tables); ok {
		return t, true, false
	}
	if fact != "" {
		return fact, true, true
	}
	return "", false, false
}

// metricHome returns the table a parsed metric belongs to: the first qualified
// column's table, else the sole dataset declaring the first unqualified column.
// Mirrors dbt.go's rule that a cross-table metric homes on its first resolvable
// operand's table.
func metricHome(e ir.Expr, owner map[string]string, tables map[string]string) (string, bool) {
	switch n := e.(type) {
	case ir.Col:
		if n.Table != "" {
			return n.Table, true
		}
		if t := owner[strings.ToLower(n.Name)]; t != "" {
			return t, true
		}
		return "", false
	case ir.Agg:
		if n.Arg == nil {
			return "", false // COUNT(*) names no column
		}
		return metricHome(n.Arg, owner, tables)
	case ir.Raw:
		toks := tokenize(n.SQL)
		for i, tk := range toks {
			if tk.typ != sqlIdent {
				continue
			}
			// A `table.column` qualifier names its dataset directly and wins
			// over colOwner, whose column-name lookup is ambiguous whenever
			// more than one dataset declares a same-named column — the
			// qualifier already disambiguates it, so it must not be
			// discarded down to a bare identifier scan.
			if i+2 < len(toks) && toks[i+1].typ == sqlOther && toks[i+1].val == "." && toks[i+2].typ == sqlIdent {
				if name, ok := tables[strings.ToLower(tk.val)]; ok {
					return name, true
				}
			}
			if t := owner[strings.ToLower(tk.val)]; t != "" {
				return t, true
			}
		}
		return "", false
	case ir.Binary:
		if t, ok := metricHome(n.Left, owner, tables); ok {
			return t, true
		}
		return metricHome(n.Right, owner, tables)
	}
	return "", false
}

// resolveAggTables homes each Agg node in e independently, rather than
// stamping one metric-level table onto every node. A cross-table expression
// like `SUM(orders.amount) / COUNT(CASE WHEN customers.active THEN 1 END)`
// has two Agg nodes belonging to two different tables; stamping both with the
// metric's overall home table (as an earlier version of this function did)
// silently mis-qualifies whichever operand isn't on the home table — e.g. a
// CASE fragment referencing another table's column rendered with the wrong
// table's Columns list, with the operand ending up entirely unqualified in
// the emitted SQL and no trace of the mistake.
//
// For each Agg node, it re-runs metricHome against that node's own Arg. When
// it resolves: Agg.Table is stamped, a Raw arg's Columns is filled from that
// table's column list, and a bare (unqualified) Col arg has its own Table
// filled in too — matching the always-qualified Col invariant the rest of the
// codebase relies on (dbt.go always sets Table on the Col args it
// constructs; see databricks_metric_view.go's qualifier stripping, which
// assumes every Col/Raw arg it renders is qualified). When it does not
// resolve (the column is declared by no dataset, or by more than one and so
// is ambiguous per colOwner), the node's Table and its Col arg's Table are
// left empty and a note is appended to *notes naming metricName and the
// unattributable sub-expression, rather than falling back to any table
// silently.
//
// A Col reached OUTSIDE an aggregate — an OSI metric whose whole expression is
// `amount`, or the bare operand in `amount / order_count` — is qualified by the
// same rule. It has to be: OSI's metrics list is MODEL-scoped, with none of the
// implicit dataset scope a dataset's own `fields:` entries have, so a bare
// column emitted there names nothing in particular. Skipping this case (as an
// earlier version did, descending only Agg and Binary) broke the very
// always-qualified invariant this comment claims, and did it silently.
//
// factHome is the fact-table fallback default, or "" when none applies. It is
// deliberately NOT re-derived per node from factTable here — the caller
// (mergeModel) passes the metric's own already-decided home ONLY when that
// decision itself required the fallback (metricHomeOrFact's viaFallback).
// That is safe rather than a new guess: metricHome's whole-tree descent tries
// every branch before giving up, so if it failed for the WHOLE expression,
// every node's own metricHome call below is guaranteed to fail too, and
// defaulting each to factHome completes the SAME already-disclosed inference
// rather than making an undisclosed new one. When the top-level decision
// succeeded normally (factHome == ""), a DIFFERENT node failing independently
// (e.g. a column no dataset declares, deep in a cross-table expression) is
// real, undisclosed ambiguity, so it keeps the original left-unqualified-and-
// noted behaviour untouched — see TestOssieMetricUnattributableSubExpressionNoted.
func resolveAggTables(e ir.Expr, metricName string, owner map[string]string, tables map[string]string, colsByTable map[string][]string, factHome string, notes *[]string) ir.Expr {
	switch n := e.(type) {
	case ir.Col:
		if n.Table != "" {
			return n
		}
		table, ok := metricHome(n, owner, tables)
		if !ok {
			if factHome != "" {
				n.Table = factHome
				return n
			}
			*notes = append(*notes, fmt.Sprintf(
				"metric %q: column %q could not be attributed to a dataset (unknown or ambiguous column); left unqualified",
				metricName, n.Name))
			return n
		}
		n.Table = table
		return n
	case ir.Agg:
		if n.Arg == nil {
			n.Table = factHome // "" leaves it exactly as before: unstamped, unqualified
			return n           // COUNT(*) has no column to qualify
		}
		table, ok := metricHome(n.Arg, owner, tables)
		if !ok {
			if factHome == "" {
				*notes = append(*notes, fmt.Sprintf(
					"metric %q: sub-expression %q could not be attributed to a dataset (unknown or ambiguous column); left unqualified",
					metricName, aggArgText(n.Arg)))
				return n
			}
			table = factHome
		}
		n.Table = table
		switch a := n.Arg.(type) {
		case ir.Raw:
			a.Columns = colsByTable[table]
			n.Arg = a
		case ir.Col:
			if a.Table == "" {
				a.Table = table
				n.Arg = a
			}
		}
		return n
	case ir.Binary:
		n.Left = resolveAggTables(n.Left, metricName, owner, tables, colsByTable, factHome, notes)
		n.Right = resolveAggTables(n.Right, metricName, owner, tables, colsByTable, factHome, notes)
		return n
	}
	return e
}

// aggArgText renders an Agg argument as readable text for a note when it
// cannot be homed.
func aggArgText(e ir.Expr) string {
	switch a := e.(type) {
	case ir.Col:
		if a.Table != "" {
			return a.Table + "." + a.Name
		}
		return a.Name
	case ir.Raw:
		return a.SQL
	}
	return ""
}

// simpleAgg reports the measure a plain aggregation over one column implies:
// its aggregate and the column expression. ok=false for anything compound,
// filtered, or COUNT(*), none of which is a column-backed measure.
func simpleAgg(e ir.Expr) (agg, col string, ok bool) {
	a, isAgg := e.(ir.Agg)
	if !isAgg || a.Filter != nil {
		return "", "", false
	}
	c, isCol := a.Arg.(ir.Col)
	if !isCol {
		return "", "", false
	}
	return a.Func, c.Name, true
}

// tableIndex returns the position of the named table in m, or -1.
func tableIndex(m *ir.Model, name string) int {
	for i := range m.Tables {
		if m.Tables[i].Name == name {
			return i
		}
	}
	return -1
}
