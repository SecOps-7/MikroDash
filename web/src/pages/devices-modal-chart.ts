// The device modal's WAN chart: the Dashboard traffic chart's mechanics, on one
// device's `device:live` points.
//
// ── IT SCROLLS, IT DOES NOT TICK ───────────────────────────────────────────
//
// Samples arrive once a second. Drawn only when one lands, the chart jumps a
// second at a time. So, exactly as `dashboard-traffic.ts` does: a frame loop
// (~30fps, `requestAnimationFrame`) advances the X axis against the ESTIMATED
// SERVER TIME every frame, the samples only append data, the right edge is held
// one measured sample interval back and eased (`rightBufferFor`/`easeBuffer`) so
// the newest point is never clipped and the edge never reverses, and the Y scale
// is lerped (`smoothMax`) instead of snapping. Every one of those formulas is
// imported from `dashboard-traffic-buffer.ts` rather than restated, which is how
// the Bandwidth page shares them too: three charts, one arithmetic.
//
// The loop stops itself when the chart is destroyed (the modal closing) and
// does nothing while the tab is hidden, so a closed modal costs nothing.

import { fmtTime } from '../timefmt';
import { fmtMbps } from '../dom';
import type { TrafficPoint } from '../gen/payloads';
import {
  RIGHT_BUFFER_MS, anchorMs, axisWindow, easeBuffer, needsFullRedraw, pruneAndMax,
  rightBufferFor, smoothMax, smoothOffset, windowedPoints, type XYPoint,
} from './dashboard-traffic-buffer';
import { trafficTickPlugin } from './dashboard-traffic';

/** The chart's window: the first frame's ring, five minutes. */
export const WAN_WINDOW_SECS = 300;

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { datasets: { data: XYPoint[] }[] };
  options: { scales: { x: { min?: number; max?: number }; y: { max?: number } } };
}
// Loaded by the shell from /vendor, as for the Dashboard's chart.
declare const Chart: undefined | (new (canvas: HTMLElement, cfg: unknown) => ChartLike);

let chart: ChartLike | null = null;
let points: TrafficPoint[] = [];
let lastSampleTs = 0, serverOffset = 0, yCurrent = 0, rbCurrent = RIGHT_BUFFER_MS, lastTickMs = 0;
let rafId: number | null = null;

function css(name: string, fallback: string): string {
  const v = typeof getComputedStyle === 'function'
    ? getComputedStyle(document.documentElement).getPropertyValue(name).trim() : '';
  return v || fallback;
}

function config(nowMs: number): unknown {
  // Rx and Tx are the app's fixed pair, read from the theme so the light
  // palette's darker blue and green apply here too.
  const rx = css('--accent-rx', '#38bdf8'), tx = css('--accent-tx', '#34d399');
  return {
    type: 'line',
    plugins: [trafficTickPlugin],
    data: {
      datasets: [
        { label: 'RX', data: [], borderColor: rx, backgroundColor: 'color-mix(in srgb,' + rx + ' 14%,transparent)', borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true },
        { label: 'TX', data: [], borderColor: tx, backgroundColor: 'color-mix(in srgb,' + tx + ' 10%,transparent)', borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      devicePixelRatio: Math.min(window.devicePixelRatio, 1.5),
      // The frame loop renders; an animated update would fight it.
      animation: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: 'rgba(7,9,15,.9)', borderColor: 'rgba(99,130,190,.2)', borderWidth: 1,
          titleFont: { family: "'JetBrains Mono',monospace", size: 11 },
          bodyFont: { family: "'JetBrains Mono',monospace", size: 11 },
          callbacks: {
            title: (items: { parsed: { x: number } }[]) => fmtTime(items[0]!.parsed.x),
            label: (c: { dataset: { label: string }; parsed: { y: number } }) =>
              ' ' + c.dataset.label + ': ' + fmtMbps(c.parsed.y),
          },
        },
      },
      scales: {
        x: {
          type: 'linear', display: true,
          min: nowMs - WAN_WINDOW_SECS * 1000 - RIGHT_BUFFER_MS, max: nowMs - RIGHT_BUFFER_MS,
          grid: { display: false, drawBorder: false }, border: { display: false },
          ticks: { display: false },
          afterFit: (s: { height: number }) => { s.height = 26; },
        },
        y: {
          beginAtZero: true,
          grid: { color: 'rgba(99,130,190,.07)' },
          ticks: {
            color: 'rgba(148,163,190,.55)',
            font: { family: "'JetBrains Mono',monospace", size: 10 },
            callback: (v: number) => fmtMbps(v),
          },
        },
      },
    },
  };
}

