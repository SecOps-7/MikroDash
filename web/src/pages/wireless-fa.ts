/**
 * The Frequency Analyser dialog.
 *
 * A WiFi channel scan that takes the chosen radio off the air and drops every
 * client on it — the one deliberately disruptive thing this application does.
 * Almost everything here is shaped by that: the warning states which of two
 * situations the operator is about to cause, the picker names each radio's
 * client count, and the button does not exist for someone who may not scan.
 *
 * The server half bounds the damage (`internal/wifiscan`): a bounded duration, a
 * wall-clock stop that does not trust the router, one scan per router, a
 * fleet-wide cap of three and a per-operator cooldown.
 */

export interface FaRow {
  ch: number;
  chNum: number | null;
  chRaw: string | null;
  nets: number | null;
  load: number | null;
  nf: number | null;
  maxSig: number | null;
  minSig: number | null;
}

export interface FaIface {
  name: string;
  running: boolean;
  clients: number;
}

/**
 * Green (open) through red (congested).
 *
 * A LADDER OF THRESHOLDS, not a gradient, and the live comment gives the reason:
 * two channels a percent apart should not read as meaningfully different. A
 * gradient would invite an operator to pick between 41% and 43%.
 */
export function congestionColour(load: number | null, alpha = 0.85): string {
  if (load == null) return `rgba(148,163,190,${alpha * 0.45})`;
  if (load < 20) return `rgba(74,222,128,${alpha})`;
  if (load < 40) return `rgba(163,230,53,${alpha})`;
  if (load < 60) return `rgba(251,191,36,${alpha})`;
  if (load < 80) return `rgba(251,146,60,${alpha})`;
  return `rgba(248,113,113,${alpha})`;
}

/**
 * Lowest load, tie-broken by fewest networks.
 *
 * NOTHING IS INFERRED ON TOP OF THE MEASUREMENT. On 2.4GHz the channels overlap,
 * so the lowest-load channel can sit between two busy ones, and restricting the
 * pick to 1/6/11 would avoid that — the live code deliberately does not, because
 * the number shown is exactly what the router measured.
 *
 * A STABLE sort: JavaScript's is, and Go's would not be. Two channels with the
 * same load and the same network count must resolve to the one the router
 * reported first, or the recommendation moves between scans of an unchanged
 * environment.
 */
export function bestChannel(rows: FaRow[]): FaRow | null {
  const scored = rows.filter((r) => r.load != null);
  if (!scored.length) return null;
  return scored
    .slice()
    .sort((a, b) => (a.load !== b.load ? (a.load as number) - (b.load as number)
      : (a.nets || 0) - (b.nets || 0)))[0] ?? null;
}

/**
 * The noise floor, as a MEDIAN rather than a mean.
 *
 * One spurious bin should not move the reported floor, and a scan across a whole
 * band routinely produces one.
 */
export function noiseFloor(rows: FaRow[]): number | null {
  const nf = rows.map((r) => r.nf).filter((v): v is number => v != null);
  if (!nf.length) return null;
  return nf.slice().sort((a, b) => a - b)[Math.floor(nf.length / 2)] ?? null;
}

const esc = (s: string): string =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

const unit = (u: string): string => `<span class="fa-stat-unit">${u}</span>`;

/**
 * The five stat boxes, as the innerHTML each one gets.
 *
 * A NAMED type rather than Record<string, string>: the five are a fixed set, and
 * a record would let a typo add a sixth key that silently goes nowhere while an
 * index read comes back possibly-undefined.
 */
export interface FaStats {
  faCurChan: string;
  faNetworks: string;
  faCongestion: string;
  faBestChan: string;
  faNoise: string;
}

export function statsHTML(rows: FaRow[], currentChannelMhz: number | null): FaStats {
  const cur = rows.filter((r) => r.ch === currentChannelMhz)[0];
  const best = bestChannel(rows);
  const nets = rows.reduce((n, r) => n + (r.nets || 0), 0);
  const median = noiseFloor(rows);
  return {
    faCurChan: `${currentChannelMhz || '&mdash;'}${unit('MHz')}`,
    // The TOTAL across every channel, not the count of channels.
    faNetworks: rows.length ? String(nets) : '&mdash;',
    faCongestion: `${cur && cur.load != null ? cur.load : '&mdash;'}${unit('%')}`,
    faBestChan: `${best ? best.ch : '&mdash;'}${unit('MHz')}`,
    faNoise: `${median == null ? '&mdash;' : median}${unit('dBm')}`,
  };
}

