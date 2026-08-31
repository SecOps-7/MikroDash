package db

import (
	"database/sql"
	"fmt"
	"log"

	"mikrodash/internal/history"
)

// The history tables' WRITE side: the four inserts `internal/history` produces
// rows for and nothing has ever performed.
//
// ── THIS IS CUTOVER CODE, AND IT IS CALLED BY NOBODY ON PURPOSE ────────────
//
// `internal/history` holds `Writer` (which buckets per-second traffic and ping
// samples into the minute rows these tables take) and `Connectivity`. Both are
// complete and pinned, and both are constructed by nobody — because two
// processes bucketing the same samples into one SQLite file would DOUBLE every
// row, and during coexistence Node is the one doing it.
//
// `PORT-QUEUE.md`'s cutover checklist lists starting them as step 0: code that
// must exist and be tested BEFORE the window rather than discovered inside it.
// This is that code's other half.
//
// Verified absent by MEASUREMENT on 2026-08-29 rather than by grep — with a
// session connected and its collectors running, all four tables gained zero rows
// in eighty seconds.
//
// ── ONE ROW TYPE, FOUR TABLES, AND THE COLUMNS ARE NOT INTERCHANGEABLE ─────
//
// `history.Row` carries `RxOrRTT`/`TxOrLoss` because a traffic row and a ping row
// have the same shape and different meanings. The mapping is taken from
// `src/db.js:670-674` verbatim:
//
//	traffic       (router_id, interface, rx_mbps, tx_mbps, ts)
//	bandwidth     (router_id, interface, rx_mb,   tx_mb,   ts)
//	ping          (router_id, target,    rtt_ms,  loss_pct, ts)
//	connectivity  (router_id, connected, ts)
//
// Mixing `rx_mbps` and `rx_mb` is the error this mapping exists to prevent: they
// are a RATE and a VOLUME, differing by a factor of the sampling interval, and a
// swap renders as a plausible-looking chart with wrong numbers.

// PersistHistory writes one batch of bucketed rows.
//
// ── ONE TRANSACTION FOR THE BATCH ─────────────────────────────────────────
//
// A minute rollover emits one row per interface plus one per ping target, and
// they all describe the same minute. Committing them separately lets a reader
// see half a minute's interfaces — and Reports aggregates BY minute, so a
// partial minute renders as a dip in a chart rather than as a visible error.
//
// RETURNS THE COUNT WRITTEN, so a caller can log honestly rather than assume.
func (d *DB) PersistHistory(rows []history.Row) (int, error) {
	if d == nil || d.sql == nil || len(rows) == 0 {
		return 0, nil
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	n := 0
	for _, r := range rows {
		if r.RouterID == "" {
			// A row with no router cannot be read back by any query in this
			// schema — every one filters on router_id. Dropped rather than
			// written, and not counted as written.
			continue
		}
		if err := insertHistoryRow(tx, r); err != nil {
			return n, fmt.Errorf("db: persist %s row for %s: %w", r.Table, r.RouterID, err)
		}
		n++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

func insertHistoryRow(tx *sql.Tx, r history.Row) error {
	switch r.Table {
	case "traffic":
		_, err := tx.Exec(
			`INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts)
			 VALUES (?, ?, ?, ?, ?)`,
			r.RouterID, r.Name, r.RxOrRTT, r.TxOrLoss, r.TS)
		return err

	case "bandwidth":
		_, err := tx.Exec(
			`INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts)
			 VALUES (?, ?, ?, ?, ?)`,
			r.RouterID, r.Name, r.RxOrRTT, r.TxOrLoss, r.TS)
		return err

	case "ping":
		// ── A MINUTE WITH NO ANSWER HAS NO RTT, AND THAT IS NOT ZERO ──────
		//
		// `HasRTT` is false when every ping in the minute was lost. NULL is the
		// only honest value: zero would render as a perfect 0 ms round trip on
		// the very chart an operator reads to see an outage, and it would drag
		// any average through it.
		var rtt any
		if r.HasRTT {
			rtt = r.RxOrRTT
		}
		_, err := tx.Exec(
			`INSERT INTO ping_samples (router_id, target, rtt_ms, loss_pct, ts)
			 VALUES (?, ?, ?, ?, ?)`,
			r.RouterID, r.Name, rtt, r.TxOrLoss, r.TS)
		return err

	case "connectivity":
		// `connected` is written as an explicit INTEGER, matching the live
		// insert's 1/0 and every row already in the table.
		//
		// NOT because a Go bool would break: passing `r.Connected` directly
		// survives the suite, because `modernc.org/sqlite` binds a bool as
		// INTEGER 0/1 and the rows still scan as ints. That was measured by
		// mutation, and it corrects an earlier version of this comment which
		// claimed a bool "risks 'true'/'false' text" — a hazard this driver does
		// not have.
		//
		// It stays explicit because the VALUE is what the schema documents, and
		// a reader comparing this insert against `src/db.js` should see the same
		// 1/0 on both sides rather than have to know a driver's binding rules.
		c := 0
		if r.Connected {
			c = 1
		}
		_, err := tx.Exec(
			`INSERT INTO connectivity_events (router_id, connected, ts) VALUES (?, ?, ?)`,
			r.RouterID, c, r.TS)
		return err
	}
	// AN UNKNOWN TABLE IS AN ERROR, not a silent skip. `history.Row.Table` is a
	// closed set of four strings; a fifth means the bucketer grew a row type this
	// writer does not know, and dropping it would lose data quietly for as long
	// as it took somebody to notice a missing chart.
	return fmt.Errorf("db: unknown history table %q", r.Table)
}

// PersistHistoryLogged is `PersistHistory` for a caller with nowhere to return
// an error — the drain loop at cutover.
//
// It logs and carries on: a failed batch must not stop the next one, because the
// alternative is a process that quietly stops recording history after its first
// transient database error.
func (d *DB) PersistHistoryLogged(rows []history.Row) int {
	n, err := d.PersistHistory(rows)
	if err != nil {
		log.Printf("[history] persist: %v (%d of %d rows written)", err, n, len(rows))
	}
	return n
}
