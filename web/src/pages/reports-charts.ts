// The three Reports charts: RTT trend, RX/TX rate, and download/upload volume.
//
// ── WHAT THE DOM GATE CANNOT SEE ────────────────────────────────────────────
//
// The acceptance test for a ported page compares innerHTML, and a Chart.js line
// is pixels on a canvas. So the guarantee here is the one routing.ts already
// makes for its doughnut: the SAME LIBRARY driven by the SAME CONFIGURATION.
// Every dataset, colour, dash pattern, axis, tick callback and tooltip formatter
// below is reproduced exactly, so a visible difference would have to come from
// Chart.js itself. The library is the file the live app already serves at
// /vendor/chart.umd.min.js — the Go server proxies everything outside /next, so
// it is the identical file rather than a second copy.
//
// ── EVERY CHART DOWNSAMPLES TO 300 POINTS ───────────────────────────────────
//
// A report can return 100,000 rows and a 140-pixel-high canvas cannot show them.
// The step is `ceil(n/300)` and the filter keeps every step-th row, which is the
// original's — NOT an average of each window. That matters: averaging would
// smooth away the spikes the chart exists to show, and the peak datasets below
// exist precisely because averaging already happened server-side.

import { el, fmtMbps, fmtDataMB } from '../dom';
import { chartLabel } from './reports';
import type { PingRow } from './reports-ping';
import type { TrafficRow, BandwidthRow, IfaceSummary } from './reports-traffic';

// Chart.js is loaded by the shell from /vendor, so it is a global here rather
// than an import. Typed loosely on purpose: the port does not own the library's
// types, and pinning them would be a second thing to keep in step.
interface ChartLike { destroy(): void }
declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

const MONO = { size: 10, family: 'JetBrains Mono,monospace' };
const GRID = { color: 'rgba(99,130,190,.08)' };
const AXIS_TICK = { maxTicksLimit: 8, color: 'rgba(148,163,190,.5)', font: MONO };

const RX_LINE = 'rgba(56,189,248,.85)';
const RX_FILL = 'rgba(56,189,248,.07)';
const TX_LINE = 'rgba(52,211,153,.8)';
const TX_FILL = 'rgba(52,211,153,.06)';

/** Keep every step-th row so the canvas draws at most 300 points. */
function downsample<T>(rows: T[]): T[] {
  const step = rows.length > 300 ? Math.ceil(rows.length / 300) : 1;
  return rows.filter((_, i) => i % step === 0);
}

/** The span the tick labels are scaled to: first sample to last, after thinning. */
function spanOf(sub: { ts: number }[]): number {
  if (sub.length < 2) return 0;
  const first = sub[0] as { ts: number };
  const last = sub[sub.length - 1] as { ts: number };
  return last.ts - first.ts;
}

/** The options every one of these line charts shares. */
function baseOptions(extra: Record<string, unknown>): Record<string, unknown> {
  return {
    responsive: true,
    maintainAspectRatio: false,
    // ANIMATION OFF. These redraw on every load and every interface change; an
    // animated 300-point line is a visible stutter, not a flourish.
    animation: false,
    layout: { padding: { bottom: 8 } },
    // `index`/`intersect:false` gives one tooltip for the whole column, so a
    // pointer anywhere near the line reads every series at that instant.
    interaction: { mode: 'index', intersect: false },
    ...extra,
  };
}

let pingChart: ChartLike | null = null;
let trafficChart: ChartLike | null = null;
let bandwidthChart: ChartLike | null = null;

/**
 * The RTT trend, with loss on a second axis.
 *
 * TWO AXES, and the right-hand one is PINNED to 0–100. Loss is a percentage, so
 * an auto-scaled axis would make a link losing 0.4% look identical to one losing
 * 40% — the line would fill the chart either way.
 *
 * `spanGaps:true` on the RTT series draws THROUGH a null, so a timed-out probe
 * does not cut the line into pieces. The loss line rises at the same instant,
 * which is where the eye should go anyway.
 */
export function renderPingChart(rows: PingRow[]): void {
  const canvas = el<HTMLCanvasElement>('rptPingChart');
  if (!canvas || typeof Chart === 'undefined') return;
  if (pingChart) {
    pingChart.destroy();
    pingChart = null;
  }
  const sub = downsample(rows);
  const span = spanOf(sub);
  const labels = sub.map((r) => chartLabel(r.ts, span));
  const rtts = sub.map((r) => (r.rtt_ms != null ? +r.rtt_ms.toFixed(1) : null));
  const losses = sub.map((r) => +r.loss_pct.toFixed(1));

  pingChart = new Chart(canvas, {
    type: 'line',
    data: {
      labels,
      datasets: [
        {
          label: 'RTT ms', data: rtts, borderColor: RX_LINE, backgroundColor: RX_FILL,
          borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true, spanGaps: true, yAxisID: 'yR',
        },
        {
          label: 'Loss %', data: losses, borderColor: 'rgba(248,113,113,.8)',
          backgroundColor: 'transparent', borderWidth: 1.5, pointRadius: 0, tension: 0.2,
          fill: false, yAxisID: 'yL',
        },
      ],
    },
    options: baseOptions({
      plugins: { legend: { display: false } },
      scales: {
        x: { ticks: AXIS_TICK, grid: GRID },
        yR: { position: 'left', ticks: { color: 'rgba(56,189,248,.7)', font: MONO }, grid: GRID },
        yL: {
          position: 'right', min: 0, max: 100,
          ticks: { color: 'rgba(248,113,113,.7)', font: MONO },
          // The right axis draws no grid of its own — two grids at different
          // scales over one plot is unreadable.
          grid: { drawOnChartArea: false },
        },
      },
    }),
  });
}