/** Rebuild the datasets from the buffer at the current time. A discontinuity,
 *  so the axis and the scale are SNAPPED, never eased - as the Dashboard does. */
function redraw(): void {
  if (!chart) return;
  const rb = rightBufferFor(points);
  const pts = windowedPoints(points, Date.now(), WAN_WINDOW_SECS, rb);
  chart.data.datasets[0]!.data = pts.map((p) => ({ x: p.ts, y: p.rx_mbps }));
  chart.data.datasets[1]!.data = pts.map((p) => ({ x: p.ts, y: p.tx_mbps }));
  let max = 0;
  for (const p of pts) max = Math.max(max, p.rx_mbps, p.tx_mbps);
  yCurrent = max || 1;
  chart.options.scales.y.max = yCurrent;
  rbCurrent = rb;
  const win = axisWindow(anchorMs(lastSampleTs, serverOffset, Date.now(), pts), WAN_WINDOW_SECS, rb);
  chart.options.scales.x.min = win.min;
  chart.options.scales.x.max = win.max;
  chart.update('none');
}

function tick(): void {
  if (!chart) { rafId = null; return; }
  rafId = requestAnimationFrame(tick);
  if (document.hidden || !lastSampleTs) return;
  const now = Date.now();
  if (now - lastTickMs < 33) return;
  const elapsed = now - lastTickMs;
  lastTickMs = now;
  rbCurrent = easeBuffer(rbCurrent, rightBufferFor(points), elapsed);
  const sn = now + serverOffset;
  const vl = sn - WAN_WINDOW_SECS * 1000 - rbCurrent;
  const rx = chart.data.datasets[0]!.data, tx = chart.data.datasets[1]!.data;
  yCurrent = smoothMax(yCurrent, pruneAndMax(rx, tx, vl) || 1);
  chart.options.scales.y.max = yCurrent;
  chart.options.scales.x.min = vl;
  chart.options.scales.x.max = sn - rbCurrent;
  chart.update('none');
}

/** Build the chart on this canvas from a first frame's ring. */
export function startWanChart(canvas: HTMLElement, ring: readonly TrafficPoint[]): void {
  stopWanChart();
  points = ring.slice();
  const last = points[points.length - 1];
  if (last) { lastSampleTs = last.ts; serverOffset = last.ts - Date.now(); }
  if (typeof Chart === 'undefined') return;
  chart = new Chart(canvas, config(Date.now()));
  redraw();
  lastTickMs = Date.now();
  if (!rafId) rafId = requestAnimationFrame(tick);
}

/** Append a later frame's points. Rendering is the frame loop's job. */
export function pushWanPoints(fresh: readonly TrafficPoint[]): void {
  for (const p of fresh) {
    points.push(p);
    lastSampleTs = p.ts;
    serverOffset = smoothOffset(serverOffset, p.ts - Date.now());
    if (!chart) continue;
    const rx = chart.data.datasets[0]!.data, tx = chart.data.datasets[1]!.data;
    // A gap (a reconnect, a hidden tab) is drawn from the buffer afresh.
    if (needsFullRedraw(rx, p.ts)) { redraw(); continue; }
    rx.push({ x: p.ts, y: p.rx_mbps });
    tx.push({ x: p.ts, y: p.tx_mbps });
  }
  const cutoff = Date.now() + serverOffset - WAN_WINDOW_SECS * 1000 - 10_000;
  while (points.length && points[0]!.ts < cutoff) points.shift();
}

/** The buffer, for the rates and for tests. */
export function wanPoints(): readonly TrafficPoint[] { return points; }

/** Tear down: the modal closed or moved to another device. Stops the loop. */
export function stopWanChart(): void {
  if (chart) { chart.destroy(); chart = null; }
  if (rafId !== null && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(rafId);
  rafId = null;
  points = [];
  lastSampleTs = 0; serverOffset = 0; yCurrent = 0; rbCurrent = RIGHT_BUFFER_MS;
}
