'use strict';
/**
 * Signal Health and Band Split, live against ported.
 *
 * ── THE BUCKET BOUNDARIES ARE THE WHOLE CARD ────────────────────────────────
 *
 * -55, -65 and -75, all `>=`. Every case sits on a boundary or one dBm either
 * side of it, because that is the only place a reasonable port disagrees.
 *
 * ── AND TWO QUIRKS THAT LOOK LIKE BUGS ──────────────────────────────────────
 *
 * A signal that will not parse becomes 0, and 0 >= -55, so it counts as
 * EXCELLENT. And the band match is exact-string, so a client on any other
 * spelling counts in NO bucket — the three numbers do not sum to the client
 * count. Both are live behaviour; a port that "fixed" either would show a
 * different card than the app it replaces, so both are in the corpus.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wireless-cards-check.js
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
const G = LIFT.golden('wireless-cards-check');
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
// The handler inside the EXTRA-CARDS IIFE, not the one that feeds the Wireless
// page. Found from the IIFE's own marker so a second `wireless:update` elsewhere
// cannot be picked up by accident — the ping card cost a debugging cycle to that.
const iifeAt = src.indexOf('All 14 new cards live here');
if (LIFT.hasReference(ROOT)) assert.ok(iifeAt > 0, 'cannot find the extra-cards IIFE');
const handlerAt = src.indexOf("socket.on('wireless:update'", iifeAt);
if (LIFT.hasReference(ROOT)) assert.ok(handlerAt > 0, 'no wireless:update handler inside the extra-cards IIFE');
const body = braceBody(handlerAt);
// GUARDED: each asks whether the lifted SLICE still contains an id.
if (LIFT.hasReference(ROOT)) {
  for (const must of ['dc-wlSigBarE', 'dc-wlBandNum24', 'dc-wlBandRow6', 'dc-sigNoData']) {
    assert.ok(body.includes(must), 'the handler slice lost ' + must);
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.wlcards-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderWirelessCards } from '../web/src/pages/dashboard-card-wireless.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wlcards-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = [
  'dc-sigNoData', 'dc-wlSigHealth',
  'dc-wlSigBarE', 'dc-wlSigCntE', 'dc-wlSigBarG', 'dc-wlSigCntG',
  'dc-wlSigBarF', 'dc-wlSigCntF', 'dc-wlSigBarP', 'dc-wlSigCntP',
  'dc-wlBandNum24', 'dc-wlBandNum5', 'dc-wlBandNum6', 'dc-wlBandRow6',
];
function makeDom() {
  const byId = new Map();
  for (const id of IDS) {
    byId.set(id, {
      id, style: {},
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
    out[id] = { text: n.textContent, width: n.style.width, display: n.style.display };
  }
  return JSON.stringify(out);
}
function liveRun(payload) {
  const byId = makeDom();
  const ctx = { Math, String, parseInt, dcEl: (id) => byId.get(id) || null };
  vm.createContext(ctx);
  vm.runInContext('function __run(data){' + body + '}', ctx);
  ctx.__run(payload);
  return snap(byId);
}
function portRun(payload) {
  const byId = makeDom();
  globalThis.document = { getElementById: (id) => byId.get(id) || null };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderWirelessCards(payload);
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

const c = (signal, band) => ({ signal, band });
const CASES = {
  'no clients at all': { clients: [] },
  'no clients key': {},
  'one excellent': { clients: [c('-40', '5GHz')] },
  // Every boundary, and one dBm either side.
  'the -55 boundary': { clients: [c('-54', '5GHz'), c('-55', '5GHz'), c('-56', '5GHz')] },
  'the -65 boundary': { clients: [c('-64', '5GHz'), c('-65', '5GHz'), c('-66', '5GHz')] },
  'the -75 boundary': { clients: [c('-74', '5GHz'), c('-75', '5GHz'), c('-76', '5GHz')] },
  'one of each bucket': { clients: [c('-40', '5GHz'), c('-60', '5GHz'), c('-70', '5GHz'), c('-90', '5GHz')] },
  // The quirk: no signal reads as EXCELLENT.
  'a client with NO signal': { clients: [c(undefined, '5GHz')] },
  'an empty signal string': { clients: [c('', '5GHz')] },
  'an unparseable signal': { clients: [c('n/a', '5GHz')] },
  'a signal with a unit suffix': { clients: [c('-58dBm', '5GHz')] },
  'a numeric signal, not a string': { clients: [c(-58, '5GHz')] },
  'a positive signal': { clients: [c('5', '5GHz')] },
  'a signal of exactly zero': { clients: [c('0', '5GHz')] },
  // Bands.
  'all three bands': { clients: [c('-50', '2.4GHz'), c('-50', '5GHz'), c('-50', '6GHz')] },
  'no 6GHz — the row hides': { clients: [c('-50', '2.4GHz'), c('-50', '5GHz')] },
  'ONLY 6GHz': { clients: [c('-50', '6GHz')] },
  // The quirk: an unknown band counts nowhere.
  'an unknown band spelling': { clients: [c('-50', '6 GHz'), c('-50', '5ghz'), c('-50', 'ax')] },
  // Bands that would match a PREFIX test but not the exact one. RouterOS spells
  // its own band property `2ghz-g/n`, so a payload carrying the raw value rather
  // than the normalised one is not far-fetched — and a prefix rewrite of this
  // match survived every case until these existed.
  'bands that a prefix match would catch': {
    clients: [c('-50', '2.4GHz-ax'), c('-50', '2.4'), c('-50', '5GHz-ac'), c('-50', '6GHzE')],
  },
  'a client with no band': { clients: [c('-50', undefined)] },
  'a mixed fleet': {
    clients: [
      c('-40', '2.4GHz'), c('-52', '5GHz'), c('-60', '5GHz'), c('-68', '2.4GHz'),
      c('-72', '6GHz'), c('-80', '2.4GHz'), c('-95', '5GHz'), c(undefined, '6GHz'),
    ],
  },
  // Rounding: three clients means 33%/33%/33%, which does not sum to 100.
  'three clients — the bars do not sum to 100%': {
    clients: [c('-40', '5GHz'), c('-60', '5GHz'), c('-70', '5GHz')],
  },
  'seven clients in one bucket': { clients: Array.from({ length: 7 }, () => c('-40', '5GHz')) },
};

for (const [name, payload] of Object.entries(CASES)) {
  cmp(name, G.live(name, () => liveRun(payload)), portRun(payload));
}

// ── believability, and the two quirks stated directly ──────────────────────
//
// ALL THREE RE-AIMED AT THE PORT. They assert quirks the PORT reproduces
// deliberately — the middle one says exactly that in its own message — so the
// port is the subject they were always about. Asked of the live side they would
// have converted to nothing.
{
  const s = JSON.parse(portRun({ clients: [c('-40', '5GHz'), c('-90', '5GHz')] }));
  assert.equal(s['dc-wlSigCntE'].text, '1', 'the card counted no excellent client');
  assert.equal(s['dc-wlSigBarE'].width, '50%', 'the bar is ' + s['dc-wlSigBarE'].width);
  assert.equal(s['dc-wlSigHealth'].display, '', 'the card stayed hidden with clients present');
}
{
  const s = JSON.parse(portRun({ clients: [c('n/a', '5GHz')] }));
  assert.equal(s['dc-wlSigCntE'].text, '1',
    'an unparseable signal no longer counts as EXCELLENT — this port reproduces that ' +
    'live quirk deliberately');
}
{
  const s = JSON.parse(portRun({ clients: [c('-50', '6 GHz')] }));
  const sum = ['dc-wlBandNum24', 'dc-wlBandNum5', 'dc-wlBandNum6']
    .reduce((a, id) => a + Number(s[id].text), 0);
  assert.equal(sum, 0,
    'an unknown band spelling was counted somewhere; the match is exact-string and counts it nowhere');
  assert.equal(s['dc-wlBandRow6'].display, 'none', 'the 6GHz row did not hide at zero');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('wireless-cards-check: %d cases identical', checked);
