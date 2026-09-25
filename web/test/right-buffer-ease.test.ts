/**
 * THE AXIS EDGE NEVER REVERSES AND NEVER STALLS (2026-09-25).
 *
 * ── THE BUG, IN THE OPERATOR'S WORDS ───────────────────────────────────────
 *
 * "sometimes i see the graph tick slightly backwards, left to right for a
 * moment and then continues flowing as normal right to left."
 *
 * `rightBufferFor` is a 90th percentile over a TRAILING window of gaps, so it
 * is a step function: a slow sample enters the window and the quantile jumps,
 * twenty samples later it leaves and the quantile drops. The edge is
 * `anchor - rb`, so each step moved the whole window backwards in time and a
 * stationary point slid RIGHT across the screen.
 *
 * ── WHAT IS ACTUALLY ASSERTED ──────────────────────────────────────────────
 *
 * Not "the easing looks smooth", which is unfalsifiable, but the INEQUALITY the
 * bound exists to guarantee:
 *
 *     edge velocity = 1 - d(rb)/dt   must stay strictly positive
 *
 * A clamp on the edge would satisfy "never reverses" and fail "never stalls",
 * and the operator asked for both: "I dont want it to pause ot stop briefly. It
 * must never stop moving." So the test drives the worst step the clamp allows
 * and checks the edge advances on EVERY frame.
 */

import fs from 'node:fs';
import path from 'node:path';
import assert from 'node:assert';
import { execFileSync } from 'node:child_process';

const ROOT = process.env.MIKRODASH_ROOT || path.join(__dirname, '..', '..');
const OUT = path.join(ROOT, 'web', 'dist', '_compare', 'port-buffer-ease.cjs');
fs.mkdirSync(path.dirname(OUT), { recursive: true });
execFileSync(path.join(ROOT, 'web', 'node_modules', '.bin', 'esbuild'),
  [path.join(ROOT, 'web', 'src', 'pages', 'dashboard-traffic-buffer.ts'),
    '--bundle', '--format=cjs', '--platform=node', '--outfile=' + OUT, '--log-level=warning'],
  { stdio: 'inherit' });
const m = require(OUT);

// ── THE GUARANTEE, DRIVEN AT THE WORST STEP THE CLAMP PERMITS ─────────────
//
// `rightBufferFor` is bounded to [1000, 2500], so the largest jump it can ask
// for is 1,500 ms in a single frame. That is the input to beat.
{
  const FRAME = 16; // ms, a 60 Hz keepalive
  let rb = 1000;
  let anchor = 1790000000000;
  let edge = anchor - rb;
  let frames = 0;
  let minStep = Infinity;

  // Ask for the maximum jump, then hold it there long enough to settle.
  for (let i = 0; i < 1200; i++) {
    anchor += FRAME;
    rb = m.easeBuffer(rb, 2500, FRAME);
    const next = anchor - rb;
    const step = next - edge;
    if (step < minStep) minStep = step;
    assert.ok(step > 0,
      `frame ${i}: the edge moved by ${step}ms - it must never reverse and never stall`);
    edge = next;
    frames++;
  }
  assert.ok(rb > 2499, `the buffer never reached its target: ${rb}`);
  assert.ok(minStep > 0, `the smallest step was ${minStep}ms`);
  // The bound is 25%, so the slowest frame must still be at least 75% of real
  // time. This is what makes it a change of PACE rather than a stutter.
  assert.ok(minStep >= FRAME * 0.74,
    `the edge slowed to ${(minStep / FRAME * 100).toFixed(0)}% of real time, below the 75% bound`);
  console.log(`  worst case: edge never below ${(minStep / FRAME * 100).toFixed(0)}% of real time`);
}

// ── THE OPPOSITE DIRECTION MUST NOT LURCH EITHER ──────────────────────────
//
// A shrinking buffer cannot reverse the edge - it speeds it up - but an
// unbounded shrink throws it FORWARD by the whole step, which is just as
// visible. The same bound caps it at 125%.
{
  const FRAME = 16;
  let rb = 2500, anchor = 1790000000000, edge = anchor - rb, maxStep = 0;
  for (let i = 0; i < 1200; i++) {
    anchor += FRAME;
    rb = m.easeBuffer(rb, 1000, FRAME);
    const next = anchor - rb;
    maxStep = Math.max(maxStep, next - edge);
    edge = next;
  }
  assert.ok(rb < 1001, `the buffer never came back down: ${rb}`);
  assert.ok(maxStep <= FRAME * 1.26,
    `the edge lurched to ${(maxStep / FRAME * 100).toFixed(0)}% of real time, above the 125% bound`);
}

// ── A STEP FUNCTION FED STRAIGHT IN IS THE BUG, AND IT IS SHOWN ───────────
//
// The control: without the easing, the same jump reverses the edge. If this
// did not reverse, the test above would be proving nothing.
{
  const FRAME = 16;
  let anchor = 1790000000000;
  const edgeBefore = anchor - 1000;
  anchor += FRAME;
  const edgeAfter = anchor - 2500;   // the raw quantile, taken as-is
  assert.ok(edgeAfter < edgeBefore,
    'the unwrapped step should move the edge backwards - if it does not, this test ' +
    'is not exercising the defect it claims to');
}

// ── DEGENERATE CLOCKS MUST NOT SPEND A BUDGET ─────────────────────────────
//
// A first frame, a resumed tab or a clock that went backwards. No elapsed time
// is no budget; a negative delta is not a licence to move the other way.
assert.strictEqual(m.easeBuffer(1000, 2500, 0), 1000, 'a zero delta moved the buffer');
assert.strictEqual(m.easeBuffer(1000, 2500, -5000), 1000, 'a negative delta moved the buffer');
assert.strictEqual(m.easeBuffer(1000, 2500, NaN), 1000, 'NaN moved the buffer');
// A huge delta - a tab hidden for a minute - is capped, so the edge does not
// jump a second and a half in one frame on return.
assert.ok(m.easeBuffer(1000, 2500, 600000) <= 1250 + 0.001,
  'a long gap spent an unbounded budget in one frame');

// Reaching the target exactly, rather than oscillating around it.
assert.strictEqual(m.easeBuffer(1000, 1010, 1000), 1010, 'a small delta did not settle exactly');

console.log('right-buffer-ease: ok');
