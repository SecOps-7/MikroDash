// The Interfaces page — a port of SIX adjacent blocks of top-level code in
// public/app.js (the `── Interface Status` banner through `── Ports panel`).
//
// It is one page and six renderers: the tile grid, the list view, the card-size
// switch, the type filter, the Interface Types card and the Ports panel. They
// share one payload and one piece of state, which is why app.js keeps them
// together and why they are one module here.
//
// ── TARGETED DOM UPDATES ARE THE POINT OF THIS PAGE ─────────────────────────
//
// Both tables rebuild a row ONLY when something it displays changed. At a two
// second poll a full innerHTML sweep would churn the DOM, drop text selection
// mid-drag and flicker every hover, on a page whose whole job is to be watched.
// The fingerprint per row is what makes that safe, and it is reproduced field
// for field.
//
// ── TWO EVENTS, AND THE SPLIT IS AN AUTHORISATION BOUNDARY ──────────────────
//
// `ifstatus:update` carries rates, addresses and MACs and is page-scoped.
// `ifstatus:names` carries names and up/down only and is router-wide, because
// the traffic chart's picker and the sidebar badge are chrome on every page.
// See internal/collect/ifstatus.go and live issue #108.

import { esc, el, fmtMbps, fmtBytes } from '../dom';
import type { Socket } from '../socket';
import { portSvg } from './port-svg';

export interface Iface {
  name: string; type: string; running: boolean; disabled: boolean;
  comment: string; macAddr: string;
  rxMbps: number; txMbps: number; ips: string[];
  rxBytes: number | null; txBytes: number | null;
  errors: number | null; drops: number | null; linkDowns: number | null;
  lastLinkUp: string;
  errorsDelta: number | null; dropsDelta: number | null; deltaWindowMs: number | null;
}

export interface IfStatusPayload { ts: number; routerId: string; interfaces: Iface[] }

export interface IfName { name: string; running: boolean; disabled: boolean }
export interface IfNamesPayload { ts: number; total: number; interfaces: IfName[] }

const IFACE_SPARK_LEN = 30;
// The empty-address placeholder in a tile: U+00A0, a NON-BREAKING space. A
// plain space collapses in HTML and the line takes no height, which is the
// whole failure the placeholder exists to avoid. Named so it survives a copy.
const IP_PLACEHOLDER = '\u00a0';
const IFACE_SIZE_KEY = 'mikrodash_iface_size';

// Colour palette for type badges — cycles for types beyond the named set.
const IF_TYPE_COLOURS: Record<string, string> = {
  ether: 'rgba(56,189,248,.9)',
  wlan: 'rgba(167,139,250,.9)',
  // RouterOS reports the newer drivers as 'wifi' and 'wg', not 'wlan' and
  // 'wireguard'. Without these two, the most common types on current hardware
  // fell through to the rotating fallback palette.
  wifi: 'rgba(167,139,250,.9)',
  bridge: 'rgba(52,211,153,.9)',
  vlan: 'rgba(251,191,36,.9)',
  wireguard: 'rgba(99,190,130,.9)',
  wg: 'rgba(99,190,130,.9)',
  'pppoe-client': 'rgba(251,113,133,.9)',
  lte: 'rgba(245,159,0,.9)',
  loopback: 'rgba(99,130,190,.6)',
};
const IF_TYPE_FALLBACKS = ['rgba(56,189,248,.7)', 'rgba(167,139,250,.7)', 'rgba(52,211,153,.7)',
  'rgba(251,191,36,.7)', 'rgba(251,113,133,.7)', 'rgba(245,159,0,.7)'];

/**
 * A stable colour for a type name.
 *
 * The Types card assigns fallbacks BY POSITION within a single render, which is
 * fine for a legend but would make a list-view pill change colour whenever an
 * interface appears or disappears. Hashing the name keeps a type the same colour
 * across every render. `>>> 0` is carried over verbatim — it coerces to unsigned
 * 32-bit, which is what keeps the index positive.
 */
function ifTypeColour(t: string): string {
  if (IF_TYPE_COLOURS[t]) return IF_TYPE_COLOURS[t]!;
  let h = 0;
  for (let i = 0; i < t.length; i++) h = (h * 31 + t.charCodeAt(i)) >>> 0;
  return IF_TYPE_FALLBACKS[h % IF_TYPE_FALLBACKS.length]!;
}

