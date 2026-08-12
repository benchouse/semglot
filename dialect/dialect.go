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
func splitSource(source string) (database, schema, table string, ok bool) {
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
