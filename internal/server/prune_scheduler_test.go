package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/store"
)

// THE SWEEP IS ACTUALLY STARTED.
//
// "Test the call site, not the callee, when the defect is 'somebody forgot to
// call it'" — and here the defect WAS that, three times over in two days.
// `db.Prune` with nobody calling it leaves the retention settings exactly as
// they were: rendered, validated, persisted, ignored. Every unit test of the
// sweep passes in that state.
//
// This reads `server.go`, because there is no request that exercises a daily
// timer and no assertion about `Prune` that can tell whether anything runs it.
func TestTheRetentionSweepIsStarted(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`pruneSched\s*=\s*srv\.buildPruneScheduler\(`).Match(src) {
		t.Fatal("nothing builds the retention sweep in server.go. The three dbRetention " +
			"settings are then read by nobody and the database grows without bound, " +
			"which is the state this was written to fix.")
	}
	// STANDALONE **AND** THE FLAG, and the second half is the one with teeth.
	//
	// This asserted `standalone` alone until 2026-08-29. `standalone` means only
	// "no -node was passed", and `tools/live-diff.sh` stands a Go server up
	// against the LIVE /data — so a dry run with no flag would have pruned the
	// production database unattended. The sweep is the only one of the four
	// switches that DELETES, so it is the one where a default-on mistake cannot
	// be undone.
	if !regexp.MustCompile(`buildPruneScheduler\(srv\.standalone && opts\.Retention\)`).Match(src) {
		t.Error("the retention sweep is no longer gated on BOTH `standalone` and the " +
			"-retention flag. It DELETES, and `standalone` alone is not evidence that " +
			"this process owns the database it is pointed at.")
	}
}

// AND THE FLAG DEFAULTS TO OFF, read out of the flag declaration.
//
// A default of true would make every verification run against the live /data a
// deletion, which is the failure this gate exists to prevent — and it would look
// exactly like working software until somebody went looking for old rows.
func TestTheRetentionFlagDefaultsToOff(t *testing.T) {
	src, err := os.ReadFile("../../cmd/mikrodash/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`flag\.Bool\("retention",\s*false`).Match(src) {
		t.Error(`-retention is not declared as flag.Bool("retention", false, ...). ` +
			"It DELETES; it must be opt-in.")
	}
	// The other three switches are off by default too. Asserted together so a
	// future default flip is a deliberate edit to a test that names all four.
	for _, f := range []string{"alert-dispatch", "backup-scheduler", "history", "retention"} {
		if !regexp.MustCompile(`flag\.Bool\("` + f + `",\s*false`).Match(src) {
			t.Errorf("-%s no longer defaults to off", f)
		}
	}
}

// THE POLICY COMES FROM THE SETTINGS FILE, and an unreadable one still sweeps.
func TestRetentionPolicyReadsTheSettingsFile(t *testing.T) {
	for _, c := range []struct {
		name string
		json string
		want db.PruneDays
	}{
		{"the operator's values", `{"dbRetentionDays":30,"dbAlertRetentionDays":60,"dbAuditRetentionDays":90}`,
			db.PruneDays{Metric: 30, Alert: 60, Audit: 90}},
		// An unwritten file resolves to the live defaults INSIDE PruneDays, so
		// what comes back here is zeroes — and zero means "default", never
		// "delete everything". The next assertion is what makes that safe.
		{"an unwritten file", `{}`, db.PruneDays{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(c.json), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("s"), 0o600); err != nil {
				t.Fatal(err)
			}
			st, err := store.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			s := &Server{store: st}
			if got := s.retentionPolicy(); got != c.want {
				t.Errorf("retentionPolicy() = %+v, want %+v", got, c.want)
			}
		})
	}

	// AN UNREADABLE FILE STILL SWEEPS, at the live defaults. Skipping instead
	// would let a damaged settings.json silently disable retention — the exact
	// failure the sweep exists to prevent, arriving through the door marked
	// "be careful".
	s := &Server{store: nil}
	p := s.retentionPolicy()
	if p.MetricDays() != 90 || p.AlertDays() != 365 || p.AuditDays() != 365 {
		t.Errorf("with no store the policy resolved to %d/%d/%d, want the live "+
			"defaults 90/365/365", p.MetricDays(), p.AlertDays(), p.AuditDays())
	}
}

