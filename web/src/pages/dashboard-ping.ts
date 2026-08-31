// The Dashboard's latency block: the round-trip figures and the bar chart.
//
// ── THREE THRESHOLDS, USED TWICE, IN DIFFERENT UNITS ────────────────────────
//
// 50ms and 150ms split good from fair from bad. `rttClass` returns a CSS class
// for the numbers and `pingColor` returns an rgba string for the chart bars —
// the same two boundaries expressed twice, because one styles text and the other
// paints a canvas. They are kept as two functions, as the original has them,
// rather than merged behind a shared table: a table would suggest the two could
// be changed together, and the stylesheet owns one of them.
//
// ── LOSS HAS ITS OWN SCALE, AND IT IS NOT THE RTT ONE ───────────────────────
//
// Zero is ok, under 50% is warn, the rest is bad. So 49% packet loss renders in
// the same colour as a 60ms round trip. That is the live behaviour.
//
// ── A REFUSAL IS NOT A BAD READING ──────────────────────────────────────────
//
// `permissionDenied` means the RouterOS API user lacks the `test` policy, so the
// card shows `N/A` with an explanation on hover rather than a zero or a dash —
// the distinction between "the link is bad" and "we were not allowed to look".

import { el } from '../dom';
import { notePayload } from '../stale';

export interface PingPoint {
  ts?: number;
  rtt?: number | null;
  loss?: number | null;
}
export interface PingPayload {
  target?: string;
  rtt?: number | null;
  loss?: number | null;
  minRtt?: number | null;
  maxRtt?: number | null;
  enabled?: boolean;
  permissionDenied?: boolean;
  ts?: number;
}
export interface PingHistoryPayload {
  target?: string;
  history?: PingPoint[];
  minRtt?: number | null;
  maxRtt?: number | null;
}

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { labels: string[]; datasets: { data: (number | null)[]; backgroundColor: string[] }[] };
}
declare const Chart: undefined | (new (canvas: HTMLElement, cfg: unknown) => ChartLike);

const MAX_PING_HIST = 60;
let pingHistory: PingPoint[] = [];
let pingChart: ChartLike | null = null;

/** The CSS class for a round-trip figure. Absent is unclassified, not bad. */
export function rttClass(rtt: number | null | undefined): string {
  if (rtt == null) return '';
  if (rtt < 50) return 'ping-ok';
  if (rtt < 150) return 'ping-warn';
  return 'ping-bad';
}

/** The bar colour for a round-trip figure. A timeout is drawn grey, not red. */
export function pingColor(rtt: number | null | undefined): string {
  if (rtt == null) return 'rgba(148,163,190,.4)';
  if (rtt < 50) return 'rgba(74,222,128,.8)';
  if (rtt < 150) return 'rgba(251,146,60,.8)';
  return 'rgba(248,113,113,.8)';
}

export function pingChartConfig(): unknown {
  return {
    type: 'bar',
    data: { labels: [], datasets: [{ data: [], backgroundColor: [], borderRadius: 2, borderSkipped: false }] },
    options: {
      responsive: true, maintainAspectRatio: false,
      // No animation: bars arrive one per second and an eased transition would
      // still be running when the next one lands.
      animation: false,
      plugins: {
        legend: { display: false },
        tooltip: {
          callbacks: {
            // `raw == null` is a TIMEOUT, and it says so rather than showing
            // "null ms" or an empty tooltip.
            label: (c: { raw: number | null }) => (c.raw == null ? 'timeout' : c.raw + 'ms'),
          },
        },
      },
      scales: {
        x: { display: false },
        y: {
          display: true, min: 0,
          grid: { color: 'rgba(99,130,190,.08)' },
          ticks: {
            color: 'rgba(148,163,190,.5)', font: { size: 9 }, maxTicksLimit: 3,
            callback: (v: number) => v + 'ms',
          },
        },
      },
    },
  };
}

