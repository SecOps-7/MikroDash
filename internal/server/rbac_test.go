package server

import (
	"database/sql"
	"path/filepath"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/rbac"

	_ "modernc.org/sqlite"
)

const rbacDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  actor_id TEXT, actor_name TEXT NOT NULL, actor_ip TEXT, action TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('app','router')), router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok','denied','failed')), detail TEXT);
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

INSERT INTO roles (id, name, builtin) VALUES ('role-w','w',0), ('role-r','r',0);
INSERT INTO role_pages (role_id, page, access) VALUES ('role-w','dns','write'), ('role-r','dns','read');
-- u-1 may WRITE dns on r-A and only READ it on r-B. This is the exact shape the
-- union gate could not express.
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id)
VALUES ('user','u-1','router','r-A','role-w'), ('user','u-1','router','r-B','role-r');
`

func testResolver(t *testing.T) *rbac.Resolver {
	t.Helper()
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(rbacDDL); err != nil {
		t.Fatal(err)
	}
	h.Close()
	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return rbac.New(database, func() []rbac.Router {
		return []rbac.Router{{ID: "r-A"}, {ID: "r-B"}}
	})
}

// connFor builds a connection whose session carries the UNION Node would have
// sent: dns=write, because the union is taken across every readable router and
// u-1 does hold write on one of them. That union is precisely what used to
// over-permit on r-B.
func connFor(t *testing.T, resolver *rbac.Resolver, routerID string) *conn {
	t.Helper()
	return &conn{
		srv: &Server{rbac: resolver},
		sess: &Session{
			Username: "someone", AuthMode: "modern",
			Pages:    map[string]string{"dns": "write"},
			Readable: []string{"r-A", "r-B"},
		},
		routerID: routerID,
		userID:   "u-1",
	}
}

// TestCanPageClosesTheUnionOverPermission is the assertion the whole RBAC item
// exists for. The session's `caps.pages` says dns=write — Node's own union — and
// on r-B that is wrong. The composed gate must refuse it.
func TestCanPageClosesTheUnionOverPermission(t *testing.T) {
	r := testResolver(t)

	if !connFor(t, r, "r-A").canPage("dns", "write") {
		t.Error("dns:write on r-A was refused; the grant allows it")
	}
	if connFor(t, r, "r-B").canPage("dns", "write") {
		t.Error("dns:write on r-B was ALLOWED — the union gate's over-permission " +
			"is still open, which is the bug this work was done to close")
	}
	if !connFor(t, r, "r-B").canPage("dns", "read") {
		t.Error("dns:read on r-B was refused; the grant allows it")
	}
}

// TestCanPageIsAnAND: the coarse gate is not merely advisory. A page the union
// does not grant must be refused even where the graph would allow it — the
// resolver can only ever narrow, never widen.
func TestCanPageIsAnAND(t *testing.T) {
	cn := connFor(t, testResolver(t), "r-A")
	cn.sess.Pages = map[string]string{} // Node granted nothing
	if cn.canPage("dns", "write") {
		t.Error("the resolver widened access past what Node's union allowed")
	}
}

// TestCanPageRespectsReadableRouters keeps the coarse router gate in force: a
// router the session may not read at all is refused before the graph matters.
func TestCanPageRespectsReadableRouters(t *testing.T) {
	cn := connFor(t, testResolver(t), "r-A")
	cn.sess.Readable = []string{"r-B"}
	if cn.canPage("dns", "write") {
		t.Error("a router outside the readable list was allowed")
	}
}

// TestAuthModeNoneShortCircuits mirrors rbac.js's `if (!_isModern()) return true`.
// In 'none' mode there is no identity, no grant graph and no user id — gating on
// any of them would lock out an install that has deliberately disabled auth.
func TestAuthModeNoneShortCircuits(t *testing.T) {
	cn := connFor(t, testResolver(t), "r-B")
	cn.sess.AuthMode = "none"
	cn.sess.Pages = map[string]string{}
	cn.sess.Readable = nil
	cn.userID = ""
	if !cn.canPage("dns", "write") {
		t.Error("'none' auth mode was gated; every request there is implicitly admin")
	}
}

// TestUnavailableResolverFallsBackToTheUnion documents the degradation rather
// than hiding it: with no database the coarse gate stands alone, which is the
// pre-existing over-permission. It must not instead deny everything.
func TestUnavailableResolverFallsBackToTheUnion(t *testing.T) {
	cn := &conn{
		srv: &Server{}, // no resolver
		sess: &Session{
			Username: "someone", AuthMode: "modern",
			Pages:    map[string]string{"dns": "write"},
			Readable: []string{"r-B"},
		},
		routerID: "r-B",
		userID:   "u-1",
	}
	if !cn.canPage("dns", "write") {
		t.Error("with no database the server denied a page the union allowed; " +
			"that locks users out rather than degrading to the documented gap")
	}
}

// TestUnknownUserFailsClosed: a session whose username resolved to no id must not
// be waved through by the resolver half.
func TestUnknownUserFailsClosed(t *testing.T) {
	cn := connFor(t, testResolver(t), "r-A")
	cn.userID = ""
	if cn.canPage("dns", "write") {
		t.Error("a session with no resolved user id was allowed")
	}
}

func TestNilSessionFailsClosed(t *testing.T) {
	cn := &conn{srv: &Server{}, routerID: "r-A"}
	if cn.canPage("dns", "read") {
		t.Error("a connection with no session was allowed")
	}
}
