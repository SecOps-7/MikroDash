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

import { esc, el, bandBadge, standardBadge, bandRank, ssidColours, installWifiGlobals,
  renderSortHeader, lsGet, lsSet, type SortState } from '../dom';
import { t } from '../i18n';
import type { Socket } from '../socket';
import { initFrequencyAnalyser } from './wireless-fa';
import type { WirelessClient, WirelessPayload } from '../gen/payloads';

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
  if (dbm >= -55) return '<span style="color:rgba(52,211,153,.9)">' + t('Excellent') + '</span>';
  if (dbm >= -65) return '<span style="color:rgba(56,189,248,.9)">' + t('Good') + '</span>';
  if (dbm >= -75) return '<span style="color:rgba(251,191,36,.9)">' + t('Fair') + '</span>';
  return '<span style="color:rgba(248,113,113,.9)">' + t('Poor') + '</span>';
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

type CmpKey = 'name' | 'signal' | 'txRate' | 'uptime' | 'band' | 'standard' | 'ip';

/**
 * An address as a sortable number.
 *
 * NOT `localeCompare`, which puts 192.168.1.100 before 192.168.1.9 and reads as
 * a broken sort rather than as a string sort. The four octets pack into one
 * integer, which is the whole of it.
 *
 * ANYTHING THAT IS NOT DOTTED-QUAD SORTS LAST, the same rule Band and Standard
 * follow: a client ARP has no address for is unknown, not lowest. That covers
 * IPv6 too — the ARP join this column comes from is v4 — and an IPv6 client
 * would sit with the addressless rather than being mangled into a number.
 */
function ipRank(ip: string): number {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(String(ip || '').trim());
  if (!m) return Number.MAX_SAFE_INTEGER;
  return ((+m[1]! * 256 + +m[2]!) * 256 + +m[3]!) * 256 + +m[4]!;
}

/**
 * Standard sorts by GENERATION, never alphabetically — the same rule `bandRank`
 * states for bands, and for the same reason: "Legacy" < "Wi-Fi 4" < … <
 * "Wi-Fi 7" is right by luck today and a future "Wi-Fi 10" breaks it silently.
 * The vocabulary is closed and owned by the collector: `WifiStandard` emits
 * exactly these six.
 *
 * UNKNOWN RANKS LAST ASCENDING. A CAPsMAN row with no generation is unknown
 * rather than oldest, so the dash belongs at the end of the natural order.
 * Descending reverses the whole array, as every other column here does, so it
 * leads on the way back — the same trade `name` and `signal` already make.
 *
 * Bands rank in `dom.ts`, because the Wifi Networks table sorts by them too.
 */
const WL_STD_RANK: Record<string, number> = {
  'Legacy': 1, 'Wi-Fi 4': 4, 'Wi-Fi 5': 5, 'Wi-Fi 6': 6, 'Wi-Fi 6E': 7, 'Wi-Fi 7': 8,
};
const UNKNOWN_LAST = 999;
const rankOf = (table: Record<string, number>, v: string): number =>
  table[v] ?? UNKNOWN_LAST;

// Comparators are written ASCENDING and reversed for descending, so the button
// bar and the column headers cannot disagree about ordering.
const WL_CMP: Record<CmpKey, (a: WirelessClient, b: WirelessClient) => number> = {
  name: (a, b) => String(a.name || a.mac || '').localeCompare(String(b.name || b.mac || '')),
  signal: (a, b) => (a.signal || 0) - (b.signal || 0),
  txRate: (a, b) => parseTxRateNum(a.txRate) - parseTxRateNum(b.txRate),
  uptime: (a, b) => uptimeToSecs(a.uptime) - uptimeToSecs(b.uptime),
  band: (a, b) => bandRank(a.band) - bandRank(b.band),
  standard: (a, b) => rankOf(WL_STD_RANK, a.standard) - rankOf(WL_STD_RANK, b.standard),
  ip: (a, b) => ipRank(a.ip) - ipRank(b.ip),
};

// Preserves what the buttons did before headers existed: strongest signal,
// fastest rate and longest uptime first, but names A to Z.
const WL_DEFAULT_DIR: Record<CmpKey, 'asc' | 'desc'> = {
  name: 'asc', signal: 'desc', txRate: 'desc', uptime: 'desc',
  // Ascending on the first click for both: 2.4GHz before 5GHz, and the OLDEST
  // standard first. That is the actionable direction — the reason to sort by
  // generation is to find the clients holding a network back, not to admire the
  // Wi-Fi 7 ones.
  band: 'asc', standard: 'asc',
  // Ascending, because an address range read low to high is how an operator
  // looks for a device: .10 before .200.
  ip: 'asc',
};

function sortClients(clients: WirelessClient[], key: string, dir: string): WirelessClient[] {
  const cmp = WL_CMP[key as CmpKey];
  if (!cmp) return clients.slice();
  const c = clients.slice().sort(cmp);
  if (dir === 'desc') c.reverse();
  return c;
}

