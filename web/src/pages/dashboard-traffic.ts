// The Dashboard's traffic chart — the last of the seven cards.
//
// ── IT DRAWS ON PINNED ARITHMETIC ───────────────────────────────────────────
//
// Every formula this file needs lives in `dashboard-traffic-buffer.ts` and is
// compared against the live expressions there. What is left here is the parts
// that touch Chart.js and the DOM: the config, the redraw, the static-tick
// plugin and the 60fps keepalive.
//
// ── THE KEEPALIVE IS WHY THE CHART LOOKS ALIVE BETWEEN SAMPLES ──────────────
//
// Samples arrive at 1 Hz. If the axis only moved when one landed, the chart
// would jump once a second. Instead a rAF loop advances the X axis against the
// ESTIMATED SERVER TIME every frame, and the sample handler only appends data.
// Both use the same anchor formula, so the first frame after a redraw continues
// exactly where the redraw painted rather than snapping sideways.
//
// It throttles itself to ~30fps (`now - lastTickMs < 33`) and bails entirely
// when the tab is hidden, the router is down, or the socket is disconnected —
// so a backgrounded tab costs nothing and a dead router's chart stops advancing
// instead of scrolling away from its last data.
//
// ── AND THE FADE EXISTS TO HIDE A CATCH-UP, NOT TO LOOK NICE ────────────────
//
// On hide the canvas is set to opacity 0. While hidden the keepalive is off, so
// when the tab comes back the axis is stale by however long it was away and the
// first frames jump forward to catch up. The fade covers exactly that: commit
// opacity 0 with no transition, force a reflow so the browser registers a real
// 0→1 change rather than collapsing it, then fade in once the axis has settled.

import type { Socket } from '../socket';
import { el, fmtMbps } from '../dom';
import { notePayload } from '../stale';
import { isRosDisconnected } from '../banners';
import {
  MAX_CLIENT_POINTS, RIGHT_BUFFER_MS, anchorMs, axisWindow, needsFullRedraw, pruneAndMax,
  pushSample, smoothMax, smoothOffset, windowedPoints,
  type TrafficSample, type XYPoint,
} from './dashboard-traffic-buffer';

/** How far the right edge sits behind the anchor, so the newest point is not clipped. */

const WINDOW_OPTIONS: Record<string, number> = { '1m': 60, '5m': 300, '15m': 900, '30m': 1800 };

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { datasets: { data: XYPoint[]; label?: string }[] };
  options: { scales: { x: { min?: number; max?: number }; y: { max?: number } } };
}
// Chart.js is loaded by the shell from /vendor, so it is a global here rather
// than an import — the same arrangement `pages/routing.ts` uses.
declare const Chart: undefined | (new (canvas: HTMLElement, cfg: unknown) => ChartLike);

let chart: ChartLike | null = null;
let allPoints: TrafficSample[] = [];
let currentIf = '';
let windowSecs = 60;
let lastSampleTs = 0, serverOffset = 0;
let yMaxTarget = 0, yMaxCurrent = 0, lastTickMs = 0;
let keepaliveId: number | null = null;
let pendingTraffic: TrafficSample | null = null;
let trafficRafId: number | null = null;

/**
 * Evenly spaced grid lines and timestamp labels at fixed pixel positions.
 *
 * Reads the axis MIN/MAX rather than the drawn data, so labels snap to the new
 * timestamps immediately while the line animates behind them. The label count
 * scales with width — at narrow sizes it collapses to a single right-aligned
 * label instead of overlapping.
 */
export const trafficTickPlugin = {
  id: 'trafficStaticTicks',
  afterDraw(c: {
    options: { scales: { x: { min?: number; max?: number } } };
    ctx: CanvasRenderingContext2D;
    chartArea: { left: number; right: number; top: number; bottom: number };
  }): void {
    const x = c.options.scales.x;
    if (!x || x.min == null || x.max == null) return;
    const ctx = c.ctx, ca = c.chartArea, w = ca.right - ca.left;
    ctx.save();
    ctx.font = "10px 'JetBrains Mono',monospace";
    ctx.textBaseline = 'top';
    const labelW = ctx.measureText(new Date(x.min).toLocaleTimeString()).width;
    const n = Math.min(7, Math.max(1, Math.floor(w / (labelW + 20))));
    if (n === 1) {
      ctx.fillStyle = 'rgba(148,163,190,.4)';
      ctx.textAlign = 'right';
      ctx.fillText(new Date(x.max).toLocaleTimeString(), ca.right, ca.bottom + 6);
    } else {
      for (let i = 0; i < n; i++) {
        const frac = i / (n - 1), px = Math.round(ca.left + frac * w);
        ctx.beginPath();
        ctx.strokeStyle = 'rgba(99,130,190,.07)';
        ctx.lineWidth = 1;
        // The half-pixel offset puts a 1px line on a device pixel instead of
        // straddling two and rendering as a 2px blur.
        ctx.moveTo(px + 0.5, ca.top);
        ctx.lineTo(px + 0.5, ca.bottom);
        ctx.stroke();
        ctx.fillStyle = 'rgba(148,163,190,.4)';
        ctx.textAlign = i === 0 ? 'left' : i === n - 1 ? 'right' : 'center';
        ctx.fillText(new Date(x.min + frac * (x.max - x.min)).toLocaleTimeString(), px, ca.bottom + 6);
      }
    }
    ctx.restore();
  },
};

