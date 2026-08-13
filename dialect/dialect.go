// Package dialect defines the dialect plugin surface (Parser/Emitter) and a
// registry mapping dialect names to implementations.
package dialect

import (
	"fmt"
	"sort"
	"strings"

	"github.com/benchouse/semglot/ir"
)

// Dialect is any registered semantic-layer dialect.
type Dialect interface {
	Name() string
}

// Parser reads a dialect's files from dir into the neutral IR.
type Parser interface {
	Dialect
	// Parse reads *.yml from each source directory (non-recursive) and merges
	// them into one IR model. Multiple sources let a dbt project's schema files
	// spread across folders (e.g. models/semantic + models/marts) be combined.
	Parse(sources ...string) (*ir.Model, error)
}

// Emitter writes the neutral IR out as a dialect's files under dir. warnings
// are non-fatal: source constructs the target could not represent and had to
// degrade or drop. They are returned rather than appended to ir.Model.Notes so
// Emit stays read-only over the model, and returned rather than accumulated on
// the emitter so it stays stateless, which matters because Register stores one
// shared instance per dialect.
type Emitter interface {
	Dialect
	Emit(m *ir.Model, dir string) (warnings []string, err error)
}

// Options carries the model/view identity a Configurable emitter needs.
type Options struct {
	Database    string // warehouse database (e.g. EVAL_MARTS)
	Schema      string // schema of the SOURCE tables the model reads (e.g. MAIN)
	ViewSchema  string // schema where the emitted view OBJECT is created (Snowflake semantic view); falls back to Schema when empty
	Name        string
	Description string
	// DbtMetaKeyPath selects where Lightdash meta lives: "" / "meta" nests under
	// meta: (dbt <=1.9); "config.meta" nests under config.meta: (dbt 1.10+).
	DbtMetaKeyPath string
	// TablePrefix is prepended to a table's IR name to form its PHYSICAL name.
	// The IR carries logical names (fct_orders); a warehouse may materialise
	// them under a prefix (ClickHouse marts__fct_orders). Consumers that bind a
	// semantic model to a physical table need the latter.
	TablePrefix string
	// DbtHexMeta emits Hex's `config.meta.hex.table` binding on every semantic
	// model. Hex's Semantic Model Sync parses dbt MetricFlow YAML straight from
	// a repo and CANNOT resolve which physical table a semantic model reads
	// without it, so without this the sync imports models that resolve to
	// nothing.
	DbtHexMeta bool
}

// Configurable is an Emitter that accepts model/view identity options.
type Configurable interface {
	WithOptions(Options) Emitter
}

var registry = map[string]Dialect{}

// Register adds a dialect to the global registry. Intended for use from init().
func Register(l Dialect) { registry[l.Name()] = l }

// Get returns a registered dialect by name.
func Get(name string) (Dialect, bool) {
	l, ok := registry[name]
	return l, ok
}

// AsParser returns the named layer as a Parser, or an error if it is unknown or
// cannot parse (a valid state for emit-only dialects).
func AsParser(name string) (Parser, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q", name)
	}
	p, ok := l.(Parser)
	if !ok {
		return nil, fmt.Errorf("dialect %q cannot be a source (no parser)", name)
	}
	return p, nil
}

// AsEmitter returns the named layer as an Emitter, or an error if it is unknown
// or cannot emit.
func AsEmitter(name string) (Emitter, error) {
	l, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown dialect %q", name)
	}
	e, ok := l.(Emitter)
	if !ok {
		return nil, fmt.Errorf("dialect %q cannot be a target (no emitter)", name)
	}
	return e, nil
}

