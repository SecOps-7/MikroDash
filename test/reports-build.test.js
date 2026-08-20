'use strict';
/**
 * The shared report builder (#60).
 *
 * The five `/api/reports/*​/export` routes used to assemble their own columns,
 * rows and stat boxes inline. Scheduled reports need the same thing, so that
 * work moved into src/reports/build.js and the routes now call it.
 *
 * The column assertions below are the point of this file. They are written as
 * literals, deliberately duplicating what build.js produces, because that is
 * what proves the extraction was faithful rather than merely plausible — if
 * either side is edited, these fail. A test that derived the expected columns
 * from the code under test would agree with any mistake.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs   = require('fs');
const os   = require('os');
const path = require('path');

// db.js resolves DATA_DIR at require time, so point it at a temp dir first.
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-build-'));
process.env.DATA_DIR = TMP;
const db = require('../src/db');
const Reports = require('../src/reports/build');
const Pdf = require('../src/reports/pdf');
const F = require('../src/reports/format');

const MIN  = 60000;
const BASE = 1785312000000;   // fixed epoch ms, so bucket maths is deterministic
const RID  = 'router-a';
const IF1  = 'ether1';
const FROM = BASE - MIN;
const TO   = BASE + 500 * MIN;

// insertAlertEvent stamps fired_at with Date.now() and takes no timestamp, so
// the alerts section has to be queried over a window that reaches the present
// rather than the fixed BASE window the samples use.
const WINDOW = (section) => (section === 'alerts'
  ? { from: 0, to: Date.now() + MIN }
  : { from: FROM, to: TO });

test('setup', () => {
  db.open();
  db.purge({});
  for (let i = 0; i < 40; i++) {
    db.insertPingSample(RID, '9.9.9.9', 10 + i, i % 5, BASE + i * MIN);
    db.insertTrafficSample(RID, IF1, i + 1, (i + 1) / 10, BASE + i * MIN);
    db.insertBandwidthSample(RID, IF1, (i + 1) / 8, (i + 1) / 80, BASE + i * MIN);
    db.insertConnectivityEvent(RID, i % 7 !== 0, BASE + i * MIN);
  }
  db.insertAlertEvent(RID, 'cpu', 'cpu-load', 'CPU at 95%');
});

// ── The columns each section must produce ──────────────────────────────────

const EXPECTED = {
  ping: {
    title: 'Ping Stability Report',
    csv: ['ts', 'target', 'rtt_ms', 'loss_pct'],
    pdf: ['Timestamp', 'Target', 'RTT (ms)', 'Loss (%)'],
    chart: true,
  },
  traffic: {
    title: 'Traffic History Report',
    csv: ['ts', 'interface', 'rx_mbps', 'tx_mbps'],
    pdf: ['Timestamp', 'Interface', 'RX (Mbps)', 'TX (Mbps)'],
    chart: true,
  },
  bandwidth: {
    title: 'Bandwidth Usage Report',
    csv: ['ts', 'interface', 'rx_mb', 'tx_mb'],
    pdf: ['Timestamp', 'Interface', 'Download (MB)', 'Upload (MB)'],
    chart: true,
  },
  alerts: {
    title: 'Alert Events Report',
    csv: ['fired_at', 'alert_type', 'subject', 'detail', 'resolved_at', 'down_time'],
    pdf: ['Fired At', 'Type', 'Subject', 'Detail', 'Resolved At', 'Down Time'],
    chart: false,
  },
  connectivity: {
    title: 'Connectivity Report',
    csv: ['ts', 'status', 'down_duration'],
    pdf: ['Timestamp', 'Status', 'Down Duration'],
    chart: false,
  },
};

for (const [section, want] of Object.entries(EXPECTED)) {
  test('the ' + section + ' section keeps the columns the export route had', () => {
    const b = Reports.build(section, { routerId: RID, iface: IF1, ...WINDOW(section), aggregate: '' });
    assert.equal(b.title, want.title);
    assert.deepEqual(b.csv.columns, want.csv);
    assert.deepEqual(b.pdf.columns, want.pdf);
    assert.ok(b.rowCount > 0, 'expected seeded rows');
  });

  test('the ' + section + ' section ' + (want.chart ? 'draws' : 'draws no') + ' chart', () => {
    // Alert events and connectivity edges are discrete, not a series, so they
    // pass stat boxes and no chartData. The renderer has to cope with both.
    const b = Reports.build(section, { routerId: RID, iface: IF1, ...WINDOW(section), aggregate: '' });
    assert.equal(!!b.pdf.meta.chartData, want.chart);
    assert.ok(Array.isArray(b.pdf.meta.stats) && b.pdf.meta.stats.length > 0);
    assert.ok(b.pdf.meta.stats.length <= 6,
      'the renderer draws stat boxes with lineBreak:false, so a seventh truncates');
  });
}

// ── Guards ─────────────────────────────────────────────────────────────────

test('an unknown section is refused, not silently empty', () => {
  assert.throws(() => Reports.build('nope', { routerId: RID, from: FROM, to: TO }),
    /unknown report section/);
});

test('traffic and bandwidth refuse to build without an interface', () => {
  // The export routes 400 on this; the scheduler needs the same answer, since a
  // blank PDF that looks like "no traffic" is worse than an error.
  for (const section of Reports.NEEDS_INTERFACE) {
    assert.throws(() => Reports.build(section, { routerId: RID, from: FROM, to: TO }),
      /need an interface/, section);
  }
});

// ── The row cap ────────────────────────────────────────────────────────────

test('the PDF table is capped, and says so in the table', () => {
  // Samples are 1-minute bucketed, so an unaggregated month is ~43,200 rows per
  // series: roughly a thousand pages, rendered synchronously on the event loop
  // that serves live dashboards.
  const many = Reports.MAX_PDF_ROWS + 250;
  const rid2 = 'router-cap';
  for (let i = 0; i < many; i++) db.insertPingSample(rid2, '1.1.1.1', 5, 0, BASE + i * MIN);

  const b = Reports.build('ping', { routerId: rid2, from: BASE - MIN, to: BASE + (many + 1) * MIN, aggregate: '' });
  assert.equal(b.rowCount, many);
  assert.equal(b.truncated, true);
  // Capped rows plus one note row explaining the cap.
  assert.equal(b.pdf.rows.length, Reports.MAX_PDF_ROWS + 1);
  assert.match(b.pdf.rows[b.pdf.rows.length - 1].Timestamp, /showing the first/);
  // The CSV is deliberately uncapped: a spreadsheet user asking for a month
  // wants the month, and it is plain text.
  assert.equal(b.csv.rows.length, many);
});

test('a report inside the cap is not marked truncated', () => {
  const b = Reports.build('ping', { routerId: RID, iface: IF1, from: FROM, to: TO, aggregate: '' });
  assert.equal(b.truncated, false);
  assert.equal(b.pdf.rows.length, b.rowCount);
});

// ── The renderer, through the buffered sink ────────────────────────────────

test('every section renders to a real PDF', async () => {
  for (const section of Reports.SECTIONS) {
    const b = Reports.build(section, { routerId: RID, iface: IF1, ...WINDOW(section), aggregate: '' });
    const buf = await Pdf.toBuffer(b.title, b.pdf.columns, b.pdf.rows, b.pdf.meta);
    // Not byte-identity across runs: PDFKit embeds a CreationDate.
    assert.equal(buf.subarray(0, 5).toString(), '%PDF-', section);
    assert.ok(buf.includes(Buffer.from('%%EOF')), section + ' has no trailer');
    assert.ok(buf.length > 500, section + ' produced a suspiciously small PDF');
  }
});

test('the size guard refuses before the whole document is built', async () => {
  const b = Reports.build('ping', { routerId: RID, iface: IF1, from: FROM, to: TO, aggregate: '' });
  await assert.rejects(
    () => Pdf.toBuffer(b.title, b.pdf.columns, b.pdf.rows, b.pdf.meta, 500),
    /exceeded 500 bytes/);
});

// ── What the move must not have lost ───────────────────────────────────────

test('formula-injection neutralisation survived the move to format.js', () => {
  // An interface name or ping target is router-controlled, and Excel executes a
  // cell starting = + - @ as a formula.
  const csv = F.toCsv([{ a: '=cmd|calc', b: 'ok' }], ['a', 'b']);
  assert.match(csv, /'=cmd/);
});

test('teardown', () => {
  db.close();
  fs.rmSync(TMP, { recursive: true, force: true });
});
