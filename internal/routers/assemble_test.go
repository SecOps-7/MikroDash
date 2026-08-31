package routers

import (
	"testing"

	"mikrodash/internal/collect"
)

func sr(id string) StatsRouter {
	return StatsRouter{ID: id, Label: id, Host: "198.51.100.1"}
}

// The trap the header names. An interactive session exists but has not polled
// yet; the background pool still holds good numbers. The original reads the
// SESSION, nulls and all, because its ternary tests whether a session exists.
//
// A port that "improved" this would show a CPU figure the live page does not,
// and would mix two connections' readings in one row.
func TestAnInteractiveSessionWinsEvenWithNoPayload(t *testing.T) {
	got := BuildStats(StatsSources{
		Routers: []StatsRouter{sr("a")},
		Main:    map[string]MainSession{"a": {Connected: true}},
		Background: map[string]Summary{"a": {
			RouterID: "a", Connected: true, System: fullSystem(),
		}},
	})
	if len(got) != 1 {
		t.Fatalf("want one row, got %d", len(got))
	}
	f := fields(t, got[0])
	if f["cpu"] != nil {
		t.Errorf("cpu = %v; the interactive session has no payload, so the row must be null "+
			"rather than falling back to the background pool's reading", f["cpu"])
	}
	if f["isActive"] != true {
		t.Error("isActive must be true whenever an interactive session exists")
	}
}

// With NO interactive session the background pool is what the row reads.
func TestABackgroundOnlyRouterReadsThePool(t *testing.T) {
	got := BuildStats(StatsSources{
		Routers: []StatsRouter{sr("a")},
		Background: map[string]Summary{"a": {
			RouterID: "a", Connected: true, System: fullSystem(),
		}},
	})
	f := fields(t, got[0])
	if f["cpu"] == nil {
		t.Error("cpu is null; the background pool had a payload")
	}
	if f["isActive"] != false {
		t.Error("isActive must be false for a router nobody has open")
	}
}

// `isActive` is PRESENCE, not reachability: it marks the router somebody is
// looking at. A session that exists but has not connected is still active.
func TestIsActiveIsPresenceNotConnectedness(t *testing.T) {
	got := BuildStats(StatsSources{
		Routers: []StatsRouter{sr("a")},
		Main:    map[string]MainSession{"a": {Connected: false, LastError: "Connection refused"}},
	})
	f := fields(t, got[0])
	if f["isActive"] != true {
		t.Error("isActive false for a router with an interactive session")
	}
	if f["connected"] != false {
		t.Error("connected true for a session that has not connected")
	}
	if f["lastError"] != "Connection refused" {
		t.Errorf("lastError = %v, want the session's reason", f["lastError"])
	}
}

// A DISABLED router is absent from the payload — not an offline row in it.
func TestDisabledRoutersAreNotRows(t *testing.T) {
	got := BuildStats(StatsSources{
		Routers: []StatsRouter{sr("a"), {ID: "b", Label: "b", Host: "h", Disabled: true}, sr("c")},
	})
	if len(got) != 2 {
		t.Fatalf("want two rows, got %d", len(got))
	}
	for _, r := range got {
		if r.ID == "b" {
			t.Error("a disabled router produced a row")
		}
	}
}

// NIL Visible means UNRESTRICTED; EMPTY means the principal may read nothing.
// Treating them alike either shows a locked-down user the whole fleet or hides
// the fleet from an unrestricted one.
func TestNilVisibleIsNotEmptyVisible(t *testing.T) {
	rs := []StatsRouter{sr("a"), sr("b")}
	if n := len(BuildStats(StatsSources{Routers: rs, Visible: nil})); n != 2 {
		t.Errorf("nil Visible produced %d rows, want every router", n)
	}
	if n := len(BuildStats(StatsSources{Routers: rs, Visible: map[string]bool{}})); n != 0 {
		t.Errorf("empty Visible produced %d rows, want none", n)
	}
	got := BuildStats(StatsSources{Routers: rs, Visible: map[string]bool{"b": true}})
	if len(got) != 1 || got[0].ID != "b" {
		t.Errorf("Visible filtered to %v", got)
	}
}

