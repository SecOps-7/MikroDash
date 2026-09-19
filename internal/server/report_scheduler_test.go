package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/reports"
	"mikrodash/internal/store"
)

// TestADailyScheduleSendsOncePerDayOnItsOwn. Nothing ever ran a schedule except
// "Send now" (review loop). The tick runs a daily 07:00 schedule once after
// 07:00, not before, not twice in the same day, and again the next day. The
// runner records its run as runSchedule does, so the tick reads real history.
func TestADailyScheduleSendsOncePerDayOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(
		`[{"id":"r1","label":"R","host":"198.51.100.1","port":8728,"username":"u","password":""}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, auditDB: d}

	created := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	if err := d.UpsertReportSchedule(db.ReportSchedule{ID: "s1", RouterID: "r1", Name: "daily",
		Sections: "ping", Aggregate: "hour", Recipients: "ops@example.com", Frequency: "daily",
		SendHour: 7, Enabled: 1, CreatedAt: created.UnixMilli(), UpdatedAt: created.UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	var clock time.Time
	sent := 0
	sched := &reportScheduler{s: s, now: func() time.Time { return clock }, stop: make(chan struct{}),
		run: func(row *db.ReportSchedule) {
			sent++
			p, _ := reports.PeriodFor(row.Frequency, clock.UnixMilli(), "")
			if err := d.RecordReportRun(db.ReportRun{ScheduleID: row.ID, RanAt: clock.UnixMilli(),
				PeriodFrom: p.From, PeriodTo: p.To, Outcome: "sent", Source: "schedule"}); err != nil {
				t.Fatal(err)
			}
		}}

	for _, step := range []struct {
		at   time.Time
		want int
		why  string
	}{
		{time.Date(2026, 9, 11, 6, 55, 0, 0, time.UTC), 0, "before the send hour"},
		{time.Date(2026, 9, 11, 7, 5, 0, 0, time.UTC), 1, "past the send hour: sent"},
		{time.Date(2026, 9, 11, 7, 10, 0, 0, time.UTC), 1, "the same day again: not sent twice"},
		{time.Date(2026, 9, 12, 7, 5, 0, 0, time.UTC), 2, "the next day: sent again"},
	} {
		clock = step.at
		sched.tick()
		if sent != step.want {
			t.Fatalf("%s: %d sent, want %d", step.why, sent, step.want)
		}
	}
}

// TestNoSessionIsTheStrictAuthAnswer. A scheduled run has no session. Reading
// that as "not modern" was the PERMISSIVE answer, which keeps a creator-less
// schedule mailing; the tick takes the strict one, and records its source.
func TestNoSessionIsTheStrictAuthAnswer(t *testing.T) {
	if !isModernSession(nil) {
		t.Error("a scheduled run (no session) is treated as sign-in off")
	}
	if runSource(nil) != "schedule" || runSource(&Session{AuthMode: "modern"}) != "manual" {
		t.Error("runs are recorded under the wrong source")
	}
}

// TestAMailFailureIsStoredSanitised. The mailer's error names the SMTP host and
// port, and it was stored in report_runs.error and returned to the page as is
// (review loop). Checked at the source, as session.go's stored error is
// (internal/safe/errors_test.go): reaching the branch needs an SMTP server.
func TestAMailFailureIsStoredSanitised(t *testing.T) {
	src, err := os.ReadFile("reports_run.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "mailer.Send(cfg, msg); err != nil {")
	if i < 0 {
		t.Fatal("reports_run.go no longer sends through mailer.Send; this check reads nothing")
	}
	if branch := s[i : i+400]; !strings.Contains(branch, "safe.Message(err.Error())") {
		t.Errorf("a mail failure is stored without safe.Message:\n%s", branch)
	}
}
