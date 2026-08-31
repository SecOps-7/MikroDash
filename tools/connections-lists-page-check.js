'use strict';
/**
 * ---- ToDo #18 IS ADOPTED, AND THIS GATE MOVED WITH IT ----------------------
 *
 * The country list used to be rebuilt wholesale every tick and its click
 * re-bound per row. The live app fixed that on 2026-08-25 by keeping the rows
 * and syncing them, and this port followed the same day: `countryRowEl` builds
 * the skeleton once, `syncCountryRow` writes only what changed, and
 * `syncCountryList` reconciles with `insertBefore`, which MOVES a node rather
 * than recreating it. The page delegates one click to the container.
 *
 * The list is therefore compared as a TREE, through `tools/lib/tree-shim.js`.
 * Markup cannot see any of it: two implementations that draw the same rows are
 * not the same thing if one throws its nodes away, and `innerHTML` on a synced
 * row is only whatever the skeleton was handed.
 *
 * ---- TWO FRAMES, BECAUSE ONE CANNOT SEE REUSE ------------------------------
 *
 * Eight frame cases render TWICE and ask which row objects survived. Four
 * mutations passed every single-frame case before they existed: rows rebuilt
 * instead of reused, vanished rows left in the list, and — needing a wipe from
 * OUTSIDE, which is what a router switch does — a cache never re-seeded.
 *
 * ---- TWO EQUIVALENT MUTANTS, WITH THE REASON -------------------------------
 *
 *   `insertBefore(row, want)` → `appendChild(row)` survives. In a real DOM and
 *   in the shim, `appendChild` MOVES an existing node too, and the loop visits
 *   every row in order, so the resulting list is identical. What differs is that
 *   every row is moved every tick rather than only the ones out of place, and
 *   moving a node the pointer is over still disturbs it. Seeing that needs a
 *   MOVE COUNT, which neither this gate nor the shim records — written down
 *   rather than left as a silent gap.
 *
 *   Dropping the cache clear in the EMPTY branch survives, because the re-seed
 *   guard covers it: the empty state assigns `innerHTML`, so the next render
 *   finds no `.conn-map-row` and clears the cache anyway. A redundant pair, like
 *   the pagination clamp in `reports-tables-check`. Removing BOTH is caught.
 *
 * ---- RED SINCE 2026-08-25, AND CORRECTLY SO --------------------------------
 *
 * ToDo #18's fix landed in the live working tree and it took the LARGER of the
 * two options that entry offered: not "delegate the click and fingerprint the
 * markup" but "update in place". `renderCountryList` no longer builds a markup
 * string. It builds ROW ELEMENTS through `_ccRowEl(cc)`, keeps them in `_ccRows`,
 * and syncs each one's cells through `_ccRowSync(row, e, sel)` — so hover, focus
 * and listeners survive a tick, which is what the entry was about.
 *
 * This gate compares INNERHTML. Against a renderer that builds nodes there is
 * nothing to compare, and the lift now fails inside `_ccRowSync` at
 * `row.querySelector(...).textContent`.
 *
 * The three new bindings ARE declared below — `_ccClickBound`, `_ccRows`,
 * `_ccRowEl`, `_ccRowSync` — so the failure is now honest: it is the shim, not
 * the lift. Fixing it properly means two things, in this order:
 *
 *   1. THE PORT ADOPTS THE FIX. It still rewrites the list wholesale every tick
 *      and therefore still has the defect it reported. That is a rewrite of
 *      `renderCountryList` into build-once-and-sync, not a line change.
 *   2. THIS GATE MOVES TO A TREE SHIM. `tools/sankey-check.js` has one, written
 *      the same day for the same reason: record `createElement` + `setAttribute`
 *      + `appendChild` and serialise. It is about twenty lines and it is what
 *      makes a node-building renderer comparable at all.
 *
 * Left red rather than loosened. A gate that is green against a port carrying a
 * defect the live app has fixed would be worse than one that says so.
 *
 * The CONNECTIONS page's COUNTRY and PORT lists, live against ported.
 *
 * `map-fs-check` and `map-tooltip-check` cover the world map's fullscreen and
 * tooltip behaviour; the page module that FEEDS it — the country list beside the
 * map and the top-ports list under it — was ungated.
 *
 * ── WHAT THIS GATE DOES NOT TOUCH, AND WHY ──────────────────────────────────
 *
 * The SVG map itself (markers, arcs, highlight classes, zoom) needs a browser
 * and is gated by `tools/live-renderer.js` against a running stack. Everything
 * here is string building and arithmetic, which is the same division
 * `routers-grid-check.js` already draws for the Routers map.
 *
 * ── THE COUNTRY LIST IS THE SUBJECT OF ToDo #18 ─────────────────────────────
 *
 * It is rewritten wholesale on every `conn:update`, and its rows are clickable
 * and hover-transitioned, so a hovered row flickers and a click can land on a
 * detached node. The port REPRODUCES that — it is the live behaviour — and this
 * gate compares the markup, not the rewrite frequency. If the fix lands upstream
 * the markup should not change, which is worth knowing: this gate will NOT catch
 * that fix, and #18 says so.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/connections-lists-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const { makeTree, serialise } = require('./lib/tree-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/connections-lists-page-check.js --freeze
const G = L.golden('connections-lists-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const COMPARED = ['connMapList', 'connMapSub', 'connPortList', 'connMapBadge'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COMPARED)); process.exit(0); }
const IDS = [...COMPARED, 'connFilterLabel', 'connSrcFilter', 'worldMap', 'worldMapWrap',
  'mapTooltip', 'sankeySvg', 'sankeyEmpty'];

const ENTRY = path.join(ROOT, 'testdata', '.cn-entry.ts');
// The markup builders, which `connections.ts` delegates to. They are exported
// from `connections-lists.ts` and were referenced by NO tool — the module read
// as "exercised" only because something else imports it, which is exactly the
// blind spot `element-coverage-audit` was built for one level up.
fs.writeFileSync(ENTRY,
  "export { syncCountryList, portListHTML } from '../web/src/pages/connections-lists.js';\n");
const OUT = path.join(ROOT, 'testdata', '.cn-port.cjs');
let bundleErr = null;
try {
  execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
    [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=silent'],
    { stdio: 'pipe' });
} catch (e) { bundleErr = e; }
fs.rmSync(ENTRY, { force: true });
if (bundleErr) {
  shout('the port does not export countryListHTML/portListHTML:\n' +
        String(bundleErr.stderr || bundleErr.message).slice(0, 400));
  process.exit(1);
}

/**
 * The COUNTRY LIST is a tree now, so it is compared as one.
 *
 * ToDo #18's fix made both sides build rows and sync them, and a markup
 * comparison has nothing to read: `innerHTML` is only what the SKELETON was
 * given, and every value since has gone into a child node. `tools/lib/tree-shim.js`
 * records what was actually built.
 *
 * SERIALS OFF: this compares two implementations at one instant, and they number
 * their nodes differently for reasons that are not about the picture. The
 * two-frame cases below get at reuse a different way — by asking whether the
 * SAME element object is still there.
 */
