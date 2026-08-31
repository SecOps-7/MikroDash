package server

// `POST /api/users/setup`, driven through the REAL mux.
//
// ── THE FIRST ASSERTION IS THAT IT IS NOT REGISTERED ────────────────────────
//
// Same shape as `auth_login_test.go`, for a sharper reason. `users.js` caches
// the file and never re-reads it, so with Node running BOTH processes would see
// zero users and BOTH would mint a first administrator — and the second would be
// invisible to whichever process cached first.
//
// EVERY PASSWORD HERE IS INVENTED IN THIS FILE. These files transplant into the
// public MikroDash repository at cutover.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/rbac"
	"mikrodash/internal/store"
)

// freshInstall is a /data with NO users.json at all — the state this route
// exists for, and the only one in which it does anything.
func freshInstall(t *testing.T, nodeURL string) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		".secret": "test-secret", "settings.json": `{}`, "routers.json": `[]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Options{NodeURL: nodeURL, WebDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), dir
}

func postSetup(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/users/setup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "10.0.0.9:1234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func setupBody(user, pass string) string {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	return string(b)
}

// TestSetupIsNotServedWhileNodeRuns. The route must reach the proxy, which in a
// test has nowhere to go and answers 502 — that 502 is the PROOF it was proxied.
// A 200 here would mean Go claimed an install Node still believes is unclaimed.
func TestSetupIsNotServedWhileNodeRuns(t *testing.T) {
	h, dir := freshInstall(t, "http://127.0.0.1:1")
	rec := postSetup(h, setupBody("ann", "an-invented-password"))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST /api/users/setup answered %d with a Node configured, want 502 (proxied). "+
			"users.js caches users.json, so both processes would see zero users and both "+
			"would mint a first administrator.", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "users.json")); !os.IsNotExist(err) {
		t.Error("a users.json was written while Node is the authority")
	}
}

// TestSetupCreatesTheFirstAdministrator — the whole path, standalone.
func TestSetupCreatesTheFirstAdministrator(t *testing.T) {
	h, dir := freshInstall(t, "")

	rec := postSetup(h, setupBody("ann", "an-invented-password"))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK   bool           `json:"ok"`
		User map[string]any `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Errorf("ok = false: %s", rec.Body.String())
	}
	if out.User["username"] != "ann" {
		t.Errorf("username = %v, want ann", out.User["username"])
	}
	// ADMIN, hard-coded by the route. An install whose only account is a viewer
	// has nobody who can promote them.
	if out.User["role"] != "admin" {
		t.Errorf("role = %v, want admin — the first account must be able to use the app",
			out.User["role"])
	}
	// THE RESPONSE IS A PUBLIC VIEW. This body goes over the wire to a browser.
	for _, secret := range []string{"passwordHash", "salt"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the response carries %s: %s", secret, rec.Body.String())
		}
	}

	// And the account really works: the password verifies against what was
	// written, through the same call a login makes.
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	users, err := st.Users()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("%d users on disk, want 1", len(users))
	}
	if !store.VerifyPassword(users[0], "an-invented-password") {
		t.Error("the password written by setup does not verify — the account cannot log in")
	}
	if store.VerifyPassword(users[0], "a-different-invented-password") {
		t.Error("a wrong password verified")
	}
}

