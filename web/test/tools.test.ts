/**
 * THE TOOLS PAGE'S PING AND TRACEROUTE (slice 8, 2026-09-18).
 *
 * Submitting the form asks the server for one bounded run; the answer is drawn
 * as one line per packet with the router's summary above. Three things are
 * pinned, each with its control:
 *
 * - A lost packet shows its status and no time; a reply shows its time.
 * - Text from the network (a reply host) is escaped.
 * - A result nobody is waiting for — the operator switched router mid-run — is
 *   dropped, while the same payload arriving for a pending run is drawn.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.tools-entry.ts');
fs.writeFileSync(ENTRY, "export { initToolsPage } from '../web/src/pages/tools.js';\n");
const OUT = path.join(ROOT, 'testdata', '.tools.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

const mod = require(OUT);
const doc = makeDoc(['pingForm', 'pingAddress', 'pingCount', 'pingRun', 'pingStatus', 'pingSummary', 'pingRows',
  'traceForm', 'traceAddress', 'traceHops', 'traceRun', 'traceStatus', 'traceSummary', 'traceRows']);
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
const handlers = {};
const sent = [];
mod.initToolsPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit: (ev, d) => sent.push([ev, d]) });

const n = doc.nodes;
const rows = () => String(n.pingRows.innerHTML);
const result = {
  code: '', message: '',
  result: {
    address: '198.51.100.1', sent: 2, received: 1, lossPct: 50, minMs: 0.114, avgMs: 0.114, maxMs: 0.114,
    replies: [
      { seq: 0, host: '<b>x</b>', status: '', rttMs: 0.114, ttl: 64, size: 56 },
      { seq: 1, host: '198.51.100.1', status: 'timeout', rttMs: null, ttl: 0, size: 0 },
    ],
  },
};

// A result with nothing pending is dropped: the control for the case below.
handlers['tools:ping'](result);
assert.ok(!/timeout/.test(rows()), 'a result nobody asked for was drawn:\n' + rows());

n.pingAddress.value = '198.51.100.1';
n.pingCount.value = '2';
n.pingForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent, [['tools:ping', { address: '198.51.100.1', count: 2 }]], 'the form did not ask for one run');
assert.strictEqual(n.pingRun.disabled, true, 'Run stays enabled while a run is pending');

handlers['tools:ping'](result);
const html = rows();
assert.ok(/0\.114 ms/.test(html), 'the reply has no time:\n' + html);
assert.ok(/<span class="wg-down">timeout<\/span>/.test(html), 'the lost packet does not say timeout:\n' + html);
assert.ok(!/<b>x<\/b>/.test(html) && /&lt;b&gt;/.test(html), 'a reply host was not escaped:\n' + html);
assert.ok(/2 sent, 1 received, 50% loss/.test(String(n.pingSummary.textContent)), 'no summary');
assert.strictEqual(n.pingRun.disabled, false, 'Run stays disabled after the result');

// A new run clears the last result at once, so a failure is not shown under the
// previous address's replies.
n.pingForm.fire('submit', { preventDefault: () => {} });
assert.ok(/Not run yet/.test(rows()), 'a new run left the previous result standing');
handlers['tools:ping']({ result: null, code: 'failed', message: 'the router said: failure: resolve failed' });
assert.ok(/resolve failed/.test(String(n.pingStatus.textContent)) && /Not run yet/.test(rows()),
  'a failed run did not show its reason alone');

// A router switch clears a result that is on screen...
n.pingForm.fire('submit', { preventDefault: () => {} });
handlers['tools:ping'](result);
assert.ok(/timeout/.test(rows()), 'the control run was not drawn');
handlers['router:switched']({ activeId: 'r2' });
assert.ok(/Not run yet/.test(rows()), 'a router switch did not clear the old router\'s result');

// ...and drops one that lands after it.
n.pingForm.fire('submit', { preventDefault: () => {} });
handlers['router:switched']({ activeId: 'r3' });
handlers['tools:ping'](result);
assert.ok(/Not run yet/.test(rows()), 'a result for the old router was drawn after the switch');

// TRACEROUTE. A ping result while a traceroute is pending is not the answer it
// is waiting for, and is dropped; the traceroute's own result is drawn.
sent.length = 0;
n.traceAddress.value = '198.51.100.1';
n.traceHops.value = '3';
n.traceForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent, [['tools:traceroute', { address: '198.51.100.1', maxHops: 3 }]], 'the trace form did not ask for one run');
assert.strictEqual(n.pingRun.disabled, true, 'Ping stays enabled while a traceroute runs');
handlers['tools:ping'](result);
assert.ok(/Not run yet/.test(rows()), 'a ping result was drawn while a traceroute was pending');
handlers['tools:traceroute']({ code: '', message: '', result: { address: '198.51.100.1', error: 'Too many hops', hops: [
  { hop: 1, address: '<i>h</i>', timedOut: false, lossPct: 0, lastMs: 1.5, bestMs: 1.5, worstMs: 1.5, status: '' },
  { hop: 2, address: '', timedOut: true, lossPct: 100, lastMs: null, bestMs: null, worstMs: null, status: '' },
] } });
const trace = String(n.traceRows.innerHTML);
assert.ok(/1\.5 ms/.test(trace) && /<span class="wg-down">timeout<\/span>/.test(trace), 'hops not drawn:\n' + trace);
assert.ok(!/<i>h<\/i>/.test(trace), 'a hop address was not escaped:\n' + trace);
assert.ok(/2 hops · Too many hops/.test(String(n.traceSummary.textContent)), 'the router\'s note on the run is missing');
assert.strictEqual(n.pingRun.disabled, false, 'Ping stays disabled after the traceroute');

fs.rmSync(OUT, { force: true });
say('tools: ok');
