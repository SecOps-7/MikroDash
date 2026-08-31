'use strict';
/**
 * The purge PREDICATE and the purge OPTIONS, lifted from the live app and run.
 *
 * ---- WHY THIS ONE GETS EXTRA CARE -----------------------------------------
 *
 * Every other corpus in this repo pins something that RENDERS or REPORTS. This
 * one pins the WHERE clause of a DELETE across five tables of history. A port
 * that builds it slightly differently does not show a wrong number — it removes
 * rows nobody asked it to remove, and there is no undo.
 *
 * So the corpus is built around the two boundaries where that goes wrong:
 *
 *   AN EMPTY PREDICATE. `{}` produces no WHERE clause at all, which deletes
 *     EVERYTHING in the target tables. That is the live behaviour and it is what
 *     "purge all history for all routers" means — but it also means a port that
 *     dropped a condition silently widens to it. Both the empty case and every
 *     narrowing case are here, so the difference is visible.
 *
 *   olderThanMs === 0. `if (opts.olderThanMs > 0)` — zero adds NO age condition,
 *     so "0 days" means EVERYTHING regardless of age rather than "nothing older
 *     than now". A port using `>= 0` would keep the rows it was told to remove.
 *
 * ---- THE TWO HALVES ARE PINNED SEPARATELY ---------------------------------
 *
 * `_purgeWhere` and `_purgeTargets` build the SQL; `_purgeScope` and
 * `_purgeOpts` decide whether the request is allowed and what it means. They
 * live in different files (`src/db.js` and `src/index.js`) and fail differently:
 * the first deletes the wrong rows, the second lets the wrong person delete.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/purge-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/purge-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'purge-cases.json');

/** Lift a top-level function or const by its declaration, to the matching brace. */
function sliceFrom(src, decl) {
  const lines = src.split('\n');
  const at = lines.findIndex((l) => l.startsWith(decl));
  if (at < 0) throw new Error(`anchor lost: ${decl}`);
  let depth = 0;
  for (let i = at; i < lines.length; i++) {
    for (const ch of lines[i]) {
      if (ch === '{' || ch === '[') depth++;
      else if (ch === '}' || ch === ']') depth--;
    }
    if (depth === 0 && /[}\];]$/.test(lines[i].trim())) return lines.slice(at, i + 1).join('\n');
  }
  throw new Error(`could not find the end of ${decl}`);
}

const dbjs = fs.readFileSync(path.join(LIVE, 'src', 'db.js'), 'utf8');
const indexjs = fs.readFileSync(path.join(LIVE, 'src', 'index.js'), 'utf8');

const TABLES = sliceFrom(dbjs, 'const PURGE_TABLES = {');
const WHERE = sliceFrom(dbjs, 'function _purgeWhere(');
const TARGETS = sliceFrom(dbjs, 'function _purgeTargets(');
const AGES = sliceFrom(indexjs, 'const _PURGE_AGES = ');
const SCOPE = sliceFrom(indexjs, 'function _purgeScope(');
const OPTS = sliceFrom(indexjs, 'function _purgeOpts(');

// The slices must still carry the two rules this file exists for.
if (!/opts\.olderThanMs > 0/.test(WHERE)) {
  throw new Error('_purgeWhere no longer guards the age on `> 0`; the "0 means everything" rule '
    + 'this corpus pins has changed');
}
if (!/where\.length \?/.test(WHERE)) {
  throw new Error('_purgeWhere no longer returns an EMPTY clause for no conditions — that is the '
    + 'delete-everything case and the most dangerous one to get wrong');
}

// ---- The SQL half ----------------------------------------------------------
//
// `Date.now()` is FROZEN, or the recorded params would differ on every run and
// the corpus could not be compared at all.
const NOW = 1700000000000;
const sqlCtx = { Date: { now: () => NOW }, Array, Object };
const sql = vm.runInNewContext(
  `${TABLES}\nconst PURGE_TYPES = Object.keys(PURGE_TABLES);\n${WHERE}\n${TARGETS}\n`
  + '({ _purgeWhere, _purgeTargets, PURGE_TABLES, PURGE_TYPES });',
  sqlCtx, { filename: 'db.js#purge' });

