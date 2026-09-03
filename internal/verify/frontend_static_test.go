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

// capsOnlyButtons: buttons the capability layer touches and no feature binds.
//
// EMPTY, AND THAT IS THE POINT. An entry here says "this control is enabled and
// disabled and does nothing when pressed", which is never a state to settle for
// — so a new one has to be argued for in writing rather than merged quietly.
var capsOnlyButtons = map[string]string{}

// TestInteractiveControlsAreBoundBeyondCaps: a button referenced ONLY by the
// capability layer is a button nothing has wired.
//
// ── THE SHAPE THAT SHIPPED TWICE IN ONE WEEK ────────────────────────────────
//
// `caps.ts` looks up a control to enable, disable or hide it. That reference
// makes the id look bound to any check that asks "is this id named anywhere",
// which is why `TestTemplateIDsAreBound` never saw either of these:
//
//	rtrAddBtn         Add Device rendered, enabled, no listener. A new install
//	                  could create an account and then not add a router at all.
//	settingsSaveBtn   Save Settings rendered, enabled, no listener. NO
//	                  server-side setting could be saved from any tab.
//
// Both were found by a person clicking, months apart, and both were reported as
// something else — "cannot add any device", "Appearance Save not working". The
// wiring audit that would have caught them read the deleted Node source and was
// retired with it.
//
// ── WHY BUTTONS, AND WHY THIS RULE AND NOT A STRICTER ONE ───────────────────
//
// Measured against the tree when this was written: 92 buttons with ids, 91
// referenced from a feature module, exactly one — `settingsSaveBtn` — from
// `caps.ts` alone. Zero false positives, and it catches both known instances.
//
// Two stricter rules were measured and rejected. Requiring every id in the
// markup to be bound gives 240 failures, nearly all labels and layout wrappers:
// a ledger that size is one nobody reads. Requiring an `addEventListener` near
// the id gives 23, of which 22 are legitimate table-driven or delegated wiring —
// a 96% false-positive rate.
//
// Inputs and selects are deliberately out of scope: the ~78 `s_*` fields are
// bound generically through `el('s_' + key)`, which `templateIDsUnbound` already
// records under the `s_` prefix.
func TestInteractiveControlsAreBoundBeyondCaps(t *testing.T) {
	root := repoRoot(t)

	ts := readFiles(t, root, "web/src/", func(r string) bool {
		// EVERY module EXCEPT the capability layer. Reading caps.ts here would
		// make the check answer its own question.
		return hasExt(r, ".ts") && !strings.HasSuffix(r, "caps.ts")
	})
	bound := joined(ts)

	ui := readFiles(t, root, "web/src/ui/", func(r string) bool { return hasExt(r, ".html") })
	if len(ui) == 0 {
		t.Fatal("no markup found under web/src/ui — this check would pass over nothing")
	}

	btn := regexp.MustCompile(`(?s)<button\b[^>]*>`)
	idOf := regexp.MustCompile(`id\s*=\s*"([^"]+)"`)
	// DELEGATION IS BINDING TOO. A tab button carries `data-brtab` and is wired
	// by `closest('[data-brtab]')`; its id is never named and never needs to be.
	// Treating that as unbound would put thirteen honest controls in the ledger
	// and teach the reader to skim it.
	dataAttr := regexp.MustCompile(`\b(data-[a-z0-9-]+)\s*=`)

	// `closest('#rs_save')` binds by id through a selector, so the quoted form to
	// look for carries the hash.
	isBound := func(id, tag string) bool {
		for _, form := range []string{"'" + id + "'", `"` + id + `"`,
			"'#" + id + "'", `"#` + id + `"`} {
			if strings.Contains(bound, form) {
				return true
			}
		}
		for _, m := range dataAttr.FindAllStringSubmatch(tag, -1) {
			if strings.Contains(bound, m[1]) {
				return true
			}
		}
		return false
	}

	var orphans []string
	seen, checked := map[string]bool{}, 0
	for _, body := range ui {
		for _, tag := range btn.FindAllString(body, -1) {
			g := idOf.FindStringSubmatch(tag)
			if g == nil || seen[g[1]] {
				continue
			}
			seen[g[1]] = true
			checked++
			id := g[1]
			if isBound(id, tag) {
				continue
			}
			if _, ok := capsOnlyButtons[id]; ok {
				continue
			}
			orphans = append(orphans, id)
		}
	}
	if checked == 0 {
		t.Fatal("no buttons with ids were found — the markup moved and this check " +
			"is scanning nothing, which would pass for ever")
	}
	sort.Strings(orphans)
	if len(orphans) != 0 {
		t.Errorf("buttons named only by caps.ts, so enabled and disabled but never "+
			"bound: %s\nA control the capability layer can grey out and nothing "+
			"listens to renders perfectly and does nothing when pressed. That is "+
			"rtrAddBtn (#124) and settingsSaveBtn (#126). Bind it, or record it in "+
			"capsOnlyButtons with the reason.", strings.Join(orphans, ", "))
	}

	// FAILS IN BOTH DIRECTIONS. An entry that has since been bound is a stale
	// excuse, and stale excuses are how a ledger becomes folklore.
	for id := range capsOnlyButtons {
		if strings.Contains(bound, `'`+id+`'`) || strings.Contains(bound, `"`+id+`"`) {
			t.Errorf("%s is recorded as caps-only and IS now bound — delete the entry", id)
		}
	}
	t.Logf("%d buttons with ids, all bound beyond caps.ts", checked)
}
