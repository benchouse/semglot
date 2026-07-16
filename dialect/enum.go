package dialect

import (
	"strings"

	"github.com/benchouse/semglot/ir"
)

// enumClause renders a field's enum as one sentence for targets that have no
// structured per-value enum field. Documented values render as "a = x; b = y";
// bare values render as "a, b, c". Returns "" for an empty enum.
func enumClause(enum []ir.EnumValue) string {
	if len(enum) == 0 {
		return ""
	}
	hasDesc := false
	for _, e := range enum {
		if e.Description != "" {
			hasDesc = true
			break
		}
	}
	parts := make([]string, len(enum))
	for i, e := range enum {
		if hasDesc && e.Description != "" {
			parts[i] = e.Value + " = " + e.Description
		} else {
			parts[i] = e.Value
		}
	}
	sep := ", "
	if hasDesc {
		sep = "; "
	}
	return "Values: " + strings.Join(parts, sep) + "."
}

// enumValues splits a field's enum for a target that HAS a structured values
// field (e.g. Cortex sample_values, nao-yaml values): it returns the bare value
// list, plus the description with per-value meanings folded in as a
// "Values: v = meaning; …" clause, added only when a value carries a meaning
// (the bare values already live in the structured field). Returns (desc, nil)
// for an empty enum.
func enumValues(desc string, enum []ir.EnumValue) (string, []string) {
	if len(enum) == 0 {
		return desc, nil
	}
	vals := make([]string, len(enum))
	hasDesc := false
	for i, e := range enum {
		vals[i] = e.Value
		if e.Description != "" {
			hasDesc = true
		}
	}
	if hasDesc {
		desc = appendClause(desc, enumClause(enum))
	}
	return desc, vals
}

// synonymClause renders a field's synonyms as one sentence for targets that
// have no structured synonyms field (e.g. nao-yaml, which folds them into the
// dimension description). Returns "" for an empty list.
func synonymClause(syn []string) string {
	if len(syn) == 0 {
		return ""
	}
	return "Synonyms: " + strings.Join(syn, ", ") + "."
}

// appendClause joins a description and a trailing clause with a single space,
// tolerating either being empty.
func appendClause(desc, clause string) string {
	switch {
	case clause == "":
		return desc
	case desc == "":
		return clause
	default:
		return desc + " " + clause
	}
}
