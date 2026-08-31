#!/usr/bin/env node
'use strict';
/**
 * The network diagram's WAN readout — the `lan:wan` handler, live against
 * ported.
 *
 * ── IT COMPARES ONE OF THE LIVE HANDLER'S THREE STATEMENTS, DELIBERATELY ────
 *
 * `app.js:1010` does three things and only ONE of them exists:
 *
 *   window._wanGeoDetect(data.wanIp)   assigned NOWHERE in the live repo
 *   ndWanIp.textContent = wip          the readout — the real one
 *   wanIpDisplay.textContent = wip     in the live repo's own KNOWN orphan set
 *
 * The port reproduces the middle one and omits the other two, because
 * reproducing them means calling a function nobody defines and looking up an
 * element that does not exist — no behaviour either way, and this port's
 * `lookup-audit` would then have to carry an orphan it invented. Reported as
 * ToDo.md #23.
 *
 * So this gate compares `ndWanIp` and says so, rather than comparing everything
 * and carrying two permanent exceptions. The live side is given the two missing
 * things as no-ops so it runs at all — which is the harness being fair to it,
 * not the port being let off.
 *
 *   MIKRODASH_SRC=../MikroDash node tools/lan-wan-check.js
 */

const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const assert = require('node:assert');
const { execFileSync } = require('node:child_process');
const { makeDoc } = require('./lib/dom-shim');

const ROOT = path.join(__dirname, '..');
const LIVE = path.resolve(process.env.MIKRODASH_SRC || path.join(ROOT, '..', 'MikroDash'));
const LIFT = require('./lib/lift.js');
const G = LIFT.golden('lan-wan-check');
const src = LIFT.liveSource(ROOT, path.join('public', 'app.js'));

// THE LIFT, AND ITS VALIDITY ASSERTIONS, ONLY WHERE THERE IS A SOURCE TO LIFT
// FROM. Every one of these asks the live source a question — has the handler
// moved, did the slice over-read — and none is answerable without it. `body` is
// consumed solely by `liveRun`, which from here on is called only inside a
// frozen closure.
let body = '';
if (LIFT.hasReference(ROOT)) {
  const START = "socket.on('lan:wan',function(data){";
  const from = src.indexOf(START);
  assert.ok(from > 0, "the lan:wan handler has moved in app.js");
  const to = src.indexOf('\n});', from);
  assert.ok(to > from && to - from < 500, 'the handler is not where its anchors say');
  body = src.slice(src.indexOf('{', from + START.length - 1) + 1, to);

  for (const must of ['ndWanIp', "split('/')"]) {
    assert.ok(body.includes(must), 'the lifted handler lost: ' + must);
  }
  for (const mustNot of ["socket.on('lan:overview'", 'netInternetIfaces']) {
    assert.ok(!body.includes(mustNot), 'the slice over-read and took in: ' + mustNot);
  }
}

const ENTRY = path.join(ROOT, 'testdata', '.lanwan-entry.ts');
const OUT = path.join(ROOT, 'testdata', '.lanwan-port.cjs');
fs.writeFileSync(ENTRY, "export { initDhcpPage } from '../web/src/pages/dhcp';\n");
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const { initDhcpPage } = require(OUT);

const IDS = ['ndWanIp'];

function liveRun(data) {
  const doc = makeDoc(IDS, {});
  const ctx = {
    String, Object, document: doc,
    // The two statements the port omits, as no-ops. `wanIpDisplay` is `null` in
    // a real browser too — the element does not exist — so this is the live
    // behaviour rather than a convenience.
    $: (id) => doc.nodes[id] || null,
    wanIpDisplay: null,
    window: {},
  };
  vm.createContext(ctx);
  vm.runInContext('(function(data){' + body + '})(' + JSON.stringify(data) + ');', ctx);
  return doc.nodes.ndWanIp ? doc.nodes.ndWanIp.textContent : null;
}

function portRun(data) {
  const doc = makeDoc(IDS, {});
  const handlers = {};
  const socket = { on: (ev, fn) => { handlers[ev] = fn; }, emit: () => {} };
  global.document = doc;
  global.window = { addEventListener: () => {} };
  try {
    initDhcpPage(socket, () => true);
  } catch (_) { /* the page wires more than this handler; only lan:wan matters */ }
  assert.ok(handlers['lan:wan'], 'the port does not subscribe lan:wan');
  handlers['lan:wan'](data);
  return doc.nodes.ndWanIp ? doc.nodes.ndWanIp.textContent : null;
}

const CASES = {
  'an address with a prefix': { ts: 1, wanIp: '203.0.113.7/24' },
  'an address with no prefix': { ts: 1, wanIp: '203.0.113.7' },
  'an empty address': { ts: 1, wanIp: '' },
  'no wanIp key at all': { ts: 1 },
  'a null wanIp': { ts: 1, wanIp: null },
  'a bare slash': { ts: 1, wanIp: '/24' },
  'an IPv6 address with a prefix': { ts: 1, wanIp: '2001:db8::1/64' },
  'several slashes': { ts: 1, wanIp: 'a/b/c' },
};

let bad = 0, checked = 0;
const liveVals = [];
for (const [name, data] of Object.entries(CASES)) {
  checked++;
  const a = G.live(name, () => liveRun(data));
  liveVals.push(a);
  const b = portRun(data);
  if (a !== b) { bad++; console.error('%s\n  live: %j\n  port: %j', name, a, b); }
}

// BELIEVABILITY: the corpus must produce more than one distinct readout, or it
// cannot tell a working handler from one that writes a constant.
//
// BUILT FROM THE VALUES THE LOOP ALREADY HAS, not by running the live handler a
// second time. That keeps the check working without a reference AND without a
// second recording per case — and it now also catches a golden flattened to a
// single repeated value, which the old form could not have.
const seen = new Set(liveVals);
assert.ok(seen.size > 1, 'every case renders the same text — this gate proves nothing');

if (bad) {
  console.error('\nlan-wan-check: %d of %d cases differ', bad, checked);
  process.exit(1);
}
console.log('lan-wan-check: %d cases identical (%d distinct readouts)', checked, seen.size);
