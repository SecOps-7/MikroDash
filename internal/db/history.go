package db

// The time-series half of the shared database: the samples the Reports page
// charts and exports.
//
// ── THE SQL IS COPIED, NOT REWRITTEN ────────────────────────────────────────
//
// Both sides run SQLite. The aggregates the reports need — AVG over a time
// bucket, MAX, SUM, a strftime month boundary — are therefore available here as
// the SAME implementation, not merely an equivalent one. Issuing the original's
// query text verbatim makes the numbers identical by construction; pulling the
// rows out and accumulating them in Go would introduce a second implementation
// of floating-point summation whose disagreements would be real, tiny, and
// impossible to attribute.
//
// So every query below is the string from src/db.js, reformatted only where Go
// requires it. Where a value is interpolated it is a LITERAL FROM THIS FILE and
// never from a caller — see aggBucket.
//
// ── READ-ONLY ───────────────────────────────────────────────────────────────
//
// Nothing here writes. The Node app owns the schema, the migrations and the
// sampling; this reads what it recorded. A port that started inserting samples
// would double every series on the page.

import (
	"database/sql"
	"math"
	"time"
)

// sampleLimit is the original's default `limit || 100000` for the un-aggregated
// queries, and aggLimit its fixed 10000 for the aggregated ones. Both are the
// original's, and both matter: a chart that silently drops rows and one that
// silently keeps them are different pages.
const (
	sampleLimit = 100000
	aggLimit    = 10000
	// eventLimit is the alert and connectivity default, `limit || 10000`, which
	// is a tenth of the sample limit and not the same number by accident.
	eventLimit = 10000
)

// bucket is the SELECT and GROUP BY pair for one aggregation interval.
//
// These strings are INTERPOLATED INTO SQL, which is safe here for a reason worth
// stating rather than assuming: they are chosen from this fixed set by exact
// match, so a caller's `aggregate` parameter can only select one of them or none
// at all. It never reaches the query text.
type bucket struct{ sel, group string }

func aggBucket(agg string) (bucket, bool) {
	switch agg {
	case "hour":
		return bucket{"(ts / 3600000) * 3600000", "(ts / 3600000)"}, true
	case "day":
		return bucket{"(ts / 86400000) * 86400000", "(ts / 86400000)"}, true
	case "week":
		// A week bucket is 604800000 ms since the EPOCH, which was a Thursday, so
		// these weeks run Thursday to Wednesday. That is the original's arithmetic
		// and every weekly chart has always been drawn on it; "fixing" it to
		// Monday would move every point. Note it does NOT agree with the Monday
		// start in internal/reports.PeriodFor — those are different questions,
		// one about a chart bucket and one about a billing period.
		return bucket{"(ts / 604800000) * 604800000", "(ts / 604800000)"}, true
	case "month":
		return bucket{
			"CAST(strftime('%s', strftime('%Y-%m-01', ts/1000, 'unixepoch')) AS INTEGER) * 1000",
			"strftime('%Y-%m', ts/1000, 'unixepoch')",
		}, true
	}
	return bucket{}, false
}

// defaultTo is the original's `toTs || Date.now()`. A zero `to` means "up to
// now", which is what the page sends when it has no end bound.
func defaultTo(to int64) int64 {
	if to == 0 {
		return time.Now().UnixMilli()
	}
	return to
}

// PingSample is one row of the ping series. RttMs is NULLABLE: a timed-out probe
// records its loss with no round-trip time, and the chart draws a gap rather
// than a zero.
type PingSample struct {
	TS     int64    `json:"ts"`
	RttMs  *float64 `json:"rtt_ms"`
	LossPc float64  `json:"loss_pct"`
	Target string   `json:"target"`
	// SampleCount is set only by the aggregated query, and is how the page knows
	// a point is a mean of many rather than one reading.
	SampleCount int `json:"sample_count,omitempty"`
}

