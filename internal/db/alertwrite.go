package db

import (
	"database/sql"
	"log"
	"time"
)

// The alert table's WRITE side: the three calls `alert.Store` needs.
//
// ── `subject IS ?`, NEVER `= ?` ────────────────────────────────────────────
//
// `subject` is NULL for router-wide alerts — a CPU alarm has no interface and no
// peer — and `= NULL` matches nothing in SQL. The live queries use `IS` for
// exactly this, and a port using `=` would file a fresh row every evaluation for
// every router-wide alert while never resolving one.
//
// ── "ALREADY OPEN?" IS ASKED OF THE DATABASE, NOT THE EVALUATOR ────────────
//
// The live comment is worth carrying whole, because it explains why this exists
// at all rather than being answered from memory:
//
//	the evaluator keeps edge-detection state in memory, and dropEvaluator()
//	wipes it on a router switch, a session rebuild and — most often — an idle
//	teardown, when nobody has had the router's page open for a while. The
//	rebuilt evaluator has no memory of having reported the thing, so it reports
//	it again. For a condition that persists, like an available RouterOS update,
//	that meant a fresh unacknowledged row every time somebody came back to the
//	page, and acknowledging one did nothing about the next.
//
// So the rule is "at most one unresolved row per (router, type, subject)", and
// the database is the only thing that survives an evaluator drop.

// HasOpenAlert reports whether an unresolved row already exists.
//
// FALSE ON ERROR, deliberately, and the live function returns false with no
// database too. The consequence is stated in its comment: callers "behave as
// they did before, which is to say something rather than nothing". For an
// alerter, a duplicate notification is a smaller failure than a silent one — the
// opposite of the fail-closed rule that governs permissions, and for the
// opposite reason.
func (d *DB) HasOpenAlert(routerID, alertType, subject string) bool {
	if d == nil || d.sql == nil {
		return false
	}
	var one int
	err := d.sql.QueryRow(`
		SELECT 1 FROM alert_events
		WHERE router_id = ? AND alert_type = ? AND subject IS ? AND resolved_at IS NULL
		LIMIT 1`, routerID, alertType, nullIfEmpty(subject)).Scan(&one)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[db] hasOpenAlert(%s/%s): %v", routerID, alertType, err)
		return false
	}
	return err == nil
}

// InsertAlertEvent files an open row and returns its id.
//
// `now` is passed in rather than read here so one evaluation stamps every row it
// files with the same instant — the live code calls `Date.now()` per insert,
// which can straddle a millisecond within a single event and makes two alerts
// fired together sort as though they were not.
func (d *DB) InsertAlertEvent(routerID, alertType, subject, detail string, now int64) int64 {
	if d == nil || d.sql == nil {
		return 0
	}
	res, err := d.sql.Exec(`
		INSERT INTO alert_events (router_id, alert_type, subject, detail, fired_at)
		VALUES (?, ?, ?, ?, ?)`,
		routerID, alertType, nullIfEmpty(subject), nullIfEmpty(detail), now)
	if err != nil {
		log.Printf("[db] insertAlertEvent(%s/%s): %v", routerID, alertType, err)
		return 0
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0
	}
	return id
}

