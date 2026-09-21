// The Connections List tab, as pure functions: the rows of `conn:list` in,
// markup out. connections.ts owns the wiring (tabs, socket, paging state);
// nothing here touches the page, so all of it is testable without one.

import { esc, fmtBytes, fmtMbps, iso2Flag, protoPill, type SortCol } from '../dom';
import type { ConnRow } from '../gen/payloads';
import { CC_NAMES, PORT_NAMES } from './connections-map';

/** Rows a page of the table holds. */
export const PAGE_SIZE = 100;

export const CONN_COLS: SortCol[] = [
  { key: 'who', label: 'Client' },
  { key: 'dst', label: 'Destination' },
  { key: 'svc', label: 'Service' },
  { key: 'proto', label: 'Protocol' },
  { key: 'state', label: 'State' },
  { key: 'txr', label: '↑ TX', cls: 'conn-num' },
  { key: 'rxr', label: '↓ RX', cls: 'conn-num' },
];

/** A row with the fields its sortable columns read. A missing rate sorts
 *  below every real one. */
export type SortableConn = ConnRow & { who: string; svc: number; txr: number; rxr: number };

export function sortable(r: ConnRow): SortableConn {
  return { ...r, who: (r.client || r.src).toLowerCase(), svc: Number(r.dstPort) || 0,
    txr: r.txRate ?? -1, rxr: r.rxRate ?? -1 };
}

/** The service a port is known as, or '' when it has no common name. */
export function serviceOf(port: string): string {
  return PORT_NAMES[port] ?? '';
}

/**
 * The rows matching a client (an exact source address) and a search, which
 * matches every word against everything a row shows: client, both addresses
 * and ports, the service, protocol, state, organisation and country, by code
 * or by name.
 */
export function filterConns<T extends ConnRow>(rows: T[], query: string, client: string): T[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  return rows.filter((r) => {
    if (client && r.src !== client) return false;
    if (!words.length) return true;
    const hay = [r.client, r.src, r.dst + ':' + r.dstPort, serviceOf(r.dstPort), r.proto,
      r.state, r.org, r.country, CC_NAMES[r.country] ?? ''].join(' ').toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

const STATE_CLS: Record<string, string> = {
  established: 'conn-st-ok',
  'syn-sent': 'conn-st-warn', 'syn-received': 'conn-st-warn',
  'time-wait': 'conn-st-muted', close: 'conn-st-muted', 'close-wait': 'conn-st-muted',
  'fin-wait': 'conn-st-muted', 'last-ack': 'conn-st-muted',
};

/** The TCP state as a pill; a dash for protocols that have none. */
export function statePill(state: string): string {
  if (!state) return '<span class="conn-sub">—</span>';
  return '<span class="conn-st ' + (STATE_CLS[state] ?? 'conn-st-other') + '">' + esc(state) + '</span>';
}

/** A byte rate as the app writes rates: bits per second. */
function rate(bytesPerSec: number | null): string {
  return bytesPerSec === null ? '—' : fmtMbps((bytesPerSec * 8) / 1e6);
}

function addr(ip: string, port: string): string {
  if (!ip) return '—';
  const host = ip.includes(':') ? '[' + ip + ']' : ip;
  return esc(host) + (port ? '<span class="conn-port">:' + esc(port) + '</span>' : '');
}

/** One row. */
export function connRowHTML(r: ConnRow): string {
  const who = r.client
    ? '<div class="conn-who">' + esc(r.client) + '</div><div class="conn-sub">' + addr(r.src, '') + '</div>'
    : '<div class="conn-who">' + addr(r.src, '') + '</div>' +
      (r.local ? '' : '<div class="conn-sub">from outside</div>');
  const flag = r.country ? '<span class="conn-flag" title="' + esc(CC_NAMES[r.country] ?? r.country) + '">' +
    iso2Flag(r.country) + '</span>' : '';
  const where = r.dstLocal ? 'LAN' : r.org || (r.country ? CC_NAMES[r.country] ?? r.country : '');
  const svc = serviceOf(r.dstPort);
  return '<tr>' +
    '<td>' + who + '</td>' +
    '<td><div class="conn-who">' + flag + addr(r.dst, r.dstPort) + '</div><div class="conn-sub">' + esc(where) +
    '</div></td>' +
    '<td>' + (svc ? '<span class="conn-svc">' + esc(svc) + '</span>'
      : (r.dstPort ? '<span class="conn-sub">' + esc(r.dstPort) + '</span>' : '<span class="conn-sub">—</span>')) +
    '</td>' +
    '<td>' + protoPill(r.proto) + '</td>' +
    '<td>' + statePill(r.state) + '</td>' +
    '<td class="conn-num"><div class="conn-rate conn-tx">' + rate(r.txRate) + '</div><div class="conn-sub">' +
    fmtBytes(r.tx) + '</div></td>' +
    '<td class="conn-num"><div class="conn-rate conn-rx">' + rate(r.rxRate) + '</div><div class="conn-sub">' +
    fmtBytes(r.rx) + '</div></td>' +
    '</tr>';
}

/** The page of rows to show, clamped to the pages there are. */
export function pageOf<T>(rows: T[], page: number): { rows: T[]; page: number; pages: number } {
  const pages = Math.max(1, Math.ceil(rows.length / PAGE_SIZE));
  const p = Math.min(Math.max(0, page), pages - 1);
  return { rows: rows.slice(p * PAGE_SIZE, (p + 1) * PAGE_SIZE), page: p, pages };
}

/** The pager: where these rows sit among all that match, and the way on. */
export function pagerHTML(shown: number, matched: number, page: number, pages: number): string {
  if (!matched) return '';
  const from = page * PAGE_SIZE + 1;
  return '<span class="conn-sub">' + from.toLocaleString() + '–' + (from + shown - 1).toLocaleString() + ' of ' +
    matched.toLocaleString() + '</span>' +
    '<button class="conn-page-btn" type="button" data-conn-page="' + (page - 1) + '"' + (page <= 0 ? ' disabled' : '') +
    ' aria-label="Previous page">‹</button>' +
    '<span class="conn-sub">page ' + (page + 1) + ' of ' + pages + '</span>' +
    '<button class="conn-page-btn" type="button" data-conn-page="' + (page + 1) + '"' +
    (page >= pages - 1 ? ' disabled' : '') + ' aria-label="Next page">›</button>';
}
