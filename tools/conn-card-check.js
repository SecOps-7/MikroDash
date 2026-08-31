'use strict';
/**
 * The Dashboard's Connections card, live against ported.
 *
 * ── THE SPARKLINE HAS NO DOM, SO THE CALLS ARE THE OUTPUT ───────────────────
 *
 * It is a <canvas> drawn through a 2D context, so there is no innerHTML to
 * compare and pixels would be a bad contract anyway. The fake context RECORDS
 * every call and every property set, in order, and the two logs are compared.
 * That catches a wrong coordinate, a wrong stroke colour and a missing
 * `clearRect` alike — none of which a DOM gate can see at all.
 *
 * ── AND THE CARD IS THREE CACHES, SO THE CASES ARE SCRIPTS ──────────────────
 *
 * Each section redraws only when its own fingerprint moves, and the fingerprints
 * are narrow on purpose: destinations key on key+count+country, so a change in
 * only `org`, `cat` or `city` does NOT redraw. That is live behaviour, it is
 * reproduced rather than corrected, and a single-payload corpus could not tell
 * the difference. Every case is a sequence and the whole card is compared after
 * each step.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/conn-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('conn-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
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
  throw new Error('unbalanced body at ' + from);
}

const flushSrc = slice('function _flushConnUpdate()', '\n}', '_flushConnUpdate');
// GUARDED: each asks whether the lifted SLICE still contains a marker.
for (const must of LIFT.hasReference(ROOT) ? ['_connSrcFp', '_connDstFp', 'top-row', 'empty-state'] : []) {
  if (!flushSrc.includes(must)) throw new Error('the _flushConnUpdate slice lost ' + must);
}
const sparkSrc = slice('function drawSparkline(', '\n}', 'drawSparkline');
const barsSrc = slice('function renderProtoBars(', '\n}', 'renderProtoBars');
const badgeSrc = slice('function svcBadge(', '\n}', 'svcBadge');
const escSrc = slice('function esc(', '\n}', 'esc');
const handlerBody = braceBody(src.indexOf("socket.on('conn:update'"));
// GUARDED: a question about the lifted slice.
if (LIFT.hasReference(ROOT)) assert.match(handlerBody, /connTotal\.textContent/,
  'the conn:update handler slice lost its total');
if (LIFT.hasReference(ROOT)) assert.match(handlerBody, /drawSparkline/,
  'the conn:update handler slice lost its sparkline');

const ENTRY = path.join(ROOT, 'testdata', '.conncard-entry.ts');
fs.writeFileSync(ENTRY,
  "export { noteConnUpdate, flushPendingConn, resetConnCaches } from '../web/src/pages/dashboard-conn.js';\n");
const OUT = path.join(ROOT, 'testdata', '.conncard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['connTotal', 'topSources', 'topDests', 'protoBars'];

function makeDom() {
  const byId = new Map();
  const calls = [];
  function node(id) {
    const n = {
      id, className: '', style: {}, children: [],
      // COERCED, as a real element coerces. The live handler assigns a NUMBER
      // to textContent and the browser stores "42"; a fake node that kept the
      // number would report a difference on every single step that has nothing
      // to do with the port.
      set textContent(v) { this._t = String(v); },
      get textContent() { return this._t === undefined ? '' : this._t; },
      // COUNTED, not just stored. Three fingerprints exist to SKIP redraws, and
      // a redraw that produces identical HTML is invisible in the DOM — putting
      // `ts` into a fingerprint makes every section redraw every tick and
      // changes nothing a snapshot of the markup can see. Counting the writes is
      // what puts the caching itself on trial.
      set innerHTML(v) { this._h = v; this._writes = (this._writes || 0) + 1; },
      get innerHTML() { return this._h || ''; },
      writes() { return this._writes || 0; },
      appendChild(c) { this.children.push(c); return c; },
    };
    byId.set(id, n);
    return n;
  }
  for (const id of IDS) node(id);
  // The canvas, and a context that records instead of drawing.
  const ctx2d = new Proxy({}, {
    get(_t, prop) {
      if (prop === 'then') return undefined;
      return (...args) => { calls.push(String(prop) + '(' + args.map((a) => JSON.stringify(a)).join(',') + ')'); };
    },
    set(_t, prop, value) { calls.push(String(prop) + '=' + JSON.stringify(value)); return true; },
  });
  const canvas = node('connSparkCanvas');
  canvas.width = 120; canvas.height = 40;
  canvas.getContext = (kind) => { calls.push('getContext(' + JSON.stringify(kind) + ')'); return ctx2d; };
  return { byId, calls, ctx2d, canvas };
}

function snapshot(dom) {
  // `getContext` is EXCLUDED, and only that. The live app caches the context at
  // script load; the port resolves it per draw, because here the page bodies are
  // fetched and injected and a lookup at import time would find nothing. That is
  // a mechanism difference with no drawing in it. Every call that PAINTS is
  // compared, and the assertion at the foot of this file proves the live side
  // really did stroke and clear — so a port that stopped drawing entirely cannot
  // hide behind this exclusion.
  const out = { canvas: dom.calls.filter((c) => !c.startsWith('getContext(')) };
  for (const id of IDS) {
    const n = dom.byId.get(id);
    out[id] = { html: n.innerHTML, text: n.textContent, writes: n.writes() };
  }
  return JSON.stringify(out);
}

function liveRunner() {
  const dom = makeDom();
  const frames = [];
  let booked = 0;
  const ctx = {
    Math, JSON, String, Array, Number, parseInt, isNaN,
    requestAnimationFrame: (fn) => { booked++; frames.push(fn); return frames.length; },
    $: (id) => dom.byId.get(id) || null,
    sparkCanvas: dom.canvas,
    sparkCtx2d: dom.ctx2d,
  };
  for (const id of IDS) ctx[id] = dom.byId.get(id);
  vm.createContext(ctx);
  vm.runInContext([
    escSrc, badgeSrc, barsSrc, sparkSrc,
    'var connHistory=[], MAX_CONN_HIST=60;',
    "var _connSrcFp='', _connDstFp='', _connProtoFp='';",
    'var _pendingConnData=null, _connRafId=null;',
    flushSrc,
    'function __note(data){' + handlerBody + '}',
    'function __flushPending(){ if(_pendingConnData && !_connRafId) _connRafId=requestAnimationFrame(_flushConnUpdate); }',
  ].join('\n'), ctx);
  return {
    dom, booked: () => booked,
    note: (d) => ctx.__note(d),
    flushPending: () => ctx.__flushPending(),
    runFrames: () => { for (const f of frames.splice(0)) f(); },
  };
}

function portRunner() {
  const dom = makeDom();
  const frames = [];
  let booked = 0;
  globalThis.document = {
    hidden: false,
    getElementById: (id) => dom.byId.get(id) || null,
    createElement: () => ({ style: {} }),
    addEventListener() {}, dispatchEvent: () => true,
  };
  globalThis.requestAnimationFrame = (fn) => { booked++; frames.push(fn); return frames.length; };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.resetConnCaches();
  return {
    dom, booked: () => booked,
    note: (d) => m.noteConnUpdate(d),
    flushPending: () => m.flushPendingConn(),
    runFrames: () => { for (const f of frames.splice(0)) f(); },
  };
}

// ── the corpus ─────────────────────────────────────────────────────────────
const pc = (t, u, i, o) => ({ tcp: t, udp: u, icmp: i, other: o });
const S = (ip, name, count) => ({ ip, name, count });
const D = (o) => Object.assign({ key: '198.51.100.7', count: 3 }, o);
const base = {
  ts: 1000, total: 42, protoCounts: pc(30, 10, 1, 1),
  topSources: [S('198.51.100.5', 'desktop', 12), S('198.51.100.6', 'phone', 7)],
  topDestinations: [D({ country: 'US', city: 'Ashburn', org: 'Amazon', cat: 'cloud' })],
};
const p = (over) => Object.assign({}, base, over);

const CASES = [
  ['one payload renders every section', [['note', p({})], ['frames']]],
  ['an identical payload redraws NOTHING', [
    ['note', p({})], ['frames'],
    ['note', p({ ts: 2000 })], ['frames'],
  ]],
  ['ts alone must not be fingerprinted', [
    // If `ts` were in any fingerprint, every tick would redraw everything and
    // the caches would be dead weight. The canvas log still grows: the total
    // and the sparkline are NOT deferred and are drawn every tick.
    ['note', p({ ts: 1 })], ['frames'],
    ['note', p({ ts: 2 })], ['frames'],
    ['note', p({ ts: 3 })], ['frames'],
  ]],
  ['a changed source count redraws sources', [
    ['note', p({})], ['frames'],
    ['note', p({ topSources: [S('198.51.100.5', 'desktop', 99), S('198.51.100.6', 'phone', 7)] })], ['frames'],
  ]],
  ['a changed source NAME does not — it is not fingerprinted', [
    ['note', p({})], ['frames'],
    ['note', p({ topSources: [S('198.51.100.5', 'laptop', 12), S('198.51.100.6', 'phone', 7)] })], ['frames'],
  ]],
  ['a changed org/cat/city does not redraw destinations', [
    ['note', p({})], ['frames'],
    ['note', p({ topDestinations: [D({ country: 'US', city: 'Boston', org: 'Google', cat: 'ads' })] })], ['frames'],
  ]],
  ['a changed destination COUNTRY does redraw', [
    ['note', p({})], ['frames'],
    ['note', p({ topDestinations: [D({ country: 'DE', city: 'Ashburn', org: 'Amazon', cat: 'cloud' })] })], ['frames'],
  ]],
  ['empty lists render the dash, not nothing', [
    ['note', p({ topSources: [], topDestinations: [] })], ['frames'],
  ]],
  ['a list that empties AFTER having rows', [
    ['note', p({})], ['frames'],
    ['note', p({ topSources: [], topDestinations: [] })], ['frames'],
  ]],
  ['a destination with no country has no geo line', [
    ['note', p({ topDestinations: [D({ org: 'Cloudflare', cat: 'cdn' })] })], ['frames'],
  ]],
  ['a country with no city is a bare flag', [
    ['note', p({ topDestinations: [D({ country: 'NL' })] })], ['frames'],
  ]],
  ['a MALFORMED country still maps every character', [
    // The live call site has no length guard — unlike `iso2Flag`, which returns
    // '' for anything that is not two characters. The port must NOT reuse that.
    ['note', p({ topDestinations: [D({ country: 'USA' })] })], ['frames'],
    ['note', p({ topDestinations: [D({ country: 'u' })] })], ['frames'],
  ]],
  ['a destination with no org renders no badge', [
    ['note', p({ topDestinations: [D({ country: 'US', city: 'Reston' })] })], ['frames'],
  ]],
  ['an org with no cat falls back to svc-other', [
    ['note', p({ topDestinations: [D({ org: 'Akamai' })] })], ['frames'],
  ]],
  ['markup in every rendered string is escaped', [
    ['note', p({
      topSources: [S('<img src=x>', '"quoted" & <b>', 5)],
      topDestinations: [D({ key: '<script>', org: 'A&B', cat: 'x"y', city: '<i>', country: 'US' })],
    })], ['frames'],
  ]],
  ['protocol bars: all zero is 0%, not NaN%', [
    ['note', p({ protoCounts: pc(0, 0, 0, 0) })], ['frames'],
  ]],
  ['protocol bars redraw only when the counts move', [
    ['note', p({})], ['frames'],
    ['note', p({ ts: 2, protoCounts: pc(30, 10, 1, 1) })], ['frames'],
    ['note', p({ ts: 3, protoCounts: pc(31, 10, 1, 1) })], ['frames'],
  ]],
  ['the sparkline needs two points before it draws', [
    ['note', p({ total: 5 })], ['frames'],
    ['note', p({ total: 9 })], ['frames'],
  ]],
  ['an all-zero history does not divide by zero', [
    ['note', p({ total: 0 })], ['frames'],
    ['note', p({ total: 0 })], ['frames'],
  ]],
  ['the history is capped and the sparkline rescales', [
    ...Array.from({ length: 65 }, (_, i) => [['note', p({ ts: i, total: i })], ['frames']]).flat(),
  ]],
  ['a burst books one frame and flushes the latest', [
    ['note', p({ total: 1 })], ['note', p({ total: 2, topSources: [S('198.51.100.9', 'x', 1)] })],
    ['frames'],
  ]],
  ['flushing with nothing pending books no frame', [
    ['note', p({})], ['frames'], ['flushPending'], ['frames'],
  ]],
];

const step = (r, [op, arg]) => ({
  note: () => r.note(arg), frames: () => r.runFrames(), flushPending: () => r.flushPending(),
}[op] || (() => { throw new Error('unknown step ' + op); }))();

let bad = 0, steps = 0;
for (const [name, script] of CASES) {
  // Recipe 3i. BOTH halves of the comparison — the DOM snapshot and the booked
  // fetch list — are captured per step, so the pair cannot drift apart.
  const liveSnaps = G.value(name + ' live run', () => {
    const l = liveRunner();
    return script.map((st) => { step(l, st); return [snapshot(l.dom), l.booked()]; });
  });
  const port = portRunner();
  script.forEach((s, i) => {
    step(port, s);
    steps++;
    const a = liveSnaps[i][0], b = snapshot(port.dom);
    const fa = liveSnaps[i][1], fb = port.booked();
    if (a === b && fa === fb) return;
    bad++;
    if (bad <= 3) {
      console.error('\nDIFF %s — after step %d (%s)', name, i + 1, s[0]);
      if (fa !== fb) console.error('  frames booked: live %d, port %d', fa, fb);
      const A = JSON.parse(a), B = JSON.parse(b);
      for (const k of Object.keys(A)) {
        if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
          console.error('  %s\n    live: %j\n    port: %j', k, A[k], B[k]);
        }
      }
    }
  });
}

// The run is only believable if something was drawn — RE-AIMED AT THE PORT,
// which is the side that has to keep drawing it.
{
  const r = portRunner();
  r.note(p({})); r.runFrames(); r.note(p({ total: 9, ts: 2 })); r.runFrames();
  const s = JSON.parse(snapshot(r.dom));
  assert.match(s.topSources.html, /top-row/, 'the sources list did not render');
  assert.match(s.topDests.html, /has-ip-tip/, 'the destinations list did not render');
  assert.match(s.protoBars.html, /proto-fill/, 'the protocol bars did not render');
  assert.equal(s.connTotal.text, '9', 'the total did not render');
  assert.ok(s.canvas.some((c) => c.startsWith('stroke(')), 'the sparkline never stroked');
  assert.ok(s.canvas.some((c) => c.startsWith('clearRect(')), 'the sparkline never cleared');
}

fs.rmSync(OUT, { force: true });
if (bad) {
  console.error('\n%d of %d steps differ', bad, steps);
  process.exit(1);
}
console.log('conn-card-check: %d cases, %d steps identical (DOM + canvas calls)', CASES.length, steps);
