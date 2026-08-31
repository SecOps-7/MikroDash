'use strict';
/**
 * The DNS page, live against ported.
 *
 * CLAUDE.md has always named this page as the worked example of a ported-page
 * acceptance test, and until now it had NO gate — `live-renderer.js dns` lifts
 * the renderer, validates its helper references and compares nothing. This is
 * the second half that command's documentation implied existed.
 *
 * Built on `tools/lib/dom-shim.js`; every structural lesson from the Queues gate
 * is applied up front rather than rediscovered: the slice is bounded and asserts
 * what it EXCLUDES, the id list is extracted from the live source, a throw is a
 * failure rather than something to compare, and the believability block drives
 * the live side on its own so a vacuous pass cannot hide.
 *
 * WHAT IT CANNOT SEE: layout, focus, real event dispatch, and the sort-header
 * rendering (`_renderSortHeader` is app-wide and stubbed on both sides).
 *
 * THE SORT HEADER IS NO LONGER STUBBED. It was, and the consequence was written
 * up here as a known gap: the header was rendered but never wired, so no click
 * could reach the sort state and a mutation replacing `sortMul(sort)` with `1`
 * survived the entire corpus. The real helper is now lifted and the shim answers
 * a bare tag selector with cells that record their listeners, so the direction,
 * the column switch and the header markup are all compared.
 *
 * WHAT REMAINS UNCOMPARED HERE: `c.cls`, the per-column class the sort header
 * carries. The DNS columns do not set it — the Wireless header does, pairing
 * `wl-col-*` on the th with the same class on its td — so a mutation dropping it
 * survives THIS gate and would be caught by a Wireless one. Named because an
 * unstated gap is the kind that gets rediscovered.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/dns-page-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc, withDocument } = require('./lib/dom-shim.js');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('dns-page-check');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
// ROUTED THROUGH liveSource, which yields '' when the reference is gone rather
// than throwing ENOENT. What the gate lifts from it is frozen below.
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

const iife = G.value('iife', () => {
  const banner = src.indexOf('/* ── DNS page ');
  assert.ok(banner > 0, 'no DNS page banner in app.js');
  const open = src.indexOf('(function', banner);
  assert.ok(open > banner && open - banner < 2000, 'the DNS IIFE is not where its banner says');
  // Both closing spellings, whichever comes first — app.js uses each in places,
  // and taking only one ran the Queues slice 2,000 lines past its page.
  const ends = ['\n}());', '\n})();'].map((p) => src.indexOf(p, open)).filter((i) => i > open);
  assert.ok(ends.length, 'the DNS IIFE never closes at column 0');
  return src.slice(open, Math.min(...ends));
});
// THE RECORDING MUST NOT BE EMPTY. A golden of empty strings would let every
// comparison below pass while comparing nothing, which is the exact failure
// this conversion exists to avoid.
for (const [__n, __v] of [['iife', iife]]) {
  if (typeof __v !== 'string' || __v.length <= 4) {
    throw new Error('the recorded ' + __n + ' is empty — the golden is broken');
  }
}
assert.ok(iife.includes('dnsSettingsBody'), 'the lifted IIFE is not the DNS page');
assert.ok(iife.includes('No static DNS entries.'), 'the lifted IIFE lost its empty state');
// INCLUSION CANNOT BOUND A SLICE. These are the checks that would have caught
// the Queues over-read, which satisfied both of its inclusion assertions.
for (const foreign of ['Queues page', 'backupsPage', 'Bandwidth Page', 'qSimpleTable']) {
  assert.ok(!iife.includes(foreign), 'the lifted DNS IIFE reaches into another page (' + foreign + ')');
}

const grab = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n', i)); };
const whole = (decl) => { const i = src.indexOf(decl); return src.slice(i, src.indexOf('\n}', i) + 2); };
// FROZEN AS ONE JOINED PROGRAM. These lifters were called INLINE inside the
// `vm.runInContext` array — freezing the JOINED RESULT covers every lift inside
// it whatever shape each has, which is cheaper than teaching a converter each.
const LIVE_HELPERS = G.value('the lifted live helpers', () => [
  grab('function esc('),
  whole('function _sortMul('),
  whole('function _renderSortHeader('),
  whole('function resRow('),
].join('\n'));
if (!LIVE_HELPERS || LIVE_HELPERS.length < 100) {
  throw new Error('the recorded live helpers are empty — the golden is broken');
}

const ENTRY = path.join(ROOT, 'testdata', '.dns-entry.ts');
fs.writeFileSync(ENTRY, "export { initDnsPage } from '../web/src/pages/dns.js';\n");
const OUT = path.join(ROOT, 'testdata', '.dns-port.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// Extracted from the live IIFE, not guessed: the page returns early on a missing
// element, so a short list yields a page that renders nothing, silently.
const IDS = ['dnsSettingsBody', 'dnsStaticBadge', 'dnsStaticSearch', 'dnsStaticTable',
  'dnsStaticThead', 'dnsSumCache', 'dnsSumRemote', 'dnsSumServers', 'dnsSumStatic'];

const snap = (doc) => {
  const n = doc.nodes;
  return JSON.stringify({
    settings: n.dnsSettingsBody.innerHTML,
    table: n.dnsStaticTable.innerHTML,
    badge: n.dnsStaticBadge.textContent,
    thead: n.dnsStaticThead.innerHTML,
    sum: {
      cache: n.dnsSumCache.textContent, static: n.dnsSumStatic.textContent,
      servers: n.dnsSumServers.textContent, remote: n.dnsSumRemote.textContent,
    },
  });
};

// Click a header cell the way a viewer does. `clicks` is a list of column
// INDICES; clicking the same column twice is what flips ascending to descending,
// so [0, 0] is the descending case and [1] is a different column ascending.
//
// The cells are re-read from the shim on every click, because each render
// replaces them — capturing them once would fire a listener belonging to markup
// that is no longer on the page.
function clickHeaders(doc, headId, clicks) {
  for (const i of clicks || []) {
    const cells = doc.nodes[headId].querySelectorAll('th');
    if (!cells[i]) throw new Error('no header cell at index ' + i + ' in #' + headId);
    cells[i].click();
  }
}

function liveRun(payload, query, clicks) {
  const doc = makeDoc(IDS, {});
  if (query) doc.nodes.dnsStaticSearch.value = query;
  const handlers = {};
  const ctx = {
    String, Array, Math, Number, Object, JSON, parseInt,
    document: doc,
    socket: { on: (ev, fn) => { handlers[ev] = fn; }, emit() {} },
    setTimeout: () => 0, clearTimeout: () => {},
  };
  vm.createContext(ctx);
  vm.runInContext([
    LIVE_HELPERS,
    'function $(id){return document.getElementById(id);}',
    // THE REAL sort header, not a stub. Stubbing it left the header markup
    // uncompared AND the click handlers unwired, which is what made the sort
    // DIRECTION unreachable — a mutation ignoring `sortMul` survived. Lifting it
    // costs nothing here and closes both.
    'function _debounce(fn){return fn;}',
    'function pageVisible(){return true;}',
    // THE REAL resRow, not a stub. The first version invented one and it emitted
    // `data-res-id`/`data-res-name` where the real helper emits
    // `data-id`/`data-identity` — so eighteen cases "differed" because the
    // HARNESS disagreed with the port, not because the port disagreed with the
    // page. A stub for a helper that produces MARKUP is a rewrite of the live
    // code, and the gate then tests the rewrite.
    iife + '}());',
  ].join('\n'), ctx);
  if (!handlers['dns:update']) {
    throw new Error('the live page registered no handler; ids it wanted and this gate ' +
      'does not provide: ' + ([...doc.unknown].join(', ') || 'none'));
  }
  handlers['dns:update'](payload);
  clickHeaders(doc, 'dnsStaticThead', clicks);
  return snap(doc);
}

function portRun(payload, query, clicks) {
  const doc = makeDoc(IDS, {});
  if (query) doc.nodes.dnsStaticSearch.value = query;
  const handlers = {};
  return withDocument(doc, () => {
    delete require.cache[require.resolve(OUT)];
    require(OUT).initDnsPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
    if (!handlers['dns:update']) throw new Error('the port registered no dns:update handler');
    handlers['dns:update'](payload);
    clickHeaders(doc, 'dnsStaticThead', clicks);
    return snap(doc);
  });
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

const SET = (o) => Object.assign({
  servers: ['198.51.100.1'], dynamicServers: [], dohEnabled: false, dohUrl: '',
  dohVerifyCert: false, allowRemoteRequests: false, cacheSize: 2048, cacheUsed: 128,
  cacheMaxTtl: '1d', mdnsRepeatIfaces: [], maxUdpPacketSize: 4096,
  queryServerTimeout: '2s', queryTotalTimeout: '10s',
}, o);
const E = (o) => Object.assign({
  id: '*1', name: 'router.lan', regexp: '', address: '198.51.100.1', type: 'A',
  ttl: '1d', comment: '', disabled: false,
}, o);
const P = (o) => Object.assign({ settings: SET({}), staticEntries: [] }, o);

// Three rows that differ in every sortable column, so a sort on any of them is
// observable rather than coincidentally already ordered.
const ROWS = [
  E({ id: '*1', name: 'z.lan', address: '198.51.100.3', type: 'A', ttl: '1d' }),
  E({ id: '*2', name: 'a.lan', address: '198.51.100.1', type: 'CNAME', ttl: '5m' }),
  E({ id: '*3', name: 'm.lan', address: '198.51.100.2', type: 'A', ttl: '' }),
];

const CASES = {
  'a normal payload': [P({}), ''],
  'no entries': [P({ staticEntries: [] }), ''],
  'one entry': [P({ staticEntries: [E({})] }), ''],
  'several entries': [P({ staticEntries: [E({}), E({ id: '*2', name: 'a.lan' }), E({ id: '*3', name: 'z.lan' })] }), ''],
  // Settings, branch by branch.
  'DoH on, cert verified': [P({ settings: SET({ dohEnabled: true, dohUrl: 'https://d.example/q', dohVerifyCert: true }) }), ''],
  'DoH on, cert NOT verified': [P({ settings: SET({ dohEnabled: true, dohUrl: 'https://d.example/q', dohVerifyCert: false }) }), ''],
  'no static servers, dynamic instead': [P({ settings: SET({ servers: [], dynamicServers: ['198.51.100.9'] }) }), ''],
  'no servers at all': [P({ settings: SET({ servers: [], dynamicServers: [] }) }), ''],
  'remote requests allowed': [P({ settings: SET({ allowRemoteRequests: true }) }), ''],
  'a null cacheUsed': [P({ settings: SET({ cacheUsed: null }) }), ''],
  'a null cacheSize': [P({ settings: SET({ cacheSize: null }) }), ''],
  'both cache values null': [P({ settings: SET({ cacheUsed: null, cacheSize: null }) }), ''],
  // `cacheUsed: 0` must not read as absent — the summary tests `=== null` and
  // `=== undefined` while the settings row tests only `=== null`.
  'a zero cacheUsed is not absent': [P({ settings: SET({ cacheUsed: 0 }) }), ''],
  'an undefined cacheUsed': [P({ settings: SET({ cacheUsed: undefined }) }), ''],
  'a null maxUdpPacketSize': [P({ settings: SET({ maxUdpPacketSize: null }) }), ''],
  'a zero maxUdpPacketSize': [P({ settings: SET({ maxUdpPacketSize: 0 }) }), ''],
  'no cacheMaxTtl': [P({ settings: SET({ cacheMaxTtl: '' }) }), ''],
  'mdns interfaces listed': [P({ settings: SET({ mdnsRepeatIfaces: ['bridge1', 'ether2'] }) }), ''],
  'no query timeouts': [P({ settings: SET({ queryServerTimeout: '', queryTotalTimeout: '' }) }), ''],
  // Rows.
  'a regexp entry': [P({ staticEntries: [E({ name: '', regexp: '.*\\.lan' })] }), ''],
  // BOTH SET. Two mutants survived the first corpus — `e.name || e.regexp` and
  // `e.regexp || e.name` are indistinguishable while only one is ever non-empty,
  // and no entry had both. MikroTik's docs list `name` (Domain name) and
  // `regexp` (Regular expression against which domain names should be verified)
  // as independent properties of /ip/dns/static with NO stated mutual
  // exclusivity, so a payload carrying both is not something this port may
  // assume away. The precedence is pinned in the row AND in the search.
  'an entry with BOTH a name and a regexp': [P({ staticEntries: [E({ name: 'n.lan', regexp: 'r-pattern' })] }), ''],
  'search finds the name when both are set': [P({ staticEntries: [E({ name: 'n.lan', regexp: 'r-pattern' })] }), 'n.lan'],
  'search misses the regexp when both are set': [P({ staticEntries: [E({ name: 'n.lan', regexp: 'r-pattern' })] }), 'r-pattern'],
  'a disabled entry': [P({ staticEntries: [E({ disabled: true })] }), ''],
  'no ttl': [P({ staticEntries: [E({ ttl: '' })] }), ''],
  'a comment': [P({ staticEntries: [E({ comment: 'note' })] }), ''],
  // The docs gap CLAUDE.md names: RouterOS supports nine record types and the
  // form offered six. The TABLE must render whatever the router returns.
  'an MX record': [P({ staticEntries: [E({ type: 'MX', address: 'mail.lan' })] }), ''],
  'an SRV record': [P({ staticEntries: [E({ type: 'SRV' })] }), ''],
  'a TXT record with markup': [P({ staticEntries: [E({ type: 'TXT', address: '<b>v=spf1</b>' })] }), ''],
  'markup in a name': [P({ staticEntries: [E({ name: '<img src=x>' })] }), ''],
  'a quote in a name': [P({ staticEntries: [E({ name: 'a"b' })] }), ''],
  'an ampersand in a comment': [P({ staticEntries: [E({ comment: 'a&b' })] }), ''],
  // Search.
  'search matching a name': [P({ staticEntries: [E({}), E({ id: '*2', name: 'other.lan' })] }), 'router'],
  'search matching an address': [P({ staticEntries: [E({})] }), '100.1'],
  'search matching a regexp entry': [P({ staticEntries: [E({ name: '', regexp: 'zed' })] }), 'zed'],
  'search matching nothing': [P({ staticEntries: [E({})] }), 'zzzz'],
  'search is trimmed and lowercased': [P({ staticEntries: [E({})] }), '  ROUTER  '],
  // Sorting is by name ascending; a missing sort field must not throw.
  'entries sorted by name': [P({ staticEntries: [E({ id: '*1', name: 'z.lan' }), E({ id: '*2', name: 'a.lan' })] }), ''],
  'an entry with no name sorts as empty': [P({ staticEntries: [E({ name: '', regexp: 'r' }), E({ id: '*2', name: 'a.lan' })] }), ''],

  // ── SORTING, DRIVEN THROUGH THE REAL HEADER ────────────────────────────────
  //
  // Impossible while `_renderSortHeader` was stubbed: the header was written but
  // never wired, so no click could reach the sort state and a mutation ignoring
  // `sortMul` survived the entire corpus.
  'sorted by name ascending (default)': [P({ staticEntries: ROWS }), '', []],
  'name clicked once stays ascending': [P({ staticEntries: ROWS }), '', [0]],
  'name clicked TWICE goes descending': [P({ staticEntries: ROWS }), '', [0, 0]],
  'name clicked three times returns to ascending': [P({ staticEntries: ROWS }), '', [0, 0, 0]],
  'sorted by address': [P({ staticEntries: ROWS }), '', [1]],
  'sorted by address descending': [P({ staticEntries: ROWS }), '', [1, 1]],
  'sorted by type': [P({ staticEntries: ROWS }), '', [2]],
  'sorted by ttl, one of them empty': [P({ staticEntries: ROWS }), '', [3]],
  'sorted by comment, all empty': [P({ staticEntries: ROWS }), '', [4]],
  // Switching columns resets to ascending rather than keeping the old direction.
  // ONE click on Name first, not two: two clicks return it to ascending, so the
  // switch happens from an ascending state and a mutation that KEEPS the old
  // direction is invisible. That is how it survived the first attempt.
  'descending on name, then a different column': [P({ staticEntries: ROWS }), '', [0, 1]],
  'descending on address, then back to name': [P({ staticEntries: ROWS }), '', [1, 2, 0]],
  'a sort survives a search': [P({ staticEntries: ROWS }), 'lan', [0, 0]],
};

for (const [name, [payload, query, clicks]] of Object.entries(CASES)) {
  let a, b;
  // A throw is never a passing case: catching on both sides and comparing the
  // messages lets a gate go green because both were broken the same way.
  try { a = liveRun(payload, query, clicks); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  try { b = portRun(payload, query, clicks); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; checked++; continue; }
  cmp(name, a, b);
}

// ── believability ──────────────────────────────────────────────────────────
{
  const s = JSON.parse(liveRun(P({ staticEntries: [E({})] }), ''));
  assert.match(s.settings, /kv-item/, 'the live settings block rendered nothing');
  assert.match(s.settings, /Allow remote requests/, 'a settings row is missing');
  assert.match(s.table, /router\.lan/, 'the live table rendered no row');
  assert.equal(s.badge, '1', 'the badge is ' + s.badge);
  assert.equal(s.sum.cache, '128 / 2048', 'the cache summary is ' + s.sum.cache);
  assert.equal(s.sum.servers, 'static', 'the servers summary is ' + s.sum.servers);
  assert.equal(s.sum.remote, 'blocked', 'the remote summary is ' + s.sum.remote);
}
{
  const s = JSON.parse(liveRun(P({}), ''));
  assert.match(s.table, /No static DNS entries\./, 'the empty state did not render');
  const t = JSON.parse(liveRun(P({ staticEntries: [E({})] }), 'zzzz'));
  assert.match(t.table, /No entries match that search\./, 'the searched-empty state did not render');
  assert.equal(t.badge, '1', 'the badge counts MATCHES rather than entries: ' + t.badge);
}
{
  const s = JSON.parse(liveRun(P({ settings: SET({ dohEnabled: true, dohUrl: 'https://d/q', dohVerifyCert: false }) }), ''));
  assert.match(s.settings, /NOT verified/, 'an unverified DoH certificate is not called out');
  assert.equal(s.sum.servers, 'DoH', 'DoH did not win the servers summary');
}

fs.rmSync(OUT, { force: true });
if (bad) { shout('\n%d of %d cases differ', bad, checked); process.exit(1); }
say('dns-page-check: %d cases identical', checked);
