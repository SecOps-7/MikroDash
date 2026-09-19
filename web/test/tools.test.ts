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
 * - The output is live: progress frames (`done: false`) are drawn as they come
 *   and leave the run pending; the `done: true` frame settles it. Progress after
 *   a router switch, or for a tool that is not pending, is dropped.
 * - RUN BECOMES STOP (2026-09-19). While a run is pending its own button is an
 *   enabled red Stop, not a disabled Run: pressing it sends `tools:stop`, never a
 *   second run, and the other tools' buttons are disabled. A `stopped` frame
 *   settles the run and draws what it carries. Leaving the page stops it too.
 *   The assertions that said "Run stays disabled while pending" were re-aimed
 *   at "the button is Stop" deliberately: the rule they held, one run at a
 *   time, is still what they check.
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
  'traceForm', 'traceAddress', 'traceHops', 'traceRun', 'traceStatus', 'traceSummary', 'traceRows',
  'torchForm', 'torchInterface', 'torchSeconds', 'torchRun', 'torchStatus', 'torchSummary', 'torchRows',
  'btestForm', 'btestAddress', 'btestUser', 'btestPassword', 'btestSeconds', 'btestProtocol', 'btestDirection',
  'btestRun', 'btestStatus', 'btestSummary', 'btestRows',
  // The live cards (tools-ping-cards.ts, tools-btest-cards.ts).
  'pingScoreRing', 'pingCardScore', 'pingScoreVal', 'pingLastVal', 'pingMinVal', 'pingMaxVal', 'pingLossVal',
  'pingCardLoss', 'pingSpark', 'pingCardLast', 'btestScaleRx', 'btestScaleTx', 'btestArcRx', 'btestNeedleRx',
  'btestValRx', 'btestAvgRx', 'btestArcTx', 'btestNeedleTx', 'btestValTx', 'btestAvgTx', 'btestLostVal', 'btestCpuVal', 'pingScroll', 'torchScroll'],
  // traceMap: an <svg> the trace map draws into with path geometry
  // (getTotalLength, getPointAtLength) this shim does not have. Its planner is
  // tested in tools-cards.test.ts, and the drawing is checked in a browser.
  // traceHopList is only read when traceMap exists, which it does not here.
  { allowUnknown: ['toolsTabs', 'traceMap', 'traceHopList', 'traceMapWrap'] });
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
const handlers = {};
const sent = [];
mod.initToolsPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit: (ev, d) => sent.push([ev, d]) }, () => true);

