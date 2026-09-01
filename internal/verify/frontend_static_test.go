package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ── Static checks over the frontend's own source ────────────────────────────
//
// These read `web/src/**/*.ts` as text and assert properties of the port alone.
// None of them refers to the implementation this app replaced, which is why they
// outlived it.

// TestRouterStatusRecordsBeforeItPaints pins the ORDER of two statements.
//
// The `router:status` handler both remembers a router's state and repaints the
// Settings row. The paint call RETURNS EARLY for a disabled row, so a record
// placed after it is a record that never happens -- and the symptom is remote from
// the cause: a disabled row re-enabled later renders an em dash, because nothing
// remembered the state it had while it was not being painted.
//
// Statement order is invisible to every other kind of test: both statements are
// present, both run, and the handler returns normally.
func TestRouterStatusRecordsBeforeItPaints(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, filepath.Join(root, "web", "src", "main.ts"))

	var bodies []string
	for i := strings.Index(src, "socket.on('router:status'"); i >= 0; {
		rest := src[i:]
		if end := strings.Index(rest, "\n  });"); end >= 0 {
			bodies = append(bodies, rest[:end])
		} else if len(rest) > 2000 {
			bodies = append(bodies, rest[:2000])
		} else {
			bodies = append(bodies, rest)
		}
		next := strings.Index(src[i+1:], "socket.on('router:status'")
		if next < 0 {
			break
		}
		i += 1 + next
	}
	if len(bodies) == 0 {
		t.Fatal("no socket.on('router:status') in main.ts — this test is measuring nothing")
	}

	writesRecord := regexp.MustCompile(`routerStatus\[`)
	var owning []string
	for _, b := range bodies {
		if writesRecord.MatchString(b) {
			owning = append(owning, b)
		}
	}
	if len(owning) != 1 {
		t.Fatalf("%d of %d router:status handlers write routerStatus; exactly one should own it",
			len(owning), len(bodies))
	}
	body := owning[0]

	record := regexp.MustCompile(`routerStatus\[[^\]]+\]\s*=`).FindStringIndex(body)
	paint := regexp.MustCompile(`updateRouterStatusBadge\s*\(`).FindStringIndex(body)
	if record == nil {
		t.Fatal("the router:status handler never writes routerStatus[...]. A disabled row would " +
			"re-render as an em dash after being re-enabled, because nothing remembered the " +
			"state it had while it was not being painted.")
	}
	if paint == nil {
		t.Fatal("the router:status handler never calls updateRouterStatusBadge — the Settings " +
			"table would keep whatever status it was rendered with.")
	}
	if record[0] > paint[0] {
		t.Fatal("routerStatus is written AFTER updateRouterStatusBadge. The paint call returns " +
			"early for a disabled row, so a record placed after it is a record that does not " +
			"happen — the same defect with the statements swapped.")
	}

	// UNCONDITIONAL, TOO. A record behind an `if` is a record that some rows do
	// not get, which is the same bug wearing a different shape.
	lineStart := strings.LastIndex(body[:record[0]], "\n") + 1
	lineEnd := strings.Index(body[record[0]:], "\n")
	if lineEnd < 0 {
		lineEnd = len(body) - record[0]
	}
	line := body[lineStart : record[0]+lineEnd]
	if regexp.MustCompile(`^\s*(if|\}\s*else)\b`).MatchString(line) ||
		regexp.MustCompile(`\?\s*[^:]*:`).MatchString(line) {
		t.Errorf("the routerStatus write looks conditional (%q) — every row must be recorded, "+
			"not just the ones taking one branch", strings.TrimSpace(line))
	}
	t.Log("router:status records before it paints, unconditionally")
}

// templateIDsUnbound: ids the port's own markup creates that nothing binds.
//
// Each is a CONSTRUCTED id -- built by concatenation at render time -- so no
// literal binding expression can exist for it. Recorded rather than ignored, and
// an entry that stops being constructed becomes a failure.
var templateIDsUnbound = map[string]string{
	"rtrColl_": "constructed: the grid builds `rtrColl_<key>`; the rows are bound by [data-coll]",
	"s_":       "constructed: `s_<pollKey>` per slider, bound by el('s_' + cfg.key) in settings-poll.ts",
	"sv_":      "constructed: `sv_<pollKey>` per slider label, written by the same loop",
}

// TestTemplateIDsAreBound: every id the port's markup creates is looked up
// somewhere, or recorded as deliberately unbound.
//
// An id that nothing binds is markup nothing drives. It renders, so no test
// notices, and the element simply never does anything.
func TestTemplateIDsAreBound(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") })
	all := joined(files)

	idInMarkup := regexp.MustCompile(`id=\\?["']([A-Za-z][\w-]*)\\?["']`)
	made := map[string]bool{}
	for _, body := range files {
		for _, m := range idInMarkup.FindAllStringSubmatch(body, -1) {
			made[m[1]] = true
		}
	}
	if len(made) == 0 {
		t.Fatal("no ids were found in any template — the pattern stopped matching")
	}

	bound := func(id string) bool {
		q := regexp.QuoteMeta(id)
		return regexp.MustCompile(
			`\bel\('` + q + `'\)` +
				`|\bel<[^>]*>\('` + q + `'\)` +
				`|\bbyId\('` + q + `'\)` +
				`|getElementById\('` + q + `'\)` +
				`|closest\('#` + q + `'\)` +
				`|querySelector\w*\('#` + q + `'\)`).MatchString(all)
	}

	var unbound []string
	nBound := 0
	for id := range made {
		if bound(id) {
			nBound++
			continue
		}
		unbound = append(unbound, id)
	}
	sort.Strings(unbound)

	for _, id := range unbound {
		if _, ok := templateIDsUnbound[id]; !ok {
			t.Errorf("id %q is created by a template and nothing binds it — either wire it up or "+
				"record why it cannot be bound", id)
		}
	}
	for id := range templateIDsUnbound {
		if !made[id] {
			t.Errorf("%q is recorded as unbound but no template creates it any more — delete the "+
				"entry rather than leaving a note that describes nothing", id)
		}
	}
	t.Logf("%d ids created by port templates, %d bound, %d recorded as unbound",
		len(made), nBound, len(unbound))
}
