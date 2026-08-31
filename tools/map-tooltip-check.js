'use strict';
/**
 * The world map's country tooltip, live against ported.
 *
 * `mapTooltip` was in the extracted markup and nothing had ever written to it —
 * one of the two gaps the wiring audit recorded for this page.
 *
 * ── WHAT IS COMPARED ────────────────────────────────────────────────────────
 *
 * The rendered HTML and the position, for a sequence of pointer moves. The
 * sequence matters: content is rewritten only when the pointer crosses into a
 * different country, and the cached wrapper rect is invalidated on exactly two
 * events — a resize, and a content change. Comparing single moves would miss
 * both rules.
 *
 *   moving within one country   position updates, HTML does NOT
 *   crossing into another       both update
 *   onto empty ocean            hides, and only if something was shown
 *   a country with no city      omits the separator rather than printing one
 *   no protocol breakdown       omits the second line entirely
 *
 *   MIKRODASH_SRC=../MikroDash node tools/map-tooltip-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('map-tooltip-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// `CC_NAMES` and `iso2Flag` are LIFTED rather than stubbed. The port imports the
// real ones, so stubbing this side compared a stub against an implementation and
// reported the difference as a port defect — it was the flag emoji versus my
// placeholder. Lifted, the flag conversion and the name table come under the
// comparison too, which is worth having: `iso2Flag` maps letters to regional
// indicator symbols and is exactly the sort of arithmetic a port gets subtly
// wrong.
function sliceNamed(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const at = src.indexOf(decl);
  if (at === -1) throw new Error('cannot find ' + name);
  const end = src.indexOf(close, at);
  if (end === -1) throw new Error(name + ' is never closed');
  return src.slice(at, end + close.length);
}
const ccNamesSrc = sliceNamed('  var CC_NAMES = {', '\n  };', 'CC_NAMES');
const flagSrc = sliceNamed('  function iso2Flag(cc){', '\n  }', 'iso2Flag');

const i = src.indexOf('  function bindTooltip(){');
if (LIFT.hasReference(ROOT) && i === -1) throw new Error('cannot find bindTooltip in app.js');
const liveSrc = src.slice(i, src.indexOf('\n  }', src.indexOf('mouseleave', i)) + 4);
if (LIFT.hasReference(ROOT) && !liveSrc.includes('mouseleave')) throw new Error('the bindTooltip slice lost its mouseleave');

const ENTRY = path.join(ROOT, 'testdata', '.maptip-entry.ts');
fs.writeFileSync(ENTRY, "export { bindMapTooltip } from '../web/src/pages/connections-worldmap.js';\n");
const OUT = path.join(ROOT, 'testdata', '.maptip-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const COUNTS = { DE: 42, FR: 0, JP: 7 };
const CITY = { DE: 'Frankfurt', JP: '' };
const PROTO = { DE: { tcp: 30, udp: 12 }, JP: {} };
const DRAWN = ['DE', 'FR', 'JP', 'ZW'];

function makeWorld() {
  // ASSIGNMENTS ARE COUNTED, not just their result. "Rewrite the content on every
  // move" and "hide something already hidden" both leave identical state, so a
  // comparison on values alone cannot see either — the same blind spot the clock
  // gate had, where the guard's whole effect is the ABSENCE of a write.
  let html = '', display = 'none';
  let htmlWrites = 0, displayWrites = 0;
  const tip = {
    get innerHTML() { return html; },
    set innerHTML(v) { html = String(v); htmlWrites++; },
    style: {
      get display() { return display; },
      set display(v) { display = String(v); displayWrites++; },
      left: '', top: '',
    },
  };
  // THE RECT MOVES on every measurement. With a constant rect a stale cache and
  // a fresh one give the same answer, so "never invalidate" and "invalidate
  // correctly" were indistinguishable — which is what let two mutations through.
  // A real wrapper's rect changes when the tooltip resizes it or the window does.
  const handlers = {};
  let measures = 0;
  const parent = { getBoundingClientRect: () => { measures++; return { left: 100 + measures, top: 50 + measures }; } };
  const mapEl = {
    parentElement: parent,
    addEventListener: (n, f) => { (handlers[n] = handlers[n] || []).push(f); },
  };
  const winHandlers = {};
  const log = [];
  return {
    tip, mapEl,
    win: { addEventListener: (n, f) => { (winHandlers[n] = winHandlers[n] || []).push(f); },
           innerWidth: 1200 },
    move: (cc, x, y) => {
      const target = cc ? { dataset: { cc } } : { dataset: {} };
      for (const f of (handlers.mousemove || [])) f({ target, clientX: x, clientY: y });
      log.push({ html: tip.innerHTML, display: tip.style.display,
                 left: tip.style.left, top: tip.style.top, htmlWrites, displayWrites });
    },
    leave: () => {
      for (const f of (handlers.mouseleave || [])) f({});
      log.push({ html: tip.innerHTML, display: tip.style.display,
                 left: tip.style.left, top: tip.style.top, htmlWrites, displayWrites });
    },
    resize: () => { for (const f of (winHandlers.resize || [])) f({}); },
    state: () => JSON.stringify(log, null, 1),
  };
}

function liveRun(act) {
  const w = makeWorld();
  const ctx = {
    String, Object, JSON,
    window: w.win,
    mapEl: w.mapEl,
    tooltipEl: w.tip,
    _countryCounts: COUNTS,
    _countryCity: CITY,
    _countryProto: PROTO,
    _pathEls: Object.fromEntries(DRAWN.map((c) => [c, {}])),
    esc: (v) => String(v == null ? '' : v).replace(/[&<>"']/g,
      (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])),
  };
  vm.createContext(ctx);
  vm.runInContext(ccNamesSrc + '\n' + flagSrc + '\n' + liveSrc + '\nbindTooltip();', ctx);
  act(w);
  return w.state();
}

function portRun(act) {
  const w = makeWorld();
  const saved = { window: global.window, document: global.document };
  global.window = w.win;
  global.document = { addEventListener() {}, getElementById: () => null };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).bindMapTooltip(w.mapEl, w.tip,
      (cc) => ({ count: COUNTS[cc] || 0, city: CITY[cc] || '', proto: PROTO[cc] || {} }),
      (cc) => DRAWN.includes(cc));
    act(w);
  } finally {
    for (const k of Object.keys(saved)) {
      if (saved[k] === undefined) delete global[k]; else global[k] = saved[k];
    }
  }
  return w.state();
}

const bad = [];
let cases = 0;
function compare(what, act) {
  cases++;
  const a = G.live(what, () => liveRun(act));
  const b = portRun(act);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

compare('hover a country with a city and protocols', (w) => w.move('DE', 300, 200));
compare('hover a country with no city', (w) => w.move('JP', 300, 200));
compare('hover a country with no connections', (w) => w.move('FR', 300, 200));
compare('hover a country the map drew but nothing knows', (w) => w.move('ZW', 300, 200));
compare('hover an unknown code', (w) => w.move('XX', 300, 200));
// Moving WITHIN one country repositions and must not rewrite the content.
compare('move within one country', (w) => { w.move('DE', 300, 200); w.move('DE', 320, 210); });
// Crossing rewrites both.
compare('cross into another country', (w) => { w.move('DE', 300, 200); w.move('JP', 400, 250); });
compare('cross and come back', (w) => { w.move('DE', 300, 200); w.move('JP', 400, 250); w.move('DE', 300, 200); });
// Ocean hides, and only if something was shown.
compare('onto the ocean from a country', (w) => { w.move('DE', 300, 200); w.move(null, 500, 300); });
compare('onto the ocean with nothing shown', (w) => w.move(null, 500, 300));
compare('ocean twice', (w) => { w.move('DE', 1, 1); w.move(null, 2, 2); w.move(null, 3, 3); });
// Leaving the map.
compare('leave the map', (w) => { w.move('DE', 300, 200); w.leave(); });
compare('leave without ever entering', (w) => w.leave());
// A resize invalidates the cached rect; the next move must still position.
compare('resize between moves', (w) => { w.move('DE', 300, 200); w.resize(); w.move('DE', 310, 220); });
// Negative and zero coordinates, where the -30 offset goes negative.
compare('near the top-left corner', (w) => w.move('DE', 100, 50));

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the map tooltip differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
  process.exit(1);
}
console.log('the map tooltip matches the live one (' + cases + ' cases: content, position, ' +
  'crossing, ocean and the cached rect)');
