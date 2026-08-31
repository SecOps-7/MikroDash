package rbac

// `AccessSummaryFor`, judged against what the LIVE `accessSummaryFor` answered
// for the same grants — including the grants whose targets have been deleted,
// which is the half a happy-path test would miss.

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type summaryCorpus struct {
	Roles   map[string]string `json:"roles"`
	Sites   map[string]string `json:"sites"`
	Routers map[string]struct {
		Label string `json:"label"`
		Host  string `json:"host"`
	} `json:"routers"`
	Cases map[string]struct {
		Grants []struct {
			ScopeType string  `json:"scope_type"`
			ScopeID   *string `json:"scope_id"`
			RoleID    string  `json:"role_id"`
		} `json:"grants"`
		Out struct {
			Global []string `json:"global"`
			Sites  []struct {
				SiteID   string   `json:"siteId"`
				SiteName string   `json:"siteName"`
				Roles    []string `json:"roles"`
			} `json:"sites"`
			Routers []struct {
				RouterID    string   `json:"routerId"`
				RouterLabel string   `json:"routerLabel"`
				Roles       []string `json:"roles"`
			} `json:"routers"`
		} `json:"out"`
	} `json:"cases"`
}

func loadSummaryCorpus(t *testing.T) summaryCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/access-summary-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c summaryCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty -- this test would pass against nothing")
	}
	return c
}

func TestAccessSummaryMatchesLive(t *testing.T) {
	c := loadSummaryCorpus(t)

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			// Only the roles, sites and routers the corpus declares EXIST. The
			// grants may name others, and those are the deleted targets.
			seedRoles := make([][2]string, 0, len(c.Roles))
			for id := range c.Roles {
				seedRoles = append(seedRoles, [2]string{id, "0"})
			}
			s := seed{roles: seedRoles}
			for _, g := range tc.Grants {
				scopeID := ""
				if g.ScopeID != nil {
					scopeID = *g.ScopeID
				}
				s.grants = append(s.grants, [4]string{"user", "u-1", g.ScopeType, scopeID})
				s.grantRoles = append(s.grantRoles, g.RoleID)
			}
			r := buildSummary(t, s, c)

			got, err := r.AccessSummaryFor("u-1")
			if err != nil {
				t.Fatal(err)
			}

			if !equalStrings(got.Global, tc.Out.Global) {
				t.Errorf("global %v, live %v", got.Global, tc.Out.Global)
			}
			if len(got.Sites) != len(tc.Out.Sites) {
				t.Fatalf("%d site rows, live %d\n  got  %+v\n  live %+v",
					len(got.Sites), len(tc.Out.Sites), got.Sites, tc.Out.Sites)
			}
			for i, want := range tc.Out.Sites {
				g := got.Sites[i]
				if g.SiteID != want.SiteID || g.SiteName != want.SiteName ||
					!equalStrings(g.Roles, want.Roles) {
					t.Errorf("site row %d: %+v, live %+v", i, g, want)
				}
			}
			if len(got.Routers) != len(tc.Out.Routers) {
				t.Fatalf("%d router rows, live %d\n  got  %+v\n  live %+v",
					len(got.Routers), len(tc.Out.Routers), got.Routers, tc.Out.Routers)
			}
			for i, want := range tc.Out.Routers {
				g := got.Routers[i]
				if g.RouterID != want.RouterID || g.RouterLabel != want.RouterLabel ||
					!equalStrings(g.Roles, want.Roles) {
					t.Errorf("router row %d: %+v, live %+v", i, g, want)
				}
			}
		})
	}
}

// TestTheSummaryIsNeverNil. Every slice is JSON-encoded and the modal iterates
// it; a nil marshals to `null`, and `null.map` is a TypeError that takes the
// whole modal out rather than showing an empty section.
func TestTheSummaryIsNeverNil(t *testing.T) {
	r := build(t, seed{})
	got, err := r.AccessSummaryFor("nobody")
	if err != nil {
		t.Fatal(err)
	}
	if got.Global == nil || got.Sites == nil || got.Routers == nil {
		t.Errorf("a nil slice marshals to null and breaks the modal: %+v", got)
	}
	// An unavailable resolver answers the same, rather than erroring.
	var nilR *Resolver
	empty, err := nilR.AccessSummaryFor("u-1")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Global == nil || empty.Sites == nil || empty.Routers == nil {
		t.Errorf("the unavailable answer carries a nil slice: %+v", empty)
	}
}

