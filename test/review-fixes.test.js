'use strict';
// Regression tests for the general code-review fixes (session 2026-05-29).
// Covers: ping bucketing, alerter DB-persistence decoupling + cooldown ordering,
// and per-router evaluator isolation.

const test   = require('node:test');
const assert = require('node:assert/strict');

// ═══════════════════════════════════════════════════════════════════════════
// db-writer ping bucketing (#11) — stub ./db before requiring db-writer
// ═══════════════════════════════════════════════════════════════════════════
const dbPath = require.resolve('../src/db');

// Records every db.insert* call so we can assert on flush behavior.
const dbStub = {
  pings: [], traffic: [], bandwidth: [], conn: [], alerts: [], resolves: [],
  reset() { this.pings = []; this.traffic = []; this.bandwidth = []; this.conn = []; this.alerts = []; this.resolves = []; },
  insertPingSample(rid, target, rtt, loss, ts) { this.pings.push({ rid, target, rtt, loss, ts }); },
  insertTrafficSample(rid, iface, rx, tx, ts) { this.traffic.push({ rid, iface, rx, tx, ts }); },
  insertBandwidthSample(rid, iface, rx, tx, ts) { this.bandwidth.push({ rid, iface, rx, tx, ts }); },
  insertConnectivityEvent(rid, c) { this.conn.push({ rid, c }); },
  insertAlertEvent(rid, type, subj, detail) { this.alerts.push({ rid, type, subj, detail }); },
  resolveAlertEvent(rid, type, subj) { this.resolves.push({ rid, type, subj }); },
};

const origDb = require.cache[dbPath];
require.cache[dbPath] = { id: dbPath, filename: dbPath, loaded: true, exports: dbStub };
const dbWriter = require('../src/db-writer');
if (origDb) require.cache[dbPath] = origDb; else delete require.cache[dbPath];

test('recordPing buckets into 1-minute averages instead of writing per-sample', () => {
  dbStub.reset();
  const base = 1_700_000_000_000; // arbitrary fixed minute boundary
  // Three samples in the same minute → no insert yet (bucket still open).
  dbWriter.recordPing('r1', '1.1.1.1', 10, 0, base + 1000);
  dbWriter.recordPing('r1', '1.1.1.1', 20, 0, base + 2000);
  dbWriter.recordPing('r1', '1.1.1.1', 30, 6, base + 3000);
  assert.equal(dbStub.pings.length, 0, 'no per-sample inserts while the minute is open');

  // A sample in the next minute rolls the bucket over → one averaged insert.
  dbWriter.recordPing('r1', '1.1.1.1', 40, 0, base + 61_000);
  assert.equal(dbStub.pings.length, 1, 'one row flushed on minute rollover');
  const row = dbStub.pings[0];
  assert.equal(row.rid, 'r1');
  assert.equal(row.rtt, 20, 'avg rtt = (10+20+30)/3');
  assert.equal(row.loss, 2, 'avg loss = (0+0+6)/3');
});

test('recordPing averages rtt only over non-null samples', () => {
  dbStub.reset();
  const base = 1_700_000_120_000;
  dbWriter.recordPing('r2', '8.8.8.8', null, 100, base + 1000); // timeout: no rtt, full loss
  dbWriter.recordPing('r2', '8.8.8.8', 50, 0, base + 2000);
  dbWriter.recordPing('r2', '8.8.8.8', null, 100, base + 3000);
  dbWriter.flushTraffic('r2');
  assert.equal(dbStub.pings.length, 1);
  assert.equal(dbStub.pings[0].rtt, 50, 'rtt averaged over the single non-null sample');
  assert.equal(dbStub.pings[0].loss, 200 / 3, 'loss averaged over all 3 samples');
});

test('flushTraffic flushes only the requested router and clears its buckets', () => {
  dbStub.reset();
  const base = 1_700_000_180_000;
  dbWriter.recordPing('rA', 'x', 10, 0, base + 1000);
  dbWriter.recordPing('rB', 'y', 20, 0, base + 1000);
  dbWriter.flushTraffic('rA');
  assert.equal(dbStub.pings.length, 1);
  assert.equal(dbStub.pings[0].rid, 'rA');
  // Flushing rA again is a no-op (bucket already cleared).
  dbWriter.flushTraffic('rA');
  assert.equal(dbStub.pings.length, 1, 'rA bucket was cleared after first flush');
});

