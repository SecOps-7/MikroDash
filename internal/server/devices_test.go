package server

// The Devices page's pool lifecycle and payload assembly.

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

	"mikrodash/internal/collect"
	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routers"
	"mikrodash/internal/session"
	"mikrodash/internal/store"
)

func devicesServer(t *testing.T) *Server {
	t.Helper()
	return &Server{hub: hub.New(), devicesWatchers: map[*hub.Client]bool{},
		conns: map[*hub.Client]*conn{}}
}

func devicesConn(s *Server, id string) *conn {
	c := hub.NewClient(id, 8)
	s.hub.Add(c)
	return &conn{srv: s, c: c, sess: &Session{AuthMode: "none"}}
}

// TestThePoolResumesOnTheFirstWatcherAndSuspendsOnTheLast.
//
// Holding a connection to every router for a page nobody has open is the cost
// this whole design exists to avoid, so the count has to be exact at both ends.
func TestThePoolResumesOnTheFirstWatcherAndSuspendsOnTheLast(t *testing.T) {
	s := devicesServer(t)
	a, b := devicesConn(s, "a"), devicesConn(s, "b")

	a.devicesFocus()
	if n := len(s.devicesWatchers); n != 1 {
		t.Fatalf("%d watchers after one focus", n)
	}
	b.devicesFocus()
	if n := len(s.devicesWatchers); n != 2 {
		t.Fatalf("%d watchers after two", n)
	}

	// The FIRST to leave must not suspend: somebody is still looking.
	a.devicesBlur()
	if n := len(s.devicesWatchers); n != 1 {
		t.Errorf("%d watchers after one blur", n)
	}
	b.devicesBlur()
	if n := len(s.devicesWatchers); n != 0 {
		t.Errorf("%d watchers after both left", n)
	}
}

// TestAFocusIsIdempotent.
//
// A browser can focus the same page twice — a reconnect replays it. Counting the
// second as a new watcher would leave the pool running forever, because the
// blurs would never bring the count back to zero.
func TestAFocusIsIdempotent(t *testing.T) {
	s := devicesServer(t)
	a := devicesConn(s, "a")

	a.devicesFocus()
	a.devicesFocus()
	if n := len(s.devicesWatchers); n != 1 {
		t.Errorf("%d watchers after focusing twice on one connection", n)
	}
	a.devicesBlur()
	if n := len(s.devicesWatchers); n != 0 {
		t.Errorf("%d watchers after the blur -- the pool would never suspend", n)
	}
}

// TestABlurFromAConnectionThatNeverFocusedIsHarmless.
//
// `devicesBlur` is called from teardown for EVERY connection, whatever page it
// was on. Treating an absent one as "the last watcher left" would suspend the
// pool while somebody else is still on the page.
func TestABlurFromAConnectionThatNeverFocusedIsHarmless(t *testing.T) {
	s := devicesServer(t)
	watcher, passerby := devicesConn(s, "w"), devicesConn(s, "p")

	watcher.devicesFocus()
	passerby.devicesBlur() // never focused

	if n := len(s.devicesWatchers); n != 1 {
		t.Errorf("%d watchers; the passer-by should have changed nothing", n)
	}
}

// TestTheStatsPayloadIsBuiltPerViewer.
//
// It carries `visible` and `maySeeWanIp`, both resolved for ONE principal, which
// is why it is a Send rather than a Broadcast.
func TestTheStatsPayloadIsBuiltPerViewer(t *testing.T) {
	s := devicesServer(t)

	// AuthMode "none" is unrestricted: nil, NOT an empty map. Empty would mean
	// "may read nothing", and getting these the same way round is the difference
	// between a locked-down user seeing everything and an admin seeing nothing.
	if v := s.visibleRouters(&Session{AuthMode: "none"}); v != nil {
		t.Errorf("auth mode none produced %v, want nil for unrestricted", v)
	}
	// No session at all is the opposite answer.
	v := s.visibleRouters(nil)
	if v == nil {
		t.Error("a nil session produced nil (unrestricted) -- it must read nothing")
	} else if len(v) != 0 {
		t.Errorf("a nil session produced %v", v)
	}
	// A modern session with no resolver takes the documented install-wide gap.
	if v := s.visibleRouters(&Session{AuthMode: "modern", Username: "x"}); v != nil {
		t.Errorf("a missing resolver produced %v, want nil (the reported gap)", v)
	}
}