export function chartConfig(nowMs: number): unknown {
  return {
    type: 'line',
    plugins: [trafficTickPlugin],
    data: {
      datasets: [
        { label: 'RX', data: [], borderColor: '#38bdf8', backgroundColor: 'rgba(56,189,248,.08)', borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true },
        { label: 'TX', data: [], borderColor: '#34d399', backgroundColor: 'rgba(52,211,153,.06)', borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      // Capped at 1.5: a 3x display would quadruple the fill cost of a chart
      // that repaints every frame, for a line nobody can see the extra detail in.
      devicePixelRatio: Math.min(window.devicePixelRatio, 1.5),
      animation: { duration: 1000, easing: 'linear' },
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: 'rgba(7,9,15,.9)', borderColor: 'rgba(99,130,190,.2)', borderWidth: 1,
          titleFont: { family: "'JetBrains Mono',monospace", size: 11 },
          bodyFont: { family: "'JetBrains Mono',monospace", size: 11 },
          callbacks: {
            title: (items: { parsed: { x: number } }[]) => new Date(items[0]!.parsed.x).toLocaleTimeString(),
            label: (c: { dataset: { label: string }; parsed: { y: number } }) =>
              ' ' + c.dataset.label + ': ' + fmtMbps(c.parsed.y),
          },
        },
      },
      scales: {
        x: {
          type: 'linear', display: true,
          min: nowMs - windowSecs * 1000 - RIGHT_BUFFER_MS, max: nowMs - RIGHT_BUFFER_MS,
          grid: { display: false, drawBorder: false },
          border: { display: false },
          ticks: { display: false },
          afterFit: (s: { height: number }) => { s.height = 26; },
        },
        y: {
          beginAtZero: true,
          grid: { color: 'rgba(99,130,190,.07)' },
          ticks: {
            color: 'rgba(148,163,190,.4)',
            font: { family: "'JetBrains Mono',monospace", size: 10 },
            callback: (v: number) => fmtMbps(v),
          },
        },
      },
    },
  };
}

function makeChartObj(): void {
  if (chart) { chart.destroy(); chart = null; }
  const canvas = el('trafficChart');
  if (!canvas || typeof Chart === 'undefined') return;
  chart = new Chart(canvas, chartConfig(Date.now()));
}

// ── THE SHARED BUFFER, READ BY THE BANDWIDTH PAGE ───────────────────────────
//
// The live app gets this for free: `app.js:6950` builds the Bandwidth chart's
// points from `allPoints`, the same module-scope array the dashboard chart uses,
// because both live in one file scope. ONE buffer, two readers.
//
// This port has them in separate modules, so the sharing has to be deliberate —
// and it must be an ACCESSOR, not the array. `allPoints` is REASSIGNED
// (`initChart` replaces it from history, `reset` empties it), so a consumer that
// captured the array once would keep reading a detached copy and quietly diverge
// the moment either happened. Returning it per call cannot.
//
// The alternative — a second buffer fed from the same `traffic:update` — is the
// thing PORT-QUEUE warns against: two arrays pruned by two rules drift apart,
// and the drift only shows up as two charts disagreeing about the same second.
export function sharedPoints(): TrafficSample[] { return allPoints; }

/** The shared clock the keepalives anchor to: `Date.now() + serverOffset`, and
 *  zero for `lastSampleTs` until the first sample has arrived. Both are updated
 *  by the dashboard's own handler for EVERY sample regardless of which page is
 *  open, which is what keeps a returning page's clock warm. */
