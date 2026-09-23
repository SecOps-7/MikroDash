/**
 * The traffic panel inside the Interfaces resource modal (#59).
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
 * ── TWO SOURCES, AND A RANGE NEVER CHANGES WHICH ONE IT USES ────────────────
 *
 * The short ranges — Live, 5 min, 30 min — come from a 1 Hz buffer fed by the
 * same `ifstatus:update` the page already receives, held per interface. The
 * long ones come from the database. A range that read one place on a recorded
 * interface and another on an unrecorded one would show two different pictures
 * under one label.
 *
 * That split is what makes LIVE WORK WHEN NOTHING IS RECORDED, which is the
 * point of it: recording is about keeping history, and "what is this interface
 * doing right now" is a question the page can already answer for anything.
 *
 * The buffer fills while the Interfaces page is open and is capped at 30
 * minutes — the longest live range — by `MAX_CLIENT_POINTS`, reused from the
 * dashboard's chart rather than counted out a second time.
 *
 * ── AN EMPTY CHART AND A SWITCHED-OFF INTERFACE LOOK IDENTICAL ──────────────
 *
 * Which is the whole reason the reply carries `recorded`. Without it an
 * interface nobody chose to record draws a blank chart that reads as a fault,
 * and the operator goes looking for a broken collector instead of a switch.
 */

import { registerExtra } from '../resource';
import { el, esc, fmtDataMB, fmtMbps } from '../dom';
import { pushSample, windowedPoints } from './dashboard-traffic-buffer';
import type { Interface, TrafficPoint } from '../gen/payloads';

declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { labels: string[]; datasets: Array<{ data: number[] }> };
}