// TestBuildStatsSourcesSurvivesAnEmptyServer.
//
// Every source is optional: no store, no sessions, no database. A
// Devices page on a fresh install must render an empty fleet rather than panic.
func TestBuildStatsSourcesSurvivesAnEmptyServer(t *testing.T) {
	s := devicesServer(t)
	src := s.buildStatsSources(&Session{AuthMode: "none"}, "")

	if src.Main == nil || src.OpenAlerts == nil || src.Sites == nil {
		t.Fatalf("a nil map reached BuildStats: %+v", src)
	}
	// And it produces a payload rather than panicking.
	if rows := routers.BuildStats(src); len(rows) != 0 {
		t.Errorf("%d rows from an empty server", len(rows))
	}
}

// TestTheStatsMarkOnlyTheViewersRouterActive.
//
// The Devices page drew "active" on every card: `isActive` was presence in
// `Main`, and warm holds put every enabled router there. It is the router this
// connection has selected, and nothing else.
func TestTheStatsMarkOnlyTheViewersRouterActive(t *testing.T) {
	sess := &Session{AuthMode: "none"}
	s, _, _ := routersServer(t, sess, "")
	rows := routers.BuildStats(s.buildStatsSources(sess, "r2"))
	if len(rows) != 2 {
		t.Fatalf("%d row(s), want both fixture routers", len(rows))
	}
	for _, r := range rows {
		if want := r.ID == "r2"; r.IsActive != want {
			t.Errorf("%s: isActive=%v, want %v", r.ID, r.IsActive, want)
		}
	}
}

// ── WITH A REAL FLEET ───────────────────────────────────────────────────────
//
// The tests above run against a server with no store. The ones below need a
// fleet to filter, mask or hold. They once also needed a real overview pool -
// four mutations survived without one - and that pool, with the tests that
// were about it alone, was deleted on 2026-10-01. The two that guarded the
// watcher count now assert the `devices` hold directly, which is what the count
// was ever for.

