// The Ping and Connectivity tabs: stat cards, sortable tables, and the ping
// pager.
//
// Two tabs in one module because they are the two that read a plain event series
// and nothing else — no interface picker, no capacity, no volume. Traffic and
// bandwidth share a different set of concerns and get their own.

import { esc, el, maxOf, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { fmtTs, fmtDuration, statCard } from './reports';
import { renderPingChart } from './reports-charts';

/** One raw or aggregated ping row, as the endpoint sends it. */
export interface PingRow {
  ts: number;
  target?: string;
  /** Null for a probe that timed out. The table shows an em dash, never a zero. */
  rtt_ms: number | null;
  loss_pct: number;
  sample_count?: number;
}

/** A connectivity row: raw when it has `connected`, aggregated when it has `total`. */
export interface ConnRow {
  ts: number;
  connected?: number;
  downtime_ms?: number | null;
  total?: number;
  online?: number;
  offline?: number;
  uptime_pct?: number;
}

/** The live page's page size. A hundred rows is what its pager assumes. */
const PING_PAGE_SIZE = 100;

// ── Ping ────────────────────────────────────────────────────────────────────

const PING_COLS: SortCol[] = [
  { key: 'ts', label: 'Time', style: '' },
  { key: 'target', label: 'Target', style: '' },
  { key: 'rtt_ms', label: 'RTT (ms)', style: 'text-align:right' },
  { key: 'loss_pct', label: 'Loss %', style: 'text-align:right' },
];

let pingRaw: PingRow[] = [];
let pingRows: PingRow[] = [];
let pingPage = 0;
const pingSort: SortState = { col: 'ts', dir: 'desc' };

/**
 * One page of the ping table.
 *
 * THE PAGE INDEX IS CLAMPED ON RENDER rather than on the click. Re-sorting or
 * re-loading can shrink the row count under a page the operator is already on,
 * and clamping here covers every route into this function — including the ones
 * that do not go through a button.
 */
export function renderPingPage(): void {
  const total = pingRows.length;
  const pages = total ? Math.ceil(total / PING_PAGE_SIZE) : 1;
  if (pingPage >= pages) pingPage = pages - 1;
  const start = pingPage * PING_PAGE_SIZE;
  const slice = pingRows.slice(start, start + PING_PAGE_SIZE);

  const tbody = el('rptPingTbody');
  if (tbody) {
    tbody.innerHTML = slice.length
      ? slice.map((r) => {
        // Loss is coloured by severity, and the thresholds are the original's:
        // anything above zero warns, 5% or more is an error. A link losing one
        // packet in a hundred is not healthy and does not read as an outage.
        const lossClass = r.loss_pct >= 5
          ? ' style="color:var(--accent-err)"'
          : r.loss_pct > 0 ? ' style="color:var(--accent-warn)"' : '';
        return '<tr><td style="color:var(--text-muted)">' + esc(fmtTs(r.ts)) + '</td>' +
          '<td style="font-family:var(--font-mono)">' + esc(r.target || '') + '</td>' +
          '<td style="text-align:right;font-family:var(--font-mono)">' +
          (r.rtt_ms != null ? esc((+r.rtt_ms).toFixed(1)) : '—') + '</td>' +
          '<td style="text-align:right;font-family:var(--font-mono)"' + lossClass + '>' +
          esc((+r.loss_pct).toFixed(1)) + '%</td></tr>';
      }).join('')
      : '<tr><td colspan="4" class="rpt-empty">No data for this range.</td></tr>';
  }

  const pager = el('rptPingPager');
  if (pager) pager.style.display = total > PING_PAGE_SIZE ? '' : 'none';
  const info = el('rptPingPageInfo');
  // `toLocaleString` on the count, so 12,345 rows reads as a number rather than
  // a serial. The original's, and it follows the browser's locale.
  if (info) {
    info.textContent = 'Page ' + (pingPage + 1) + ' of ' + pages +
      ' (' + total.toLocaleString() + ' rows)';
  }
  const prev = el<HTMLButtonElement>('rptPingPrev');
  if (prev) prev.disabled = pingPage === 0;
  const next = el<HTMLButtonElement>('rptPingNext');
  if (next) next.disabled = pingPage >= pages - 1;
}

function applyPingSort(): void {
  pingRows = sortRows(pingRaw, pingSort.col, pingSort.dir);
  pingPage = 0;
  renderPingPage();
  renderSortHeader('rptPingThead', PING_COLS, pingSort, applyPingSort);
}

/**
 * The Ping tab.
 *
 * ── THE STATS COME FROM THE ROWS HERE, UNLIKE TRAFFIC ───────────────────────
 *
 * The traffic tab reads its peaks off the server summary, because an aggregated
 * row is a bucket average and the max across those is a peak of averages. Ping
 * does not: `uptime` is the share of samples under 1% loss, a property of the
 * ROWS the operator is looking at, and there is no server-side summary for it.
 */
export function renderPing(rows: PingRow[]): void {
  const rtts = rows.filter((r) => r.rtt_ms != null).map((r) => r.rtt_ms as number);
  const losses = rows.map((r) => r.loss_pct);
  const avgRtt = rtts.length ? (rtts.reduce((a, b) => a + b, 0) / rtts.length).toFixed(1) : '—';
  // `maxOf`, not `Math.max(...rtts)`: the spread passes every element as an
  // argument and overflows the stack past ~65k of them, and a ping query returns
  // up to 100,000 rows. The live app documents this on its own maxOf; this port
  // wrote the spread anyway and it would have crashed on exactly the long ranges
  // an operator opens when something is wrong.
  const maxRtt = rtts.length ? maxOf(rtts).toFixed(1) : '—';
  const avgLoss = losses.length
    ? (losses.reduce((a, b) => a + b, 0) / losses.length).toFixed(1) : '—';
  // UNDER 1% LOSS COUNTS AS UP. Not "zero loss": a single dropped probe in a
  // hundred is normal on a live link, and a strict test would report 60% uptime
  // for a connection nobody noticed a problem with.
  const uptime = losses.length
    ? ((losses.filter((l) => l < 1).length / losses.length) * 100).toFixed(1) + '%' : '—';

  const stats = el('rptPingStats');
  if (stats) {
    stats.innerHTML =
      statCard(uptime, 'Uptime') +
      statCard(avgRtt !== '—' ? avgRtt + ' ms' : '—', 'Avg RTT') +
      statCard(maxRtt !== '—' ? maxRtt + ' ms' : '—', 'Max RTT') +
      statCard(avgLoss !== '—' ? avgLoss + '%' : '—', 'Avg Loss') +
      statCard(rows.length.toLocaleString(), 'Samples');
  }

  renderPingChart(rows);

  pingRaw = rows;
  pingSort.col = 'ts';
  pingSort.dir = 'desc';
  applyPingSort();
}

/** Wire the pager buttons once. */
export function wirePingPager(): void {
  el('rptPingPrev')?.addEventListener('click', () => {
    if (pingPage > 0) {
      pingPage--;
      renderPingPage();
    }
  });
  el('rptPingNext')?.addEventListener('click', () => {
    const pages = Math.ceil(pingRows.length / PING_PAGE_SIZE);
    if (pingPage < pages - 1) {
      pingPage++;
      renderPingPage();
    }
  });
}

// ── Connectivity ────────────────────────────────────────────────────────────

const CONN_COLS_AGG: SortCol[] = [
  { key: 'ts', label: 'Time', style: '' },
  { key: 'total', label: 'Total', style: 'text-align:right' },
  { key: 'online', label: 'Online', style: 'text-align:right' },
  { key: 'offline', label: 'Offline', style: 'text-align:right' },
  { key: 'uptime_pct', label: 'Uptime&nbsp;%', style: 'text-align:right' },
];

const CONN_COLS_RAW: SortCol[] = [
  { key: 'ts', label: 'Time', style: '' },
  { key: 'connected', label: 'Status', style: '' },
  { key: 'downtime_ms', label: 'Down Duration', style: 'text-align:right' },
];

let connRaw: ConnRow[] = [];
const connSort: SortState = { col: 'ts', dir: 'desc' };
/** Which shape the last load returned. See isAggregated. */
let connAgg = false;

/**
 * Whether these rows are the aggregated shape.
 *
 * DECIDED FROM THE ROWS, NOT FROM THE DROPDOWN. The original tests
 * `agg && rows[0].total !== undefined` — the select says what was ASKED for and
 * the rows say what came back, and they disagree for the moment between changing
 * the dropdown and the response arriving. Reading the row is what stops an
 * aggregated header being drawn over raw rows.
 */
function isAggregated(rows: ConnRow[], agg: string): boolean {
  // `rows[0]` is indexed under noUncheckedIndexedAccess, so it is typed as
  // possibly undefined even after the length check. The optional chain says the
  // same thing the length check does and satisfies the compiler without a cast.
  return !!agg && rows.length > 0 && rows[0]?.total !== undefined;
}

function applyConnSort(): void {
  const sorted = sortRows(connRaw, connSort.col, connSort.dir);
  const cols = connAgg ? CONN_COLS_AGG : CONN_COLS_RAW;
  const tbody = el('rptConnTbody');
  if (tbody) {
    if (!sorted.length) {
      tbody.innerHTML = '<tr><td colspan="' + (connAgg ? 5 : 3) +
        '" class="rpt-empty">No connectivity events for this range.</td></tr>';
    } else if (connAgg) {
      tbody.innerHTML = sorted.map((r) =>
        '<tr>' +
        '<td style="font-family:var(--font-mono);font-size:.71rem;color:var(--text-muted)">' +
        esc(fmtTs(r.ts)) + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);font-size:.71rem">' +
        esc(String(r.total)) + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);font-size:.71rem;color:var(--accent-ok,#4ade80)">' +
        esc(String(r.online)) + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);font-size:.71rem;color:var(--accent-err,#f87171)">' +
        esc(String(r.offline)) + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);font-size:.71rem">' +
        esc((+(r.uptime_pct as number)).toFixed(1)) + '%</td></tr>').join('');
    } else {
      tbody.innerHTML = sorted.map((r) => {
        const badge = r.connected
          ? '<span class="rtr-status-badge rtr-status-badge--on">Online</span>'
          : '<span class="rtr-status-badge rtr-status-badge--off">Offline</span>';
        // "ONGOING" IS NOT ZERO. A null duration on an offline row means the
        // outage has no end yet — the annotation walks backwards and finds no
        // online event after it — and showing 0s would report a router that is
        // still down as one that recovered instantly.
        const dur = !r.connected
          ? (r.downtime_ms != null
            ? esc(fmtDuration(r.downtime_ms))
            : '<span style="color:var(--accent-warn)">Ongoing</span>')
          : '<span style="color:var(--text-muted)">—</span>';
        return '<tr>' +
          '<td style="font-family:var(--font-mono);font-size:.71rem;color:var(--text-muted)">' +
          esc(fmtTs(r.ts)) + '</td>' +
          '<td>' + badge + '</td>' +
          '<td style="text-align:right;font-family:var(--font-mono);font-size:.71rem">' + dur + '</td>' +
          '</tr>';
      }).join('');
    }
  }
  renderSortHeader('rptConnThead', cols, connSort, applyConnSort);
}