interface HistoryReply {
  ok?: boolean;
  rows?: TrafficPoint[];
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

/**
 * The ranges drawn from the LIVE buffer. `secs` is the window each shows.
 *
 * Live is first and is the default, because the question an operator opens an
 * interface to ask is almost always about now.
 */
const LIVE_RANGES: Array<{ key: string; label: string; secs: number }> = [
  { key: 'live', label: 'Live', secs: 60 },
  { key: '5m', label: '5 min', secs: 300 },
  { key: '30m', label: '30 min', secs: 1800 },
];

/** The ranges the SERVER answers. Its map of the same keys decides the
 *  aggregation each is drawn at; these are the labels and the keys it takes. */
const HISTORY_RANGES: Array<{ key: string; label: string }> = [
  { key: '1h', label: '1 hour' },
  { key: '24h', label: '24 hours' },
  { key: '7d', label: '7 days' },
  { key: '30d', label: '30 days' },
];

const RANGE_KEY = 'md.ifaceHistoryRange';

let activeID = '';
let iface = '';
let range = 'live';
let chart: ChartLike | null = null;
let last: HistoryReply | null = null;

/**
 * One 1 Hz buffer per interface, fed by the page's own payload.
 *
 * EVERY interface is buffered rather than only the open one, because a panel
 * that started filling when it opened would show Live with one point in it and
 * "30 min" with almost nothing — the window has to already exist by the time
 * the question is asked. Each is capped at MAX_CLIENT_POINTS by `pushSample`.
 */
const liveBuf = new Map<string, TrafficPoint[]>();

/** Feed the buffers. Called by the Interfaces page on every update. */
export function recordLiveSamples(ifaces: readonly Interface[]): void {
  const ts = Date.now();
  const seen = new Set<string>();
  ifaces.forEach((i) => {
    seen.add(i.name);
    let buf = liveBuf.get(i.name);
    if (!buf) { buf = []; liveBuf.set(i.name, buf); }
    pushSample(buf, { ts, rx_mbps: i.rxMbps || 0, tx_mbps: i.txMbps || 0 });
  });
  // AN INTERFACE THAT IS GONE LOSES ITS BUFFER, or a router whose VLANs come
  // and go accumulates windows nothing will ever draw.
  Array.from(liveBuf.keys()).forEach((name) => {
    if (!seen.has(name)) liveBuf.delete(name);
  });
  if (isLive(range)) repaintLive();
}

function isLive(key: string): boolean {
  return LIVE_RANGES.some((r) => r.key === key);
}

function liveWindow(): TrafficPoint[] {
  const spec = LIVE_RANGES.find((r) => r.key === range);
  const buf = liveBuf.get(iface);
  if (!spec || !buf) return [];
  // rightBufferMs 0: this chart has no leading edge to leave room for, unlike
  // the dashboard's scrolling axis.
  return windowedPoints(buf, Date.now(), spec.secs, 0);
}

/** Remembered per browser, because an operator working in 7-day views wants the
 *  next interface in a 7-day view too. An unknown stored value falls back. */
function storedRange(): string {
  try {
    const v = localStorage.getItem(RANGE_KEY);
    if (v && (isLive(v) || HISTORY_RANGES.some((r) => r.key === v))) return v;
  } catch { /* storage disabled: the default is correct */ }
  return 'live';
}

function rememberRange(v: string): void {
  try { localStorage.setItem(RANGE_KEY, v); } catch { /* ignore */ }
}

function destroyChart(): void {
  if (chart) { chart.destroy(); chart = null; }
}

/** Is the dialog still on screen? The slot keeps its markup after the modal
 *  closes, so without this a live range would redraw a hidden chart for ever. */
function dialogOpen(): boolean {
  const m = document.getElementById('resModal');
  return !!m && m.classList.contains('open') && !!el('ifhBody');
}

function statBox(v: string, l: string, cls: string): string {
  return '<div class="ifh-stat"><div class="ifh-stat-val ' + cls + '">' + esc(v) + '</div>' +
    '<div class="ifh-stat-lbl">' + esc(l) + '</div></div>';
}

/**
 * The stat line for a RECORDED range: totals, and the peak rate.
 *
 * The peak needs no unit noun, unlike the Reports volume peak: an hour row
 * stores the largest MINUTE's rate (`rx_max_mbps`), so a rate peak survives
 * compaction exactly and means the same thing at every range.
 */
function stats(d: HistoryReply): string {
  return '<div class="ifh-stats" id="ifhStats">' +
    statBox(fmtDataMB(d.rxTotalMb), 'Downloaded', 'ifh-rx') +
    statBox(fmtDataMB(d.txTotalMb), 'Uploaded', 'ifh-tx') +
    statBox(d.rxMaxMbps == null ? '—' : fmtMbps(d.rxMaxMbps), 'Peak down', 'ifh-rx') +
    statBox(d.txMaxMbps == null ? '—' : fmtMbps(d.txMaxMbps), 'Peak up', 'ifh-tx') +
    '</div>';
}

/** The LIVE stat line: the current rate and the peak inside the window.
 *
 *  NO TOTALS. A total is a quantity of bytes, and integrating a 1 Hz buffer
 *  that pauses whenever the page is hidden would invent one. The recorded
 *  ranges have minute rows behind them and can answer it honestly. */
function liveStats(pts: readonly TrafficPoint[]): string {
  const nowPt = pts[pts.length - 1];
  const maxRx = pts.reduce((a, p) => (p.rx_mbps > a ? p.rx_mbps : a), 0);
  const maxTx = pts.reduce((a, p) => (p.tx_mbps > a ? p.tx_mbps : a), 0);
  return '<div class="ifh-stats" id="ifhStats">' +
    statBox(fmtMbps(nowPt ? nowPt.rx_mbps : 0), 'Now down', 'ifh-rx') +
    statBox(fmtMbps(nowPt ? nowPt.tx_mbps : 0), 'Now up', 'ifh-tx') +
    statBox(fmtMbps(maxRx), 'Peak down', 'ifh-rx') +
    statBox(fmtMbps(maxTx), 'Peak up', 'ifh-tx') +
    '</div>';
}

function rangeBar(): string {
  const btn = (r: { key: string; label: string }): string =>
    '<button type="button" class="ifh-range' + (r.key === range ? ' active' : '') +
    '" data-ifh-range="' + esc(r.key) + '">' + esc(r.label) + '</button>';
  return '<div class="ifh-ranges">' +
    LIVE_RANGES.map(btn).join('') +
    '<span class="ifh-range-sep"></span>' +
    HISTORY_RANGES.map(btn).join('') +
    '</div>';
}

/**
 * The switch, pointing whichever way it currently should.
 *
 * THE DEFAULT INTERFACE HAS NO SWITCH. It is recorded because it is the WAN —
 * Reports, the capacity lines and the WAN badge all read that series — and the
 * resolver puts it back whatever the stored list says, so a control that
 * appeared to turn it off would lie.
 */
function recordControl(d: HistoryReply): string {
  if (!d.mayRecord) return '';
  if (d.defaultIf && d.defaultIf === iface) {
    return '<span class="ifh-note-dim">Recorded because it is this router&#39;s WAN.</span>';
  }
  return d.recorded
    ? '<button type="button" class="btn btn-sm ifh-record" data-ifh-rec="off">Stop recording</button>'
    : '<button type="button" class="btn btn-sm ifh-record" data-ifh-rec="on">Record this interface</button>';
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
 * added. The explanation is for when there is nothing at all.
 */
function body(d: HistoryReply | null): string {
  if (isLive(range)) {
    const pts = liveWindow();
    if (!pts.length) {
      return '<div class="ifh-note">Waiting for the first live sample&#8230;</div>';
    }
    return '<div class="ifh-chart"><canvas id="ifhChart" height="150"></canvas></div>' +
      liveStats(pts) +
      // The recording state is still worth saying HERE, because this is where
      // an operator is looking when they wonder why the longer ranges are
      // empty. Live itself never depends on it.
      (d ? '<div class="ifh-note ifh-note-dim">' +
        (d.recorded ? '' : 'Not recorded, so the ranges on the right have no history to draw. ') +
        recordControl(d) + '</div>' : '');
  }
  if (!d) return '<div class="ifh-note">Loading&#8230;</div>';
  if (d.rows && d.rows.length) {
    return '<div class="ifh-chart"><canvas id="ifhChart" height="150"></canvas></div>' +
      stats(d) +
      '<div class="ifh-note ifh-note-dim">' +
      (d.recorded ? '' : 'Recording is off, so this is the history kept from when it was on. ') +
      recordControl(d) + '</div>';
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
      'on disk and no extra connection to the router. Live still works either way.</div>' +
      (d.mayRecord ? recordControl(d)
        : '<div class="ifh-note-dim">An administrator can switch it on.</div>') +
      '</div>';
  }
  return '<div class="ifh-note">No traffic recorded in this range yet.' +
    '<div class="ifh-note-dim">' + recordControl(d) + '</div></div>';
}

/** A live tick: move the points without rebuilding the DOM or the chart. */
function repaintLive(): void {
  if (!dialogOpen()) return;
  const pts = liveWindow();
  if (!chart || !document.getElementById('ifhChart')) {
    paint();
    return;
  }
  const now = Date.now();
  chart.data.labels = pts.map((p) => tick(p.ts, now));
  const ds = chart.data.datasets;
  if (ds[0]) ds[0].data = pts.map((p) => +(+p.rx_mbps).toFixed(3));
  if (ds[1]) ds[1].data = pts.map((p) => +(+p.tx_mbps).toFixed(3));
  chart.update('none');
  const host = el('ifhStats');
  if (host) host.outerHTML = liveStats(pts);
}

function paint(): void {
  const host = el('ifhBody');
  if (host) host.innerHTML = body(last);
  destroyChart();
  const rows = isLive(range) ? liveWindow() : ((last && last.rows) || []);
  if (!rows.length) return;
  const canvas = el<HTMLCanvasElement>('ifhChart');
  if (!canvas || typeof Chart === 'undefined') return;
  const nowMs = Date.now();
  chart = new Chart(canvas, {
    type: 'line',
    data: {
      labels: rows.map((r) => tick(r.ts, nowMs)),
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

/**
 * A short axis label.
 *
 * ── A LIVE WINDOW IS LABELLED BY AGE, NOT BY THE CLOCK ─────────────────────
 *
 * It was `mm:ss`, and on the 60-second view that reads as a time of day: the
 * axis ran "12:59 … 13:53" for fifty-four SECONDS of traffic, which anybody
 * would take for fifty-four minutes. Found by looking at the rendered chart,
 * not by reading the code — the numbers were right and the axis was a lie.
 *
 * So a live range says how long ago each point was, and the recorded ranges
 * keep the clock and the date, where a wall-clock time is what an operator is
 * actually trying to line something up against.
 */
function tick(ts: number, now: number): string {
  const d = new Date(ts);
  const p2 = (n: number): string => (n < 10 ? '0' + n : String(n));
  if (isLive(range)) {
    const age = Math.round((now - ts) / 1000);
    if (age <= 0) return 'now';
    if (age < 90) return '-' + age + 's';
    return '-' + Math.round(age / 60) + 'm';
  }
  if (range === '1h' || range === '24h') return p2(d.getHours()) + ':' + p2(d.getMinutes());
  return p2(d.getDate()) + '/' + p2(d.getMonth() + 1);
}

/**
 * Ask the server for the history half.
 *
 * A LIVE RANGE STILL ASKS, and that is deliberate rather than wasteful: the
 * reply carries whether this interface is recorded and whether the viewer may
 * change it, which is what the note under a live chart says. One request per
 * interface opened, never per tick.
 */
function load(): void {
  if (!activeID || !iface) return;
  const want = iface;
  const ask = isLive(range) ? '1h' : range;
  fetch('/api/interfaces/history?routerId=' + encodeURIComponent(activeID) +
        '&interface=' + encodeURIComponent(iface) +
        '&range=' + encodeURIComponent(ask), { credentials: 'same-origin' })
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
 * Turn recording on or off for this interface, through the ROUTER record —
 * where the setting lives and where its permission is enforced.
 *
 * The whole list is sent because the field is an array, and it is the STORED
 * list rather than the resolved one: the resolved set always contains the
 * default interface, so sending that back would write today's WAN into the
 * array and leave it there after `defaultIf` moved.
 */
function setRecording(on: boolean): void {
  if (!last || !last.mayRecord || !activeID || !iface) return;
  const stored = (last.recordedIfaces || []).filter((n) => n !== iface);
  if (on) stored.push(iface);
  const btn = document.querySelector('.ifh-record') as HTMLButtonElement | null;
  const was = btn ? btn.textContent : '';
  if (btn) { btn.disabled = true; btn.textContent = on ? 'Switching on…' : 'Switching off…'; }
  fetch('/api/routers/' + encodeURIComponent(activeID), {
    method: 'PUT', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ recordedIfaces: stored }),
  })
    .then((r) => (r.ok ? r.json() : null))
    .then((j) => {
      if (!j || j.ok === false) {
        if (btn) { btn.disabled = false; btn.textContent = was; }
        return;
      }
      // RE-READ rather than patch `last` here: the server resolves the list,
      // and believing our own arithmetic about it is how the two drift.
      load();
    })
    .catch(() => { if (btn) { btn.disabled = false; btn.textContent = was; } });
}

export function initInterfaceHistory(socket: {
  on(ev: 'router:active', fn: (d: { activeId?: string }) => void): void;
}): void {
  range = storedRange();
  fetch('/api/routers', { credentials: 'same-origin' })
    .then((r) => (r.ok ? r.json() : null))
    .then((j) => { if (j && j.activeId) activeID = String(j.activeId); })
    .catch(() => { /* the panel reports it has nothing rather than throwing */ });
  socket.on('router:active', (d) => {
    const next = (d && d.activeId) || activeID;
    if (next !== activeID) {
      // A DIFFERENT ROUTER'S ether1 IS NOT THIS ONE'S. The buffers are keyed by
      // name, so they go with the router that filled them.
      liveBuf.clear();
      activeID = next;
    }
  });

  registerExtra('iface', {
    render(mode, ctx) {
      // An ADD form has no interface to have a history for.
      if (mode === 'add' || !ctx.identity) return '';
      iface = ctx.identity;
      last = null;
      return '<div class="ifh">' +
        '<div class="ifh-head"><div class="ifh-title">Traffic</div>' +
        rangeBar() + '</div>' +
        '<div id="ifhBody"><div class="ifh-note">Loading&#8230;</div></div>' +
        '</div>';
    },
    wire() {
      document.querySelectorAll('[data-ifh-range]').forEach((b) => {
        b.addEventListener('click', () => {
          const want = (b as HTMLElement).getAttribute('data-ifh-range') || 'live';
          if (want === range) return;
          const wasLive = isLive(range);
          range = want;
          rememberRange(range);
          document.querySelectorAll('[data-ifh-range]').forEach((o) => {
            o.classList.toggle('active',
              (o as HTMLElement).getAttribute('data-ifh-range') === range);
          });
          destroyChart();
          paint();
          // The reply is the same whichever live range is chosen and is already
          // in hand, so moving between them costs no request.
          if (!(wasLive && isLive(range))) load();
        });
      });
      const host = el('ifhBody');
      if (host) {
        host.addEventListener('click', (e) => {
          const t = e.target as HTMLElement | null;
          if (t && t.classList.contains('ifh-record')) {
            setRecording(t.getAttribute('data-ifh-rec') !== 'off');
          }
        });
      }
      paint();
      load();
    },
  });
}