const snap = (doc, listEl) => {
  const n = doc.nodes;
  const out = {};
  for (const id of COMPARED) {
    if (id === 'connMapList') continue;
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent } : null;
  }
  out.connMapList = serialise(listEl, { serials: false });
  return JSON.stringify(out, null, 1);
};

function liveRun(call) {
  const doc = makeDoc(IDS, {});
  // The list is a REAL tree on both sides; everything else stays a markup node.
  const tree = makeTree();
  const listEl = tree.mk('div');
  doc.nodes.connMapList = listEl;
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: Object.assign(Object.create(doc), {
      createElement: (tag) => tree.mk(tag),
      getElementById: (id) => (id === 'connMapList' ? listEl : doc.getElementById(id)),
    }),
    window: {},
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
    __out: null,
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function svcBadge('),
    L.whole(src, 'function iso2Flag('),
    L.whole(src, 'var PORT_NAMES = {'),
    L.whole(src, 'var CC_NAMES = {'),
    L.whole(src, 'function drawSparkSVG('),
    'var _sparkData = {};',
    // ── DECLARED BECAUSE ToDo #18'S FIX ADDED IT ──────────────────────────
    //
    // `renderCountryList` now binds its click ONCE on the container instead of
    // per row per tick, and the latch lives at file scope — outside the slice
    // this gate lifts. Without it the lifted function throws on its first call.
    //
    // Declared rather than widening the slice: the latch is one boolean and the
    // BINDING is not what this gate compares (it compares the rows' markup, and
    // `map-fs-check` owns the "does the page wire it" question for its own
    // page). A `false` here means the bind branch runs exactly once, which is
    // the state a real page is in on its first render.
    'var _ccClickBound = false;',
    'var _selectedCC = null;',
    'var _ccRows = {};',
    L.whole(src, 'function _ccRowEl('),
    L.whole(src, 'function _ccRowSync('),
    'function $(id){return document.getElementById(id);}',
    L.whole(src, 'function renderCountryList('),
    L.whole(src, 'function renderPortList('),
    '__out = { renderCountryList: renderCountryList, renderPortList: renderPortList };',
  ].join('\n'), ctx);
  call(ctx.__out, doc, listEl);
  return snap(doc, listEl);
}