// TestSetupClosesForEver — the second call is refused, whatever it asks for.
func TestSetupClosesForEver(t *testing.T) {
	h, _ := freshInstall(t, "")

	if rec := postSetup(h, setupBody("ann", "an-invented-password")); rec.Code != http.StatusOK {
		t.Fatalf("the first call answered %d: %s", rec.Code, rec.Body.String())
	}
	rec := postSetup(h, setupBody("attacker", "another-invented-password"))
	if rec.Code != http.StatusConflict {
		t.Errorf("the second call answered %d, want 409 — this route claims the instance", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Setup already complete") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

// TestSetupRefusesBadInput, in the LIVE ORDER.
//
// A request with a bad username AND a short password is told about the
// USERNAME: the checks are sequential upstream, and a port validating the
// password first would give different feedback on the one screen an operator
// cannot get past. The anchoring cases matter most — an unanchored regex accepts
// anything CONTAINING a valid run, so "bad name" would pass.
func TestSetupRefusesBadInput(t *testing.T) {
	for _, c := range []struct {
		why, user, pass, want string
	}{
		{"a space", "bad name", "an-invented-password", "Invalid username"},
		{"empty", "", "an-invented-password", "Invalid username"},
		{"a slash", "a/b", "an-invented-password", "Invalid username"},
		{"an at sign", "a@b", "an-invented-password", "Invalid username"},
		{"a newline in the middle", "a\nb", "an-invented-password", "Invalid username"},
		{"a trailing newline, which an unanchored $ would allow", "ann\n",
			"an-invented-password", "Invalid username"},
		{"65 characters", strings.Repeat("a", 65), "an-invented-password", "Invalid username"},
		{"non-ASCII", "Ünal", "an-invented-password", "Invalid username"},
		{"three characters", "ann", "abc", "Password too short"},
		{"an empty password", "ann", "", "Password too short"},
		// THE ORDER. Both are wrong; the username is what comes back.
		{"both wrong reports the USERNAME", "bad name", "abc", "Invalid username"},
	} {
		t.Run(c.why, func(t *testing.T) {
			h, dir := freshInstall(t, "")
			rec := postSetup(h, setupBody(c.user, c.pass))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("answered %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.want) {
				t.Errorf("body = %s, want %q", rec.Body.String(), c.want)
			}
			// AND NOTHING WAS WRITTEN. A refusal that still created the file
			// would close the route against the operator who typed a space.
			if _, err := os.Stat(filepath.Join(dir, "users.json")); !os.IsNotExist(err) {
				t.Error("a refused request created users.json, closing setup for ever")
			}
		})
	}

	// The shapes that MUST be accepted, or a legitimate name is refused.
	for _, user := range []string{"ann", "a", "A_b.c-d", strings.Repeat("a", 64), "007"} {
		h, _ := freshInstall(t, "")
		if rec := postSetup(h, setupBody(user, "an-invented-password")); rec.Code != http.StatusOK {
			t.Errorf("username %q was refused: %d %s", user, rec.Code, rec.Body.String())
		}
	}
	// Exactly four characters is the floor, not one above it.
	h, _ := freshInstall(t, "")
	if rec := postSetup(h, setupBody("ann", "abcd")); rec.Code != http.StatusOK {
		t.Errorf("a four-character password was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// TestOnlyOneAdministratorSurvivesAConcurrentRush.
//
// This is what the live `_setupClaimed` latch exists for, and the reason this
// port uses a MUTEX instead: the JavaScript flag closes a gap around an `await`,
// while Go handlers genuinely run at once. A flag alone would be weaker here,
// not equivalent.
//
// Without the lock the failure is not a race that occasionally loses a request —
// it is TWO ADMINISTRATORS on an install meant to have one, the second being
// whoever else was pointing a script at the port.
func TestOnlyOneAdministratorSurvivesAConcurrentRush(t *testing.T) {
	h, dir := freshInstall(t, "")

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	// Released together, so the requests actually overlap rather than queueing.
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = postSetup(h, setupBody("user", "an-invented-password")).Code
		}(i)
	}
	close(start)
	wg.Wait()

	// TWO KINDS OF REFUSAL, and both are correct. The limiter admits five a
	// minute, so a rush of eight is turned away partly by the LATCH (409) and
	// partly by the LIMITER (429) — which is worth seeing rather than designing
	// around: in production the limiter is what keeps the concurrency window
	// small enough for the latch to be the last line rather than the only one.
	//
	// The first version of this test asserted seven conflicts and failed on
	// three 429s. The limiter was doing its job; the assertion was wrong.
	ok, conflict, limited, other := 0, 0, 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		case http.StatusTooManyRequests:
			limited++
		default:
			other++
		}
	}
	if ok != 1 {
		t.Errorf("%d requests succeeded, want exactly 1. Every extra one is an administrator "+
			"nobody meant to create. codes=%v", ok, codes)
	}
	if other != 0 {
		t.Errorf("%d requests answered something other than 200, 409 or 429: %v", other, codes)
	}
	// AT LEAST ONE 409, or this proved only that the limiter works. Five requests
	// reach the handler and one succeeds, so four must be refused BY THE LATCH.
	if conflict < 1 {
		t.Errorf("no request was refused by the latch (%v). With the limiter admitting %d and "+
			"one succeeding, the rest must hit the zero-users check.", codes, 5)
	}
	if ok+conflict+limited != n {
		t.Errorf("codes do not add up: %v", codes)
	}

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	users, err := st.Users()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Errorf("%d users on disk, want 1 — the file itself is the proof, not the status codes",
			len(users))
	}
}

// TestTheFirstAdministratorIsNotLockedOut.
//
// The live comment: "Without this the very first administrator of a fresh
// install holds no grants, and every guard refuses them — locked out of their own
// instance the moment setup completes."
//
// `freshInstall` builds a server with NO database, so the grants cannot be
// written and the route logs that. What is asserted here is the half that must
// hold regardless — the ACCOUNT is created and usable, because deleting it would
// leave an install nobody can claim, the route having already closed — plus the
// PLAN the route hands the writer, which must be one global grant and never
// zero. Zero is the lockout.
func TestTheFirstAdministratorIsNotLockedOut(t *testing.T) {
	h, dir := freshInstall(t, "")
	if rec := postSetup(h, setupBody("ann", "an-invented-password")); rec.Code != http.StatusOK {
		t.Fatalf("setup answered %d: %s", rec.Code, rec.Body.String())
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	users, err := st.Users()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Role != "admin" {
		t.Fatalf("the account was not created as an admin: %+v", users)
	}

	// The same call the route makes, with the same arguments: role "admin" and an
	// EMPTY router list, which means unrestricted. Asserted here as well as in
	// internal/rbac because this route is the caller that would be wrong about
	// it — and because a fresh install has no routers, so the empty case is the
	// only one it can ever hit.
	plan := rbac.PlanUserGrants(rbac.UserForGrants{
		ID: users[0].ID, Username: users[0].Username, Role: "admin",
		AllowedRouterIDs: []string{},
	}, map[string]bool{})
	if plan.Made != 1 {
		t.Fatalf("the plan makes %d grants, want 1. Zero is the lockout the live comment "+
			"describes: every guard refuses an administrator who holds none.", plan.Made)
	}
	last := plan.Steps[len(plan.Steps)-1]
	if last.ScopeType != "global" || last.Role != "admin" {
		t.Errorf("the grant is %+v, want a global admin grant", last)
	}
}

// ── The three paths a fixture without a database cannot reach ───────────────
//
// The tests above build a server with no `auditDB`, so `grantFirstAdmin`
// returns before it writes anything. Three mutations survived on that: treating
// a `UserCount` error as zero users, dropping the rate limiter, and handing the
// grant planner a router list that produces NO grant — the lockout. Each is
// closed below, and each needed a different fixture rather than a bigger one.

// freshInstallWithDB is freshInstall plus a real database, so the grant writes
// actually run.
func freshInstallWithDB(t *testing.T) (http.Handler, *db.DB) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		".secret": "test-secret", "settings.json": `{}`, "routers.json": `[]`,
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
	if err := execOn(t, dbDir, routerRbacDDL2); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	srv, err := New(st, Options{WebDir: t.TempDir(), AuditDB: d})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler(), d
}

// routerRbacDDL2 is the minimum this route touches: the grants table and the
// role it references. `grants.role_id` is `NOT NULL REFERENCES roles(id)`, so
// the row cannot be written without a matching role — which is the constraint
// `internal/db` leans on instead of re-checking, and a fixture without it could
// not exercise the write at all.
//
// (No backticks in this comment: it sits inside a Go raw string.)
const routerRbacDDL2 = `
-- db.Open reads schema_version before anything else and reports "is this the
-- right /data?" when it is missing. A PRAGMA user_version is NOT what it reads,
-- which is how the first version of this fixture failed.
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);

-- The trail, so the auth.setup event can be ASSERTED. Without this table every
-- write logs "[audit] record failed" and passes, which is the audit package's
-- documented never-throws behaviour -- and a route recording nothing would look
-- identical to one recording correctly.
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  actor_id TEXT, actor_name TEXT NOT NULL, actor_ip TEXT, action TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('app','router')), router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok','denied','failed')), detail TEXT);

CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT, builtin INTEGER NOT NULL DEFAULT 0);
-- COPIED FROM THE LIVE MIGRATION, constraints included. Three things were
-- missing from the first version of this fixture and each broke the write in a
-- different way: created_at (upsertGrant writes it), the UNIQUE (its
-- ON CONFLICT targets exactly those four columns and SQLite refuses a clause
-- matching no constraint), and scope_id NOT NULL DEFAULT the empty string.
--
-- The last one is not incidental. The live comment: a nullable scope_id would
-- let "one principal hold two global grants and the constraint below would
-- silently never fire". A fixture is only useful where it matches the schema on
-- disk -- which is the whole reason tools/schema-audit.js exists.
CREATE TABLE grants (
  id TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT,
  scope_type TEXT NOT NULL,
  scope_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  created_by TEXT,
  CHECK ((scope_type =  'global' AND scope_id =  '')
      OR (scope_type <> 'global' AND scope_id <> '')),
  UNIQUE (principal_type, principal_id, scope_type, scope_id));
-- THE ROLE IDS, NOT THE LEGACY NAMES. resolveRoleID maps the name a caller
-- sends onto an id, and the two differ for exactly one of the three:
--   admin -> administrator,  operator -> operator,  viewer -> readonly
-- A fixture inserting a role called 'admin' fails the FOREIGN KEY with nothing
-- but "constraint failed (787)" to say why, which is how this one first went
-- wrong.
-- (No backticks anywhere in this string: it is a Go raw literal, and one here
-- ends it -- a parse error pointing at a line far from the cause. Sixth time
-- in this project.)
INSERT INTO roles (id, name, builtin) VALUES ('administrator','Administrator',1);
INSERT INTO roles (id, name, builtin) VALUES ('operator','Operator',1);
INSERT INTO roles (id, name, builtin) VALUES ('readonly','Read only',1);
`

// TestSetupWritesTheGlobalAdminGrant — the write, not the plan.
//
// `internal/rbac` pins WHICH grants; this pins that the route applies them. The
// difference is the whole reason the two were split, and a mutation feeding the
// planner a router list that yields nothing survived every test that had no
// database to write to.
func TestSetupWritesTheGlobalAdminGrant(t *testing.T) {
	h, d := freshInstallWithDB(t)

	rec := postSetup(h, setupBody("ann", "an-invented-password"))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup answered %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		User map[string]any `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	id, _ := out.User["id"].(string)
	if id == "" {
		t.Fatal("the response carries no user id")
	}

	grants, err := d.ListGrants(db.GrantFilter{PrincipalType: "user", PrincipalID: id})
	if err != nil {
		t.Fatal(err)
	}
	// EXACTLY ONE, GLOBAL. Zero is the lockout the live comment describes:
	// "every guard refuses them — locked out of their own instance the moment
	// setup completes."
	if len(grants) != 1 {
		t.Fatalf("%d grants for the first administrator, want 1. Zero means the account "+
			"exists and can do nothing: %+v", len(grants), grants)
	}
	if grants[0].ScopeType != "global" {
		t.Errorf("scope is %q, want global — a fresh install has no routers to scope to",
			grants[0].ScopeType)
	}

	// AND THE EVENT. The live repo's note on adding it: "A public route that
	// mints an administrator. It is the single most consequential write in the
	// app and had no record of any kind."
	//
	// The actor id stays NULL — `ForLogin`'s whole point, since there is no
	// session — while the NAME is the account just created. Getting those two
	// the wrong way round is a bug this port has already made once, on login,
	// and it is invisible to any test that only checks a row exists.
	page, err := d.QueryAuditEvents(db.Query{IncludeApp: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page.Rows {
		if e.Action != "auth.setup" {
			continue
		}
		found = true
		if e.ActorName != "ann" {
			t.Errorf("actor_name is %q, want ann", e.ActorName)
		}
		// THE ACTOR ID STAYS NULL — `ForLogin`'s whole point, since there is no
		// session. A pointer, so nil is the assertion.
		if e.ActorID != nil {
			t.Errorf("actor_id is %q, want NULL: there is nobody signed in", *e.ActorID)
		}
		if e.TargetID == nil || *e.TargetID != id {
			t.Errorf("target_id is %v, want the new user id %q", e.TargetID, id)
		}
	}
	if !found {
		t.Errorf("no auth.setup event was recorded. This is the most consequential write "+
			"in the app and it happens with nobody signed in; %d events present", len(page.Rows))
	}
}

// TestSetupRefusesWhenTheUserFileCannotBeRead.
//
// THE DELIBERATE DIVERGENCE, and the only place it is observable. The live
// `_readFile` returns `[]` for a file it cannot parse, so `userCount()` answers
// 0 — which RE-OPENS this route on a corrupted install and lets anybody claim
// it. `store.UserCount` returns the error and this route refuses.
//
// A DIRECTORY where the file belongs, not a chmod: these tests run as ROOT in
// the container, so removing read permission does not stop root reading it and
// the case would silently become a no-op.
//
// ── THE UserCount CHECK IS REDUNDANT TODAY, AND KEPT ────────────────────────
//
// A mutation making the route treat a UserCount error as ZERO USERS survives
// this test, and correctly: `store.CreateUser` reads the same file and refuses
// it too, so the request still fails — through a different branch, with the same
// 500. The two are redundant BY DESIGN.
//
// Recorded rather than resolved, because the obvious reactions are both wrong.
// Deleting the UserCount check would leave the route depending on CreateUser
// happening to re-read the file — a property of an implementation two packages
// away, which an optimisation could remove without anything failing here.
// Contorting the test to tell the two 500s apart would pin which branch fires
// rather than what an operator gets.
func TestSetupRefusesWhenTheUserFileCannotBeRead(t *testing.T) {
	h, dir := freshInstall(t, "")
	if err := os.Mkdir(filepath.Join(dir, "users.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := postSetup(h, setupBody("attacker", "an-invented-password"))
	if rec.Code == http.StatusOK {
		t.Fatalf("setup SUCCEEDED against a users.json it could not read: %s. "+
			"A file that cannot be read is not zero users — treating it as zero re-opens "+
			"an unauthenticated route that mints an administrator.", rec.Body.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("answered %d, want 500", rec.Code)
	}

	// A CORRUPT file, which is the likelier shape of the same failure.
	h2, dir2 := freshInstall(t, "")
	if err := os.WriteFile(filepath.Join(dir2, "users.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := postSetup(h2, setupBody("attacker", "an-invented-password")); rec.Code == http.StatusOK {
		t.Errorf("setup SUCCEEDED against a corrupt users.json: %s", rec.Body.String())
	}
}

// TestSetupIsRateLimited.
//
// Five a minute, matching `setupLimiter` — twelve times tighter than the write
// routes, because this route needs no session and every attempt is a guess at
// owning the instance.
//
// Untested, dropping the limiter changed nothing: the concurrency test counts
// 429s but passes without any, since the zero-users gate refuses the extras
// anyway. The limiter matters for the case that gate does NOT cover — an
// install still waiting to be claimed, being probed.
func TestSetupIsRateLimited(t *testing.T) {
	h, _ := freshInstall(t, "")

	// Every request is INVALID, so the zero-users gate never closes and each one
	// reaches the limiter. Without this the first success would close setup and
	// the rest would be 409s whether or not a limiter existed.
	codes := make([]int, 7)
	for i := range codes {
		codes[i] = postSetup(h, setupBody("bad name", "an-invented-password")).Code
	}
	limited := 0
	for _, c := range codes {
		if c == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Errorf("seven attempts, none refused by the limiter: %v. This route is "+
			"unauthenticated; five a minute is what stops it being brute-forced.", codes)
	}
	// The first five must NOT be limited, or the limit is tighter than the live
	// one and a fumbled setup locks the operator out for a minute.
	for i := 0; i < 5; i++ {
		if codes[i] == http.StatusTooManyRequests {
			t.Errorf("attempt %d was limited; the live budget is five a minute: %v", i+1, codes)
		}
	}
}