const n = doc.nodes;
const rows = () => String(n.pingRows.innerHTML);
const result = {
  code: '', message: '', done: true,
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
assert.deepStrictEqual(sent, [['tools:ping', { address: '198.51.100.1', count: 2, continuous: false }]], 'the form did not ask for one run');
const isStop = (b) => b.textContent === 'Stop' && b.classList.contains('sbtn-danger') && !b.disabled;
assert.ok(isStop(n.pingRun), 'the running tool\'s button is not a red, enabled Stop');
assert.strictEqual(n.traceRun.disabled, true, 'another tool can start while a run is pending');

handlers['tools:ping'](result);
const html = rows();
assert.ok(/0\.114 ms/.test(html), 'the reply has no time:\n' + html);
assert.ok(/<span class="wg-down">timeout<\/span>/.test(html), 'the lost packet does not say timeout:\n' + html);
assert.ok(!/<b>x<\/b>/.test(html) && /&lt;b&gt;/.test(html), 'a reply host was not escaped:\n' + html);
assert.ok(/2 sent, 1 received, 50% loss/.test(String(n.pingSummary.textContent)), 'no summary');
// THE CARDS follow the same frame: half the packets lost scores 0.
assert.strictEqual(String(n.pingLossVal.textContent), '50', 'the Loss card was not drawn');
assert.strictEqual(String(n.pingScoreVal.textContent), '0', 'the Score card does not score a 50% loss as 0');
assert.ok(n.pingCardLoss.classList.contains('is-bad') && n.pingCardScore.classList.contains('grade-poor'),
  'the Loss and Score cards are not marked bad');
assert.ok(n.pingRun.textContent === 'Ping' && !n.pingRun.classList.contains('sbtn-danger') && !n.pingRun.disabled,
  'the button is not Ping again after the result');

// A new run clears the last result at once, so a failure is not shown under the
// previous address's replies.
n.pingForm.fire('submit', { preventDefault: () => {} });
assert.ok(/Not run yet/.test(rows()), 'a new run left the previous result standing');
handlers['tools:ping']({ done: true, result: null, code: 'failed', message: 'the router said: failure: resolve failed' });
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

// LIVE OUTPUT. Progress frames arrive while the run is going: each is drawn,
// the run stays pending (its button stays Stop, the status still says running), and
// the done frame settles it.
const replies = result.result.replies;
const frame = (k: number, done: boolean) => ({ code: '', message: '', done,
  result: { ...result.result, sent: k, replies: replies.slice(0, k) } });
n.pingForm.fire('submit', { preventDefault: () => {} });
handlers['tools:ping'](frame(1, false));
assert.ok(/0\.114 ms/.test(rows()) && !/timeout/.test(rows()), 'the first progress frame was not drawn:\n' + rows());
assert.ok(isStop(n.pingRun), 'a progress frame settled the run');
assert.ok(/Running/.test(String(n.pingStatus.textContent)), 'a progress frame cleared the running status');
handlers['tools:ping'](frame(2, false));
assert.ok(/<span class="wg-down">timeout<\/span>/.test(rows()), 'the second progress frame was not drawn:\n' + rows());
assert.ok(isStop(n.pingRun), 'the second progress frame settled the run');
handlers['tools:ping'](frame(2, true));
assert.ok(n.pingRun.textContent === 'Ping' && !n.pingRun.disabled, 'the done frame did not settle the run');
assert.strictEqual(String(n.pingStatus.textContent), '', 'the running status outlived the done frame');
assert.ok(/2 sent/.test(String(n.pingSummary.textContent)), 'the done frame was not drawn');
// Progress for a tool that is not pending is dropped (the control is above)...
n.traceForm.fire('submit', { preventDefault: () => {} });
handlers['tools:ping'](frame(1, false));
assert.ok(/timeout/.test(rows()), 'a ping progress frame was drawn while a traceroute was pending:\n' + rows());
handlers['tools:traceroute']({ code: '', message: '', done: true, result: { address: 'x', error: '', hops: [] } });
// ...and so is progress that lands after a router switch.
n.pingForm.fire('submit', { preventDefault: () => {} });
handlers['router:switched']({ activeId: 'r3b' });
handlers['tools:ping'](frame(1, false));
assert.ok(/Not run yet/.test(rows()), 'a progress frame for the old router was drawn after the switch');

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
handlers['tools:traceroute']({ code: '', message: '', done: true, result: { address: '198.51.100.1', error: 'Too many hops', hops: [
  { hop: 1, address: '<i>h</i>', timedOut: false, lossPct: 0, lastMs: 1.5, bestMs: 1.5, worstMs: 1.5, status: '' },
  { hop: 2, address: '', timedOut: true, lossPct: 100, lastMs: null, bestMs: null, worstMs: null, status: '' },
] } });
const trace = String(n.traceRows.innerHTML);
assert.ok(/1\.5 ms/.test(trace) && /<span class="wg-down">timeout<\/span>/.test(trace), 'hops not drawn:\n' + trace);
assert.ok(!/<i>h<\/i>/.test(trace), 'a hop address was not escaped:\n' + trace);
assert.ok(/2 hops · Too many hops/.test(String(n.traceSummary.textContent)), 'the router\'s note on the run is missing');
assert.strictEqual(n.pingRun.disabled, false, 'Ping stays disabled after the traceroute');

// TORCH needs write access. Until `tools:caps` says this viewer has it, its
// button is disabled; a reader is told why; a writer can run it.
assert.strictEqual(n.torchRun.disabled, true, 'Watch is offered before the permission is known');
handlers['tools:caps']({ mayWrite: false, interfaces: ['ether1', '<b>w</b>'] });
assert.strictEqual(n.torchRun.disabled, true, 'Watch is offered to a viewer who may not write Tools');
assert.ok(/write access/.test(String(n.torchStatus.textContent)), 'a reader is not told why Watch is off');
assert.ok(/<option>ether1<\/option>/.test(String(n.torchInterface.innerHTML)) && !/<b>w<\/b>/.test(String(n.torchInterface.innerHTML)),
  'the interface list is missing or unescaped:\n' + n.torchInterface.innerHTML);
assert.strictEqual(n.pingRun.disabled, false, 'a read tool was disabled for a reader');
handlers['tools:caps']({ mayWrite: true, interfaces: ['ether1'] });
assert.strictEqual(n.torchRun.disabled, false, 'Watch stays disabled for a viewer who may write Tools');
sent.length = 0;
n.torchInterface.value = 'ether1';
n.torchSeconds.value = '3';
n.torchForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent, [['tools:torch', { interface: 'ether1', seconds: 3, continuous: false }]], 'the torch form did not ask for one run');
handlers['tools:torch']({ code: '', message: '', done: true, result: { interface: 'ether1', seconds: 3, reports: 2, omitted: 2, totalRxBps: 2000000, totalTxBps: 0,
  flows: [{ protocol: 'tcp', srcAddress: '198.51.100.1', srcPort: '443', dstAddress: '198.51.100.2', dstPort: '50000', rxBps: 2000000, txBps: 0 }] } });
assert.ok(/2\.00 Mbps/.test(String(n.torchRows.innerHTML)) && /198\.51\.100\.1:443/.test(String(n.torchRows.innerHTML)),
  'the flow was not drawn:\n' + n.torchRows.innerHTML);
