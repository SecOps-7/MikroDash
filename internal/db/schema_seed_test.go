package db

import (
	"testing"

	"mikrodash/internal/pages"
)

// ── THE SEEDED MATRIX MUST NAME REAL PAGES ─────────────────────────────────
//
// `role_pages.page` is a permission key, and an unknown one is DENIED before any
// role is consulted. So a seed naming a page that no longer exists does not
// error — it silently gives the readonly and operator roles nothing on that
// page, on every install created afterwards.
//
// This is the same failure the 2026-09-01 rename caused for grants already
// stored, which `pages.Renamed` and `RenamePageGrants` exist to repair. The seed
// is the other end of it: those keys are written fresh, so they must be current
// rather than repaired.
func TestTheSeededRolePagesNameRealPages(t *testing.T) {
	for _, p := range roleReadPages {
		if !pages.Has(p) {
			t.Errorf("the role seed grants %q, which is not a page key — every new "+
				"install would give readonly and operator nothing on it", p)
		}
		// AND IT MUST NOT BE A RENAMED KEY. `pages.Has` would already be false,
		// but naming the replacement makes the fix obvious rather than a hunt.
		if now, renamed := pages.Renamed[p]; renamed {
			t.Errorf("the role seed grants %q, which was renamed to %q", p, now)
		}
	}
	for p := range operatorWritePages {
		if !pages.Has(p) {
			t.Errorf("operator is seeded with write on %q, which is not a page key", p)
		}
	}
	// Believability: an empty list would pass every assertion above.
	if len(roleReadPages) < 10 {
		t.Fatalf("the seed lists only %d pages — it has been truncated", len(roleReadPages))
	}
}

// The three builtin roles are what `grants.role_id` REFERENCES. A fresh install
// without them takes a FOREIGN KEY failure on the first administrator's grant,
// which is how this was found: the database existed, the tables existed, and the
// account still could do nothing.
func TestTheBuiltinRolesAreSeeded(t *testing.T) {
	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	for _, want := range []string{"administrator", "operator", "readonly"} {
		var n int
		if err := d.sql.QueryRow(`SELECT COUNT(*) FROM roles WHERE id = ?`, want).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("role %q is not seeded", want)
		}
	}

	// Administrator holds NO page rows: its reach is structural, so a page added
	// in a later release is covered with no data change. Seeding rows for it
	// would freeze that list at today's pages.
	var admin int
	if err := d.sql.QueryRow(
		`SELECT COUNT(*) FROM role_pages WHERE role_id = 'administrator'`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	if admin != 0 {
		t.Errorf("administrator has %d role_pages rows; its reach is structural", admin)
	}

	// Read Only has no `reports` row, and that is deliberate: a reports row
	// confers router:history, which would hand every viewer historical exports
	// they do not have today. Operator does have it.
	var roReports, opReports int
	_ = d.sql.QueryRow(
		`SELECT COUNT(*) FROM role_pages WHERE role_id='readonly' AND page='reports'`).Scan(&roReports)
	_ = d.sql.QueryRow(
		`SELECT COUNT(*) FROM role_pages WHERE role_id='operator' AND page='reports'`).Scan(&opReports)
	if roReports != 0 {
		t.Error("readonly was seeded with reports, which confers router:history")
	}
	if opReports != 1 {
		t.Error("operator is missing its reports row")
	}
}
