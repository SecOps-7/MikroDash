package server

// The notification bell's two write routes, driven through the REAL mux.
//
// Not through the handler functions directly: the limiter and the method/path
// patterns are part of what these routes are, and a test calling the handler
// straight would pass on a route that was never registered. That mistake was
// made once already in this package — `user-notify`'s limiter test asserted on
// limiters it had built itself, and a mutation making two routes share a budget
// survived it.

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

const alertTestDDL = `
-- db.Open reads this before anything else, and reports "is this the right
-- /data?" when it is missing. Version 14 is what the live migrations have
-- reached; the number only has to satisfy the floor Open checks.
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);

-- The trail. Present so these tests can ASSERT what was recorded: without it
-- every write logged "[audit] record failed" and passed, which is the audit
-- package's documented never-throws behaviour and exactly the state in which a
-- route that recorded nothing would look identical to one that recorded
-- correctly.
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  actor_id TEXT, actor_name TEXT NOT NULL, actor_ip TEXT, action TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('app','router')), router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok','denied','failed')), detail TEXT);

CREATE TABLE IF NOT EXISTS alert_events (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  router_id       TEXT    NOT NULL,
  alert_type      TEXT    NOT NULL,
  subject         TEXT,
  detail          TEXT,
  fired_at        INTEGER NOT NULL,
  resolved_at     INTEGER,
  acknowledged_at INTEGER,
  acknowledged_by TEXT
);`

// alertServer builds a server with an alert store, a hub and a primed session.
//
// The session is injected into Auth's cache rather than served by a fake Node:
// what these tests are about is what the ROUTES do once a caller is known, and
// standing up an HTTP validator to answer one question would make every case
// depend on it.
func alertServer(t *testing.T, sess *Session) (*Server, *http.ServeMux, map[string]int64) {
	s, mux, ids, _ := alertServerIn(t, sess)
	return s, mux, ids
}