// TestTheRowOrderIsStableBetweenCalls.
//
// Go map iteration is randomised, and the live `[...view.bySite]` walks a Map in
// INSERTION order. Without the order slices the rows reshuffle on every request
// — the modal would show the same access in a different arrangement each time it
// is opened, which reads as data changing.
//
// Asserted by repetition rather than against an expected order: the corpus
// already pins WHICH order, and what a single comparison cannot see is that the
// order is a decision rather than one draw of a random one.
func TestTheRowOrderIsStableBetweenCalls(t *testing.T) {
	c := loadSummaryCorpus(t)
	s := seed{roles: [][2]string{{"role-ops", "0"}, {"role-ro", "0"}}}
	// Four sites and two routers, so a random arrangement has 24 orderings to
	// stumble into and repeating the same one by chance is unlikely.
	for _, id := range []string{"site-hq", "site-dc"} {
		s.grants = append(s.grants, [4]string{"user", "u-1", "site", id})
		s.grantRoles = append(s.grantRoles, "role-ops")
	}
	for _, id := range []string{"rtr-1", "rtr-2"} {
		s.grants = append(s.grants, [4]string{"user", "u-1", "router", id})
		s.grantRoles = append(s.grantRoles, "role-ro")
	}
	r := buildSummary(t, s, c)

	first, err := r.AccessSummaryFor("u-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sites) != 2 || len(first.Routers) != 2 {
		t.Fatalf("%d sites and %d routers, want 2 and 2 -- the fixture is not exercising order",
			len(first.Sites), len(first.Routers))
	}
	for i := 0; i < 20; i++ {
		again, aerr := r.AccessSummaryFor("u-1")
		if aerr != nil {
			t.Fatal(aerr)
		}
		for j := range first.Sites {
			if again.Sites[j].SiteID != first.Sites[j].SiteID {
				t.Fatalf("site order changed between calls: %v then %v",
					first.Sites, again.Sites)
			}
		}
		for j := range first.Routers {
			if again.Routers[j].RouterID != first.Routers[j].RouterID {
				t.Fatalf("router order changed between calls: %v then %v",
					first.Routers, again.Routers)
			}
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// buildSummary is `build` seeded from the corpus.
//
// The ROUTER LIST comes from the corpus rather than the package's
// `testRouters`, because this is the one test that needs their LABELS — and one
// of them deliberately has none, so the `label || host` fallback is exercised.
func buildSummary(t *testing.T, s seed, c summaryCorpus) *Resolver {
	t.Helper()
	for id, name := range c.Sites {
		s.sites = append(s.sites, [2]string{id, name})
	}
	s.roleNames = c.Roles
	r := build(t, s)
	routers := make([]Router, 0, len(c.Routers))
	for id, rt := range c.Routers {
		routers = append(routers, Router{ID: id, Label: rt.Label, Host: rt.Host})
	}
	r.routers = func() []Router { return routers }
	return r
}

// TestCanPageAnywhereIsWeakerThanCanPage.
//
// It exists for requests with no router in them, and it is deliberately the
// looser question — "may you use this page at all" rather than "may you use it
// here". The pairing is what makes that visible: a principal granted the page on
// ONE router answers yes anywhere and no on the others.
func TestCanPageAnywhereIsWeakerThanCanPage(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"viewer", "0"}},
		rolePages:  [][3]string{{"viewer", "dashboard", "read"}},
		grants:     [][4]string{{"user", "u-1", "router", "r-A"}},
		grantRoles: []string{"viewer"},
	})

	anywhere, err := r.CanPageAnywhere("u-1", "dashboard", "read")
	if err != nil {
		t.Fatal(err)
	}
	if !anywhere {
		t.Error("a principal granted dashboard on ONE router was refused everywhere -- this is " +
			"the check that stops a routerless request failing closed and locking everyone out")
	}
	// ...and the per-router answer is still NO on a router they were not granted.
	if can(t, r, "u-1", "dashboard", "read", "r-B") {
		t.Error("CanPage allowed the page on a router with no grant. CanPageAnywhere being " +
			"true must not make the scoped answer true")
	}

	// A principal with no grants at all is refused.
	none, _ := r.CanPageAnywhere("u-nobody", "dashboard", "read")
	if none {
		t.Error("a principal with no grants was allowed the page anywhere")
	}
	// A DIFFERENT page is refused for a principal whose role does not name it —
	// or this passes against an implementation that ignores the page argument.
	other, _ := r.CanPageAnywhere("u-1", "firewall", "read")
	if other {
		t.Error("a page the role does not confer was allowed -- the page argument is ignored")
	}
	// ...and so is a stronger ACCESS on a role that only confers read.
	write, _ := r.CanPageAnywhere("u-1", "dashboard", "write")
	if write {
		t.Error("write was allowed on a read-only grant -- the access argument is ignored")
	}
}

