package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func migrateDir(t *testing.T, settings, routers string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("mig-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if routers != "" {
		if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(routers), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const twoRouters = `[
  {"id":"a","label":"A","host":"198.51.100.1","port":8728,"username":"u","password":""},
  {"id":"b","label":"B","host":"198.51.100.2","port":8728,"username":"u","password":"",
   "collection":{"mode":"stream"}}
]`

func blockOf(t *testing.T, dir, id string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "routers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []map[string]any
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r["id"] == id {
			b, _ := r["collection"].(map[string]any)
			return b
		}
	}
	t.Fatalf("no router %q in the file", id)
	return nil
}

// THE MIGRATION RUNS AT OPEN AND WRITES THE PLAN.
//
// The call site as much as the plan: `internal/collection` already pins the
// mapping against the live function, and a mapping nothing invokes leaves an
// upgrading install silently reverted from Poll to Stream — which is the live
// comment's own description of the failure.
func TestTheCollectionMigrationRunsAtOpen(t *testing.T) {
	dir := migrateDir(t,
		`{"streamSystem":false,"streamPing":false,"streamConns":false,
		  "streamTalkers":false,"streamIfrates":false}`, twoRouters)

	st0, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st0.MigrateCollectionMode(); err != nil {
		t.Fatal(err)
	}

	if got := blockOf(t, dir, "a")["mode"]; got != "poll" {
		t.Errorf("router a's mode = %v, want poll — the operator's global Poll was lost", got)
	}
	// THE PINNED ROUTER IS UNTOUCHED. Its own choice outranks a retired global.
	if got := blockOf(t, dir, "b")["mode"]; got != "stream" {
		t.Errorf("router b was pinned to stream and is now %v", got)
	}

	// AND THE FLAG IS SET, so the next start does nothing.
	st2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := st2.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := cfg["collectionMigrated"].(bool); !b {
		t.Error("collectionMigrated was not set; every start would re-plan")
	}
}

// AN ALREADY-MIGRATED INSTALL IS LEFT ALONE, even with the legacy keys still on
// disk — which they are, because nothing deletes them.
func TestAnAlreadyMigratedInstallIsNotTouched(t *testing.T) {
	dir := migrateDir(t,
		`{"collectionMigrated":true,"streamSystem":false,"streamPing":false,
		  "streamConns":false,"streamTalkers":false,"streamIfrates":false}`, twoRouters)
	st0, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st0.MigrateCollectionMode(); err != nil {
		t.Fatal(err)
	}
	if b := blockOf(t, dir, "a"); b != nil {
		t.Errorf("a migrated install was re-migrated: router a gained %v", b)
	}
}

// THE FLAG IS SET EVEN WHEN NOTHING NEEDED WRITING.
//
// All-true is the new default and plans nothing. If the flag were only written
// alongside a change, every start would re-read and re-plan for ever, and the
// install would stay one settings edit away from a migration it had passed.
func TestTheFlagIsSetWhenThePlanIsEmpty(t *testing.T) {
	dir := migrateDir(t,
		`{"streamSystem":true,"streamPing":true,"streamConns":true,
		  "streamTalkers":true,"streamIfrates":true}`, twoRouters)
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MigrateCollectionMode(); err != nil {
		t.Logf("migration returned: %v", err)
	}
	if b := blockOf(t, dir, "a"); b != nil {
		t.Errorf("all-true is the default and must write nothing; router a gained %v", b)
	}
	cfg, err := st.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := cfg["collectionMigrated"].(bool); !b {
		t.Error("the flag was not set after an empty plan")
	}
}

// THE MIXED BRANCH LANDS AS A REAL BLOCK ON DISK, overrides included.
func TestTheMixedBranchIsWrittenWithItsOverrides(t *testing.T) {
	dir := migrateDir(t,
		`{"streamSystem":true,"streamPing":false,"streamConns":false,
		  "streamTalkers":true,"streamIfrates":true}`, twoRouters)
	st0, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st0.MigrateCollectionMode(); err != nil {
		t.Fatal(err)
	}
	b := blockOf(t, dir, "a")
	if b == nil || b["mode"] != "stream" {
		t.Fatalf("router a's block = %v, want mode stream", b)
	}
	ovr, _ := b["overrides"].(map[string]any)
	if len(ovr) != 2 || ovr["streamPing"] != false || ovr["streamConns"] != false {
		t.Errorf("overrides = %v, want exactly the two collectors that were polled", ovr)
	}
}

// A FLEET THAT DOES NOT DECODE DEFERS THE MIGRATION RATHER THAN HALF-DOING IT.
//
// Marking the install migrated while a record was unreadable would skip that
// router's migration permanently — the flag is one-way.
func TestAnUndecodableFleetDefersTheMigration(t *testing.T) {
	dir := migrateDir(t,
		`{"streamPing":false,"streamConns":false}`,
		`[{"id":"a","label":"A","host":"198.51.100.1","port":"not-a-number","username":"u","password":""}]`)
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MigrateCollectionMode(); err != nil {
		t.Logf("migration returned: %v", err)
	}
	cfg, err := st.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := cfg["collectionMigrated"].(bool); b {
		t.Error("the install was marked migrated while a router record was unreadable; " +
			"that router would never be migrated")
	}
}
