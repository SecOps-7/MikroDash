/**
 * The traffic history panel inside the Interfaces resource modal (#59).
 *
 * ── IT HANGS OFF THE DIALOG THAT ALREADY OPENS ──────────────────────────────
 *
 * Clicking an interface row already opens the resource dialog, which has an
 * extras slot under its fields. A second modal beside it would mean a second
 * thing to close and two places an interface can be inspected from. So this
 * registers in the slot and appears in the dialog the operator already opened.
 *
 * ── AND IT RENDERS FOR A READ-ONLY VIEWER ───────────────────────────────────
 *
 * Reading history is a read. The slot used to be skipped whenever the form was
 * read-only, which is right for an extra offering a write and wrong for this
 * one; `ResourceExtra.render` now takes the context and each extra decides.
 *
 * ── AN EMPTY CHART AND A SWITCHED-OFF INTERFACE LOOK IDENTICAL ──────────────
 *
 * Which is the whole reason the reply carries `recorded`. Without it an
 * interface nobody chose to record draws a blank chart that reads as a fault,
 * and the operator goes looking for a broken collector instead of a switch.
 */

import { registerExtra } from '../resource';
import { el, esc, fmtDataMB, fmtMbps } from '../dom';

declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

interface ChartLike { destroy(): void }

interface HistoryRow { ts: number; rx_mbps: number; tx_mbps: number }

interface HistoryReply {
  ok?: boolean;
  rows?: HistoryRow[];
  resolution?: string;
  recorded?: boolean;
  mayRecord?: boolean;
  recordedIfaces?: string[];
  defaultIf?: string;
  rxTotalMb?: number;
  txTotalMb?: number;
  rxMaxMbps?: number | null;
  txMaxMbps?: number | null;
}

/** The ranges offered. The server owns which aggregation each is drawn at;
 *  these are only the labels and the keys it accepts. */
const RANGES: Array<{ key: string; label: string }> = [
  { key: '1h', label: '1 hour' },
  { key: '24h', label: '24 hours' },
  { key: '7d', label: '7 days' },
  { key: '30d', label: '30 days' },
];

const RANGE_KEY = 'md.ifaceHistoryRange';

let activeID = '';
let iface = '';
let range = '24h';
let chart: ChartLike | null = null;
let last: HistoryReply | null = null;

/** Remembered per browser, because an operator working in 7-day views wants the
 *  next interface in a 7-day view too. An unknown stored value falls back. */
function storedRange(): string {
  try {
    const v = localStorage.getItem(RANGE_KEY);
    if (v && RANGES.some((r) => r.key === v)) return v;
  } catch { /* storage disabled: the default is correct */ }
  return '24h';
}

function rememberRange(v: string): void {
  try { localStorage.setItem(RANGE_KEY, v); } catch { /* ignore */ }
}

function destroyChart(): void {
  if (chart) { chart.destroy(); chart = null; }
}

/**
 * The stat line: totals for the range, and the PEAK RATE.
 *
 * The peak needs no unit noun, unlike the Reports volume peak: an hour row
 * stores the largest MINUTE's rate (`rx_max_mbps`), so a rate peak survives
 * compaction exactly and means the same thing at every range.
 */
function stats(d: HistoryReply): string {
  const box = (v: string, l: string, cls: string): string =>
    '<div class="ifh-stat"><div class="ifh-stat-val ' + cls + '">' + esc(v) + '</div>' +
    '<div class="ifh-stat-lbl">' + esc(l) + '</div></div>';
  return '<div class="ifh-stats">' +
    box(fmtDataMB(d.rxTotalMb), 'Downloaded', 'ifh-rx') +
    box(fmtDataMB(d.txTotalMb), 'Uploaded', 'ifh-tx') +
    box(d.rxMaxMbps == null ? '—' : fmtMbps(d.rxMaxMbps), 'Peak down', 'ifh-rx') +
    box(d.txMaxMbps == null ? '—' : fmtMbps(d.txMaxMbps), 'Peak up', 'ifh-tx') +
    '</div>';
}