// devicesServerWithFleet gives the server a store holding three routers: one
// ordinary, one DISABLED, and a third. It was `devicesServerWithPool` and also
// built an overview pool, which was deleted on 2026-10-01; what its callers
// actually use is the fleet.
func devicesServerWithFleet(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	// r1 CARRIES geo.auto.ip, which is a WAN address and the thing the socket
	// payload strips. Without it `TestTheSocketShapeStrips...` SKIPS — and it did,
	// silently, on the run that introduced it. A skipped test is not a gate.
	//
	// 203.0.113.9 is TEST-NET-3, so it is recognisably not anybody's address.
	routersJSON := `[
	  {"id":"r1","label":"One","host":"198.51.100.1","port":8728,"username":"u","password":"",
	   "geo":{"auto":{"ip":"203.0.113.9","cc":"DE"},"place":{"name":"Berlin"}}},
	  {"id":"r2","label":"Two","host":"198.51.100.2","port":8728,"username":"u","password":"","disabled":true},
	  {"id":"r3","label":"Three","host":"198.51.100.3","port":8728,"username":"u","password":""}
	]`
	for name, body := range map[string]string{
		"routers.json": routersJSON, ".secret": "test-secret", "settings.json": `{}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	s := devicesServer(t)
	s.store = st
	return s
}

// heldFleet is `devicesServerWithFleet` with a real session manager and the
// fleet holds switched on, so `syncFleetHolds` actually takes and drops holds.
// The routers are TEST-NET addresses: the sessions dial nothing that answers,
// and a hold is asserted, never a reading.
func heldFleet(t *testing.T) *Server {
	t.Helper()
	s := devicesServerWithFleet(t)
	s.sessions = session.NewManager(s.store, s.hub)
	t.Cleanup(func() { s.sessions.Shutdown() })
	s.holdFleet = true
	return s
}

// TestTheDevicesHoldGoesWithTheLastWatcher.
//
// The `devices` hold runs collectors on EVERY router while it is taken, so it
// must be taken while anybody has the page open and dropped the moment nobody
// does - and not a moment sooner, or the second viewer's cards go blank.
//
// RE-AIMED 2026-10-01 from `TestOnlyTheLastWatcherLeavingSuspendsThePool`. It
// asserted the overview pool's suspend, which was a proxy for exactly this; the
// pool is gone, so the hold is asserted directly. The WARM hold must survive the
// last blur: it is what keeps the router observed when nobody is looking.
func TestTheDevicesHoldGoesWithTheLastWatcher(t *testing.T) {
	s := heldFleet(t)
	a, b := devicesConn(s, "a"), devicesConn(s, "b")

	a.devicesFocus()
	if !hasHold(s.sessions.Held("r1"), "devices") {
		t.Fatalf("r1 is held %v with a watcher on the page, want devices", s.sessions.Held("r1"))
	}
	b.devicesFocus()

	a.devicesBlur()
	if !hasHold(s.sessions.Held("r1"), "devices") {
		t.Error("the devices hold went while b is still watching; b's cards lose their readings")
	}
	b.devicesBlur()
	held := s.sessions.Held("r1")
	if hasHold(held, "devices") {
		t.Error("the devices hold outlived the last watcher: collectors on every router, " +
			"for a page nobody has open")
	}
	if !hasHold(held, "warm") {
		t.Errorf("r1 is held %v after the page closed, want warm: nothing observes it now", held)
	}
}

// TestABlurFromANonWatcherKeepsTheDevicesHold.
//
// `devicesBlur` runs at teardown for EVERY connection, whatever page it was on.
// A passer-by's blur must not be treated as "the last watcher left". (Was
// `TestABlurFromANonWatcherDoesNotSuspendAnEmptyPool`.)
func TestABlurFromANonWatcherKeepsTheDevicesHold(t *testing.T) {
	s := heldFleet(t)
	watcher, passerby := devicesConn(s, "w"), devicesConn(s, "p")

	watcher.devicesFocus()
	passerby.devicesBlur()
	if !hasHold(s.sessions.Held("r1"), "devices") {
		t.Error("a blur from a connection that never focused dropped the devices hold " +
			"while a real watcher is still on the page")
	}
	watcher.devicesBlur()
}

// TestAnRBACErrorIsNotAPermission.
//
// `EffectiveRouterIDs` fails the whole call rather than dropping a router,
// because a partial allow-list is indistinguishable from a smaller legitimate
// one. What this pins is what the CALLER does with that: an empty set, not nil.
func TestAnRBACErrorIsNotAPermission(t *testing.T) {
	s := devicesServerWithFleet(t)

	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(alertTestDDL + alertRbacDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	s.auditDB = d
	s.rbac = rbac.New(d, func() []rbac.Router { return []rbac.Router{{ID: "r1"}} })
	if !s.rbac.Available() {
		t.Fatal("the resolver is unavailable, so the break below changes nothing")
	}
	// THE USER MUST RESOLVE AND MUST HOLD A GRANT, or the query short-circuits
	// before it ever reads the table this test breaks — `Can` returns
	// (false, nil) for an empty user id, and `roleSetsInScope` returns nothing
	// for a user with no grants, so `roleConfers` is never called and NO ERROR
	// occurs. The first version of this test had neither, and the mutation it
	// exists to kill survived it.
	users := `[{"id":"u-1","username":"carol","passwordHash":"x","salt":"y","role":"viewer"}]`
	if err := os.WriteFile(filepath.Join(s.store.Dir, "users.json"), []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.userIDFor("carol"); got != "u-1" {
		t.Fatalf("userIDFor(carol) = %q, want u-1", got)
	}

	// Believability, in BOTH directions: it answers, and it answers YES, so the
	// difference below is the error rather than an absence of permission.
	before, err := s.rbac.EffectiveRouterIDs("u-1", "router:read")
	if err != nil {
		t.Fatalf("the resolver errored before it was broken: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("u-1 can read nothing even before the break, so an empty result " +
			"afterwards would prove nothing")
	}

	if err := execOn(t, dir, breakTheRoleGraph); err != nil {
		t.Fatal(err)
	}
	if _, err := s.rbac.EffectiveRouterIDs("u-1", "router:read"); err == nil {
		t.Fatal("breaking the graph produced no error, so this test cannot tell an " +
			"error from an empty permission set")
	}
	v := s.visibleRouters(&Session{AuthMode: "modern", Username: "carol"})
	if v == nil {
		t.Error("an RBAC error produced nil -- which means UNRESTRICTED, so a broken " +
			"query would show the whole fleet to someone who may read none of it")
	} else if len(v) != 0 {
		t.Errorf("an RBAC error produced %v", v)
	}
}

// ── THE ROUTER LIST ─────────────────────────────────────────────────────────

// TestTheRouterListIsFilteredPerPrincipal.
//
// `routers:update` carries addresses and usernames, so a viewer restricted to
// two routers must not receive the fleet because somebody else made an edit.
func TestTheRouterListIsFilteredPerPrincipal(t *testing.T) {
	s := devicesServerWithFleet(t) // r1, r2 (disabled), r3

	// Unrestricted: everything, including the DISABLED one — the dropdown shows
	// it so an operator can re-enable it.
	all := s.routerListForSocket(&Session{AuthMode: "none"})
	if len(all) != 3 {
		t.Fatalf("an unrestricted principal saw %d routers, want 3", len(all))
	}
	var sawDisabled bool
	for _, r := range all {
		if r["disabled"] == true {
			sawDisabled = true
		}
	}
	if !sawDisabled {
		t.Error("the disabled router is missing -- it must be listed so it can be re-enabled")
	}

	// No session: nothing. Not the fleet.
	if none := s.routerListForSocket(nil); len(none) != 0 {
		t.Errorf("a nil session received %d routers", len(none))
	}
}

// TestThePasswordIsMaskedNeverReal.
//
// ── THIS TEST'S PREMISE CHANGED ON 2026-08-28 ───────────────────────────────
//
// It asserted the field was ABSENT, with the reasoning "a mask that is forgotten
// leaks, an absent field cannot". That is a good argument about the password and
// it was being used to justify dropping eleven OTHER fields with it — which live
// verification then found missing from the payload.
//
// The live app sends `password: '(mask)'`, and the modal needs it: the field is
// blanked on edit with a "leave blank to keep current" placeholder, which is a
// promise about a credential the page has to know EXISTS. So the property is not
// absence, it is that the value is never a real one.
func TestThePasswordIsMaskedNeverReal(t *testing.T) {
	s := devicesServerWithFleet(t)
	list := s.routerListForSocket(&Session{AuthMode: "none"})
	if len(list) == 0 {
		t.Fatal("the list is empty, so the checks below prove nothing")
	}
	for _, r := range list {
		pw, ok := r["password"]
		if !ok {
			t.Error("the password key is absent; the live payload carries it and the modal " +
				"reads it to decide whether a credential is already stored")
			continue
		}
		if pw != store.Mask && pw != "" {
			t.Errorf("the password is %q — it must be the mask or empty, never a value", pw)
		}
	}
	// AND NO CIPHERTEXT EITHER. The stored envelope is base64 and would sail past
	// a check that only looked for the plaintext.
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := s.store.Routers()
	for _, rec := range raw {
		if rec.Encrypted != "" && strings.Contains(string(b), rec.Encrypted) {
			t.Error("the stored ciphertext reached the payload")
		}
	}
}

// TestTheFieldsTheLivePayloadCarriesAreAllThere.
//
// The twelve that were missing until 2026-08-28, named so a failure says which.
// `store.PublicRouters` keeps everything by spreading, so this fails only if
// somebody reintroduces a field list.
func TestTheFieldsTheLivePayloadCarriesAreAllThere(t *testing.T) {
	s := devicesServerWithFleet(t)
	list := s.routerListForSocket(&Session{AuthMode: "none"})
	if len(list) == 0 {
		t.Fatal("the list is empty")
	}
	// Present on every record regardless of what the fixture set, because
	// getPublic masks the one and _normalizeSites fills the other two.
	for _, k := range []string{"password", "siteId", "siteIds"} {
		if _, ok := list[0][k]; !ok {
			t.Errorf("%s is missing from the payload", k)
		}
	}
}

// httpRouterList drives the REAL `GET /api/routers` handler as one principal and
// returns the `routers` array it served.
//
// ── WHY THE HANDLER AND NOT THE PROJECTION ────────────────────────────────
//
// The defect upstream fixed in `a4ac96e` was not a broken strip — the strip was
// correct and had been for months. It was a CALL SITE that did not use it: three
// paths withheld the WAN address and the fourth handed it out. Asserting on
// `routerListForSocket` alone would pass with the route wired to anything at all,
// which is exactly the blindness that let the live bug live.
//
// `SetLocal` is how a test becomes a signed-in principal: in standalone mode it
// IS the authority, so installing it here is the same path a real request takes
// rather than a bypass of one.
func httpRouterList(t *testing.T, s *Server, sess *Session) []map[string]any {
	t.Helper()
	if s.auth == nil {
		s.auth = NewAuth()
	}
	s.auth.SetLocal(func(string) (*Session, bool) { return sess, true })
	t.Cleanup(func() { s.auth.SetLocal(nil) })

	req := httptest.NewRequest(http.MethodGet, "/api/routers", nil)
	req.Header.Set("Cookie", "mikrodash_sid=test-token")
	rec := httptest.NewRecorder()
	s.routersList(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/routers: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Routers []map[string]any `json:"routers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the router list: %v", err)
	}
	if len(body.Routers) == 0 {
		t.Fatal("the route served no routers, so the disclosure assertions prove nothing")
	}
	return body.Routers
}

