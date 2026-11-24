package db

import (
	"errors"
	"testing"
)

// Migration 41: the custom apps' tables, on a database that predates them.
func TestMigrationFortyOneAddsTheAppRuleTables(t *testing.T) {
	d := openTest(t, t.TempDir())
	for _, stmt := range []string{
		`DROP TABLE app_rules`,
		`DROP TABLE app_categories`,
		`DELETE FROM schema_version WHERE version >= 41`,
	} {
		if _, err := d.sql.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := d.AppRules(); err == nil {
		t.Fatal("app_rules still answers after it was dropped; the wind-back did not take")
	}
	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if v, _ := d.SchemaVersion(); v != schemaVersion || schemaVersion < 41 {
		t.Errorf("schema is v%d after Migrate (constant %d), want the current version, at least 41", v, schemaVersion)
	}
	if r, err := d.AppRules(); err != nil || len(r) != 0 {
		t.Errorf("rules after the migration: %v %v", r, err)
	}
	if c, err := d.AppCategories(); err != nil || len(c) != 0 {
		t.Errorf("categories after the migration: %v %v", c, err)
	}
}

// A rule is replaced whole, keeping who made it and when; a delete of nothing
// says so.
func TestAppRulesRoundTrip(t *testing.T) {
	d := openTest(t, t.TempDir())
	if err := d.PutAppRule("u_game", `{"id":"u_game","name":"Game"}`, "usr-1", 100); err != nil {
		t.Fatal(err)
	}
	if err := d.PutAppRule("u_game", `{"id":"u_game","name":"Game 2"}`, "usr-2", 200); err != nil {
		t.Fatal(err)
	}
	rows, err := d.AppRules()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rules: %v %v", rows, err)
	}
	r := rows[0]
	if r.Rule != `{"id":"u_game","name":"Game 2"}` || r.CreatedBy != "usr-1" || r.CreatedAt != 100 ||
		r.UpdatedBy != "usr-2" || r.UpdatedAt != 200 {
		t.Errorf("after the update: %+v", r)
	}
	if err := d.DeleteAppRule("u_game"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteAppRule("u_game"); !errors.Is(err, ErrAppRuleNotFound) {
		t.Errorf("a second delete: %v, want ErrAppRuleNotFound", err)
	}

	if err := d.PutAppCategory("u_kids", "Kids", "pink", "usr-1", 300); err != nil {
		t.Fatal(err)
	}
	if err := d.PutAppCategory("u_kids", "Children", "ok", "usr-2", 400); err != nil {
		t.Fatal(err)
	}
	cats, err := d.AppCategories()
	if err != nil || len(cats) != 1 || cats[0].Name != "Children" || cats[0].Tone != "ok" || cats[0].CreatedBy != "usr-1" {
		t.Errorf("categories: %+v %v", cats, err)
	}
	if err := d.DeleteAppCategory("u_kids"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteAppCategory("u_kids"); !errors.Is(err, ErrAppRuleNotFound) {
		t.Errorf("a second delete: %v", err)
	}
}

// A deleted app's hours are added to the unnamed row of the same router and
// hour, made where there is none, and the app's rows are gone.
func TestFoldAppUsageMovesHoursIntoUnnamed(t *testing.T) {
	d := openTest(t, t.TempDir())
	if err := d.AddAppUsage([]AppUsageRow{
		{RouterID: "r1", App: "u_game", Hour: 3_600_000, RxBytes: 10, TxBytes: 1},
		{RouterID: "r1", App: "_unnamed", Hour: 3_600_000, RxBytes: 5, TxBytes: 5},
		{RouterID: "r2", App: "u_game", Hour: 7_200_000, RxBytes: 7, TxBytes: 0},
		{RouterID: "r1", App: "netflix", Hour: 3_600_000, RxBytes: 99, TxBytes: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.FoldAppUsage("u_game", "_unnamed"); err != nil {
		t.Fatal(err)
	}
	got := map[string]AppUsageRow{}
	for _, rid := range []string{"r1", "r2"} {
		rows, err := d.AppUsage(rid, 0, 1e13)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			got[rid+"/"+r.App] = r
		}
	}
	if _, ok := got["r1/u_game"]; ok {
		t.Error("the app's rows are still there")
	}
	if u := got["r1/_unnamed"]; u.RxBytes != 15 || u.TxBytes != 6 {
		t.Errorf("r1 unnamed %+v, want rx 15 tx 6", u)
	}
	if u := got["r2/_unnamed"]; u.RxBytes != 7 || u.Hour != 7_200_000 {
		t.Errorf("r2 unnamed %+v, want rx 7 at the same hour", u)
	}
	if n := got["r1/netflix"]; n.RxBytes != 99 {
		t.Errorf("another app moved: %+v", n)
	}
}