// The palette is rgba, so the pill background is the same colour at low alpha.
function ifTypePill(t: string): string {
  if (!t) return '<span class="ifl-na">&mdash;</span>';
  const col = ifTypeColour(t);
  const bg = col.replace(/,\s*[\d.]+\)$/, ',.14)');
  return '<span class="ifl-type-pill" style="color:' + col + ';background:' + bg + '">' + esc(t) + '</span>';
}

function ifaceSparkSvg(history: number[]): string {
  if (!history || history.length < 2) return '';
  const w = 56, h = 18, pad = 1.5;
  // Always baseline at zero so rising traffic is visually obvious.
  const max = Math.max.apply(null, history) || 1;
  const pts = history.map((v, i) => {
    const x = pad + (i / (history.length - 1)) * (w - pad * 2);
    const y = h - pad - (v / max) * (h - pad * 2);
    return x.toFixed(1) + ',' + y.toFixed(1);
  });
  return '<svg class="iface-spark" width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '">' +
    '<polyline points="' + pts.join(' ') + '" fill="none" stroke="rgba(56,189,248,.6)" stroke-width="1.4" stroke-linejoin="round" stroke-linecap="round"/>' +
    '</svg>';
}

function ifaceRateRow(dir: string, mbps: number, peak: number): string {
  const pct = peak > 0 ? Math.min(100, (mbps / peak) * 100) : 0;
  const isZero = !mbps || mbps === 0;
  const valCls = isZero ? 'zero' : dir;
  const label = dir === 'rx' ? '↓' : '↑';
  return '<div class="iface-rate-row">' +
    '<span class="iface-rate-label">' + label + '</span>' +
    '<div class="iface-rate-bar-wrap"><div class="iface-rate-bar ' + dir + '" style="width:' + pct.toFixed(1) + '%"></div></div>' +
    '<span class="iface-rate-val ' + valCls + '">' + fmtMbps(mbps) + '</span>' +
    '</div>';
}

/**
 * A counter cell.
 *
 * A counter of null means the interface does not report it. Rendering that as
 * "0" would claim a clean bill of health the router never gave us, so it shows a
 * dash instead. Only MOVEMENT since the last poll gets the badge: a lifetime
 * count says a fault happened at some point, the delta says it is happening now.
 */
function iflCounter(v: number | null, delta: number | null): string {
  if (v === null || v === undefined) {
    return '<span class="ifl-na" title="Not reported by this interface type">&mdash;</span>';
  }
  const cls = v > 0 ? 'ifl-bad' : 'ifl-zero';
  let body = '<span class="' + cls + '">' + v.toLocaleString() + '</span>';
  if (delta !== null && delta > 0) {
    body += '<span class="ifl-delta" title="' + delta.toLocaleString() +
            ' since the last poll">+' + delta.toLocaleString() + '</span>';
  }
  return body;
}

function iflBytes(v: number | null): string {
  if (v === null || v === undefined) return '<span class="ifl-na">&mdash;</span>';
  return fmtBytes(v);
}

/**
 * The Last Up cell.
 *
 * RouterOS reports link-up time in the ROUTER'S local timezone with no offset,
 * so a browser in a different zone would skew the age. A timestamp that parses
 * into the future is that skew showing, and the raw string is shown instead of a
 * nonsensical negative age.
 */
function iflLastUp(s: string): string {
  if (!s) return '<span class="ifl-na">&mdash;</span>';
  const t = Date.parse(s.replace(' ', 'T'));
  if (!isFinite(t)) return '<span title="' + esc(s) + '">' + esc(s) + '</span>';
  const sec = (Date.now() - t) / 1000;
  if (sec < 0) return '<span title="' + esc(s) + '">' + esc(s) + '</span>';
  const out = sec < 60 ? Math.floor(sec) + 's'
    : sec < 3600 ? Math.floor(sec / 60) + 'm'
      : sec < 86400 ? Math.floor(sec / 3600) + 'h'
        : Math.floor(sec / 86400) + 'd';
  return '<span title="' + esc(s) + '">' + out + '</span>';
}

interface IflCol { str?: boolean; get: (i: Iface) => string | number | null }

