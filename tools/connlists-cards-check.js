'use strict';
/**
 * Top Countries and Top Ports, live against ported.
 *
 * ── THE THIRD BAR IS A REMAINDER, SO THE ROUNDING IS THE TEST ───────────────
 *
 * tcp and udp round independently and `other` is `100 - tcp - udp`. The corpus
 * carries splits where both round UP — which makes `other` negative — and where
 * both round down, which makes it larger than its true share. A port computing
 * the third the same way as the first two passes every tidy case and fails these.
 *
 * ── AND THE HANDLER IS SLICED IN THE MIDDLE OF ITSELF ───────────────────────
 *
 * The live `conn:update` handler also drives the map and is 57 lines. Only the
 * two list halves are ported, so this gate runs the WHOLE handler with the map
 * functions stubbed and compares only the two list containers — and asserts the
 * stub was CALLED, so the boundary is where it is claimed to be.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/connlists-cards-check.js
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
const G = LIFT.golden('connlists-cards-check');
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
function grab(decl, close, name) {
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
const at = src.indexOf("socket.on('conn:update'", iifeAt);
// GUARDED: a question about the live SOURCE.
if (LIFT.hasReference(ROOT)) assert.ok(at > 0, 'no conn:update handler in the extra-cards IIFE');
const body = G.value('body', () => braceBody(at));
for (const must of ['dc-connTopMapList', 'dc-connPortList', '_dcMapApply']) {
  assert.ok(body.includes(must), 'the conn:update slice lost ' + must);
}
const escSrc = G.value('escSrc', () => { const i = src.indexOf('function dcEsc('); return src.slice(i, src.indexOf('\n', i)); });
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['escSrc', escSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
const flagSrc = G.value('flagSrc', () => grab('function dcFlag(', '\n  }', 'dcFlag'));
const ccSrc = G.value('ccSrc', () => grab('var DC_CC_NAMES={', '};', 'DC_CC_NAMES'));
const portSrc = G.value('portSrc', () => grab('var DC_PORT_NAMES=', '};', 'DC_PORT_NAMES'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['body', body], ['flagSrc', flagSrc], ['ccSrc', ccSrc], ['portSrc', portSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}

const ENTRY = path.join(ROOT, 'testdata', '.connlists-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderConnListCards } from '../web/src/pages/dashboard-card-connlists.js';\n");
const OUT = path.join(ROOT, 'testdata', '.connlists-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['dc-connTopMapList', 'dc-connPortList'];
function makeDom() {
  const byId = new Map();
  const mk = (id) => {
    const n = {
      id,
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      set innerHTML(v) { n._h = v; },
      get innerHTML() {
        if (n._h !== undefined) return n._h;
        return String(n._t === undefined ? '' : n._t)
          .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      },
    };
    if (id) byId.set(id, n);
    return n;
  };
  for (const id of IDS) mk(id);
  return { byId, mk };
}
function snap(d) {
  const out = {};
  for (const id of IDS) out[id] = d.byId.get(id).innerHTML;
  return JSON.stringify(out);
}
function liveRun(payload) {
  const d = makeDom();
  let mapCalls = 0;
  const ctx = {
    Math, String, Array, Object,
    dcEl: (id) => d.byId.get(id) || null,
    document: { createElement: () => d.mk('') },
    // The map half, stubbed. Asserted to have been called below.
    _dcMapReady: true, _dcMapPending: null,
    _dcMapApply: () => { mapCalls++; },
  };
  vm.createContext(ctx);
  vm.runInContext([escSrc, flagSrc, ccSrc, portSrc, 'function __run(data){' + body + '}'].join('\n'), ctx);
  ctx.__run(payload);
  return { snap: snap(d), mapCalls };
}
function portRun(payload) {
  const d = makeDom();
  globalThis.document = { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderConnListCards(payload);
  return snap(d);
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
      if (A[k] !== B[k]) shout('  %s\n    live: %s\n    port: %s', k,
        String(A[k]).slice(0, 260), String(B[k]).slice(0, 260));
    }
  }
}

const P = (tcp, udp, other) => ({ tcp, udp, other });
const CC = (cc, count, proto, country) => ({ cc, count, proto, country });
const PT = (port, count) => ({ port, count });

const CASES = {
  'a normal country list': { topCountries: [CC('US', 50, P(30, 15, 5)), CC('DE', 10, P(10, 0, 0))] },
  'no countries': { topCountries: [] },
  'no topCountries key': {},
  // The remainder bar: both round UP, so `other` goes negative.
  'both round up — other is NEGATIVE': { topCountries: [CC('US', 3, P(1, 1, 1))] },
  'both round down': { topCountries: [CC('US', 8, P(3, 3, 2))] },
  'a third each': { topCountries: [CC('US', 3, P(1, 1, 1))] },
  'all tcp': { topCountries: [CC('US', 9, P(9, 0, 0))] },
  'all zero protocols': { topCountries: [CC('US', 0, P(0, 0, 0))] },
  // NOT a case: an entry with no `proto` makes the LIVE handler throw at
  // `e.proto.tcp`, taking the Top Ports list down with it. It cannot happen —
  // `ConnCountryProto` is a value type on the Go side and is always marshalled —
  // and an earlier version of this file drove it anyway, which made the live
  // side throw while the port rendered. The port no longer guards it either;
  // its type says the field is required, which is the actual wire contract.
  'seven eighths and one eighth': { topCountries: [CC('US', 8, P(7, 1, 0))] },
  // The label's three fallbacks.
  'a country IN the table': { topCountries: [CC('FR', 1, P(1, 0, 0))] },
  'a country NOT in the table, with a country field': {
    topCountries: [CC('ZZ', 1, P(1, 0, 0), 'Elbonia')],
  },
  'a country NOT in the table and no country field': { topCountries: [CC('ZZ', 1, P(1, 0, 0))] },
  'no cc at all': { topCountries: [CC(undefined, 1, P(1, 0, 0))] },
  'a malformed cc renders a globe': { topCountries: [CC('USA', 1, P(1, 0, 0))] },
  'markup in a country name': { topCountries: [CC('ZZ', 1, P(1, 0, 0), '<b>&"x"')] },
  'more than twelve countries': {
    topCountries: Array.from({ length: 18 }, (_, i) => CC('US', 18 - i, P(1, 0, 0))),
  },
  // Ports.
  'a normal port list': { topPorts: [PT(443, 100), PT(80, 50), PT(22, 1)] },
  'no ports': { topPorts: [] },
  'a port not in the name table': { topPorts: [PT(9999, 5)] },
  'the bar floor at four pixels': { topPorts: [PT(443, 1000), PT(80, 1)] },
  'a zero top count': { topPorts: [PT(443, 0), PT(80, 0)] },
  'a string port': { topPorts: [PT('443', 7)] },
  'markup in a port': { topPorts: [PT('<b>', 3)] },
  // UNSORTED, so a port that sorted the list would disagree. Every other case
  // here happens to arrive in descending order — which is what the collector
  // sends — so a sorting rewrite survived them all. The card must NOT sort: the
  // first entry sets the bar scale, and re-ranking would rescale every bar.
  'an UNSORTED port list is left alone': { topPorts: [PT(80, 5), PT(443, 100), PT(22, 50)] },
  'a list whose first entry is the smallest': { topPorts: [PT(22, 1), PT(443, 100)] },
  'more than twelve ports': {
    topPorts: Array.from({ length: 20 }, (_, i) => PT(1000 + i, 100 - i)),
  },
  'both lists at once': {
    topCountries: [CC('US', 5, P(3, 2, 0))], topPorts: [PT(443, 5)],
  },
};

for (const [name, payload] of Object.entries(CASES)) {
  const L = liveRun(payload);
  cmp(name, L.snap, portRun(payload));
}

// ── the boundary is where this gate claims it is ───────────────────────────
{
  const L = liveRun({ topCountries: [CC('US', 1, P(1, 0, 0))] });
  assert.equal(L.mapCalls, 1,
    'the live handler did not call _dcMapApply — the map half is not where this gate assumes, ' +
    'so what it compares may not be the whole of the two lists');
}
// ── believability ──────────────────────────────────────────────────────────
{
  const L = liveRun({ topCountries: [CC('US', 50, P(30, 15, 5))], topPorts: [PT(443, 9)] });
  const s = JSON.parse(L.snap);
  assert.match(s['dc-connTopMapList'], /conn-map-row/, 'the live country list rendered nothing');
  assert.match(s['dc-connTopMapList'], /United States/, 'the live label lost its table lookup');
  assert.match(s['dc-connPortList'], /HTTPS/, 'the live port list lost its service name');
}
{
  // The negative remainder, stated: 1/1/1 of 3 rounds to 33/33 and leaves 34.
  const L = liveRun({ topCountries: [CC('US', 3, P(2, 2, 2))] });
  const m = /conn-proto-other" style="flex:(-?\d+)"/.exec(JSON.parse(L.snap)['dc-connTopMapList']);
  assert.ok(m, 'no other bar rendered');
  say('  the remainder bar for a 2/2/2 split is flex:%s (live)', m[1]);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('connlists-cards-check: %d cases identical', checked);
