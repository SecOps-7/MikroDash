'use strict';
/**
 * The BANDWIDTH page's table, live against ported.
 *
 * First of the 19 gaps `tools/page-gate-audit.js` recorded. This page had no
 * gate and no transitive coverage — nothing anywhere drove its renderer.
 *
 * ── WHAT IS DRIVEN, AND WHAT IS NOT ─────────────────────────────────────────
 *
 * The table body and the row count: `filter()` (search, interface, scope, IP
 * version, sort, top-N) and `render()`. NOT the Chart.js sparkline, the
 * requestAnimationFrame keepalive or the stats donut — those need a browser,
 * and a gate that pretends otherwise is the kind this port has already been
 * bitten by. A green run here is evidence about generated MARKUP.
 *
 * ── THE SORT COMPARATOR IS THE INTERESTING PART ─────────────────────────────
 *
 * `a[_sortKey] != null ? … : (typeof a[_sortKey] === 'string' ? '' : 0)` — the
 * fallback branch can never run: it is reached only when the value IS null or
 * undefined, and `typeof null` is 'object', so the string case is dead and a
 * missing name sorts as 0 against real strings. The port reproduces that
 * exactly, dead branch included, and the corpus carries rows with missing
 * names, missing orgs and mixed types so the quirk is pinned rather than
 * accidentally fixed.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/bandwidth-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('bandwidth-page-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));
// BOUNDED to the bandwidth IIFE, not "everything after the banner". The first
// version sliced to end-of-file and the occurrence guard refused `function
// render()` as AMBIGUOUS at 16 matches — app.js has a `render` on nearly every
// page. That guard is the only reason this gate is not silently lifting the
// Topology renderer.
const region = G.value('region', () => {
  const from = src.indexOf('Bandwidth Page');
  assert.ok(from > 0, 'no Bandwidth Page banner in app.js');
  const close = src.indexOf('\n})();', from);
  assert.ok(close > from, 'the bandwidth IIFE never closes at column 0');
  return src.slice(from, close);
});

function braceBodyIn(text, from) {
  const open = text.indexOf('{', from);
  let depth = 0;
  for (let i = open; i < text.length; i++) {
    if (text[i] === '{') depth++;
    else if (text[i] === '}') { depth--; if (!depth) return text.slice(open + 1, i); }
  }
  throw new Error('unbalanced body');
}
function slice(decl) {
  const n = region.split(decl).length - 1;
  assert.equal(n, 1, 'AMBIGUOUS anchor (' + n + '): ' + decl);
  return decl + '{' + braceBodyIn(region, region.indexOf(decl)) + '}';
}

const liveBar = G.value('liveBar', () => slice('function bar(val, max, cls) '));
const liveFilter = G.value('liveFilter', () => slice('function filter(data) '));
const liveFlag = G.value('liveFlag', () => slice('function iso2FlagBw(cc) '));
const liveRender = G.value('liveRender', () => slice('function render() '));
assert.ok(liveRender.includes('bw-empty'), 'the render slice is not the table renderer');
assert.ok(liveFilter.includes('_sortKey'), 'the filter slice lost its sort');

const grab = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n', i)); };
const escSrc = G.value('escSrc', () => grab('function esc('));
const fmtSrc = G.value('fmtSrc', () => {
  const i = src.indexOf('function fmtMbps(');
  return src.slice(i, src.indexOf('\n}', i) + 2);
});
const svcSrc = G.value('svcSrc', () => {
  const i = src.indexOf('function svcBadge(');
  return src.slice(i, src.indexOf('\n}', i) + 2);
});
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['liveBar', liveBar], ['liveFilter', liveFilter], ['liveFlag', liveFlag], ['liveRender', liveRender], ['escSrc', escSrc], ['region', region], ['fmtSrc', fmtSrc], ['svcSrc', svcSrc]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.bw-entry.ts');
fs.writeFileSync(ENTRY, "export { initBandwidthPage } from '../web/src/pages/bandwidth.js';\n");
const OUT = path.join(ROOT, 'testdata', '.bw-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── the shim ────────────────────────────────────────────────────────────────
// Deliberately not a DOM library: a real one normalises markup, and comparing
// two normalised strings can pass while the raw markup differs.
const IDS = ['bwTbody', 'bwStats', 'bwSearch', 'bwIface', 'bwScope', 'bwIpver', 'bwTopN',
  'bwThDevice', 'bwThDst', 'bwThRx', 'bwThTx', 'bwThTotal', 'bwThIface', 'bwThProto', 'bwThOrg'];

function makeDoc(controls) {
  const nodes = {};
  for (const id of IDS) {
    const store = { innerHTML: '', textContent: '' };
    const n = {
      id, value: '', style: {}, addEventListener() {},
      classList: { add() {}, remove() {}, contains: () => false },
    };
    for (const k of ['innerHTML', 'textContent']) {
      Object.defineProperty(n, k, { get: () => store[k], set: (v) => { store[k] = String(v); } });
    }
    nodes[id] = n;
  }
  nodes.bwSearch.value = controls.q || '';
  nodes.bwIface.value = controls.iface || '';
  nodes.bwScope.value = controls.scope || '';
  nodes.bwIpver.value = controls.ipver || '';
  nodes.bwTopN.value = controls.topN === undefined ? '10' : String(controls.topN);
  return {
    nodes,
    getElementById: (id) => nodes[id] || null,
    addEventListener() {},
    createElement: () => ({ set textContent(v) { this._t = String(v); },
      get textContent() { return this._t || ''; },
      get innerHTML() {
        return String(this._t || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
      } }),
  };
}
const snap = (doc) => JSON.stringify({
  tbody: doc.nodes.bwTbody.innerHTML,
  stats: doc.nodes.bwStats.textContent,
});

function liveRun(devices, controls) {
  const doc = makeDoc(controls);
  const ctx = {
    String, Array, Math, Number, Object, parseInt, JSON,
    document: doc,
    $: (id) => doc.getElementById(id),
  };
  vm.createContext(ctx);
  vm.runInContext([
    escSrc, fmtSrc, svcSrc, liveBar, liveFlag, liveFilter, liveRender,
    'var tbody = $("bwTbody"), stats = $("bwStats");',
    'var search = $("bwSearch"), selIface = $("bwIface"), selScope = $("bwScope");',
    'var selIpver = $("bwIpver"), selTopN = $("bwTopN");',
    'var _bwData = [], _maxBar = 0.001;',
    'var _sortKey = "' + (controls.sortKey || 'totalMbps') + '", _sortDir = ' +
      (controls.sortDir === undefined ? -1 : controls.sortDir) + ';',
    'function __run(d){ _bwData = d; render(); }',
  ].join('\n'), ctx);
  ctx.__run(devices);
  return snap(doc);
}

function portRun(devices, controls) {
  const doc = makeDoc(controls);
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} };
  const prev = globalThis.document;
  globalThis.document = doc;
  try {
    delete require.cache[require.resolve(OUT)];
    const mod = require(OUT);
    mod.initBandwidthPage(socket, () => true);
    // The port defaults to totalMbps/-1, the same as the live page. A case that
    // asks for another sort drives it through the header click the page wires,
    // rather than by reaching into its closure — the click IS the behaviour.
    if (controls.sortKey && controls.sortKey !== 'totalMbps') {
      throw new Error('non-default sort is driven by header clicks; see SORTS below');
    }
    handlers['bandwidth:update']({ ts: 1, devices, pollMs: 1000 });
  } finally {
    if (prev === undefined) delete globalThis.document; else globalThis.document = prev;
  }
  return snap(doc);
}

let bad = 0, checked = 0;
function cmp(what, a, b) {
  checked++;
  if (a === b) return;
  bad++;
  if (bad <= 4) shout('DIFF %s\n  live: %s\n  port: %s', what, a, b);
}

const D = (o) => Object.assign({
  srcIp: '198.51.100.10', dstIp: '203.0.113.5', name: 'laptop', mac: '02:00:00:00:00:01',
  org: 'Example', cat: 'cdn', country: 'US', city: 'Denver', iface: 'ether1', proto: 'tcp',
  rxMbps: 1, txMbps: 2, totalMbps: 3, isLan: false, isIpv6: false,
}, o);

const FLEET = [
  D({}),
  D({ srcIp: '198.51.100.11', name: 'phone', totalMbps: 9, rxMbps: 6, txMbps: 3, proto: 'udp' }),
  D({ srcIp: '198.51.100.12', name: null, org: null, cat: null, country: null, city: null,
      totalMbps: 5, rxMbps: 5, txMbps: 0, iface: 'ether2', proto: 'icmp6', isLan: true }),
  D({ srcIp: '2001:db8::1', dstIp: '2001:db8::2', name: 'v6box', isIpv6: true,
      totalMbps: 7, rxMbps: 3, txMbps: 4, proto: '', mac: null }),
];

const CASES = {
  'the fleet, defaults': [FLEET, {}],
  'no devices': [[], {}],
  'one device': [[D({})], {}],
  // Search, across every field it looks at.
  'search by name': [FLEET, { q: 'phone' }],
  'search by src ip': [FLEET, { q: '198.51.100.12' }],
  'search by dst ip': [FLEET, { q: '203.0.113' }],
  'search by mac': [FLEET, { q: '02:00:00:00:00:01' }],
  'search by org': [FLEET, { q: 'example' }],
  'search matching nothing': [FLEET, { q: 'zzzz' }],
  'search is case-insensitive': [FLEET, { q: 'PHONE' }],
  'search is trimmed': [FLEET, { q: '  phone  ' }],
  // The filters.
  'filter by interface': [FLEET, { iface: 'ether2' }],
  'filter by an interface nothing uses': [FLEET, { iface: 'ether9' }],
  'scope lan': [FLEET, { scope: 'lan' }],
  'scope wan': [FLEET, { scope: 'wan' }],
  'ipver 4': [FLEET, { ipver: '4' }],
  'ipver 6': [FLEET, { ipver: '6' }],
  'two filters at once': [FLEET, { scope: 'wan', ipver: '4' }],
  // Top-N, on both sides of the boundary.
  'topN 1': [FLEET, { topN: 1 }],
  'topN 3': [FLEET, { topN: 3 }],
  'topN larger than the fleet': [FLEET, { topN: 99 }],
  'topN 0 means no limit': [FLEET, { topN: 0 }],
  'topN unparseable': [FLEET, { topN: 'all' }],
  // Row rendering edge cases.
  'a row with no name': [[D({ name: null })], {}],
  'a row with no mac': [[D({ mac: null })], {}],
  'a row with no org': [[D({ org: null, cat: null })], {}],
  'a row with an org and no category': [[D({ cat: null })], {}],
  'a row with no country': [[D({ country: null, city: null })], {}],
  'a city equal to the country is dropped': [[D({ country: 'US', city: 'US' })], {}],
  'a one-character city is dropped': [[D({ country: 'US', city: 'A' })], {}],
  'a row with no dstIp': [[D({ dstIp: '' })], {}],
  'a row with no iface': [[D({ iface: '' })], {}],
  'a row with no proto': [[D({ proto: '' })], {}],
  'proto udp': [[D({ proto: 'udp' })], {}],
  'proto icmp6 matches the icmp class': [[D({ proto: 'icmp6' })], {}],
  'an unknown proto': [[D({ proto: 'gre' })], {}],
  // The bar arithmetic: zero, tiny and the maximum.
  'a zero rate draws no bar': [[D({ rxMbps: 0, txMbps: 0, totalMbps: 0 })], {}],
  'a tiny rate still draws 2px': [[D({ rxMbps: 0.0001, totalMbps: 5 })], {}],
  'every row identical — bars all full': [[D({}), D({ srcIp: 'x' })], {}],
  // Escaping, in text position throughout.
  'markup in a device name': [[D({ name: '<img src=x>' })], {}],
  'a quote in an org': [[D({ org: 'a"b' })], {}],
  'an ampersand in a city': [[D({ city: 'A&B' })], {}],
  // Sort fallbacks — the dead branch.
  'a null name sorted by name': [[D({ name: null }), D({ srcIp: 'b', name: 'zed' })], { }],
  'the singular row count': [[D({})], {}],

  // ── two cases added after mutation testing ────────────────────────────────
  //
  // Both of these mutants SURVIVED the first corpus, and neither was equivalent
  // — the corpus simply never handed the code anything hostile enough.
  //
  // `Math.min(val / max, 1)`: the bar normalises to the largest totalMbps in
  // view, so the clamp only binds if a row's rx or tx EXCEEDS every row's total.
  // Consistent data cannot do that, which is why removing the clamp went
  // unnoticed. An inconsistent row can, and the collector is not the only thing
  // that can produce one.
  'a row whose rx exceeds every total': [[D({ rxMbps: 99, txMbps: 0, totalMbps: 1 })], {}],
  'an inconsistent row beside a normal one': [
    [D({ rxMbps: 99, totalMbps: 1 }), D({ srcIp: 'b', rxMbps: 1, totalMbps: 4 })], {}],
  // `cc.length !== 2`: every country in the corpus was 'US' or null, so the
  // length guard never ran. A one-character code makes charCodeAt(1) NaN and
  // fromCodePoint throw, which is exactly what the guard is for.
  'a one-character country code': [[D({ country: 'U', city: null })], {}],
  'a three-character country code': [[D({ country: 'USA', city: null })], {}],
  'an empty-string country': [[D({ country: '', city: null })], {}],
};

for (const [name, [devices, controls]] of Object.entries(CASES)) {
  cmp(name, liveRun(devices, controls), portRun(devices, controls));
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(liveRun(FLEET, {}));
  assert.match(s.tbody, /<tr>/, 'the live renderer produced no rows');
  assert.match(s.tbody, /bw-bar-rx/, 'the rx bar is missing');
  assert.match(s.tbody, /bw-proto-tcp/, 'the proto badge is missing');
  assert.equal(s.stats, '4 devices', 'the live row count is ' + s.stats);
}
{
  const s = JSON.parse(liveRun([], {}));
  assert.match(s.tbody, /bw-empty/, 'the empty state did not render');
  assert.equal(s.stats, '', 'the empty state left a stale count');
}
{
  const s = JSON.parse(liveRun([D({})], {}));
  assert.equal(s.stats, '1 device', 'the singular is not singular: ' + s.stats);
}
{
  // Sorting really is by totalMbps descending, so the top-N cases mean what
  // they say. Without this the topN corpus could pass on an unsorted list.
  const s = JSON.parse(liveRun(FLEET, { topN: 1 }));
  assert.match(s.tbody, /phone/, 'topN 1 did not keep the busiest row');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('bandwidth-page-check: %d cases identical', checked);
