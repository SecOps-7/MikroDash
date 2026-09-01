package verify

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/pages"
)

// ── THE SIXTH MEANING OF A PAGE KEY ─────────────────────────────────────────
//
// CLAUDE.md lists five things a page key is at once. There is a sixth, and it is
// the one that bit: a VISIBILITY GUARD. `isVisible('rosusers')` and
// `pageVisible('topology')` ask "is this page the one on screen?", comparing
// against `currentPage` — so a key renamed anywhere else turns the guard
// permanently false.
//
// Nothing fails when that happens. The socket still delivers, the collector
// still emits, the room is still joined; the handler simply declines to render.
// The Users page sat on "Waiting for user data…" exactly this way, and the
// Network Topology page quietly stopped scheduling live frames and animations.
//
// A grep for the pagechange spelling (`detail === '<key>'`) does not find these,
// which is how they survived a rename that was otherwise thorough.
func TestVisibilityGuardsNameRealPages(t *testing.T) {
	root := repoRoot(t)
	guard := regexp.MustCompile(`(?:isVisible|pageVisible)\('([a-z0-9-]+)'\)`)

	files := readFiles(t, root, "web/src", func(rel string) bool {
		return strings.HasSuffix(rel, ".ts") && !isTestSource(rel)
	})

	seen := 0
	for rel, body := range files {
		for _, m := range guard.FindAllStringSubmatch(uncomment(body), -1) {
			seen++
			if !pages.Has(m[1]) {
				t.Errorf("%s guards on %q, which is not a page key — "+
					"the guard is permanently false and that page never re-renders", rel, m[1])
			}
		}
	}
	// Believability: a regexp that matched nothing would pass this test forever.
	if seen < 10 {
		t.Fatalf("found only %d visibility guards — the scan stopped seeing its subject", seen)
	}
}

// The generated tables under web/src/gen are built from FROZEN JSON in testdata,
// because the generators that produced that JSON read the Node app and were
// deleted with the port-parity harness. Frozen means a rename cannot reach them
// on its own, and two of them shipped today naming pages that no longer exist:
// PAGE_KEYS (the digit shortcuts) and VIEW_PRESETS (the nav presets).
//
// THE JSON IS CHECKED, NOT THE .ts. The .ts is regenerated from it, so a .ts
// edited by hand is ahead of its own source and the next regeneration silently
// reverts it — which is the state this repository was actually in.
func TestFrozenPageTablesNameRealPages(t *testing.T) {
	root := repoRoot(t)

	read := func(name string, into any) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, "testdata", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if err := json.Unmarshal(b, into); err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
	}

	var pt struct {
		Pages       []struct{ Key string } `json:"pages"`
		PageKeys    []string               `json:"pageKeys"`
		AllNavPages []string               `json:"allNavPages"`
	}
	read("pages-table.json", &pt)

	var vp struct {
		NavMap  map[string]string   `json:"navMap"`
		Presets map[string][]string `json:"presets"`
	}
	read("view-presets.json", &vp)

	check := func(where, key string) {
		t.Helper()
		if !pages.Has(key) {
			t.Errorf("%s names %q, which is not a page key", where, key)
		}
	}
	for _, p := range pt.Pages {
		check("pages-table.json pages[].key", p.Key)
	}
	for _, k := range pt.PageKeys {
		check("pages-table.json pageKeys", k)
	}
	for _, k := range pt.AllNavPages {
		check("pages-table.json allNavPages", k)
	}
	for settingsKey, k := range vp.NavMap {
		check("view-presets.json navMap["+settingsKey+"]", k)
	}
	for name, list := range vp.Presets {
		for _, k := range list {
			check("view-presets.json presets."+name, k)
		}
	}

	if len(pt.PageKeys) < 20 || len(vp.NavMap) < 20 {
		t.Fatalf("pageKeys=%d navMap=%d — a table went empty and this check stopped asking anything",
			len(pt.PageKeys), len(vp.NavMap))
	}
}

// The other half of the ledger, and the reason it is here rather than only in
// `internal/pages`: `Renamed` is append-only and its entries outlive everyone's
// memory of the rename, so the one thing that must stay true is that every old
// name really is old. An entry whose KEY came back as a live page would quietly
// rewrite that page's stored grants on every startup.
func TestRenamedNamesNoLivePage(t *testing.T) {
	for old, now := range pages.Renamed {
		if pages.Has(old) {
			t.Errorf("pages.Renamed would move grants off %q, which is a LIVE page", old)
		}
		if !pages.Has(now) {
			t.Errorf("pages.Renamed sends %q to %q, which is not a page", old, now)
		}
	}
}
