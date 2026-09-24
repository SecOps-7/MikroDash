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
// four more - including a file header stating the opposite of what its own code
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
	// doc is the file the claim lives in, relative to the repository root.
	// Empty means CLAUDE.md, which is where most of them are.
	//
	// A CLAIM IS NOT ONE PER NUMBER, IT IS ONE PER SENTENCE THAT STATES IT. The
	// same count is written in more than one place, and only the places listed
	// here are measured: CLAUDE.md's table row was pinned while the prose
	// twenty lines below it was not, so the prose could keep a number the audit
	// had already corrected in the table - with the audit green. CONTRIBUTING.md
	// was never read at all and had drifted by eleven and seven.
	doc string
	// find must capture exactly one number from the document.
	find    *regexp.Regexp
	measure func(t *testing.T, root string) int
}

// verifyGoTests counts the Go tests in internal/verify.
func verifyGoTests(t *testing.T, root string) int {
	return countMatches(t, filepath.Join(root, "internal", "verify"), ".go",
		regexp.MustCompile(`(?m)^func Test\w+\(t \*testing\.T\)`))
}

// webTestFiles counts the frontend test FILES.
//
// FILES, NOT CASES, and both documents say "test files" to match. `node --test`
// reports more cases than files, but no static count reproduces that number
// reliably - `test(` appears in strings and helpers too - and a claim that
// cannot be measured exactly is a claim that drifts silently.
func webTestFiles(t *testing.T, root string) int {
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
}

func TestDocumentedClaimsAreTrue(t *testing.T) {
	root := repoRoot(t)
	docs := map[string]string{}
	readDoc := func(name string) string {
		if name == "" {
			name = "CLAUDE.md"
		}
		if _, ok := docs[name]; !ok {
			docs[name] = mustRead(t, filepath.Join(root, name))
		}
		return docs[name]
	}

	// ONE CLAIM PER SENTENCE, AND ONE SENTENCE PER NUMBER. CLAUDE.md used to state
	// the frontend count three times and the verify count twice, each copy added
	// after another had drifted, and each needing its own claim here. It states
	// each once now, so there is one place to update and one place to measure.
	// A count written a second time needs a claim of its own, or it drifts.
	claims := []docClaim{
		{
			label:   "CLAUDE.md table, internal/verify: Go tests",
			find:    regexp.MustCompile(`\|\s*` + "`internal/verify/`" + `\s*\|\s*(\d+) Go tests`),
			measure: verifyGoTests,
		},
		{
			label:   "CLAUDE.md table, web/test: frontend tests",
			find:    regexp.MustCompile(`\|\s*` + "`web/test/`" + `\s*\|\s*(\d+) test files`),
			measure: webTestFiles,
		},
		{
			// CONTRIBUTING.md CARRIES THE SAME TABLE and was never read by this
			// audit, so it drifted freely: 23 and 15 against a measured 34 and
			// 22. A contributor reads that file first.
			label:   "CONTRIBUTING.md, internal/verify: Go tests",
			doc:     "CONTRIBUTING.md",
			find:    regexp.MustCompile(`\|\s*` + "`internal/verify/`" + `\s*\|\s*(\d+) Go tests`),
			measure: verifyGoTests,
		},
		{
			label:   "CONTRIBUTING.md, web/test: frontend tests",
			doc:     "CONTRIBUTING.md",
			find:    regexp.MustCompile(`\|\s*` + "`web/test/`" + `\s*\|\s*(\d+) test files`),
			measure: webTestFiles,
		},
		{
			label: "go.mod: direct dependencies",
			// "Seven are in: ..." - spelled, so the digit is derived below.
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
		m := c.find.FindStringSubmatch(readDoc(c.doc))
		if m == nil {
			where := c.doc
			if where == "" {
				where = "CLAUDE.md"
			}
			t.Errorf("%s: the claim is no longer in %s in a shape this can read - either "+
				"restate it or drop the claim, do not leave it unmeasured", c.label, where)
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
			t.Errorf("%s: the document says %d, measured %d. Fix the document - a number that has "+
				"drifted is how every expired premise in this repository started.",
				c.label, want, got)
		}
	}
	// "RE-MEASURED", not "true": this line prints on a failing run too, and the
	// failures above are the verdict. Saying "and true" beside a reported
	// mismatch is the same species of claim this file exists to catch.
	t.Logf("%d documented claims re-measured", len(claims))
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
