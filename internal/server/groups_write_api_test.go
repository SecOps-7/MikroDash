package server

// The three group administration routes.
//
// The DB writers beneath them are already tested in `internal/db`. What this
// covers is the route's own decisions, and the two that matter are both orphan
// guards nothing else can see:
//
//   - EMPTYING the group that holds the only global admin grant. Nobody's
//     account is touched and nobody is deleted; the membership list is simply
//     replaced with one that no longer contains them.
//   - DELETING that group, which takes its grants with it.
//
// Plus the ORDER in the update route: the probe runs before the name is
// written, so a refused membership change refuses the whole request.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// groupsWriteDDL adds a groups table and a group that CONFERS ADMINISTRATION.
//
// The group grant is what makes the two probes testable at all: a fixture where
// every administrator holds their grant directly cannot express "the last
// administrator is an administrator BECAUSE of this group".
// (No backticks in this comment: it sits inside a Go raw string.)
const groupsWriteDDL = `
CREATE TABLE IF NOT EXISTS groups (
  id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT,
  created_at INTEGER NOT NULL DEFAULT 0);
INSERT INTO groups (id, name) VALUES ('g-1','Operators'), ('g-2','Auditors');
-- g-1 holds a GLOBAL grant with a BUILTIN role, and u-2 is its only member. So
-- u-2 administers the install THROUGH the group, and neither emptying g-1 nor
-- deleting it touches u-2's own record.
INSERT INTO grants (principal_type, principal_id, scope_type, scope_id, role_id, created_at)
  VALUES ('group','g-1','global','','administrator',7);
INSERT INTO group_members (group_id, user_id) VALUES ('g-1','u-2');
`

func groupsWriteServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	s, mux, _ := usersWriteServer(t, sess, seedUsersJSON, groupsWriteDDL)
	s.registerGroupsWrite(mux)
	return s, mux
}

func TestGroupCreate(t *testing.T) {
	for _, c := range []struct {
		why  string
		body string
		want int
	}{
		{"a valid group", `{"name":"Support"}`, 200},
		{"with a description", `{"name":"Support","description":"the support desk"}`, 200},
		{"no name", `{}`, 400},
		{"an empty name", `{"name":""}`, 400},
		{"a name of spaces", `{"name":"   "}`, 400},
		{"a duplicate name", `{"name":"Operators"}`, 409},
		{"a body that is not JSON", `nope`, 400},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux := groupsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			w := doJSON(mux, "POST", "/api/groups", c.body, authed)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
			groups, err := s.auditDB.ListGroups()
			if err != nil {
				t.Fatal(err)
			}
			// A REFUSAL WRITES NOTHING. The fixture ships two groups.
			if c.want != 200 && len(groups) != 2 {
				t.Errorf("a refused request left %d group(s)", len(groups))
			}
			if c.want == 200 && len(groups) != 3 {
				t.Errorf("a successful create left %d group(s)", len(groups))
			}
		})
	}
}

