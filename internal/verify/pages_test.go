package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pagesNotMounted: extracted page markup deliberately mounted nowhere.
var pagesNotMounted = map[string]string{}

// TestPagesAreFullyMounted: a page's markup, the build's page list and the
// router's PORTED set must agree.
//
// HALF-MOUNTED IS THE INTERESTING STATE. A page composed into index.html but
// absent from PORTED renders an empty shell; one in PORTED but not composed
// navigates to markup that is not there. Both look like "the page is broken" at
// runtime and neither is visible in either list on its own.
func TestPagesAreFullyMounted(t *testing.T) {
	root := repoRoot(t)

	composed := stringSetFrom(t,
		mustRead(t, filepath.Join(root, "cmd", "webbuild", "main.go")),
		`var PAGES = \[\]string\{([\s\S]*?)\}`, "cmd/webbuild's PAGES")
	ported := stringSetFrom(t,
		mustRead(t, filepath.Join(root, "web", "src", "main.ts")),
		`const PORTED = new Set\(\[([\s\S]*?)\]\)`, "main.ts's PORTED")

	ents, err := os.ReadDir(filepath.Join(root, "web", "src", "ui"))
	if err != nil {
		t.Fatalf("reading web/src/ui: %v", err)
	}
	pageFile := regexp.MustCompile(`^page-([a-z]+)\.html$`)
	var extracted []string
	for _, e := range ents {
		if m := pageFile.FindStringSubmatch(e.Name()); m != nil {
			extracted = append(extracted, m[1])
		}
	}
	sort.Strings(extracted)
	if len(extracted) < 15 {
		t.Fatalf("only %d extracted page bodies found — the scan broke", len(extracted))
	}

	mounted := 0
	for _, key := range extracted {
		inBuild, inPorted := composed[key], ported[key]
		switch {
		case inBuild && inPorted:
			mounted++
		case !inBuild && !inPorted:
			if _, ok := pagesNotMounted[key]; !ok {
				t.Errorf("page-%s.html is extracted and mounted NOWHERE. Mount it in both "+
					"webbuild's PAGES and main.ts's PORTED, or record what blocks it.", key)
			}
		default:
			t.Errorf("%s is HALF-MOUNTED: %s but %s.", key,
				pick(inBuild, "composed into index.html", "NOT composed"),
				pick(inPorted, "in PORTED", "NOT in PORTED"))
		}
	}
	t.Logf("%d extracted bodies — %d fully mounted, %d recorded as blocked",
		len(extracted), mounted, len(pagesNotMounted))
}

