package server

// The two grant routes.
//
// The route IS the validation — `db.UpsertGrant` takes whatever it is handed —
// so this file is mostly about the five checks and the statuses they carry. Two
// things are worth naming because they are easy to get subtly wrong and neither
// shows up as a failure:
//
//   - a bad ROLE ID is a 400 and a missing SITE is a 404. Both name something
//     that is not there; the split is that one is a malformed field and the
//     other is a well-formed reference to an absent thing.
//   - the checks run IN ORDER, so a request that is wrong in two ways reports
//     the FIRST. A port that validated in a different order would answer a
//     different, equally true, message — and nothing would catch it.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"mikrodash/internal/db"
)

// grantsWriteDDL adds a sites table, so the site-scope existence check has
// something real to find and something real to miss.
// (No backticks in this comment: it sits inside a Go raw string.)
const grantsWriteDDL = `
CREATE TABLE IF NOT EXISTS sites (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  lat REAL, lon REAL, place_name TEXT, place_region TEXT, place_cc TEXT,
  created_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO sites (id, name) VALUES ('site-1','Head Office');
`

func grantsWriteServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	s, mux, _ := usersWriteServer(t, sess, seedUsersJSON, groupsWriteDDL, rolesWriteDDL,
		grantsWriteDDL)
	s.registerGrantsWrite(mux)
	return s, mux
}

func TestGrantCreateValidatesInOrder(t *testing.T) {
	for _, c := range []struct {
		why  string
		body string
		want int
		msg  string
	}{
		{"a global grant on a user", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"global"}`, 200, ""},
		{"a site grant", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"site","scopeId":"site-1"}`, 200, ""},
		{"a group grant", `{"principalType":"group","principalId":"g-1","roleId":"readonly","scopeType":"global"}`, 200, ""},

		// 1. PRINCIPAL TYPE.
		{"no principal type", `{"principalId":"u-1","roleId":"readonly","scopeType":"global"}`, 400, "Invalid principal type"},
		{"an unknown principal type", `{"principalType":"robot","principalId":"u-1","roleId":"readonly","scopeType":"global"}`, 400, "Invalid principal type"},

		// 2. ROLE. A bad role id is a 400 — it is a malformed field, not an
		// absent thing.
		{"no role at all", `{"principalType":"user","principalId":"u-1","scopeType":"global"}`, 400, "Invalid role"},
		{"a role id that names nothing", `{"principalType":"user","principalId":"u-1","roleId":"nosuchrole","scopeType":"global"}`, 400, "Invalid role"},
		{"an unrecognised legacy role name", `{"principalType":"user","principalId":"u-1","role":"superuser","scopeType":"global"}`, 400, "Invalid role"},
		// THE LEGACY NAMES still work, and map onto ids that are not their names.
		{"the legacy name admin", `{"principalType":"user","principalId":"u-1","role":"admin","scopeType":"global"}`, 200, ""},
		{"the legacy name viewer maps to readonly", `{"principalType":"user","principalId":"u-1","role":"viewer","scopeType":"global"}`, 200, ""},

		// 3. SCOPE TYPE.
		{"no scope type", `{"principalType":"user","principalId":"u-1","roleId":"readonly"}`, 400, "Invalid scope type"},
		{"an unknown scope type", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"planet"}`, 400, "Invalid scope type"},

		// 4. A SCOPE ID, for anything but global.
		{"a site grant with no scope id", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"site"}`, 400, "Scope id required"},
		{"a router grant with an empty scope id", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"router","scopeId":""}`, 400, "Scope id required"},
		{"a null scope id", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"site","scopeId":null}`, 400, "Scope id required"},

		// 5. EXISTENCE — 404, not 400.
		{"a site that does not exist", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"site","scopeId":"site-nope"}`, 404, "No such site"},
		{"a router that does not exist", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"router","scopeId":"rtr-nope"}`, 404, "No such router"},
		{"a group that does not exist", `{"principalType":"group","principalId":"g-nope","roleId":"readonly","scopeType":"global"}`, 404, "No such group"},

		// ── THE ORDER ITSELF ────────────────────────────────────────────
		//
		// Each of these is wrong in TWO ways, and the message says which check
		// ran first. Without them a port could validate in any order and every
		// single-fault case above would still pass.
		{"a bad principal type AND a bad role reports the principal type",
			`{"principalType":"robot","principalId":"u-1","roleId":"nosuchrole","scopeType":"global"}`, 400, "Invalid principal type"},
		{"a bad role AND a bad scope type reports the role",
			`{"principalType":"user","principalId":"u-1","roleId":"nosuchrole","scopeType":"planet"}`, 400, "Invalid role"},
		{"a bad scope type AND a missing scope id reports the scope type",
			`{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"planet","scopeId":""}`, 400, "Invalid scope type"},
		{"a missing scope id on a site that does not exist reports the missing id",
			`{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"site"}`, 400, "Scope id required"},
		{"an absent group AND a bad scope id reports the SCOPE first",
			`{"principalType":"group","principalId":"g-nope","roleId":"readonly","scopeType":"site","scopeId":"site-nope"}`, 404, "No such site"},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			before := grantRowCount(t, s)
			w := doJSON(mux, "POST", "/api/grants", c.body, authed)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
			if c.msg != "" && !strings.Contains(w.Body.String(), c.msg) {
				t.Errorf("message is %s, want one containing %q", w.Body.String(), c.msg)
			}
			after := grantRowCount(t, s)
			if c.want != 200 && after != before {
				t.Errorf("a refused request wrote a grant (%d -> %d)", before, after)
			}
		})
	}
}