// Names returns the registered dialect names, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// relRoleSuffix returns the disambiguating suffix for r's relationship/join
// name: "" when r is the only relationship between its (Left, Right) table
// pair in all — so today's plain name is unchanged — otherwise r's left
// column names joined with "_" (e.g. "customer_sk", or "region_start" for a
// multi-column FK). Two or more relationships between the same table pair is
// a role-playing dimension (e.g. ship-to vs bill-to customer, order_date vs
// ship_date to a shared date dimension): each FK's own left columns make a
// deterministic, source-order-independent disambiguator, unlike numbering by
// encounter order. Every emitter that names relationships/joins (cortex,
// snowflake-semantic-view, databricks-metric-view) calls this so all three
// disambiguate identically; each applies its own casing/separator on top.
func relRoleSuffix(all []ir.Relationship, r ir.Relationship) string {
	n := 0
	for _, o := range all {
		if o.Left == r.Left && o.Right == r.Right {
			n++
		}
	}
	if n <= 1 {
		return ""
	}
	cols := make([]string, len(r.Columns))
	for i, cp := range r.Columns {
		cols[i] = cp.Left
	}
	return strings.Join(cols, "_")
}

// relationshipNames resolves the name each relationship in all must carry in
// target's artifact, returning names[i] for all[i] plus warn[i] — the reason
// all[i]'s DECLARED name could not be used, or "" when nothing was lost. The
// caller decides where each warning goes (a returned emitter warning, an
// artifact comment, or both), which is why they are returned per relationship
// rather than appended through a callback.
//
// This is the "prefer when set, fall back and warn" contract ir.Table.Source
// already uses, applied to ir.Relationship.Name: a name the source dialect
// declared is the author's own identifier and beats anything semglot can
// generate, but it must not be preferred when preferring it would be WRONG.
// fallback builds the generated name (each target's own casing/shape, on top of
// relRoleSuffix), and is what a declared name loses to in three cases:
//
//   - it is empty — a dbt `relationships` test is anonymous, so this is the
//     normal, unwarned path, and today's generated name is unchanged;
//   - it collides — with another relationship's declared name, or with another
//     relationship's generated name. relRoleSuffix exists precisely to keep two
//     FKs between the same table pair (a role-playing dimension) apart, and
//     every one of these targets requires relationship/join names to be unique,
//     so a declared name that collides must not silently win and take the other
//     relationship down with it;
//   - it is invalid for the target — valid reports whether the target can hold
//     the name at all (typically a bare SQL identifier, since cortex, Snowflake
//     DDL and a Databricks join alias each need one). A nil valid accepts any
//     non-empty name, which is right for ossie: the OSI schema types `name` as
//     a plain string.
//
// Collisions are compared case-insensitively — the superset of what the targets
// need, since Snowflake upper-cases its names and Databricks lower-cases its
// join aliases, and two names differing only in case would collide there even
// though they differ here. A declared name equal to its OWN generated name is
// not a collision, and is kept SILENTLY: see the short-circuit below.
//
// emits reports whether a relationship reaches target's artifact at all; a nil
// emits means every one does. Only emitted relationships take part in the
// collision counts, and a skipped one is never warned about: a relationship the
// target drops (ossie, snowflake-semantic-view and databricks-metric-view all
// skip a column-less one — see relHasColumns) occupies no name, so counting it
// would push a perfectly usable declared name onto the fallback path and warn
// about a clash with something that was never written.
func relationshipNames(all []ir.Relationship, target string, emits func(ir.Relationship) bool, fallback func(ir.Relationship) string, valid func(string) bool) (names, warn []string) {
	names = make([]string, len(all))
	warn = make([]string, len(all))
	generated := make([]string, len(all))
	emitted := make([]bool, len(all))
	declaredCount := map[string]int{}
	generatedCount := map[string]int{}
	for i, r := range all {
		generated[i] = fallback(r)
		emitted[i] = emits == nil || emits(r)
		if !emitted[i] {
			continue
		}
		generatedCount[strings.ToLower(generated[i])]++
		if n := strings.TrimSpace(r.Name); n != "" {
			declaredCount[strings.ToLower(n)]++
		}
	}
	for i, r := range all {
		names[i] = generated[i]
		if !emitted[i] {
			// Not written, so nothing is at stake: keep the declared name in
			// names[i] for any caller that reads it anyway, and stay silent.
			if n := strings.TrimSpace(r.Name); n != "" {
				names[i] = n
			}
			continue
		}
		n := strings.TrimSpace(r.Name)
		if n == "" {
			continue
		}
		key := strings.ToLower(n)
		// A declared name the target would have GENERATED anyway is kept, and
		// kept silently. Every branch below falls back to generated[i], so
		// here there is nothing to fall back TO: the warning would read
		// "emitted as %q instead" naming the very name it claims to reject.
		// This is the commonest star shape — two facts joining one dimension,
		// one of them declaring the dimension's own name — so the false
		// warning fires on conforming input, which is how readers learn to
		// ignore warnings. Any duplication that survives here is a property of
		// the FALLBACK generator: it would appear identically had the author
		// declared nothing at all, which is silent today. (The check also
		// subsumes the count adjustment it replaces — an own generated name
		// can no longer be counted against its own declaration below.)
		if strings.EqualFold(generated[i], n) {
			names[i] = n
			continue
		}
		switch {
		case declaredCount[key] > 1:
			warn[i] = fmt.Sprintf(
				"%s: its declared name is used by more than one relationship, which %s requires to be unique; emitted as %q instead",
				relLabel(n, r), target, generated[i])
		case generatedCount[key] > 0:
			warn[i] = fmt.Sprintf(
				"%s: its declared name collides with another relationship's generated name, which %s requires to be unique; emitted as %q instead",
				relLabel(n, r), target, generated[i])
		case valid != nil && !valid(n):
			warn[i] = fmt.Sprintf(
				"%s: its declared name is not a valid %s relationship name; emitted as %q instead",
				relLabel(n, r), target, generated[i])
		default:
			names[i] = n
		}
	}
	return names, warn
}

