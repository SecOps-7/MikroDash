package server

// The three role administration routes.
//
// Three properties nothing else can see, and every one of them is silent when
// wrong:
//
//   - the BUILT-IN role refuses edits and deletes. Editing it would either do
//     nothing or silently narrow every administrator in the fleet; deleting it
//     would leave `globalAdminQuery` matching no role at all.
//   - a role still assigned by grants refuses deletion WITH A COUNT, rather than
//     surfacing the foreign-key error.
//   - both validators run before the write, so a role is never created and then
//     rejected.

import (
	"encoding/json"
	"mikrodash/internal/db"
	"net/http"
	"strings"
	"testing"
)

// rolesWriteDDL gives the fixture a CUSTOM role to edit and a role_pages table.
//
// `usersWriteDDL` already inserts `administrator` with `builtin = 1`, which is
// what the refusals are about — so the built-in half needs no extra setup.
// (No backticks in this comment: it sits inside a Go raw string.)
const rolesWriteDDL = `
-- THE UNIQUE NAME. usersGrantsDDL declares roles.name as NOT NULL and nothing
-- more, so a rename onto a taken name SUCCEEDED and the 409 branch was
-- unreachable -- the route was correct and the fixture could not express the
-- constraint it depends on.
CREATE UNIQUE INDEX IF NOT EXISTS roles_name_unique ON roles (name);
-- And the one upsertGrant conflicts on, for the delete-refusal fixtures. It is
-- also declared by usersWriteDDL; repeated here so this DDL stands alone if the
-- harnesses are ever separated.
CREATE UNIQUE INDEX IF NOT EXISTS grants_principal_scope
  ON grants (principal_type, principal_id, scope_type, scope_id);
CREATE TABLE IF NOT EXISTS role_pages (
  role_id TEXT NOT NULL, page TEXT NOT NULL, access TEXT NOT NULL);
INSERT INTO roles (id, name, builtin) VALUES ('r-custom','Support',0);
INSERT INTO role_pages (role_id, page, access) VALUES ('r-custom','dashboard','read');
-- A role nothing references, so the delete path has something that can succeed.
INSERT INTO roles (id, name, builtin) VALUES ('r-unused','Spare',0);
`

func rolesWriteServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	s, mux, _ := usersWriteServer(t, sess, seedUsersJSON, rolesWriteDDL)
	s.registerRolesWrite(mux)
	return s, mux
}

// firstKnownPage is a real page key, read from the catalogue rather than typed.
// A hard-coded key that was renamed upstream would make every "valid pages" case
// silently exercise the unknown-page branch instead.
func firstKnownPage(t *testing.T) string {
	t.Helper()
	if len(pageCatalogue) == 0 {
		t.Fatal("the page catalogue is empty")
	}
	return pageCatalogue[0].Key
}

func TestRoleCreate(t *testing.T) {
	page := firstKnownPage(t)
	for _, c := range []struct {
		why  string
		body string
		want int
	}{
		{"a valid role", `{"name":"Helpdesk"}`, 200},
		{"with a page matrix", `{"name":"Helpdesk","pages":[{"page":"` + page + `","access":"read"}]}`, 200},
		{"no name", `{}`, 400},
		{"a duplicate name", `{"name":"Support"}`, 409},
		{"an unknown page", `{"name":"Helpdesk","pages":[{"page":"nosuchpage","access":"read"}]}`, 400},
		{"a bad access level", `{"name":"Helpdesk","pages":[{"page":"` + page + `","access":"none"}]}`, 400},
		{"pages that are not an array", `{"name":"Helpdesk","pages":"read"}`, 400},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			w := doJSON(mux, "POST", "/api/roles", c.body, authed)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
			roles, err := s.auditDB.ListRoles()
			if err != nil {
				t.Fatal(err)
			}
			// THE FIXTURE SHIPS EIGHT: manager, auditor and viewer from
			// usersGrantsDDL; administrator, operator and readonly from
			// usersWriteDDL; r-custom and r-unused from this file. Counted rather
			// than remembered — the first version said five and every case failed
			// on the count rather than on what it was testing.
			want := 8
			if c.want == 200 {
				want = 9
			}
			if len(roles) != want {
				// The PAGE-REJECTION cases are the point of this assertion: a
				// port that created the role and then validated its pages would
				// leave one behind a 400, and the operator's retry would hit a
				// duplicate-name 409 for a role they cannot see having made.
				t.Errorf("%d role(s) after a %d, want %d", len(roles), w.Code, want)
			}
		})
	}
}