// TestTheLegacyRoleNamesMapOntoIdsThatAreNotTheirNames.
//
// ── WHY A STATUS ASSERTION IS NOT ENOUGH ───────────────────────────────────
//
// `{admin: 'administrator', operator: 'operator', viewer: 'readonly'}` — two of
// the three map onto an id that is NOT the name sent. Asserting only that the
// request succeeded cannot see a broken map: the fixture happens to contain a
// role whose id is literally `viewer`, so a port that passed the name straight
// through would find a real role and answer 200.
//
// That mutant survived until this test existed. What it changes is WHICH role the
// grant confers — silently, and in the direction of more access if the names ever
// diverge.
func TestTheLegacyRoleNamesMapOntoIdsThatAreNotTheirNames(t *testing.T) {
	for _, c := range []struct{ sent, wantRoleID string }{
		{"admin", "administrator"},
		{"operator", "operator"},
		{"viewer", "readonly"},
	} {
		t.Run(c.sent, func(t *testing.T) {
			s, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			// BELIEVABILITY: a role whose id IS the sent name must exist for
			// `viewer`, or the mutation this test kills would have been caught by
			// the status alone and the case proves nothing.
			if c.sent == "viewer" {
				same, err := s.auditDB.GetRole("viewer")
				if err != nil {
					t.Fatal(err)
				}
				if same == nil {
					t.Skip("the fixture no longer has a role whose id is 'viewer', so passing the " +
						"name straight through would already fail on the status")
				}
			}
			w := doJSON(mux, "POST", "/api/grants",
				`{"principalType":"user","principalId":"u-1","role":"`+c.sent+
					`","scopeType":"site","scopeId":"site-1"}`, authed)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			row := s.findGrant("user", "u-1", "site", "site-1")
			if row == nil {
				t.Fatal("no grant was written")
			}
			if row.RoleID == nil || *row.RoleID != c.wantRoleID {
				t.Errorf("role_id = %v, want %q — the legacy name %q must map onto that id, not "+
					"onto itself", row.RoleID, c.wantRoleID, c.sent)
			}
		})
	}
}

