/**
 * THE TOOLS PAGE'S PING AND TRACEROUTE (slice 8, 2026-09-18).
 *
 * Submitting the form asks the server for one bounded run; the answer is drawn
 * as one line per packet with the router's summary above. Three things are
 * pinned, each with its control:
 *
 * - A lost packet shows its status and no time; a reply shows its time.
 * - Text from the network (a reply host) is escaped.
 * - A result nobody is waiting for - the operator switched router mid-run - is
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
fs.writeFileSync(ENTRY, "export { initToolsPage, filenameOf, readWithProgress } from '../web/src/pages/tools.js';\n");
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
  'btestValRx', 'btestAvgRx', 'btestArcTx', 'btestNeedleTx', 'btestValTx', 'btestAvgTx', 'btestLostVal', 'btestCpuVal', 'pingScroll', 'torchScroll',
  // The count pills beside each page's title. Bandwidth Test has none.
  'pingBadge', 'traceBadge', 'torchBadge',
  // The Packet Sniffer (2026-09-27): its form, its sortable header, its four
  // cards and the Export button.
  'snifferForm', 'snifferInterface', 'snifferProtocol', 'snifferPort', 'snifferAddress',
  'snifferDirection', 'snifferRun', 'snifferStatus', 'snifferSummary', 'snifferHead',
  'snifferRows', 'snifferBadge', 'snifferScroll', 'snifferExport',
  'snifferExportProgress', 'snifferExportBar', 'snifferExportStatus',
  'snifferPacketsVal', 'snifferBytesVal', 'snifferProtoVal', 'snifferProtoFoot',
  'snifferTalkerVal', 'snifferTalkerFoot'],
  // traceMap: an <svg> the trace map draws into with path geometry
  // (getTotalLength, getPointAtLength) this shim does not have. Its planner is
  // tested in tools-cards.test.ts, and the drawing is checked in a browser.
  // traceHopList is only read when traceMap exists, which it does not here.
  { allowUnknown: ['traceMap', 'traceHopList', 'traceMapWrap'] });
global.document = doc;
global.window = { addEventListener: () => {}, setTimeout, clearTimeout };
const handlers = {};
const sent = [];
// THE EXPORT'S fetch IS STUBBED WITH A SYNCHRONOUS THENABLE, deliberately. A
// real promise resolves in a microtask, which has not run by the time the next
// assertion in this file executes, so a test that "awaited" it would be reading
// the state before the handler wrote it. What is pinned here is the REQUEST -
// the URL and the router it names - and the response half is `filenameOf`,
// tested directly below, plus the live check in a browser.
const fetched = [];
global.fetch = (url) => { fetched.push(url); return { then: () => ({ catch: () => {} }) }; };
mod.initToolsPage({ on: (ev, fn) => { handlers[ev] = fn; }, emit: (ev, d) => sent.push([ev, d]) },
  () => true, () => 'r-live');

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

// TORCH needs write access ON ITS OWN PAGE. Until `tools:caps` says this viewer
// has it, its button is disabled; a reader is told why; a writer can run it.
assert.strictEqual(n.torchRun.disabled, true, 'Watch is offered before the permission is known');
handlers['tools:caps']({ mayTorch: false, mayBtest: false, maySniff: false, snifferAllowed: true, interfaces: ['ether1', '<b>w</b>'] });
assert.strictEqual(n.torchRun.disabled, true, 'Watch is offered to a viewer who may not write the Torch page');
assert.ok(/write access/.test(String(n.torchStatus.textContent)), 'a reader is not told why Watch is off');
assert.ok(/<option>ether1<\/option>/.test(String(n.torchInterface.innerHTML)) && !/<b>w<\/b>/.test(String(n.torchInterface.innerHTML)),
  'the interface list is missing or unescaped:\n' + n.torchInterface.innerHTML);
assert.strictEqual(n.pingRun.disabled, false, 'a read tool was disabled for a reader');
// ONE FLAG PER WRITE TOOL, which is the whole reason the four are separate
// pages: Torch and Bandwidth Test are separate grants, so a frame can permit one
// and refuse the other. A single `mayWrite` could not tell these two apart.
handlers['tools:caps']({ mayTorch: true, mayBtest: false, maySniff: false, snifferAllowed: true, interfaces: ['ether1'] });
assert.strictEqual(n.torchRun.disabled, false, 'Watch stays disabled for a viewer who may write the Torch page');
assert.strictEqual(n.btestRun.disabled, true, 'Test is offered to a viewer who may not write the Bandwidth Test page');
handlers['tools:caps']({ mayTorch: true, mayBtest: true, maySniff: false, snifferAllowed: true, interfaces: ['ether1'] });
assert.strictEqual(n.btestRun.disabled, false, 'Test stays disabled for a viewer who may write its page');
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
// THE COUNT PILL beside each page's title counts what that page lists, and goes
// blue when it counts something - the furniture every generated page has.
assert.strictEqual(String(n.torchBadge.textContent), '1', 'the Torch pill does not count its flows');
assert.strictEqual(String(n.torchBadge.className), 'card-badge active-blue', 'a pill counting something is not blue');
assert.strictEqual(String(n.traceBadge.textContent), '2', 'the Traceroute pill does not count its hops');

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

// AND A SIBLING TOOL PAGE IS LEAVING TOO (2026-09-27). The four diagnostics are
// four pages now, so going from Ping to Torch leaves the Ping page as surely as
// going to the Dashboard does; the run must stop, and the new page's caps must
// be asked for. Arriving on a tool page with nothing running asks only for caps,
// which is the control that separates the two sends.
sent.length = 0;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'tools-ping' });
assert.deepStrictEqual(sent, [['tools:caps', {}]], 'opening a tool page with nothing running did more than ask for caps');
n.pingForm.fire('submit', { preventDefault: () => {} });
sent.length = 0;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'tools-torch' });
assert.deepStrictEqual(sent, [['tools:stop', {}], ['tools:caps', {}]],
  'moving from Ping to Torch did not stop the ping and ask for the new page\'s caps');
handlers['tools:ping']({ ...frame(1, true), code: 'stopped' });
// AND THE RUN'S OWN PAGE IS NOT LEAVING: a pagechange back to the page the run
// is on must not stop it. The control for the rule above.
n.pingForm.fire('submit', { preventDefault: () => {} });
sent.length = 0;
doc.dispatchEvent({ type: 'mikrodash:pagechange', detail: 'tools-ping' });
assert.deepStrictEqual(sent, [['tools:caps', {}]], 'a pagechange to the running tool\'s own page stopped it');
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

// ── THE PACKET SNIFFER (2026-09-27) ─────────────────────────────────────────
//
// The fifth tool, and the one whose gate has TWO failing modes: the viewer may
// not write the page, or the DEVICE refuses the sniffer in device-mode, which is
// nobody's permission. Each is checked against the other as its control, because
// a single flag would pass whichever message it happened to pick.
handlers['tools:caps']({ mayTorch: true, mayBtest: true, maySniff: false, snifferAllowed: true,
  interfaces: ['ether1', '<b>w</b>'] });
assert.strictEqual(n.snifferRun.disabled, true, 'Start is offered to a viewer who may not write the Sniffer page');
assert.ok(/write access/.test(String(n.snifferStatus.textContent)),
  'a reader is not told why Start is off: ' + n.snifferStatus.textContent);
handlers['tools:caps']({ mayTorch: true, mayBtest: true, maySniff: true, snifferAllowed: false, interfaces: ['ether1'] });
assert.strictEqual(n.snifferRun.disabled, true, 'Start is offered on a device whose sniffer is switched off');
assert.ok(/device-mode/.test(String(n.snifferStatus.textContent)),
  'a device-mode refusal is reported as a permission problem: ' + n.snifferStatus.textContent);
handlers['tools:caps']({ mayTorch: true, mayBtest: true, maySniff: true, snifferAllowed: true,
  interfaces: ['ether1', '<b>w</b>'] });
assert.strictEqual(n.snifferRun.disabled, false, 'Start stays off for a viewer who may write and a device that allows it');
assert.strictEqual(String(n.snifferStatus.textContent), '', 'a permitted viewer is still told they are blocked');
// The picker keeps its All interfaces entry - an empty filter-interface is what
// RouterOS means by every interface - and escapes the names.
assert.ok(/<option value="">All interfaces<\/option>/.test(String(n.snifferInterface.innerHTML)) &&
  /<option>ether1<\/option>/.test(String(n.snifferInterface.innerHTML)) &&
  !/<b>w<\/b>/.test(String(n.snifferInterface.innerHTML)),
  'the sniffer interface picker is wrong:\n' + n.snifferInterface.innerHTML);
// Nothing captured, so nothing to export - the control for the assertion below.
assert.strictEqual(n.snifferExport.disabled, true, 'Export is offered before anything has been captured');

sent.length = 0;
n.snifferInterface.value = 'ether1';
n.snifferProtocol.value = 'tcp';
n.snifferPort.value = '443';
n.snifferAddress.value = '198.51.100.0/24';
n.snifferDirection.value = 'rx';
n.snifferForm.fire('submit', { preventDefault: () => {} });
assert.deepStrictEqual(sent, [['tools:sniffer', { interface: 'ether1', ipProtocol: 'tcp', port: '443',
  address: '198.51.100.0/24', direction: 'rx' }]], 'the sniffer form did not ask for one capture');
assert.ok(isStop(n.snifferRun), 'the running capture\'s button is not a red, enabled Stop');

const capture = {
  running: true, totalPackets: 332, totalBytes: 101208,
  topProtocol: 'tcp', topProtocolShare: 96.76, topTalker: '198.51.100.10', topTalkerBytes: 100896,
  packets: [
    { num: 9, time: 10.5, interface: 'ether1', direction: 'tx', source: '198.51.100.10:8728',
      dest: '<i>x</i>', protocol: 'tcp', size: 1400, tcpFlags: 'psh,ack' },
    { num: 8, time: 9.25, interface: 'ether1', direction: 'rx', source: '198.51.100.11',
      dest: '198.51.100.10', protocol: 'icmp', size: 70, tcpFlags: '' },
  ],
};
handlers['tools:sniffer']({ code: '', message: '', done: false, result: capture });
const sniff = () => String(n.snifferRows.innerHTML);
assert.ok(/198\.51\.100\.10:8728/.test(sniff()), 'the capture was not drawn:\n' + sniff());
assert.ok(!/<i>x<\/i>/.test(sniff()) && /&lt;i&gt;/.test(sniff()), 'an address was not escaped:\n' + sniff());
assert.ok(/<span class="sniff-dir sniff-dir-tx">tx<\/span>/.test(sniff()) &&
  /<span class="sniff-dir sniff-dir-rx">rx<\/span>/.test(sniff()),
  'the direction is not a pill:\n' + sniff());
assert.ok(/<span class="bw-proto bw-proto-tcp">tcp<\/span>/.test(sniff()) &&
  /<span class="bw-proto bw-proto-icmp">icmp<\/span>/.test(sniff()),
  'the protocol is not the shared pill:\n' + sniff());
assert.ok(isStop(n.snifferRun), 'a progress frame settled the capture');
// ── THE CARDS COUNT THE CAPTURE, NOT THE ROWS ─────────────────────────────
//
// 332 packets against 2 rows on screen. Counting the rows is the plausible wrong
// answer, and it looks entirely reasonable on the card, so it is asserted
// against the row count explicitly.
assert.strictEqual(String(n.snifferPacketsVal.textContent), '332', 'the Packets card does not count the capture');
assert.notStrictEqual(String(n.snifferPacketsVal.textContent), String(capture.packets.length),
  'the Packets card counted the rows on screen');
assert.strictEqual(String(n.snifferBytesVal.textContent), '98.8 KB', 'the Captured card is wrong');
assert.ok(/bw-proto-tcp/.test(String(n.snifferProtoVal.innerHTML)), 'the Top protocol card has no pill');
assert.strictEqual(String(n.snifferProtoFoot.textContent), '96.76% of bytes', 'the Top protocol share is wrong');
assert.strictEqual(String(n.snifferTalkerVal.textContent), '198.51.100.10', 'the Top talker card is wrong');
assert.ok(/98\.5 KB both ways/.test(String(n.snifferTalkerFoot.textContent)),
  'the Top talker total is wrong: ' + n.snifferTalkerFoot.textContent);
// The count pill counts the rows the page LISTS, as every other page's does.
assert.strictEqual(String(n.snifferBadge.textContent), '2', 'the Sniffer pill does not count its packets');
assert.strictEqual(String(n.snifferBadge.className), 'card-badge active-blue', 'a pill counting something is not blue');
assert.ok(/332 packets seen \u00b7 showing the latest 2/.test(String(n.snifferSummary.textContent)),
  'the summary does not say the table is only the latest rows: ' + n.snifferSummary.textContent);

// SORTING. Newest first to begin with, and a click on a column reorders the
// table without waiting for the next poll. Size ascending puts the 70-byte
// packet first, which is the opposite of the default order - so a sort that
// silently did nothing would fail here.
const firstNum = () => /<tr><td>(\d+)<\/td>/.exec(sniff())[1];
assert.strictEqual(firstNum(), '9', 'the capture does not start newest first');
const th = n.snifferHead.querySelectorAll('th');
assert.strictEqual(th.length, 8, 'the sortable header was not drawn: ' + th.length + ' cells');
th[7].click();
assert.strictEqual(firstNum(), '8', 'clicking Size did not reorder the table');
th[7].click();
assert.strictEqual(firstNum(), '9', 'a second click on Size did not reverse it');
th[0].click();
assert.strictEqual(firstNum(), '8', 'clicking # did not sort ascending');
th[0].click();

// EXPORT. Enabled once there is something captured, and it asks for the ACTIVE
// router's capture.
assert.strictEqual(n.snifferExport.disabled, false, 'Export is still off with a capture on screen');
fetched.length = 0;
n.snifferExport.fire('click', {});
assert.deepStrictEqual(fetched, ['/api/tools/sniffer/pcap?routerId=r-live'],
  'Export did not ask for the active router\'s capture: ' + JSON.stringify(fetched));
// THE NAME COMES FROM THE SERVER, which read the capture's magic bytes; the page
// never decides whether it is a .pcapng or a .pcap.
assert.strictEqual(mod.filenameOf('attachment; filename="chr-test-capture.pcapng"'), 'chr-test-capture.pcapng',
  'the download name was not read out of the Content-Disposition');
assert.strictEqual(mod.filenameOf(null), '', 'a missing Content-Disposition produced a name anyway');

// A device that refuses says so, and the capture settles.
handlers['tools:sniffer']({ code: 'device-mode', message: '', done: true, result: null });
assert.ok(/device-mode/.test(String(n.snifferStatus.textContent)),
  'a device-mode refusal mid-run was not explained: ' + n.snifferStatus.textContent);
assert.ok(n.snifferRun.textContent === 'Start' && !n.snifferRun.disabled, 'the refusal left the button as Stop');
// A filter the router would refuse carries the reason, which is the server's
// words rather than one of this page's canned ones.
n.snifferForm.fire('submit', { preventDefault: () => {} });
handlers['tools:sniffer']({ code: 'filter', message: 'the ports must be up to 16 comma-separated numbers, 1 to 65535',
  done: true, result: null });
assert.ok(/comma-separated numbers/.test(String(n.snifferStatus.textContent)),
  'a refused filter did not say what was wrong: ' + n.snifferStatus.textContent);
// And a router switch clears the capture and puts Export back.
n.snifferForm.fire('submit', { preventDefault: () => {} });
handlers['tools:sniffer']({ code: '', message: '', done: true, result: capture });
assert.ok(/198\.51\.100\.10/.test(sniff()), 'the control capture was not drawn');
handlers['router:switched']({ activeId: 'r5' });
assert.ok(/Not run yet/.test(sniff()), 'a router switch did not clear the capture');
assert.strictEqual(n.snifferExport.disabled, true, 'Export survived a router switch');
assert.strictEqual(String(n.snifferPacketsVal.textContent), '-', 'the cards survived a router switch');

// ── THE PCAP EXPORT'S PROGRESS BAR ──────────────────────────────────────────
//
// A megabyte of capture is about 39 seconds off a busy router, so the server
// streams it with Content-Length declared and the page counts what has arrived.
// Two claims, and the second needs a control: the bar is INDETERMINATE until
// the headers land (nothing yet knows how big the file will be) and DETERMINATE
// after, because "waiting" and "stalled at 0%" must not look the same.
n.snifferExport.disabled = false;
n.snifferExport.fire('click', {});
assert.ok(n.snifferExportProgress.classList.contains('is-on'),
  'the export bar never appeared');
assert.ok(n.snifferExportProgress.classList.contains('is-wait'),
  'the bar was determinate before Content-Length was known, so waiting looks like stalled');
assert.strictEqual(String(n.snifferExportStatus.textContent), 'Preparing the capture…',
  'the wait phase did not say what it was waiting for: ' + n.snifferExportStatus.textContent);
// AND IT IS THE EXPORT'S OWN LINE. A capture writes "Running…" to the form's
// status on every frame; sharing it meant the byte count was overwritten about
// twice a second while the bar carried on moving.
assert.notStrictEqual(String(n.snifferStatus.textContent), 'Preparing the capture…',
  'the export wrote to the capture\'s status line, which a running capture overwrites');

// readWithProgress counts each chunk against the total. `now` is a synchronous
// thenable, so the pump unrolls before the next statement - the same trick the
// clipboard stub uses, because these checks do not await.
const now = (v) => ({ then: (f) => f(v) });
const chunks = [new Uint8Array(30), new Uint8Array(70)];
let at = -1;
const res = { body: { getReader: () => ({ read: () => { at++; return now(at < chunks.length ? { done: false, value: chunks[at] } : { done: true }); } }) } };
const seen = [];
const blob = mod.readWithProgress(res, 100, (got, total) => seen.push([got, total]));
assert.deepStrictEqual(seen, [[0, 100], [30, 100], [100, 100]],
  'the reader did not report progress per chunk: ' + JSON.stringify(seen));
assert.strictEqual(blob.size, 100, 'the assembled blob lost bytes: ' + blob.size);

// THE FALLBACK: a response with no readable body reads whole, which is what the
// page did before. No progress is not the same as no download.
let blobbed = 0;
const whole = mod.readWithProgress({ blob: () => { blobbed++; return now('the file'); } }, 0, () => {});
assert.strictEqual(blobbed, 1, 'a response with no body did not fall back to reading it whole');
whole.then((b) => assert.strictEqual(b, 'the file', 'the fallback did not hand back what blob() gave'));

fs.rmSync(OUT, { force: true });
say('tools: ok');
