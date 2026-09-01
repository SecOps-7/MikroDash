package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// classesExpectedDead: classes this port toggles that nothing answers.
var classesExpectedDead = map[string]string{}

// TestToggledClassesAreAnswered: every class the port adds, removes or toggles is
// answered by something -- a stylesheet rule, extracted markup, or a read-back in
// the port's own code.
//
// A class that nothing answers is a state change with no effect: the element gets
// the class, the page looks identical, and the feature silently does not work.
//
// ── IT NEEDED NO RECORDING EITHER ───────────────────────────────────────────
//
// The JavaScript original froze "the live stylesheet class names" out of the
// deleted reference, because `/css/*` was proxied to Node during coexistence. That
// is over: `dashboard-grid.css` and `topology.css` are committed HERE, under
// `web/public/css/`, and served by this binary. Reading them locally answers the
// same question with no recording at all -- which is why this survives the
// harness rather than dying with it.
func TestToggledClassesAreAnswered(t *testing.T) {
	root := repoRoot(t)

	tsFiles := readFiles(t, root, "web/src/", func(r string) bool { return hasExt(r, ".ts") })
	tsAll := joined(tsFiles)
	markup := joined(readFiles(t, root, "web/src/ui/", func(r string) bool { return hasExt(r, ".html") }))

	// EVERY stylesheet this app serves, not just app.css.
	css := mustRead(t, filepath.Join(root, "web", "public", "app.css"))
	// EVERY stylesheet the page loads, vendored ones included. `.visible` is
	// Tabler's, and reading only the hand-written CSS made this accuse a working
	// login fade of hooking into nothing.
	for _, dir := range []string{
		filepath.Join(root, "web", "public", "css"),
		filepath.Join(root, "web", "public", "vendor"),
	} {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".css") {
				css += "\n" + mustRead(t, filepath.Join(dir, e.Name()))
			}
		}
	}
	if len(css) < 20000 {
		t.Fatalf("only %d bytes of CSS read — the stylesheets moved, and this test would accuse "+
			"every styled class of hooking into nothing", len(css))
	}

	toggled := map[string]bool{}
	for _, m := range regexp.MustCompile(`classList\.(?:add|remove|toggle)\(\s*'([a-zA-Z][\w-]*)'`).
		FindAllStringSubmatch(tsAll, -1) {
		toggled[m[1]] = true
	}
	if len(toggled) < 20 {
		t.Fatalf("only %d toggled classes found — the scan broke", len(toggled))
	}

	styled := func(c string) bool {
		return regexp.MustCompile(`\.` + regexp.QuoteMeta(c) + `(?:[^\w-]|$)`).MatchString(css)
	}
	readBack := func(c string) bool {
		q := regexp.QuoteMeta(c)
		return regexp.MustCompile(`classList\.contains\(\s*'` + q + `'` +
			`|querySelector\w*\(\s*'[^']*\.` + q + `(?:[^\w-]|$)` +
			`|matches\(\s*'[^']*\.` + q + `(?:[^\w-]|$)` +
			`|closest\(\s*'[^']*\.` + q + `(?:[^\w-]|$)`).MatchString(tsAll)
	}
	inMarkup := func(c string) bool {
		return regexp.MustCompile(`class="[^"]*\b` + regexp.QuoteMeta(c) + `\b`).MatchString(markup)
	}

	var dead []string
	for c := range toggled {
		if !styled(c) && !readBack(c) && !inMarkup(c) {
			dead = append(dead, c)
		}
	}
	sort.Strings(dead)

	have := map[string]bool{}
	for _, c := range dead {
		have[c] = true
		if _, ok := classesExpectedDead[c]; !ok {
			t.Errorf("the port toggles .%s and nothing answers it — no stylesheet rule, no "+
				"markup, no read-back. The class goes on and the page looks identical.", c)
		}
	}
	for c := range classesExpectedDead {
		if !have[c] {
			t.Errorf(".%s is recorded as answered by nothing, but something answers it now — "+
				"delete the entry", c)
		}
	}
	t.Logf("%d classes toggled by the port, every one answered by the stylesheet, the markup or "+
		"the port itself", len(toggled))
}

// settingsKeysRecorded: settings keys nothing reads, with why.
var settingsKeysRecorded = map[string]string{
	"firewallTopN": "dead in the original too — it had no consumer either.",
}

// TestEverySettingsKeyIsRead: a key with a default is a key something consumes.
//
// A settings key nothing reads is a control the operator can change that does
// nothing. It saves, it round-trips, it reloads — and no behaviour follows.
func TestEverySettingsKeyIsRead(t *testing.T) {
	root := repoRoot(t)

	tables := filepath.Join(root, "internal", "store", "settings_tables.json")
	raw, err := os.ReadFile(tables)
	if err != nil {
		t.Fatalf("reading %s: %v", tables, err)
	}
	// The defaults block is the key list; parsed by text rather than a struct so
	// a shape change fails loudly here instead of silently yielding nothing.
	defaults := regexp.MustCompile(`"defaults"\s*:\s*\{`).FindStringIndex(string(raw))
	if defaults == nil {
		t.Fatal("settings_tables.json has no \"defaults\" object — the generated shape changed")
	}
	keys := map[string]bool{}
	for _, m := range regexp.MustCompile(`"([A-Za-z][A-Za-z0-9_]*)"\s*:`).
		FindAllStringSubmatch(string(raw)[defaults[1]:], -1) {
		keys[m[1]] = true
	}
	if len(keys) < 20 {
		t.Fatalf("only %d settings keys read — the parse broke", len(keys))
	}

	ignored := func(rel string) bool {
		return rel == "internal/store/settings_tables.json" ||
			rel == "internal/store/settings_write_tables.json" ||
			rel == "internal/store/disclose.go" ||
			strings.HasPrefix(rel, "web/src/gen/") ||
			// THIS PACKAGE. The ledger below names the very key it records, so
			// scanning ourselves would prove every recorded key is "read" -- the
			// same trap `stripComments` avoids in the credential scan.
			strings.HasPrefix(rel, "internal/verify/") ||
			// AND THE SETTINGS MARKUP. `page-settings.html` renders a CONTROL
			// for each key, which is not a consumer: a control the operator can
			// change that nothing acts on is precisely the defect this looks
			// for, so counting the control as a read would make it unfalsifiable.
			strings.HasPrefix(rel, "web/src/ui/") ||
			strings.HasSuffix(rel, "_test.go")
	}
	var sources []string
	for _, dir := range []string{"internal/", "web/src/", "cmd/"} {
		for rel, body := range readFiles(t, root, dir, func(r string) bool {
			return !ignored(r) && hasExt(r, ".go", ".ts", ".js", ".html", ".json")
		}) {
			_ = rel
			sources = append(sources, body)
		}
	}
	hay := strings.Join(sources, "\n")

	var unread []string
	for k := range keys {
		if !strings.Contains(hay, k) {
			unread = append(unread, k)
		}
	}
	sort.Strings(unread)

	have := map[string]bool{}
	for _, k := range unread {
		have[k] = true
		if _, ok := settingsKeysRecorded[k]; !ok {
			t.Errorf("settings key %q has a default and nothing reads it — the operator can "+
				"change a control that does nothing", k)
		}
	}
	for k := range settingsKeysRecorded {
		if !have[k] {
			t.Errorf("%q is recorded as unread, but something reads it now — delete the entry", k)
		}
	}
	t.Logf("%d settings keys, every one read or recorded", len(keys))
}
