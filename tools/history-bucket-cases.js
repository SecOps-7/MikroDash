'use strict';
/**
 * Pin the history WRITER's bucketing against the live implementation.
 *
 * ── WHY THIS IS PORTED NOW AND SWITCHED OFF ────────────────────────────────
 *
 * `src/db-writer.js` accumulates one-minute buckets and writes ONE row per
 * minute per (router, interface-or-target). Part 125 found that nothing on the
 * Go side writes `traffic_samples`, `bandwidth_usage`, `ping_samples` or
 * `connectivity_events` — Node does, and at cutover that stops.
 *
 * A Go writer cannot be TURNED ON during coexistence: both processes would
 * bucket the same minute and insert two rows for it, and Reports averages over
 * those rows. That is the same doubling class as the Routers stats pool. So this
 * is ported as PURE arithmetic with no database at all, exactly as the backup
 * scheduler was — everything exists, nothing is started.
 *
 * ── WHAT THE CASES ARE FOR ──────────────────────────────────────────────────
 *
 * The arithmetic looks trivial and is not:
 *
 *   - a bucket flushes when the minute ROLLS OVER, so the last bucket is never
 *     written without an explicit flush — the live comment says "call on session
 *     teardown to avoid data loss";
 *   - the written timestamp is `minuteTs + 30000`, the MIDDLE of the minute, not
 *     its start;
 *   - traffic flushes on `count > 0` while bandwidth flushes on
 *     `sumRxMb + sumTxMb > 0` — so a minute of genuine ZEROES writes a traffic
 *     row and no bandwidth row;
 *   - ping averages RTT over the samples that HAVE one (`rttCount`) and loss
 *     over all of them (`count`), which are different divisors;
 *   - keys are `router:name` and the name may contain colons (an IPv6 ping
 *     target), so the split is on the FIRST colon only.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/history-bucket-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'internal', 'history', 'testdata', 'bucket-cases.json');

const srcText = fs.readFileSync(path.join(LIVE, 'src', 'db-writer.js'), 'utf8');

// Run the REAL writer with a fake `db` that records rows instead of inserting.
function makeWriter() {
  const rows = [];
  const db = {
    insertTrafficSample: (rid, name, rx, tx, ts) => rows.push({ table: 'traffic', rid, name, rx, tx, ts }),
    insertBandwidthSample: (rid, name, rx, tx, ts) => rows.push({ table: 'bandwidth', rid, name, rx, tx, ts }),
    insertPingSample: (rid, name, rtt, loss, ts) => rows.push({ table: 'ping', rid, name, rtt, loss, ts }),
    insertConnectivityEvent: (rid, connected, ts) => rows.push({ table: 'connectivity', rid, connected, ts }),
  };
  const module_ = { exports: {} };
  const ctx = {
    module: module_, exports: module_.exports, Map, Math, Date,
    require: (id) => { if (id === './db') return db; throw new Error('unexpected require: ' + id); },
  };
  vm.createContext(ctx);
  vm.runInContext(srcText, ctx);
  return { api: module_.exports, rows };
}

// Every call is (op, ...args). Time is explicit everywhere — the live code falls
// back to Date.now() and a corpus that relied on that would not be reproducible.
const SCENARIOS = {
  'one traffic sample, never flushed': [
    ['traffic', 'r1', 'ether1', 100, 50, 60000],
  ],
  'two samples in ONE minute, then flush': [
    ['traffic', 'r1', 'ether1', 100, 50, 60000],
    ['traffic', 'r1', 'ether1', 200, 150, 90000],
    ['flushTraffic', null],
  ],
  'the minute ROLLS OVER, which writes the previous bucket': [
    ['traffic', 'r1', 'ether1', 100, 50, 60000],
    ['traffic', 'r1', 'ether1', 300, 100, 120000],
  ],
  'three minutes in a row': [
    ['traffic', 'r1', 'ether1', 10, 10, 60000],
    ['traffic', 'r1', 'ether1', 20, 20, 120000],
    ['traffic', 'r1', 'ether1', 30, 30, 180000],
    ['flushTraffic', null],
  ],
  'a minute of ZEROES writes traffic and no bandwidth': [
    ['traffic', 'r1', 'ether1', 0, 0, 60000],
    ['traffic', 'r1', 'ether1', 0, 0, 120000],
  ],
  // FLUSHED rather than rolled over. Every other zero case reaches the rollover
  // branch; this one reaches the same pair of tests inside `flushTraffic`, and
  // without it a mutation collapsing them THERE survives — the two code paths
  // duplicate the rule and a corpus has to exercise both.
  'a minute of ZEROES, flushed rather than rolled over': [
    ['traffic', 'r1', 'ether1', 0, 0, 60000],
    ['flushTraffic', null],
  ],
  'zeroes flushed for ONE router only': [
    ['traffic', 'r1', 'ether1', 0, 0, 60000],
    ['traffic', 'r2', 'ether1', 5, 5, 60000],
    ['flushTraffic', 'r1'],
  ],
  'zero rx but non-zero tx writes both': [
    ['traffic', 'r1', 'ether1', 0, 8, 60000],
    ['traffic', 'r1', 'ether1', 0, 0, 120000],
  ],
  'two interfaces are separate buckets': [
    ['traffic', 'r1', 'ether1', 100, 0, 60000],
    ['traffic', 'r1', 'ether2', 200, 0, 60000],
    ['flushTraffic', null],
  ],
  'two routers are separate buckets': [
    ['traffic', 'r1', 'ether1', 100, 0, 60000],
    ['traffic', 'r2', 'ether1', 200, 0, 60000],
    ['flushTraffic', null],
  ],
  'flushing ONE router leaves the other open': [
    ['traffic', 'r1', 'ether1', 100, 0, 60000],
    ['traffic', 'r2', 'ether1', 200, 0, 60000],
    ['flushTraffic', 'r1'],
  ],
  'no router id is ignored': [['traffic', '', 'ether1', 100, 50, 60000], ['flushTraffic', null]],
  'no interface name is ignored': [['traffic', 'r1', '', 100, 50, 60000], ['flushTraffic', null]],
  'a mid-minute timestamp floors to the same bucket': [
    ['traffic', 'r1', 'ether1', 100, 0, 61000],
    ['traffic', 'r1', 'ether1', 200, 0, 119999],
    ['flushTraffic', null],
  ],
  'fractional rates': [
    ['traffic', 'r1', 'ether1', 0.5, 0.25, 60000],
    ['traffic', 'r1', 'ether1', 1.5, 0.75, 90000],
    ['flushTraffic', null],
  ],
  // ── ping ─────────────────────────────────────────────────────────────────
  'one ping sample, flushed': [['ping', 'r1', '1.1.1.1', 12.5, 0, 60000], ['flushTraffic', null]],
  'ping averages rtt over samples that HAVE one': [
    ['ping', 'r1', '1.1.1.1', 10, 0, 60000],
    ['ping', 'r1', '1.1.1.1', null, 100, 90000],
    ['ping', 'r1', '1.1.1.1', 20, 0, 100000],
    ['flushTraffic', null],
  ],
  'every ping lost — rtt is null, loss is 100': [
    ['ping', 'r1', '1.1.1.1', null, 100, 60000],
    ['ping', 'r1', '1.1.1.1', null, 100, 90000],
    ['flushTraffic', null],
  ],
  'an IPv6 target contains colons': [
    ['ping', 'r1', '2001:db8::1', 5, 0, 60000],
    ['ping', 'r1', '2001:db8::1', 15, 0, 90000],
    ['flushTraffic', null],
  ],
  'ping rolls over': [
    ['ping', 'r1', '1.1.1.1', 10, 0, 60000],
    ['ping', 'r1', '1.1.1.1', 20, 0, 120000],
  ],
  'ping with no router id is ignored': [['ping', '', '1.1.1.1', 10, 0, 60000], ['flushTraffic', null]],
  'two ping targets are separate buckets': [
    ['ping', 'r1', '1.1.1.1', 10, 0, 60000],
    ['ping', 'r1', '8.8.8.8', 20, 0, 60000],
    ['flushTraffic', null],
  ],
  // ── mixed ────────────────────────────────────────────────────────────────
  'traffic and ping together': [
    ['traffic', 'r1', 'ether1', 80, 40, 60000],
    ['ping', 'r1', '1.1.1.1', 12, 0, 60000],
    ['flushTraffic', null],
  ],
};

const cases = [];
for (const [name, steps] of Object.entries(SCENARIOS)) {
  const { api, rows } = makeWriter();
  for (const [op, ...args] of steps) {
    if (op === 'traffic') api.recordTraffic(...args);
    else if (op === 'ping') api.recordPing(...args);
    else if (op === 'flushTraffic') api.flushTraffic(args[0]);
    else throw new Error('unknown op ' + op);
  }
  cases.push({ name, steps, rows });
}

assert.ok(cases.some((c) => c.rows.length), 'no scenario produced a row — the harness is not driving the writer');
assert.ok(cases.some((c) => c.rows.some((r) => r.table === 'bandwidth')),
  'no bandwidth row — the zero-suppression rule cannot be pinned');
assert.ok(cases.some((c) => c.rows.some((r) => r.table === 'ping' && r.rtt === null)),
  'no all-lost ping row — the two-divisor rule cannot be pinned');

const body = JSON.stringify({ cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== body) { console.error(path.relative(ROOT, OUT) + ' is stale — rerun without --check'); process.exit(1); }
  console.log(path.relative(ROOT, OUT) + ' is up to date (' + cases.length + ' scenarios)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  console.log('wrote ' + path.relative(ROOT, OUT) + ' (' + cases.length + ' scenarios, ' +
    cases.reduce((n, c) => n + c.rows.length, 0) + ' rows)');
}
