'use strict';
/**
 * Routes and BGP Peers, live against ported.
 *
 * ── THE DOUGHNUT IS COMPARED AS DATA AND AS DRAWING ─────────────────────────
 *
 * Its slices, labels and colours are arrays, so they compare directly. Its
 * CENTRE is canvas text drawn by an inline plugin, so a recording context
 * captures the calls — that is where a total of zero shows an em dash rather
 * than a nought, and where the text is read from a CSS variable.
 *
 * ── AND THE COUNT SETTER TREATS null AND undefined DIFFERENTLY ──────────────
 *
 * `v !== undefined` — so an absent key is an em dash and an explicit null
 * renders the string "null". Both spellings are in the corpus, because that is
 * the only place the two readings of "no value" diverge.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/routing-cards-check.js
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
const G = LIFT.golden('routing-cards-check');
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
const iifeAt = src.indexOf('All 14 new cards live here');
if (LIFT.hasReference(ROOT)) assert.ok(iifeAt > 0, 'cannot find the extra-cards IIFE');
const handlerAt = src.indexOf("socket.on('routing:update'", iifeAt);
if (LIFT.hasReference(ROOT)) assert.ok(handlerAt > 0, 'no routing:update handler inside the extra-cards IIFE');
const body = braceBody(handlerAt);
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('dc-rtBgpEstab'), 'the slice is not the routing handler');
const donutAt = src.indexOf('function dcUpdateDonut(');
const donutSrc = src.slice(donutAt, src.indexOf('\n  }', src.indexOf('_dcDonut.update', donutAt)) + 4);
for (const must of ['DONUT_COLOURS', 'afterDraw', "cutout:'68%'"]) {
  if (LIFT.hasReference(ROOT)) assert.ok(donutSrc.includes(must), 'the dcUpdateDonut slice lost ' + must);
}

const ENTRY = path.join(ROOT, 'testdata', '.rtcards-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderRoutingCards, resetRoutingCards, donutSlices, donutConfig, drawDonutCentre } " +
  "from '../web/src/pages/dashboard-card-routing.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rtcards-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['dc-rtConnect', 'dc-rtStatic', 'dc-rtDynamic', 'dc-rtBgp', 'dc-rtOspf',
  'dc-rtBgpTotal', 'dc-rtBgpEstab', 'dc-rtBgpDown'];

function recorder() {
  const calls = [];
  const ctx = new Proxy({}, {
    get(_t, p) {
      if (p === 'then') return undefined;
      if (p === 'measureText') return () => ({ width: 20 });
      return (...a) => calls.push(String(p) + '(' + a.map((x, i) => {
        // `fillText`'s first argument is COERCED by the canvas, so a number and
        // its string spell the same pixels: the live plugin passes
        // `_dcDonutTotal || '—'`, a number when non-zero, and the port passes it
        // through `String()`. Only the TYPE is normalised — a wrong VALUE still
        // differs, which is what this comparison is for.
        if (String(p) === 'fillText' && i === 0) return JSON.stringify(String(x));
        return JSON.stringify(x);
      }).join(',') + ')');
    },
    set(_t, p, v) { calls.push(String(p) + '=' + JSON.stringify(v)); return true; },
  });
  return { ctx, calls };
}
function makeWorld() {
  const byId = new Map();
  for (const id of IDS) {
    byId.set(id, {
      id,
      set textContent(v) { this._t = String(v); },
      get textContent() { return this._t === undefined ? '' : this._t; },
    });
  }
  byId.set('dc-rtDonutCanvas', { id: 'dc-rtDonutCanvas' });
  const made = [];
  const chart = {
    data: { labels: [], datasets: [{ data: [], backgroundColor: [] }] },
    updates: [], update(m) { this.updates.push(m); }, destroy() {},
  };
  return { byId, made, chart };
}
function snap(w) {
  const out = { made: w.made.length, chartData: w.chart.data, updates: w.chart.updates.slice() };
  for (const id of IDS) out[id] = w.byId.get(id).textContent;
  // The first construction's config, minus its functions.
  if (w.made[0]) {
    out.config = JSON.parse(JSON.stringify(w.made[0], (k, v) => (typeof v === 'function' ? 'FN' : v)));
  }
  return JSON.stringify(out);
}

function liveRun(payloads) {
  const w = makeWorld();
  const ctx = {
    Math, String, Number, JSON, Object,
    dcEl: (id) => w.byId.get(id) || null,
    Chart: function (canvas, cfg) { w.made.push(cfg); w.chart._cfg = cfg; return w.chart; },
    getComputedStyle: () => ({ getPropertyValue: () => ' rgba(1,2,3,.9) ' }),
    document: { documentElement: {} },
    _dcDonut: null, _dcDonutTotal: 0,
  };
  vm.createContext(ctx);
  vm.runInContext(donutSrc + '\nfunction __run(data){' + body + '}', ctx);
  for (const p of payloads) ctx.__run(p);
  return { snap: snap(w), w, ctx };
}
function portRun(payloads) {
  const w = makeWorld();
  globalThis.document = { getElementById: (id) => w.byId.get(id) || null, documentElement: {} };
  globalThis.getComputedStyle = () => ({ getPropertyValue: () => ' rgba(1,2,3,.9) ' });
  globalThis.Chart = function (canvas, cfg) { w.made.push(cfg); w.chart._cfg = cfg; return w.chart; };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.resetRoutingCards();
  for (const p of payloads) m.renderRoutingCards(p);
  return { snap: snap(w), w, m };
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
        shout('  %s\n    live: %s\n    port: %s', k,
          JSON.stringify(A[k]).slice(0, 220), JSON.stringify(B[k]).slice(0, 220));
      }
    }
  }
}

const rc = (o) => ({ routeCounts: o });
const CASES = {
  'a full set': [rc({ connect: 3, static: 5, dynamic: 2, bgp: 7, ospf: 1, total: 18 })],
  'no other slice — the parts sum to the total': [rc({ connect: 2, static: 3, dynamic: 0, bgp: 0, ospf: 0, total: 5 })],
  'an other slice': [rc({ connect: 2, static: 3, dynamic: 0, bgp: 0, ospf: 0, total: 12 })],
  'total LOWER than the parts — clamped at zero': [rc({ connect: 5, static: 5, total: 2 })],
  'everything zero': [rc({ connect: 0, static: 0, dynamic: 0, bgp: 0, ospf: 0, total: 0 })],
  'an empty routeCounts': [rc({})],
  'no routeCounts key': [{}],
  // The two spellings of "no value".
  'an ABSENT count renders a dash': [rc({ static: 4, total: 4 })],
  'an explicit NULL renders "null"': [rc({ connect: null, static: 4, bgp: null, total: 4 })],
  'a null total': [rc({ static: 4, total: null })],
  'connect counts as known but is not a slice': [rc({ connect: 10, static: 1, total: 11 })],
  // The BGP summary half.
  'a bgp summary': [{ routeCounts: { total: 1 }, summary: { total: 4, established: 3, down: 1 } }],
  'a summary with nulls': [{ routeCounts: {}, summary: { total: null, established: 2 } }],
  'no summary key': [rc({ total: 1 })],
  // Sequences: the second payload must MUTATE rather than rebuild.
  'two payloads — the second updates in place': [
    rc({ connect: 1, static: 1, total: 2 }),
    rc({ connect: 2, static: 5, dynamic: 1, total: 20 }),
  ],
  'three payloads': [
    rc({ static: 1, total: 1 }), rc({ static: 2, total: 2 }), rc({ static: 3, bgp: 1, total: 9 }),
  ],
};

for (const [name, payloads] of Object.entries(CASES)) {
  cmp(name, G.live(name, () => liveRun(payloads).snap), portRun(payloads).snap);
}

// ── the centre text AFTER A SECOND PAYLOAD ─────────────────────────────────
//
// The plugin reads `_dcDonutTotal` at DRAW time, and the assignment sits before
// the construct/update branch so every payload refreshes it. Moving it inside
// the construction branch survived every case above, because each of those
// drives the plugin only after the FIRST payload — so the centre would have
// shown a stale total for the whole life of the page and nothing noticed.
for (const [name, payloads, want] of [
  ['a second payload updates the centre', [rc({ static: 1, total: 3 }), rc({ static: 2, total: 9 })], '9'],
  ['a third payload too', [rc({ total: 1 }), rc({ total: 2 }), rc({ total: 30 })], '30'],
  ['and a drop back to zero shows a dash', [rc({ total: 5 }), rc({ total: 0 })], '—'],
]) {
  const area = { left: 0, right: 200, top: 0, bottom: 200 };
  // FROZEN AS ONE VALUE: the live plugin is DRIVEN (`afterDraw`) before its
  // recorder is read, so freezing the read alone would leave the driver running.
  const liveCalls = G.value(name + ' live centre', () => {
    const L = liveRun(payloads);
    const a = recorder();
    L.w.made[0].plugins[0].afterDraw({ ctx: a.ctx, chartArea: area });
    return a.calls;
  });
  const P = portRun(payloads);
  const b = recorder();
  P.m.drawDonutCentre({ ctx: b.ctx, chartArea: area });
  cmp(name, JSON.stringify(liveCalls), JSON.stringify(b.calls));
  // RE-AIMED AT THE PORT. This asked whether the LIVE centre drew the expected
  // total — but the whole point of the block, stated in the comment above it, is
  // that a port which reads the total at construction time shows a stale figure
  // forever. That is a claim about the PORT, so the port is what must be asked.
  assert.ok(b.calls.some((c) => c.includes(JSON.stringify(want))),
    'the centre did not draw ' + want + ' after ' + payloads.length + ' payloads: ' + b.calls.join(' '));
}

// ── the centre text, driven through the plugin ─────────────────────────────
for (const total of [0, 1, 7, 42, 1000]) {
  const area = { left: 0, right: 200, top: 0, bottom: 200 };
  const liveCalls = G.value('donut centre live at total=' + total, () => {
    const L = liveRun([rc({ static: 1, total })]);
    const a = recorder();
    L.w.made[0].plugins[0].afterDraw({ ctx: a.ctx, chartArea: area });
    return a.calls;
  });
  const P = portRun([rc({ static: 1, total })]);
  const b = recorder();
  P.m.drawDonutCentre({ ctx: b.ctx, chartArea: area });
  cmp('the donut centre at total=' + total, JSON.stringify(liveCalls), JSON.stringify(b.calls));
}

// ── BELIEVABILITY, RE-AIMED AT THE PORT ────────────────────────────────────
//
// Both blocks asked the LIVE donut whether it has an Other slice and whether the
// centre draws the total. Those are properties the PORT has to keep — the live
// donut's behaviour stops mattering the moment the reference goes — so the port
// is what they now ask.
{
  const P = portRun([rc({ connect: 2, static: 3, dynamic: 0, bgp: 0, ospf: 0, total: 12 })]);
  const cfg = P.w.made[0];
  assert.equal(cfg.data.labels.length, 5, 'the donut has ' + cfg.data.labels.length + ' slices, want 5 with an Other');
  assert.equal(cfg.data.datasets[0].data[4], 7, 'the Other slice is ' + cfg.data.datasets[0].data[4]);
  const a = recorder();
  P.m.drawDonutCentre({ ctx: a.ctx, chartArea: { left: 0, right: 100, top: 0, bottom: 100 } });
  assert.ok(a.calls.some((c) => c.startsWith('fillText("12"')), 'the centre did not draw the total: ' + a.calls.join(' '));
}
{
  const P = portRun([rc({ static: 0, total: 0 })]);
  const a = recorder();
  P.m.drawDonutCentre({ ctx: a.ctx, chartArea: { left: 0, right: 100, top: 0, bottom: 100 } });
  assert.ok(a.calls.some((c) => c.includes('\\u2014') || c.includes('—')),
    'a total of zero should draw an em dash, not a nought: ' + a.calls.join(' '));
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('routing-cards-check: %d comparisons identical', checked);
