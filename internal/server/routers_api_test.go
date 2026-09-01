package server

// `PUT /api/routers/:id`, through the REAL mux.
//
// The interesting half is the privileged-field strip:
// `rbac.StripPrivilegedRouterFields` has its own unit tests, and these pin that
// the ROUTE calls it — which is the part its own comment says gets forgotten.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/rbac"
	"mikrodash/internal/store"
)

// routerRbacDDL grants carol router:manage on r1 via the page matrix, and NOT
// system:principals. `router:manage` is projected from write access to the
// `devices` page; `system:principals` is global-only and is never granted here.
const routerRbacDDL = `
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT, builtin INTEGER NOT NULL DEFAULT 0);
CREATE TABLE role_pages (role_id TEXT NOT NULL, page TEXT NOT NULL, access TEXT NOT NULL);
-- id IS A TEXT UUID, matching the live schema: grants.id is TEXT PRIMARY KEY
-- and upsertGrant fills it with crypto.randomUUID(). Every fixture here declared
-- INTEGER PRIMARY KEY AUTOINCREMENT until 2026-08-26, which is not the shape on
-- disk -- GrantRow.ID was int64 and scanned happily against all of them while
-- being unable to read a single real row. The default keeps the INSERTs readable.
-- (No backticks in this comment: it sits inside a Go raw string.)
CREATE TABLE grants (
  id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
  scope_type TEXT NOT NULL, scope_id TEXT,
  -- REQUIRED: internal/db/rolewrite.go leans on ON DELETE RESTRICT instead of
  -- re-checking, so a fixture without this key cannot exercise the refusal.
  -- tools/schema-audit.js lists it under RELIED_ON.
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT);
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);

-- builtin 0: a builtin role holds every KNOWN permission structurally, which
-- would grant system:principals too and the strip would never run.
INSERT INTO roles (id, name, builtin) VALUES ('manager','manager',0);
INSERT INTO role_pages (role_id, page, access) VALUES ('manager','devices','write');
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
VALUES ('user','u-1','router','r1','manager');
`

const routersFixture = `[
  {"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":"",
   "siteIds":["site-a"]},
  {"id":"r2","label":"Two","host":"198.51.100.2","port":8728,"username":"u","password":""}
]`

