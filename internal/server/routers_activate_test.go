package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
)

// `POST /api/routers/{id}/activate`.
//
// The route the first-run overlay's Connect button needs, and the reason that
// overlay stayed unmounted. Every case here is a way for a first run to end
// badly: a success reported for a router that does not exist, a teardown of
// every session to re-select the one already selected, or a settings file that
// did not record the choice.

const activateRouters = `[
  {"id":"r-one","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":""},
  {"id":"r-two","label":"Two","host":"198.51.100.2","port":8728,"username":"u","password":""}
]`

func activateServer(t *testing.T, sess *Session, active string) (*Server, *http.ServeMux, string) {
	t.Helper()
	s, mux, dir := usersWriteServer(t, sess, seedUsersJSON)
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(activateRouters), 0o600); err != nil {
		t.Fatal(err)
	}
	settings := `{}`
	if active != "" {
		settings = `{"activeRouterId":"` + active + `"}`
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	s.registerRouterActivate(mux)
	return s, mux, dir
}

func activeID(t *testing.T, s *Server) string {
	t.Helper()
	cfg, err := s.store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cfg["activeRouterId"].(string)
	return id
}

func TestActivateRecordsTheChoice(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")
	w := doJSON(mux, "POST", "/api/routers/r-two/activate", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		OK, Switching, AlreadyActive bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// `switching`, NOT a bare ok. The overlay's guard is
	// `if (!d.ok && !d.switching)`, so this shape is part of the contract.
	if !got.OK || !got.Switching {
		t.Errorf("answered %+v, want ok+switching", got)
	}
	if got.AlreadyActive {
		t.Error("a real switch reported alreadyActive")
	}
	if id := activeID(t, s); id != "r-two" {
		t.Errorf("settings.json records %q, want r-two — the choice was not written", id)
	}
}

// RE-ACTIVATING THE CURRENT ROUTER IS A NO-OP, and says so.
//
// The overlay and the picker both call this. Tearing every session down to
// arrive back where it started would show a switching overlay and a full
// reconnect for a button press that changed nothing.
func TestActivatingTheCurrentRouterChangesNothing(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")
	w := doJSON(mux, "POST", "/api/routers/r-one/activate", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct{ OK, AlreadyActive, Switching bool }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || !got.AlreadyActive {
		t.Errorf("answered %+v, want ok+alreadyActive", got)
	}
	if got.Switching {
		t.Error("re-activating the current router reported a switch")
	}
	// AND NO AUDIT ROW. The live route records nothing on this path, because
	// nothing happened.
	page, err := s.auditDB.QueryAuditEvents(db.Query{
		RouterIDs: []string{"r-one", "r-two"}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Rows {
		if r.Action == "router.activate" {
			t.Error("a no-op activation was recorded in the audit trail")
		}
	}
}

// AN UNKNOWN ROUTER IS REFUSED — a deliberate divergence from the live route,
// which accepts the id and fails asynchronously after answering ok.
func TestActivatingAnUnknownRouterIs404(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")
	w := doJSON(mux, "POST", "/api/routers/r-nope/activate", "", authed)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 — %s", w.Code, w.Body.String())
	}
	if id := activeID(t, s); id != "r-one" {
		t.Errorf("the active router became %q after a refused activation", id)
	}
}

func TestActivateIsAudited(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")
	if w := doJSON(mux, "POST", "/api/routers/r-two/activate", "", authed); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	// `RouterIDs`, NOT `IncludeApp`. The event carries a RouterID, so its scope
	// is `router` — and `QueryAuditEvents` fails closed, returning nothing for a
	// query that names neither. Asking with `IncludeApp` alone reported "no
	// router.activate row" over a row that was there, which reads as a missing
	// audit write and was a wrong question. The same mistake cost a tick on the
	// db.purge test.
	page, err := s.auditDB.QueryAuditEvents(db.Query{RouterIDs: []string{"r-two"}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page.Rows {
		if r.Action == "router.activate" {
			return
		}
	}
	t.Error("no router.activate row — the one action that changes what every " +
		"session is looking at left no trace")
}

func TestActivateRequiresAGlobalAdmin(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "modern", Username: "nobody"}, "r-one")
	w := doJSON(mux, "POST", "/api/routers/r-two/activate", "", authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("status %d, want 403 — %s", w.Code, w.Body.String())
	}
	if id := activeID(t, s); id != "r-one" {
		t.Errorf("a forbidden request still changed the active router to %q", id)
	}
}

func TestActivateRequiresASession(t *testing.T) {
	_, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")
	if w := doJSON(mux, "POST", "/api/routers/r-two/activate", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", w.Code)
	}
}

// WITH NO ACTIVE ROUTER AT ALL — the first-run case this route exists for.
func TestActivatingOnAFreshInstall(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	w := doJSON(mux, "POST", "/api/routers/r-one/activate", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if id := activeID(t, s); id != "r-one" {
		t.Errorf("settings.json records %q after a first-run activation", id)
	}
}

// THE SETTINGS WRITE TOUCHES ONE KEY. `store.Merge` + `SaveSettings` is used so
// an unrelated setting is not dropped — an activation that reset the operator's
// thresholds would be a very expensive way to switch routers.
func TestActivateLeavesOtherSettingsAlone(t *testing.T) {
	s, mux, dir := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")
	const seeded = `{"activeRouterId":"r-one","alertCpuThreshold":55,"notifTitle":"Mine",` +
		`"somethingFuture":"keep me"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := doJSON(mux, "POST", "/api/routers/r-two/activate", "", authed); w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got["activeRouterId"] != "r-two" {
		t.Errorf("activeRouterId = %v", got["activeRouterId"])
	}
	if got["alertCpuThreshold"] != float64(55) {
		t.Errorf("alertCpuThreshold = %v, want 55 — an unrelated setting was rewritten",
			got["alertCpuThreshold"])
	}
	if got["notifTitle"] != "Mine" {
		t.Errorf("notifTitle = %v", got["notifTitle"])
	}
	// AN UNMODELLED SETTINGS KEY IS DROPPED, AND THAT IS CORRECT — the opposite
	// of the rule for routers.json, which is why it is asserted here rather than
	// assumed either way.
	//
	// `store.Merge` keeps only `k in DEFAULTS || ENCRYPTED_FIELDS.includes(k)`,
	// mirroring the live `load()`: "a retired setting left on disk must not
	// reappear in the payload". `routers.json` has the opposite rule because a
	// router record carries fields this port genuinely does not model
	// (`pingTarget`, `connDownThresholdSec`), and dropping those would lose real
	// configuration.
	//
	// This test first asserted the routers rule here and failed. Recorded as an
	// assertion rather than deleted, so the difference between the two files is
	// stated where someone would otherwise have to rediscover it.
	if _, still := got["somethingFuture"]; still {
		t.Error("an unmodelled settings key survived — Merge is supposed to drop it")
	}
	_ = s
}

// ── moveFollowers ───────────────────────────────────────────────────────────

// TestOnlyTheOldDefaultsFollowersMove.
//
// ── THIS IS THE BUG THE PARAMETER EXISTS FOR ───────────────────────────────
//
// `moveFollowers` was first extracted from the DELETE route as
// `moveDefaultFollowers(next)`, moving every connection whose router was not the
// target. That is a much wider move than either caller wants, and it survived
// the whole suite because nothing here drives a WebSocket connection — it was
// caught by reading the diff, not by a test. This is that test.
//
// Three connections: one on the old default, one PINNED to a third router by
// `router:switch`, and one already on the target. Only the first may move.
func TestOnlyTheOldDefaultsFollowersMove(t *testing.T) {
	s, mux, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "r-one")

	mk := func(name, router string) *conn {
		c := hub.NewClient(name, 8)
		s.hub.Add(c)
		s.hub.Join(c, "router-"+router)
		s.hub.Join(c, "router-"+router+"-page-home")
		cn := &conn{srv: s, c: c, sess: &Session{AuthMode: "none"}, routerID: router}
		s.connsMu.Lock()
		s.conns[c] = cn
		s.connsMu.Unlock()
		return cn
	}
	follower := mk("follower", "r-one") // on the old default
	pinned := mk("pinned", "r-three")   // switched to a third router by hand
	already := mk("already", "r-two")   // already where we are going

	if w := doJSON(mux, "POST", "/api/routers/r-two/activate", "", authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	if follower.routerID != "r-two" {
		t.Errorf("the follower is still on %q — it now sits in a room nothing broadcasts to",
			follower.routerID)
	}
	// THE ONE THAT MATTERS. A session pinned elsewhere keeps its own view; the
	// live comment on this route says a global emit "would wrongly flip their
	// selector to a router whose data they aren't receiving", and moving their
	// rooms is worse — it changes what they receive.
	if pinned.routerID != "r-three" {
		t.Errorf("a session PINNED to r-three was dragged to %q by an activation it "+
			"had nothing to do with", pinned.routerID)
	}
	if already.routerID != "r-two" {
		t.Errorf("a connection already on the target moved to %q", already.routerID)
	}

	// AND THE ROOMS FOLLOWED, not just the field. A connection whose id changed
	// while its rooms did not is subscribed to a router it is no longer showing.
	rooms := map[string]bool{}
	for _, r := range follower.c.Rooms() {
		rooms[r] = true
	}
	if !rooms["router-r-two"] {
		t.Errorf("the follower's rooms are %v — it did not join the new router", follower.c.Rooms())
	}
	for r := range rooms {
		if strings.HasPrefix(r, "router-r-one") {
			t.Errorf("the follower is still in %q, so it keeps receiving the old router", r)
		}
	}
	// The pinned session's rooms are untouched.
	for _, r := range pinned.c.Rooms() {
		if strings.HasPrefix(r, "router-r-two") {
			t.Errorf("the pinned session was joined to %q", r)
		}
	}
}

// AN EMPTY `from` MOVES NOTHING.
//
// A first-run install has no active router, so `wasActive` is "" — and a loop
// that treated that as a wildcard would re-room every connection on the very
// first activation.
func TestAnEmptyFromMovesNothing(t *testing.T) {
	s, _, _ := activateServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	c := hub.NewClient("someone", 8)
	s.hub.Add(c)
	s.hub.Join(c, "router-r-three")
	cn := &conn{srv: s, c: c, sess: &Session{AuthMode: "none"}, routerID: "r-three"}
	s.connsMu.Lock()
	s.conns[c] = cn
	s.connsMu.Unlock()

	// A CONNECTION THAT HAS NOT PICKED A ROUTER YET. Its `routerID` is "", which
	// MATCHES an empty `from` — so without the guard this one is swept up and
	// joined to the new router's rooms.
	//
	// That is the whole reason the guard exists, and it is the only case that
	// distinguishes it: every other connection is excluded by `!= from` anyway.
	// Whether such a socket "should" follow the new default is arguable; what is
	// not arguable is that it should not happen as a side effect of an empty
	// string matching an empty string.
	fresh := hub.NewClient("fresh", 8)
	s.hub.Add(fresh)
	blank := &conn{srv: s, c: fresh, sess: &Session{AuthMode: "none"}, routerID: ""}
	s.connsMu.Lock()
	s.conns[fresh] = blank
	s.connsMu.Unlock()

	s.moveFollowers("", "r-one")

	if cn.routerID != "r-three" {
		t.Errorf("an empty `from` moved a connection to %q", cn.routerID)
	}
	if blank.routerID != "" {
		t.Errorf("a connection with no router yet was swept onto %q by an empty `from`",
			blank.routerID)
	}
	if len(blank.c.Rooms()) != 0 {
		t.Errorf("a connection with no router yet was joined to %v", blank.c.Rooms())
	}
}
