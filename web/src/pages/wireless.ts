// The Wifi Clients page — a port of the `── Wireless` IIFE in public/app.js.
//
// Who is CONNECTED. What this router broadcasts is the Wifi Networks page, and
// deliberately a different collector on a different cadence.
//
// ── THE PALETTE LIVES IN dom.ts, NOT HERE ───────────────────────────────────
//
// The live app defines `ssidColours` and `bandBadge` in this IIFE and hangs them
// on `window` for the Wifi Networks page to borrow. This port has them as
// ordinary exports that both pages import, and `installWifiGlobals` republishes
// them under the live names so a LIFTED renderer finds them during a DOM
// comparison. Two copies would mean one network wearing two colours depending on
// which page you were looking at.

import { esc, el, bandBadge, ssidColours, installWifiGlobals,
  renderSortHeader, type SortState } from '../dom';
import type { Socket } from '../socket';
import { initFrequencyAnalyser } from './wireless-fa';

export interface WirelessClient {
  mac: string; signal: number; iface: string; txRate: string; band: string;
  ip: string; rxRate: string; uptime: string; ssid: string; name: string;
  source?: string;
}

export interface WirelessSSID {
  ssid: string; ifaces: string[]; bands: string[];
  disabled: boolean; running: boolean; clients: number;
}

export interface WirelessPayload {
  ts: number; clients: WirelessClient[]; mode: string; pollMs: number;
  capsmanAvailable: boolean; ssids: WirelessSSID[]; ssidsManagedElsewhere: number;
}

/** The signal column's four bars, from app.js's `signalBars`. */
export function signalBars(dbm: number): string {
  const bars = dbm >= -55 ? 4 : dbm >= -65 ? 3 : dbm >= -75 ? 2 : dbm > -85 ? 1 : 0;
  let h = '<span class="signal-bars">';
  for (let i = 1; i <= 4; i++) h += '<span' + (i <= bars ? ' class="lit"' : '') + '>&#8203;</span>';
  return h + '</span>';
}

/**
 * A RouterOS rate as Mbps, from app.js's `parseTxRate`.
 *
 * Two input shapes, because two stacks report differently: "866.7Mbps" from one
 * and a bare bits-per-second integer from the other. Anything else is passed
 * through unchanged rather than being mangled into a number.
 */
export function parseTxRate(raw: string): string {
  if (!raw) return '—';
  const s = String(raw).trim();
  const m = s.match(/^([\d.]+)\s*(G|Gbps|M|Mbps|K|Kbps|k)\b/i);
  if (m) {
    const val = parseFloat(m[1]!), unit = m[2]!.toLowerCase();
    const mbps = unit === 'g' || unit === 'gbps' ? val * 1000
      : unit === 'k' || unit === 'kbps' ? val / 1000 : val;
    return (Number.isInteger(mbps) ? mbps : +mbps.toFixed(1)) + ' Mbps';
  }
  if (/^\d+$/.test(s)) {
    const mbps = parseInt(s, 10) / 1e6;
    return (Number.isInteger(mbps) ? mbps : +mbps.toFixed(1)) + ' Mbps';
  }
  return s;
}

function sigQuality(dbm: number): string {
  if (dbm >= -55) return '<span style="color:rgba(52,211,153,.9)">Excellent</span>';
  if (dbm >= -65) return '<span style="color:rgba(56,189,248,.9)">Good</span>';
  if (dbm >= -75) return '<span style="color:rgba(251,191,36,.9)">Fair</span>';
  return '<span style="color:rgba(248,113,113,.9)">Poor</span>';
}

/** A rate as a NUMBER, for sorting. Zero when it cannot be read, so an
 *  unparseable rate sorts last rather than throwing the comparator off. */
function parseTxRateNum(raw: string): number {
  if (!raw) return 0;
  const m = String(raw).trim().match(/([\d.]+)\s*(G|M|K)/i);
  if (!m) return 0;
  const v = parseFloat(m[1]!), u = m[2]!.toUpperCase();
  return u === 'G' ? v * 1000 : u === 'K' ? v / 1000 : v;
}