// relIdentityClause renders a relationship's declared identity — the name its
// author gave the join, and the synonyms an agent matches a question against —
// as a prose clause, or "" when it declares neither. For the two targets that
// carry text rather than a structural slot (nao-yaml's notes:, nao-context-
// rules' join list), which lose nothing by having none. Deliberately shaped
// like sourceClause and synonymClause, whose prose folds it sits beside.
func relIdentityClause(r ir.Relationship) string {
	var parts []string
	if n := strings.TrimSpace(r.Name); n != "" {
		parts = append(parts, "Join name: "+n+".")
	}
	if c := synonymClause(r.Synonyms); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, " ")
}

// relLabel names a relationship for a warning: by the name it is known by when
// there is one, and by its endpoints alone when there is not — an anonymous
// relationship (every dbt one) has no other identity, and a bare `relationship
// ""` names nothing a reader can act on. Distinct from the parse-side
// `relationship %q` subjects in ossie.go, whose wording is pinned by
// test/ossie_conformance_test.go's wantNotes.
func relLabel(name string, r ir.Relationship) string {
	if strings.TrimSpace(name) == "" {
		return fmt.Sprintf("relationship %s -> %s", r.Left, r.Right)
	}
	return fmt.Sprintf("relationship %q (%s -> %s)", name, r.Left, r.Right)
}

// relNameWarning reports that r's DECLARED name has nowhere to go in target,
// or "" when r declares none. Used by the targets that carry a join but not an
// identifier for it — a dbt `relationships` data test, a Lightdash join, a
// supersimple relation and nao-yaml all describe the join by its endpoints
// alone — so the author's own name for it is lost rather than renamed. The
// name is the relationship's identity in the source document (OSI calls it
// "unique identifier for the relationship"), so losing it silently is exactly
// what this branch's ruling forbids.
func relNameWarning(target string, r ir.Relationship) string {
	if strings.TrimSpace(r.Name) == "" {
		return ""
	}
	return fmt.Sprintf("%s: declared name not emitted; %s has no name slot on a join",
		relLabel(r.Name, r), target)
}