const WHERE_INPUTS = [
  // THE EMPTY PREDICATE — deletes everything in the target tables.
  ['no options at all', {}, 'ts'],
  ['an empty router id is not a condition', { routerId: '' }, 'ts'],
  ['a zero age is not a condition', { olderThanMs: 0 }, 'ts'],
  ['both absent', { routerId: '', olderThanMs: 0 }, 'ts'],
  // ONE CONDITION.
  ['a router only', { routerId: 'rtr-1' }, 'ts'],
  ['an age only', { olderThanMs: 86400000 }, 'ts'],
  // BOTH.
  ['a router and an age', { routerId: 'rtr-1', olderThanMs: 7 * 86400000 }, 'ts'],
  // THE OTHER TIMESTAMP COLUMN. `alert_events` uses `fired_at`, and a port that
  // hard-coded `ts` would filter that table on a column it does not have.
  ['the fired_at column', { olderThanMs: 86400000 }, 'fired_at'],
  ['a router and an age on fired_at', { routerId: 'rtr-1', olderThanMs: 86400000 }, 'fired_at'],
  // A NEGATIVE age is not a condition either — the guard is `> 0`.
  ['a negative age', { olderThanMs: -1 }, 'ts'],
];

const whereCases = WHERE_INPUTS.map(([why, opts, tsCol]) => {
  const w = sql._purgeWhere(opts, tsCol);
  return { why, opts, tsCol, sql: w.sql, params: w.params };
});

const TARGET_INPUTS = [
  ['no types means every type', undefined],
  ['an empty list means every type', []],
  ['one type', ['ping']],
  ['two types', ['ping', 'traffic']],
  // `events` maps to TWO tables with DIFFERENT timestamp columns, which is the
  // case a port modelling one table per type would get wrong.
  ['events is two tables', ['events']],
  ['an unknown type is dropped', ['nosuchtype']],
  ['an unknown type among good ones', ['ping', 'nosuchtype', 'bandwidth']],
  // NOT AN ARRAY falls back to every type, because the guard is `Array.isArray`.
  ['a string is not a list, so every type', 'ping'],
  ['null means every type', null],
  ['every type named explicitly', ['ping', 'traffic', 'bandwidth', 'events']],
];

const targetCases = TARGET_INPUTS.map(([why, types]) => ({
  why,
  types: types === undefined ? null : types,
  typesWasUndefined: types === undefined,
  targets: sql._purgeTargets(types),
}));

// ---- The options half ------------------------------------------------------
//
// `_purgeOpts` calls `_purgeScope`, which asks RBAC. Both permissions are
// supplied per case, so the combinations are recorded rather than reasoned about.
function runOpts(body, opts) {
  const o = opts || {};
  const ctx = {
    Array, Object, Number, String,
    db: { PURGE_TYPES: sql.PURGE_TYPES },
    _isModern: () => o.modern !== false,
    Rbac: {
      can: (_session, perm) => (perm === 'system:db' ? !!o.systemDb : !!o.routerPurge),
    },
  };
  const fns = vm.runInNewContext(
    `${AGES}\n${SCOPE}\n${OPTS}\n({ _purgeOpts });`,
    ctx, { filename: 'index.js#purgeOpts' });
  return fns._purgeOpts({ body, authSession: { userId: 'u-1' } });
}

