package server

import (
	"log"
	"sync"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/db"
)

// THE DAILY RETENTION SWEEP'S CALLER — live's `startPruneInterval`.
//
// ── THE HALF THE DEFECT WAS ACTUALLY ABOUT ────────────────────────────────
//
// `db.Prune` without this is another setting that is read and thrown away, which
// is precisely the shape being fixed: `dbRetentionDays`, `dbAlertRetentionDays`
// and `dbAuditRetentionDays` were rendered, validated and persisted, and no code
// in the port ever consulted one. Writing the sweep and not starting it would
// leave that true while looking solved — the same trap `buildBackupScheduler`
// fell into, where a flag switched on a component that could not act.
//
// So the test for this file reads the START site, not the sweep.
//
// ── STANDALONE ONLY ───────────────────────────────────────────────────────
//
// During coexistence the Node app runs `startPruneInterval` against the same
// database, and two sweeps would duplicate the work — harmlessly, since a DELETE
// by age is idempotent and the second finds nothing, but pointlessly. More to
// the point, retention is an INSTALL-wide policy and one process should own it.
// Standalone means there is no Node, so this process owns everything.
//
// ── THE SETTINGS ARE RE-READ ON EVERY SWEEP ───────────────────────────────
//
// Live's `run()` calls `getSettings()` each time rather than closing over a
// snapshot, so an operator who shortens retention sees it applied on the next
// sweep instead of the next restart. Reproduced: this reads the store per tick.
//
// That is a deliberate difference from `topN`, which the live app also reads
// from settings and deliberately does NOT re-read (see `Connections.WithTopN`).
// The two are not inconsistent — one is a collector's construction parameter,
// the other a policy the timer consults — and reproducing each as it is, is the
// port's contract.
type pruneScheduler struct {
	// mu guards `stop`, which Stop both reads and clears. See Stop.
	mu   sync.Mutex
	stop chan struct{}
}

// pruneInterval is live's `24 * 3600 * 1000` ms, pinned by the corpus.
const pruneInterval = 24 * time.Hour

// buildPruneScheduler starts the sweep, or says why it did not.
func (s *Server) buildPruneScheduler(enabled bool) *pruneScheduler {
	if !enabled {
		// The message names BOTH conditions, because it stopped being true when
		// -retention was added: saying only "standalone" sent a reader looking
		// for a mode they were already in.
		log.Printf("[db] retention sweep off; nothing ages out of the database " +
			"(needs -retention, and only runs standalone)")
		return nil
	}
	if s.auditDB == nil {
		// NOT a fatal condition. The app must serve when the database cannot be
		// opened — `auditDB` is nil exactly then — and refusing to start over a
		// sweep that would have nothing to sweep is worse than not sweeping.
		log.Printf("[db] retention sweep needs the database; not started")
		return nil
	}
	ps := &pruneScheduler{stop: make(chan struct{})}
	log.Printf("[db] retention sweep on (daily)")

	// IMMEDIATELY, THEN DAILY — live's `run(); _pruneTimer = setInterval(run, …)`.
	// The immediate run is what stops a process restarted every few hours from
	// never pruning at all, and `tools/db-prune-cases.js` asserts that the live
	// side still does it rather than assuming.
	go func() {
		s.runPrune()
		t := time.NewTicker(pruneInterval)
		defer t.Stop()
		for {
			select {
			case <-ps.stop:
				return
			case <-t.C:
				s.runPrune()
			}
		}
	}()
	return ps
}

// Stop ends the sweep. Safe to call twice, and safe on a nil scheduler.
//
// ── THE NIL-CHECK WAS NOT A GUARD ───────────────────────────────────────────
//
// This read `if ps != nil && ps.stop != nil { close(ps.stop); ps.stop = nil }`
// with no lock: check-then-act on shared state. Two callers both see a non-nil
// channel, both close it, and the second panics with "close of closed channel",
// which takes the process. `ps.stop` is also written unsynchronised, so it is a
// data race before it is a panic.
//
// One caller reaches it today — `Server.Shutdown` — so it was not reachable.
// That is an invariant of the CALLER, and this file already records what happens
// when this scheduler drifts from its sibling: the note in `server.go` explains
// that its `Stop` was once unreachable entirely while "the sibling two lines
// above was stopped correctly the whole time". Same asymmetry, second axis.
//
// `internal/backups`' `Scheduler.Stop` does its check-and-set under the
// scheduler mutex and says "Safe to call twice". This now matches it, so the
// guarantee belongs to Stop rather than to whoever calls it.
func (ps *pruneScheduler) Stop() {
	if ps == nil {
		return
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.stop == nil {
		return
	}
	close(ps.stop)
	ps.stop = nil
}

// runPrune reads the current policy and sweeps once.
func (s *Server) runPrune() {
	if s.auditDB == nil {
		return
	}
	n := s.auditDB.PruneLogged(s.retentionPolicy(), time.Now().UnixMilli())
	if n == 0 {
		// NO AUDIT ROW FOR A NO-OP. The live sweep records `db.prune` only when
		// it deleted something, and the reason is self-referential: this sweep is
		// also what ages audit rows out, so a daily row saying "deleted nothing"
		// would be the trail burying itself.
		return
	}
	// ALL FOUR FIELDS THE LIVE ROW CARRIES, and the three policies are the
	// point of it: "deleted 40,000 rows" says nothing an operator can act on
	// without the retention that produced it. `db.js`:
	//
	//	extra: { deleted: total, metricsDays: retentionDays,
	//	         eventsDays: alertRetentionDays, auditDays: auditRetentionDays }
	//
	// The RESOLVED policy is recorded, not the raw setting, so a row written
	// against an unwritten settings file says 90 rather than 0 — the number that
	// actually governed the delete.
	p := s.retentionPolicy()
	s.auditSystem(audit.Event{
		Action: "db.prune", TargetType: "database",
		Extra: []audit.KV{
			{Key: "deleted", Value: n},
			{Key: "metricsDays", Value: p.MetricDays()},
			{Key: "eventsDays", Value: p.AlertDays()},
			{Key: "auditDays", Value: p.AuditDays()},
		},
	})
}

// retentionPolicy reads the three settings; `db.PruneDays` supplies the
// fallbacks, so a missing key and an explicit zero behave alike — as they do in
// live's `s.dbRetentionDays || 90`.
//
// An unreadable settings file is NOT a reason to skip the sweep: every field
// then resolves to its live default (90/365/365), which is what an install that
// has changed nothing gets anyway. Skipping instead would mean a damaged
// settings file silently disables retention and the database grows without
// bound — the failure this file exists to prevent.
func (s *Server) retentionPolicy() db.PruneDays {
	if s.store == nil {
		return db.PruneDays{}
	}
	cfg, err := s.store.Settings()
	if err != nil {
		log.Printf("[db] retention: settings unreadable (%v); using defaults", err)
		return db.PruneDays{}
	}
	get := func(key string) int {
		switch n := cfg[key].(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
		return 0
	}
	return db.PruneDays{
		Metric: get("dbRetentionDays"),
		Alert:  get("dbAlertRetentionDays"),
		Audit:  get("dbAuditRetentionDays"),
	}
}
