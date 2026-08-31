'use strict';
/**
 * The Connection Flow diagram, live against ported.
 *
 * `connections-sankey.ts` is 291 lines and NOTHING drove it. `connflow-card-check`
 * names `renderSankey` and REPLACES it with a recorder -- deliberately, because
 * that gate is about the wrapper's arguments -- so the renderer itself had never
 * been compared against anything.
 *
 * ---- IT BUILDS ELEMENTS, NOT MARKUP ---------------------------------------
 *
 * Both sides use `createElementNS` + `setAttribute` + `appendChild`, so there is
 * no innerHTML to read. A shim that records the tree and serialises it compares
 * them exactly: tag, attributes in insertion order, text, children. That is the
 * same argument `map-geometry-check` makes for arc paths -- geometry is a string
 * once you write it down -- extended one step to the elements carrying it.
 *
 * ---- THE LIVE SIDE IS DRIVEN THROUGH ITS OWN DOOR --------------------------
 *
 * The block exposes `window._connSankeyRender(srcs, dsts)` for the country
 * filter, and that is the entry the page itself uses. The port's `renderSankey`
 * takes its targets as arguments where the live block closes over
 * `$('sankeySvg')` -- a mechanism difference the port rules allow -- so each side
 * is given the SAME two nodes by the route it expects.
 *
 * ---- WHAT IS NOT COMPARED --------------------------------------------------
 *
 * The THROTTLE and the fingerprint skip (`SANKEY_THROTTLE`, `_sankeyFp`) belong
 * to the live block's `conn:update` handler, not to the renderer, and the port
 * puts them elsewhere. This drives the render directly on both sides, so a case
 * here is one FRAME, not a sequence.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/sankey-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/sankey-check.js --freeze
const G = L.golden('sankey-check');
const { makeTree, serialise } = require('./lib/tree-shim.js');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

// The page's own ids, driven here -- see `element-coverage-audit`.
const COVERS = ['sankeySvg', 'sankeyEmpty'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const region = G.value('region', () => L.region(src, {
  banner: '// ── Sankey: Connection Flow',
  must: ['_connSankeyRender', 'linkPath', 'sankeySvg'],
  mustNot: ['CAPsMAN', 'DNS page', 'Queues page'],
}));
// BELIEVABILITY OF THE LIFT: a short slice would still evaluate and would expose
// a renderer that drew nothing, and every case would compare two empty trees.
assert.match(region, /createElementNS/, 'the slice builds no elements');
assert.match(region, /nodeColour/, 'the slice carries no colour rule');

const ENTRY = path.join(ROOT, 'testdata', '.sk-entry.ts');
fs.writeFileSync(ENTRY, "export { renderSankey } from '../web/src/pages/connections-sankey.js';\n");
const OUT = path.join(ROOT, 'testdata', '.sk.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

/**
 * The document this comparison runs against.
 *
 * The element recorder lives in `tools/lib/tree-shim.js` now — it was written
 * here first and extracted on 2026-08-25 when the live country list moved to the
 * same build-and-sync shape, which needs the same thing. `dom-shim` explicitly
 * will not do this: its node building exists to let a renderer FINISH, not to be
 * compared.
 *
 * SERIALS ARE OFF. This gate compares two implementations at one instant, and
 * they number their nodes differently for reasons that are not about the
 * picture. A gate comparing one implementation across two FRAMES wants them on —
 * that is how "did the redraw reuse the row" becomes a question you can ask.
 */
function makeSvgDom() {
  const { mk } = makeTree();
  const svg = mk('svg');
  svg.id = 'sankeySvg';
  // WIDTH COMES FROM THE PARENT, and it is an input to every coordinate the
  // diagram computes: `targetSvg.parentElement.clientWidth || 600` on the live
  // side. Both sides get the same box, or every path differs for a reason that
  // belongs to the harness rather than to either implementation.
  svg.parentElement = { clientWidth: 900, clientHeight: 320 };
  const empty = mk('div');
  empty.id = 'sankeyEmpty';
  return { mk, svg, empty };
}

const ser = (n) => serialise(n, { serials: false });