/**
 * What this table remembers between visits: whether it groups by access point,
 * and which groups are folded away.
 *
 * `localStorage`, like `nav.ts`'s sidebar state and for the same reason — it is
 * a per-browser view preference, not configuration, and nothing on the server
 * has any business holding it. `lsGet`/`lsSet` carry the guards; see dom.ts.
 */
const WL_PREFS_KEY = 'mkd_wifi_clients';

interface WlPrefs {
  grouped: boolean;
  collapsed: string[];
}

function loadPrefs(): WlPrefs {
  const raw = lsGet<Partial<WlPrefs> | null>(WL_PREFS_KEY, null);
  return {
    grouped: raw && typeof raw.grouped === 'boolean' ? raw.grouped : true,
    collapsed: Array.isArray(raw?.collapsed) ? raw.collapsed.map(String) : [],
  };
}

function savePrefs(p: WlPrefs): void {
  lsSet(WL_PREFS_KEY, p);
}

export function initWirelessPage(socket: Socket, isVisible: (page: string) => boolean): void {
  installWifiGlobals();
  // The Frequency Analyser lives in a modal owned by this page's toolbar button.
  initFrequencyAnalyser(socket);

  const wirelessTable = el('wirelessTable');
  const wirelessTabBadge = el('wirelessTabBadge');
  let clients: WirelessClient[] = [];

  // GROUPING IS A VIEW, NOT A FILTER. Off, the table is a flat list in sort
  // order and the Interface column carries what the group header said; on, it is
  // the same rows under a header per access point, each foldable. Nothing is
  // hidden either way except by a fold the viewer asked for.
  const prefs = loadPrefs();
  let grouped = prefs.grouped;
  const collapsed = new Set<string>(prefs.collapsed);

  // BOTH CONTROLS DRIVE ONE OBJECT, so whichever you use, the other reflects it.
  const sort: SortState = { col: 'signal', dir: WL_DEFAULT_DIR.signal };

  /**
   * The button bar, showing which column is sorted AND which way.
   *
   * The arrow is `data-dir` plus a stylesheet rule rather than appended text,
   * because the label lives in the markup: writing into `textContent` would mean
   * remembering what the button said before the first arrow was added, and a
   * second sync appending to the first one's output is exactly the bug that
   * makes a header read "Signal ↓↓".
   */
  function syncSortBtns(): void {
    const wrap = el('wifiSortBtns');
    if (!wrap) return;
    wrap.querySelectorAll('.wl-sort-btn').forEach((b) => {
      const btn = b as HTMLElement;
      const on = btn.dataset.sort === sort.col;
      btn.classList.toggle('active', on);
      if (on) btn.setAttribute('data-dir', sort.dir); else btn.removeAttribute('data-dir');
    });
  }

  /** The state of the grouping toggle, on the button that carries it. */
  function syncGroupBtn(): void {
    const btn = el('wlGroupBtn');
    if (!btn) return;
    btn.classList.toggle('active', grouped);
    btn.setAttribute('aria-pressed', grouped ? 'true' : 'false');
    btn.textContent = grouped ? t('Grouped by AP') : t('Flat list');
  }

  function persist(): void {
    savePrefs({ grouped, collapsed: [...collapsed] });
  }

  /** One client row. Identical grouped or flat — the grouping decides what sits
   *  ABOVE these rows, not what is in them. */
  function clientRow(c: WirelessClient): string {
    const sig = parseInt(String(c.signal), 10) || 0;
    const ipStr = c.ip
      ? '<div style="font-size:.62rem;color:var(--accent-rx)">' + esc(c.ip) + '</div>' : '';
    const macStr = '<div style="font-size:.6rem;color:var(--text-muted)">' + esc(c.mac) + '</div>';
    return '<tr>' +
      '<td>' +
        '<div style="font-weight:600;font-size:.78rem">' + esc(c.name || c.mac) + '</div>' +
        ipStr + macStr +
      '</td>' +
      '<td class="wl-col-iface" style="color:var(--text-muted);font-size:.73rem">' +
        esc(c.iface || '—') + '</td>' +
      '<td>' + bandBadge(c.band) + '</td>' +
      '<td>' + standardBadge(c.standard) + '</td>' +
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
  }

  function renderWireless(): void {
    if (!wirelessTable) return;
    // Interface carries NO key: the table is GROUPED by interface, so sorting on
    // it would order rows by the thing the grouping has already collapsed.
    //
    // BAND AND STANDARD DO SORT, and the earlier note here that they were
    // "derived labels" was not a reason — every column in this table is derived
    // from something. What they sort by is ranked rather than alphabetical; see
    // WL_BAND_RANK.
    //
    // What a sort on either does is reorder the GROUPS, because groups are built
    // in the order the sorted list first mentions each interface. That is the
    // useful behaviour and worth knowing: sorting by Standard puts the interface
    // holding the oldest client at the top, and orders within each group too.
    //
    // The wl-col-* classes are passed through because the matching td carries
    // them.
    renderSortHeader('wlThead', [
      { key: 'name', label: t('Device') },
      { label: t('Interface'), cls: 'wl-col-iface' },
      { key: 'band', label: t('Band') },
      { key: 'standard', label: t('Standard') },
      { key: 'signal', label: t('Signal'), cls: 'text-end' },
      { key: 'txRate', label: 'TX / RX' },
      { key: 'uptime', label: t('Uptime'), cls: 'wl-col-uptime' },
    ], sort, () => { syncSortBtns(); renderWireless(); });

    const rows = sortClients(clients, sort.col, sort.dir);
    if (!rows.length) {
      wirelessTable.innerHTML = '<tr><td colspan="7" class="empty-state">' + t('No wireless clients') + '</td></tr>';
      return;
    }

    if (!grouped) {
      wirelessTable.innerHTML = rows.map(clientRow).join('');
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

    // A single group is not a grouping — the header would just repeat the
    // interface column on every row, and there is nothing to fold it away from.
    const headers = order.length > 1;
    // Button id -> the interface it folds, and the state folding it MOVES TO.
    //
    // A real <button> rather than a click handler on the <tr>, because it is the
    // only control in this table and it should be reachable by keyboard; the id
    // is what lets the handler be attached after the markup is written.
    //
    // A TARGET RATHER THAN A FLIP. The button already states which way it goes,
    // in `aria-expanded`, and a handler that inverted whatever it found could
    // disagree with what the row it was rendered into says.
    const byBtn: Record<string, { key: string; shut: boolean }> = {};

    let html = '';
    order.forEach((key, i) => {
      const g = groups[key]!;
      const shut = headers && collapsed.has(key);
      if (headers) {
        const btnId = 'wlGrp' + String(i);
        byBtn[btnId] = { key, shut: !shut };
        const isCapsman = g.clients.some((c) => c.source === 'capsman');
        html += '<tr class="wl-group-row"><td colspan="7">' +
          '<button type="button" class="wl-group-toggle" id="' + btnId + '"' +
            ' aria-expanded="' + (shut ? 'false' : 'true') + '">' +
            '<span class="wl-group-caret">' + (shut ? '&#9656;' : '&#9662;') + '</span>' +
            '<span class="wl-group-label">' + esc(g.iface) + '</span>' +
            (isCapsman ? '<span class="badge badge-outline-azure ms-1" style="font-size:.6rem">CAP</span>' : '') +
            (g.ssid ? '<span class="wl-group-sub">' + esc(g.ssid) + '</span>' : '') +
            '<span class="wl-group-sub">' + g.clients.length + ' client' +
              (g.clients.length !== 1 ? 's' : '') + '</span>' +
          '</button>' +
        '</td></tr>';
      }
      if (!shut) html += g.clients.map(clientRow).join('');
    });
    wirelessTable.innerHTML = html;

    Object.keys(byBtn).forEach((id) => {
      el(id)?.addEventListener('click', () => {
        const want = byBtn[id]!;
        if (want.shut) collapsed.add(want.key); else collapsed.delete(want.key);
        persist();
        renderWireless();
      });
    });
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
          ? (managed === 1 ? t('1 radio managed by CAPsMAN — SSIDs are set on the manager.')
            : t('{n} radios managed by CAPsMAN — SSIDs are set on the manager.', { n: managed }))
          : t('No SSIDs configured on this router.')) +
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

  socket.on('wireless:update', (data) => {
    clients = (data && data.clients) || [];
    renderCards(data);
    renderWireless();
  });

  // A button press picks the column and resets it to that column's NATURAL
  // direction; toggling is the header's job. Both write the same state, so the
  // header indicator follows the button and vice versa.
  // A SECOND PRESS ON THE SAME BUTTON REVERSES IT, which is what the column
  // headers have always done — the bar used to reset to the column's natural
  // direction on every press, so half the orders it can produce were reachable
  // from the headers and not from the buttons. Pressing a DIFFERENT button still
  // starts at that column's natural direction, because that is the order somebody
  // asking for it means the first time.
  el('wifiSortBtns')?.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('.wl-sort-btn') as HTMLElement | null;
    if (!btn) return;
    const key = btn.dataset.sort || 'signal';
    if (sort.col === key) {
      sort.dir = sort.dir === 'asc' ? 'desc' : 'asc';
    } else {
      sort.col = key;
      sort.dir = WL_DEFAULT_DIR[key as CmpKey] || 'desc';
    }
    syncSortBtns();
    renderWireless();
  });

  // FOLDS ARE NOT DISCARDED when grouping is switched off and back on: the
  // viewer folded a noisy access point away, and switching to a flat list to
  // find one client is not a statement that they want it back.
  el('wlGroupBtn')?.addEventListener('click', () => {
    grouped = !grouped;
    persist();
    syncGroupBtn();
    renderWireless();
  });

  syncGroupBtn();

  void isVisible;
}