// alertServerIn is the same, returning the data directory as well. Kept separate
// so the common case does not carry a value it ignores.
func alertServerIn(t *testing.T, sess *Session) (*Server, *http.ServeMux, map[string]int64, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(alertTestDDL); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, row := range []struct {
		key, router string
		resolved    any
	}{
		{"open-a", "r1", nil},
		{"open-a2", "r1", nil},
		{"closed-a", "r1", int64(500)},
		{"open-b", "r2", nil},
	} {
		res, err := h.Exec(`INSERT INTO alert_events
      (router_id, alert_type, subject, detail, fired_at, resolved_at)
      VALUES (?, ?, ?, ?, ?, ?)`,
			row.router, row.key, "subj", "detail", int64(100), row.resolved)
		if err != nil {
			t.Fatal(err)
		}
		ids[row.key], _ = res.LastInsertId()
	}
	_ = h.Close()

	d, err := db.Open(dir)
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

	// A STORE, because `routerNames` resolves the label the payload carries and a
	// nil store makes it null whatever the route does — a mutation dropping the
	// name map from the ack response survived the whole suite until this existed.
	//
	// r2 is deliberately UNLABELLED, so `label || host` is exercised in both
	// directions rather than only the easy one.
	routers := `[{"id":"r1","label":"Office","host":"198.51.100.1","port":8728,` +
		`"username":"u","password":""},` +
		`{"id":"r2","label":"","host":"198.51.100.2","port":8728,` +
		`"username":"u","password":""}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(routers), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{auditDB: d, auth: auth, hub: hub.New(), store: st}
	mux := http.NewServeMux()
	s.registerAlerts(mux)
	return s, mux, ids, dir
}

// execOn runs DDL against the SAME database file on a second handle.
//
// `db.DB` exports no Exec, and it should not: the package's job is to own its
// statements. A test that needs a table the migrations do not create — the RBAC
// graph here — opens its own handle rather than widening that surface.
func execOn(t *testing.T, dir, sqlText string) error {
	t.Helper()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		return err
	}
	defer func() { _ = h.Close() }()
	_, err = h.Exec(sqlText)
	return err
}

func alertPost(mux *http.ServeMux, path, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

const authed = "mikrodash_sid=tok"

// TestBothRoutesAreRegistered.
func TestBothRoutesAreRegistered(t *testing.T) {
	_, mux, _ := alertServer(t, &Session{AuthMode: "none"})
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/alerts/7/ack"},
		{"POST", "/api/alerts/clear-all"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		h, pattern := mux.Handler(req)
		if h == nil || pattern == "" {
			t.Errorf("%s %s matches no route", tc.method, tc.path)
		}
	}
	// `clear-all` must NOT be swallowed by the `{id}` pattern: it would parse as
	// an id, fail, and answer 400 instead of clearing anything.
	req := httptest.NewRequest("POST", "/api/alerts/clear-all", nil)
	_, pattern := mux.Handler(req)
	if strings.Contains(pattern, "{id}") {
		t.Errorf("clear-all matched %q -- the id pattern swallowed it", pattern)
	}
}

// TestAnAnonymousCallerIsRefused. Neither route is reachable without a session.
func TestAnAnonymousCallerIsRefused(t *testing.T) {
	_, mux, ids := alertServer(t, &Session{AuthMode: "none"})
	for _, tc := range []struct{ path, body string }{
		{"/api/alerts/" + itoa64(ids["open-a"]) + "/ack", ""},
		{"/api/alerts/clear-all", `{"routerId":"r1"}`},
	} {
		if w := alertPost(mux, tc.path, tc.body, ""); w.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d without a session, want 401", tc.path, w.Code)
		}
	}
}

// TestAckAcknowledgesAndBroadcasts.
func TestAckAcknowledgesAndBroadcasts(t *testing.T) {
	s, mux, ids := alertServer(t, &Session{AuthMode: "none", Username: "alice"})

	w := alertPost(mux, "/api/alerts/"+itoa64(ids["open-a"])+"/ack", "", authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		OK    bool `json:"ok"`
		Alert struct {
			ID             int64   `json:"id"`
			Label          string  `json:"label"`
			RouterName     *string `json:"routerName"`
			AcknowledgedBy *string `json:"acknowledgedBy"`
			AcknowledgedAt *int64  `json:"acknowledgedAt"`
		} `json:"alert"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Alert.ID != ids["open-a"] {
		t.Fatalf("body = %s", w.Body.String())
	}
	// The response carries the SHAPE, not the raw row: `label` is derived and the
	// column it comes from is `alert_type`. A route returning the store's row
	// would omit it and the bell would render "open_a".
	if body.Alert.Label == "" {
		t.Error("the response has no label -- the bell would render the raw key")
	}
	if body.Alert.AcknowledgedAt == nil || body.Alert.AcknowledgedBy == nil ||
		*body.Alert.AcknowledgedBy != "alice" {
		t.Errorf("acknowledged as %v at %v, want alice with a timestamp",
			body.Alert.AcknowledgedBy, body.Alert.AcknowledgedAt)
	}
	// THE ROUTER NAME. Without it an alert cannot say which router it came from,
	// which is the whole difficulty with three identical update alerts in one
	// bell — and the browser receiving `alert:acked` has only this payload.
	if body.Alert.RouterName == nil || *body.Alert.RouterName != "Office" {
		t.Errorf("routerName = %v, want Office", body.Alert.RouterName)
	}

	// And the store agrees — the response is not the only thing that changed.
	rid, err := s.auditDB.AlertRouterID(ids["open-a"])
	if err != nil || rid != "r1" {
		t.Fatalf("router %q, err %v", rid, err)
	}
}

// TestAnUnlabelledRouterShowsItsHost.
//
// `_r.label || _r.host`: a router nobody named renders as its address rather
// than as an empty name in the bell.
func TestAnUnlabelledRouterShowsItsHost(t *testing.T) {
	s, _, _ := alertServer(t, &Session{AuthMode: "none"})
	if got := s.routerNames("r2")["r2"]; got != "198.51.100.2" {
		t.Errorf("routerNames(r2) = %q, want the host", got)
	}
	if got := s.routerNames("r1")["r1"]; got != "Office" {
		t.Errorf("routerNames(r1) = %q, want the label", got)
	}
	if m := s.routerNames("gone"); len(m) != 0 {
		t.Errorf("a router that is not in the store produced %v", m)
	}
}

