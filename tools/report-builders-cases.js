'use strict';
/**
 * The five report BUILDERS, run against synthetic rows.
 *
 * `src/reports/build.js` turns database rows into the three things the PDF
 * renderer takes -- `columns`, `rows` and a `meta` carrying the stat boxes and
 * the chart series. `internal/reportpdf` can already draw all of that exactly;
 * this is the half that decides WHAT to draw, and it is the last thing between
 * the port and `format=pdf` answering with a document.
 *
 * ---- HOW THE LIVE CODE IS REACHED -----------------------------------------
 *
 * `build` is exported but its builders call `db` and `Routers` themselves, so
 * there is no seam to pass rows through. Rather than lift five functions and
 * their six helpers, this loads the real modules and REPLACES the query
 * functions on them: `build.js` captured the module objects at require time, so
 * patching their properties is seen by the code under test.
 *
 * That keeps the thing being measured the real thing. A lifted copy of five
 * builders would be the largest extraction in this repo and the easiest to let
 * drift.
 *
 * ---- WHAT THE CASES ARE FOR -----------------------------------------------
 *
 * Each builder has a different way of saying "no data", and they are not
 * consistent -- which is the point of covering all five rather than one:
 *
 *   ping          every stat is `arr.length ? … : '—'`, and `rtt_ms` is filtered
 *                 for null SEPARATELY from `loss_pct`, so a run with RTTs but no
 *                 losses is a real state.
 *   traffic       stats come from a SQL summary, not the rows, so empty rows and
 *                 a null summary are independent failures.
 *   bandwidth     `s.bandwidthSamples` is a COUNT that survives when every
 *                 maximum is null, and the box label changes with `aggregate`.
 *   alerts        `topEntry` is `Object.entries(counts).sort(...)[0]`, which is
 *                 undefined for no rows -- and a TIE resolves by whatever order
 *                 the keys were inserted, because the sort is not stable on ties
 *                 of one key.
 *   connectivity  an unresolved outage has `downtime_ms == null` and reads
 *                 "Ongoing", which is neither a duration nor a blank.
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash \
 *     node /work/tools/report-builders-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));

const db = require(path.join(SRC, 'src', 'db'));
const Routers = require(path.join(SRC, 'src', 'routers'));
const Settings = require(path.join(SRC, 'src', 'settings'));

// A fixed timezone, so the corpus does not record the container's clock. tsFmt
// reads this on every call.
Settings.load = () => ({ displayTimezone: '' });

const Build = require(path.join(SRC, 'src', 'reports', 'build'));

const T0 = Date.UTC(2026, 7, 25, 6, 0, 0);
const MIN = 60000;

/** Install one case's data, then build. */
function run(section, opts, data) {
  db.queryPingSamples = () => data.ping || [];
  db.queryPingSamplesAgg = () => data.ping || [];
  db.queryTrafficSamples = () => data.traffic || [];
  db.queryTrafficSamplesAgg = () => data.traffic || [];
  db.queryBandwidthSamples = () => data.bandwidth || [];
  db.queryBandwidthSamplesAgg = () => data.bandwidth || [];
  db.queryAlertEvents = () => data.alerts || [];
  db.queryConnectivityEvents = () => data.conn || [];
  db.queryTrafficSummary = () => data.trafficSummary || {};
  db.queryBandwidthSummary = () => data.bandwidthSummary || {};
  // RESOLVED ONCE and recorded, rather than defaulted here and left implicit.
  // `_routerLabel` and `ifaceSummary`'s capacities both read this, so a corpus
  // that omitted it would leave the Go side guessing at both the label and the
  // line rate -- and a wrong capacity is invisible in every stat except
  // "Peak Util".
  const router = data.router === null
    ? null
    : (data.router || { label: 'Test Router', bwDownMbps: 100, bwUpMbps: 20 });
  data.router = router;
  Routers.getById = () => router;

  const out = Build.build(section, opts);
  // Only the PDF half is on trial here -- the CSV half is already ported and
  // pinned by internal/reports/export_test.go.
  return {
    title: out.title,
    rowCount: out.rowCount,
    truncated: out.truncated,
    columns: out.pdf.columns,
    // The first and last rows only: a capped case carries 5001 of them and the
    // interesting ones are the ends.
    firstRow: out.pdf.rows[0] || null,
    lastRow: out.pdf.rows.length ? out.pdf.rows[out.pdf.rows.length - 1] : null,
    rowsLength: out.pdf.rows.length,
    meta: out.pdf.meta,
  };
}

const pingRows = (n, f) => Array.from({ length: n }, (_, i) => ({
  ts: T0 + i * MIN, target: '198.51.100.1', ...f(i),
}));