// TestEveryLookupHasAProducer: an id the port looks up is an id something in the
// port creates.
//
// A lookup that resolves to nothing returns null and the code around it quietly
// does less than it should. The producer may be extracted markup, a template
// literal in TypeScript, or the login shell.
//
// ── IT NEEDED NO RECORDING, ONLY A PATH ─────────────────────────────────────
//
// The JavaScript original took its "scripts the served page loads" from the
// deleted reference. Every one of those is committed HERE now -- `preflight.ts`
// builds to `web/dist/preflight.js` -- so this reads the local tree and the
// recording falls away entirely.
func TestEveryLookupHasAProducer(t *testing.T) {
	root := repoRoot(t)

	produced := map[string]bool{}
	for _, body := range readFiles(t, root, "web/src/ui/", func(r string) bool { return hasExt(r, ".html") }) {
		for _, m := range regexp.MustCompile(`id="([A-Za-z0-9_-]+)"`).FindAllStringSubmatch(body, -1) {
			produced[m[1]] = true
		}
	}
	tsFiles := readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") })
	ts := joined(tsFiles)
	// Ids the TypeScript itself writes into markup it builds.
	for _, m := range regexp.MustCompile(`id=\\?["']([A-Za-z][\w-]*)\\?["']`).FindAllStringSubmatch(ts, -1) {
		produced[m[1]] = true
	}
	// AND IDS ASSIGNED TO AN ELEMENT, which is markup built without markup:
	// `st.id = 'navBoot'` in preflight, `node.id = 'sysMetaTemp'` created lazily
	// on first use. Missing these made two real producers look absent.
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`\.id\s*=\s*["']([A-Za-z][\w-]*)["']`),
		regexp.MustCompile(`setAttribute\(\s*['"]id['"]\s*,\s*['"]([A-Za-z][\w-]*)['"]`),
	} {
		for _, m := range re.FindAllStringSubmatch(ts, -1) {
			produced[m[1]] = true
		}
	}
	if len(produced) < 100 {
		t.Fatalf("only %d produced ids found — the scan broke", len(produced))
	}

	lookedUp := map[string]bool{}
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`\bel(?:<[^>]*>)?\('([A-Za-z][\w-]*)'\)`),
		regexp.MustCompile(`getElementById\('([A-Za-z][\w-]*)'\)`),
		regexp.MustCompile(`\bbyId\('([A-Za-z][\w-]*)'\)`),
	} {
		for _, m := range re.FindAllStringSubmatch(ts, -1) {
			lookedUp[m[1]] = true
		}
	}
	if len(lookedUp) < 50 {
		t.Fatalf("only %d lookups found — the scan broke", len(lookedUp))
	}

	// A CONSTRUCTED id cannot be matched literally: `el('s_' + cfg.key)` is a
	// lookup whose name does not exist as a token anywhere.
	constructed := regexp.MustCompile(`\bel(?:<[^>]*>)?\('[A-Za-z][\w-]*'\s*\+`).MatchString(ts)

	var orphans []string
	for id := range lookedUp {
		if !produced[id] {
			orphans = append(orphans, id)
		}
	}
	sort.Strings(orphans)

	have := map[string]bool{}
	for _, id := range orphans {
		have[id] = true
		if _, ok := lookupsWithoutProducer[id]; !ok {
			t.Errorf("the port looks up #%s and nothing in the port produces it — the lookup "+
				"resolves to null and whatever depends on it silently does nothing", id)
		}
	}
	for id := range lookupsWithoutProducer {
		if !have[id] {
			t.Errorf("#%s is recorded as having no producer, but something produces it now — "+
				"delete the entry", id)
		}
	}
	t.Logf("%d lookups across the port, every one produced (%d produced ids, constructed "+
		"lookups present: %v)", len(lookedUp), len(produced), constructed)
}

// lookupsWithoutProducer: ids the port looks up that nothing here creates.
//
// ── THESE WERE HIDDEN BY THE RECORDING ──────────────────────────────────────
//
// The JavaScript original also counted ids produced by the DELETED app's own
// loaded scripts, so both of these resolved and it reported a clean run. They do
// not resolve here, and that is the truth: the element is simply not in this
// port's markup.
//
// Both lookups are GUARDED -- `const sub = el('connMapSub'); if (sub) ...` -- so
// nothing crashes and nothing misbehaves; the branch is dead. They are recorded
// rather than deleted because removing the code is a behaviour decision, not a
// verification one, and recording them is what makes that decision visible
// instead of leaving two dead lookups nobody can see.
var lookupsWithoutProducer = map[string]string{
	"connMapSub": "the connections map subtitle. Guarded by `if (sub)`; the element is not in " +
		"this port's markup, so the branch never runs.",
	"wlBand6": "the 6 GHz band slot on the wireless page. Guarded the same way.",
}

// stringSetFrom pulls the quoted entries out of a declaration, failing when the
// declaration's shape changes rather than returning an empty set that would make
// every comparison below pass vacuously.
func stringSetFrom(t *testing.T, src, pattern, what string) map[string]bool {
	t.Helper()
	m := regexp.MustCompile(pattern).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("could not read %s — the declaration shape changed", what)
	}
	out := map[string]bool{}
	for _, q := range regexp.MustCompile(`['"]([a-z]+)['"]`).FindAllStringSubmatch(m[1], -1) {
		out[q[1]] = true
	}
	if len(out) < 15 {
		t.Fatalf("%s holds only %d entries; the match broke", what, len(out))
	}
	return out
}

func pick(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

var _ = json.Marshal
var _ = strings.TrimSpace
