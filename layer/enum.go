package layer

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
