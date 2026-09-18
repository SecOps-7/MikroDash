package verify

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/aitools"
	"mikrodash/internal/resource"
)

// TestTheParityTableNamesRealTools holds docs/mikromcp-parity.md to the code.
//
// Every MikroDash name in the table's second column must exist: a read tool in
// the generated catalogue, `change_row:<resource>` a resource in the registry,
// `run_action:<key>` a declared action. A table that names a tool since renamed
// or removed would claim coverage the assistant no longer has, and nothing else
// reads it. Every row has a known status, and a row that does NOT cover its
// tool (excluded, gap, pending, partial, context) says why.
func TestTheParityTableNamesRealTools(t *testing.T) {
	body := mustRead(t, filepath.Join(repoRoot(t), "docs", "mikromcp-parity.md"))
	tools := map[string]bool{}
	for _, tl := range aitools.All() {
		tools[tl.Name] = true
	}
	statuses := map[string]bool{"covered": true, "partial": true, "context": true, "pending": true, "excluded": true, "gap": true}
	row := regexp.MustCompile("^\\| `([a-z_]+)` \\| (.*?) \\| ([a-z]+) \\| (.*) \\|$")
	name := regexp.MustCompile("`([^`]+)`")
	rows := 0
	for _, line := range strings.Split(body, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rows++
		mcp, refs, status, note := m[1], m[2], m[3], strings.TrimSpace(m[4])
		if !statuses[status] {
			t.Errorf("%s: unknown status %q", mcp, status)
		}
		if status != "covered" && note == "" {
			t.Errorf("%s is %s and says nothing about why", mcp, status)
		}
		for _, n := range name.FindAllStringSubmatch(refs, -1) {
			ref := n[1]
			switch {
			case strings.HasPrefix(ref, "change_row:"):
				if resource.ByKey(strings.TrimPrefix(ref, "change_row:")) == nil {
					t.Errorf("%s names %s, and no such resource exists", mcp, ref)
				}
			case strings.HasPrefix(ref, "run_action:"):
				if _, ok := aitools.ActionByKey(strings.TrimPrefix(ref, "run_action:")); !ok {
					t.Errorf("%s names %s, and no such action is declared", mcp, ref)
				}
			default:
				if !tools[ref] {
					t.Errorf("%s names tool %s, which the catalogue does not have", mcp, ref)
				}
			}
		}
	}
	if rows < 100 {
		t.Fatalf("only %d rows read from the parity table; the parse broke or the table shrank", rows)
	}
}
