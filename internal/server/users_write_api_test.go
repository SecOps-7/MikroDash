package server

// The three ADMINISTRATION routes for user accounts.
//
// ── WHAT IS WORTH TESTING HERE AND WHAT IS NOT ─────────────────────────────
//
// The store writers beneath these are already pinned against the live functions
// by `tools/userwrite-cases.js` and `internal/store/users_update_test.go` — 22
// update cases, 3 delete cases, 13 mutations. What those cannot see is the
// HTTP layer's own decisions, and every one of them is a rule whose failure is
// SILENT:
//
//   - the conditional grant projection. `syncUserGrants` deletes every grant the
//     principal holds and rebuilds them, so running it on a RENAME would destroy
//     an administrator's work with a 200 and no message.
//   - the orphan probes. Both the delete route and the legacy `role` field can
//     leave an install with nobody able to administer it.
//   - the cascade. `user_notify_config` holds ENCRYPTED CHANNEL CREDENTIALS, so
//     a row outliving its account means a reused id inherits somebody's Telegram
//     token.
//
// Those are what this file is for.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// usersWriteServer reuses `usersServer`'s harness and adds the write routes.
// usersWriteDDL is what the CASCADE needs and the read fixture does not have.
//
// `usersGrantsDDL` was written for `GET /api/users`, which joins grants and
// nothing else. The delete route clears three tables, and two of them were
// simply absent — which surfaced as "no such table: user_layouts" rather than as
// a wrong answer, because SQLite has no opinion about a DELETE on a table that
// does not exist until you run one.
//
// `group_members` is here for `WouldOrphanGlobalAdmin`, which reads it: an
// administrator whose grant is held THROUGH A GROUP is exactly the case the
// probe exists to see, and a fixture without the table cannot express it.
const usersWriteDDL = `
-- THE UNIQUE INDEX upsertGrant CONFLICTS ON. Without it every projection fails
-- with "ON CONFLICT clause does not match any PRIMARY KEY or UNIQUE constraint",
-- which is a logged error and a 200 — so the grants simply do not appear and the
-- test reads as "nothing was projected".
CREATE UNIQUE INDEX IF NOT EXISTS grants_principal_scope
  ON grants (principal_type, principal_id, scope_type, scope_id);
-- A BUILTIN role at global scope, held by u-1. globalAdminQuery counts only
-- builtin = 1 roles -- "a custom role may confer everything today and be edited
-- to confer nothing tomorrow" -- so a fixture whose roles are all custom has ZERO
-- administrators, and WouldOrphanGlobalAdmin then refuses EVERY delete. That is
-- faithful behaviour answering a question the fixture asked wrong.
-- (No backticks in this comment: it sits inside a Go raw string. Putting a pair
-- around a query name here is what broke the build on the first attempt.)
-- THE IDS THE PROJECTION ACTUALLY WRITES, which are not the role NAMES.
-- legacyRoleIDs maps admin->administrator, operator->operator, viewer->readonly,
-- and resolveRoleID falls back to 'readonly' for anything else. A fixture whose
-- roles table is keyed by the names instead fails the foreign key on every
-- projection -- which is a logged error and a 200, so the grants simply never
-- appear and the test reads as "nothing was projected".
-- DISTINCT NAMES. The first version named the readonly role 'viewer', which
-- collides with usersGrantsDDL's own 'viewer' role -- harmless until
-- rolesWriteDDL declared the UNIQUE index that the real schema has, at which
-- point the index could not be created at all and every role test failed on the
-- constraint rather than on what it was testing. The ids are what
-- legacyRoleIDs maps onto; the names are free.
INSERT INTO roles (id, name, builtin) VALUES
  ('administrator','admin',1), ('operator','operator',1), ('readonly','readonly',1);
-- PROMOTED, not inserted. usersGrantsDDL already gives u-1 a global grant (with
-- the custom role 'auditor'), and the unique index above is exactly what stops a
-- second row at the same scope -- so an INSERT here fails the constraint it was
-- added to declare. Isolated to this harness: the read tests build their own
-- database and never run this DDL.
UPDATE grants SET role_id = 'administrator'
 WHERE principal_type = 'user' AND principal_id = 'u-1' AND scope_type = 'global';
-- A SECOND global administrator. Without one, "you may not delete your own
-- account" and "that would leave nobody with administrator access" both refuse
-- the same request with the same status, and dropping the first guard SURVIVES
-- -- which is what a mutation run showed on 2026-08-28. Two admins make the two
-- rules separable, and the assertion below reads the MESSAGE.
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id, created_at)
  VALUES ('user','u-2','global','','administrator',6);
CREATE TABLE IF NOT EXISTS group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS user_layouts (
  user_id TEXT NOT NULL, kind TEXT NOT NULL, data TEXT NOT NULL,
  updated_at INTEGER NOT NULL, PRIMARY KEY (user_id, kind));
CREATE TABLE IF NOT EXISTS user_notify_config (
  user_id TEXT PRIMARY KEY, data TEXT NOT NULL, updated_at INTEGER NOT NULL);
`

