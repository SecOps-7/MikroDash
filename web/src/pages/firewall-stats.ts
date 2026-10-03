/**
 * The firewall rule dialog's Statistics tab: the rule's live counters and a
 * rate graph, as WinBox shows them (operator request, 2026-10-03).
 *
 * ── NO READ OF ITS OWN ──────────────────────────────────────────────────────
 *
 * The Firewall page already refreshes the table on screen every poll, whole
 * rows and counters included (internal/collect/firewall.go), and the rule being
 * edited is always a row of that table: it was clicked there. So the graph is
 * fed from the page's own `firewall:update`, and opening it costs the router
 * nothing. The price is the cadence: a point per Firewall poll, the interval
 * set in Settings, where WinBox ticks every second.
 *
 * ── IT MOVES AS THE DASHBOARD'S BANDWIDTH GRAPH DOES ────────────────────────
 *
 * The operator's ask (2026-10-03), and the same pieces: a frame loop advances
 * the time axis against the ESTIMATED SERVER TIME and readings only append
 * points, the right edge trails "now" by one reading interval so the line
 * always reaches it, the y axis eases toward its new top rather than jumping,
 * and the labels are clock times drawn by the Dashboard's own tick plugin
 * (pages/dashboard-traffic.ts, pages/dashboard-traffic-buffer.ts). What
 * differs is the interval: a reading per Firewall poll rather than a second, so
 * the right buffer is measured from these readings, and the window is wider.
 *
 * ── A RATE NEEDS TWO READINGS ───────────────────────────────────────────────
 *
 * Bits and packets per second are the difference between two readings over the
 * time between them, by the payload's own timestamp. A counter that went DOWN
 * (`reset-counters`, or the rule recreated under the same id) is not a negative
 * rate; that reading starts over.
 */

import { registerExtra } from '../resource';
import { el, fmtBytes, fmtBps } from '../dom';
import { fmtTime } from '../timefmt';
import { trafficTickPlugin } from './dashboard-traffic';
import { axisWindow, easeBuffer, smoothMax, smoothOffset, KEEPALIVE_SLACK_MS } from './dashboard-traffic-buffer';
import type { FirewallPayload, FirewallRule } from '../gen/payloads';

declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

interface XY { x: number; y: number }

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { datasets: Array<{ data: XY[] }> };
  options: { scales: { x: { min?: number; max?: number }; y: { max?: number }; y1: { max?: number } } };
}

/** The payload table each firewall resource's rows come from. */
const TABLE: Record<string, keyof FirewallPayload> = {
  fwFilter: 'filter', fwNat: 'nat', fwMangle: 'mangle', fwRaw: 'raw',
  fwFilter6: 'filter6', fwNat6: 'nat6', fwMangle6: 'mangle6', fwRaw6: 'raw6',
};

export interface Reading { t: number; bytes: number; packets: number }
export interface RatePoint { t: number; bps: number; pps: number }

/**
 * The rate between two readings, or null when there is none to give: no time
 * between them, or a counter that went backwards and so started over.
 */
export function rateBetween(prev: Reading, cur: Reading): RatePoint | null {
  const dt = (cur.t - prev.t) / 1000;
  if (dt <= 0 || cur.bytes < prev.bytes || cur.packets < prev.packets) return null;
  return { t: cur.t, bps: ((cur.bytes - prev.bytes) * 8) / dt, pps: (cur.packets - prev.packets) / dt };
}

/**
 * The gap to hold open at the right edge: the reading interval, measured.
 *
 * The Dashboard's `rightBufferFor` caps at 2.5 s because its samples are a
 * second apart; a reading here is a Firewall poll apart, often 10 s, and a
 * capped buffer would let the edge run past the newest point and the line stop
 * short of it every interval. So the 90th percentile of the recent gaps, the
 * Dashboard's own statistic, uncapped, and the poll interval until there are
 * gaps to measure.
 */
export function readingInterval(times: readonly number[], pollMs: number): number {
  const gaps: number[] = [];
  for (let i = Math.max(1, times.length - 20); i < times.length; i += 1) {
    const g = times[i]! - times[i - 1]!;
    if (g > 0) gaps.push(g);
  }
  if (gaps.length < 3) return Math.max(1000, pollMs || 10000);
  gaps.sort((a, b) => a - b);
  return gaps[Math.min(gaps.length - 1, Math.floor(gaps.length * 0.9))]!;
}

/** The window, in seconds: about thirty readings, between one and ten minutes. */
export function windowSecsFor(intervalMs: number): number {
  return Math.min(600, Math.max(60, Math.round((intervalMs * 30) / 1000)));
}

let open: { key: string; id: string } | null = null;
let last: Reading | null = null;
let times: number[] = [];
let pollMs = 0;
let lastSampleTs = 0;
let serverOffset = 0;
let rbCurrent = 0;
let bpsMaxCurrent = 0;
let ppsMaxCurrent = 0;
let lastTickMs = 0;
let keepaliveId = 0;
let chart: ChartLike | null = null;
let current: () => Partial<FirewallPayload> = () => ({});