/** The colour the congestion figure is written in, or '' for none. */
export function congestionColourFor(rows: FaRow[], currentChannelMhz: number | null): string {
  const cur = rows.filter((r) => r.ch === currentChannelMhz)[0];
  return cur && cur.load != null ? congestionColour(cur.load, 1) : '';
}

/** The channel grid. */
export function gridHTML(rows: FaRow[], currentChannelMhz: number | null): string {
  if (!rows.length) return '';
  return rows.map((r) => {
    const isCur = r.ch === currentChannelMhz;
    const load = r.load == null ? null : r.load;
    return `<div class="fa-chan${isCur ? ' is-current' : ''}"` +
      ` style="background:${congestionColour(load, 0.22)}` +
      `;border-color:${congestionColour(load, 0.5)}"` +
      ` title="${esc(String(r.chRaw || r.ch))}${r.nets != null ? ` · ${r.nets} networks` : ''}">` +
      `<div class="fa-chan-num">${r.chNum == null ? '&mdash;' : `ch ${r.chNum}`}</div>` +
      `<div class="fa-chan-freq">${r.ch}</div>` +
      `<div class="fa-chan-load" style="color:${congestionColour(load, 1)}">` +
      `${load == null ? '&mdash;' : `${load}%`}</div>` +
      `</div>`;
  }).join('');
}

/**
 * The warning above the picker.
 *
 * MEASURED, NOT ASSUMED — the live comment records the experiment: scanning a
 * radio with clients on it dropped all 15 within 2 seconds, held them at zero
 * for the full 30, and they took over 15 seconds to start returning. Scanning an
 * idle radio dropped nothing. So the warning states which of the two the
 * operator is about to do, rather than warning uniformly and being ignored.
 */
export function warningHTML(ifaces: FaIface[], selected: string): string {
  const rec = ifaces.filter((i) => i.name === selected)[0];
  if (!rec) return 'Scanning takes the selected radio off the air.';
  const n = rec.clients || 0;
  if (n === 0) {
    return 'This radio has <b>no clients connected</b>, so scanning it should ' +
      'interrupt nobody. Other radios on this router are unaffected.';
  }
  return `Scanning takes this radio off the air. Its <b>${n} connected ` +
    `${n === 1 ? 'client' : 'clients'} will be disconnected</b> for the duration of the ` +
    'scan, including any on its other SSIDs, and may take some seconds to return afterwards. ' +
    'Other radios on this router are unaffected.';
}

/**
 * The radio picker's options.
 *
 * EACH ONE CARRIES ITS CLIENT COUNT, and the count is the whole radio's with its
 * virtual APs rolled in. The live comment records why it is not decoration:
 * measured on a live fleet, the two obvious-looking radios had zero clients and
 * the other two had every one of them. Without the number the picker gives no
 * clue which radio is idle and which carries the network.
 */
export function ifaceOptionsHTML(ifaces: FaIface[]): string {
  if (!ifaces.length) return '<option disabled selected>No scannable radio</option>';
  return ifaces.map((i) => {
    const n = i.clients || 0;
    return `<option value="${esc(i.name)}">${esc(i.name)}` +
      ` · ${n ? `${n} ${n === 1 ? 'client' : 'clients'}` : 'no clients'}</option>`;
  }).join('');
}

/**
 * What the controls look like while a scan runs, and while it does not.
 *
 * Extracted as a value rather than left as four element writes so it can be
 * compared against the live behaviour: `element-coverage-audit` called the
 * first version of this module's gate "the shape that hid the VPN and Logs
 * pages" — a gate covering the rendered HTML and none of the interaction.
 *
 * The picker and the duration are DISABLED mid-scan, not merely ignored: a
 * change to either would apply to the next scan and read as applying to this
 * one.
 */
export interface FaControls {
  scanDisplay: string;
  stopDisplay: string;
  spinOn: boolean;
  ifaceDisabled: boolean;
  durationDisabled: boolean;
}

export function controlsFor(scanning: boolean): FaControls {
  return {
    scanDisplay: scanning ? 'none' : '',
    stopDisplay: scanning ? '' : 'none',
    spinOn: scanning,
    ifaceDisabled: scanning,
    durationDisabled: scanning,
  };
}