func routersServer(t *testing.T, sess *Session, settingsJSON string) (
	*Server, *http.ServeMux, string,
) {
	t.Helper()
	dir := t.TempDir()
	if settingsJSON == "" {
		settingsJSON = `{}`
	}
	for name, body := range map[string]string{
		"routers.json": routersFixture, "settings.json": settingsJSON,
		".secret": "test-secret",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	dbDir := t.TempDir()
	if err := execOn(t, dbDir, alertTestDDL); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// AN HOUR, NOT A MINUTE. A minute is plenty of wall-clock for one test and is
	// NOT plenty of CPU time when the whole package runs: on 2026-08-26
	// `TestTheLimiterIsRegistered` sent its 130 requests over 61 seconds under
	// load, the cached session expired mid-loop, and every request after that
	// answered 401 — so the limiter never tripped and the test reported "no
	// limiter is registered". Nothing was wrong with the limiter; the suite had
	// simply grown. A TTL that is a function of how busy the machine is makes
	// every auth-dependent test a flake generator.
	auth := NewAuth("", time.Hour)
	auth.cache["tok"] = cached{session: sess, until: time.Now().Add(time.Minute)}

	s := &Server{store: st, auditDB: d, auth: auth, hub: hub.New(),
		devicesWatchers: map[*hub.Client]bool{},
		conns:           map[*hub.Client]*conn{}}
	mux := http.NewServeMux()
	s.registerRouters(mux)
	routerDBDir[s] = dbDir
	return s, mux, dir
}

// routerDBDir remembers where each test server's database lives, so the helpers
// below can open their OWN handle.
//
// `db.DB` exports no Exec and should not: the package owns its statements, and
// `execOn`'s comment already says a test needing a table the migrations do not
// create opens its own connection rather than widening that surface. This is the
// same rule, one level up.
var routerDBDir = map[*Server]string{}

func routerPut(mux *http.ServeMux, id, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("PUT", "/api/routers/"+id, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func routerFile(t *testing.T, dir string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("routers.json is not valid JSON: %v", err)
	}
	return out
}

func routerByID(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	for _, r := range routerFile(t, dir) {
		if r["id"] == id {
			return r
		}
	}
	t.Fatalf("router %s is not in the file", id)
	return nil
}

// TestAnOrdinaryEditIsApplied.
func TestAnOrdinaryEditIsApplied(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	w := routerPut(mux, "r1", `{"label":"Renamed"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := routerByID(t, dir, "r1")["label"]; got != "Renamed" {
		t.Errorf("label = %#v after the edit", got)
	}
}

// TestDisablingTheActiveRouterIsRefused.
//
// Refused BEFORE anything is written, and the message names the remedy rather
// than the rule — an operator who hits this needs to know what to do next.
func TestDisablingTheActiveRouterIsRefused(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"activeRouterId":"r1"}`)

	w := routerPut(mux, "r1", `{"disabled":true}`, authed)
	if w.Code != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Switch to another router") {
		t.Errorf("the refusal does not name the remedy: %s", w.Body.String())
	}
	if got := routerByID(t, dir, "r1")["disabled"]; got == true {
		t.Error("the router was disabled despite the refusal")
	}

	// ...and a DIFFERENT router disables fine, or the check is refusing
	// everything rather than the active one.
	if w := routerPut(mux, "r2", `{"disabled":true}`, authed); w.Code != http.StatusOK {
		t.Fatalf("disabling a non-active router answered %d: %s", w.Code, w.Body.String())
	}
	if got := routerByID(t, dir, "r2")["disabled"]; got != true {
		t.Errorf("r2 disabled = %#v", got)
	}
}

// TestDisablingBroadcastsToItsWatchers.
//
// They are in the router's room and would otherwise sit on a page that never
// updates again.
func TestDisablingBroadcastsToItsWatchers(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	watcher, other := hub.NewClient("w", 8), hub.NewClient("o", 8)
	s.hub.Add(watcher)
	s.hub.Add(other)
	s.hub.Join(watcher, "router-r1")
	s.hub.Join(other, "router-r2")

	if w := routerPut(mux, "r1", `{"disabled":true}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	var sawDisabled bool
	for len(watcher.Send) > 0 {
		if strings.Contains(string(<-watcher.Send), "router:disabled") {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Error("r1's watcher was not told the router was disabled")
	}
	// r2's watcher gets perms:changed (fleet-wide) but NOT router:disabled.
	for len(other.Send) > 0 {
		if b := <-other.Send; strings.Contains(string(b), "router:disabled") {
			t.Errorf("r2's watcher received %s -- the disable notice is per-router", b)
		}
	}
}

// TestAnEditBroadcastsPermsChanged.
//
// A membership change alters who can reach the router, so every browser's cached
// authorization view is stale. Easy to miss: it reads as router config.
func TestAnEditBroadcastsPermsChanged(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	c := hub.NewClient("c", 8)
	s.hub.Add(c) // in NO room: perms:changed is fleet-wide

	if w := routerPut(mux, "r1", `{"label":"x"}`, authed); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	select {
	case b := <-c.Send:
		if !strings.Contains(string(b), "perms:changed") {
			t.Errorf("received %s", b)
		}
	case <-time.After(time.Second):
		t.Error("a client in no room received nothing -- perms:changed is fleet-wide, " +
			"so a room broadcast would leave most browsers with a stale view")
	}
}

// TestAnUnknownRouterIs404.
func TestAnUnknownRouterIs404(t *testing.T) {
	_, mux, _ := routersServer(t, &Session{AuthMode: "none"}, "")
	if w := routerPut(mux, "nope", `{"label":"x"}`, authed); w.Code != http.StatusNotFound {
		t.Errorf("answered %d for a router that does not exist, want 404", w.Code)
	}
}

// TestAnAnonymousCallerCannotEdit.
func TestAnAnonymousCallerCannotEdit(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none"}, "")
	if w := routerPut(mux, "r1", `{"label":"hijacked"}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("answered %d without a session, want 401", w.Code)
	}
	if got := routerByID(t, dir, "r1")["label"]; got == "hijacked" {
		t.Error("the router was edited without a session")
	}
}

// TestTheAuditRowCarriesNoPassword.
//
// `audit.Diff` would mask a field it recognises as a credential — but the
// decrypted plaintext is on the struct, and relying on a name-matching pattern
// is one rename away from writing a router password into a table that is
// deliberately hard to delete from. `routerAuditView` omits it structurally.
func TestTheAuditRowCarriesNoPassword(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	const placeholder = "PLACEHOLDER-not-a-real-credential"
	if w := routerPut(mux, "r1", `{"password":"`+placeholder+`"}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	// ROUTER-SCOPED, so the query needs the router in its allow-list.
	// `auditActions` asks only for app-scope rows and returned nothing — which
	// The believability check caught rather than passing vacuously.
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r1", "r2"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail := ""
	for _, row := range page.Rows {
		if row.Action == "router.update" && row.Detail != nil {
			detail = *row.Detail
		}
	}
	if detail == "" {
		t.Fatal("router.update recorded nothing, so this proves nothing")
	}
	if strings.Contains(detail, placeholder) {
		t.Errorf("the router password reached the audit trail: %s", detail)
	}
}

// ── THE STRIP ───────────────────────────────────────────────────────────────
//
// `rbac.StripPrivilegedRouterFields` is unit-tested on its own. What these pin
// is that the ROUTE calls it — the part its own comment says gets forgotten —
// and that it is reached under a REAL resolver, since `AuthMode: "none"` makes
// `mayManagePrincipals` short-circuit to true and every case above would then
// pass with the strip deleted.

// scopedRoutersServer grants router:manage on r1 through the page matrix, and
// withholds system:principals.
func scopedRoutersServer(t *testing.T) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, mux, dir := routersServer(t, &Session{AuthMode: "modern", Username: "carol"}, "")

	if err := execOn(t, dir, ""); err == nil { // the store dir has no database
		_ = err
	}
	dbDir := t.TempDir()
	if err := execOn(t, dbDir, alertTestDDL+routerRbacDDL); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s.auditDB = d
	// AND THE MAP FOLLOWS THE HANDLE. `routerDBDir` says it remembers where each
	// test server's database lives; swapping `auditDB` without updating it made
	// that false, and a later helper adding a table through the map created it in
	// a database the server had already stopped using.
	routerDBDir[s] = dbDir
	s.rbac = rbac.New(d, func() []rbac.Router {
		return []rbac.Router{{ID: "r1"}, {ID: "r2"}}
	})

	users := `[{"id":"u-1","username":"carol","passwordHash":"x","salt":"y","role":"viewer"}]`
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.userIDFor("carol"); got != "u-1" {
		t.Fatalf("userIDFor(carol) = %q, want u-1", got)
	}

	// BELIEVABILITY, both ways: she may manage r1 and may NOT manage principals.
	// Without the first, every case below is a 403 for the wrong reason; without
	// the second, the strip never runs and the tests pin nothing.
	if !s.mayManageRouter(&Session{AuthMode: "modern", Username: "carol"}, "r1") {
		t.Fatal("carol cannot manage r1 -- the grant did not take")
	}
	if s.mayManagePrincipals(&Session{AuthMode: "modern", Username: "carol"}) {
		t.Fatal("carol CAN manage principals, so the strip would never run")
	}
	return s, mux, dir
}

// TestSiteMembershipIsStrippedFromANonAdministrator.
//
// The escalation: many-to-many made a membership write purely ADDITIVE — a
// repeatable, invisible way to inject a device into any scope, with every site
// id enumerable from an ungated GET /api/sites.
func TestSiteMembershipIsStrippedFromANonAdministrator(t *testing.T) {
	_, mux, dir := scopedRoutersServer(t)

	w := routerPut(mux, "r1", `{"label":"Renamed","siteIds":["site-a","site-INJECTED"]}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s -- the rest of the edit is legitimate and must apply",
			w.Code, w.Body.String())
	}

	got := routerByID(t, dir, "r1")
	// The legitimate half APPLIED. The fields are dropped, not the request
	// refused, and a refusal would also tell the caller which fields are
	// privileged.
	if got["label"] != "Renamed" {
		t.Errorf("label = %#v; the non-privileged part of the edit was lost", got["label"])
	}
	ids, _ := got["siteIds"].([]any)
	for _, id := range ids {
		if id == "site-INJECTED" {
			t.Fatalf("siteIds = %v -- a non-administrator injected the device into a "+
				"site, which is repeatable and invisible", ids)
		}
	}
	if len(ids) != 1 || ids[0] != "site-a" {
		t.Errorf("siteIds = %v, want the untouched [site-a]", ids)
	}
}

