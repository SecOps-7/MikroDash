package server

// `PUT /api/sites/:id/routers`, through the REAL mux.
//
// The decision itself is pinned against the live loop in
// `internal/routers/membership_test.go`. What is here is everything the route
// adds around it — the administrator gate, the 404, the array check, the writes
// and the audit rows.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/routers"
	"mikrodash/internal/store"
)

// sitesDDL is the table plus two rows.
//
// ── THE NULLABLE COLUMNS ARE NULLABLE HERE TOO ──────────────────────────────
//
// This said `description TEXT NOT NULL DEFAULT ”` and so on, which is NOT the
// live schema (migration 4 and 10: description, lat, lon and the three place_*
// columns are all nullable). A fixture that cannot produce a NULL cannot produce
// the row that breaks a scan into a plain `string` -- and `GetSite` had exactly
// that bug, passing every test here while it would have 500'd on any site created
// without a description, which is most of them.
//
// `site-a` is the one `r1` starts in, and it is inserted with NO description on
// purpose.
const sitesDDL = `
CREATE TABLE sites (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT, lat REAL, lon REAL,
  place_name TEXT, place_region TEXT, place_cc TEXT,
  created_at INTEGER NOT NULL);
-- site-a has NULL everywhere it may; site-b carries a full location, so the
-- scan is exercised in both directions.
INSERT INTO sites (id, name, created_at) VALUES ('site-a','Depot', 0);
INSERT INTO sites (id, name, description, lat, lon, place_name, place_region, place_cc, created_at)
VALUES ('site-b','Annexe','the annexe', 12.5, -3.25, 'Northtown', 'NR', 'ZZ', 0);
`

func sitesServer(t *testing.T, sess *Session) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, _, dir := routersServer(t, sess, "")
	if err := execOn(t, routerDBDir[s], sitesDDL); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerSites(mux)
	return s, mux, dir
}