// TestAckRejectsABadId. Zero, negative and trailing garbage never reach the
// store: the live route runs `parseInt` then `Number.isFinite(id) && id > 0`.
func TestAckRejectsABadId(t *testing.T) {
	_, mux, _ := alertServer(t, &Session{AuthMode: "none"})
	for _, id := range []string{"0", "-1", "abc", "1x", ""} {
		w := alertPost(mux, "/api/alerts/"+id+"/ack", "", authed)
		if id == "" {
			// `/api/alerts//ack` never reaches the handler: net/http cleans the
			// doubled separator and answers 301 to the tidied path, which then
			// matches nothing. Asserted as "not a success" rather than as a
			// specific code, because WHICH refusal the mux picks is its business
			// and the property here is that an empty id cannot acknowledge
			// anything.
			if w.Code < 300 {
				t.Errorf("an empty id answered %d -- it reached the handler", w.Code)
			}
			continue
		}
		if w.Code != http.StatusBadRequest {
			t.Errorf("id %q answered %d, want 400", id, w.Code)
		}
	}
}

// TestAckOnAnUnknownAlertIs404.
func TestAckOnAnUnknownAlertIs404(t *testing.T) {
	_, mux, ids := alertServer(t, &Session{AuthMode: "none"})
	var max int64
	for _, v := range ids {
		if v > max {
			max = v
		}
	}
	w := alertPost(mux, "/api/alerts/"+itoa64(max+1000)+"/ack", "", authed)
	if w.Code != http.StatusNotFound {
		t.Errorf("answered %d for an alert that does not exist, want 404", w.Code)
	}
}

// TestBothWritesAreRecorded.
//
// ── THE ACK IS RECORDED BEFORE THE 404, DELIBERATELY ────────────────────────
//
// The live route records and only then checks whether the row came back. An
// attempt on an alert that vanished between the scope lookup and the write is
// still an attempt, and the trail is the only place it appears. So a caller
// cannot make a write vanish from the audit by racing it.
func TestBothWritesAreRecorded(t *testing.T) {
	s, mux, ids := alertServer(t, &Session{AuthMode: "none", Username: "alice"})

	if w := alertPost(mux, "/api/alerts/"+itoa64(ids["open-a"])+"/ack", "", authed); w.Code != 200 {
		t.Fatalf("ack: %d %s", w.Code, w.Body.String())
	}
	if w := alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r1"}`, authed); w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body.String())
	}

	// IncludeApp and the router list are how this query is SCOPED, and both are
	// supplied so the read is not silently empty for a reason unrelated to what
	// the routes did.
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r1", "r2"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatalf("read the trail: %v -- these assertions would pass on an empty one", err)
	}
	got := map[string]string{}
	for _, r := range page.Rows {
		rid := ""
		if r.RouterID != nil {
			rid = *r.RouterID
		}
		got[r.Action] = rid
	}
	if len(got) == 0 {
		t.Fatal("the trail is empty, so nothing below distinguishes a route that " +
			"records from one that does not")
	}
	for _, want := range []string{"alert.ack", "alert.clear"} {
		if rid, ok := got[want]; !ok {
			t.Errorf("%q was not recorded", want)
		} else if rid != "r1" {
			t.Errorf("%q recorded router %q, want r1 -- an entry that cannot say which "+
				"router it was about", want, rid)
		}
	}
}

// TestClearAllResolvesOnlyItsOwnRouter.
//
// The router id is the ONLY thing scoping this write, so a route that dropped it
// would clear the whole fleet from one button.
func TestClearAllResolvesOnlyItsOwnRouter(t *testing.T) {
	s, mux, _ := alertServer(t, &Session{AuthMode: "none", Username: "eve"})

	w := alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r1"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		OK    bool `json:"ok"`
		Count int  `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Count != 2 {
		t.Errorf("cleared %d, want the 2 open alerts on r1 (body %s)", body.Count, w.Body.String())
	}

	open, err := s.auditDB.OpenAlerts("r2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Errorf("r2 has %d open alerts after clearing r1, want 1 -- the write was unscoped",
			len(open))
	}
	if open, _ := s.auditDB.OpenAlerts("r1", 0); len(open) != 0 {
		t.Errorf("r1 still has %d open alerts", len(open))
	}

	// A SECOND clear finds nothing and says so, without failing.
	w2 := alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r1"}`, authed)
	if w2.Code != http.StatusOK || !strings.Contains(w2.Body.String(), `"count":0`) {
		t.Errorf("the second clear answered %d %s", w2.Code, w2.Body.String())
	}
}