// The port returns a STRING where the live function writes into the DOM. The
// adapter puts the string where live puts it, so one snapshot compares both —
// and the subtitle, which only the live side writes, is set the same way live
// sets it so the two are not compared against each other's absence.
function portRun(call) {
  const doc = makeDoc(IDS, {});
  const tree = makeTree();
  const listEl = tree.mk('div');
  doc.nodes.connMapList = listEl;
  const prevDoc = globalThis.document;
  const prevWin = globalThis.window;
  globalThis.window = {};
  try {
    // The row cache the page owns. One per RUN, because each run is a fresh
    // page — a cache shared between runs would make the second one reuse the
    // first's rows and hide exactly what these cases are about.
    const portRows = {};
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      const mod = require(OUT);
      // `document.createElement` must reach the TREE, and `withDocument`
      // installs the markup doc — so it is layered here rather than replaced.
      globalThis.document = Object.assign(Object.create(globalThis.document), {
        createElement: (tag) => tree.mk(tag),
      });
      call({
        renderCountryList: (list, sel) => {
          mod.syncCountryList(listEl, list, SPARKS, sel, portRows);
          if (list.length) doc.nodes.connMapSub.textContent = list.length + ' countries active';
        },
        renderPortList: (ports) => {
          doc.nodes.connPortList.innerHTML = mod.portListHTML(ports || []);
        },
      }, doc, listEl);
      return snap(doc, listEl);
    });
  } finally {
    if (prevWin === undefined) delete globalThis.window; else globalThis.window = prevWin;
    if (prevDoc === undefined) delete globalThis.document; else globalThis.document = prevDoc;
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
      if (x !== y) {
        let i = 0;
        while (i < Math.min(x.length, y.length) && x[i] === y[i]) i++;
        shout('DIFF %s [%s] at %d\n  live: …%s\n  port: …%s', what, k, i,
          x.slice(Math.max(0, i - 20), i + 110), y.slice(Math.max(0, i - 20), i + 110));
      }
    }
  }
}

const SPARKS = {};

const CC = (o) => Object.assign({
  cc: 'US', city: 'Denver', proto: { tcp: 10, udp: 4, other: 1 },
  orgs: [{ org: 'Example', cat: 'cdn', count: 9 }],
}, o);
const PT = (o) => Object.assign({ port: 443, count: 120 }, o);

