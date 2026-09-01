package db

import (
	"testing"

	"mikrodash/internal/pages"
)

// grantRows reads the table back as role -> page -> access, so an assertion can
// name what it expects instead of walking rows.
func grantRows(t *testing.T, d *DB) map[string]map[string]string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT role_id, page, access FROM role_pages`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var role, page, access string
		if err := rows.Scan(&role, &page, &access); err != nil {
			t.Fatal(err)
		}
		if out[role] == nil {
			out[role] = map[string]string{}
		}
		out[role][page] = access
	}
	return out
}

// OR IGNORE on the role because adminDDL already seeds `readonly` and
// `administrator`; the grants are the point, not the role row.
func seedRole(t *testing.T, d *DB, role string, grants map[string]string) {
	t.Helper()
	if _, err := d.sql.Exec(
		`INSERT OR IGNORE INTO roles (id, name, builtin, created_at) VALUES (?, ?, 0, 0)`,
		role, role); err != nil {
		t.Fatal(err)
	}
	for page, access := range grants {
		if _, err := d.sql.Exec(
			`INSERT INTO role_pages (role_id, page, access) VALUES (?, ?, ?)`,
			role, page, access); err != nil {
			t.Fatal(err)
		}
	}
}

// THE BUG THIS EXISTS FOR, reproduced from the install it was found on: grants
// naming `topology` and `wireless` after those pages were renamed, so the roles
// holding them had silently lost Network Topology and Wifi Clients.
func TestRenamePageGrantsMovesStaleKeys(t *testing.T) {
	d := roleDB(t)
	seedRole(t, d, "operator", map[string]string{
		"topology": "read",
		"wireless": "write",
		"logs":     "read", // a current key, which must not move
	})

	n, err := d.RenamePageGrants()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("moved %d grants, want 2", n)
	}

	got := grantRows(t, d)["operator"]
	// The ACCESS travels with the grant. Moving the row but dropping "write"
	// back to "read" would be a quieter version of the same bug.
	if got["network-topology"] != "read" {
		t.Errorf("network-topology = %q, want read", got["network-topology"])
	}
	if got["wifi-clients"] != "write" {
		t.Errorf("wifi-clients = %q, want write", got["wifi-clients"])
	}
	if got["logs"] != "read" {
		t.Errorf("logs = %q, want read — an unrenamed page must not move", got["logs"])
	}
	if _, stale := got["topology"]; stale {
		t.Error("the stale `topology` row survived")
	}
	if _, stale := got["wireless"]; stale {
		t.Error("the stale `wireless` row survived")
	}
}

// role_pages is PRIMARY KEY (role_id, page), so a role holding BOTH names would
// collide on update. The newer grant wins and the stale row still goes: without
// OR IGNORE this is not a wrong answer but a hard error, taking the whole
// startup fixup down for every other role too.
func TestRenamePageGrantsSurvivesARoleHoldingBothNames(t *testing.T) {
	d := roleDB(t)
	seedRole(t, d, "readonly", map[string]string{
		"wireless":     "write",
		"wifi-clients": "read",
	})

	if _, err := d.RenamePageGrants(); err != nil {
		t.Fatalf("collided instead of ignoring: %v", err)
	}

	got := grantRows(t, d)["readonly"]
	if got["wifi-clients"] != "read" {
		t.Errorf("wifi-clients = %q, want the existing `read` kept", got["wifi-clients"])
	}
	if _, stale := got["wireless"]; stale {
		t.Error("the stale `wireless` row survived the collision")
	}
}

// It runs on every startup, so converging matters as much as being right: a
// second pass must find nothing and change nothing.
func TestRenamePageGrantsIsIdempotent(t *testing.T) {
	d := roleDB(t)
	seedRole(t, d, "operator", map[string]string{"topology": "read"})

	if _, err := d.RenamePageGrants(); err != nil {
		t.Fatal(err)
	}
	first := grantRows(t, d)

	n, err := d.RenamePageGrants()
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("second pass moved %d grants, want 0", n)
	}
	second := grantRows(t, d)
	if len(first["operator"]) != len(second["operator"]) ||
		first["operator"]["network-topology"] != second["operator"]["network-topology"] {
		t.Errorf("second pass changed the table: %v -> %v", first, second)
	}
}

// The page the operator actually asked about. Guarding it by name because it is
// the rename that prompted the ledger, and a regression here is invisible.
func TestRouterUsersGrantsReachTheUsersPage(t *testing.T) {
	if pages.Renamed["router-users"] != "users" {
		t.Fatalf("router-users maps to %q, want users", pages.Renamed["router-users"])
	}
	d := roleDB(t)
	seedRole(t, d, "operator", map[string]string{"router-users": "write"})

	if _, err := d.RenamePageGrants(); err != nil {
		t.Fatal(err)
	}
	if got := grantRows(t, d)["operator"]["users"]; got != "write" {
		t.Errorf("users = %q, want write", got)
	}
}

// A handle with no database must not panic: `adb` is nil whenever Open failed,
// and main.go calls this on the success path only today. Cheap to hold.
func TestRenamePageGrantsOnNilDB(t *testing.T) {
	var d *DB
	if n, err := d.RenamePageGrants(); n != 0 || err != nil {
		t.Errorf("nil DB returned (%d, %v)", n, err)
	}
}
