package server

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/backups"
	"mikrodash/internal/db"
	"mikrodash/internal/store"

	_ "modernc.org/sqlite"
)

// The scheduler's wiring. Every case here is a way to be silently wrong: a
// scheduler that runs when it must not, a retention that deletes what an
// operator asked to keep, or a password on its way into a log line.

const schedTestDDL = `
CREATE TABLE schema_version (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
INSERT INTO schema_version (version, applied_at) VALUES (14, 0);

-- What LastBackupRun reads. Present so "never run" means the table is EMPTY
-- rather than missing: an absent table makes every query error, and this
-- package's rule is that an unreadable history reads as never-run — so without
-- it every case would take the error path and the distinction would go untested.
-- LIFTED FROM ../MikroDash/src/db.js, not invented. The first version of this
-- fixture made up "path" and "size" columns, and tools/schema-audit.js caught
-- both — which is the whole reason that audit exists: a test passing against a
-- schema the real database does not have proves nothing about the real database.
CREATE TABLE config_backups (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  router_id    TEXT    NOT NULL,
  taken_at     INTEGER NOT NULL,
  outcome      TEXT    NOT NULL,
  source       TEXT    NOT NULL DEFAULT 'schedule',
  actor        TEXT,
  stem         TEXT,
  dir          TEXT,
  fingerprint  TEXT,
  rsc_bytes    INTEGER NOT NULL DEFAULT 0,
  backup_bytes INTEGER NOT NULL DEFAULT 0,
  model        TEXT,
  serial       TEXT,
  os_version   TEXT,
  ms           INTEGER NOT NULL DEFAULT 0,
  pruned_at    INTEGER,
  error        TEXT
);`

