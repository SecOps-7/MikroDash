/**
 * THE TOOLS PAGE'S CARDS AND ROUTE MAP, BY THEIR PURE PARTS (2026-09-19).
 *
 * - The ping Score is the E-model R-factor scaled to 100: worked examples, and
 *   the step past 160 ms of effective delay where each millisecond costs more.
 * - Jitter is the mean absolute difference between consecutive replies.
 * - The speed gauge's scale is the next 1-2-5 step at or above the peak.
 * - The route plan: the router, then each hop that answered, in order; a hop
 *   the database cannot place sits where the route already is and draws no
 *   line; a timed-out hop and the hop still being probed are not planned; and a
 *   longer table only ever appends steps.
 * - The view fits the route at 2:1 and never zooms tighter than a region.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const say = console.log.bind(console);
const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const ENTRY = path.join(ROOT, 'testdata', '.tools-cards-entry.ts');
fs.writeFileSync(ENTRY, [
  "export { pingScore, jitterOf, pingCardValues, scoreGrade } from '../web/src/pages/tools-ping-cards.js';",
  "export { niceMax } from '../web/src/pages/tools-btest-cards.js';",
  "export { planTrace, fitBox } from '../web/src/pages/tools-trace-map.js';",
].join('\n') + '\n');
const OUT = path.join(ROOT, 'testdata', '.tools-cards.cjs');
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [ENTRY, '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
fs.rmSync(ENTRY, { force: true });
const m = require(OUT);

// ── THE SCORE ────────────────────────────────────────────────────────────────
// 20 ms, 2 ms jitter, no loss: eff 34, R 92.35, 99.
assert.strictEqual(m.pingScore(20, 2, 0), 99, 'a good link does not score 99');
// The same with 5% loss: R 79.85, 86.
assert.strictEqual(m.pingScore(20, 2, 5), 86, 'each percent of loss does not cost 2.5 of R');
// Past 160 ms of effective delay the cost steepens: 190 ms is eff 200, R 85.2
// (91), not the 88.2 (95) the gentle slope would give.
assert.strictEqual(m.pingScore(190, 0, 0), 91, 'the E-model does not steepen past 160 ms');
assert.strictEqual(m.pingScore(5, 0, 100), 0, 'total loss does not floor at 0');
assert.strictEqual(m.pingScore(0, 0, 0), 100, 'a perfect link does not read 100');
assert.deepStrictEqual([m.scoreGrade(80), m.scoreGrade(79), m.scoreGrade(60), m.scoreGrade(59)],
  ['good', 'fair', 'fair', 'poor'], 'the grades moved');

// ── JITTER ───────────────────────────────────────────────────────────────────
assert.strictEqual(m.jitterOf([10, 14, 12, 12]), 2, 'jitter is not the mean consecutive difference');
assert.strictEqual(m.jitterOf([7]), 0, 'one reply has jitter');

// ── THE CARDS' VALUES ────────────────────────────────────────────────────────
const reply = (seq: number, rtt: number | null) => ({ seq, host: 'h', status: rtt == null ? 'timeout' : '', rttMs: rtt, ttl: 64, size: 56 });
const v = m.pingCardValues({ address: 'a', sent: 4, received: 3, lossPct: 25, minMs: 10, avgMs: 12, maxMs: 14,
  replies: [reply(0, 10), reply(1, 14), reply(2, null), reply(3, 12)] });
assert.deepStrictEqual([v.lastMs, v.minMs, v.maxMs, v.lossPct, v.spark], [12, 10, 14, 25, [10, 14, 12]],
  'the cards do not read the run: ' + JSON.stringify(v));
assert.strictEqual(v.score, m.pingScore(12, 3, 25), 'the Score is not computed from the run');
const none = m.pingCardValues({ address: 'a', sent: 0, received: 0, lossPct: 0, minMs: null, avgMs: null, maxMs: null, replies: [] });
assert.ok(none.score === null && none.lossPct === null, 'nothing sent is not all dashes');
const lost = m.pingCardValues({ address: 'a', sent: 3, received: 0, lossPct: 100, minMs: null, avgMs: null, maxMs: null,
  replies: [reply(0, null)] });
assert.strictEqual(lost.score, 0, 'sent and nothing back does not score 0');

// ── THE GAUGE'S SCALE ────────────────────────────────────────────────────────
assert.deepStrictEqual([0, 0.4, 1, 1.2, 3, 7, 12, 480, 940].map((x) => m.niceMax(x)), [1, 0.5, 1, 2, 5, 10, 20, 500, 1000],
  'the scale is not the next 1-2-5 step');

// ── THE ROUTE PLAN ───────────────────────────────────────────────────────────
const hop = (n: number, extra: Record<string, unknown>) => ({ hop: n, address: '', timedOut: false, lossPct: 0,
  lastMs: 1, bestMs: 1, worstMs: 1, status: '', country: '', city: '', lat: null, lon: null, ...extra });
const route = {
  address: '198.51.100.9', error: '', origin: { lat: 52.37, lon: 4.9, label: 'Amsterdam' },
  hops: [
    hop(1, { address: '192.168.88.1' }),                                             // private: at the router
    hop(2, { timedOut: true }),                                                       // no answer: not drawn
    hop(3, { address: '198.51.100.7', country: 'NL', lat: 52.37, lon: 4.9 }),         // the same place: no line
    hop(4, { address: '198.51.100.8', country: 'DE', lat: 50.11, lon: 8.68 }),        // Frankfurt: a line
    hop(5, { address: '' }),                                                          // still being probed
  ],
};
const steps = m.planTrace(route);
assert.deepStrictEqual(steps.map((s: { hop: number }) => s.hop), [0, 1, 3, 4],
  'the plan is not the router then each answering hop: ' + JSON.stringify(steps.map((s: { hop: number }) => s.hop)));
assert.ok(steps[1].located === false && steps[1].from === null, 'a private hop drew a line or claimed a place');
assert.deepStrictEqual(steps[1].at, steps[0].at, 'a private hop is not at the router');
assert.strictEqual(steps[2].from, null, 'a hop in the same place drew a line');
assert.deepStrictEqual(steps[3].from, steps[2].at, 'Frankfurt\'s line does not start at the hop before it');
// Append-only: the next frame (hop 5 answered) keeps every step and adds one.
const next = m.planTrace({ ...route, hops: [...route.hops.slice(0, 4),
  hop(5, { address: '198.51.100.9', country: 'US', lat: 37.4, lon: -122.1 })] });
assert.deepStrictEqual(next.slice(0, steps.length), steps, 'a longer table rewrote steps already drawn');
assert.strictEqual(next.length, steps.length + 1, 'the answered hop was not appended');
// No origin: nothing before the first placed hop, which then draws no line.
const bare = m.planTrace({ ...route, origin: null });
assert.deepStrictEqual(bare.map((s: { hop: number }) => s.hop), [3, 4], 'with no origin a private hop was placed');
assert.strictEqual(bare[0].from, null, 'the first placed hop drew a line from nowhere');

// ── THE VIEW ─────────────────────────────────────────────────────────────────
const world = m.fitBox([]);
assert.deepStrictEqual(world, { x: 0, y: 0, w: 1000, h: 500 }, 'no points is not the whole world');
const one = m.fitBox([[500, 150]]);
assert.ok(one.w === 60 && one.h === 30, 'one point zooms tighter than a region: ' + JSON.stringify(one));
const wide = m.fitBox([[100, 200], [700, 210]]);
assert.ok(Math.abs(wide.w / wide.h - 2) < 1e-9 && wide.x >= 0 && wide.x + wide.w <= 1000,
  'the view is not 2:1 inside the map: ' + JSON.stringify(wide));
assert.deepStrictEqual(m.fitBox([[10, 10], [990, 490]]), world, 'a route across the world does not show all of it');

fs.rmSync(OUT, { force: true });
say('tools-cards: ok');
