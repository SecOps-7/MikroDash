'use strict';
/**
 * The Dashboard's Top Talkers card, live against ported, by DOM equality.
 *
 * ── THE CASES ARE THE ONES THE LIVE COMMENT WARNS ABOUT ─────────────────────
 *
 *   an empty list CLEARS      it is news, not silence. Treating it as "nothing
 *                             changed" left the previous rows up while the stale
 *                             timer had just been re-armed by that payload — so
 *                             the card looked healthy and showed devices the
 *                             router had stopped reporting.
 *   available:false           says "Kid Control is not available", not the
 *                             narrower "No devices", which would be a guess.
 *   available absent          IS NOT available:false. Only an explicit false
 *                             makes the stronger claim.
 *   a device with no name     renders an em dash, not an empty cell.
 *
 * `fmtMbps` is LIFTED from the live source rather than stubbed: the number
 * formatting is most of what this card renders, and a stub would compare a
 * placeholder against an implementation.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/talkers-render-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/talkers-render-check.js --freeze
const G = L.golden('talkers-render-check');
const src = L.liveSource(ROOT);

function slice(decl, close, name) {
  // NO REFERENCE, NO SLICE. `L.liveSource` returns '' when `../MikroDash` is
  // absent, and this then threw `cannot find <name>` at module scope — before
  // any frozen output could be served. An empty slice is harmless because the
  // live half is never entered when the output is frozen.
  if (src === '') return '';
  const i = src.indexOf(decl);
  if (i === -1) throw new Error('cannot find ' + name + ' — it has moved or been rewritten');
  const j = src.indexOf(close, i);
  if (j === -1) throw new Error(name + ' is never closed');
  return src.slice(i, j + close.length);
}
const handlerSrc = slice("socket.on('talkers:update',function(data){", '\n});', 'the talkers handler');
const fmtSrc = slice('function fmtMbps(', '\n}', 'fmtMbps');
const escSrc = slice('function esc(', '\n}', 'esc');

const ENTRY = path.join(ROOT, 'testdata', '.talkers-entry.ts');
fs.writeFileSync(ENTRY, "export { renderTalkers } from '../web/src/pages/dashboard-talkers.js';\n");
const OUT = path.join(ROOT, 'testdata', '.talkers-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function liveRun(payload) {
  const table = { innerHTML: '' };
  const handlers = {};
  const ctx = {
    String, Array, JSON, Number, Math,
    socket: { on: (n, f) => { handlers[n] = f; } },
    talkersTable: table,
    lastTalkers: null,
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + fmtSrc + '\n' + handlerSrc, ctx);
  handlers['talkers:update'](payload);
  return table.innerHTML;
}

function portRun(payload) {
  const table = { innerHTML: '' };
  const saved = global.document;
  global.document = { getElementById: (id) => (id === 'talkersTable' ? table : null) };
  try {
    delete require.cache[require.resolve(OUT)];
    require(OUT).renderTalkers(payload);
  } finally {
    if (saved === undefined) delete global.document; else global.document = saved;
  }
  return table.innerHTML;
}

const bad = [];
let cases = 0;
function compare(what, payload) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(payload)), b = portRun(payload);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

const D = (name, mac, rx, tx) => ({ name, mac, rx_mbps: rx, tx_mbps: tx });

compare('one device', { devices: [D('laptop', '02:00:00:00:00:01', 1.5, 0.25)], available: true });
compare('five devices', { devices: [
  D('a', '02:00:00:00:00:01', 12.5, 3.25), D('b', '02:00:00:00:00:02', 0, 0),
  D('c', '02:00:00:00:00:03', 0.001, 999.999), D('d', '02:00:00:00:00:04', 1, 1),
  D('e', '02:00:00:00:00:05', 0.5, 0.5)], available: true });
// The empty cases, which are the whole point.
compare('no devices, available', { devices: [], available: true });
compare('no devices, available FALSE', { devices: [], available: false });
compare('no devices, available ABSENT', { devices: [] });
compare('no devices key at all', {});
compare('devices null', { devices: null });
compare('available is a falsy value that is NOT false', { devices: [], available: 0 });
compare('available undefined explicitly', { devices: [], available: undefined });
// Missing fields on a device.
compare('a device with no name', { devices: [D(undefined, '02:00:00:00:00:01', 1, 2)], available: true });
compare('a device with an EMPTY name', { devices: [D('', '02:00:00:00:00:01', 1, 2)], available: true });
compare('a device with no mac', { devices: [D('x', undefined, 1, 2)], available: true });
compare('a device with no rates', { devices: [{ name: 'x', mac: '02:00:00:00:00:01' }], available: true });
compare('a device with null rates', { devices: [D('x', '02:00:00:00:00:01', null, null)], available: true });
// Names that are markup — device names come from Kid Control, which an operator types.
compare('a name that is markup', {
  devices: [D('<script>alert(1)</script>', '02:00:00:00:00:01', 1, 2)], available: true });
compare('a name with quotes and an ampersand', {
  devices: [D('Kirsten\'s "iPhone" & iPad', '02:00:00:00:00:01', 1, 2)], available: true });
// Rates at the edges of the formatter.
for (const [rx, tx] of [[0, 0], [0.0004, 0.0004], [0.5, 0.5], [1, 1], [999.5, 1000], [1e6, 1e-6]]) {
  compare('rates ' + rx + '/' + tx, { devices: [D('x', '02:00:00:00:00:01', rx, tx)], available: true });
}

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the Top Talkers card differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
  process.exit(1);
}
console.log('the Top Talkers card matches the live one (' + cases + ' cases: the two empty ' +
  'states, missing fields, markup in a name, and the rate formatter\'s edges)');