// PingSamples is the raw series.
func (d *DB) PingSamples(routerID string, from, to int64) ([]PingSample, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(`
    SELECT ts, rtt_ms, loss_pct, target FROM ping_samples
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `, routerID, from, defaultTo(to), sampleLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PingSample{}
	for rows.Next() {
		var s PingSample
		if err := rows.Scan(&s.TS, &s.RttMs, &s.LossPc, &s.Target); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PingSamplesAgg is the bucketed series. An unknown interval yields no rows,
// exactly as the original's `if (!b) return []` does — the caller asked for
// something that does not exist, and inventing a default would silently answer a
// different question.
func (d *DB) PingSamplesAgg(routerID string, from, to int64, agg string) ([]PingSample, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	b, ok := aggBucket(agg)
	if !ok {
		return []PingSample{}, nil
	}
	rows, err := d.sql.Query(`
    SELECT `+b.sel+` AS ts,
           target,
           AVG(CASE WHEN rtt_ms IS NOT NULL THEN rtt_ms ELSE NULL END) AS rtt_ms,
           AVG(loss_pct) AS loss_pct,
           COUNT(*) AS sample_count
    FROM   ping_samples
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    GROUP  BY `+b.group+`, target
    ORDER  BY ts ASC LIMIT ?
  `, routerID, from, defaultTo(to), aggLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PingSample{}
	for rows.Next() {
		var s PingSample
		// AVG over a bucket whose rtt_ms are ALL null is itself null, which is why
		// this stays a pointer through the aggregate.
		if err := rows.Scan(&s.TS, &s.Target, &s.RttMs, &s.LossPc, &s.SampleCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TrafficSample is one row of the per-interface RATE series.
type TrafficSample struct {
	TS        int64   `json:"ts"`
	Interface string  `json:"interface"`
	RxMbps    float64 `json:"rx_mbps"`
	TxMbps    float64 `json:"tx_mbps"`
	// The Max fields and SampleCount are set only by the aggregated query. A mean
	// alone hides the burst that filled the link for ninety seconds.
	RxMaxMbps   *float64 `json:"rx_max_mbps,omitempty"`
	TxMaxMbps   *float64 `json:"tx_max_mbps,omitempty"`
	SampleCount int      `json:"sample_count,omitempty"`
}

// TrafficSamples is the raw rate series for one interface.
func (d *DB) TrafficSamples(routerID, iface string, from, to int64) ([]TrafficSample, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(`
    SELECT ts, interface, rx_mbps, tx_mbps FROM traffic_samples
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `, routerID, iface, from, defaultTo(to), sampleLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TrafficSample{}
	for rows.Next() {
		var s TrafficSample
		if err := rows.Scan(&s.TS, &s.Interface, &s.RxMbps, &s.TxMbps); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TrafficSamplesAgg is the bucketed rate series, with the peaks kept.
func (d *DB) TrafficSamplesAgg(routerID, iface string, from, to int64, agg string) ([]TrafficSample, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	b, ok := aggBucket(agg)
	if !ok {
		return []TrafficSample{}, nil
	}
	rows, err := d.sql.Query(`
    SELECT `+b.sel+` AS ts,
           interface,
           AVG(rx_mbps) AS rx_mbps,
           AVG(tx_mbps) AS tx_mbps,
           MAX(rx_mbps) AS rx_max_mbps,
           MAX(tx_mbps) AS tx_max_mbps,
           COUNT(*) AS sample_count
    FROM   traffic_samples
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    GROUP  BY `+b.group+`
    ORDER  BY ts ASC LIMIT ?
  `, routerID, iface, from, defaultTo(to), aggLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []TrafficSample{}
	for rows.Next() {
		var s TrafficSample
		if err := rows.Scan(&s.TS, &s.Interface, &s.RxMbps, &s.TxMbps,
			&s.RxMaxMbps, &s.TxMaxMbps, &s.SampleCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// BandwidthSample is one row of the per-interface VOLUME series — megabytes
// moved, not megabits per second. The two are charted on the same page and
// confusing them is the easiest mistake here to make.
type BandwidthSample struct {
	TS        int64   `json:"ts"`
	Interface string  `json:"interface"`
	RxMb      float64 `json:"rx_mb"`
	TxMb      float64 `json:"tx_mb"`
	// Set only by the aggregated query.
	RxMaxMb     *float64 `json:"rx_max_mb,omitempty"`
	TxMaxMb     *float64 `json:"tx_max_mb,omitempty"`
	SampleCount int      `json:"sample_count,omitempty"`
}

// BandwidthSamples is the raw volume series for one interface.
func (d *DB) BandwidthSamples(routerID, iface string, from, to int64) ([]BandwidthSample, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(`
    SELECT ts, interface, rx_mb, tx_mb FROM bandwidth_usage
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `, routerID, iface, from, defaultTo(to), sampleLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []BandwidthSample{}
	for rows.Next() {
		var s BandwidthSample
		if err := rows.Scan(&s.TS, &s.Interface, &s.RxMb, &s.TxMb); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// BandwidthSamplesAgg is the bucketed volume series.
//
// VOLUME IS SUMMED WHERE RATE IS AVERAGED, and that asymmetry with the traffic
// query is deliberate rather than an oversight to be tidied: averaging megabytes
// over a day answers "how much did a typical sample carry", which nobody asked.
// A mean rate is meaningful; a total volume is. Both keep their MAX.
func (d *DB) BandwidthSamplesAgg(routerID, iface string, from, to int64, agg string) ([]BandwidthSample, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	b, ok := aggBucket(agg)
	if !ok {
		return []BandwidthSample{}, nil
	}
	rows, err := d.sql.Query(`
    SELECT `+b.sel+` AS ts,
           interface,
           SUM(rx_mb) AS rx_mb,
           SUM(tx_mb) AS tx_mb,
           MAX(rx_mb) AS rx_max_mb,
           MAX(tx_mb) AS tx_max_mb,
           COUNT(*) AS sample_count
    FROM   bandwidth_usage
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    GROUP  BY `+b.group+`
    ORDER  BY ts ASC LIMIT ?
  `, routerID, iface, from, defaultTo(to), aggLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []BandwidthSample{}
	for rows.Next() {
		var s BandwidthSample
		if err := rows.Scan(&s.TS, &s.Interface, &s.RxMb, &s.TxMb,
			&s.RxMaxMb, &s.TxMaxMb, &s.SampleCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TrafficInterfaces and BandwidthInterfaces are what the interface picker offers.
//
// Distinct names from the SAMPLES rather than from the live router, so an
// interface that has since been renamed or removed still lists for the period it
// has history in — which is the period a report is about.
func (d *DB) TrafficInterfaces(routerID string) ([]string, error) {
	return d.distinctInterfaces(
		`SELECT DISTINCT interface FROM traffic_samples WHERE router_id = ? ORDER BY interface`, routerID)
}

func (d *DB) BandwidthInterfaces(routerID string) ([]string, error) {
	return d.distinctInterfaces(
		`SELECT DISTINCT interface FROM bandwidth_usage WHERE router_id = ? ORDER BY interface`, routerID)
}

func (d *DB) distinctInterfaces(query, routerID string) ([]string, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(query, routerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []string{}
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name.String)
	}
	return out, rows.Err()
}

// ── Alerts and connectivity ─────────────────────────────────────────────────

// AlertEvent is one alert row. Every column after the type is NULLABLE, and the
// page renders each absence differently: no subject, unresolved, unacknowledged.
// Collapsing them to empty strings would make "still firing" indistinguishable
// from "resolved at the epoch".
type AlertEvent struct {
	ID             int64   `json:"id"`
	AlertType      string  `json:"alert_type"`
	Subject        *string `json:"subject"`
	Detail         *string `json:"detail"`
	FiredAt        int64   `json:"fired_at"`
	ResolvedAt     *int64  `json:"resolved_at"`
	AcknowledgedAt *int64  `json:"acknowledged_at"`
	AcknowledgedBy *string `json:"acknowledged_by"`
}

// AlertEvents is the alert list for a range.
//
// FILTERED AND ORDERED ON fired_at, NOT ts — this table has no ts column, and it
// is the only one here that does not. Ordered DESCENDING, unlike every sample
// query: a chart reads forwards in time and an alert list reads newest first.
func (d *DB) AlertEvents(routerID string, from, to int64) ([]AlertEvent, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(`
    SELECT id, alert_type, subject, detail, fired_at, resolved_at,
           acknowledged_at, acknowledged_by
    FROM   alert_events
    WHERE  router_id = ? AND fired_at >= ? AND fired_at <= ?
    ORDER  BY fired_at DESC LIMIT ?
  `, routerID, from, defaultTo(to), eventLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AlertEvent{}
	for rows.Next() {
		var a AlertEvent
		if err := rows.Scan(&a.ID, &a.AlertType, &a.Subject, &a.Detail, &a.FiredAt,
			&a.ResolvedAt, &a.AcknowledgedAt, &a.AcknowledgedBy); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ConnectivityEvent is one reachability transition. Connected is an INTEGER on
// disk and stays one here: the JSON the page already consumes carries 0 and 1,
// and turning it into a bool would change the payload.
type ConnectivityEvent struct {
	TS        int64 `json:"ts"`
	Connected int   `json:"connected"`
}

func (d *DB) ConnectivityEvents(routerID string, from, to int64) ([]ConnectivityEvent, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(`
    SELECT ts, connected FROM connectivity_events
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    ORDER  BY ts ASC LIMIT ?
  `, routerID, from, defaultTo(to), eventLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ConnectivityEvent{}
	for rows.Next() {
		var c ConnectivityEvent
		if err := rows.Scan(&c.TS, &c.Connected); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConnectivityBucket is one bucket of the uptime series.
type ConnectivityBucket struct {
	TS        int64   `json:"ts"`
	Total     int     `json:"total"`
	Online    int     `json:"online"`
	Offline   int     `json:"offline"`
	UptimePct float64 `json:"uptime_pct"`
}

// ConnectivityEventsAgg is the bucketed uptime series.
//
// The percentage is ROUNDED IN SQL to one decimal, and computing it here instead
// would be a second rounding implementation for a number the page prints
// verbatim. The CAST to REAL is what stops SQLite doing integer division and
// reporting every bucket as 0% or 100%.
func (d *DB) ConnectivityEventsAgg(routerID string, from, to int64, agg string) ([]ConnectivityBucket, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	b, ok := aggBucket(agg)
	if !ok {
		return []ConnectivityBucket{}, nil
	}
	rows, err := d.sql.Query(`
    SELECT `+b.sel+` AS ts,
           COUNT(*) AS total,
           SUM(connected) AS online,
           COUNT(*) - SUM(connected) AS offline,
           ROUND(CAST(SUM(connected) AS REAL) / COUNT(*) * 100, 1) AS uptime_pct
    FROM   connectivity_events
    WHERE  router_id = ? AND ts >= ? AND ts <= ?
    GROUP  BY `+b.group+`
    ORDER  BY ts ASC LIMIT ?
  `, routerID, from, defaultTo(to), aggLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ConnectivityBucket{}
	for rows.Next() {
		var c ConnectivityBucket
		if err := rows.Scan(&c.TS, &c.Total, &c.Online, &c.Offline, &c.UptimePct); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ── Summaries ───────────────────────────────────────────────────────────────

// TrafficSummaryRow is the stat card above the rate chart.
//
// WHY IT IS COMPUTED IN SQL RATHER THAN FROM THE ROWS THE API SHIPPED. The cards
// used to be reduced in the browser from whatever the row query returned, which
// was wrong two ways at once: aggregated rows are averages, so the max across
// them is a peak of averages rather than a real peak, and the row queries are
// capped by LIMIT, so totals silently truncated on long ranges. Computing over
// the whole range is correct regardless of the aggregation setting and
// regardless of how many rows are shipped.
//
// Every rate is a POINTER because an empty range has no average, and a card
// reading 0 Mbps is a claim about a quiet link rather than about no data.
type TrafficSummaryRow struct {
	Samples   int      `json:"samples"`
	RxAvgMbps *float64 `json:"rxAvgMbps"`
	TxAvgMbps *float64 `json:"txAvgMbps"`
	RxMaxMbps *float64 `json:"rxMaxMbps"`
	TxMaxMbps *float64 `json:"txMaxMbps"`
	RxP95Mbps *float64 `json:"rxP95Mbps"`
	TxP95Mbps *float64 `json:"txP95Mbps"`
}

// TrafficSummary is the rate summary for one interface over a range.
func (d *DB) TrafficSummary(routerID, iface string, from, to int64, pct float64) (TrafficSummaryRow, error) {
	empty := TrafficSummaryRow{}
	if d == nil || d.sql == nil {
		return empty, nil
	}
	toTS := defaultTo(to)
	// `Math.min(99, Math.max(1, Number(pct) || 95))`. THE `|| 95` MATTERS: a
	// requested percentile of 0 — or anything that does not parse — becomes 95,
	// not 1. A clamp alone would turn 0 into the first sample.
	p := pct
	if p == 0 || p != p { // NaN is never equal to itself
		p = 95
	}
	if p < 1 {
		p = 1
	}
	if p > 99 {
		p = 99
	}

	var n int
	var rxAvg, txAvg, rxMax, txMax *float64
	err := d.sql.QueryRow(`
    SELECT COUNT(*)     AS n,
           AVG(rx_mbps) AS rx_avg, AVG(tx_mbps) AS tx_avg,
           MAX(rx_mbps) AS rx_max, MAX(tx_mbps) AS tx_max
    FROM   traffic_samples
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
  `, routerID, iface, from, toTS).Scan(&n, &rxAvg, &txAvg, &rxMax, &txMax)
	if err != nil {
		return empty, err
	}
	// `if (!r || !r.n) return empty` — an empty range reports nulls throughout,
	// including a sample count of zero.
	if n == 0 {
		return empty, nil
	}

	rxP95, err := d.percentileCol("traffic_samples", "rx_mbps", routerID, iface, from, toTS, n, p)
	if err != nil {
		return empty, err
	}
	txP95, err := d.percentileCol("traffic_samples", "tx_mbps", routerID, iface, from, toTS, n, p)
	if err != nil {
		return empty, err
	}
	return TrafficSummaryRow{
		Samples: n, RxAvgMbps: rxAvg, TxAvgMbps: txAvg,
		RxMaxMbps: rxMax, TxMaxMbps: txMax, RxP95Mbps: rxP95, TxP95Mbps: txP95,
	}, nil
}

// BandwidthSummaryRow is the stat card above the volume chart.
//
// NOTE THE ASYMMETRY WITH TrafficSummaryRow, which is the original's: the totals
// are plain numbers defaulting to ZERO, while the maxima are pointers. "No bytes
// moved" is a meaningful total and an honest zero; "no peak" is not.
type BandwidthSummaryRow struct {
	Samples   int      `json:"samples"`
	RxTotalMb float64  `json:"rxTotalMb"`
	TxTotalMb float64  `json:"txTotalMb"`
	RxMaxMb   *float64 `json:"rxMaxMb"`
	TxMaxMb   *float64 `json:"txMaxMb"`
}

// BandwidthSummary is the volume summary for one interface over a range.
//
// Kept on bandwidth_usage rather than derived from traffic_samples: the two are
// the same measurement at different scalings but are not reliably
// interconvertible, because a bandwidth bucket is only written when the minute
// actually moved bytes and a minute may carry fewer than sixty samples.
func (d *DB) BandwidthSummary(routerID, iface string, from, to int64) (BandwidthSummaryRow, error) {
	empty := BandwidthSummaryRow{}
	if d == nil || d.sql == nil {
		return empty, nil
	}
	var n int
	var rxSum, txSum, rxMax, txMax *float64
	err := d.sql.QueryRow(`
    SELECT COUNT(*)   AS n,
           SUM(rx_mb) AS rx_sum, SUM(tx_mb) AS tx_sum,
           MAX(rx_mb) AS rx_max, MAX(tx_mb) AS tx_max
    FROM   bandwidth_usage
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
  `, routerID, iface, from, defaultTo(to)).Scan(&n, &rxSum, &txSum, &rxMax, &txMax)
	if err != nil {
		return empty, err
	}
	if n == 0 {
		return empty, nil
	}
	// `r.rx_sum || 0` — a null total is reported as zero.
	out := BandwidthSummaryRow{Samples: n, RxMaxMb: rxMax, TxMaxMb: txMax}
	if rxSum != nil {
		out.RxTotalMb = *rxSum
	}
	if txSum != nil {
		out.TxTotalMb = *txSum
	}
	return out, nil
}

// percentileCol is a NEAREST-RANK percentile over one column.
//
// SQLite has no percentile function, and ORDER BY with an OFFSET is exact and
// needs no extra index — the existing (router_id, interface, ts) index narrows
// the range and the sort runs over that subset only. `table` and `col` are
// literals from this file, never from a caller, so they cannot carry injection.
//
// The offset is `ceil(n * pct / 100) - 1` clamped into the row range, which is
// the nearest-rank definition. Reproduced rather than reasoned about: an
// off-by-one here moves the 95th percentile by one sample, which nobody would
// notice and which would be wrong on every report.
func (d *DB) percentileCol(table, col, routerID, iface string, from, to int64, n int, pct float64) (*float64, error) {
	if n < 1 {
		return nil, nil
	}
	off := int(math.Ceil(float64(n)*pct/100)) - 1
	if off < 0 {
		off = 0
	}
	if off > n-1 {
		off = n - 1
	}
	var v *float64
	err := d.sql.QueryRow(`
    SELECT `+col+` AS v FROM `+table+`
    WHERE  router_id = ? AND interface = ? AND ts >= ? AND ts <= ?
    ORDER  BY `+col+` ASC LIMIT 1 OFFSET ?
  `, routerID, iface, from, to, off).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

// ── Report schedules ────────────────────────────────────────────────────────
//
// READ ONLY, like everything else in this file. Creating a schedule is a
// write-level grant on the Reports page — it mails router history to arbitrary
// third-party addresses indefinitely, without anyone signing in again — and that
// path is not ported here.

// ReportSchedule is one row of report_schedules.
//
// `sections` and `recipients` are JSON ARRAYS STORED AS TEXT, so they stay
// strings at this layer and are decoded where the shape is known. A column that
// holds JSON is still a column; parsing it in the query layer would make every
// caller inherit whatever the parse decided about a malformed value.
type ReportSchedule struct {
	ID             string  `json:"id"`
	RouterID       string  `json:"router_id"`
	Name           string  `json:"name"`
	Sections       string  `json:"sections"`
	Interface      *string `json:"interface"`
	Aggregate      string  `json:"aggregate"`
	Recipients     string  `json:"recipients"`
	Frequency      string  `json:"frequency"`
	SendHour       int     `json:"send_hour"`
	Enabled        int     `json:"enabled"`
	DisabledReason *string `json:"disabled_reason"`
	CreatedBy      *string `json:"created_by"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// ReportSchedulesFor lists a router's schedules, oldest first.
//
// ORDERED BY created_at, which is the original's — so the list does not reshuffle
// when somebody edits one. A name sort would have looked tidier and would move a
// row the moment it was renamed.
func (d *DB) ReportSchedulesFor(routerID string) ([]ReportSchedule, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	rows, err := d.sql.Query(`
    SELECT id, router_id, name, sections, interface, aggregate, recipients,
           frequency, send_hour, enabled, disabled_reason, created_by,
           created_at, updated_at
    FROM   report_schedules WHERE router_id = ? ORDER BY created_at
  `, routerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ReportSchedule{}
	for rows.Next() {
		var s ReportSchedule
		if err := rows.Scan(&s.ID, &s.RouterID, &s.Name, &s.Sections, &s.Interface,
			&s.Aggregate, &s.Recipients, &s.Frequency, &s.SendHour, &s.Enabled,
			&s.DisabledReason, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ReportRun is one attempt to send a scheduled report.
//
// `id` and `schedule_id` are carried even though the schedules endpoint reads
// neither: the live query is `SELECT *`, and a port that narrowed it would be
// answering a different question from the one the gate compares against. The
// cost of two extra columns is nothing; the cost of a query layer that quietly
// returns less than its reference is a difference nobody can see.
type ReportRun struct {
	ID         int64   `json:"id"`
	ScheduleID string  `json:"schedule_id"`
	RanAt      int64   `json:"ran_at"`
	PeriodFrom int64   `json:"period_from"`
	PeriodTo   int64   `json:"period_to"`
	Outcome    string  `json:"outcome"`
	Source     string  `json:"source"`
	Actor      *string `json:"actor"`
	Recipients int     `json:"recipients_n"`
	Bytes      int64   `json:"bytes"`
	Rows       int     `json:"rows_n"`
	Ms         int64   `json:"ms"`
	Error      *string `json:"error"`
}

// reportRunKeep is REPORT_RUN_KEEP: the retention cap, and therefore the most any
// query may ask for.
//
// IT IS 100, NOT 20. The original reads
// `Math.min(Number(limit) || 20, REPORT_RUN_KEEP)`, and the 20 there is the
// DEFAULT for a caller that names no limit — a different number doing a
// different job. Conflating them made this port clamp every request to 20 and
// silently return a fifth of a schedule's history to anyone who asked for more.
//
// Caught by seeding more runs than the cap: with three rows a request for 999
// and a wrongly-clamped one both returned three, and the mistake was invisible.
const reportRunKeep = 100

// reportRunDefault is the `|| 20` — what a caller gets when it names no limit.
const reportRunDefault = 20

// ReportRuns is a schedule's history, newest first.
func (d *DB) ReportRuns(scheduleID string, limit int) ([]ReportRun, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	// `Math.min(Number(limit) || 20, REPORT_RUN_KEEP)` — a zero or absent limit
	// becomes the DEFAULT, and nothing may exceed the retention cap.
	if limit <= 0 {
		limit = reportRunDefault
	}
	// NOT GATED, and said so rather than left looking covered. The case set seeds
	// 25 runs, so clamping to 100 and not clamping at all return the same rows —
	// a mutation removing this line passes. Gating it would mean 101 near-
	// identical rows in the case file to exercise one comparison.
	//
	// It is also unreachable from this port today: the only caller is the
	// schedules list, which asks for 1. Kept because it is the original's and
	// because the next caller may not be so modest.
	if limit > reportRunKeep {
		limit = reportRunKeep
	}
	rows, err := d.sql.Query(`
    SELECT id, schedule_id, ran_at, period_from, period_to, outcome, source, actor,
           recipients_n, bytes, rows_n, ms, error
    FROM   report_runs WHERE schedule_id = ?
    ORDER  BY ran_at DESC LIMIT ?
  `, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []ReportRun{}
	for rows.Next() {
		var r ReportRun
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.RanAt, &r.PeriodFrom, &r.PeriodTo, &r.Outcome, &r.Source,
			&r.Actor, &r.Recipients, &r.Bytes, &r.Rows, &r.Ms, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReportSchedule fetches one schedule by id, or nil.
//
// The CALLER checks the router. That is the live app's pattern — `_scheduleRow`
// reads the row and 404s when `row.router_id` does not match the query — and it
// is the pattern precisely because naming a router you may write must never
// reach a record belonging to another one. Returning the row here and deciding
// there keeps the ownership test in the place that knows who is asking.
func (d *DB) ReportSchedule(id string) (*ReportSchedule, error) {
	if d == nil || d.sql == nil {
		return nil, nil
	}
	var s ReportSchedule
	err := d.sql.QueryRow(`
    SELECT id, router_id, name, sections, interface, aggregate, recipients,
           frequency, send_hour, enabled, disabled_reason, created_by,
           created_at, updated_at
    FROM   report_schedules WHERE id = ?
  `, id).Scan(&s.ID, &s.RouterID, &s.Name, &s.Sections, &s.Interface,
		&s.Aggregate, &s.Recipients, &s.Frequency, &s.SendHour, &s.Enabled,
		&s.DisabledReason, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}