function uptimeToSecs(u: string): number {
  if (!u) return 0;
  let total = 0;
  const add = (re: RegExp, mult: number): void => {
    const m = u.match(re);
    if (m) total += parseInt(m[1]!, 10) * mult;
  };
  add(/(\d+)w/, 604800);
  add(/(\d+)d/, 86400);
  add(/(\d+)h/, 3600);
  add(/(\d+)m/, 60);
  add(/(\d+)s/, 1);
  return total;
}

type CmpKey = 'name' | 'signal' | 'txRate' | 'uptime';

// Comparators are written ASCENDING and reversed for descending, so the button
// bar and the column headers cannot disagree about ordering.
const WL_CMP: Record<CmpKey, (a: WirelessClient, b: WirelessClient) => number> = {
  name: (a, b) => String(a.name || a.mac || '').localeCompare(String(b.name || b.mac || '')),
  signal: (a, b) => (a.signal || 0) - (b.signal || 0),
  txRate: (a, b) => parseTxRateNum(a.txRate) - parseTxRateNum(b.txRate),
  uptime: (a, b) => uptimeToSecs(a.uptime) - uptimeToSecs(b.uptime),
};

// Preserves what the buttons did before headers existed: strongest signal,
// fastest rate and longest uptime first, but names A to Z.
const WL_DEFAULT_DIR: Record<CmpKey, 'asc' | 'desc'> = {
  name: 'asc', signal: 'desc', txRate: 'desc', uptime: 'desc',
};

function sortClients(clients: WirelessClient[], key: string, dir: string): WirelessClient[] {
  const cmp = WL_CMP[key as CmpKey];
  if (!cmp) return clients.slice();
  const c = clients.slice().sort(cmp);
  if (dir === 'desc') c.reverse();
  return c;
}

