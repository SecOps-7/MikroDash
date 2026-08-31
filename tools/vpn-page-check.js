'use strict';
/**
 * The VPN PAGE, live against ported.
 *
 * ── NOT THE SAME THING AS `vpn-card-check.js` ───────────────────────────────
 *
 * One live handler serves both the dashboard mini card and the VPN page, and the
 * existing card gate provides exactly one element — `vpnTable` — so everything
 * else that handler writes was uncompared: the peer grid, the PPP and IPsec
 * tables and their cards, the five summary stats and the page badge. That is why
 * `vpn` stayed on the ungated ledger while a gate with "vpn" in its name already
 * passed.
 *
 * ── A FOURTH PLACE PAGE CODE LIVES ──────────────────────────────────────────
 *
 * This handler sits at FILE SCOPE — no IIFE, no banner — so `region()` cannot
 * find it and there is nothing to bound. `lift.handler()` was added for it: it
 * brace-matches the inline function body of a named `socket.on`.
 *
 * WHAT IT CANNOT SEE: layout, focus, and the grid's card ordering beyond markup.
 *
 * ── TWO EQUIVALENT MUTANTS, BOTH WITH THE REASON ───────────────────────────
 *
 * `hsToSecs` has exactly ONE consumer on each side — the badge — and the badge
 * short-circuits before calling it. That makes two mutations unobservable, and
 * checking the call graph is what distinguishes "equivalent" from "untested":
 *
 *   - `'never' → 0` instead of `Infinity`: `vpnHsBadge` returns "Never
 *     connected" on `uptime === 'never'` BEFORE parsing, so the return value is
 *     never read for that input.
 *   - The week multiplier: the badge's coarsest band is "600 seconds or more",
 *     and any value expressed in weeks clears that by three orders of magnitude.
 *     A wrong multiplier cannot move it across a boundary.
 *
 * Both would become observable the moment anything sorts or displays the parsed
 * seconds. Nothing does today, on either side.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/vpn-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');
const L = require('./lib/lift.js');
// The live half is FROZEN so this gate outlives `../MikroDash` — see golden()
// in lib/lift.js. Re-freeze with: node tools/vpn-page-check.js --freeze
const G = L.golden('vpn-page-check');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const src = L.liveSource(ROOT);

const body = G.value('body', () => L.handler(src, 'vpn:update'));
assert.ok(body.includes('vpnStatTotal'), 'the lifted handler lost the page summary');
assert.ok(body.includes('vpnPppTbody'), 'the lifted handler lost the PPP table');
const IDS = G.value('IDS', () => L.idsFor(src, body));

// Declare what this gate provides, for `tools/element-coverage-audit.js`. Placed
// BEFORE the bundle step so asking costs nothing: a text scan cannot see ids
// derived at runtime, and guessing at them is what the audit exists to stop.
if (process.argv.includes('--ids')) { console.log(JSON.stringify(IDS)); process.exit(0); }

const FILE_ELS = G.value('FILE_ELS', () => L.fileScopeEls(src, body));

const ENTRY = path.join(ROOT, 'testdata', '.vpn-entry.ts');
fs.writeFileSync(ENTRY, "export { initVpnPage } from '../web/src/pages/vpn.js';\n");
const OUT = path.join(ROOT, 'testdata', '.vpn-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── ONE LIVE HANDLER, TWO PORT MODULES ──────────────────────────────────────
//
// The live handler writes the dashboard mini table AND the page. The port splits
// them: `dashboard-vpn.ts` owns `vpnTable`, `vpn.ts` owns the rest. Driving only
// the page module and then comparing `vpnTable` reports a difference that is the
// gate driving half the port, not the port disagreeing with the page — the same
// unfairness as seeding a selection on one side only.
//
// `vpnTable` is therefore excluded here and covered by `tools/vpn-card-check.js`,
// which provides that element and nothing else. Between the two gates the whole
// handler is compared; neither alone would say so.
const CARD_IDS = ['vpnTable'];
const PAGE_IDS = IDS.filter((id) => !CARD_IDS.includes(id));
assert.ok(PAGE_IDS.length >= 12, 'the page id list collapsed: ' + PAGE_IDS.join(','));

const snap = (doc) => {
  const n = doc.nodes;
  const g = (id) => (n[id] ? { h: n[id].innerHTML, t: n[id].textContent, c: n[id].className,
    d: n[id].style && n[id].style.display } : null);
  const out = {};
  for (const id of PAGE_IDS.slice().sort()) out[id] = g(id);
  return JSON.stringify(out);
};

function liveRun(payload) {
  const doc = makeDoc(IDS, {});
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt, parseFloat, isFinite,
    document: doc,
    setTimeout: () => 0, clearTimeout: () => {}, window: {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    L.line(src, 'function esc('),
    L.whole(src, 'function fmtMbps('),
    L.whole(src, 'function fmtBytes('),
    L.whole(src, 'function resRow('),
    L.whole(src, 'function parseDurationSec('),
    L.whole(src, 'function vpnHsBadge('),
    L.whole(src, 'function vpnHsToSecs('),
    'function $(id){return document.getElementById(id);}',
    // A settings-driven module-scope value (app.js:2841, overridden from the
    // saved page prefs). Taken from the live source rather than hard-coded, so a
    // change to the default reaches this gate instead of being invented here.
    L.line(src, 'var _vpnDashTopN'),
    L.declare(FILE_ELS),
    'function __run(data){' + body + '}',
  ].join('\n'), ctx);
  ctx.__run(payload);
  return snap(doc);
}

function portRun(payload) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const prevWin = globalThis.window;
  globalThis.window = {};
  try {
    return withDocument(doc, () => {
      delete require.cache[require.resolve(OUT)];
      require(OUT).initVpnPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
      if (!handlers['vpn:update']) throw new Error('the port registered no vpn:update handler');
      handlers['vpn:update'](payload);
      return snap(doc);
    });
  } finally {
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
      if (x !== y) shout('DIFF %s [%s]\n  live: %s\n  port: %s', what, k, x, y);
    }
  }
}

// `type: 'WireGuard'` is LOAD-BEARING, and so is the `tunnels` key below. The
// first version of this corpus used a `wgPeers` array of untyped peers; the live
// handler reads `data.tunnels` and filters on the type, so every case compared
// two empty pages and PASSED. Only the believability block caught it — the fifth
// time in this port that assertion has been the difference between a gate and a
// green light.
const PEER = (o) => Object.assign({
  id: '*1', type: 'WireGuard', name: 'phone', interface: 'wg0', endpoint: '198.51.100.9:51820',
  lastHandshake: '1m30s', state: 'active', rxRate: 125000, txRate: 250000,
  rx: 1048576, tx: 2097152, allowedAddress: '10.0.0.2/32',
}, o);
// Both shapes read off the live row builders rather than guessed. The IPsec row
// reads name/state/side/uptime/enc/auth — an earlier fixture invented
// srcAddress/dstAddress/rxBytes/txBytes and rendered a table of em dashes that
// the port matched exactly, proving nothing.
const PPP = (o) => Object.assign({
  name: 'dialin', service: 'pppoe', address: '10.1.1.2',
  callerId: '02:00:00:00:00:01', uptime: '2h', rx: 1048576, tx: 2097152,
}, o);
const IPSEC = (o) => Object.assign({
  name: 'peer-a', state: 'established', side: 'initiator', uptime: '3h',
  enc: 'aes-256-cbc', auth: 'sha256',
}, o);
const P = (o) => Object.assign({ tunnels: [], ppp: [], ipsec: [] }, o);

const CASES = {
  'nothing at all': [P({})],
  'one active peer': [P({ tunnels: [PEER({})] })],
  'several peers': [P({ tunnels: [PEER({}), PEER({ id: '*9', name: 'laptop' })] })],
  // The four peer states drive both the summary counters and the badges.
  'an active peer': [P({ tunnels: [PEER({ state: 'active' })] })],
  'a stale peer': [P({ tunnels: [PEER({ state: 'stale' })] })],
  'a peer that never handshook': [P({ tunnels: [PEER({ state: 'never', lastHandshake: '' })] })],
  'an idle peer': [P({ tunnels: [PEER({ state: 'idle' })] })],
  'one of each state': [P({ tunnels: [
    PEER({ id: '*1', state: 'active' }), PEER({ id: '*2', state: 'stale' }),
    PEER({ id: '*3', state: 'never', lastHandshake: '' }), PEER({ id: '*4', state: 'idle' })] })],
  // Peer fields.
  'a peer with no name falls back to the interface': [P({ tunnels: [PEER({ name: '' })] })],
  'a peer with neither name nor interface': [P({ tunnels: [PEER({ name: '', interface: '' })] })],
  'a peer with no endpoint': [P({ tunnels: [PEER({ endpoint: '' })] })],
  'a peer with no handshake': [P({ tunnels: [PEER({ lastHandshake: '' })] })],
  'a peer with zero rates': [P({ tunnels: [PEER({ rxRate: 0, txRate: 0 })] })],
  'a peer with null rates': [P({ tunnels: [PEER({ rxRate: null, txRate: null })] })],
  'a peer with no allowed address': [P({ tunnels: [PEER({ allowedAddress: '' })] })],
  // Handshake ordering: the dashboard table sorts by handshake age.
  'peers sort by handshake age': [P({ tunnels: [
    PEER({ id: '*1', name: 'old', lastHandshake: '5m' }),
    PEER({ id: '*2', name: 'new', lastHandshake: '10s' })] })],
  'an unparseable handshake': [P({ tunnels: [PEER({ lastHandshake: 'ages' })] })],
  // ZERO IS A REAL READING. A peer that has just handshaken reports `0s`, and
  // the port's `total || Infinity` turned that into the stalest possible value —
  // a freshly connected peer rendered red. This is the case that caught it.
  'a handshake of 0s is FRESH, not stale': [P({ tunnels: [PEER({ lastHandshake: '0s' })] })],
  'a handshake of 0m0s': [P({ tunnels: [PEER({ lastHandshake: '0m0s' })] })],
  // The badge's three colour bands, either side of each boundary.
  'handshake 2m59s is ok': [P({ tunnels: [PEER({ lastHandshake: '2m59s' })] })],
  'handshake 3m is warn': [P({ tunnels: [PEER({ lastHandshake: '3m' })] })],
  'handshake 9m59s is warn': [P({ tunnels: [PEER({ lastHandshake: '9m59s' })] })],
  'handshake 10m is stale': [P({ tunnels: [PEER({ lastHandshake: '10m' })] })],
  'handshake in weeks': [P({ tunnels: [PEER({ lastHandshake: '2w1d' })] })],
  'the literal string never': [P({ tunnels: [PEER({ lastHandshake: 'never' })] })],
  // PPP and IPsec, which the card gate never saw at all.
  'one ppp session': [P({ ppp: [PPP({})] })],
  'several ppp sessions': [P({ ppp: [PPP({}), PPP({ name: 'other' })] })],
  'a ppp session with no address': [P({ ppp: [PPP({ address: '' })] })],
  'a ppp session with zero counters': [P({ ppp: [PPP({ rx: 0, tx: 0 })] })],
  'a ppp session with no service': [P({ ppp: [PPP({ service: '' })] })],
  'a ppp session with no caller id': [P({ ppp: [PPP({ callerId: '' })] })],
  'one ipsec peer': [P({ ipsec: [IPSEC({})] })],
  'an ipsec peer that is not established': [P({ ipsec: [IPSEC({ state: 'connecting' })] })],
  'an ipsec peer with no side': [P({ ipsec: [IPSEC({ side: '' })] })],
  'an ipsec peer with no cipher': [P({ ipsec: [IPSEC({ enc: '', auth: '' })] })],
  'all three kinds at once': [P({ tunnels: [PEER({})], ppp: [PPP({})], ipsec: [IPSEC({})] })],
  // Escaping, in each table.
  'markup in a peer name': [P({ tunnels: [PEER({ name: '<img src=x>' })] })],
  'a quote in an endpoint': [P({ tunnels: [PEER({ endpoint: 'a"b' })] })],
  'markup in a ppp name': [P({ ppp: [PPP({ name: '<b>x</b>' })] })],
  'markup in an ipsec address': [P({ ipsec: [IPSEC({ name: '<i>peer</i>' })] })],
  // Missing collections entirely.
  'no tunnels key': [{ ppp: [], ipsec: [] }],
  // A non-WireGuard tunnel must be filtered OUT of every peer count.
  'a non-WireGuard tunnel is ignored': [P({ tunnels: [PEER({ type: 'OpenVPN' })] })],
  'WireGuard beside another type': [P({ tunnels: [PEER({}), PEER({ id: '*7', type: 'SSTP' })] })],
  'no ppp key': [{ tunnels: [PEER({})], ipsec: [] }],
  'no ipsec key': [{ tunnels: [PEER({})], ppp: [] }],
};

for (const [name, [payload]] of Object.entries(CASES)) {
  let a, b;
  try { a = G.live(name, () => liveRun(payload)); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(G.live('auto:3', () => liveRun(P({ tunnels: [PEER({})], ppp: [PPP({})], ipsec: [IPSEC({})] }))));
  assert.equal(s.vpnStatTotal.t, '1', 'the peer total is ' + s.vpnStatTotal.t);
  assert.equal(s.vpnStatConn.t, '1', 'the connected count is ' + s.vpnStatConn.t);
  // The half the card gate never reached.
  assert.match(s.vpnPppTbody.h, /dialin/, 'the PPP table rendered nothing');
  assert.match(s.vpnIpsecTbody.h, /peer-a/, 'the IPsec table rendered nothing');
  assert.ok(!/—<\/td>/.test(s.vpnIpsecTbody.h.slice(0, 200)),
    'the IPsec row is all em dashes — the fixture shape is wrong: ' + s.vpnIpsecTbody.h);
  assert.equal(s.vpnPageCount.t, '1', 'the page badge is ' + s.vpnPageCount.t);
}
{
  const s = JSON.parse(G.live('auto:2', () => liveRun(P({}))));
  assert.equal(s.vpnStatTotal.t, '0', 'an empty payload gave a total of ' + s.vpnStatTotal.t);
  assert.ok(!/active-blue/.test(s.vpnPageCount.c || ''), 'an empty page badge stayed active');
}
{
  // The states really do count differently.
  const one = JSON.parse(G.live('auto:1', () => liveRun(P({ tunnels: [
    PEER({ id: '*1', state: 'active' }), PEER({ id: '*2', state: 'stale' }),
    PEER({ id: '*3', state: 'never', lastHandshake: '' }), PEER({ id: '*4', state: 'idle' })] }))));
  assert.equal(one.vpnStatTotal.t, '4', 'total is ' + one.vpnStatTotal.t);
  assert.equal(one.vpnStatConn.t, '1', 'only the active peer is connected: ' + one.vpnStatConn.t);
  assert.equal(one.vpnStatStale.t, '1', 'stale count is ' + one.vpnStatStale.t);
  // NOT "everything not active", despite the element's name. The live handler
  // computes such a list — `var idle = wgPeers.filter(t => t.state !== 'active')`
  // at app.js:2061 — and then never reads it; `vpnStatIdle` is set from
  // `never.length` instead, deliberately, so a peer that connected once and went
  // away is not lumped in with one that never connected at all. Pinned to what
  // it DOES, with the reason, so nobody "fixes" it toward its label.
  assert.equal(one.vpnStatIdle.t, '1', 'the Idle stat counts NEVER-connected peers: ' + one.vpnStatIdle.t);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('vpn-page-check: %d cases identical', checked);
