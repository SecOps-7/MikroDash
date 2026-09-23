package db

import (
	"fmt"
	"log"
)

// HOURLY ROLLUPS — the long half of the traffic history (#59).
//
// ── WHY THEY EXIST, WITH THE NUMBER THAT FORCED THEM ───────────────────────
//
// A minute row per interface costs about 270 bytes once its index is counted,
// measured on a real install: ~11.6 MB per interface-month, ~142 MB per
// interface-year. One interface was affordable; letting an operator record
// eight was not, and the recording switch is the whole point of #59.
//
// An hour row is a sixtieth of that, and a chart covering a month cannot draw
// minutes anyway. So minute rows are kept for `RawMinuteDays` and the hours go
// on under the ordinary retention.
//
// ── WHAT AN HOUR ROW CARRIES, AND WHY IT IS NOT JUST AN AVERAGE ────────────
//
// The aggregated reads report the average, the PEAK and how many samples were
// behind them. An hour row that held only an average would silently change two
// of those: the peak of a month would become the largest hourly mean rather
// than the largest minute, and a coarser bucket built from unweighted averages
// would drift wherever an hour is short of samples.
//
// So each row keeps avg, max and the sample count, and the coarser buckets
// weight by that count (`SUM(avg*samples)/SUM(samples)`), which is exact rather
// than close. Volume is a SUM, so summing sums is exact by itself.
//
// ── IDEMPOTENT, BECAUSE THE JOB RUNS AGAIN ─────────────────────────────────
//
// The scheduler rolls the recent hours up every few minutes and the whole raw
// window once a day, so any hour is computed many times. Each write REPLACES
// its row on (router_id, interface, ts) rather than adding to it: a job that
// accumulated would double a month's total the first time it overlapped
// itself, and the number would look plausible.
const rollupTablesDDL = `
CREATE TABLE IF NOT EXISTS traffic_hourly (
          router_id TEXT    NOT NULL,
          interface TEXT    NOT NULL,
          ts        INTEGER NOT NULL,
          rx_mbps   REAL    NOT NULL,
          tx_mbps   REAL    NOT NULL,
          rx_max_mbps REAL  NOT NULL,
          tx_max_mbps REAL  NOT NULL,
          samples   INTEGER NOT NULL,
          PRIMARY KEY (router_id, interface, ts)
        );
CREATE TABLE IF NOT EXISTS bandwidth_hourly (
          router_id TEXT    NOT NULL,
          interface TEXT    NOT NULL,
          ts        INTEGER NOT NULL,
          rx_mb     REAL    NOT NULL,
          tx_mb     REAL    NOT NULL,
          samples   INTEGER NOT NULL,
          PRIMARY KEY (router_id, interface, ts)
        );`

const msPerHour = 3600000

