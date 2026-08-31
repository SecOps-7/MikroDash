package rbac

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"mikrodash/internal/db"

	_ "modernc.org/sqlite"
)

// The tables canPage walks, copied from src/db.js's migrations, plus the
// audit_events table db.Open's schema check implies. Kept close to the real DDL
// so a test schema cannot pass where the real one would reject.
const grantDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);
CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL,
  actor_id TEXT, actor_name TEXT NOT NULL, actor_ip TEXT, action TEXT NOT NULL,
  scope TEXT NOT NULL CHECK (scope IN ('app','router')), router_id TEXT,
  target_type TEXT, target_id TEXT, target_name TEXT,
  outcome TEXT NOT NULL CHECK (outcome IN ('ok','denied','failed')), detail TEXT);
-- THE FULL ROLES SHAPE, not the three columns CanPage needs. It was
-- (id, name, builtin) while nothing in this package read a role's description
-- or created_at, and AccessSummaryFor goes through GetRole, which selects all
-- five -- so every case failed with "no such column: description". A fixture
-- narrower than the live schema cannot exercise the code that reads it, which
-- is the failure schema-audit exists to catch on the other side.
CREATE TABLE roles (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  description TEXT,
  builtin     INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL DEFAULT 0
);
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
  role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT);
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
-- The sites table is here for AccessSummaryFor, the only thing in this package
-- that reads a site's NAME. THE NULLABLE COLUMNS ARE NULLABLE: GetSite's own
-- comment records what a fixture declaring them NOT NULL DEFAULT '' hid -- the
-- scan fails on an ordinary site with no description and the lookup 500s. A
-- schema that cannot produce the row which breaks the code is not a test.
-- (No backticks in this comment: this DDL is a Go raw string, and a backtick
-- here ends it. Fifth time in this port; the parse error names a line far from
-- the cause, which is why it is worth saying at every one of them.)
CREATE TABLE sites (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  description  TEXT,
  lat          REAL,
  lon          REAL,
  place_name   TEXT,
  place_region TEXT,
  place_cc     TEXT,
  created_at   INTEGER NOT NULL
);
`

// The fleet every test resolves against: two routers in one site, one in none,
// and one in TWO — the multi-site case (#117), which a grant on either site must
// reach.
var testRouters = []Router{
	{ID: "r-A", SiteIDs: []string{"site-1"}},
	{ID: "r-B", SiteIDs: []string{"site-1"}},
	{ID: "r-lonely", SiteIDs: nil},
	{ID: "r-multi", SiteIDs: []string{"site-1", "site-2"}},
}

type seed struct {
	roles      [][2]string // id, builtin ("0"/"1")
	rolePages  [][3]string // roleId, page, access
	grants     [][4]string // principalType, principalId, scopeType, scopeId
	grantRoles []string    // roleId per grant, same index
	members    [][2]string // groupId, userId
	sites      [][2]string // id, name
	// roleNames is optional: a role's NAME where it differs from its id. Every
	// test here but the access summary is about what a role CONFERS, so the id
	// serves as the name and this stays nil.
	roleNames map[string]string
}

func build(t *testing.T, s seed) *Resolver {
	t.Helper()
	dir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(grantDDL); err != nil {
		t.Fatal(err)
	}
	for _, r := range s.roles {
		name := r[0]
		if s.roleNames != nil {
			if n, ok := s.roleNames[r[0]]; ok {
				name = n
			}
		}
		if _, err := h.Exec(`INSERT INTO roles (id, name, builtin) VALUES (?, ?, ?)`, r[0], name, r[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range s.rolePages {
		if _, err := h.Exec(`INSERT INTO role_pages (role_id, page, access) VALUES (?, ?, ?)`, p[0], p[1], p[2]); err != nil {
			t.Fatal(err)
		}
	}
	for i, g := range s.grants {
		var scopeID any
		if g[3] != "" {
			scopeID = g[3]
		}
		if _, err := h.Exec(
			`INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id) VALUES (?,?,?,?,?)`,
			g[0], g[1], g[2], scopeID, s.grantRoles[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range s.sites {
		if _, err := h.Exec(
			`INSERT INTO sites (id, name, created_at) VALUES (?, ?, 0)`, st[0], st[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range s.members {
		if _, err := h.Exec(`INSERT INTO group_members (group_id, user_id) VALUES (?, ?)`, m[0], m[1]); err != nil {
			t.Fatal(err)
		}
	}
	h.Close()

	database, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database, func() []Router { return testRouters })
}

func can(t *testing.T, r *Resolver, user, page, access, router string) bool {
	t.Helper()
	ok, err := r.CanPage(user, page, access, router)
	if err != nil {
		t.Fatalf("CanPage(%s,%s,%s,%s): %v", user, page, access, router, err)
	}
	return ok
}

// ── THE case this package was written for ────────────────────────────────────

// TestPerRouterAccessIsNotUnioned reproduces the exact over-permission the note
// in auth.go describes: "a principal holding dns:write on router A and dns:read
// on router B would be offered the write controls on B". The union gate said yes
// to both. This must say yes to A and no to B.
func TestPerRouterAccessIsNotUnioned(t *testing.T) {
	r := build(t, seed{
		roles:     [][2]string{{"role-w", "0"}, {"role-r", "0"}},
		rolePages: [][3]string{{"role-w", "dns", "write"}, {"role-r", "dns", "read"}},
		grants: [][4]string{
			{"user", "u-1", "router", "r-A"},
			{"user", "u-1", "router", "r-B"},
		},
		grantRoles: []string{"role-w", "role-r"},
	})

	if !can(t, r, "u-1", "dns", "write", "r-A") {
		t.Error("dns:write on r-A should be allowed — the grant says so")
	}
	if can(t, r, "u-1", "dns", "write", "r-B") {
		t.Error("dns:write on r-B was ALLOWED — this is the over-permission the " +
			"union gate had, and closing it is the whole point of this package")
	}
	if !can(t, r, "u-1", "dns", "read", "r-B") {
		t.Error("dns:read on r-B should be allowed")
	}
}

// ── scope ────────────────────────────────────────────────────────────────────

func TestGlobalGrantReachesEveryRouter(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-r", "0"}},
		rolePages:  [][3]string{{"role-r", "dns", "read"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"role-r"},
	})
	for _, id := range []string{"r-A", "r-B", "r-lonely"} {
		if !can(t, r, "u-1", "dns", "read", id) {
			t.Errorf("a global grant did not reach %s", id)
		}
	}
}

// TestRouterInheritsItsSitesGrant is the half that fails CLOSED if forgotten,
// which is the quiet direction for a fleet: a principal whose access comes
// entirely from a site would be denied everything.
func TestRouterInheritsItsSitesGrant(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-r", "0"}},
		rolePages:  [][3]string{{"role-r", "dns", "read"}},
		grants:     [][4]string{{"user", "u-1", "site", "site-1"}},
		grantRoles: []string{"role-r"},
	})
	if !can(t, r, "u-1", "dns", "read", "r-A") {
		t.Error("a site grant did not reach a router in that site")
	}
	if can(t, r, "u-1", "dns", "read", "r-lonely") {
		t.Error("a site grant reached a router with no site")
	}
}

// TestRouterGrantConfersNothingSiteWide is the opposite direction, and the one
// that fails OPEN if forgotten.
func TestRouterGrantConfersNothingSiteWide(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-w", "0"}},
		rolePages:  [][3]string{{"role-w", "dns", "write"}},
		grants:     [][4]string{{"user", "u-1", "router", "r-A"}},
		grantRoles: []string{"role-w"},
	})
	if can(t, r, "u-1", "dns", "write", "r-B") {
		t.Error("a router-scoped grant leaked to a sibling router in the same site")
	}
}

// ── groups ───────────────────────────────────────────────────────────────────

// TestGroupMembershipConfersAccess: omitting the group half of the query fails
// closed, locking out a legitimate user rather than admitting a stranger — but
// locked out is still broken.
func TestGroupMembershipConfersAccess(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-w", "0"}},
		rolePages:  [][3]string{{"role-w", "dns", "write"}},
		grants:     [][4]string{{"group", "g-1", "global", ""}},
		grantRoles: []string{"role-w"},
		members:    [][2]string{{"g-1", "u-1"}},
	})
	if !can(t, r, "u-1", "dns", "write", "r-A") {
		t.Error("a grant held by a group the user belongs to conferred nothing")
	}
	if can(t, r, "u-2", "dns", "write", "r-A") {
		t.Error("a group grant reached a user who is not a member")
	}
}

// ── access ranking ───────────────────────────────────────────────────────────

func TestWriteSatisfiesRead(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-w", "0"}},
		rolePages:  [][3]string{{"role-w", "dns", "write"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"role-w"},
	})
	if !can(t, r, "u-1", "dns", "read", "r-A") {
		t.Error("write did not satisfy a read requirement")
	}
}

func TestReadDoesNotSatisfyWrite(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-r", "0"}},
		rolePages:  [][3]string{{"role-r", "dns", "read"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"role-r"},
	})
	if can(t, r, "u-1", "dns", "write", "r-A") {
		t.Error("read satisfied a write requirement")
	}
}

// ── builtin ──────────────────────────────────────────────────────────────────

// TestBuiltinRoleConfersEveryPage: Administrator has no role_pages rows at all,
// so a port that only read the table would deny an administrator everything —
// silently, and only on installs that have one.
func TestBuiltinRoleConfersEveryPage(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-admin", "1"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"role-admin"},
	})
	for _, page := range PageKeys {
		if !can(t, r, "u-1", page, "write", "r-A") {
			t.Errorf("builtin role did not confer write on %q", page)
		}
	}
}

// TestBuiltinCannotConferAnUnknownPage pins the guard ORDER. The unknown-page
// check runs before the graph, so "every page" means every REAL page.
func TestBuiltinCannotConferAnUnknownPage(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-admin", "1"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}},
		grantRoles: []string{"role-admin"},
	})
	for _, page := range []string{"dnss", "", "DNS", "../dns", "nonexistent"} {
		if can(t, r, "u-1", page, "write", "r-A") {
			t.Errorf("an administrator was granted the unknown page %q", page)
		}
	}
}

// ── failing closed ───────────────────────────────────────────────────────────

func TestFailsClosed(t *testing.T) {
	r := build(t, seed{
		roles:      [][2]string{{"role-w", "0"}},
		rolePages:  [][3]string{{"role-w", "dns", "write"}},
		grants:     [][4]string{{"user", "u-1", "global", ""}, {"user", "u-9", "global", ""}},
		grantRoles: []string{"role-w", "role-deleted"},
	})
	for _, tc := range []struct {
		name, user, page, access, routerID string
	}{
		{"no user", "", "dns", "write", "r-A"},
		{"no router", "u-1", "dns", "write", ""},
		{"unknown router", "u-1", "dns", "write", "r-nope"},
		{"unknown access level", "u-1", "dns", "admin", "r-A"},
		{"empty access level", "u-1", "dns", "", "r-A"},
		{"unknown page", "u-1", "nope", "write", "r-A"},
		{"user with no grants", "u-stranger", "dns", "write", "r-A"},
		{"grant naming a deleted role", "u-9", "dns", "write", "r-A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if can(t, r, tc.user, tc.page, tc.access, tc.routerID) {
				t.Error("allowed; every one of these must fail closed")
			}
		})
	}
}

// TestUnavailableResolverAnswersNo: a resolver with no database must not claim
// authority. The CALLER decides what to do about that — see auth.go, which keeps
// the coarser gate rather than locking everyone out.
func TestUnavailableResolverAnswersNo(t *testing.T) {
	var r *Resolver
	if r.Available() {
		t.Error("a nil resolver reported itself available")
	}
	if ok, err := r.CanPage("u-1", "dns", "read", "r-A"); ok || err != nil {
		t.Errorf("nil resolver: got %v, %v", ok, err)
	}
}

// ── the page list must not drift from pages.js ───────────────────────────────

// TestPageKeysMatchLive reads the live registry and fails when the copy in
// pages.go disagrees. SKIPs without MIKRODASH_SRC, like the proplist drift gate —
// which is why the green check mounts the live repo read-only.
func TestPageKeysMatchLive(t *testing.T) {
	src := os.Getenv("MIKRODASH_SRC")
	if src == "" {
		t.Skip("MIKRODASH_SRC not set — mount the live repo to run the page-key drift gate")
	}
	b, err := os.ReadFile(filepath.Join(src, "src", "pages.js"))
	if err != nil {
		t.Skipf("cannot read pages.js: %v", err)
	}
	// CATEGORIES in the same file uses the same `key: '...'` shape, so this
	// asserts CONTAINMENT rather than equality: every key the port claims must
	// exist there. A page removed upstream is what this catches; a category key
	// the port does not list is not an error.
	found := map[string]bool{}
	for _, m := range regexp.MustCompile(`key:\s*'([a-zA-Z0-9_-]+)'`).FindAllSubmatch(b, -1) {
		found[string(m[1])] = true
	}
	for _, k := range PageKeys {
		if !found[k] {
			t.Errorf("PageKeys has %q but pages.js does not — the copy has drifted", k)
		}
	}
	if len(found) < len(PageKeys) {
		t.Errorf("pages.js names %d keys, PageKeys has %d", len(found), len(PageKeys))
	}
}

// #117: A DEVICE IN SEVERAL SITES IS REACHABLE FROM A GRANT ON ANY OF THEM.
//
// The live comment states it outright — "a device in A and B is reachable from a
// grant on EITHER" — and this port matched exactly ONE site until 2026-08-25, so
// a grant on the device's second site conferred nothing.
//
// The failure direction was the safe one: refusing a legitimate operator rather
// than admitting a stranger. It is still a divergence, and a quiet one — an
// operator told "no access" blames their own grants, not the server.
func TestAGrantOnAnySiteReachesTheDevice(t *testing.T) {
	for _, site := range []string{"site-1", "site-2"} {
		t.Run("a grant on "+site, func(t *testing.T) {
			r := build(t, seed{
				roles:      [][2]string{{"role-r", "0"}},
				rolePages:  [][3]string{{"role-r", "dns", "read"}},
				grants:     [][4]string{{"user", "u-1", "site", site}},
				grantRoles: []string{"role-r"},
			})
			if !can(t, r, "u-1", "dns", "read", "r-multi") {
				t.Errorf("a grant on %s did not reach r-multi, which is in it", site)
			}
		})
	}

	// A SITE THE DEVICE IS NOT IN CONFERS NOTHING — without this the cases above
	// would pass on a resolver that ignored the scope entirely.
	t.Run("an unrelated site", func(t *testing.T) {
		r := build(t, seed{
			roles:      [][2]string{{"role-r", "0"}},
			rolePages:  [][3]string{{"role-r", "dns", "read"}},
			grants:     [][4]string{{"user", "u-1", "site", "site-3"}},
			grantRoles: []string{"role-r"},
		})
		if can(t, r, "u-1", "dns", "read", "r-multi") {
			t.Error("a grant on site-3 reached r-multi, which is not in it")
		}
	})

	// And a device in ONE site is unaffected by the widening.
	t.Run("a single-site device still matches", func(t *testing.T) {
		r := build(t, seed{
			roles:      [][2]string{{"role-r", "0"}},
			rolePages:  [][3]string{{"role-r", "dns", "read"}},
			grants:     [][4]string{{"user", "u-1", "site", "site-2"}},
			grantRoles: []string{"role-r"},
		})
		if can(t, r, "u-1", "dns", "read", "r-A") {
			t.Error("a grant on site-2 reached r-A, which is only in site-1")
		}
	})
}