const CASES = {
  // ---- ping ----
  'ping normal': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    ping: pingRows(30, (i) => ({ rtt_ms: 10 + (i % 7) * 1.5, loss_pct: i % 10 === 0 ? 2.5 : 0 })) }],
  'ping empty': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, { ping: [] }],
  // RTTs present, but every one of them null: `rtts` is empty while `losses` is
  // not, so three stats say '—' and two do not.
  'ping all rtt null': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    ping: pingRows(8, () => ({ rtt_ms: null, loss_pct: 100 })) }],
  // Every sample lossy, so uptime is 0.0%.
  'ping total loss': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    ping: pingRows(5, () => ({ rtt_ms: null, loss_pct: 100 })) }],
  // A NULL loss_pct, which JS coerces twice and differently. `losses` is
  // `rows.map(r => r.loss_pct)` -- unfiltered -- so a null is still counted in
  // `losses.length`, sums as 0 (`0 + null === 0`), and passes `l < 1` because
  // `null < 1` is TRUE. So a null loss silently reads as a perfectly good
  // sample in all three of Uptime, Avg Loss and the denominator. A Go port
  // treating it as missing would disagree on every one.
  'ping null loss': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    ping: [
      { ts: T0, target: 'x', rtt_ms: 10, loss_pct: null },
      { ts: T0 + MIN, target: 'x', rtt_ms: 12, loss_pct: 50 },
      { ts: T0 + 2 * MIN, target: 'x', rtt_ms: 14, loss_pct: 0 },
    ] }],
  // A ZERO rtt_ms, which a sub-millisecond reply on a LAN really does produce.
  // The cell is `r.rtt_ms ?? ''` -- NULLISH, not `||` -- so a 0 renders as "0"
  // and only a null blanks. The two are indistinguishable without this case, and
  // a mutation swapping them survived until it existed.
  'ping zero rtt': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    ping: [
      { ts: T0, target: 'x', rtt_ms: 0, loss_pct: 0 },
      { ts: T0 + MIN, target: 'x', rtt_ms: null, loss_pct: 0 },
    ] }],
  'ping aggregated': ['ping', { routerId: 'r1', from: T0, to: T0 + 60 * MIN, aggregate: 'hour' }, {
    ping: pingRows(12, (i) => ({ rtt_ms: 5 + i, loss_pct: 0 })) }],

  // ---- traffic ----
  'traffic normal': ['traffic', { routerId: 'r1', iface: 'ether1', from: T0, to: T0 + 60 * MIN }, {
    traffic: Array.from({ length: 20 }, (_, i) => ({
      ts: T0 + i * MIN, interface: 'ether1', rx_mbps: 10.25 + i, tx_mbps: 1.05 + i / 2 })),
    trafficSummary: { rxAvgMbps: 20.05, txAvgMbps: 5.25, rxMaxMbps: 95.55, txMaxMbps: 18.15,
      rxP95Mbps: 88.25, txP95Mbps: 15.05, samples: 20 },
    bandwidthSummary: { rxTotalMb: 100, txTotalMb: 50, samples: 20 } }],
  // A summary of nulls: every stat becomes '—', including the composed
  // "Peak Util" box which tests rxPeakPct rather than its own value.
  'traffic null summary': ['traffic', { routerId: 'r1', iface: 'ether1', from: T0, to: T0 + 60 * MIN }, {
    traffic: [{ ts: T0, interface: 'ether1', rx_mbps: 0, tx_mbps: 0 }],
    trafficSummary: { rxAvgMbps: null, txAvgMbps: null, rxMaxMbps: null, txMaxMbps: null,
      rxP95Mbps: null, txP95Mbps: null, samples: 0 },
    bandwidthSummary: { rxTotalMb: null, txTotalMb: null, samples: 0 } }],
  // Over the configured line capacity: the live comment says utilisation is
  // deliberately NOT clamped at 100.
  'traffic over capacity': ['traffic', { routerId: 'r1', iface: 'ether1', from: T0, to: T0 + 60 * MIN }, {
    traffic: [{ ts: T0, interface: 'ether1', rx_mbps: 150, tx_mbps: 40 }],
    trafficSummary: { rxAvgMbps: 120, txAvgMbps: 30, rxMaxMbps: 150.5, txMaxMbps: 40.5,
      rxP95Mbps: 140, txP95Mbps: 35, samples: 1 },
    bandwidthSummary: { rxTotalMb: 1, txTotalMb: 1, samples: 1 },
    router: { label: 'Small Pipe', bwDownMbps: 100, bwUpMbps: 20 } }],

  // ---- bandwidth ----
  'bandwidth normal': ['bandwidth', { routerId: 'r1', iface: 'ether1', from: T0, to: T0 + 60 * MIN }, {
    bandwidth: Array.from({ length: 15 }, (_, i) => ({
      ts: T0 + i * MIN, interface: 'ether1', rx_mb: 12.35 + i, tx_mb: 2.05 + i / 4 })),
    trafficSummary: { samples: 15 },
    bandwidthSummary: { rxTotalMb: 1234.5, txTotalMb: 456.75, rxMaxMb: 95.5, txMaxMb: 12.25,
      samples: 1500 } }],
  // Aggregated: the two "Busiest …" labels and the last box's label both change.
  'bandwidth aggregated': ['bandwidth', { routerId: 'r1', iface: 'ether1', from: T0, to: T0 + 60 * MIN, aggregate: 'day' }, {
    bandwidth: [{ ts: T0, interface: 'ether1', rx_mb: 1, tx_mb: 1 }],
    trafficSummary: { samples: 1 },
    bandwidthSummary: { rxTotalMb: 2e6, txTotalMb: 3e6, rxMaxMb: 1e6, txMaxMb: 5, samples: 7 } }],
  // Null maxima with a live sample COUNT -- the count survives where the
  // maxima do not.
  'bandwidth null maxima': ['bandwidth', { routerId: 'r1', iface: 'ether1', from: T0, to: T0 + 60 * MIN }, {
    bandwidth: [{ ts: T0, interface: 'ether1', rx_mb: 0, tx_mb: 0 }],
    trafficSummary: { samples: 0 },
    bandwidthSummary: { rxTotalMb: null, txTotalMb: null, rxMaxMb: null, txMaxMb: null, samples: 42 } }],

  // ---- alerts ----
  'alerts mixed': ['alerts', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    alerts: [
      { fired_at: T0, resolved_at: T0 + 5 * MIN, alert_type: 'cpu', subject: 'CPU', detail: 'high' },
      { fired_at: T0 + MIN, resolved_at: null, alert_type: 'link', subject: 'ether1', detail: '' },
      { fired_at: T0 + 2 * MIN, resolved_at: T0 + 3 * MIN, alert_type: 'cpu', subject: null, detail: null },
    ] }],
  'alerts empty': ['alerts', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, { alerts: [] }],
  // A TIE on the count. `sort` is stable in V8, so the FIRST key inserted wins,
  // and key order is insertion order -- which is the order the rows arrive in.
  'alerts tied top type': ['alerts', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    alerts: [
      { fired_at: T0, resolved_at: null, alert_type: 'zebra', subject: 'a', detail: '' },
      { fired_at: T0 + MIN, resolved_at: null, alert_type: 'alpha', subject: 'b', detail: '' },
    ] }],
  'alerts all open': ['alerts', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    alerts: [{ fired_at: T0, resolved_at: null, alert_type: 'link', subject: 's', detail: 'd' }] }],

  // ---- connectivity ----
  'connectivity mixed': ['connectivity', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    conn: [
      { ts: T0, connected: 0 },
      { ts: T0 + 5 * MIN, connected: 1 },
      { ts: T0 + 20 * MIN, connected: 0 },
      { ts: T0 + 21 * MIN, connected: 1 },
    ] }],
  // Ends offline: the last outage has no resolution, so `downtime_ms` is null
  // and the cell reads "Ongoing" rather than a duration or a blank.
  'connectivity ongoing': ['connectivity', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    conn: [{ ts: T0, connected: 1 }, { ts: T0 + 10 * MIN, connected: 0 }] }],
  'connectivity empty': ['connectivity', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, { conn: [] }],
  'connectivity all online': ['connectivity', { routerId: 'r1', from: T0, to: T0 + 60 * MIN }, {
    conn: [{ ts: T0, connected: 1 }, { ts: T0 + MIN, connected: 1 }] }],

  // ---- the router label, which every section puts in meta.router ----
  'router with no label falls back to host': ['ping', { routerId: 'r1', from: T0, to: T0 + MIN }, {
    ping: pingRows(2, () => ({ rtt_ms: 1, loss_pct: 0 })),
    router: { label: '', host: '198.51.100.7' } }],
  'router missing entirely is the id': ['ping', { routerId: 'r1', from: T0, to: T0 + MIN }, {
    ping: pingRows(2, () => ({ rtt_ms: 1, loss_pct: 0 })), router: null }],
};