// relSynonymsWarning reports that r's join synonyms have nowhere to go in
// target, or "" when r has none. Cortex relationships, a Snowflake semantic
// view's relationships clause and a Databricks join each carry a name and the
// column pairing and nothing else — no synonym list, and no per-relationship
// comment to fold one into the way a table's synonyms fold into its comment.
// So unlike ir.Table.Synonyms, which every prose-capable target absorbs, these
// are genuinely lost and have to say so.
//
// name is the name the relationship is emitted under, which is how a reader
// finds the join the warning is about.
func relSynonymsWarning(target, name string, r ir.Relationship) string {
	if len(r.Synonyms) == 0 {
		return ""
	}
	return fmt.Sprintf("%s: synonyms %v not emitted; %s has no synonym slot on a relationship",
		relLabel(name, r), r.Synonyms, target)
}

// relNotEmittedWarning reports that r reaches target's artifact NOT AT ALL —
// no join, and with it neither the join's declared name nor its synonyms.
//
// This is a different loss from the one relNameWarning and relSynonymsWarning
// describe. Those say "the join is there, its name is not"; this says the join
// itself is missing, so they must not be used in its place — they would tell a
// reader the join was written when it was not.
//
// It exists because the targets that write joins per TABLE (databricks-metric-
// view roots one view at each table and carries the joins leaving it; dbt hangs
// a `relationships` test on a model's FK column; lightdash writes meta.joins on
// the left model; supersimple writes a relation on the PARENT model) iterate
// relationships from inside a table loop. A relationship whose relevant
// endpoint names no ir.Table in the model is therefore never iterated by
// anything, and vanishes without passing any of those emitters' warning sites.
// reason states which endpoint was missing, in the target's own vocabulary.
func relNotEmittedWarning(target string, r ir.Relationship, reason string) string {
	w := fmt.Sprintf("%s: not emitted to %s at all: %s", relLabel(r.Name, r), target, reason)
	if len(r.Synonyms) > 0 {
		w += fmt.Sprintf("; its synonyms %v are lost with it", r.Synonyms)
	}
	return w
}

// relEndpointMissing reports the reason string for relNotEmittedWarning when
// endpoint (one of r's two table names) is not an ir.Table in the model. role
// names what the target would have needed that table for.
func relEndpointMissing(endpoint, role string) string {
	return fmt.Sprintf("the model declares no table %q, and %s", endpoint, role)
}

// relHasColumns reports whether r has at least one column pair, i.e. whether
// it describes a join at all. It is the emits predicate (see
// relationshipNames) of every target that skips a column-less relationship
// rather than writing a join with no ON condition.
func relHasColumns(r ir.Relationship) bool { return len(r.Columns) > 0 }

// splitSource splits an ir.Table.Source into its three dot-separated parts
// (database, schema, table). ok is false unless source has EXACTLY three
// non-empty parts. This is cortex's OWN requirement, not a general one:
// cortexBaseTable has separate Database/Schema/Table YAML fields, so only a
// clean three-part reference can be reassembled into them (see the note on
// the shared dialect_test.go doc, and dialect/README.md). Targets that hold
// their physical reference as a single dotted string (snowflake-semantic-
// view, databricks-metric-view, supersimple) do NOT need this — they can
// take Source verbatim regardless of its part count; see looksLikeQuery for
// the check that actually applies to them.
//
// Callers must not half-apply a failed split (e.g. treating the last segment
// as the table name and inventing the rest) — that produces a plausible,
// wrong, unwarned address. Fall back to profile reconstruction instead, and
// warn via unsplittableSourceWarning.
//
// A query is rejected outright, before any splitting: dot-counting alone does
// NOT tell a reference from a query, because the canonical OSI query form
// `SELECT ... FROM db.schema.tbl` has exactly the two dots a three-part
// address has. Splitting that yields database "SELECT * FROM db" and table
// "tbl WHERE ..." — a plausible, wrong, unwarned address, which is precisely
// what the paragraph above forbids. The check lives here rather than only at
// the call site so no future caller can reintroduce it.
func splitSource(source string) (database, schema, table string, ok bool) {
	if looksLikeQuery(source) {
		return "", "", "", false
	}
	parts := strings.Split(source, ".")
	if len(parts) != 3 {
		return "", "", "", false
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", false
		}
	}
	return parts[0], parts[1], parts[2], true
}

