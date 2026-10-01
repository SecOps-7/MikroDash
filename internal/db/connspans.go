package db

// The fleet-wide reads behind the Devices page's overview: each router's
// connectivity strip and its last backup, in a FIXED number of queries however
// large the fleet is. One query per router per card would be a hundred queries
// for a hundred devices on every refresh.

import (
	"database/sql"
	"strings"

	"mikrodash/internal/history"
)

// ConnStatesAt is every router's state AT moment t: the newest connectivity row
// at or before it. This is the "state before the window" a strip starts from,
// and there was no query for it - so a window could not tell an outage that
// began yesterday from one that never happened.
//
// SQLite's documented bare-column rule does the work: with `MAX(ts)` in the
// select list, `connected` is taken from the row that holds the maximum.
// `TestConnStatesAtTakesTheNewestRowAtOrBefore` pins that, because it is a
// SQLite guarantee rather than SQL's.
func (d *DB) ConnStatesAt(t int64) (map[string]bool, error) {
	rows, err := d.sql.Query(`
    SELECT router_id, connected, MAX(ts) FROM connectivity_events
    WHERE  ts <= ? GROUP BY router_id`, t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var connected int
		var ts int64
		if err := rows.Scan(&id, &connected, &ts); err != nil {
			return nil, err
		}
		out[id] = connected != 0
	}
	return out, rows.Err()
}

// ConnEventsIn is every transition strictly after `from` and at or before `to`,
// for the whole fleet, oldest first per router. Rows at or before `from` are
// `ConnStatesAt`'s, so the two never count one row twice.
//
// NO LIMIT: these are transitions, not samples - one row per state change - so a
// month for a fleet is small, and a cap would silently drop the newest outages.
func (d *DB) ConnEventsIn(from, to int64) (map[string][]history.ConnEvent, error) {
	rows, err := d.sql.Query(`
    SELECT router_id, ts, connected FROM connectivity_events
    WHERE  ts > ? AND ts <= ? ORDER BY router_id, ts`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]history.ConnEvent{}
	for rows.Next() {
		var id string
		var e history.ConnEvent
		var connected int
		if err := rows.Scan(&id, &e.TS, &connected); err != nil {
			return nil, err
		}
		e.Connected = connected != 0
		out[id] = append(out[id], e)
	}
	return out, rows.Err()
}

// BackupSucceeded is every `config_backups.outcome` that means a usable restore
// point was taken: a new configuration, or one identical to the last. Declared
// here because `db` cannot import `internal/backups`, which imports `db`;
// `backups.TestTheOverviewKnowsWhichOutcomesSucceeded` holds the two together.
var BackupSucceeded = []string{"changed", "unchanged"}

// BackupBrief is one router's backup state at a glance.
type BackupBrief struct {
	// LastAt and LastOutcome are the NEWEST run, of any outcome - "the last
	// thing that happened".
	LastAt      int64  `json:"lastAt"`
	LastOutcome string `json:"lastOutcome"`
	// LastSuccessAt is the newest run that produced a restore point. It is the
	// answer to "when could I last have restored this?", which the newest run
	// does not give when that run FAILED. Nil when none ever has.
	LastSuccessAt *int64 `json:"lastSuccessAt"`
}

// BackupOverview is every router's BackupBrief, in one query.
func (d *DB) BackupOverview() (map[string]BackupBrief, error) {
	// THE IN-LIST IS BUILT FROM THE SLICE, so adding an outcome there cannot
	// leave this query asking about the old set.
	marks := strings.TrimSuffix(strings.Repeat("?,", len(BackupSucceeded)), ",")
	args := make([]any, len(BackupSucceeded))
	for i, o := range BackupSucceeded {
		args[i] = o
	}
	rows, err := d.sql.Query(`
    SELECT b.router_id, b.taken_at, b.outcome,
           (SELECT MAX(s.taken_at) FROM config_backups s
             WHERE s.router_id = b.router_id AND s.outcome IN (`+marks+`)) AS last_success
    FROM   config_backups b
    WHERE  b.taken_at = (SELECT MAX(m.taken_at) FROM config_backups m
                          WHERE m.router_id = b.router_id)
    ORDER  BY b.router_id, b.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]BackupBrief{}
	for rows.Next() {
		var id string
		var b BackupBrief
		var success sql.NullInt64
		if err := rows.Scan(&id, &b.LastAt, &b.LastOutcome, &success); err != nil {
			return nil, err
		}
		// Two runs stamped in the same millisecond: the higher id - the later
		// insert - is first, and it is the one kept.
		if _, have := out[id]; have {
			continue
		}
		if success.Valid {
			v := success.Int64
			b.LastSuccessAt = &v
		}
		out[id] = b
	}
	return out, rows.Err()
}
