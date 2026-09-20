/**
 * THE WIREGUARD PAGE'S PEERS TAB.
 *
 * It is a hand-built panel rather than a generated table because two of its
 * columns cannot be generated: a transfer RATE needs two readings and a clock,
 * and "active / stale / never" is a word DERIVED from the age of a handshake
 * rather than anything a router returns. So what is worth pinning is the panel's
 * contract with area.ts, and the derivations that justified building it.
 *
 * ── WHAT EACH CASE IS FOR ───────────────────────────────────────────────────
 *
 *   the tab           a panel with no registered module has NO TAB AT ALL
 *                     (area.ts panelsOf), so "the module is wired" and "the tab
 *                     exists" are the same assertion.
 *   never vs stale    two different problems: a config that was never installed
 *                     against a client that left. Rendering both as "not
 *                     connected" hides which one is on screen.
 *   overlap           two peers claiming one address is silent in RouterOS and
 *                     silently breaks routing — WireGuard routes by longest
 *                     prefix, so only one of them ever receives the traffic.
 *   hidden            `vpn:update` arrives every few seconds whether or not this
 *                     tab is up. Drawing while hidden is work charged to every
 *                     viewer of the Interfaces tab.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc, RES_MODAL_IDS } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const KEY = 'wireguard';

const ENTRY = path.join(ROOT, 'testdata', '.wgpeers-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { initAreaPages } from '../web/src/pages/area.js';",
  "export { initWireguardPeers, overlappingPeers } from '../web/src/pages/wireguard-peers.js';",
  "export { AREAS } from '../web/src/gen/areas.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.wgpeers.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const area = mod.AREAS.find((a) => a.key === KEY);
assert.ok(area, 'internal/areas declares no wireguard area');

const ids = [...['areaBody-', 'areaBadge-', 'areaTabs-', 'areaAdd-', 'areaGroupTable-', 'areaGroupNote-'].map((p) => p + KEY),
  'areaPanel-wireguard-peers', 'wgPeerHead', 'wgPeerRows'];
const doc = makeDoc(ids, { allowUnknown: [...RES_MODAL_IDS], query: { '[data-res-add]': [], '[data-res-rows]': [] } });
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
(globalThis as any).CustomEvent = function (type, init) { return { type, detail: init && init.detail }; };

const handlers = {};
const socket = { on: (ev, fn) => { (handlers[ev] = handlers[ev] || []).push(fn); }, emit: () => {} };
// THE ORDER MATTERS, and main.ts has the same comment: a panel registered after
// the area pages have drawn their tab strip has no tab.
mod.initWireguardPeers(socket);
mod.initAreaPages(socket, (p) => p === KEY);
const fire = (ev, d) => (handlers[ev] || []).forEach((fn) => fn(d));
const n = doc.nodes;
const host = n['areaPanel-wireguard-peers'];

const target = (map) => ({
  id: '',
  closest: (sel) => (map[sel] ? { getAttribute: (a) => (a in map[sel] ? map[sel][a] : null), textContent: '' } : null),
});

function peer(over) {
  return Object.assign({
    id: '*1', publicKey: 'k1', type: 'WireGuard', name: 'peer', state: 'active',
    comment: '', lastHandshake: '5s', keepalive: '25s', endpoint: '198.51.100.9:13231',
    allowedIp: '10.0.0.2/32', interface: 'wg0', rx: 100, tx: 200, rxRate: 0, txRate: 0,
    disabled: false, responder: false,
  }, over);
}
const update = (tunnels) => fire('vpn:update', { ts: 1, tunnels, ppp: [], ipsec: [], pollMs: 10000 });

let failed = 0;
function check(what, fn) {
  try { fn(); say('  ok   ' + what); } catch (e) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('wireguard: the peers tab');

// ── the pure derivation, driven directly ────────────────────────────────────
check('overlapping allowed addresses are found, per interface', () => {
  const clash = mod.overlappingPeers([
    peer({ publicKey: 'a', allowedIp: '10.0.0.2/32', interface: 'wg0' }),
    peer({ publicKey: 'b', allowedIp: '10.0.0.2/32', interface: 'wg0' }),
    peer({ publicKey: 'c', allowedIp: '10.0.0.3/32', interface: 'wg0' }),
    // SAME ADDRESS, DIFFERENT INTERFACE: two tunnels are two routing domains,
    // so this is not a clash and flagging it would make the warning noise.
    peer({ publicKey: 'd', allowedIp: '10.0.0.2/32', interface: 'wg1' }),
  ]);
  assert.ok(clash.has('a') && clash.has('b'), 'two peers sharing an address were not flagged');
  assert.ok(!clash.has('c'), 'a peer with its own address was flagged');
  assert.ok(!clash.has('d'), 'a peer on another interface was flagged');
});

check('a comma separated allowed list is split before comparing', () => {
  const clash = mod.overlappingPeers([
    peer({ publicKey: 'a', allowedIp: '10.0.0.2/32, 10.0.0.9/32' }),
    peer({ publicKey: 'b', allowedIp: '10.0.0.9/32' }),
  ]);
  assert.ok(clash.has('a') && clash.has('b'), 'an overlap inside a comma separated list was missed');
});

// ── the panel is hidden until its tab is chosen ─────────────────────────────
fire('router:switched', { activeId: 'r1' });
fire('area:update', {
  ts: 1, pollMs: 60000, area: KEY, title: area.title, denied: false,
  tables: area.tables.map((t) => ({
    resource: t.resource, title: t.title, columns: t.columns, rows: [],
    unsupported: false, singleton: false,
  })),
});
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: KEY });

// ── choosing the tab ────────────────────────────────────────────────────────
const at = area.tables.length;
check('the Peers tab follows the tables, and choosing it shows the panel', () => {
  const tabs = String(n['areaTabs-' + KEY].innerHTML);
  assert.ok(new RegExp('data-areatabindex="' + at + '">Peers</button>').test(tabs),
    'no Peers tab after the tables — a panel with no registered module has none: ' + tabs);
  doc.dispatchEvent({ type: 'click', target: target({ '[data-areatab]': { 'data-areatab': KEY, 'data-areatabindex': String(at) } }) });
  assert.strictEqual(n['areaBody-' + KEY].style.display, 'none', 'the Interfaces table is still shown');
  assert.strictEqual(host.style.display, '', 'the Peers panel is not shown');
});

const rows = () => String(n.wgPeerRows.innerHTML);

// ── AND LEAVING IT STOPS THE WORK ───────────────────────────────────────────
//
// THIS CASE IS PLACED HERE DELIBERATELY. Written before the tab was ever
// opened it passed against a panel with its `shown` guard removed, because the
// panel's `host` was still null and the early return fired for that reason
// instead — a check measuring something other than what it names. Found by
// planting exactly that removal. After a visit `host` is set, so only the
// `shown` flag can stop the draw.
check('leaving the tab stops it rebuilding on every payload', () => {
  update([peer({ publicKey: 'a', name: 'before' })]);
  assert.ok(/before/.test(rows()), 'the panel did not draw while it was shown — ' +
    'the rest of this case would then prove nothing');
  doc.dispatchEvent({ type: 'click', target: target({ '[data-areatab]': { 'data-areatab': KEY, 'data-areatabindex': '0' } }) });
  n.wgPeerRows.innerHTML = '';
  update([peer({ publicKey: 'b', name: 'after' })]);
  assert.strictEqual(rows(), '',
    'the table was rebuilt while the panel was hidden; that runs every few seconds ' +
    'for every viewer of the Interfaces tab');
  // Back to Peers for the cases below.
  doc.dispatchEvent({ type: 'click', target: target({ '[data-areatab]': { 'data-areatab': KEY, 'data-areatabindex': String(at) } }) });
});

check('the three handshake states are three, not two', () => {
  update([
    peer({ publicKey: 'a', name: 'fresh', state: 'active', lastHandshake: '5s' }),
    peer({ publicKey: 'b', name: 'gone', state: 'stale', lastHandshake: '2h13m' }),
    peer({ publicKey: 'c', name: 'unused', state: 'never', lastHandshake: '' }),
  ]);
  const html = rows();
  assert.ok(/hs-ok/.test(html), 'a peer that handshook seconds ago is not marked fresh');
  assert.ok(/hs-stale/.test(html), 'a peer whose handshake went stale is not marked stale');
  assert.ok(/hs-never[^]*Never connected/.test(html),
    'a peer that never connected is not distinguished from one that went away');
});

check('a peer that has just handshaken is the freshest, not the stalest', () => {
  // `0s` is what RouterOS reports immediately after a handshake, and the
  // obvious `|| Infinity` guard turns it into the oldest possible value. That
  // bug shipped once already, in the page this tab replaces.
  update([peer({ publicKey: 'a', state: 'active', lastHandshake: '0s' })]);
  assert.ok(/hs-ok/.test(rows()), 'a peer handshaking right now was graded stale');
});

check('the flags render, and the overlap warning with them', () => {
  update([
    peer({ publicKey: 'a', name: 'off', disabled: true }),
    peer({ publicKey: 'b', name: 'resp', responder: true, allowedIp: '10.0.0.7/32' }),
    peer({ publicKey: 'c', name: 'dup1', allowedIp: '10.0.0.8/32' }),
    peer({ publicKey: 'd', name: 'dup2', allowedIp: '10.0.0.8/32' }),
  ]);
  const html = rows();
  assert.ok(/>disabled</.test(html), 'a disabled peer is not marked');
  assert.ok(/>responder</.test(html), 'a responder peer is not marked');
  assert.strictEqual((html.match(/>overlap</g) || []).length, 2,
    'the overlap warning did not land on exactly the two peers sharing an address');
});

check('rates use the fixed Rx and Tx colours', () => {
  update([peer({ publicKey: 'a', rxRate: 2048, txRate: 4096 })]);
  const html = rows();
  assert.ok(/--accent-rx[^]*↓/.test(html), 'Rx is not drawn in the Rx colour');
  assert.ok(/--accent-tx[^]*↑/.test(html), 'Tx is not drawn in the Tx colour');
});

check('the panel owns the count badge while it is up', () => {
  update([peer({ publicKey: 'a' }), peer({ publicKey: 'b' })]);
  assert.strictEqual(String(n['areaBadge-' + KEY].textContent), '2',
    'the badge shows the Interfaces count while the Peers tab is open');
  assert.ok(String(n['areaBadge-' + KEY].className).includes('active-blue'),
    'a non-zero count is not marked active-blue');
});

check('the rows carry their identity for the write path', () => {
  update([peer({ publicKey: 'kAAA', id: '*7' })]);
  assert.ok(/data-id="\*7"/.test(rows()), 'the row lost its RouterOS id');
  assert.ok(/data-identity="kAAA"/.test(rows()),
    'the row does not round-trip its public key, which is what proves it has not moved');
  assert.ok(/data-res="wgPeer"/.test(rows()), 'the row does not name its resource');
});

check('an empty peer list says what to do about it', () => {
  update([]);
  assert.ok(/empty-state/.test(rows()), 'no empty state was rendered');
  assert.ok(/Add one/.test(rows()), 'the empty state does not say what to do');
});

say(failed ? '  ' + failed + ' failed' : 'wireguard-peers: all checks passed');
fs.rmSync(OUT, { force: true });
if (failed) process.exit(1);
