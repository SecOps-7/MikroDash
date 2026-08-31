'use strict';
/**
 * THE ALERT WRITES — `acknowledgeAlert`, `resolveAllAlerts` and
 * `getAlertRouterId`, run for real.
 *
 * The read half of the bell landed with `tools/alertfeed-cases.js`. This is the
 * write half: the two things the panel's two buttons do, plus the scope lookup
 * that decides whether the caller is allowed to do them at all.
 *
 * ---- WHY THE TIMESTAMPS ARE RECORDED SYMBOLICALLY -------------------------
 *
 * Every one of these stamps `Date.now()`, so a corpus holding the literal
 * numbers would differ on every run and `--check` could never pass. The feed
 * generator solved that by rewriting the timestamps afterwards; that is not
 * available here, because WHEN a row was stamped is the property under test.
 *
 * So each timestamp is recorded as one of three things:
 *
 *   null          the column is still NULL.
 *   'seeded'      it still holds the constant the seed wrote — untouched.
 *   'fresh'       THIS call changed it.
 *   'earlier'     an EARLIER call in this script changed it, and this one did not.
 *
 * ---- AND THE LABEL IS DECIDED BY COMPARISON, NOT BY THE CLOCK -------------
 *
 * The first version classified against `[Date.now() before, Date.now() after]`
 * the call. That is not deterministic: two calls landing in the SAME millisecond
 * make an earlier call's write fall inside this call's window, and the same row
 * is labelled 'earlier' or 'fresh' depending on machine speed. It passed
 * `--check` on one run and failed on the next, which is the worst way for a
 * corpus to be wrong — it looks like a real difference in the code.
 *
 * So a column is 'fresh' when its value DIFFERS from what the previous snapshot
 * saw, and keeps its old label when it does not. No clock is consulted, and the
 * question asked — "did this call write here?" — is the one the labels claim to
 * answer.
 *
 * The classifier still refuses rather than guessing when a value it has never
 * seen appears in a column nothing should have touched; that refusal fired twice
 * while this file was being written, once on a seeded `resolved_at` it had not
 * been told about.
 *
 * ---- THE FOUR RULES THIS EXISTS TO PIN ------------------------------------
 *
 *  1. `acknowledgeAlert` UPDATEs `WHERE acknowledged_at IS NULL`, so a SECOND
 *     acknowledgement does not overwrite the first. The row still comes back —
 *     the SELECT is unconditional — so the caller sees success and the ORIGINAL
 *     person's name. A port dropping that clause reassigns credit to whoever
 *     clicked last, and the audit trail then disagrees with the row.
 *
 *  2. It does NOT require the alert to be open. Acknowledging something after it
 *     recovered is a legitimate way to say "seen it", and the live comment says
 *     so. A port adding `AND resolved_at IS NULL` makes the button silently do
 *     nothing on exactly the rows the panel shows under "Recently resolved".
 *
 *  3. `resolveAllAlerts` acknowledges AND resolves, and acknowledges only where
 *     nobody has yet. Resolving is what the Routers page counts, so the older
 *     acknowledge-only version emptied the bell and left the router reading
 *     "Alerting" forever. Keeping an existing acknowledger's name is what stops
 *     Reports attributing someone else's row to whoever cleared the list.
 *
 *  4. Both stamp ONE instant across the batch — one `Date.now()`, not one per
 *     row. Asserted below, because a per-row clock reads as correct until two
 *     rows land either side of a millisecond and the pair sorts apart.
 *
 * ── CONTAINER ONLY ─────────────────────────────────────────────────────────
 *
 *   docker exec -e MIKRODASH_SRC=/app mikrodash node /work/tools/alertwrite-cases.js
 *
 * DATA_DIR is pointed at a temp directory BEFORE src/db.js is required, because
 * that module resolves its path at load time.
 */

const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const OUT = process.env.ALERTWRITE_OUT || path.join(ROOT, 'testdata', 'alertwrite-cases.json');

const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'mikrodash-alertwrite-'));
process.env.DATA_DIR = TMP;

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const db = require(path.join(SRC, 'src', 'db.js'));

