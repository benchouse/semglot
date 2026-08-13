package dialect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// osiSchemaPath is the vendored copy of Apache Ossie's own JSON Schema, at the
// commit test/models/ossie/vendor/README.md pins. It is the spec's machine-
// readable property list — the thing this file checks the osi* Go structs
// against.
const osiSchemaPath = "../test/models/ossie/vendor/osi-schema.json"

// jsonSchemaNode is the sliver of JSON Schema this check reads: a type's own
// properties, plus the oneOf branches AIContext uses to be `string | object`.
// Everything else in the schema (types, enums, required, descriptions) is
// deliberately ignored — the question here is only which property NAMES a type
// declares, and whether the Go struct that decodes it has a yaml tag for each.
type jsonSchemaNode struct {
	Properties map[string]json.RawMessage `json:"properties"`
	OneOf      []jsonSchemaNode           `json:"oneOf"`
}

// propertyNames returns every property name n declares, including those on any
// oneOf branch (unioned, since a Go struct has to decode whichever branch a
// document actually uses).
func (n jsonSchemaNode) propertyNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(props map[string]json.RawMessage) {
		for p := range props {
			if seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	add(n.Properties)
	for _, b := range n.OneOf {
		for _, p := range b.propertyNames() {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

type osiSchemaFile struct {
	jsonSchemaNode
	Defs map[string]jsonSchemaNode `json:"$defs"`
}

// osiSchemaStructs maps each schema type to the Go struct that decodes it. The
// key "" is the schema's own top level (the document root), which has no $defs
// entry of its own.
//
// The mapping is written out by hand rather than derived, and osiSchemaScalars
// below lists the remaining $defs explicitly, so that the two together must
// name EVERY $def in the schema — TestOssieSchemaMappingIsComplete fails on a
// $def in neither list. A new type in a future spec version is then a
// deliberate decision (decode it, or record why it needs no struct), never a
// silent skip.
var osiSchemaStructs = map[string]any{
	"":                  osiFile{},
	"SemanticModel":     osiModel{},
	"Dataset":           osiDataset{},
	"Field":             osiField{},
	"Dimension":         osiDimension{},
	"Relationship":      osiRelationship{},
	"Metric":            osiMetric{},
	"Expression":        osiExpression{},
	"DialectExpression": osiDialectExpr{},
	"AIContext":         osiAIContext{},
	"CustomExtension":   osiCustomExtension{},
}

// osiSchemaScalars are the $defs that are scalar types, not objects: they
// declare no properties and are decoded into a plain Go string field on
// whichever struct references them (e.g. osiField.DataType for DataType), so
// they have no struct of their own. The value records why, so this is a
// documented exemption rather than an unexplained omission.
var osiSchemaScalars = map[string]string{
	"Dialect":  "string enum; decoded as osiDialectExpr.Dialect",
	"Vendor":   "free string; decoded as osiCustomExtension.VendorName",
	"DataType": "string enum; decoded as osiField.DataType / osiMetric.DataType",
}

func loadOSISchema(t *testing.T) osiSchemaFile {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(osiSchemaPath))
	if err != nil {
		t.Fatalf("read vendored OSI schema: %v", err)
	}
	var s osiSchemaFile
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("parse vendored OSI schema: %v", err)
	}
	if len(s.Defs) == 0 {
		t.Fatal("vendored OSI schema has no $defs; it is not the schema this check expects")
	}
	return s
}

// yamlTags returns the yaml key each of v's fields decodes, keyed by name, so
// a missing property can be reported as "no Go field decodes it" rather than
// as a bare boolean.
func yamlTags(v any) map[string]string {
	out := map[string]string{}
	rt := reflect.TypeOf(v)
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		key, _, _ := strings.Cut(tag, ",")
		if key == "" {
			key = strings.ToLower(f.Name) // yaml.v3's default for an untagged field
		}
		out[key] = f.Name
	}
	return out
}

// TestOssieSchemaMappingIsComplete pins the schema -> struct mapping itself:
// every $def must be either mapped to a Go struct or listed as a scalar, and
// nothing may be mapped that the schema does not declare. Without this, adding
// a $def upstream would simply not be checked by the test below, which is the
// failure mode that let osiRelationship go without an ai_context field.
func TestOssieSchemaMappingIsComplete(t *testing.T) {
	s := loadOSISchema(t)
	for name := range s.Defs {
		_, mapped := osiSchemaStructs[name]
		_, scalar := osiSchemaScalars[name]
		switch {
		case mapped && scalar:
			t.Errorf("$def %q is listed both as a Go struct and as a scalar; pick one", name)
		case !mapped && !scalar:
			t.Errorf("$def %q is new: map it in osiSchemaStructs, or record in osiSchemaScalars why it needs no struct", name)
		}
	}
	for name := range osiSchemaStructs {
		if name == "" {
			continue // the document root, which has no $defs entry
		}
		if _, ok := s.Defs[name]; !ok {
			t.Errorf("osiSchemaStructs maps %q, which the schema no longer declares", name)
		}
	}
	for name := range osiSchemaScalars {
		if _, ok := s.Defs[name]; !ok {
			t.Errorf("osiSchemaScalars lists %q, which the schema no longer declares", name)
		}
	}
}

// TestOssieStructsCoverSchema walks every schema type and asserts the Go struct
// that decodes it has a yaml tag for each property the spec declares.
//
// This exists because two properties were lost by inspection alone rather than
// by a check: Relationship.ai_context had no field on osiRelationship at all
// (so yaml.v3 discarded the key before any code could even note it dropped),
// and CustomExtension.data was never decoded. Neither was visible in a test —
// an undeclared key is silence, not a failure — until the spec's own property
// list was enumerated against the structs, which is what this does.
//
// The reverse direction is checked too: a yaml tag with no schema property
// means semglot emits a key the spec's `additionalProperties: false` rejects.
func TestOssieStructsCoverSchema(t *testing.T) {
	s := loadOSISchema(t)
	names := make([]string, 0, len(osiSchemaStructs))
	for name := range osiSchemaStructs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		node := s.jsonSchemaNode
		if name != "" {
			node = s.Defs[name]
		}
		typeName := name
		if typeName == "" {
			typeName = "(document root)"
		}
		t.Run(typeName, func(t *testing.T) {
			goType := osiSchemaStructs[name]
			tags := yamlTags(goType)
			props := node.propertyNames()
			if len(props) == 0 {
				t.Fatalf("schema type %s declares no properties; the mapping is looking at the wrong node", typeName)
			}
			for _, p := range props {
				if _, ok := tags[p]; !ok {
					t.Errorf("%s: schema property %q has no yaml tag on %T; add a field for it (or the key is silently discarded on parse)",
						typeName, p, goType)
				}
			}
			for tag := range tags {
				if !contains(props, tag) {
					t.Errorf("%s: %T has a yaml tag %q the schema does not declare; the spec sets additionalProperties: false, so emitting it produces an invalid document",
						typeName, goType, tag)
				}
			}
			t.Logf("%s -> %T: %d properties checked (%s)", typeName, goType, len(props), strings.Join(props, ", "))
		})
	}
	if len(names) != 11 {
		t.Errorf("checked %d schema types, expected 11 (10 object $defs plus the document root); update this count deliberately", len(names))
	}
}
