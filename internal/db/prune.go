package db

import (
	"fmt"
	"log"
)

// The DAILY RETENTION SWEEP — `prune()` and `startPruneInterval` in `src/db.js`.
//
// ── THE PORT HAD NONE, AND THE SETTINGS PAGE SAID OTHERWISE ────────────────
//
// `dbRetentionDays`, `dbAlertRetentionDays` and `dbAuditRetentionDays` are
// rendered by the Settings page, validated by the write route and persisted —
// and until 2026-08-29 nothing in this port ever read one. An operator could set
// a retention policy that did nothing while the database grew without bound.
//
// The third instance in two days of one shape: a setting rendered, validated,
// persisted, never consumed. The other two were the operator's own reports
// (`topN`, and `topTalkersN` beside it). This one was found by counting the
// class out of the generated settings table rather than waiting to be told.
//
// ── IT IS NOT `purge.go` ──────────────────────────────────────────────────
//
// `purge.go` is the operator-triggered cleanup card: it deletes by ROUTER, on
// demand, and deliberately excludes `audit_events`. This deletes by AGE, on a
// timer, across the whole install — and `audit_events` is in it, because age is
// the only thing allowed to remove an audit row.
//
// ── THE MAPPING IS LIFTED, NOT RETYPED ────────────────────────────────────
//
// The db-prune corpus reads the six DELETEs and three cutoffs out of the
// live `prune()`, and `prune_test.go` asserts this file against them. A seventh
// rule and a fourth policy have since been added for a table live never had;
// they are marked `portAdded` and held by their own ledger, so the recording
// keeps answering the one question it can. Two of the
// six are exactly the sort a retyped table gets wrong:
//
//   - `alert_events` keys on `fired_at`. The other five use `ts`.
//   - `connectivity_events` ages on the ALERT retention despite its column being
//     `ts` like the metrics. Aging it with the metrics would throw away a year
//     of connectivity history under a 90-day metric policy — and that is the
//     table the Reports page's outage view is drawn from.
//
// ── ZERO MEANS "DEFAULT", NOT "KEEP NOTHING" ──────────────────────────────
//
// The live cutoffs are `(retentionDays || 90) * 86400000`. A missing OR ZERO
// setting takes the default, so a settings file that has never had the field
// written keeps 90 days rather than deleting everything. Reproduced exactly:
// this is a delete path, and getting it backwards is unrecoverable.
type pruneRule struct {
	table  string
	column string
	days   func(p PruneDays) int
	// portAdded marks a rule this port added rather than lifted from the Node
	// app.
	//
	// THE PARITY LEDGER COMPARES POSITIONALLY against a recording of an app that
	// no longer exists, so a port-added rule has nothing there to compare
	// against. Appending one to the list would fail the length check before a
	// single table was examined — and widening the corpus to accept it would
	// stop it answering "does the port prune what live pruned", which is the
	// only question it is able to answer. So a port-added rule is excluded from
	// that comparison and recorded by its own ledger instead.
	portAdded bool
}

// PruneDays is the retention policies, in days.
//
// Three were lifted from live. `AI` is this port's own, for a table live never
// had.
type PruneDays struct {
	Metric int
	Alert  int
	Audit  int
	AI     int
}