/** The countdown line while a scan runs. */
export function scanStatus(endsAt: number, now: number): string {
  const left = Math.max(0, Math.ceil((endsAt - now) / 1000));
  // Past zero the router owes us a result; say we are waiting rather than
  // counting into negative numbers.
  return left > 0 ? `Scanning… ${left}s — clients disconnected` : 'Finishing…';
}

// ── the dialog itself ───────────────────────────────────────────────────────

import { el } from '../dom';
import { makeSpectrumChart, renderSpectrum, destroySpectrumChart } from './wireless-fa-chart';
import type { Socket } from '../socket';

interface FaState {
  scanning: boolean;
  scanId: string | null;
  currentChannelMhz: number | null;
  endsAt: number;
}

/**
 * Wire the Frequency Analyser.
 *
 * Everything above this line is pure and gated by the fa-dialog check
 * against the live renderers. This part is the wiring: which element each string
 * lands in, and which events move the state.
 */
export function initFrequencyAnalyser(socket: Socket): void {
  const modal = el('faModal');
  const openBtn = el('faOpenBtn');
  if (!modal || !openBtn) return;

  const ifaceSel = el('faIface') as HTMLSelectElement | null;
  const durSel = el('faDuration') as HTMLSelectElement | null;
  const scanBtn = el('faScanBtn') as HTMLButtonElement | null;
  const stopBtn = el('faStopBtn') as HTMLElement | null;
  const statusEl = el('faStatus');
  const gridEl = el('faChanGrid');
  const emptyEl = el('faEmpty');
  const spinEl = el('faSpin');
  if (!ifaceSel || !durSel || !scanBtn || !stopBtn || !statusEl || !gridEl || !emptyEl) return;

  let rows: FaRow[] = [];
  let ifaces: FaIface[] = [];
  const state: FaState = { scanning: false, scanId: null, currentChannelMhz: null, endsAt: 0 };
  let tick: ReturnType<typeof setInterval> | null = null;

  function setStatus(text: string, scanning: boolean): void {
    statusEl!.textContent = text;
    statusEl!.classList.toggle('is-scanning', scanning);
  }

  function setScanning(on: boolean): void {
    state.scanning = on;
    const c = controlsFor(on);
    scanBtn!.style.display = c.scanDisplay;
    stopBtn!.style.display = c.stopDisplay;
    if (spinEl) spinEl.classList.toggle('on', c.spinOn);
    ifaceSel!.disabled = c.ifaceDisabled;
    durSel!.disabled = c.durationDisabled;
    if (tick) { clearInterval(tick); tick = null; }
    if (on) {
      // 250ms, not 1s: a countdown that ticks on its own second boundary reads as
      // stuttering against the one the operator is watching.
      tick = setInterval(() => setStatus(scanStatus(state.endsAt, Date.now()), true), 250);
    }
  }

  function render(): void {
    emptyEl!.style.display = rows.length ? 'none' : '';
    const stats = statsHTML(rows, state.currentChannelMhz);
    // EACH ID WRITTEN OUT, not looped over the map's keys.
    //
    // A loop is shorter and hides which elements this page owns: `wiring-audit`
    // scans the TypeScript for the ids the live app writes, so ids that only
    // ever exist as map keys read as unwired — and an id that stopped being
    // written would read as wired for exactly as long as the key survived.
    // Being greppable is the point.
    setHTML('faCurChan', stats.faCurChan);
    setHTML('faNetworks', stats.faNetworks);
    setHTML('faCongestion', stats.faCongestion);
    setHTML('faBestChan', stats.faBestChan);
    setHTML('faNoise', stats.faNoise);
    const congestion = el('faCongestion');
    if (congestion) congestion.style.color = congestionColourFor(rows, state.currentChannelMhz);
    gridEl!.innerHTML = gridHTML(rows, state.currentChannelMhz);
    // THE CANVAS, last — the live `render()` calls `renderStats(); renderGrid();
    // renderChart();` in that order. It is a no-op until the dialog has been
    // opened and the chart built, and on an install without Chart.js it stays
    // one: the stat boxes and the grid above are what answer "which channel
    // should I use", which is why the page was usable without this for so long.
    renderSpectrum(rows);
  }

  function setHTML(id: string, html: string): void {
    const node = el(id);
    if (node) node.innerHTML = html;
  }

  function updateWarning(): void {
    const el2 = el('faWarnText');
    if (el2) el2.innerHTML = warningHTML(ifaces, ifaceSel!.value);
  }

  function open(): void {
    modal!.classList.add('open');
    rows = [];
    state.scanId = null;
    // BUILT ON OPEN, not at mount. The canvas is inside a dialog that is
    // `display:none` until now, and Chart.js measures its element at
    // construction — built while hidden it comes up zero-sized and stays that
    // way until something forces a resize.
    //
    // The two legend callbacks are the ones `spectrumConfig` declares and
    // The fa-chart check pins: the band has no dataset, so its item is
    // appended by hand, and the default click handler would throw on an item
    // with no `datasetIndex`.
    makeSpectrumChart({
      rows: () => rows,
      currentChannelMhz: () => state.currentChannelMhz,
      legendLabels: (chart) => faLegendLabels(chart),
      legendClick: (e, item, legend) => faLegendClick(e, item, legend),
    });
    render();
    setStatus('Idle', false);
    socket.emit('wifiscan:interfaces');
  }

  function close(): void {
    modal!.classList.remove('open');
    // BEST EFFORT ONLY. The server's wall-clock stop is what actually bounds the
    // outage; this just avoids leaving a radio down because somebody wandered off.
    if (state.scanning) socket.emit('wifiscan:stop', { scanId: state.scanId });
    setScanning(false);
    // TORN DOWN WITH THE DIALOG. Chart.js registers a resize observer and an
    // animation frame per instance; leaving one attached to a hidden canvas
    // costs both for as long as the page lives, and reopening would build a
    // second over the same element.
    destroySpectrumChart();
  }

  ifaceSel.addEventListener('change', updateWarning);
  openBtn.addEventListener('click', open);
  modal.addEventListener('click', (e) => { if (e.target === modal) close(); });
  const closeX = modal.querySelector('[data-modal-close]');
  if (closeX) closeX.addEventListener('click', close);
  document.addEventListener('keydown', (e: KeyboardEvent) => {
    if (e.key === 'Escape' && modal!.classList.contains('open')) close();
  });

  scanBtn.addEventListener('click', () => {
    if (!ifaceSel.value) return;
    socket.emit('wifiscan:start', {
      iface: ifaceSel.value, durationSec: parseInt(durSel.value, 10),
    });
  });
  stopBtn.addEventListener('click', () => socket.emit('wifiscan:stop', { scanId: state.scanId }));

  socket.on('wifiscan:interfaces', (d: { permitted?: boolean; interfaces?: FaIface[] }) => {
    if (!d) return;
    // THE BUTTON EXISTS ONLY FOR SOMEONE WHO MAY ACTUALLY SCAN, and only when
    // there is something to scan. Hiding it is not the security boundary — the
    // server refuses — but offering a disruptive action that will be refused is
    // a worse answer than not offering it.
    openBtn!.style.display = d.permitted && (d.interfaces || []).length ? '' : 'none';
    ifaces = d.interfaces || [];
    const keep = ifaceSel.value;
    ifaceSel.innerHTML = ifaceOptionsHTML(ifaces);
    if (keep && ifaces.some((i) => i.name === keep)) ifaceSel.value = keep;
    scanBtn.disabled = !ifaces.length;
    if (!ifaces.length) {
      emptyEl!.textContent = 'This router reports no radio that can be scanned. ' +
        'Virtual APs, CAPsMAN-managed radios and the legacy wireless package are not supported.';
    }
    updateWarning();
  });

  socket.on('wifiscan:state', (d: {
    scanning?: boolean; scanId?: string; endsAt?: number; currentChannelMhz?: number | null;
    rows?: FaRow[];
  }) => {
    if (!d) return;
    state.scanId = d.scanId || null;
    state.endsAt = d.endsAt || 0;
    state.currentChannelMhz = d.currentChannelMhz ?? null;
    rows = d.rows || [];
    setScanning(!!d.scanning);
    render();
  });

  socket.on('wifiscan:rows', (d: { rows?: FaRow[] }) => {
    if (!d) return;
    rows = d.rows || [];
    render();
  });

  socket.on('wifiscan:done', (d: { reason?: string; rows?: FaRow[]; sampleCount?: number }) => {
    if (!d) return;
    rows = d.rows || rows;
    setScanning(false);
    render();
    // The reason is the operator's, not the developer's: "complete" is the normal
    // end of a timed burst and must not read as a warning.
    const n = d.sampleCount || 0;
    setStatus(d.reason === 'complete'
      ? `Done — ${rows.length} channels from ${n} samples`
      : d.reason === 'disconnected' ? 'Router disconnected mid-scan'
        : d.reason === 'aborted' ? 'Stopped' : `Ended: ${d.reason || 'unknown'}`, false);
  });

  socket.on('wifiscan:error', (d: { code?: string; message?: string; iface?: string }) => {
    if (!d) return;
    setScanning(false);
    setStatus(scanErrorText(d), false);
  });

  // ── ASK ON PAGE ENTRY, OR THE BUTTON NEVER APPEARS ────────────────────────
  //
  // The live listener, verbatim in intent: "Ask again on page entry so the
  // button reflects the current router."
  //
  // WITHOUT IT THE FEATURE IS UNREACHABLE, not merely stale, and the loop is
  // the whole bug. `faOpenBtn` ships `style="display:none"` in the extracted
  // markup and is unhidden by exactly one line — the `wifiscan:interfaces`
  // handler above. This port asked for that payload in ONE place: `open()`,
  // which runs when the modal opens. The modal opens from the button. So the
  // button waited for an answer that only a click on the button could request.
  //
  // Live asks in three places and this port had one of them. Nothing failed:
  // every pure renderer here is gated by the fa-dialog check against the
  // live originals and all of it passes, because the defect is not in any of
  // them — it is that the entry point is never shown. Found by driving both
  // apps and diffing the visible buttons, which is the check that sees a
  // feature nobody can reach.
  //
  // It is ALSO a per-router question — a radio the caller may scan on one
  // router may be CAPsMAN-managed on the next — which is why live re-asks on
  // entry rather than once at startup.
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent<string>).detail === 'wifi-clients') socket.emit('wifiscan:interfaces');
  });
}