const cases = [];
for (const [name, [section, opts, data]] of Object.entries(CASES)) {
  cases.push({ name, section, opts, data, out: run(section, opts, data) });
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  const by = Object.fromEntries(cases.map((c) => [c.name, c.out]));
  const stat = (n, label) => (by[n].meta.stats.find((s) => s.label === label) || {}).value;

  assert.equal(by['ping empty'].rowCount, 0, 'the empty ping case built rows from nowhere');
  assert.equal(stat('ping empty', 'Avg RTT'), '—', 'an empty ping report has a numeric Avg RTT');
  assert.notEqual(stat('ping normal', 'Avg RTT'), '—', 'the normal ping report produced no Avg RTT');
  assert.equal(stat('ping all rtt null', 'Avg RTT'), '—', 'all-null RTTs still produced an average');
  assert.notEqual(stat('ping all rtt null', 'Avg Loss'), '—',
    'losses were discarded along with the RTTs -- the two filters are not independent');
  assert.equal(stat('ping total loss', 'Uptime'), '0.0%', 'total loss did not read as 0.0% uptime');
  // Two of three samples are "good": the 0 and the NULL. If null ever stops
  // counting as good, or stops counting at all, this moves.
  assert.equal(stat('ping null loss', 'Uptime'), '66.7%',
    'a null loss_pct no longer counts as a good sample -- `null < 1` and `0 + null` are the reason');
  // The 0 must survive into the cell and the null must not.
  assert.equal(by['ping zero rtt'].firstRow['RTT (ms)'], 0,
    'a zero RTT was blanked -- the cell is using || where the live code uses ??');
  assert.equal(by['ping zero rtt'].lastRow['RTT (ms)'], '',
    'a null RTT did not blank');
  assert.equal(stat('ping null loss', 'Avg Loss'), '16.7%',
    'a null loss_pct is no longer summing as zero over the full row count');

  assert.equal(stat('traffic null summary', 'Peak Util'), '—',
    'a null summary produced a utilisation figure');
  assert.ok(/1[0-9][0-9]%/.test(stat('traffic over capacity', 'Peak Util')),
    'utilisation was clamped at 100% -- the live code deliberately does not clamp: '
    + stat('traffic over capacity', 'Peak Util'));

  assert.ok(stat('bandwidth aggregated', 'Buckets'), 'the aggregated bandwidth box is not "Buckets"');
  assert.ok(stat('bandwidth normal', 'Samples'), 'the unaggregated bandwidth box is not "Samples"');
  assert.ok(by['bandwidth aggregated'].meta.stats.some((s) => s.label.includes('Day')),
    'the aggregated "Busiest" labels do not name the bucket');
  assert.ok(by['bandwidth normal'].meta.stats.some((s) => s.label.includes('Minute')),
    'the unaggregated "Busiest" labels do not say Minute');
  assert.equal(stat('bandwidth null maxima', 'Total Download'), '0 KB',
    'a null total did not coerce to zero');
  assert.notEqual(stat('bandwidth null maxima', 'Samples'), '0',
    'the sample COUNT was lost along with the maxima');

  assert.equal(stat('alerts empty', 'Top Type'), '—', 'an empty alert report named a top type');
  assert.equal(stat('alerts mixed', 'Top Type'), 'cpu', 'the most frequent type is not winning');
  assert.equal(stat('alerts tied top type', 'Top Type'), 'zebra',
    'a tie no longer resolves to the first type seen -- the sort is not stable');
  assert.equal(stat('alerts all open', 'Resolved'), '0', 'an all-open report counted resolutions');

  assert.equal(by['alerts mixed'].meta.chartData, undefined,
    'the alerts report grew a chart -- its events are discrete');
  assert.ok(by['ping normal'].meta.chartData, 'the ping report has no chart');

  assert.equal(stat('connectivity empty', 'Total Downtime'), '—', 'an empty report had downtime');
  assert.equal(stat('connectivity all online', 'Offline Events'), '0',
    'an all-online report counted outages');
  assert.equal(by['connectivity ongoing'].lastRow['Down Duration'], 'Ongoing',
    'an unresolved outage did not read "Ongoing"');
  assert.equal(stat('connectivity ongoing', 'Longest Outage'), '—',
    'an unresolved outage was counted as the longest');

  assert.equal(by['router with no label falls back to host'].meta.router, '198.51.100.7',
    'a blank label did not fall back to the host');
  assert.equal(by['router missing entirely is the id'].meta.router, 'r1',
    'a missing router did not fall back to the id');
}

const OUT = path.join(ROOT, 'testdata', 'report-builders-cases.json');
const payload = JSON.stringify({
  note: 'GENERATED by tools/report-builders-cases.js from the live src/reports/build.js. Do not edit.',
  cases,
}, null, 1) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== payload) {
    console.error('report-builders-cases: testdata/report-builders-cases.json is stale — re-run without --check');
    process.exit(1);
  }
  console.log('report-builders-cases: up to date (' + cases.length + ' cases)');
} else {
  fs.writeFileSync(OUT, payload);
  console.log('report-builders-cases: wrote ' + cases.length + ' cases across '
    + new Set(cases.map((c) => c.section)).size + ' sections');
}