// TestTheCapabilityFlagsAreSent.
//
// ── THE BUG THIS EXISTS FOR ─────────────────────────────────────────────────
//
// `Capabilities` carried Pages and Readable alone, and that is what
// `/api/auth/status` sent. `web/src/caps.ts` reads `managePrincipals`,
// `manageSettings` and `createRouters` DIRECTLY — an absent key is `undefined`,
// which is falsy — so an administrator got the Add Router button hidden and Save
// Settings disabled with "Administrator access required".
//
// Invisible during coexistence, because Node answers that route there. It would
// have appeared at cutover looking like a permissions failure rather than a
// missing field, which is the worst way for it to present.
func TestTheCapabilityFlagsAreSent(t *testing.T) {
	c := loadSummaryCorpus(t)
	admin := buildSummary(t, seed{
		roles:      [][2]string{{"administrator", "1"}},
		grants:     [][4]string{{"user", "u-admin", "global", ""}},
		grantRoles: []string{"administrator"},
	}, c)

	caps, err := admin.CapabilitiesFor("u-admin")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]bool{
		"managePrincipals": caps.ManagePrincipals,
		"manageSettings":   caps.ManageSettings,
		"manageDb":         caps.ManageDB,
		"createRouters":    caps.CreateRouters,
	} {
		if !got {
			t.Errorf("%s is false for a builtin ADMINISTRATOR. The client reads this flag "+
				"directly and a false hides the control -- an admin would be shown an app they "+
				"appear to have no rights in", name)
		}
	}

	// THE BELIEVABILITY TWIN. Without it this passes against an implementation
	// that hardcodes every flag true, which is the opposite failure and worse.
	viewer := buildSummary(t, seed{
		roles:      [][2]string{{"viewer", "0"}},
		rolePages:  [][3]string{{"viewer", "dns", "read"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"viewer"},
	}, c)
	vcaps, err := viewer.CapabilitiesFor("u-1")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]bool{
		"managePrincipals": vcaps.ManagePrincipals,
		"manageSettings":   vcaps.ManageSettings,
		"manageDb":         vcaps.ManageDB,
		"createRouters":    vcaps.CreateRouters,
	} {
		if got {
			t.Errorf("%s is true for a read-only role -- the flags are not being computed", name)
		}
	}
}

