/**
 * A RETURNING TAB'S TRAFFIC CHART IS CURRENT BEFORE IT IS SEEN.
 *
 * ── WHAT THIS REPLACED ──────────────────────────────────────────────────────
 *
 * The chart used to be hidden (opacity 0) whenever the page was reported
 * hidden, and faded back in on the next sample, to cover the axis catching up
 * after the keepalive had been stopped. It blanked the chart for up to a second
 * every time, a taskbar click included. Samples are buffered while hidden, so
 * `resumeTrafficChart` rebuilds the chart at the current time instead, from the
 * handler that reports the page visible.
 *
 * Three things are pinned, each against a plausible wrong implementation:
 *
 *   A LONG HIDE    samples that arrived while hidden are drawn on resume, and
 *                  nothing ever set the canvas to opacity 0.
 *   A BRIEF HIDE   the taskbar case: the gap is under the full-redraw threshold,
 *                  so only resume's own redraw moves the axis to now. Without it
 *                  the first frame back shows the axis where it was left.
 *   NOT RUNNING    with the socket disconnected, or the page still hidden, resume
 *                  touches nothing, so a dead connection's chart is not scrolled
 *                  away from its last data.
 */
import assert from 'node:assert';
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');

const ENTRY = path.join(ROOT, 'testdata', '.traffic-resume-entry.ts');
fs.writeFileSync(ENTRY,
  "export { onTrafficHistory, noteTrafficUpdate, resumeTrafficChart, sharedClock, sharedPoints } from '../web/src/pages/dashboard-traffic.js';\n" +
  "export { RIGHT_BUFFER_MS, rightBufferFor, seedPoints, needsFullRedraw, KEEPALIVE_SLACK_MS, REDRAW_GAP_MS } from '../web/src/pages/dashboard-traffic-buffer.js';\n" +
  "export { fmtMbps } from '../web/src/dom.js';\n");
const OUT = path.join(ROOT, 'testdata', '.traffic-resume.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });

// ── the shim ────────────────────────────────────────────────────────────────
const g = globalThis as any;
const els = new Map<string, any>();
function fakeEl(id: string): any {
  return {
    id, style: {} as Record<string, string>, value: '', options: [], _t: '',
    set textContent(v: unknown) { this._t = String(v); },
    get textContent() { return this._t; },
    classList: { add() {}, remove() {}, toggle() {}, contains: () => false },
    addEventListener() {}, closest: () => null, querySelector: () => null,
    querySelectorAll: () => [], getAttribute: () => null, setAttribute() {},
  };
}
let disconnected = false;
g.document = {
  hidden: false,
  body: { classList: { contains: (c: string) => c === 'is-disconnected' && disconnected } },
  getElementById: (id: string) => { if (!els.has(id)) els.set(id, fakeEl(id)); return els.get(id); },
  addEventListener() {}, querySelector: () => null, querySelectorAll: () => [],
};
g.window = { devicePixelRatio: 1, addEventListener() {} };
g.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };

// Frames are queued and run only when drained, so "hidden" can mean what it
// means in a browser: the flush a sample books does not run until asked.
const frames = new Map<number, () => void>();
let nextFrame = 1;
const cancelled: number[] = [];
g.requestAnimationFrame = (fn: () => void) => { const id = nextFrame++; frames.set(id, fn); return id; };
g.cancelAnimationFrame = (id: number) => { cancelled.push(id); frames.delete(id); };
const drain = () => { const due = [...frames.values()]; frames.clear(); due.forEach((f) => f()); };

// A clock the test moves, so "time passed while hidden" is exact.
let clock = 1_800_000_000_000;
Date.now = () => clock;

const charts: any[] = [];
g.Chart = class {
  data: any; options: any; updates = 0;
  constructor(_canvas: unknown, cfg: any) { this.data = cfg.data; this.options = cfg.options; charts.push(this); }
  update() { this.updates++; }
  destroy() {}
};

const m = require(OUT);
fs.rmSync(OUT, { force: true });

const sample = (ts: number, rx: number, tx: number) => ({ ifName: 'ether1', ts, rx_mbps: rx, tx_mbps: tx });
const lastX = (c: any, i: number) => c.data.datasets[i].data[c.data.datasets[i].data.length - 1];
// THE SAME GAP THE CHART USES, not the old fixed constant. The right buffer is
// measured from the samples now (a router in stream mode does not deliver on a
// metronome), so a test that kept computing it as RIGHT_BUFFER_MS would be
// asserting against a formula the code no longer uses - and would fail on any
// fixture whose gaps are not exactly one second.
const expectedMax = () =>
  Date.now() + m.sharedClock().serverOffset - m.rightBufferFor(m.sharedPoints());

// ── a live chart ────────────────────────────────────────────────────────────
m.onTrafficHistory({ ifName: 'ether1', points: [
  { ts: clock - 3000, rx_mbps: 1, tx_mbps: 1 },
  { ts: clock - 2000, rx_mbps: 1, tx_mbps: 1 },
] });
const chart = charts[charts.length - 1];
assert.ok(chart, 'no chart was built from the history');
clock += 1000;
m.noteTrafficUpdate(sample(clock, 2, 2));
drain();
const canvas = els.get('trafficChart');