const CASES = {
  'countries: none': [(m) => m.renderCountryList([], null)],
  'countries: one': [(m) => m.renderCountryList([CC({})], null)],
  'countries: several': [(m) => m.renderCountryList([CC({}), CC({ cc: 'NO', city: 'Oslo' })], null)],
  'countries: one SELECTED': [(m) => m.renderCountryList([CC({}), CC({ cc: 'NO' })], 'NO')],
  'countries: selection that is not present': [(m) => m.renderCountryList([CC({})], 'ZZ')],
  'countries: no city': [(m) => m.renderCountryList([CC({ city: '' })], null)],
  'countries: an unknown cc': [(m) => m.renderCountryList([CC({ cc: 'ZZ' })], null)],
  'countries: no orgs': [(m) => m.renderCountryList([CC({ orgs: [] })], null)],
  'countries: several orgs': [(m) => m.renderCountryList([CC({ orgs: [
    { org: 'A', cat: 'cdn', count: 5 }, { org: 'B', cat: null, count: 2 }] })], null)],
  'countries: an org with no category': [(m) => m.renderCountryList([CC({ orgs: [
    { org: 'A', cat: null, count: 1 }] })], null)],
  'countries: zero protocol counts': [(m) => m.renderCountryList([CC({ proto: { tcp: 0, udp: 0, other: 0 } })], null)],
  'countries: only udp': [(m) => m.renderCountryList([CC({ proto: { udp: 5 } })], null)],
  'countries: missing proto keys': [(m) => m.renderCountryList([CC({ proto: {} })], null)],
  'countries: markup in a city': [(m) => m.renderCountryList([CC({ city: '<img src=x>' })], null)],
  'countries: a quote in an org': [(m) => m.renderCountryList([CC({ orgs: [
    { org: 'a"b', cat: 'cdn', count: 1 }] })], null)],
  // Ports.
  'ports: none': [(m) => m.renderPortList([])],
  'ports: undefined': [(m) => m.renderPortList(undefined)],
  'ports: one': [(m) => m.renderPortList([PT({})])],
  'ports: several scale to the busiest': [(m) => m.renderPortList([PT({}), PT({ port: 80, count: 60 })])],
  'ports: a well-known port has a name': [(m) => m.renderPortList([PT({ port: 22 })])],
  'ports: an unknown port has none': [(m) => m.renderPortList([PT({ port: 65000 })])],
  'ports: a zero busiest count': [(m) => m.renderPortList([PT({ count: 0 })])],
  'ports: a tiny count still gets a visible bar': [(m) => m.renderPortList(
    [PT({ count: 1000 }), PT({ port: 80, count: 1 })])],
  'ports: many': [(m) => m.renderPortList(Array.from({ length: 12 },
    (_, i) => PT({ port: 1000 + i, count: 100 - i * 5 })))],
};