// THE INTERVAL AND THE IMMEDIATE RUN MATCH THE LIFTED CORPUS.
//
// A daily sweep that only ran on the interval would never run at all on a
// process restarted more often than once a day — which is every development
// machine, and any install on a container that redeploys.
func TestThePruneIntervalMatchesLive(t *testing.T) {
	b, err := os.ReadFile("../../testdata/db-prune-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		IntervalMs      int64 `json:"intervalMs"`
		RunsImmediately bool  `json:"runsImmediately"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if got := pruneInterval.Milliseconds(); got != c.IntervalMs {
		t.Errorf("the sweep runs every %d ms; live runs every %d", got, c.IntervalMs)
	}
	if !c.RunsImmediately {
		t.Fatal("live no longer runs the sweep immediately; this port still does")
	}
	src, err := os.ReadFile("prune_scheduler.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`go func\(\) \{\s*s\.runPrune\(\)`).Match(src) {
		t.Error("the sweep no longer runs once before entering its ticker loop, so a " +
			"process restarted more often than daily would never prune")
	}
}

// THE #105 MIGRATION IS GATED ON `standalone` TOO, and for the same reason as
// the retention sweep: it WRITES, and `tools/live-diff.sh` stands a Go server up
// against the LIVE /data with `-node` set.
//
// A proxying process is not the owner of that directory. Live agrees about the
// seam — its migration is an IIFE in `index.js`, the app — while the router seed
// stays in `routers.js` data access and runs for any reader, as this port's does.
func TestTheCollectionMigrationIsStandaloneOnly(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`srv\.standalone && srv\.store != nil`).Match(src) {
		t.Error("MigrateCollectionMode is no longer gated on `standalone`. It writes " +
			"router records and settings.json, and a process proxying to Node is " +
			"pointed at a /data it does not own.")
	}
}

// EVERY STARTUP ACTION THAT ACTS ON SHARED STATE HAS A GATE, AND THIS NAMES THEM.
//
// ── THE CLASS, WRITTEN DOWN AFTER TWO INSTANCES IN THREE DAYS ─────────────
//
// `tools/live-diff.sh` and `tools/live-socket-diff.js` stand a Go server up
// against the LIVE `/data` to compare payloads. Anything `New` does at startup,
// that server does too — to production.
//
//   - the RETENTION SWEEP deletes rows. Gated on `standalone && -retention`
//     after it was found running on `standalone` alone.
//   - the #105 MIGRATION rewrites routers and settings.json. Gated on
//     `standalone` after a diff run was found to have been safe only because
//     the install was already migrated.
//
// Both were added without asking whether a diff run would perform them. This
// test is the question, asked once, in a place that fails.
//
// ── WHAT IS DELIBERATELY UNGATED, AND WHY ─────────────────────────────────
//
// The ALERT EVALUATOR writes rows with no standalone gate, and that is a
// recorded decision rather than an oversight (`alert_wire.go`): "A row filed
// twice is a duplicate an operator can delete. A message sent twice is not. So
// the writes go in now." The port was designed to evaluate while proxying, so
// gating it here would undo the decision rather than protect anything. Named
// below so the next reader does not have to re-derive that.
func TestEveryStartupActionIsGated(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	for _, c := range []struct{ what, mustMatch, why string }{
		{"the background pool", `buildPool\(srv\.standalone && !opts\.NoPool\)`,
			"it holds a connection to every router"},
		{"the always-on alert pool", `buildAlertPool\(srv\.standalone && !opts\.NoPool\)`,
			"it holds a connection to every router"},
		{"the retention sweep", `buildPruneScheduler\(srv\.standalone && opts\.Retention\)`,
			"it DELETES rows"},
		{"the #105 migration", `srv\.standalone && srv\.store != nil`,
			"it rewrites router records and settings.json"},
		{"the backup scheduler", `buildBackupScheduler\(opts\.BackupScheduler\)`,
			"it writes files to routers"},
		{"the alert dispatch", `buildAlertDispatch\(opts\.AlertDispatch\)`,
			"it sends messages that cannot be un-received"},
		{"the history recorder", `buildHistoryWire\(opts\.History\)`,
			"it writes rows a second process would double"},
	} {
		if !regexp.MustCompile(c.mustMatch).MatchString(s) {
			t.Errorf("%s no longer matches its expected gate (%s), and %s. "+
				"If the gate changed deliberately, change it here too and say why; "+
				"a verification run against the live /data performs whatever this does.",
				c.what, c.mustMatch, c.why)
		}
	}
}

// EVERY HISTORY RECORDER IS FLUSHED ON SHUTDOWN.
//
// ── WHY THE COUNT IS THE ASSERTION ────────────────────────────────────────
//
// A history bucket rolls over only when the NEXT minute's first sample arrives,
// so a process that stops mid-minute leaves that minute unwritten unless
// something flushes. `internal/session` has always done it on Release and
// Shutdown. The POOL path — added with continuous history and now the PRIMARY
// recorder, because it is what records while nobody is watching — had no such
// call, so every restart silently lost the minute in progress.
//
// Counting the flush sites against the recorder sites is what found it. A test
// that only checked "the session flushes" would have passed throughout.
func TestBothHistoryRecordersAreFlushedOnShutdown(t *testing.T) {
	// The SESSION half, in its own package.
	sess, err := os.ReadFile("../session/session.go")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(regexp.MustCompile(`history\.Flush\(`).FindAll(sess, -1)); n < 2 {
		t.Errorf("session.go has %d history flush call(s), want at least 2 "+
			"(Release and Shutdown)", n)
	}

	// The POOL half, here.
	srv, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`historyWire\.Flush\(s\.alertPool\.HistoryRouter\(\)\)`).Match(srv) {
		t.Error("Shutdown does not flush the alertpool's history. That pool is the " +
			"recorder whenever no browser is open — almost always — so every restart " +
			"loses the minute in progress.")
	}
	// AND BEFORE THE POOL IS CLOSED, or the flush has nothing to flush from.
	// THE RECEIVER IS PART OF THE PATTERN. Without `s\.` the first match is the
	// COMMENT above the flush, which says "BEFORE `alertPool.Close()`" — so the
	// first version of this test reported the order was wrong when the code was
	// right, and would have had somebody "fix" correct code.
	flush := regexp.MustCompile(`s\.historyWire\.Flush\(`).FindIndex(srv)
	closed := regexp.MustCompile(`s\.alertPool\.Close\(\)`).FindIndex(srv)
	if flush == nil || closed == nil {
		t.Fatal("could not locate both the flush and the close")
	}
	if flush[0] > closed[0] {
		t.Error("the history flush runs AFTER alertPool.Close(); the collectors it " +
			"draws from are gone by then")
	}
}

// THE ALERT POOL IS RE-SYNCED WHENEVER THE EXCLUSION SET CHANGES.
//
// ── THE DEFECT THIS PINS ──────────────────────────────────────────────────
//
// `syncAlertPool` excludes every router that has a live `Session`, and nothing
// re-ran it at the moment that set changed. It was called from the Devices page,
// the routers API, the sites API and startup — never from `router:select`.
//
// So selecting a router left the pool holding it too: TWO system collectors
// feeding ONE evaluator. They disagree about `updateAvailable` — the rule fires
// on available-with-a-version and resolves on not-available — so the two sources
// alternated. MEASURED on 2026-08-30: 50 `routeros_update` rows in 24 hours on
// the active router, against ZERO in the live app's database over the same
// period, most already resolved.
//
// Both halves are asserted. `Acquire` without `Release` would leave a router
// covered by nothing once the last browser closed, which is the gap the
// always-on pool exists to close.
func TestTheAlertPoolIsResyncedWhenASessionTakesOrReleasesARouter(t *testing.T) {
	src, err := os.ReadFile("ws.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)

	acquire := strings.Index(s, "cn.srv.sessions.Acquire(")
	if acquire < 0 {
		t.Fatal("cannot find the Acquire call — this test is measuring nothing")
	}
	// SCOPED TO THIS FUNCTION, not "anywhere after the acquire". The first
	// version searched the rest of the FILE and found `releaseRouter`'s call
	// hundreds of lines below, so deleting the one on the select path left it
	// passing — the mutation said so. A test that accepts a match from a
	// different function is not testing this one.
	after := s[acquire:]
	if end := strings.Index(after, "\nfunc "); end >= 0 {
		after = after[:end]
	}
	// The re-sync must come AFTER the acquire, or the pool is asked to exclude a
	// session that does not exist yet and keeps the router.
	if i := strings.Index(after, "cn.srv.syncAlertPool()"); i < 0 {
		t.Error("router:select does not re-sync the alert pool after acquiring a " +
			"session. The pool then keeps the router the session just took, and two " +
			"system collectors feed one evaluator — which flapped routeros_update 50 " +
			"times in a day.")
	}

	rel := strings.Index(s, "func (cn *conn) releaseRouter()")
	if rel < 0 {
		t.Fatal("cannot find releaseRouter")
	}
	body := s[rel:]
	if j := strings.Index(body, "\n}"); j >= 0 {
		body = body[:j]
	}
	if !strings.Contains(body, "cn.srv.syncAlertPool()") {
		t.Error("releaseRouter does not re-sync the alert pool. The last browser " +
			"closing would leave that router covered by nothing — no status, no " +
			"alerts, no history.")
	}
}

// THE DISPATCHER IS BUILT AND NOT INVOKED, AND THE LOG MUST SAY SO.
//
// ── WHAT WAS MEASURED ─────────────────────────────────────────────────────
//
// `Evaluate()`'s return value — the `[]Fired` — is DISCARDED at both call sites
// (`alertpool_wire.go`, `session.go`), and `srv.dispatch` is assigned in `New`
// and never read. So a fired alert reaches no transport: rows are written, and
// nothing is sent.
//
// That is the correct state — PORT-QUEUE blocker 5, the caller is not ported —
// but `-alert-dispatch` announced "notifications will be SENT" next to
// `buildAlertWire`'s "NOTHING is dispatched", one line apart, at every startup
// for as long as the flag existed. Both were printed all week and the
// contradiction went unread.
//
// This test fails when the claim and the wiring disagree AGAIN — in either
// direction. If somebody ports the caller, `srv.dispatch` gains a reader and the
// second half fails, which is the prompt to restore the promise in the log.
func TestTheDispatchBannerMatchesTheWiring(t *testing.T) {
	wire, err := os.ReadFile("alert_wire.go")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}

	// Is the dispatcher READ anywhere outside its own construction?
	uses := regexp.MustCompile(`srv\.dispatch|s\.dispatch\b`).FindAll(srv, -1)
	assigned := regexp.MustCompile(`srv\.dispatch = `).FindAll(srv, -1)
	invoked := len(uses) > len(assigned)

	promises := regexp.MustCompile(`notifications will be SENT`).Match(wire)
	if invoked && !promises {
		t.Error("the dispatcher is now invoked but the startup banner still says the " +
			"caller is not ported — restore the promise, blocker 5 is closeable")
	}
	if !invoked && promises {
		t.Error("the startup banner promises notifications will be SENT, and nothing " +
			"reads srv.dispatch. That claim printed next to `NOTHING is dispatched` " +
			"for a week.")
	}

	// ── AND THE TWO BANNERS MUST NOT CONTRADICT EACH OTHER ────────────────
	//
	// The check above compares the banner against the WIRING and missed the
	// simpler failure: the same file claiming both things at once. It printed
	// "evaluator on — rows are written, NOTHING is dispatched" one line above
	// "DISPATCH IS ON — notifications will be SENT", at every startup, and was
	// still doing it during the cutover on 2026-08-30 — because this test
	// measured one half of the pair and called the pair checked.
	//
	// An UNCONDITIONAL denial cannot be true when the promise is reachable.
	// Whether anything is sent belongs to the dispatch banner, which states both
	// cases; the evaluator banner may only describe the evaluator.
	// MATCHES THE LOG STATEMENT, NOT THE PROSE. The first version of this check
	// searched the whole file and fired on the COMMENT that explains the fix —
	// the same "fooled by my own comment" shape that has cost this project four
	// tests already. A banner is a `log.Printf`, so that is what to look for.
	denies := regexp.MustCompile(`log\.Printf\("\[alert\][^"]*NOTHING is dispatched`).Match(wire)
	if denies && promises {
		t.Error("alert_wire.go prints an unconditional `NOTHING is dispatched` AND " +
			"`notifications will be SENT`. Both appear at every startup and one of " +
			"them is wrong whichever way the flag is set. The evaluator banner must " +
			"describe the evaluator only.")
	}
}

// EVERY BACKGROUND COMPONENT THE SERVER HOLDS IS STOPPED ON SHUTDOWN.
//
// ── THE CLASS: ASSIGNED AND NEVER READ ────────────────────────────────────
//
// Go does not warn about a struct field that is written and never read, so a
// component can be constructed, started, and left with an unreachable `Stop`.
// Three instances in this port before this test existed:
//
//   - the BACKUP SCHEDULER was built and never `Start`ed (`-backup-scheduler`
//     switched on something that could not act).
//   - the ALERT DISPATCHER is built and never invoked — `srv.dispatch` has no
//     reader at all, so no notification has ever been sent (LOOP.md 0k).
//   - the RETENTION SWEEP was assigned and never read, so `Stop` was
//     unreachable and its daily ticker outlived the server.
//
// Found by counting, not by reading: for each field, assignments against reads.
func TestEveryBackgroundComponentIsStoppedOnShutdown(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, "func (s *Server) Shutdown()")
	if i < 0 {
		t.Fatal("no Shutdown — this test is measuring nothing")
	}
	body := s[i:]
	if j := strings.Index(body, "\n}"); j >= 0 {
		body = body[:j]
	}

	for _, c := range []struct{ field, call string }{
		{"backupSched", "s.backupSched.Stop()"},
		{"pruneSched", "s.pruneSched.Stop()"},
		{"alertPool", "s.alertPool.Close()"},
		{"pool", "s.pool.Close()"},
		{"auditDB", "s.auditDB.Close()"},
	} {
		if !strings.Contains(body, c.call) {
			t.Errorf("Shutdown does not call %s. That component keeps running after the "+
				"server is gone — a timer, a goroutine or a connection with no owner.",
				c.call)
		}
	}
}