// Rx and Tx in the app's rx blue and tx green; the protocol as its pill.
assert.ok(/<td style="color:var\(--accent-rx\)">2\.00 Mbps<\/td>/.test(String(n.torchRows.innerHTML)) &&
  /<td style="color:var\(--accent-tx\)">/.test(String(n.torchRows.innerHTML)),
  'Rx and Tx are not in the rx and tx colours:\n' + n.torchRows.innerHTML);
assert.ok(/<span class="bw-proto bw-proto-tcp">tcp<\/span>/.test(String(n.torchRows.innerHTML)),
  'the protocol is not a pill:\n' + n.torchRows.innerHTML);
assert.ok(/2 quieter flows not shown/.test(String(n.torchSummary.textContent)), 'omitted flows are not admitted to');

// STOP. Pressing the running tool's button asks the server to stop, and sends
// no second run; the stopped frame draws the run so far and settles it.
sent.length = 0;
n.pingCount.value = 'continuous';
n.pingForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent, [['tools:ping', { address: '198.51.100.1', count: 0, continuous: true }]],
  'the Continuous option did not reach the request');
handlers['tools:ping'](frame(1, false));
n.pingForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent.slice(1), [['tools:stop', {}]], 'pressing Stop did not ask for the run to stop');
assert.strictEqual(n.pingRun.disabled, true, 'Stop can be pressed twice while the stop is on its way');
assert.ok(/Stopping/.test(String(n.pingStatus.textContent)), 'the status does not say it is stopping');
handlers['tools:ping']({ ...frame(2, true), code: 'stopped' });
assert.ok(/<span class="wg-down">timeout<\/span>/.test(rows()), 'the stopped frame\'s run so far was not drawn:\n' + rows());
assert.ok(/Stopped/.test(String(n.pingStatus.textContent)), 'a stopped run does not say so');
assert.ok(n.pingRun.textContent === 'Ping' && !n.pingRun.disabled, 'a stopped run left the button as Stop');
n.pingCount.value = '2';
// LEAVING THE PAGE STOPS THE RUN; opening it again, or leaving with nothing
// running, sends nothing (the control).
sent.length = 0;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'dashboard' });
assert.deepStrictEqual(sent, [], 'leaving the page with nothing running sent something');
n.pingForm.fire('submit', { preventDefault: () => {} });
sent.length = 0;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'dashboard' });
assert.deepStrictEqual(sent, [['tools:stop', {}]], 'leaving the page did not stop the run');
handlers['tools:ping']({ ...frame(1, true), code: 'stopped' });

// THE BANDWIDTH TEST sends the password once, then empties its field; a failed
// test says what RouterOS said.
sent.length = 0;
n.btestAddress.value = '198.51.100.53';
n.btestUser.value = 'md-btest';
n.btestPassword.value = 'hunter2';
n.btestSeconds.value = '3';
n.btestProtocol.value = 'tcp';
n.btestDirection.value = 'both';
n.btestForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent, [['tools:btest', { address: '198.51.100.53', user: 'md-btest', password: 'hunter2',
  seconds: 3, protocol: 'tcp', direction: 'both' }]], 'the test form did not ask for one run with its login');
assert.strictEqual(n.btestPassword.value, '', 'the password was left in the page after the run was sent');
handlers['tools:btest']({ done: true, code: 'failed', message: 'the test did not run: authentication failed', result: null });
assert.ok(/authentication failed/.test(String(n.btestStatus.textContent)), 'a failed test did not say why');
n.btestForm.fire('submit', { preventDefault: () => {} });
handlers['tools:btest']({ code: '', message: '', done: true, result: { address: '198.51.100.53', done: true, status: 'done testing',
  direction: 'both', duration: '3s', rxBps: 4612040, txBps: 1702032, lostPackets: 0, localCpu: 0, remoteCpu: 3 } });
assert.ok(/4\.61 Mbps/.test(String(n.btestRows.innerHTML)) && /far one 3%/.test(String(n.btestRows.innerHTML)),
  'the result was not drawn:\n' + n.btestRows.innerHTML);
// The gauges: no current rate in this frame, so the needles rest and the
// averages are written beneath them.
assert.ok(/avg 4\.61 Mbps/.test(String(n.btestAvgRx.textContent)) && /avg 1\.70 Mbps/.test(String(n.btestAvgTx.textContent)),
  'the gauges do not show the run averages: ' + n.btestAvgRx.textContent + ' / ' + n.btestAvgTx.textContent);

// A router switch forgets the permission until the new router's caps arrive.
handlers['router:switched']({ activeId: 'r4' });
assert.strictEqual(n.torchRun.disabled, true, 'a router switch kept the old router\'s write permission');
assert.ok(sent.some(([ev]) => ev === 'tools:caps'), 'a router switch on the open page did not ask for the new caps');

fs.rmSync(OUT, { force: true });
say('tools: ok');