func TestGroupCreateWritesItsMembership(t *testing.T) {
	s, mux := groupsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/groups", `{"name":"Support","memberUserIds":["u-1","u-2"]}`, authed)
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Group struct {
			ID string `json:"id"`
		} `json:"group"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	members, err := s.auditDB.GroupMembers(got.Group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Errorf("the new group has %d member(s), want 2", len(members))
	}
}

// TestGroupMembershipIsArrayGuarded.
//
// `Array.isArray(req.body.memberUserIds)` — a string, a number or null LEAVES
// the membership alone rather than emptying it. Same rule as `allowedRouterIds`
// on the user routes, and the failure is the same: a client that sent the wrong
// shape would silently empty a group.
func TestGroupMembershipIsArrayGuarded(t *testing.T) {
	for _, c := range []struct {
		why  string
		body string
		want int
	}{
		{"a string", `{"memberUserIds":"u-1"}`, 1},
		{"null", `{"memberUserIds":null}`, 1},
		{"a number", `{"memberUserIds":7}`, 1},
		{"an absent key", `{"name":"Renamed"}`, 1},
		// AN EXPLICIT EMPTY ARRAY DOES get through — otherwise "remove everybody
		// from this group" would be impossible. It is refused here only because
		// g-1 is what confers administration; g-2 is the one to empty.
		{"an explicit empty array on a harmless group", `{"memberUserIds":[]}`, 0},
	} {
		t.Run(c.why, func(t *testing.T) {
			s, mux := groupsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			target := "g-1"
			if c.want == 0 {
				// g-2 holds no grants, so emptying it orphans nobody.
				target = "g-2"
				if _, err := s.auditDB.SetGroupMembers("g-2", []string{"u-1"}); err != nil {
					t.Fatal(err)
				}
			}
			w := doJSON(mux, "PUT", "/api/groups/"+target, c.body, authed)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			members, err := s.auditDB.GroupMembers(target)
			if err != nil {
				t.Fatal(err)
			}
			if len(members) != c.want {
				t.Errorf("%s has %d member(s), want %d", target, len(members), c.want)
			}
		})
	}
}

// TestEmptyingTheAdminGroupIsRefused — the least obvious orphan path.
func TestEmptyingTheAdminGroupIsRefused(t *testing.T) {
	s, mux := groupsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})

	// Leave the GROUP as the only source of administration.
	if _, err := s.auditDB.DeleteGrantsForPrincipal("user", "u-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.auditDB.DeleteGrantsForPrincipal("user", "u-2"); err != nil {
		t.Fatal(err)
	}
	admins, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	// BELIEVABILITY: u-2 must be an administrator, and only through g-1. If the
	// fixture left nobody, every write would be refused and this would pass
	// without exercising the guard.
	if len(admins) != 1 || admins[0] != "u-2" {
		t.Fatalf("administrators are %v; this test needs exactly [u-2], held through g-1", admins)
	}

	w := doJSON(mux, "PUT", "/api/groups/g-1", `{"name":"Renamed","memberUserIds":[]}`, authed)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "nobody with administrator access") {
		t.Errorf("refused with %s", w.Body.String())
	}

	// THE MEMBERSHIP IS INTACT — the probe rolls back.
	members, err := s.auditDB.GroupMembers("g-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Errorf("g-1 has %d member(s) after the refusal; the probe must roll back", len(members))
	}
	// AND SO IS THE NAME. The probe runs BEFORE the update, so the whole request
	// is refused rather than the dangerous half of it — a partial edit behind a
	// 400 is worse than no edit.
	group, err := s.auditDB.GetGroup("g-1")
	if err != nil {
		t.Fatal(err)
	}
	if group.Name != "Operators" {
		t.Errorf("the group was renamed to %q behind a 400. The membership probe must run before "+
			"the name is written.", group.Name)
	}
}

// TestDeletingTheAdminGroupIsRefused — the blunter path to the same place.
func TestDeletingTheAdminGroupIsRefused(t *testing.T) {
	s, mux := groupsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
	for _, u := range []string{"u-1", "u-2"} {
		if _, err := s.auditDB.DeleteGrantsForPrincipal("user", u); err != nil {
			t.Fatal(err)
		}
	}
	admins, err := s.auditDB.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(admins) != 1 || admins[0] != "u-2" {
		t.Fatalf("administrators are %v; this test needs exactly [u-2], held through g-1", admins)
	}

	w := doJSON(mux, "DELETE", "/api/groups/g-1", "", authed)
	if w.Code != 400 {
		t.Fatalf("status %d, want 400 — %s", w.Code, w.Body.String())
	}
	group, err := s.auditDB.GetGroup("g-1")
	if err != nil {
		t.Fatal(err)
	}
	if group == nil {
		t.Error("the group was deleted anyway; the probe must run BEFORE the delete")
	}
}

func TestGroupUpdateAndDeleteStatuses(t *testing.T) {
	for _, c := range []struct {
		why            string
		method, id, bd string
		want           int
	}{
		{"rename", "PUT", "g-2", `{"name":"Renamed"}`, 200},
		{"rename to a taken name", "PUT", "g-2", `{"name":"Operators"}`, 409},
		{"rename to empty", "PUT", "g-2", `{"name":""}`, 400},
		{"an empty patch leaves it alone", "PUT", "g-2", `{}`, 200},
		{"update an unknown group", "PUT", "g-nope", `{"name":"X"}`, 404},
		{"delete a group that confers nothing", "DELETE", "g-2", "", 200},
		{"delete an unknown group", "DELETE", "g-nope", "", 404},
	} {
		t.Run(c.why, func(t *testing.T) {
			_, mux := groupsWriteServer(t, &Session{AuthMode: "none", Username: "admin"})
			w := doJSON(mux, c.method, "/api/groups/"+c.id, c.bd, authed)
			if w.Code != c.want {
				t.Errorf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
		})
	}
}

// TestGroupWriteRoutesRequireGlobalAdmin — the security boundary.
//
// Everything above runs as `AuthMode: "none"`, where `isGlobalAdmin`
// short-circuits to true.
func TestGroupWriteRoutesRequireGlobalAdmin(t *testing.T) {
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/groups", `{"name":"Support"}`},
		{"PUT", "/api/groups/g-2", `{"name":"Renamed"}`},
		{"DELETE", "/api/groups/g-2", ""},
	} {
		t.Run(c.method, func(t *testing.T) {
			s, mux := groupsWriteServer(t, &Session{AuthMode: "modern", Username: "nobody"})
			w := doJSON(mux, c.method, c.path, c.body, authed)
			if w.Code != http.StatusForbidden {
				t.Errorf("status %d, want 403 — %s", w.Code, w.Body.String())
			}
			groups, err := s.auditDB.ListGroups()
			if err != nil {
				t.Fatal(err)
			}
			if len(groups) != 2 {
				t.Errorf("a forbidden request left %d group(s)", len(groups))
			}
		})
	}
}
