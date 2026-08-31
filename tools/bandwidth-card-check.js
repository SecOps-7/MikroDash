'use strict';
/**
 * The Bandwidth card, live against ported.
 *
 * ── CAPACITY IS STATE, SO THE CASES ARE SCRIPTS ─────────────────────────────
 *
 * `_dcBwDown`/`_dcBwUp` are updated by two OTHER events and read by the sample
 * handler, and `_dcBwSyncCapacity` only writes them when the active router is
 * FOUND. So the interesting cases are orderings — an id before the list, a
 * switch to a router that is not there, a list that arrives twice — and none of
 * them is visible from a single payload.
 *
 * ── AND THE PERCENTAGE HAS THREE STATES ─────────────────────────────────────
 *
 * `—` when idle, `<1%` for a trickle, and a rounded figure otherwise. The
 * boundary cases sit either side of 1% and on it, because `<1%` exists exactly
 * so a live-but-tiny link does not read as idle.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/bandwidth-card-check.js
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
const G = LIFT.golden('bandwidth-card-check');
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
function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  return src.slice(i, src.indexOf(close, i) + close.length);
}
const iifeAt = src.indexOf('All 14 new cards live here');
const capAt = src.indexOf('Router bandwidth capacity', iifeAt);
if (LIFT.hasReference(ROOT)) assert.ok(capAt > 0, 'cannot find the capacity block');
// FOUR `traffic:update` handlers exist in app.js; this is the one after the
// capacity block inside the extra-cards IIFE.
const handlerAt = src.indexOf("socket.on('traffic:update'", capAt);
if (LIFT.hasReference(ROOT)) assert.ok(handlerAt > 0, 'no traffic:update handler after the capacity block');
const body = G.value('body', () => braceBody(handlerAt));
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('dc-bwBarRx'), 'the slice is not the Bandwidth card handler');
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('fmtPct'), 'the slice lost its percentage formatter');
const syncSrc = G.value('syncSrc', () => slice('function _dcBwSyncCapacity()', '\n  }', '_dcBwSyncCapacity'));
const rateSrc = G.value('rateSrc', () => slice('function dcSplitRate(', '\n  }', 'dcSplitRate'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['body', body], ['syncSrc', syncSrc], ['rateSrc', rateSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}

const ENTRY = path.join(ROOT, 'testdata', '.bwcard-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderBandwidthCard, setBwRouters, setBwActiveRouter, resetBandwidthCard } " +
  "from '../web/src/pages/dashboard-card-bandwidth.js';\n");
const OUT = path.join(ROOT, 'testdata', '.bwcard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['dc-bwLiveRxNum', 'dc-bwLiveRxUnit', 'dc-bwLiveTxNum', 'dc-bwLiveTxUnit',
  'dc-bwBarRx', 'dc-bwBarTx', 'dc-bwPctRx', 'dc-bwPctTx'];
function makeDom() {
  const byId = new Map();
  for (const id of IDS) {
    byId.set(id, {
      id, style: {},
      set textContent(v) { this._t = String(v); },
      get textContent() { return this._t === undefined ? '' : this._t; },
    });
  }
  return byId;
}
function snap(byId) {
  const out = {};
  for (const id of IDS) {
    const n = byId.get(id);
    out[id] = { text: n.textContent, height: n.style.height };
  }
  return JSON.stringify(out);
}

function liveRunner() {
  const byId = makeDom();
  const ctx = {
    Math, String, Number,
    dcEl: (id) => byId.get(id) || null,
    _dcBwDown: 1000, _dcBwUp: 1000, _dcBwRouters: [], _dcBwActiveId: '',
  };
  vm.createContext(ctx);
  vm.runInContext([rateSrc, syncSrc,
    'function __sample(sample){' + body + '}',
    'function __routers(l){ _dcBwRouters = l||[]; _dcBwSyncCapacity(); }',
    'function __active(d){ _dcBwActiveId = (d&&d.activeId)||""; _dcBwSyncCapacity(); }',
  ].join('\n'), ctx);
  return {
    byId,
    sample: (s) => ctx.__sample(s),
    routers: (l) => ctx.__routers(l),
    active: (d) => ctx.__active(d),
  };
}
function portRunner() {
  const byId = makeDom();
  globalThis.document = { getElementById: (id) => byId.get(id) || null, createElement: () => ({ style: {} }) };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.resetBandwidthCard();
  return {
    byId,
    sample: (s) => m.renderBandwidthCard(s),
    routers: (l) => m.setBwRouters(l),
    active: (d) => m.setBwActiveRouter(d && d.activeId),
  };
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

const S = (rx, tx) => ({ rx_mbps: rx, tx_mbps: tx });
const R = (id, down, up) => ({ id, bwDownMbps: down, bwUpMbps: up });

const SCRIPTS = {
  'a sample with no capacity known — the 1000 default': [['sample', S(100, 50)]],
  'idle': [['sample', S(0, 0)]],
  'a trickle under 1%': [['sample', S(0.5, 0.4)]],
  'exactly 1%': [['sample', S(10, 10)]],
  'just under 1%': [['sample', S(9.99, 9.99)]],
  'saturated': [['sample', S(1000, 1000)]],
  'over capacity — clamped': [['sample', S(5000, 5000)]],
  'sub-Kbps rates': [['sample', S(0.0005, 0.0002)]],
  'gigabit rates': [['sample', S(1200, 1100)]],
  'missing keys': [['sample', {}]],
  'a list then an id': [
    ['routers', [R('a', 100, 50)]], ['active', { activeId: 'a' }], ['sample', S(50, 25)],
  ],
  'an id BEFORE the list': [
    ['active', { activeId: 'a' }], ['routers', [R('a', 100, 50)]], ['sample', S(50, 25)],
  ],
  // The quirk: an unknown router keeps the PREVIOUS capacity.
  'a switch to a router not in the list keeps the old capacity': [
    ['routers', [R('a', 100, 50)]], ['active', { activeId: 'a' }], ['sample', S(50, 25)],
    ['active', { activeId: 'ghost' }], ['sample', S(50, 25)],
  ],
  'a router with no capacity fields falls back to 1000': [
    ['routers', [R('a', undefined, undefined)]], ['active', { activeId: 'a' }], ['sample', S(500, 500)],
  ],
  'a router with ZERO capacity falls back to 1000': [
    ['routers', [{ id: 'a', bwDownMbps: 0, bwUpMbps: 0 }]], ['active', { activeId: 'a' }],
    ['sample', S(500, 500)],
  ],
  'an empty router list': [
    ['routers', []], ['active', { activeId: 'a' }], ['sample', S(500, 500)],
  ],
  'no activeId at all': [
    ['routers', [R('a', 100, 50)]], ['active', {}], ['sample', S(50, 25)],
  ],
  // A NEGATIVE capacity. `|| 1000` catches zero and NaN but not a negative:
  // `-5 || 1000` is `-5`, and these values are operator-set in routers.json. It
  // is the only way `bwDown > 0` is ever false, so without this case the guard
  // is unreachable and a mutation removing it survives.
  'a negative capacity': [
    ['routers', [R('a', -100, -50)]], ['active', { activeId: 'a' }], ['sample', S(50, 25)],
  ],
  'a negative capacity with an idle link': [
    ['routers', [R('a', -100, -50)]], ['active', { activeId: 'a' }], ['sample', S(0, 0)],
  ],
  'asymmetric capacity': [
    ['routers', [R('a', 1000, 100)]], ['active', { activeId: 'a' }], ['sample', S(500, 50)],
  ],
  'a second list replaces the first': [
    ['routers', [R('a', 100, 50)]], ['active', { activeId: 'a' }], ['sample', S(50, 25)],
    ['routers', [R('a', 200, 200)]], ['sample', S(50, 25)],
  ],
};

for (const [name, script] of Object.entries(SCRIPTS)) {
  const L = liveRunner(), P = portRunner();
  script.forEach(([op, arg], i) => {
    L[op](arg); P[op](arg);
    cmp(name + ' after step ' + (i + 1) + ' (' + op + ')', snap(L.byId), snap(P.byId));
  });
}

// ── believability ──────────────────────────────────────────────────────────
{
  const L = liveRunner();
  L.routers([R('a', 100, 50)]); L.active({ activeId: 'a' }); L.sample(S(50, 25));
  const s = JSON.parse(snap(L.byId));
  assert.equal(s['dc-bwBarRx'].height, '50.0%', 'the live rx bar is ' + s['dc-bwBarRx'].height);
  assert.equal(s['dc-bwPctRx'].text, '50%', 'the live rx percentage is ' + s['dc-bwPctRx'].text);
  assert.equal(s['dc-bwLiveRxNum'].text, '50.00', 'the live rx figure is ' + s['dc-bwLiveRxNum'].text);
  // The quirk, stated: an unknown router does NOT reset to the default.
  L.active({ activeId: 'ghost' }); L.sample(S(50, 25));
  const t = JSON.parse(snap(L.byId));
  assert.equal(t['dc-bwBarRx'].height, '50.0%',
    'switching to an unknown router changed the scale to ' + t['dc-bwBarRx'].height +
    ' — the live card keeps the previous capacity');
}
{
  const L = liveRunner();
  L.sample(S(0, 0));
  assert.equal(JSON.parse(snap(L.byId))['dc-bwPctRx'].text, '—', 'idle should read as an em dash');
  L.sample(S(0.5, 0.5));
  assert.equal(JSON.parse(snap(L.byId))['dc-bwPctRx'].text, '<1%', 'a trickle should read <1%');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('bandwidth-card-check: %d comparisons identical', checked);
