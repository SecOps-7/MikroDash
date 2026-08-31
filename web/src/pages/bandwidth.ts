// The Bandwidth page — a port of the `Bandwidth Page` IIFE in public/app.js.
//
// Per-device throughput, derived from connection byte deltas. The device table,
// its filters and its sort live here.
//
// ── THE CHART IS NOT PORTED YET, AND THE PAGE IS NOT IN `PORTED` BECAUSE OF IT ─
//
// This page has two data sources: `bandwidth:update` for the table, and
// `traffic:update` for the compact live chart at the top. The chart is a
// Chart.js instance built from the DASHBOARD's sample buffer and its shared
// clock (`allPoints`, `_serverOffset`, `_lastSampleTs`), so it is ported with
// the Dashboard rather than here — the two would otherwise keep two buffers of
// the same stream.
//
// Until then this module is not wired into main.ts. A page that quietly rendered
// its table and left an empty canvas where the chart belongs would be a
// regression dressed as progress; the live page keeps serving instead, which is
// what the strangler-fig arrangement is for.

import { esc, el, fmtMbps, debounce } from '../dom';
import {
  RIGHT_BUFFER_MS, anchorMs, axisWindow, bandwidthSeedPoints, needsFullRedraw,
  pruneAndMax, smoothMax, type TrafficSample, type XYPoint,
} from './dashboard-traffic-buffer';
import { sharedClock, sharedPoints } from './dashboard-traffic';

/** Only what this page touches on a Chart.js instance. */
interface BwChart {
  data: { datasets: { data: XYPoint[] }[] };
  options: { scales: { x: { min?: number; max?: number }; y: { max?: number } } };
  update(mode?: string): void;
  destroy(): void;
}
type BwChartCtor = new (canvas: unknown, cfg: unknown) => BwChart;
import type { Socket } from '../socket';

export interface BandwidthDevice {
  srcIp: string; dstIp: string;
  rxMbps: number; txMbps: number; totalMbps: number;
  proto: string; iface: string;
  name: string; mac: string;
  country: string; city: string;
  org: string | null; cat: string | null;
  isLan: boolean; isIpv6: boolean;
}

export interface BandwidthPayload { ts: number; devices: BandwidthDevice[]; pollMs: number }

/** A regional-indicator flag from an ISO-3166 alpha-2 code. */
export function iso2Flag(cc: string): string {
  if (!cc || cc.length !== 2) return '';
  const base = 0x1F1E6;
  return String.fromCodePoint(base + cc.charCodeAt(0) - 65) +
         String.fromCodePoint(base + cc.charCodeAt(1) - 65);
}

/** The service badge, from app.js. `cat` is the ASN category, or `other`. */
export function svcBadge(org: string, cat: string | null): string {
  if (!org) return '';
  return '<span class="svc-badge svc-' + (cat || 'other') + '">' + esc(org) + '</span>';
}

/**
 * A mini bar, normalised to the busiest row IN THE CURRENT VIEW.
 *
 * Not to the busiest row overall: the filters exist to narrow the question, and
 * a bar scaled to something the filter removed would read as "nothing is
 * happening here" on a page whose whole job is to show what is.
 */
export function bar(val: number, max: number, cls: string): string {
  const pct = max > 0 ? Math.min(val / max, 1) : 0;
  const w = Math.max(Math.round(pct * 60), pct > 0 ? 2 : 0);
  return '<span class="bw-bar ' + cls + '" style="width:' + w + 'px"></span>';
}

function protoCell(p: string): string {
  if (!p) return '—';
  const cls = p === 'tcp' ? 'bw-proto-tcp'
    : p === 'udp' ? 'bw-proto-udp'
      : p.indexOf('icmp') !== -1 ? 'bw-proto-icmp' : 'bw-proto-other';
  return '<span class="bw-proto ' + cls + '">' + esc(p) + '</span>';
}

type SortKey = 'name' | 'dstIp' | 'rxMbps' | 'txMbps' | 'totalMbps' | 'iface' | 'proto' | 'org';

