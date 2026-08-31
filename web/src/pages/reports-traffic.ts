// The Traffic History and Bandwidth Usage tabs.
//
// ── TWO TABS THAT MUST NOT BLUR INTO EACH OTHER ─────────────────────────────
//
// Traffic is about SPEED: peaks, means, the 95th percentile ISPs bill on, and
// utilisation against the configured line capacity. Bandwidth is about VOLUME:
// how many bytes moved. They are the same measurement at different scalings, and
// the live app keeps them rigorously apart — no Mbps on the bandwidth tab, no
// accumulated totals on the traffic one — because the moment one shows the
// other's units the two tabs stop meaning different things.
//
// ── EVERY FIGURE COMES FROM THE SERVER SUMMARY ──────────────────────────────
//
// Not from `rows`, and this is the tab's most important rule. With an
// aggregation selected the rows are bucket AVERAGES, so a max across them is a
// peak of averages — which buried a 938 Mbps spike as about 4 Mbps on a daily
// view. The rows are also capped by the query LIMIT, so totals reduced from them
// silently under-count a long range. The summary is computed in SQL over the
// whole range and is right regardless of either.

import {
  esc, el, fmtDataMB, fmtMbps, renderSortHeader, sortRows,
  type SortCol, type SortState,
} from '../dom';
import { fmtTs, statCard, utilPct } from './reports';
import { renderTrafficChart, renderBandwidthChart } from './reports-charts';

export interface TrafficRow {
  ts: number;
  interface?: string;
  rx_mbps: number;
  tx_mbps: number;
  rx_max_mbps?: number;
  tx_max_mbps?: number;
  sample_count?: number;
}

export interface BandwidthRow {
  ts: number;
  interface?: string;
  rx_mb: number;
  tx_mb: number;
  rx_max_mb?: number;
  tx_max_mb?: number;
  sample_count?: number;
}

/** The merged rate-and-volume summary both tabs receive. */
export interface IfaceSummary {
  // TWO COUNTS, NAMED APART. Both server-side summaries carry a sample count and
  // the merged object serves BOTH tabs, so a single `samples` key meant whichever
  // one happened to win the merge — the card under the RATE chart reported how
  // many volume rows exist. There is deliberately no `samples` any more, so a
  // consumer has to say which it means. See ifaceSummary in internal/server.
  trafficSamples?: number;
  bandwidthSamples?: number;
  rxAvgMbps?: number | null;
  txAvgMbps?: number | null;
  rxMaxMbps?: number | null;
  txMaxMbps?: number | null;
  rxP95Mbps?: number | null;
  txP95Mbps?: number | null;
  rxTotalMb?: number;
  txTotalMb?: number;
  rxMaxMb?: number | null;
  txMaxMb?: number | null;
  capacityDownMbps?: number;
  capacityUpMbps?: number;
  rxPeakPct?: number | null;
  txPeakPct?: number | null;
  rxP95Pct?: number | null;
  txP95Pct?: number | null;
}

const BW_PAGE_SIZE = 100;

const mbpsOrDash = (v: number | null | undefined): string => (v == null ? '—' : fmtMbps(v));

/**
 * Which bucket a volume peak belongs to.
 *
 * A volume peak is per bucket, so the card has to say WHICH bucket or the number
 * means nothing. Without an aggregation the stored granularity is one minute —
 * hence "Busiest Minute", which is not a unit anyone would guess.
 */
function bucketNoun(agg: string): string {
  return agg === 'hour' ? 'Hour'
    : agg === 'day' ? 'Day'
      : agg === 'week' ? 'Week'
        : agg === 'month' ? 'Month' : 'Minute';
}

// ── Traffic ─────────────────────────────────────────────────────────────────

const TRAFFIC_COLS: SortCol[] = [
  { key: 'ts', label: 'Time', style: '' },
  { key: 'interface', label: 'Interface', style: '' },
  { key: 'rx_mbps', label: 'RX (Mbps)', style: 'text-align:right' },
  { key: 'tx_mbps', label: 'TX (Mbps)', style: 'text-align:right' },
];

let trafficRaw: TrafficRow[] = [];
const trafficSort: SortState = { col: 'ts', dir: 'desc' };

