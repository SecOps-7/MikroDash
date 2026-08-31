'use strict';
/**
 * The world map's fullscreen overlay, live against ported.
 *
 * ── WHAT IS COMPARED ────────────────────────────────────────────────────────
 *
 * WHERE THE SVG IS, and what was left behind. Fullscreen is a portal: the node
 * moves to a body-level overlay and has to come back between the same two
 * siblings, because a card's stacking and scroll context is what it went there
 * to escape.
 *
 * So the comparison is the DOM order around the wrapper, plus the overlay's
 * class, the body's overflow, and whether an Escape listener is outstanding.
 * That last one matters: a handler left bound after close makes Escape start
 * closing things on unrelated pages.
 *
 *   open, close, and back              the SVG returns to its exact slot
 *   opened twice                       must not portal an already-portalled node
 *   closed when never opened           must do nothing rather than move the SVG
 *   Escape while open                  closes; Escape while closed does not
 *   the slot has siblings after it     the placeholder is why it comes back in
 *                                      the middle rather than at the end
 *
 *   MIKRODASH_SRC=../MikroDash node tools/map-fs-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('map-fs-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const i = src.indexOf('    function openMapFs(){');
if (LIFT.hasReference(ROOT)) if (i === -1) throw new Error('cannot find openMapFs');
const j = src.indexOf("    function onFsKey(e){", i);
// FROZEN — this is the program `vm.runInContext` EXECUTES, so the source is
// what must survive. Freezing the executed text keeps the live half running.
const liveSrc = G.value('liveSrc', () => src.slice(i, src.indexOf('\n', j)) + '\n');
if (!liveSrc || liveSrc.length < 100) throw new Error('the recorded liveSrc is empty');
if (LIFT.hasReference(ROOT)) if (!liveSrc.includes('closeMapFs')) throw new Error('the slice lost closeMapFs');

const ENTRY = path.join(ROOT, 'testdata', '.mapfs-entry.ts');
fs.writeFileSync(ENTRY, "export { bindMapFullscreen } from '../web/src/pages/connections-worldmap.js';\n");
const OUT = path.join(ROOT, 'testdata', '.mapfs-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ---- THE PAGE'S IDS REACH THE RIGHT SLOTS -------------------------------
//
// Everything below drives `bindMapFullscreen` with nodes this file MAKES, which
// keeps the comparison readable -- and means nothing here proves the PAGE hands
// it the right ones. Swapping `btn:` and `close:` in `connections.ts` would pass
// every case below while making the fullscreen button close a map that is not
// open.
//
// The live app reads the three by id into `fsBtn` / `fsOverlay` / `fsClose`
// (`../MikroDash/public/app.js:4540`); the port passes them as named options. So
// the CONTRACT is checked textually here -- which id reaches which slot.
//
// ---- READING A MODULE IS NOT GATING IT ----------------------------------
//
// This check was written on 2026-08-25 and reverted the same day: reading
// `connections.ts` made `page-gate-audit` credit this gate with GATING that
// page, whose `conn:update` wiring -- twelve functions, most of them SVG -- is
// genuinely ungated. `--not-gates` below is how a gate says which modules it
// only reads. That audit checks the declaration in both directions: a module
// named there and no longer referenced fails it.
//
// The three ids are still NOT claimed for coverage, and that takes an explicit
// empty `--ids`: a gate answering none is text-scanned, and the check below
// necessarily writes them as string literals.
const COVERS = [];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }
if (process.argv.includes('--not-gates')) {
  console.log(JSON.stringify(['pages/connections'])); process.exit(0);
}

{
  const call = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'connections.ts'), 'utf8')
    .match(/bindMapFullscreen\([^)]*\{([^}]*)\}/);
  if (LIFT.hasReference(ROOT)) assert.ok(call, 'connections.ts no longer calls bindMapFullscreen with an options object');
  for (const [slot, id] of [['btn', 'mapFullscreenBtn'], ['overlay', 'mapFsOverlay'],
                            ['close', 'mapFsClose']]) {
    const want = new RegExp(slot + ":\\s*el\\('" + id + "'\\)");
    assert.ok(want.test(call[1]),
      'connections.ts passes something other than #' + id + ' as `' + slot + '` -- the live app ' +
      'reads that id for that job (app.js:4540), and every case in this file would still pass');
  }
}

/** A DOM small enough to read: one wrapper with three children, plus an overlay. */
function makeDom() {
  const mk = (name) => {
    const node = {
      _n: name, childNodes: [], parentNode: null, _classes: new Set(), style: {},
      classList: {
        add: (c) => node._classes.add(c), remove: (c) => node._classes.delete(c),
        contains: (c) => node._classes.has(c),
      },
      insertBefore(child, ref) {
        detach(child);
        const at = ref ? node.childNodes.indexOf(ref) : -1;
        if (at === -1) node.childNodes.push(child); else node.childNodes.splice(at, 0, child);
        child.parentNode = node;
        return child;
      },
      appendChild(child) { return node.insertBefore(child, null); },
      removeChild(child) {
        const at = node.childNodes.indexOf(child);
        if (at !== -1) node.childNodes.splice(at, 1);
        child.parentNode = null;
        return child;
      },
      addEventListener() {}, removeEventListener() {},
    };
    return node;
  };
  const detach = (n) => { if (n.parentNode) n.parentNode.removeChild(n); };
  const wrap = mk('wrap');
  const before = mk('legend');
  const svg = mk('svg');
  const after = mk('controls');
  for (const c of [before, svg, after]) wrap.appendChild(c);
  const overlay = mk('overlay');
  const body = mk('body');
  return { wrap, svg, before, after, overlay, body, mk };
}