const SORT_COLS: Array<{ id: string; key: SortKey }> = [
  { id: 'bwThDevice', key: 'name' },
  { id: 'bwThDst', key: 'dstIp' },
  { id: 'bwThRx', key: 'rxMbps' },
  { id: 'bwThTx', key: 'txMbps' },
  { id: 'bwThTotal', key: 'totalMbps' },
  { id: 'bwThIface', key: 'iface' },
  { id: 'bwThProto', key: 'proto' },
  { id: 'bwThOrg', key: 'org' },
];

/**
 * The live RX/TX readout: a rate split into a NUMBER and a UNIT, written into
 * two separate elements.
 *
 * Every threshold is a boundary where the two halves can disagree with the rest
 * of the page, so the original's exact comparisons are kept: `>= 1000` Gbps,
 * `>= 1` Mbps, `>= 0.001` Kbps, and below that an em dash with an EMPTY unit —
 * a link doing 0.0009 Mbps reads "—" rather than "0.9 Kbps".
 *
 * THE DECIMAL PLACES DIFFER PER UNIT (2, 2, 1) and that is not decoration: it
 * stops a Kbps figure claiming hundredths of a kilobit nothing measured.
 *
 * `Number(x) || 0` is the original's `+mbps || 0`. It is REPRODUCED, not needed:
 * mutating it to a bare `Number(x)` survives the whole corpus, because NaN fails
 * all three `>=` comparisons and lands on the same dash branch that `0` does.
 * There is no input that separates them.
 *
 * That is recorded rather than quietly dropped, and the comment here first
 * claimed the opposite — that without `|| 0` a router reporting nothing would
 * render "NaN" beside a unit. The mutation check disproved it three minutes
 * later. The `|| 0` stays because this is a port and the original has it; the
 * surviving mutation is the honest note that it cannot be observed.
 *
 * Pinned by tools/bandwidth-rate-cases.js, which lifts the original rather than
 * retyping it.
 */
export function splitRate(mbps: unknown): { num: string; unit: string } {
  const n = Number(mbps) || 0;
  if (n >= 1000) return { num: (n / 1000).toFixed(2), unit: 'Gbps' };
  if (n >= 1) return { num: n.toFixed(2), unit: 'Mbps' };
  if (n >= 0.001) return { num: (n * 1000).toFixed(1), unit: 'Kbps' };
  return { num: '\u2014', unit: '' };
}

/**
 * Everything `_syncBwChart` (app.js:6947) writes onto the compact chart, as
 * DATA — the datasets and the two axis extents — so it can be compared without
 * a canvas or a Chart.js.
 *
 * The arithmetic is the dashboard chart's, deliberately: same max, same anchor
 * formula, same axis window. The ONE difference is which points it seeds from,
 * and that difference is real — see `bandwidthSeedPoints`, which keeps three
 * seconds more than the dashboard's `windowedPoints` so the first frame agrees
 * with what this chart's own keepalive will retain.
 *
 * The anchor uses the SHARED clock rather than a local one. That is what stops
 * the seeding frame painting at a different X than the keepalive continues from,
 * which the original records as "no forward snap" — and it is why
 * `dashboard-traffic.ts` exports `sharedClock()` at all.
 */
export function bwSyncState(
  points: readonly TrafficSample[], nowMs: number,
  clock: { lastSampleTs: number; serverOffset: number; windowSecs: number },
  rightBufferMs: number,
): { rx: XYPoint[]; tx: XYPoint[]; yMax: number; xMin: number; xMax: number } {
  const pts = bandwidthSeedPoints(points, nowMs, clock.windowSecs, rightBufferMs);
  const rx = pts.map((p) => ({ x: p.ts, y: p.rx_mbps }));
  const tx = pts.map((p) => ({ x: p.ts, y: p.tx_mbps }));
  let dMax = 0;
  for (const p of pts) {
    if (p.rx_mbps > dMax) dMax = p.rx_mbps;
    if (p.tx_mbps > dMax) dMax = p.tx_mbps;
  }
  // `|| 1` keeps an idle interface's axis at 1 Mbps rather than collapsing to
  // zero, where noise would look like saturation. Unlike the keepalive this
  // SNAPS rather than easing — a redraw is a new view, not a continuation.
  const yMax = dMax || 1;
  const anchor = anchorMs(clock.lastSampleTs, clock.serverOffset, nowMs, pts);
  const win = axisWindow(anchor, clock.windowSecs, rightBufferMs);
  return { rx, tx, yMax, xMin: win.min, xMax: win.max };
}