/**
 * A refusal, in the operator's terms.
 *
 * The server answers in codes so the page can say something useful about each;
 * an unknown one falls through to the message rather than being swallowed.
 */
export function scanErrorText(d: { code?: string; message?: string; iface?: string }): string {
  switch (d.code) {
    case 'busy': return `Already scanning ${d.iface || 'this router'}`;
    case 'fleet-busy': return 'Too many scans running across the fleet — try again shortly';
    case 'cooldown': return 'Scanned very recently — wait a few seconds';
    case 'denied': return 'Not permitted to scan this router';
    case 'router-offline': return 'Router is offline';
    case 'capsman-managed': return 'That radio is managed by CAPsMAN';
    case 'not-a-radio': return 'That interface is not a radio';
    case 'no-such-interface': return 'No such radio on this router';
    case 'unavailable': return 'Radio list not ready yet — try again in a moment';
    default: return d.message || 'Scan failed';
  }
}

/**
 * The spectrum chart's tooltip body: what one channel row contributes.
 *
 * Six measurements, in the order the live app pushes them, with its exact
 * labels — the column padding is part of the string, because the tooltip is
 * monospaced and the columns line up only if the spaces are reproduced.
 *
 * ── `!= null`, NOT TRUTHINESS ───────────────────────────────────────────────
 *
 * A load of 0%, a noise floor of 0 dBm and a network count of 0 are all
 * MEASUREMENTS. The live test is `!= null`, so they appear; a port using a
 * truthiness check would silently drop every zero, which is most of a quiet
 * channel's row. The corpus carries an all-zero case for exactly that.
 */
