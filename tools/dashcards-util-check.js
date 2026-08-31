'use strict';
/**
 * The extra cards' shared helpers, live against ported.
 *
 * ── EACH ONE HAS A NEAR-TWIN THIS PORT ALREADY HAS ──────────────────────────
 *
 * `dcEsc` is not `esc`, `dcFlag` is not `iso2Flag`, `dcDrawGauge` is not the DHCP
 * page's `renderDhcpGauge`. The differences are small and visible, so this gate
 * drives BOTH the live helper and its twin over the same inputs and asserts they
 * DISAGREE somewhere. A corpus on which a helper and its twin agree everywhere
 * would pass whichever one the port had wired up, which is exactly the mistake
 * available here.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/dashcards-util-check.js
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
const G = LIFT.golden('dashcards-util-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
// `dcEsc` is a ONE-LINER, so it closes at its own end of line — not at the next
// `\n  }`, which is a later function's brace and swallowed three helpers whole.
// FROZEN. An IIFE-form lift — `freeze-src.py` only rewrites plain
// `const X = lifter(...)` assignments, so this one needed doing by hand.
const escSrc = G.value('escSrc', () => {
  const i = src.indexOf('function dcEsc(');
  if (i === -1) throw new Error('cannot find dcEsc');
  return src.slice(i, src.indexOf('\n', i));
});
const flagSrc = G.value('flagSrc', () => slice('function dcFlag(', '\n  }', 'dcFlag'));
const rateSrc = G.value('rateSrc', () => slice('function dcSplitRate(', '\n  }', 'dcSplitRate'));
const gaugeSrc = G.value('gaugeSrc', () => slice('function dcDrawGauge(', '\n  }', 'dcDrawGauge'));
// The twins, for the disagreement assertions.
// The twins, sliced by their ACTUAL shape rather than a guessed close pattern.
// `esc` is a top-level one-liner; `iso2Flag` is indented inside an IIFE and so
// closes with `\n  }`. Slicing the latter to `\n}` overshot by 180 braces and
// swallowed the rest of the IIFE — caught by the balance assertion below, which
// is why that assertion exists.
// FROZEN (IIFE-form lift; freeze-src.py only rewrites plain
// `const X = lifter(...)` assignments).
const twinEsc = G.value('twinEsc', () => {
  const i = src.indexOf('function esc(');
  return src.slice(i, src.indexOf('\n', i));
});
const twinFlag = G.value('twinFlag', () => slice('function iso2Flag(', '\n  }', 'iso2Flag'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['flagSrc', flagSrc], ['rateSrc', rateSrc], ['gaugeSrc', gaugeSrc], ['twinFlag', twinFlag]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}

// EVERY slice is checked for its own shape. A close pattern that overshoots
// produces code that still parses right up until it does not, and the failure
// then points at a line number in a concatenation rather than at the slice.
assert.match(gaugeSrc, /dc-dhcpGaugeFill/, 'the dcDrawGauge slice lost its ids');
for (const [name, code, want] of [
  ['dcEsc', escSrc, /textContent/], ['dcFlag', flagSrc, /fromCodePoint/],
  ['dcSplitRate', rateSrc, /Gbps/], ['dcDrawGauge', gaugeSrc, /setAttribute/],
  ['esc', twinEsc, /replace/], ['iso2Flag', twinFlag, /fromCodePoint/],
]) {
  assert.match(code, want, 'the ' + name + ' slice does not contain what it should');
  const opens = (code.match(/{/g) || []).length, closes = (code.match(/}/g) || []).length;
  assert.equal(opens, closes,
    'the ' + name + ' slice has ' + opens + ' { and ' + closes + ' } — its close pattern ' +
    'overshot or stopped short');
}

const ENTRY = path.join(ROOT, 'testdata', '.dcutil-entry.ts');
fs.writeFileSync(ENTRY,
  "export { dcEsc, dcFlag, dcSplitRate, dcDrawGauge } from '../web/src/pages/dashboard-cards-util.js';\n" +
  "export { esc } from '../web/src/dom.js';\n" +
  "export { iso2Flag } from '../web/src/pages/connections-map.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dcutil-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['dc-dhcpGaugeFill', 'dc-dhcpGaugeTrack', 'dc-dhcpGaugePct'];
function makeDom() {
  const byId = new Map();
  const mk = (id) => {
    const n = {
      id, attrs: {},
      setAttribute(k, v) { n.attrs[k] = String(v); },
      getAttribute: (k) => (k in n.attrs ? n.attrs[k] : null),
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      // A real div's innerHTML after setting textContent — the browser escapes
      // `&` and the angle brackets and LEAVES QUOTES ALONE. That asymmetry with
      // `esc()` is the whole point of dcEsc having its own function.
      set innerHTML(v) { n._h = v; },
      get innerHTML() {
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
  for (const id of IDS) {
    const n = d.byId.get(id);
    out[id] = { d: n.attrs.d, stroke: n.attrs.stroke, fill: n.attrs.fill, text: n.textContent };
  }
  return JSON.stringify(out);
}

function liveCtx(d) {
  const ctx = {
    Math, Number, String, JSON,
    document: { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') },
    dcEl: (id) => d.byId.get(id) || null,
  };
  vm.createContext(ctx);
  vm.runInContext([escSrc, flagSrc, rateSrc, gaugeSrc, twinEsc, twinFlag].join('\n'), ctx);
  return ctx;
}
globalThis.document = { createElement: () => makeDom().mk('') };
const port = require(OUT);

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (JSON.stringify(a) === JSON.stringify(b)) return;
  bad++;
  if (bad <= 5) shout('DIFF %s\n  live: %j\n  port: %j', what, a, b);
}

// ── dcEsc ──────────────────────────────────────────────────────────────────
const ESC_IN = [
  '', 'plain', "O'Brien", '"quoted"', '<b>bold</b>', 'A & B', '<img src=x onerror=1>',
  '&amp;', '&', '<', '>', "it's <b>&</b> \"x\"", null, undefined, 0, 42, 'ünïcøde', '🌐',
];
{
  const d = makeDom(); const L = liveCtx(d);
  globalThis.document = { createElement: () => d.mk('') };
  for (const v of ESC_IN) cmp('dcEsc(' + JSON.stringify(v) + ')', L.dcEsc(v), port.dcEsc(v));
  // AND IT MUST DIFFER FROM `esc` SOMEWHERE, or the port could wire up either.
  const disagree = ESC_IN.filter((v) => L.dcEsc(v) !== L.esc(v));
  assert.ok(disagree.length > 0,
    'dcEsc and esc agree on every input in this corpus — it cannot tell which one the port used');
  say('  dcEsc differs from esc on %d of %d inputs (e.g. %j)',
    disagree.length, ESC_IN.length, disagree[0]);
}

// ── dcFlag ─────────────────────────────────────────────────────────────────
const CC_IN = ['US', 'us', 'Us', 'GB', 'DE', 'ZW', '', null, undefined, 'U', 'USA', '12', 'u1', '  '];
{
  const d = makeDom(); const L = liveCtx(d);
  for (const v of CC_IN) cmp('dcFlag(' + JSON.stringify(v) + ')', L.dcFlag(v), port.dcFlag(v));
  const disagree = CC_IN.filter((v) => L.dcFlag(v) !== L.iso2Flag(v));
  assert.ok(disagree.length > 0,
    'dcFlag and iso2Flag agree everywhere in this corpus — the globe fallback is untested');
  say('  dcFlag differs from iso2Flag on %d of %d inputs (e.g. %j)',
    disagree.length, CC_IN.length, disagree[0]);
}

// ── dcSplitRate ────────────────────────────────────────────────────────────
const RATES = [
  0, 0.0001, 0.0009, 0.001, 0.0011, 0.5, 0.999, 1, 1.005, 1.5, 9.999, 10, 99.995,
  100, 999.99, 999.999, 1000, 1000.5, 1500, 12345, 1e6, -1, -0.5,
  '12.5', '', 'abc', null, undefined, NaN, Infinity,
];
{
  const d = makeDom(); const L = liveCtx(d);
  for (const v of RATES) cmp('dcSplitRate(' + JSON.stringify(v) + ')', L.dcSplitRate(v), port.dcSplitRate(v));
}

// ── dcDrawGauge ────────────────────────────────────────────────────────────
const PCTS = [0, 0.1, 0.4, 0.41, 0.5, 1, 25, 50, 69, 70, 71, 89, 90, 91, 99, 100, 150, -5, 37.5];
{
  for (const pct of PCTS) {
    const dl = makeDom(), dp = makeDom();
    liveCtx(dl).dcDrawGauge(pct);
    globalThis.document = { getElementById: (id) => dp.byId.get(id) || null, createElement: () => dp.mk('') };
    port.dcDrawGauge(pct);
    cmp('dcDrawGauge(' + pct + ')', JSON.parse(snap(dl)), JSON.parse(snap(dp)));
  }
  // The quirk worth stating: 0% shows an em dash here, and `0%` on the DHCP page.
  const d0 = makeDom();
  liveCtx(d0).dcDrawGauge(0);
  assert.equal(JSON.parse(snap(d0))['dc-dhcpGaugePct'].text, '—',
    'the live card shows something other than an em dash at 0% — it and the DHCP page have ' +
    'converged, and this port reproduces them disagreeing');
  // And the sub-half-degree arc is suppressed.
  const dTiny = makeDom();
  liveCtx(dTiny).dcDrawGauge(0.4);
  assert.equal(JSON.parse(snap(dTiny))['dc-dhcpGaugeFill'].d, '',
    'a sub-half-degree fill drew an arc; it should draw nothing');
  const dBig = makeDom();
  liveCtx(dBig).dcDrawGauge(50);
  assert.ok(JSON.parse(snap(dBig))['dc-dhcpGaugeFill'].d, 'the live gauge drew no fill at 50%');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d comparisons differ', bad, checked); process.exit(1); }
say('dashcards-util-check: %d comparisons identical', checked);