/**
 * The compact chart's config — `_makeBwChart` (app.js:6918).
 *
 * IT IS NOT THE DASHBOARD'S CONFIG and must not be folded into it. The
 * differences are all deliberate and all visible: no tick plugin (this chart
 * draws no timestamps of its own), no `devicePixelRatio` cap, tooltip fonts at
 * 10 rather than 11, the X axis HIDDEN rather than displayed with an `afterFit`
 * height, and a Y axis that this one actually shows — `beginAtZero`, four ticks
 * at most, each formatted with `fmtMbps`.
 *
 * Returned as data so it can be compared without a Chart.js. Its callbacks
 * cannot be compared as values, so the gate CALLS them.
 */
export function bwChartConfig(nowMs: number, windowSecs: number, rightBufferMs: number): unknown {
  return {
    type: 'line',
    data: {
      datasets: [
        { label: 'RX', data: [], borderColor: '#38bdf8', backgroundColor: 'rgba(56,189,248,.08)', borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true },
        { label: 'TX', data: [], borderColor: '#34d399', backgroundColor: 'rgba(52,211,153,.06)', borderWidth: 1.5, tension: 0.3, pointRadius: 0, fill: true },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      animation: { duration: 1000, easing: 'linear' },
      interaction: { mode: 'index', intersect: false },
      plugins: {
        legend: { display: false },
        tooltip: {
          backgroundColor: 'rgba(7,9,15,.9)', borderColor: 'rgba(99,130,190,.2)', borderWidth: 1,
          titleFont: { family: "'JetBrains Mono',monospace", size: 10 },
          bodyFont: { family: "'JetBrains Mono',monospace", size: 10 },
          callbacks: {
            title: (items: { parsed: { x: number } }[]) => new Date(items[0]!.parsed.x).toLocaleTimeString(),
            label: (c: { dataset: { label: string }; parsed: { y: number } }) =>
              ' ' + c.dataset.label + ': ' + fmtMbps(c.parsed.y),
          },
        },
      },
      scales: {
        x: {
          type: 'linear', display: false,
          min: nowMs - windowSecs * 1000 - rightBufferMs, max: nowMs - rightBufferMs,
        },
        y: {
          beginAtZero: true,
          grid: { color: 'rgba(99,130,190,.06)' },
          ticks: {
            color: 'rgba(148,163,190,.4)',
            font: { family: "'JetBrains Mono',monospace", size: 9 },
            callback: (v: number) => fmtMbps(v),
            maxTicksLimit: 4,
          },
        },
      },
    },
  };
}

/**
 * One frame of the compact chart's keepalive — `_bwTick` (app.js:6972).
 *
 * The keepalive owns X scrolling and the Y lerp BETWEEN samples, so the chart
 * slides at 60fps rather than stepping once a second. It mutates `rx`/`tx` in
 * place through `pruneAndMax`, exactly as the original shifts its dataset
 * arrays, and returns the three axis values the caller writes.
 *
 * TWO DIFFERENCES FROM THE DASHBOARD'S TICK, both intentional in the original:
 * there is no 33ms throttle here, and no background-alive guard — this chart
 * self-stops when the page is not being viewed and re-syncs on return, which is
 * why the caller checks visibility before booking the next frame rather than
 * this function doing it.
 */
export function bwTickState(
  rx: XYPoint[], tx: XYPoint[], yCurrent: number, nowMs: number,
  clock: { serverOffset: number; windowSecs: number }, rightBufferMs: number,
): { yMax: number; xMin: number; xMax: number } {
  const sn = nowMs + clock.serverOffset;
  const vl = sn - (clock.windowSecs * 1000) - rightBufferMs;
  const newMax = pruneAndMax(rx, tx, vl);
  // smoothMax carries the original's `|| 1` on the TARGET, so an idle interface
  // eases toward 1 Mbps rather than collapsing to zero.
  return { yMax: smoothMax(yCurrent, newMax), xMin: vl, xMax: sn - rightBufferMs };
}

export function initBandwidthPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbody = el('bwTbody');
  const stats = el('bwStats');
  const search = el<HTMLInputElement>('bwSearch');
  const selIface = el<HTMLSelectElement>('bwIface');
  const selScope = el<HTMLSelectElement>('bwScope');
  const selIpver = el<HTMLSelectElement>('bwIpver');
  const selTopN = el<HTMLSelectElement>('bwTopN');
  if (!tbody) return;

  let data: BandwidthDevice[] = [];
  let sortKey: SortKey = 'totalMbps';
  // -1 is descending, which is the default for every rate column: the question
  // this page answers is "who is using the most".
  let sortDir = -1;

  function filtered(): BandwidthDevice[] {
    const q = (search?.value || '').toLowerCase().trim();
    const iface = selIface?.value || '';
    const scope = selScope?.value || '';
    const ipver = selIpver?.value || '';
    const topN = selTopN ? parseInt(selTopN.value, 10) : 10;

    let out = data.filter((r) => {
      if (q && !(
        r.srcIp.toLowerCase().includes(q) ||
        r.dstIp.toLowerCase().includes(q) ||
        (r.name || '').toLowerCase().includes(q) ||
        (r.mac || '').toLowerCase().includes(q) ||
        (r.org || '').toLowerCase().includes(q)
      )) return false;
      if (iface && r.iface !== iface) return false;
      if (scope === 'lan' && !r.isLan) return false;
      if (scope === 'wan' && r.isLan) return false;
      if (ipver === '4' && r.isIpv6) return false;
      if (ipver === '6' && !r.isIpv6) return false;
      return true;
    });

    out = out.slice().sort((a, b) => {
      const av = (a[sortKey] ?? (typeof a[sortKey] === 'string' ? '' : 0)) as string | number;
      const bv = (b[sortKey] ?? (typeof b[sortKey] === 'string' ? '' : 0)) as string | number;
      if (typeof av === 'string' || typeof bv === 'string') {
        return sortDir === 1
          ? String(av).localeCompare(String(bv))
          : String(bv).localeCompare(String(av));
      }
      return sortDir === -1 ? bv - av : av - bv;
    });

    // Top N AFTER the sort, so it means "the busiest ten" rather than "ten
    // arbitrary rows, then sorted".
    if (topN > 0) out = out.slice(0, topN);
    return out;
  }

  function render(): void {
    const rows = filtered();
    if (!rows.length) {
      tbody!.innerHTML = '<tr><td colspan="8" class="bw-empty">No active bandwidth</td></tr>';
      if (stats) stats.textContent = '';
      return;
    }

    const maxBar = rows.reduce((m, r) => Math.max(m, r.totalMbps), 0.001);

    tbody!.innerHTML = rows.map((r) => {
      const flag = r.country ? '<span class="bw-flag">' + iso2Flag(r.country) + '</span>' : '';
      const dstLabel = r.dstIp
        ? '<span class="bw-ip">' + esc(r.dstIp) + '</span>' +
          (r.country
            ? '<br><span style="font-size:.65rem;color:var(--text-muted)">' + flag + esc(r.country) +
              // A city equal to the country, or a single character, is the geo
              // database saying it does not know — showing it would be noise.
              (r.city && r.city.length > 1 && r.city !== r.country ? ', ' + esc(r.city) : '') +
              '</span>'
            : '')
        : '—';
      const devLabel =
        (r.name ? '<div class="bw-name">' + esc(r.name) + '</div>' : '') +
        '<div class="bw-ip">' + esc(r.srcIp) + '</div>' +
        (r.mac ? '<div class="bw-mac">' + esc(r.mac) + '</div>' : '');
      const orgLabel = r.org ? svcBadge(r.org, r.cat) : '—';
      return '<tr>' +
        '<td>' + devLabel + '</td>' +
        '<td>' + dstLabel + '</td>' +
        '<td class="bw-rate bw-rate-rx">' + fmtMbps(r.rxMbps) + bar(r.rxMbps, maxBar, 'bw-bar-rx') + '</td>' +
        '<td class="bw-rate bw-rate-tx">' + fmtMbps(r.txMbps) + bar(r.txMbps, maxBar, 'bw-bar-tx') + '</td>' +
        '<td class="bw-rate bw-rate-total">' + fmtMbps(r.totalMbps) + '</td>' +
        '<td><span class="bw-ip">' + esc(r.iface || '—') + '</span></td>' +
        '<td>' + protoCell(r.proto) + '</td>' +
        '<td>' + orgLabel + '</td>' +
      '</tr>';
    }).join('');

    if (stats) stats.textContent = rows.length + ' device' + (rows.length !== 1 ? 's' : '');
  }

  function refreshSortHeaders(): void {
    SORT_COLS.forEach((c) => {
      const e = el(c.id);
      if (!e) return;
      e.className = c.key === sortKey ? (sortDir === -1 ? 'sort-desc' : 'sort-asc') : '';
    });
  }

  SORT_COLS.forEach((col) => {
    el(col.id)?.addEventListener('click', () => {
      if (sortKey === col.key) {
        sortDir *= -1;
      } else {
        // Text columns open ASCENDING and rate columns descending, because "who
        // is busiest" and "find this name" are different questions.
        sortKey = col.key;
        sortDir = (col.key === 'name' || col.key === 'proto' || col.key === 'org') ? 1 : -1;
      }
      refreshSortHeaders();
      render();
    });
  });
  refreshSortHeaders();

  [search, selIface, selScope, selIpver, selTopN].forEach((e) => {
    e?.addEventListener('input', debounce(render, 0));
  });

  /**
   * The interface dropdown is seeded from the INTERFACE collector, not from the
   * devices — so an interface with no traffic right now is still offered, and
   * choosing it says "nothing here" rather than not being available to ask.
   *
   * Rebuilt only when the set actually changed: rebuilding it on every payload
   * would close the dropdown under anyone using it.
   */
  socket.on('ifstatus:update', (p: { interfaces?: Array<{ name: string; running: boolean; disabled: boolean; ips: string[] }> }) => {
    if (!selIface) return;
    const ifaces = ((p && p.interfaces) || [])
      .filter((i) => i.running && !i.disabled && i.ips && i.ips.length)
      .map((i) => i.name)
      .sort();
    const existing = Array.from(selIface.options).map((o) => o.value).filter(Boolean).sort();
    if (ifaces.length === existing.length && ifaces.every((n, i) => n === existing[i])) return;
    const cur = selIface.value;
    selIface.innerHTML = '<option value="">All interfaces</option>';
    ifaces.forEach((name) => {
      const o = document.createElement('option');
      o.value = name;
      o.textContent = name;
      if (name === cur) o.selected = true;
      selIface.appendChild(o);
    });
  });

  // ── the compact traffic chart ─────────────────────────────────────────────
  //
  // Plumbing only. Every decision it makes lives in a pinned pure function:
  // `bwChartConfig`, `bwSyncState`, `bwTickState` and the shared
  // `needsFullRedraw`. What is left here is the canvas, the rAF booking and the
  // stat cards.
  //
  // THE BUFFER IS THE DASHBOARD'S, read through `sharedPoints()`. Not a copy:
  // see that function for why a second array fed from the same stream drifts.
  let chart: BwChart | null = null;
  let yCurrent = 0;
  let keepaliveId: number | null = null;

  function syncChart(animated: boolean): void {
    if (!chart) return;
    const st = bwSyncState(sharedPoints(), Date.now(), sharedClock(), RIGHT_BUFFER_MS);
    chart.data.datasets[0]!.data = st.rx;
    chart.data.datasets[1]!.data = st.tx;
    // The redraw SNAPS the axis; only the keepalive eases it. Seeding
    // `yCurrent` here is what stops the first frame lerping up from wherever
    // the previous interface left it.
    yCurrent = st.yMax;
    chart.options.scales.y.max = st.yMax;
    chart.options.scales.x.min = st.xMin;
    chart.options.scales.x.max = st.xMax;
    chart.update(animated ? undefined : 'none');
  }

  function tick(): void {
    // Self-stops when the page is not being viewed, and re-syncs on return —
    // unlike the dashboard's chart, which stays alive. The visibility check is
    // HERE rather than in `bwTickState` so that function stays pure.
    if (!chart || !isVisible('bandwidth') || !sharedClock().lastSampleTs) {
      keepaliveId = null;
      return;
    }
    keepaliveId = requestAnimationFrame(tick);
    const rx = chart.data.datasets[0]!.data;
    const tx = chart.data.datasets[1]!.data;
    const st = bwTickState(rx, tx, yCurrent, Date.now(), sharedClock(), RIGHT_BUFFER_MS);
    yCurrent = st.yMax;
    chart.options.scales.y.max = st.yMax;
    chart.options.scales.x.min = st.xMin;
    chart.options.scales.x.max = st.xMax;
    chart.update('none');
  }
  function startKeepalive(): void {
    if (!keepaliveId) keepaliveId = requestAnimationFrame(tick);
  }

  function makeChart(): void {
    if (chart) { chart.destroy(); chart = null; }
    const canvas = el('bwTrafficChart');
    if (!canvas) return;
    const Ctor = (window as unknown as { Chart?: BwChartCtor }).Chart;
    if (!Ctor) return;
    chart = new Ctor(canvas, bwChartConfig(Date.now(), sharedClock().windowSecs, RIGHT_BUFFER_MS));
  }

  function updateStats(rxMbps: number, txMbps: number): void {
    const rx = splitRate(rxMbps), tx = splitRate(txMbps);
    const set = (id: string, v: string): void => { const n = el(id); if (n) n.textContent = v; };
    set('bwLiveRxNum', rx.num); set('bwLiveRxUnit', rx.unit);
    set('bwLiveTxNum', tx.num); set('bwLiveTxUnit', tx.unit);
  }

  socket.on('traffic:update', (sample: TrafficSample & { ifName?: string }) => {
    // The dashboard's handler updates the shared clock for EVERY sample
    // regardless of which page is open, which is what keeps this keepalive's
    // clock warm when the page is returned to. So this one may bail early.
    if (!isVisible('bandwidth')) return;
    updateStats(sample.rx_mbps, sample.tx_mbps);
    if (!chart) { makeChart(); syncChart(false); startKeepalive(); return; }
    const rx = chart.data.datasets[0]!.data;
    const tx = chart.data.datasets[1]!.data;
    // A gap means a straight line through time that never happened, so rebuild
    // from the buffer instead of appending. Shared with the dashboard chart,
    // which uses the identical rule — unlike the SEEDING, which deliberately
    // differs by three seconds.
    if (needsFullRedraw(rx, sample.ts)) { syncChart(false); startKeepalive(); return; }
    rx.push({ x: sample.ts, y: sample.rx_mbps });
    tx.push({ x: sample.ts, y: sample.tx_mbps });
    startKeepalive();
    // Scale advance and rendering are the keepalive's, not this handler's.
  });

  socket.on('bandwidth:update', (p: BandwidthPayload) => {
    data = (p && p.devices) || [];
    if (isVisible('bandwidth')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    // Picks up whatever arrived while the page was hidden.
    if ((e as CustomEvent).detail === 'bandwidth') render();
  });
}
