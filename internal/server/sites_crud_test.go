package server

// `GET/POST/PUT/DELETE /api/sites`, through the REAL mux.
//
// The validation is pinned in `internal/sites`, the storage in `internal/db` and
// the cascade in `internal/store`. What is here is the routes: the gate, the
// status codes, the audit rows, the broadcasts, and the ORDER of the delete's
// four effects.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
)

func siteReq(mux *http.ServeMux, method, path, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func decodeSites(t *testing.T, w *httptest.ResponseRecorder) []db.Site {
	t.Helper()
	var reply struct {
		OK    bool      `json:"ok"`
		Sites []db.Site `json:"sites"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("reply is not JSON: %s", w.Body.String())
	}
	if !reply.OK {
		t.Fatalf("reply is not ok: %s", w.Body.String())
	}
	return reply.Sites
}

func TestListSitesReturnsThemInNameOrder(t *testing.T) {
	_, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	got := decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed))
	if len(got) != 2 {
		t.Fatalf("%d sites, want 2", len(got))
	}
	if got[0].Name != "Annexe" || got[1].Name != "Depot" {
		t.Errorf("order is %s, %s -- want name order", got[0].Name, got[1].Name)
	}
	// The nullable columns read back as empty rather than erroring, and the
	// populated one keeps its location.
	if got[1].Description != nil || got[1].Lat != nil {
		t.Errorf("the NULL-column site read back as %+v", got[1])
	}
	if got[0].Lat == nil || *got[0].Lat != 12.5 {
		t.Errorf("the located site lost its coordinates: %+v", got[0])
	}
}

// TestListSitesNeedsOnlyASession.
//
// The live route carries NO Rbac middleware, and `routers_api.go` depends on
// that being true where it strips the site fields from a non-administrator's
// router write: "every site id enumerable from an ungated GET /api/sites". If
// this route were tightened, that comment would become wrong and the strip would
// look like belt-and-braces rather than the load-bearing check it is.
func TestListSitesNeedsOnlyASession(t *testing.T) {
	s, _, _ := scopedRoutersServer(t) // carol: router:manage, NOT system:principals
	if err := execOn(t, routerDBDir[s], sitesDDL); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerSites(mux)

	// Believability: she really is refused the ADMIN routes on this same mux.
	if w := siteReq(mux, "POST", "/api/sites", `{"name":"X"}`, authed); w.Code != 403 {
		t.Fatalf("a non-administrator was allowed to create a site (%d)", w.Code)
	}
	if w := siteReq(mux, "GET", "/api/sites", "", authed); w.Code != 200 {
		t.Errorf("GET /api/sites gave %d for a signed-in non-administrator", w.Code)
	}
	// ...but not signed in at all is still refused.
	if w := siteReq(mux, "GET", "/api/sites", "", ""); w.Code != 401 {
		t.Errorf("an anonymous list gave %d, want 401", w.Code)
	}
}

func TestCreateSiteWritesAndBroadcasts(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	c := hub.NewClient("c", 8)
	s.hub.Add(c)

	w := siteReq(mux, "POST", "/api/sites", `{"name":"Warehouse","description":"  north  "}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		OK   bool    `json:"ok"`
		Site db.Site `json:"site"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Site.Name != "Warehouse" || str(reply.Site.Description) != "north" {
		t.Errorf("stored %+v -- the description should be trimmed", reply.Site)
	}
	if reply.Site.ID == "" {
		t.Error("no id was returned")
	}

	if list := decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed)); len(list) != 3 {
		t.Errorf("%d sites after a create, want 3", len(list))
	}
	assertSitesBroadcast(t, c, true)
	assertAuditAction(t, s, "site.create", true)
}

// TestCreateRefusesADuplicateNameWith409.
func TestCreateRefusesADuplicateNameWith409(t *testing.T) {
	_, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	// CASE-INSENSITIVELY: the column is UNIQUE COLLATE NOCASE, because these are
	// human labels and two differing only in case are a mistake.
	w := siteReq(mux, "POST", "/api/sites", `{"name":"dEpOt"}`, authed)
	if w.Code != 409 {
		t.Errorf("status %d, want 409: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "already exists") {
		t.Errorf("body = %s", w.Body.String())
	}
	// Believability: a distinct name still works, so 409 is about the name.
	if w := siteReq(mux, "POST", "/api/sites", `{"name":"Distinct"}`, authed); w.Code != 200 {
		t.Errorf("a distinct name gave %d", w.Code)
	}
}

// TestABadBodyIs400AndWritesNothing.
func TestABadBodyIs400AndWritesNothing(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	for _, body := range []string{
		`{}`, `{"name":""}`, `{"name":"   "}`,
		`{"name":"X","description":"` + strings.Repeat("y", 257) + `"}`,
		`{"name":"X","place":{"lat":1,"lon":2}}`,
	} {
		if w := siteReq(mux, "POST", "/api/sites", body, authed); w.Code != 400 {
			t.Errorf("body %.40s gave %d, want 400", body, w.Code)
		}
	}
	if list := decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed)); len(list) != 2 {
		t.Errorf("a refused create still wrote: %d sites", len(list))
	}
	assertAuditAction(t, s, "site.create", false)
}

// TestARenameLeavesTheLocationAlone.
//
// The route's half of the absent-versus-null rule. `site-b` carries a full
// location; renaming it must not blank the pin.
func TestARenameLeavesTheLocationAlone(t *testing.T) {
	_, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	w := siteReq(mux, "PUT", "/api/sites/site-b", `{"name":"Annexe 2"}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, s := range decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed)) {
		if s.ID != "site-b" {
			continue
		}
		if s.Name != "Annexe 2" {
			t.Errorf("name = %q", s.Name)
		}
		if s.Lat == nil || *s.Lat != 12.5 || str(s.PlaceName) != "Northtown" {
			t.Errorf("a rename blanked the location: %+v", s)
		}
		if str(s.Description) != "the annexe" {
			t.Errorf("a rename blanked the description: %q", str(s.Description))
		}
		return
	}
	t.Fatal("site-b vanished")
}

