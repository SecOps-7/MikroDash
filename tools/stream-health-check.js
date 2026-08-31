'use strict';
/**
 * The stream-degradation warnings and the WAN badge, live against ported.
 *
 * ── THE WARNING'S ELEMENT ID IS BUILT AT RUNTIME ────────────────────────────
 *
 * `STREAM_WARN_CARDS[collector] + 'Warn'`, so grepping the live source for
 * `trafficCardWarn` finds nothing and these two elements looked like orphaned
 * markup in this port's coverage ledger for several ticks. The table is READ OUT
 * of the live source here rather than retyped, so a collector added over there
 * fails this gate instead of silently having no warning.
 *
 * ── RECOVERY MUST CLEAR BOTH ────────────────────────────────────────────────
 *
 * The card's tint and the sentence. So every case is a SEQUENCE — degrade, then
 * recover — because a port that set the state and never cleared it passes any
 * single-payload comparison.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/stream-health-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('stream-health-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function braceBody(from) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const open = src.indexOf('{', from);
  let depth = 0;
  for (let i = open; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (!depth) return src.slice(open + 1, i); }
  }
  throw new Error('unbalanced body');
}
// FROZEN. An IIFE-form lift — `freeze-src.py` only rewrites plain
// `const X = lifter(...)` assignments, so this one needed doing by hand.
const tableSrc = G.value('tableSrc', () => {
  const i = src.indexOf('var STREAM_WARN_CARDS');
  if (i === -1) throw new Error('cannot find STREAM_WARN_CARDS');
  return src.slice(i, src.indexOf('\n', i));
});
const healthBody = G.value('healthBody', () => braceBody(src.indexOf("socket.on('stream:health'")));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['healthBody', healthBody]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
assert.ok(healthBody.includes('is-degraded'), 'the stream:health slice lost its card class');
// FROZEN (IIFE-form lift; freeze-src.py only rewrites plain
// `const X = lifter(...)` assignments).
const wanSrc = G.value('wanSrc', () => {
  const i = src.indexOf('function renderWanStatus(');
  return src.slice(i, src.indexOf('\n}', i) + 2);
});
assert.ok(wanSrc.includes('wan-disabled'), 'the renderWanStatus slice lost its states');

// The table, so the corpus covers exactly the collectors the live app knows.
const tableCtx = {};
vm.createContext(tableCtx);
vm.runInContext(tableSrc, tableCtx);
const CARDS = tableCtx.STREAM_WARN_CARDS;
assert.ok(Object.keys(CARDS).length >= 2, 'only ' + Object.keys(CARDS).length + ' warn cards');
say('  live warn cards: %s', JSON.stringify(CARDS));

const ENTRY = path.join(ROOT, 'testdata', '.streamhealth-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderStreamHealth, renderWanStatus } from '../web/src/pages/dashboard-stream-health.js';\n");
const OUT = path.join(ROOT, 'testdata', '.streamhealth-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [...Object.values(CARDS), ...Object.values(CARDS).map((c) => c + 'Warn'), 'wanStatusBadge'];
function makeDom(omit) {
  const byId = new Map();
  for (const id of IDS) {
    if (omit && omit.includes(id)) continue;
    const n = {
      id, className: '', classes: new Set(),
      classList: {
        add: (c) => n.classes.add(c), remove: (c) => n.classes.delete(c),
        contains: (c) => n.classes.has(c),
      },
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
    };
    byId.set(id, n);
  }
  return byId;
}
function snap(byId) {
  const out = {};
  for (const id of IDS) {
    const n = byId.get(id);
    out[id] = n ? { text: n.textContent, cls: n.className, classes: [...n.classes].sort() } : null;
  }
  return JSON.stringify(out);
}
function liveSide(omit) {
  const byId = makeDom(omit);
  const ctx = { String, $: (id) => byId.get(id) || null };
  vm.createContext(ctx);
  vm.runInContext([tableSrc, wanSrc,
    'function __health(h){' + healthBody + '}',
    'var wanStatusBadge = $("wanStatusBadge");',
  ].join('\n'), ctx);
  return { byId, health: (h) => ctx.__health(h), wan: (s) => ctx.renderWanStatus(s) };
}
function portSide(omit) {
  const byId = makeDom(omit);
  globalThis.document = { getElementById: (id) => byId.get(id) || null };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  return { byId, health: m.renderStreamHealth, wan: m.renderWanStatus };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) {
    const A = JSON.parse(a), B = JSON.parse(b);
    shout('DIFF %s', what);
    for (const k of Object.keys(A)) {
      if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
        shout('  %s\n    live: %j\n    port: %j', k, A[k], B[k]);
      }
    }
  }
}

// ── stream health ──────────────────────────────────────────────────────────
const H = (collector, degraded, restarts) => ({ collector, degraded, restarts });
const SCRIPTS = {};
for (const coll of Object.keys(CARDS)) {
  SCRIPTS['degraded: ' + coll] = [H(coll, true, 3)];
  SCRIPTS['degrade then RECOVER: ' + coll] = [H(coll, true, 3), H(coll, false, 3)];
  SCRIPTS['healthy from the start: ' + coll] = [H(coll, false, 0)];
  SCRIPTS['restart count of zero: ' + coll] = [H(coll, true, 0)];
  SCRIPTS['no restart count: ' + coll] = [H(coll, true, undefined)];
  SCRIPTS['degrade twice: ' + coll] = [H(coll, true, 1), H(coll, true, 9)];
}
Object.assign(SCRIPTS, {
  'an unknown collector': [H('nosuch', true, 3)],
  'no collector key': [{ degraded: true, restarts: 1 }],
  'an undefined payload': [undefined],
  'null': [null],
  'one collector degrading does not touch the other': [
    H('traffic', true, 2), H('connections', false, 0),
  ],
});
for (const [name, script] of Object.entries(SCRIPTS)) {
  const L = liveSide(), P = portSide();
  script.forEach((h, i) => {
    L.health(h); P.health(h);
    cmp('health: ' + name + ' step ' + (i + 1), snap(L.byId), snap(P.byId));
  });
}
// A card whose warning element is missing must be left alone entirely.
{
  const omit = [Object.values(CARDS)[0] + 'Warn'];
  const L = liveSide(omit), P = portSide(omit);
  const h = H(Object.keys(CARDS)[0], true, 4);
  L.health(h); P.health(h);
  cmp('health: the warning element is missing', snap(L.byId), snap(P.byId));
}

// ── the WAN badge ──────────────────────────────────────────────────────────
const WAN = {
  'up': { ifName: 'ether1', running: true },
  'down': { ifName: 'ether1', running: false },
  'disabled': { ifName: 'ether1', disabled: true },
  'disabled WINS over running': { ifName: 'ether1', disabled: true, running: true },
  // The `|| '?'` fallback exists in all THREE branches, so each needs a case
  // without a name — a mutation to one branch survives if only another is
  // covered, which is exactly what happened with the disabled branch.
  'no interface name, running': { running: true },
  'no interface name, DISABLED': { disabled: true },
  'no interface name, down': {},
  'an empty name, disabled': { ifName: '', disabled: true },
  'an empty interface name': { ifName: '', running: true },
  'nothing at all': {},
  'a name with markup': { ifName: '<b>', running: true },
};
for (const [name, s] of Object.entries(WAN)) {
  const L = liveSide(), P = portSide();
  L.wan(s); P.wan(s);
  cmp('wan: ' + name, snap(L.byId), snap(P.byId));
}
// The badge is rebuilt from scratch each time, so a switch must not accumulate.
{
  const L = liveSide(), P = portSide();
  for (const s of [{ ifName: 'a', running: true }, { ifName: 'a', disabled: true }, { ifName: 'a', running: false }]) {
    L.wan(s); P.wan(s);
    cmp('wan: successive states', snap(L.byId), snap(P.byId));
  }
  assert.equal(JSON.parse(snap(L.byId)).wanStatusBadge.cls, 'wan-badge wan-down',
    'the live badge accumulated classes across states');
}

// ── believability ──────────────────────────────────────────────────────────
{
  const L = liveSide();
  L.health(H('traffic', true, 5));
  const s = JSON.parse(snap(L.byId));
  assert.match(s.trafficCardWarn.text, /restarted 5 times/, 'the live warning is ' + s.trafficCardWarn.text);
  assert.ok(s.trafficCard.classes.includes('is-degraded'), 'the live card was not tinted');
  L.health(H('traffic', false, 5));
  const t = JSON.parse(snap(L.byId));
  assert.equal(t.trafficCardWarn.text, '', 'recovery did not clear the warning');
  assert.ok(!t.trafficCard.classes.includes('is-degraded'), 'recovery did not clear the tint');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('stream-health-check: %d comparisons identical', checked);