func TestRoleCreateWritesItsPages(t *testing.T) {
	page := firstKnownPage(t)
	s, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/roles",
		`{"name":"Helpdesk","pages":[{"page":"`+page+`","access":"write"}]}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Role struct {
			ID    string           `json:"id"`
			Pages []map[string]any `json:"pages"`
		} `json:"role"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// THE RESPONSE CARRIES THE VIEW, which is what the editor re-renders from —
	// the pages as they now stand rather than as they were sent.
	if len(got.Role.Pages) != 1 || got.Role.Pages[0]["page"] != page {
		t.Fatalf("the response's pages are %v", got.Role.Pages)
	}
	if got.Role.Pages[0]["access"] != "write" {
		t.Errorf("access = %v, want write", got.Role.Pages[0]["access"])
	}
	stored, err := s.auditDB.RolePages(got.Role.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 {
		t.Errorf("%d page(s) stored, want 1", len(stored))
	}
}

// TestTheBuiltinRoleIsRefused — both verbs, and the reason differs.
func TestTheBuiltinRoleIsRefused(t *testing.T) {
	for _, c := range []struct {
		method, body, want string
	}{
		{"PUT", `{"name":"Renamed"}`, "cannot be edited"},
		{"DELETE", "", "cannot be deleted"},
	} {
		t.Run(c.method, func(t *testing.T) {
			s, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			w := doJSON(mux, c.method, "/api/roles/administrator", c.body, authed)
			if w.Code != 400 {
				t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), c.want) {
				t.Errorf("refused with %s, want a message containing %q", w.Body.String(), c.want)
			}
			role, err := s.auditDB.GetRole("administrator")
			if err != nil {
				t.Fatal(err)
			}
			if role == nil {
				t.Fatal("the built-in role was deleted anyway")
			}
			if role.Name != "admin" {
				t.Errorf("the built-in role was renamed to %q", role.Name)
			}
		})
	}
}

// TestTheBuiltinRefusalComesBeforeTheBodyIsParsed.
//
// A malformed edit of the Administrator role must report the reason it could
// never have worked — not a validation error suggesting that fixing the body
// would help. The live route checks `existing.builtin` before `_parseName`.
func TestTheBuiltinRefusalComesBeforeTheBodyIsParsed(t *testing.T) {
	_, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "PUT", "/api/roles/administrator", `{"name":""}`, authed)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot be edited") {
		t.Errorf("refused with %s — an empty name on the built-in role should report that the "+
			"role cannot be edited, not that the name is invalid", w.Body.String())
	}
}

// TestDeletingAnAssignedRoleReportsTheCount.
//
// The foreign key would refuse it anyway; saying HOW MANY grants block it is
// more useful than surfacing a constraint error. The count is singular-aware.
func TestDeletingAnAssignedRoleReportsTheCount(t *testing.T) {
	s, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})

	// ONE grant on the custom role, so the singular form is exercised.
	if err := s.auditDB.UpsertGrant(dbGrantSpecForTest("u-1", "r-custom")); err != nil {
		t.Fatal(err)
	}
	n, err := s.auditDB.CountGrantsForRole("r-custom")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("the fixture leaves %d grant(s) on r-custom; this test needs exactly one", n)
	}

	w := doJSON(mux, "DELETE", "/api/roles/r-custom", "", authed)
	if w.Code != 409 {
		t.Fatalf("status %d, want 409 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "assigned by 1 grant\"") {
		t.Errorf("message is %s — the count must be singular for one grant, which is the half "+
			"an off-by-one would leave looking like a typo", w.Body.String())
	}
	role, err := s.auditDB.GetRole("r-custom")
	if err != nil {
		t.Fatal(err)
	}
	if role == nil {
		t.Error("the role was deleted anyway")
	}
}