/** The Connectivity tab. `agg` is the aggregate dropdown's current value. */
export function renderConn(rows: ConnRow[], agg: string): void {
  connAgg = isAggregated(rows, agg);

  let onlineN: number;
  let offlineN: number;
  let uptime: string;
  let totalDownMs: number | null;
  if (connAgg) {
    onlineN = rows.reduce((a, r) => a + +(r.online as number), 0);
    offlineN = rows.reduce((a, r) => a + +(r.offline as number), 0);
    const total = onlineN + offlineN;
    uptime = total ? ((onlineN / total) * 100).toFixed(1) + '%' : '—';
    // No downtime total from buckets: a bucket knows how many samples were
    // offline, not how long any outage lasted.
    totalDownMs = null;
  } else {
    onlineN = rows.filter((r) => r.connected).length;
    offlineN = rows.length - onlineN;
    uptime = rows.length ? ((onlineN / rows.length) * 100).toFixed(1) + '%' : '—';
    totalDownMs = rows.reduce((a, r) => a + (r.downtime_ms || 0), 0);
  }

  const stats = el('rptConnStats');
  if (stats) {
    // THE FOURTH CARD CHANGES IDENTITY, which is the original's shape: it is
    // "Total Downtime" only when there is downtime to report, and the event
    // count otherwise. A "Total Downtime: 0s" card on a healthy week reads as a
    // measurement that failed rather than a week with no outages.
    stats.innerHTML =
      statCard(uptime, 'Connection Uptime') +
      statCard(onlineN, 'Online Events') +
      statCard(offlineN, 'Offline Events') +
      (!connAgg && totalDownMs
        ? statCard(fmtDuration(totalDownMs), 'Total Downtime')
        : statCard(rows.length, connAgg ? 'Buckets' : 'Total Events'));
  }

  connRaw = rows;
  connSort.col = 'ts';
  connSort.dir = 'desc';
  applyConnSort();
}
