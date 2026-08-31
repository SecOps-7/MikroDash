'use strict';
/**
 * The INTERFACES page's Ports panel, live against ported.
 *
 * ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
 *
 * It was found missing while porting the Dashboard's Physical Ports card. The
 * two draw the same row from the same payload, and `tools/live-renderer.js
 * interfaces` looked like it covered this one — but that tool only WRITES a
 * comparison bundle and exits 0. Mutating the shared `portSvg` and watching it
 * report success is what showed it asserts nothing about this panel. A gate that
 * has never been shown to fail is not a gate.
 *
 * The two panels differ on purpose — `ether` only here, and `esc` in the label
 * where the card uses `dcEsc`. Both differences are pinned on both sides, so
 * "make them consistent" fails in whichever direction it is attempted.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/ifports-panel-check.js
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
const G = LIFT.golden('ifports-panel-check');
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
  const n = src.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  const i = src.indexOf(decl);
  return decl + '{' + braceBody(i) + '}';
}

const renderSrc = G.value('renderSrc', () => slice('function renderIfPorts(ifaces) '));
// The live page's element helper is `$`; the port's is `el`. Same function,
// different name — the sandbox supplies `$` so the lifted source runs as written
// rather than being edited to match the port, which would be the gate testing a
// rewrite of the live code instead of the live code.
assert.ok(renderSrc.includes('ifPortsPanel'), 'the slice is not the ports panel');
assert.ok(!renderSrc.includes('sfp'), 'this panel is ether-only; the slice looks like the dashboard card');
const portSvgSrc = G.value('portSvgSrc', () => slice('function portSvg(sz) '));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['renderSrc', renderSrc], ['portSvgSrc', portSvgSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
const escPageSrc = G.value('escPageSrc', () => { const i = src.indexOf('function esc('); return src.slice(i, src.indexOf('\n', i)); });
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['escPageSrc', escPageSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.ifp-entry.ts');
fs.writeFileSync(ENTRY, "export { renderIfPorts } from '../web/src/pages/interfaces.js';\n");
const OUT = path.join(ROOT, 'testdata', '.ifp-port.cjs');
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
      get innerHTML() { return n._h === undefined ? '' : n._h; },
    };
    if (id) byId.set(id, n);
    return n;
  };
  mk('ifPortsPanel');
  return { byId, mk };
}
const snap = (d) => d.byId.get('ifPortsPanel').innerHTML;

function liveRun(ifaces) {
  const d = makeDom();
  const ctx = {
    String, Array, Math, Number,
    document: { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') },
  };
  vm.createContext(ctx);
  vm.runInContext(escPageSrc + '\n' + portSvgSrc + '\n' +
    'function $(id){return document.getElementById(id);}\n' + renderSrc, ctx);
  ctx.renderIfPorts(ifaces);
  return snap(d);
}
function portRun(ifaces) {
  const d = makeDom();
  globalThis.document = { getElementById: (id) => d.byId.get(id) || null, createElement: () => d.mk('') };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderIfPorts(ifaces);
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
  'a normal payload': [I('ether1', 'ether'), I('ether2', 'ether', { running: false })],
  'no interfaces': [],
  // THE DIFFERENCE FROM THE DASHBOARD CARD: sfp is not physical here.
  'sfp is NOT drawn on this panel': [I('sfp1', 'sfp')],
  'sfp-sfpplus is NOT drawn either': [I('sfp-sfpplus1', 'sfp-sfpplus')],
  'sfp alongside ether — only the ether': [I('sfp1', 'sfp'), I('ether1', 'ether')],
  'a bridge and a vlan are filtered out': [I('bridge1', 'bridge'), I('vlan10', 'vlan'), I('ether1', 'ether')],
  'a down port': [I('ether1', 'ether', { running: false })],
  'a disabled port': [I('ether1', 'ether', { disabled: true })],
  'disabled AND running': [I('ether1', 'ether', { disabled: true, running: true })],
  'one address': [I('ether1', 'ether', { ips: ['198.51.100.1/24'] })],
  'several — only the first': [I('ether1', 'ether', { ips: ['198.51.100.1/24', '198.51.100.9/24'] })],
  'no ips key': [{ name: 'ether1', type: 'ether', running: true, disabled: false }],
  '8 ports': Array.from({ length: 8 }, (_, i) => I('e' + i, 'ether')),
  '9 ports': Array.from({ length: 9 }, (_, i) => I('e' + i, 'ether')),
  '16 ports': Array.from({ length: 16 }, (_, i) => I('e' + i, 'ether')),
  '17 ports': Array.from({ length: 17 }, (_, i) => I('e' + i, 'ether')),
  '24 ports': Array.from({ length: 24 }, (_, i) => I('e' + i, 'ether')),
  '25 ports': Array.from({ length: 25 }, (_, i) => I('e' + i, 'ether')),
  'a name with a QUOTE': [I('ether1" onmouseover="x', 'ether')],
  'a name with markup': [I('<img src=x>', 'ether')],
  'a name with an ampersand': [I('a&b', 'ether')],
  'an address with a quote': [I('ether1', 'ether', { ips: ['198.51.100.1" x'] })],
  'an empty name': [I('', 'ether')],
};

for (const [name, ifaces] of Object.entries(CASES)) cmp(name, liveRun(ifaces), portRun(ifaces));

// ── believability ──────────────────────────────────────────────────────────
{
  const s = liveRun([I('ether1', 'ether', { ips: ['198.51.100.1/24'] })]);
  assert.match(s, /class="if-port-item" data-state="up"/, 'no port item rendered');
  assert.match(s, /<svg class="if-port-svg"/, 'portSvg did not run');
  assert.match(s, /198\.51\.100\.1\/24/, 'the address is missing');
}
{
  assert.match(liveRun([]), /No ethernet ports/, 'the empty state did not render');
  assert.ok(!/if-port-item/.test(liveRun([I('sfp1', 'sfp')])),
    'this panel drew an sfp — it is ether-only, unlike the dashboard card');
}
{
  // This panel escapes the label for ATTRIBUTE context in a TEXT position —
  // harmless, and the opposite choice from the dashboard card. Pinned so the
  // two are not quietly unified.
  const s = liveRun([I('a"b', 'ether')]);
  assert.match(s, /<span class="if-port-label">a&quot;b<\/span>/,
    'the label no longer uses esc — it has been unified with the dashboard card: ' + s);
  assert.match(s, /title="a&quot;b/, 'the title is not attribute-escaped');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('ifports-panel-check: %d cases identical', checked);
