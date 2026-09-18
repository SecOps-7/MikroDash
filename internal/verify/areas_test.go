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

// TestEveryAreaHasItsOwnIcon, both ways: every area declares an icon, no two
// areas share one, and none repeats a hand-built nav entry's icon — so every
// generated page is recognisable in the collapsed nav, rather than all of them
// wearing the one placeholder they wore until 2026-09-18.
//
// The icon reaches innerHTML, so it is also held to plain SVG shapes with plain
// attributes: no element that runs or loads anything, no event handler, no
// quote that could close the markup it is dropped into.
func TestEveryAreaHasItsOwnIcon(t *testing.T) {
	root := repoRoot(t)
	shell, err := os.ReadFile(filepath.Join(root, "web", "src", "ui", "shell.html"))
	if err != nil {
		t.Fatal(err)
	}
	handBuilt := map[string]bool{}
	for _, m := range regexp.MustCompile(`<span class="nav-icon"><svg viewBox="0 0 24 24"[^>]*>(.*?)</svg></span>`).
		FindAllStringSubmatch(string(shell), -1) {
		handBuilt[m[1]] = true
	}
	if len(handBuilt) < 10 {
		t.Fatalf("read %d hand-built nav icons from shell.html; the parse broke", len(handBuilt))
	}
	shape := regexp.MustCompile(`^(<(path|circle|rect|line|polyline|polygon|ellipse)` +
		`( (d|cx|cy|r|rx|ry|x|y|x1|y1|x2|y2|width|height|points)="[0-9A-Za-z .,-]*")+/>)+$`)
	owner := map[string]string{}
	for _, a := range areas.All() {
		if a.Icon == "" {
			t.Errorf("area %q declares no icon, so its nav entry is a label with a gap beside it", a.Key)
			continue
		}
		if !shape.MatchString(a.Icon) {
			t.Errorf("area %q's icon is not plain SVG shapes (path, circle, rect, line, polyline, "+
				"polygon, ellipse, with geometry attributes only): %s", a.Key, a.Icon)
		}
		if other, dup := owner[a.Icon]; dup {
			t.Errorf("areas %q and %q share an icon; each page needs its own", other, a.Key)
		}
		owner[a.Icon] = a.Key
		if handBuilt[a.Icon] {
			t.Errorf("area %q's icon is a hand-built nav entry's icon in shell.html", a.Key)
		}
	}
}

// TestEveryAreaShellHasItsTitleFirstThenItsTabs: every generated page's header,
// and the Tools page's, reads left to right as the title at the far left, the
// count pill beside it, then the tab strip, with only the actions (the Add slot)
// in the right-hand `.hdr-actions` corner. The shells are
// cmd/areagen's, so this holds the TEMPLATE: a new area inherits the layout, and
// a template that moves the tabs back to the right fails here for every area.
//
// The pill's colour is not in the markup — area.ts sets `active-blue` when it
// counts something — so web/test/area-sort.test.ts holds that, and sorting.
func TestEveryAreaShellHasItsTitleFirstThenItsTabs(t *testing.T) {
	root := repoRoot(t)
	type shell struct{ file, tabs, badge string }
	var shells []shell
	for _, a := range areas.All() {
		shells = append(shells, shell{"page-" + a.Key + ".html", `id="areaTabs-` + a.Key + `"`, `id="areaBadge-` + a.Key + `"`})
	}
	shells = append(shells, shell{"page-tools.html", `id="toolsTabs"`, ""})
	for _, s := range shells {
		b, err := os.ReadFile(filepath.Join(root, "web", "src", "ui", s.file))
		if err != nil {
			t.Fatal(err)
		}
		html := string(b)
		body := strings.Index(html, `class="card-body`)
		if body < 0 {
			t.Errorf("%s: no card body; the header cannot be found", s.file)
			continue
		}
		header := html[:body]
		tabs := strings.Index(header, s.tabs)
		title := strings.Index(header, `<h3 class="card-title`)
		actions := strings.Index(header, `hdr-actions`)
		if tabs < 0 || title < 0 {
			t.Errorf("%s: the header has no tab strip or no title", s.file)
			continue
		}
		// THE TITLE IS AT THE FAR LEFT and the tabs follow it (the operator,
		// 2026-09-18). Tabs first, as the Routing page has them, pushed the
		// page's own name into the middle of the header.
		if title > tabs {
			t.Errorf("%s: the tab strip comes before the title; the title is at the far left, the tabs after it", s.file)
		}
		if actions >= 0 && tabs > actions {
			t.Errorf("%s: the tab strip is in the right-hand .hdr-actions corner, which holds actions only", s.file)
		}
		if !regexp.MustCompile(regexp.QuoteMeta(s.tabs) + ` class="stab-bar rttab-bar"`).MatchString(header) {
			t.Errorf("%s: the tab strip is not `stab-bar rttab-bar`, the header strip that centres on its title", s.file)
		}
		if s.badge == "" {
			continue
		}
		badge := strings.Index(header, s.badge+` class="card-badge"`)
		if badge < title || badge > tabs {
			t.Errorf("%s: the count pill is not a card-badge beside the title, between it and the tabs", s.file)
		}
	}
}