// Sortable columns. `str` marks the ones compared as text; everything else is
// numeric, including Last Up, which sorts on parsed time rather than the string.
const IFL_COLS: Record<string, IflCol> = {
  name: { str: true, get: (i) => i.name || '' },
  type: { str: true, get: (i) => i.type || '' },
  ip: { str: true, get: (i) => (i.ips && i.ips.length ? i.ips[0]! : '') },
  rxMbps: { get: (i) => i.rxMbps || 0 },
  txMbps: { get: (i) => i.txMbps || 0 },
  rxBytes: { get: (i) => i.rxBytes },
  txBytes: { get: (i) => i.txBytes },
  errors: { get: (i) => i.errors },
  drops: { get: (i) => i.drops },
  linkDowns: { get: (i) => i.linkDowns },
  lastLinkUp: {
    get: (i) => {
      const t = Date.parse(String(i.lastLinkUp || '').replace(' ', 'T'));
      return isFinite(t) ? t : null;
    },
  },
};



// At FILE SCOPE, as in the live app (`public/app.js:1676`) — it was nested here
// while nothing else needed it, and the Dashboard's Physical Ports card is what
// showed that nesting had also left it undrivable by a gate. It closes over
// nothing: `el`, `esc` and `portSvg` are all module-level.
export function renderIfPorts(ifaces: Iface[]): void {
  const panel = el('ifPortsPanel');
  if (!panel) return;
  const ethers = ifaces.filter((i) => i.type === 'ether');
  if (!ethers.length) {
    panel.innerHTML = '<div style="font-size:.72rem;color:var(--text-muted)">No ethernet ports</div>';
    return;
  }
  // Port size scales down when there are many ports so they all fit one row.
  const n = ethers.length;
  const sz = n <= 8 ? 44 : n <= 16 ? 36 : n <= 24 ? 30 : 26;
  panel.innerHTML = ethers.map((i) => {
    const state = i.disabled ? 'dis' : i.running ? 'up' : 'down';
    return '<div class="if-port-item" data-state="' + state + '" title="' + esc(i.name) +
      (i.ips && i.ips.length ? ' — ' + esc(i.ips[0]!) : '') +
      (i.running ? ' (up)' : i.disabled ? ' (disabled)' : ' (down)') + '">' +
      portSvg(sz) +
      '<span class="if-port-label">' + esc(i.name) + '</span>' +
    '</div>';
  }).join('');
}