const snap = (dom) => JSON.stringify({
  svg: ser(dom.svg),
  svgStyle: dom.svg.style,
  emptyStyle: dom.empty.style,
  emptyText: dom.empty._text,
}, null, 1);

function liveRun(srcs, dsts, again) {
  const dom = makeSvgDom();
  const win = {};
  const ctx = {
    Math, Array, Object, Number, String, JSON, isFinite, parseInt, parseFloat,
    document: {
      createElementNS: (_ns, tag) => dom.mk(tag),
      createElement: (tag) => dom.mk(tag),
      getElementById: (id) => (id === 'sankeySvg' ? dom.svg
        : id === 'sankeyEmpty' ? dom.empty : null),
      addEventListener() {},
      // The block measures its container before it draws. There is no layout
      // here, so the query answers a fixed box — the WIDTH is an input to the
      // geometry, and both sides must be given the same one or every path
      // differs for a reason that is the harness's.
      querySelector: () => ({ clientWidth: 900, clientHeight: 320,
        getBoundingClientRect: () => ({ width: 900, height: 320, left: 0, top: 0 }) }),
      querySelectorAll: () => [],
    },
    // The block also listens for `resize` to re-render at the new width. That is
    // layout, not geometry, and this gate drives the render directly — so the
    // listener is recorded and never fired.
    window: Object.assign(win, { addEventListener() {}, removeEventListener() {}, innerWidth: 1200 }),
    socket: { on() {}, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
    ResizeObserver: function () { return { observe() {}, disconnect() {} }; },
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    '(function () {' + region + '}());',
  ].join('\n'), ctx);
  assert.equal(typeof win._connSankeyRender, 'function',
    'the live block did not expose _connSankeyRender -- the lift has broken');
  win._connSankeyRender(srcs, dsts);
  // A SECOND render into the same diagram. `svg.innerHTML = ''` at the top is
  // the only thing stopping the second frame drawing on top of the first, and a
  // gate whose every case starts from a fresh element can never see it — the
  // same blind spot the DHCP server filter had.
  if (again) win._connSankeyRender(again[0], again[1]);
  return snap(dom);
}

function portRun(srcs, dsts, again) {
  const dom = makeSvgDom();
  const prev = globalThis.document;
  globalThis.document = {
    createElementNS: (_ns, tag) => dom.mk(tag),
    createElement: (tag) => dom.mk(tag),
    getElementById: () => null,
    querySelector: () => ({ clientWidth: 900, clientHeight: 320,
      getBoundingClientRect: () => ({ width: 900, height: 320, left: 0, top: 0 }) }),
    querySelectorAll: () => [],
  };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).renderSankey(dom.svg, dom.empty, srcs, dsts);
    if (again) require(OUT).renderSankey(dom.svg, dom.empty, again[0], again[1]);
    return snap(dom);
  } finally {
    if (prev === undefined) delete globalThis.document; else globalThis.document = prev;
  }
}

const S = (o) => Object.assign({ ip: '198.51.100.10', name: 'pc1', count: 5 }, o);
const D = (o) => Object.assign({ name: 'example.net', cat: 'cdn', count: 5 }, o);

