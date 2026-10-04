/**
 * THE DASHBOARD'S DRAWING LOOPS STOP WHILE ANOTHER PAGE IS SHOWN (2026-10-04).
 *
 * The traffic graph's keepalive stopped only for a hidden tab and the Network
 * Flow loop never stopped, so together they cost about 380 of the 430 ms the
 * browser spent each second on a page that was not the Dashboard. A loop that
 * merely returns early still costs a frame every refresh, so "stopped" here
 * means it no longer schedules itself.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

// A frame queue the test advances by hand.
let queue = new Map<number, (t: number) => void>();
let nextId = 1;
const cancelled: number[] = [];
(globalThis as any).requestAnimationFrame = (cb: (t: number) => void) => { const id = nextId++; queue.set(id, cb); return id; };
(globalThis as any).cancelAnimationFrame = (id: number) => { cancelled.push(id); queue.delete(id); };
const runFrames = (n: number) => {
  for (let i = 0; i < n; i++) {
    const q = queue; queue = new Map();
    q.forEach((cb) => cb(performance.now()));
  }
};

const doc = makeDoc([]);
doc.hidden = false;
doc.body = doc.createElement();
(globalThis as any).document = doc;

const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'dashboard-traffic-pause.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic.ts'),
   '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const traffic = require(OUT);

let failed = 0;
function check(what: string, fn: () => void) {
  try { fn(); say('  ok   ' + what); }
  catch (e: any) { failed++; say('  FAIL ' + what + '\n       ' + e.message); }
}

say('the traffic graph loop runs only while the Dashboard is shown');

check('shown, the loop keeps scheduling itself', () => {
  traffic.setTrafficShown(true);
  assert.equal(queue.size, 1, 'showing the Dashboard did not start the loop');
  runFrames(5);
  assert.equal(queue.size, 1, 'the loop stopped by itself while shown');
});

check('another page stops it: no frame is left scheduled', () => {
  traffic.setTrafficShown(false);
  assert.equal(queue.size, 0,
    'a frame is still scheduled after leaving the Dashboard, so the browser keeps ' +
    'producing frames for a chart nobody can see');
  assert.ok(cancelled.length > 0, 'the pending frame was not cancelled');
  runFrames(3);
  assert.equal(queue.size, 0, 'the loop restarted itself on another page');
});

check('a frame already in flight when the page changes does not reschedule', () => {
  traffic.setTrafficShown(true);
  const inFlight = [...queue.values()];
  traffic.setTrafficShown(false);
  inFlight.forEach((cb) => cb(performance.now()));
  assert.equal(queue.size, 0, 'a frame that fired after the page change scheduled another');
});

check('returning to the Dashboard starts it again', () => {
  traffic.setTrafficShown(true);
  assert.equal(queue.size, 1, 'the loop did not restart on returning to the Dashboard');
  traffic.setTrafficShown(true);
  assert.equal(queue.size, 1, 'showing the Dashboard twice started a second loop');
});

// ── THE SAMPLE HANDLER MUST NOT RESTART IT, AND THE OTHER LOOP AND THE WIRING
//
// Source-level: `flushTraffic` needs a Chart instance and the Network Flow loop
// a real SVG, neither of which the shim has.
check('a sample arriving on another page does not restart the traffic loop', () => {
  const src = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic.ts'), 'utf8');
  const flush = src.slice(src.indexOf('function flushTraffic('));
  assert.ok(/if \(!keepaliveId && shown\) keepaliveTick\(\);/.test(flush),
    'flushTraffic restarts the loop whatever page is shown, so every sample brings it back');
});

check('the Network Flow loop stops scheduling itself, and wakes on return', () => {
  const src = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard-card-netflow.ts'), 'utf8');
  const frame = src.slice(src.indexOf('function frame('), src.indexOf('function update('));
  const stop = frame.indexOf('if (!onScreen) { running = false; return; }');
  const schedule = frame.indexOf('requestAnimationFrame(frame);');
  assert.ok(stop >= 0, 'the frame loop does not stop while another page is shown');
  assert.ok(stop < schedule, 'the frame loop schedules the next frame before checking it is shown');
  assert.ok(/onScreen = isShown;\s*if \(onScreen\) wake\?\.\(\);/.test(src),
    'showing the Dashboard does not wake the stopped loop');
});

check('the Dashboard tells both loops on every page change', () => {
  const src = fs.readFileSync(path.join(ROOT, 'web', 'src', 'pages', 'dashboard.ts'), 'utf8');
  const at = src.indexOf("addEventListener('mikrodash:pagechange'");
  assert.ok(at >= 0, 'the Dashboard does not listen for page changes');
  const body = src.slice(at, at + 400);
  assert.ok(/setTrafficShown\(isShown\)/.test(body), 'the traffic loop is not told');
  assert.ok(/setNetFlowShown\(isShown\)/.test(body), 'the Network Flow loop is not told');
});

fs.rmSync(OUT, { force: true });
if (failed) { say(failed + ' failed'); process.exit(1); }
say('dashboard-loops-pause: all checks passed');