export function initInterfacesPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const ifaceGrid = el('ifaceGrid');
  const ifaceCount = el('ifaceCount');
  const ifaceTypeFilter = el<HTMLSelectElement>('ifaceTypeFilter');
  const ifaceSelect = el<HTMLSelectElement>('ifaceSelect');

  let typeFilter = '';
  // Last payload, kept so switching view or type filter can re-render the list
  // immediately instead of waiting for the next poll.
  let lastIfaces: Iface[] = [];
  let view = 'sm';
  const peaks: Record<string, { rx: number; tx: number }> = {};
  // Per-interface ring buffer of combined rx+tx Mbps samples for the sparkline.
  // 30 samples at ~5 s poll interval = ~2.5 min of trend history.
  const history: Record<string, number[]> = {};

  // No sort until a header is clicked, so the default order stays the router's
  // own, matching the tile view.
  const sort = { key: '', dir: 1 };
  let iflOrder = '';
  let selectKey = '';

  // ── The list view ─────────────────────────────────────────────────────────

  function iflSortRows(rows: Iface[]): Iface[] {
    const col = IFL_COLS[sort.key];
    if (!col) return rows;
    const dir = sort.dir;
    return rows.slice().sort((a, b) => {
      const av = col.get(a), bv = col.get(b);
      // Unknown values sort last in BOTH directions. Sorting Errors descending
      // should surface the worst interfaces, not bury them under the ones that
      // report no counter at all.
      const an = av === null || av === undefined || av === '';
      const bn = bv === null || bv === undefined || bv === '';
      if (an && bn) return 0;
      if (an) return 1;
      if (bn) return -1;
      // Numeric collation, so ether10 sorts after ether2 rather than before it.
      if (col.str) {
        return String(av).localeCompare(String(bv), undefined,
          { numeric: true, sensitivity: 'base' }) * dir;
      }
      return ((av as number) - (bv as number)) * dir;
    });
  }

  function iflRefreshHeaders(): void {
    document.querySelectorAll('.iface-list th[data-sort]').forEach((th) => {
      th.className = th.className.replace(/\s*sort-(asc|desc)/g, '');
      if ((th as HTMLElement).dataset.sort === sort.key) {
        th.className += (sort.dir === 1 ? ' sort-asc' : ' sort-desc');
      }
    });
  }

  function iflSetSort(key: string): void {
    if (!IFL_COLS[key]) return;
    if (sort.key === key) {
      sort.dir *= -1;
    } else {
      // Text starts ascending (A first); counters start DESCENDING, since the
      // reason to sort by Errors is to see the worst offender.
      sort.key = key;
      sort.dir = IFL_COLS[key]!.str ? 1 : -1;
    }
    iflRefreshHeaders();
    renderIfaceList(lastIfaces);
  }

  function renderIfaceList(ifaces: Iface[]): void {
    const tbody = el('ifaceListBody');
    if (!tbody) return;
    let rows = ifaces.filter((i) => !typeFilter || i.type === typeFilter);
    rows = iflSortRows(rows);
    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="11" class="empty-state">No interfaces</td></tr>';
      iflOrder = '';
      return;
    }

    const existing: Record<string, HTMLElement> = {};
    tbody.querySelectorAll('tr[data-iface]').forEach((e) => {
      existing[(e as HTMLElement).dataset.iface || ''] = e as HTMLElement;
    });
    if (!Object.keys(existing).length) tbody.innerHTML = '';

    const seen: Record<string, boolean> = {};
    const els: HTMLElement[] = [];
    rows.forEach((i) => {
      seen[i.name] = true;
      const cls = i.disabled ? 'disabled' : i.running ? 'up' : 'down';
      const ipStr = i.ips && i.ips.length ? i.ips.join(', ') : '';
      // Rebuild a row only when something it displays actually changed.
      const fp = [cls, ipStr, i.rxMbps, i.txMbps, i.rxBytes, i.txBytes,
        i.errors, i.drops, i.errorsDelta, i.dropsDelta, i.linkDowns, i.lastLinkUp].join('|');
      let tr = existing[i.name];
      // Collected in sorted order either way — an unchanged row still needs its
      // position known so the reorder pass below can place it.
      if (tr && tr.dataset.fp === fp) { els.push(tr); return; }
      if (!tr) {
        tr = document.createElement('tr');
        tr.dataset.iface = i.name;
        tbody.appendChild(tr);
      }
      els.push(tr);
      tr.className = cls;
      tr.dataset.fp = fp;
      const dotCls = i.disabled ? 'dis' : i.running ? 'up' : 'down';
      tr.innerHTML =
        '<td class="ifl-name" title="' + esc(i.name + (i.comment ? ' · ' + i.comment : '')) + '">' +
          '<span class="iface-dot ' + dotCls + '"></span>' + esc(i.name) + '</td>' +
        '<td class="ifl-type">' + ifTypePill(i.type) + '</td>' +
        '<td class="ifl-ip" title="' + esc(ipStr) + '">' +
          (ipStr ? esc(ipStr) : '<span class="ifl-na">&mdash;</span>') + '</td>' +
        '<td class="ifl-num ' + (i.rxMbps ? 'ifl-rx' : 'ifl-zero') + '">' + fmtMbps(i.rxMbps || 0) + '</td>' +
        '<td class="ifl-num ' + (i.txMbps ? 'ifl-tx' : 'ifl-zero') + '">' + fmtMbps(i.txMbps || 0) + '</td>' +
        '<td class="ifl-num">' + iflBytes(i.rxBytes) + '</td>' +
        '<td class="ifl-num">' + iflBytes(i.txBytes) + '</td>' +
        '<td class="ifl-num">' + iflCounter(i.errors, i.errorsDelta) + '</td>' +
        '<td class="ifl-num">' + iflCounter(i.drops, i.dropsDelta) + '</td>' +
        '<td class="ifl-num">' + iflCounter(i.linkDowns, null) + '</td>' +
        '<td>' + iflLastUp(i.lastLinkUp) + '</td>';
    });

    Object.keys(existing).forEach((name) => { if (!seen[name]) existing[name]!.remove(); });

    // Rows are reused in place, so DOM order does not follow the sorted array on
    // its own. Re-append only when the order actually CHANGED: appendChild moves
    // an existing node, and doing that every tick would drop text selection for
    // no reason. With no sort applied the order is constant, so this never runs.
    const orderKey = rows.map((i) => i.name).join('|');
    if (orderKey !== iflOrder) {
      iflOrder = orderKey;
      els.forEach((tr) => tbody.appendChild(tr));
    }
  }

  // ── The Interface Types card ──────────────────────────────────────────────

  function renderIfTypes(ifaces: Iface[]): void {
    const panel = el('ifTypeGrid');
    if (!panel) return;
    // Count by type, preserving insertion order.
    const counts: Record<string, number> = {};
    const order: string[] = [];
    ifaces.forEach((i) => {
      const t = i.type || 'ether';
      if (!counts[t]) { counts[t] = 0; order.push(t); }
      counts[t]!++;
    });
    if (!order.length) {
      panel.innerHTML = '<div class="if-type-item"><span class="if-type-label">—</span>' +
        '<span class="if-type-count">—</span></div>';
      return;
    }
    let fallbackIdx = 0;
    panel.innerHTML = order.map((t) => {
      const col = IF_TYPE_COLOURS[t] || IF_TYPE_FALLBACKS[fallbackIdx++ % IF_TYPE_FALLBACKS.length];
      return '<div class="if-type-item">' +
        '<span class="if-type-label" title="' + esc(t) + '">' + esc(t) + '</span>' +
        '<span class="if-type-count" style="color:' + col + '">' + counts[t] + '</span>' +
      '</div>';
    }).join('');
  }

  // ── The Ports panel ───────────────────────────────────────────────────────



  // ── The tile grid ─────────────────────────────────────────────────────────

  function renderTiles(ifaces: Iface[]): void {
    if (!ifaceGrid) return;
    const grid = ifaceGrid;

    // Targeted DOM update — existing tiles in place, new ones created, deleted
    // ones removed. A full innerHTML replacement makes the rate bars flash.
    const existing: Record<string, HTMLElement> = {};
    grid.querySelectorAll('.iface-tile[data-iface]').forEach((e) => {
      existing[(e as HTMLElement).dataset.iface || ''] = e as HTMLElement;
    });
    // First render: the grid holds only the initial "Waiting…" placeholder.
    let coldStart = !Object.keys(existing).length && !!grid.querySelector('.empty-state');

    const seen: Record<string, boolean> = {};
    ifaces.forEach((i) => {
      seen[i.name] = true;
      const cls = i.disabled ? 'disabled' : i.running ? 'up' : 'down';
      const dotCls = i.disabled ? 'dis' : i.running ? 'up' : 'down';
      const ipStr = i.ips && i.ips.length ? i.ips[0]! : '';
      const p = peaks[i.name] || { rx: 1, tx: 1 };
      const tile = existing[i.name];

      if (!tile) {
        if (coldStart) { grid.innerHTML = ''; coldStart = false; }
        const div = document.createElement('div');
        div.className = 'iface-tile ' + cls;
        div.dataset.iface = i.name;
        div.dataset.ifaceType = i.type || '';
        div.innerHTML =
          ifaceSparkSvg(history[i.name] || []) +
          // title carries the full text, so a name the CSS had to truncate is
          // still readable on hover — without letting a long one change the
          // tile's size.
          '<div class="iface-name" title="' + esc(i.name) + '"><span class="iface-dot ' + dotCls + '"></span>' +
            esc(i.name) + '</div>' +
          '<div class="iface-type" title="' + esc(i.type + (i.comment ? ' · ' + i.comment : '')) + '">' +
            esc(i.type) + (i.comment ? ' · ' + esc(i.comment) : '') + '</div>' +
          // Always rendered, with a blank placeholder when the interface has no
          // address. Omitting it made those tiles one line shorter, so their
          // rate bars sat higher than their neighbours' and whole rows came up
          // short.
          //
          // THE PLACEHOLDER IS U+00A0, NOT A SPACE, and it has to be: a plain
          // space collapses to nothing in HTML, the line takes no height, and
          // the tile comes up short anyway — the very thing the placeholder is
          // there to prevent. Written as an escape so it cannot be mistaken for
          // an ordinary space by the next reader, or by a copy.
          '<div class="iface-ip">' + (ipStr ? esc(ipStr) : IP_PLACEHOLDER) + '</div>' +
          '<div class="iface-rates">' +
            ifaceRateRow('rx', i.rxMbps || 0, p.rx) +
            ifaceRateRow('tx', i.txMbps || 0, p.tx) +
          '</div>';
        grid.appendChild(div);
        return;
      }

      // Existing tile — only touch what changed.
      tile.className = 'iface-tile ' + cls;
      tile.dataset.ifaceType = i.type || '';

      const sparkEl = tile.querySelector('.iface-spark');
      const newSpark = ifaceSparkSvg(history[i.name] || []);
      if (newSpark) {
        const tmp = document.createElement('div');
        tmp.innerHTML = newSpark;
        if (sparkEl) tile.replaceChild(tmp.firstChild!, sparkEl);
        else tile.insertAdjacentHTML('afterbegin', newSpark);
      } else if (sparkEl) {
        sparkEl.remove();
      }

      const dot = tile.querySelector('.iface-dot');
      if (dot) dot.className = 'iface-dot ' + dotCls;

      // The IP element is NEVER removed — losing a line would make the tile
      // shorter than its neighbours — so an interface without an address keeps a
      // blank placeholder in its place.
      const ipEl = tile.querySelector('.iface-ip');
      const ipText = ipStr || IP_PLACEHOLDER;
      if (ipEl) {
        if (ipEl.textContent !== ipText) ipEl.textContent = ipText;
      } else {
        const typeEl = tile.querySelector('.iface-type');
        if (typeEl) typeEl.insertAdjacentHTML('afterend', '<div class="iface-ip">' + esc(ipText) + '</div>');
      }

      const ratesEl = tile.querySelector('.iface-rates');
      if (ratesEl) {
        ratesEl.innerHTML =
          ifaceRateRow('rx', i.rxMbps || 0, p.rx) +
          ifaceRateRow('tx', i.txMbps || 0, p.tx);
      }
    });

    Object.keys(existing).forEach((name) => { if (!seen[name]) existing[name]!.remove(); });
  }

  // ── The card-size switch ──────────────────────────────────────────────────

  function applySize(size: string): void {
    view = size;
    const isList = size === 'list';
    const wrap = el('ifaceListWrap');
    const sel = el<HTMLSelectElement>('ifaceCardSize');
    if (ifaceGrid) {
      ifaceGrid.hidden = isList;
      // Keep the last real size on the grid so returning from list view restores
      // the card scale rather than defaulting back to compact.
      if (!isList) ifaceGrid.dataset.size = size;
    }
    if (wrap) wrap.hidden = !isList;
    if (sel) sel.value = size;
    if (isList) renderIfaceList(lastIfaces);
  }

  // ── The traffic chart's picker ────────────────────────────────────────────

  /**
   * Fed from `ifstatus:names`, the router-wide half of the split delivery.
   *
   * It lives here because this is where the names arrive, but the element it
   * fills is chrome — the traffic chart's interface select — and it is rebuilt
   * only when the set of active names changed, because rebuilding it drops the
   * viewer's own selection.
   */
  function rebuildIfaceSelect(names: string[]): void {
    if (!ifaceSelect) return;
    const key = names.join(',');
    if (key === selectKey) return;
    selectKey = key;
    const currentIf = ifaceSelect.value;
    ifaceSelect.innerHTML = '';
    names.forEach((n) => {
      const opt = document.createElement('option');
      opt.value = n;
      opt.textContent = n;
      ifaceSelect.appendChild(opt);
    });
    // The current interface went down — switch to the first active one.
    if (currentIf && names.indexOf(currentIf) === -1 && names.length) {
      ifaceSelect.value = names[0]!;
      socket.emit('traffic:select', { ifName: names[0] });
    } else {
      ifaceSelect.value = currentIf || names[0] || '';
    }
  }

  // ── Wiring ────────────────────────────────────────────────────────────────

  socket.on('ifstatus:names', (data: IfNamesPayload) => {
    const ifaces = (data && data.interfaces) || [];
    rebuildIfaceSelect(ifaces.filter((i) => i.running && !i.disabled).map((i) => i.name));
  });

  socket.on('ifstatus:update', (data: IfStatusPayload) => {
    const ifaces = (data && data.interfaces) || [];
    lastIfaces = ifaces;
    if (ifaceCount) {
      ifaceCount.textContent = String(ifaces.length);
      ifaceCount.className = 'card-badge' + (ifaces.length > 0 ? ' active-blue' : '');
    }
    const wiredUp = ifaces.filter((i) => i.running && !i.disabled && i.type === 'ether');
    const ndWired = el('ndWiredCount');
    if (ndWired) ndWired.textContent = String(wiredUp.length);

    // The grid is hidden in list view, so its empty state is not enough — the
    // table would keep showing the previous poll's rows.
    if (!ifaces.length) {
      if (ifaceGrid) ifaceGrid.innerHTML = '<div class="empty-state">No interfaces</div>';
      if (view === 'list') renderIfaceList([]);
      return;
    }
    if (!ifaceGrid) return;

    // Peaks DECAY rather than reset, so a bar keeps a scale a moment after the
    // burst that set it; the floor of 1 Mbps stops an idle link drawing a full
    // bar for a trickle.
    ifaces.forEach((i) => {
      if (!peaks[i.name]) peaks[i.name] = { rx: 0, tx: 0 };
      const p = peaks[i.name]!;
      p.rx = Math.max(i.rxMbps || 0, p.rx * 0.995);
      p.tx = Math.max(i.txMbps || 0, p.tx * 0.995);
      if (p.rx < 1) p.rx = 1;
      if (p.tx < 1) p.tx = 1;
      if (!history[i.name]) history[i.name] = [];
      history[i.name]!.push((i.rxMbps || 0) + (i.txMbps || 0));
      if (history[i.name]!.length > IFACE_SPARK_LEN) history[i.name]!.shift();
    });

    renderTiles(ifaces);

    // The type filter offers what this payload actually contains.
    if (ifaceTypeFilter) {
      const types: string[] = [];
      ifaces.forEach((i) => { if (i.type && types.indexOf(i.type) === -1) types.push(i.type); });
      types.sort();
      ifaceTypeFilter.innerHTML = '<option value="">All Types</option>' +
        types.map((t) => '<option value="' + esc(t) + '">' + esc(t) + '</option>').join('');
      if (typeFilter && types.indexOf(typeFilter) !== -1) ifaceTypeFilter.value = typeFilter;
      ifaceTypeFilter.classList.toggle('active', !!typeFilter);
    }

    // Apply the filter's visibility and update the count badge.
    const total = ifaces.length;
    let visible = 0;
    ifaceGrid.querySelectorAll('.iface-tile[data-iface-type]').forEach((e) => {
      const show = !typeFilter || (e as HTMLElement).dataset.ifaceType === typeFilter;
      (e as HTMLElement).style.display = show ? '' : 'none';
      if (show) visible++;
    });
    if (ifaceCount) {
      ifaceCount.textContent = typeFilter ? (visible + '/' + total) : String(total);
      ifaceCount.className = 'card-badge' + (total > 0 ? ' active-blue' : '');
    }

    if (view === 'list') renderIfaceList(ifaces);

    renderIfTypes(ifaces);
    renderIfPorts(ifaces);
  });

  ifaceTypeFilter?.addEventListener('change', function () {
    typeFilter = this.value;
    this.classList.toggle('active', !!typeFilter);
    let total = 0, visible = 0;
    ifaceGrid?.querySelectorAll('.iface-tile[data-iface-type]').forEach((e) => {
      total++;
      const show = !typeFilter || (e as HTMLElement).dataset.ifaceType === typeFilter;
      (e as HTMLElement).style.display = show ? '' : 'none';
      if (show) visible++;
    });
    if (ifaceCount) ifaceCount.textContent = typeFilter ? (visible + '/' + total) : String(total);
    // The list view filters its own rows rather than hiding tiles, so it needs
    // an explicit re-render — the tile visibility sweep above does not reach it.
    if (view === 'list') renderIfaceList(lastIfaces);
  });

  const sizeSel = el<HTMLSelectElement>('ifaceCardSize');
  let saved = 'sm';
  try { saved = localStorage.getItem(IFACE_SIZE_KEY) || 'sm'; } catch { /* private mode */ }
  applySize(saved);
  sizeSel?.addEventListener('change', () => {
    applySize(sizeSel.value);
    try { localStorage.setItem(IFACE_SIZE_KEY, sizeSel.value); } catch { /* private mode */ }
  });

  // Delegated so it survives the tbody being rebuilt; the headers themselves are
  // static, but one listener is cheaper than eleven.
  const head = document.querySelector('.iface-list thead');
  head?.addEventListener('click', (e) => {
    const th = (e.target as HTMLElement | null)?.closest?.('th[data-sort]') as HTMLElement | null;
    if (th) iflSetSort(th.dataset.sort || '');
  });

  void isVisible;
}
