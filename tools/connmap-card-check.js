'use strict';
/**
 * The Connections Map card, live against ported.
 *
 * ── THE ANIMATION IS RANDOMISED, SO THE RNG IS INJECTED ─────────────────────
 *
 * Each comet's duration is jittered and its `begin` is a negative offset, both
 * from `Math.random()`. Both sides are driven by the SAME seeded generator —
 * the live one through its sandbox's `Math`, the port through its `rng`
 * parameter — so the output is comparable without pretending the randomness is
 * not there.
 *
 * ── AND THE INTERESTING PART IS WHAT IS *NOT* REBUILT ───────────────────────
 *
 * An arc is rebuilt only when its path changes, because rebuilding restarts the
 * comet. So the cases are SEQUENCES: the same countries with different counts
 * must leave the arc nodes ALONE, and a country dropping out must remove its
 * arc while merely blanking its label. Comparing one render could not see any of
 * that — it is all about node identity across updates.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/connmap-card-check.js
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
const G = LIFT.golden('connmap-card-check');
// FROZEN — a lifter called INLINE at the vm call site rather than assigned to a
// const, which is why no pattern caught it.
let initSrc;
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

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
// FROZEN. An array built by MAPPING a lifter over names — a fourth syntactic
// form. The joined result is executed by `vm`, so the text is what must survive.
const pieces = G.value('the lifted map pieces',
  () => ['_dcMapMakeArcD', '_dcMapUpdateHighlights', '_dcMapUpdateArcs',
    '_dcMapUpdateLabels', '_dcMapApply'].map((n) => grab('function ' + n + '(', '\n  }', n)));
initSrc = G.value('initSrc', () => grab('function _dcMapInit()', '\n  }', '_dcMapInit'));
if (!initSrc || initSrc.length < 20) throw new Error('the recorded _dcMapInit is empty');
if (!Array.isArray(pieces) || pieces.length !== 5 || pieces.some((x) => !x || x.length < 20)) {
  throw new Error('the recorded map pieces are empty — the golden is broken');
}
if (LIFT.hasReference(ROOT)) assert.ok(pieces[2].includes('animateMotion'), 'the arc slice lost its animation');
if (LIFT.hasReference(ROOT)) assert.ok(pieces[3].includes('map-label'), 'the label slice lost its class');

const ENTRY = path.join(ROOT, 'testdata', '.connmap-entry.ts');
fs.writeFileSync(ENTRY, "export { createConnMap } from '../web/src/pages/dashboard-card-map.js';\n");
const OUT = path.join(ROOT, 'testdata', '.connmap-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const CENTROIDS = { GB: [480, 120], US: [180, 160], DE: [500, 130], AU: [820, 380], BR: [300, 330] };
const LOCAL = 'GB';

// A seeded generator, shared by both sides so the jitter is reproducible.
function makeRng() {
  let seed = 987654321;
  return () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; };
}

// A fake SVG tree that records identity, so "was this node rebuilt?" is askable.
function makeDom() {
  let nextId = 1;
  const mk = (tag) => {
    const n = {
      _uid: nextId++, tagName: tag, attrs: {}, children: [], parentNode: null,
      classes: new Set(),
      classList: { add: (c) => n.classes.add(c), remove: (...c) => c.forEach((x) => n.classes.delete(x)) },
      setAttribute(k, v) { n.attrs[k] = String(v); },
      getAttribute: (k) => (k in n.attrs ? n.attrs[k] : null),
      appendChild(c) {
        if (c._frag) { c.children.forEach((g) => { g.parentNode = n; n.children.push(g); }); c.children = []; return c; }
        c.parentNode = n; n.children.push(c); return c;
      },
      removeChild(c) { n.children = n.children.filter((x) => x !== c); c.parentNode = null; return c; },
      querySelector(sel) { return n.children.find((c) => c.tagName === sel) || null; },
      addEventListener() {},
      get firstChild() { return n.children[0] || null; },
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      set innerHTML(v) { n._h = v; }, get innerHTML() { return n._h || ''; },
      style: {}, dataset: {},
      parentElement: { getBoundingClientRect: () => ({ left: 0, top: 0 }) },
    };
    return n;
  };
  const svg = mk('svg'); svg.id = 'dc-worldMap';
  const tip = mk('div'); tip.id = 'dc-mapTooltip';
  const byId = new Map([['dc-worldMap', svg], ['dc-mapTooltip', tip]]);
  return {
    byId, mk, svg,
    doc: {
      getElementById: (id) => byId.get(id) || null,
      createElementNS: (_ns, tag) => mk(tag),
      createElement: (tag) => mk(tag),
      createDocumentFragment: () => { const f = mk('#fragment'); f._frag = true; return f; },
      addEventListener() {},
    },
  };
}

// What the map looks like, by node identity as well as content.
function snap(d) {
  const svg = d.svg;
  const [countryLayer, arcLayer, lblLayer] = svg.children;
  const arcs = (arcLayer ? arcLayer.children : []).map((g) => {
    const p = g.children.find((c) => c.tagName === 'path');
    const circle = g.children.find((c) => c.tagName === 'circle');
    const anim = circle ? circle.children[0] : null;
    return {
      uid: g._uid,
      d: p ? p.attrs.d : null, cls: p ? p.attrs.class : null,
      r: circle ? circle.attrs.r : null, comet: circle ? circle.attrs.class : null,
      dur: anim ? anim.attrs.dur : null, begin: anim ? anim.attrs.begin : null,
      animPath: anim ? anim.attrs.path : null,
    };
  });
  const labels = (lblLayer ? lblLayer.children : []).map((t) => ({
    uid: t._uid, x: t.attrs.x, y: t.attrs.y, text: t.textContent, cls: t.attrs.class,
  }));
  const countries = (countryLayer ? countryLayer.children : []).map((p) => ({
    cc: p.attrs['data-cc'], classes: [...p.classes].sort(),
  }));
  return JSON.stringify({ arcs, labels, countries });
}

function liveSide() {
  const d = makeDom();
  const rng = makeRng();
  const ctx = {
    Object, String, Number,
    Math: Object.assign(Object.create(Math), { random: rng }),
    document: d.doc,
    dcEl: (id) => d.byId.get(id) || null,
    esc: (s) => String(s),
    DC_CC_NAMES: {},
    window: { _worldMapCentroids: CENTROIDS, _worldMapLocalCC: LOCAL, _worldMapPathDs: { GB: 'M0', US: 'M1', DE: 'M2', AU: 'M3', BR: 'M4' } },
    _dcMapPathEls: {}, _dcMapArcEls: {}, _dcMapLabelEls: {},
    _dcMapArcLayer: null, _dcMapLblLayer: null, _dcMapCounts: {},
    _dcMapReady: false, _dcMapPending: null,
  };
  vm.createContext(ctx);
  vm.runInContext(pieces.join('\n') + '\n' + initSrc, ctx);
  return { d, ctx };
}
function portSide() {
  const d = makeDom();
  const rng = makeRng();
  globalThis.document = d.doc;
  globalThis.window = { _worldMapCentroids: CENTROIDS, _worldMapLocalCC: LOCAL, _worldMapPathDs: { GB: 'M0', US: 'M1', DE: 'M2', AU: 'M3', BR: 'M4' } };
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT).createConnMap(rng);
  return { d, m };
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 3) {
    const A = JSON.parse(a), B = JSON.parse(b);
    shout('DIFF %s', what);
    for (const k of Object.keys(A)) {
      if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
        shout('  %s\n    live: %s\n    port: %s', k,
          JSON.stringify(A[k]).slice(0, 300), JSON.stringify(B[k]).slice(0, 300));
      }
    }
  }
}

const C = (cc, count) => ({ cc, count });
const SCRIPTS = {
  'one destination': [[C('US', 10)]],
  'several destinations': [[C('US', 10), C('DE', 5), C('AU', 1)]],
  'the local country is skipped': [[C('GB', 99), C('US', 10)]],
  'a country with no centroid': [[C('ZZ', 10), C('US', 5)]],
  'no destinations at all': [[]],
  'hot and cold': [[C('US', 100), C('DE', 50), C('AU', 49)]],
  // Sequences: what is REBUILT and what is left alone.
  'the same countries with different counts leave the arcs alone': [
    [C('US', 10), C('DE', 5)],
    [C('US', 99), C('DE', 1)],
  ],
  'a country dropping out removes its arc and blanks its label': [
    [C('US', 10), C('DE', 5)],
    [C('US', 10)],
  ],
  'a country returning gets a new arc': [
    [C('US', 10), C('DE', 5)],
    [C('US', 10)],
    [C('US', 10), C('DE', 7)],
  ],
  'everything drops out': [[C('US', 10), C('DE', 5)], []],
  'three rounds of churn': [
    [C('US', 1)], [C('DE', 2)], [C('AU', 3), C('BR', 4)],
  ],
};

for (const [name, rounds] of Object.entries(SCRIPTS)) {
  const L = liveSide(), P = portSide();
  L.ctx._dcMapInit();
  P.m.init();
  rounds.forEach((countries, i) => {
    L.ctx._dcMapApply(countries);
    P.m.apply(countries);
    cmp(name + ' round ' + (i + 1), snap(L.d), snap(P.d));
  });
}

// ── RE-INIT, which the Add panel makes reachable ───────────────────────────
//
// The card can be removed and re-added, so `init` runs twice on the same <svg>.
// It clears the element first; without that the second run stacks a whole second
// map on the first. Every script above calls init ONCE, so a mutation removing
// the clear survived them all.
{
  const L = liveSide(), P = portSide();
  L.ctx._dcMapInit(); P.m.init();
  L.ctx._dcMapApply([C('US', 10)]); P.m.apply([C('US', 10)]);
  cmp('before re-init', snap(L.d), snap(P.d));
  L.ctx._dcMapInit(); P.m.init();
  cmp('after re-init', snap(L.d), snap(P.d));
  L.ctx._dcMapApply([C('DE', 4)]); P.m.apply([C('DE', 4)]);
  cmp('after re-init and a new payload', snap(L.d), snap(P.d));
  const s = JSON.parse(snap(L.d));
  assert.equal(s.countries.length, 5,
    'the live map has ' + s.countries.length + ' countries after two inits — it should clear ' +
    'the svg, not stack a second map');
}

// ── the pending mechanism ──────────────────────────────────────────────────
{
  // A payload before init must be held and applied on init, not dropped.
  const L = liveSide(), P = portSide();
  L.ctx._dcMapPending = [C('US', 7)];
  P.m.onConnUpdate([C('US', 7)]);
  L.ctx._dcMapInit();
  P.m.init();
  cmp('a payload held until the map is ready', snap(L.d), snap(P.d));
  assert.ok(JSON.parse(snap(L.d)).arcs.length > 0, 'the live map dropped its pending payload');
}

// ── believability ──────────────────────────────────────────────────────────
{
  const L = liveSide();
  L.ctx._dcMapInit();
  L.ctx._dcMapApply([C('US', 100), C('DE', 10)]);
  const s = JSON.parse(snap(L.d));
  assert.equal(s.countries.length, 5, 'the live map drew ' + s.countries.length + ' countries');
  assert.equal(s.arcs.length, 2, 'the live map drew ' + s.arcs.length + ' arcs');
  assert.ok(s.arcs[0].dur && /s$/.test(s.arcs[0].dur), 'no animation duration: ' + s.arcs[0].dur);
  assert.ok(/^-/.test(s.arcs[0].begin), 'the begin offset should be negative: ' + s.arcs[0].begin);
  assert.ok(s.arcs.some((a) => /hot/.test(a.cls)), 'no arc was marked hot');
  assert.equal(s.labels.length, 2, 'the live map drew ' + s.labels.length + ' labels');
}
{
  // The identity claim, stated: an unchanged path keeps its node.
  const L = liveSide();
  L.ctx._dcMapInit();
  L.ctx._dcMapApply([C('US', 10)]);
  const before = JSON.parse(snap(L.d)).arcs[0].uid;
  L.ctx._dcMapApply([C('US', 999)]);
  const after = JSON.parse(snap(L.d)).arcs[0].uid;
  assert.equal(before, after,
    'the live map rebuilt an arc whose path did not change — the comet would restart on every tick');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('connmap-card-check: %d comparisons identical', checked);
