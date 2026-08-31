#!/usr/bin/env node
'use strict';
/**
 * Pin the PPP rate arithmetic against the LIVE implementation.
 *
 * WHY THIS EXISTS. The golden differential gate covers every collector whose
 * fixture produces a payload — but this fleet runs no PPP, so the /ppp/active
 * fixture is the empty-menu junk row and the golden is the empty state. The
 * interesting half of the collector, the part that turns two byte readings into
 * a rate, is reached by no fixture on either side. src/collectors/ppp.js says as
 * much in its own header: "NOT VERIFIED AGAINST HARDWARE".
 *
 * So the cases are synthetic and the ANSWERS are not: this runs the live
 * `parsePppSessions` over each scenario and records what it returns.
 * internal/collect/ppp_test.go replays the same inputs through the Go port and
 * compares. Neither side is asked about itself.
 *
 * The same shape as tools/audit-cases.js, and for the same reason — where a
 * fixture cannot reach, run the original and diff.
 *
 * Every value here is synthetic: TEST-NET-2 addresses, locally-administered
 * MACs, invented account names, and a FIXED `now` in epoch milliseconds. The
 * arithmetic is about differences between timestamps, so a wall clock would make
 * the file stale on every run for no reason.
 *
 *   node tools/ppp-cases.js            # write testdata/ppp-cases.json
 *   node tools/ppp-cases.js --check    # fail if it is stale
 */

const fs   = require('node:fs');
const path = require('node:path');

// Resolved against the CWD, not this file: MIKRODASH_SRC is habitually passed as
// `../MikroDash`, and require() would otherwise resolve it against tools/.
const ROOT = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT  = process.env.PPP_OUT || path.join(__dirname, '..', 'testdata', 'ppp-cases.json');

const Ppp = require(path.join(ROOT, 'src', 'collectors', 'ppp.js'));
if (typeof Ppp.parsePppSessions !== 'function') {
  console.error('src/collectors/ppp.js no longer exports parsePppSessions — this generator ' +
                'was pinning a function that has moved. Find it before regenerating.');
  process.exit(1);
}

const T0 = 1700000000000;   // fixed, so a rerun produces the same file

const sess = (over) => Object.assign({
  '.id': '*1', name: 'bob', service: 'pppoe', address: '198.51.100.5',
  'caller-id': '02:00:5E:10:00:01', uptime: '1h2m3s', encoding: 'AES-128',
  'session-id': '0x81000001', 'bytes-in': '1000', 'bytes-out': '2000',
}, over);

// Each step feeds one row set at one instant, carrying `prev` forward. The
// carried state is the point: a rate only exists because a previous reading did.
const CASES = [
  { name: 'first sample has no rate at all',
    steps: [{ at: 0, rows: [sess({})] }] },

  { name: 'second sample two seconds later derives a rate',
    steps: [{ at: 0, rows: [sess({})] },
            { at: 2000, rows: [sess({ 'bytes-in': '3000', 'bytes-out': '5000' })] }] },

  { name: 'a counter reset clamps to zero rather than going negative',
    steps: [{ at: 0, rows: [sess({ 'bytes-in': '9000', 'bytes-out': '9000' })] },
            { at: 2000, rows: [sess({ 'bytes-in': '10', 'bytes-out': '20' })] }] },

  { name: 'unchanged bytes past the idle threshold read as idle',
    steps: [{ at: 0, rows: [sess({})] },
            { at: 12000, rows: [sess({})] }] },

  { name: 'the baseline only advances when the bytes move',
    steps: [{ at: 0, rows: [sess({})] },
            { at: 3000, rows: [sess({})] },
            { at: 6000, rows: [sess({ 'bytes-in': '7000', 'bytes-out': '8000' })] }] },

  { name: 'the empty-menu junk row is dropped',
    steps: [{ at: 0, rows: [{ undefined: '' }] }] },

  { name: 'a row with no .id is keyed by name and service',
    steps: [{ at: 0, rows: [sess({ '.id': '' })] },
            { at: 2000, rows: [sess({ '.id': '', 'bytes-in': '5000' })] }] },

  { name: 'sessions are ordered by name',
    steps: [{ at: 0, rows: [
      sess({ '.id': '*3', name: 'zoe' }),
      sess({ '.id': '*1', name: 'Alice' }),
      sess({ '.id': '*2', name: 'bob' }),
    ] }] },

  { name: 'byte limits are parsed when present and null when absent',
    steps: [{ at: 0, rows: [
      sess({ '.id': '*1', name: 'withlimits', 'limit-bytes-in': '1000000', 'limit-bytes-out': '2000000' }),
      sess({ '.id': '*2', name: 'nolimits' }),
    ] }] },

  { name: 'a session that goes away stops being tracked',
    steps: [{ at: 0, rows: [sess({ '.id': '*1', name: 'alice' }), sess({ '.id': '*2', name: 'bob' })] },
            { at: 2000, rows: [sess({ '.id': '*1', name: 'alice', 'bytes-in': '4000' })] }] },

  { name: 'non-numeric byte counters take their leading number',
    steps: [{ at: 0, rows: [sess({ 'bytes-in': '123abc', 'bytes-out': '' })] }] },

  { name: 'service is upper-cased and a missing one stays empty',
    steps: [{ at: 0, rows: [
      sess({ '.id': '*1', name: 'a', service: 'pppoe' }),
      sess({ '.id': '*2', name: 'b', service: '' }),
    ] }] },
];

function run() {
  return CASES.map((c) => {
    const prev = new Map();
    const steps = c.steps.map((s) => {
      const out = Ppp.parsePppSessions(s.rows, prev, T0 + s.at);
      return {
        atMs: s.at,
        rows: s.rows,
        want: out,
        // The carried state, so the Go side is compared on what it REMEMBERS as
        // well as on what it answered. A port that returned the right rates
        // while leaking or pruning the wrong keys would pass on `want` alone.
        prevAfter: [...prev.entries()]
          .map(([k, v]) => ({ key: k, rx: v.rx, tx: v.tx, tsOffsetMs: v.ts - T0 }))
          .sort((a, b) => a.key.localeCompare(b.key)),
      };
    });
    return { name: c.name, steps };
  });
}

function main() {
  const check = process.argv.includes('--check');
  const body = JSON.stringify({ baseMs: T0, cases: run() }, null, 2) + '\n';

  if (check) {
    const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : null;
    if (cur !== body) {
      console.error('ppp cases are stale — run: node tools/ppp-cases.js');
      process.exit(1);
    }
    console.log('ppp cases up to date (' + JSON.parse(body).cases.length + ' scenarios)');
    return;
  }
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, body);
  const parsed = JSON.parse(body);
  const steps = parsed.cases.reduce((n, c) => n + c.steps.length, 0);
  console.log('wrote ' + path.relative(path.join(__dirname, '..'), OUT) +
              ' (' + parsed.cases.length + ' scenarios, ' + steps + ' readings)');
}

main();