// TestTheScalarMirrorIsStrippedToo.
//
// `siteId` alone is enough for the same escalation, so honouring it while
// dropping `siteIds` leaves the hole open through the older field.
func TestTheScalarMirrorIsStrippedToo(t *testing.T) {
	_, mux, dir := scopedRoutersServer(t)

	if w := routerPut(mux, "r1", `{"siteId":"site-INJECTED"}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := routerByID(t, dir, "r1")
	if got["siteId"] == "site-INJECTED" {
		t.Error("the SCALAR mirror got through -- stripping only the plural leaves an " +
			"older client a way in")
	}
	ids, _ := got["siteIds"].([]any)
	if len(ids) != 1 || ids[0] != "site-a" {
		t.Errorf("siteIds = %v, want the untouched [site-a]", ids)
	}
}

// TestAnAdministratorMaySetMembership.
//
// The other direction. Without this the tests above hold equally for a route
// that dropped the fields from everybody, which would break the feature.
func TestAnAdministratorMaySetMembership(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	if w := routerPut(mux, "r1", `{"siteIds":["site-b","site-c"]}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	ids, _ := routerByID(t, dir, "r1")["siteIds"].([]any)
	if len(ids) != 2 || ids[0] != "site-b" {
		t.Errorf("siteIds = %v; an administrator must be able to set membership", ids)
	}
}

// TestAStrippedAttemptIsRecorded.
//
// The live route drops the keys and says nothing. This port records the attempt,
// because the trail is the only place a repeated probe would show.
func TestAStrippedAttemptIsRecorded(t *testing.T) {
	s, mux, _ := scopedRoutersServer(t)

	if w := routerPut(mux, "r1", `{"siteIds":["x"]}`, authed); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r1"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	var detail string
	for _, row := range page.Rows {
		if row.Action == "router.update" && row.Detail != nil {
			detail = *row.Detail
		}
	}
	if detail == "" {
		t.Fatal("router.update recorded nothing")
	}
	if !strings.Contains(detail, "droppedPrivilegedFields") {
		t.Errorf("the dropped fields are not in the trail: %s", detail)
	}
}

// TestManagingOneRouterDoesNotConferAnother.
//
// The gate is `router:manage` on the TARGET. Every scoped test above grants
// carol r1 and then edits r1, so removing the check entirely survived them all:
// nothing asked what happens when she reaches for a router she was not granted.
func TestManagingOneRouterDoesNotConferAnother(t *testing.T) {
	_, mux, dir := scopedRoutersServer(t)

	// Believability: the granted one works, so the refusal below is about the
	// grant rather than about the route refusing everything.
	if w := routerPut(mux, "r1", `{"label":"Allowed"}`, authed); w.Code != http.StatusOK {
		t.Fatalf("the granted router answered %d: %s", w.Code, w.Body.String())
	}

	w := routerPut(mux, "r2", `{"label":"Reached"}`, authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("editing an ungranted router answered %d, want 403", w.Code)
	}
	if got := routerByID(t, dir, "r2")["label"]; got == "Reached" {
		t.Error("a principal who may manage r1 edited r2")
	}
}

// TestAMissingResolverRefusesTheEdit.
//
// The opposite of `mayAck`, deliberately: acknowledging an alert has a worst
// case of a cleared bell, and locking every operator out over an install-wide
// condition would be worse than allowing it. Rewriting a router connection is
// not in that class.
func TestAMissingResolverRefusesTheEdit(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "modern", Username: "carol"}, "")
	// No rbac resolver at all on this server.

	w := routerPut(mux, "r1", `{"label":"Reached"}`, authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("a modern session with NO resolver answered %d, want 403", w.Code)
	}
	if got := routerByID(t, dir, "r1")["label"]; got == "Reached" {
		t.Error("a router was edited with no way to check the caller permission")
	}
}