// TestClearAllRejectsARouterIdItCannotTrust.
//
// The pattern is anchored, so a value with a path separator, a quote or a space
// is refused before RBAC or the store sees it — and an ABSENT one is refused the
// same way, because `String((req.body && req.body.routerId) || ”)` gives "".
func TestClearAllRejectsARouterIdItCannotTrust(t *testing.T) {
	_, mux, _ := alertServer(t, &Session{AuthMode: "none"})
	for _, body := range []string{
		`{}`, `{"routerId":""}`, `{"routerId":"../r1"}`, `{"routerId":"r1 r2"}`,
		`{"routerId":"r1'"}`, `{"routerId":null}`, ``, `not json`,
		`{"routerId":"` + strings.Repeat("a", 65) + `"}`,
	} {
		w := alertPost(mux, "/api/alerts/clear-all", body, authed)
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %q answered %d, want 400", body, w.Code)
		}
	}
	// ...and a 64-character one is accepted, or the length bound is untested in
	// the direction that matters.
	ok := `{"routerId":"` + strings.Repeat("a", 64) + `"}`
	if w := alertPost(mux, "/api/alerts/clear-all", ok, authed); w.Code != http.StatusOK {
		t.Errorf("a 64-character router id answered %d, want 200", w.Code)
	}
}

// TestTheLimiterIsRegistered — 120/minute, on the MUX rather than on a limiter
// this test built.
func TestTheLimiterIsRegistered(t *testing.T) {
	_, mux, _ := alertServer(t, &Session{AuthMode: "none"})
	last := 0
	for i := 1; i <= 130; i++ {
		last = alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r1"}`, authed).Code
		if last == http.StatusTooManyRequests {
			if i <= 120 {
				t.Fatalf("request %d of 120 was rate-limited", i)
			}
			return
		}
	}
	t.Errorf("130 requests all answered %d -- no limiter is registered", last)
}

// TestMayAckShortCircuits — the three cases every permission check here takes.
func TestMayAckShortCircuits(t *testing.T) {
	s := &Server{}
	if s.mayAck(nil, "r1") {
		t.Error("a nil session was permitted")
	}
	if !s.mayAck(&Session{AuthMode: "none"}, "r1") {
		t.Error("auth mode none was refused -- there is no identity to grant anything to")
	}
	// No resolver: the documented install-wide gap, reported at startup. A silent
	// refusal here would lock every operator out of their own bell.
	if !s.mayAck(&Session{AuthMode: "modern", Username: "bob"}, "r1") {
		t.Error("a missing RBAC resolver became a per-request refusal")
	}
}