export function spectrumTooltipLines(
  row: FaRow | undefined, currentChannelMhz: number | null,
): string[] {
  if (!row) return [];
  const out: string[] = [];
  if (row.load != null) out.push('Load        ' + row.load + '%');
  if (row.nets != null) out.push('Networks    ' + row.nets);
  if (row.nf != null) out.push('Noise floor ' + row.nf + ' dBm');
  if (row.maxSig != null) out.push('Max signal  ' + row.maxSig + ' dBm');
  if (row.minSig != null) out.push('Min signal  ' + row.minSig + ' dBm');
  if (row.ch === currentChannelMhz) out.push('— this radio —');
  return out;
}

/**
 * Where the current-channel band is drawn, and how wide.
 *
 * ── THE WIDTH COMES FROM THE BAR, NOT THE CATEGORY ──────────────────────────
 *
 * The live comment: "Take the width from the bar itself so the band lines up
 * exactly, rather than from the category spacing — Chart.js insets bars within
 * their category, so a category-wide band would sit visibly proud of them."
 *
 * The fallback, for when the bar element has not been laid out yet, is the
 * category spacing clamped to [10, 44] — and 18 when there is only one column,
 * because there is then no spacing to measure at all.
 *
 * `bar` is the laid-out element, `pixelFor` the x scale's own lookup.
 */
