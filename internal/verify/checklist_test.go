package verify

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheCollectorChecklistIsComplete holds "Adding a collector" in
// docs/Collector-Architecture.md to the code, in both directions.
//
// ── WHY A CHECKLIST NEEDS A GATE ────────────────────────────────────────────
//
// Adding `ipAddresses` touched sixty-two files, and the old five-step list named
// fewer than a dozen of them. The rest were found one failing gate at a time. A
// checklist is only worth reading if it is complete, and it goes stale the moment
// somebody adds a new place that enumerates collectors. So:
//
//  1. A file naming EVERY registry collector is, by construction, a place a new
//     collector must be added. Each must be named in the checklist.
//  2. Every path and every test the checklist names must exist, so a renamed
//     file or gate cannot leave the list pointing at nothing.
//  3. The table-collector list must be exactly the files embedding `tableCore`.
func TestTheCollectorChecklistIsComplete(t *testing.T) {
	root := repoRoot(t)
	doc := mustRead(t, filepath.Join(root, "docs", "Collector-Architecture.md"))

	checklist := sectionAfter(t, doc, "### Adding a collector")
	named := map[string]bool{}
	tick := regexp.MustCompile("`([^`]+)`")
	tests := testFunctions(t, root)
	for _, m := range tick.FindAllStringSubmatch(checklist, -1) {
		v := m[1]
		switch {
		case regexp.MustCompile(`^Test\w+$`).MatchString(v):
			if !tests[v] {
				t.Errorf("the checklist names %s, and no test by that name exists", v)
			}
		case strings.Contains(v, "/") && !strings.ContainsAny(v, "<> *"):
			named[v] = true
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(v))); err != nil {
				t.Errorf("the checklist names %s, which does not exist", v)
			}
		}
	}
	if len(named) < 20 {
		t.Fatalf("only %d paths were read from the checklist; the section has moved or its format "+
			"changed, and this check is measuring nothing", len(named))
	}

	keys := registryKeys(t)
	word := make([]*regexp.Regexp, 0, len(keys))
	for _, k := range keys {
		word = append(word, regexp.MustCompile(`(?:^|[^A-Za-z0-9_])`+regexp.QuoteMeta(k)+`(?:[^A-Za-z0-9_]|$)`))
	}
	enumerators := 0
	for _, dir := range []string{"internal", "cmd", "testdata", "tools", "web/src", "web/test"} {
		base := filepath.Join(root, filepath.FromSlash(dir))
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
			if d.IsDir() {
				if rel == "web/src/gen" || strings.HasPrefix(rel, "testdata/fixtures") {
					return filepath.SkipDir
				}
				return nil
			}
			switch filepath.Ext(path) {
			case ".go", ".json", ".ts", ".js", ".html":
			default:
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, re := range word {
				if !re.Match(b) {
					return nil
				}
			}
			enumerators++
			if !named[rel] {
				t.Errorf("%s names every registry collector and is not in the checklist. A new "+
					"collector has to be added there too: add a row, with what fails if it is missed.", rel)
			}
			return nil
		})
	}
	if enumerators == 0 {
		t.Fatal("no file names every registry collector, so the key scan has stopped matching")
	}

	// ── THE TABLE-COLLECTOR LIST ────────────────────────────────────────────
	tables := sectionAfter(t, doc, "### Table collectors: one lifecycle")
	intro := tables
	if i := strings.Index(intro, "Each embeds"); i >= 0 {
		intro = intro[:i]
	}
	isKey := map[string]bool{}
	for _, k := range keys {
		isKey[k] = true
	}
	listed := map[string]bool{}
	for _, m := range tick.FindAllStringSubmatch(intro, -1) {
		if isKey[m[1]] {
			listed[m[1]] = true
		}
	}
	embed := regexp.MustCompile(`(?m)^\ttableCore\[\w+\]$`)
	for _, k := range keys {
		src, err := os.ReadFile(filepath.Join(root, "internal", "collect", strings.ToLower(k)+".go"))
		embeds := err == nil && embed.Match(src)
		switch {
		case embeds && !listed[k]:
			t.Errorf("%s embeds tableCore and is missing from the table-collector list", k)
		case !embeds && listed[k]:
			t.Errorf("%s is in the table-collector list and does not embed tableCore", k)
		}
	}
	if len(listed) == 0 {
		t.Fatal("no collector was read from the table-collector list; the sentence has moved")
	}
}

// sectionAfter is the text from a heading to the next heading of the same or a
// higher level.
func sectionAfter(t *testing.T, doc, heading string) string {
	t.Helper()
	i := strings.Index(doc, heading)
	if i < 0 {
		t.Fatalf("docs/Collector-Architecture.md has no %q heading", heading)
	}
	level := strings.Count(strings.Fields(heading)[0], "#")
	rest := doc[i+len(heading):]
	for _, line := range regexp.MustCompile(`(?m)^#{1,6} `).FindAllStringIndex(rest, -1) {
		hashes := strings.Count(strings.Fields(rest[line[0]:])[0], "#")
		if hashes <= level {
			return rest[:line[0]]
		}
	}
	return rest
}

// testFunctions is every Go test function in the repository.
func testFunctions(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	re := regexp.MustCompile(`(?m)^func (Test\w+)\(`)
	for _, dir := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range re.FindAllSubmatch(b, -1) {
				out[string(m[1])] = true
			}
			return nil
		})
	}
	return out
}