export function sharedClock(): { lastSampleTs: number; serverOffset: number; windowSecs: number } {
  return { lastSampleTs, serverOffset, windowSecs };
}

export function redrawChart(): void {
  const pts = windowedPoints(allPoints, Date.now(), windowSecs, RIGHT_BUFFER_MS);
  if (!chart) makeChartObj();
  if (!chart) return;
  chart.data.datasets[0]!.data = pts.map((p) => ({ x: p.ts, y: p.rx_mbps }));
  chart.data.datasets[1]!.data = pts.map((p) => ({ x: p.ts, y: p.tx_mbps }));
  let dMax = 0;
  for (const p of pts) {
    if (p.rx_mbps > dMax) dMax = p.rx_mbps;
    if (p.tx_mbps > dMax) dMax = p.tx_mbps;
  }
  yMaxTarget = dMax || 1;
  // Set, NOT eased: a redraw is a discontinuity already, and easing the axis
  // from the old scale would animate a change the data did not make.
  yMaxCurrent = yMaxTarget;
  chart.options.scales.y.max = yMaxCurrent;
  const anchor = anchorMs(lastSampleTs, serverOffset, Date.now(), pts);
  const win = axisWindow(anchor, windowSecs, RIGHT_BUFFER_MS);
  chart.options.scales.x.min = win.min;
  chart.options.scales.x.max = win.max;
  chart.update('none');
}

export function applyWindow(secs: number): void {
  windowSecs = secs;
  redrawChart();
}

export function initChart(points: TrafficSample[] | undefined): void {
  allPoints = (points || []).slice(-MAX_CLIENT_POINTS);
  if (!chart) makeChartObj();
  redrawChart();
}

/**
 * Freeze the chart on hide.
 *
 * Clearing `lastSampleTs` is what stops the keepalive: it bails on a falsy one
 * and resumes from the smoothed offset when the next sample lands, so there is
 * no resume jump. The live app binds this to window BLUR as well as
 * visibilitychange — dropping behind another application does not always fire
 * visibilitychange, and blur does.
 */
export function hideTrafficChart(): void {
  lastSampleTs = 0;
  const ctx = el('trafficChart');
  if (ctx) { ctx.style.transition = 'none'; ctx.style.opacity = '0'; }
}

function keepaliveTick(): void {
  keepaliveId = requestAnimationFrame(keepaliveTick);
  if (!chart || document.hidden || !lastSampleTs || isRosDisconnected() ||
      document.body.classList.contains('is-disconnected')) return;
  const now = Date.now();
  if (now - lastTickMs < 33) return;
  lastTickMs = now;
  const sn = now + serverOffset;
  const vl = sn - windowSecs * 1000 - RIGHT_BUFFER_MS;
  const rd = chart.data.datasets[0]!.data, td = chart.data.datasets[1]!.data;
  yMaxTarget = pruneAndMax(rd, td, vl) || 1;
  yMaxCurrent = smoothMax(yMaxCurrent, yMaxTarget);
  chart.options.scales.y.max = yMaxCurrent;
  chart.options.scales.x.min = vl;
  chart.options.scales.x.max = sn - RIGHT_BUFFER_MS;
  chart.update('none');
}

function flushTraffic(): void {
  trafficRafId = null;
  if (!pendingTraffic) return;
  const p = pendingTraffic;
  pendingTraffic = null;
  if (!document.hidden) {
    const rx = el('liveRx'), tx = el('liveTx');
    if (rx) rx.textContent = fmtMbps(p.rx_mbps);
    if (tx) tx.textContent = fmtMbps(p.tx_mbps);
  }
  const ctx = el('trafficChart');
  // Guarded on !hidden: a throttled rAF can still fire while the page is
  // occluded, and restoring opacity there un-hides the canvas before the
  // reveal — killing the fade and exposing the catch-up jump it exists to hide.
  if (!document.hidden && ctx && ctx.style.opacity === '0') {
    ctx.style.transition = 'none';
    void ctx.offsetHeight;
    ctx.style.transition = 'opacity 0.4s ease';
    ctx.style.opacity = '1';
  }
  lastSampleTs = p.ts;
  serverOffset = smoothOffset(serverOffset, p.ts - Date.now());
  if (!keepaliveId) keepaliveTick();
  if (!chart) return;
  const rx = chart.data.datasets[0]!.data, tx = chart.data.datasets[1]!.data;
  if (needsFullRedraw(rx, p.ts)) { redrawChart(); return; }
  rx.push({ x: p.ts, y: p.rx_mbps });
  tx.push({ x: p.ts, y: p.tx_mbps });
  // Scale advance and rendering are the keepalive's job.
}