export function initWirelessPage(socket: Socket, isVisible: (page: string) => boolean): void {
  installWifiGlobals();
  // The Frequency Analyser lives in a modal owned by this page's toolbar button.
  initFrequencyAnalyser(socket);

  const wirelessTable = el('wirelessTable');
  const wirelessTabBadge = el('wirelessTabBadge');
  let clients: WirelessClient[] = [];

  // BOTH CONTROLS DRIVE ONE OBJECT, so whichever you use, the other reflects it.
  const sort: SortState = { col: 'signal', dir: WL_DEFAULT_DIR.signal };

  function syncSortBtns(): void {
    const wrap = el('wifiSortBtns');
    if (!wrap) return;
    wrap.querySelectorAll('.wl-sort-btn').forEach((b) => {
      b.classList.toggle('active', (b as HTMLElement).dataset.sort === sort.col);
    });
  }

  function renderWireless(): void {
    if (!wirelessTable) return;
    // Interface and Band carry NO key on purpose: the table is grouped by
    // interface, so sorting on it is meaningless, and Band is a derived label.
    // The wl-col-* classes are passed through because the matching td carries
    // them.
    renderSortHeader('wlThead', [
      { key: 'name', label: 'Device' },
      { label: 'Interface', cls: 'wl-col-iface' },
      { label: 'Band' },
      { key: 'signal', label: 'Signal', cls: 'text-end' },
      { key: 'txRate', label: 'TX / RX' },
      { key: 'uptime', label: 'Uptime', cls: 'wl-col-uptime' },
    ], sort, () => { syncSortBtns(); renderWireless(); });

    const rows = sortClients(clients, sort.col, sort.dir);
    if (!rows.length) {
      wirelessTable.innerHTML = '<tr><td colspan="6" class="empty-state">No wireless clients</td></tr>';
      return;
    }

    // Grouped by interface, in the order the sorted list first mentions each.
    const groups: Record<string, { iface: string; ssid: string; clients: WirelessClient[] }> = {};
    const order: string[] = [];
    rows.forEach((c) => {
      const key = c.iface || 'unknown';
      if (!groups[key]) { groups[key] = { iface: key, ssid: c.ssid, clients: [] }; order.push(key); }
      groups[key]!.clients.push(c);
    });

    let html = '';
    order.forEach((key) => {
      const g = groups[key]!;
      // A single group is not a grouping — the header would just repeat the
      // interface column on every row.
      if (order.length > 1) {
        const isCapsman = g.clients.some((c) => c.source === 'capsman');
        html += '<tr class="wl-group-row"><td colspan="6">' +
          '<span class="wl-group-label">' + esc(g.iface) + '</span>' +
          (isCapsman ? '<span class="badge badge-outline-azure ms-1" style="font-size:.6rem">CAP</span>' : '') +
          (g.ssid ? '<span class="wl-group-sub">' + esc(g.ssid) + '</span>' : '') +
          '<span class="wl-group-sub">' + g.clients.length + ' client' +
            (g.clients.length !== 1 ? 's' : '') + '</span>' +
        '</td></tr>';
      }
      g.clients.forEach((c) => {
        const sig = parseInt(String(c.signal), 10) || 0;
        const ipStr = c.ip
          ? '<div style="font-size:.62rem;color:var(--accent-rx)">' + esc(c.ip) + '</div>' : '';
        const macStr = '<div style="font-size:.6rem;color:var(--text-muted)">' + esc(c.mac) + '</div>';
        html += '<tr>' +
          '<td>' +
            '<div style="font-weight:600;font-size:.78rem">' + esc(c.name || c.mac) + '</div>' +
            ipStr + macStr +
          '</td>' +
          '<td class="wl-col-iface" style="color:var(--text-muted);font-size:.73rem">' +
            esc(c.iface || '—') + '</td>' +
          '<td>' + bandBadge(c.band) + '</td>' +
          '<td class="text-end">' +
            signalBars(sig) +
            '<span style="font-size:.68rem;color:var(--text-muted);margin-left:.3rem">' + sig + ' dBm</span>' +
            '<div style="font-size:.62rem;margin-top:.1rem">' + sigQuality(sig) + '</div>' +
          '</td>' +
          '<td>' +
            '<div class="wl-rate">' + esc(parseTxRate(c.txRate)) + '</div>' +
            (c.rxRate ? '<div class="wl-rate-rx">↑ ' + esc(parseTxRate(c.rxRate)) + '</div>' : '') +
          '</td>' +
          '<td class="wl-col-uptime" style="color:var(--text-muted);font-size:.73rem">' +
            esc(c.uptime || '—') + '</td>' +
        '</tr>';
      });
    });
    wirelessTable.innerHTML = html;
  }

  /**
   * The SSID card, driven by the INTERFACE list rather than by connected
   * clients: an SSID with nobody on it is still being broadcast, and a card that
   * only showed networks in use would hide exactly the one you are trying to
   * work out why nobody is on.
   */
  function renderSsids(data: WirelessPayload): void {
    const list = el('wlSsidList');
    if (!list) return;
    const ssids = (data && data.ssids) || [];
    if (!ssids.length) {
      // Say WHY it is empty. A CAP takes its configuration from the manager, so
      // it has no SSID of its own to report — that is not the same as a router
      // with no wireless, and reading as "none" would send someone hunting.
      const managed = (data && data.ssidsManagedElsewhere) || 0;
      list.innerHTML = '<div class="wl-ssid-empty">' +
        (managed
          ? managed + ' radio' + (managed === 1 ? '' : 's') +
            ' managed by CAPsMAN — SSIDs are set on the manager.'
          : 'No SSIDs configured on this router.') +
        '</div>';
      return;
    }
    const colours = ssidColours(ssids.map((sd) => sd.ssid));
    list.innerHTML = ssids.map((sd) => {
      const off = sd.disabled || !sd.running;
      // A DISABLED network keeps the muted treatment rather than its colour —
      // colouring it would say "this one is special" when it means "this one is
      // off".
      const style = off ? '' : ' style="color:' + colours[sd.ssid] + '"';
      return '<div class="wl-ssid-row' + (off ? ' wl-ssid-off' : '') + '" title="' +
          esc(sd.ifaces.join(', ')) + '">' +
        '<span class="wl-ssid-name"' + style + '>' + esc(sd.ssid) + '</span>' +
        // The same badge the clients table uses, so a band means the same colour
        // wherever it appears on the page.
        (sd.bands || []).map((b) => bandBadge(b)).join('') +
        '<span class="wl-ssid-clients">' + (sd.clients || 0) + '</span>' +
      '</div>';
    }).join('');
  }

  function renderCards(data: WirelessPayload): void {
    const ndWC = el('ndWirelessCount');
    if (ndWC) ndWC.textContent = String(clients.length);
    if (wirelessTabBadge) {
      wirelessTabBadge.textContent = String(clients.length);
      wirelessTabBadge.className = 'card-badge' + (clients.length > 0 ? ' active-blue' : '');
    }

    // Band split.
    let b24 = 0, b5 = 0, b6 = 0;
    clients.forEach((c) => {
      if (c.band === '2.4GHz') b24++;
      else if (c.band === '5GHz') b5++;
      else if (c.band === '6GHz') b6++;
    });
    const set = (id: string, v: string): void => { const e = el(id); if (e) e.textContent = v; };
    set('wlBandNum24', String(b24));
    set('wlBandNum5', String(b5));
    set('wlBandNum6', String(b6));
    const r6 = el('wlBandRow6');
    // The 6GHz row appears only when something is on it: most of this fleet has
    // no 6GHz radio, and an always-zero row is noise on every one of them.
    if (r6) r6.style.display = b6 > 0 ? '' : 'none';

    // The legacy header badges, still read by the dashboard card.
    set('wlBand24', '2.4GHz: ' + b24);
    set('wlBand5', '5GHz: ' + b5);
    const el6 = el('wlBand6');
    if (el6) {
      el6.textContent = '6GHz: ' + b6;
      el6.style.display = b6 > 0 ? '' : 'none';
    }

    // Signal health. The four bands are the same thresholds `sigQuality` uses,
    // so the card and the column can never disagree.
    let cntE = 0, cntG = 0, cntF = 0, cntP = 0;
    clients.forEach((c) => {
      const s = parseInt(String(c.signal), 10) || 0;
      if (s >= -55) cntE++;
      else if (s >= -65) cntG++;
      else if (s >= -75) cntF++;
      else cntP++;
    });
    const total = clients.length || 1;
    const setSig = (barId: string, cntId: string, count: number): void => {
      const b = el(barId), cn = el(cntId);
      if (b) b.style.width = Math.round((count / total) * 100) + '%';
      if (cn) cn.textContent = String(count);
    };
    setSig('wlSigBarE', 'wlSigCntE', cntE);
    setSig('wlSigBarG', 'wlSigCntG', cntG);
    setSig('wlSigBarF', 'wlSigCntF', cntF);
    setSig('wlSigBarP', 'wlSigCntP', cntP);

    renderSsids(data);
  }

  socket.on('wireless:update', (data: WirelessPayload) => {
    clients = (data && data.clients) || [];
    renderCards(data);
    renderWireless();
  });

  // A button press picks the column and resets it to that column's NATURAL
  // direction; toggling is the header's job. Both write the same state, so the
  // header indicator follows the button and vice versa.
  el('wifiSortBtns')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('.wl-sort-btn') as HTMLElement | null;
    if (!btn) return;
    sort.col = btn.dataset.sort || 'signal';
    sort.dir = WL_DEFAULT_DIR[sort.col as CmpKey] || 'desc';
    syncSortBtns();
    renderWireless();
  });

  void isVisible;
}