function rangeBar(): string {
  return '<div class="ifh-ranges">' + RANGES.map((r) =>
    '<button type="button" class="ifh-range' + (r.key === range ? ' active' : '') +
    '" data-ifh-range="' + esc(r.key) + '">' + esc(r.label) + '</button>').join('') +
    '</div>';
}

/**
 * The body under the range bar: a chart, or why there is not one.
 *
 * ── ROWS FIRST, AND THAT ORDER WAS WRONG THE FIRST TIME ─────────────────────
 *
 * This asked `recorded` before it asked whether there was anything to draw,
 * which hides a chart from an interface that USED to be recorded. Found live:
 * ether3 came back `recorded:false` with an hour row inside the 30-day range,
 * because the hourly rollups outlive the switch being turned off. The history
 * is real and the panel was refusing to show it.
 *
 * So: draw whatever there is, and say separately that nothing new is being
 * added. The explanation and the switch are for when there is nothing at all.
 */
function body(d: HistoryReply | null): string {
  if (!d) return '<div class="ifh-note">Loading&#8230;</div>';
  const has = !!(d.rows && d.rows.length);
  if (has) {
    return '<div class="ifh-chart"><canvas id="ifhChart" height="150"></canvas></div>' +
      stats(d) +
      (d.recorded ? '' :
        '<div class="ifh-note ifh-note-dim">Recording is off for this interface, so this ' +
        'is the history kept from when it was on.' +
        (d.mayRecord
          ? ' <button type="button" class="btn btn-sm ifh-record">Record it again</button>'
          : '') + '</div>');
  }
  if (!d.recorded) {
    // NOT AN ERROR, AND IT SAYS SO. The switch is offered only to somebody who
    // may change the router record; everybody else is told the state and who
    // can change it, rather than shown a control that would be refused.
    return '<div class="ifh-note">' +
      '<div class="ifh-note-title">This interface is not being recorded</div>' +
      '<div>History is kept for ' +
      (d.defaultIf ? 'the WAN interface (' + esc(d.defaultIf) + ')' : 'the WAN interface') +
      ' and any interface switched on here. Recording one costs a row a minute ' +
      'on disk and no extra connection to the router.</div>' +
      (d.mayRecord
        ? '<button type="button" class="btn btn-sm ifh-record">Record this interface</button>'
        : '<div class="ifh-note-dim">An administrator can switch it on.</div>') +
      '</div>';
  }
  return '<div class="ifh-note">No traffic recorded in this range yet.</div>';
}

function paint(): void {
  const host = el('ifhBody');
  if (host) host.innerHTML = body(last);
  destroyChart();
  if (!last || !last.recorded || !last.rows || !last.rows.length) return;
  const canvas = el<HTMLCanvasElement>('ifhChart');
  if (!canvas || typeof Chart === 'undefined') return;
  const rows = last.rows;
  chart = new Chart(canvas, {
    type: 'line',
    data: {
      labels: rows.map((r) => tick(r.ts)),
      datasets: [
        {
          label: 'RX', data: rows.map((r) => +(+r.rx_mbps).toFixed(3)),
          // THE FIXED COLOURS, read from the theme rather than written here:
          // Rx is --accent-rx and Tx is --accent-tx everywhere in this app.
          borderColor: cssVar('--accent-rx'), backgroundColor: 'rgba(56,189,248,.12)',
          borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
        },
        {
          label: 'TX', data: rows.map((r) => +(+r.tx_mbps).toFixed(3)),
          borderColor: cssVar('--accent-tx'), backgroundColor: 'rgba(74,222,128,.12)',
          borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
        },
      ],
    },
    options: {
      responsive: true, maintainAspectRatio: false, animation: false,
      interaction: { mode: 'index', intersect: false },
      plugins: { legend: { display: true, labels: { boxWidth: 10, font: { size: 10 } } } },
      scales: {
        x: { ticks: { maxTicksLimit: 6, font: { size: 9 } }, grid: { display: false } },
        y: {
          beginAtZero: true, ticks: { font: { size: 9 } },
          title: { display: true, text: 'Mbps' },
        },
      },
    },
  });
}

function cssVar(name: string): string {
  try {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim() || '#888';
  } catch { return '#888'; }
}