func usersWriteServer(t *testing.T, sess *Session, usersJSON string,
	extraDDL ...string) (*Server, *http.ServeMux, string) {
	t.Helper()
	// BEFORE `db.Open`, via usersServer's `extraDDL` — see its header. Applying
	// it afterwards with `execOn` left pooled connections resolving ON CONFLICT
	// against a schema that predated the unique index.
	s, mux, dir := usersServer(t, sess, usersJSON,
		append([]string{usersWriteDDL}, extraDDL...)...)
	s.registerUsersWrite(mux)
	return s, mux, dir
}

func doJSON(mux *http.ServeMux, method, path, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func readUsersFile(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("users.json is not a bare array: %v", err)
	}
	return out
}

func grantCount(t *testing.T, s *Server, principalID string) int {
	t.Helper()
	rows, err := s.auditDB.GrantsForUser(principalID)
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

const seedUsersJSON = `[
  {"id":"u-1","username":"alice","role":"admin","allowedRouterIds":[],
   "salt":"aa","passwordHash":"bb","createdAt":1700000000000},
  {"id":"u-2","username":"bob","role":"viewer","allowedRouterIds":["r1"],
   "salt":"cc","passwordHash":"dd","createdAt":1700000001000}
]`

// ── POST /api/users ────────────────────────────────────────────────────────

func TestUserCreateValidatesBeforeWriting(t *testing.T) {
	cases := []struct {
		why  string
		body string
		want int
	}{
		{"a valid account", `{"username":"carol","password":"longenough"}`, 200},
		{"no username", `{"password":"longenough"}`, 400},
		{"a username with a space", `{"username":"bad name","password":"longenough"}`, 400},
		// The regex is ANCHORED, and this is the case that says so: an
		// unanchored copy accepts anything CONTAINING a valid run.
		{"a username that CONTAINS a valid run", `{"username":"ok name ok","password":"longenough"}`, 400},
		{"a 64-character username", `{"username":"` + strings.Repeat("a", 64) + `","password":"longenough"}`, 200},
		{"a 65-character username", `{"username":"` + strings.Repeat("a", 65) + `","password":"longenough"}`, 400},
		{"no password", `{"username":"carol"}`, 400},
		{"a three-character password", `{"username":"carol","password":"abc"}`, 400},
		{"exactly four characters", `{"username":"carol","password":"abcd"}`, 200},
		{"an invalid role", `{"username":"carol","password":"longenough","role":"superuser"}`, 400},
		{"a valid role", `{"username":"carol","password":"longenough","role":"operator"}`, 200},
		// TRUTHY-guarded, not presence-guarded: `if (role && !ROLES.includes(role))`.
		// An EMPTY role falls through to the default rather than being refused.
		{"an empty role defaults rather than refusing", `{"username":"carol","password":"longenough","role":""}`, 200},
		{"a duplicate username", `{"username":"alice","password":"longenough"}`, 409},
		// ORDER: the role is checked BEFORE the duplicate. Without this case,
		// deleting the route's own role check survives -- `CreateUser` validates
		// too, so the status stays 400 and only the ORDER gives it away.
		{"an invalid role on a DUPLICATE username is still a role error",
			`{"username":"alice","password":"longenough","role":"superuser"}`, 400},
		{"an empty body", `{}`, 400},
		{"a body that is not JSON", `not json`, 400},
	}
	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			_, mux, dir := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
			w := doJSON(mux, "POST", "/api/users", c.body, authed)
			if w.Code != c.want {
				t.Errorf("status %d, want %d — body %s", w.Code, c.want, w.Body.String())
			}
			// A REFUSAL WRITES NOTHING. Without this the validation could run
			// after the write and every status assertion above would still pass.
			if c.want != 200 && len(readUsersFile(t, dir)) != 2 {
				t.Errorf("a refused request still created an account")
			}
		})
	}
}

