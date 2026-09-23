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
// ── BEHIND -retention ─────────────────────────────────────────────────────
//
// Retention is an INSTALL-wide policy that DELETES, so it runs only when asked
// for (the image passes the flag).
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

// rollupInterval is how often the hour rows are brought up to date (#59).
//
// Minutes, not hours, and not because a completed hour changes after it is
// written. It is so an install restarted every few minutes — a container being
// rebuilt, a machine that sleeps — still reaches a tick, which is the same
// reason the daily sweep also runs immediately at startup. The pass is a
// grouped read over at most six hours of rows.
const rollupInterval = 5 * time.Minute

// buildPruneScheduler starts the database's maintenance timer, or says why it
// did not. It carries two jobs on two cadences, and THEY ARE BEHIND DIFFERENT
// FLAGS on purpose:
//
//   - The hourly roll-up rides with `-history`, because it only WRITES: it
//     summarises minute rows into hour rows and removes nothing. Every range
//     longer than the raw window reads the hour tables, so an install that
//     never rolled up would draw an empty month-long chart out of a database
//     full of traffic.
//   - Folding those minutes away, and the retention sweep itself, stay behind
//     `-retention`, this app's one switch that DELETES — off by default
//     precisely because that mistake cannot be undone. Summarising is not
//     deletion; removing the minutes afterwards is.
func (s *Server) buildPruneScheduler(retention, history bool) *pruneScheduler {
	if !retention {
		log.Printf("[db] retention sweep off; nothing ages out of the database " +
			"(pass -retention to enable)")
	}
	if s.auditDB == nil {
		// NOT a fatal condition. The app must serve when the database cannot be
		// opened — `auditDB` is nil exactly then — and refusing to start over a
		// sweep that would have nothing to sweep is worse than not sweeping.
		log.Printf("[db] retention sweep needs the database; not started")
		return nil
	}
	if !retention && !history {
		return nil
	}
	ps := &pruneScheduler{stop: make(chan struct{})}
	if retention {
		log.Printf("[db] retention sweep on (daily)")
	}
	if history {
		log.Printf("[db] traffic history rolled up hourly, every %s", rollupInterval)
	}

	// IMMEDIATELY, THEN DAILY — live's `run(); _pruneTimer = setInterval(run, …)`.
	// The immediate run is what stops a process restarted every few hours from
	// never pruning at all, and the db-prune corpus asserts that the live
	// side still does it rather than assuming.
	go func() {
		// A nil channel blocks for ever in a select, so a job that is switched
		// off costs one branch here and nothing in the loop.
		var rollC, dayC <-chan time.Time
		if history {
			s.runRollUp()
			t := time.NewTicker(rollupInterval)
			defer t.Stop()
			rollC = t.C
		}
		if retention {
			s.runPrune()
			t := time.NewTicker(pruneInterval)
			defer t.Stop()
			dayC = t.C
		}
		for {
			select {
			case <-ps.stop:
				return
			case <-rollC:
				s.runRollUp()
			case <-dayC:
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

// runRollUp brings the hour rows up to date. It never deletes; see the flag
// split on buildPruneScheduler.
func (s *Server) runRollUp() {
	if s.auditDB == nil {
		return
	}
	s.auditDB.RollUpRecent(time.Now().UnixMilli())
}

// runPrune reads the current policy and sweeps once.
func (s *Server) runPrune() {
	if s.auditDB == nil {
		return
	}
	// COMPACTION FIRST, AND THE ORDER IS THE SAFE ONE. The sweep below deletes
	// by age, and a minute row it removes is gone whether or not an hour row
	// stands for it. Compacting first means every minute the sweep can reach has
	// already been offered to the roll-up, so the coarse history survives the
	// fine one rather than both ending at the same date.
	s.auditDB.CompactLogged(time.Now().UnixMilli())

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
			{Key: "aiDays", Value: p.AIDays()},
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
		AI:     get("dbAiRetentionDays"),
	}
}
