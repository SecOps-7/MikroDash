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
 * The short ranges — Live to 30 min — come from a 1 Hz buffer fed by the
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
import { pushSample, windowedPoints, rightBufferFor } from './dashboard-traffic-buffer';
import type { Interface, TrafficPoint } from '../gen/payloads';

declare const Chart: undefined | (new (canvas: HTMLCanvasElement, cfg: unknown) => ChartLike);

interface XY { x: number; y: number }

interface ChartLike {
  destroy(): void;
  update(mode?: string): void;
  data: { datasets: Array<{ data: XY[] }> };
  options: { scales: { x: { min: number; max: number } } };
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
const LIVE_RANGES: Array<{ key: string; label: string; secs: number; smooth: boolean }> = [
  // `smooth` scrolls the window every frame so the line SLIDES rather than
  // stepping once a second. Whether that is worth paying for is decided by ONE
  // number: how far the window travels in a second, across a plot about 700px
  // wide. Above a pixel there is a visible step to smooth away; below one there
  // is nothing to see and a frame-rate redraw of every point to pay for.
  //
  //	Live  60s   ~11.7 px/s   smoothed
  //	5m   300s    ~2.3 px/s   smoothed
  //	15m  900s    ~0.8 px/s   stepped
  //	30m 1800s    ~0.4 px/s   stepped
  { key: 'live', label: 'Live', secs: 60, smooth: true },
  { key: '5m', label: '5 min', secs: 300, smooth: true },
  { key: '15m', label: '15 min', secs: 900, smooth: false },
  { key: '30m', label: '30 min', secs: 1800, smooth: false },
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

/** Is this range one that scrolls? See `scroll` below for why 30 min is not. */
function smoothRange(): boolean {
  const spec = LIVE_RANGES.find((r) => r.key === range);
  return !!spec && spec.smooth;
}

/**
 * How far PAST the left edge points are kept, so the line runs off the axis
 * instead of stopping short of it.
 *
 * `windowedPoints` cuts at `>= cutoff`, and the cutoff is exactly the axis
 * minimum — so the oldest point it returns sits up to one sample interval
 * INSIDE the plot, and the trace visibly ends before the frame does. On a
 * 60-second window that is about nine pixels of gap, and it grows and snaps
 * shut as each point falls out. Keeping a few seconds more lets Chart.js clip
 * the line at the edge, which is what the edge is for.
 */
const LEFT_OVERHANG_MS = 3000;

function liveWindow(): TrafficPoint[] {
  const spec = LIVE_RANGES.find((r) => r.key === range);
  const buf = liveBuf.get(iface);
  if (!spec || !buf) return [];
  // The axis holds a gap at the right and is clipped at the left, so the points
  // kept have to cover BOTH: the same right-hand gap, plus enough beyond the
  // left edge for the line to cross it.
  return windowedPoints(buf, Date.now(), spec.secs, rightBufferFor(buf) + LEFT_OVERHANG_MS);
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
  return '<div class="ifh-stats" id="ifhStats">' + statsInner(d) + '</div>';
}

function statsInner(d: HistoryReply): string {
  return '' +
    statBox(fmtDataMB(d.rxTotalMb), 'Downloaded', 'ifh-rx') +
    statBox(fmtDataMB(d.txTotalMb), 'Uploaded', 'ifh-tx') +
    statBox(d.rxMaxMbps == null ? '—' : fmtMbps(d.rxMaxMbps), 'Peak down', 'ifh-rx') +
    statBox(d.txMaxMbps == null ? '—' : fmtMbps(d.txMaxMbps), 'Peak up', 'ifh-tx');
}

/** The LIVE stat line: the current rate and the peak inside the window.
 *
 *  NO TOTALS. A total is a quantity of bytes, and integrating a 1 Hz buffer
 *  that pauses whenever the page is hidden would invent one. The recorded
 *  ranges have minute rows behind them and can answer it honestly. */
function liveStats(all: readonly TrafficPoint[]): string {
  return '<div class="ifh-stats" id="ifhStats">' + liveStatsInner(all) + '</div>';
}

/**
 * The boxes alone, so a tick can replace the CONTENTS of the stats row.
 *
 * It used to assign `outerHTML`, which swaps the node itself — the id then has
 * to be re-found on the replacement, and anything holding the old element is
 * holding a detached one. Writing into a stable wrapper is the same picture
 * with none of that.
 */
function liveStatsInner(all: readonly TrafficPoint[]): string {
  // THE PEAK IS OVER WHAT IS DRAWN. The points carry an overhang past the left
  // edge so the line can cross it, and a peak taken over those would report a
  // number that is no longer anywhere on the chart.
  const spec = LIVE_RANGES.find((r) => r.key === range);
  const b = liveBounds(Date.now(), spec ? spec.secs : 60);
  const pts = all.filter((p) => p.ts >= b.min);
  const nowPt = pts[pts.length - 1];
  const maxRx = pts.reduce((a, p) => (p.rx_mbps > a ? p.rx_mbps : a), 0);
  const maxTx = pts.reduce((a, p) => (p.tx_mbps > a ? p.tx_mbps : a), 0);
  return statBox(fmtMbps(nowPt ? nowPt.rx_mbps : 0), 'Down', 'ifh-rx') +
    statBox(fmtMbps(nowPt ? nowPt.tx_mbps : 0), 'Up', 'ifh-tx') +
    statBox(fmtMbps(maxRx), 'Peak down', 'ifh-rx') +
    statBox(fmtMbps(maxTx), 'Peak up', 'ifh-tx');
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
 * The recording switch, as the app's own toggle rather than a pair of buttons.
 *
 * `.stoggle` is what the dialog's own fields render (the Disabled toggle two
 * rows above this one), in its `-bare` variant so it sits inside the panel
 * instead of carrying a second panel background.
 *
 * THE DEFAULT INTERFACE'S TOGGLE IS ON AND DISABLED. It is recorded because it
 * is the WAN — Reports, the capacity lines and the WAN badge all read that
 * series — and the resolver puts it back whatever the stored list says, so a
 * switch that could be moved would lie about what happens next.
 */
function recordControl(d: HistoryReply): string {
  if (!d.mayRecord) return '';
  const isWan = !!d.defaultIf && d.defaultIf === iface;
  const on = isWan || !!d.recorded;
  return '<label class="stoggle stoggle-bare ifh-rec">' +
    '<span class="stoggle-label">Record Traffic History' +
    (isWan ? '<span class="ifh-note-dim"> · this router&#39;s WAN</span>' : '') +
    '</span>' +
    '<span class="stoggle-switch">' +
    '<input type="checkbox" class="ifh-record"' + (on ? ' checked' : '') +
    (isWan ? ' disabled' : '') + '>' +
    '<span class="stoggle-track"></span><span class="stoggle-thumb"></span>' +
    '</span></label>';
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

/**
 * The window the x axis shows, in milliseconds.
 *
 * A LIVE RANGE IS ALWAYS ITS FULL WIDTH, whether or not there is enough history
 * to fill it. The axis used to be one category per sample, so five seconds of
 * traffic stretched across the whole chart and the trace sat hard against the
 * left edge from the first tick — it looked full when it was nearly empty.
 * Fixing the window instead means new data enters at the RIGHT and the line
 * grows leftwards until it fills, which is what a live graph is expected to do.
 */
function xWindow(): { min: number; max: number } {
  if (isLive(range)) {
    const spec = LIVE_RANGES.find((r) => r.key === range);
    return liveBounds(Date.now(), spec ? spec.secs : 60);
  }
  const rows = (last && last.rows) || [];
  const first = rows[0];
  const lastRow = rows[rows.length - 1];
  const now = Date.now();
  return { min: first ? first.ts : now - 3600000, max: lastRow ? lastRow.ts : now };
}

/**
 * The visible window for a live range, ending one sample interval SHORT of now.
 *
 * ── THE NEWEST SECOND IS DELIBERATELY OFF-SCREEN ───────────────────────────
 *
 * The right-hand end of the line is the part still being drawn: the newest
 * sample lands, the segment to it appears, and a moment later another arrives.
 * Held flush against the frame that is a twitching stub at the edge. Keeping a
 * gap of one interval means the leading edge is always complete by the time it
 * is visible.
 *
 * `rightBufferFor` is the dashboard's own helper, read by both of its charts
 * for exactly this — a second copy here is the drift that file says it exists
 * to prevent. It MEASURES the gap from the samples rather than assuming a
 * second, which matters here too: the interface poll is an operator setting.
 */
function liveBounds(now: number, secs: number): { min: number; max: number } {
  const rb = rightBufferFor(liveBuf.get(iface) || []);
  return { min: now - secs * 1000 - rb, max: now - rb };
}

/** The points of one series, at their real timestamps. */
function series(rows: readonly TrafficPoint[], key: 'rx_mbps' | 'tx_mbps'): XY[] {
  return rows.map((r) => ({ x: r.ts, y: +(+r[key]).toFixed(3) }));
}

/**
 * A live tick: hand the chart the new points, and let the SCROLL move them.
 *
 * NOTHING IS TWEENED. Chart.js animation interpolates each point's value
 * towards the next one, and on an index-based axis that is not a scroll — it
 * is every point in the line sliding vertically at once, which reads as the
 * whole chart morphing. With the points at their true timestamps the movement
 * is the window advancing, so the shape is rigid and only the view moves.
 */
function repaintLive(): void {
  if (!dialogOpen()) return;
  const pts = liveWindow();
  if (!chart || !document.getElementById('ifhChart')) {
    paint();
    return;
  }
  const ds = chart.data.datasets;
  if (ds[0]) ds[0].data = series(pts, 'rx_mbps');
  if (ds[1]) ds[1].data = series(pts, 'tx_mbps');
  scrollTo(Date.now());
  const host = el('ifhStats');
  if (host) host.innerHTML = liveStatsInner(pts);
}

/** Move the window to `now` and redraw. `'none'` because the motion IS the
 *  window: anything tweened on top of it would fight the scroll. */
function scrollTo(now: number): void {
  if (!chart) return;
  const spec = LIVE_RANGES.find((r) => r.key === range);
  if (spec) {
    const b = liveBounds(now, spec.secs);
    chart.options.scales.x.min = b.min;
    chart.options.scales.x.max = b.max;
  }
  chart.update('none');
}

/**
 * The scroll: advance the window every animation frame rather than every
 * sample.
 *
 * A window that only moved when a sample arrived would jump a second's worth of
 * pixels at a time — the "tick" this replaced. Driven by requestAnimationFrame,
 * which also means the browser stops it while the tab is hidden, so a dashboard
 * left open in a background tab costs nothing.
 *
 * NOT FOR THE 30-MINUTE WINDOW: it advances 0.3 pixels a second, so there is
 * nothing to smooth and a frame-rate redraw of 1800 points would be paid for
 * motion no one can see.
 */
let raf = 0;
function startScroll(): void {
  stopScroll();
  if (!smoothRange()) return;
  const step = (): void => {
    if (!dialogOpen() || !smoothRange() || !chart) { raf = 0; return; }
    scrollTo(Date.now());
    raf = requestAnimationFrame(step);
  };
  raf = requestAnimationFrame(step);
}

function stopScroll(): void {
  if (raf) { cancelAnimationFrame(raf); raf = 0; }
}

function paint(): void {
  const host = el('ifhBody');
  if (host) host.innerHTML = body(last);
  destroyChart();
  stopScroll();
  const rows = isLive(range) ? liveWindow() : ((last && last.rows) || []);
  if (!rows.length) return;
  const canvas = el<HTMLCanvasElement>('ifhChart');
  if (!canvas || typeof Chart === 'undefined') return;
  const win = xWindow();
  chart = new Chart(canvas, {
    type: 'line',
    data: {
      datasets: [
        {
          label: 'RX', data: series(rows, 'rx_mbps'),
          // THE FIXED COLOURS, read from the theme rather than written here:
          // Rx is --accent-rx and Tx is --accent-tx everywhere in this app.
          borderColor: cssVar('--accent-rx'), backgroundColor: 'rgba(56,189,248,.12)',
          borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
        },
        {
          label: 'TX', data: series(rows, 'tx_mbps'),
          borderColor: cssVar('--accent-tx'), backgroundColor: 'rgba(74,222,128,.12)',
          borderWidth: 1.5, pointRadius: 0, tension: 0.2, fill: true,
        },
      ],
    },
    options: {
      responsive: true, maintainAspectRatio: false,
      // NOTHING ANIMATES. See repaintLive: a tween on an index axis morphs the
      // line rather than scrolling it, and the scroll is the window moving.
      animation: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: true, labels: { boxWidth: 10, font: { size: 10 } } },
        // THE HEADING OVER Rx AND Tx IS A CLOCK TIME. Chart.js's default title
        // on a linear axis is the raw x value, so it read "1790000000000".
        tooltip: {
          callbacks: {
            title: (items: Array<{ parsed: { x: number } }>): string => {
              const first = items[0];
              return first ? stamp(first.parsed.x) : '';
            },
          },
        },
      },
      scales: {
        x: {
          // LINEAR OVER MILLISECONDS, not a time axis: Chart.js's time scale
          // needs a date adapter this build does not ship, and a linear axis
          // with a formatter is the same picture without the dependency.
          type: 'linear', min: win.min, max: win.max,
          ticks: {
            maxTicksLimit: 6, font: { size: 9 }, autoSkip: true,
            callback: (v: number): string => tick(v, xWindowMax()),
          },
          grid: { display: false },
        },
        y: {
          beginAtZero: true, ticks: { font: { size: 9 } },
          title: { display: true, text: 'Mbps' },
        },
      },
    },
  });
  startScroll();
}

/** The right-hand edge the axis labels are measured against. Read from the
 *  chart so a label cannot disagree with the window it sits under. */
function xWindowMax(): number {
  return chart ? chart.options.scales.x.max : Date.now();
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
const p2 = (n: number): string => (n < 10 ? '0' + n : String(n));

function tick(ts: number, now: number): string {
  const d = new Date(ts);
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
 * The heading over one tooltip: a wall-clock time, in every range.
 *
 * ── WHY IT DOES NOT FOLLOW THE AXIS ────────────────────────────────────────
 *
 * `tick` labels a live window by AGE, and that is right for an axis, which
 * describes a span. A tooltip names a single instant, and "-12s" is the one
 * thing an operator cannot line up against a log line or an alert. So the axis
 * says how long ago and the tooltip says when, and they are not in conflict:
 * they answer different questions about the same point.
 *
 * The detail follows the resolution the range is drawn at, because a stamp more
 * precise than its sample invents accuracy — a 30-day chart is daily buckets,
 * and printing "14:37" over one would be a lie about the bucket's shape.
 */
function stamp(ts: number): string {
  const d = new Date(ts);
  const clock = p2(d.getHours()) + ':' + p2(d.getMinutes());
  const day = p2(d.getDate()) + '/' + p2(d.getMonth() + 1);
  if (isLive(range)) return clock + ':' + p2(d.getSeconds());  // 1 Hz samples
  if (range === '1h') return clock;                            // per minute
  if (range === '30d') return day;                             // per day
  return day + ' ' + clock;                                    // per hour
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
  const box = document.querySelector('.ifh-record') as HTMLInputElement | null;
  // Disabled while it is in flight, so a second click cannot race the first.
  if (box) box.disabled = true;
  fetch('/api/routers/' + encodeURIComponent(activeID), {
    method: 'PUT', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ recordedIfaces: stored }),
  })
    .then((r) => (r.ok ? r.json() : null))
    .then((j) => {
      if (!j || j.ok === false) {
        // PUT IT BACK. The browser has already moved the switch; leaving it
        // where the click left it would show a state the server refused.
        if (box) { box.disabled = false; box.checked = !on; }
        return;
      }
      // RE-READ rather than patch `last` here: the server resolves the list,
      // and believing our own arithmetic about it is how the two drift.
      load();
    })
    .catch(() => { if (box) { box.disabled = false; box.checked = !on; } });
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
        // CHANGE, not click: the label wraps the input, so a click lands on
        // whichever span was under the pointer while `change` fires once on
        // the input itself and carries the state it landed in.
        host.addEventListener('change', (e) => {
          const t = e.target as HTMLInputElement | null;
          if (t && t.classList.contains('ifh-record')) setRecording(!!t.checked);
        });
      }
      paint();
      load();
    },
  });
}
