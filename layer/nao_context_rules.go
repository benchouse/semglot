package layer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/benchouse/semglot/ir"
)

func init() { Register(naoContextRules{}) }

type naoContextRules struct{}

func (naoContextRules) Name() string { return "nao-context-rules" }

func (naoContextRules) Emit(m *ir.Model, dir string) error {
	resolve := metricResolver(m)

	var b bytes.Buffer
	b.WriteString("# Rules\n\n## Key metrics reference\n\n")
	for _, t := range m.Tables {
		for _, mt := range t.Metrics {
			name := mt.Label
			if name == "" {
				name = mt.Name
			}
			fmt.Fprintf(&b, "- **%s**: `%s`.", name, renderSQL(mt.Def, resolve))
			if mt.Description != "" {
				fmt.Fprintf(&b, " %s", mt.Description)
			}
			b.WriteByte('\n')
		}
	}
	// Allowed values: categorical columns that declare an enum.
	var enums []string
	for _, t := range m.Tables {
		for _, d := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
			if c := enumClause(d.Enum); c != "" {
				enums = append(enums, fmt.Sprintf("- `%s.%s`: %s", t.Name, d.Name, strings.TrimPrefix(c, "Values: ")))
			}
		}
	}
	if len(enums) > 0 {
		b.WriteString("\n## Allowed values\n\n")
		for _, e := range enums {
			b.WriteString(e)
			b.WriteByte('\n')
		}
	}
	if len(m.Relationships) > 0 {
		b.WriteString("\n## Joins & routing\n\n")
		for _, r := range m.Relationships {
			for _, c := range r.Columns {
				fmt.Fprintf(&b, "- `%s.%s → %s.%s`\n", r.Left, c.Left, r.Right, c.Right)
			}
		}
	}
	// Table traps: best-effort, only what the model documents.
	var traps []string
	for _, t := range m.Tables {
		if t.Description != "" {
			traps = append(traps, fmt.Sprintf("- **%s**: %s", t.Name, t.Description))
		}
	}
	traps = append(traps, notesToBullets(m.Notes)...)
	if len(traps) > 0 {
		b.WriteString("\n## Table traps\n\n")
		for _, tr := range traps {
			b.WriteString(tr)
			b.WriteByte('\n')
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "RULES.md"), b.Bytes(), 0o644)
}

func notesToBullets(notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, "- "+n)
	}
	return out
}