// TestEveryAreaPillNamesARealColumnAndKind, both ways.
//
// One way: a Pills entry naming a column the table does not show is a pill that
// never draws, and one naming a kind outside PillKinds has no colour in
// web/src/pages/area.ts — tsc catches the second only once it is generated, so
// it is caught here first, with the area's name on it.
//
// The other way: a kind no column uses, or a CommonPills flag no table shows,
// is a mechanism with no instances, and is deleted rather than kept for later.
func TestEveryAreaPillNamesARealColumnAndKind(t *testing.T) {
	kinds := map[string]bool{}
	for _, k := range areas.PillKinds {
		kinds[k] = true
	}
	for col, k := range areas.CommonPills {
		if !kinds[k] {
			t.Errorf("CommonPills draws %q as %q, which is not one of PillKinds", col, k)
		}
	}
	usedKinds := map[string]bool{}
	shownCommon := map[string]bool{}
	pillCols := 0
	for _, a := range areas.All() {
		for _, tbl := range a.Tables {
			cols := map[string]bool{}
			for _, c := range tbl.Columns {
				cols[c] = true
				if k := tbl.PillFor(c); k != "" {
					usedKinds[k] = true
					pillCols++
				}
				if _, ok := areas.CommonPills[c]; ok {
					shownCommon[c] = true
				}
			}
			for col, k := range tbl.Pills {
				if !cols[col] {
					t.Errorf("area %q table %q draws %q as a pill, but the table does not show that column",
						a.Key, tbl.Resource, col)
				}
				if !kinds[k] {
					t.Errorf("area %q table %q draws %q as %q, which is not one of PillKinds",
						a.Key, tbl.Resource, col, k)
				}
			}
		}
	}
	for _, k := range areas.PillKinds {
		if !usedKinds[k] {
			t.Errorf("no column is drawn as a %q pill: a kind with no instances is deleted, not kept", k)
		}
	}
	for col := range areas.CommonPills {
		if !shownCommon[col] {
			t.Errorf("CommonPills names %q, which no area's table shows", col)
		}
	}
	// A floor, so a PillFor that stopped answering cannot pass by finding nothing.
	if pillCols < 40 {
		t.Errorf("only %d area columns are drawn as pills; the declaration has far more", pillCols)
	}
}

// TestEveryAreaGroupByNamesAShownField, both ways: a GroupBy naming a field the
// resource does not have groups every row under "" and the page offers one
// group of everything; and at least one table declares it, or the mechanism has
// no instance and is deleted rather than kept.
func TestEveryAreaGroupByNamesAShownField(t *testing.T) {
	grouped := 0
	for _, a := range areas.All() {
		for _, tbl := range a.Tables {
			if tbl.GroupBy == "" {
				continue
			}
			grouped++
			res := resource.ByKey(tbl.Resource)
			found := false
			for _, f := range res.Fields {
				if f.Name == tbl.GroupBy && f.ROS != "" && f.Type != resource.TypeSecret {
					found = true
				}
			}
			if !found {
				t.Errorf("area %q table %q groups by %q, which is not a readable field of that resource",
					a.Key, tbl.Resource, tbl.GroupBy)
			}
			shown := false
			for _, c := range tbl.Columns {
				shown = shown || c == tbl.GroupBy
			}
			if !shown {
				t.Errorf("area %q table %q groups by %q but does not show it as a column", a.Key, tbl.Resource, tbl.GroupBy)
			}
		}
	}
	if grouped == 0 {
		t.Error("no area table declares GroupBy: a mechanism with no instance is deleted, not kept")
	}
}

// TestEveryAreaHasAPresetTier: each area names the Visible Pages and Roles tier
// it joins, so a new page is placed in a preset the moment it is declared rather
// than being left out of every preset, as all 23 were until 2026-09-18.
func TestEveryAreaHasAPresetTier(t *testing.T) {
	tiers := map[string]int{}
	for _, a := range areas.All() {
		switch a.Tier {
		case "home", "standard", "advanced":
			tiers[a.Tier]++
		default:
			t.Errorf("area %q has tier %q; want home, standard or advanced", a.Key, a.Tier)
		}
	}
	// The operator's placement (2026-09-18): these five in Standard, the rest
	// in Advanced. A change to it is a change to this line, deliberately.
	for _, key := range []string{"ip-addresses", "address-lists", "interface-lists", "ip-pools", "dhcp-servers"} {
		if a, ok := areas.ByKey(key); !ok || a.Tier != "standard" {
			t.Errorf("area %q is not in Standard, where the operator placed it", key)
		}
	}
	if tiers["standard"] != 5 {
		t.Errorf("%d areas in Standard; the operator placed 5 there", tiers["standard"])
	}
}
