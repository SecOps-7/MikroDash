package db

import "testing"

// Migration 22: user_layouts learns the 'lang' kind, the account's copy of the
// interface language (#94).
//
// ── A REBUILD, SO THE RISK IS THE ROWS ALREADY THERE ────────────────────────
//
// SQLite cannot alter a CHECK, so the table is copied into a new one and
// renamed into place. A copy that dropped a column, or a row, would lose every
// user's saved dashboard, topology and sidebar, and nothing would fail: the
// pages simply open at their defaults. So this winds a database back to the old
// table, fills it, migrates, and reads every row back.
func TestMigrationTwentyTwoAddsLangAndKeepsTheLayouts(t *testing.T) {
	d := openTest(t, t.TempDir())

	for _, stmt := range []string{
		`DROP TABLE user_layouts`,
		`CREATE TABLE "user_layouts" (
          user_id    TEXT NOT NULL,
          kind       TEXT NOT NULL CHECK (kind IN ('dashboard','topology','nav')),
          data       TEXT NOT NULL,
          updated_at INTEGER NOT NULL,
          PRIMARY KEY (user_id, kind)
        )`,
		`INSERT INTO user_layouts VALUES ('u1', 'dashboard', '{"cards":[1,2]}', 1758000000001)`,
		`INSERT INTO user_layouts VALUES ('u1', 'nav', '{"grouped":true,"expanded":[]}', 1758000000002)`,
		`INSERT INTO user_layouts VALUES ('u2', 'topology', '{"pins":{}}', 1758000000003)`,
		`DELETE FROM schema_version WHERE version >= 22`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	// THE CONTROL: the old table refuses the new kind, so a pass below is the
	// migration's doing and not a table that never needed it.
	if err := d.SetLayout("u1", "lang", map[string]string{"lang": "de"}); err == nil {
		t.Fatal("the wound-back table accepted a 'lang' row; the wind-back did not take")
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v < 22 {
		t.Errorf("schema is v%d after Migrate, want at least 22", v)
	}

	// Every row, byte for byte, with its timestamp.
	for _, want := range []struct {
		user, kind, data string
		at               int64
	}{
		{"u1", "dashboard", `{"cards":[1,2]}`, 1758000000001},
		{"u1", "nav", `{"grouped":true,"expanded":[]}`, 1758000000002},
		{"u2", "topology", `{"pins":{}}`, 1758000000003},
	} {
		var data string
		var at int64
		err := d.sql.QueryRow(`SELECT data, updated_at FROM user_layouts WHERE user_id = ? AND kind = ?`,
			want.user, want.kind).Scan(&data, &at)
		if err != nil || data != want.data || at != want.at {
			t.Errorf("%s/%s after the rebuild: %q at %d (%v), want %q at %d",
				want.user, want.kind, data, at, err, want.data, want.at)
		}
	}

	if err := d.SetLayout("u1", "lang", map[string]string{"lang": "de"}); err != nil {
		t.Fatalf("a 'lang' row was refused after the migration: %v", err)
	}
	if got, _ := d.Layout("u1", "lang"); string(got) != `{"lang":"de"}` {
		t.Errorf("Layout(u1, lang) = %s, want {\"lang\":\"de\"}", got)
	}
	// And the kinds outside the list are still refused: the CHECK came across.
	if err := d.SetLayout("u1", "anything", map[string]string{}); err == nil {
		t.Error("an unknown kind was accepted; the rebuilt table lost its CHECK")
	}
}
