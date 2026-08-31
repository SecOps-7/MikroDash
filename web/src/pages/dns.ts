// The DNS page — a port of the DNS IIFE in public/app.js.
//
// Line for line where it matters. The markup strings, the class names, the em
// dashes and the order of the key/value rows are the live app's, because the
// acceptance criterion for this page is that it renders identically, not that
// it renders correctly.

import { esc, el, resRow, debounce, renderSortHeader, sortMul,
         type SortCol, type SortState } from '../dom';
import type { Socket } from '../socket';
import { mountAdds, mountRows } from '../resource';

export interface DNSSettings {
  servers: string[];
  dynamicServers: string[];
  dohEnabled: boolean;
  dohUrl: string;
  dohVerifyCert: boolean;
  dohMaxServerConnections: number | null;
  dohMaxConcurrentQueries: number | null;
  dohTimeout: string;
  allowRemoteRequests: boolean;
  cacheSize: number | null;
  cacheUsed: number | null;
  cacheMaxTtl: string;
  maxUdpPacketSize: number | null;
  maxConcurrentQueries: number | null;
  queryServerTimeout: string;
  queryTotalTimeout: string;
  mdnsRepeatIfaces: string[];
  vrf: string;
}

export interface DNSStaticEntry {
  id: string;
  name: string;
  regexp: string;
  address: string;
  type: string;
  ttl: string;
  disabled: boolean;
  comment: string;
}

export interface DNSPayload {
  ts: number;
  pollMs: number;
  settings: DNSSettings;
  staticEntries: DNSStaticEntry[];
  available: boolean;
}

const COLS_S: SortCol[] = [
  { key: 'name', label: 'Name' },
  { key: 'address', label: 'Address' },
  { key: 'type', label: 'Type' },
  { key: 'ttl', label: 'TTL' },
  { key: 'comment', label: 'Comment' },
];

function kv(key: string, val: string, cls?: string): string {
  return '<div class="kv-item"><div class="kv-key">' + esc(key) + '</div>' +
    '<div class="kv-val' + (cls ? ' ' + cls : '') + '">' + val + '</div></div>';
}

export function initDnsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const settingsBody = el('dnsSettingsBody');
  if (!settingsBody) return;

  let data: DNSPayload | null = null;
  const sortS: SortState = { col: 'name', dir: 'asc' };

  function renderSettings(): void {
    const s = data?.settings;
    if (!s) return;
    const servers = s.servers.length ? s.servers.join(', ')
      : s.dynamicServers.length ? s.dynamicServers.join(', ') + ' (dynamic)'
      : '—';
    let html = '';
    html += s.dohEnabled
      ? kv('DNS over HTTPS', esc(s.dohUrl), 'on') +
        kv('Certificate check', s.dohVerifyCert ? 'verified' : 'NOT verified',
          s.dohVerifyCert ? 'on' : 'warn')
      : kv('DNS over HTTPS', 'off', 'off');
    html += kv('Servers', esc(servers));
    html += kv('Allow remote requests', s.allowRemoteRequests ? 'yes' : 'no',
      s.allowRemoteRequests ? 'warn' : 'off');
    html += kv('Cache', (s.cacheUsed === null ? '—' : String(s.cacheUsed)) + ' / ' +
      (s.cacheSize === null ? '—' : String(s.cacheSize)) + ' KiB');
    html += kv('Cache max TTL', esc(s.cacheMaxTtl || '—'));
    html += kv('mDNS repeat', esc(s.mdnsRepeatIfaces.join(', ') || '—'));
    html += kv('Max UDP packet', s.maxUdpPacketSize === null ? '—' : String(s.maxUdpPacketSize));
    html += kv('Query timeout', esc(s.queryServerTimeout || '—') + ' / ' + esc(s.queryTotalTimeout || '—'));
    settingsBody!.innerHTML = html;
  }

  function sorted(rows: DNSStaticEntry[], sort: SortState): DNSStaticEntry[] {
    return rows.slice().sort((a, b) => {
      const av = ((a as unknown as Record<string, unknown>)[sort.col] ?? '').toString().toLowerCase();
      const bv = ((b as unknown as Record<string, unknown>)[sort.col] ?? '').toString().toLowerCase();
      return sortMul(sort) * av.localeCompare(bv);
    });
  }

  function renderStatic(): void {
    const rows = data?.staticEntries || [];
    const search = el<HTMLInputElement>('dnsStaticSearch');
    const q = (search?.value || '').toLowerCase().trim();
    const list = rows.filter((e) => {
      if (!q) return true;
      return ((e.name || e.regexp) + ' ' + e.address).toLowerCase().indexOf(q) !== -1;
    });
    renderSortHeader('dnsStaticThead', COLS_S, sortS, () => render());
    const badge = el('dnsStaticBadge');
    if (badge) badge.textContent = String(rows.length);
    const table = el('dnsStaticTable');
    if (!table) return;
    table.innerHTML = list.length ? sorted(list, sortS).map((e) =>
      '<tr' + (e.disabled ? ' style="opacity:.55"' : '') + resRow(e.id, e.name) + '>' +
      '<td>' + esc(e.name || e.regexp) +
        (e.regexp ? ' <span class="wl-band wl-band-24">regexp</span>' : '') + '</td>' +
      '<td class="mono">' + esc(e.address) + '</td>' +
      '<td>' + esc(e.type) + '</td>' +
      '<td>' + esc(e.ttl || '—') + '</td>' +
      '<td style="color:var(--text-muted)">' + esc(e.comment || '') + '</td>' +
      '</tr>').join('')
      : '<tr><td colspan="5" class="empty-state">' +
        (q ? 'No entries match that search.' : 'No static DNS entries.') + '</td></tr>';
  }

  function render(): void {
    if (!data) return;
    renderSettings();
    renderStatic();
  }

  function renderSummary(): void {
    if (!data) return;
    const s = data.settings;
    const cache = el('dnsSumCache');
    if (cache) {
      cache.textContent = (s.cacheUsed === null || s.cacheUsed === undefined)
        ? '—' : s.cacheUsed + ' / ' + (s.cacheSize || '?');
    }
    const stat = el('dnsSumStatic');
    if (stat) stat.textContent = String((data.staticEntries || []).length);
    const srv = el('dnsSumServers');
    if (srv) {
      srv.textContent = s.dohEnabled ? 'DoH'
        : (s.servers && s.servers.length) ? 'static'
        : (s.dynamicServers && s.dynamicServers.length) ? 'dynamic' : '—';
    }
    const remote = el('dnsSumRemote');
    if (remote) remote.textContent = s.allowRemoteRequests ? 'allowed' : 'blocked';
  }

  socket.on('dns:update', (d: DNSPayload) => {
    if (!d) return;
    data = d;
    renderSummary();
    if (isVisible('dns')) render();
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'dns' && data) render();
  });

  const ss = el<HTMLInputElement>('dnsStaticSearch');
  if (ss) ss.addEventListener('input', debounce(renderStatic, 150));

  // The row and the Add button both open the resource form. The row carries the
  // `.id` and the identity that resRow() wrote onto it, which is what lets the
  // server refuse a write against a row that has changed underneath.
  mountAdds(socket);
  mountRows(socket);
}
