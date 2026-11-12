// The Apps Usage page: traffic by application, with each app's logo.
//
// Two sources. The Last hour tab is `apps:update`, from
// internal/collect/apps.go: the last sixty minutes in bytes, sent once a minute
// (the page moved with every reading until 2026-11-12, which the operator found
// too much movement). The 24 hours, 7 days and 30 days tabs are the stored
// history, GET /api/apps/history (internal/server/apps_history.go): bytes per
// app per hour, kept for every router while Settings -> Features -> "Apps Usage
// history" is on, which it is by default. Both are turned into one view the
// cards, ring, tiles and table draw, so the two cannot drift apart; both are in
// bytes, the last hour adds devices and unnamed traffic, and the stored tabs
// add a stacked bar per hour or day.
//
// ── HOW A LOGO IS DRAWN ─────────────────────────────────────────────────────
//
// The catalogue says, per app (internal/apps):
//   logo "mask"  a single-colour shape (Simple Icons, CoreUI Brands, Arcticons,
//                Lucide), drawn as a CSS mask filled with the brand colour, or
//                with the theme's text colour when that colour is too dark to
//                read on the dark theme (`dark`), so it follows every palette;
//   logo "image" a full-colour logo, shown as it is;
//   logo ""      none: a monogram tile in the brand colour.
//
// ── COLOURS ─────────────────────────────────────────────────────────────────
//
// Brand colours are used for the logos, the ring's arcs and the tiles' glow,
// and nowhere else. Rx is --accent-rx and Tx --accent-tx, as on every page.

