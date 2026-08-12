package dialect

import (
	"strings"
	"testing"

	"github.com/benchouse/semglot/ir"
)

// ir.Relationship.Name is the identifier the SOURCE gave a join — OSI calls it
// the "unique identifier for the relationship", and an author who writes
// `store_sales_to_date` means that name, not the `store_sales_to_date_dim` a
// generator mints from the endpoints. Synonyms are the join's own ai_context
// phrasings ("when the sale occurred"), which is what an agent matches a
// natural-language question against when choosing between joins.
//
// The rule this file pins, for EVERY registered emitter rather than the three
// that name relationships structurally: a declared name and declared join
// synonyms must either reach the emitted artifact or be named in a returned
// warning. It is the same contract table_source_test.go pins for
// ir.Table.Source, applied to the other thing only the source document knows.
//
// The second half pins the fallback: a declared name that would be WRONG in
// the target (colliding, or not an identifier where the target needs one) must
// lose to the generated name AND say so — silently emitting a duplicate or an
// unquotable join alias is the "silently wrong result" the branch's ruling
// rules out just as firmly as a silent drop.

// relModel is two tables joined by rels: enough for every emitter to produce a
// well-formed artifact (databricks-metric-view drops a table with no dimension;
// supersimple and lightdash want a metric to publish).
func relModel(rels ...ir.Relationship) *ir.Model {
	table := func(name, col string) ir.Table {
		return ir.Table{
			Name:       name,
			PrimaryKey: []string{col},
			Dimensions: []ir.Field{{Name: col, Expr: col}, {Name: "status", Expr: "status"}},
			Measures:   []ir.Measure{{Field: ir.Field{Name: name + "_amount", Expr: "amount"}, Agg: "sum"}},
			Metrics: []ir.Metric{{
				Name: name + "_revenue",
				Def:  ir.Agg{Func: "sum", Table: name, Arg: ir.Col{Table: name, Name: "amount"}},
			}},
		}
	}
	return &ir.Model{
		Tables:        []ir.Table{table("orders", "customer_id"), table("customers", "id")},
		Relationships: rels,
	}
}

// join is one orders -> customers relationship on customer_id = id.
func join(name string, synonyms ...string) ir.Relationship {
	return ir.Relationship{
		Name: name, Left: "orders", Right: "customers", Synonyms: synonyms,
		Columns: []ir.ColumnPair{{Left: "customer_id", Right: "id"}},
	}
}

