package db

import (
	"errors"
	"fmt"
	"time"
)

// Config Management's run and target states. Every state is written BEFORE
// the router command it describes, so a crash leaves the row saying what was
// under way rather than what had finished.
const (
	CfgRunPreflight       = "preflight"
	CfgRunAwaitingConfirm = "awaiting-confirm"
	CfgRunCanary          = "canary"
	CfgRunAwaitingCanary  = "awaiting-canary"
	CfgRunRolling         = "rolling"
	CfgRunDone            = "done"
	CfgRunHalted          = "halted"
	CfgRunCancelled       = "cancelled"
	CfgRunInterrupted     = "interrupted"
	CfgRunExpired         = "expired"

	CfgTargetPending         = "pending"
	CfgTargetPreflightOK     = "preflight-ok"
	CfgTargetPreflightFailed = "preflight-failed"
	CfgTargetApplying        = "applying"
	CfgTargetApplied         = "applied"
	CfgTargetFailed          = "failed"
	CfgTargetFailedPartial   = "failed-partial"
	CfgTargetUnknown         = "unknown"
	CfgTargetNotAttempted    = "not-attempted"
)

// cfgRunFinished lists the states a run never leaves.
var cfgRunFinished = []string{CfgRunDone, CfgRunHalted, CfgRunCancelled, CfgRunInterrupted, CfgRunExpired}

// InterruptCfgRuns closes every run a previous process left open, and reports
// how many.
//
// ── NOTHING RESUMES ─────────────────────────────────────────────────────────
//
// A run's secret values lived only in that process's memory, so a restart
// cannot continue one even if it wanted to. It also should not: the operator
// who confirmed the run confirmed it for then, against the routers as they
// were then. So an open run becomes `interrupted`, and its targets say what
// can be known about them:
//
//   - one that was `applying` becomes `unknown`: the import may have run to
//     the end, stopped partway, or not started, and the only honest answer is
//     to look, from the restore point taken before it;
//   - one not yet applied becomes `not-attempted`;
//   - one already finished keeps its state.
//
// Called from cmd/mikrodash beside Migrate, never from Open: cmd/compat opens
// a real /data read-only.
func (d *DB) InterruptCfgRuns() (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	now := time.Now().UnixMilli()
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	open := `run_id IN (SELECT id FROM cfg_runs WHERE state NOT IN (?,?,?,?,?))`
	fin := make([]any, len(cfgRunFinished))
	for i, s := range cfgRunFinished {
		fin[i] = s
	}
	if _, err := tx.Exec(`UPDATE cfg_run_targets
	    SET state = ?, finished_at = ?,
	        warning = CASE WHEN backup_id IS NULL
	          THEN 'MikroDash restarted while this router was being changed. Check the router.'
	          ELSE 'MikroDash restarted while this router was being changed. Check the router; '
	               || 'restore point #' || backup_id || ' was taken before the change.' END
	    WHERE state = ? AND `+open,
		append([]any{CfgTargetUnknown, now, CfgTargetApplying}, fin...)...); err != nil {
		return 0, fmt.Errorf("interrupting targets in flight: %w", err)
	}
	if _, err := tx.Exec(`UPDATE cfg_run_targets SET state = ?
	    WHERE state IN (?,?) AND `+open,
		append([]any{CfgTargetNotAttempted, CfgTargetPending, CfgTargetPreflightOK}, fin...)...); err != nil {
		return 0, fmt.Errorf("interrupting targets not started: %w", err)
	}
	res, err := tx.Exec(`UPDATE cfg_runs
	    SET state = ?, updated_at = ?, finished_at = ?,
	        error = 'MikroDash restarted during this run. Nothing resumes on its own.'
	    WHERE state NOT IN (?,?,?,?,?)`,
		append([]any{CfgRunInterrupted, now, now}, fin...)...)
	if err != nil {
		return 0, fmt.Errorf("interrupting runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// PinnedBackupIDs is every backup a full-binary template is made of.
// Retention skips them and the operator's delete refuses them: the template
// would otherwise point at files that are gone.
func (d *DB) PinnedBackupIDs() (map[int64]bool, error) {
	out := map[int64]bool{}
	if d == nil || d.sql == nil {
		return out, errors.New("db not open")
	}
	rows, err := d.sql.Query(`SELECT DISTINCT backup_id FROM cfg_templates WHERE backup_id IS NOT NULL`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return out, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// DeleteCfgBaselinesForRouter removes the drift baselines of a router that
// has left the fleet. A baseline is what drift compares the router against;
// with the router gone it describes nothing. The run ledger (cfg_run_targets)
// is kept, for the reason routerPurgeExcluded gives.
func (d *DB) DeleteCfgBaselinesForRouter(routerID string) (int64, error) {
	if d == nil || d.sql == nil {
		return 0, errors.New("db not open")
	}
	res, err := d.sql.Exec(`DELETE FROM cfg_baselines WHERE router_id = ?`, routerID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