export function spectrumBandGeometry(
  bar: { x: number; width?: number } | null,
  labelCount: number,
  pixelFor: (index: number) => number,
  index: number,
): { x: number; w: number } {
  const x = bar ? bar.x : pixelFor(index);
  const w = (bar && bar.width)
    ? bar.width
    : Math.max(10, Math.min(labelCount > 1
        ? Math.abs(pixelFor(1) - pixelFor(0)) : 18, 44));
  return { x, w };
}

/**
 * The spectrum chart's floor: the bar base and the y-axis minimum.
 *
 * Signal is negative dBm, so a plain value would hang DOWN from the zero line.
 * Anchoring each bar at the floor makes it rise out of the noise the way a
 * spectrum display should.
 */
export const FA_FLOOR_DBM = -100;

/** The datasets, rebuilt from the current scan rows. */
export function spectrumData(rows: FaRow[]): {
  labels: (number | null)[];
  signal: ([number, number] | null)[];
  colours: string[];
  noise: (number | null)[];
} {
  return {
    labels: rows.map((r) => r.ch),
    // FLOATING BARS `[base, top]`. A channel where nothing was detected gets NO
    // BAR rather than a fabricated one — `null`, not the floor, which would draw
    // a zero-height bar indistinguishable from a very weak signal.
    signal: rows.map((r) => (r.maxSig == null ? null : [FA_FLOOR_DBM, r.maxSig])),
    // Bar height is signal strength; its COLOUR carries congestion, so one
    // glance answers both "is anything there" and "how busy is it".
    colours: rows.map((r) => congestionColour(r.load)),
    noise: rows.map((r) => r.nf),
  };
}

/**
 * The Chart.js configuration for the spectrum plot.
 *
 * A BUILDER rather than a literal, for the reason `dashboard-traffic.ts` gives:
 * the gate hands both sides a fake `Chart` and compares the captured config key
 * for key, and a function-valued option can only be compared by calling it.
 *
 * `rows` and `currentChannelMhz` are read through getters rather than captured,
 * because the live config closes over `_rows` and `_state` — both of which are
 * reassigned by every scan, so a snapshot taken at construction would freeze the
 * tooltip on the first result.
 */
export function spectrumConfig(deps: {
  rows: () => FaRow[];
  currentChannelMhz: () => number | null;
  legendLabels: (chart: unknown) => LegendItem[];
  legendClick: (e: unknown, item: LegendItem, legend: unknown) => void;
}): SpectrumConfig {
  return {
    type: 'bar',
    data: {
      labels: [],
      datasets: [
        // Signal power as bars, each coloured by that channel's congestion.
        // Congestion stays on the chart as the bar colour and is readable as a
        // number in the tooltip and the grid above — it does not need a line of
        // its own competing with the signal trace.
        { label: 'Signal power', data: [], yAxisID: 'y', order: 3,
          backgroundColor: [], borderRadius: 2, borderSkipped: false },
        // The reference the signal is measured against.
        { label: 'Noise floor', type: 'line', data: [], yAxisID: 'y', order: 2,
          borderColor: 'rgba(148,163,190,.75)', borderDash: [4, 3],
          borderWidth: 1.5, pointRadius: 0, tension: 0.25, fill: false, spanGaps: true },
      ],
    },
    options: {
      responsive: true, maintainAspectRatio: false, animation: false,
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: {
          display: true,
          labels: {
            color: 'rgba(148,163,190,.8)', boxWidth: 10, font: { size: 10 },
            usePointStyle: true,
            // The current-channel band is drawn by a plugin, so it has no
            // dataset for the legend to pick up. Appended by hand, or the one
            // mark people ask about is the one nothing explains.
            generateLabels: deps.legendLabels,
          },
          // That appended item has no datasetIndex; the default handler would
          // throw trying to toggle a dataset that does not exist.
          onClick: deps.legendClick,
        },
        tooltip: {
          callbacks: {
            title: (c: { dataIndex: number }[]): string => {
              const r = deps.rows()[c[0]!.dataIndex];
              if (!r) return '';
              return r.ch + ' MHz' + (r.chNum != null ? '  (ch ' + r.chNum + ')' : '');
            },
            // The bars carry no per-dataset line; every measurement is in the
            // body below.
            label: (): null => null,
            afterBody: (c: { dataIndex: number }[]): string[] =>
              spectrumTooltipLines(deps.rows()[c[0]!.dataIndex], deps.currentChannelMhz()),
          },
        },
      },
      scales: {
        x: { ticks: { color: 'rgba(148,163,190,.5)', font: { size: 9 }, maxRotation: 0,
                      autoSkipPadding: 12 },
             grid: { display: false } },
        y: { position: 'left', min: FA_FLOOR_DBM, max: -20,
             title: { display: true, text: 'dBm', color: 'rgba(148,163,190,.5)',
                      font: { size: 9 } },
             grid: { color: 'rgba(99,130,190,.08)' },
             ticks: { color: 'rgba(148,163,190,.5)', font: { size: 9 } } },
      },
    },
  };
}