function dialogOpen(): boolean {
  const m = document.getElementById('resModal');
  return !!m && m.classList.contains('open') && !!el('fwsBody');
}

function stop(): void {
  open = null;
  last = null;
  times = [];
  lastSampleTs = 0;
  rbCurrent = 0;
  bpsMaxCurrent = 0;
  ppsMaxCurrent = 0;
  if (keepaliveId) { cancelAnimationFrame(keepaliveId); keepaliveId = 0; }
  if (chart) { chart.destroy(); chart = null; }
}

function ruleOf(d: Partial<FirewallPayload>): FirewallRule | null {
  if (!open) return null;
  const rows = d[TABLE[open.key]!] as FirewallRule[] | null | undefined;
  return (rows || []).find((r) => r.id === open!.id) || null;
}

/** A firewall payload arrived: take this rule's reading out of it. */
export function feedFirewallStats(d: Partial<FirewallPayload>): void {
  if (!open) return;
  if (!dialogOpen()) { stop(); return; }
  const r = ruleOf(d);
  if (!r || !d.ts) { paintReadouts(null, null); return; }
  pollMs = d.pollMs || pollMs;
  const cur: Reading = { t: d.ts, bytes: r.bytes, packets: r.packets };
  if (last && cur.t <= last.t) return; // the same reading again
  const p = last ? rateBetween(last, cur) : null;
  last = cur;
  times.push(cur.t);
  if (times.length > 40) times.splice(0, times.length - 40);
  lastSampleTs = cur.t;
  serverOffset = smoothOffset(serverOffset, cur.t - Date.now());
  if (!chart) makeChart();
  if (p && chart) {
    chart.data.datasets[0]!.data.push({ x: p.t, y: p.bps });
    chart.data.datasets[1]!.data.push({ x: p.t, y: p.pps });
  }
  paintReadouts(r, p);
  // Scale advance and rendering are the keepalive's job, as on the Dashboard.
  if (!keepaliveId) keepaliveId = requestAnimationFrame(keepaliveTick);
}

/** The live readouts, as the Dashboard's legend shows RX and TX. */
function paintReadouts(r: FirewallRule | null, p: RatePoint | null): void {
  if (!r) {
    const host = el('fwsGone');
    if (host) host.style.display = '';
    return;
  }
  const rate = el('fwsRate');
  if (rate && p) rate.textContent = fmtBps(p.bps);
  const pkt = el('fwsPps');
  if (pkt && p) pkt.textContent = Math.round(p.pps).toLocaleString() + ' p/s';
  // The tab's own Bytes and Packets boxes, which held the values at opening,
  // kept live.
  const box = (name: string): HTMLInputElement | null =>
    document.querySelector<HTMLInputElement>('#resModal [data-res-field="' + name + '"] input');
  const b = box('bytes');
  if (b) b.value = fmtBytes(r.bytes) + ' (' + r.bytes.toLocaleString() + ')';
  const k = box('packets');
  if (k) k.value = r.packets.toLocaleString();
}

/**
 * The frame loop: advance the window to the estimated server time, prune what
 * scrolled off, ease the y axes. Throttled to ~30 fps as the Dashboard's is,
 * and idle while the Statistics tab is not the one on screen.
 */
function keepaliveTick(): void {
  if (!dialogOpen()) { stop(); return; }
  keepaliveId = requestAnimationFrame(keepaliveTick);
  const canvas = el('fwsChart');
  if (!chart || !lastSampleTs || document.hidden || !canvas || canvas.offsetParent === null) return;
  const now = Date.now();
  if (now - lastTickMs < 33) return;
  const elapsed = lastTickMs ? now - lastTickMs : 0;
  lastTickMs = now;
  const target = readingInterval(times, pollMs);
  rbCurrent = rbCurrent ? easeBuffer(rbCurrent, target, elapsed) : target;
  const win = axisWindow(now + serverOffset, windowSecsFor(target), rbCurrent);
  const bps = chart.data.datasets[0]!.data, pps = chart.data.datasets[1]!.data;
  // Shifted together: two datasets of one reading stream.
  while (bps.length && bps[0]!.x < win.min - KEEPALIVE_SLACK_MS - target) { bps.shift(); pps.shift(); }
  let bMax = 0, pMax = 0;
  for (const q of bps) if (q.y > bMax) bMax = q.y;
  for (const q of pps) if (q.y > pMax) pMax = q.y;
  // smoothMax's floor is 1, sized for megabits; these are bits and packets.
  bpsMaxCurrent = bpsMaxCurrent ? smoothMax(bpsMaxCurrent, (bMax || 1000) * 1.1) : (bMax || 1000) * 1.1;
  ppsMaxCurrent = ppsMaxCurrent ? smoothMax(ppsMaxCurrent, (pMax || 1) * 1.1) : (pMax || 1) * 1.1;
  chart.options.scales.x.min = win.min;
  chart.options.scales.x.max = win.max;
  chart.options.scales.y.max = bpsMaxCurrent;
  chart.options.scales.y1.max = ppsMaxCurrent;
  chart.update('none');
}