func TestDeletingAnAssignedRoleIsPluralForTwo(t *testing.T) {
	s, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	for _, u := range []string{"u-1", "u-2"} {
		g := dbGrantSpecForTest(u, "r-custom")
		g.ScopeType, g.ScopeID = "router", "rtr-"+u
		if err := s.auditDB.UpsertGrant(g); err != nil {
			t.Fatal(err)
		}
	}
	w := doJSON(mux, "DELETE", "/api/roles/r-custom", "", authed)
	if w.Code != 409 {
		t.Fatalf("status %d, want 409 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "assigned by 2 grants") {
		t.Errorf("message is %s, want the plural form", w.Body.String())
	}
}

func TestRoleUpdateAndDeleteStatuses(t *testing.T) {
	for _, c := range []struct {
		why            string
		method, id, bd string
		want           int
	}{
		{"rename a custom role", "PUT", "r-custom", `{"name":"Renamed"}`, 200},
		{"rename to a taken name", "PUT", "r-custom", `{"name":"Spare"}`, 409},
		{"rename to empty", "PUT", "r-custom", `{"name":""}`, 400},
		{"an empty patch", "PUT", "r-custom", `{}`, 200},
		{"update an unknown role", "PUT", "r-nope", `{"name":"X"}`, 404},
		{"delete an unassigned role", "DELETE", "r-unused", "", 200},
		{"delete an unknown role", "DELETE", "r-nope", "", 404},
	} {
		t.Run(c.why, func(t *testing.T) {
			_, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			w := doJSON(mux, c.method, "/api/roles/"+c.id, c.bd, authed)
			if w.Code != c.want {
				t.Errorf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// TestRoleUpdateReplacesThePageMatrix — and an ABSENT one leaves it alone.
//
// The pair `ParseRolePages` exists to keep apart. An absent `pages` key on a
// rename must not strip the role's pages; an explicit empty array must.
func TestRoleUpdateReplacesThePageMatrix(t *testing.T) {
	page := firstKnownPage(t)
	for _, c := range []struct {
		why   string
		body  string
		wantN int
	}{
		{"a rename leaves the pages alone", `{"name":"Renamed"}`, 1},
		{"an empty array revokes them", `{"pages":[]}`, 0},
		{"a new matrix replaces them", `{"pages":[{"page":"` + page + `","access":"write"}]}`, 1},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux := rolesWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			before, err := s.auditDB.RolePages("r-custom")
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != 1 {
				t.Fatalf("the fixture gives r-custom %d page(s); this test needs one", len(before))
			}
			w := doJSON(mux, "PUT", "/api/roles/r-custom", c.body, authed)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			after, err := s.auditDB.RolePages("r-custom")
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != c.wantN {
				t.Errorf("%d page(s), want %d — an ABSENT pages key means leave them alone and an "+
					"EMPTY ARRAY means revoke them, and collapsing the two either strips a role "+
					"on a rename or ignores a revocation", len(after), c.wantN)
			}
		})
	}
}

func TestRoleWriteRoutesRequireGlobalAdmin(t *testing.T) {
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/roles", `{"name":"Helpdesk"}`},
		{"PUT", "/api/roles/r-custom", `{"name":"Renamed"}`},
		{"DELETE", "/api/roles/r-unused", ""},
	} {
		t.Run(c.method, func(t *testing.T) {
			s, mux := rolesWriteServer(t, &Session{AuthMode: "modern", Username: "nobody"})
			w := doJSON(mux, c.method, c.path, c.body, authed)
			if w.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403 — %s", w.Code, w.Body.String())
			}
			roles, err := s.auditDB.ListRoles()
			if err != nil {
				t.Fatal(err)
			}
			if len(roles) != 8 {
				t.Errorf("a forbidden request left %d role(s)", len(roles))
			}
		})
	}
}

// dbGrantSpecForTest is a global grant on one role, for the delete-refusal
// tests. Global by default because the scope is irrelevant to a COUNT.
func dbGrantSpecForTest(userID, roleID string) db.GrantSpec {
	return db.GrantSpec{
		PrincipalType: "user", PrincipalID: userID,
		RoleID: roleID, ScopeType: "global",
	}
}