// TestEveryShapeStripsTheWanAddress.
//
// `geo.auto.ip` is a WAN address. `/api/localcc` withholds it from anyone
// without `system:settings`, and so does `_routersForSocket` — its comment:
// "otherwise adding a location would quietly undo an existing disclosure rule".
//
// ── THIS TEST USED TO ASSERT THE OPPOSITE FOR THE HTTP HALF ────────────────
//
// It was `…AndTheHttpShapeDoesNot`, pinning a live defect this port reproduced
// deliberately: `GET /api/routers` disclosed the address that three other paths
// withheld. The note said "the day upstream fixes it the difference fails here
// rather than drifting", and that is what happened — `a4ac96e`, 2026-08-29,
// found by this port's own payload diff. The port now serves one shape from one
// function, so there is no second projection to disagree.
//
// The mutation guidance is unchanged and still load-bearing: compare against the
// SAME principal, never against an `AuthMode: "none"` session, which may save
// settings and is never stripped either way. The first version of this test did
// that and a mutation making the paths differ survived it.
func TestEveryShapeStripsTheWanAddress(t *testing.T) {
	s := devicesServerWithFleet(t)
	// A principal with no session cannot save settings, so it must not see the
	// address in the socket payload.
	viewer := &Session{Username: "viewer", AuthMode: "modern"}

	wan := func(list []map[string]any) (found bool) {
		for _, r := range list {
			geo, ok := r["geo"].(map[string]any)
			if !ok {
				continue
			}
			auto, ok := geo["auto"].(map[string]any)
			if !ok {
				continue
			}
			if _, has := auto["ip"]; has {
				found = true
			}
		}
		return
	}

	// A FAILURE, not a skip. The fixture is this file's own, so an absent address
	// means somebody removed it — and the first version of this test DID skip,
	// silently, which is the failure mode it exists to prevent.
	unrestricted := s.routerListForSocket(&Session{AuthMode: "none"})
	if !wan(unrestricted) {
		t.Fatal("no router in the fixture carries geo.auto.ip, so this test would prove " +
			"nothing. Put it back in devicesServerWithFleet rather than skipping.")
	}

	// THE SOCKET SHAPE STRIPS IT from a principal who cannot save settings.
	if wan(s.routerListForSocket(viewer)) {
		t.Error("the socket payload disclosed geo.auto.ip to a principal without system:settings")
	}
	// AND SO DOES THE HTTP SHAPE, for the SAME principal — the half that changed.
	// Driven through the real handler rather than the projection function,
	// because "the projection strips" and "the route calls the projection" are
	// separate claims and the defect upstream fixed was the second one: the
	// strip existed and one of four callers did not use it.
	if wan(httpRouterList(t, s, viewer)) {
		t.Error("GET /api/routers disclosed geo.auto.ip to a principal without " +
			"system:settings. That was a live defect this port reproduced on purpose " +
			"until upstream fixed it in a4ac96e; it must not come back.")
	}
	// A principal who CAN save settings keeps it in both shapes.
	if !wan(s.routerListForSocket(&Session{AuthMode: "none"})) {
		t.Error("the socket payload stripped the address from a principal entitled to see it")
	}

	// THE REST OF THE GEO BLOCK SURVIVES. A strip that removed `auto` wholesale,
	// or the whole `geo`, would take the country and the place name with it — and
	// the map draws from those.
	for _, r := range s.routerListForSocket(viewer) {
		geo, ok := r["geo"].(map[string]any)
		if !ok {
			continue
		}
		auto, ok := geo["auto"].(map[string]any)
		if !ok {
			t.Error("the strip removed geo.auto entirely; only `ip` should go")
			continue
		}
		if auto["cc"] != "DE" {
			t.Errorf("the strip took geo.auto.cc with it: %+v", auto)
		}
		if _, ok := geo["place"]; !ok {
			t.Error("the strip took geo.place with it")
		}
	}

	// AND IT IS A COPY — asserted on `stripWanIP` DIRECTLY, not through the two
	// list calls.
	//
	// Going through them proves nothing today: `store.PublicRouters` re-reads the
	// file on every call, so each caller gets fresh maps and a delete in place
	// cannot be observed. A mutation replacing the copy with `delete(auto, "ip")`
	// survived exactly that check.
	//
	// The property is still worth holding: the moment that read is cached, or a
	// caller passes one slice to two principals, an in-place delete strips the
	// address for everybody — including someone entitled to see it. So it is
	// tested where it is true rather than where it happens to be invisible.
	shared := map[string]any{
		"id":  "r1",
		"geo": map[string]any{"auto": map[string]any{"ip": "203.0.113.9", "cc": "DE"}},
	}
	stripped := stripWanIP(shared)
	if _, gone := stripped["geo"].(map[string]any)["auto"].(map[string]any)["ip"]; gone {
		t.Error("stripWanIP did not remove the address")
	}
	orig := shared["geo"].(map[string]any)["auto"].(map[string]any)
	if _, still := orig["ip"]; !still {
		t.Error("stripWanIP mutated its ARGUMENT. These records come from a shared read: " +
			"an in-place delete strips the address for every later caller too, including " +
			"one entitled to see it.")
	}
}

