package db

// `GlobalAdminUserIDs` against the LIVE query, run by
// `tools/global-admin-cases.js` over the LIVE schema.

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

type adminCase struct {
	Roles []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Builtin bool   `json:"builtin"`
	} `json:"roles"`
	Grants []struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Role    string `json:"role"`
		Scope   string `json:"scope"`
		ScopeID string `json:"scopeId"`
	} `json:"grants"`
	Members []struct {
		Group string `json:"group"`
		User  string `json:"user"`
	} `json:"members"`
	AdminIDs []string `json:"adminIds"`
}

type adminCorpus struct {
	Query string               `json:"query"`
	Cases map[string]adminCase `json:"cases"`
}

// adminDDL is the live shape of the four tables this query touches, checked by
// tools/schema-audit.js. The migrations seed exactly ONE builtin role, which is
// what makes a global readonly grant not administrator access.
// (No backticks in this comment: it sits inside a Go raw string.)
const adminDDL = `
CREATE TABLE roles (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  builtin INTEGER NOT NULL, created_at INTEGER NOT NULL);
CREATE TABLE groups (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT,
  created_at INTEGER NOT NULL);
CREATE TABLE group_members (group_id TEXT NOT NULL, user_id TEXT NOT NULL);
CREATE TABLE grants (
  id             TEXT PRIMARY KEY DEFAULT (hex(randomblob(16))),
  principal_type TEXT NOT NULL, principal_id TEXT NOT NULL,
  -- THE FOREIGN KEY IS PART OF THE BEHAVIOUR, not decoration: ON DELETE
  -- RESTRICT is what refuses deleting a role a grant still holds, and
  -- internal/db/rolewrite.go relies on the ENGINE for that rather than
  -- re-checking it. A fixture without it deletes the role happily and the test
  -- that exists to pin the refusal passes against a port that has none.
  role_id        TEXT NOT NULL REFERENCES roles(id) ON DELETE RESTRICT, role TEXT,
  scope_type     TEXT NOT NULL, scope_id TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL, created_by TEXT);
INSERT INTO roles (id, name, builtin, created_at) VALUES ('administrator','Administrator',1,0);
INSERT INTO roles (id, name, builtin, created_at) VALUES ('readonly','Read only',0,0);
`

func loadAdminCorpus(t *testing.T) adminCorpus {
	t.Helper()
	b, err := os.ReadFile("../../testdata/global-admin-cases.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c adminCorpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(c.Cases) == 0 {
		t.Fatal("the corpus is empty -- this test would pass against nothing")
	}
	return c
}

func adminDB(t *testing.T, tc adminCase) *DB {
	t.Helper()
	dir := newDB(t, MinSchema, false)
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(adminDDL); err != nil {
		t.Fatal(err)
	}
	for _, r := range tc.Roles {
		b := 0
		if r.Builtin {
			b = 1
		}
		if _, err := h.Exec(
			`INSERT INTO roles (id, name, builtin, created_at) VALUES (?, ?, ?, 0)`,
			r.ID, r.Name, b); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	addGroup := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		if _, err := h.Exec(
			`INSERT INTO groups (id, name, created_at) VALUES (?, ?, 0)`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range tc.Grants {
		if g.Type == "group" {
			addGroup(g.ID)
		}
		if _, err := h.Exec(`INSERT INTO grants
		    (principal_type, principal_id, role_id, scope_type, scope_id, created_at)
		  VALUES (?, ?, ?, ?, ?, 0)`,
			g.Type, g.ID, g.Role, g.Scope, g.ScopeID); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range tc.Members {
		addGroup(m.Group)
		if _, err := h.Exec(
			`INSERT INTO group_members (group_id, user_id) VALUES (?, ?)`,
			m.Group, m.User); err != nil {
			t.Fatal(err)
		}
	}
	_ = h.Close()
	return openTest(t, dir)
}

func TestGlobalAdminMatchesLive(t *testing.T) {
	c := loadAdminCorpus(t)

	// THE QUERY ITSELF must still be the live one. A corpus regenerated from a
	// CHANGED original would otherwise silently redefine what this port must do,
	// and every case below would agree with the new definition.
	if normSQL(c.Query) != normSQL(globalAdminQuery) {
		t.Errorf("the live query has changed:\n  live %s\n  port %s",
			normSQL(c.Query), normSQL(globalAdminQuery))
	}

	// Believability: both answers appear, or a port returning a constant passes.
	var empty, nonEmpty bool
	for _, tc := range c.Cases {
		if len(tc.AdminIDs) == 0 {
			empty = true
		} else {
			nonEmpty = true
		}
	}
	if !empty || !nonEmpty {
		t.Fatal("every case answers the same way, so this corpus cannot tell a working " +
			"query from a constant")
	}

	for name, tc := range c.Cases {
		t.Run(name, func(t *testing.T) {
			d := adminDB(t, tc)
			got, err := d.GlobalAdminUserIDs()
			if err != nil {
				t.Fatal(err)
			}
			sort.Strings(got)
			want := append([]string{}, tc.AdminIDs...)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("%v, live %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("%v, live %v", got, want)
				}
			}
		})
	}
}

// TestWouldOrphanNeverAppliesTheChange.
//
// The guard runs the mutation to ask the question and must leave no trace. A
// version that committed would DELETE the last administrator's grant while
// reporting that doing so would orphan everybody — the worst of both answers.
func TestWouldOrphanNeverAppliesTheChange(t *testing.T) {
	d := adminDB(t, loadAdminCorpus(t).Cases["aDirectGlobalAdmin"])

	before, err := d.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("the fixture starts with %d admins, want 1", len(before))
	}

	orphan, err := d.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		_, e := tx.Exec(`DELETE FROM grants`)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if !orphan {
		t.Error("deleting every grant did not report an orphan")
	}

	after, err := d.GlobalAdminUserIDs()
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Errorf("the probe APPLIED its mutation: %d admins left, want 1", len(after))
	}
	var grants int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants == 0 {
		t.Error("the grants were really deleted -- the probe committed")
	}
}