const R = 'router-a';
const OTHER = 'router-b';

const handle = db.open();
assert.ok(handle, 'db.open() returned no handle');

// Values no `Date.now()` can collide with, so a seeded timestamp and a written
// one are never confused for each other. Both are in SEEDED because the
// classifier must be able to say "this column was left alone" about EITHER
// column — the first run only knew the ack one, and refused to label a seeded
// `resolved_at` rather than guessing. That refusal is the point of it.
const SEEDED_ACK_AT = 1000;
const SEEDED_RESOLVED_AT = 5000;
const SEEDED = new Set([SEEDED_ACK_AT, SEEDED_RESOLVED_AT]);
const SEEDED_ACK_BY = 'seeded-operator';

/**
 * The seed. Every row is a different combination of (open?, acknowledged?),
 * because that pair is exactly what the two functions branch on.
 */
const ROWS = [
  { key: 'open-unacked',     router: R,     resolved: null, ackedAt: null },
  { key: 'open-acked',       router: R,     resolved: null, ackedAt: SEEDED_ACK_AT },
  { key: 'closed-unacked',   router: R,     resolved: SEEDED_RESOLVED_AT, ackedAt: null },
  { key: 'closed-acked',     router: R,     resolved: SEEDED_RESOLVED_AT, ackedAt: SEEDED_ACK_AT },
  { key: 'open-second-ack',  router: R,     resolved: null, ackedAt: null },
  // NEVER TOUCHED until clear-all, and that is its whole job. Every other open
  // row on R has been acknowledged by an earlier case by the time clear-all
  // runs, which made clear-all's acknowledge statement a no-op on this data —
  // deleting the statement outright still passed. This row is the one it acts
  // on, so the two halves of clear-all can be told apart.
  { key: 'open-untouched',   router: R,     resolved: null, ackedAt: null },
  // Another router's open row. `resolveAllAlerts` is scoped, and a port that
  // dropped the router from the WHERE would clear the whole fleet from one
  // button — which is why this one is here rather than for symmetry.
  { key: 'other-open',       router: OTHER, resolved: null, ackedAt: null },
];

const ids = {};
for (const r of ROWS) {
  ids[r.key] = Number(db.insertAlertEvent(r.router, r.key, 'subj-' + r.key, 'detail'));
  assert.ok(ids[r.key] > 0, 'insert returned no id for ' + r.key);
  const n = handle.prepare(
    'UPDATE alert_events SET resolved_at = ?, acknowledged_at = ?, acknowledged_by = ? WHERE id = ?'
  ).run(r.resolved, r.ackedAt, r.ackedAt === null ? null : SEEDED_ACK_BY, ids[r.key]).changes;
  assert.equal(n, 1, 'seed update touched ' + n + ' rows for ' + r.key);
}

// Every id the seed minted, so an "unknown id" case cannot collide with one.
const MAX_ID = Math.max(...Object.values(ids));
const UNKNOWN_ID = MAX_ID + 1000;

/**
 * The running label for every (row, column), and the raw values they were last
 * seen holding. `snapshot()` advances both.
 */
const labels = {};
const lastRaw = {};

const readRow = (id) => handle.prepare(
  'SELECT id, router_id, alert_type, subject, detail, fired_at, resolved_at, '
  + 'acknowledged_at, acknowledged_by FROM alert_events WHERE id = ?').get(id) || null;

/** Literal column values, for asserting that a call changed NOTHING. */
function rawSnapshot() {
  const out = {};
  for (const r of ROWS) {
    const row = readRow(ids[r.key]);
    out[r.key] = [row.resolved_at, row.acknowledged_at, row.acknowledged_by];
  }
  return out;
}

/** The label a column starts with, before any call has run. */
function seedLabel(v, what) {
  if (v === null || v === undefined) return null;
  if (SEEDED.has(v)) return 'seeded';
  throw new Error(what + ': the seed left ' + v + ' there, which is neither NULL nor one of '
    + 'the seeded constants — it cannot be labelled, and guessing would record a fiction '
    + 'as an answer');
}

