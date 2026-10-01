package db

// Reading and writing `monitor_runs`: when each router was being observed. The
// table and the reason it exists are in monitorruns_schema.go.

import (
	"strings"
)

// MonitorRun is one stretch of observation of one router, in epoch ms.
type MonitorRun struct {
	RouterID   string
	StartedAt  int64
	LastSeenAt int64
}

// OpenMonitorRun starts a run at `at` and returns its id, which the writer keeps
// to touch and close it. `last_seen_at` starts equal to `started_at`, so a run
// that is opened and the process dies a second later still records the second.
func (d *DB) OpenMonitorRun(routerID string, at int64) (int64, error) {
	res, err := d.sql.Exec(
		`INSERT INTO monitor_runs (router_id, started_at, last_seen_at) VALUES (?, ?, ?)`,
		routerID, at, at)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// TouchMonitorRuns moves `last_seen_at` to `at` for every run named, in ONE
// statement. That is the whole of the heartbeat's cost: one UPDATE a minute for
// the fleet, however large the fleet is.
func (d *DB) TouchMonitorRuns(ids []int64, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, at)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := d.sql.Exec(
		`UPDATE monitor_runs SET last_seen_at = ? WHERE id IN (?`+
			strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

// MonitorRunsIn is every run that OVERLAPS [from, to], for the whole fleet,
// keyed by router - one query, however many routers the caller asks about.
//
// Overlap, not containment: a run that started before the window and is still
// open covers the window's start, and dropping it would draw that start grey.
func (d *DB) MonitorRunsIn(from, to int64) (map[string][]MonitorRun, error) {
	rows, err := d.sql.Query(
		`SELECT router_id, started_at, last_seen_at FROM monitor_runs
		  WHERE last_seen_at >= ? AND started_at <= ?
		  ORDER BY router_id, started_at`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]MonitorRun{}
	for rows.Next() {
		var r MonitorRun
		if err := rows.Scan(&r.RouterID, &r.StartedAt, &r.LastSeenAt); err != nil {
			return nil, err
		}
		out[r.RouterID] = append(out[r.RouterID], r)
	}
	return out, rows.Err()
}
