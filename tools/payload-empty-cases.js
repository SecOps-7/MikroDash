'use strict';
/**
 * `payloadEmpty`, run from `src/collectors/util.js`, and the DORMANCY ELIGIBLE
 * set, filtered from the live registry.
 *
 * ---- WHY THESE TWO TOGETHER -----------------------------------------------
 *
 * They are the two halves of one question the dormancy supervisor asks each
 * tick: WHICH collectors may be judged, and IS this one's payload empty. Both
 * are declared in the live registry rather than in the supervisor —
 * "`emptyKey` is what makes a collector eligible for dormancy" — so a port that
 * derived either by hand would be transcribing a table.
 *
 * ---- THE MIDDLE OUTCOME IS THE POINT --------------------------------------
 *
 * `payloadEmpty` has THREE outcomes, not two, and the middle one is the arm a
 * port collapses. A key that is MISSING, or that holds something which is not an
 * array, is not emptiness — it is the absence of an answer, and `readable` stays
 * false so the collector is left alone. Only a key that IS a list and IS empty
 * counts, and with several keys every readable one must be empty.
 *
 * Collapsing that puts a collector to sleep the first time its payload arrives
 * in a shape nobody expected.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/payload-empty-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/payload-empty-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'payload-empty-cases.json');

const { payloadEmpty } = require(path.join(LIVE, 'src', 'collectors', 'util.js'));
const { COLLECTORS } = require(path.join(LIVE, 'src', 'collection.js'));

const CASES = [
  ['a single list that is empty', { hosts: [] }, 'hosts'],
  ['a single list with a row', { hosts: [{ a: 1 }] }, 'hosts'],
  // THE MIDDLE OUTCOME: nothing readable, so not empty.
  ['the key is missing entirely', { other: [] }, 'hosts'],
  ['the key holds null', { hosts: null }, 'hosts'],
  ['the key holds an object rather than a list', { hosts: { a: 1 } }, 'hosts'],
  ['the key holds a number', { hosts: 0 }, 'hosts'],
  ['a null payload', null, 'hosts'],
  ['an empty payload object', {}, 'hosts'],
  // NO emptyKey at all — the guard that makes an ineligible collector safe.
  ['no emptyKey', { hosts: [] }, null],
  ['an empty emptyKey array', { hosts: [] }, []],
  // SEVERAL KEYS. Every readable one must be empty.
  ['two lists, both empty', { networks: [], clients: [] }, ['networks', 'clients']],
  ['two lists, one with a row', { networks: [], clients: [{ a: 1 }] }, ['networks', 'clients']],
  ['two keys, only one readable and it is empty', { networks: [], clients: null },
    ['networks', 'clients']],
  ['two keys, only one readable and it has a row', { networks: [{ a: 1 }], clients: null },
    ['networks', 'clients']],
  ['two keys, neither readable', { networks: null, clients: 'x' }, ['networks', 'clients']],
];

const cases = CASES.map(([why, payload, emptyKey]) => ({
  why, payload, emptyKey, empty: payloadEmpty(payload, emptyKey),
}));

// The live filter, verbatim.
const eligible = COLLECTORS.filter((c) => c.emptyKey && c.disableable).map((c) => c.key);

// ---- Believability ---------------------------------------------------------
const byWhy = Object.fromEntries(cases.map((c) => [c.why, c]));
const need = (k) => {
  if (!byWhy[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return byWhy[k];
};

// A corpus that is all-false agrees with a function that always returns false —
// which is the safest wrong answer and therefore the easiest to ship.
if (!cases.some((c) => c.empty === true)) throw new Error('no case is empty');
if (!cases.some((c) => c.empty === false)) throw new Error('no case is non-empty');

// THE THREE OUTCOMES, named. The middle group must all be false FOR THE SECOND
// REASON — nothing readable — not because a list had rows.
if (need('a single list that is empty').empty !== true) throw new Error('an empty list is not empty');
if (need('a single list with a row').empty !== false) throw new Error('a populated list is empty');
for (const why of ['the key is missing entirely', 'the key holds null',
  'the key holds an object rather than a list', 'the key holds a number']) {
  if (need(why).empty !== false) {
    throw new Error(`${why}: reported EMPTY. An unreadable key is the absence of an answer, `
      + 'not emptiness — a port that collapses this sleeps a collector on a surprising payload');
  }
}
// THE MULTI-KEY RULE, both ways.
if (need('two lists, both empty').empty !== true) throw new Error('two empty lists are not empty');
if (need('two lists, one with a row').empty !== false) throw new Error('one row did not save it');
if (need('two keys, only one readable and it is empty').empty !== true) {
  throw new Error('an unreadable sibling should not prevent the verdict');
}
if (need('two keys, neither readable').empty !== false) throw new Error('nothing readable is not empty');

// THE ELIGIBLE SET. Non-trivial in both directions, or the filter is not a filter.
if (eligible.length < 10) throw new Error(`only ${eligible.length} collectors are eligible`);
if (eligible.length === COLLECTORS.length) {
  throw new Error('EVERY collector is eligible, so the filter selects nothing — either the '
    + 'registry changed or `emptyKey && disableable` is no longer discriminating');
}
// And a collector that is excluded for EACH reason, so both halves are exercised.
{
  const noEmptyKey = COLLECTORS.filter((c) => !c.emptyKey && c.disableable).map((c) => c.key);
  const notDisableable = COLLECTORS.filter((c) => c.emptyKey && !c.disableable).map((c) => c.key);
  if (!noEmptyKey.length) {
    throw new Error('no collector is excluded for having no emptyKey, so dropping that half of '
      + 'the filter would not change the set');
  }
  // ── A LIMIT OF THIS CORPUS, MEASURED RATHER THAN ASSUMED ────────────────
  //
  // NOTHING in today's registry has an `emptyKey` and is NOT disableable. So the
  // `&& c.disableable` half of the live filter selects nothing extra right now,
  // and a port that dropped it would produce the identical set — this corpus
  // CANNOT tell the two apart, and the Go test says so rather than claiming a
  // coverage it does not have.
  //
  // Recorded exactly like `tools/collection-cases.js`'s note that the dependency
  // graph is one level deep, and for the same reason: the gap is a property of
  // today's data, not of the code, and one registry line would close it.
  //
  // ASSERTED SO IT CANNOT SILENTLY CHANGE. If a non-disableable collector ever
  // gains an emptyKey, this throws and the note above has to be deleted rather
  // than left lying.
  if (notDisableable.length) {
    throw new Error(`${notDisableable.join(', ')} now have an emptyKey and are NOT disableable. `
      + 'That is new: this corpus recorded that no such collector existed, and the Go test '
      + 'carries the same note. Add a case and delete both notes.');
  }
}

const json = JSON.stringify(
  { generated_from: 'src/collectors/util.js payloadEmpty + src/collection.js COLLECTORS',
    // Recorded so the Go side can assert the same limit rather than assume it.
    disableableIsRedundantToday: true,
    eligible, cases }, null, 2) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/payload-empty-cases.json - re-run tools/payload-empty-cases.js');
    process.exit(1);
  }
  console.log(`payload-empty-cases: up to date (${cases.length} cases, ${eligible.length} eligible)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${cases.length} cases, ${eligible.length} eligible)`);
}
