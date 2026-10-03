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
 * nothing. The price is the cadence: a point per Firewall poll (10 s by
 * default) where WinBox ticks every second, and the graph says so.
 *
 * ── A RATE NEEDS TWO READINGS ───────────────────────────────────────────────
 *
 * Bits and packets per second are the difference between two readings over the
 * time between them, by the payload's own timestamp. A counter that went DOWN
 * (`reset-counters`, or the rule recreated under the same id) is not a negative
 * rate; that reading starts over.
 */

import { registerExtra } from '../resource';
import { el, esc, fmtBytes, fmtBps } from '../dom';
import { fmtTime } from '../timefmt';
import type { FirewallPayload, FirewallRule } from '../gen/payloads';

declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

interface XY { x: number; y: number }

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { datasets: Array<{ data: XY[] }> };
}

/** The payload table each firewall resource's rows come from. */
const TABLE: Record<string, keyof FirewallPayload> = {
  fwFilter: 'filter', fwNat: 'nat', fwMangle: 'mangle', fwRaw: 'raw',
  fwFilter6: 'filter6', fwNat6: 'nat6', fwMangle6: 'mangle6', fwRaw6: 'raw6',
};

/** How many rate points the graph keeps: at the default 10 s poll, ten minutes. */
const KEEP = 60;

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

let open: { key: string; id: string } | null = null;
let last: Reading | null = null;
let points: RatePoint[] = [];
let pollMs = 0;
let chart: ChartLike | null = null;
let current: () => Partial<FirewallPayload> = () => ({});

function dialogOpen(): boolean {
  const m = document.getElementById('resModal');
  return !!m && m.classList.contains('open') && !!el('fwsBody');
}

function stop(): void {
  open = null;
  last = null;
  points = [];
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
  if (!r || !d.ts) { paintStats(null); return; }
  pollMs = d.pollMs || pollMs;
  const cur: Reading = { t: d.ts, bytes: r.bytes, packets: r.packets };
  if (last && cur.t <= last.t) return; // the same reading again
  const p = last ? rateBetween(last, cur) : null;
  last = cur;
  if (p) {
    points.push(p);
    if (points.length > KEEP) points.splice(0, points.length - KEEP);
  }
  paintStats(r);
  paintChart();
}

function statBox(v: string, l: string): string {
  return '<div class="ifh-stat"><div class="ifh-stat-val">' + esc(v) + '</div>' +
    '<div class="ifh-stat-lbl">' + esc(l) + '</div></div>';
}

function paintStats(r: FirewallRule | null): void {
  const host = el('fwsStats');
  if (!host) return;
  if (!r) {
    host.innerHTML = '<div class="ifh-note">This rule is not in the table on screen any more.</div>';
    return;
  }
  const p = points[points.length - 1];
  host.innerHTML =
    statBox(p ? fmtBps(p.bps) : '-', 'Rate') +
    statBox(p ? Math.round(p.pps).toLocaleString() + ' p/s' : '-', 'Packet rate');
  // The tab's own Bytes and Packets boxes, which held the values at opening,
  // kept live rather than repeated here.
  const box = (name: string): HTMLInputElement | null =>
    document.querySelector<HTMLInputElement>('#resModal [data-res-field="' + name + '"] input');
  const b = box('bytes');
  if (b) b.value = fmtBytes(r.bytes) + ' (' + r.bytes.toLocaleString() + ')';
  const k = box('packets');
  if (k) k.value = r.packets.toLocaleString();
  const note = el('fwsNote');
  if (note) {
    const every = pollMs ? Math.round(pollMs / 100) / 10 + ' s' : 'the Firewall poll interval';
    note.textContent = points.length
      ? 'A point every ' + every + ', the Firewall poll interval.'
      : 'Measuring: the first rate needs a second reading, in about ' + every + '.';
  }
}

function cssVar(name: string): string {
  try {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888';
  } catch { return '#888'; }
}

function paintChart(): void {
  const canvas = el<HTMLCanvasElement>('fwsChart');
  if (!canvas || typeof Chart === 'undefined' || !points.length) return;
  const bps = points.map((p) => ({ x: p.t, y: p.bps }));
  const pps = points.map((p) => ({ x: p.t, y: p.pps }));
  if (chart) {
    chart.data.datasets[0]!.data = bps;
    chart.data.datasets[1]!.data = pps;
    chart.update('none');
    return;
  }
  const now = (): number => points[points.length - 1]?.t || Date.now();
  chart = new Chart(canvas, {
    type: 'line',
    data: {
      datasets: [
        {
          // Not Rx or Tx: a rule's rate has no direction, so neither fixed
          // colour; the accent and the alternate accent instead.
          label: 'Rate', data: bps, yAxisID: 'y',
          borderColor: cssVar('--accent-rx'), backgroundColor: 'rgba(56,189,248,.12)',
          borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
        },
        {
          label: 'Packet rate', data: pps, yAxisID: 'y1',
          borderColor: cssVar('--accent-alt'), borderWidth: 1.5, pointRadius: 0, tension: 0.2,
        },
      ],
    },
    options: {
      responsive: true, maintainAspectRatio: false, animation: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: true, labels: { boxWidth: 10, font: { size: 10 } } },
        tooltip: {
          callbacks: {
            label: (i: { datasetIndex: number; parsed: { y: number } }): string =>
              i.datasetIndex === 0 ? 'Rate ' + fmtBps(i.parsed.y)
                : 'Packet rate ' + Math.round(i.parsed.y).toLocaleString() + ' p/s',
            title: (items: Array<{ parsed: { x: number } }>): string => {
              const x = items[0]?.parsed.x;
              return x ? fmtTime(x) : '';
            },
          },
        },
      },
      scales: {
        // Linear over milliseconds and labelled by age, as the interface
        // history graph is: this build ships no date adapter.
        x: {
          type: 'linear',
          ticks: {
            maxTicksLimit: 6, font: { size: 9 },
            callback: (v: number): string => {
              const age = Math.round((now() - v) / 1000);
              return age <= 0 ? 'now' : age < 90 ? '-' + age + 's' : '-' + Math.round(age / 60) + 'm';
            },
          },
          grid: { display: false },
        },
        y: {
          beginAtZero: true, position: 'left',
          ticks: { font: { size: 9 }, callback: (v: number): string => fmtBps(v) },
        },
        y1: {
          beginAtZero: true, position: 'right', grid: { drawOnChartArea: false },
          // Whole packets: fractional steps rounded to the same label twice.
          ticks: { font: { size: 9 }, precision: 0, callback: (v: number): string => v + ' p/s' },
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
          '<div class="ifh-stats" id="fwsStats"></div>' +
          '<div class="fws-chart"><canvas id="fwsChart"></canvas></div>' +
          '<div class="ifh-note" id="fwsNote"></div></div></div>';
      },
      // DEFERRED: the dialog wires its extra before it opens, and a reading
      // taken then saw a closed dialog and stopped the feed for good.
      wire() { setTimeout(() => feedFirewallStats(current()), 0); },
    });
  }
}
