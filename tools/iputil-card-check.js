'use strict';
/**
 * The IP Utilisation card, live against ported.
 *
 * ── IT IS THE THIRD GAUGE OVER THE SAME QUANTITY ────────────────────────────
 *
 * The DHCP page's headline gauge and this card both show pool utilisation, and
 * they compute it differently: the page falls back to the lease-table length
 * before the first `lan:overview`, the card does not. So the corpus carries
 * payloads with `totalLeases` ABSENT — which is precisely where the two differ —
 * and this gate pins the card's own answer rather than assuming the page's.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/iputil-card-check.js
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
const G = LIFT.golden('iputil-card-check');
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
function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent; without this the helper throws at module scope before a frozen
  // output can be served. Harmless because the live half is never entered
  // once the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name);
  return src.slice(i, src.indexOf(close, i) + close.length);
}
// The handler inside the extra-cards IIFE. There are THREE `lan:overview`
// handlers in this file — the Networks card's, the DHCP page's and this one — so
// it is found from the IIFE marker rather than by first occurrence.
const iifeAt = src.indexOf('All 14 new cards live here');
if (LIFT.hasReference(ROOT)) assert.ok(iifeAt > 0, 'cannot find the extra-cards IIFE');
const handlerAt = src.indexOf("socket.on('lan:overview'", iifeAt);
if (LIFT.hasReference(ROOT)) assert.ok(handlerAt > 0, 'no lan:overview handler inside the extra-cards IIFE');
const body = braceBody(handlerAt);
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('dc-dhcpGaugeLbl'), 'the slice is not the IP Utilisation handler');
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('dcDrawGauge'), 'the slice lost its gauge call');
const gaugeSrc = slice('function dcDrawGauge(', '\n  }', 'dcDrawGauge');

const ENTRY = path.join(ROOT, 'testdata', '.iputil-entry.ts');
fs.writeFileSync(ENTRY, "export { renderIpUtilCard } from '../web/src/pages/dashboard-card-iputil.js';\n");
const OUT = path.join(ROOT, 'testdata', '.iputil-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['dc-dhcpGaugeFill', 'dc-dhcpGaugeTrack', 'dc-dhcpGaugePct', 'dc-dhcpGaugeLbl'];
function makeDom() {
  const byId = new Map();
  for (const id of IDS) {
    byId.set(id, {
      id, attrs: {},
      setAttribute(k, v) { this.attrs[k] = String(v); },
      getAttribute(k) { return k in this.attrs ? this.attrs[k] : null; },
      set textContent(v) { this._t = String(v); },
      get textContent() { return this._t === undefined ? '' : this._t; },
    });
  }
  return byId;
}
function snap(byId) {
  const out = {};
  for (const id of IDS) {
    const n = byId.get(id);
    out[id] = { d: n.attrs.d, stroke: n.attrs.stroke, fill: n.attrs.fill, text: n.textContent };
  }
  return JSON.stringify(out);
}
function liveRun(payload) {
  const byId = makeDom();
  const ctx = { Math, String, Number, dcEl: (id) => byId.get(id) || null };
  vm.createContext(ctx);
  vm.runInContext(gaugeSrc + '\nfunction __run(data){' + body + '}', ctx);
  ctx.__run(payload);
  return snap(byId);
}
function portRun(payload) {
  const byId = makeDom();
  globalThis.document = { getElementById: (id) => byId.get(id) || null };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderIpUtilCard(payload);
  return snap(byId);
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
        shout('  %s\n    live: %j\n    port: %j', k, A[k], B[k]);
      }
    }
  }
}

const CASES = {
  'a normal pool': { totalPoolSize: 512, totalLeases: 110 },
  'empty pool': { totalPoolSize: 0, totalLeases: 0 },
  'no pool key': { totalLeases: 5 },
  // Where this card and the DHCP page's gauge differ: the page falls back to the
  // lease-table length, this one reads 0.
  'no totalLeases key': { totalPoolSize: 512 },
  'neither key': {},
  'zero used, real pool': { totalPoolSize: 501, totalLeases: 0 },
  'full': { totalPoolSize: 256, totalLeases: 256 },
  'over-full': { totalPoolSize: 256, totalLeases: 300 },
  'one address': { totalPoolSize: 1, totalLeases: 1 },
  'the colour boundaries': { totalPoolSize: 100, totalLeases: 70 },
  'just under amber': { totalPoolSize: 100, totalLeases: 69 },
  'just under red': { totalPoolSize: 100, totalLeases: 89 },
  'exactly red': { totalPoolSize: 100, totalLeases: 90 },
  'rounds up': { totalPoolSize: 8, totalLeases: 3 },
  'rounds down': { totalPoolSize: 8, totalLeases: 2 },
  'a sub-half-degree fill': { totalPoolSize: 1000, totalLeases: 2 },
  'the CCR2004 shape': { totalPoolSize: 1024, totalLeases: 574 },
};

for (const [name, payload] of Object.entries(CASES)) {
  cmp(name, G.live(name, () => liveRun(payload)), portRun(payload));
}

// ── believability, RE-AIMED AT THE PORT ────────────────────────────────────
//
// These are the checks that the gauge RENDERS — a label, an arc, a percentage.
// Without them the comparisons above could be two blank cards agreeing, so they
// are the last thing that should quietly disappear. They asked the live side
// because it was there; the port is what has to keep drawing a gauge.
{
  const s = JSON.parse(portRun({ totalPoolSize: 512, totalLeases: 110 }));
  assert.equal(s['dc-dhcpGaugeLbl'].text, '110 / 512 used', 'the label reads ' + s['dc-dhcpGaugeLbl'].text);
  assert.ok(s['dc-dhcpGaugeFill'].d, 'the gauge drew no arc');
  assert.equal(s['dc-dhcpGaugePct'].text, '21%', 'the percentage is ' + s['dc-dhcpGaugePct'].text);
}
{
  // With no pool the label is the bare word, not `0 / 0 used`.
  const s = JSON.parse(portRun({}));
  assert.equal(s['dc-dhcpGaugeLbl'].text, 'used',
    'the label with no pool reads ' + JSON.stringify(s['dc-dhcpGaugeLbl'].text));
  assert.equal(s['dc-dhcpGaugePct'].text, '—', 'a zero percentage should show an em dash here');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('iputil-card-check: %d cases identical', checked);
