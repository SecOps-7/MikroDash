package verify

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/areas"
	"mikrodash/internal/collect"
	"mikrodash/internal/pages"
)

// TestPageOwnershipIsReal checks `pages.Page.Collector` against two independent
// facts: the collector registry, and the rooms the collectors actually emit to.
//
// ── WHY THIS LIVES HERE AND NOT IN internal/pages ───────────────────────────
//
// `internal/pages` imports NOTHING, and three commands depend on that - it is
// read by `cmd/webbuild`, `cmd/pagesgen` and `internal/server`, and giving it a
// dependency on `internal/collection` would drag the registry into the frontend
// build. So the cross-check goes where cross-checks go.
//
// ── THE THIRD ASSERTION IS THE ONE THAT MAKES THIS WORTH HAVING ─────────────
//
// Checking that an owner names a real registry key proves only that somebody
// typed a valid string. Checking that the named collector EMITS TO `page-<key>`
// proves the declaration describes the running app - and that is the property
// phase 4.2 wants to build on, because the whole point of a view declaring its
// rooms is that nothing else gets to have a second opinion about them.
func TestPageOwnershipIsReal(t *testing.T) {
	root := repoRoot(t)

	// ── 1. EVERY OWNER NAMES A REAL REGISTRY KEY ────────────────────────────
	var tables struct {
		Registry []struct {
			Key string `json:"key"`
		} `json:"registry"`
	}
	raw := mustRead(t, filepath.Join(root, "internal", "collection", "collection_tables.json"))
	if err := json.Unmarshal([]byte(raw), &tables); err != nil {
		t.Fatalf("collection_tables.json: %v", err)
	}
	known := map[string]bool{}
	for _, c := range tables.Registry {
		known[c.Key] = true
	}

	owned := 0
	byCollector := map[string]string{}
	for _, p := range pages.All {
		if p.Collector == "" {
			continue
		}
		owned++
		if !known[p.Collector] {
			t.Errorf("page %q is owned by %q, which is not a collector in the registry",
				p.Key, p.Collector)
		}
		// ── 2. NO TWO PAGES SHARE AN OWNER ──────────────────────────────
		//
		// Ownership is "the collector this page exists to show", and two pages
		// cannot both be that. `ifStatus` is the case it guards: it FEEDS
		// `interfaces` and `network-topology` both, and owns only the first.
		//
		// `areas` IS THE ONE EXCEPTION, and it is exact rather than loose: it
		// exists for every generated page, so it owns each declared area and
		// nothing else. Held both ways below, after the loop.
		if p.Collector == "areas" {
			if _, ok := areas.ByKey(p.Key); !ok {
				t.Errorf("%q is owned by `areas` and is not a declared area", p.Key)
			}
			continue
		}
		if other, dup := byCollector[p.Collector]; dup {
			t.Errorf("%q owns both %q and %q. Feeding two pages is normal; OWNING two "+
				"is not - ownership is the page a collector exists for.",
				p.Collector, other, p.Key)
		}
		byCollector[p.Collector] = p.Key
	}
	if owned == 0 {
		t.Fatal("no page declares an owner - this check is reading nothing")
	}
	ownerOf := map[string]string{}
	for _, p := range pages.All {
		ownerOf[p.Key] = p.Collector
	}
	for _, a := range areas.All() {
		if ownerOf[a.Key] != "areas" {
			t.Errorf("area %q is not a page owned by `areas` (owner %q)", a.Key, ownerOf[a.Key])
		}
	}

	// ── 3. THE OWNER DECLARES page-<key> ───────────────────────────────────
	//
	// READ AS DATA since 4.2. This used to scan `internal/collect` source for an
	// emit literal, which meant a regex, a collector-key-to-filename table, and an
	// exemption for `logs` and `talkers` because they emitted to a named constant
	// and could not be seen at all.
	//
	// Now that every audience is declared, the check asks the declaration
	// directly. No regex to drift, no filenames to keep, and no exemptions -
	// `logs` is checked like everything else.
	//
	// THE FIRST VERSION OF THIS ASSERTION WAS THEATRE, and it is worth
	// remembering why: it searched EVERY collector file, so re-declaring
	// `interfaces` as owned by `netwatch` passed - `ifstatus.go` still emitted to
	// `page-interfaces` and the scan did not care who did. Reading one
	// collector's own declaration is what makes it mean something.
	var unproven []string
	for _, p := range pages.All {
		if p.Collector == "" {
			continue
		}
		want := "page-" + p.Key
		found := false
		for _, r := range collect.RoomsOf(p.Collector) {
			if r == want {
				found = true
			}
		}
		if !found {
			unproven = append(unproven, p.Key+" (owner "+p.Collector+" declares "+
				strings.Join(collect.RoomsOf(p.Collector), " ")+")")
		}
	}
	sort.Strings(unproven)
	if len(unproven) > 0 {
		t.Errorf("these pages declare an owner and nothing emits to their room: %v.\n"+
			"Either the declaration is wrong, or the collector stopped emitting there - "+
			"and the second is silent, because the page simply never updates.", unproven)
	}
	t.Logf("%d of %d pages declare an owner; %d pages own nothing and cannot be emptied "+
		"by a collector", owned, len(pages.All), len(pages.All)-owned)
}
