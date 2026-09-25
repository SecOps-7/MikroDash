// The traffic chart's sample buffer and its shared clock.
//
// ── WHY THIS IS ITS OWN FILE, AHEAD OF THE CHART ────────────────────────────
//
// The Dashboard's chart is the last card, and the reason it was left until last
// is that it does not own its state: `allPoints`, `_lastSampleTs` and the
// EMA-smoothed `_serverOffset` are read by the BANDWIDTH page too, which anchors
// its own X axis to them. Everything in this file is the part of that which is
// pure - buffer in, numbers out, no Chart.js and no DOM - so it can be pinned
// against the live formulas exactly before any drawing code depends on it.
//
// ── THE CLOCK IS SMOOTHED BECAUSE ARRIVAL TIME IS NOISY ─────────────────────
//
// Each sample carries the server's timestamp and arrives a few hundred
// milliseconds later than the last, jittering. The raw offset `ts - now` is
// therefore noisy, and driving an axis from it directly makes the chart swing
// back and forth. An EMA at 0.1 smooths it.
//
// ── AND THE SEED TEST IS `FALSY`, WHICH IS A QUIRK WORTH KEEPING ────────────
//
// `_serverOffset = _serverOffset ? smoothed : raw` re-seeds whenever the current
// offset is exactly 0 - which is not only the initial state: an offset that
// smooths its way to precisely zero, or a client whose clock agrees with the
// router's to the millisecond, lands there too and takes the seed branch again.
// The effect is invisible (re-seeding to a raw offset near zero) but it is what
// the live app does, and a port that used `=== null` would diverge on a case a
// fixture will never contain.

import type { TrafficPoint } from '../gen/payloads';

export interface XYPoint { x: number; y: number }

/** 30 min at 1 Hz - matches the server's HISTORY_MINUTES default. */
export const MAX_CLIENT_POINTS = 1800;

/** Append a sample, holding the buffer at its cap. Mutates, as the original does. */
export function pushSample(points: TrafficPoint[], sample: TrafficPoint): void {
  points.push({ ts: sample.ts, rx_mbps: sample.rx_mbps, tx_mbps: sample.tx_mbps });
  if (points.length > MAX_CLIENT_POINTS) points.shift();
}

/** The buffer trimmed to what the visible window covers. */
export function windowedPoints(
  points: readonly TrafficPoint[], nowMs: number, windowSecs: number, rightBufferMs: number,
): TrafficPoint[] {
  const cutoff = nowMs - (windowSecs * 1000) - rightBufferMs;
  // A FORWARD FILTER, following the live app's fix.
  //
  // It used to walk backwards and `break` at the first point older than the
  // cutoff - same result on a monotonic buffer and cheaper, but ONE out-of-order
  // sample truncated everything before it. Reachable two ways: `traffic:history`
  // loads a server-supplied array wholesale, and the timestamps are the ROUTER's,
  // so a clock stepping backwards after NTP corrects a drifted RTC produces one.
  //
  // This port reproduced the quirk deliberately and pinned it with three
  // non-monotonic cases, which is what made the flip safe: reported as ToDo #14
  // on 2026-08-24, fixed there the same day, and the pinned cases turned red
  // the moment it was - which is how the change was noticed rather than
  // discovered later.
  //
  // The live fix scans the whole buffer rather than keeping the early exit. At
  // 1800 points once per frame that was judged acceptable there; matching it
  // matters more than the saving.
  return points.filter((p) => p.ts >= cutoff);
}

/** The EMA that turns a noisy per-sample offset into a usable clock. */
export function smoothOffset(prev: number, rawOffset: number): number {
  return prev ? prev + (rawOffset - prev) * 0.1 : rawOffset;
}

/**
 * Where the right-hand edge of the axis sits.
 *
 * With a sample in hand it is the ESTIMATED SERVER TIME, so the axis keeps
 * advancing between samples; with none it falls back to the last point's
 * timestamp, and to now if there are no points at all. The redraw and the
 * keepalive must use the same formula or the first keepalive frame after a
 * redraw snaps the chart sideways.
 */
export function anchorMs(
  lastSampleTs: number, serverOffset: number, nowMs: number, pts: readonly TrafficPoint[],
): number {
  return lastSampleTs ? nowMs + serverOffset : (pts.length ? pts[pts.length - 1]!.ts : nowMs);
}

/** The X axis for a given anchor. */
export function axisWindow(
  anchor: number, windowSecs: number, rightBufferMs: number,
): { min: number; max: number } {
  return { min: anchor - windowSecs * 1000 - rightBufferMs, max: anchor - rightBufferMs };
}

/**
 * Drop points that have scrolled off the left, and report the tallest that is
 * left. RX and TX are shifted TOGETHER - they are two datasets of one sample
 * stream, and pruning them independently would misalign the pair.
 *
 * The 3-second slack past the visible edge is deliberate: a point is kept a
 * little after it leaves the window so the line does not end in mid-air at the
 * left edge while it animates out.
 */