const OPT_INPUTS = [
  // THE AGE PRESETS. Anything not in the list is refused, which is what stops a
  // caller inventing an age the UI never offers.
  ['no age at all', { routerId: 'rtr-1' }, { routerPurge: true }],
  ['age 0 — everything', { routerId: 'rtr-1', olderThanDays: 0 }, { routerPurge: true }],
  ['age 1', { routerId: 'rtr-1', olderThanDays: 1 }, { routerPurge: true }],
  ['age 7', { routerId: 'rtr-1', olderThanDays: 7 }, { routerPurge: true }],
  ['age 365', { routerId: 'rtr-1', olderThanDays: 365 }, { routerPurge: true }],
  ['age 2 is not a preset', { routerId: 'rtr-1', olderThanDays: 2 }, { routerPurge: true }],
  ['age 400 is not a preset', { routerId: 'rtr-1', olderThanDays: 400 }, { routerPurge: true }],
  ['a negative age', { routerId: 'rtr-1', olderThanDays: -1 }, { routerPurge: true }],
  ['an age as a numeric string', { routerId: 'rtr-1', olderThanDays: '7' }, { routerPurge: true }],
  ['an age that is not a number', { routerId: 'rtr-1', olderThanDays: 'week' }, { routerPurge: true }],
  // THE TYPES.
  ['no types key means all', { routerId: 'rtr-1', olderThanDays: 0 }, { routerPurge: true }],
  ['one valid type', { routerId: 'rtr-1', olderThanDays: 0, types: ['ping'] }, { routerPurge: true }],
  ['an empty list is REFUSED', { routerId: 'rtr-1', olderThanDays: 0, types: [] }, { routerPurge: true }],
  ['a list of only invalid types is refused',
    { routerId: 'rtr-1', olderThanDays: 0, types: ['nope'] }, { routerPurge: true }],
  ['invalid types are filtered out of a good list',
    { routerId: 'rtr-1', olderThanDays: 0, types: ['ping', 'nope'] }, { routerPurge: true }],
  ['types as a string is not a list, so all',
    { routerId: 'rtr-1', olderThanDays: 0, types: 'ping' }, { routerPurge: true }],
  // THE SCOPE. A GLOBAL purge needs `system:db`; a router purge needs
  // `router:purge` on that router.
  ['a global purge with system:db', { olderThanDays: 0 }, { systemDb: true }],
  ['a global purge WITHOUT system:db', { olderThanDays: 0 }, { systemDb: false }],
  ['a router purge with permission', { routerId: 'rtr-1', olderThanDays: 0 }, { routerPurge: true }],
  ['a router purge WITHOUT permission', { routerId: 'rtr-1', olderThanDays: 0 }, { routerPurge: false }],
  ['a whitespace router id reads as global',
    { routerId: '   ', olderThanDays: 0 }, { systemDb: true }],
  // AUTH MODE none: no RBAC at all, so any scope is allowed.
  ['auth mode none allows a global purge', { olderThanDays: 0 }, { modern: false }],
  ['auth mode none allows a router purge',
    { routerId: 'rtr-1', olderThanDays: 0 }, { modern: false }],
  // ORDER: the SCOPE is checked before the age, so a request that is wrong in
  // both reports the scope.
  ['a bad scope AND a bad age reports the scope', { olderThanDays: 999 }, { systemDb: false }],
];

const optCases = OPT_INPUTS.map(([why, body, o]) => {
  const r = runOpts(body, o);
  return {
    why, body, perms: o,
    error: r.error || null,
    routerId: r.error ? null : r.routerId,
    types: r.error ? null : r.types,
    olderThanMs: r.error ? null : r.olderThanMs,
  };
});

// ---- Believability ---------------------------------------------------------
const byWhy = (list) => Object.fromEntries(list.map((c) => [c.why, c]));
const W = byWhy(whereCases), T = byWhy(targetCases), O = byWhy(optCases);
const need = (m, k) => {
  if (!m[k]) throw new Error(`a believability check names a case that is gone: ${k}`);
  return m[k];
};

