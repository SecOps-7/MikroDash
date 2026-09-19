package db

// The alert counts the Routers page shows.
//
// The Alerts page's and the bell's reads are in alertfeed.go, and the Reports
// page's in history.go.

import "errors"

// CountOpenAlertsByRouter is how many alerts are still open, per router.
//
// ONE GROUPED QUERY, NOT ONE PER ROUTER. The Routers page refreshes every two
// seconds and asks about every router a session can see, so the per-router form
// would be N statements on a timer. The original says the same and uses the
// existing (router_id, fired_at) index.
//
// ── A ROUTER WITH NOTHING OPEN IS ABSENT, NOT ZERO ──────────────────────────
//
// Faithful to the original, which builds the object only from returned rows.
// The caller decides what "no alerts" looks like, and in Go that is what a map
// read already gives: `counts[id]` is 0 for a missing key, so the payload gets
// its zero without this pretending to have counted one.
func (d *DB) CountOpenAlertsByRouter() (map[string]int, error) {
	if d == nil || d.sql == nil {
		return map[string]int{}, errors.New("db not open")
	}
	rows, err := d.sql.Query(`
    SELECT router_id, COUNT(*) AS n
    FROM   alert_events
    WHERE  resolved_at IS NULL
    GROUP  BY router_id`)
	if err != nil {
		return map[string]int{}, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return map[string]int{}, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
