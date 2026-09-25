/**
 * THE CONNECTIONS CARD'S SPARKLINE IS PER ROUTER (2026-09-25).
 *
 * ── THE BUG, IN THE OPERATOR'S WORDS ───────────────────────────────────────
 *
 * "when I switch to another router with no connections, it goes flat, and when
 * I return to my primary Router, its still flat because it was changed."
 *
 * Two defects made that one symptom. The history was a single array that the
 * router switch EMPTIED along with the fingerprint caches, so the old router's
 * shape was destroyed rather than set aside. And `drawSparkline` returned early
 * when it had fewer than two points WITHOUT clearing the canvas, so what stayed
 * on screen was the other router's line.
 *
 * The fingerprint caches must still be forgotten on a switch - two routers can
 * agree on their top talker and its count, and the card would keep the wrong
 * rows. This pins the distinction: caches forgotten, history kept.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';
import { makeDoc } from './dom-shim.js';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.connspark-entry.ts');
fs.writeFileSync(ENTRY, "export * from '../web/src/pages/dashboard-conn.js';\n");
const OUT = path.join(ROOT, 'testdata', '.connspark.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const mod = require(OUT);

const doc = makeDoc(['connTotal', 'connSparkCanvas', 'protoBars',
  'connSrcList', 'connDstList', 'connCountryList', 'connPortList', 'connProcessed']);
global.document = doc;
// The flush is deferred to a frame that never arrives here: the sparkline is
// drawn synchronously by the handler, which is what this gate is about.
global.requestAnimationFrame = () => 0;

// ── A CANVAS THAT RECORDS RATHER THAN PAINTS ──────────────────────────────
//
// The shim has no canvas, so one is attached. Recording the calls is what lets
// a clear be told apart from a draw - the distinction the second defect turned
// on, and one no assertion about "the history" could make.
const canvas = doc.nodes['connSparkCanvas'];
canvas.width = 120;
canvas.height = 24;
let ops: string[] = [];
canvas.getContext = () => ({
  clearRect: () => ops.push('clear'),
  beginPath: () => ops.push('begin'),
  moveTo: () => ops.push('point'),
  lineTo: () => ops.push('point'),
  stroke: () => ops.push('stroke'),
  strokeStyle: '', lineWidth: 0, lineJoin: '',
});

const update = (total: number) => mod.noteConnUpdate({
  ts: Date.now(), total, processed: total, processingCapped: false,
  protoCounts: { tcp: total, udp: 0, icmp: 0, other: 0 },
  topSources: [], topDestinations: [], topCountries: [], topPorts: [], pollMs: 3000,
});
const points = () => ops.filter((o) => o === 'point').length;
const drew = () => ops.includes('stroke');
const cleared = () => ops.includes('clear');
const reset = () => { ops = []; };

// ── ROUTER A BUILDS A SHAPE ───────────────────────────────────────────────
mod.setConnRouter('routerA');
update(10); update(40); update(25);
reset();
update(30);
assert.ok(drew(), 'four samples on routerA must draw a line');
assert.strictEqual(points(), 4, 'the line must have one point per sample');

// ── SWITCHING TO AN IDLE ROUTER CLEARS, AND DRAWS NOTHING ─────────────────
//
// This is the first half of the report. A router with no connections has no
// history yet, and the canvas must be emptied rather than left showing the
// previous router's line.
reset();
mod.setConnRouter('routerB');
assert.ok(cleared(), 'switching to a router with no history must CLEAR the canvas');
assert.ok(!drew(), 'a router with no history must not draw a line');

reset();
update(0);
assert.ok(!drew(), 'one sample is not a line');
reset();
update(0);
assert.ok(drew(), 'two flat samples draw a flat line');
assert.strictEqual(points(), 2, 'routerB has two samples of its own, not routerA\'s four');

// ── COMING BACK RESTORES THE SHAPE. THE WHOLE POINT. ──────────────────────
reset();
mod.setConnRouter('routerA');
assert.ok(drew(), 'returning to routerA must redraw ITS history, not start from nothing');
assert.strictEqual(points(), 4,
  'routerA kept its four samples while routerB was showing; before the fix the switch ' +
  'emptied the one shared array and this was 0');

// The next sample continues that history rather than beginning a new one.
reset();
update(35);
assert.strictEqual(points(), 5, 'the fifth sample extends routerA\'s line');

// ── A REPEAT IS A RECONNECT, NOT A SWITCH ─────────────────────────────────
//
// `router:active` fires on connect, switch and hot-swap alike and both paths
// call this, so the same id arriving twice must leave the card alone.
reset();
mod.setConnRouter('routerA');
assert.strictEqual(ops.length, 0,
  'setting the router already showing must do nothing at all - a reconnect is not a switch');

// ── AND THE FIRST ROUTER IS KEYED, NOT FILED UNDER NOTHING ───────────────
reset();
mod.setConnRouter('');
assert.ok(cleared(), 'an empty id is still a switch away from routerA, so the canvas clears');
reset();
mod.setConnRouter('routerA');
assert.strictEqual(points(), 5, 'routerA survives a trip through the empty id');

fs.rmSync(OUT, { force: true });
console.log('dashboard-conn-spark: ok');