func itoa64(n int64) string {
	b := [20]byte{}
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
		if n == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// ── THE SCOPE CHECK ─────────────────────────────────────────────────────────
//
// Everything above runs with `AuthMode: "none"`, where `mayAck` short-circuits
// to true. That left the permission check — the security boundary of both
// routes — entirely untested: mutations deleting it from EITHER route survived
// the whole suite. These drive a real resolver instead.

const alertRbacDDL = `
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

INSERT INTO roles (id, name, builtin) VALUES ('acker','acker',0);
-- router:ack IS NOT STORED. Permissions are PROJECTED from the page matrix:
-- writeConfers["dashboard"] = {"router:ack"}, so write access to the dashboard is
-- what confers it. A first draft of this schema invented a role_perms table and
-- granted the permission directly; nothing reads such a table, the grant silently
-- conferred nothing, and the believability assertion below is what said so
-- instead of three tests failing for an unexplained 403.
--
-- builtin is 0 on purpose: a builtin role holds every KNOWN permission
-- structurally, which would make this pass without the projection working at all.
--
-- (No backticks in here: this is a Go raw string and one would end it. That is
-- the second time in this port -- internal/db/alertfeed.go carries the same note.)
INSERT INTO role_pages (role_id, page, access) VALUES ('acker','dashboard','write');
-- u-1 may acknowledge on r1 AND NOWHERE ELSE. r2's alerts are the ones the
-- boundary is about.
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
VALUES ('user','u-1','router','r1','acker');
`

// breakTheRoleGraph removes the table the projection walks, leaving the resolver
// "available" and its answers erroring. `role_pages` is that table, because the
// permission is derived from page rows rather than stored.
const breakTheRoleGraph = `DROP TABLE role_pages`

// scopedAlertServer is alertServer with a resolver that grants router:ack on r1
// only.
func scopedAlertServer(t *testing.T) (*Server, *http.ServeMux, map[string]int64, string) {
	t.Helper()
	s, mux, ids, dir := alertServerIn(t, &Session{AuthMode: "modern", Username: "carol"})

	if err := execOn(t, dir, alertRbacDDL); err != nil {
		t.Fatalf("rbac schema: %v", err)
	}
	// `userIDFor` resolves the session's username through users.json — the grant
	// is held by the user ID, not the name. Without a store this returns "", and
	// `rbac.Can` refuses an empty user id outright: every case below would be a
	// 403 for the wrong reason and the permitted router would fail too.
	users := `[{"id":"u-1","username":"carol","passwordHash":"x","salt":"y","role":"viewer"}]`
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.userIDFor("carol"); got != "u-1" {
		t.Fatalf("userIDFor(carol) = %q, want u-1 -- the grant is held by the id, so "+
			"every case below would refuse for the wrong reason", got)
	}
	s.rbac = rbac.New(s.auditDB, func() []rbac.Router {
		return []rbac.Router{{ID: "r1"}, {ID: "r2"}}
	})
	if !s.rbac.Available() {
		t.Fatal("the resolver reports unavailable, so mayAck would take the documented " +
			"gap and grant everything -- these tests would prove nothing")
	}

	// BELIEVABILITY: the resolver must answer both ways here, or every assertion
	// below holds equally for a resolver that says no to everything.
	if ok, err := s.rbac.Can("u-1", "router:ack", "r1"); err != nil || !ok {
		t.Fatalf("u-1 cannot ack on r1 (%v, %v) -- the grant did not take", ok, err)
	}
	if ok, _ := s.rbac.Can("u-1", "router:ack", "r2"); ok {
		t.Fatal("u-1 can ack on r2 -- the grant is not scoped, so nothing below " +
			"distinguishes a checked route from an unchecked one")
	}
	return s, mux, ids, dir
}

// TestAckIsRefusedOnARouterTheCallerCannotReach.
//
// The caller supplies an alert ID and nothing else, so without resolving the
// owner and checking it, a user restricted to one router could acknowledge
// alerts on every other one.
func TestAckIsRefusedOnARouterTheCallerCannotReach(t *testing.T) {
	s, mux, ids, _ := scopedAlertServer(t)

	w := alertPost(mux, "/api/alerts/"+itoa64(ids["open-b"])+"/ack", "", authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("acking r2's alert answered %d, want 403", w.Code)
	}
	rows, err := s.auditDB.OpenAlerts("r2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].AcknowledgedAt != nil {
		t.Error("r2's alert was acknowledged despite the 403")
	}
}

// TestClearAllIsRefusedOnARouterTheCallerCannotReach.
func TestClearAllIsRefusedOnARouterTheCallerCannotReach(t *testing.T) {
	s, mux, _, _ := scopedAlertServer(t)

	w := alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r2"}`, authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("clearing r2 answered %d, want 403", w.Code)
	}
	if rows, _ := s.auditDB.OpenAlerts("r2", 0); len(rows) != 1 {
		t.Errorf("r2 has %d open alerts after the refusal, want 1", len(rows))
	}
}

// TestAResolverErrorIsNotAPermission.
//
// `mayAck` treats an unanswerable question as no. Granting on error would turn
// one broken query into fleet-wide acknowledge rights — the opposite of the
// "resolver unavailable" case, which is install-wide and reported at startup.
func TestAResolverErrorIsNotAPermission(t *testing.T) {
	s, _, _, dir := scopedAlertServer(t)
	if err := execOn(t, dir, breakTheRoleGraph); err != nil {
		t.Fatal(err)
	}
	if s.mayAck(&Session{AuthMode: "modern", Username: "carol"}, "r1") {
		t.Error("a resolver error granted the permission")
	}
}

