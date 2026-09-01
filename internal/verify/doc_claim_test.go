package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestDocumentedClaimsAreTrue re-measures the numbers `CLAUDE.md` asserts.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// This repository's most expensive recurring defect is a premise that expired: a
// closed blocker still listed as open, a count that drifted, a comment describing
// a constraint that no longer applies. Three such claims were wrong on the day the
// JavaScript version of this check was first written, and this migration found
// four more — including a file header stating the opposite of what its own code
// did.
//
// Prose cannot be compiled. This is the closest available substitute: every
// number the document states about ITSELF or about this tree is measured, and a
// drift fails.
//
// ── AND WHY IT IS WRITTEN LAST ──────────────────────────────────────────────
//
// It pins whatever the document currently says. Writing it before the document
// settled would have pinned the numbers that were about to change.
type docClaim struct {
	label string
	// find must capture exactly one number from the document.
	find    *regexp.Regexp
	measure func(t *testing.T, root string) int
}

func TestDocumentedClaimsAreTrue(t *testing.T) {
	root := repoRoot(t)
	claude := mustRead(t, filepath.Join(root, "CLAUDE.md"))

	claims := []docClaim{
		{
			label: "internal/verify: Go tests",
			find:  regexp.MustCompile(`\|\s*` + "`internal/verify/`" + `\s*\|\s*(\d+) Go tests`),
			measure: func(t *testing.T, root string) int {
				return countMatches(t, filepath.Join(root, "internal", "verify"), ".go",
					regexp.MustCompile(`(?m)^func Test\w+\(t \*testing\.T\)`))
			},
		},
		{
			label: "web/test: frontend tests",
			find:  regexp.MustCompile(`\|\s*` + "`web/test/`" + `\s*\|\s*(\d+) test files`),
			measure: func(t *testing.T, root string) int {
				// FILES, NOT CASES, and the document says "test files" to
				// match. `node --test` reports 18 cases across them, but no
				// static count reproduces that number reliably -- `test(` appears
				// in strings and helpers too -- and a claim that cannot be
				// measured exactly is a claim that drifts silently.
				dir := filepath.Join(root, "web", "test")
				ents, err := os.ReadDir(dir)
				if err != nil {
					t.Fatalf("reading %s: %v", dir, err)
				}
				n := 0
				for _, e := range ents {
					if strings.HasSuffix(e.Name(), ".test.ts") {
						n++
					}
				}
				return n
			},
		},
		{
			label: "go.mod: direct dependencies",
			// "Seven are in: ..." — spelled, so the digit is derived below.
			find: regexp.MustCompile(`(?i)\b(seven|eight|six|nine) are in:`),
			measure: func(t *testing.T, root string) int {
				mod := mustRead(t, filepath.Join(root, "go.mod"))
				block := sliceBetween(t, mod, "require (", "\n)")
				n := 0
				for _, line := range strings.Split(block, "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "require (") {
						continue
					}
					if strings.Contains(line, "// indirect") {
						continue
					}
					n++
				}
				return n
			},
		},
	}

	words := map[string]int{"six": 6, "seven": 7, "eight": 8, "nine": 9}
	for _, c := range claims {
		m := c.find.FindStringSubmatch(claude)
		if m == nil {
			t.Errorf("%s: the claim is no longer in CLAUDE.md in a shape this can read — either "+
				"restate it or drop the claim, do not leave it unmeasured", c.label)
			continue
		}
		want, err := strconv.Atoi(m[1])
		if err != nil {
			if v, ok := words[strings.ToLower(m[1])]; ok {
				want = v
			} else {
				t.Errorf("%s: %q is not a number this can check", c.label, m[1])
				continue
			}
		}
		got := c.measure(t, root)
		if got != want {
			t.Errorf("%s: CLAUDE.md says %d, measured %d. Fix the document — a number that has "+
				"drifted is how every expired premise in this repository started.",
				c.label, want, got)
		}
	}
	t.Logf("%d documented claims re-measured and true", len(claims))
}

// countMatches counts regex hits across a directory's files of one extension.
func countMatches(t *testing.T, dir, ext string, re *regexp.Regexp) int {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		n += len(re.FindAllString(mustRead(t, filepath.Join(dir, e.Name())), -1))
	}
	return n
}