// TestUserCreateProjectsGrantsOnlyWhenTheLegacyFieldsAreSent.
//
// The live comment: "The Users card grants access through /api/grants now
// (#108), so a new user starts with none and is granted explicitly — projecting
// a default 'viewer' here would hand every new account read of every router."
//
// So a plain create must leave the account with NO grants, and one that carries
// `role` or `allowedRouterIds` must get the projection. Both directions, because
// either alone passes a port that always projects or never does.
func TestUserCreateProjectsGrantsOnlyWhenTheLegacyFieldsAreSent(t *testing.T) {
	for _, c := range []struct {
		why    string
		body   string
		grants bool
	}{
		{"no legacy fields — no grants", `{"username":"carol","password":"longenough"}`, false},
		{"role sent — projected", `{"username":"carol","password":"longenough","role":"admin"}`, true},
		{"allowedRouterIds sent — projected", `{"username":"carol","password":"longenough","allowedRouterIds":[]}`, true},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
			w := doJSON(mux, "POST", "/api/users", c.body, authed)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			var got struct {
				User map[string]any `json:"user"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			id, _ := got.User["id"].(string)
			if id == "" {
				t.Fatal("the response carried no user id")
			}
			n := grantCount(t, s, id)
			if c.grants && n == 0 {
				t.Error("the legacy fields were sent and nothing was projected")
			}
			if !c.grants && n != 0 {
				t.Errorf("a plain create projected %d grant(s). The live comment is explicit: "+
					"projecting a default here would hand every new account read of every router.", n)
			}
		})
	}
}

func TestUserCreateNeverReturnsTheCredential(t *testing.T) {
	_, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
	w := doJSON(mux, "POST", "/api/users", `{"username":"carol","password":"longenough"}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{"passwordHash", "salt"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Errorf("the create response mentions %q", secret)
		}
	}
}

// ── PUT /api/users/:id ─────────────────────────────────────────────────────

func TestUserUpdateStatuses(t *testing.T) {
	cases := []struct {
		why  string
		id   string
		body string
		want int
	}{
		{"a rename", "u-2", `{"username":"bobby"}`, 200},
		{"an unknown id", "u-nope", `{"username":"x"}`, 404},
		// ORDER again: the role is checked before the record is looked up. The
		// store validates too, so without this case deleting the route's check
		// survives -- the status only differs for an id that does not exist.
		{"an invalid role on an unknown id is a role error, not a 404", "u-nope",
			`{"role":"superuser"}`, 400},
		{"an invalid username", "u-2", `{"username":"bad name"}`, 400},
		{"an invalid role", "u-2", `{"role":"superuser"}`, 400},
		// AN EXPLICIT NULL IS A VALUE THAT WAS SENT. `updates.role !== undefined`
		// is true for it, so it reaches the role check and is refused — where an
		// ABSENT role is a no-op. A body decoded into a struct collapses the two.
		{"an explicit null role is refused", "u-2", `{"role":null}`, 400},
		// REFUSED as of 2026-08-29. This asserted 200 for two days — reproducing
		// the live defect on purpose, filed in ../MikroDash/ToDo.md rather than
		// quietly fixed. Upstream fixed it in `f5416c2` (typeof before the
		// pattern) and this follows.
		//
		// THE STATUS IS NOT THE INTERESTING PART, and asserting only the status
		// is why this suite stayed green across the upstream change: a null
		// username used to be ACCEPTED and renamed the account to the four
		// characters "null", which is a 200 either way you look at it. The
		// resulting username is what the case below checks.
		{"an explicit null username is refused", "u-2", `{"username":null}`, 400},
		{"a non-string username is refused too", "u-2", `{"username":42}`, 400},
		{"an empty patch is a no-op", "u-2", `{}`, 200},
		{"a password alone", "u-2", `{"password":"a-new-one"}`, 200},
	}
	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			_, mux, dir := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
			w := doJSON(mux, "PUT", "/api/users/"+c.id, c.body, authed)
			if w.Code != c.want {
				t.Errorf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
			if c.want == 400 {
				// REFUSED MEANS UNCHANGED.
				for _, u := range readUsersFile(t, dir) {
					if u["id"] == c.id && u["username"] != "bob" {
						t.Errorf("a refused update still renamed the account to %v", u["username"])
					}
				}
			}
		})
	}
}