// TestAnExplicitNullPlaceClearsTheLocation. The other side of the same rule.
func TestAnExplicitNullPlaceClearsTheLocation(t *testing.T) {
	_, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})

	if w := siteReq(mux, "PUT", "/api/sites/site-b", `{"place":null}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, s := range decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed)) {
		if s.ID != "site-b" {
			continue
		}
		if s.Lat != nil || s.PlaceName != nil {
			t.Errorf("the location survived an explicit clear: %+v", s)
		}
		if str(s.Description) != "the annexe" {
			t.Error("clearing the location took the description with it")
		}
		return
	}
	t.Fatal("site-b vanished")
}

func TestUpdatingAnUnknownSiteIs404(t *testing.T) {
	_, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	if w := siteReq(mux, "PUT", "/api/sites/nope", `{"name":"X"}`, authed); w.Code != 404 {
		t.Errorf("status %d, want 404", w.Code)
	}
	if w := siteReq(mux, "DELETE", "/api/sites/nope", "", authed); w.Code != 404 {
		t.Errorf("delete of an unknown site gave %d, want 404", w.Code)
	}
}

// TestDeletingASiteDetachesItsDevicesFirst.
//
// A device pointing at a site that no longer exists renders a blank chip and is
// unreachable to a site-scoped grant, so the detach has to happen and has to
// happen BEFORE the row goes -- afterwards the site is not there to name.
func TestDeletingASiteDetachesItsDevicesFirst(t *testing.T) {
	s, mux, dir := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	c := hub.NewClient("c", 8)
	s.hub.Add(c)

	// Believability: r1 IS in site-a first, so "detached" below is a real change.
	if got := siteIDsOf(t, dir, "r1"); len(got) != 1 || got[0] != "site-a" {
		t.Fatalf("the fixture does not start with r1 in site-a: %v", got)
	}

	w := siteReq(mux, "DELETE", "/api/sites/site-a", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		OK       bool `json:"ok"`
		Detached int  `json:"detached"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Detached != 1 {
		t.Errorf("detached = %d, want 1", reply.Detached)
	}
	if got := siteIDsOf(t, dir, "r1"); len(got) != 0 {
		t.Errorf("r1 still belongs to the deleted site: %v", got)
	}
	if list := decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed)); len(list) != 1 {
		t.Errorf("%d sites after a delete, want 1", len(list))
	}
	assertSitesBroadcast(t, c, true)
	assertAuditAction(t, s, "site.delete", true)
}

