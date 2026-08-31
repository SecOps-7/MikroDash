'use strict';
/**
 * `alertSessions.syncSessions` — the pool's LIFECYCLE rule, pinned by driving
 * the live function rather than by reading it.
 *
 * ---- WHY A LIFT AND NOT A REQUIRE ------------------------------------------
 *
 * `src/alertSessions.js` pulls in a ROS client, the alerter, Settings, db-writer
 * and six collectors at module load. `syncSessions` itself touches exactly three
 * things — the `_sessions` map, `_stopSession` and `_buildSession` — so it is
 * lifted by content anchor and driven with those three faked. NOTHING in the
 * rule is stubbed: the exclusions, the flag-change comparison and both loops are
 * the live code.
 *
 * The fake `_buildSession` returns `{alertsEnabled}`, which is the only field
 * the rule reads back (`alertSessions.js:128` builds
 * `{ros, collectors, evaluator, alertsEnabled, destroyed}`; line 35 compares
 * `session.alertsEnabled !== !!router.alertsEnabled`).
 *
 *   MIKRODASH_SRC=../MikroDash node tools/alertpool-sync-cases.js
 *   MIKRODASH_SRC=../MikroDash node tools/alertpool-sync-cases.js --check
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const OUT = path.join(ROOT, 'testdata', 'alertpool-sync-cases.json');

const src = fs.readFileSync(path.join(LIVE, 'src', 'alertSessions.js'), 'utf8');
const decl = 'function syncSessions(allRouters, activeRouterId, excludeIds) {';
const start = src.indexOf(decl);
if (start === -1) throw new Error('cannot find syncSessions — alertSessions.js has been rewritten');
const end = src.indexOf('\n}', start);
if (end === -1) throw new Error('syncSessions is never closed');
const body = src.slice(start, end + 2);

// BELIEVABILITY. A slice that lost an exclusion would record "everything gets a
// session" as the live rule, which is the exact failure this guards.
for (const marker of ['id === activeRouterId', 'excludeIds && excludeIds.has(id)',
  'session.alertsEnabled !== !!router.alertsEnabled', '_stopSession', '_buildSession']) {
  assert.ok(body.includes(marker),
    'the lifted syncSessions has no ' + marker + ' — the slice is wrong, or a rule stopped ' +
    'being applied, which would silently change which routers this pool watches');
}

function run(live, all, activeId, excluded) {
  const built = [], stopped = [];
  const ctx = {
    Map, Set, console,
    _sessions: new Map(Object.entries(live).map(([id, ae]) => [id, { alertsEnabled: ae }])),
    _stopSession: (id) => stopped.push(id),
    _buildSession: (r) => { built.push(r.id); return { alertsEnabled: !!r.alertsEnabled }; },
  };
  vm.createContext(ctx);
  vm.runInContext(body + '\nmodule = { exports: syncSessions };', ctx);
  ctx.module.exports(all, activeId, excluded ? new Set(excluded) : undefined);
  return {
    built: built.sort(),
    stopped: stopped.sort(),
    after: [...ctx._sessions.keys()].sort(),
  };
}

const R = (id, extra = {}) => ({ id, label: id, host: '198.51.100.1', port: 8728,
  username: 'u', password: '', alertsEnabled: false, disabled: false, ...extra });

// NOTE: the live function does NOT filter disabled routers — its callers pass an
// already-filtered list (`Routers.loadAll().filter(r => !r.disabled)`). The
// corpus therefore never sends a disabled router, and the port's planner is
// tested for that filter separately. Recording the boundary rather than
// pretending the two sides split the work the same way.
const CASES = {
  emptyFleet:        { live: {}, all: [], active: '' },
  oneRouterNoActive: { live: {}, all: [R('a')], active: '' },
  activeIsExcluded:  { live: {}, all: [R('a'), R('b')], active: 'a' },
  poolOwnedExcluded: { live: {}, all: [R('a'), R('b')], active: '', excluded: ['b'] },
  bothExclusions:    { live: {}, all: [R('a'), R('b'), R('c')], active: 'a', excluded: ['b'] },
  alreadyRunning:    { live: { a: false }, all: [R('a')], active: '' },
  routerRemoved:     { live: { a: false, b: false }, all: [R('a')], active: '' },
  becameActive:      { live: { a: false, b: false }, all: [R('a'), R('b')], active: 'b' },
  becamePoolOwned:   { live: { a: false, b: false }, all: [R('a'), R('b')], active: '', excluded: ['b'] },
  // The flag change: dropped AND rebuilt in one call.
  alertsTurnedOn:    { live: { a: false }, all: [R('a', { alertsEnabled: true })], active: '' },
  alertsTurnedOff:   { live: { a: true }, all: [R('a', { alertsEnabled: false })], active: '' },
  alertsUnchangedOn: { live: { a: true }, all: [R('a', { alertsEnabled: true })], active: '' },
  // An empty activeId must not exclude anything.
  emptyActiveId:     { live: {}, all: [R('a'), R('b')], active: '' },
  mixed:             { live: { a: true, gone: false }, all: [R('a', { alertsEnabled: false }), R('b')],
                       active: '', excluded: [] },
};

const cases = {};
for (const [name, c] of Object.entries(CASES)) {
  cases[name] = { input: { live: c.live, active: c.active, excluded: c.excluded || null,
                           all: c.all.map((r) => ({ id: r.id, alertsEnabled: r.alertsEnabled })) },
                  result: run(c.live, c.all, c.active, c.excluded) };
}

// Assertions the corpus must satisfy, so a live change that inverts a rule fails
// HERE rather than being written down as the new truth.
assert.deepEqual(cases.activeIsExcluded.result.built, ['b'],
  'the active router was given a session — it already has an interactive one');
assert.deepEqual(cases.poolOwnedExcluded.result.built, ['a'],
  'a pool-owned router was given a second session');
assert.deepEqual(cases.alertsTurnedOn.result.stopped, ['a'],
  'the alertsEnabled change did not tear the old session down');
assert.deepEqual(cases.alertsTurnedOn.result.built, ['a'],
  'the alertsEnabled change did not rebuild the session');
assert.deepEqual(cases.alreadyRunning.result, { built: [], stopped: [], after: ['a'] },
  'an unchanged router was churned');
assert.deepEqual(cases.emptyActiveId.result.built, ['a', 'b'],
  'an empty activeRouterId excluded something');

const json = JSON.stringify({ cases }, null, 1) + '\n';
if (process.argv.includes('--check')) {
  const have = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (have !== json) {
    console.error('alertpool-sync-cases.json is STALE — re-run without --check');
    process.exit(1);
  }
  console.log('alertpool-sync-cases.json is current');
} else {
  fs.writeFileSync(OUT, json);
  console.log('wrote %s (%d cases)', OUT, Object.keys(cases).length);
}