export function noteTrafficUpdate(sample: TrafficSample & { ifName?: string }): void {
  if (!currentIf || sample.ifName !== currentIf) return;
  // Buffered ALWAYS, even hidden or on another page: only the DOM update is
  // deferred, so history survives a backgrounded tab rather than developing a
  // hole in it.
  pushSample(allPoints, sample);
  pendingTraffic = sample;
  if (!trafficRafId) trafficRafId = requestAnimationFrame(flushTraffic);
}

/**
 * Should this arriving history be answered by asking for the operator's pick
 * back?
 *
 * Three conditions, and each has a plausible wrong answer:
 *
 *   THERE IS A PICK        no pick means the operator never chose; the server's
 *                          default is simply what they get.
 *   IT IS NOT ALREADY IT   the re-request produces history naming the pick, and
 *                          without this that history would re-trigger the
 *                          restore forever.
 *   IT IS STILL SELECTABLE  when an interface goes down it leaves the options and
 *                          `rebuildIfaceSelect` moves to the first live one.
 *                          Without this guard the restore and that auto-switch
 *                          argue every tick, one moving away and the other
 *                          dragging back.
 *
 * Extracted from the handler so it can be gated: the surrounding code needs a
 * DOM and a socket, and this needs neither.
 */
export function shouldRestorePick(
  arrivingIfName: string | undefined, picked: string, options: string[],
): boolean {
  if (!picked) return false;
  if (arrivingIfName === picked) return false;
  return options.indexOf(picked) !== -1;
}

export function onTrafficHistory(data: { ifName?: string; points?: TrafficSample[] }): void {
  currentIf = data.ifName || '';
  const sel = el<HTMLSelectElement>('ifaceSelect');
  if (sel) sel.value = data.ifName || '';

  // A reconnect arrives here with the server's default, because the new socket
  // has a new subscription. If the operator had chosen something else, ask for
  // it back.
  //
  // GUARDED ON THE PICK STILL BEING IN THE LIST, which is what keeps this from
  // fighting the auto-switch: when an interface goes down it leaves the options,
  // `rebuildIfaceSelect` moves to the first live one, and this stays quiet
  // because the pick is no longer selectable. It resumes if the interface comes
  // back and the socket reconnects.
  //
  // NO LOOP: this only reacts to history ARRIVING, and the re-request produces
  // history naming the pick itself, which fails the first condition.
  // THE PICK IS TESTED BEFORE THE OPTIONS ARE READ, and that order is not
  // cosmetic. The live condition is `_userPickedIf && … && [].some.call(
  // ifaceSelect.options, …)`, which short-circuits: with no pick, the options
  // are never touched. Extracting the decision into a function made the list an
  // ARGUMENT, so it was built eagerly — and `Array.prototype.map` on a select
  // with no `options` throws, which two gates caught immediately on a minimal
  // payload. An extraction that changes evaluation order is not a refactor.
  if (sel && userPickedIf && shouldRestorePick(data.ifName, userPickedIf,
    Array.prototype.map.call(sel.options || [], (o: HTMLOptionElement) => o.value) as string[])) {
    sel.value = userPickedIf;
    requestInterface?.(userPickedIf);
    // The history for the pick is on its way; drawing this one first would flash.
    return;
  }

  const pts = data.points || [];
  initChart(pts);
  if (pts.length) {
    const last = pts[pts.length - 1]!;
    const rx = el('liveRx'), tx = el('liveTx');
    if (rx) rx.textContent = fmtMbps(last.rx_mbps);
    if (tx) tx.textContent = fmtMbps(last.tx_mbps);
  }
  // A new router's history must not trip the 10s stale threshold while that
  // router is still connecting.
  notePayload('trafficCard');
}

/**
 * Forget the history: another router's samples are not this one's, and neither
 * are samples from before a socket gap.
 *
 * ── WHAT THIS CLEARS, AND WHAT IT DELIBERATELY DOES NOT ─────────────────────
 *
 * `currentIf` and `allPoints`, which is exactly what the live app clears at both
 * of its own sites — `app.js:2957` (socket `connect`) and `app.js:8048`
 * (`router:switching`).
 *
 * It used to also zero `lastSampleTs`, `serverOffset` and `pendingTraffic`, and
 * the live app clears none of those ANYWHERE. `serverOffset` is the one that
 * matters: it is an EMA of the server/browser clock skew, and `app.js:2318`
 * says in as many words that keeping it is what makes a resume smooth — "the
 * keepalive bails on !_lastSampleTs and resumes cleanly from the (EMA-smoothed)
 * _serverOffset when the next sample arrives, so there is no resume jump."
 * Zeroing it made the next sample set the offset raw, which moves the first
 * samples after a switch along the X axis. That is a user-visible difference in
 * a chart, which is the line a port may not move.
 */