const CASES = {
  // The empty states, which are what a viewer sees most of the time.
  'nothing at all': [[], []],
  'sources but no destinations': [[S({})], []],
  'destinations but no sources': [[], [D({})]],
  // A total of ZERO with rows present is a different branch from no rows.
  'rows that all count zero': [[S({ count: 0 })], [D({ count: 0 })]],

  'one source, one destination': [[S({})], [D({})]],
  'two sources into one destination': [[S({}), S({ ip: '198.51.100.11', name: 'pc2' })], [D({})]],
  'one source into two destinations': [[S({})], [D({}), D({ name: 'other.net', cat: 'cloud' })]],
  'several of each': [
    [S({}), S({ ip: '198.51.100.11', name: 'pc2', count: 9 }),
      S({ ip: '198.51.100.12', name: 'pc3', count: 1 })],
    [D({}), D({ name: 'b.net', cat: 'social', count: 7 }), D({ name: 'c.net', cat: 'dns', count: 2 })],
  ],
  // EVERY CATEGORY, because each is a different colour and an unknown one falls
  // back -- a palette that lost a key would show two categories in one colour.
  'every category': [[S({ count: 8 })], [
    D({ name: 'a', cat: 'cdn', count: 1 }), D({ name: 'b', cat: 'cloud', count: 1 }),
    D({ name: 'c', cat: 'social', count: 1 }), D({ name: 'd', cat: 'streaming', count: 1 }),
    D({ name: 'e', cat: 'messaging', count: 1 }), D({ name: 'f', cat: 'video', count: 1 }),
    D({ name: 'g', cat: 'dns', count: 1 }), D({ name: 'h', cat: 'other', count: 1 }),
  ]],
  'a category nobody has heard of': [[S({})], [D({ cat: 'quantum' })]],
  'no category at all': [[S({})], [D({ cat: undefined })]],
  // The SOURCE palette cycles, so a seventh host reuses the first colour.
  'seven sources cycle the palette': [
    Array.from({ length: 7 }, (_, i) => S({ ip: '198.51.100.' + (10 + i), name: 'pc' + i, count: 1 })),
    [D({ count: 7 })]],
  // Wildly uneven counts: the link widths are proportional, and a rounding rule
  // that differed would show here rather than on equal shares.
  'one huge flow beside a tiny one': [
    [S({ count: 1000 }), S({ ip: '198.51.100.11', name: 'pc2', count: 1 })],
    [D({ count: 1000 }), D({ name: 'b.net', cat: 'dns', count: 1 })]],
  // Escaping and long text: names come from DNS and from DHCP, so neither is
  // this page's to trust.
  'markup in a name': [[S({ name: '<b>pc</b>' })], [D({ name: '<img src=x>' })]],
  'a quote in a name': [[S({ name: 'a"b' })], [D({ name: "c'd" })]],
  'a very long destination name': [[S({})], [D({ name: 'a'.repeat(80) })]],
  'a source with no name falls back to its ip': [[S({ name: '' })], [D({})]],

  // ── A SECOND FRAME ───────────────────────────────────────────────────────
  //
  // The diagram is redrawn on every poll. Without the clear at the top the
  // second frame draws on top of the first and the picture doubles, which no
  // case starting from a fresh element can see.
  'the same payload twice': [[S({})], [D({})], [[S({})], [D({})]]],
  'a different payload second': [[S({})], [D({})],
    [[S({ ip: '198.51.100.11', name: 'pc2', count: 3 })], [D({ name: 'b.net', cat: 'dns' })]]],
  'a full frame then an empty one': [[S({})], [D({})], [[], []]],
  'an empty frame then a full one': [[], [], [[S({})], [D({})]]],
};

let bad = 0;
let checked = 0;
for (const [name, [srcs, dsts, again]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(srcs, dsts, again)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(srcs, dsts, again); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  checked++;
  if (a !== b) {
    bad++;
    if (bad <= 2) shout('DIFF %s\n  live: %s\n  port: %s', name, a.slice(0, 600), b.slice(0, 600));
  }
}

// ---- BELIEVABILITY --------------------------------------------------------
//
// Every case compares two serialised trees, and two renderers that drew nothing
// would produce identical ones. The LIVE side alone must draw, and must draw
// DIFFERENTLY for inputs that are supposed to differ.
{
  const empty = JSON.parse(G.live('auto:6', () => liveRun([], [])));
  assert.ok(!empty.svg.kids, 'the LIVE renderer drew children for an empty payload');
  const one = JSON.parse(G.live('auto:5', () => liveRun([S({})], [D({})])));
  assert.ok(one.svg.kids && one.svg.kids.length,
    'the LIVE renderer drew nothing for a real payload -- the lift is not rendering');
  const two = G.live('auto:4', () => liveRun([S({}), S({ ip: '198.51.100.11', name: 'pc2' })], [D({})]));
  assert.notEqual(G.live('auto:3', () => liveRun([S({})], [D({})])), two, 'a second source changed nothing');
  const catA = G.live('auto:2', () => liveRun([S({})], [D({ cat: 'cdn' })]));
  const catB = G.live('auto:1', () => liveRun([S({})], [D({ cat: 'social' })]));
  assert.notEqual(catA, catB, 'two different categories drew identically -- the colour rule is ' +
    'not being exercised');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('sankey-check: %d cases identical', checked);