func schedServer(t *testing.T, routersJSON string) *Server {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"routers.json": routersJSON, "settings.json": `{}`,
		".secret": "test-secret", "users.json": `[]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// THE DDL COMES FIRST. `db.Open` reads schema_version before anything else
	// and refuses a directory without it, and its pooled connections cache the
	// schema — so a table created after Open is not reliably visible.
	h, err := sql.Open("sqlite", filepath.Join(dir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(schedTestDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()

	// A REAL DATABASE, and the reason is a mutation that survived.
	//
	// The first version of this helper left `auditDB` nil, so
	// `TestTheBackupSchedulerIsOffUnlessAskedFor` passed against a
	// `buildBackupScheduler` whose FLAG CHECK had been deleted — the nil-database
	// guard returned nil and the test could not tell which guard had fired. The
	// test was measuring the wrong thing while looking green.
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &Server{store: st, auditDB: d}
}

// schedServerWithBackupPassword is schedServer plus a router whose backup
// password is actually SET.
//
// It has to seal the value with the store's own key, which means writing
// routers.json twice — once to open the store, once with the ciphertext. Worth
// it: with an empty password every "the password did not leak" assertion passes
// vacuously, which is how the unknown-router mutant survived its first pass.
//
// The value is a made-up string, sealed, inside a t.TempDir. Nothing identifying
// and nothing real, as the fixture rule requires.
func schedServerWithBackupPassword(t *testing.T, plain string) *Server {
	t.Helper()
	s := schedServer(t, twoRouters)
	sealed, err := s.store.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(twoRouters,
		`"backup":{"enabled":true,"schedule":"daily","time":"03:00","keepCount":0}`,
		`"backup":{"enabled":true,"schedule":"daily","time":"03:00","keepCount":0,`+
			`"password":`+strconv.Quote(sealed)+`}`, 1)
	if err := os.WriteFile(filepath.Join(s.store.Dir, "routers.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// ── OFF IS THE DEFAULT, AND IT IS NOT A PREFERENCE ─────────────────────────
//
// Two schedulers against one fleet take two backups of every router on the same
// timetable. During coexistence Node owns the job.
func TestTheBackupSchedulerIsOffUnlessAskedFor(t *testing.T) {
	s := schedServer(t, `[]`)
	if got := s.buildBackupScheduler(false); got != nil {
		t.Errorf("a scheduler was built with the flag off: %v", got)
	}
}

// AND IT REFUSES TO BUILD WITHOUT ITS DEPENDENCIES, rather than building a
// scheduler that panics on its first tick at whatever hour the operator set.
func TestTheSchedulerWillNotBuildWithoutAHistoryDatabase(t *testing.T) {
	s := schedServer(t, `[]`)
	s.auditDB = nil
	if got := s.buildBackupScheduler(true); got != nil {
		t.Errorf("a scheduler was built with no database: %v", got)
	}
}

const twoRouters = `[
 {"id":"r1","label":"Edge","host":"198.51.100.1","username":"u","password":"",
  "backup":{"enabled":true,"schedule":"daily","time":"03:00","keepCount":0}},
 {"id":"r2","label":"Branch","host":"198.51.100.2","username":"u","password":"",
  "disabled":true,
  "backup":{"enabled":true,"schedule":"weekly"}}
]`

// ── A DISABLED ROUTER IS STILL LISTED ──────────────────────────────────────
//
// The scheduler skips it itself, before asking IsDue. Filtering here would move
// that decision into a second place and leave the two able to disagree — and the
// direction of the disagreement matters: a router quietly missing from the list
// stops being backed up with nothing reporting it.
func TestADisabledRouterIsStillHandedToTheScheduler(t *testing.T) {
	s := schedServer(t, twoRouters)
	got := s.schedRouters()
	if len(got) != 2 {
		t.Fatalf("got %d routers, want 2: %+v", len(got), got)
	}
	byID := map[string]backups.SchedRouter{}
	for _, r := range got {
		byID[r.ID] = r
	}
	if !byID["r2"].Disabled {
		t.Error("r2 is disabled in the file and did not arrive disabled")
	}
	if byID["r1"].Disabled {
		t.Error("r1 is not disabled in the file and arrived disabled")
	}
	if byID["r1"].Label != "Edge" {
		t.Errorf("label did not carry: %q", byID["r1"].Label)
	}
	if byID["r1"].Backup == nil || byID["r1"].Backup.Schedule != "daily" {
		t.Errorf("the schedule did not carry: %+v", byID["r1"].Backup)
	}
	if byID["r1"].Backup.Time == nil || *byID["r1"].Backup.Time != "03:00" {
		t.Errorf("the time did not carry: %+v", byID["r1"].Backup.Time)
	}
	// A ROUTER THAT SET NO TIME MUST ARRIVE WITH NIL, not with "". IsDue reads
	// nil as "take the default hour" and "" as a time it cannot parse.
	if byID["r2"].Backup.Time != nil {
		t.Errorf("r2 set no time and arrived with %q", *byID["r2"].Backup.Time)
	}
}

// ── THE PASSWORD CANNOT REACH THE SCHEDULER'S ROUTER ───────────────────────
//
// `SchedRouter` is what the scheduler logs, compares and carries. The backup
// password encrypts the .backup binary, so it is a credential in its own right,
// and `store.BackupBlock` says it "must never reach a page payload".
//
// This asserts it STRUCTURALLY — `backups.Backup` must have no field that could
// hold one — rather than by checking one instance came out empty. A field that
// exists is a field somebody fills in later.
func TestTheBackupPasswordCannotReachAScheduledRouter(t *testing.T) {
	ty := reflect.TypeOf(backups.Backup{})
	for i := 0; i < ty.NumField(); i++ {
		n := strings.ToLower(ty.Field(i).Name)
		if strings.Contains(n, "pass") || strings.Contains(n, "secret") ||
			strings.Contains(n, "encrypted") {
			t.Errorf("backups.Backup grew field %q — the scheduler carries this "+
				"struct into its logs", ty.Field(i).Name)
		}
	}
	// And the whole SchedRouter, serialised, carries nothing that looks like one.
	s := schedServer(t, twoRouters)
	b, err := json.Marshal(s.schedRouters())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"pass", "secret", "encrypted"} {
		if strings.Contains(strings.ToLower(string(b)), bad) {
			t.Errorf("a scheduled router serialises %q: %s", bad, b)
		}
	}
}

// ── RETENTION: ABSENT TAKES THE DEFAULT, ZERO IS A VALUE ───────────────────
func TestRetentionDistinguishesAbsentFromZero(t *testing.T) {
	zero := 0
	seven := 7
	for _, tc := range []struct {
		name               string
		rec                backupRecord
		wantCount, wantDay int
	}{
		{"nothing set takes both defaults", backupRecord{},
			backups.DefaultKeepCount, backups.DefaultKeepDays},
		{"an explicit zero is kept, not defaulted",
			backupRecord{keepCount: &zero}, 0, backups.DefaultKeepDays},
		{"a set value wins", backupRecord{keepCount: &seven, keepDays: &zero}, 7, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := retentionFor(tc.rec)
			if got.KeepCount != tc.wantCount || got.KeepDays != tc.wantDay {
				t.Errorf("got keepCount=%d keepDays=%d, want %d and %d",
					got.KeepCount, got.KeepDays, tc.wantCount, tc.wantDay)
			}
		})
	}
}

// The router's OWN retention is read, not the install default, and a zero in the
// file survives the trip through the store.
func TestARoutersOwnRetentionIsRead(t *testing.T) {
	s := schedServer(t, twoRouters)
	got := retentionFor(s.backupRecord("r1"))
	if got.KeepCount != 0 {
		t.Errorf("r1 sets keepCount 0 in the file; got %d", got.KeepCount)
	}
	if got.KeepDays != backups.DefaultKeepDays {
		t.Errorf("r1 sets no keepDays; got %d, want the default %d",
			got.KeepDays, backups.DefaultKeepDays)
	}
}

// ── AN UNREADABLE LAST RUN READS AS "NEVER RUN" ────────────────────────────
//
// The safe direction, not the cautious one: zero makes IsDue take a backup that
// may be redundant. Reporting a RECENT run instead would SKIP one, and a missing
// restore point is worse than a duplicate.
func TestAnUnreadableLastRunReadsAsNeverRun(t *testing.T) {
	s := schedServer(t, twoRouters)
	s.auditDB = nil
	if got := s.lastBackupRun("r1"); got != 0 {
		t.Errorf("got %d, want 0 for an unreadable history", got)
	}
}

// An unknown router yields an empty record rather than another router's.
// ── AN UNKNOWN ROUTER RETURNS NOTHING, NOT THE FIRST ROUTER'S ─────────────
//
// The router whose password IS set is what makes this discriminating. Against a
// fleet with no backup passwords the assertion passed vacuously, and a mutant
// that fell through to `list[0]` survived: every field it could have leaked was
// already empty.
func TestAnUnknownRouterHasNoBackupRecord(t *testing.T) {
	const fake = "not-a-real-backup-password"
	s := schedServerWithBackupPassword(t, fake)

	// The fixture is only meaningful if r1 really does carry it.
	if got := s.backupRecord("r1"); got.password != fake {
		t.Fatalf("the fixture did not take: r1 password is %q", got.password)
	}
	got := s.backupRecord("nope")
	if got.block != nil {
		t.Errorf("an unknown router returned a backup block: %+v", got.block)
	}
	if got.password != "" {
		t.Errorf("an unknown router returned another router's password")
	}
	if got.keepCount != nil || got.keepDays != nil {
		t.Errorf("an unknown router returned retention: %+v", got)
	}
}

// THE FLAG MUST ACTUALLY START IT.
//
// `buildBackupScheduler` returned a scheduler that nothing ticked, so
// `-backup-scheduler` switched on a component that could not act — an operator
// passing the flag would have got no backups and no error. That was correct
// while the flag stood in for a cutover step and stopped being correct the
// moment the step was taken (2026-08-29).
//
// A source pin because the defect is "somebody forgot to call it", which is the
// shape a test of the callee cannot see: `Start` itself works fine.
func TestTheBackupSchedulerIsStarted(t *testing.T) {
	b, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatalf("reading server.go: %v", err)
	}
	src := string(b)
	code := regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(src, "")

	if !strings.Contains(code, "srv.backupSched = srv.buildBackupScheduler(") {
		t.Fatal("server.go no longer builds the backup scheduler — this test is measuring nothing")
	}
	if !regexp.MustCompile(`srv\.backupSched\.Start\(`).MatchString(code) {
		t.Error("nothing calls srv.backupSched.Start(): -backup-scheduler would switch on a " +
			"scheduler that never ticks, so an operator gets no backups and no error")
	}
	// ...and only when it exists. `Start` on a nil scheduler panics at boot.
	if !regexp.MustCompile(`srv\.backupSched != nil`).MatchString(code) {
		t.Error("Start is called without a nil check — with the flag off the scheduler is nil " +
			"and this panics on startup, which is a worse failure than no backups")
	}
}