// ── The capacity toggle ─────────────────────────────────────────────────────

const RPT_CAP_KEY = 'mikrodash_rpt_capacity';

/** Whether the capacity reference lines are switched on. Off by default. */
export function showCapacity(): boolean {
  try {
    return localStorage.getItem(RPT_CAP_KEY) === '1';
  } catch {
    return false;
  }
}

function setShowCapacity(on: boolean): void {
  try {
    localStorage.setItem(RPT_CAP_KEY, on ? '1' : '0');
  } catch {
    // A browser with storage disabled still gets a working toggle for this visit.
  }
}

/** What the traffic chart last drew, so the toggle can redraw it identically. */
let lastTrafficRows: TrafficRow[] = [];
let lastTrafficSummary: IfaceSummary | null = null;
let lastTrafficAgg = '';

/**
 * Wire the capacity checkbox once.
 *
 * It REDRAWS FROM THE ROWS ALREADY IN HAND rather than refetching: toggling a
 * reference line changes nothing about which samples are wanted.
 */
export function wireCapacityToggle(): void {
  const cb = el<HTMLInputElement>('rptShowCapacity');
  if (!cb) return;
  cb.checked = showCapacity();
  cb.addEventListener('change', () => {
    setShowCapacity(cb.checked);
    renderTrafficChart(lastTrafficRows, lastTrafficSummary, lastTrafficAgg);
  });
}

/**
 * The RX/TX rate chart.
 *
 * ── THE FAINT DASHED LINES ARE THE POINT OF THE AGGREGATED VIEW ─────────────
 *
 * An aggregated point is a bucket AVERAGE, so a spike inside the bucket is
 * invisible — the same failure that once showed a 938 Mbps burst as about
 * 4 Mbps on a daily chart. The per-bucket peaks are drawn faintly beside the
 * mean so both are readable at once. They are omitted when unaggregated, where
 * the rows already ARE the peaks and a second identical line would be noise.
 *
 * ── CAPACITY IS OFF BY DEFAULT, AND BELONGS ON THIS CHART ───────────────────
 *
 * The axis here is already Mbps, so the line is a direct comparison with no
 * conversion — on the volume chart it would be meaningless. Off by default
 * because on a 1 Gbps link carrying a few Mbps it rescales the y-axis by orders
 * of magnitude and flattens the real curve onto the baseline.
 */
