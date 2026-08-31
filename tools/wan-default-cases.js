#!/usr/bin/env node
'use strict';
/**
 * Pin "which uplink is carrying our return traffic" — the `activeDefaultWan`
 * block inside `_wanRead` (src/index.js), which decides what `wanGuard` is then
 * asked about.
 *
 * ── WHY THIS ONE RULE GETS ITS OWN CORPUS ───────────────────────────────────
 *
 * The live comment records that it was verified against hardware:
 *
 *   ONLY WHEN THERE IS EXACTLY ONE. Verified on a live router: four default
 *   routes can be active at distance 1 at the same time, and picking the first
 *   would name an uplink our packets may not use — warning about the wrong one
 *   while staying silent on the right one. Ambiguity is reported as unknown,
 *   which makes the guard warn for any WAN rather than guess.
 *
 * That is a rule with a WRONG answer that looks right: `[0].gateway` passes any
 * single-route corpus and fails only on the multi-homed router the comment was
 * written for. It is also the input to a guard that fails open, so getting it
 * wrong is silent in both directions — name the wrong WAN and the real one goes
 * unwarned; the admin sees a prompt about an uplink they are not using and
 * learns to click through.
 *
 * ── THE BLOCK IS LIFTED, NOT RETYPED ────────────────────────────────────────
 *
 * It is ten lines inside a 7,200-line file and not exported, so it is sliced out
 * by two literal anchors and run in a Function with `rows` and `routes` in
 * scope. Retyping it into this generator would test the retyping.
 *
 * The slice asserts what it EXCLUDES as well as what it contains: an anchor that
 * drifted could silently take in the `return` below it or the `self`/`path`
 * lines above, and inclusion alone cannot catch that — a lesson this repo
 * learned when a lifted Queues region ran 2,000 lines past its own page while
 * both of its inclusion assertions passed.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/wan-default-cases.js [--check]
 */

const fs = require('node:fs');
const path = require('node:path');
const assert = require('node:assert');

const SRC = path.resolve(process.env.MIKRODASH_SRC || path.join(__dirname, '..', '..', 'MikroDash'));
const OUT = path.join(__dirname, '..', 'testdata', 'wan-default-cases.json');
const CHECK = process.argv.includes('--check');

const index = fs.readFileSync(path.join(SRC, 'src', 'index.js'), 'utf8');

const START = "const activeDefaults = (routes || [])";
const END = "return { rows, path, activeDefaultWan };";
const from = index.indexOf(START);
assert.ok(from > 0, 'the activeDefaultWan block has moved in index.js');
const to = index.indexOf(END, from);
assert.ok(to > from && to - from < 1200, 'the block is not where its anchors say');
const block = index.slice(from, to);

// MUST contain — the three decisions the block makes.
for (const must of ['activeDefaults.length === 1', "byName", "byLease", "|| gw"]) {
  assert.ok(block.includes(must), 'the lifted block lost: ' + must);
}
// MUST NOT contain — proof the slice stopped where it was meant to.
for (const mustNot of ['resolveManagementPath', 'resolveSelfAddresses', 'return {', 'session.ros.write']) {
  assert.ok(!block.includes(mustNot), 'the slice over-read and took in: ' + mustNot);
}

const run = new Function('rows', 'routes', block + '\n return activeDefaultWan;');