const order = (n) => n.childNodes.map((c) => c._n).join(',');

function makeHarness(dom) {
  let keyHandlers = 0;
  const btn = { _h: {}, addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); } };
  const close = { _h: {}, addEventListener(n, f) { (this._h[n] = this._h[n] || []).push(f); } };
  const doc = {
    createComment: (t) => dom.mk('#' + t),
    body: dom.body,
    _keys: [],
    addEventListener(n, f) { if (n === 'keydown') { this._keys.push(f); keyHandlers++; } },
    removeEventListener(n, f) {
      if (n === 'keydown') { const at = this._keys.indexOf(f); if (at !== -1) this._keys.splice(at, 1); keyHandlers--; }
    },
  };
  return {
    doc, btn, close,
    fire: (el, name) => { for (const f of (el._h[name] || [])) f({}); },
    key: (k) => { for (const f of [...doc._keys]) f({ key: k }); },
    state: () => JSON.stringify({
      wrap: order(dom.wrap),
      overlay: order(dom.overlay),
      overlayActive: dom.overlay._classes.has('active'),
      bodyOverflow: dom.body.style.overflow ?? '',
      keyHandlers: doc._keys.length,
      svgParent: dom.svg.parentNode ? dom.svg.parentNode._n : null,
    }, null, 1),
  };
}

function liveRun(act) {
  const dom = makeDom();
  const h = makeHarness(dom);
  const ctx = {
    document: h.doc,
    svg: dom.svg, wrap: dom.wrap,
    fsOverlay: dom.overlay,
    svgPlaceholder: h.doc.createComment('map-svg-placeholder'),
    _touchTarget: null,
    bindTouch() {}, unbindTouch() {},
  };
  vm.createContext(ctx);
  vm.runInContext(liveSrc, ctx);
  act({ open: () => ctx.openMapFs(), close: () => ctx.closeMapFs(), key: h.key }, h);
  return h.state();
}

function portRun(act) {
  const dom = makeDom();
  const h = makeHarness(dom);
  const saved = { document: global.document };
  global.document = h.doc;
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).bindMapFullscreen(dom.wrap, dom.svg, { retarget() {} },
      { btn: h.btn, overlay: dom.overlay, close: h.close });
    act({ open: () => h.fire(h.btn, 'click'), close: () => h.fire(h.close, 'click'), key: h.key }, h);
  } finally {
    if (saved.document === undefined) delete global.document; else global.document = saved.document;
  }
  return h.state();
}

const bad = [];
let cases = 0;
function compare(what, act) {
  cases++;
  const a = liveRun(act), b = portRun(act);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

compare('open', (api) => api.open());
compare('open then close', (api) => { api.open(); api.close(); });
compare('open, close, open again', (api) => { api.open(); api.close(); api.open(); });
compare('open, close, open, close', (api) => { api.open(); api.close(); api.open(); api.close(); });
compare('Escape while open', (api) => { api.open(); api.key('Escape'); });
compare('Escape while closed', (api) => api.key('Escape'));
compare('a key that is not Escape while open', (api) => { api.open(); api.key('Enter'); });
compare('Escape twice', (api) => { api.open(); api.key('Escape'); api.key('Escape'); });
// ── TWO STATED DIFFERENCES, ASSERTED AS DIFFERENCES ─────────────────────────
//
// This port guards both entry points with an `open` flag; the live app has no
// such guard, so:
//
//   opened twice     the live code inserts its placeholder INSIDE the overlay
//                    beside the already-portalled SVG, and closing then returns
//                    the map to the overlay rather than to the card.
//   closed unopened  the live code dereferences a placeholder with no parent and
//                    THROWS.
//
// Neither is reachable: the button that opens is hidden while the overlay is up,
// and the one that closes lives inside the overlay. Pinned here so they stay
// decisions — if either side changes, this fails and the note is revisited.
function assertDiffers(what, act, expectLiveThrows) {
  cases++;
  let a = null, threw = false;
  try { a = liveRun(act); } catch { threw = true; }
  const b = portRun(act);
  if (LIFT.hasReference(ROOT)) if (expectLiveThrows && !threw) {
    bad.push(what + ': the live app no longer throws, so this recorded difference is stale');
    return;
  }
  if (!expectLiveThrows && a === b) {
    bad.push(what + ': the two now AGREE — delete this case and compare them normally');
  }
}
assertDiffers('opened twice without closing', (api) => { api.open(); api.open(); }, false);
assertDiffers('closed when never opened', (api) => api.close(), true);

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the fullscreen overlay differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
  process.exit(1);
}
console.log('the fullscreen overlay matches the live one (' + cases + ' cases: the portal, the ' +
  'return slot, Escape and the listener count)');