// r.defaultIf, then the global setting, then "ether1".
func TestDefaultInterfacePrecedence(t *testing.T) {
	rates := &collect.IfStatusPayload{Interfaces: []collect.Interface{
		{Name: "ether1", RxMbps: 1},
		{Name: "sfp1", RxMbps: 2},
		{Name: "wan9", RxMbps: 3},
	}}
	cases := []struct {
		name, routerIf, globalIf string
		wantRx                   float64
	}{
		{"the router's own choice wins", "wan9", "sfp1", 3},
		{"the global setting is next", "", "sfp1", 2},
		{"ether1 is the last resort", "", "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := sr("a")
			r.DefaultIf = c.routerIf
			got := BuildStats(StatsSources{
				Routers:    []StatsRouter{r},
				DefaultIf:  c.globalIf,
				Background: map[string]Summary{"a": {RouterID: "a", Connected: true, IfStatus: rates}},
			})
			f := fields(t, got[0])
			if f["rxMbps"] != c.wantRx {
				t.Errorf("rxMbps = %v, want %v (watched the wrong interface)", f["rxMbps"], c.wantRx)
			}
		})
	}
}

// A router the fleet knows and NEITHER pool serves: offline, with nothing to
// say. Not an error, and not a crash.
func TestARouterInNeitherPoolIsQuietlyOffline(t *testing.T) {
	got := BuildStats(StatsSources{Routers: []StatsRouter{sr("a")}})
	f := fields(t, got[0])
	if f["connected"] != false {
		t.Error("connected true with no session at all")
	}
	if f["lastError"] != nil {
		t.Errorf("lastError = %v, want null — there is no reason to give", f["lastError"])
	}
	if f["cpu"] != nil || f["clients"] != nil {
		t.Error("absent payloads must render null")
	}
}

// The payload keeps the order it was given; the original maps over its filtered
// list and does not sort.
func TestOrderIsPreserved(t *testing.T) {
	got := BuildStats(StatsSources{Routers: []StatsRouter{sr("c"), sr("a"), sr("b")}})
	want := []string{"c", "a", "b"}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("row %d is %q, want %q — the payload was reordered", i, got[i].ID, id)
		}
	}
}

// Open alerts are independent of `connected`: a reachable router can still have
// something wrong on it.
func TestOpenAlertsAreIndependentOfConnected(t *testing.T) {
	got := BuildStats(StatsSources{
		Routers:    []StatsRouter{sr("a")},
		Background: map[string]Summary{"a": {RouterID: "a", Connected: true}},
		OpenAlerts: map[string]int{"a": 3},
	})
	if f := fields(t, got[0]); f["openAlerts"] != float64(3) {
		t.Errorf("openAlerts = %v on a connected router, want 3", f["openAlerts"])
	}
}