function applyTrafficSort(): void {
  const sorted = sortRows(trafficRaw, trafficSort.col, trafficSort.dir);
  const tbody = el('rptTrafficTbody');
  if (tbody) {
    tbody.innerHTML = sorted.length
      ? sorted.map((r) =>
        '<tr><td style="color:var(--text-muted)">' + esc(fmtTs(r.ts)) + '</td>' +
        '<td style="font-family:var(--font-mono)">' + esc(r.interface || '') + '</td>' +
        // THREE decimals on a rate where volume gets one: a link idling at
        // 0.004 Mbps and one at 0.000 are different facts, and rounding to 0.0
        // loses the distinction this tab exists to show.
        '<td style="text-align:right;font-family:var(--font-mono);color:var(--accent-rx)">' +
        esc((+r.rx_mbps).toFixed(3)) + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);color:var(--accent-tx)">' +
        esc((+r.tx_mbps).toFixed(3)) + '</td></tr>').join('')
      : '<tr><td colspan="4" class="rpt-empty">No data for this range.</td></tr>';
  }
  renderSortHeader('rptTrafficThead', TRAFFIC_COLS, trafficSort, applyTrafficSort);
}

/** The Traffic History tab. `agg` is the aggregate dropdown's current value. */
export function renderTraffic(rows: TrafficRow[], summary: IfaceSummary | null, agg: string): void {
  const s = summary || {};
  const sampleLabel = agg ? 'Buckets' : 'Samples';
  // OVER 100% IS A SIGNAL, NOT AN ERROR. The utilisation is deliberately
  // unclamped — see UtilPct on the server — so a link reporting 177% is telling
  // you the configured capacity is wrong. The warning triangle says so without
  // hiding the number.
  const over = (s.rxPeakPct ?? 0) > 100 || (s.txPeakPct ?? 0) > 100;

  const stats = el('rptTrafficStats');
  if (stats) {
    stats.innerHTML =
      statCard(mbpsOrDash(s.rxMaxMbps), 'Peak RX') +
      statCard(mbpsOrDash(s.txMaxMbps), 'Peak TX') +
      statCard(mbpsOrDash(s.rxAvgMbps), 'Avg RX') +
      statCard(mbpsOrDash(s.txAvgMbps), 'Avg TX') +
      statCard(mbpsOrDash(s.rxP95Mbps), '95th %ile RX') +
      statCard(mbpsOrDash(s.txP95Mbps), '95th %ile TX') +
      statCard(utilPct(s.rxPeakPct ?? null) + ' / ' + utilPct(s.txPeakPct ?? null),
        'Peak Util RX/TX' + (over ? ' ⚠' : '')) +
      // THE COUNT SWITCHES SOURCE with the aggregation: bucket count from the
      // rows, sample count from the summary. `rows.length` is the number of
      // buckets drawn, while the summary count is every sample behind them — and
      // with no aggregation the rows are LIMIT-capped, so only the summary is
      // right. `trafficSamples`, because this card sits under the RATE chart and
      // means rows in traffic_samples.
      statCard((agg ? rows.length : (s.trafficSamples || 0)).toLocaleString(), sampleLabel);
  }

  renderTrafficChart(rows, s, agg);

  trafficRaw = rows;
  trafficSort.col = 'ts';
  trafficSort.dir = 'desc';
  applyTrafficSort();
}

// ── Bandwidth ───────────────────────────────────────────────────────────────

const BW_COLS: SortCol[] = [
  { key: 'ts', label: 'Time', style: '' },
  { key: 'interface', label: 'Interface', style: '' },
  { key: 'rx_mb', label: 'Download (MB)', style: 'text-align:right' },
  { key: 'tx_mb', label: 'Upload (MB)', style: 'text-align:right' },
];

let bwRaw: BandwidthRow[] = [];
let bwRows: BandwidthRow[] = [];
let bwPage = 0;
const bwSort: SortState = { col: 'ts', dir: 'desc' };