// TestAChangeThatLeavesAnAdminIsAllowed.
//
// The other direction. Without it, "orphan" could be true of everything and
// every principal write would be refused forever.
func TestAChangeThatLeavesAnAdminIsAllowed(t *testing.T) {
	d := adminDB(t, loadAdminCorpus(t).Cases["aMixedInstall"])

	orphan, err := d.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		// Remove ONE of the two administrators.
		_, e := tx.Exec(`DELETE FROM grants WHERE principal_id = 'u-1'`)
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if orphan {
		t.Error("removing one of two administrators reported an orphan")
	}
}

// TestAFailingMutationIsAnErrorNotAnAnswer.
//
// Reporting "this would not orphan anybody" because the query failed is how the
// last administrator gets removed by the check meant to prevent it.
func TestAFailingMutationIsAnErrorNotAnAnswer(t *testing.T) {
	d := adminDB(t, loadAdminCorpus(t).Cases["aDirectGlobalAdmin"])

	orphan, err := d.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		_, e := tx.Exec(`DELETE FROM no_such_table`)
		return e
	})
	if err == nil {
		t.Error("a failing mutation was not reported")
	}
	if orphan {
		t.Error("a failure answered the question as well")
	}
}

// TestAFailingCOUNTIsAnErrorToo.
//
// The mutation succeeding and the QUESTION failing is a different path from the
// mutation failing, and it is the more dangerous one: the change is sitting in
// the transaction, the guard cannot tell whether it orphaned anybody, and
// answering `false` says "go ahead". A mutation that drops the roles table makes
// the count query fail while the mutation itself succeeds.
func TestAFailingCOUNTIsAnErrorToo(t *testing.T) {
	d := adminDB(t, loadAdminCorpus(t).Cases["aDirectGlobalAdmin"])

	orphan, err := d.WouldOrphanGlobalAdmin(func(tx *sql.Tx) error {
		// SUCCEEDS. What breaks is the question asked afterwards.
		_, e := tx.Exec(`DROP TABLE roles`)
		return e
	})
	if err == nil {
		t.Error("the count failed and the guard reported no error -- a change it could not " +
			"evaluate was about to be described as safe")
	}
	if orphan {
		t.Error("a failure answered the question as well")
	}

	// ...and the drop was rolled back with everything else.
	var roles int
	if err := d.sql.QueryRow(`SELECT COUNT(*) FROM roles`).Scan(&roles); err != nil {
		t.Fatalf("the roles table did not survive the probe: %v", err)
	}
	if roles == 0 {
		t.Error("the probe left the roles table empty")
	}
}

// normSQL collapses whitespace so the comparison is about the QUERY and not its
// indentation, which differs between a JS template literal and a Go constant.
func normSQL(s string) string {
	out := make([]rune, 0, len(s))
	space := true
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' {
			if !space {
				out = append(out, ' ')
			}
			space = true
			continue
		}
		space = false
		out = append(out, r)
	}
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}