// #117: a device may belong to SEVERAL sites.
func TestSiteIDsOfNormalisesTheRecord(t *testing.T) {
	cases := []struct {
		name    string
		siteIDs []string
		siteID  string
		want    []string
	}{
		{"neither", nil, "", nil},
		{"only the singular", nil, "s1", []string{"s1"}},
		{"an array", []string{"s1", "s2"}, "", []string{"s1", "s2"}},
		// THE TRAP. An explicit empty array means "no sites"; falling through to
		// the singular would resurrect a membership just cleared.
		{"an EMPTY array beats a singular", []string{}, "s1", []string{}},
		{"an array beats a singular", []string{"s2"}, "s1", []string{"s2"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SiteIDsOf(c.siteIDs, c.siteID)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

// TestSiteNamesStayAlignedWithTheirIDs.
//
// ── RENAMED TWICE, AND THE HISTORY IS WHY THIS COMMENT IS LONG ─────────────
//
// It began as `TestSiteNamesDropUnresolvableIDsAndKeepTheIDs`, asserting 3 ids
// against 2 names as though that were the contract. It was not. It was a defect,
// ported faithfully from the live builder along with the CONSUMER that zips the
// two — `web/src/pages/routers.ts`, whose `names[id] = nm[i] || id` is the line
// that misaligns. So this port reproduced both halves of a bug and then wrote a
// gate that pinned it.
//
// The session working in ../MikroDash found that, from a claim this port made
// about its own safety: "the port refuses to zip them". True of the Go and
// irrelevant, because the zipping consumer is downstream and this port wrote it
// too. Renamed to `...ReproduceTheLiveMISALIGNMENT` while the fix was pending,
// and now to what it actually pins.
//
// Two premises this port had also stated were wrong for the live app, and
// checking beat arguing: "a site this viewer cannot see" does not arise there
// (`db.listSites` is an unfiltered SELECT, so an unresolvable id means DELETED),
// and the "nameless chip" worry does not either (the chips resolve BY ID from
// `_sitesById` and filter where they render, so a blank never reaches one).
//
// THE RULE NOW: same length, blank for a deleted site, and the blank is removed
// at the point of DISPLAY. Fixed upstream in e76962d; followed here.
func TestSiteNamesStayAlignedWithTheirIDs(t *testing.T) {
	r := sr("a")
	r.SiteIDs = []string{"known", "vanished", "other"}
	got := BuildStats(StatsSources{
		Routers: []StatsRouter{r},
		Sites: map[string]Site{
			"known": {Name: "Depot"},
			"other": {Name: "Annexe"},
		},
	})
	f := fields(t, got[0])

	ids, _ := f["siteIds"].([]any)
	names, _ := f["siteNames"].([]any)
	if len(ids) != 3 {
		t.Errorf("siteIds = %v; an unresolvable id must still be sent, or a membership the "+
			"operator set disappears", ids)
	}
	// SAME LENGTH AS THE IDS, with a blank where the site is gone. That is the
	// whole fix: the client zips them by index, so a dropped element shifts every
	// name after it onto the wrong site.
	if len(names) != len(ids) {
		t.Fatalf("siteNames = %v (%d) against %d ids -- the two are zipped by index on "+
			"the client, so a length difference misaligns every entry after the gap",
			names, len(names), len(ids))
	}
	if names[0] != "Depot" || names[1] != "" || names[2] != "Annexe" {
		t.Errorf("siteNames = %v, want [Depot \"\" Annexe] -- the blank holds the deleted "+
			"site's position", names)
	}
}

// `siteId` / `siteName` are backward-compatible MIRRORS of the first entry.
func TestTheSingularFieldsMirrorTheFirstSite(t *testing.T) {
	r := sr("a")
	r.SiteIDs = []string{"s2", "s1"}
	got := BuildStats(StatsSources{
		Routers: []StatsRouter{r},
		Sites:   map[string]Site{"s1": {Name: "One"}, "s2": {Name: "Two"}},
	})
	f := fields(t, got[0])
	if f["siteId"] != "s2" {
		t.Errorf("siteId = %v, want the FIRST id", f["siteId"])
	}
	if f["siteName"] != "Two" {
		t.Errorf("siteName = %v, want the first site's name", f["siteName"])
	}
}

// A device in NO site sends an empty array, not null — the live builder always
// sends a list, and a browser doing `.map` over null would throw where the
// original renders nothing.
func TestNoSitesSendsAnEmptyArrayNotNull(t *testing.T) {
	got := BuildStats(StatsSources{Routers: []StatsRouter{sr("a")}})
	f := fields(t, got[0])
	ids, ok := f["siteIds"].([]any)
	if !ok || ids == nil {
		t.Errorf("siteIds = %v; want an empty array", f["siteIds"])
	}
	if len(ids) != 0 {
		t.Errorf("siteIds = %v, want empty", ids)
	}
	if f["siteId"] != nil {
		t.Errorf("siteId = %v, want null when there are no sites", f["siteId"])
	}
}