function cssVar(name: string): string {
  try {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888';
  } catch { return '#888'; }
}

const mono = "'JetBrains Mono',monospace";

function makeChart(): void {
  const canvas = el<HTMLCanvasElement>('fwsChart');
  if (!canvas || typeof Chart === 'undefined') return;
  const now = Date.now();
  chart = new Chart(canvas, {
    type: 'line',
    // The Dashboard's tick plugin: grid lines and clock-time labels at fixed
    // pixel positions, read from the axis rather than the data.
    plugins: [trafficTickPlugin],
    data: {
      datasets: [
        {
          // Not Rx or Tx: a rule's rate has no direction, so neither fixed
          // colour; the accent and the alternate accent instead.
          label: 'Rate', data: [], yAxisID: 'y',
          borderColor: cssVar('--accent-rx'), backgroundColor: 'rgba(56,189,248,.08)',
          borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true,
        },
        {
          label: 'Packet rate', data: [], yAxisID: 'y1',
          borderColor: cssVar('--accent-alt'), borderWidth: 1.5, tension: 0.3, pointRadius: 0,
        },
      ],
    },
    options: {
      responsive: true, maintainAspectRatio: false,
      devicePixelRatio: Math.min(window.devicePixelRatio, 1.5),
      animation: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: 'rgba(7,9,15,.9)', borderColor: 'rgba(99,130,190,.2)', borderWidth: 1,
          titleFont: { family: mono, size: 11 }, bodyFont: { family: mono, size: 11 },
          callbacks: {
            title: (items: Array<{ parsed: { x: number } }>): string =>
              items[0] ? fmtTime(items[0].parsed.x) : '',
            label: (i: { datasetIndex: number; parsed: { y: number } }): string =>
              i.datasetIndex === 0 ? ' Rate: ' + fmtBps(i.parsed.y)
                : ' Packet rate: ' + Math.round(i.parsed.y).toLocaleString() + ' p/s',
          },
        },
      },
      scales: {
        x: {
          type: 'linear', display: true,
          min: now - 60000, max: now,
          grid: { display: false, drawBorder: false },
          border: { display: false },
          ticks: { display: false },
          afterFit: (sc: { height: number }) => { sc.height = 26; },
        },
        y: {
          beginAtZero: true, position: 'left',
          grid: { color: 'rgba(99,130,190,.07)' },
          ticks: { color: 'rgba(148,163,190,.4)', font: { family: mono, size: 10 },
            callback: (v: number): string => fmtBps(v) },
        },
        y1: {
          beginAtZero: true, position: 'right', grid: { drawOnChartArea: false },
          // Whole packets only. The eased top of the axis is a fraction, and
          // was labelled "11.011011011011012 p/s".
          ticks: { color: 'rgba(148,163,190,.4)', font: { family: mono, size: 10 }, precision: 0,
            callback: (v: number): string => (Number.isInteger(v) ? v + ' p/s' : '') },
        },
      },
    },
  });
}

/** Register the Statistics extra on every firewall resource. `getLast` is the
 *  page's latest payload, so the dialog opens on a reading rather than a blank. */
export function initFirewallStats(getLast: () => Partial<FirewallPayload>): void {
  current = getLast;
  for (const key of Object.keys(TABLE)) {
    registerExtra(key, {
      tab: 'Statistics',
      render(mode, ctx) {
        stop();
        if (mode === 'add' || !ctx.id) return '';
        open = { key, id: ctx.id };
        return '<div class="fws"><div id="fwsBody">' +
          '<div class="chart-legend">' +
          '<div class="legend-item"><div class="legend-dot" style="background:var(--accent-rx)"></div>' +
          '<span style="color:var(--text-muted)">Rate</span><span class="legend-rate" id="fwsRate">-</span></div>' +
          '<div class="legend-item"><div class="legend-dot" style="background:var(--accent-alt)"></div>' +
          '<span style="color:var(--text-muted)">Packet rate</span><span class="legend-rate" id="fwsPps">-</span></div>' +
          '</div>' +
          '<div class="ifh-note" id="fwsGone" style="display:none">This rule is not in the table on screen any more.</div>' +
          '<div class="fws-chart"><canvas id="fwsChart"></canvas></div></div></div>';
      },
      // DEFERRED: the dialog wires its extra before it opens, and a reading
      // taken then saw a closed dialog and stopped the feed for good.
      wire() { setTimeout(() => feedFirewallStats(current()), 0); },
    });
  }
}
