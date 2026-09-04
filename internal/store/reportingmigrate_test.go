package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The one-time reporting migration.
//
// Before the toggle, ONE router recorded history: the active one. A nil default
// meaning "on" would have started recording the whole fleet the moment this
// shipped; meaning "off" would have stopped the recording that was happening.
// This writes the truth once so an upgrade changes nothing.

func migrateStore(t *testing.T, routersJSON string) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	for name, body := range map[string]string{
		"routers.json":  routersJSON,
		"settings.json": `{"activeRouterId":"r2"}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func recordsOf(t *testing.T, s *Store) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("routers.json is not valid JSON after the migration: %v", err)
	}
	return out
}

const threeRouters = `[
  {"id":"r1","label":"One","host":"198.51.100.1","username":"u","password":""},
  {"id":"r2","label":"Two","host":"198.51.100.2","username":"u","password":""},
  {"id":"r3","label":"Three","host":"198.51.100.3","username":"u","password":""}
]`

// TestOnlyTheActiveRouterStartsReporting is the upgrade contract.
func TestOnlyTheActiveRouterStartsReporting(t *testing.T) {
	s := migrateStore(t, threeRouters)
	n, err := s.MigrateReportingDefaults(s.ActiveRouterID())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("migrated %d records, want all 3", n)
	}
	want := map[string]bool{"r1": false, "r2": true, "r3": false}
	for _, r := range recordsOf(t, s) {
		id, _ := r["id"].(string)
		got, ok := r["reportingEnabled"].(bool)
		if !ok {
			t.Errorf("%s has no reportingEnabled after the migration", id)
			continue
		}
		if got != want[id] {
			t.Errorf("%s reportingEnabled = %v, want %v (r2 is the active router, "+
				"and was the only one recording before the flag existed)", id, got, want[id])
		}
	}
}

// TestTheMigrationIsIdempotent — it runs on every start.
func TestTheMigrationIsIdempotent(t *testing.T) {
	s := migrateStore(t, threeRouters)
	if _, err := s.MigrateReportingDefaults(s.ActiveRouterID()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.MigrateReportingDefaults(s.ActiveRouterID())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("the second run changed %d record(s); it must write nothing", n)
	}
	after, _ := os.ReadFile(filepath.Join(s.Dir, "routers.json"))
	if string(before) != string(after) {
		t.Error("the file was rewritten by a run that changed nothing")
	}
	// ── AND IT WAS NOT WRITTEN AT ALL ──────────────────────────────────────
	//
	// Comparing bytes cannot see this: the encoder is deterministic, so a
	// pointless rewrite produces an identical file and the check above passes.
	// The modification time is the only evidence. A mutation removing the
	// `changed == 0` early return survived until this line existed.
	if err := os.Chtimes(filepath.Join(s.Dir, "routers.json"),
		time.Unix(0, 0), time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MigrateReportingDefaults(s.ActiveRouterID()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(time.Unix(0, 0)) {
		t.Error("routers.json was written by a run that changed nothing; every " +
			"restart would re-encode the whole fleet for no reason")
	}
}

// TestAnOperatorsChoiceIsNeverOverwritten. Someone who turns reporting OFF on
// the active router must not have it turned back on at the next restart.
func TestAnOperatorsChoiceIsNeverOverwritten(t *testing.T) {
	s := migrateStore(t, `[
	  {"id":"r2","label":"Two","host":"198.51.100.2","username":"u","password":"",
	   "reportingEnabled":false},
	  {"id":"r3","label":"Three","host":"198.51.100.3","username":"u","password":"",
	   "reportingEnabled":true}
	]`)
	n, err := s.MigrateReportingDefaults(s.ActiveRouterID())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("overwrote %d record(s) that already had an answer", n)
	}
	for _, r := range recordsOf(t, s) {
		id, _ := r["id"].(string)
		got, _ := r["reportingEnabled"].(bool)
		if id == "r2" && got {
			t.Error("the active router's explicit OFF was overwritten")
		}
		if id == "r3" && !got {
			t.Error("a non-active router's explicit ON was overwritten")
		}
	}
}

// TestUnmodelledFieldsSurviveTheMigration — the record is re-encoded, and the
// struct models 16 of the file's fields. Anything it does not know about must
// come through untouched.
func TestUnmodelledFieldsSurviveTheMigration(t *testing.T) {
	s := migrateStore(t, `[
	  {"id":"r2","label":"Two","host":"198.51.100.2","username":"u","password":"",
	   "collection":{"mode":"poll","off":["wifi"]},"geo":{"place":{"name":"Berlin"}},
	   "connDownThresholdSec":0,"addedAt":1700000000000,"serial":"ABC123"}
	]`)
	if _, err := s.MigrateReportingDefaults(s.ActiveRouterID()); err != nil {
		t.Fatal(err)
	}
	r := recordsOf(t, s)[0]
	for _, k := range []string{"collection", "geo", "connDownThresholdSec", "addedAt", "serial"} {
		if _, ok := r[k]; !ok {
			t.Errorf("%q was dropped by the migration", k)
		}
	}
	if got, _ := r["reportingEnabled"].(bool); !got {
		t.Error("the active router did not get reporting")
	}
}

// TestNoFleetIsNotAnError — a fresh install has no routers.json at all.
func TestNoFleetIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_SECRET", "test-secret")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	n, err := st.MigrateReportingDefaults("")
	if err != nil || n != 0 {
		t.Errorf("a fresh install reported n=%d err=%v", n, err)
	}
}

// TestNoActiveRouterLeavesEverythingOff — an install that has never selected
// one was recording nothing, so it stays that way.
func TestNoActiveRouterLeavesEverythingOff(t *testing.T) {
	s := migrateStore(t, threeRouters)
	if _, err := s.MigrateReportingDefaults(""); err != nil {
		t.Fatal(err)
	}
	for _, r := range recordsOf(t, s) {
		if got, _ := r["reportingEnabled"].(bool); got {
			t.Errorf("%v got reporting with no active router set", r["id"])
		}
	}
}