for (const r of ROWS) {
  const row = readRow(ids[r.key]);
  lastRaw[r.key] = { resolvedAt: row.resolved_at, acknowledgedAt: row.acknowledged_at };
  labels[r.key] = {
    resolvedAt: seedLabel(row.resolved_at, r.key + '.resolved_at'),
    acknowledgedAt: seedLabel(row.acknowledged_at, r.key + '.acknowledged_at'),
  };
}

/**
 * Advance the labels by one call and return them.
 *
 * A column that CHANGED since the last snapshot is 'fresh'; one that did not
 * keeps what it had, except that last call's 'fresh' becomes 'earlier'.
 */
function snapshot() {
  const out = {};
  for (const r of ROWS) {
    const row = readRow(ids[r.key]);
    const lab = labels[r.key];
    const prev = lastRaw[r.key];
    for (const [col, v] of [['resolvedAt', row.resolved_at], ['acknowledgedAt', row.acknowledged_at]]) {
      if (v !== prev[col]) {
        lab[col] = v === null ? null : 'fresh';
      } else if (lab[col] === 'fresh') {
        lab[col] = 'earlier';
      }
      prev[col] = v;
    }
    out[r.key] = {
      resolvedAt: lab.resolvedAt,
      acknowledgedAt: lab.acknowledgedAt,
      acknowledgedBy: row.acknowledged_by,
    };
  }
  return out;
}

/** Run one call, recording what it returned and what it left behind. */
function run(name, fn) {
  const result = fn();
  cases[name] = { result, state: snapshot() };
  return cases[name];
}

const cases = {};

/** Run one call, recording what it returned and what it left behind. */
function run(name, fn) {
  const before = Date.now();
  const result = fn();
  const after = Date.now();
  cases[name] = { result, state: snapshot(before, after) };
  return cases[name];
}

// ---- getAlertRouterId ----------------------------------------------------
cases.scopeKnown = { result: db.getAlertRouterId(ids['open-unacked']) };
cases.scopeOtherRouter = { result: db.getAlertRouterId(ids['other-open']) };
cases.scopeUnknown = { result: db.getAlertRouterId(UNKNOWN_ID) };
// A non-integer id. The route parses with parseInt first, but the store is the
// last line and must not throw on one.
cases.scopeZero = { result: db.getAlertRouterId(0) };

// ---- acknowledgeAlert ----------------------------------------------------
// An OPEN, unacknowledged row: the ordinary case.
run('ackOpen', () => {
  const row = db.acknowledgeAlert(ids['open-unacked'], 'alice');
  return row ? { id: row.id, routerId: row.router_id, alertType: row.alert_type } : null;
});
// A CLOSED row. Acknowledging after recovery is allowed and is rule 2.
run('ackClosed', () => {
  const row = db.acknowledgeAlert(ids['closed-unacked'], 'bob');
  return row ? { id: row.id, routerId: row.router_id, alertType: row.alert_type } : null;
});
// ALREADY acknowledged: rule 1. The row comes back, the name does NOT change.
run('ackAlreadyAcked', () => {
  const row = db.acknowledgeAlert(ids['open-acked'], 'carol');
  return row ? { id: row.id, routerId: row.router_id, alertType: row.alert_type } : null;
});
// An unknown id returns null rather than throwing — the route turns that into a 404.
run('ackUnknown', () => {
  const row = db.acknowledgeAlert(UNKNOWN_ID, 'dave');
  return row ? { id: row.id } : null;
});
// `username || null`: an empty string is stored as NULL, not as "".
run('ackEmptyUser', () => {
  const row = db.acknowledgeAlert(ids['open-second-ack'], '');
  return row ? { id: row.id, acknowledgedBy: row.acknowledged_by } : null;
});

