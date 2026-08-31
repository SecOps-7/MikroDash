#!/usr/bin/env node
'use strict';
/**
 * The DHCP page's LEASE TABLE, live against ported.
 *
 * ── STEP TWO, AND WHY IT MATTERS THAT IT EXISTS ─────────────────────────────
 *
 * `tools/live-renderer.js dhcp` lifts the live renderer and writes a bundle. It
 * compares NOTHING — it prints "wrote …" and exits 0 whether or not the port
 * agrees. Until this file, nothing consumed that bundle, so the DHCP table was
 * a SHIPPED page (`dhcp` is in `main.ts`'s PORTED) whose entire table no gate
 * had ever looked at. The lift had also rotted unnoticed for that reason; see
 * `docs/port-history/retired/lift-audit.js`.
 *
 * Both sides are driven from ONE `leases:list` payload and their `dhcpTable`
 * innerHTML compared, which is what catches an attribute a screenshot would
 * miss.
 *
 * ── RE-RENDERS ARE TESTABLE HERE, AND WERE NOT ──────────────────────────────
 *
 * `_renderDhcpServerOptions` falls back to "All leases" when the chosen server
 * has VANISHED from a later payload — a rule that only runs on a RE-RENDER that
 * already carries a selection.
 *
 * The lifted bundle could not be re-rendered: `live-renderer.js` declared the
 * page's state INSIDE `__runLive_dhcp`, so a second call re-declared everything
 * and two calls were two fresh pages. Three cases here LOOKED like they covered
 * the fallback and a mutation deleting it survived them. The state is hoisted
 * now (2026-08-25) and the `then:` cases below drive a genuine second payload.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/live-renderer.js dhcp   # step one
 *   MIKRODASH_SRC=../MikroDash node tools/dhcp-table-check.js     # step two
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim');

const say = console.log.bind(console);
const shout = console.error.bind(console);
const ROOT = path.join(__dirname, '..');

const IDS = ['dhcpTable', 'dhcpTotalBadge', 'dhcpSubnetTable', 'dhcpSearch', 'dhcpServerFilter',
  'dhcpGaugeFill', 'dhcpGaugePct', 'dhcpGaugeTrack', 'lanOverview', 'netInternetIfaces',
  'ndGateway', 'ndLanCidr'];

// What this gate COMPARES, for element-coverage-audit — the table and its badge.
// The gauge has its own gate (`dhcp-gauge-check`) and is deliberately not
// claimed here: two gates claiming one element is how a number stops meaning
// anything.
//
// `dhcpSearch` IS claimed, and the test is whether the gate can tell a change:
// every case sets its value and the filtered table is compared, so a page that
// stopped reading the box fails here. Being a driven INPUT rather than a
// compared OUTPUT does not make it uncovered — it was reported so only because
// this list did not name it.
const COVERS = ['dhcpTable', 'dhcpTotalBadge', 'dhcpServerFilter', 'dhcpSearch'];
if (process.argv.includes('--ids')) { console.log(JSON.stringify(COVERS)); process.exit(0); }

const LIVE_BUNDLE = path.join(ROOT, 'web', 'dist', '_compare', 'live-dhcp.js');
if (!fs.existsSync(LIVE_BUNDLE)) {
  shout('no lifted live renderer — run: node tools/live-renderer.js dhcp');
  process.exit(1);
}
const liveSrc = fs.readFileSync(LIVE_BUNDLE, 'utf8');
// The bundle is generated, so a silent change in what it exports would leave
// this driving nothing.
assert.ok(liveSrc.includes('window.__runLive_dhcp'), 'the lifted bundle no longer exports __runLive_dhcp');

const PORT_BUNDLE = path.join(ROOT, 'web', 'dist', '_compare', 'port-dhcp.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'dhcp.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + PORT_BUNDLE, '--log-level=warning'],
  { stdio: 'inherit' });

const snap = (doc) => JSON.stringify({
  table: doc.nodes.dhcpTable ? doc.nodes.dhcpTable.innerHTML : null,
  // The badge's TEXT AND CLASS. Reading only the text let a mutation dropping
  // `active-blue` survive: the badge turns blue when there are leases, so the
  // colour is data.
  badge: doc.nodes.dhcpTotalBadge
    ? [doc.nodes.dhcpTotalBadge.textContent, doc.nodes.dhcpTotalBadge.className] : null,
  // The server filter's OPTIONS, its VALUE and whether it is shown. The value
  // matters because a server can vanish between updates and the control must
  // fall back to All — a rule that only became testable when the shim learned
  // that a select's value follows its options (dom-shim rule 11).
  servers: doc.nodes.dhcpServerFilter ? [
    doc.nodes.dhcpServerFilter.innerHTML,
    doc.nodes.dhcpServerFilter.value,
    doc.nodes.dhcpServerFilter.style.display,
  ] : null,
});

// THE FILTERS ARE NOT READ AT RENDER TIME. Both sides keep the query in a
// variable that an `input`/`change` listener sets, so assigning `.value` on the
// shim node filters nothing — the believability assert caught exactly that, with
// a "filtered" render identical to the unfiltered one. The event has to be
// FIRED, which is what a real keystroke does.
function applyFilters(doc, o) {
  if (o.query !== undefined && doc.nodes.dhcpSearch) {
    doc.nodes.dhcpSearch.value = o.query;
    doc.nodes.dhcpSearch.fire('input');
  }
  if (o.server !== undefined && doc.nodes.dhcpServerFilter) {
    doc.nodes.dhcpServerFilter.value = o.server;
    doc.nodes.dhcpServerFilter.fire('change');
  }
}

function runLive(payload, o) {
  const doc = makeDoc(IDS);
  const win = {};
  new Function('window', 'document', 'requestAnimationFrame',
    liveSrc + '\nwindow.__runLive_dhcp(arguments[3], arguments[4]);')(
    win, doc, () => {}, payload, o.extra || null);
  applyFilters(doc, o);
  // A genuine SECOND payload to the same page — possible since the bundle's
  // state was hoisted out of the exported function.
  if (o.then) win.__runLive_dhcp(o.then, null);
  return snap(doc);
}

function runPort(payload, o) {
  const doc = makeDoc(IDS);
  const handlers = {};
  const prev = global.document;
  global.document = doc;
  try {
    const { initDhcpPage } = require(PORT_BUNDLE);
    initDhcpPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit() {} }, () => true);
    // Extras first, exactly as the lifted harness orders them: they are state
    // the payload is rendered against.
    for (const ev of Object.keys(o.extra || {})) handlers[ev]?.(o.extra[ev]);
    assert.ok(handlers['leases:list'], 'the port does not subscribe leases:list');
    handlers['leases:list'](payload);
    applyFilters(doc, o);
    if (o.then) handlers['leases:list'](o.then);
  } finally {
    if (prev === undefined) delete global.document; else global.document = prev;
    delete require.cache[PORT_BUNDLE];
  }
  return snap(doc);
}

// BOTH `name` AND `hostName`, because a real lease carries both and the page
// reads them in DIFFERENT places: the search matches on `name`, the table cell
// shows `hostName`. The first version of this fixture set only `hostName`, so
// `l.name` was undefined, the haystack contained the string "undefined", and a
// mutation removing `.toLowerCase()` survived — the search was matching nothing
// case-sensitive because it was matching nothing at all.
//
// `fixture-key-audit` cannot see this class: `hostName` IS read by an
// implementation, so the key is not dead. A fixture setting ONE of two fields a
// renderer reads is a narrower gap, and mutation is what finds it.
const L = (o) => Object.assign({
  ip: '10.0.0.10', mac: '02:00:00:00:00:01', name: 'pc1', hostName: 'pc1', comment: '',
  server: 'defconf', status: 'bound', expiresAfter: '23h', dynamic: true, disabled: false,
  lastSeen: null, blocked: false,
}, o);
const S = (o) => Object.assign({ name: 'defconf', iface: 'bridge', vlanId: '', count: 1 }, o);
const P = (leases, servers) => ({ leases, servers: servers || [] });

const CASES = {
  'no leases': [P([]), {}],
  'one lease': [P([L({})]), {}],
  'several leases': [P([L({}), L({ ip: '10.0.0.11', mac: '02:00:00:00:00:02', name: 'pc2', hostName: 'pc2' })]), {}],
  'a STATIC lease': [P([L({ dynamic: false })]), {}],
  'a disabled lease': [P([L({ disabled: true })]), {}],
  'a lease that is not bound': [P([L({ status: 'waiting' })]), {}],
  'no host name': [P([L({ name: '', hostName: '' })]), {}],
  'a comment': [P([L({ comment: 'the printer' })]), {}],
  'no expiry': [P([L({ expiresAfter: '' })]), {}],
  'a blocked lease': [P([L({ blocked: true })]), {}],
  // Escaping, on every operator-supplied field.
  'markup in a host name': [P([L({ name: '<img src=x>', hostName: '<img src=x>' })]), {}],
  'a quote in a comment': [P([L({ comment: 'a"b' })]), {}],
  // The LEASE's `server` field, which is a different column from the SERVER
  // row's own name below. Both were called 'markup in a server name' until
  // 2026-08-25, and an object literal keeps the last one — so this case had not
  // run since the second was added.
  'markup in a lease\'s server field': [P([L({ server: '<b>s</b>' })]), {}],
  // The filters, which is what dhcpSearch and dhcpServerFilter drive.
  'a search that matches': [P([L({}), L({ ip: '10.0.0.11', name: 'other', hostName: 'other' })]), { query: 'pc1' }],
  'a search that matches nothing': [P([L({})]), { query: 'zzz' }],
  'a search by IP': [P([L({}), L({ ip: '10.0.0.99', name: 'x', hostName: 'x' })]), { query: '0.99' }],
  'a search by MAC': [P([L({})]), { query: '00:01' }],
  // MIXED CASE ON BOTH SIDES. `query: 'PC1'` alone proved nothing: the fixture's
  // host name was already lowercase, so a case-sensitive search still matched
  // and the mutation removing `.toLowerCase()` survived. The haystack has to
  // carry capitals too.
  'an UPPERCASE search against a MixedCase name': [P([L({ name: 'PC1-Office', hostName: 'PC1-Office' })]), { query: 'pc1-OFFICE' }],
  'a lowercase search against a MixedCase name': [P([L({ name: 'PC1-Office', hostName: 'PC1-Office' })]), { query: 'office' }],
  'a search BY COMMENT': [P([L({ comment: 'the printer' }), L({ ip: '10.0.0.14', name: 'x', hostName: 'x' })]),
    { query: 'printer' }],
  // A server whose name CONTAINS another's, so a loose match would take both.
  'a server filter that must be EXACT':
    [P([L({ server: 'lan' }), L({ ip: '10.0.0.15', server: 'lan-guest' })]), { server: 'lan' }],
  'a server filter that matches': [P([L({}), L({ ip: '10.0.0.12', server: 'other' })]), { server: 'defconf' }],
  'a server filter that matches nothing': [P([L({})]), { server: 'nosuch' }],
  'both filters together': [P([L({}), L({ ip: '10.0.0.13', server: 'other', name: 'pc9', hostName: 'pc9' })]),
    { query: 'pc1', server: 'defconf' }],

  // ── the SERVER FILTER's options ──────────────────────────────────────────
  'no servers hides the control': [P([L({})], []), {}],
  'one server': [P([L({})], [S({})]), {}],
  'several servers': [P([L({})], [S({}), S({ name: 'guest', iface: 'vlan20', vlanId: '20', count: 3 })]), {}],
  'a server whose iface EQUALS its name shows no iface segment':
    [P([L({})], [S({ name: 'bridge', iface: 'bridge' })]), {}],
  'a server with a VLAN': [P([L({})], [S({ vlanId: '40' })]), {}],
  'a server with no iface at all': [P([L({})], [S({ iface: '' })]), {}],
  'markup in a server name': [P([L({})], [S({ name: '<b>x</b>' })]), {}],
  // ── RE-RENDER: choose a server, then a new payload arrives ───────────────
  'the chosen server SURVIVES a reload that still has it':
    [P([L({})], [S({}), S({ name: 'guest' })]), {
      server: 'guest', then: P([L({})], [S({}), S({ name: 'guest' })]),
    }],
  'a VANISHED server falls back to All':
    [P([L({})], [S({}), S({ name: 'guest' })]), {
      server: 'guest', then: P([L({})], [S({})]),
    }],
  'a reload with NO servers hides the control':
    [P([L({})], [S({})]), { server: 'defconf', then: P([L({})], []) }],
};

let bad = 0, checked = 0;
for (const [name, [payload, o]] of Object.entries(CASES)) {
  checked++;
  let a, b;
  try { a = runLive(payload, o); } catch (e) { shout('LIVE THREW on %s: %s', name, e.message); bad++; continue; }
  try { b = runPort(payload, o); } catch (e) { shout('PORT THREW on %s: %s', name, e.message); bad++; continue; }
  if (a !== b) {
    bad++;
    console.error('%s\n  live: %s\n  port: %s', name, a.slice(0, 400), b.slice(0, 400));
  }
}

// BELIEVABILITY: the corpus must produce more than one distinct table, and the
// filters must actually remove a row — otherwise this compares one answer to
// itself and the filter cases prove nothing.
const rendered = new Set(Object.values(CASES).map(([p, o]) => runLive(p, o)));
assert.ok(rendered.size > 3, 'the corpus barely varies — it cannot see a renderer that ignores its input');
const all = runLive(P([L({}), L({ ip: '10.0.0.11', name: 'other', hostName: 'other' })]), {});
const filtered = runLive(P([L({}), L({ ip: '10.0.0.11', name: 'other', hostName: 'other' })]), { query: 'pc1' });
assert.notStrictEqual(all, filtered, 'the search filter removes nothing — the filter cases are decorative');

if (bad) {
  shout('\ndhcp-table-check: %d of %d cases differ', bad, checked);
  process.exit(1);
}
say('dhcp-table-check: %d cases identical', checked);