// orDefault is the live `x || fallback`: zero and negative both take the default.
func orDefault(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// MetricDays, AlertDays and AuditDays resolve one policy each, applying the live
// fallback. EXPORTED because the audit row records the RESOLVED number: a row
// written against an unwritten settings file must say 90, the value that
// actually governed the delete, not the 0 that was stored.
func (p PruneDays) MetricDays() int { return orDefault(p.Metric, 90) }
func (p PruneDays) AlertDays() int  { return orDefault(p.Alert, 365) }
func (p PruneDays) AuditDays() int  { return orDefault(p.Audit, 365) }

// AIDays is the assistant's conversation history.
//
// THIRTY DAYS, AND SHORTER THAN EVERY OTHER POLICY ON PURPOSE. A transcript is
// the least operational and most personal thing this database holds: it is a
// record of what somebody asked about their own network, in their own words, and
// it earns its keep only while it is still the conversation they are having. The
// audit trail answers "who changed what" a year later; a chat thread from March
// answers nothing in September.
//
// It takes the same `orDefault` treatment as the rest, so zero means thirty
// rather than "keep nothing" — the trap the file header describes, and the one
// that is unrecoverable on a delete path.
func (p PruneDays) AIDays() int { return orDefault(p.AI, 30) }

// pruneRules is the six lifted DELETEs in the live order, then this port's own.
//
// ORDER IS PRESERVED because the log line reports one total, and a reader
// comparing it against the live app's should not have to reconcile two
// orderings. It has no effect on the result.
var pruneRules = []pruneRule{
	{"ping_samples", "ts", PruneDays.MetricDays, false},
	{"traffic_samples", "ts", PruneDays.MetricDays, false},
	{"bandwidth_usage", "ts", PruneDays.MetricDays, false},
	{"alert_events", "fired_at", PruneDays.AlertDays, false},
	{"connectivity_events", "ts", PruneDays.AlertDays, false},
	// AUDIT ROWS AGE OUT HERE AND NOWHERE ELSE. The live comment: they are
	// "absent from PURGE_TABLES and from deleteRouterData() on purpose, so age is
	// the ONLY thing that can remove one — nobody can aim a delete at a single
	// event." `purge.go` already excludes them for that reason; this is the other
	// half of the arrangement, and without it the trail was immortal rather than
	// retained.
	{"audit_events", "ts", PruneDays.AuditDays, false},

	// ── PORT-ADDED, AND THE ONLY RULE THAT IS ────────────────────────────────
	//
	// Live had no assistant and therefore no transcript to age. This is the
	// deliberate divergence from the lifted mapping, marked so the parity ledger
	// can keep comparing the six against the recording while a separate ledger
	// holds this one to account.
	//
	// It is the ONLY thing besides an operator pressing Clear that removes a
	// conversation: `routerPurgeExcluded` in purge.go records why removing a
	// router must not.
	{"ai_messages", "ts", PruneDays.AIDays, true},
}

const msPerDay = 86400000

// Prune deletes everything older than each policy and returns the row count.
//
// `now` is a parameter rather than `time.Now()` so a test can place rows either
// side of a cutoff without sleeping.
func (d *DB) Prune(p PruneDays, now int64) (int, error) {
	if d == nil || d.sql == nil {
		return 0, nil
	}
	total := 0
	for _, r := range pruneRules {
		cutoff := now - int64(r.days(p))*msPerDay
		// `r.table` and `r.column` come from `pruneRules`, a package-level
		// literal, and never from a request — there is no placeholder form for
		// an identifier in SQL.
		res, err := d.sql.Exec("DELETE FROM "+r.table+" WHERE "+r.column+" < ?", cutoff)
		if err != nil {
			// The count SO FAR is returned with the error rather than discarded:
			// the rows really are gone, and a caller that logged 0 would be
			// wrong about the database it just changed.
			return total, fmt.Errorf("db: prune %s: %w", r.table, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// PruneLogged is `Prune` for the daily timer, which has nowhere to return an
// error.
//
// It reports the count so the caller can decide whether to record an audit row —
// the live sweep records `db.prune` ONLY when it deleted something, because a
// daily no-op that recorded itself would bury the very trail it is written into.
func (d *DB) PruneLogged(p PruneDays, now int64) int {
	n, err := d.Prune(p, now)
	if err != nil {
		log.Printf("[db] prune: %v (%d rows deleted before it stopped)", err, n)
	}
	if n > 0 {
		log.Printf("[db] pruned %d rows (metrics: %dd, events: %dd)",
			n, p.MetricDays(), p.AlertDays())
	}
	return n
}