// TestAckBroadcastsToItsOwnRouterOnly.
//
// Two people looking at the same alert should not each have to acknowledge it —
// but "everyone on that router" is not "everyone". A broadcast to the wrong room
// tells browsers watching a different router to mark an alert they cannot see.
func TestAckBroadcastsToItsOwnRouterOnly(t *testing.T) {
	s, mux, ids := alertServer(t, &Session{AuthMode: "none", Username: "alice"})

	watcher := hub.NewClient("w1", 8)
	other := hub.NewClient("w2", 8)
	s.hub.Add(watcher)
	s.hub.Add(other)
	s.hub.Join(watcher, "router-r1")
	s.hub.Join(other, "router-r2")

	if w := alertPost(mux, "/api/alerts/"+itoa64(ids["open-a"])+"/ack", "", authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	select {
	case b := <-watcher.Send:
		if !strings.Contains(string(b), "alert:acked") {
			t.Errorf("r1's watcher received %s", b)
		}
	case <-time.After(time.Second):
		t.Fatal("r1's watcher received nothing -- the broadcast never reached its own room")
	}
	select {
	case b := <-other.Send:
		t.Errorf("r2's watcher received %s -- the broadcast was not scoped to one router", b)
	default:
	}
}

// TestClearAllIsSilentWhenNothingChanged.
//
// A clear that found an empty list must not tell every browser to re-render, and
// an `alerts:cleared-all` with no ids is indistinguishable at the receiver from
// one whose ids it does not hold.
func TestClearAllIsSilentWhenNothingChanged(t *testing.T) {
	s, mux, _ := alertServer(t, &Session{AuthMode: "none", Username: "eve"})

	watcher := hub.NewClient("w1", 8)
	s.hub.Add(watcher)
	s.hub.Join(watcher, "router-r1")

	if w := alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r1"}`, authed); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case b := <-watcher.Send:
		if !strings.Contains(string(b), "alerts:cleared-all") {
			t.Errorf("the first clear emitted %s", b)
		}
	case <-time.After(time.Second):
		t.Fatal("the first clear emitted nothing, so the silence below proves nothing")
	}

	if w := alertPost(mux, "/api/alerts/clear-all", `{"routerId":"r1"}`, authed); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	select {
	case b := <-watcher.Send:
		t.Errorf("a clear that cleared nothing still emitted %s", b)
	default:
	}
}

// TestAnUnknownAlertRecordsNothing.
//
// The 404 comes BEFORE the audit entry, so a probe for alert ids cannot fill the
// trail. (The 404 AFTER the write is a different case and IS recorded: by then
// the caller has passed the scope check on a real router.)
func TestAnUnknownAlertRecordsNothing(t *testing.T) {
	s, mux, ids := alertServer(t, &Session{AuthMode: "none", Username: "alice"})
	var max int64
	for _, v := range ids {
		if v > max {
			max = v
		}
	}
	if w := alertPost(mux, "/api/alerts/"+itoa64(max+1000)+"/ack", "", authed); w.Code != 404 {
		t.Fatalf("status %d", w.Code)
	}
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r1", "r2"}, IncludeApp: true, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 0 {
		t.Errorf("a probe for a nonexistent alert recorded %d entries", len(page.Rows))
	}
}

// TestARefusedRequestAnswersOnce.
//
// One refusal, one body. A handler that wrote 401 and then carried on would
// answer 401 with TWO JSON objects concatenated — which still reads as 401 to a
// status check, and breaks any client that parses the body.
func TestARefusedRequestAnswersOnce(t *testing.T) {
	_, mux, ids := alertServer(t, &Session{AuthMode: "none"})
	w := alertPost(mux, "/api/alerts/"+itoa64(ids["open-a"])+"/ack", "", "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
	var v any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Errorf("the refusal body is not one JSON object: %q (%v)", w.Body.String(), err)
	}
}

// ── THE CONNECT EMIT ────────────────────────────────────────────────────────