// unsplittableSourceWarning reports that table's declared Source could not be
// split into target's separate Database/Schema/Table fields, so the emitter
// fell back to reconstructing a physical reference from the profile instead
// of silently half-applying it. Used only by cortex — the one target whose
// physical-reference shape genuinely requires three separate fields; see
// querySourceWarning for the equivalent used by the string-valued targets.
func unsplittableSourceWarning(target, table, source string) string {
	return fmt.Sprintf(
		"table %q: declared source %q could not be split into %s's separate database/schema/table fields; reconstructed from the profile instead",
		table, source, target)
}

// looksLikeQuery reports whether source reads as a SQL query rather than a
// plain table reference. The OSI spec permits `source` to be "either
// database_name.schema_name.table_name or query" — a target whose physical-
// reference field is a single opaque string (snowflake-semantic-view,
// databricks-metric-view, supersimple) can take a genuine table reference
// verbatim no matter how many dot-separated parts it has (a two-part
// schema.table resolves against the session database / current catalog just
// fine), but must not paste a query into that slot as if it were an address:
// a snowflake TABLES clause and a supersimple `table:` field both expect a
// reference, not a subquery. Detected by whitespace, parentheses, or a
// leading SELECT — a plain dotted identifier list contains none of those.
func looksLikeQuery(source string) bool {
	s := strings.TrimSpace(source)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r(") {
		return true
	}
	return strings.HasPrefix(strings.ToUpper(s), "SELECT")
}

// querySourceWarning reports that table's declared Source is a query rather
// than a table reference (which the OSI spec explicitly permits, but which
// target's physical-reference field cannot hold as-is), so the emitter fell
// back to reconstructing an address from the profile instead of pasting the
// query in where a table reference belongs.
func querySourceWarning(target, table, source string) string {
	return fmt.Sprintf(
		"table %q: declared source %q is a query, not a table reference; %s needs an address here, so it was reconstructed from the profile instead",
		table, source, target)
}

// dbtSchemaSourceWarning reports that table's declared Source has no home at
// all in a dbt schema file. Used by the two targets that emit one — `dbt`
// itself and `lightdash`, whose artifact IS a dbt schema.yml.
//
// A dbt `models:` entry documents a model dbt already builds; the physical
// relation it resolves to comes from dbt's own graph (`ref()` plus the
// model's materialization config), never from the properties file. Lightdash
// reads that same compiled relation out of dbt's manifest. The one adjacent
// slot, `config: {database, schema, alias}`, does not DESCRIBE an address —
// it RELOCATES the table dbt builds, so writing an upstream physical address
// there would silently rewrite the project rather than record a fact about
// it. Neither target can carry the address, so both report it lost instead of
// half-expressing it.
func dbtSchemaSourceWarning(target, table, source string) string {
	return fmt.Sprintf(
		"table %q: declared source %q not emitted: a dbt schema file has no slot for a table's physical address (dbt resolves it through ref()), so %s cannot carry it",
		table, source, target)
}

// sourceClause renders a table's declared physical address as a prose clause,
// for the two targets that carry it as text rather than in a structural slot
// (nao-yaml's notes:, nao-context-rules' table reference). Deliberately
// shaped like synonymClause, whose fold into the same prose slots it mirrors.
func sourceClause(source string) string {
	if strings.TrimSpace(source) == "" {
		return ""
	}
	return "Physical source: " + source + "."
}