const D = '0.0.0.0/0';
const CASES = [
  { name: 'one active default, gateway is an interface name',
    routes: [{ 'dst-address': D, gateway: 'ether1', active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1' }, { '.id': '*2', interface: 'ether2' }] },

  { name: 'one active default, gateway is a LEASE address — resolved by row.gateway',
    routes: [{ 'dst-address': D, gateway: '198.51.100.1', active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1', gateway: '198.51.100.1' }] },

  { name: 'gateway matches NOTHING — the raw gateway is used as the name',
    routes: [{ 'dst-address': D, gateway: '203.0.113.254', active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1', gateway: '198.51.100.1' }] },

  { name: 'FOUR active defaults at once — ambiguous, so unknown',
    routes: [
      { 'dst-address': D, gateway: 'ether1', active: 'true' },
      { 'dst-address': D, gateway: 'ether2', active: 'true' },
      { 'dst-address': D, gateway: 'ether3', active: 'true' },
      { 'dst-address': D, gateway: 'ether4', active: 'true' },
    ],
    rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'two defaults, only ONE active — the active one wins',
    routes: [
      { 'dst-address': D, gateway: 'ether1', active: 'false' },
      { 'dst-address': D, gateway: 'ether2', active: 'true' },
    ],
    rows: [{ '.id': '*1', interface: 'ether1' }, { '.id': '*2', interface: 'ether2' }] },

  { name: 'no default route at all',
    routes: [{ 'dst-address': '10.0.0.0/8', gateway: 'ether1', active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'a default route that is not active',
    routes: [{ 'dst-address': D, gateway: 'ether1', active: 'false' }],
    rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'active is a BOOLEAN true, not the string — RouterOS sends strings',
    routes: [{ 'dst-address': D, gateway: 'ether1', active: true }],
    rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'the one active default has NO gateway field',
    routes: [{ 'dst-address': D, active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'an IPv6 default is not an IPv4 one',
    routes: [{ 'dst-address': '::/0', gateway: 'ether1', active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'routes missing entirely',
    routes: null, rows: [{ '.id': '*1', interface: 'ether1' }] },

  { name: 'no dhcp-client rows to resolve against',
    routes: [{ 'dst-address': D, gateway: 'ether1', active: 'true' }], rows: [] },

  { name: 'BOTH an interface-name match and a lease match exist — name wins',
    routes: [{ 'dst-address': D, gateway: 'ether9', active: 'true' }],
    rows: [{ '.id': '*1', interface: 'ether1', gateway: 'ether9' },
           { '.id': '*2', interface: 'ether9', gateway: '198.51.100.1' }] },
];

const cases = CASES.map((c) => ({
  name: c.name,
  routes: c.routes,
  rows: c.rows,
  activeDefaultWan: run(c.rows, c.routes),
}));

// BELIEVABILITY: the rule has a known-wrong twin — `[0].gateway` — and a corpus
// that cannot separate them proves nothing. Run the wrong rule over the same
// inputs and require that it disagrees somewhere.
const naive = (rows, routes) => {
  const d = (routes || []).filter(r => r && r['dst-address'] === D && r.active === 'true');
  return d.length && d[0].gateway ? d[0].gateway : '';
};
const separates = cases.filter((c, i) => naive(CASES[i].rows, CASES[i].routes) !== c.activeDefaultWan);
if (!separates.length) {
  throw new Error('every case agrees with `[0].gateway` — this corpus cannot see the exactly-one rule');
}
const named = cases.filter(c => c.activeDefaultWan).length;
if (!named || named === cases.length) {
  throw new Error('the corpus is all-unknown or all-named and cannot discriminate');
}

const text = JSON.stringify({ generatedFrom: 'src/index.js _wanRead', cases }, null, 2) + '\n';
if (CHECK) {
  const cur = fs.existsSync(OUT) ? fs.readFileSync(OUT, 'utf8') : '';
  if (cur !== text) { console.error('wan-default-cases.json is STALE — run: node tools/wan-default-cases.js'); process.exit(1); }
  console.log(`wan-default-cases.json up to date (${cases.length} cases, ${separates.length} separate it from [0].gateway)`);
} else {
  fs.mkdirSync(path.dirname(OUT), { recursive: true });
  fs.writeFileSync(OUT, text);
  console.log(`wrote ${cases.length} cases (${named} name a WAN, ${separates.length} disagree with [0].gateway) -> ${path.relative(process.cwd(), OUT)}`);
}
