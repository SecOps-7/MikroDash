package db

import (
	"strconv"
	"strings"
	"testing"
)

func cfgExec(t *testing.T, d *DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := d.sql.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func cfgTableSQL(t *testing.T, d *DB) string {
	t.Helper()
	rows, err := d.sql.Query(`SELECT name, sql FROM sqlite_master
	    WHERE name LIKE 'cfg_%' OR name LIKE 'idx_cfg_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		b.WriteString(name + ": " + sql + "\n")
	}
	return b.String()
}

// Migration 20 builds, on an existing install, exactly what a fresh one is
// born with.
func TestMigrationTwentyBuildsWhatAFreshDatabaseHas(t *testing.T) {
	d := openTest(t, t.TempDir())
	fresh := cfgTableSQL(t, d)
	if strings.Count(fresh, "CREATE TABLE") != 4 {
		t.Fatalf("a fresh database has these Config Management tables:\n%s", fresh)
	}

	// Wind back to an install from before Config Management.
	cfgExec(t, d,
		`DROP TABLE cfg_baselines`, `DROP TABLE cfg_run_targets`,
		`DROP TABLE cfg_runs`, `DROP TABLE cfg_templates`,
		`DELETE FROM schema_version WHERE version >= 20`)
	if got := cfgTableSQL(t, d); got != "" {
		t.Fatalf("the wind-back left %s", got)
	}

	if _, err := d.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got := cfgTableSQL(t, d); got != fresh {
		t.Errorf("migrated:\n%s\nfresh:\n%s", got, fresh)
	}
	// And a second run changes nothing, as every port migration must allow.
	cfgExec(t, d, `DELETE FROM schema_version WHERE version >= 20`)
	if _, err := d.Migrate(); err != nil {
		t.Errorf("running migration 20 twice: %v", err)
	}
}

func cfgSeedTemplate(t *testing.T, d *DB, id string, backupID string) {
	t.Helper()
	cfgExec(t, d, `INSERT INTO cfg_templates
	    (id, name, kind, body, fingerprint, created_by, created_at, updated_at, backup_id)
	    VALUES ('`+id+`', '`+id+`', 'fragment', '/ip dns set servers=192.0.2.53', 'fp', 'u1', 1, 1, `+backupID+`)`)
}

func cfgSeedRun(t *testing.T, d *DB, id, state string) {
	t.Helper()
	cfgExec(t, d, `INSERT INTO cfg_runs
	    (id, template_id, template_name, revision, method, body_masked, fingerprint, state,
	     created_by, created_at, updated_at)
	    VALUES ('`+id+`', 't1', 'DNS', 1, 'additions', 'x', 'fp', '`+state+`', 'u1', 1, 1)`)
}

func cfgTarget(t *testing.T, d *DB, run, router, state string, backupID string) {
	t.Helper()
	cfgExec(t, d, `INSERT INTO cfg_run_targets (run_id, router_id, position, state, backup_id)
	    VALUES ('`+run+`', '`+router+`', 0, '`+state+`', `+backupID+`)`)
}

func cfgState(t *testing.T, d *DB, q string, args ...any) (state, note string) {
	t.Helper()
	var n *string
	if err := d.sql.QueryRow(q, args...).Scan(&state, &n); err != nil {
		t.Fatal(err)
	}
	if n != nil {
		note = *n
	}
	return state, note
}

// A restart never resumes a run, and says what it can about each router.
func TestARestartInterruptsAndNeverResumes(t *testing.T) {
	d := openTest(t, t.TempDir())
	cfgSeedTemplate(t, d, "t1", "NULL")
	cfgSeedRun(t, d, "open", CfgRunRolling)
	cfgTarget(t, d, "open", "r-done", CfgTargetApplied, "NULL")
	cfgTarget(t, d, "open", "r-mid", CfgTargetApplying, "42")
	cfgTarget(t, d, "open", "r-mid2", CfgTargetApplying, "NULL")
	cfgTarget(t, d, "open", "r-next", CfgTargetPreflightOK, "NULL")
	cfgTarget(t, d, "open", "r-later", CfgTargetPending, "NULL")
	cfgTarget(t, d, "open", "r-bad", CfgTargetPreflightFailed, "NULL")
	cfgSeedRun(t, d, "closed", CfgRunDone)
	// Not a state a finished run can hold; it must still be left alone.
	cfgTarget(t, d, "closed", "r-old", CfgTargetApplying, "NULL")

	n, err := d.InterruptCfgRuns()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("interrupted %d runs, want the one open one", n)
	}
	if s, _ := cfgState(t, d, `SELECT state, error FROM cfg_runs WHERE id = 'open'`); s != CfgRunInterrupted {
		t.Errorf("the open run is %q", s)
	}
	if s, _ := cfgState(t, d, `SELECT state, error FROM cfg_runs WHERE id = 'closed'`); s != CfgRunDone {
		t.Errorf("a finished run was changed to %q", s)
	}
	want := map[string]string{
		"r-done": CfgTargetApplied, "r-mid": CfgTargetUnknown, "r-mid2": CfgTargetUnknown,
		"r-next": CfgTargetNotAttempted, "r-later": CfgTargetNotAttempted,
		"r-bad": CfgTargetPreflightFailed, "r-old": CfgTargetApplying,
	}
	for router, w := range want {
		s, warn := cfgState(t, d, `SELECT state, warning FROM cfg_run_targets WHERE router_id = ?`, router)
		if s != w {
			t.Errorf("%s is %q, want %q", router, s, w)
		}
		if router == "r-mid" && !strings.Contains(warn, "restore point #42") {
			t.Errorf("the router caught mid-change does not name its restore point: %q", warn)
		}
		if router == "r-mid2" && (warn == "" || strings.Contains(warn, "restore point")) {
			t.Errorf("with no restore point the warning is %q", warn)
		}
	}
	// Idempotent: a second start finds nothing open.
	if n, _ := d.InterruptCfgRuns(); n != 0 {
		t.Errorf("a second start interrupted %d runs", n)
	}
}

// Deleting a template keeps the record of what it did.
func TestTheLedgerOutlivesItsTemplate(t *testing.T) {
	d := openTest(t, t.TempDir())
	cfgSeedTemplate(t, d, "t1", "NULL")
	cfgSeedRun(t, d, "run1", CfgRunDone)
	cfgTarget(t, d, "run1", "r1", CfgTargetApplied, "NULL")
	cfgExec(t, d, `INSERT INTO cfg_baselines (template_id, router_id, body, fingerprint, taken_at)
	    VALUES ('t1', 'r1', 'x', 'fp', 1)`)

	if ok, err := d.DeleteCfgTemplate("t1"); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}

	var tid *string
	var name string
	if err := d.sql.QueryRow(`SELECT template_id, template_name FROM cfg_runs WHERE id = 'run1'`).
		Scan(&tid, &name); err != nil {
		t.Fatalf("the run went with its template: %v", err)
	}
	if tid != nil || name != "DNS" {
		t.Errorf("template_id=%v name=%q; want NULL and the name kept", tid, name)
	}
	var n int
	_ = d.sql.QueryRow(`SELECT count(*) FROM cfg_run_targets WHERE run_id = 'run1'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d target rows survive, want 1", n)
	}
	_ = d.sql.QueryRow(`SELECT count(*) FROM cfg_baselines`).Scan(&n)
	if n != 0 {
		t.Errorf("a baseline of a deleted template survives")
	}
}

// The binary a full-binary template is made of cannot be deleted from under it.
func TestAPinnedBackupCannotBeDeleted(t *testing.T) {
	d := openTest(t, t.TempDir())
	id, err := d.RecordBackup(BackupRun{RouterID: "r1", TakenAt: 1, Outcome: "changed"})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := d.RecordBackup(BackupRun{RouterID: "r1", TakenAt: 2, Outcome: "changed"})
	cfgSeedTemplate(t, d, "t1", strconv.FormatInt(id, 10))

	pinned, err := d.PinnedBackupIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !pinned[id] || pinned[other] || len(pinned) != 1 {
		t.Errorf("pinned %v, want only %d", pinned, id)
	}
	if _, err := d.DeleteBackup(id); err == nil {
		t.Error("the pinned backup's row was deleted")
	}
	if ok, err := d.DeleteBackup(other); err != nil || !ok {
		t.Errorf("an unpinned backup could not be deleted: %v", err)
	}
}

func TestRemovingARouterTakesItsBaselinesOnly(t *testing.T) {
	d := openTest(t, t.TempDir())
	cfgSeedTemplate(t, d, "t1", "NULL")
	cfgExec(t, d,
		`INSERT INTO cfg_baselines (template_id, router_id, body, fingerprint, taken_at) VALUES ('t1','r1','x','f',1)`,
		`INSERT INTO cfg_baselines (template_id, router_id, body, fingerprint, taken_at) VALUES ('t1','r2','x','f',1)`)
	if n, err := d.DeleteCfgBaselinesForRouter("r1"); err != nil || n != 1 {
		t.Fatalf("removed %d: %v", n, err)
	}
	var n int
	_ = d.sql.QueryRow(`SELECT count(*) FROM cfg_baselines WHERE router_id = 'r2'`).Scan(&n)
	if n != 1 {
		t.Error("another router's baseline went too")
	}
}

// Retention never sees a pinned backup, so it neither counts nor removes it.
func TestRetentionSkipsAPinnedBackup(t *testing.T) {
	d := openTest(t, t.TempDir())
	stem, dir := "s", "/d"
	var ids []int64
	for i := int64(1); i <= 3; i++ {
		id, err := d.RecordBackup(BackupRun{RouterID: "r1", TakenAt: i, Outcome: "changed", Stem: &stem, Dir: &dir})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	cfgSeedTemplate(t, d, "t1", strconv.FormatInt(ids[1], 10))
	rows, err := d.PrunableBackups("r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("retention sees %d rows, want the 2 unpinned", len(rows))
	}
	for _, r := range rows {
		if r.ID == ids[1] {
			t.Error("retention sees the pinned backup")
		}
	}
	if all, _ := d.StoredBackups("r1"); len(all) != 3 {
		t.Errorf("the History list lost the pinned backup: %d rows", len(all))
	}
}

// A canned template is not a row, and its baseline is kept all the same.
func TestACannedTemplateHasABaseline(t *testing.T) {
	d := openTest(t, t.TempDir())
	if err := d.SetCfgBaseline(CfgBaseline{TemplateID: "canned:home-firewall", RouterID: "r1", Body: "x",
		Fingerprint: "f", TakenAt: 1}); err != nil {
		t.Fatalf("a canned template's baseline was refused: %v", err)
	}
}
