package integration_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// This file checks the emitted okf bundle against the format's own rules rather
// than against our expectations, so it catches breakage the goldens cannot: a
// golden happily pins a malformed bundle.
//
// Two sources of truth, and they disagree:
//
//   - okf/SPEC.md requires only a non-empty `type`.
//   - okf/src/reference_agent/bundle/document.py requires type, title,
//     description AND timestamp to be non-empty (REQUIRED_FRONTMATTER_KEYS).
//
// The reference implementation is what actually reads bundles, so we assert the
// stricter set. test/okf_contract_test.py runs the real thing over the same
// bundle; this test is its dependency-free stand-in so CI always has a check.

// okfRequiredFrontmatter mirrors document.py's REQUIRED_FRONTMATTER_KEYS.
var okfRequiredFrontmatter = []string{"type", "title", "description", "timestamp"}

// okfReservedFiles are the spec's reserved names, which carry no frontmatter.
var okfReservedFiles = map[string]bool{"index.md": true, "log.md": true}

// markdownLink matches an inline markdown link target.
var markdownLink = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// TestOKFConformance verifies every emitted concept is a conforming OKF
// document, and every link in the bundle resolves to a file that exists.
func TestOKFConformance(t *testing.T) {
	dir := t.TempDir()
	emitBundleTo(t, "okf", dir)

	var concepts, links int
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".md" {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		body := string(b)

		if okfReservedFiles[d.Name()] {
			if strings.HasPrefix(body, "---") {
				t.Errorf("%s: reserved file must not carry frontmatter", rel)
			}
		} else {
			concepts++
			checkFrontmatter(t, rel, body)
		}

		// Every link must resolve. The spec tolerates broken links (they may
		// mark knowledge not yet written), but ours are all generated, so a
		// broken one is our bug.
		for _, m := range markdownLink.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.Contains(target, "://") {
				continue // external
			}
			links++
			resolved := filepath.Join(filepath.Dir(path), filepath.FromSlash(target))
			if strings.HasPrefix(target, "/") {
				resolved = filepath.Join(dir, filepath.FromSlash(target))
			}
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s: link %q does not resolve", rel, target)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk bundle: %v", err)
	}
	if concepts == 0 || links == 0 {
		t.Fatalf("bundle looks empty: %d concepts, %d links", concepts, links)
	}
}

// checkFrontmatter parses a concept's frontmatter the way document.py does and
// asserts the required keys are present and non-empty.
func checkFrontmatter(t *testing.T, rel, body string) {
	t.Helper()
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		t.Errorf("%s: no frontmatter block", rel)
		return
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		t.Errorf("%s: unterminated frontmatter block", rel)
		return
	}
	var fm map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		t.Errorf("%s: frontmatter is not valid YAML: %v", rel, err)
		return
	}
	for _, k := range okfRequiredFrontmatter {
		v, ok := fm[k]
		if !ok || v == nil || v == "" {
			t.Errorf("%s: frontmatter key %q is missing or empty", rel, k)
		}
	}
}