/** A short axis label. The range decides what is worth showing: inside a day
 *  the clock, beyond it the date. */
function tick(ts: number): string {
  const d = new Date(ts);
  const p2 = (n: number): string => (n < 10 ? '0' + n : String(n));
  if (range === '1h' || range === '24h') return p2(d.getHours()) + ':' + p2(d.getMinutes());
  return p2(d.getDate()) + '/' + p2(d.getMonth() + 1);
}

function load(): void {
  if (!activeID || !iface) return;
  const want = iface;
  fetch('/api/interfaces/history?routerId=' + encodeURIComponent(activeID) +
        '&interface=' + encodeURIComponent(iface) +
        '&range=' + encodeURIComponent(range), { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((j: HistoryReply | null) => {
      // The dialog may have moved to another interface while this was in flight.
      if (want !== iface) return;
      last = j && j.ok ? j : null;
      paint();
    })
    .catch(() => { last = null; paint(); });
}

/**
 * Switch recording on for this interface, through the ROUTER record — which is
 * where the setting lives and where its permission is enforced. The whole list
 * is sent because the field is an array; `recordedIfaces` came back with the
 * reply for exactly that.
 */
function startRecording(): void {
  if (!last || !last.mayRecord || !activeID || !iface) return;
  const next = (last.recordedIfaces || []).slice();
  if (!next.includes(iface)) next.push(iface);
  const btn = document.querySelector('.ifh-record') as HTMLButtonElement | null;
  if (btn) { btn.disabled = true; btn.textContent = 'Switching on…'; }
  fetch('/api/routers/' + encodeURIComponent(activeID), {
    method: 'PUT', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ recordedIfaces: next }),
  })
    .then((r) => (r.ok ? r.json() : null))
    .then((j) => {
      if (!j || j.ok === false) {
        if (btn) { btn.disabled = false; btn.textContent = 'Record this interface'; }
        return;
      }
      // Rows begin at the next minute boundary, so the chart would stay empty
      // for up to a minute. Say that, rather than redraw an empty chart that
      // looks like the switch did nothing.
      const host = el('ifhBody');
      if (host) {
        host.innerHTML = '<div class="ifh-note"><div class="ifh-note-title">Recording</div>' +
          '<div>The first sample is written at the next minute boundary.</div></div>';
      }
    })
    .catch(() => {
      if (btn) { btn.disabled = false; btn.textContent = 'Record this interface'; }
    });
}

export function initInterfaceHistory(socket: {
  on(ev: 'router:active', fn: (d: { activeId?: string }) => void): void;
}): void {
  range = storedRange();
  fetch('/api/routers', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((j) => { if (j && j.activeId) activeID = String(j.activeId); })
    .catch(() => { /* the panel reports it has nothing rather than throwing */ });
  socket.on('router:active', (d) => { activeID = (d && d.activeId) || activeID; });

  registerExtra('iface', {
    render(mode, ctx) {
      // An ADD form has no interface to have a history for.
      if (mode === 'add' || !ctx.identity) return '';
      iface = ctx.identity;
      last = null;
      return '<div class="ifh">' +
        '<div class="ifh-head"><div class="ifh-title">Traffic history</div>' +
        rangeBar() + '</div>' +
        '<div id="ifhBody"><div class="ifh-note">Loading&#8230;</div></div>' +
        '</div>';
    },
    wire() {
      document.querySelectorAll('[data-ifh-range]').forEach((b) => {
        b.addEventListener('click', () => {
          const want = (b as HTMLElement).getAttribute('data-ifh-range') || '24h';
          if (want === range) return;
          range = want;
          rememberRange(range);
          document.querySelectorAll('[data-ifh-range]').forEach((o) => {
            o.classList.toggle('active',
              (o as HTMLElement).getAttribute('data-ifh-range') === range);
          });
          last = null;
          paint();
          load();
        });
      });
      const host = el('ifhBody');
      if (host) {
        host.addEventListener('click', (e) => {
          const t = e.target as HTMLElement | null;
          if (t && t.classList.contains('ifh-record')) startRecording();
        });
      }
      load();
    },
  });
}