// TestTheListBroadcastIsPerSocket.
//
// One payload per connection, built from that connection's session. A single
// `BroadcastAll` would send one marshalled list to everybody.
func TestTheListBroadcastIsPerSocket(t *testing.T) {
	s := devicesServerWithFleet(t)

	unrestricted := devicesConn(s, "a")
	// A connection whose session may read NOTHING.
	restricted := devicesConn(s, "b")
	restricted.sess = nil

	s.connsMu.Lock()
	s.conns[unrestricted.c] = unrestricted
	s.conns[restricted.c] = restricted
	s.connsMu.Unlock()

	s.broadcastRouterList()

	got := map[string]int{}
	for name, c := range map[string]*conn{"a": unrestricted, "b": restricted} {
		select {
		case raw := <-c.c.Send:
			var env struct {
				Data []map[string]any `json:"data"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got[name] = len(env.Data)
		case <-time.After(time.Second):
			t.Fatalf("%s received nothing", name)
		}
	}
	if got["a"] == 0 {
		t.Error("the unrestricted connection received an empty list")
	}
	if got["b"] != 0 {
		t.Errorf("the restricted connection received %d routers -- the payload is not "+
			"filtered per principal", got["b"])
	}
	if got["a"] == got["b"] {
		t.Error("both connections received the same number of routers, so nothing here " +
			"distinguishes a per-socket build from one broadcast")
	}
}

// ── THE DEVICES PAGE REFRESHES ITSELF ──────────────────────────────────────
//
// The live app runs `setInterval(_emitRouters, 2000)` for as long as the page is
// open. The port sent `routers:stats` ONCE, on focus, and never again — so the
// table froze at whatever the fleet looked like in the instant the page opened.
//
// That is worse than it sounds, because of WHEN it froze: `devicesFocus` has
// just told the background pool to sync, and a pool that has not connected yet
// reports every unwatched router as OFFLINE. The page therefore showed a fleet
// of one online device and stayed that way until it was reopened, which reads as
// a broken pool rather than a missing timer. Found by driving the running app
// with a browser on 2026-08-28, not by any test.
//
// Pinned three ways because each failure is silent and different: the interval
// must exist, it must match the live one, and it must be stoppable.

func TestTheDevicesRefreshMatchesTheLiveInterval(t *testing.T) {
	// `setInterval(_emitRouters, 2000)` — src/index.js, inside the `page:focus`
	// handler's `if (name === 'devices')` branch.
	const live = 2 * time.Second
	if devicesRefresh != live {
		t.Errorf("devicesRefresh = %v, the live app uses %v. The Devices page is entirely live "+
			"numbers; a slower tick is a table that lags and a faster one is extra load on every "+
			"router in the fleet.", devicesRefresh, live)
	}
}

func TestTheDevicesTickStartsAndStops(t *testing.T) {
	cn := &conn{}

	if cn.devicesTick != nil {
		t.Fatal("a fresh connection is already ticking")
	}
	cn.startDevicesTick()
	if cn.devicesTick == nil {
		t.Fatal("startDevicesTick did not start a ticker — the page would freeze on the payload " +
			"it received when it opened")
	}

	// IDEMPOTENT. A browser can send `page:focus` for a page it already has
	// focused; starting a second ticker would leak a goroutine per focus and
	// double the send rate for that viewer.
	first := cn.devicesTick
	cn.startDevicesTick()
	if cn.devicesTick != first {
		t.Error("a second focus replaced the ticker; the first goroutine is now unreachable and " +
			"still sending")
	}

	cn.stopDevicesTick()
	if cn.devicesTick != nil || cn.devicesStop != nil {
		t.Error("stopDevicesTick left state behind")
	}

	// IDEMPOTENT THE OTHER WAY, and this one is not hypothetical: `devicesBlur`
	// is called from `page:blur` AND from teardown, because a browser that closes
	// its tab never sends a blur. A second stop must not close a closed channel,
	// which panics and takes the whole process with it.
	cn.stopDevicesTick()

	// And it must be startable again — navigating away and back is ordinary.
	cn.startDevicesTick()
	if cn.devicesTick == nil {
		t.Error("the page could not be reopened after a blur")
	}
	cn.stopDevicesTick()
}

// TestDevicesBlurStopsTheTick — the wiring, not just the primitives.
//
// `devicesBlur` is what `page:blur` and teardown both call. If it stopped the
// pool but not the ticker, a viewer who navigated away would keep receiving
// payloads for a page they are not looking at, and each one re-syncs the pool —
// so the "suspend when nobody is watching" behaviour would be undone by the
// thing that is supposed to trigger it.
func TestDevicesBlurStopsTheTick(t *testing.T) {
	srv := &Server{devicesWatchers: map[*hub.Client]bool{}}
	cn := &conn{srv: srv}
	cn.startDevicesTick()
	cn.devicesBlur()
	if cn.devicesTick != nil {
		t.Error("devicesBlur left the refresh running. Every tick re-syncs the pool, so this " +
			"would keep the background sessions alive for a page nobody has open — the exact " +
			"cost the suspend/resume pair exists to avoid.")
	}
}

// TestDevicesFocusStartsTheTick — the wiring on the OTHER side.
//
// Written after a mutation run: removing `cn.startDevicesTick()` from
// `devicesFocus` SURVIVED the three tests above, because they drive the
// primitives and the blur path and nothing drove focus. That mutant IS the
// original defect — the page renders once and freezes — so a suite that cannot
// kill it is a suite that would not have caught the bug it was written for.
func TestDevicesFocusStartsTheTick(t *testing.T) {
	srv := &Server{
		hub:             hub.New(),
		devicesWatchers: map[*hub.Client]bool{},
	}
	// No store and no pool: `syncPool` returns early on a nil store and
	// `sendRoutersStats` builds an empty payload. What is under test is the
	// wiring, and both of those are exercised on their own elsewhere.
	//
	// A REAL hub client, though. `hub.Send` dereferences it, so a nil one is a
	// segfault rather than a failed assertion — which is a crash reporting the
	// wrong thing.
	c := hub.NewClient("devices-focus-test", 8)
	srv.hub.Add(c)
	cn := &conn{srv: srv, c: c}
	defer cn.stopDevicesTick()

	cn.devicesFocus()
	if cn.devicesTick == nil {
		t.Fatal("devicesFocus did not start the refresh. The Devices page shows nothing but live " +
			"numbers, so without it the table freezes on the payload it received when the page " +
			"opened — and it freezes at the worst moment, before the background pool has " +
			"connected, so every router nobody is watching reads OFFLINE and stays that way.")
	}
}

// ── THE FRAME THE FIX EXISTS FOR CARRIES THE GAUGES ────────────────────────
//
// The symptom the prime was added for is a card with a green badge over an
// empty CPU, memory and uptime until a collector ticks. Nothing errors and
// nothing logs, so the property has to be asserted where it is visible: the
// row a browser receives.
//
// ── THE COVERAGE IS SPLIT, AND EVERY HALF IS NAMED ─────────────────────────
//
// `session.Manager` has no dial seam - it calls `routeros.Dial` directly - so a
// server test cannot plant a primed session in the manager's live set. So:
//
//	the prime RUNS, on a focus, before the frame   `internal/verify/prime_test.go`
//	the session hands it out when it has nothing   `session.TestSystemOrPrimed...`
//	the stats read ASKS for it                     `TestTheStatsReadAsksForThePrimedReading`
//	a primed reading REACHES the row               here
//
// RE-AIMED 2026-10-01. This drove the reading in through `fillFromSessions` and
// the overview pool's `Background` map. Both are gone; every router is a session
// in `Main`, which reads `SystemOrPrimed`. That is also a fix: `BuildStats`
// always preferred `Main`, so the snapshot's primed fallback had never reached a
// row for any router that had a session - only pool-only rows were ever patched.
func TestAPrimedReadingReachesTheStatsRow(t *testing.T) {
	// What `SystemOrPrimed` returns for a warm session that has been primed:
	// the prime is one `/system/resource/print` and nothing else.
	primed := &collect.SystemPayload{CPULoad: 42, UptimeRaw: "9d9h9m"}

	rows := routers.BuildStats(routers.StatsSources{
		Routers: []routers.StatsRouter{
			{ID: "r1", Label: "One"},
			{ID: "r3", Label: "Three"},
		},
		Main: map[string]routers.MainSession{
			"r1": {Connected: true, Known: true, System: primed},
			"r3": {Connected: true, Known: true, System: primed},
		},
	})
	if len(rows) != 2 {
		t.Fatalf("%d row(s), want 2", len(rows))
	}
	for _, row := range rows {
		if !row.Known || !row.Connected {
			t.Errorf("%s: known=%v connected=%v; a warm session holds this "+
				"router and reports it up, so the card must not draw Offline",
				row.ID, row.Known, row.Connected)
		}
		if row.CPU == nil || *row.CPU != 42 {
			t.Errorf("%s: cpu=%v; the primed reading did not reach the row, "+
				"which is the green badge over a blank gauge all over again",
				row.ID, row.CPU)
		}
	}
}

// TestTheStatsReadAsksForThePrimedReading — the call site, read from the source,
// because the defect it guards is an omission: reading `System().Last()` instead
// of `SystemOrPrimed()` compiles, runs, and quietly blanks every warm router's
// gauges until its collector ticks.
func TestTheStatsReadAsksForThePrimedReading(t *testing.T) {
	b, err := os.ReadFile("devices.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func (s *Server) buildStatsSources(")
	if i < 0 {
		t.Fatal("buildStatsSources is gone; this check is reading nothing")
	}
	body := src[i:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}
	if !strings.Contains(body, "sn.SystemOrPrimed()") {
		t.Error("buildStatsSources does not read SystemOrPrimed(): a warm router's card " +
			"shows a green badge over blank gauges until its collector ticks")
	}
}