// TestARenameDoesNotDestroyGrants.
//
// THE most consequential rule in this file. `syncUserGrants` deletes every grant
// the principal holds and rebuilds them from `role` + `allowedRouterIds`, so a
// port running it unconditionally would answer 200 to a rename and silently
// destroy every grant an administrator had built in the editor.
//
// The seed gives u-1 two grants and u-2 one. Renaming u-2 must leave its grant
// exactly where it was.
func TestARenameDoesNotDestroyGrants(t *testing.T) {
	s, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
	before := grantCount(t, s, "u-2")
	if before == 0 {
		t.Fatal("the fixture gives u-2 no grants, so this test cannot observe them being lost")
	}

	w := doJSON(mux, "PUT", "/api/users/u-2", `{"username":"bobby"}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if after := grantCount(t, s, "u-2"); after != before {
		t.Errorf("a RENAME changed the grant count from %d to %d. syncUserGrants deletes every "+
			"grant the principal holds and rebuilds them from the legacy fields, so it must run "+
			"ONLY when the request carried one of them — otherwise renaming a user destroys an "+
			"administrator's work, with a 200 and no message.", before, after)
	}
}

// ── DELETE /api/users/:id ──────────────────────────────────────────────────

func TestUserDeleteRefusesYourOwnAccount(t *testing.T) {
	s, mux, dir := usersWriteServer(t, &Session{AuthMode: "none", Username: "alice"}, seedUsersJSON)
	// `userIDFor` resolves the session's username to an id; alice is u-1.
	if got := s.userIDFor("alice"); got != "u-1" {
		t.Fatalf("the fixture does not resolve alice to u-1 (got %q), so this test would pass "+
			"for the wrong reason", got)
	}
	w := doJSON(mux, "DELETE", "/api/users/u-1", "", authed)
	if w.Code != 400 {
		t.Errorf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	// THE MESSAGE, not just the status. The orphan probe also answers 400, so a
	// port that dropped this guard entirely would still refuse — for the wrong
	// reason, and telling the operator the wrong thing. The fixture gives u-2 a
	// global administrator grant precisely so the two rules are separable.
	if !strings.Contains(w.Body.String(), "Cannot delete your own account") {
		t.Errorf("refused with %s — the own-account rule is what should have stopped this, and "+
			"which rule stopped you is what the operator reads", w.Body.String())
	}
	if len(readUsersFile(t, dir)) != 2 {
		t.Error("the account was deleted anyway")
	}
}

func TestUserDeleteRemovesTheAccountAndItsRows(t *testing.T) {
	s, mux, dir := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)

	// A notification config and a layout, so the cascade has something to clear.
	if err := s.auditDB.SetLayout("u-2", "dashboard", map[string]any{"cards": []any{}}); err != nil {
		t.Fatal(err)
	}
	if grantCount(t, s, "u-2") == 0 {
		t.Fatal("u-2 has no grants in the fixture; the cascade would be untestable")
	}

	w := doJSON(mux, "DELETE", "/api/users/u-2", "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	left := readUsersFile(t, dir)
	if len(left) != 1 || left[0]["id"] != "u-1" {
		t.Errorf("users.json holds %d record(s) after the delete", len(left))
	}
	if n := grantCount(t, s, "u-2"); n != 0 {
		t.Errorf("%d grant(s) survived the account. Users live in JSON, so there is no foreign "+
			"key to cascade through — the rows have to be cleared here or they point at an id a "+
			"later account could be issued.", n)
	}
	blob, err := s.auditDB.Layout("u-2", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 0 {
		t.Errorf("the layout survived the account: %s", blob)
	}
}

func TestUserDeleteUnknownIdIs404(t *testing.T) {
	_, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
	w := doJSON(mux, "DELETE", "/api/users/u-nope", "", authed)
	if w.Code != 404 {
		t.Errorf("status %d, want 404 — %s", w.Code, w.Body.String())
	}
}

// ── The routes are gated ───────────────────────────────────────────────────
//
// Every test above runs as `AuthMode: "none"`, where `isGlobalAdmin`
// short-circuits to true — which leaves the permission check, the security
// boundary of all three routes, untested. This drives the real path.
func TestUserWriteRoutesRequireGlobalAdmin(t *testing.T) {
	for _, c := range []struct {
		method, path, body string
	}{
		{"POST", "/api/users", `{"username":"carol","password":"longenough"}`},
		{"PUT", "/api/users/u-2", `{"username":"bobby"}`},
		{"DELETE", "/api/users/u-2", ""},
	} {
		t.Run(c.method, func(t *testing.T) {
			// `AuthMode: "modern"` with no rbac resolver: `isGlobalAdmin` returns
			// false, which is the fail-closed answer.
			_, mux, dir := usersWriteServer(t, &Session{AuthMode: "modern", Username: "nobody"},
				seedUsersJSON)
			w := doJSON(mux, c.method, c.path, c.body, authed)
			if w.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403 — %s", w.Code, w.Body.String())
			}
			if len(readUsersFile(t, dir)) != 2 {
				t.Error("a forbidden request still changed users.json")
			}
		})
	}
}

func TestUserWriteRoutesRequireASession(t *testing.T) {
	_, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)
	w := doJSON(mux, "POST", "/api/users", `{"username":"carol","password":"longenough"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 — %s", w.Code, w.Body.String())
	}
}

// TestUserDeleteRefusesTheLastAdministrator.
//
// ── THE MUTANT THAT SURVIVED ───────────────────────────────────────────────
//
// Deleting the orphan probe from the delete route survived the whole suite on
// 2026-08-28, because the fixture carries TWO global administrators — so no test
// ever deleted the last one. A guard is not tested by a case it never fires on.
//
// This narrows the install to one administrator and deletes them, as somebody
// else, so neither the own-account rule nor a second admin can account for the
// refusal.
func TestUserDeleteRefusesTheLastAdministrator(t *testing.T) {
	s, mux, dir := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)

	// Leave u-2 as the ONLY global administrator.
	if _, err := s.auditDB.DeleteGrantsForPrincipal("user", "u-1"); err != nil {
		t.Fatal(err)
	}
	admins, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	// BELIEVABILITY. If the fixture left zero administrators the route would
	// refuse everything and this test would pass without exercising anything;
	// if it left two, the probe would allow the delete and the test would fail
	// for the wrong reason.
	if len(admins) != 1 || admins[0] != "u-2" {
		t.Fatalf("the fixture leaves %v as global administrators; this test needs exactly [u-2]",
			admins)
	}

	w := doJSON(mux, "DELETE", "/api/users/u-2", "", authed)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nobody with administrator access") {
		t.Errorf("refused with %s — the orphan probe is what should have stopped this",
			w.Body.String())
	}
	// AND NOTHING WAS WRITTEN. The probe runs before the delete, so a port that
	// checked afterwards would answer 400 with the account already gone.
	if len(readUsersFile(t, dir)) != 2 {
		t.Error("the account was deleted anyway; the probe must run BEFORE the write")
	}
	if n := grantCount(t, s, "u-2"); n == 0 {
		t.Error("the grants were cleared anyway")
	}
}

