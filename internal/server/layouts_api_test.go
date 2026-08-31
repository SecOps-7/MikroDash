package server

// The two saved layouts. The storage and the position validator are tested in
// their own packages; what is here is the ROUTES — the permission questions,
// the fallbacks and the merge.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/store"

	_ "modernc.org/sqlite"
)

func layoutReq(t *testing.T, h http.Handler, method, url, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body == "" {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Cookie", "mikrodash_sid="+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestLayoutsNeedASessionAndAPermission.
//
// The fixture grants nothing, so every route must refuse — 401 with no cookie
// and 403 with one. The 403 is the interesting half: a saved layout is a
// per-user preference, and answering it to a principal with no dashboard and no
// topology access would be a small disclosure on every one of them.
func TestLayoutsNeedASessionAndAPermission(t *testing.T) {
	h, token := signedInServer(t, "a-password-for-layouts")

	for _, c := range []struct{ method, url, body string }{
		{"GET", "/api/dashboard-layout", ""},
		{"POST", "/api/dashboard-layout", `{"cards":[]}`},
		{"GET", "/api/topology-layout?routerId=r-A", ""},
		{"POST", "/api/topology-layout", `{"routerId":"r-A","positions":{}}`},
	} {
		if rec := layoutReq(t, h, c.method, c.url, c.body, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with no cookie answered %d, want 401", c.method, c.url, rec.Code)
		}
		// The fixture's user holds no grants, so the grant graph refuses.
		if rec := layoutReq(t, h, c.method, c.url, c.body, token); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s answered %d for a principal with no grants, want 403",
				c.method, c.url, rec.Code)
		}
	}
}

// TestTheTopologyRouteChecksBeforeItValidates.
//
// #108: these routes "were a cross-router probe — any authenticated session
// could confirm a router's existence and read its saved node positions". The
// order matters as much as the check: a route that validated the id first would
// answer differently for a WELL-FORMED id it may not see and a MALFORMED one,
// and that difference is the probe.
//
// A principal with no grants must get the SAME 403 either way.
func TestTheTopologyRouteChecksBeforeItValidates(t *testing.T) {
	h, token := signedInServer(t, "another-password-for-layouts")

	wellFormed := layoutReq(t, h, "GET", "/api/topology-layout?routerId=r-A", "", token)
	malformed := layoutReq(t, h, "GET", "/api/topology-layout?routerId=../etc", "", token)
	if wellFormed.Code != malformed.Code {
		t.Errorf("a well-formed router id answered %d and a malformed one %d. The difference "+
			"tells an unprivileged caller which ids are real, which is the cross-router probe "+
			"#108 closed", wellFormed.Code, malformed.Code)
	}
	if wellFormed.Code != http.StatusForbidden {
		t.Errorf("answered %d, want 403", wellFormed.Code)
	}
}

