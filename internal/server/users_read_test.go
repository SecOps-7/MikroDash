package server

// `GET /api/users` — the Users card's one fetch.
//
// The STRIP is `store.PublicUsers`, pinned against the live `_toPublic` by
// The users-public corpus. What is pinned here is the ROUTE: that it
// strips at all, that it joins each user's grants, that a broken file is an
// error rather than an empty list, and that only an administrator sees any of it.

import (
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

// usersFixture carries a credential in every shape the record has ever used, so
// "the strip runs" is not holding because there was nothing to strip. The values
// are obvious placeholders — this repo is public.
const usersFixture = `[
  {"id":"u-1","username":"alice","role":"admin",
   "passwordHash":"PLACEHOLDER-not-a-real-hash","salt":"PLACEHOLDER-salt",
   "allowedRouterIds":["r1"],"createdAt":1},
  {"id":"u-2","username":"bob","role":"viewer",
   "passwordHash":"PLACEHOLDER-not-a-real-hash-2","salt":"PLACEHOLDER-salt-2",
   "unmodelledField":"kept"}
]`

const usersGrantsDDL = `
-- THE LIVE SHAPE, not a convenient one: a TEXT uuid id, a NOT NULL scope_id that
-- is EMPTY for a global grant, and created_at. A fixture looser than the schema
-- is how GrantRow.ID stayed int64 while being unable to read a real row.
-- (No backticks in this comment: it sits inside a Go raw string.)
-- ROLES FIRST, because grants.role_id carries a real foreign key and SQLite
-- cannot reference a table that does not exist yet. It used to be created by the
-- scoped test further down; moved here so the key is declarable at all.
-- description IS DECLARED HERE, not added later by an ALTER. GetRole and
-- ListRoles both select it, so a fixture without it answers "no such column" --
-- and adding it with ALTER TABLE ADD COLUMN instead left the pool's cached
-- schema stale enough that a later ON CONFLICT on the GRANTS table could not
-- find its unique index, which reads as a missing index rather than as a
-- schema-cache problem. Measured 2026-08-28: the index was present in
-- sqlite_master and the statement still would not prepare.
-- (No backticks in this comment: it sits inside a Go raw string.)
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  builtin INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO roles (id, name) VALUES
  ('manager','manager'), ('auditor','auditor'), ('viewer','viewer');
CREATE TABLE grants (
  id             TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL,
  principal_id   TEXT NOT NULL,
  role_id        TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  role           TEXT,
  scope_type     TEXT NOT NULL,
  scope_id       TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  created_by     TEXT);
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id, created_at) VALUES
  ('user','u-1','router','r1','manager',1),
  ('user','u-1','global','','auditor',2),
  ('user','u-2','site','s1','viewer',3),
  -- A GROUP grant sharing u-1's id. Ids are opaque, and a filter on the id alone
  -- would hand a user their group's access as if it were their own.
  ('group','u-1','router','r9','manager',4);
`

// usersServer builds the harness. `extraDDL` is applied BEFORE `db.Open`, and
// that ordering is not cosmetic.
//
// ── DDL AFTER Open IS INVISIBLE TO CONNECTIONS ALREADY IN THE POOL ─────────
//
// Measured 2026-08-28. An index created by `execOn` after `db.Open` is present
// in `sqlite_master` and a fresh connection uses it happily — but a connection
// the pool had already established resolves `ON CONFLICT (...)` against the
// schema it cached, and answers "ON CONFLICT clause does not match any PRIMARY
// KEY or UNIQUE constraint" for an index that is right there.
//
// It is nondeterministic by which connection serves the call, which is why it
// looked like a missing index: the same fixture worked when the write went
// through an HTTP handler (a fresh pooled connection) and failed when a test
// called the writer directly.
//
// Production never hits this — `db.Open` runs the migrations itself. So the fix
// belongs here: every fixture's DDL goes in before the pool exists.
func usersServer(t *testing.T, sess *Session, usersJSON string,
	extraDDL ...string) (*Server, *http.ServeMux, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"routers.json": `[]`, "settings.json": `{}`, ".secret": "test-secret",
		"users.json": usersJSON,
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
	if err := execOn(t, dbDir, alertTestDDL+usersGrantsDDL+strings.Join(extraDDL, "\n")); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	auth := NewAuth("", time.Hour)
	auth.cache["tok"] = cached{session: sess, until: time.Now().Add(time.Hour)}

	s := &Server{store: st, auditDB: d, auth: auth, hub: hub.New(),
		devicesWatchers: map[*hub.Client]bool{},
		conns:           map[*hub.Client]*conn{}}
	mux := http.NewServeMux()
	s.registerPrincipals(mux)
	// `routerDBDir` is how the helpers find a server's database; a server that
	// does not record its own is invisible to them. See the note on that map.
	routerDBDir[s] = dbDir
	return s, mux, dir
}

func getUsers(mux *http.ServeMux, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/users", nil)
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func TestUsersReadStripsAndJoinsGrants(t *testing.T) {
	_, mux, _ := usersServer(t, &Session{AuthMode: "none", Username: "admin"}, usersFixture)

	w := getUsers(mux, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var reply struct {
		OK    bool             `json:"ok"`
		Users []map[string]any `json:"users"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.OK || len(reply.Users) != 2 {
		t.Fatalf("reply = %+v", reply)
	}

	// THE STRIP. Believability first: the fixture really does carry the two
	// fields, or their absence below would prove nothing.
	if !strings.Contains(usersFixture, "passwordHash") || !strings.Contains(usersFixture, "salt") {
		t.Fatal("the fixture has no credential fields, so the strip is untested")
	}
	body := w.Body.String()
	for _, secret := range []string{"passwordHash", "salt", "PLACEHOLDER-not-a-real-hash"} {
		if strings.Contains(body, secret) {
			t.Errorf("%q reached the browser", secret)
		}
	}
	// ...and what the card NEEDS survived, including a field nothing models.
	for _, keep := range []string{"username", "allowedRouterIds", "createdAt", "unmodelledField"} {
		if !strings.Contains(body, keep) {
			t.Errorf("%q was dropped -- the strip is a DENYLIST, and the Users card "+
				"distinguishes \"no access\" from \"access to nothing\"", keep)
		}
	}

	// THE JOIN, per user and scoped to the USER principal type.
	byName := map[string]map[string]any{}
	for _, u := range reply.Users {
		byName[u["username"].(string)] = u
	}
	alice, _ := byName["alice"]["grants"].([]any)
	if len(alice) != 2 {
		t.Errorf("alice has %d grants, want 2 (a group grant shares her id and must not "+
			"be counted as hers)", len(alice))
	}
	bob, _ := byName["bob"]["grants"].([]any)
	if len(bob) != 1 {
		t.Errorf("bob has %d grants, want 1", len(bob))
	}
}

// TestABrokenUsersFileIsAnErrorNotAnEmptyList.
//
// `users.json` failing to parse must not read as "this install has no users".
// The Users card would show an empty table with nothing to say why, and an
// administrator would reasonably conclude their accounts were gone.
func TestABrokenUsersFileIsAnErrorNotAnEmptyList(t *testing.T) {
	_, mux, _ := usersServer(t, &Session{AuthMode: "none"}, `{"not":"an array"}`)

	w := getUsers(mux, authed)
	if w.Code != 500 {
		t.Errorf("status %d, want 500: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"users":[]`) {
		t.Error("a broken file was served as an empty user list")
	}
}

// TestOnlyAnAdministratorReadsTheUserList, under a REAL resolver — `AuthMode:
// "none"` short-circuits `isGlobalAdmin` to true, which is this port's recurring
// blind spot.
func TestOnlyAnAdministratorReadsTheUserList(t *testing.T) {
	s, mux, dir := usersServer(t, &Session{AuthMode: "modern", Username: "carol"}, usersFixture)
	// NOT `routerRbacDDL`: that creates its own `grants`, and this server already
	// has one. Only the parts it does not.
	if err := execOn(t, routerDBDirOf(t, s), `
	  -- The roles table is created by usersGrantsDDL, before grants, so the foreign
	  -- key is declarable; only the page matrix and the membership table are here.
	  -- (No backticks in these SQL comments: they sit inside a Go raw string --
	  -- the fourth time this trap has been hit, and CLAUDE.md records the others.)
	  CREATE TABLE role_pages (role_id TEXT NOT NULL, page TEXT NOT NULL, access TEXT NOT NULL);
	  CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
	  -- builtin 0: a builtin role holds every KNOWN permission structurally,
	  -- which would grant system:principals and the refusal below would never
	  -- happen.
	  INSERT INTO role_pages (role_id, page, access) VALUES ('manager','devices','write');
	  INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id, created_at)
	  VALUES ('user','u-1','router','r1','manager',1);
	`); err != nil {
		t.Fatal(err)
	}
	// carol is u-1 in the RBAC fixture: devices:write on r1, never system:principals.
	if err := os.WriteFile(filepath.Join(dir, "users.json"),
		[]byte(`[{"id":"u-1","username":"carol","passwordHash":"x","salt":"y","role":"viewer"}]`),
		0o600); err != nil {
		t.Fatal(err)
	}

	s.rbac = rbac.New(s.auditDB, func() []rbac.Router { return []rbac.Router{{ID: "r1"}} })

	// Believability: she resolves and is NOT an administrator. Without this the
	// 403 below could be a lookup failure rather than a refusal.
	if s.userIDFor("carol") != "u-1" {
		t.Fatalf("userIDFor(carol) = %q", s.userIDFor("carol"))
	}
	if s.isGlobalAdmin(&Session{AuthMode: "modern", Username: "carol"}) {
		t.Fatal("carol IS an administrator, so this test proves nothing")
	}

	if w := getUsers(mux, authed); w.Code != 403 {
		t.Errorf("status %d, want 403", w.Code)
	}
	if w := getUsers(mux, ""); w.Code != 401 {
		t.Errorf("an anonymous read gave %d, want 401", w.Code)
	}
}

// routerDBDirOf finds a server's database directory the way the other helpers
// do, without depending on which one built it.
func routerDBDirOf(t *testing.T, s *Server) string {
	t.Helper()
	if d, ok := routerDBDir[s]; ok {
		return d
	}
	t.Fatal("this server's database directory is not recorded")
	return ""
}
