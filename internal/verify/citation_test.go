package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestCitedPathsExist: every source path cited in a comment or a document is a
// file that is actually there.
//
// ── WHY IT EARNS ITS KEEP ───────────────────────────────────────────────────
//
// This repository explains itself in prose, heavily, and a comment naming a file
// is the most common form that takes. A path that stops being true is worse than
// no comment: it sends the next reader looking for something that moved, and
// nothing else in the build has any opinion about the inside of a comment.
//
// It has fired three times on comments that failed their own check, and the
// deletion of the port record on 2026-08-31 broke roughly seventy citations at
// once -- which is exactly the failure mode this catches.
//
// ── WIDER THAN THE ORIGINAL, DELIBERATELY ───────────────────────────────────
//
// The JavaScript version scanned `tools/*.js` and `CLAUDE.md`. That was the wrong
// scope: the citations that broke on 2026-08-31 were in shipped Go and
// TypeScript comments, which it never read. This reads the documents AND the
// source.
var citePattern = regexp.MustCompile(
	"`((?:web/src|internal|tools|cmd|testdata|docs)/[A-Za-z0-9_./-]+\\.(?:ts|go|js|mjs|json|md|sh|css|html))`")

// isIllustrative: a path written as a shape rather than a location, e.g.
// `internal/.../thing.go`. Nothing is claimed to exist, so nothing is checked.
func isIllustrative(p string) bool { return strings.Contains(p, "...") }

// expectedAbsent are paths cited that are EXPECTED not to exist — a note about
// something deleted, or a file a later change will add. Each needs a reason, and
// an entry that starts existing is itself a failure, so the list cannot rot.
var expectedAbsent = map[string]string{}

func TestCitedPathsExist(t *testing.T) {
	root := repoRoot(t)

	sources := readFiles(t, root, "", func(rel string) bool {
		if strings.HasPrefix(rel, "web/public/vendor/") || strings.HasPrefix(rel, "testdata/") {
			return false
		}
		// `Changes.md` IS EXCLUDED, and the JavaScript original excluded it for
		// the same reason: it is GITIGNORED and transient, reset to a bare
		// header after every push, and on a fresh clone it does not exist at
		// all. Scanning it also makes this check fail on its own subject matter
		// -- a note SAYING "x.js was deleted" reads as a citation OF x.js. That
		// is the same trap `stripComments` exists to avoid in the credential
		// scan: a checker that fails on its own explanation teaches the next
		// reader to weaken it.
		if rel == "Changes.md" {
			return false
		}
		return hasExt(rel, ".md", ".go", ".ts", ".js", ".sh")
	})

	cited := map[string]map[string]bool{}
	for rel, body := range sources {
		for _, m := range citePattern.FindAllStringSubmatch(body, -1) {
			p := m[1]
			if isIllustrative(p) {
				continue
			}
			if cited[p] == nil {
				cited[p] = map[string]bool{}
			}
			cited[p][rel] = true
		}
	}
	if len(cited) == 0 {
		t.Fatal("no citations were found at all — the pattern has stopped matching, and this " +
			"test is passing by looking at nothing")
	}

	paths := make([]string, 0, len(cited))
	for p := range cited {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	missing := 0
	for _, p := range paths {
		_, err := os.Stat(filepath.Join(root, p))
		exists := err == nil
		reason, declared := expectedAbsent[p]

		switch {
		case exists && declared:
			t.Errorf("%s is listed as expected-absent (%s) but now EXISTS — remove the entry "+
				"rather than leaving a note that has stopped being true", p, reason)
		case !exists && !declared:
			missing++
			t.Errorf("%s does not exist, cited by: %s", p, strings.Join(sortedKeys(cited[p]), ", "))
		}
	}
	if missing > 0 {
		t.Fatalf("%d cited path(s) do not exist — fix the citation or the file, do not delete "+
			"the comment", missing)
	}
	t.Logf("%d source paths cited across %d files, all present", len(paths), len(sources))
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