export function renderTrafficChart(
  rows: TrafficRow[], summary: IfaceSummary | null, agg: string,
): void {
  lastTrafficRows = rows;
  lastTrafficSummary = summary;
  lastTrafficAgg = agg;

  const canvas = el<HTMLCanvasElement>('rptTrafficChart');
  if (!canvas || typeof Chart === 'undefined') return;
  if (trafficChart) {
    trafficChart.destroy();
    trafficChart = null;
  }
  const s = summary || {};
  const sub = downsample(rows);
  const span = spanOf(sub);
  const labels = sub.map((r) => chartLabel(r.ts, span));

  const sets: Record<string, unknown>[] = [
    {
      label: 'RX', data: sub.map((r) => +(+r.rx_mbps).toFixed(3)),
      borderColor: RX_LINE, backgroundColor: RX_FILL,
      borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
    },
    {
      label: 'TX', data: sub.map((r) => +(+r.tx_mbps).toFixed(3)),
      borderColor: TX_LINE, backgroundColor: TX_FILL,
      borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
    },
  ];

  const first = sub[0];
  if (agg && sub.length && first && first.rx_max_mbps != null) {
    sets.push({
      label: 'Peak RX in bucket',
      data: sub.map((r) => +(+(r.rx_max_mbps as number)).toFixed(3)),
      borderColor: 'rgba(56,189,248,.35)', borderDash: [3, 3], borderWidth: 1,
      pointRadius: 0, tension: 0.2, fill: false,
    });
    sets.push({
      label: 'Peak TX in bucket',
      data: sub.map((r) => +(+(r.tx_max_mbps as number)).toFixed(3)),
      borderColor: 'rgba(52,211,153,.35)', borderDash: [3, 3], borderWidth: 1,
      pointRadius: 0, tension: 0.2, fill: false,
    });
  }

  if (showCapacity() && s.capacityDownMbps && labels.length) {
    sets.push({
      label: 'Capacity RX (' + s.capacityDownMbps + ' Mbps)',
      data: labels.map(() => s.capacityDownMbps),
      borderColor: 'rgba(148,163,190,.55)', borderDash: [6, 4], borderWidth: 1,
      pointRadius: 0, fill: false,
    });
    // A second line only when the link is ASYMMETRIC. Two identical dashed lines
    // on a symmetric link just thicken one of them.
    if (s.capacityUpMbps && s.capacityUpMbps !== s.capacityDownMbps) {
      sets.push({
        label: 'Capacity TX (' + s.capacityUpMbps + ' Mbps)',
        data: labels.map(() => s.capacityUpMbps),
        borderColor: 'rgba(148,163,190,.35)', borderDash: [2, 4], borderWidth: 1,
        pointRadius: 0, fill: false,
      });
    }
  }

  trafficChart = new Chart(canvas, {
    type: 'line',
    data: { labels, datasets: sets },
    options: baseOptions({
      plugins: {
        legend: {
          display: true,
          labels: { color: 'rgba(148,163,190,.7)', font: MONO, boxWidth: 12 },
        },
        tooltip: {
          callbacks: {
            label: (ctx: { dataset: { label: string }; parsed: { y: number } }) =>
              ' ' + ctx.dataset.label + ': ' + fmtMbps(ctx.parsed.y),
          },
        },
      },
      scales: {
        x: { ticks: AXIS_TICK, grid: GRID },
        y: {
          beginAtZero: true,
          ticks: {
            color: 'rgba(148,163,190,.5)', font: MONO,
            callback: (v: number) => fmtMbps(v),
          },
          grid: GRID,
        },
      },
    }),
  });
}

/**
 * The download/upload volume chart.
 *
 * VOLUME ONLY — no capacity line. A capacity in Mbps means nothing against an
 * axis of megabytes per bucket, so it lives on the rate chart where the units
 * line up.
 *
 * The faint dashed pair is the same idea as the rate chart's, one level down: an
 * aggregated point is a bucket SUM, so the busiest minute inside it is invisible
 * without them.
 */
export function renderBandwidthChart(rows: BandwidthRow[], agg: string): void {
  const canvas = el<HTMLCanvasElement>('rptBandwidthChart');
  if (!canvas || typeof Chart === 'undefined') return;
  if (bandwidthChart) {
    bandwidthChart.destroy();
    bandwidthChart = null;
  }
  const sub = downsample(rows);
  const span = spanOf(sub);
  const labels = sub.map((r) => chartLabel(r.ts, span));

  const sets: Record<string, unknown>[] = [
    {
      label: 'Download', data: sub.map((r) => +(+r.rx_mb).toFixed(3)),
      borderColor: RX_LINE, backgroundColor: RX_FILL,
      borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
    },
    {
      label: 'Upload', data: sub.map((r) => +(+r.tx_mb).toFixed(3)),
      borderColor: TX_LINE, backgroundColor: TX_FILL,
      borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
    },
  ];

  const first = sub[0];
  if (agg && sub.length && first && first.rx_max_mb != null) {
    sets.push({
      label: 'Busiest minute ↓',
      data: sub.map((r) => +(+(r.rx_max_mb as number)).toFixed(3)),
      borderColor: 'rgba(56,189,248,.35)', borderDash: [3, 3], borderWidth: 1,
      pointRadius: 0, tension: 0.2, fill: false,
    });
    sets.push({
      label: 'Busiest minute ↑',
      data: sub.map((r) => +(+(r.tx_max_mb as number)).toFixed(3)),
      borderColor: 'rgba(52,211,153,.35)', borderDash: [3, 3], borderWidth: 1,
      pointRadius: 0, tension: 0.2, fill: false,
    });
  }

  bandwidthChart = new Chart(canvas, {
    type: 'line',
    data: { labels, datasets: sets },
    options: baseOptions({
      plugins: {
        legend: {
          display: true,
          labels: { color: 'rgba(148,163,190,.7)', font: MONO, boxWidth: 12 },
        },
        tooltip: {
          callbacks: {
            label: (ctx: { dataset: { label: string }; parsed: { y: number } }) =>
              ' ' + ctx.dataset.label + ': ' + fmtDataMB(ctx.parsed.y),
          },
        },
      },
      scales: {
        x: { ticks: AXIS_TICK, grid: GRID },
        y: {
          beginAtZero: true,
          ticks: {
            color: 'rgba(148,163,190,.5)', font: MONO,
            callback: (v: number) => fmtDataMB(v),
          },
          grid: GRID,
        },
      },
    }),
  });
}