for (const [name, [call]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live('case:' + name, () => liveRun(call)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(call); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

/** `liveRun`/`portRun` with the list element handed through. */
// A COUNTER, because this helper is called many times and one key for all of
// them records only the last. Stable because the freeze run and the compare
// run execute the same sequence.
let __rawN = 0;
const liveRunRaw = (fn) => G.live('raw:' + (++__rawN), () => liveRun((m, _doc, listEl) => fn(m, listEl)));
const portRunRaw = (fn) => portRun((m, _doc, listEl) => fn(m, listEl));

// ── TWO FRAMES, WHICH IS WHAT THE TREE SHIM IS FOR ─────────────────────────
//
// Every case above renders ONCE, and four mutations survived them all: rows
// rebuilt instead of reused, a reorder that clones, vanished rows left behind,
// and a cache never re-seeded after the list is wiped. None of those is visible
// in one frame — the picture is identical either way. What differs is whether
// the SAME element object is still there, which is what keeps hover, focus and
// a click that started before the redraw.
//
// So each side is driven twice and asked which row objects SURVIVED. The answer
// is compared across sides, and asserted to be non-trivial on the live one —
// two implementations that both rebuild everything would agree on "none".
function framesLive(first, second, sel1, sel2, wipe) {
  return frames((call) => liveRunRaw(call), first, second, sel1, sel2, wipe);
}
function framesPort(first, second, sel1, sel2, wipe) {
  return frames((call) => portRunRaw(call), first, second, sel1, sel2, wipe);
}
function frames(runner, first, second, sel1, sel2, wipe) {
  let out = null;
  runner((m, listEl) => {
    m.renderCountryList(first, sel1 || null);
    const before = listEl.kids.slice();
    // A WIPE FROM OUTSIDE, which is what a router switch does
    // (`main.ts:switchRouter` clears the dashboard, and the live page's own
    // reset assigns `innerHTML = ''`). Neither cache guard was exercised until
    // this existed: removing BOTH of them together still passed, because
    // nothing had ever emptied the list by a route other than the two functions
    // that also clear the cache.
    if (wipe) listEl.innerHTML = '';
    m.renderCountryList(second, sel2 || null);
    const after = listEl.kids.slice();
    out = {
      // Identity, by object — not by markup, which is the whole point.
      reused: after.filter((r) => before.includes(r)).length,
      before: before.length,
      after: after.length,
      order: after.map((r) => r.dataset.cc),
      // A row removed from the list must also be detached, or it lingers in a
      // cache and re-attaches on a later tick.
      detached: before.filter((r) => !after.includes(r)).every((r) => r.parentNode === null),
    };
  });
  return out;
}

const FRAME_CASES = {
  'the same two countries twice': [[CC({ cc: 'US' }), CC({ cc: 'NO' })],
    [CC({ cc: 'US' }), CC({ cc: 'NO' })]],
  'one country drops out': [[CC({ cc: 'US' }), CC({ cc: 'NO' })], [CC({ cc: 'US' })]],
  'one country arrives': [[CC({ cc: 'US' })], [CC({ cc: 'US' }), CC({ cc: 'NO' })]],
  'the two swap places': [[CC({ cc: 'US' }), CC({ cc: 'NO' })],
    [CC({ cc: 'NO' }), CC({ cc: 'US' })]],
  'the list empties': [[CC({ cc: 'US' }), CC({ cc: 'NO' })], []],
  'the list empties and comes back': [[CC({ cc: 'US' })], [CC({ cc: 'US' })]],
  'the selection moves': [[CC({ cc: 'US' }), CC({ cc: 'NO' })],
    [CC({ cc: 'US' }), CC({ cc: 'NO' })], null, 'NO'],
  'counts change but the rows do not': [[CC({ cc: 'US', count: 1 })],
    [CC({ cc: 'US', count: 99 })]],
  // WIPED FROM OUTSIDE between the frames — a router switch. The cache must be
  // re-seeded, or the next tick re-attaches the PREVIOUS router's rows.
  'wiped between frames': [[CC({ cc: 'US' }), CC({ cc: 'NO' })],
    [CC({ cc: 'US' }), CC({ cc: 'NO' })], null, null, true],
  'wiped, and different countries come back': [[CC({ cc: 'US' }), CC({ cc: 'NO' })],
    [CC({ cc: 'DE' })], null, null, true],
};

for (const [name, [a1, a2, s1, s2, wipe]] of Object.entries(FRAME_CASES)) {
  const live = G.live('frame:' + name, () => framesLive(a1, a2, s1, s2, wipe));
  const port = framesPort(a1, a2, s1, s2, wipe);
  checked++;
  if (JSON.stringify(live) !== JSON.stringify(port)) {
    bad++;
    shout('DIFF (frames) %s\n  live: %s\n  port: %s', name, JSON.stringify(live), JSON.stringify(port));
  }
}
{
  // BELIEVABILITY: the live side must actually reuse, or every comparison above
  // is between two implementations that both rebuild and agree on nothing.
  const l = G.live('auto:8', () => framesLive([CC({ cc: 'US' }), CC({ cc: 'NO' })], [CC({ cc: 'US' }), CC({ cc: 'NO' })]));
  assert.equal(l.reused, 2, 'the LIVE list rebuilt its rows — reuse is not being exercised');
  const moved = G.live('auto:7', () => framesLive([CC({ cc: 'US' }), CC({ cc: 'NO' })], [CC({ cc: 'NO' }), CC({ cc: 'US' })]));
  assert.equal(moved.reused, 2, 'the LIVE list recreated rows on a reorder rather than moving them');
  assert.deepEqual(moved.order, ['NO', 'US'], 'the reorder did not happen');
  const gone = G.live('auto:6', () => framesLive([CC({ cc: 'US' }), CC({ cc: 'NO' })], [CC({ cc: 'US' })]));
  assert.equal(gone.after, 1, 'the vanished country was left in the LIVE list');
  assert.ok(gone.detached, 'the removed LIVE row was not detached');
}

// ── believability ──────────────────────────────────────────────────────────
//
// Read off the TREE now, not the markup: the list is built as nodes since ToDo
// #18's fix, and `connMapList.h` is only whatever the skeleton was handed.
const rowsOf = (snapshot) => (snapshot.connMapList.kids || [])
  .filter((k) => (k.cls || '').split(' ').includes('conn-map-row'));
const flat = (n) => JSON.stringify(n);
{
  const s = JSON.parse(G.live('auto:5', () => liveRun((m) => m.renderCountryList([CC({})], null))));
  assert.equal(rowsOf(s).length, 1, 'the live country list rendered no row');
  assert.match(flat(s.connMapList), /Denver/, 'the city is missing');
  assert.match(s.connMapSub.t, /1 countries active/, 'the subtitle is ' + s.connMapSub.t);
}
{
  const s = JSON.parse(G.live('auto:4', () => liveRun((m) => m.renderCountryList([], null))));
  assert.match(s.connMapList.html || '', /No geo data yet/, 'the empty state did not render');
  assert.equal(rowsOf(s).length, 0, 'the empty state left rows behind');
}
{
  // The selected country is marked, and only that one.
  const s = JSON.parse(G.live('auto:3', () => liveRun((m) => m.renderCountryList([CC({ cc: 'US' }), CC({ cc: 'NO' })], 'NO'))));
  const sel = rowsOf(s).filter((r) => (r.cls || '').split(' ').includes('selected'));
  assert.equal(sel.length, 1, 'expected exactly one selected row, got ' + sel.length);
}
{
  // The bars are PIXELS, not per cent — `width:Math.max(4, pct)px`, so the
  // busiest port is 100px and a half-as-busy one is 50px. My first assertion
  // looked for '100%' and failed against correct output; reading the row builder
  // is what settled it.
  //
  // The `Math.max(4, …)` floor is the interesting part: a port with a single
  // connection beside one with thousands still gets a visible bar rather than a
  // hairline, so "almost nothing" and "nothing" do not look the same.
  const s = JSON.parse(G.live('auto:2', () => liveRun((m) => m.renderPortList([PT({ count: 120 }), PT({ port: 80, count: 60 })]))));
  assert.match(s.connPortList.h, /width:100px/, 'the busiest port bar is not full width');
  assert.match(s.connPortList.h, /width:50px/, 'the second bar did not scale to it');
  const tiny = JSON.parse(G.live('auto:1', () => liveRun((m) => m.renderPortList([PT({ count: 1000 }), PT({ port: 80, count: 1 })]))));
  assert.match(tiny.connPortList.h, /width:4px/, 'the minimum bar width floor is gone');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('connections-lists-page-check: %d cases identical', checked);
