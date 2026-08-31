'use strict';
/**
 * The DHCP page's headline gauge, live against ported.
 *
 * ── THE TWO NUMERATORS MUST AGREE ───────────────────────────────────────────
 *
 * The gauge and the per-subnet bars sit on the same screen, inches apart. The
 * gauge used to divide the LEASE TABLE'S ROW COUNT by the pool size while the
 * bars used the server's per-subnet counts, so a router with static reservations
 * showed 99% above bars reading 22% (live issue #115). Both now take the
 * server's `totalLeases`.
 *
 * That is a claim about two numbers matching, so the cases carry payloads where
 * the two DISAGREE — reservations present, table longer than the used count —
 * and check the gauge follows the server rather than the table.
 *
 * ── AND ZERO IS NOT ABSENT ──────────────────────────────────────────────────
 *
 * The fallback to the row count exists only for a cold load, before the first
 * `lan:overview`. A legitimately ZERO used-count must NOT take it: that is
 * exactly what a router whose every lease is a `waiting` reservation reports,
 * and falling back there would restore the bug in the one case it was reported
 * for.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/dhcp-gauge-check.js
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
const G = LIFT.golden('dhcp-gauge-check');
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
const gaugeSrc = G.value('gaugeSrc', () => slice('function renderDhcpGauge()', '\n}', 'renderDhcpGauge'));
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['gaugeSrc', gaugeSrc]]) {
  assert.ok(typeof __v === 'string' && __v.length > 4,
    'the recorded ' + __n + ' is empty — the golden is broken');
}
if (LIFT.hasReference(ROOT)) assert.match(gaugeSrc, /totalLeases/,
  'the live gauge no longer reads totalLeases — it has gone back to the lease-table row count, ' +
  'or the field was renamed. Either way this port now disagrees with it.');

const ENTRY = path.join(ROOT, 'testdata', '.dhcpgauge-entry.ts');
fs.writeFileSync(ENTRY, "export { initDhcpPage } from '../web/src/pages/dhcp.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dhcpgauge-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const IDS = ['dhcpGaugeFill', 'dhcpGaugeTrack', 'dhcpGaugePct'];
function makeDom() {
  const byId = new Map();
  const mk = (id) => {
    const n = {
      id, attrs: {}, style: {},
      setAttribute(k, v) { n.attrs[k] = String(v); },
      getAttribute: (k) => (k in n.attrs ? n.attrs[k] : null),
      set textContent(v) { n._t = String(v); },
      get textContent() { return n._t === undefined ? '' : n._t; },
      set innerHTML(v) { n._h = v; }, get innerHTML() { return n._h || ''; },
      classList: { add() {}, remove() {}, contains: () => false },
      addEventListener() {}, querySelectorAll: () => [], querySelector: () => null,
      appendChild: (c) => c, children: [],
    };
    byId.set(id, n);
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

// The live gauge reads three module-level variables. Driven directly, which is
// what makes the numerator question askable at all.
function liveGauge(totalPool, networksData, leaseRows) {
  const d = makeDom();
  const ctx = {
    Math, Number, String,
    $: (id) => d.byId.get(id) || null,
    _dhcpTotalPoolSize: totalPool,
    _dhcpNetworksData: networksData,
    allLeases: leaseRows,
  };
  vm.createContext(ctx);
  vm.runInContext(gaugeSrc + '\nrenderDhcpGauge();', ctx);
  return snap(d);
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) shout('DIFF %s\n  live: %s\n  port: %s', what, a, b);
}

// The port's gauge is inside a page module with a socket; it is driven through
// the same two payloads the page receives.
function portGauge(totalPool, networksData, leaseRows) {
  const d = makeDom();
  const handlers = new Map();
  globalThis.document = {
    getElementById: (id) => d.byId.get(id) || null,
    createElement: () => d.mk(''),
    addEventListener() {}, querySelectorAll: () => [], querySelector: () => null,
  };
  globalThis.localStorage = { getItem: () => null, setItem() {} };
  globalThis.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(null) });
  delete require.cache[require.resolve(OUT)];
  const m = require(OUT);
  m.initDhcpPage({ on: (e, cb) => handlers.set(e, cb), emit() {} });
  // leases FIRST, so a fallback to the row count would be visible, then the
  // networks payload that must override it.
  handlers.get('leases:list')?.({ leases: leaseRows, servers: [] });
  if (networksData) {
    handlers.get('lan:overview')?.(Object.assign({ networks: [{ cidr: '198.51.100.0/24' }] }, networksData));
  }
  return snap(d);
}

const rows = (n) => Array.from({ length: n }, (_, i) => ({
  address: '198.51.100.' + (i + 1), macAddress: '02:00:00:00:00:0' + (i % 10), status: 'bound',
}));

const CASES = [
  ['the table and the server agree', 512, { totalPoolSize: 512, totalLeases: 110 }, rows(110)],
  ['reservations: the table is LONGER than the used count', 512,
    { totalPoolSize: 512, totalLeases: 110 }, rows(507)],
  ['the reported CCR2004 shape', 1024, { totalPoolSize: 1024, totalLeases: 574 }, rows(1224)],
  ['ZERO used, every lease a reservation', 501, { totalPoolSize: 501, totalLeases: 0 }, rows(100)],
  ['a full pool', 256, { totalPoolSize: 256, totalLeases: 256 }, rows(256)],
  ['over-full', 256, { totalPoolSize: 256, totalLeases: 300 }, rows(300)],
  ['no pool at all', 0, { totalPoolSize: 0, totalLeases: 0 }, rows(5)],
  // A COLD LOAD HAS NEITHER. `_dhcpTotalPoolSize` and `_dhcpNetworksData` are
  // assigned on consecutive lines of the same `lan:overview` handler, so a pool
  // size without a networks payload is not a state the app can be in — an
  // earlier version of this case passed 512 with no payload and the two sides
  // "differed" over something unreachable.
  ['a cold load: no networks payload and no pool yet', 0, null, rows(40)],
  ['exactly half', 200, { totalPoolSize: 200, totalLeases: 100 }, rows(180)],
  ['one address', 1, { totalPoolSize: 1, totalLeases: 1 }, rows(1)],
  // A networks payload with a pool but NO `totalLeases`. Reachable: a server
  // that predates the field, which during coexistence is the Node side this
  // port proxies to. Only here does the cold-load fallback actually fire, and
  // without this case a mutation deleting it survives.
  ['a networks payload with no totalLeases at all', 512, { totalPoolSize: 512 }, rows(40)],
  ['no totalLeases, and no leases either', 512, { totalPoolSize: 512 }, []],
  // Fractions that ROUND UP. Every other case here happens to floor and round
  // to the same integer, so a truncating percentage survived them all.
  ['three eighths — rounds up, does not truncate', 8, { totalPoolSize: 8, totalLeases: 3 }, rows(3)],
  ['one third', 3, { totalPoolSize: 3, totalLeases: 1 }, rows(1)],
  ['just under a half', 1000, { totalPoolSize: 1000, totalLeases: 495 }, rows(495)],
  ['just over a half', 1000, { totalPoolSize: 1000, totalLeases: 505 }, rows(505)],
];

for (const [name, pool, nets, leaseRows] of CASES) {
  // The live side reads `_dhcpTotalPoolSize` and `_dhcpNetworksData` separately;
  // the port takes both from the same payload, so the pool is passed twice.
  cmp(name, liveGauge(pool, nets, leaseRows), portGauge(pool, nets, leaseRows));
}

// ── believability, and the property the section is about ───────────────────
{
  // The gauge must follow the SERVER, not the table. With 507 rows and 110 used
  // in a 512 pool, the two answers are 99% and 21% — if they were the same the
  // corpus would prove nothing.
  const tableAnswer = Math.round((507 / 512) * 100);
  const serverAnswer = Math.round((110 / 512) * 100);
  assert.notEqual(tableAnswer, serverAnswer, 'the corpus case does not distinguish the two numerators');
  const s = JSON.parse(liveGauge(512, { totalPoolSize: 512, totalLeases: 110 }, rows(507)));
  assert.equal(s.dhcpGaugePct.text, serverAnswer + '%',
    'the live gauge read ' + s.dhcpGaugePct.text + ', not the server count — issue #115 is back');
  assert.ok(s.dhcpGaugeFill.d, 'the live gauge drew no fill arc');
}
{
  // Zero must not take the cold-load fallback.
  const s = JSON.parse(liveGauge(501, { totalPoolSize: 501, totalLeases: 0 }, rows(100)));
  assert.equal(s.dhcpGaugePct.text, '0%',
    'a zero used-count fell back to the lease-table length and read ' + s.dhcpGaugePct.text);
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('dhcp-gauge-check: %d cases identical', checked);
