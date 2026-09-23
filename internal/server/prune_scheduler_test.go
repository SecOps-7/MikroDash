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
	// BEHIND THE FLAG. It was `standalone && -retention` while a Node process
	// could own the /data; that mode is retired (2026-09-19), and the flag is
	// the half with teeth: the sweep is the only switch that DELETES, so it is
	// the one where a default-on mistake cannot be undone.
	if !regexp.MustCompile(`buildPruneScheduler\(opts\.Retention,`).Match(src) {
		t.Error("the retention sweep is no longer gated on the -retention flag. It DELETES.")
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
	// RE-AIMED 2026-09-23: the immediate call now sits inside `if retention {`,
	// because the same goroutine also carries the hourly roll-up and each job
	// starts only when its own flag is on (#59). The property is unchanged —
	// the sweep runs once before the loop — so this follows it there rather
	// than being deleted.
	if !regexp.MustCompile(`if retention \{\s*s\.runPrune\(\)`).Match(src) {
		t.Error("the sweep no longer runs once before entering its ticker loop, so a " +
			"process restarted more often than daily would never prune")
	}
	// The roll-up needs it for the same reason and more often: an install
	// redeployed every few minutes would otherwise never reach a tick at all.
	// AND IT CATCHES UP RATHER THAN COVERING THE ROUTINE WINDOW. Found live: a
	// restart leaves hours with minutes and no hour row, sitting BETWEEN hour
	// rows where no routine bound reaches them, and every long-range chart then
	// understates recent traffic while still drawing.
	if !regexp.MustCompile(`if history \{(?s).*?s\.runRollUp\(true\)`).Match(src) {
		t.Error("the hourly roll-up no longer catches up before entering its ticker " +
			"loop, so a restart leaves a hole the routine pass cannot see")
	}
}

// EVERY STARTUP ACTION THAT ACTS ON SHARED STATE HAS A GATE, AND THIS NAMES THEM.
//
// ── THE CLASS, WRITTEN DOWN AFTER TWO INSTANCES IN THREE DAYS ─────────────
//
// Anything `New` does at startup, every process pointed at that /data does too.
// This names each action and its gate, so a gate cannot change unnoticed.
//
// ── RE-AIMED 2026-09-19, DELIBERATELY ─────────────────────────────────────
//
// Every gate here carried `srv.standalone`, meaning "no -node was passed": a
// process proxying to the Node app beside it was not the owner of the /data and
// must not act on it. The coexistence mode is retired, so that half is gone and
// each gate is its flag alone. The #105 migration now runs whenever a store is
// open; its own guard makes a migrated install a no-op.
//
// ── WHAT IS DELIBERATELY UNGATED, AND WHY ─────────────────────────────────
//
// The ALERT EVALUATOR writes rows with no gate, and that is a
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
		{"the background pool", `buildPool\(!opts\.NoPool\)`,
			"it holds a connection to every router"},
		// WAS `buildAlertPool(...)`. The alert pool is gone; the same switch now
		// sets `holdFleet`, which is what decides whether a session is held for
		// every router nobody is watching. Same gate, same consequence, one
		// implementation instead of two.
		{"the always-on fleet holds", `srv\.holdFleet = !opts\.NoPool`,
			"it holds a connection to every router"},
		// TWO FLAGS SINCE 2026-09-23 (#59). The same builder now carries the
		// hourly roll-up, which only WRITES, so it rides with -history while
		// everything that deletes stays behind -retention. Both arguments are
		// named here so collapsing them back into one is a visible edit.
		{"the retention sweep", `buildPruneScheduler\(opts\.Retention, opts\.History\)`,
			"it DELETES rows"},
		{"the #105 migration", `if srv\.store != nil \{\s*if err := srv\.store\.MigrateCollectionMode`,
			"it rewrites router records and settings.json"},
		{"the backup scheduler", `buildBackupScheduler\(opts\.BackupScheduler\)`,
			"it writes files to routers"},
		{"the alert dispatch", `buildAlertDispatch\(opts\.AlertDispatch\)`,
			"it sends messages that cannot be un-received"},
		{"the report scheduler", `buildReportScheduler\(opts\.AlertDispatch\)`,
			"it emails reports, and two processes would each send them"},
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
	// `FlushAll`, not `Flush(HistoryRouter())`. That spelling named the ONE
	// router that recorded, back when reporting was whichever router was active.
	// With reporting a per-router setting there are several, and naming one
	// would lose the open minute for every other on each restart — the same data
	// loss this test exists for, narrowed to one router by accident.
	if !regexp.MustCompile(`historyWire\.FlushAll\(\)`).Match(srv) {
		t.Error("Shutdown does not flush the overview pool's history. That pool " +
			"records while the Devices page is open, so every restart loses the " +
			"minute in progress.")
	}
	// AND BEFORE THE POOL IS CLOSED, or the flush has nothing to flush from.
	// THE RECEIVER IS PART OF THE PATTERN. Without `s\.` the first match is the
	// COMMENT above the flush, so the first version of this test reported the
	// order was wrong when the code was right, and would have had somebody "fix"
	// correct code.
	//
	// ── AGAINST `pool.Close()`, NOT THE SESSIONS ──────────────────────────
	//
	// This used to name `s.alertPool.Close()`. That package is gone and the
	// background recorder is a HELD SESSION; `sessions.Shutdown()` flushes each
	// session as it tears it down, so that half is the manager's own invariant
	// and not something a source read of `Shutdown` can see. The overview pool
	// has no flush of its own, which is what leaves this line load-bearing.
	flush := regexp.MustCompile(`s\.historyWire\.FlushAll\(`).FindIndex(srv)
	closed := regexp.MustCompile(`s\.pool\.Close\(\)`).FindIndex(srv)
	if flush == nil || closed == nil {
		t.Fatal("could not locate both the flush and the close")
	}
	if flush[0] > closed[0] {
		t.Error("the history flush runs AFTER pool.Close(); the collectors it " +
			"draws from are gone by then")
	}
}

// THE ALERT POOL IS RE-SYNCED WHENEVER THE EXCLUSION SET CHANGES.
//
// ── THE DEFECT THIS PINS ──────────────────────────────────────────────────
//
// `syncFleetHolds` excludes every router that has a live `Session`, and nothing
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
	if i := strings.Index(after, "cn.srv.syncFleetHolds()"); i < 0 {
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
	if !strings.Contains(body, "cn.srv.syncFleetHolds()") {
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
// (the alert pool's wiring, and `session.go`), and `srv.dispatch` is assigned in `New`
// and never read. So a fired alert reaches no transport: rows are written, and
// nothing is sent.
//
// That is the correct state — cutover blocker 5, the caller is not ported —
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