func sitePut(mux *http.ServeMux, siteID, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/sites/"+siteID+"/routers", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// siteIDsOf reads one router's membership back out of routers.json.
func siteIDsOf(t *testing.T, dir, id string) []string {
	t.Helper()
	for _, r := range routerFile(t, dir) {
		if r["id"] != id {
			continue
		}
		raw, ok := r["siteIds"]
		if !ok || raw == nil {
			// Absent is not empty, and the caller cares which.
			return nil
		}
		list, ok := raw.([]any)
		if !ok {
			t.Fatalf("%s siteIds is %T, not an array", id, raw)
		}
		out := []string{}
		for _, v := range list {
			out = append(out, v.(string))
		}
		return out
	}
	t.Fatalf("router %s is not in the file", id)
	return nil
}

// TestASaveAddsAndDetachesInOneRequest.
func TestASaveAddsAndDetachesInOneRequest(t *testing.T) {
	_, mux, dir := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	// Believability: r1 is IN site-a and r2 is not, so the assertions below
	// distinguish a working route from one that wrote nothing.
	if got := siteIDsOf(t, dir, "r1"); len(got) != 1 || got[0] != "site-a" {
		t.Fatalf("the fixture does not start with r1 in site-a: %v", got)
	}

	w := sitePut(mux, "site-a", `{"routerIds":["r2"]}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		OK      bool `json:"ok"`
		Changed int  `json:"changed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.OK || reply.Changed != 2 {
		t.Errorf("reply = %+v, want ok with changed 2", reply)
	}
	if got := siteIDsOf(t, dir, "r1"); len(got) != 0 {
		t.Errorf("r1 was not detached: %v", got)
	}
	if got := siteIDsOf(t, dir, "r2"); len(got) != 1 || got[0] != "site-a" {
		t.Errorf("r2 was not added: %v", got)
	}
}

// TestJoiningASiteKeepsTheOthers -- the #117 defect, through the route rather
// than through the pure function, because the route is what writes the file and
// a patch that replaced `siteIds` wholesale would still pass the unit test.
func TestJoiningASiteKeepsTheOthers(t *testing.T) {
	_, mux, dir := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	if w := sitePut(mux, "site-b", `{"routerIds":["r1"]}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := siteIDsOf(t, dir, "r1")
	if len(got) != 2 || got[0] != "site-a" || got[1] != "site-b" {
		t.Errorf("r1 = %v, want [site-a site-b] -- joining a site must not take it "+
			"out of the one it was in", got)
	}
}

// TestAnUnknownSiteDetachesNothing.
//
// The 404 is not politeness. The removal branch does not care whether the site
// exists, so without the lookup a typo'd id would walk the fleet, find nobody
// who should be there... and detach every device that carries it, reporting 200.
func TestAnUnknownSiteDetachesNothing(t *testing.T) {
	_, mux, dir := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	// `site-a` HAS a member, so an unguarded route has something to destroy.
	w := sitePut(mux, "site-a-typo", `{"routerIds":[]}`, authed)
	if w.Code != 404 {
		t.Errorf("status %d, want 404", w.Code)
	}
	if got := siteIDsOf(t, dir, "r1"); len(got) != 1 || got[0] != "site-a" {
		t.Errorf("r1 = %v -- an unknown site id changed a membership", got)
	}
}

// TestABrokenStoreIsNotReportedAsAMissingSite.
//
// Collapsing the two would tell an operator their site id was wrong when the
// database is unreadable -- and they would go and check the id.
func TestABrokenStoreIsNotReportedAsAMissingSite(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	// Believability: the same request works before the store is broken.
	if w := sitePut(mux, "site-a", `{"routerIds":["r1"]}`, authed); w.Code != 200 {
		t.Fatalf("the request failed before the store was broken: %d", w.Code)
	}
	if err := s.auditDB.Close(); err != nil {
		t.Fatal(err)
	}
	if w := sitePut(mux, "site-a", `{"routerIds":["r1"]}`, authed); w.Code != 500 {
		t.Errorf("status %d, want 500 -- an unreadable database read as a bad site id",
			w.Code)
	}
}

// TestRouterIdsMustBeAnArray.
//
// ABSENT IS NOT EMPTY. `[]` is a real request — "this site has no devices" — so
// a missing field cannot default to it: a client that forgot the key would empty
// the site and be told it worked.
func TestRouterIdsMustBeAnArray(t *testing.T) {
	_, mux, dir := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	for _, body := range []string{`{}`, `{"routerIds":null}`, `{"routerIds":"r1"}`, ``} {
		w := sitePut(mux, "site-a", body, authed)
		if w.Code != 400 {
			t.Errorf("body %q gave status %d, want 400", body, w.Code)
		}
	}
	if got := siteIDsOf(t, dir, "r1"); len(got) != 1 {
		t.Errorf("a refused request still wrote: r1 = %v", got)
	}

	// ...and the explicit empty array IS honoured.
	if w := sitePut(mux, "site-a", `{"routerIds":[]}`, authed); w.Code != 200 {
		t.Fatalf("an explicit [] gave status %d: %s", w.Code, w.Body.String())
	}
	if got := siteIDsOf(t, dir, "r1"); len(got) != 0 {
		t.Errorf("an explicit [] did not empty the site: r1 = %v", got)
	}
}

// TestOnlyAnAdministratorMaySetSiteMembership.
//
// UNDER A REAL RESOLVER, because `AuthMode: "none"` makes `mayManagePrincipals`
// short-circuit to true and every other case in this file would pass with the
// gate deleted. Carol has `router:manage` on r1 — which is what a non-admin with
// write access to the Devices page holds — and that must not be enough.
func TestOnlyAnAdministratorMaySetSiteMembership(t *testing.T) {
	s, _, dir := scopedRoutersServer(t)
	if err := execOn(t, routerDBDir[s], sitesDDL); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerSites(mux)

	w := sitePut(mux, "site-b", `{"routerIds":["r1"]}`, authed)
	if w.Code != 403 {
		t.Errorf("status %d, want 403 -- router:manage is not administration, and "+
			"this route is the additive escalation `routers_api.go` strips for", w.Code)
	}
	if got := siteIDsOf(t, dir, "r1"); len(got) != 1 || got[0] != "site-a" {
		t.Errorf("the refused request still wrote: r1 = %v", got)
	}
}

// TestAnAnonymousRequestIsRefused.
func TestAnAnonymousRequestIsRefused(t *testing.T) {
	_, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	if w := sitePut(mux, "site-a", `{"routerIds":[]}`, ""); w.Code != 401 {
		t.Errorf("status %d, want 401", w.Code)
	}
}

// TestEveryChangedDeviceGetsAnAuditRow.
//
// A membership change alters who can reach a device, so it is precisely the kind
// of write that has to be attributable. The row is per DEVICE, not per request.
func TestEveryChangedDeviceGetsAnAuditRow(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	if w := sitePut(mux, "site-a", `{"routerIds":["r2"]}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r1", "r2"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range page.Rows {
		if row.Action == "router.site" && row.RouterID != nil {
			seen[*row.RouterID] = true
		}
	}
	if !seen["r1"] || !seen["r2"] {
		t.Errorf("router.site rows for %v, want both r1 (detached) and r2 (added)", seen)
	}
}

// TestANoOpSaveWritesNothingAtAll.
//
// The live loop `continue`s on a device already in the right state. Reproducing
// that matters twice over: an audit row per device per save would bury the real
// changes, and a `perms:changed` broadcast on a save that changed nothing makes
// every connected client refetch its permissions for no reason.
func TestANoOpSaveWritesNothingAtAll(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	c := hub.NewClient("c", 8)
	s.hub.Add(c)

	w := sitePut(mux, "site-a", `{"routerIds":["r1"]}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		Changed int `json:"changed"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Changed != 0 {
		t.Errorf("changed = %d, want 0", reply.Changed)
	}

	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r1", "r2"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Rows {
		if row.Action == "router.site" {
			t.Errorf("a no-op save recorded a router.site row")
		}
	}

	// ...and nothing was broadcast either.
	select {
	case b := <-c.Send:
		t.Errorf("a no-op save broadcast %s -- every connected client refetches its "+
			"permissions on that event, so an unconditional broadcast turns a save "+
			"button into a fleet-wide storm", b)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestARealChangeDoesBroadcastPermsChanged.
//
// The other half of the pair above: without it, "a no-op broadcasts nothing"
// would hold for a route that never broadcast at all, and a membership change
// leaves every browser's cached authorization view stale.
func TestARealChangeDoesBroadcastPermsChanged(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	c := hub.NewClient("c", 8)
	s.hub.Add(c) // in NO room: perms:changed is fleet-wide

	if w := sitePut(mux, "site-b", `{"routerIds":["r1"]}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	deadline := time.After(time.Second)
	for {
		select {
		case b := <-c.Send:
			if strings.Contains(string(b), "perms:changed") {
				return
			}
		case <-deadline:
			t.Fatal("no perms:changed after a membership change")
		}
	}
}

// TestTheTwoSiteIDNormalisersAgree.
//
// `internal/routers` is pure and cannot import the store, so `memberSiteIDs`
// restates `store.RouterSiteIDs`. Two copies of one rule drift, and this one has
// a subtle half: THE ARRAY WINS OUTRIGHT WHEN PRESENT, EVEN WHEN EMPTY, because
// falling through to the scalar mirror there would resurrect a membership just
// cleared. A copy that "tidied" that into `len(SiteIDs) > 0` would pass every
// test in this file, since the fixture has no record with both fields.
func TestTheTwoSiteIDNormalisersAgree(t *testing.T) {
	type row struct {
		ids    []string
		scalar string
	}
	cases := []row{
		{nil, ""},
		{nil, "s1"},
		{[]string{}, ""},
		{[]string{}, "s1"}, // the one that matters: cleared, with a stale mirror
		{[]string{"s2"}, "s1"},
		{[]string{"s2", "s3"}, ""},
	}
	// Believability: the corpus must contain a case where the two rules COULD
	// differ, or agreement is vacuous.
	var discriminating bool
	for _, c := range cases {
		if c.ids != nil && len(c.ids) == 0 && c.scalar != "" {
			discriminating = true
		}
	}
	if !discriminating {
		t.Fatal("no case has an empty array beside a set scalar, so this test cannot " +
			"tell the two rules apart")
	}

	for _, c := range cases {
		want := store.RouterSiteIDs(store.Router{SiteIDs: c.ids, SiteID: c.scalar})
		// Through the exported decision: a device with these fields, asked to
		// join a site it is not in, reports its `Before`.
		got := routers.SiteMembership(
			[]routers.SiteMemberRouter{{ID: "x", SiteIDs: c.ids, SiteID: c.scalar}},
			"brand-new-site", []string{"x"})
		if len(got) != 1 {
			t.Fatalf("%+v produced %d changes", c, len(got))
		}
		if len(got[0].Before) != len(want) {
			t.Errorf("%+v: routers says %v, store says %v", c, got[0].Before, want)
			continue
		}
		for i := range want {
			if got[0].Before[i] != want[i] {
				t.Errorf("%+v: routers says %v, store says %v", c, got[0].Before, want)
				break
			}
		}
	}
}