// TestUserUpdateRefusesDemotingTheLastAdministrator.
//
// ── FOUND BY A MISLABELLED MUTANT ──────────────────────────────────────────
//
// `if orphan {` appears TWICE in the route file — once in the update route's
// projection probe and once in the delete route's. The mutation runner replaces
// the first occurrence, so a mutant labelled "delete: the orphan probe is
// skipped" was actually deleting the UPDATE route's, and it survived. The label
// was wrong; the survival was real, and this is the guard nothing was covering.
//
// The live route is explicit about why it exists: "Demoting the last
// administrator through the legacy field is still a way to orphan
// administration, so the projection is probed before it runs." It is a
// DIFFERENT hole from the delete route's — nobody is removed, and the account
// still exists; it simply stops conferring administration.
func TestUserUpdateRefusesDemotingTheLastAdministrator(t *testing.T) {
	s, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)

	// Leave u-2 as the ONLY global administrator.
	if _, err := s.auditDB.DeleteGrantsForPrincipal("user", "u-1"); err != nil {
		t.Fatal(err)
	}
	admins, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 1 || admins[0] != "u-2" {
		t.Fatalf("the fixture leaves %v as global administrators; this test needs exactly [u-2]",
			admins)
	}

	// A LEGACY ROLE FIELD, which is what triggers the projection at all. Sending
	// `viewer` rebuilds u-2's grants as read-only — and u-2 is the last admin.
	w := doJSON(mux, "PUT", "/api/users/u-2", `{"role":"viewer"}`, authed)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nobody with administrator access") {
		t.Errorf("refused with %s — the projection probe is what should have stopped this",
			w.Body.String())
	}
	// AND THE GRANTS ARE UNTOUCHED. The probe rolls back, so a port that ran the
	// projection and then checked would answer 400 with administration already
	// gone — the worst possible outcome, because the message says it did not
	// happen.
	after, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0] != "u-2" {
		t.Errorf("global administrators are now %v — the probe must roll back, and the refusal "+
			"message says the change did not happen", after)
	}
}

