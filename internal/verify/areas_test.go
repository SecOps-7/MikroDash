package verify

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/areas"
	"mikrodash/internal/pages"
	"mikrodash/internal/resource"
)

// The ledgers for `internal/areas`, each failing in BOTH directions.
//
// An area is a page, a nav entry, a permission key and a set of tables, all from
// one declaration — so a declaration that names something absent is a page
// nobody can open, a nav entry in no group, or a table of nothing. None of that
// shows up as a compile error, and a generated page fails quietly: it renders,
// with nothing in it.

// TestEveryAreaNamesARealPageKey, both ways: every area is a page, and no area
// claims a page key that some hand-built page already owns.
func TestEveryAreaNamesARealPageKey(t *testing.T) {
	real := map[string]bool{}
	for _, p := range pages.All {
		real[p.Key] = true
	}
	seen := map[string]bool{}
	for _, a := range areas.All() {
		if a.Key == "" {
			t.Error("an area has no page key")
			continue
		}
		if !real[a.Key] {
			t.Errorf("area %q names no page in internal/pages: its URL, its room and its "+
				"permission key would all be unknown, and an unknown page key is denied before "+
				"any role is consulted", a.Key)
		}
		if seen[a.Key] {
			t.Errorf("two areas claim the page key %q", a.Key)
		}
		seen[a.Key] = true
		if a.Title == "" {
			t.Errorf("area %q has no title, so its nav entry has no words in it", a.Key)
		}
		if a.Poll <= 0 {
			t.Errorf("area %q declares no poll interval", a.Key)
		}
	}
}

// TestEveryAreaNamesARealNavGroup. The groups are the shell's, read from the
// markup rather than from a list beside this test: a group renamed there and not
// here would put an area in a group that does not exist, and the nav entry would
// be composed into nothing.
func TestEveryAreaNamesARealNavGroup(t *testing.T) {
	root := repoRoot(t)
	shell, err := os.ReadFile(filepath.Join(root, "web", "src", "ui", "shell.html"))
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]bool{}
	for _, m := range regexp.MustCompile(`class="nav-group" data-cat="([a-z-]+)"`).
		FindAllStringSubmatch(string(shell), -1) {
		groups[m[1]] = true
	}
	if len(groups) < 5 {
		t.Fatalf("read %d nav groups from shell.html; the parse broke", len(groups))
	}
	for _, a := range areas.All() {
		if !groups[a.NavGroup] {
			names := make([]string, 0, len(groups))
			for g := range groups {
				names = append(names, g)
			}
			sort.Strings(names)
			t.Errorf("area %q names the nav group %q, which shell.html does not have (%s)",
				a.Key, a.NavGroup, strings.Join(names, ", "))
		}
	}
}

// TestEveryAreaTableNamesARealResourceAndRealColumns.
//
// The resource keeps saying what a row is; an area points at one. A table naming
// a resource the registry does not have is an empty tab, and a column naming a
// field the resource does not declare is a header over an empty cell — both of
// which render without complaint.
func TestEveryAreaTableNamesARealResourceAndRealColumns(t *testing.T) {
	for _, a := range areas.All() {
		if len(a.Tables) == 0 {
			t.Errorf("area %q declares no tables, so its page has nothing on it", a.Key)
		}
		for _, tbl := range a.Tables {
			res := resource.ByKey(tbl.Resource)
			if res == nil {
				t.Errorf("area %q names the resource %q, which the registry does not have",
					a.Key, tbl.Resource)
				continue
			}
			// AND THE RESOURCE'S OWN PAGE MUST BE THE AREA'S. The resource's
			// Page is what the write path checks permission against, so a
			// resource belonging to another page would be readable on this one
			// and writable only by someone who may write the other.
			if res.Page != a.Key {
				t.Errorf("area %q names resource %q, whose page is %q: its writes would be "+
					"gated on a page this area is not", a.Key, tbl.Resource, res.Page)
			}
			fields := map[string]bool{}
			for _, f := range res.Fields {
				fields[f.Name] = true
			}
			for _, col := range tbl.Columns {
				if !fields[col] {
					t.Errorf("area %q table %q names the column %q, which is not a field of that "+
						"resource", a.Key, tbl.Resource, col)
				}
			}
		}
	}
}

// TestEveryAreaResourceHasAFixtureAndAnAPISurfaceRow.
//
// The two pieces of per-area work the mechanism CANNOT generate, and the two that
// are quietly skipped: a capture to replay, so the area's rendering is checked
// against what a router actually returned, and a row in the frozen API surface,
// so the menu this app reads is written down and checked against MikroTik's
// documentation.
func TestEveryAreaResourceHasAFixtureAndAnAPISurfaceRow(t *testing.T) {
	root := repoRoot(t)
	surface, err := os.ReadFile(filepath.Join(root, "docs", "routeros-api-surface.md"))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]bool{}
	dirs, err := os.ReadDir(filepath.Join(root, "testdata", "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, "testdata", "fixtures", d.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			fixtures[strings.TrimSuffix(f.Name(), ".json")] = true
		}
	}
	for _, key := range areas.Resources() {
		res := resource.ByKey(key)
		if res == nil {
			continue // reported by the ledger above
		}
		if !strings.Contains(string(surface), "`"+res.Menu+"`") {
			t.Errorf("resource %q reads %s, which docs/routeros-api-surface.md does not record: "+
				"the menu this app sends is not written down anywhere it is checked against "+
				"MikroTik's documentation", key, res.Menu)
		}
		if !fixtures[key] {
			t.Errorf("resource %q has no fixture under testdata/fixtures: nothing replays what a "+
				"router actually returned for it, so its rendering is checked against nothing",
				key)
		}
	}
}
