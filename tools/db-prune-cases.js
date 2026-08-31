#!/usr/bin/env node
'use strict';
/**
 * THE DAILY RETENTION SWEEP, lifted from the live `prune()`.
 *
 * ── WHY THIS EXISTS AT ALL ─────────────────────────────────────────────────
 *
 * The port had no retention sweep. The Settings page renders
 * `dbRetentionDays`, `dbAlertRetentionDays` and `dbAuditRetentionDays`, the
 * write route validates and persists them, and NOTHING in the port ever read
 * one — so an operator could set a retention policy that did nothing, and the
 * database grew without bound.
 *
 * That is the third instance in two days of one shape: a setting rendered,
 * validated, persisted, and never consumed. The other two were the operator's
 * reports (`topN`, and `topTalkersN` beside it). This one was found by counting
 * the class out of the generated settings table instead of waiting for a third
 * report.
 *
 * ── WHAT IS LIFTED, AND WHY IT IS A LEDGER RATHER THAN A REPLAY ────────────
 *
 * `prune()` is six DELETEs and three cutoffs. Running it would need
 * better-sqlite3 and the container; the BEHAVIOUR is covered instead by a Go
 * test against a real temporary database, which `modernc.org/sqlite` makes
 * possible with no cgo and no container.
 *
 * What a Go test cannot check is whether the port deletes from the same tables,
 * on the same columns, against the same cutoff — a port that pruned six tables
 * of its own invention would pass every behavioural test it wrote. So this lifts
 * the MAPPING out of `src/db.js` and the Go side asserts against it. Two traps
 * in six rows, and both are the kind a retyped table gets wrong:
 *
 *   - `alert_events` keys on `fired_at`, not `ts`. Five of the six use `ts`.
 *   - `connectivity_events` ages on the ALERT retention, not the metric one,
 *     even though its column is `ts` like the metrics. Getting that wrong
 *     deletes a year of connectivity history under a 90-day metric policy.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/db-prune-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const SRC = process.env.MIKRODASH_SRC || '../MikroDash';
const OUT = path.join(__dirname, '..', 'testdata', 'db-prune-cases.json');

const db = fs.readFileSync(path.join(SRC, 'src', 'db.js'), 'utf8');

// The function body, brace-matched from its declaration. A line-count slice
// would drift the first time a comment is added above it.
const start = db.indexOf('function prune(');
assert.ok(start >= 0, 'could not find prune() in the live db.js');
let depth = 0;
let end = start;
for (let i = db.indexOf('{', start); i < db.length; i++) {
  if (db[i] === '{') depth++;
  else if (db[i] === '}') { depth--; if (depth === 0) { end = i; break; } }
}
const body = db.slice(start, end + 1);

// ── the three cutoffs and their fallbacks ─────────────────────────────────
//
// The FALLBACK is part of the contract, not a detail: `s.dbRetentionDays || 90`
// means a missing OR ZERO setting takes the default. A port that treated 0 as
// "keep nothing" would delete the entire history on a settings file that had
// simply never been written.
const cutoffs = {};
for (const m of body.matchAll(
  /const\s+(\w+Cutoff)\s*=\s*Date\.now\(\)\s*-\s*\(\s*(\w+)\s*\|\|\s*(\d+)\s*\)\s*\*\s*(\d+)/g)) {
  cutoffs[m[1]] = { arg: m[2], fallbackDays: Number(m[3]), msPerDay: Number(m[4]) };
}
assert.strictEqual(Object.keys(cutoffs).length, 3,
  `expected three cutoffs, found ${Object.keys(cutoffs).length}`);
for (const [name, c] of Object.entries(cutoffs)) {
  assert.strictEqual(c.msPerDay, 86400000, `${name} is not measured in days`);
}

// ── the six deletes ───────────────────────────────────────────────────────
const tables = [];
for (const m of body.matchAll(
  /DELETE FROM\s+(\w+)\s+WHERE\s+(\w+)\s*<\s*\?'\)\.run\((\w+)\)/g)) {
  tables.push({ table: m[1], column: m[2], cutoff: m[3] });
}
assert.strictEqual(tables.length, 6, `expected six DELETEs, found ${tables.length}`);

// The traps, asserted HERE as well as in Go. If upstream ever normalises these
// the lift fails loudly rather than quietly teaching the port a new mapping.
const byTable = Object.fromEntries(tables.map((t) => [t.table, t]));
assert.strictEqual(byTable.alert_events.column, 'fired_at',
  'alert_events no longer keys on fired_at — the port mirrors this exactly');
assert.strictEqual(byTable.connectivity_events.cutoff, byTable.alert_events.cutoff,
  'connectivity_events no longer shares the alert cutoff');
assert.notStrictEqual(byTable.connectivity_events.cutoff, byTable.traffic_samples.cutoff,
  'connectivity_events now shares the METRIC cutoff — that is a behaviour change');

// The audit row the sweep writes. Only when something was deleted: a daily
// no-op that recorded itself would bury the trail it is written into.
const recordsAudit = /action:\s*'db\.prune'/.test(body);
const recordsOnlyWhenNonZero = /if\s*\(total\s*>\s*0\)/.test(body);
assert.ok(recordsAudit, 'the sweep no longer records db.prune');
assert.ok(recordsOnlyWhenNonZero, 'the sweep now records even when it deleted nothing');

// ── the interval ──────────────────────────────────────────────────────────
const iv = db.match(/_pruneTimer\s*=\s*setInterval\(run,\s*([\d\s*]+)\)/);
assert.ok(iv, 'could not read the prune interval');
const intervalMs = iv[1].split('*').map((n) => Number(n.trim())).reduce((a, b) => a * b, 1);
const runsImmediately = /\brun\(\);\s*\n\s*_pruneTimer/.test(db);
assert.ok(runsImmediately,
  'the live sweep no longer runs immediately — a restart would then leave a day unpruned');

const out = {
  generatedFrom: 'src/db.js:prune, startPruneInterval',
  cutoffs, tables, intervalMs, runsImmediately,
  recordsAudit, recordsOnlyWhenNonZero,
};

const text = JSON.stringify(out, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== text) {
    console.error('db-prune-cases: STALE — re-run without --check');
    process.exit(1);
  }
  console.log(`db-prune-cases: current (${tables.length} tables, ${Object.keys(cutoffs).length} cutoffs)`);
} else {
  fs.writeFileSync(OUT, text);
  console.log(`db-prune-cases: wrote ${tables.length} tables, ${Object.keys(cutoffs).length} cutoffs, ` +
    `interval ${intervalMs}ms`);
}
