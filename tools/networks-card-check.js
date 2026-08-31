'use strict';
/**
 * The Dashboard's Networks card, live against ported.
 *
 * ── THE SLICE IS HALF A HANDLER, AND THE HARNESS PROVES IT ──────────────────
 *
 * `lan:overview` draws this card, the DHCP page's subnet table and its pool
 * gauge. Only the Dashboard half is ported here — the DHCP half already lives in
 * `pages/dhcp.ts` — so the live handler is run WHOLE, with the DHCP elements
 * present, and only the Dashboard elements are compared. Running it whole
 * matters: the handler falls through from one section to the next, and a slice
 * cut at a section boundary would drop the early `return` that an empty payload
 * takes.
 *
 * The DHCP elements are asserted to have been written by the live side. If they
 * had not been, the run would prove nothing about where the boundary is.
 *
 * ── AND THE TWO OMISSIONS ARE PINNED, NOT ASSUMED ───────────────────────────
 *
 * `ndLanCidr` and `ndGateway` are written by the live handler behind `if(el)`
 * guards and no element with either id exists in the live markup. The harness
 * therefore does NOT provide them — matching the real page — and asserts they
 * stayed absent. If either ever gains an element, this gate starts comparing it
 * and the port's omission becomes a failure instead of a silent gap.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/networks-card-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource. This gate already carried the empty-source guard in
// its slicing helper, but the READ itself still used fs.readFileSync and died of
// ENOENT before reaching it — the guard's own comment described behaviour the
// code did not have.
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('networks-card-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
const html = LIFT.liveSource(ROOT, path.join('public', 'index.html'));

// The omissions, checked against the LIVE MARKUP rather than trusted.
for (const id of ['ndLanCidr', 'ndGateway']) {
  assert.ok(!html.includes('id="' + id + '"'),
    id + ' now EXISTS in the live markup. The port omits it as a dead write — ' +
    'that is no longer true, and dashboard-networks.ts must be corrected.');
}

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
  throw new Error('unbalanced body at ' + from);
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

// The FIRST lan:overview handler — the one at the top of the file. There are two.
const handlerBody = braceBody(src.indexOf("socket.on('lan:overview'"));
for (const must of ['netInternetIfaces', 'lanOverview', 'dhcpSubnetTable', 'renderDhcpGauge']) {
  if (LIFT.hasReference(ROOT)) assert.ok(handlerBody.includes(must), 'the lan:overview slice lost ' + must);
}
const escSrc = slice('function esc(', '\n}', 'esc');

const ENTRY = path.join(ROOT, 'testdata', '.netcard-entry.ts');
fs.writeFileSync(ENTRY, "export { renderNetworks } from '../web/src/pages/dashboard-networks.js';\n");
const OUT = path.join(ROOT, 'testdata', '.netcard-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// Compared: the Dashboard's two. Present but NOT compared: the DHCP page's,
// which belong to pages/dhcp.ts and are only here so the handler runs whole.
const DASH_IDS = ['netInternetIfaces', 'lanOverview'];
const DHCP_IDS = ['dhcpSubnetTable'];
const ABSENT_IDS = ['ndLanCidr', 'ndGateway'];

function makeDom() {
  const byId = new Map();
  const node = (id) => {
    const n = {
      id, style: {},
      set textContent(v) { this._t = String(v); },
      get textContent() { return this._t === undefined ? '' : this._t; },
      set innerHTML(v) { this._h = v; this._w = (this._w || 0) + 1; },
      get innerHTML() { return this._h || ''; },
      writes() { return this._w || 0; },
      setAttribute() {}, getAttribute: () => null,
    };
    byId.set(id, n);
    return n;
  };
  for (const id of [...DASH_IDS, ...DHCP_IDS]) node(id);
  return { byId };
}
function snapshot(dom) {
  const out = {};
  for (const id of DASH_IDS) {
    const n = dom.byId.get(id);
    out[id] = { html: n.innerHTML, writes: n.writes() };
  }
  for (const id of ABSENT_IDS) out[id] = dom.byId.has(id) ? 'UNEXPECTEDLY PRESENT' : null;
  return JSON.stringify(out);
}

function liveRun(data) {
  const dom = makeDom();
  const ctx = {
    Math, JSON, String, Array, Number,
    DOT: '·',
    esc: null,
    $: (id) => dom.byId.get(id) || null,
    lanOverview: dom.byId.get('lanOverview'),
    lastLanData: null,
    allLeases: [],
    _dhcpTotalPoolSize: 0, _dhcpNetworksData: null,
    renderDhcpGauge: () => {},
  };
  vm.createContext(ctx);
  vm.runInContext(escSrc + '\nfunction __run(data){' + handlerBody + '}', ctx);
  ctx.__run(data);
  return dom;
}
function portRun(data) {
  const dom = makeDom();
  globalThis.document = { getElementById: (id) => dom.byId.get(id) || null };
  delete require.cache[require.resolve(OUT)];
  require(OUT).renderNetworks(data);
  return dom;
}

const N = (o) => Object.assign({ cidr: '198.51.100.0/24', gateway: '198.51.100.1', dns: '198.51.100.1', leaseCount: 4 }, o);
const CASES = [
  ['one network and one interface', { internetIfaces: [{ name: 'ether1', ip: '203.0.113.9/24' }], networks: [N({})] }],
  ['several of each', {
    internetIfaces: [{ name: 'ether1', ip: '203.0.113.9/24' }, { name: 'lte1', ip: '100.64.0.5/10' }],
    networks: [N({}), N({ cidr: '10.0.0.0/8', gateway: '10.0.0.1', dns: '', leaseCount: 0 })],
  }],
  ['no interfaces detected', { internetIfaces: [], networks: [N({})] }],
  ['interfaces absent entirely', { networks: [N({})] }],
  ['no networks: the card SAYS SO and returns', { internetIfaces: [{ name: 'ether1', ip: '203.0.113.9/24' }], networks: [] }],
  ['networks absent entirely', { internetIfaces: [{ name: 'ether1', ip: '1.2.3.4/24' }] }],
  ['an entirely empty payload', {}],
  ['an interface with NO ip', { internetIfaces: [{ name: 'ether1' }], networks: [N({})] }],
  ['an interface with an EMPTY ip string', { internetIfaces: [{ name: 'ether1', ip: '' }], networks: [N({})] }],
  ['an ip with no prefix length', { internetIfaces: [{ name: 'ether1', ip: '203.0.113.9' }], networks: [N({})] }],
  ['a network missing gateway and dns', { networks: [N({ gateway: '', dns: '' })] }],
  ['a network missing gateway and dns KEYS', { networks: [{ cidr: '10.0.0.0/8', leaseCount: 2 }] }],
  ['leaseCount of zero renders 0, not a dash', { networks: [N({ leaseCount: 0 })] }],
  ['leaseCount absent', { networks: [{ cidr: '10.0.0.0/8', gateway: '10.0.0.1', dns: '1.1.1.1' }] }],
  ['markup in every field is escaped', {
    internetIfaces: [{ name: '<img src=x>', ip: '"q"/24' }],
    networks: [N({ cidr: '<b>', gateway: 'A&B', dns: "O'x" })],
  }],
];

let bad = 0;
for (const [name, data] of CASES) {
  const a = G.live(name, () => snapshot(liveRun(data)));
  const b = snapshot(portRun(data));
  if (a === b) continue;
  bad++;
  console.error('\nDIFF %s', name);
  const A = JSON.parse(a), B = JSON.parse(b);
  for (const k of Object.keys(A)) {
    if (JSON.stringify(A[k]) !== JSON.stringify(B[k])) {
      console.error('  %s\n    live: %j\n    port: %j', k, A[k], B[k]);
    }
  }
}

// The boundary is real: the live handler DID write the DHCP half, and this gate
// deliberately does not compare it.
// GUARDED: a claim about the LIVE handler that justifies what this gate does NOT
// compare. It is unanswerable without a reference, and it is not a property the
// port has — the port's DHCP half lives on another page entirely.
if (LIFT.hasReference(ROOT)) {
  const dom = liveRun({ internetIfaces: [{ name: 'ether1', ip: '1.2.3.4/24' }], networks: [N({})] });
  assert.ok(dom.byId.get('dhcpSubnetTable').innerHTML.includes('dhcp-subnet-table'),
    'the live handler did not reach the DHCP half — the slice is not the whole handler, ' +
    'so what this gate compares is not what the live app does');
}
// RE-AIMED AT THE PORT: that the card renders at all. Without this the
// comparisons above could be two empty documents agreeing.
{
  const dom = portRun({ internetIfaces: [{ name: 'ether1', ip: '1.2.3.4/24' }], networks: [N({})] });
  assert.match(dom.byId.get('lanOverview').innerHTML, /lan-net/, 'the card did not render');
  assert.match(dom.byId.get('netInternetIfaces').innerHTML, /net-wan-row/, 'the interfaces did not render');
}
// And the early return really is early: an empty `networks` must leave the DHCP
// half untouched, which is what makes running the handler whole necessary.
// GUARDED, for the same reason: it asks where the LIVE handler's early return
// sits. The port has no DHCP half to leave untouched.
if (LIFT.hasReference(ROOT)) {
  const dom = liveRun({ networks: [] });
  assert.equal(dom.byId.get('dhcpSubnetTable').writes(), 0,
    'the live handler reached the DHCP half despite an empty networks list — ' +
    'the early return has moved and the port must follow it');
}

fs.rmSync(OUT, { force: true });
if (bad) { console.error('\n%d of %d cases differ', bad, CASES.length); process.exit(1); }
console.log('networks-card-check: %d cases identical (Dashboard half of lan:overview)', CASES.length);