/**
 * The tail the KEEPALIVE keeps beyond the visible window, in milliseconds.
 *
 * It was written twice as a bare `3000` - once in the prune below and once in
 * the Bandwidth chart's seeding - and the two have to agree or the chart snaps
 * on its first frame. Named so they cannot drift apart.
 */
export const KEEPALIVE_SLACK_MS = 3000;

/**
 * The gap held open at the RIGHT edge, in milliseconds - one sample interval,
 * so the newest point is never drawn flush against the frame.
 *
 * A single global in the live app (`app.js:249`), read by BOTH charts. It was
 * briefly declared twice here, once per chart module, which is the drift this
 * file's other shared constant exists to prevent - so it lives beside it.
 */
export const RIGHT_BUFFER_MS = 1000;

/** How many recent gaps `rightBufferFor` measures, and the bounds it clamps to. */
const BUFFER_SAMPLES = 20;
const BUFFER_MAX_MS = 2500;

/**
 * The gap to hold open at the right edge, measured from the samples themselves.
 *
 * ── WHY THE FIXED 1000 WAS NOT ENOUGH ──────────────────────────────────────
 *
 * `RIGHT_BUFFER_MS` is documented as "one sample interval", which is true when
 * a sample arrives every second. A router in STREAM mode does not keep that
 * promise: measured on the operator's hAP ax^3 (2026-09-23), 55 points over 63
 * seconds with a mean gap of 1,170 ms - 22 gaps near 1,000, 27 near 1,250, four
 * near 1,500 and one of 2,000.
 *
 * With a fixed 1,000 the axis edge runs ahead of the newest sample whenever one
 * arrives late, and the line visibly falls short of the frame - the trailing gap
 * swung between −1,424 ms (drawn past the edge and clipped, as intended) and
 * +535 ms (a visible hole).
 *
 * ── AND WHY A HIGH QUANTILE RATHER THAN THE AVERAGE ─────────────────────────
 *
 * The newest sample's age ranges from nothing, just after one lands, to a whole
 * interval, just before the next. A buffer of the MEAN interval is therefore
 * too small about half the time - the hole would appear half as often instead
 * of going away. Covering the recent worst case is what makes the line reach
 * the edge, so this takes a high quantile of the recent gaps.
 *
 * WHAT IT COSTS, since it is a real trade: the chart shows data up to that far
 * back rather than up to a second back. The clamp bounds it, so one hiccup
 * cannot push the whole chart into the past.
 */
export function rightBufferFor(points: readonly TrafficPoint[]): number {
  const n = points.length;
  if (n < 4) return RIGHT_BUFFER_MS;
  const gaps: number[] = [];
  for (let i = Math.max(1, n - BUFFER_SAMPLES); i < n; i += 1) {
    const a = points[i - 1];
    const b = points[i];
    if (a && b && b.ts > a.ts) gaps.push(b.ts - a.ts);
  }
  if (gaps.length < 3) return RIGHT_BUFFER_MS;
  gaps.sort((x, y) => x - y);
  // The 90th percentile: covers the ordinary jitter without letting a single
  // stall drag the edge back for the next twenty samples.
  const q = gaps[Math.min(gaps.length - 1, Math.floor(gaps.length * 0.9))] || RIGHT_BUFFER_MS;
  return Math.min(BUFFER_MAX_MS, Math.max(RIGHT_BUFFER_MS, q));
}

/**
 * The points the BANDWIDTH chart seeds from, which are NOT the points the
 * dashboard chart seeds from.
 *
 * The two live functions genuinely differ and the difference is exactly this
 * slack. `windowedPoints()` (app.js:774) filters at `ts >= cutoff`;
 * `_syncBwChart` (app.js:6950) walks back to `cutoff - 3000` and keeps three
 * seconds more.
 *
 * That is not an inconsistency to tidy away. The keepalive prunes at
 * `viewLeft - 3000` on BOTH charts, so seeding with the same slack hands the
 * bandwidth chart exactly the set its own keepalive would retain - no point is
 * drawn on the seeding frame and dropped on the next. Reusing `windowedPoints`
 * here would look correct, pass any test written from the dashboard's
 * behaviour, and show up as a one-frame flicker at the left edge that nobody
 * would trace back to a shared helper.
 */