// mentions reports whether s contains sub, ignoring case — snowflake-semantic-
// view upper-cases every identifier it writes and databricks-metric-view
// lower-cases its join aliases, so a case-sensitive check would call a carried
// name dropped.
func mentions(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// usesAsIdentifier reports whether an artifact uses name as a relationship's
// IDENTIFIER, as opposed to merely mentioning it — cortex, snowflake-semantic-
// view and databricks-metric-view all fold their degrade notes into the
// artifact itself, so a warning saying "…emitted as X instead" makes the
// rejected name appear in the file without it being used for anything. The two
// forms are `name: <n>` (the YAML targets) and `<n> as …` (Snowflake's DDL).
func usesAsIdentifier(files, name string) bool {
	l, n := strings.ToLower(files), strings.ToLower(name)
	return strings.Contains(l, "name: "+n+"\n") || strings.Contains(l, n+" as ")
}

// mentionsAll reports whether some warning names every one of parts, which is
// what makes a warning actionable rather than a shrug.
func (r emitResult) mentionsAll(parts ...string) bool {
	for _, w := range r.warnings {
		ok := true
		for _, p := range parts {
			if !mentions(w, p) {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// TestEveryEmitterCarriesOrWarnsAboutDeclaredRelationshipName is the sweep. A
// dialect added later is covered automatically: emitAll loops over Names().
func TestEveryEmitterCarriesOrWarnsAboutDeclaredRelationshipName(t *testing.T) {
	const name = "orders_to_customers_by_account"
	for dialect, res := range emitAll(t, relModel(join(name))) {
		if mentions(res.files, name) {
			continue // carried structurally, or as prose
		}
		if !res.mentionsAll(name) {
			t.Errorf("%s: declared relationship name %q neither emitted nor warned about; warnings=%v\n--- files ---\n%s",
				dialect, name, res.warnings, res.files)
		}
	}
}

// TestEveryEmitterCarriesOrWarnsAboutRelationshipSynonyms is the same for the
// join's ai_context.synonyms, which have no home outside ossie and the two
// prose targets.
func TestEveryEmitterCarriesOrWarnsAboutRelationshipSynonyms(t *testing.T) {
	const syn = "who bought the thing"
	for dialect, res := range emitAll(t, relModel(join("orders_to_customers", syn))) {
		if mentions(res.files, syn) {
			continue
		}
		if !res.mentionsAll(syn) {
			t.Errorf("%s: declared join synonym %q neither emitted nor warned about; warnings=%v\n--- files ---\n%s",
				dialect, syn, res.warnings, res.files)
		}
	}
}

// TestDeclaredRelationshipNameWinsOverGenerated pins the preference for the
// three structural targets plus ossie: the declared name is what lands in the
// artifact, and the generated one does not appear at all.
func TestDeclaredRelationshipNameWinsOverGenerated(t *testing.T) {
	const declared = "orders_placed_by"
	res := emitAll(t, relModel(join(declared)))
	for dialect, generated := range map[string]string{
		"ossie":                   "orders_to_customers",
		"cortex":                  "orders_to_customers",
		"snowflake-semantic-view": "ORDERS_CUSTOMERS",
		// databricks-metric-view names a join after the table it pulls in, and
		// uses that name as the relation ALIAS in every `on` condition, so the
		// declared name has to replace it there too.
		"databricks-metric-view": "customers.id",
	} {
		r := res[dialect]
		if !mentions(r.files, declared) {
			t.Errorf("%s: declared relationship name %q not emitted:\n%s", dialect, declared, r.files)
		}
		if mentions(r.files, generated) {
			t.Errorf("%s: still emitted the generated name %q despite a declared one:\n%s", dialect, generated, r.files)
		}
		for _, w := range r.warnings {
			if mentions(w, declared) && mentions(w, "instead") {
				t.Errorf("%s: a usable declared name must not warn; got %q", dialect, w)
			}
		}
	}
}

// TestDuplicateDeclaredRelationshipNameFallsBack: two relationships declaring
// one name would collide, and every one of these targets requires the name to
// be unique. Both fall back to their generated names, both warn, and the two
// generated names still differ — relRoleSuffix's role-playing-dimension
// disambiguation must survive a declared name trying to overwrite it.
func TestDuplicateDeclaredRelationshipNameFallsBack(t *testing.T) {
	const dup = "orders_to_customers_dup"
	shipTo := ir.Relationship{
		Name: dup, Left: "orders", Right: "customers",
		Columns: []ir.ColumnPair{{Left: "ship_to_id", Right: "id"}},
	}
	billTo := ir.Relationship{
		Name: dup, Left: "orders", Right: "customers",
		Columns: []ir.ColumnPair{{Left: "bill_to_id", Right: "id"}},
	}
	res := emitAll(t, relModel(shipTo, billTo))
	for dialect, r := range res {
		if usesAsIdentifier(r.files, dup) {
			t.Errorf("%s: a name declared by two relationships must not be used as an identifier:\n%s", dialect, r.files)
		}
		// A prose target records both declarations as text — nothing there has
		// to be unique, so the name is carried rather than warned about.
		if mentions(r.files, dup) || r.mentionsAll(dup) {
			continue
		}
		t.Errorf("%s: dropped a colliding declared name without a warning; warnings=%v", dialect, r.warnings)
	}
	// The generated names both survive, distinctly, in the structural targets.
	for _, dialect := range []string{"ossie", "cortex", "snowflake-semantic-view"} {
		for _, want := range []string{"ship_to_id", "bill_to_id"} {
			if !mentions(res[dialect].files, want) {
				t.Errorf("%s: role-playing disambiguator %q lost:\n%s", dialect, want, res[dialect].files)
			}
		}
	}
}

// TestDeclaredRelationshipNameCollidingWithGeneratedFallsBack is the subtler
// collision: one relationship's declared name is another's GENERATED name.
// Preferring it would emit that name twice, which is the same invalid document
// the duplicate case produces.
func TestDeclaredRelationshipNameCollidingWithGeneratedFallsBack(t *testing.T) {
	// customers -> orders generates "customers_to_orders" in cortex/ossie; the
	// orders -> customers relationship declares exactly that.
	back := ir.Relationship{
		Left: "customers", Right: "orders",
		Columns: []ir.ColumnPair{{Left: "id", Right: "customer_id"}},
	}
	m := relModel(join("customers_to_orders"), back)
	for _, dialect := range []string{"ossie", "cortex"} {
		res := emitAll(t, m)[dialect]
		if n := strings.Count(strings.ToLower(res.files), "name: customers_to_orders\n"); n != 1 {
			t.Errorf("%s: the name must be used exactly once (by the relationship that GENERATES it), used %d times:\n%s",
				dialect, n, res.files)
		}
		if !res.mentionsAll("customers_to_orders", "collides") {
			t.Errorf("%s: want a warning about the collision; got %v", dialect, res.warnings)
		}
	}
}

// TestInvalidDeclaredRelationshipNameFallsBack: OSI types `name` as a plain
// string, so a name with spaces is legal there and must round-trip — but a
// Cortex relationship name, a Snowflake DDL identifier and a Databricks join
// alias each need a bare identifier, and emitting one with spaces produces an
// artifact the target rejects.
func TestInvalidDeclaredRelationshipNameFallsBack(t *testing.T) {
	const spaced = "orders to customers"
	res := emitAll(t, relModel(join(spaced)))
	if !mentions(res["ossie"].files, spaced) {
		t.Errorf("ossie types name as a plain string; %q must be carried:\n%s", spaced, res["ossie"].files)
	}
	for _, dialect := range []string{"cortex", "snowflake-semantic-view", "databricks-metric-view"} {
		r := res[dialect]
		if mentions(r.files, "name: "+spaced) || mentions(r.files, spaced+" as ") {
			t.Errorf("%s: emitted a non-identifier as a relationship name:\n%s", dialect, r.files)
		}
		if !r.mentionsAll(spaced, "not a valid") {
			t.Errorf("%s: want a warning that the declared name is unusable; got %v", dialect, r.warnings)
		}
	}
}

// TestDatabricksRefusesSourceAsJoinName: `source` is the alias a metric view's
// own base relation already carries — every join condition is written
// `source.<col> = <join>.<col>` — so a join claiming it would silently
// re-point the left-hand side of its own ON clause at the joined table.
func TestDatabricksRefusesSourceAsJoinName(t *testing.T) {
	res := emitAll(t, relModel(join("source")))["databricks-metric-view"]
	if !strings.Contains(res.files, "name: customers") {
		t.Errorf("want the generated join name, got:\n%s", res.files)
	}
	if !res.mentionsAll("source", "not a valid") {
		t.Errorf("want a warning refusing the name; got %v", res.warnings)
	}
	if strings.Contains(res.files, "source.customer_id = source.id") {
		t.Errorf("a join aliased `source` corrupts its own on-condition:\n%s", res.files)
	}
}

// TestUndeclaredRelationshipNameIsUnchangedAndQuiet: an anonymous relationship
// (every dbt one, since a `relationships` test has no name) must keep today's
// generated name and must not warn — a warning per join there would be noise
// about a loss that did not happen, and would land in every dbt-sourced build.
func TestUndeclaredRelationshipNameIsUnchangedAndQuiet(t *testing.T) {
	for dialect, res := range emitAll(t, relModel(join(""))) {
		for _, w := range res.warnings {
			if mentions(w, "relationship") {
				t.Errorf("%s: nothing was declared, so nothing was lost; got %q", dialect, w)
			}
		}
	}
	res := emitAll(t, relModel(join("")))
	for dialect, want := range map[string]string{
		"ossie":                   "name: orders_to_customers",
		"cortex":                  "name: orders_to_customers",
		"snowflake-semantic-view": "ORDERS_CUSTOMERS as ORDERS(CUSTOMER_ID) references CUSTOMERS(ID)",
		"databricks-metric-view":  "name: customers",
	} {
		if !strings.Contains(res[dialect].files, want) {
			t.Errorf("%s: generated name changed; want %q in:\n%s", dialect, want, res[dialect].files)
		}
	}
}
