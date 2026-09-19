package db

import "testing"

// TestMigrationSeventeenSurvivesADeletedBuiltinRole. Operator and Read Only are
// deletable (builtin=0), and migration 17 inserted role_pages rows naming them.
// OR IGNORE does not cover a FOREIGN KEY failure, so an install that had deleted
// either role failed 17 on every boot, and 18 -- the assistant's history table
// -- never ran behind it (review loop, Medium 3).
func TestMigrationSeventeenSurvivesADeletedBuiltinRole(t *testing.T) {
	d := openTest(t, t.TempDir())
	for _, stmt := range []string{
		`DELETE FROM role_pages WHERE role_id = 'readonly'`,
		`DELETE FROM grants WHERE role_id = 'readonly'`,
		`DELETE FROM roles WHERE id = 'readonly'`,
		`DROP TABLE ai_messages`,
		`DELETE FROM schema_version WHERE version >= 17`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate on an install without Read Only: %v", err)
	}
	if v, _ := d.SchemaVersion(); v < 18 {
		t.Errorf("schema is v%d after Migrate, want at least 18", v)
	}
	if _, err := d.sql.Exec(`SELECT count(*) FROM ai_messages`); err != nil {
		t.Errorf("ai_messages was not created: %v", err)
	}
	// The role that remains still gets its grant: the control.
	var n int
	if err := d.sql.QueryRow(
		`SELECT count(*) FROM role_pages WHERE role_id = 'operator' AND page = 'ai-agent'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("operator's ai-agent grant: %d rows (%v), want 1", n, err)
	}
}