// TestSendOpenAlertsCarriesBothFeeds.
//
// Without this the bell starts empty on every load and fills only as new alerts
// happen — the "empty again after a refresh while the database holds open
// alerts" problem the live emit exists to solve.
//
// It is a SEND, not a broadcast: this is one browser's opening state, and
// broadcasting it would reset the panel of everybody else already on that
// router, discarding any alert they had acknowledged since their own connect.
func TestSendOpenAlertsCarriesBothFeeds(t *testing.T) {
	s, _, _, dir := alertServerIn(t, &Session{AuthMode: "none"})

	// ── THE FIXTURE IS BUILT FOR THE THINGS THAT CAN GO WRONG ────────────────
	//
	// A first version resolved r2's only open alert and then looped over the
	// OPEN feed to check the router name — a loop over nothing. Three mutations
	// survived on that: no name map, a year-long recent window, and the wrong
	// row limit. Each needed a row the fixture did not have.
	now := time.Now().UnixMilli()
	rows := []string{}
	rows = append(rows, sqlAlert("r2", "resolved-recently", now-2*3600*1000, now-3600*1000))
	// And 60 more inside the window, so the 50-row limit is distinguishable from
	// the open feed's 200.
	for i := 0; i < 60; i++ {
		rows = append(rows, sqlAlert("r2", "bulk-"+itoa64(int64(i)),
			now-2*3600*1000, now-int64(i)-1000))
	}
	if err := execOn(t, dir, strings.Join(rows, "\n")); err != nil {
		t.Fatal(err)
	}

	me := hub.NewClient("me", 8)
	other := hub.NewClient("other", 8)
	s.hub.Add(me)
	s.hub.Add(other)
	s.hub.Join(me, "router-r2")
	s.hub.Join(other, "router-r2")

	cn := &conn{srv: s, c: me, sess: &Session{AuthMode: "none"}}
	cn.sendOpenAlerts("r2")

	select {
	case b := <-me.Send:
		var env struct {
			Event string `json:"event"`
			Data  struct {
				RouterID string `json:"routerId"`
				Open     []struct {
					RouterName *string `json:"routerName"`
					Label      string  `json:"label"`
				} `json:"open"`
				Recent []struct {
					AlertType  string `json:"alertType"`
					ResolvedAt *int64 `json:"resolvedAt"`
				} `json:"recent"`
			} `json:"data"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("payload %s: %v", b, err)
		}
		if env.Event != "alerts:open" {
			t.Fatalf("event %q", env.Event)
		}
		if env.Data.RouterID != "r2" {
			t.Errorf("routerId %q, want r2", env.Data.RouterID)
		}
		// BOTH feeds carry rows, or a payload that dropped one would look correct.
		if len(env.Data.Open) == 0 {
			t.Fatal("the open feed is empty -- every assertion over it below is a loop " +
				"over nothing, which is how three mutations survived the first version")
		}
		if len(env.Data.Recent) == 0 {
			t.Fatal("the recent feed is empty")
		}
		// THE LIMIT is the recent feed's 50, not the open feed's 200. 62 rows are
		// eligible.
		if len(env.Data.Recent) != 50 {
			t.Errorf("the recent feed has %d rows, want the 50-row default",
				len(env.Data.Recent))
		}
		// THE WINDOW is 24 hours. The row resolved two days ago is eligible for
		// every wider window and for none of the right one.
		for _, r := range env.Data.Recent {
			if r.ResolvedAt == nil {
				t.Error("an OPEN alert is in the recent feed")
			}
		}
		// The rows are the PAYLOAD SHAPE, not store rows: r2 is unlabelled, so
		// its name is the host, and `label` is derived from `alert_type`.
		for _, r := range env.Data.Open {
			if r.RouterName == nil || *r.RouterName != "198.51.100.2" {
				t.Errorf("routerName %v, want the unlabelled router's host", r.RouterName)
			}
			if r.Label == "" {
				t.Error("a row has no label -- the bell would render the raw key")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was sent")
	}

	// AND NOBODY ELSE. The other client is in the same room.
	select {
	case b := <-other.Send:
		t.Errorf("a second client in the same room received %s -- this is a Send, not a "+
			"Broadcast, and it would reset their panel", b)
	default:
	}
}

// TestTheRecentWindowIsTwentyFourHours.
//
// ── ITS OWN FIXTURE, AND THAT IS THE POINT ──────────────────────────────────
//
// This started as one assertion inside the test above, and it could not fail:
// that fixture carries 60 rows resolved within the last two hours to exercise
// the 50-row limit, and the recent feed is ordered newest-RESOLVED first. A row
// resolved two days ago never reaches the top 50 however wide the window is, so
// a mutation widening it to a YEAR survived.
//
// Two properties, two fixtures. The limit needs more rows than the cap; the
// window needs fewer, so the old row is visible when it should not be.
func TestTheRecentWindowIsTwentyFourHours(t *testing.T) {
	s, _, _, dir := alertServerIn(t, &Session{AuthMode: "none"})

	now := time.Now().UnixMilli()
	day := int64(24 * 3600 * 1000)
	if err := execOn(t, dir, strings.Join([]string{
		sqlAlert("r2", "resolved-recently", now-2*3600*1000, now-3600*1000),
		sqlAlert("r2", "resolved-two-days-ago", now-3*day, now-2*day),
	}, "\n")); err != nil {
		t.Fatal(err)
	}

	me := hub.NewClient("me", 8)
	s.hub.Add(me)
	cn := &conn{srv: s, c: me, sess: &Session{AuthMode: "none"}}
	cn.sendOpenAlerts("r2")

	var env struct {
		Data struct {
			Recent []struct {
				AlertType string `json:"alertType"`
			} `json:"recent"`
		} `json:"data"`
	}
	select {
	case b := <-me.Send:
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("nothing was sent")
	}

	var sawRecent, sawOld bool
	for _, r := range env.Data.Recent {
		switch r.AlertType {
		case "resolved-recently":
			sawRecent = true
		case "resolved-two-days-ago":
			sawOld = true
		}
	}
	if !sawRecent {
		t.Error("a row resolved an hour ago is missing -- the window excludes everything, " +
			"so the assertion below proves nothing")
	}
	if sawOld {
		t.Error("a row resolved 48 hours ago is in the feed -- the window is 24 hours")
	}
}

// sqlAlert is one INSERT, for fixtures that need rows at chosen instants. The
// store stamps `Date.now()`, so a row two days old cannot be made through it.
func sqlAlert(router, typ string, fired, resolved int64) string {
	return "INSERT INTO alert_events (router_id, alert_type, subject, detail, fired_at, " +
		"resolved_at) VALUES ('" + router + "','" + typ + "','s','d'," +
		itoa64(fired) + "," + itoa64(resolved) + ");"
}

// TestTheConnectEmitNamesTheRouterBeingJoined.
//
// A SOURCE check, in the manner of `TestTheBackgroundCollectorCountIsRecorded`,
// and it is here because the CALL SITE is not otherwise reachable from a unit
// test: `selectRouter` acquires a live router session first. A mutation passing
// the wrong identifier survived every behavioural test in this file, because
// they all call `sendOpenAlerts` directly.
//
// What it asserts is narrow and true: inside `selectRouter`, the emit is handed
// the SAME identifier as the room join, and it happens before `router:switched`
// — which the surrounding comment requires to be last.
func TestTheConnectEmitNamesTheRouterBeingJoined(t *testing.T) {
	b, err := os.ReadFile("ws.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (cn *conn) selectRouter(")
	if i < 0 {
		t.Fatal("selectRouter is gone -- this check is stale, not passing")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	join := strings.Index(body, `cn.srv.hub.Join(cn.c, "router-"+id)`)
	emit := strings.Index(body, "cn.sendOpenAlerts(id)")
	switched := strings.Index(body, `"router:switched"`)
	if join < 0 {
		t.Fatal("selectRouter no longer joins router-<id>")
	}
	if emit < 0 {
		t.Fatal("selectRouter does not call sendOpenAlerts(id) -- the bell would open " +
			"empty on every load, or would be told about the wrong router")
	}
	if switched < 0 {
		t.Fatal("selectRouter no longer sends router:switched")
	}
	if !(join < emit && emit < switched) {
		t.Errorf("the order is join=%d emit=%d switched=%d; the emit must follow the "+
			"join and precede router:switched, which has to stay last", join, emit, switched)
	}
}

// TestSendOpenAlertsSurvivesAnUnreadableTable.
//
// A failure here costs the bell its history. Taking the router switch down with
// it would turn a cosmetic problem into an unusable app, which is why the live
// side wraps this in try/catch and warns.
func TestSendOpenAlertsSurvivesAnUnreadableTable(t *testing.T) {
	s, _, _ := alertServer(t, &Session{AuthMode: "none"})
	me := hub.NewClient("me", 8)
	s.hub.Add(me)

	// No store at all is the same shape as an unopenable one.
	cn := &conn{srv: &Server{hub: s.hub}, c: me, sess: &Session{AuthMode: "none"}}
	cn.sendOpenAlerts("r1") // must not panic

	select {
	case b := <-me.Send:
		t.Errorf("a server with no alert store sent %s", b)
	default:
	}
}
