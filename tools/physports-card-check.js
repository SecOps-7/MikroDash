'use strict';
/**
 * The Physical Ports card, live against ported.
 *
 * ── WHY THIS CARD WAITED ────────────────────────────────────────────────────
 *
 * Its `title` attribute was built with `dcEsc`, which does not escape quotes —
 * ToDo #16. Unlike the API Diagnostics card next door, the text going into this
 * attribute is ROUTER-SUPPLIED: an interface name. The live repo settled the
 * reachability question on hardware, confirming a name containing a raw quote
 * survives the API, so the card was held back rather than ported with the quirk
 * reproduced. It is ported here against the FIXED form.
 *
 * ── THE TWO PANELS ARE NOT MEANT TO MATCH ───────────────────────────────────
 *
 * The Interfaces page draws the same row from the same payload, and the two
 * disagree in two places on purpose. This gate pins BOTH differences, because
 * "make them consistent" is the obvious wrong instinct for a port:
 *
 *   - The card admits `sfp` and `sfp-sfpplus`; the panel takes `ether` alone.
 *   - The card's LABEL uses dcEsc, the panel's uses esc. Both are correct in
 *     text position and differ only on quotes, so the corpus carries an
 *     interface named with one and asserts the card leaves it RAW — an
 *     assertion that fails if someone "fixes" the label to match its neighbour.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/physports-card-check.js
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
const G = LIFT.golden('physports-card-check');
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
function slice(decl) {
  // NO REFERENCE, NO SLICE. Its callers feed `liveRun`, which from here on runs
  // only inside a frozen closure — so returning '' is never reached rather than
  // being a silent empty lift.
  if (src === '') return '';
  const i = src.indexOf(decl);
  assert.ok(i > 0, 'not found: ' + decl);
  return decl + '{' + braceBody(i) + '}';
}

const iifeAt = src.indexOf('All 14 new cards live here');
const at = src.indexOf("socket.on('ifstatus:update'", iifeAt);
if (LIFT.hasReference(ROOT)) assert.ok(at > iifeAt, 'no ifstatus:update handler in the extra-cards IIFE');
const body = braceBody(at);
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('dc-ifPortsPanel'), 'the slice is not the ports handler');
if (LIFT.hasReference(ROOT)) assert.ok(body.includes('sfp-sfpplus'), 'the slice lost its interface filter');

const portSvgSrc = slice('function portSvg(sz) ');
const escSrc = (() => { const i = src.indexOf('function dcEsc('); return src.slice(i, src.indexOf('\n', i)); })();
const escPageSrc = (() => { const i = src.indexOf('function esc('); return src.slice(i, src.indexOf('\n', i)); })();

const ENTRY = path.join(ROOT, 'testdata', '.phys-entry.ts');
fs.writeFileSync(ENTRY, "export { renderPhysPortsCard } from '../web/src/pages/dashboard-card-physports.js';\n");
const OUT = path.join(ROOT, 'testdata', '.phys-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function makeDom() {
  const byId = new Map();
  const mk = (id) => {
    const n = {
      id,
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      set innerHTML(v) { n._h = v; },
      get innerHTML() {
        if (n._h !== undefined) return n._h;
        return String(n._t === undefined ? '' : n._t)
          .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      },
    };
    if (id) byId.set(id, n);
    return n;
  };
  mk('dc-ifPortsPanel');
  return { byId, mk };
}
const snap = (d) => d.byId.get('dc-ifPortsPanel').innerHTML;

function liveRun(payload) {
  const d = makeDom();
  const ctx = {
    String, Array, Math, Number,
    dcEl: (id) => d.byId.get(id) || null,
    document: { createElement: () => d.mk('') },
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + escPageSrc + '\n' + portSvgSrc +
    '\nfunction __run(data){' + body + '}', ctx);
  ctx.__run(payload);
  return snap(d);
}
function portRun(payload) {
  const d = makeDom();
  globalThis.document = { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderPhysPortsCard(payload);
  return snap(d);
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) shout('DIFF %s\n  live: %s\n  port: %s', what, a, b);
}

const I = (name, type, extra) => Object.assign(
  { name, type, running: true, disabled: false, ips: [] }, extra || {});

const CASES = {
  'a normal payload': { interfaces: [I('ether1', 'ether'), I('ether2', 'ether', { running: false })] },
  'no interfaces': { interfaces: [] },
  'no interfaces key': {},
  'an empty payload': {},
  // NO `undefined` case: the live handler dereferences `data` unguarded and
  // throws. socket.io does not deliver undefined for this event, so a case for
  // it would only be testing a guard the port had no business adding.
  // Only physical types survive the filter — and sfp DOES, unlike the panel.
  'a bridge and a vlan are filtered out': {
    interfaces: [I('bridge1', 'bridge'), I('vlan10', 'vlan'), I('ether1', 'ether')],
  },
  'ONLY non-physical types renders the empty state': {
    interfaces: [I('bridge1', 'bridge'), I('wg0', 'wg')],
  },
  'sfp is physical here': { interfaces: [I('sfp1', 'sfp')] },
  'sfp-sfpplus is physical here': { interfaces: [I('sfp-sfpplus1', 'sfp-sfpplus')] },
  'sfpplus is NOT (no such type in the filter)': { interfaces: [I('x', 'sfpplus')] },
  // The three states, and their precedence: disabled beats running.
  'a down port': { interfaces: [I('ether1', 'ether', { running: false })] },
  'a disabled port': { interfaces: [I('ether1', 'ether', { disabled: true })] },
  'disabled AND running — disabled wins the state': {
    interfaces: [I('ether1', 'ether', { disabled: true, running: true })],
  },
  // …but the TITLE reads the flags in the other order: running is tested first,
  // so a disabled-and-running port is marked `dis` and titled "(up)".
  'the title and the state disagree on disabled+running': {
    interfaces: [I('ether1', 'ether', { disabled: true, running: true })],
  },
  // Address handling: only the FIRST is shown.
  'a port with one address': { interfaces: [I('ether1', 'ether', { ips: ['198.51.100.1/24'] })] },
  'a port with several — only the first': {
    interfaces: [I('ether1', 'ether', { ips: ['198.51.100.1/24', '198.51.100.9/24'] })],
  },
  'a port with no ips key': { interfaces: [{ name: 'ether1', type: 'ether', running: true, disabled: false }] },
  'a port with an empty ips array': { interfaces: [I('ether1', 'ether', { ips: [] })] },
  // The size ladder, on both sides of every boundary.
  '8 ports': { interfaces: Array.from({ length: 8 }, (_, i) => I('e' + i, 'ether')) },
  '9 ports': { interfaces: Array.from({ length: 9 }, (_, i) => I('e' + i, 'ether')) },
  '16 ports': { interfaces: Array.from({ length: 16 }, (_, i) => I('e' + i, 'ether')) },
  '17 ports': { interfaces: Array.from({ length: 17 }, (_, i) => I('e' + i, 'ether')) },
  '24 ports': { interfaces: Array.from({ length: 24 }, (_, i) => I('e' + i, 'ether')) },
  '25 ports': { interfaces: Array.from({ length: 25 }, (_, i) => I('e' + i, 'ether')) },
  '1 port': { interfaces: [I('ether1', 'ether')] },
  // Escaping. The name reaches an attribute AND a text node, by two different
  // escapers, so each hostile name is checked in both positions at once.
  'a name with a QUOTE (ToDo #16)': { interfaces: [I('ether1" onmouseover="x', 'ether')] },
  'a name with an apostrophe': { interfaces: [I("ether'1", 'ether')] },
  'a name with markup': { interfaces: [I('<img src=x onerror=y>', 'ether')] },
  'a name with an ampersand': { interfaces: [I('a&b', 'ether')] },
  'an ADDRESS with a quote': { interfaces: [I('ether1', 'ether', { ips: ['198.51.100.1" x'] })] },
  'a name that is empty': { interfaces: [I('', 'ether')] },
  'a real-world mix': {
    interfaces: [
      I('ether1', 'ether', { ips: ['198.51.100.1/24'] }),
      I('ether2', 'ether', { running: false }),
      I('sfp-sfpplus1', 'sfp-sfpplus', { disabled: true }),
      I('bridge1', 'bridge'),
    ],
  },
};

for (const [name, payload] of Object.entries(CASES)) {
  cmp(name, G.live(name, () => liveRun(payload)), portRun(payload));
}

// ── believability, RE-AIMED AT THE PORT ────────────────────────────────────
//
// "the live side must actually be producing these things" was the point, and the
// port is now the side that must. Without these the comparisons could be two
// empty strings agreeing.
{
  const s = portRun({ interfaces: [I('ether1', 'ether', { ips: ['198.51.100.1/24'] })] });
  assert.match(s, /class="if-port-item" data-state="up"/, 'no port item rendered');
  assert.match(s, /<svg class="if-port-svg"/, 'portSvg did not run');
  assert.match(s, /198\.51\.100\.1\/24/, 'the address is missing from the title');
  assert.match(s, /\(up\)/, 'the title lost its state suffix');
}
{
  const s = portRun({ interfaces: [] });
  assert.match(s, /No ethernet ports/, 'the empty state did not render');
  assert.ok(!/if-port-item/.test(s), 'the empty state rendered a port');
}
{
  // ToDo #16, FIXED — RE-AIMED AT THE PORT. The quote in the TITLE is escaped, so
  // the attribute holds and `onmouseover` cannot become one. Written as the
  // assertion that would turn red the moment anyone reverts it.
  //
  // This is an XSS regression guard, and it was pointed at the LIVE card — the
  // one that stops existing. The port is the card that ships, so the port is what
  // must keep escaping. Of everything in this gate this is the assertion least
  // able to afford being quietly converted to nothing.
  const s = portRun({ interfaces: [I('ether1" onmouseover="x', 'ether')] });
  assert.match(s, /title="ether1&quot; onmouseover=&quot;x/,
    'the interface name is not escaped for attribute context — ToDo #16 is back: ' + s);
  assert.ok(!/title="ether1" onmouseover="x/.test(s), 'the title attribute is closable');
  // …and the LABEL, in text position, deliberately leaves the quote alone. This
  // is the difference from the Interfaces panel, pinned so it is not "fixed".
  assert.match(s, /<span class="if-port-label">ether1" onmouseover="x<\/span>/,
    'the label no longer uses dcEsc — it has been unified with the Interfaces panel: ' + s);
}
{
  // The size ladder is real, not a constant — RE-AIMED AT THE PORT, which is the
  // side that must keep stepping the port width down as the count rises.
  const eight = portRun({ interfaces: Array.from({ length: 8 }, (_, i) => I('e' + i, 'ether')) });
  const nine = portRun({ interfaces: Array.from({ length: 9 }, (_, i) => I('e' + i, 'ether')) });
  assert.match(eight, /width="44"/, '8 ports did not draw at 44');
  assert.match(nine, /width="36"/, '9 ports did not draw at 36');
}
{
  // The sfp difference from the Interfaces panel. It said "asserted on the live
  // side" and that is exactly what had to change: the difference is a property of
  // THIS card, and the card that ships is the port's.
  const s = portRun({ interfaces: [I('sfp1', 'sfp')] });
  assert.match(s, /if-port-item/, 'the dashboard card dropped sfp — it should admit it');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('physports-card-check: %d cases identical', checked);