import type { Socket } from '../socket';
import type { AppUsage, AppUsagePayload, AppDevice } from '../gen/payloads';
import { esc, el, pill, fmtBytes, sparkPoints, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { featureOn, onFeatures } from '../caps';
import { fmtMonthDay, fmtTime } from '../timefmt';
import type { PillTone } from '../palette';

const LOGO_BASE = '/vendor/app-icons/';
const RING_R = 80;
const RING_C = 2 * Math.PI * RING_R;
const RING_SLICES = 8;
const TILES = 12;
/** How many apps the stored tabs' bars stack by name; the rest are one grey segment. */
const BAR_APPS = 8;

/** A category's pill tone. Every category in testdata/apps/catalogue.json has
 *  one; apps-page.test.ts holds the two lists together. */
export const APP_CAT_TONE: Record<string, PillTone> = {
  conferencing: 'rx', messaging: 'tx', streaming: 'pink', music: 'alt', gaming: 'ok',
  social: 'alt', ai: 'pink', productivity: 'warn', cloud: 'rx', updates: 'muted',
  shopping: 'warn', search: 'muted', vpn: 'err', iot: 'tx', cdn: 'muted', dns: 'muted', other: 'muted',
};

const CAT_LABEL: Record<string, string> = {
  conferencing: 'calls', ai: 'AI', iot: 'devices', cdn: 'CDN', dns: 'network', vpn: 'VPN & remote',
};

const catPill = (c: string): string => pill(CAT_LABEL[c] || c, APP_CAT_TONE[c] || 'muted', true);

/** The app's initials, for an app with no logo. */
export function monogram(name: string): string {
  const words = name.replace(/[^A-Za-z0-9 ]/g, ' ').trim().split(/\s+/).filter(Boolean);
  if (!words.length) return '?';
  return (words.length === 1 ? words[0]!.slice(0, 2) : words[0]![0]! + words[1]![0]!).toUpperCase();
}

/** The logo for an app, at one of three sizes. Colour comes from the --app
 *  custom property the caller's tile sets. */
export function logoHtml(a: Pick<AppUsage, 'id' | 'name' | 'logo' | 'dark' | 'color'>, size: 'sm' | 'md' | 'lg'): string {
  const style = '--app:' + esc(a.color);
  const cls = 'app-logo app-logo-' + size + (a.dark ? ' is-dark' : '');
  if (a.logo === 'mask') {
    const url = 'url(' + LOGO_BASE + encodeURIComponent(a.id) + '.svg)';
    return '<span class="' + cls + ' is-mask" role="img" aria-label="' + esc(a.name) + '" style="' + style +
      ';-webkit-mask-image:' + url + ';mask-image:' + url + '"></span>';
  }
  if (a.logo === 'image') {
    return '<img class="' + cls + ' is-image" src="' + LOGO_BASE + encodeURIComponent(a.id) +
      '.svg" alt="' + esc(a.name) + '" loading="lazy" decoding="async">';
  }
  return '<span class="' + cls + ' is-mono" role="img" aria-label="' + esc(a.name) + '" style="' + style + '">' +
    esc(monogram(a.name)) + '</span>';
}

type Brand = Pick<AppUsage, 'id' | 'name' | 'logo' | 'dark' | 'color'>;

const badge = (a: Brand, size: 'sm' | 'md' | 'lg'): string =>
  '<span class="app-badge app-badge-' + size + '" style="--app:' + esc(a.color) + '">' + logoHtml(a, size) + '</span>';

const rates = (rx: number, tx: number): string =>
  '<span class="au-rx">&darr; ' + esc(fmtBytes(rx)) + '</span> <span class="au-tx">&uarr; ' + esc(fmtBytes(tx)) + '</span>';

/** One app as the page draws it, over the last hour or a stored period, in bytes. */
export interface ViewApp extends Brand {
  category: string;
  rx: number; tx: number; total: number; share: number;
  devices: number; top: string; spark: number[];
}

/** What the cards, ring, tiles and table draw, in bytes. `hour` is false for
 *  the stored tabs, which keep no devices and no unnamed traffic. */
export interface AppsView {
  hour: boolean;
  rx: number; tx: number;
  unnamed: number;
  apps: ViewApp[];
  /** "in the last hour", "in the last 24 hours", ... for the labels. */
  span: string;
}

/** GET /api/apps/history. */
export interface AppHistory {
  apps: Array<Brand & { category: string; rxBytes: number; txBytes: number }>;
  buckets: Array<{ ts: number; apps: Record<string, number> }>;
  from: number; to: number;
}

export type AppsPeriod = 'day' | 'week' | 'month';

const SPAN: Record<AppsPeriod, string> = { day: 'in the last 24 hours', week: 'in the last 7 days', month: 'in the last 30 days' };

/** The last hour as a view. Pure. */
export function hourView(p: AppUsagePayload): AppsView {
  return {
    hour: true, rx: p.rxBytes, tx: p.txBytes, unnamed: p.unnamedBytes, span: 'in the last hour',
    apps: p.apps.map((a) => ({ id: a.id, name: a.name, category: a.category, color: a.color, logo: a.logo,
      dark: a.dark, rx: a.rxBytes, tx: a.txBytes, total: a.totalBytes, share: a.share, devices: a.devices,
      top: a.top, spark: a.spark })),
  };
}

/** The note that the hour is still filling, or '' once the payload covers a
 *  whole hour (from the collector's start, or after a gap in its readings). */
export function fillingNote(p: AppUsagePayload): string {
  const mins = Math.floor(Math.max(0, p.to - p.from) / 60000);
  if (mins >= 59) return '';
  return 'The hour is still filling: ' + (mins < 1 ? 'less than a minute' : mins + (mins === 1 ? ' minute' : ' minutes')) +
    ' measured so far. The page moves once a minute.';
}

/** The stored totals as a view: each app's share of the recorded bytes, and
 *  its line across the buckets for the tile's sparkline. Pure. */
export function historyView(h: AppHistory, period: AppsPeriod): AppsView {
  let rx = 0, tx = 0;
  for (const a of h.apps) { rx += a.rxBytes; tx += a.txBytes; }
  const all = rx + tx;
  return {
    hour: false, rx, tx, unnamed: 0, span: SPAN[period],
    apps: h.apps.map((a) => {
      const total = a.rxBytes + a.txBytes;
      return { id: a.id, name: a.name, category: a.category, color: a.color, logo: a.logo, dark: a.dark,
        rx: a.rxBytes, tx: a.txBytes, total, share: all > 0 ? (100 * total) / all : 0, devices: 0, top: '',
        spark: h.buckets.map((b) => b.apps[a.id] || 0) };
    }),
  };
}

/** A stacked bar per bucket, the busiest apps in their brand colours and the
 *  rest as one grey segment. Axes and grid are theme tokens. */
export function historyBars(h: AppHistory, period: AppsPeriod): string {
  if (!h.buckets.length) return '';
  const W = 720, H = 170, L = 52, B = 18, T = 6;
  const pw = W - L, ph = H - B - T;
  const named = h.apps.slice(0, BAR_APPS);
  const ids = new Set(named.map((a) => a.id));
  const totals = h.buckets.map((b) => Object.values(b.apps).reduce((s, v) => s + v, 0));
  const max = Math.max(1, ...totals);
  const slot = pw / h.buckets.length;
  const bw = Math.max(1, slot * 0.7);
  const y = (v: number): number => T + ph - (v / max) * ph;
  const label = (ts: number): string => period === 'day' ? fmtTime(ts, false) : fmtMonthDay(ts);
  const every = period === 'month' ? 5 : period === 'day' ? 3 : 1;
  let out = [0, 0.5, 1].map((f) =>
    '<line class="au-hist-grid" x1="' + L + '" x2="' + W + '" y1="' + y(max * f).toFixed(1) + '" y2="' + y(max * f).toFixed(1) + '"/>' +
    '<text class="au-hist-axis" x="' + (L - 4) + '" y="' + (y(max * f) + 3).toFixed(1) + '" text-anchor="end">' +
    esc(f === 0 ? '0' : fmtBytes(Math.round(max * f))) + '</text>').join('');
  h.buckets.forEach((b, i) => {
    const x = L + i * slot + (slot - bw) / 2;
    let acc = 0;
    const seg = (v: number, colour: string, name: string): void => {
      if (v <= 0) return;
      out += '<rect x="' + x.toFixed(1) + '" y="' + y(acc + v).toFixed(1) + '" width="' + bw.toFixed(1) + '" height="' +
        Math.max(0.5, y(acc) - y(acc + v)).toFixed(1) + '" style="fill:' + esc(colour) + '"><title>' +
        esc(label(b.ts) + ' · ' + name + ': ' + fmtBytes(v)) + '</title></rect>';
      acc += v;
    };
    for (const a of named) seg(b.apps[a.id] || 0, a.color || 'var(--text-muted)', a.name);
    seg(Object.entries(b.apps).reduce((s, [id, v]) => s + (ids.has(id) ? 0 : v), 0), 'var(--text-muted)', 'other apps');
    if (i % every === 0) {
      out += '<text class="au-hist-axis" x="' + (x + bw / 2).toFixed(1) + '" y="' + (H - 4) + '" text-anchor="middle">' +
        esc(label(b.ts)) + '</text>';
    }
  });
  const legend = named.map((a) => '<span class="au-hist-key"><i style="background:' + esc(a.color || 'var(--text-muted)') +
    '"></i>' + esc(a.name) + '</span>').join('') +
    (h.apps.length > named.length ? '<span class="au-hist-key"><i style="background:var(--text-muted)"></i>other apps</span>' : '');
  return '<svg class="au-hist-chart" viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="none" role="img" aria-label="App usage by ' +
    (period === 'day' ? 'hour' : 'day') + '">' + out + '</svg><div class="au-hist-legend">' + legend + '</div>';
}

const COLS: SortCol[] = [
  { key: 'name', label: 'App' },
  { key: 'category', label: 'Category' },
  { key: 'devices', label: 'Devices', cls: 'text-end' },
  { key: 'rx', label: 'Rx', cls: 'text-end' },
  { key: 'tx', label: 'Tx', cls: 'text-end' },
  { key: 'total', label: 'Total', cls: 'text-end' },
  { key: 'share', label: 'Share' },
];

function call<T>(url: string, init?: RequestInit): Promise<T> {
  return fetch(url, { credentials: 'same-origin', ...init }).then((r) =>
    r.json().then((d) => (r.ok ? d as T : Promise.reject(new Error((d && d.error) || r.statusText)))));
}

export function initAppsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let last: AppUsagePayload | null = null;
  let stored: AppHistory | null = null;
  let query = '';
  let openApp = '';
  let tab = 'hour';
  let routerId = '';
  const sort: SortState = { col: 'total', dir: 'desc' };
  const arcs = new Map<string, SVGCircleElement>();

  const setText = (id: string, v: string): void => { const n = el(id); if (n) n.textContent = v; };
  const period = (): AppsPeriod => tab as AppsPeriod;

  /** What the page shows now: the last hour, or the stored tab's totals. */
  function current(): AppsView | null {
    if (tab === 'hour') return last ? hourView(last) : null;
    return historyView(stored || { apps: [], buckets: [], from: 0, to: 0 }, period());
  }

  function renderCards(v: AppsView): void {
    const total = v.rx + v.tx;
    setText('appsTotal', fmtBytes(total));
    const rx = el('appsTotalRx'); if (rx) rx.innerHTML = '&darr; ' + esc(fmtBytes(v.rx));
    const tx = el('appsTotalTx'); if (tx) tx.innerHTML = '&uarr; ' + esc(fmtBytes(v.tx));
    setText('appsCount', String(v.apps.length));
    setText('appsCountFoot', v.span);
    const top = v.apps[0];
    const topEl = el('appsTop');
    if (topEl) topEl.innerHTML = top
      ? '<span class="au-top">' + badge(top, 'sm') + '<span>' + esc(top.name) + '</span></span>' : '-';
    setText('appsTopFoot', top
      ? fmtBytes(top.total) + ', ' + top.share.toFixed(0) + '% of ' + (v.hour ? 'traffic' : 'recorded') : ' ');
    if (v.hour) {
      const unPct = total > 0 ? (100 * v.unnamed) / total : 0;
      setText('appsUnnamed', total > 0 ? unPct.toFixed(0) + '%' : '-');
      setText('appsUnnamedFoot', fmtBytes(v.unnamed) + ' not named');
    } else {
      setText('appsUnnamed', '-');
      setText('appsUnnamedFoot', 'last hour only');
    }
    const b = el('appsBadge');
    if (b) { b.textContent = String(v.apps.length); b.className = 'card-badge' + (v.apps.length ? ' active-blue' : ''); }
  }

  /** The ring: one arc per busiest app, then "other apps", then unnamed, each
   *  arc an existing element moved rather than redrawn, so it eases. */
  function renderRing(v: AppsView): void {
    const svg = el('appsRing') as unknown as SVGSVGElement | null;
    const orbit = el('appsOrbit');
    if (!svg || !orbit) return;
    if (!svg.querySelector('.au-ring-track')) {
      svg.innerHTML = '<circle class="au-ring-track" cx="100" cy="100" r="' + RING_R + '"/>';
    }
    const total = v.rx + v.tx;
    const slices: { id: string; colour: string; v: number }[] = v.apps.slice(0, RING_SLICES)
      .map((a) => ({ id: a.id, colour: a.color || 'var(--text-muted)', v: a.total }));
    const rest = v.apps.slice(RING_SLICES).reduce((s, a) => s + a.total, 0);
    if (rest > 0) slices.push({ id: '~other', colour: 'var(--text-muted)', v: rest });
    if (v.unnamed > 0) slices.push({ id: '~unnamed', colour: 'var(--border)', v: v.unnamed });
    const gap = slices.length > 1 ? 3 : 0;
    let start = 0;
    const keep = new Set<string>();
    for (const s of slices) {
      const len = total > 0 ? (s.v / total) * RING_C : 0;
      let c = arcs.get(s.id);
      if (!c) {
        c = document.createElementNS('http://www.w3.org/2000/svg', 'circle') as SVGCircleElement;
        c.setAttribute('class', 'au-ring-arc');
        c.setAttribute('cx', '100'); c.setAttribute('cy', '100'); c.setAttribute('r', String(RING_R));
        svg.appendChild(c);
        arcs.set(s.id, c);
      }
      c.style.stroke = s.colour;
      c.style.strokeDasharray = Math.max(0, len - gap).toFixed(2) + ' ' + RING_C.toFixed(2);
      c.style.strokeDashoffset = (-start).toFixed(2);
      keep.add(s.id);
      start += len;
    }
    for (const [id, c] of arcs) {
      if (!keep.has(id)) { c.remove(); arcs.delete(id); }
    }
    setText('appsRingValue', fmtBytes(total));
    const n = v.apps.length;
    setText('appsRingLabel', n ? n + (n === 1 ? ' app ' : ' apps ') + (v.hour ? 'this hour' : 'recorded') : 'all traffic');

    // The logos of the busiest apps, sitting on the ring at the middle of their
    // arc. Percentages, so the orbit scales with the ring.
    let at = 0;
    orbit.innerHTML = v.apps.slice(0, 6).map((a) => {
      const len = total > 0 ? a.total / total : 0;
      const mid = (at + len / 2) * 2 * Math.PI - Math.PI / 2;
      at += len;
      // Only an app holding at least 5% of the ring gets a logo on it: smaller
      // arcs sit too close together, and their logos would stack.
      if (len < 0.05) return '';
      const x = 50 + 46 * Math.cos(mid);
      const y = 50 + 46 * Math.sin(mid);
      return '<button type="button" class="au-orbit-item" data-app="' + esc(a.id) + '" style="left:' + x.toFixed(1) +
        '%;top:' + y.toFixed(1) + '%" title="' + esc(a.name + ': ' + fmtBytes(a.total)) + '">' + badge(a, 'md') + '</button>';
    }).join('');
  }

  function renderTiles(v: AppsView): void {
    const wrap = el('appsTiles');
    if (!wrap) return;
    const list = v.apps.slice(0, TILES);
    if (!list.length) {
      wrap.innerHTML = '<div class="au-empty">' + (v.hour ? 'No app traffic in the last hour yet. Apps appear here as devices use them.'
        : 'Nothing recorded ' + esc(v.span) + ' yet.') + '</div>';
      return;
    }
    wrap.innerHTML = list.map((a) => {
      const spark = a.spark.length > 1
        ? '<svg class="au-tile-spark" viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true">' +
          '<polyline points="' + sparkPoints(a.spark, 120, 28, 2) + '"/></svg>' : '';
      const who = !v.hour ? a.share.toFixed(0) + '% of recorded'
        : esc(a.devices + (a.devices === 1 ? ' device' : ' devices')) + (a.top ? ', mostly ' + esc(a.top) : '');
      return '<button type="button" class="au-tile" data-app="' + esc(a.id) + '" style="--app:' + esc(a.color) + '">' +
        '<span class="au-tile-head">' + badge(a, 'lg') +
          '<span class="au-tile-id"><span class="au-tile-name">' + esc(a.name) + '</span>' + catPill(a.category) + '</span></span>' +
        '<span class="au-tile-rate">' + esc(fmtBytes(a.total)) + '</span>' +
        '<span class="au-tile-sub">' + rates(a.rx, a.tx) + '</span>' +
        '<span class="au-tile-who">' + who + '</span>' +
        spark +
        '<span class="au-share" aria-hidden="true"><span style="width:' + Math.min(100, a.share).toFixed(1) + '%"></span></span>' +
        '</button>';
    }).join('');
  }

  function renderTable(v: AppsView): void {
    renderSortHeader('appsThead', COLS, sort, () => renderTable(v));
    const body = el('appsTable');
    if (!body) return;
    const q = query.trim().toLowerCase();
    const rows = v.apps.filter((a) => !q || a.name.toLowerCase().includes(q) || a.category.includes(q));
    if (!rows.length) {
      body.innerHTML = '<tr><td colspan="7" class="empty-state">' +
        (q ? 'No app matches' : v.hour ? 'Waiting for traffic' : 'Nothing recorded yet') + '</td></tr>';
      return;
    }
    body.innerHTML = sortRows(rows, sort.col, sort.dir).map((a) =>
      '<tr class="au-row" data-app="' + esc(a.id) + '" style="--app:' + esc(a.color) + '">' +
      '<td><span class="au-name">' + badge(a, 'sm') + '<span>' + esc(a.name) + '</span></span></td>' +
      '<td>' + catPill(a.category) + '</td>' +
      '<td class="text-end mono">' + (v.hour ? a.devices : '-') + '</td>' +
      '<td class="text-end mono au-rx">' + esc(fmtBytes(a.rx)) + '</td>' +
      '<td class="text-end mono au-tx">' + esc(fmtBytes(a.tx)) + '</td>' +
      '<td class="text-end mono">' + esc(fmtBytes(a.total)) + '</td>' +
      '<td><span class="au-share-cell"><span class="au-share"><span style="width:' + Math.min(100, a.share).toFixed(1) +
        '%"></span></span><span class="mono">' + a.share.toFixed(1) + '%</span></span></td></tr>').join('');
  }

  /** The note: on the last hour, whether it is still filling and the DNS
   *  cache's state; on a stored tab, that the feature is off when it is. */
  function renderNote(): void {
    const n = el('appsNote');
    if (!n) return;
    const parts: string[] = [];
    if (tab !== 'hour') {
      if (!featureOn('appHistoryEnabled')) {
        parts.push('Stored app usage is switched off. Switch on "Apps Usage history" in Settings, Features, ' +
          'to keep every router\'s app usage by the hour.');
      } else if (!routerId) {
        parts.push('Select a router to see its stored app usage.');
      }
    } else if (last) {
      const fill = fillingNote(last);
      if (fill) parts.push(fill);
      if (!last.dnsAt) {
        parts.push('Reading the router\'s DNS cache. Until it arrives, apps are named by port and network owner only.');
      } else if (!last.dnsNames) {
        parts.push('The router\'s DNS cache is empty, so apps are named by port and network owner only. ' +
          'Devices on this network are not using the router for DNS, or use their own encrypted DNS.');
      }
    }
    const text = parts.join(' ');
    n.textContent = text;
    n.hidden = !text;
  }

  function renderBars(): void {
    const c = el('appsHistChart');
    if (!c) return;
    c.hidden = tab === 'hour' || !stored || !stored.apps.length;
    c.innerHTML = c.hidden || !stored ? '' : historyBars(stored, period());
  }

  function renderModal(): void {
    const v = current();
    if (!openApp || !v) return;
    const a = v.apps.find((x) => x.id === openApp);
    const title = el('appsModalTitle');
    const body = el('appsModalBody');
    if (!a || !title || !body) return;
    title.innerHTML = '<span class="au-name">' + badge(a, 'sm') + '<span>' + esc(a.name) + '</span></span>';
    const sum = '<div class="au-modal-sum">' + catPill(a.category) + ' <span class="mono">' + esc(fmtBytes(a.total)) + '</span> ' +
      rates(a.rx, a.tx) + ' <span class="muted-note">' + esc(v.span) + '</span>';
    if (!v.hour) {
      body.innerHTML = sum + '</div>' +
        '<div class="empty-state">Devices are not stored. The Last hour tab shows who used it.</div>';
      return;
    }
    const la = last && last.apps.find((x) => x.id === openApp);
    const devs: AppDevice[] = (last && last.devices && last.devices[a.id]) || [];
    const via = !la ? '' : la.via === 'name' ? 'by DNS name' : la.via === 'port' ? 'by port' : la.via === 'asn' ? 'by network owner' : '';
    body.innerHTML = sum + (via ? ', <span class="muted-note">named ' + via + '</span>' : '') + '</div>' +
      (devs.length
        ? '<table class="table table-vcenter mb-0"><thead><tr><th>Device</th><th>Address</th><th class="text-end">Rx</th><th class="text-end">Tx</th></tr></thead><tbody>' +
          devs.map((d) => '<tr><td>' + esc(d.name || '-') + (d.mac ? '<div class="muted-note mono">' + esc(d.mac) + '</div>' : '') + '</td>' +
            '<td class="mono">' + esc(d.ip) + '</td><td class="text-end mono au-rx">' + esc(fmtBytes(d.rxBytes)) + '</td>' +
            '<td class="text-end mono au-tx">' + esc(fmtBytes(d.txBytes)) + '</td></tr>').join('') + '</tbody></table>'
        : '<div class="empty-state">No device used it in the last hour.</div>');
  }

  function render(): void {
    if (!isVisible('apps')) return;
    renderNote();
    renderBars();
    const v = current();
    if (!v) return;
    renderCards(v);
    renderRing(v);
    renderTiles(v);
    renderTable(v);
    renderModal();
  }

  // ── the stored history ───────────────────────────────────────────────────

  function loadHistory(): void {
    stored = null;
    if (tab === 'hour' || !featureOn('appHistoryEnabled') || !routerId) { render(); return; }
    const want = tab;
    const rid = routerId;
    call<AppHistory>('/api/apps/history?routerId=' + encodeURIComponent(rid) + '&period=' + encodeURIComponent(want))
      .then((h) => { if (tab === want && routerId === rid) { stored = h; render(); } })
      .catch((e: Error) => {
        const n = el('appsNote');
        if (n && tab === want) { n.textContent = 'The stored usage could not be read: ' + e.message; n.hidden = false; }
      });
  }

  function open(id: string): void {
    openApp = id;
    renderModal();
    el('appsModal')?.classList.add('open');
  }

  document.addEventListener('click', (e) => {
    const t = (e.target as HTMLElement | null)?.closest?.('#page-apps [data-app]') as HTMLElement | null;
    if (t && t.dataset.app) open(t.dataset.app);
  });
  el<HTMLInputElement>('appsSearch')?.addEventListener('input', (e) => {
    query = (e.target as HTMLInputElement).value;
    const v = current();
    if (v) renderTable(v);
  });

  // The tabs. Last hour is the payload; the other three are the stored history.
  const tabs = el('appsTabs');
  tabs?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('[data-appstab]') as HTMLElement | null;
    if (!b || !b.dataset.appstab) return;
    tab = b.dataset.appstab;
    tabs.querySelectorAll('[data-appstab]').forEach((x) => {
      const on = (x as HTMLElement).dataset.appstab === tab;
      x.classList.toggle('active', on);
      x.setAttribute('aria-selected', on ? 'true' : 'false');
    });
    loadHistory();
  });

  socket.on('apps:update', (p) => { last = p; if (tab === 'hour') render(); });
  socket.on('router:active', (d) => { routerId = (d && d.activeId) || routerId; loadHistory(); });
  socket.on('router:switched', (d) => {
    routerId = (d && d.activeId) || '';
    last = null;
    loadHistory();
  });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'apps') return;
    if (tab === 'hour') render(); else loadHistory();
  });
  onFeatures(() => { if (tab !== 'hour') loadHistory(); });
}