// TestUserUpdateAllowsRE-GRANTING_TheLastAdministrator.
//
// ── WHY THE PROBE MUST RUN THE PROJECTION, NOT A DELETE ────────────────────
//
// `syncUserGrants` deletes every grant the principal holds and then REBUILDS
// them, so whether administration survives depends on what the rebuild puts
// back. Probing a bare delete instead is strictly harsher: it refuses whenever
// the subject is the last administrator, including when the edit was about to
// restore exactly the grant it removed.
//
// The case that separates them is the ordinary one: the last administrator's
// record is re-saved with `role: "admin"`. The projection deletes the admin
// grant and immediately writes it back, so nobody is orphaned and the edit must
// succeed. A delete-probe answers "that would leave nobody with administrator
// access" — to somebody who was not changing anything.
//
// Found by mutation: swapping the projection probe for a delete probe survived
// the suite until this existed.
func TestUserUpdateAllowsRegrantingTheLastAdministrator(t *testing.T) {
	s, mux, _ := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)

	// u-1, NOT u-2, and the difference is the whole test. u-2 carries
	// `allowedRouterIds: ["r1"]`, so projecting `role: admin` over it builds a
	// ROUTER-SCOPED grant on r1 — and the fixture's routers.json is empty, so
	// the plan drops it and the account really is left with nothing. Refusing
	// that is correct. u-1's list is empty, which is what produces a GLOBAL
	// grant and makes this a genuine re-grant.
	if _, err := s.auditDB.DeleteGrantsForPrincipal("user", "u-2"); err != nil {
		t.Fatal(err)
	}
	admins, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 1 || admins[0] != "u-1" {
		t.Fatalf("the fixture leaves %v as global administrators; this test needs exactly [u-1]",
			admins)
	}

	w := doJSON(mux, "PUT", "/api/users/u-1", `{"role":"admin"}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d, want 200 — %s\nRe-saving the last administrator's own role must "+
			"succeed: the projection deletes the grant and writes it straight back. A probe that "+
			"ran a bare delete instead refuses this, telling somebody who changed nothing that "+
			"they would lock everybody out.", w.Code, w.Body.String())
	}
	after, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0] != "u-1" {
		t.Errorf("global administrators are now %v — the projection should have put the grant "+
			"back", after)
	}
}

// A REFUSED USERNAME LEAVES THE ACCOUNT ALONE.
//
// ── THE STATUS WAS NEVER THE PROPERTY ──────────────────────────────────────
//
// `TestUserUpdateStatuses` asserted 200 for `{"username":null}` and stayed green
// while the account was being RENAMED to the four characters "null". A status
// code cannot tell those apart: accepting the rename and ignoring the field are
// both 200. What distinguishes them is the row afterwards.
//
// That is the whole reason an upstream security fix (`f5416c2`) crossed into
// this port with every gate green. The case below is the one that would have
// gone red on the day.
func TestARefusedUsernameDoesNotRenameTheAccount(t *testing.T) {
	for _, c := range []struct {
		why, body string
	}{
		{"an explicit null", `{"username":null}`},
		{"a number", `{"username":42}`},
		{"a bool", `{"username":true}`},
	} {
		t.Run(c.why, func(t *testing.T) {
			_, mux, dir := usersWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, seedUsersJSON)

			before := usernameOf(t, dir, "u-2")
			if before == "" {
				t.Fatal("the fixture has no u-2 — this test would prove nothing")
			}

			doJSON(mux, "PUT", "/api/users/u-2", c.body, authed)

			if after := usernameOf(t, dir, "u-2"); after != before {
				t.Errorf("username became %q, want %q unchanged. A non-string username "+
					"must not rename the account — `alert_events.acknowledged_by` stores "+
					"a username as raw text, so every later acknowledgement would be "+
					"attributed to it.", after, before)
			}
		})
	}
}

func usernameOf(t *testing.T, dir, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "users.json"))
	if err != nil {
		t.Fatalf("reading users.json: %v", err)
	}
	var users []map[string]any
	if err := json.Unmarshal(b, &users); err != nil {
		t.Fatalf("users.json: %v", err)
	}
	for _, u := range users {
		if u["id"] == id {
			s, _ := u["username"].(string)
			return s
		}
	}
	return ""
}