// TestTheFlagsAreNotInterchangeable.
//
// The admin fixture has every flag TRUE and the viewer every flag FALSE, so
// swapping two permissions between flags is invisible in both — the mutation
// putting `system:settings` behind `managePrincipals` SURVIVED on exactly that.
//
// `settings` at WRITE confers `system:settings` and never `system:principals`
// (see `writeConfers`), which is the one shape where the two flags disagree.
func TestTheFlagsAreNotInterchangeable(t *testing.T) {
	c := loadSummaryCorpus(t)
	r := buildSummary(t, seed{
		roles:      [][2]string{{"settings-only", "0"}},
		rolePages:  [][3]string{{"settings-only", "settings", "write"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"settings-only"},
	}, c)

	caps, err := r.CapabilitiesFor("u-1")
	if err != nil {
		t.Fatal(err)
	}
	if !caps.ManageSettings {
		t.Error("settings:write did not confer manageSettings")
	}
	if caps.ManagePrincipals {
		t.Error("settings:write conferred managePrincipals. The two flags are DIFFERENT " +
			"permissions -- one grants the Settings page, the other grants users, groups, " +
			"sites and grants")
	}
	if caps.ManageDB {
		t.Error("settings:write conferred manageDb, which is the global purge")
	}
	if caps.CreateRouters {
		t.Error("settings:write conferred createRouters")
	}
}

// TestAnUnauthenticatedCapabilityAnswerHasNoNils.
//
// The EARLY RETURN path, which the populated one hides: with a user id the six
// lists are filled by `EffectiveRouterIDs`, so dropping one from the initialiser
// changes nothing. With no user id nothing fills them, and a nil marshals to
// `null` where the client reads `.length`.
func TestAnUnauthenticatedCapabilityAnswerHasNoNils(t *testing.T) {
	c := loadSummaryCorpus(t)
	r := buildSummary(t, seed{}, c)
	for _, id := range []string{"", "u-nobody"} {
		caps, err := r.CapabilitiesFor(id)
		if err != nil {
			t.Fatal(err)
		}
		for name, l := range map[string][]string{
			"readable": caps.Routers.Readable, "manageable": caps.Routers.Manageable,
			"history": caps.Routers.History, "ackable": caps.Routers.Ackable,
			"diagnosable": caps.Routers.Diagnosable, "scannable": caps.Routers.Scannable,
		} {
			if l == nil {
				t.Errorf("userID %q: caps.routers.%s is nil -- it marshals to null and the "+
					"client reads .length on it during the first paint", id, name)
			}
		}
		if caps.Pages == nil {
			t.Errorf("userID %q: caps.pages is nil", id)
		}
	}
}

// TestEveryRouterListIsSentAndNonNil.
//
// `caps.routers` carries SIX lists. A nil marshals to `null`, and the client
// reads `.length` on them — so a missing list is not an empty answer, it is a
// TypeError during the first paint.
func TestEveryRouterListIsSentAndNonNil(t *testing.T) {
	c := loadSummaryCorpus(t)
	r := buildSummary(t, seed{
		roles:      [][2]string{{"administrator", "1"}},
		grants:     [][4]string{{"user", "u-admin", "global", ""}},
		grantRoles: []string{"administrator"},
	}, c)

	caps, err := r.CapabilitiesFor("u-admin")
	if err != nil {
		t.Fatal(err)
	}
	lists := map[string][]string{
		"readable": caps.Routers.Readable, "manageable": caps.Routers.Manageable,
		"history": caps.Routers.History, "ackable": caps.Routers.Ackable,
		"diagnosable": caps.Routers.Diagnosable, "scannable": caps.Routers.Scannable,
	}
	for name, l := range lists {
		if l == nil {
			t.Errorf("caps.routers.%s is nil -- it marshals to null and the client reads "+
				".length on it", name)
		}
		if len(l) == 0 {
			t.Errorf("caps.routers.%s is empty for an administrator", name)
		}
	}
	// `Readable` and `Routers.Readable` are the same answer by two names, and
	// the server gates on the first. They must not drift.
	if len(caps.Readable) != len(caps.Routers.Readable) {
		t.Errorf("Readable (%d) and Routers.Readable (%d) disagree",
			len(caps.Readable), len(caps.Routers.Readable))
	}

	// A principal with NOTHING still gets six empty lists, never nils.
	none, _ := r.CapabilitiesFor("u-nobody")
	for name, l := range map[string][]string{
		"readable": none.Routers.Readable, "manageable": none.Routers.Manageable,
		"history": none.Routers.History, "ackable": none.Routers.Ackable,
		"diagnosable": none.Routers.Diagnosable, "scannable": none.Routers.Scannable,
	} {
		if l == nil {
			t.Errorf("caps.routers.%s is nil for a principal with no grants", name)
		}
	}
}

// TestConferredFailsClosed. The third fail-closed rule extracted today, after
// `disclosureAllowed` and `permitted`, and for the identical reason: nothing can
// make the grant graph fail through the route, so the arm was untested inline.
func TestConferredFailsClosed(t *testing.T) {
	if conferred(true, errTest) {
		t.Error("a FAILED lookup conferred the capability -- an error is not a yes")
	}
	if conferred(false, errTest) {
		t.Error("a failed lookup that also said no conferred it")
	}
	if conferred(false, nil) {
		t.Error("a successful NO conferred it")
	}
	if !conferred(true, nil) {
		t.Error("a successful yes was refused")
	}
}

var errTest = errors.New("the grant graph is unavailable")
