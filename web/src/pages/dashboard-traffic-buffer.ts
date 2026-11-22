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
 * The points a chart is REBUILT from: everything from the axis's left edge,
 * and the keepalive's slack beyond it.
 *
 * ── THE GAP AT THE LEFT EDGE (the operator, 2026-11-22) ─────────────────────
 *
 * A rebuild used to keep only the points INSIDE the window (`windowedPoints`),
 * with the cut taken on the BROWSER's clock (`Date.now()`) while the axis was
 * placed on the SERVER's (`Date.now() + serverOffset`). So the point just off
 * the left edge was dropped, the line began at the first point inside, and an
 * empty strip sat at the left edge until it scrolled out - widened by however
 * far the browser's clock ran ahead of the server's. Measured on the hAP ax^3:
 * 17 rebuilds in 120 s, every one leaving a strip of 266 to 967 ms, and no
 * strip at any other time.
 *
 * Taking the cut from the axis window itself puts both on one clock, and the
 * slack is the keepalive's own (`pruneAndMax`), so a rebuild holds exactly the
 * points the next frame would keep: the line reaches the edge from the first
 * frame, and no point is drawn once and dropped the frame after.
 *
 * The Bandwidth page had kept the slack all along (it seeded with
 * `bandwidthSeedPoints`) but still cut on the browser's clock; it uses this too.
 */
export function seedPoints(points: readonly TrafficPoint[], viewLeft: number): TrafficPoint[] {
  // A FILTER, not a walk back from the newest point: one out-of-order sample
  // (an NTP step on the server) must not truncate everything before it.
  const cutoff = viewLeft - KEEPALIVE_SLACK_MS;
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
  return !rx.length || sampleTs - rx[rx.length - 1]!.x > REDRAW_GAP_MS;
}

/**
 * How far past the last drawn point a sample must land to count as a break.
 *
 * It was 2,000 ms, which a router in stream mode crosses in ordinary running:
 * the hAP ax^3 sent samples 2.0 to 2.8 s apart twelve times in 90 s
 * (2026-11-22), and each one rebuilt the chart from scratch. A hidden tab, a
 * page change or a router going away is a gap of many seconds; 5 s tells the
 * two apart. Appending across a gap shorter than that draws the same line a
 * rebuild would, because the buffer holds the same points.
 */
export const REDRAW_GAP_MS = 5000;