// ── 1. a long hide ──────────────────────────────────────────────────────────
{
  g.document.hidden = true;
  for (let i = 0; i < 5; i++) {
    clock += 1000;
    m.noteTrafficUpdate(sample(clock, 5, 6));
  }
  assert.ok(frames.size > 0, 'a sample while hidden should book a flush that has not run');
  assert.notStrictEqual(canvas.style.opacity, '0', 'the canvas was hidden on the way out');

  clock += 300;
  g.document.hidden = false;
  const before = chart.updates;
  cancelled.length = 0;
  m.resumeTrafficChart();

  assert.ok(cancelled.length > 0, 'the queued flush was not cancelled; it would apply the sample twice');
  assert.deepStrictEqual(lastX(chart, 0), { x: clock - 300, y: 5 },
    'RX does not end at the newest sample that arrived while hidden');
  assert.deepStrictEqual(lastX(chart, 1), { x: clock - 300, y: 6 },
    'TX does not end at the newest sample that arrived while hidden');
  assert.strictEqual(els.get('liveRx').textContent, m.fmtMbps(5), 'the live RX figure is stale');
  assert.ok(chart.updates > before, 'the chart was not redrawn');
  assert.ok(Math.abs(chart.options.scales.x.max - expectedMax()) < 1,
    'the axis is not at the current time: ' + (chart.options.scales.x.max - expectedMax()) + ' ms off');
  assert.notStrictEqual(canvas.style.opacity, '0', 'the canvas is hidden after resume');
  say('ok  after a long hide the chart is redrawn at now with every buffered sample');
}

// ── 2. a brief hide, under the full-redraw threshold ────────────────────────
{
  drain();
  g.document.hidden = true;
  clock += 800;
  m.noteTrafficUpdate(sample(clock, 7, 8));
  clock += 900;
  g.document.hidden = false;
  m.resumeTrafficChart();

  assert.deepStrictEqual(lastX(chart, 0), { x: clock - 900, y: 7 }, 'the sample from the brief hide is missing');
  assert.ok(Math.abs(chart.options.scales.x.max - expectedMax()) < 1,
    'after a brief hide the axis stayed where it was left: ' +
    (chart.options.scales.x.max - expectedMax()) + ' ms behind now');
  say('ok  after a brief hide the axis is moved to now at once, not on the next frame');
}

// ── 3. not while it should not run ──────────────────────────────────────────
{
  drain();
  for (const [label, setUp, tearDown] of [
    ['the socket disconnected', () => { disconnected = true; }, () => { disconnected = false; }],
    ['the page still hidden', () => { g.document.hidden = true; }, () => { g.document.hidden = false; }],
  ] as Array<[string, () => void, () => void]>) {
    clock += 1000;
    m.noteTrafficUpdate(sample(clock, 9, 9));
    setUp();
    const updates = chart.updates, max = chart.options.scales.x.max, end = lastX(chart, 0);
    clock += 5000;
    m.resumeTrafficChart();
    assert.strictEqual(chart.updates, updates, 'resume redrew the chart with ' + label);
    assert.strictEqual(chart.options.scales.x.max, max, 'resume moved the axis with ' + label);
    assert.deepStrictEqual(lastX(chart, 0), end, 'resume applied a sample with ' + label);
    tearDown();
    frames.clear();
  }
  say('ok  resume does nothing while the socket is down or the page is still hidden');
}

// ── 4. a rebuild reaches the left edge (the operator, 2026-11-22) ──────────
//
// A rebuild kept only the points inside the window, cut on the browser's clock,
// so the line began short of the left edge: a strip measured at 266 to 967 ms
// after every rebuild on the hAP ax^3. It now keeps the axis's left edge on the
// server's clock plus the keepalive's slack, so the first point drawn is at or
// left of the edge.
{
  drain();
  // Two minutes of history, a point a second, ending now; then a rebuild.
  const pts = [];
  for (let i = 120; i >= 0; i--) pts.push({ ts: clock - i * 1000, rx_mbps: 1, tx_mbps: 1 });
  m.onTrafficHistory({ ifName: 'ether1', points: pts });
  for (const label of ['the history', 'a resume']) {
    if (label === 'a resume') { clock += 400; m.resumeTrafficChart(); }
    const c = charts[charts.length - 1];
    const first = c.data.datasets[0].data[0];
    const min = c.options.scales.x.min;
    assert.ok(first.x <= min, label + ': the line starts ' + (first.x - min) + ' ms right of the left edge');
    assert.ok(first.x >= min - m.KEEPALIVE_SLACK_MS - 1000,
      label + ': kept a point the keepalive would drop at once (' + (min - first.x) + ' ms left of the edge)');
  }

  // The cut is the axis's, whatever the browser's clock says: with the server
  // four seconds behind the browser, the first point is still at the edge.
  const cut = m.seedPoints(pts, clock - 60_000 - 4000);
  assert.ok(cut[0].ts <= clock - 64_000, 'seedPoints did not keep the point left of the edge');
  assert.ok(cut[0].ts >= clock - 64_000 - m.KEEPALIVE_SLACK_MS, 'seedPoints kept more than the slack');
  // One out-of-order sample does not truncate what came before it.
  const shuffled = [...pts.slice(0, 100), { ts: clock - 200_000, rx_mbps: 0, tx_mbps: 0 }, ...pts.slice(100)];
  assert.strictEqual(m.seedPoints(shuffled, clock - 10_000).length, m.seedPoints(pts, clock - 10_000).length,
    'an out-of-order sample cut the points after it');

  // A late sample in stream mode (2.0 to 2.8 s measured) is appended, not a
  // reason to rebuild; a real break still is.
  const drawn = [{ x: clock, y: 1 }];
  assert.strictEqual(m.needsFullRedraw(drawn, clock + 2800), false, 'a sample 2.8 s late forced a rebuild');
  assert.strictEqual(m.needsFullRedraw(drawn, clock + m.REDRAW_GAP_MS + 1), true, 'a real break did not rebuild');
  assert.strictEqual(m.needsFullRedraw([], clock), true, 'an empty chart did not rebuild');
  say('ok  a rebuild reaches the left edge on the server clock, and a late sample does not rebuild');
}

say('traffic-resume: all checks passed');