// THE EMPTY PREDICATE really is empty, and the narrowing ones really narrow.
if (need(W, 'no options at all').sql !== '') {
  throw new Error('an empty options object produced a WHERE clause; the delete-everything case is '
    + 'not being exercised');
}
if (!need(W, 'a router only').sql.includes('router_id = ?')) {
  throw new Error('a router id did not produce a condition');
}
if (!need(W, 'an age only').sql.includes('ts < ?')) {
  throw new Error('an age did not produce a condition');
}
// ZERO IS NOT A CONDITION. This is the rule that decides whether "0 days" means
// everything or nothing.
if (need(W, 'a zero age is not a condition').sql !== '') {
  throw new Error('a zero age produced a condition — "0 days" must mean EVERYTHING, and a port '
    + 'that used >= 0 would keep the rows it was asked to delete');
}
if (need(W, 'a negative age').sql !== '') {
  throw new Error('a negative age produced a condition');
}
// THE PARAMS carry the frozen clock, so a port computing the cutoff differently
// is visible.
{
  const c = need(W, 'an age only');
  if (c.params.length !== 1 || c.params[0] !== NOW - 86400000) {
    throw new Error(`the age param is ${JSON.stringify(c.params)}, expected the frozen cutoff`);
  }
}
// THE COLUMN is used, not assumed.
if (!need(W, 'the fired_at column').sql.includes('fired_at < ?')) {
  throw new Error('the ts column argument is being ignored');
}

// `events` IS TWO TABLES with different columns.
{
  const c = need(T, 'events is two tables');
  if (c.targets.length !== 2) {
    throw new Error(`events mapped to ${c.targets.length} table(s); a port modelling one table per `
      + 'type would miss one');
  }
  const cols = c.targets.map((t) => t.ts).sort();
  if (cols[0] === cols[1]) {
    throw new Error('the two event tables now share a timestamp column, so this case no longer '
      + 'separates them');
  }
}
// NO TYPES means EVERY table, which is more than any single type.
if (need(T, 'no types means every type').targets.length
    <= need(T, 'one type').targets.length) {
  throw new Error('an absent type list did not widen to every table');
}
if (need(T, 'an unknown type is dropped').targets.length !== 0) {
  throw new Error('an unknown type produced targets');
}

// THE OPTIONS half: something is refused, something is allowed.
if (!optCases.some((c) => c.error)) throw new Error('no option case is refused');
if (!optCases.some((c) => !c.error)) throw new Error('every option case is refused');
// THE PRESETS.
if (need(O, 'age 2 is not a preset').error !== 'Invalid age filter') {
  throw new Error('a non-preset age was accepted');
}
if (need(O, 'no age at all').error !== 'Invalid age filter') {
  throw new Error('an ABSENT age was accepted — `Number(undefined)` is NaN and NaN is not in the '
    + 'preset list, so the live code refuses it');
}
// AN EMPTY TYPE LIST is refused, where an ABSENT one means all.
if (need(O, 'an empty list is REFUSED').error !== 'No valid data types selected') {
  throw new Error('an empty type list was accepted');
}
if (need(O, 'no types key means all').error) {
  throw new Error('an absent type list was refused');
}
// THE SCOPE permissions, both directions.
if (need(O, 'a global purge WITHOUT system:db').error
    !== 'Select a router — your account cannot purge all routers') {
  throw new Error('a global purge was allowed without system:db');
}
if (need(O, 'a global purge with system:db').error) {
  throw new Error('a global purge was refused WITH system:db');
}
if (need(O, 'a router purge WITHOUT permission').error !== 'Router not permitted') {
  throw new Error('a router purge was allowed without router:purge');
}
// ORDER.
if (need(O, 'a bad scope AND a bad age reports the scope').error
    !== 'Select a router — your account cannot purge all routers') {
  throw new Error('the age was checked before the scope');
}

const json = JSON.stringify({
  generated_from: 'src/db.js _purgeWhere/_purgeTargets + src/index.js _purgeScope/_purgeOpts',
  now: NOW,
  where: whereCases,
  targets: targetCases,
  options: optCases,
}, null, 2) + '\n';

if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('STALE: testdata/purge-cases.json - re-run tools/purge-cases.js');
    process.exit(1);
  }
  console.log(`purge-cases: up to date (${whereCases.length} predicates, ${targetCases.length} `
    + `target sets, ${optCases.length} option sets)`);
} else {
  fs.writeFileSync(OUT, json);
  console.log(`wrote ${OUT} (${whereCases.length} predicates, ${targetCases.length} target sets, `
    + `${optCases.length} option sets — ${optCases.filter((c) => c.error).length} refused)`);
}