// ═══════════════════════════════════════════════════════════════════════════
// alerter: DB persistence decoupled from channels + cooldown gates send only (#6)
// ═══════════════════════════════════════════════════════════════════════════
const notifierPath = require.resolve('../src/notifier');
const routersPath  = require.resolve('../src/routers');
const notifierStub = { calls: [], send: async function (s, t, b) { this.calls.push({ t, b }); } };
const routersStub  = { getById: () => null };
const origNotifier = require.cache[notifierPath];
const origRouters  = require.cache[routersPath];
const origDb2      = require.cache[dbPath];
require.cache[notifierPath] = { id: notifierPath, filename: notifierPath, loaded: true, exports: notifierStub };
require.cache[routersPath]  = { id: routersPath,  filename: routersPath,  loaded: true, exports: routersStub };
require.cache[dbPath]       = { id: dbPath,        filename: dbPath,        loaded: true, exports: dbStub };
const alerter = require('../src/alerter');
if (origNotifier) require.cache[notifierPath] = origNotifier; else delete require.cache[notifierPath];
if (origRouters)  require.cache[routersPath]  = origRouters;  else delete require.cache[routersPath];
if (origDb2)      require.cache[dbPath]        = origDb2;      else delete require.cache[dbPath];

function makeSettings(o = {}) {
  return { telegramEnabled: false, pushbulletEnabled: false, smtpEnabled: false, ntfyEnabled: false,
    notifCpu: true, notifCooldownSec: 60, alertCpuThreshold: 80,
    notifTitle: 'T', notifBody: '{{detail}}', notifBodyUp: '{{detail}} up', ...o };
}

test('alert is persisted to DB even when no notification channel is configured', async () => {
  dbStub.reset(); notifierStub.calls = [];
  alerter.updateSettings(makeSettings({ telegramEnabled: false }));
  const router = { id: 'rDB', label: 'R', alertsEnabled: true };
  const { evaluate } = alerter.createEvaluator(() => 'R', () => router);
  evaluate('system:update', { cpuLoad: 95 });
  await new Promise(r => setImmediate(r));
  assert.equal(notifierStub.calls.length, 0, 'no push sent with no channel');
  assert.equal(dbStub.alerts.length, 1, 'but the alert IS persisted');
  assert.equal(dbStub.alerts[0].rid, 'rDB');
  assert.equal(dbStub.alerts[0].type, 'high_cpu');
});

test('cooldown is not consumed when no channel is active, so enabling one later still notifies', async () => {
  dbStub.reset(); notifierStub.calls = [];
  // First: no channel. A down event persists but must NOT stamp the cooldown.
  alerter.updateSettings(makeSettings({ telegramEnabled: false, notifCooldownSec: 600 }));
  const router = { id: 'rCD', label: 'R', alertsEnabled: true };
  const { evaluate } = alerter.createEvaluator(() => 'R', () => router);
  evaluate('system:update', { cpuLoad: 95 });
  await new Promise(r => setImmediate(r));
  assert.equal(notifierStub.calls.length, 0);

  // Now enable a channel and re-cross the threshold (normal → high again).
  // The earlier down (no channel) must NOT have stamped the 'cpu:router:down'
  // cooldown, so this high crossing delivers a notification.
  alerter.updateSettings(makeSettings({ telegramEnabled: true, notifCooldownSec: 600 }));
  evaluate('system:update', { cpuLoad: 50 }); // back to normal (recovery)
  evaluate('system:update', { cpuLoad: 95 }); // high again (down)
  await new Promise(r => setImmediate(r));
  const downCalls = notifierStub.calls.filter(c => /High CPU/.test(c.b) || /at 95%/.test(c.b));
  assert.equal(downCalls.length, 1, 'the down notification fires — cooldown was not pre-consumed');
});

test('per-router evaluators keep independent threshold state', async () => {
  dbStub.reset(); notifierStub.calls = [];
  alerter.updateSettings(makeSettings({ telegramEnabled: true, notifCooldownSec: 0 }));
  const a = alerter.createEvaluator(() => 'A', () => ({ id: 'rX', alertsEnabled: true }));
  const b = alerter.createEvaluator(() => 'B', () => ({ id: 'rY', alertsEnabled: true }));
  a.evaluate('system:update', { cpuLoad: 95 }); // A goes high
  b.evaluate('system:update', { cpuLoad: 10 }); // B stays normal — must not be affected by A
  await new Promise(r => setImmediate(r));
  // Exactly one down alert (from A); B never crossed, so no second alert.
  const downAlerts = dbStub.alerts.filter(x => x.type === 'high_cpu');
  assert.equal(downAlerts.length, 1);
  assert.equal(downAlerts[0].rid, 'rX', 'alert attributed to the router that actually crossed');
});