// ResolveAlertEvent closes every matching open row and returns their ids.
//
// ── THE IDS ARE THE POINT, AND THEY ARE SELECTED FIRST ─────────────────────
//
// The browser's bell needs to know exactly which entries just resolved; without
// the ids it would have to re-derive the match by type and subject on the
// client, which is a second implementation of the rule this UPDATE already
// encodes. They are selected BEFORE the update because the WHERE clause stops
// matching after it.
//
// EVERY matching row, not one. The live function has always done that, and its
// comment names it as "the tell that duplicates were being created".
func (d *DB) ResolveAlertEvent(routerID, alertType, subject string, now int64) []int64 {
	if d == nil || d.sql == nil {
		return []int64{}
	}
	subj := nullIfEmpty(subject)
	rows, err := d.sql.Query(`
		SELECT id FROM alert_events
		WHERE router_id = ? AND alert_type = ? AND subject IS ? AND resolved_at IS NULL`,
		routerID, alertType, subj)
	if err != nil {
		log.Printf("[db] resolveAlertEvent(%s/%s): %v", routerID, alertType, err)
		return []int64{}
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if len(ids) == 0 {
		return []int64{}
	}
	if _, err := d.sql.Exec(`
		UPDATE alert_events SET resolved_at = ?
		WHERE router_id = ? AND alert_type = ? AND subject IS ? AND resolved_at IS NULL`,
		now, routerID, alertType, subj); err != nil {
		log.Printf("[db] resolveAlertEvent update(%s/%s): %v", routerID, alertType, err)
		// THE IDS ARE STILL RETURNED. They were open when this was asked, and a
		// caller told "nothing resolved" would leave the bell showing rows the
		// next read will contradict.
	}
	return ids
}

// nullIfEmpty maps Go's zero string onto SQL NULL.
//
// `subject || null` and `detail || null` in the live inserts. Go has no
// undefined, so the empty string is the only thing that can arrive for "no
// subject" — and storing "" instead of NULL would make `subject IS NULL` miss
// every router-wide alert this port filed.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ── THE ROUTES' WRITES ─────────────────────────────────────────────────────
//
// RESTORED 2026-08-29 after this file was accidentally overwritten. The three
// functions below were reconstructed from `alertwrite_test.go`, which survived
// and pins them against `testdata/alertwrite-cases.json`, and from the live
// `src/db.js`. The test is the specification; if anything here is subtly wrong,
// it fails rather than passing quietly.

// AlertRouterID answers which router an alert belongs to, or "" if there is no
// such row.
//
// A SEPARATE LOOKUP before the acknowledge, because the permission question is
// "may you act on THIS router" and the row is the only thing that knows which
// router that is. Folding it into the write would mean deciding permission after
// mutating.
func (d *DB) AlertRouterID(id int64) (string, error) {
	if d == nil || d.sql == nil {
		return "", nil
	}
	var routerID string
	err := d.sql.QueryRow(`SELECT router_id FROM alert_events WHERE id = ?`, id).Scan(&routerID)
	if err == sql.ErrNoRows {
		// NOT AN ERROR. The corpus records null for "no such alert", and the
		// caller turns "" into a 404 — an error here would become a 500 and tell
		// the operator the database was broken when the id was simply wrong.
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return routerID, nil
}

// AcknowledgeAlert marks one row seen and returns it as it now stands.
//
// ── IT DOES NOT REQUIRE THE ALERT TO BE OPEN ───────────────────────────────
//
// The live comment: "acknowledging something after it recovered is a legitimate
// way to say 'seen it'". So the UPDATE is guarded on `acknowledged_at IS NULL`
// and NOT on `resolved_at IS NULL`.
//
// ── AND IT RE-READS RATHER THAN REPORTING WHAT IT WROTE ────────────────────
//
// The row comes back from a SELECT after the UPDATE, so a row someone else had
// already acknowledged returns THEIR name and time rather than this caller's.
// The `acknowledged_at IS NULL` guard is what makes the first acknowledgement
// the one that sticks.
func (d *DB) AcknowledgeAlert(id int64, username string) (*AlertRow, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	if _, err := d.sql.Exec(`
		UPDATE alert_events SET acknowledged_at = ?, acknowledged_by = ?
		WHERE id = ? AND acknowledged_at IS NULL`,
		time.Now().UnixMilli(), nullIfEmpty(username), id); err != nil {
		return nil, err
	}
	row, err := d.alertRowByID(id)
	if err != nil {
		return nil, err
	}
	return row, nil
}

// ResolveAllAlerts clears a router's list: resolve every open row, and
// acknowledge them on the way past.
//
// ── "CLEAR" MUST RESOLVE, NOT JUST ACKNOWLEDGE ─────────────────────────────
//
// The Routers page counts OPEN alerts. The live version of this used to
// acknowledge only, which "emptied the bell while leaving the router reading
// 'Alerting' forever" — and an alert whose condition went away without the
// evaluator seeing it clear has no other route out of the open set.
//
// ── THE ACKNOWLEDGE IS GUARDED, THE RESOLVE IS NOT ─────────────────────────
//
// `AND acknowledged_at IS NULL` on the first UPDATE: whoever clears the list is
// the one who saw it, but a row someone else already acknowledged keeps THEIR
// name. Without that guard, rows resolved by a person and attributed to nobody
// read in Reports exactly like the evaluator having resolved them on its own.
//
// ── ONE INSTANT ACROSS BOTH STATEMENTS ─────────────────────────────────────
//
// `now` is read ONCE. A row this clears is both acknowledged and resolved, and
// two `Date.now()` calls would stamp the two columns milliseconds apart — which
// reads as "acknowledged, then resolved a moment later" rather than as one act.
// `TestClearAllStampsOneInstant` kills an inlined clock.
//
// ROWS ARE KEPT, NEVER DELETED, so Reports and the CSV export still show what
// happened. Deleting is a separate deliberate act and lives in Settings →
// Database.
func (d *DB) ResolveAllAlerts(routerID, username string) ([]int64, error) {
	if d == nil || d.sql == nil {
		return []int64{}, nil
	}
	rows, err := d.sql.Query(
		`SELECT id FROM alert_events WHERE router_id = ? AND resolved_at IS NULL`, routerID)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []int64{}, nil
	}

	now := time.Now().UnixMilli()
	tx, err := d.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`
		UPDATE alert_events SET acknowledged_at = ?, acknowledged_by = ?
		WHERE router_id = ? AND resolved_at IS NULL AND acknowledged_at IS NULL`,
		now, nullIfEmpty(username), routerID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE alert_events SET resolved_at = ? WHERE router_id = ? AND resolved_at IS NULL`,
		now, routerID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

// alertRowByID reads one row in the feed's shape.
func (d *DB) alertRowByID(id int64) (*AlertRow, error) {
	var r AlertRow
	err := d.sql.QueryRow(`
		SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
		       acknowledged_at, acknowledged_by
		FROM alert_events WHERE id = ?`, id).Scan(
		&r.ID, &r.RouterID, &r.AlertType, &r.Subject, &r.Detail,
		&r.FiredAt, &r.ResolvedAt, &r.AcknowledgedAt, &r.AcknowledgedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}