// TestDashboardLayoutFallsBackToTheSharedRow.
//
// "No layout of their own yet — fall back to the shared one so the client's
// localStorage cache is refreshed rather than left stale from a previous user."
// Answering null would leave the previous user's arrangement on screen.
func TestDashboardLayoutFallsBackToTheSharedRow(t *testing.T) {
	h, token, d := grantedServer(t, "a-third-layout-password")

	// No row at all: null, and NOT an error.
	rec := layoutReq(t, h, "GET", "/api/dashboard-layout", "", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("answered %d with no layout stored, want 200", rec.Code)
	}
	if got := trimJSON(rec.Body.Bytes()); got != "null" {
		t.Errorf("with nothing stored the answer was %s, want null", got)
	}

	// A SHARED row and no personal one: the shared row comes back.
	if err := d.SetLayout("_shared", "dashboard", map[string]any{"cards": []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	rec = layoutReq(t, h, "GET", "/api/dashboard-layout", "", token)
	if !bytes.Contains(rec.Body.Bytes(), []byte("shared")) {
		t.Errorf("the shared row was not used as the fallback: %s", rec.Body.String())
	}

	// A PERSONAL row wins over the shared one.
	if rec := layoutReq(t, h, "POST", "/api/dashboard-layout",
		`{"cards":["mine"]}`, token); rec.Code != http.StatusOK {
		t.Fatalf("save answered %d: %s", rec.Code, rec.Body.String())
	}
	rec = layoutReq(t, h, "GET", "/api/dashboard-layout", "", token)
	if !bytes.Contains(rec.Body.Bytes(), []byte("mine")) ||
		bytes.Contains(rec.Body.Bytes(), []byte("shared")) {
		t.Errorf("the personal row did not win over the shared one: %s", rec.Body.String())
	}
}

// TestDashboardSaveRequiresACardsArrayAndStoresNothingElse.
func TestDashboardSaveRequiresACardsArrayAndStoresNothingElse(t *testing.T) {
	h, token, _ := grantedServer(t, "a-fourth-layout-password")

	for _, body := range []string{
		`{}`, `{"cards":null}`, `{"cards":"a"}`, `{"cards":{"0":"a"}}`, `{"cards":7}`, `not json`,
	} {
		if rec := layoutReq(t, h, "POST", "/api/dashboard-layout", body, token); rec.Code != http.StatusBadRequest {
			t.Errorf("body %s answered %d, want 400", body, rec.Code)
		}
	}

	// EXTRA FIELDS ARE DROPPED. The live route writes `{ cards: body.cards }`,
	// so a port storing the whole body would persist whatever a caller sent
	// into a blob the dashboard later reads back.
	if rec := layoutReq(t, h, "POST", "/api/dashboard-layout",
		`{"cards":["a"],"evil":"payload"}`, token); rec.Code != http.StatusOK {
		t.Fatalf("a valid save answered %d", rec.Code)
	}
	rec := layoutReq(t, h, "GET", "/api/dashboard-layout", "", token)
	if bytes.Contains(rec.Body.Bytes(), []byte("evil")) {
		t.Errorf("an extra field was stored: %s", rec.Body.String())
	}
}

// TestTopologySaveMergesAndResets.
//
// "a save for one router must never discard another router's layout", and a
// reset posts `{}` — which DELETES the key rather than storing an empty object,
// so the next read falls through to the computed layout.
func TestTopologySaveMergesAndResets(t *testing.T) {
	h, token, _ := grantedServer(t, "a-fifth-layout-password")

	save := func(rid, positions string) {
		t.Helper()
		body := `{"routerId":"` + rid + `","positions":` + positions + `}`
		if rec := layoutReq(t, h, "POST", "/api/topology-layout", body, token); rec.Code != http.StatusOK {
			t.Fatalf("save for %s answered %d: %s", rid, rec.Code, rec.Body.String())
		}
	}
	get := func(rid string) string {
		t.Helper()
		rec := layoutReq(t, h, "GET", "/api/topology-layout?routerId="+rid, "", token)
		if rec.Code != http.StatusOK {
			t.Fatalf("get for %s answered %d", rid, rec.Code)
		}
		return rec.Body.String()
	}

	save("r-A", `{"AA:BB:CC:DD:EE:FF":{"x":1,"y":2}}`)
	save("r-B", `{"11:22:33:44:55:66":{"x":3,"y":4}}`)

	if !bytes.Contains([]byte(get("r-A")), []byte("AA:BB:CC:DD:EE:FF")) {
		t.Errorf("r-A's layout is gone after saving r-B: %s", get("r-A"))
	}
	if !bytes.Contains([]byte(get("r-B")), []byte("11:22:33:44:55:66")) {
		t.Errorf("r-B's layout did not save: %s", get("r-B"))
	}

	// A RESET removes r-A and leaves r-B alone.
	save("r-A", `{}`)
	if bytes.Contains([]byte(get("r-A")), []byte("AA:BB")) {
		t.Errorf("a reset did not clear r-A: %s", get("r-A"))
	}
	if !bytes.Contains([]byte(get("r-B")), []byte("11:22:33:44:55:66")) {
		t.Errorf("resetting r-A discarded r-B: %s", get("r-B"))
	}
	// An unknown router answers an EMPTY MAP, not null: the page then draws its
	// computed layout rather than treating the response as a failure.
	if got := get("r-lonely"); trimJSON([]byte(got)) != `{"positions":{}}` {
		t.Errorf("an unknown router answered %s, want an empty positions map", got)
	}
}

// TestTopologySaveRefusesAMalformedMap.
//
// `cleanPositions` answers null and the caller must treat that as 400 — its own
// header says so, "rather than as 'no positions'". Treating it as empty would
// turn every malformed save into a silent wipe.
func TestTopologySaveRefusesAMalformedMap(t *testing.T) {
	h, token, _ := grantedServer(t, "a-sixth-layout-password")

	if rec := layoutReq(t, h, "POST", "/api/topology-layout",
		`{"routerId":"r-A","positions":{"AA:BB:CC:DD:EE:FF":{"x":1,"y":2}}}`,
		token); rec.Code != http.StatusOK {
		t.Fatalf("the good save answered %d", rec.Code)
	}

	for _, positions := range []string{
		`"nope"`, `[{"x":1,"y":2}]`, `{"a/b":{"x":1,"y":2}}`, `{"a":5}`,
		`{"a":{"x":"left","y":1}}`, `{"constructor":{"x":1,"y":2}}`,
	} {
		body := `{"routerId":"r-A","positions":` + positions + `}`
		if rec := layoutReq(t, h, "POST", "/api/topology-layout", body, token); rec.Code != http.StatusBadRequest {
			t.Errorf("positions %s answered %d, want 400", positions, rec.Code)
		}
	}
	// ...AND THE GOOD LAYOUT SURVIVED every one of those refusals. That is the
	// property the 400 exists for: a refusal that had wiped the row would still
	// have answered 400.
	rec := layoutReq(t, h, "GET", "/api/topology-layout?routerId=r-A", "", token)
	if !bytes.Contains(rec.Body.Bytes(), []byte("AA:BB:CC:DD:EE:FF")) {
		t.Errorf("a refused save wiped the stored layout: %s", rec.Body.String())
	}
}

func trimJSON(b []byte) string { return string(bytes.TrimSpace(b)) }

// layoutDDL is the layout table plus the grant graph the two permission checks
// walk. `navTestDDL` has only the first; these routes need both.
// (No backticks in this comment: it sits inside a Go raw string.)
const layoutDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);
CREATE TABLE user_layouts (
  user_id    TEXT NOT NULL,
  kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology','nav')),
  data       TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, kind)
);
CREATE TABLE roles (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
CREATE TABLE role_pages (role_id TEXT NOT NULL, page TEXT NOT NULL, access TEXT NOT NULL);
-- THE FOREIGN KEY IS NOT DECORATION. schema-audit refuses a grants fixture
-- without it, because internal/db/rolewrite.go DeleteRole relies on
-- ON DELETE RESTRICT instead of re-checking in Go -- so a fixture omitting the
-- key cannot exercise the refusal at all. That check was added earlier the same
-- day this DDL was written, and caught it.
CREATE TABLE grants (
  id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
  scope_type TEXT NOT NULL, scope_id TEXT,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT);
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
-- The audit trail. It was missing, and its absence showed only as a logged
-- "record failed: no such table" while every test still passed -- so nothing
-- written to it was ever compared. That is what let the login actor be wrong.
CREATE TABLE audit_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          INTEGER NOT NULL,
  actor_id    TEXT, actor_name TEXT, actor_ip TEXT,
  action      TEXT NOT NULL,
  scope       TEXT, router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome     TEXT, detail TEXT);