// TestDeletingASiteRemovesItsGrants.
//
// A grant naming a removed site is a permission with no visible subject: it
// appears in no principal's summary and cannot be revoked through the UI.
func TestDeletingASiteRemovesItsGrants(t *testing.T) {
	s, mux, _ := sitesServer(t, &Session{AuthMode: "none", Username: "admin"})
	if err := execOn(t, routerDBDir[s], `
	  -- A TEXT uuid id, matching the live schema; see internal/db/principals.go.
	  -- The roles table exists so grants.role_id can carry its REAL foreign key:
	  -- internal/db/rolewrite.go leans on ON DELETE RESTRICT instead of
	  -- re-checking, so a fixture without the key cannot exercise the refusal.
	  CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL,
	    builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
	  INSERT INTO roles (id, name) VALUES ('manager','manager');
	  CREATE TABLE grants (
	    id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
	    principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
	    scope_type TEXT NOT NULL, scope_id TEXT,
	    role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT);
	  INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
	  VALUES ('user','u-1','site','site-a','manager'), ('user','u-1','site','site-b','manager'),
	         ('user','u-1','router','site-a','manager');`); err != nil {
		t.Fatal(err)
	}

	if w := siteReq(mux, "DELETE", "/api/sites/site-a", "", authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	n, err := s.auditDB.DeleteGrantsForScope("site", "site-a")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d site grants survived the delete", n)
	}
	// The OTHER site's grant, and a ROUTER grant that happens to share the id,
	// both survive -- ids are opaque and nothing stops them colliding.
	if m, _ := s.auditDB.DeleteGrantsForScope("site", "site-b"); m != 1 {
		t.Errorf("another site's grant was removed (%d left)", m)
	}
	if m, _ := s.auditDB.DeleteGrantsForScope("router", "site-a"); m != 1 {
		t.Errorf("a ROUTER grant sharing the id was removed with the site (%d left)", m)
	}
}

// TestOnlyAnAdministratorMayWriteSites, under a REAL resolver.
func TestOnlyAnAdministratorMayWriteSites(t *testing.T) {
	s, _, _ := scopedRoutersServer(t)
	if err := execOn(t, routerDBDir[s], sitesDDL); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	s.registerSites(mux)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/sites", `{"name":"X"}`},
		{"PUT", "/api/sites/site-a", `{"name":"X"}`},
		{"DELETE", "/api/sites/site-a", ""},
	} {
		if w := siteReq(mux, c.method, c.path, c.body, authed); w.Code != 403 {
			t.Errorf("%s %s gave %d, want 403", c.method, c.path, w.Code)
		}
	}
	// Nothing was written by any of them.
	if list := decodeSites(t, siteReq(mux, "GET", "/api/sites", "", authed)); len(list) != 2 {
		t.Errorf("%d sites after three refused writes, want 2", len(list))
	}
}

func assertSitesBroadcast(t *testing.T, c *hub.Client, want bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case b := <-c.Send:
			if strings.Contains(string(b), "sites:update") {
				if !want {
					t.Error("sites:update was broadcast when nothing changed")
				}
				return
			}
		case <-deadline:
			if want {
				t.Error("no sites:update -- another administrator's tab stays stale")
			}
			return
		}
	}
}

func assertAuditAction(t *testing.T, s *Server, action string, want bool) {
	t.Helper()
	page, err := s.auditDB.QueryAuditEvents(db.Query{IncludeApp: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Rows {
		if row.Action == action {
			if !want {
				t.Errorf("%s was recorded for a refused request", action)
			}
			return
		}
	}
	if want {
		t.Errorf("%s was not recorded", action)
	}
}

// str reads a nullable column for comparison, mapping NULL to "".
//
// The four text columns on a site are `*string` because the WIRE distinguishes
// NULL from empty — the live app sends `null` for an unset description and this
// port was sending `""`. A test asserting a value does not care which it was, so
// it says so here once rather than nil-checking at seven call sites.
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
