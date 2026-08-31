'use strict';
/**
 * The WIFI NETWORKS page, live against ported. Built on `tools/lib/lift.js`.
 *
 * ── ONE COLOUR PER SSID, NOT PER ROW ────────────────────────────────────────
 *
 * The same network broadcast on 2.4 and 5 GHz is ONE network and has to look
 * like one, so the colour map is built from the UNIQUE ssids rather than from
 * the rows. A corpus of rows that all differ cannot tell the two apart, so the
 * cases include the same ssid on two radios.
 *
 * The colour function itself lives on `window._ssidColours`, published by the
 * Wireless page — so a band colour means the same thing wherever it appears.
 * Both sides get the real one, lifted; stubbing it would compare two charts of
 * my own invention.
 *
 * ── FOUR NOTES AND TWO EMPTY STATES ─────────────────────────────────────────
 *
 * `wnStackNote`, `wnVirtualNote` and `wnCapNote` each appear only in their own
 * situation, and the empty table says either "no wireless interfaces" or "no
 * networks configured" depending on the stack. Same species as the CAPsMAN
 * states: telling somebody the wrong kind of empty sends them hunting.
 *
 * ── WHERE THE TYPE CHECKER IS THE GATE ─────────────────────────────────────
 *
 * Mutating away the `|| []` fallbacks does not compile: the payload types
 * declare those arrays, so `st.radios` is never undefined as far as TypeScript
 * is concerned. The CASES still send `radios: undefined`, because the WIRE does
 * not typecheck — a collector that failed its radio read sends what it has — and
 * both sides handle it identically. The compiler covers the port's internals;
 * the corpus covers what arrives from outside it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wifi-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/wifi-page-check.js --freeze
const G = L.golden('wifi-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const iife = G.value('iife', () => L.region(src, {
  contains: '(function wifiPage() {',
  must: ['wnTable', 'renderSecProfiles', '_ssidColours'],
  // IDENTIFIERS, NOT PROSE. 'CAPsMAN page' was the first choice and it fired —
  // on this page's own `wnCapNote`, which legitimately talks about CAPsMAN
  // because a network can be managed by one. An exclusion marker has to be
  // something only the other page's CODE contains.
  mustNot: ['auditTable', 'rtRoutesTbody', 'capsmanTable', 'bridgesHostTable'],
}));

// This page declares its OWN `el()`, so `idsFor` — which looks for `$('id')` —
// finds nothing. The ids come from those calls instead.
const IDS = [...new Set([...iife.matchAll(/el\('([A-Za-z0-9_-]+)'\)/g)].map((m) => m[1]))];
assert.ok(IDS.includes('wnTable'), 'the id scan missed the table');
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const ENTRY = path.join(ROOT, 'testdata', '.wn-entry.ts');
fs.writeFileSync(ENTRY, "export { initWifiPage } from '../web/src/pages/wifi.js';\n");
const OUT = path.join(ROOT, 'testdata', '.wn-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const snap = (doc) => {
  const n = doc.nodes;
  const out = {};
  for (const id of IDS.slice().sort()) {
    out[id] = n[id] ? { h: n[id].innerHTML, t: n[id].textContent,
      d: n[id].style && n[id].style.display } : null;
  }
  return JSON.stringify(out);
};

// The real colour function AND the palette it indexes, both published by the
// Wireless page. Lifted together: the function alone throws on a missing
// `SSID_COLOURS`, and inventing a palette here would compare two colour schemes
// of my own choosing rather than the app's.
const COLOUR_FN = G.value('COLOUR_FN', () => L.whole(src, 'var SSID_COLOURS = [')).replace(/;?\s*$/, ';') + '\n' +
  L.whole(src, 'function ssidColours(');

function liveRun(script) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: (fn) => { fn(); return 0; }, clearTimeout: () => {},
    requestAnimationFrame: (fn) => { fn(); return 0; },
    CustomEvent: function (type, init) { return { type, detail: init && init.detail }; },
    window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function bandBadge('),
    COLOUR_FN,
    'window._ssidColours = ssidColours;',
    'window._bandBadge = bandBadge;',
    'function $(id){return document.getElementById(id);}',
    'function pageVisible(){return true;}',
    'function _debounce(fn){return fn;}',
    iife.startsWith('(function') ? iife : '(function wifiPage() {' + iife + '})();',
  ].join('\n'), ctx);
  if (!handlers['wifi:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate does not ' +
      'provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  for (const [ev, p] of script) {
    if (!handlers[ev]) throw new Error('nothing subscribes ' + ev);
    handlers[ev](p);
  }
  return snap(doc);
}

function portRun(script) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const prevWin = globalThis.window;
  const prevST = globalThis.setTimeout;
  globalThis.setTimeout = ((fn) => { fn(); return 0; });
  // The port reads the same published helper; give it the same real one.
  const ctx = { _ssidColours: null };
  globalThis.window = ctx;
  try {
    const sandbox = { module: { exports: {} } };
    vm.runInNewContext(COLOUR_FN + '\nmodule.exports = ssidColours;', sandbox);
    ctx._ssidColours = sandbox.module.exports;
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initWifiPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      for (const [ev, p] of script) {
        if (!handlers[ev]) throw new Error('the port does not subscribe ' + ev);
        handlers[ev](p);
      }
      return snap(doc);
    });
  } finally {
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
      if (x !== y) {
        let i = 0;
        while (i < Math.min(x.length, y.length) && x[i] === y[i]) i++;
        shout('DIFF %s [%s] at %d\n  live: …%s\n  port: …%s', what, k, i,
          x.slice(Math.max(0, i - 20), i + 100), y.slice(Math.max(0, i - 20), i + 100));
      }
    }
  }
}

const N = (o) => Object.assign({
  radio: 'wifi1', ssid: 'net', band: '5ghz-ax', disabled: false, running: true,
  master: true, security: 'wpa2', clients: 3, channel: '5180/ax/eeCe',
}, o);
const RAD = (o) => Object.assign({
  name: 'wifi1', band: '5ghz-ax', channel: '5180', country: 'no_country_set',
}, o);
const SEC = (o) => Object.assign({
  name: 'wpa2', authTypes: ['wpa2-psk'], encryption: ['ccmp'], used: true,
}, o);
const P = (o) => Object.assign({
  networks: [], radios: [], security: [], stack: 'wifiwave2', clients: 0,
}, o);
const upd = (o) => [['wifi:update', P(o)]];

const CASES = {
  // The two empty states.
  'no wireless interfaces at all': upd({ networks: [], stack: 'none' }),
  'wireless present but nothing configured': upd({ networks: [], stack: 'wifiwave2' }),
  'one network': upd({ networks: [N({})], radios: [RAD({})] }),
  'several on one radio': upd({ networks: [N({}), N({ ssid: 'guest', master: false })], radios: [RAD({})] }),
  'two radios': upd({ networks: [N({}), N({ radio: 'wifi2', band: '2ghz-n' })],
    radios: [RAD({}), RAD({ name: 'wifi2', band: '2ghz-n' })] }),
  // ONE COLOUR PER SSID: the same network on two radios is one network.
  'the SAME ssid on two radios': upd({ networks: [
    N({ radio: 'wifi1', band: '5ghz-ax' }), N({ radio: 'wifi2', band: '2ghz-n' })],
    radios: [RAD({}), RAD({ name: 'wifi2', band: '2ghz-n' })] }),
  'two different ssids on two radios': upd({ networks: [
    N({ radio: 'wifi1' }), N({ radio: 'wifi2', ssid: 'other', band: '2ghz-n' })],
    radios: [RAD({}), RAD({ name: 'wifi2', band: '2ghz-n' })] }),
  'a network with no ssid': upd({ networks: [N({ ssid: '' })], radios: [RAD({})] }),
  // Network state.
  'a disabled network': upd({ networks: [N({ disabled: true })], radios: [RAD({})] }),
  'a network that is not running': upd({ networks: [N({ running: false })], radios: [RAD({})] }),
  'a virtual AP': upd({ networks: [N({ master: false })], radios: [RAD({})] }),
  'no clients': upd({ networks: [N({ clients: 0 })], radios: [RAD({})] }),
  'no security profile': upd({ networks: [N({ security: '' })], radios: [RAD({})] }),
  'no channel': upd({ networks: [N({ channel: '' })], radios: [RAD({})] }),
  // Radios and the country note.
  'a radio with no country set': upd({ networks: [N({})], radios: [RAD({ country: 'no_country_set' })] }),
  'a radio with a country': upd({ networks: [N({})], radios: [RAD({ country: 'Norway' })] }),
  'a network whose radio is unknown': upd({ networks: [N({ radio: 'ghost' })], radios: [RAD({})] }),
  // `st.radios || []` only differs from `st.radios` when the key is ABSENT, and
  // every case supplied one. A payload with networks but no radios array is what
  // a collector sends when the radio read failed but the network read did not.
  'networks with NO radios key': upd({ networks: [N({})], radios: undefined }),
  'no radios key and no networks': upd({ networks: [], radios: undefined }),
  'no security key at all': upd({ networks: [N({})], radios: [RAD({})], security: undefined }),
  // Security profiles.
  'one security profile': upd({ networks: [N({})], radios: [RAD({})], security: [SEC({})] }),
  'an unused profile': upd({ security: [SEC({ used: false })] }),
  'a profile with several auth types': upd({ security: [SEC({ authTypes: ['wpa2-psk', 'wpa3-psk'] })] }),
  'a profile with no encryption': upd({ security: [SEC({ encryption: [] })] }),
  'no security profiles at all': upd({ networks: [N({})], radios: [RAD({})], security: [] }),
  // Escaping.
  'markup in an ssid': upd({ networks: [N({ ssid: '<img src=x>' })], radios: [RAD({})] }),
  'a quote in a radio name': upd({ networks: [N({ radio: 'a"b' })], radios: [RAD({ name: 'a"b' })] }),
  'markup in a security profile name': upd({ security: [SEC({ name: '<b>x</b>' })] }),
  // A router switch clears rather than leaving another router's networks.
  'a router switch clears': [['wifi:update', P({ networks: [N({})], radios: [RAD({})] })],
    ['router:switched', {}]],
};

for (const [name, script] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(script)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(script); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:4', () => liveRun(upd({ networks: [N({})], radios: [RAD({})] }))));
  assert.match(s.wnTable.h, /net/, 'the live table rendered no network');
  assert.equal(s.wnBadge.t, '1', 'the badge is ' + s.wnBadge.t);
}
{
  // The two empty states are different sentences.
  const none = JSON.parse(G.live('auto:3', () => liveRun(upd({ networks: [], stack: 'none' })))).wnTable.h;
  const unconfigured = JSON.parse(G.live('auto:2', () => liveRun(upd({ networks: [], stack: 'wifiwave2' })))).wnTable.h;
  assert.match(none, /no wireless interfaces/, 'the no-hardware state is wrong');
  assert.match(unconfigured, /No wireless networks are configured/, 'the unconfigured state is wrong');
  assert.notEqual(none, unconfigured, 'both empty states rendered the same sentence');
}
{
  // ONE COLOUR PER SSID. The same network on two radios must be coloured once —
  // a colour map built per ROW would give the 2.4 and 5 GHz halves of one
  // network two different colours.
  const same = JSON.parse(G.live('auto:1', () => liveRun(upd({ networks: [
    N({ radio: 'wifi1' }), N({ radio: 'wifi2', band: '2ghz-n' })],
    radios: [RAD({}), RAD({ name: 'wifi2', band: '2ghz-n' })] })))).wnTable.h;
  const colours = [...same.matchAll(/color:(#[0-9a-f]{3,6}|rgba?\([^)]*\))/gi)].map((m) => m[1]);
  assert.ok(colours.length >= 2, 'no ssid colours were applied at all');
  assert.equal(new Set(colours).size, 1,
    'one SSID on two radios got more than one colour: ' + JSON.stringify(colours));
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('wifi-page-check: %d cases identical', checked);
