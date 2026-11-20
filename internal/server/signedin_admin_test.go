package server

import (
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/pages"
	"mikrodash/internal/rbac"
	"mikrodash/internal/store"
)

// signedInAdmin makes `sess` a signed-in global administrator of `s`, the way
// first-run setup makes the first one: a user record in users.json, the
// builtin Administrator role granted over everything, and the session's pages
// and routers resolved from that grant as `localSession` resolves them.
//
// ── IT REPLACES `AuthMode: "none"` ─────────────────────────────────────────
//
// These tests used sign-in off as a shortcut to "allowed everything". Sign-in
// is always required now (#165), so administration is earned through the
// same grant graph production reads, and no test can pass on a short circuit
// that no install can reach.
//
// The resolver gets a database of its own, migrated and seeded by db.Open, so
// a test whose audit database is a hand-made fixture keeps that fixture.
func signedInAdmin(t *testing.T, s *Server, sess *Session) {
	t.Helper()
	if s.store == nil {
		t.Fatal("signedInAdmin: the server has no store to hold the user")
	}
	if sess.Username == "" {
		sess.Username = "admin"
	}
	uid := s.userIDFor(sess.Username)
	if uid == "" {
		rec, err := s.store.CreateUser(store.NewUser{Username: sess.Username,
			Password: "test-password-1", Role: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		uid, _ = rec["id"].(string)
	}
	if uid == "" {
		t.Fatal("signedInAdmin: the user was not created")
	}

	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := database.UpsertGrant(db.GrantSpec{PrincipalType: "user", PrincipalID: uid,
		RoleID: "administrator", ScopeType: "global"}); err != nil {
		t.Fatal(err)
	}

	st := s.store
	s.rbac = rbac.New(database, func() []rbac.Router {
		// The production resolver's list (server.go): every device, with its sites.
		list, _ := st.Devices()
		out := make([]rbac.Router, 0, len(list))
		for _, r := range list {
			out = append(out, rbac.Router{ID: r.ID, SiteIDs: store.RouterSiteIDs(r),
				Label: r.Label, Host: r.Host})
		}
		return out
	})
	caps, err := s.rbac.CapabilitiesFor(uid)
	if err != nil {
		t.Fatal(err)
	}
	sess.Role = "admin"
	sess.Pages = caps.Pages
	sess.Readable = caps.Readable
	// Pages are the union over readable routers, so a server with no routers
	// leaves them empty for an administrator too. What must hold is the global
	// answer, asked the way every handler asks it.
	if !s.isGlobalAdmin(sess) {
		t.Fatal("signedInAdmin: the session is not a global administrator; " +
			"every test built on it would prove nothing")
	}
}

// readerOf is a signed-in viewer who may READ every page on these routers and
// write none: what the socket and assistant tests meant by "sign-in off, so
// every page reads" before sign-in became mandatory (#165). On a server with
// no resolver a read takes the session's own union, the documented fallback,
// and a write fails closed, so these tests keep measuring rooms, tools and
// races rather than permissions.
func readerOf(routers ...string) *Session {
	read := map[string]string{}
	for _, k := range pages.Keys() {
		read[k] = "read"
	}
	return &Session{Username: "viewer", Pages: read, Readable: routers}
}
