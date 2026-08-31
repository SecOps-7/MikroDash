'use strict';
/**
 * The ROUTING page, live against ported. Built on `tools/lib/lift.js`.
 *
 * Two tables in one page — BGP peers and the route table — behind a tab bar,
 * plus three filter selects on each and a summary.
 *
 * ── THE TABS ARE DELEGATED, WHICH THE SHIM HAD TO LEARN ────────────────────
 *
 * The tab bar is one `click` listener on the page that walks up from the event
 * target with `closest('[data-rttab]')`. A shim whose `closest` returns null
 * leaves every click unhandled, so the tabs are unreachable and everything
 * behind them uncompared — the same shape that hid the Router Users defect, one
 * layer down.
 *
 * The attribute here IS the one in the markup (`data-rttab`, panes
 * `.rttab-panel`), checked first because that is exactly where the previous page
 * was wrong.
 *
 * WHAT IT CANNOT SEE: the donut canvas, layout, focus.
 *
 * ── ARROW-KEY NAVIGATION IS DRIVEN NOW ──────────────────────────────────────
 *
 * This list used to include it, with the reason: "driving it means synthesising
 * keyboard events with a focus model, which this shim does not have". That was
 * wrong. The handler finds its position from `rtTab` — the module's own state —
 * not from `document.activeElement`; only the trailing `next.focus()` wanted a
 * stub, and focus is not compared.
 *
 * What actually blocked it was narrower and duller: the click path reads the
 * strip as `.stab` and the arrow path as `[data-rttab]`, and the shim answered
 * only the first. The arrow query came back empty, the handler found its tab at
 * index -1 and returned. Declaring both selectors with equal-looking arrays does
 * NOT fix it — that builds two sets of nodes, so one path marks a button active
 * and the other reports it is not. The shim takes an ALIAS (`'[data-rttab]':
 * '.stab'`) so both names reach the same four buttons.
 *
 * Seven cases; five mutations killed, including swapped arrows, a dropped
 * wraparound and a removed listener.
 *
 * The `|| 'routes'` fallback at the arrow site still survives, and that is
 * correct rather than uncovered: `[data-rttab]` only matches elements that HAVE
 * the attribute, so `getAttribute` cannot return null there. The live app has no
 * such fallback. It is one of this port's invented guards — unreachable, so
 * harmless, and worth knowing about rather than defending with a case that
 * cannot exist.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/routing-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/routing-page-check.js --freeze
const G = L.golden('routing-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  contains: "var search  = $('rtSearch')",
  must: ['rtTbody', 'rtRoutesTbody', 'data-rttab'],
  mustNot: ['Bridges page', 'CAPsMAN page', 'Router Users page'],
}));
// `page-routing` is EXCLUDED from the comparison and CLAIMED as covered, which
// are two different questions. Its innerHTML is the whole page, so comparing it
// would drown every other difference — but the tab PANELS are found through it,
// so a wrong id leaves them unswitched, and misspelling it fails this gate.
// Measured on 2026-08-25, the same way `page-bridges` was.
const PAGE_ID = 'page-routing';
const IDS = G.value('IDS', () => L.idsFor(src, iife)).filter((id) => id !== PAGE_ID);
const COMPARED = IDS.filter((id) => id !== 'rtDonutCanvas');
if (process.argv.includes('--ids')) {
  console.log(JSON.stringify(COMPARED.concat([PAGE_ID]))); process.exit(0);
}

const ENTRY = path.join(ROOT, 'testdata', '.rt-entry.ts');
fs.writeFileSync(ENTRY, "export { initRoutingPage } from '../web/src/pages/routing.js';\n");
const OUT = path.join(ROOT, 'testdata', '.rt-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const PANES = ['rttab-routes', 'rttab-bgp'];

function newDoc() {
  // SCOPED, because the page scopes them: `bar.querySelectorAll('.stab')` and
  // `page.querySelectorAll('.rttab-panel')`. The live comment says why — the
  // switcher this was modelled on queries document-wide, which is safe only
  // while exactly one such strip exists.
  return makeDoc([...IDS, 'page-routing'], {
    elementQuery: {
      // A THIRD button carrying a value neither tab uses. `setRtTab` falls back
      // to 'routes' for anything it does not know, and without a bogus value
      // that fallback is unreachable — nothing else can produce one.
      // FOUR buttons: the two real tabs, one carrying a value neither tab uses,
      // and one carrying NO value at all. They reach different fallbacks —
      // `setRtTab` maps an unknown KEY to 'routes', while `|| 'routes'` at the
      // click site catches a NULL attribute. A bogus string cannot reach the
      // second, which is why it survived until this existed.
      // BOTH SELECTORS THE PAGE USES, answering the same four buttons. The
      // click path walks up with `closest('[data-rttab]')` and reads `.stab`;
      // the ARROW-KEY path queries `[data-rttab]` directly. A shim answering
      // only `.stab` returns [] for the arrow query, the handler finds its
      // current tab at index -1 and returns — so arrow navigation could not be
      // driven at all, which is what this gate recorded as needing "a focus
      // model". It does not: the handler reads its position from `rtTab`, not
      // from `document.activeElement`. Only the trailing `next.focus()` needs a
      // stub, and focus is not compared.
      rtTabBar: {
        '.stab': ['routes', 'bgp', 'nonsense', { value: undefined }],
        '[data-rttab]': '.stab',   // the SAME buttons — see the shim's alias note
      },
      'page-routing': { '.rttab-panel': PANES.map((id) => ({ id })) },
    },
    queryAttr: { '.stab': 'data-rttab' },
  });
}

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of PANES) out[id] = n[id] ? { active: n[id].classList.contains('active') } : null;
  // THE TAB BUTTONS THEMSELVES. Which one is lit is what a viewer sees, and
  // without it a mutation to the attribute the ACTIVE-marking loop reads
  // survives: the pane switches correctly while the button strip does not
  // follow, so the page shows BGP with Routes still highlighted.
  out.tabButtons = doc.nodes.rtTabBar.querySelectorAll('.stab').map((b) => ({
    v: b.getAttribute('data-rttab'),
    active: b.classList.contains('active'),
    sel: b.attributes && b.attributes['aria-selected'],
  }));
  for (const id of COMPARED.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent, c: n[id].className } : null;
  }
  return JSON.stringify(out);
};

function drive(doc, fire, payload, o) {
  for (const [id, v] of Object.entries(o.selects || {})) {
    if (doc.nodes[id]) doc.nodes[id].value = v;
  }
  fire('routing:update', payload);
  // The tab bar is DELEGATED: one click listener that walks up with `closest`.
  if (o.tabIndex !== undefined) {
    const btn = doc.nodes.rtTabBar.querySelectorAll('.stab')[o.tabIndex];
    if (!btn) throw new Error('no header cell at index ' + o.tabIndex);
    btn.closest = (sel) => (sel === '[data-rttab]' ? btn : null);
    doc.nodes.rtTabBar.fire('click', { target: btn });
  }
  if (o.tab) {
    // The listener is on `rtTabBar` and walks up from the event target with
    // `closest('[data-rttab]')` — so the click is fired ON THE BAR with a target
    // that answers `closest`, which is what a real click does.
    const btn = doc.nodes.rtTabBar.querySelectorAll('.stab')
      .find((n) => n.getAttribute('data-rttab') === o.tab);
    if (!btn) throw new Error('no tab button for ' + o.tab);
    btn.closest = (sel) => (sel === '[data-rttab]' ? btn : null);
    doc.nodes.rtTabBar.fire('click', { target: btn });
  }
  for (const [id, v] of Object.entries(o.selectsAfter || {})) {
    if (doc.nodes[id]) { doc.nodes[id].value = v; doc.nodes[id].fire('change'); }
  }
  // ── ARROW KEYS ALONG THE STRIP ──────────────────────────────────────────
  //
  // Fired on the BAR, which is where the listener is. Each key is a separate
  // dispatch so wraparound is reached by pressing Left on the first tab rather
  // than by constructing a state the page cannot get into on its own.
  //
  // AFTER the click block, so a case combining the two starts from the tab the
  // click selected — the point of `arrows after a click`.
  for (const key of o.keys || []) {
    doc.nodes.rtTabBar.fire('keydown', { key, preventDefault() {} });
  }

};

function liveRun(payload, opts) {
  const o = opts || {};
  const doc = newDoc();
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {}, window: {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
    Chart: function () { return { destroy() {}, update() {} }; },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function _sortMul('),
    L.whole(src, 'function _renderSortHeader('),
    L.whole(src, 'function resRow('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    L.declare(L.fileScopeEls(src, iife)),
    '(function(){' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['routing:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  drive(doc, (ev, p) => {
    if (!handlers[ev]) throw new Error('nothing subscribes ' + ev);
    handlers[ev](p);
  }, payload, o);
  return snap(doc);
}

function portRun(payload, opts) {
  const o = opts || {};
  const doc = newDoc();
  const handlers = {};
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  const prevRaf = globalThis.requestAnimationFrame;
  const prevChart = globalThis.Chart;
  globalThis.window = {};
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  globalThis.requestAnimationFrame = ((fn) => { fn(); return 0; });
  globalThis.Chart = function () { return { destroy() {}, update() {} }; };
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initRoutingPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      drive(doc, (ev, p) => {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](p);
      }, payload, o);
      return snap(doc);
    });
  } finally {
    globalThis.Chart = prevChart;
    globalThis.requestAnimationFrame = prevRaf;
    globalThis.setTimeout = prevST;
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
  }
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    for (const k of Object.keys(A)) {
      const x = JSON.stringify(A[k]), y = JSON.stringify(B[k]);
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k,
        String(x).slice(0, 320), String(y).slice(0, 320));
    }
  }
}

// THE EMITTED SHAPE, not the collector's internal one. `src/collectors/routing.js:311`
// maps `({ _id, _raw, flags, ...r }) => ({ ...r, id: _id })`, so `flags` is
// STRIPPED before the payload leaves — there is no `dynamic` and no `disabled`
// on a route, and there never were `pref` or `scope`.
//
// This fixture carried all four until 2026-08-24, and two CASES were built on
// them: "a dynamic route" and "a disabled route" set a key nothing reads and
// compared two renderers both drawing an ordinary route. They are gone rather
// than renamed, because there is nothing on this payload to rename them to.
const R = (o) => Object.assign({
  id: '*1', dst: '0.0.0.0/0', gateway: '198.51.100.1', distance: 1,
  active: true, comment: '', type: 'static', protocol: 'static', family: 'ip4',
}, o);
// THE REAL KEY NAMES, taken from the emit site (src/collectors/routing.js:265).
// This said `remoteAddress / localAs / uptime / prefixCount` until 2026-08-24
// and the payload has never carried any of them: they are `remoteAddr`,
// `uptimeSec` and `prefixes`, with no local-AS at all. Both renderers read the
// real ones, so every peer in this gate had no address, no uptime and no prefix
// count, and agreed about it.
//
// `fixture-key-audit` found `localAs` and `prefixCount`. It did NOT find
// `remoteAddress` or `uptime`, because both strings occur elsewhere in the two
// trees and its test is "appears nowhere at all" — see the limit in its header.
// Those two were found by reading the emit site after the other two pointed
// here, which is the honest description of how this was caught.
const PEER = (o) => Object.assign({
  key: 'p1', peerType: 'ebgp', name: 'upstream', description: '',
  remoteAddr: '203.0.113.1', remoteAs: 64512, state: 'established',
  uptimeSec: 12000, prefixes: 812, prefixHistory: [800, 806, 812],
  updatesSent: 12, updatesRecv: 340, lastError: '',
  holdTime: 180, keepalive: 60, flapping: false,
}, o);
const P = (o) => Object.assign({ routes: [], peers: [], summary: {} }, o);

const CASES = {
  'nothing': [P({}), {}],
  'one route': [P({ routes: [R({})] }), {}],
  'several routes': [P({ routes: [R({}), R({ id: '*9', dst: '10.0.0.0/8' })] }), {}],
  // (the two cases that were here tested `dynamic` and `disabled`, which this
  //  payload does not carry — see the R() header)
  'an inactive route': [P({ routes: [R({ active: false })] }), {}],
  'a route with a comment': [P({ routes: [R({ comment: 'to the DC' })] }), {}],
  'markup in a route comment': [P({ routes: [R({ comment: '<b>x</b>' })] }), {}],
  'a v6 route': [P({ routes: [R({ family: 'ip6', dst: '2001:db8::/32' })] }), {}],
  'no gateway': [P({ routes: [R({ gateway: '' })] }), {}],
  'distance zero': [P({ routes: [R({ distance: 0 })] }), {}],
  'a bgp-typed route': [P({ routes: [R({ type: 'bgp' })] }), {}],
  // Peers, on the other tab.
  'one peer': [P({ peers: [PEER({})] }), { tab: 'bgp' }],
  'several peers': [P({ peers: [PEER({}), PEER({ id: '*3', name: 'backup' })] }), { tab: 'bgp' }],
  'a peer that is not established': [P({ peers: [PEER({ state: 'idle' })] }), { tab: 'bgp' }],
  'a peer with no uptime': [P({ peers: [PEER({ uptime: '' })] }), { tab: 'bgp' }],
  'a peer with no prefixes': [P({ peers: [PEER({ prefixes: 0 })] }), { tab: 'bgp' }],
  'peers with the routes tab selected': [P({ peers: [PEER({})], routes: [R({})] }), {}],
  'routes with the bgp tab selected': [P({ peers: [PEER({})], routes: [R({})] }), { tab: 'bgp' }],
  'switching to bgp and back': [P({ peers: [PEER({})], routes: [R({})] }), { tab: 'routes' }],
  'an unknown tab value falls back to routes': [P({ peers: [PEER({})], routes: [R({})] }), { tab: 'nonsense' }],
  'a button with NO tab attribute falls back too': [P({ peers: [PEER({})], routes: [R({})] }), { tabIndex: 3 }],

  // ── ARROW-KEY NAVIGATION ALONG THE STRIP ─────────────────────────────────
  //
  // Recorded here as unreachable — "driving it means synthesising keyboard
  // events with a focus model, which this shim does not have". It does not need
  // one: the handler finds its position from `rtTab`, the module's own state,
  // not from `document.activeElement`. Only the trailing `next.focus()` wanted a
  // stub, and focus is not compared.
  //
  // FOUR buttons are in the strip, two of them bogus, so the index arithmetic
  // walks over values `setRtTab` does not know — which is where a wrong
  // wraparound shows up as the wrong PANE rather than as a quiet no-op.
  'arrow right from routes': [P({ peers: [PEER({})], routes: [R({})] }), { keys: ['ArrowRight'] }],
  'arrow right twice': [P({ peers: [PEER({})], routes: [R({})] }), { keys: ['ArrowRight', 'ArrowRight'] }],
  'arrow LEFT from the first tab wraps to the last':
    [P({ peers: [PEER({})], routes: [R({})] }), { keys: ['ArrowLeft'] }],
  'arrow right all the way round': [P({ peers: [PEER({})], routes: [R({})] }),
    { keys: ['ArrowRight', 'ArrowRight', 'ArrowRight', 'ArrowRight'] }],
  'arrow right then left returns': [P({ peers: [PEER({})], routes: [R({})] }),
    { keys: ['ArrowRight', 'ArrowLeft'] }],
  'a key that is not an arrow does nothing':
    [P({ peers: [PEER({})], routes: [R({})] }), { keys: ['Enter', 'a', 'ArrowUp'] }],
  'arrows after a click start from the clicked tab':
    [P({ peers: [PEER({})], routes: [R({})] }), { tab: 'bgp', keys: ['ArrowRight'] }],
  // Filters.
  'filter routes to active': [P({ routes: [R({}), R({ id: '*9', active: false })] }),
    { selects: { rtRouteSelActive: 'active' } }],
  'filter routes by family': [P({ routes: [R({}), R({ id: '*9', family: 'ip6' })] }),
    { selects: { rtRouteSelFamily: 'ip6' } }],
  'filter routes by type': [P({ routes: [R({}), R({ id: '*9', type: 'bgp' })] }),
    { selects: { rtRouteSelType: 'bgp' } }],
  'search routes': [P({ routes: [R({}), R({ id: '*9', dst: '10.0.0.0/8' })] }),
    { selects: { rtRouteSearch: '10.0' } }],
  'search matching nothing': [P({ routes: [R({})] }), { selects: { rtRouteSearch: 'zzz' } }],
  // Escaping.
  'markup in a destination': [P({ routes: [R({ dst: '<img src=x>' })] }), {}],
  'a quote in a peer name': [P({ peers: [PEER({ name: 'a"b' })] }), { tab: 'bgp' }],
};

for (const [name, [payload, opts]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(payload, opts)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, opts); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:5', () => liveRun(P({ routes: [R({})] }), {})));
  assert.match(s.rtRoutesTbody.h, /0\.0\.0\.0\/0/, 'the live route table rendered no row');
}
{
  // THE TAB REALLY SWITCHES. If it did not, everything behind it would compare
  // as two unchanged pages — which is what made the Router Users defect
  // invisible until the tabs could be driven.
  const routes = JSON.parse(G.live('auto:4', () => liveRun(P({ peers: [PEER({})], routes: [R({})] }), {})));
  const bgp = JSON.parse(G.live('auto:3', () => liveRun(P({ peers: [PEER({})], routes: [R({})] }), { tab: 'bgp' })));
  assert.notEqual(JSON.stringify(routes['rttab-bgp']), JSON.stringify(bgp['rttab-bgp']),
    'the bgp pane did not change when its tab was clicked — the tabs are not being driven');
  assert.match(bgp.rtTbody.h, /upstream/, 'the peer table rendered no row on the bgp tab');
}
{
  // The active filter really removes rows.
  const all = JSON.parse(G.live('auto:2', () => liveRun(P({ routes: [R({}), R({ id: '*9', dst: '10.0.0.0/8', active: false })] }), {})));
  const act = JSON.parse(G.live('auto:1', () => liveRun(P({ routes: [R({}), R({ id: '*9', dst: '10.0.0.0/8', active: false })] }),
    { selects: { rtRouteSelActive: 'active' } })));
  assert.ok(all.rtRoutesTbody.h.length > act.rtRoutesTbody.h.length,
    'the active filter removed nothing');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('routing-page-check: %d cases identical', checked);