// ── POST /api/routers ───────────────────────────────────────────────────────

func routerPost(mux *http.ServeMux, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/routers", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestCreatingARouterAppendsIt.
func TestCreatingARouterAppendsIt(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	before := len(routerFile(t, dir))

	w := routerPost(mux, `{"host":"198.51.100.9","label":"New"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	after := routerFile(t, dir)
	if len(after) != before+1 {
		t.Fatalf("%d records, want %d", len(after), before+1)
	}
	if after[len(after)-1]["label"] != "New" {
		t.Errorf("label = %#v", after[len(after)-1]["label"])
	}
}

// TestAHostIsRequired. Checked BEFORE the normaliser, so it is a 400 rather than
// the normaliser's own refusal.
func TestAHostIsRequired(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	before := len(routerFile(t, dir))

	for _, body := range []string{`{}`, `{"host":""}`, `{"host":"   "}`, ``} {
		w := routerPost(mux, body, authed)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q answered %d, want 400", body, w.Code)
		}
	}
	if len(routerFile(t, dir)) != before {
		t.Error("a router was created without a host")
	}
}

// TestAnInvalidHostIsRefused, by the normaliser rather than the route.
func TestAnInvalidHostIsRefused(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	before := len(routerFile(t, dir))

	// A space is not in VALID_HOST.
	if w := routerPost(mux, `{"host":"not a host"}`, authed); w.Code != http.StatusBadRequest {
		t.Errorf("answered %d for an invalid host, want 400", w.Code)
	}
	if len(routerFile(t, dir)) != before {
		t.Error("a router with an invalid host was written")
	}
}

// TestTheCreatedRouterComesBackMasked.
//
// The form shows the field as configured without receiving the value.
func TestTheCreatedRouterComesBackMasked(t *testing.T) {
	_, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	const placeholder = "PLACEHOLDER-not-a-real-credential"
	w := routerPost(mux, `{"host":"198.51.100.30","password":"`+placeholder+`"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), placeholder) {
		t.Errorf("the response carries the credential: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), store.Mask) {
		t.Errorf("the response does not mark the field as configured: %s", w.Body.String())
	}

	// ...and a router with NO password comes back with an empty string, not the
	// mask, or the form would show an unconfigured field as configured.
	w2 := routerPost(mux, `{"host":"198.51.100.31"}`, authed)
	if strings.Contains(w2.Body.String(), store.Mask) {
		t.Errorf("a router with no password came back masked: %s", w2.Body.String())
	}
}

// TestOnlyAnAdministratorMayCreate.
//
// There is no router to scope `router:manage` to — the device does not exist
// yet — so the gate is global.
func TestOnlyAnAdministratorMayCreate(t *testing.T) {
	_, mux, dir := scopedRoutersServer(t) // carol: router:manage on r1, no principals
	before := len(routerFile(t, dir))

	// Believability: she CAN edit the router she manages, so the refusal below is
	// about the create gate rather than about her being unauthenticated.
	if w := routerPut(mux, "r1", `{"label":"Fine"}`, authed); w.Code != http.StatusOK {
		t.Fatalf("carol cannot edit r1 (%d), so this test proves nothing", w.Code)
	}

	w := routerPost(mux, `{"host":"198.51.100.40"}`, authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("a non-administrator created a router: %d %s", w.Code, w.Body.String())
	}
	if len(routerFile(t, dir)) != before {
		t.Error("a router was created by a non-administrator")
	}
}

// ── DELETE /api/routers/:id ─────────────────────────────────────────────────

func routerDelete(mux *http.ServeMux, id, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("DELETE", "/api/routers/"+id, nil)
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// TestRemovingARouterTakesOnlyItsOwnRecord.
func TestRemovingARouterTakesOnlyItsOwnRecord(t *testing.T) {
	_, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	if len(routerFile(t, dir)) != 2 {
		t.Fatalf("the fixture is not two routers")
	}

	if w := routerDelete(mux, "r1", authed); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	after := routerFile(t, dir)
	if len(after) != 1 {
		t.Fatalf("%d records left, want 1", len(after))
	}
	if after[0]["id"] != "r2" {
		t.Errorf("the WRONG router was removed: %v remains", after[0]["id"])
	}
}

// TestRemovingAnUnknownRouterIs404, and records the attempt anyway.
func TestRemovingAnUnknownRouterIs404(t *testing.T) {
	s, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	if w := routerDelete(mux, "nope", authed); w.Code != http.StatusNotFound {
		t.Errorf("answered %d, want 404", w.Code)
	}
	if len(routerFile(t, dir)) != 2 {
		t.Error("the fleet changed on a 404")
	}
	// RECORDED BEFORE THE 404: an attempt on a router that vanished is still an
	// attempt, and the trail is the only place it shows.
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"nope"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, row := range page.Rows {
		if row.Action == "router.delete" {
			saw = true
		}
	}
	if !saw {
		t.Error("the attempt was not recorded -- a probe for router ids leaves no trace")
	}
}

// TestRemovingARouterDoesNotStripTheOthers.
//
// The same hazard `AddRouter` had: this port's Router struct is a subset of the
// record, so filtering a decoded fleet and re-marshalling would drop four fields
// from every survivor.
func TestRemovingARouterDoesNotStripTheOthers(t *testing.T) {
	dir := t.TempDir()
	const seeded = `[{"id":"r1","label":"Gone","host":"198.51.100.1","port":8728,
	    "username":"u","password":""},
	  {"id":"r2","label":"Kept","host":"198.51.100.2","port":8728,"username":"u",
	    "password":"","pingTarget":"192.0.2.9","alertsEnabled":true,
	    "connDownThresholdSec":90,"addedAt":1700000000000,"somethingFuture":"keep me"}]`
	for name, body := range map[string]string{
		"routers.json": seeded, "settings.json": `{}`, ".secret": "test-secret",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := st.RemoveRouter("r1")
	if err != nil || !ok {
		t.Fatalf("remove: %v %v", ok, err)
	}
	all := routerFile(t, dir)
	if len(all) != 1 {
		t.Fatalf("%d records left", len(all))
	}
	for k, want := range map[string]any{
		"pingTarget": "192.0.2.9", "alertsEnabled": true,
		"connDownThresholdSec": float64(90), "addedAt": float64(1700000000000),
		"somethingFuture": "keep me",
	} {
		got, present := all[0][k]
		if !present {
			t.Errorf("%s was STRIPPED from the surviving router", k)
			continue
		}
		if got != want {
			t.Errorf("%s = %#v, want %#v", k, got, want)
		}
	}
}

// TestRemovingTheACTIVERouterPromotesAnother.
//
// And relocates every socket watching it: they are in rooms nothing will
// broadcast to again, so leaving them there is a page that silently stops.
func TestRemovingTheActiveRouterPromotesAnother(t *testing.T) {
	s, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"activeRouterId":"r1"}`)

	watcher := hub.NewClient("w", 8)
	s.hub.Add(watcher)
	s.hub.Join(watcher, "router-r1")
	cn := &conn{srv: s, c: watcher, sess: &Session{AuthMode: "none"}, routerID: "r1"}
	s.connsMu.Lock()
	s.conns[watcher] = cn
	s.connsMu.Unlock()

	if w := routerDelete(mux, "r1", authed); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if cn.routerID != "r2" {
		t.Errorf("the watching connection is still on %q -- it would sit in a room "+
			"nothing broadcasts to again", cn.routerID)
	}
	var inNew bool
	for _, room := range watcher.Rooms() {
		if room == "router-r2" {
			inNew = true
		}
		if strings.HasPrefix(room, "router-r1") {
			t.Errorf("still in %q, a room for a router that no longer exists", room)
		}
	}
	if !inNew {
		t.Error("the connection did not join the promoted router's room")
	}

	// The promotion is PERSISTED, or a restart goes back to the removed router.
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["activeRouterId"] != "r2" {
		t.Errorf("activeRouterId = %#v after promotion, want r2", cfg["activeRouterId"])
	}
}

// TestRemovingTheLastRouterAsksForSetup.
//
// An EMPTY routers:update rides along, so a dropdown built from the last payload
// stops offering a device that is gone.
func TestRemovingTheLastRouterAsksForSetup(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"activeRouterId":"r2"}`)

	c := hub.NewClient("c", 8)
	s.hub.Add(c)
	s.connsMu.Lock()
	s.conns[c] = &conn{srv: s, c: c, sess: &Session{AuthMode: "none"}}
	s.connsMu.Unlock()

	if w := routerDelete(mux, "r1", authed); w.Code != 200 {
		t.Fatalf("first removal: %d", w.Code)
	}
	if w := routerDelete(mux, "r2", authed); w.Code != 200 {
		t.Fatalf("second removal: %d", w.Code)
	}

	var sawSetup, sawEmptyList bool
	for len(c.Send) > 0 {
		msg := string(<-c.Send)
		if strings.Contains(msg, "setup:required") {
			sawSetup = true
		}
		if strings.Contains(msg, `"routers:update"`) && strings.Contains(msg, `"data":[]`) {
			sawEmptyList = true
		}
	}
	if !sawSetup {
		t.Error("no setup:required after the last router was removed")
	}
	if !sawEmptyList {
		t.Error("no EMPTY routers:update -- a dropdown built from the last payload would " +
			"keep offering a router that is gone")
	}
}

// TestRemovingARouterPurgesWhatOnlyMadeSenseWithIt.
//
// The route calls three separate removals, and NOTHING asserted any of them
// until this existed: mutations deleting the grant removal, the schedule removal
// and the whole time-series purge all survived the suite. Each is a different
// kind of leftover — a permission with no subject, a live outbound email loop,
// and rows a Reports query would still join.
func TestRemovingARouterPurgesWhatOnlyMadeSenseWithIt(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	// Seed the three things, for BOTH routers, so "r2 kept its rows" is a real
	// assertion rather than a vacuous one.
	seedPurgeables(t, s)

	for _, rid := range []string{"r1", "r2"} {
		if countRows(t, s, "grants", "scope_id", rid) == 0 ||
			countRows(t, s, "report_schedules", "router_id", rid) == 0 ||
			countRows(t, s, "ping_samples", "router_id", rid) == 0 {
			t.Fatalf("%s is not fully seeded", rid)
		}
	}

	if w := routerDelete(mux, "r1", authed); w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	for name, table := range map[string]string{
		"grants": "scope_id", "report_schedules": "router_id", "ping_samples": "router_id",
	} {
		if n := countRows(t, s, name, table, "r1"); n != 0 {
			t.Errorf("%s still has %d rows for the removed router", name, n)
		}
		if n := countRows(t, s, name, table, "r2"); n == 0 {
			t.Errorf("%s lost the OTHER router's rows", name)
		}
	}

	// AND THE BACKUP SURVIVES. A restore point is not time-series data.
	if n := countRows(t, s, "config_backups", "router_id", "r1"); n == 0 {
		t.Error("removing the router destroyed its RESTORE POINTS")
	}
}

func seedPurgeables(t *testing.T, s *Server) {
	t.Helper()
	// THE REAL COLUMNS, checked by tools/schema-audit.js. These were four-column
	// stand-ins with a scratch `v` until 2026-08-26 -- convenient, and not the
	// tables the app has. The purge deletes by router_id and would pass either
	// way; the reason to match anyway is that a fixture which is not the schema
	// is not evidence, which this repo learned three times in one day.
	// (No backticks in these SQL comments: they sit inside Go raw strings.)
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ping_samples (id INTEGER PRIMARY KEY,
		   router_id TEXT NOT NULL, target TEXT NOT NULL, rtt_ms REAL,
		   loss_pct REAL NOT NULL, ts INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS traffic_samples (id INTEGER PRIMARY KEY,
		   router_id TEXT NOT NULL, interface TEXT NOT NULL, rx_mbps REAL NOT NULL,
		   tx_mbps REAL NOT NULL, ts INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS bandwidth_usage (id INTEGER PRIMARY KEY,
		   router_id TEXT NOT NULL, interface TEXT NOT NULL, rx_mb REAL NOT NULL,
		   tx_mb REAL NOT NULL, ts INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS connectivity_events (id INTEGER PRIMARY KEY,
		   router_id TEXT NOT NULL, connected INTEGER NOT NULL, ts INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS report_schedules (id TEXT PRIMARY KEY,
		   router_id TEXT NOT NULL, name TEXT NOT NULL, sections TEXT NOT NULL,
		   interface TEXT, aggregate TEXT NOT NULL, recipients TEXT NOT NULL,
		   frequency TEXT NOT NULL, send_hour INTEGER NOT NULL, enabled INTEGER NOT NULL,
		   disabled_reason TEXT, created_by TEXT,
		   created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		// A TEXT uuid id, as the live migration leaves it -- this was the SECOND
		// grants fixture in this file, and the one the manual sweep missed.
		`CREATE TABLE IF NOT EXISTS roles (id TEXT PRIMARY KEY, name TEXT NOT NULL,
		   builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0)`,
		`INSERT OR IGNORE INTO roles (id, name) VALUES ('role','role')`,
		`CREATE TABLE IF NOT EXISTS grants (id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
		   principal_type TEXT NOT NULL, principal_id TEXT NOT NULL, role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
		   role TEXT, scope_type TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
		   created_at INTEGER NOT NULL, created_by TEXT)`,
		`CREATE TABLE IF NOT EXISTS config_backups (id INTEGER PRIMARY KEY,
		   router_id TEXT NOT NULL, taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
		   source TEXT NOT NULL, actor TEXT, stem TEXT, dir TEXT, fingerprint TEXT,
		   rsc_bytes INTEGER NOT NULL, backup_bytes INTEGER NOT NULL, model TEXT,
		   serial TEXT, os_version TEXT, ms INTEGER NOT NULL, pruned_at INTEGER, error TEXT)`,
	}
	for _, rid := range []string{"r1", "r2"} {
		stmts = append(stmts,
			`INSERT INTO ping_samples (router_id, target, loss_pct, ts)
			   VALUES ('`+rid+`', '1.1.1.1', 0, 1)`,
			`INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts)
			   VALUES ('`+rid+`', 'ether1', 1, 1, 1)`,
			`INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts)
			   VALUES ('`+rid+`', 'ether1', 1, 1, 1)`,
			`INSERT INTO connectivity_events (router_id, connected, ts)
			   VALUES ('`+rid+`', 1, 1)`)
		stmts = append(stmts,
			`INSERT INTO report_schedules (id, router_id, name, sections, aggregate, recipients,
			   frequency, send_hour, enabled, created_at, updated_at)
			   VALUES ('s-`+rid+`','`+rid+`','n','[]','sum','[]','daily',8,1,1,1)`,
			`INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id,
			   created_at) VALUES ('user','u-1','router','`+rid+`','role',1)`,
			`INSERT INTO config_backups (router_id, taken_at, outcome, source, stem,
			   rsc_bytes, backup_bytes, ms)
			   VALUES ('`+rid+`', 1, 'ok', 'manual', 'keep', 1, 1, 1)`)
	}
	if err := execOn(t, routerDBDir[s], strings.Join(stmts, ";\n")+";"); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func countRows(t *testing.T, s *Server, table, col, id string) int {
	t.Helper()
	h, err := sql.Open("sqlite", filepath.Join(routerDBDir[s], "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = h.Close() }()
	var n int
	if err := h.QueryRow(
		`SELECT COUNT(*) FROM `+table+` WHERE `+col+` = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestOnlySomeoneWhoManagesTheRouterMayRemoveIt.
//
// Every case above runs as `AuthMode: "none"`, where the check short-circuits to
// true — so removing it entirely survived them all.
func TestOnlySomeoneWhoManagesTheRouterMayRemoveIt(t *testing.T) {
	_, mux, dir := scopedRoutersServer(t) // carol manages r1 and not r2
	before := len(routerFile(t, dir))

	w := routerDelete(mux, "r2", authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("removing an unmanaged router answered %d, want 403", w.Code)
	}
	if len(routerFile(t, dir)) != before {
		t.Error("an unmanaged router was removed")
	}

	// ...and the one she DOES manage goes, or the check is refusing everything.
	if w := routerDelete(mux, "r1", authed); w.Code != http.StatusOK {
		t.Fatalf("removing the managed router answered %d: %s", w.Code, w.Body.String())
	}
}

// ── A WRONGLY-TYPED FIELD MUST NOT ERASE THE FLEET ─────────────────────────
//
// `PUT /api/routers/:id` decodes into `map[string]any` and `store.UpdateRouter`
// writes what it is given. `Router` has typed fields and `Routers()` decodes the
// whole file in ONE Unmarshal, so a string where a bool belongs does not spoil
// one record — it returns ZERO routers, and every page, session and collector
// reads that call.
//
// Measured 2026-08-29 before the fix: `{"disabled":"false"}` left routers.json
// holding the string, after which `Routers()` answered 0 routers and
// `json: cannot unmarshal string into Go struct field Router.disabled of type
// bool`. An authorised operator could take the whole fleet out with one
// well-formed request, and only a hand-edit of the file brought it back.
//
// THIS TEST EXISTS AT THE ROUTE, not at the coercion. The store-level tests pass
// a patch through `CoerceRouterPatch` explicitly, so they stay green if the
// route stops calling it — which a mutation confirmed: deleting the call from
// `routers_api.go` left the whole server suite passing.
func TestAWronglyTypedPatchDoesNotEraseTheFleet(t *testing.T) {
	for _, body := range []string{
		`{"disabled":"false"}`,
		`{"disabled":"true"}`,
		`{"tls":"false"}`,
		`{"tlsInsecure":"true"}`,
		`{"alertsEnabled":"1"}`,
		`{"port":"8729"}`,
		`{"bwDownMbps":"500"}`,
		`{"connDownThresholdSec":"45"}`,
	} {
		t.Run(body, func(t *testing.T) {
			s, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

			before, _ := s.store.Routers()
			if len(before) == 0 {
				t.Fatal("the fixture fleet is empty — this test would prove nothing")
			}

			if w := routerPut(mux, "r1", body, authed); w.Code >= 500 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			after, problems := s.store.Routers()
			if len(after) != len(before) {
				t.Errorf("the fleet went from %d routers to %d after %s — routers.json is "+
					"undecodable, so EVERY router vanished, not just r1. problems=%v",
					len(before), len(after), body, problems)
			}
			for _, p := range problems {
				t.Errorf("after %s: %v", body, p)
			}
			_ = dir
		})
	}
}

// AND THE ACTIVE-ROUTER GUARD MUST SEE A STRING TOO.
//
// The guard asserted `.(bool)` on the RAW value, so `{"disabled":"true"}` failed
// the assertion and was never refused — while the live app, coercing with `!!`
// first, refuses it. Disabling the router somebody is looking at is exactly the
// case the guard exists for.
func TestDisablingTheActiveRouterIsRefusedEvenAsAString(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"activeRouterId":"r1"}`)
	if !s.isActiveRouter("r1") {
		t.Fatal("r1 is not the active router — this test would prove nothing")
	}
	w := routerPut(mux, "r1", `{"disabled":"true"}`, authed)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400 — the string \"true\" disabled the active router: %s",
			w.Code, w.Body.String())
	}
}