/** One page of the bandwidth table. Clamped on render, like the ping pager. */
export function renderBwPage(): void {
  const total = bwRows.length;
  const pages = total ? Math.ceil(total / BW_PAGE_SIZE) : 1;
  if (bwPage >= pages) bwPage = pages - 1;
  const start = bwPage * BW_PAGE_SIZE;
  const slice = bwRows.slice(start, start + BW_PAGE_SIZE);

  const tbody = el('rptBwTbody');
  if (tbody) {
    tbody.innerHTML = slice.length
      ? slice.map((r) =>
        '<tr>' +
        '<td style="color:var(--text-muted)">' + esc(fmtTs(r.ts)) + '</td>' +
        '<td style="font-family:var(--font-mono)">' + esc(r.interface || '') + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);color:var(--accent-rx)">' +
        esc(fmtDataMB(+r.rx_mb)) + '</td>' +
        '<td style="text-align:right;font-family:var(--font-mono);color:var(--accent-tx)">' +
        esc(fmtDataMB(+r.tx_mb)) + '</td></tr>').join('')
      : '<tr><td colspan="4" class="rpt-empty">No data for this range.</td></tr>';
  }

  const pager = el('rptBwPager');
  if (pager) pager.style.display = total > BW_PAGE_SIZE ? '' : 'none';
  const info = el('rptBwPageInfo');
  if (info) {
    info.textContent = 'Page ' + (bwPage + 1) + ' of ' + pages +
      ' (' + total.toLocaleString() + ' rows)';
  }
  const prev = el<HTMLButtonElement>('rptBwPrev');
  if (prev) prev.disabled = bwPage === 0;
  const next = el<HTMLButtonElement>('rptBwNext');
  if (next) next.disabled = bwPage >= pages - 1;
}

function applyBwSort(): void {
  bwRows = sortRows(bwRaw, bwSort.col, bwSort.dir);
  bwPage = 0;
  renderBwPage();
  renderSortHeader('rptBwThead', BW_COLS, bwSort, applyBwSort);
}

/** The Bandwidth Usage tab. */
export function renderBandwidth(
  rows: BandwidthRow[], summary: IfaceSummary | null, agg: string,
): void {
  const s = summary || {};
  const countLabel = agg ? 'Buckets' : 'Samples';

  const stats = el('rptBwStats');
  if (stats) {
    stats.innerHTML =
      statCard(fmtDataMB(s.rxTotalMb), 'Total Download') +
      statCard(fmtDataMB(s.txTotalMb), 'Total Upload') +
      statCard(s.rxMaxMb == null ? '—' : fmtDataMB(s.rxMaxMb), 'Busiest ' + bucketNoun(agg) + ' ↓') +
      statCard(s.txMaxMb == null ? '—' : fmtDataMB(s.txMaxMb), 'Busiest ' + bucketNoun(agg) + ' ↑') +
      // `bandwidthSamples`: this card is under the VOLUME chart.
      statCard((agg ? rows.length : (s.bandwidthSamples || 0)).toLocaleString(), countLabel);
  }

  // THE CARDS AND THE TABLE DISAGREE ON PURPOSE, AND THE PAGE SAYS SO. The stat
  // cards cover the whole range because they come from SQL; the chart and table
  // show only the rows that fit under the query LIMIT. Letting those two sit side
  // by side unexplained is how a total that "does not add up" becomes a support
  // question.
  const hint = el('rptBwTruncHint');
  if (hint) {
    const truncated = !agg && !!s.bandwidthSamples && s.bandwidthSamples > rows.length;
    hint.style.display = truncated ? '' : 'none';
    if (truncated) {
      hint.textContent = 'Chart and table show ' + rows.length.toLocaleString() +
        ' of ' + (s.bandwidthSamples as number).toLocaleString() +
        ' samples — choose an aggregation to cover the full range. ' +
        'Totals above are for the full range.';
    }
  }

  renderBandwidthChart(rows, agg);

  bwRaw = rows;
  bwSort.col = 'ts';
  bwSort.dir = 'desc';
  applyBwSort();
}

/** Wire the bandwidth pager buttons once. */
export function wireBwPager(): void {
  el('rptBwPrev')?.addEventListener('click', () => {
    if (bwPage > 0) {
      bwPage--;
      renderBwPage();
    }
  });
  el('rptBwNext')?.addEventListener('click', () => {
    const pages = Math.ceil(bwRows.length / BW_PAGE_SIZE);
    if (bwPage < pages - 1) {
      bwPage++;
      renderBwPage();
    }
  });
}
