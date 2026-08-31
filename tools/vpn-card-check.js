'use strict';
/**
 * The Dashboard's VPN card, live against ported.
 *
 * ── THE SORT IS THE INTERESTING PART ────────────────────────────────────────
 *
 * Peers are ordered by how long ago they last handshook, most recent first, and
 * `parseDurationSec` decides that. Two of its answers are Infinity: a missing or
 * `never` handshake, and — because it ends `return m || Infinity` — a parsed
 * ZERO. So `0s`, the most recent handshake possible, sorts with the oldest.
 *
 * That is reachable: a peer that handshook this instant reports `0s`. A port
 * that "fixed" it would put a different peer at the top of the card.
 *
 * The corpus therefore carries durations in every shape the field takes, and the
 * cases that pin the sort are the ones where two peers differ only in it.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/vpn-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/vpn-card-check.js --freeze
const G = L.golden('vpn-card-check');
const src = L.liveSource(ROOT, path.join('public', 'app.js'));

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
// The MINI CARD half of the handler only: the rest of it fills the VPN page's
// summary stats, which belong to that page and are ported there.
// Sliced to the NEXT SECTION MARKER, not to the first `}` at this indent — that
// one closes the `if` and leaves the `else` branch, which is the half that
// actually renders rows, outside the slice. The live side then rendered nothing
// and every case "differed".
const cardSrc = (() => {
  const from = src.indexOf('  connected.sort(function(a,b){');
  const to = src.indexOf('  // ── VPN page summary stats', from);
// GUARDED ON THE REFERENCE — this validates the LIFT, which is meaningless
// when there is nothing to lift from. See L.hasReference in lib/lift.js.
  if (L.hasReference(ROOT) && (from === -1 || to === -1)) throw new Error('cannot bound the VPN mini card');
  return src.slice(from, to);
})();
// GUARDED ON THE REFERENCE — validates the LIFT, meaningless without one.
if (L.hasReference(ROOT) && !cardSrc.includes('.join(\'\')')) throw new Error('the mini-card slice lost its row builder');
const durSrc = slice('function parseDurationSec(s){', '\n}', 'parseDurationSec');
const escSrc = slice('function esc(', '\n}', 'esc');

const ENTRY = path.join(ROOT, 'testdata', '.vpncard-entry.ts');
fs.writeFileSync(ENTRY,
  "export { renderVpnCard, parseDurationSec } from '../web/src/pages/dashboard-vpn.js';\n" +
  "export { applyPageVisibility } from '../web/src/caps.js';\n");
const OUT = path.join(ROOT, 'testdata', '.vpncard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

function liveRun(tunnels, topN) {
  const table = { innerHTML: '' };
  const ctx = {
    String, Array, JSON, Number, parseInt, Infinity, RegExp,
    vpnTable: table,
    _vpnDashTopN: topN,
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\n' + durSrc + '\n' +
    'function __render(allTunnels){' +
    "  var wgPeers = allTunnels.filter(function(t){ return t.type === 'WireGuard'; });" +
    "  var connected = wgPeers.filter(function(t){ return t.state === 'active'; });" +
    cardSrc + '\n}', ctx);
  ctx.__render(tunnels);
  return table.innerHTML;
}

function portRun(tunnels, topN) {
  const table = { innerHTML: '' };
  const saved = global.document;
  global.document = {
    getElementById: (id) => (id === 'vpnTable' ? table : null),
    querySelectorAll: () => [],
    addEventListener() {},
  };
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    // topN reaches the card through the REAL settings path, so this also pins
    // that the card reads the value a settings broadcast writes.
    mod.applyPageVisibility({ vpnDashTopN: topN });
    mod.renderVpnCard({ tunnels });
  } finally {
    if (saved === undefined) delete global.document; else global.document = saved;
  }
  return table.innerHTML;
}

const bad = [];
let cases = 0;
function compare(what, tunnels, topN = 5) {
  cases++;
  const a = G.live(G.seq(), () => liveRun(tunnels, topN)), b = portRun(tunnels, topN);
  if (a !== b) bad.push(what + '\n  live: ' + a + '\n  port: ' + b);
}

const P = (o) => ({ type: 'WireGuard', state: 'active', ...o });

compare('one active peer', [P({ name: 'phone', lastHandshake: '1m30s', endpoint: '198.51.100.4:51820' })]);
compare('a peer with no endpoint', [P({ name: 'phone', lastHandshake: '20s' })]);
compare('a peer with no name, falling back to the interface', [P({ interface: 'wg0', lastHandshake: '5s' })]);
compare('a peer with neither name nor interface', [P({ lastHandshake: '5s' })]);
compare('a peer with no handshake', [P({ name: 'x' })]);

// ── The sort ────────────────────────────────────────────────────────────────
compare('ordered by recency', [
  P({ name: 'old', lastHandshake: '2h' }),
  P({ name: 'new', lastHandshake: '10s' }),
  P({ name: 'middling', lastHandshake: '5m' })]);
compare('a ZERO handshake sorts LAST, not first', [
  P({ name: 'zero', lastHandshake: '0s' }),
  P({ name: 'ten', lastHandshake: '10s' })]);
compare('never sorts last', [
  P({ name: 'never', lastHandshake: 'never' }),
  P({ name: 'recent', lastHandshake: '1s' })]);
compare('never and zero together', [
  P({ name: 'never', lastHandshake: 'never' }),
  P({ name: 'zero', lastHandshake: '0s' }),
  P({ name: 'one', lastHandshake: '1s' })]);
compare('weeks and days', [
  P({ name: 'w', lastHandshake: '1w' }),
  P({ name: 'd', lastHandshake: '8d' }),
  P({ name: 'h', lastHandshake: '200h' })]);
compare('a compound duration', [
  P({ name: 'a', lastHandshake: '1w2d3h4m5s' }),
  P({ name: 'b', lastHandshake: '9d' })]);
compare('a duration with no unit', [
  P({ name: 'bare', lastHandshake: '42' }),
  P({ name: 'unit', lastHandshake: '42s' })]);

// ── Filtering ───────────────────────────────────────────────────────────────
compare('non-WireGuard tunnels are excluded', [
  P({ name: 'wg', lastHandshake: '1s' }),
  { type: 'OpenVPN', state: 'active', name: 'ovpn', lastHandshake: '0s' }]);
compare('inactive peers are excluded', [
  P({ name: 'up', lastHandshake: '1s' }),
  P({ name: 'stale', state: 'stale', lastHandshake: '2s' }),
  P({ name: 'never', state: 'never' })]);
compare('every peer inactive', [P({ name: 'a', state: 'stale' })]);
compare('no tunnels', []);

// ── The cut ─────────────────────────────────────────────────────────────────
const many = Array.from({ length: 9 }, (_, i) => P({ name: 'p' + i, lastHandshake: (i + 1) + 's' }));
compare('more peers than the cut', many, 5);
compare('a cut of 1', many, 1);
compare('a cut larger than the list', many, 50);

// Operator-supplied values.
compare('a name that is markup', [P({ name: '<script>x</script>', lastHandshake: '1s' })]);
compare('an endpoint with quotes', [P({ name: 'x', endpoint: '"1.2.3.4:1"', lastHandshake: '1s' })]);

fs.rmSync(OUT, { force: true });
if (bad.length) {
  console.error('the VPN card differs from the live one:\n\n' + bad.slice(0, 2).join('\n\n') + '\n');
  process.exit(1);
}
console.log('the VPN card matches the live one (' + cases + ' cases: the recency sort including ' +
  'its two Infinities, the filters, the cut and the escaping)');