// ---- resolveAllAlerts ----------------------------------------------------
// What is still open on R at this point: only `other-open` is elsewhere, and
// `open-acked` is open but already acknowledged — rule 3's "keep their name".
const clearedIds = db.resolveAllAlerts(R, 'eve');
cases.clearAll = {
  result: clearedIds.map((id) => keyOf(id)).sort(),
  state: snapshot(),
};
const afterClearRaw = rawSnapshot();
// A SECOND clear finds nothing open and returns an empty array — and must not
// re-stamp anything, which the state records.
run('clearAllAgain', () => db.resolveAllAlerts(R, 'frank').map(keyOf));
// A router with no rows at all.
run('clearUnknownRouter', () => db.resolveAllAlerts('nope', 'grace').map(keyOf));

function keyOf(id) {
  for (const k of Object.keys(ids)) if (ids[k] === Number(id)) return k;
  throw new Error('resolveAllAlerts returned id ' + id + ', which the seed never minted');
}

// ---- BELIEVABILITY -------------------------------------------------------
{
  // The scope lookup actually discriminates.
  assert.equal(cases.scopeKnown.result, R, 'the scope lookup lost the router');
  assert.equal(cases.scopeOtherRouter.result, OTHER,
    'the scope lookup gave the same answer for two different routers, so it proves nothing');
  assert.equal(cases.scopeUnknown.result, null, 'an unknown alert id resolved to a router');

  // Rule 1: the second acknowledgement did NOT take.
  assert.ok(cases.ackAlreadyAcked.result, 'an already-acknowledged row returned null');
  assert.equal(cases.ackAlreadyAcked.state['open-acked'].acknowledgedBy, SEEDED_ACK_BY,
    'a second acknowledgement overwrote the first — `WHERE acknowledged_at IS NULL` is '
    + 'what stops credit moving to whoever clicked last');
  assert.equal(cases.ackAlreadyAcked.state['open-acked'].acknowledgedAt, 'seeded',
    'the timestamp moved on a row that was already acknowledged');

  // Rule 2: a CLOSED row acknowledges.
  assert.equal(cases.ackClosed.state['closed-unacked'].acknowledgedAt, 'fresh',
    'acknowledging a resolved alert did nothing — the panel shows those under '
    + '"Recently resolved" and the button would silently fail on them');
  assert.equal(cases.ackClosed.state['closed-unacked'].acknowledgedBy, 'bob');

  // And the ordinary case wrote what it should, without resolving anything.
  assert.equal(cases.ackOpen.state['open-unacked'].acknowledgedAt, 'fresh');
  assert.equal(cases.ackOpen.state['open-unacked'].acknowledgedBy, 'alice');
  assert.equal(cases.ackOpen.state['open-unacked'].resolvedAt, null,
    'acknowledging RESOLVED the alert — the two are different acts and the Routers '
    + 'page counts only the second');

  assert.equal(cases.ackUnknown.result, null, 'an unknown id returned a row');
  assert.equal(cases.ackEmptyUser.result.acknowledgedBy, null,
    '`username || null` stored an empty string rather than NULL');

  // Rule 3: clear-all resolved AND acknowledged, and kept the existing name.
  assert.deepEqual(cases.clearAll.result,
    ['open-acked', 'open-second-ack', 'open-unacked', 'open-untouched'],
    'clear-all returned the wrong set of ids');
  assert.ok(!cases.clearAll.result.includes('other-open'),
    'clear-all reached another router — one button would clear the whole fleet');
  assert.equal(cases.clearAll.state['other-open'].resolvedAt, null,
    'another router\'s alert was resolved');
  assert.equal(cases.clearAll.state['open-unacked'].resolvedAt, 'fresh');
  assert.equal(cases.clearAll.state['open-unacked'].acknowledgedBy, 'alice',
    'clear-all reassigned a row acknowledged moments earlier');
  assert.equal(cases.clearAll.state['open-unacked'].acknowledgedAt, 'earlier',
    'clear-all re-stamped a row `ackOpen` had already acknowledged');
  assert.equal(cases.clearAll.state['open-acked'].acknowledgedBy, SEEDED_ACK_BY,
    'clear-all took credit for a row somebody else had already acknowledged — in '
    + 'Reports that is indistinguishable from the evaluator resolving it alone');
  assert.equal(cases.clearAll.state['open-second-ack'].acknowledgedBy, null,
    'the row acknowledged with an empty username picked up a name');
  // Its `acknowledged_by` is NULL and its `acknowledged_at` is NOT — set by
  // `ackEmptyUser` a few calls earlier. That combination is the one that catches
  // a port testing the wrong column: `WHERE acknowledged_at IS NULL` skips this
  // row and the NULL name survives, where `WHERE acknowledged_by IS NULL` would
  // match it and hand the row to whoever cleared the list.
  assert.equal(cases.clearAll.state['open-second-ack'].acknowledgedAt, 'earlier',
    'clear-all re-stamped a row that was already acknowledged (with a NULL name)');

  // The row nothing had touched: clear-all is what acknowledged it, in HER name.
  assert.equal(cases.clearAll.state['open-untouched'].acknowledgedBy, 'eve',
    'clear-all resolved a row without acknowledging it — the acknowledge half can be '
    + 'deleted outright and every other row hides it, because they were all acknowledged '
    + 'already');
  assert.equal(cases.clearAll.state['open-untouched'].acknowledgedAt, 'fresh');
  assert.equal(cases.clearAll.state['open-untouched'].resolvedAt, 'fresh');

  // ---- ONE INSTANT, AND WHERE IT ACTUALLY SHOWS -------------------------
  //
  // NOT "two rows would sort apart". A single UPDATE evaluates its parameter
  // once, so every row one statement touches gets the same number whether the
  // caller hoists the clock or inlines it — a mutation inlining it survived,
  // which is how that reasoning was found to be wrong.
  //
  // What the hoist buys is agreement BETWEEN the two statements: a row clear-all
  // both acknowledged and resolved carries the SAME instant in both columns.
  // Inline the clock and they differ by however long the first UPDATE took.
  {
    const r = handle.prepare(
      'SELECT resolved_at, acknowledged_at FROM alert_events WHERE id = ?')
      .get(ids['open-untouched']);
    assert.equal(r.acknowledged_at, r.resolved_at,
      'clear-all acknowledged at ' + r.acknowledged_at + ' and resolved at ' + r.resolved_at
      + ' — one `Date.now()` shared by both statements is what keeps them equal');
  }

  // Rule 4: ONE instant across the batch, not one per row.
  const resolvedAts = ['open-unacked', 'open-acked', 'open-second-ack', 'open-untouched']
    .map((k) => handle.prepare('SELECT resolved_at FROM alert_events WHERE id = ?')
      .get(ids[k]).resolved_at);
  assert.equal(new Set(resolvedAts).size, 1,
    'clear-all stamped ' + new Set(resolvedAts).size + ' distinct instants across '
    + resolvedAts.length + ' rows — one `Date.now()` is what keeps the batch from '
    + 'sorting apart');

  // The second clear found nothing and changed nothing.
  assert.deepEqual(cases.clearAllAgain.result, [], 'a second clear-all found open rows');
  assert.deepEqual(rawSnapshot(), afterClearRaw,
    'a clear-all that returned nothing still wrote to the table');
  assert.deepEqual(cases.clearUnknownRouter.result, []);

  // Nothing was DELETED — Reports still shows what happened.
  const total = handle.prepare('SELECT COUNT(*) AS n FROM alert_events').get().n;
  assert.equal(total, ROWS.length,
    'clear-all deleted rows: ' + total + ' of ' + ROWS.length + ' remain. Deleting is a '
    + 'separate deliberate act and lives in Settings -> Database');
}

const out = { seededAckAt: SEEDED_ACK_AT, seededAckBy: SEEDED_ACK_BY, seed: ROWS, cases };
const json = JSON.stringify(out, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('alertwrite-cases.json is STALE — regenerate it in the container');
    process.exit(1);
  }
  console.log('alertwrite-cases.json is current (' + Object.keys(cases).length + ' cases)');
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, json);
  console.log('wrote ' + OUT + ' (' + Object.keys(cases).length + ' cases)');
}