/**
 * The interface the OPERATOR chose, as opposed to the one currently streaming.
 *
 * Deliberately NOT cleared on socket connect, which is the whole point. The
 * server keys its traffic subscription on the socket, and a reconnect is a new
 * socket — so the subscription reverts to `defaultIf` and the operator's choice
 * is simply gone. A network blip silently moved them back to the WAN interface
 * minutes after they picked something else (upstream issue #119, second report).
 * The page has not reloaded, so this survives and the choice can be restored.
 *
 * It IS cleared on a router switch, because a different router has different
 * interfaces and carrying a name across would be meaningless.
 *
 * Ported from upstream `d7548b0`, found by `tools/reset-contract-audit.js` on
 * 2026-08-28 — the audit noticed the live `router:switching` handler clearing a
 * variable this port had nothing to map onto.
 */
let userPickedIf = '';

/**
 * How the restore below asks for the operator's interface back.
 *
 * The live handler is a closure over the module's `socket`; this module takes it
 * at init instead, because `onTrafficHistory` is also called by
 * `pages/dashboard.ts` and gating the restore on which caller ran would make the
 * behaviour depend on the page rather than on the reconnect.
 */
let requestInterface: ((ifName: string) => void) | null = null;

/**
 * What a RECONNECT forgets: the chart's history and nothing else.
 *
 * ── THIS EXISTS BECAUSE ONE FUNCTION COULD NOT SAY BOTH THINGS ─────────────
 *
 * The live app clears the chart at two moments with two DIFFERENT sets, written
 * inline at each site: `socket.on('connect')` clears `currentIf` and
 * `allPoints`; `router:switching` clears those AND `_userPickedIf`.
 *
 * This port had one `resetTraffic` wired to both. That was correct until
 * upstream `d7548b0` added `_userPickedIf` to the switch site only — at which
 * point the single function silently started clearing the operator's chosen
 * interface on every reconnect, which is the exact symptom of issue #119's
 * second report ("it seems to switch to ether2 after some time"). The function's
 * own comment claimed the opposite, and `reset-contract-audit.js` records the
 * asymmetry in a comment without asserting it, so every gate stayed green.
 *
 * Splitting is what makes the asymmetry expressible at all.
 */
export function resetTrafficOnReconnect(): void {
  // Both of these, and only these — the live `connect` handler's own two
  // statements. A socket gap otherwise leaves `allPoints` holding samples from
  // before it, and the post-reconnect history is appended to them: a chart drawn
  // straight across a period during which nothing was received.
  currentIf = '';
  allPoints = [];
}

/**
 * What a ROUTER SWITCH forgets: the above, plus the operator's pick.
 */
export function resetTraffic(): void {
  resetTrafficOnReconnect();
  // Cleared HERE but NOT on reconnect, and the difference is the whole fix: a
  // reconnect is the same operator looking at the same router, so their choice
  // should survive it. A router switch is a different fleet of interfaces, and
  // carrying a name across would either miss or, worse, match something
  // unrelated that happens to share it.
  userPickedIf = '';
}

export function initTraffic(socket: Socket): void {
  requestInterface = (ifName) => socket.emit('traffic:select', { ifName });
  socket.on('traffic:history', (d) => onTrafficHistory(d as { ifName?: string; points?: TrafficSample[] }));
  socket.on('traffic:update', (d) => noteTrafficUpdate(d as TrafficSample & { ifName?: string }));

  const sel = el<HTMLSelectElement>('ifaceSelect');
  if (sel) {
    sel.addEventListener('change', () => {
      // Recorded here and ONLY here: this is the operator acting. The
      // auto-switch in `rebuildIfaceSelect`, which moves off an interface that
      // has gone down, must not overwrite it — that is the app coping, not a
      // choice, and remembering it would mean a flap permanently rewrote what
      // the operator asked for.
      userPickedIf = sel.value;
      socket.emit('traffic:select', { ifName: sel.value });
    });
  }
  const win = el<HTMLSelectElement>('windowSelect');
  if (win) win.addEventListener('change', () => applyWindow(WINDOW_OPTIONS[win.value] || 60));
}