/**
 * The legend's items: Chart.js's own, plus the band.
 *
 * ── THE BAND HAS NO DATASET, SO IT HAS NO LEGEND ITEM ──────────────────────
 *
 * It is drawn by a plugin, and the legend is built from datasets — so without
 * this the one mark people ask about is the one nothing explains. Appended
 * AFTER the defaults so it reads last, matching the draw order.
 *
 * `Chart.defaults` is reached through the global rather than imported: the live
 * app calls `Chart.defaults.plugins.legend.labels.generateLabels(chart)` to get
 * the standard items and then adds to them, which is what keeps the two dataset
 * entries identical to every other chart in the app.
 */
export function faLegendLabels(chart: unknown): LegendItem[] {
  const C = (globalThis as unknown as { Chart?: ChartDefaults }).Chart;
  const gen = C?.defaults?.plugins?.legend?.labels?.generateLabels;
  // NO CHART.JS, NO DEFAULTS — and the band item alone is still the right
  // answer, not an empty legend: it is the item this function exists to add.
  const items = typeof gen === 'function' ? gen(chart) : [];
  items.push(FA_BAND_LEGEND);
  return items;
}

/**
 * The legend's click handler.
 *
 * ── THE GUARD IS THE WHOLE FUNCTION ────────────────────────────────────────
 *
 * The appended band item has no `datasetIndex`, and Chart.js's default handler
 * would throw trying to toggle a dataset that does not exist. Clicking the one
 * legend entry that explains the band would break the chart.
 *
 * Everything else defers to the default, so hiding a dataset behaves exactly as
 * it does on every other chart.
 */
export function faLegendClick(e: unknown, item: LegendItem, legend: unknown): void {
  if (item.datasetIndex === undefined) return;
  const C = (globalThis as unknown as { Chart?: ChartDefaults }).Chart;
  const onClick = C?.defaults?.plugins?.legend?.onClick;
  if (typeof onClick === 'function') onClick.call(legend, e, item, legend);
}

interface ChartDefaults {
  defaults?: {
    plugins?: {
      legend?: {
        labels?: { generateLabels?: (chart: unknown) => LegendItem[] };
        onClick?: (this: unknown, e: unknown, item: LegendItem, legend: unknown) => void;
      };
    };
  };
}

/** The legend item the band plugin has no dataset for. */
export const FA_BAND_LEGEND: LegendItem = {
  text: 'Active Channel',
  fillStyle: 'rgba(56,189,248,.35)',
  strokeStyle: 'rgba(56,189,248,.85)',
  lineWidth: 1.5,
  pointStyle: 'rect',
  hidden: false,
};

export interface LegendItem {
  text: string;
  fillStyle?: string;
  strokeStyle?: string;
  lineWidth?: number;
  pointStyle?: string;
  hidden?: boolean;
  datasetIndex?: number;
}

export interface SpectrumConfig {
  type: string;
  data: { labels: unknown[]; datasets: Record<string, unknown>[] };
  options: Record<string, unknown>;
}
