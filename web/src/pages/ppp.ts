// The PPP page — a port of the PPP IIFE in public/app.js (issue #32).
//
// Rates are derived SERVER-SIDE from byte deltas, because RouterOS reports
// cumulative bytes only. A null rate means "no measurement window yet", not
// idle, and this page renders that as an em dash rather than as zero — the
// distinction is the whole reason the collector makes the field nullable.
//
// The markup strings, class names and em dashes are the live app's. The
// acceptance criterion is that it renders identically, not that it renders
// correctly.

import { esc, el, debounce, renderSortHeader, sortMul, fmtMbps, fmtBytes,
         parseUptime, type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';

export interface PppSession {
  id: string; name: string; service: string; address: string; callerId: string;
  uptime: string; encoding: string; sessionId: string;
  limitIn: number | null; limitOut: number | null;
  rx: number; tx: number;
  rxRate: number | null; txRate: number | null;
}

export interface PppProfile {
  name: string; localAddress: string; remoteAddress: string;
  rateLimit: string; onlyOne: string; encryption: string;
}

export interface PppServer {
  serviceName: string; interface: string; maxSessions: string;
  auth: string; disabled: boolean;
}

export interface PppPayload {
  ts: number; pollMs: number;
  sessions: PppSession[]; profiles: PppProfile[]; servers: PppServer[];
  byService: Record<string, number>;
  totalRxRate: number | null; totalTxRate: number | null;
  available: boolean;
}

const COLS: SortCol[] = [
  { key: 'name', label: 'User' },
  { key: 'service', label: 'Service' },
  { key: 'address', label: 'Address' },
  { key: 'callerId', label: 'Caller ID' },
  { key: 'uptime', label: 'Uptime' },
  { key: 'rate', label: 'RX / TX' },
  { key: 'total', label: 'Total In / Out' },
];

export function initPppPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const tbodyEl = el('pppTable');
  const theadRow = el('pppThead');
  // Bails on a page that is not in the document, exactly as the live IIFE does.
  if (!tbodyEl || !theadRow) return;
  // Re-bound so the narrowing survives into the closures below; `render` is
  // called from four places and none of them can re-check.
  const tbody: HTMLElement = tbodyEl;

  let data: PppPayload | null = null;
  const sort: SortState = { col: 'name', dir: 'asc' };

  /**
   * The sort key for one column.
   *
   * UPTIME SORTS ON A FORMATTED STRING, and that is faithful rather than sloppy:
   * `parseUptime` returns "1w 2d 3h", the live `sortVal` returns it unchanged,
   * and the comparator below then takes the string branch. So the column orders
   * lexicographically and puts "10m" before "2h". Reported upstream; reproduced
   * here because the ordering is on screen.
   */
  function sortVal(s: PppSession, key: string): string | number {
    if (key === 'rate') return (s.rxRate || 0) + (s.txRate || 0);
    if (key === 'total') return s.rx + s.tx;
    if (key === 'uptime') return parseUptime(s.uptime);
    return String((s as unknown as Record<string, unknown>)[key] || '').toLowerCase();
  }

  function render(): void {
    if (!data) return;
    const search = el<HTMLInputElement>('pppSearch');
    const q = ((search && search.value) || '').toLowerCase().trim();

    const rows = data.sessions.filter((s) => {
      if (!q) return true;
      return (s.name + ' ' + s.address + ' ' + s.callerId).toLowerCase().indexOf(q) !== -1;
    }).slice().sort((a, b) => {
      const av = sortVal(a, sort.col);
      const bv = sortVal(b, sort.col);
      if (typeof av === 'string') return sortMul(sort) * av.localeCompare(bv as string);
      return sortMul(sort) * ((av as number) - (bv as number));
    });

    renderSortHeader('pppThead', COLS, sort, () => render());

    const badge = el('pppBadge');
    if (badge) {
      badge.textContent = String(data.sessions.length);
      badge.className = 'card-badge' + (data.sessions.length ? ' active-blue' : '');
    }

    // Two different empty states, and the difference is the point: a router with
    // no PPP service is not the same as a router whose sessions have all gone.
    const empty = data.available
      ? 'No active PPP sessions. They appear here when a PPPoE, L2TP, SSTP or PPTP client connects.'
      : 'This router has no PPP service configured.';

    tbody.innerHTML = rows.length ? rows.map((s) => {
      const r = s.rxRate === null
        ? '<span style="color:var(--text-muted)" title="No measurement window yet">&mdash;</span>'
        : '<span style="color:var(--accent-rx)">' + fmtMbps((s.rxRate * 8) / 1e6) + '</span> / ' +
          '<span style="color:var(--accent-tx,#f59f00)">' + fmtMbps(((s.txRate as number) * 8) / 1e6) + '</span>';
      return '<tr>' +
        '<td>' + esc(s.name) + '</td>' +
        '<td><span class="vpn-proto-pill">' + esc(s.service || 'PPP') + '</span></td>' +
        '<td>' + esc(s.address) + '</td>' +
        '<td>' + esc(s.callerId) + '</td>' +
        '<td>' + esc(s.uptime) + '</td>' +
        '<td>' + r + '</td>' +
        '<td>' + fmtBytes(s.rx) + ' / ' + fmtBytes(s.tx) + '</td>' +
      '</tr>';
    }).join('') : '<tr><td colspan="7" class="empty-state">' + esc(empty) + '</td></tr>';

    renderConfig();
  }

  // Servers and profiles share one table, distinguished by a Kind pill. Servers
  // first, then profiles — the order is the order of the two loops in the
  // original, and it is visible.
  function renderConfig(): void {
    const tb = el('pppServerTable');
    if (!tb || !data) return;
    const rows: string[][] = [];
    for (const s of data.servers || []) {
      rows.push(['Server', s.serviceName || '(unnamed)', s.interface,
        s.maxSessions ? ('max ' + s.maxSessions) : '',
        s.disabled ? 'disabled' : 'enabled']);
    }
    for (const p of data.profiles || []) {
      rows.push(['Profile', p.name, p.localAddress, p.rateLimit || p.remoteAddress || '', p.onlyOne || '']);
    }
    tb.innerHTML = rows.length ? rows.map((r) =>
      '<tr><td><span class="wl-band wl-band-24">' + esc(r[0] as string) + '</span></td>' +
      r.slice(1).map((c) => '<td>' + (c ? esc(String(c)) : '<span style="color:var(--text-muted)">&mdash;</span>') + '</td>').join('') +
      '</tr>').join('') : '<tr><td colspan="5" class="empty-state">No PPP servers or profiles.</td></tr>';
  }

  function renderSummary(): void {
    if (!data) return;
    const count = el('pppSumCount');
    if (count) count.textContent = String(data.sessions.length);

    // Plain lexicographic sort, not localeCompare: `Object.keys(...).sort()` in
    // the original, which is the bare comparison.
    const svc = Object.keys(data.byService || {}).sort();
    const services = el('pppSumServices');
    if (services) {
      services.textContent = svc.length
        ? svc.map((k) => k + ' ' + (data as PppPayload).byService[k]).join('  ') : '—';
    }

    const toMbps = (v: number | null): number | null => (v === null ? null : (v * 8) / 1e6);
    const rx = toMbps(data.totalRxRate);
    const tx = toMbps(data.totalTxRate);
    const rxEl = el('pppSumRx');
    const txEl = el('pppSumTx');
    // innerHTML, not textContent: the placeholder is an entity, and the live app
    // assigns it the same way.
    if (rxEl) rxEl.innerHTML = rx === null ? '&mdash;' : fmtMbps(rx);
    if (txEl) txEl.innerHTML = tx === null ? '&mdash;' : fmtMbps(tx);
  }

  socket.on('ppp:update', (d: PppPayload) => {
    if (!d) return;
    data = d;
    // The summary updates whether or not the page is showing; the table only
    // when it is. That asymmetry is the original's, and it is why arriving on
    // the page fires a render of its own, below.
    renderSummary();
    if (isVisible('ppp')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'ppp' && data) render();
  });

  const se = el<HTMLInputElement>('pppSearch');
  se?.addEventListener('input', debounce(render, 150));
}