// RollUp writes the hour rows for every completed hour between `from` and `to`,
// and reports how many it wrote.
//
// THE CURRENT HOUR IS LEFT ALONE. Rolling it up would write a row from the
// minutes so far and then replace it on the next pass, which is harmless, and
// it would also be READ as a full hour by anything asking for a range that ends
// now — a bar a third of its real height, arriving every few minutes. The hour
// is written once it is over.
func (d *DB) RollUp(from, to int64) (int, error) {
	if d == nil || d.sql == nil {
		return 0, nil
	}
	start := (from / msPerHour) * msPerHour
	end := (to / msPerHour) * msPerHour // exclusive: the hour containing `to` is still open
	if end <= start {
		return 0, nil
	}
	total := 0
	for _, q := range []struct{ what, sql string }{
		{"traffic", `
    INSERT OR REPLACE INTO traffic_hourly
                (router_id, interface, ts, rx_mbps, tx_mbps, rx_max_mbps, tx_max_mbps, samples)
    SELECT       router_id, interface, (ts / 3600000) * 3600000,
                 AVG(rx_mbps), AVG(tx_mbps), MAX(rx_mbps), MAX(tx_mbps), COUNT(*)
    FROM         traffic_samples
    WHERE        ts >= ? AND ts < ?
    GROUP BY     router_id, interface, ts / 3600000`},
		{"bandwidth", `
    INSERT OR REPLACE INTO bandwidth_hourly
                (router_id, interface, ts, rx_mb, tx_mb, samples)
    SELECT       router_id, interface, (ts / 3600000) * 3600000,
                 SUM(rx_mb), SUM(tx_mb), COUNT(*)
    FROM         bandwidth_usage
    WHERE        ts >= ? AND ts < ?
    GROUP BY     router_id, interface, ts / 3600000`},
	} {
		res, err := d.sql.Exec(q.sql, start, end)
		if err != nil {
			// The rows already written are reported with the error, as Prune
			// does: they really are there, and a caller logging zero would be
			// wrong about the database it just changed.
			return total, fmt.Errorf("db: roll up %s: %w", q.what, err)
		}
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// compactWindow is how far back a routine pass rolls up. Wider than the gap
// between passes on purpose: a process that was stopped for an hour, or a
// machine that slept, comes back and still writes the hours it missed.
const compactWindow = 6 * msPerHour

// Compact is the whole maintenance pass: it rolls completed hours up and then
// removes the minute rows that have been summarised and are past the raw
// window. It reports how many rows it wrote and how many it deleted.
//
// ── THE MINUTES ARE NOT DELETED BY AGE, AND THAT IS THE POINT ──────────────
//
// An age rule in `pruneRules` would have been simpler and is wrong in a way
// that only shows up after an outage: a process that was down while a window
// aged past the cutoff would delete those minutes on its next sweep WITHOUT
// ever having rolled them up, losing the window instead of compacting it.
//
// So a minute row is deleted only where the hour that contains it is present in
// the rollup. If the rollup failed, the minutes stay and the next pass tries
// again; the cost of being wrong is disk, not history.
func (d *DB) Compact(now int64) (rolled, deleted int, err error) {
	if d == nil || d.sql == nil {
		return 0, 0, nil
	}
	// The recent hours first, so a chart covering today is current whether or
	// not anything ages out in this pass.
	rolled += d.RollUpRecent(now)

	cutoff := ((now - int64(RawMinuteDays)*msPerDay) / msPerHour) * msPerHour
	// EVERYTHING STILL SITTING BEYOND THE CUTOFF, bounded by what is actually
	// there rather than by a fixed window: normally nothing, and after an outage
	// exactly the missed span.
	var oldest *int64
	if err := d.sql.QueryRow(
		`SELECT MIN(ts) FROM (SELECT MIN(ts) AS ts FROM traffic_samples
		  UNION ALL SELECT MIN(ts) FROM bandwidth_usage)`).Scan(&oldest); err != nil {
		return rolled, 0, fmt.Errorf("db: compact, oldest sample: %w", err)
	}
	if oldest == nil || *oldest >= cutoff {
		return rolled, 0, nil
	}
	n, err := d.RollUp(*oldest, cutoff)
	rolled += n
	if err != nil {
		return rolled, 0, err
	}

	deleted, err = d.foldSummarisedMinutes(cutoff)
	if err != nil {
		return rolled, deleted, err
	}
	return rolled, deleted, nil
}

// foldSummarisedMinutes removes the minute rows older than `cutoff` WHOSE HOUR
// IS ALREADY IN THE ROLLUP, and reports how many it removed.
//
// The EXISTS clause is the whole safety property, which is why this is a step
// with its own name and its own test rather than four lines inside Compact:
// without it the pass is an age-based delete, and an age-based delete throws
// away exactly the window a failed or missed rollup did not summarise.
func (d *DB) foldSummarisedMinutes(cutoff int64) (int, error) {
	deleted := 0
	for _, t := range []struct{ minutes, hours string }{
		{"traffic_samples", "traffic_hourly"},
		{"bandwidth_usage", "bandwidth_hourly"},
	} {
		res, err := d.sql.Exec(`
    DELETE FROM `+t.minutes+`
    WHERE  ts < ?
      AND  EXISTS (SELECT 1 FROM `+t.hours+` h
                   WHERE  h.router_id = `+t.minutes+`.router_id
                     AND  h.interface = `+t.minutes+`.interface
                     AND  h.ts        = (`+t.minutes+`.ts / 3600000) * 3600000)`, cutoff)
		if err != nil {
			return deleted, fmt.Errorf("db: compact %s: %w", t.minutes, err)
		}
		k, _ := res.RowsAffected()
		deleted += int(k)
	}
	return deleted, nil
}

// CompactLogged is Compact for the timers, which have nowhere to return an
// error. It logs only when it did something, so a quiet install does not write
// a line every few minutes for ever.
func (d *DB) CompactLogged(now int64) (int, int) {
	rolled, deleted, err := d.Compact(now)
	if err != nil {
		log.Printf("[db] compact: %v (%d rolled, %d deleted before it stopped)", err, rolled, deleted)
	}
	if deleted > 0 {
		log.Printf("[db] compact: %d hour row(s) written, %d minute row(s) folded into them",
			rolled, deleted)
	}
	return rolled, deleted
}

// RollUpRecent brings the hour rows up to date for the window a routine pass
// covers, and is the half of compaction that DELETES NOTHING.
//
// It is therefore the half that runs on an install with no retention policy:
// without it no hour row is ever written, and every range longer than the raw
// window reads a table that is empty rather than a chart. Compact calls it too,
// so "bring the recent hours up to date" has one implementation.
func (d *DB) RollUpRecent(now int64) int {
	n, err := d.RollUp(now-compactWindow, now)
	if err != nil {
		log.Printf("[db] roll up: %v (%d row(s) written before it stopped)", err, n)
	}
	return n
}