// TestFindGrantMatchesOnScopeTypeAsWellAsId.
//
// Two grants can share a scope ID and differ only in scope TYPE — ids are opaque
// strings and nothing stops a site and a router having the same one. Matching on
// the id alone returns whichever the query happened to order first, and the
// create route would then answer with the wrong row: the right grant is written
// and the browser is told about a different one.
//
// Written after that mutant survived. `TestGrantCreateReturnsTheRow` could not
// see it, because the principal there holds exactly one grant.
func TestFindGrantMatchesOnScopeTypeAsWellAsId(t *testing.T) {
	s, _ := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})

	// The SAME scope id under two different scope types. Written straight through
	// the store: the create route would refuse the router one, because the
	// fixture's routers.json is empty — which is the check working, and not what
	// is under test here.
	for _, scopeType := range []string{"site", "router"} {
		if err := s.auditDB.UpsertGrant(db.GrantSpec{
			PrincipalType: "user", PrincipalID: "u-1", RoleID: "readonly",
			ScopeType: scopeType, ScopeID: "site-1",
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, scopeType := range []string{"site", "router"} {
		row := s.findGrant("user", "u-1", scopeType, "site-1")
		if row == nil {
			t.Fatalf("no grant found for scope type %q", scopeType)
		}
		if row.ScopeType != scopeType {
			t.Errorf("asked for the %q grant and got the %q one — a match on the scope id alone "+
				"returns whichever the query ordered first, so the create route would answer with "+
				"a row it did not write", scopeType, row.ScopeType)
		}
	}
}

func grantRowCount(t *testing.T, s *Server) int {
	t.Helper()
	rows, err := s.auditDB.ListGrants(db.GrantFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

// TestGrantCreateReturnsTheRow.
//
// `UpsertGrant` answers only an error, so the route reads the row back. The
// Access Management card renders from this response, and a null `grant` would
// leave the new row invisible until a reload.
func TestGrantCreateReturnsTheRow(t *testing.T) {
	_, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/grants",
		`{"principalType":"user","principalId":"u-1","roleId":"operator","scopeType":"site","scopeId":"site-1"}`,
		authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Grant *db.GrantRow `json:"grant"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Grant == nil {
		t.Fatal("the response carried no grant; the card renders from this and would show nothing")
	}
	if got.Grant.ID == "" {
		t.Error("the returned grant has no id")
	}
	if got.Grant.ScopeType != "site" {
		t.Errorf("scope_type = %q, want site", got.Grant.ScopeType)
	}
	if got.Grant.ScopeID == nil || *got.Grant.ScopeID != "site-1" {
		t.Errorf("scope_id = %v, want site-1", got.Grant.ScopeID)
	}
}

// TestAGlobalGrantStoresAnEmptyScopeId — never null, never a leftover.
//
// `grantwrite.go` records the reason: SQLite treats NULLs as distinct in a
// UNIQUE index, so a NULL scope_id would let one principal hold two global
// grants and the constraint would silently never fire.
func TestAGlobalGrantStoresAnEmptyScopeId(t *testing.T) {
	s, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	// A scopeId sent ALONGSIDE a global scope must be discarded, not stored.
	w := doJSON(mux, "POST", "/api/grants",
		`{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"global","scopeId":"site-1"}`,
		authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	row := s.findGrant("user", "u-1", "global", "")
	if row == nil {
		t.Fatal("no global grant was written for u-1")
	}
	if row.ScopeID != nil && *row.ScopeID != "" {
		t.Errorf("scope_id = %q; a global grant stores the empty string, so that the unique index "+
			"can actually stop a second one", *row.ScopeID)
	}
}

// TestGrantCreateIsAnUpsert — the same principal and scope twice REPLACES.
func TestGrantCreateIsAnUpsert(t *testing.T) {
	s, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	before := grantRowCount(t, s)
	for _, roleID := range []string{"readonly", "operator"} {
		w := doJSON(mux, "POST", "/api/grants",
			`{"principalType":"user","principalId":"u-1","roleId":"`+roleID+`","scopeType":"site","scopeId":"site-1"}`,
			authed)
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	if after := grantRowCount(t, s); after != before+1 {
		t.Errorf("two grants at one scope left %d rows, want %d — it must REPLACE", after, before+1)
	}
	row := s.findGrant("user", "u-1", "site", "site-1")
	if row == nil || row.RoleID == nil || *row.RoleID != "operator" {
		t.Errorf("the second write did not replace the first: %+v", row)
	}
}

// ── DELETE ─────────────────────────────────────────────────────────────────

func TestGrantDeleteRemovesTheRow(t *testing.T) {
	s, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	// A grant that confers nothing, so the orphan probe has no opinion.
	if err := s.auditDB.UpsertGrant(db.GrantSpec{
		PrincipalType: "user", PrincipalID: "u-1", RoleID: "readonly",
		ScopeType: "site", ScopeID: "site-1",
	}); err != nil {
		t.Fatal(err)
	}
	row := s.findGrant("user", "u-1", "site", "site-1")
	if row == nil {
		t.Fatal("the fixture grant was not written")
	}

	w := doJSON(mux, "DELETE", "/api/grants/"+row.ID, "", authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if s.findGrant("user", "u-1", "site", "site-1") != nil {
		t.Error("the grant survived the delete")
	}
}

func TestGrantDeleteUnknownIdIs404(t *testing.T) {
	_, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "DELETE", "/api/grants/nosuchgrant", "", authed)
	if w.Code != 404 {
		t.Errorf("status %d, want 404 — %s", w.Code, w.Body.String())
	}
}

// TestDeletingTheLastAdminGrantIsRefused — the most direct orphan path there is.
func TestDeletingTheLastAdminGrantIsRefused(t *testing.T) {
	s, mux := grantsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})

	// Leave u-1's global administrator grant as the only source of
	// administration. BOTH of the others have to go: `usersWriteDDL` gives u-2 a
	// global administrator grant of its own — added so that "cannot delete your
	// own account" and "would orphan" stayed separable on the user routes — and
	// `groupsWriteDDL` gives g-1 one that reaches u-2 through the group.
	for _, p := range [][2]string{{"user", "u-2"}, {"group", "g-1"}} {
		if _, err := s.auditDB.DeleteGrantsForPrincipal(p[0], p[1]); err != nil {
			t.Fatal(err)
		}
	}
	admins, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 1 || admins[0] != "u-1" {
		t.Fatalf("administrators are %v; this test needs exactly [u-1]", admins)
	}
	row := s.findGrant("user", "u-1", "global", "")
	if row == nil {
		t.Fatal("u-1 holds no global grant, so there is nothing to refuse the deletion of")
	}

	w := doJSON(mux, "DELETE", "/api/grants/"+row.ID, "", authed)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nobody with administrator access") {
		t.Errorf("refused with %s", w.Body.String())
	}
	// AND THE GRANT SURVIVES — the probe rolls back, and the refusal message
	// says the change did not happen.
	if s.findGrant("user", "u-1", "global", "") == nil {
		t.Error("the grant was deleted anyway; the probe must roll back")
	}
}

func TestGrantWriteRoutesRequireGlobalAdmin(t *testing.T) {
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/grants", `{"principalType":"user","principalId":"u-1","roleId":"readonly","scopeType":"global"}`},
		{"DELETE", "/api/grants/anything", ""},
	} {
		t.Run(c.method, func(t *testing.T) {
			s, mux := grantsWriteServer(t, &Session{AuthMode: "modern", Username: "nobody"})
			before := grantRowCount(t, s)
			w := doJSON(mux, c.method, c.path, c.body, authed)
			if w.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403 — %s", w.Code, w.Body.String())
			}
			if after := grantRowCount(t, s); after != before {
				t.Errorf("a forbidden request changed the grant table (%d -> %d)", before, after)
			}
		})
	}
}
