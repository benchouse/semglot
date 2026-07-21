package dialect

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

func (naoContextRules) Emit(m *ir.Model, dir string) ([]string, error) {
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
	// Columns: per-column descriptions + synonyms. nao's auto-synced schema
	// carries only column name+type (the warehouse has no column comments), so
	// these semantics reach the agent ONLY through the context layer — this
	// keeps the rules arm at parity with the semantic-model/cortex layers, which
	// both emit column descriptions + synonyms. Columns with neither add nothing
	// beyond the synced name+type and are omitted.
	var cols []string
	for _, t := range m.Tables {
		for _, d := range append(append([]ir.Field{}, t.Dimensions...), t.TimeDimensions...) {
			body := appendClause(strings.TrimSpace(d.Description), synonymClause(d.Synonyms))
			if body == "" {
				continue
			}
			cols = append(cols, fmt.Sprintf("- `%s.%s`: %s", t.Name, d.Name, body))
		}
	}
	if len(cols) > 0 {
		b.WriteString("\n## Columns\n\n")
		for _, c := range cols {
			b.WriteString(c)
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
	// Table reference: each table's grain + purpose (its description). NOT
	// "traps" — this is a glossary, and deliberately carries only what the dbt
	// model documents; the withheld data-quirk discriminators never appear here.
	var tables []string
	for _, t := range m.Tables {
		if t.Description != "" {
			tables = append(tables, fmt.Sprintf("- **%s**: %s", t.Name, t.Description))
		}
	}
	tables = append(tables, notesToBullets(m.Notes)...)
	if len(tables) > 0 {
		b.WriteString("\n## Table reference\n\n")
		for _, tr := range tables {
			b.WriteString(tr)
			b.WriteByte('\n')
		}
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return nil, os.WriteFile(filepath.Join(dir, "RULES.md"), b.Bytes(), 0o644)
}

func notesToBullets(notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		out = append(out, "- "+n)
	}
	return out
}
