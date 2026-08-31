package db

import "errors"

// The two feeds the notification bell reads.
//
// ── THE RULE THAT KEPT THESE UNPORTED, AND HOW IT WAS HONOURED ──────────────
//
// `alerts.go` records it: "a read nothing calls is a read nothing gates", and
// warns that porting a query ahead of its caller adds code no corpus drives and
// no gate covers. That rule is not waived here — it is satisfied. These land
// WITH `tools/alertfeed-cases.js`, which seeds a database, runs the LIVE queries
// against it and records both the rows and the answers, so there is no window in
// which they exist and nothing exercises them. The bell (queue item 14) is the
// production caller and follows.

// AlertRow is one row of either feed.
//
// Subject, Detail, ResolvedAt, AcknowledgedAt and AcknowledgedBy are POINTERS
// because every one of them is nullable and the difference is visible: an
// unacknowledged alert with a zero timestamp would render as "seen at the epoch"
// rather than as unseen.
type AlertRow struct {
	ID             int64   `json:"id"`
	RouterID       string  `json:"router_id"`
	AlertType      string  `json:"alert_type"`
	Subject        *string `json:"subject"`
	Detail         *string `json:"detail"`
	FiredAt        int64   `json:"fired_at"`
	ResolvedAt     *int64  `json:"resolved_at"`
	AcknowledgedAt *int64  `json:"acknowledged_at"`
	AcknowledgedBy *string `json:"acknowledged_by"`
}

// The defaults the live queries apply when no limit is given.
const (
	OpenAlertsDefaultLimit   = 200
	RecentAlertsDefaultLimit = 50
)

// OpenAlerts is every alert still open on one router, newest FIRED first.
//
// A LIMIT OF ZERO TAKES THE DEFAULT. The live expression is `limit || 200`, and
// zero is falsy in JavaScript — so a caller passing 0 gets 200 rows, not none. A
// port passing the zero through to SQL returns an empty feed and the bell
// silently shows nothing, which looks exactly like "no alerts".
func (d *DB) OpenAlerts(routerID string, limit int) ([]AlertRow, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db: not open")
	}
	if limit <= 0 {
		limit = OpenAlertsDefaultLimit
	}
	return d.scanAlerts(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events
    WHERE  router_id = ? AND resolved_at IS NULL
    ORDER  BY fired_at DESC LIMIT ?
  `, routerID, limit)
}

// RecentAlerts is every alert on one router that has RESOLVED since `sinceTS`,
// newest RESOLVED first.
//
// ── "RECENT" MEANS RESOLVED, NOT LATELY ─────────────────────────────────────
//
// An alert that is still open is never in this feed, however recently it fired.
// Reading "recent" as "lately" would put every open alert in both lists and the
// bell would count each of them twice.
//
// ── AND IT SORTS ON A DIFFERENT COLUMN FROM OpenAlerts ──────────────────────
//
// `resolved_at DESC`, not `fired_at DESC`. The two disagree whenever an alert
// fires early and resolves late: it outranks one that fired late and resolved
// early. Sorting on the wrong column produces a list that looks plausible and is
// backwards, which is why the corpus carries exactly that pair — and refuses to
// pass if the seed cannot tell the two orders apart.
//
// `sinceTS` is compared with `>=`, so a row resolved exactly at the boundary is
// included.
func (d *DB) RecentAlerts(routerID string, sinceTS int64, limit int) ([]AlertRow, error) {
	if d == nil || d.sql == nil {
		return nil, errors.New("db: not open")
	}
	if limit <= 0 {
		limit = RecentAlertsDefaultLimit
	}
	if sinceTS < 0 {
		sinceTS = 0
	}
	return d.scanAlerts(`
    SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events
    -- resolved_at IS NOT NULL is REDUNDANT beside the comparison, and kept
    -- anyway: in SQL's three-valued logic NULL >= x is NULL and never true, so
    -- an open alert is excluded either way. Mutation testing says so — dropping
    -- it changes nothing. It stays because it states the intent the function's
    -- name makes, and because a later COALESCE(resolved_at, 0) would silently
    -- pull every open alert into this feed without it.
    --
    -- (No backticks in here: this is a Go raw string, and one would end it.)
    WHERE  router_id = ? AND resolved_at IS NOT NULL AND resolved_at >= ?
    ORDER  BY resolved_at DESC LIMIT ?
  `, routerID, sinceTS, limit)
}

func (d *DB) scanAlerts(query string, args ...any) ([]AlertRow, error) {
	rows, err := d.sql.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := []AlertRow{}
	for rows.Next() {
		var r AlertRow
		if err := rows.Scan(&r.ID, &r.RouterID, &r.AlertType, &r.Subject, &r.Detail,
			&r.FiredAt, &r.ResolvedAt, &r.AcknowledgedAt, &r.AcknowledgedBy); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