export function bandwidthSeedPoints(
  points: readonly TrafficPoint[], nowMs: number, windowSecs: number, rightBufferMs: number,
): TrafficPoint[] {
  const cutoff = nowMs - (windowSecs * 1000) - rightBufferMs - KEEPALIVE_SLACK_MS;
  // A FILTER, and it became one on 2026-08-25 when the live side fixed its
  // second copy.
  //
  // This walked backwards from the newest point and stopped at the first one
  // older than the cutoff - the same answer on a monotonic buffer and cheaper,
  // but ONE out-of-order sample (an NTP correction on a router with a drifted
  // RTC) truncated everything before it. The live repo had fixed that in
  // `windowedPoints` after this port reported it, and `_syncBwChart` still
  // carried the old shape, so the two copies of one rule disagreed. This port
  // REPRODUCED the quirk rather than tidying it, and reported the second copy as
  // ToDo #24.
  //
  // #24 is fixed now, and the gate said so: `bandwidth-chart-check` went red
  // within minutes of the edit landing in the live working tree - which is the
  // outcome the note that used to sit here predicted, in as many words.
  //
  // The THREE-SECOND SLACK STAYS, and the live fix kept it too, folded into the
  // cutoff rather than into the comparison. It is not part of the bug: the
  // keepalive prunes at `viewLeft - 3000`, so seeding with that slack hands this
  // chart exactly the set its own keepalive would retain. Dropping it would show
  // as a one-frame flicker at the left edge that nobody would trace back here.
  return points.filter((p) => p.ts >= cutoff);
}

export function pruneAndMax(rx: XYPoint[], tx: XYPoint[], viewLeft: number): number {
  while (rx.length > 0 && rx[0]!.x < viewLeft - KEEPALIVE_SLACK_MS) { rx.shift(); tx.shift(); }
  let newMax = 0;
  for (const p of rx) if (p.y > newMax) newMax = p.y;
  for (const p of tx) if (p.y > newMax) newMax = p.y;
  return newMax;
}

/**
 * How fast the right buffer may move, as a FRACTION OF REAL TIME.
 *
 * This is the whole mechanism, so it is worth stating as an inequality. The
 * axis edge is `anchor - rb`, and `anchor` advances with the wall clock, so
 *
 *     edge velocity = 1 - d(rb)/dt
 *
 * Bounding `|d(rb)/dt|` at 0.25 bounds the edge to between 0.75x and 1.25x real
 * time. It can therefore never stop and never reverse - which is the property
 * being bought, and it is a property of the BOUND, not of any particular
 * smoothing curve.
 */
const BUFFER_RATE = 0.25;

/**
 * Ease the right buffer toward its measured target at a bounded rate.
 *
 * ── THE BUG THIS EXISTS FOR ────────────────────────────────────────────────
 *
 * `rightBufferFor` is a 90th percentile over a TRAILING WINDOW of gaps, so it
 * is a step function: when a slow sample enters that window the quantile jumps,
 * and twenty samples later it drops back. Fed straight into the axis, each step
 * moved `min` and `max` together - the whole window slid backwards in time, and
 * a point that had not moved slid RIGHT across the screen. The operator saw the
 * graph tick briefly backwards and then resume.
 *
 * ── WHY RATE-LIMIT THE BUFFER RATHER THAN CLAMP THE EDGE ───────────────────
 *
 * Clamping the edge to be non-decreasing also fixes the reversal, and was the
 * first thing tried. It buys monotonicity at the price of a STALL: while the
 * buffer catches up the edge sits still, and a live graph that stops looks
 * broken in its own way. Bounding the rate keeps the edge moving through the
 * correction, which is the difference between a change of pace and a stutter.
 *
 * ── AND WHY BOTH DIRECTIONS ───────────────────────────────────────────────
 *
 * Only growth can reverse the edge, so only growth has to be limited for
 * correctness. Shrinking is limited too because an unbounded shrink lurches the
 * edge FORWARD by the same amount - not a reversal, but just as visible, and
 * the symmetric rule is one number instead of two cases.
 *
 * `elapsedMs` is passed in rather than read from a clock so this stays pure and
 * the bound can be asserted directly.
 */
export function easeBuffer(current: number, target: number, elapsedMs: number): number {
  // A FIRST FRAME, A RESUMED TAB OR A CLOCK JUMP. With no elapsed time there is
  // no budget to move, and a negative or absurd delta must not be spent as one.
  if (!(elapsedMs > 0)) return current;
  const budget = Math.min(elapsedMs, 1000) * BUFFER_RATE;
  const delta = target - current;
  if (delta > budget) return current + budget;
  if (delta < -budget) return current - budget;
  return target;
}

/**
 * The Y axis easing. `|| 1` keeps an idle interface's axis at 1 Mbps rather
 * than collapsing to zero, which would make noise look like saturation.
 */
export function smoothMax(current: number, target: number): number {
  return current + ((target || 1) - current) * 0.08;
}

/**
 * Whether a sample is too far past the last drawn point to append.
 *
 * A gap means the tab was hidden, the page was elsewhere or the router went
 * away; appending across it draws a straight line through time that never
 * happened, so the chart is rebuilt from the buffer instead. An EMPTY dataset
 * also forces the rebuild - there is nothing to append to.
 */
export function needsFullRedraw(rx: readonly XYPoint[], sampleTs: number): boolean {
  return !rx.length || sampleTs - rx[rx.length - 1]!.x > 2000;
}