INSERT INTO roles (id, name) VALUES ('viewer', 'Read only');
INSERT INTO role_pages (role_id, page, access) VALUES
  ('viewer', 'dashboard', 'read'), ('viewer', 'topology', 'read'),
  ('viewer', 'router', 'read');
`

// grantedServer is signedInServer with ROUTERS and a GRANT, so the permission
// checks pass and the routes can be exercised rather than only refused.
//
// The grant is GLOBAL and read-only: enough for both pages, and deliberately not
// admin, so a route that quietly required more than the live one would fail here
// rather than pass on an over-privileged fixture.
func grantedServer(t *testing.T, password string) (http.Handler, string, *db.DB) {
	t.Helper()
	st := authFixtureWithRouters(t, password)

	dbDir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dbDir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(layoutDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(
		`INSERT INTO grants (principal_type, principal_id, scope_type, role_id)
		 VALUES ('user', 'u-1', 'global', 'viewer')`); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	navDBPath = filepath.Join(dbDir, "mikrodash.db")

	srv, err := New(st, Options{NodeURL: "", WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	rec := postLogin(handler, "someone", password)
	if rec.Code != http.StatusOK {
		t.Fatalf("the fixture could not sign in: %d", rec.Code)
	}
	token := strings.SplitN(
		strings.TrimPrefix(rec.Header().Get("Set-Cookie"), "mikrodash_sid="), ";", 2)[0]
	return handler, token, d
}

// authFixtureWithRouters is authFixture plus three routers, because
// `CanPageAnywhere` walks the READABLE ROUTER LIST — with an empty routers.json
// it answers false however generous the grants are, and every test here would
// see 403 and prove nothing.
func authFixtureWithRouters(t *testing.T, password string) *store.Store {
	t.Helper()
	st := authFixture(t, password)
	routers := `[{"id":"r-A","label":"A","host":"10.0.0.1"},
	             {"id":"r-B","label":"B","host":"10.0.0.2"},
	             {"id":"r-lonely","label":"C","host":"10.0.0.3"}]`
	if err := os.WriteFile(filepath.Join(st.Dir, "routers.json"), []byte(routers), 0o600); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestPermittedFailsClosed.
//
// The mutation `err == nil && !ok` survived while this rule was inline: no test
// can make the grant graph fail through a route, so a database blip would have
// opened both saved layouts to anybody. Extracted so the arm can be executed.
func TestPermittedFailsClosed(t *testing.T) {
	if permitted(true, errors.New("database is gone")) {
		t.Error("a FAILED grant lookup permitted the request. An error is not a yes")
	}
	if permitted(false, errors.New("database is gone")) {
		t.Error("a failed lookup that also said no permitted the request")
	}
	if permitted(false, nil) {
		t.Error("a successful lookup that said NO permitted the request")
	}
	if !permitted(true, nil) {
		t.Error("a successful yes was refused")
	}
}

// TestTheTopologyCheckIsScopedToTheRouter.
//
// It must be `CanPage(..., routerID)` and not `CanPageAnywhere`. With a GLOBAL
// grant the two agree for every router, which is why the mutation swapping them
// survived — this fixture grants topology on ONE router, where they differ.
//
// #108 again: the weaker check restores the cross-router probe, letting a
// principal read the saved node positions of a router they cannot see.
func TestTheTopologyCheckIsScopedToTheRouter(t *testing.T) {
	h, token, _ := scopedGrantServer(t, "a-scoped-layout-password")

	// The granted router answers.
	if rec := layoutReq(t, h, "GET", "/api/topology-layout?routerId=r-A", "", token); rec.Code != http.StatusOK {
		t.Fatalf("the granted router answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// A router with no grant is REFUSED, not answered with an empty map.
	if rec := layoutReq(t, h, "GET", "/api/topology-layout?routerId=r-B", "", token); rec.Code != http.StatusForbidden {
		t.Errorf("a router this principal cannot read answered %d, want 403. CanPageAnywhere "+
			"would allow it, which is the cross-router probe #108 closed", rec.Code)
	}
	// ...and so is a SAVE for it.
	body := `{"routerId":"r-B","positions":{"AA:BB:CC:DD:EE:FF":{"x":1,"y":2}}}`
	if rec := layoutReq(t, h, "POST", "/api/topology-layout", body, token); rec.Code != http.StatusForbidden {
		t.Errorf("a save for an unreadable router answered %d, want 403", rec.Code)
	}
}

// TestAResetDeletesTheKeyRatherThanStoringAnEmptyMap.
//
// Both answer `{"positions":{}}` through the API, so the difference is only
// visible in the STORED ROW — which is why the mutation survived a route-level
// test. The live code deletes; a port that stored an empty object would grow the
// blob by one key per reset, for ever, in a row that is never pruned.
func TestAResetDeletesTheKeyRatherThanStoringAnEmptyMap(t *testing.T) {
	h, token, d := grantedServer(t, "a-reset-layout-password")

	save := func(rid, positions string) {
		t.Helper()
		body := `{"routerId":"` + rid + `","positions":` + positions + `}`
		if rec := layoutReq(t, h, "POST", "/api/topology-layout", body, token); rec.Code != http.StatusOK {
			t.Fatalf("save answered %d: %s", rec.Code, rec.Body.String())
		}
	}
	save("r-A", `{"AA:BB:CC:DD:EE:FF":{"x":1,"y":2}}`)
	save("r-B", `{"11:22:33:44:55:66":{"x":3,"y":4}}`)
	save("r-A", `{}`)

	// KEYED ON THE USER ID, matching Node. See layoutUser.
	blob, err := d.Layout("u-1", "topology")
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(blob, &row); err != nil {
		t.Fatalf("the stored row is not an object: %s", blob)
	}
	if _, present := row["r-A"]; present {
		t.Errorf("a reset stored an empty map instead of DELETING the key: %s", blob)
	}
	if _, present := row["r-B"]; !present {
		t.Errorf("the reset took another router's layout with it: %s", blob)
	}
}

// scopedGrantServer grants topology on ONE ROUTER rather than globally, which is
// the only shape where `CanPage` and `CanPageAnywhere` give different answers.
func scopedGrantServer(t *testing.T, password string) (http.Handler, string, *db.DB) {
	t.Helper()
	st := authFixtureWithRouters(t, password)

	dbDir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dbDir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(layoutDDL); err != nil {
		t.Fatal(err)
	}
	// router:read on r-A only, plus the pages. Without the router grant the
	// principal reads no routers at all and every answer is 403 for the wrong
	// reason.
	if _, err := h.Exec(
		`INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
		 VALUES ('user', 'u-1', 'router', 'r-A', 'viewer')`); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	srv, err := New(st, Options{NodeURL: "", WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	rec := postLogin(handler, "someone", password)
	if rec.Code != http.StatusOK {
		t.Fatalf("the fixture could not sign in: %d", rec.Code)
	}
	token := strings.SplitN(
		strings.TrimPrefix(rec.Header().Get("Set-Cookie"), "mikrodash_sid="), ";", 2)[0]
	return handler, token, d
}

// citySearchServer grants `devices` at WRITE, which projects to `router:manage`
// and confers NO `system:principals` — the shape that exercises the second arm
// of the city-search guard on its own.
func citySearchServer(t *testing.T, password string) (http.Handler, string, *db.DB) {
	t.Helper()
	st := authFixtureWithRouters(t, password)

	dbDir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dbDir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(layoutDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(
		`INSERT INTO role_pages (role_id, page, access) VALUES ('viewer', 'devices', 'write')`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(
		`INSERT INTO grants (principal_type, principal_id, scope_type, role_id)
		 VALUES ('user', 'u-1', 'global', 'viewer')`); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	navDBPath = filepath.Join(dbDir, "mikrodash.db")

	// NO GeoDir: the gazetteer is unreadable, which is a supported state and the
	// one this harness can reach without shipping tens of megabytes of data.
	srv, err := New(st, Options{NodeURL: "", WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	handler := srv.Handler()
	rec := postLogin(handler, "someone", password)
	if rec.Code != http.StatusOK {
		t.Fatalf("the fixture could not sign in: %d", rec.Code)
	}
	token := strings.SplitN(
		strings.TrimPrefix(rec.Header().Get("Set-Cookie"), "mikrodash_sid="), ";", 2)[0]
	return handler, token, d
}
