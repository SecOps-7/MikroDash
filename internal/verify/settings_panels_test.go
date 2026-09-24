package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// EVERY SETTINGS CARD LIVES INSIDE EXACTLY ONE TAB PANEL.
//
// ── THE BUG THIS EXISTS FOR, WHICH SHIPPED ─────────────────────────────────
//
// Removing the Navigation card left one `</div>` behind. It closed
// `#stab-appearance` early, so "Visible Pages" fell outside every panel - and a
// card outside them all is subject to no `.stab-panel { display: none }` at all.
// It therefore appeared on the General tab, the Notifications tab, the About
// tab, every tab. The operator found it.
//
// ── WHY NOTHING CAUGHT IT ──────────────────────────────────────────────────
//
// The page still rendered. tsc has no opinion about markup, the web tests mount
// modules rather than this file, and a browser check that opens ONE tab sees a
// perfectly normal page: the escaped card sits exactly where it always did on
// the tab it belongs to. It is only wrong on the other seven.
//
// ── WHY A BRACKET WALK AND NOT A SELECTOR ──────────────────────────────────
//
// The question is about NESTING, which is the thing that broke. Counting cards,
// or checking each panel contains the ones it should, both pass while a card
// hangs outside - the count is unchanged and the panel it left still holds the
// rest. Only walking the div depth answers "is this inside that".
var (
	reStabPanel = regexp.MustCompile(`<div class="stab-panel[^"]*"[^>]*id="([\w-]+)"`)
	reDivTag    = regexp.MustCompile(`<div\b[^>]*>|</div>`)
	reCardTitle = regexp.MustCompile(`class="scard-title">([^<]+)<`)
)

func TestEverySettingsCardIsInsideOneTabPanel(t *testing.T) {
	root := repoRoot(t)
	src := mustRead(t, filepath.Join(root, "web", "src", "ui", "page-settings.html"))

	panels := reStabPanel.FindAllStringSubmatchIndex(src, -1)
	// AN EMPTY SCAN IS A BROKEN SCAN. If the markup is restructured so the
	// pattern stops matching, this must fail loudly rather than report that
	// every card is fine.
	if len(panels) < 5 {
		t.Fatalf("found %d tab panels in page-settings.html - this scan has broken, "+
			"and an empty result agrees with every assertion below", len(panels))
	}

	// Which panel, if any, encloses each card.
	home := map[string]string{}
	for _, p := range panels {
		id := src[p[2]:p[3]]
		depth, end := 0, -1
		for _, tag := range reDivTag.FindAllStringIndex(src[p[0]:], -1) {
			if strings.HasPrefix(src[p[0]+tag[0]:], "</div>") {
				depth--
			} else {
				depth++
			}
			if depth == 0 {
				end = p[0] + tag[1]
				break
			}
		}
		if end < 0 {
			t.Fatalf("panel %s is never closed - the markup is unbalanced", id)
		}
		for _, m := range reCardTitle.FindAllStringSubmatch(src[p[0]:end], -1) {
			title := strings.TrimSpace(m[1])
			if was, dup := home[title]; dup {
				t.Errorf("card %q is inside both %s and %s", title, was, id)
			}
			home[title] = id
		}
	}

	var orphans []string
	for _, m := range reCardTitle.FindAllStringSubmatch(src, -1) {
		title := strings.TrimSpace(m[1])
		if _, ok := home[title]; !ok {
			orphans = append(orphans, title)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("%v sit outside every tab panel, so no `.stab-panel { display: none }` "+
			"hides them and they render on EVERY settings tab - which is invisible "+
			"until a second tab is opened", orphans)
	}
	t.Logf("%d card(s) across %d panel(s), each in exactly one", len(home), len(panels))
}
