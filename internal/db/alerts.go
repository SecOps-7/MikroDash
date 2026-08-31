package db

// The alert counts the Routers page shows.
//
// ── ONLY THE GROUPED COUNT IS HERE, AND THAT IS THE RULE ────────────────────
//
// A READ NOTHING CALLS IS A READ NOTHING GATES. `src/db.js` has three more alert
// reads — `queryOpenAlerts`, `queryRecentAlerts` and `queryAlertEvents` — and
// none is ported, because nothing in this port asks for them: the Alerts page
// and the notification bell are both unported, and `wiring-audit` records the
// bell's five ids as blocked on the alert FEED.
//
// Porting a query ahead of its caller would add code no corpus drives and no
// gate covers, and it would read as progress. When the bell lands, its query
// lands with it and is gated by the same work.
//
// (This sentence used to end "see the package header's rule that this side stays
// the smaller of the two". The package header carries no such rule — the rule is
// this paragraph, and it now says so itself rather than pointing at a
// cross-reference that was never there. Third comment found this session naming
// something that does not exist; the other two were a gate and a generator.)

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