function makePingChart(canvasId: string): ChartLike | null {
  const ctx = el(canvasId);
  if (!ctx || typeof Chart === 'undefined') return null;
  return new Chart(ctx, pingChartConfig());
}

export function updatePingChart(chart: ChartLike | null, history: PingPoint[]): void {
  if (!chart) return;
  // The LAST FIFTY, where the history holds sixty: the chart is narrower than
  // the buffer, and the extra ten are what a viewer scrolls back to see in the
  // tooltip rather than what is drawn.
  const pts = history.slice(-50);
  chart.data.labels = pts.map(() => '');
  chart.data.datasets[0]!.data = pts.map((p) => (p.rtt == null ? null : p.rtt));
  chart.data.datasets[0]!.backgroundColor = pts.map((p) => pingColor(p.rtt));
  chart.update('none');
}

export function renderPingUI(
  rtt: number | null | undefined, loss: number | null | undefined,
  minRtt: number | null | undefined, maxRtt: number | null | undefined,
): void {
  const rttEl = el('ndPingRtt'), lossEl = el('ndPingLoss');
  if (rttEl) {
    rttEl.textContent = rtt != null ? String(rtt) : '—';
    rttEl.className = 'ping-val ' + rttClass(rtt);
  }
  if (lossEl) {
    lossEl.textContent = loss + '%';
    // Loss has its own scale — see the header.
    lossEl.className = 'ping-val ' + (loss === 0 ? 'ping-ok' : (loss as number) < 50 ? 'ping-warn' : 'ping-bad');
  }
  const minEl = el('ndPingMin'), maxEl = el('ndPingMax');
  if (minEl) {
    minEl.textContent = minRtt != null ? String(minRtt) : '—';
    minEl.className = 'ping-val ' + rttClass(minRtt);
  }
  if (maxEl) {
    maxEl.textContent = maxRtt != null ? String(maxRtt) : '—';
    maxEl.className = 'ping-val ' + rttClass(maxRtt);
  }
  if (!pingChart) pingChart = makePingChart('pingChartNet');
  updatePingChart(pingChart, pingHistory);
}

export function onPingHistory(data: PingHistoryPayload): void {
  pingHistory = (data.history || []).slice(-MAX_PING_HIST);
  const lbl = el('pingTargetLabel');
  if (lbl && data.target) lbl.textContent = data.target;
  if (pingHistory.length) {
    const last = pingHistory[pingHistory.length - 1]!;
    renderPingUI(last.rtt, last.loss, data.minRtt, data.maxRtt);
  }
}

export function onPingUpdate(data: PingPayload): void {
  if (data.enabled === false) return; // ping switched off in settings
  // THE NETWORKS CARD'S STALE TIMER, re-armed by every ping.
  //
  // A second `ping:update` handler in the live app does only this, ~100 lines
  // before the renderer. The generated stale table has one event per card and
  // records `lan:overview` for this one, so without this the card is kept alive
  // by a payload that arrives every few MINUTES rather than every few seconds —
  // and the ping block, which sits inside that card, would go on updating
  // underneath a stale overlay.
  notePayload('networksCard');
  if (data.permissionDenied) {
    const rttEl = el('ndPingRtt'), lossEl = el('ndPingLoss');
    if (rttEl) { rttEl.textContent = '—'; rttEl.className = 'ping-val'; }
    if (lossEl) {
      lossEl.textContent = 'N/A';
      lossEl.className = 'ping-val ping-warn';
      lossEl.title = 'Add "test" policy to your RouterOS API user to enable ping';
    }
    return;
  }
  const lbl = el('pingTargetLabel');
  if (lbl && data.target) lbl.textContent = data.target;
  pingHistory.push({ ts: data.ts || Date.now(), rtt: data.rtt, loss: data.loss });
  if (pingHistory.length > MAX_PING_HIST) pingHistory.shift();
  renderPingUI(data.rtt, data.loss, data.minRtt, data.maxRtt);
}

/** A switch to another router shares no latency history. */
export function resetPing(): void {
  pingHistory = [];
}
