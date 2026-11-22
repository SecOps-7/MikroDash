// The Apps Usage page: traffic by application, with each app's logo.
//
// Two sources. The Last hour tab is `apps:update`, from
// internal/collect/apps.go: the last sixty minutes in bytes, the open minute
// included, sent with every reading so the page shows bytes as they come in.
// The 24 hours, 7 days and 30 days tabs are the stored history,
// GET /api/apps/history (internal/server/apps_history.go): bytes per app per
// hour, and the unnamed bytes, kept for every router with Reporting on. Both
// are turned into one view the cards, ring, tiles and table draw, so the two
// cannot drift apart; both are in bytes, the last hour adds devices, and the
// stored tabs add a stacked bar per hour or day.
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
//
// ── WHAT #167 ADDED ─────────────────────────────────────────────────────────
//
//   - VIEW BY Applications or Categories: the same figures summed by category
//     (categoryRows), each category opening onto its apps. Categories are the
//     catalogue's and the administrators' own (GET /api/apps/catalogue, or
//     /api/apps/categories on a shared page).
//   - A CLIENT filter on the Last hour: the payload for one client comes from
//     GET /api/apps/live, fetched again at every `apps:update`. The stored tabs
//     keep no clients, so it does not reach them.
//   - The Unnamed card opens the last hour's unnamed traffic in detail
//     (apps-unnamed.ts).
//   - A Manage apps tab for a global administrator (apps-manage.ts).

import type { Socket } from '../socket';
import type { AppUsage, AppUsagePayload, AppDevice } from '../gen/payloads';
import { esc, el, pill, fmtBytes, sparkPoints, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { fmtMonthDay, fmtTime } from '../timefmt';
import { TONE_TOKEN, type PillTone } from '../palette';
import { initAppsManage, type AppCatalogue } from './apps-manage';
import { initAppsUnnamed } from './apps-unnamed';

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

/** The tones a category may take (internal/apps CategoryTones). */
export const CAT_TONES: readonly PillTone[] = ['rx', 'tx', 'ok', 'warn', 'err', 'alt', 'pink', 'muted'];

/** A category as the server lists it: a built-in one by id alone, an
 *  administrator's with its name and tone. */
export interface CategoryInfo { id: string; name: string; tone: string; builtin: boolean; apps: number }

/** The administrators' categories, by id. */
const customCats = new Map<string, { name: string; tone: PillTone }>();

/** Takes the categories the server listed. */
export function setAppCategories(list: readonly CategoryInfo[]): void {
  customCats.clear();
  for (const c of list) {
    if (!c.builtin) customCats.set(c.id, { name: c.name, tone: (CAT_TONES as readonly string[]).includes(c.tone) ? c.tone as PillTone : 'muted' });
  }
}

/** A category's label: an administrator's name, the built-in one's, or
 *  "uncategorized" for a custom app given none (which is not unnamed traffic:
 *  that has no app at all). */
export function catLabel(c: string): string {
  if (!c) return 'uncategorized';
  return customCats.get(c)?.name || CAT_LABEL[c] || c;
}

export function catTone(c: string): PillTone {
  return customCats.get(c)?.tone || APP_CAT_TONE[c] || 'muted';
}

export const catPill = (c: string): string => pill(catLabel(c), catTone(c), true);

/** The app's initials, for an app with no logo. */
export function monogram(name: string): string {
  const words = name.replace(/[^A-Za-z0-9 ]/g, ' ').trim().split(/\s+/).filter(Boolean);
  if (!words.length) return '?';
  return (words.length === 1 ? words[0]!.slice(0, 2) : words[0]![0]! + words[1]![0]!).toUpperCase();
}

/** The logo for an app, at one of three sizes. Colour comes from the --app
 *  custom property the caller's tile sets. */
export function logoHtml(a: Brand, size: 'sm' | 'md' | 'lg'): string {
  const style = '--app:' + esc(a.color);
  const cls = 'app-logo app-logo-' + size + (a.dark ? ' is-dark' : '');
  // An administrator's app shows a built-in app's logo by that app's id.
  const file = a.icon || a.id;
  if (a.logo === 'mask') {
    const url = 'url(' + LOGO_BASE + encodeURIComponent(file) + '.svg)';
    return '<span class="' + cls + ' is-mask" role="img" aria-label="' + esc(a.name) + '" style="' + style +
      ';-webkit-mask-image:' + url + ';mask-image:' + url + '"></span>';
  }
  if (a.logo === 'image') {
    return '<img class="' + cls + ' is-image" src="' + LOGO_BASE + encodeURIComponent(file) +
      '.svg" alt="' + esc(a.name) + '" loading="lazy" decoding="async">';
  }
  return '<span class="' + cls + ' is-mono" role="img" aria-label="' + esc(a.name) + '" style="' + style + '">' +
    esc(monogram(a.name)) + '</span>';
}

type Brand = Pick<AppUsage, 'id' | 'name' | 'logo' | 'dark' | 'color'> & { icon?: string };

/** An app's logo on its brand-tinted tile. */
export const badge = (a: Brand, size: 'sm' | 'md' | 'lg'): string =>
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
  apps: Array<Brand & { category: string; custom?: boolean; rxBytes: number; txBytes: number }>;
  buckets: Array<{ ts: number; apps: Record<string, number>; unnamed: number }>;
  unnamedRxBytes: number; unnamedTxBytes: number;
  from: number; to: number;
  /** Whether this router's app usage is being kept: its Reporting setting. */
  recording: boolean;
}

export type AppsPeriod = 'day' | 'week' | 'month';

const SPAN: Record<AppsPeriod, string> = { day: 'in the last 24 hours', week: 'in the last 7 days', month: 'in the last 30 days' };

/** What the last hour's figures cover: "in the last hour", or, while less than
 *  an hour has been measured, the minutes that have. Pure. */
export function hourSpan(p: AppUsagePayload): string {
  const mins = Math.floor(Math.max(0, p.to - p.from) / 60000);
  if (mins >= 59) return 'in the last hour';
  return mins < 2 ? 'in the last minute' : 'in the last ' + mins + ' minutes';
}

/** The last hour as a view. Pure. */
export function hourView(p: AppUsagePayload): AppsView {
  return {
    hour: true, rx: p.rxBytes, tx: p.txBytes, unnamed: p.unnamedBytes, span: hourSpan(p),
    apps: p.apps.map((a) => ({ id: a.id, name: a.name, category: a.category, color: a.color, logo: a.logo,
      dark: a.dark, icon: a.icon, rx: a.rxBytes, tx: a.txBytes, total: a.totalBytes, share: a.share, devices: a.devices,
      top: a.top, spark: a.spark })),
  };
}

/** The stored totals as a view: each app's share of all the recorded bytes,
 *  unnamed included as on the last hour, and its line across the buckets for
 *  the tile's sparkline. Pure. */
export function historyView(h: AppHistory, period: AppsPeriod): AppsView {
  let rx = h.unnamedRxBytes, tx = h.unnamedTxBytes;
  for (const a of h.apps) { rx += a.rxBytes; tx += a.txBytes; }
  const all = rx + tx;
  return {
    hour: false, rx, tx, unnamed: h.unnamedRxBytes + h.unnamedTxBytes, span: SPAN[period],
    apps: h.apps.map((a) => {
      const total = a.rxBytes + a.txBytes;
      return { id: a.id, name: a.name, category: a.category, color: a.color, logo: a.logo, dark: a.dark, icon: a.icon,
        rx: a.rxBytes, tx: a.txBytes, total, share: all > 0 ? (100 * total) / all : 0, devices: 0, top: '',
        spark: h.buckets.map((b) => b.apps[a.id] || 0) };
    }),
  };
}

/** One category's traffic in a view. */
export interface CatRow {
  id: string; label: string; tone: PillTone;
  rx: number; tx: number; total: number; share: number;
  /** Its apps, busiest first. */
  apps: ViewApp[];
}

/** A view summed by category, busiest first: the same bytes, the same shares
 *  of all traffic, so the categories add up to the apps. Pure. */
export function categoryRows(v: AppsView): CatRow[] {
  const all = v.rx + v.tx;
  const by = new Map<string, CatRow>();
  for (const a of v.apps) {
    let r = by.get(a.category);
    if (!r) {
      r = { id: a.category, label: catLabel(a.category), tone: catTone(a.category), rx: 0, tx: 0, total: 0, share: 0, apps: [] };
      by.set(a.category, r);
    }
    r.rx += a.rx; r.tx += a.tx; r.total += a.total;
    r.apps.push(a);
  }
  const rows = [...by.values()];
  for (const r of rows) {
    r.share = all > 0 ? (100 * r.total) / all : 0;
    r.apps.sort((x, y) => y.total - x.total || x.name.localeCompare(y.name));
  }
  return rows.sort((x, y) => y.total - x.total || x.label.localeCompare(y.label));
}

/** A stacked bar per bucket, the busiest apps in their brand colours and the
 *  rest as one grey segment. Axes and grid are theme tokens. */
export function historyBars(h: AppHistory, period: AppsPeriod): string {
  if (!h.buckets.length) return '';
  const W = 720, H = 170, L = 52, B = 18, T = 6;
  const pw = W - L, ph = H - B - T;
  const named = h.apps.slice(0, BAR_APPS);
  const ids = new Set(named.map((a) => a.id));
  const totals = h.buckets.map((b) => Object.values(b.apps).reduce((s, v) => s + v, b.unnamed));
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
    seg(b.unnamed, 'var(--border)', 'unnamed');
    if (i % every === 0) {
      out += '<text class="au-hist-axis" x="' + (x + bw / 2).toFixed(1) + '" y="' + (H - 4) + '" text-anchor="middle">' +
        esc(label(b.ts)) + '</text>';
    }
  });
  const legend = named.map((a) => '<span class="au-hist-key"><i style="background:' + esc(a.color || 'var(--text-muted)') +
    '"></i>' + esc(a.name) + '</span>').join('') +
    (h.apps.length > named.length ? '<span class="au-hist-key"><i style="background:var(--text-muted)"></i>other apps</span>' : '') +
    (h.buckets.some((b) => b.unnamed > 0) ? '<span class="au-hist-key"><i style="background:var(--border)"></i>unnamed</span>' : '');
  return '<svg class="au-hist-chart" viewBox="0 0 ' + W + ' ' + H + '" preserveAspectRatio="none" role="img" aria-label="App usage by ' +
    (period === 'day' ? 'hour' : 'day') + '">' + out + '</svg><div class="au-hist-legend">' + legend + '</div>';
}

const CAT_COLS: SortCol[] = [
  { key: 'label', label: 'Category' },
  { key: 'count', label: 'Apps', cls: 'text-end' },
  { key: 'rx', label: 'Rx', cls: 'text-end' },
  { key: 'tx', label: 'Tx', cls: 'text-end' },
  { key: 'total', label: 'Total', cls: 'text-end' },
  { key: 'share', label: 'Share' },
];

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

const EMPTY_HISTORY: AppHistory = { apps: [], buckets: [], unnamedRxBytes: 0, unnamedTxBytes: 0, from: 0, to: 0, recording: false };

/** One arc of the ring. */
export interface RingSlice { id: string; colour: string; v: number }

/** The ring's arcs: the busiest apps in their brand colours, then the rest as
 *  "other apps", then the traffic no step could name. Pure. */
export function ringSlices(list: ReadonlyArray<{ id: string; color: string; total: number }>, unnamed: number,
  max: number = RING_SLICES): RingSlice[] {
  const slices: RingSlice[] = list.slice(0, max)
    .map((a) => ({ id: a.id, colour: a.color || 'var(--text-muted)', v: a.total }));
  const rest = list.slice(max).reduce((s, a) => s + a.total, 0);
  if (rest > 0) slices.push({ id: '~other', colour: 'var(--text-muted)', v: rest });
  if (unnamed > 0) slices.push({ id: '~unnamed', colour: 'var(--border)', v: unnamed });
  return slices;
}

/** Draws the ring's arcs into an svg (viewBox 0 0 200 200), each arc an
 *  existing element moved rather than redrawn, so it eases. `arcs` is the
 *  caller's, one per ring: the Apps Usage page and its Dashboard card. */
export function paintRing(svg: SVGSVGElement, arcs: Map<string, SVGCircleElement>, slices: RingSlice[], total: number): void {
  // A document that cannot build SVG nodes (the test harness's DOM) gets no
  // arcs rather than a throw partway through the caller's render, as the
  // Network Flow card does.
  if (typeof document.createElementNS !== 'function' || typeof svg.querySelector !== 'function') return;
  if (!svg.querySelector('.au-ring-track')) {
    svg.innerHTML = '<circle class="au-ring-track" cx="100" cy="100" r="' + RING_R + '"/>';
    arcs.clear();
  }
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
}

/** A category's colour, for the ring and its tile: the pill tone's token. */
const toneColour = (t: PillTone): string => 'var(' + TONE_TOKEN[t] + ')';

export function initAppsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let last: AppUsagePayload | null = null;
  let stored: AppHistory | null = null;
  let query = '';
  let openApp = '';
  let tab = 'hour';
  let routerId = '';
  /** Applications or categories. */
  let view: 'apps' | 'categories' = 'apps';
  /** The client the Last hour is narrowed to ("" every client), and its payload. */
  let client = '';
  let filtered: AppUsagePayload | null = null;
  let fetching = false, again = false;
  let catalogue: AppCatalogue | null = null;
  /** Whether this viewer can read the client list: a shared page cannot. */
  let clientsReadable = true;
  const openCats = new Set<string>();
  const sort: SortState = { col: 'total', dir: 'desc' };
  const catSort: SortState = { col: 'total', dir: 'desc' };
  const arcs = new Map<string, SVGCircleElement>();

  const setText = (id: string, v: string): void => { const n = el(id); if (n) n.textContent = v; };
  const period = (): AppsPeriod => tab as AppsPeriod;

  /** The last hour as it is shown: every client's, or the one picked. */
  const hourPayload = (): AppUsagePayload | null => (client ? filtered : last);

  /** What the page shows now: the last hour, or the stored tab's totals. */
  function current(): AppsView | null {
    if (tab === 'hour') { const p = hourPayload(); return p ? hourView(p) : null; }
    return historyView(stored || EMPTY_HISTORY, period());
  }

  const manage = initAppsManage({ routerId: () => routerId, onChanged: () => loadCatalogue() });
  const unnamed = initAppsUnnamed({
    routerId: () => routerId, client: () => client, canEdit: () => !!catalogue?.canEdit, manage,
    apps: () => (catalogue?.apps || []).map((a) => ({ id: a.id, name: a.name })),
  });

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
    const unPct = total > 0 ? (100 * v.unnamed) / total : 0;
    setText('appsUnnamed', total > 0 ? unPct.toFixed(0) + '%' : '-');
    setText('appsUnnamedFoot', fmtBytes(v.unnamed) + ' not named');
    const b = el('appsBadge');
    if (b) { b.textContent = String(v.apps.length); b.className = 'card-badge' + (v.apps.length ? ' active-blue' : ''); }
  }

  /** The ring, then the logos of the busiest apps sitting on it. By
   *  category, the arcs are the categories' colours and carry no logos. */
  function renderRing(v: AppsView): void {
    const svg = el('appsRing') as unknown as SVGSVGElement | null;
    const orbit = el('appsOrbit');
    if (!svg || !orbit) return;
    const total = v.rx + v.tx;
    setText('appsRingValue', fmtBytes(total));
    if (view === 'categories') {
      const cats = categoryRows(v);
      paintRing(svg, arcs, ringSlices(cats.map((c) => ({ id: 'cat:' + c.id, color: toneColour(c.tone), total: c.total })),
        v.unnamed), total);
      const n = cats.length;
      setText('appsRingLabel', n ? n + (n === 1 ? ' category' : ' categories') : 'all traffic');
      orbit.innerHTML = '';
      return;
    }
    paintRing(svg, arcs, ringSlices(v.apps, v.unnamed), total);
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
    if (view === 'categories') {
      const cats = categoryRows(v).slice(0, TILES);
      wrap.innerHTML = cats.length ? cats.map((c) =>
        '<button type="button" class="au-tile" data-cat="' + esc(c.id) + '" style="--app:' + toneColour(c.tone) + '">' +
          '<span class="au-tile-head"><span class="au-tile-id">' + catPill(c.id) +
          '<span class="au-tile-who">' + c.apps.length + (c.apps.length === 1 ? ' app' : ' apps') + '</span></span></span>' +
          '<span class="au-tile-rate">' + esc(fmtBytes(c.total)) + '</span>' +
          '<span class="au-tile-sub">' + rates(c.rx, c.tx) + '</span>' +
          '<span class="au-tile-who">' + esc(c.apps.slice(0, 3).map((a) => a.name).join(', ')) + '</span>' +
          '<span class="au-share" aria-hidden="true"><span style="width:' + Math.min(100, c.share).toFixed(1) + '%"></span></span>' +
        '</button>').join('')
        : '<div class="au-empty">' + (v.hour ? 'No app traffic in the last hour yet.' : 'Nothing recorded ' + esc(v.span) + ' yet.') + '</div>';
      return;
    }
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

  const shareCell = (share: number): string =>
    '<td><span class="au-share-cell"><span class="au-share"><span style="width:' + Math.min(100, share).toFixed(1) +
    '%"></span></span><span class="mono">' + share.toFixed(1) + '%</span></span></td>';

  function appRow(a: ViewApp, v: AppsView, child: boolean): string {
    return '<tr class="au-row' + (child ? ' tbl-indent' : '') + '" data-app="' + esc(a.id) + '" style="--app:' + esc(a.color) + '">' +
      '<td><span class="au-name">' + badge(a, 'sm') + '<span>' + esc(a.name) + '</span></span></td>' +
      (child ? '<td class="text-end mono">' + (v.hour ? a.devices : '-') + '</td>'
        : '<td>' + catPill(a.category) + '</td><td class="text-end mono">' + (v.hour ? a.devices : '-') + '</td>') +
      '<td class="text-end mono au-rx">' + esc(fmtBytes(a.rx)) + '</td>' +
      '<td class="text-end mono au-tx">' + esc(fmtBytes(a.tx)) + '</td>' +
      '<td class="text-end mono">' + esc(fmtBytes(a.total)) + '</td>' + shareCell(a.share) + '</tr>';
  }

  function renderTable(v: AppsView): void {
    const body = el('appsTable');
    const q = query.trim().toLowerCase();
    if (view === 'categories') {
      renderSortHeader('appsThead', CAT_COLS, catSort, () => renderTable(v));
      if (!body) return;
      const rows = categoryRows(v)
        .filter((c) => !q || c.label.toLowerCase().includes(q) || c.apps.some((a) => a.name.toLowerCase().includes(q)))
        .map((c) => ({ ...c, count: c.apps.length }));
      if (!rows.length) {
        body.innerHTML = '<tr><td colspan="6" class="empty-state">' + (q ? 'No category matches' : v.hour ? 'Waiting for traffic' : 'Nothing recorded yet') + '</td></tr>';
        return;
      }
      body.innerHTML = sortRows(rows, catSort.col, catSort.dir).map((c) => {
        const open = openCats.has(c.id);
        return '<tr class="au-row au-cat-row" data-cat="' + esc(c.id) + '" aria-expanded="' + open + '">' +
          '<td><span class="au-cat-caret" aria-hidden="true">' + (open ? '&#9662;' : '&#9656;') + '</span> ' + catPill(c.id) + '</td>' +
          '<td class="text-end mono">' + c.count + '</td>' +
          '<td class="text-end mono au-rx">' + esc(fmtBytes(c.rx)) + '</td>' +
          '<td class="text-end mono au-tx">' + esc(fmtBytes(c.tx)) + '</td>' +
          '<td class="text-end mono">' + esc(fmtBytes(c.total)) + '</td>' + shareCell(c.share) + '</tr>' +
          (open ? c.apps.map((a) => appRow(a, v, true)).join('') : '');
      }).join('');
      return;
    }
    renderSortHeader('appsThead', COLS, sort, () => renderTable(v));
    if (!body) return;
    const rows = v.apps.filter((a) => !q || a.name.toLowerCase().includes(q) || catLabel(a.category).toLowerCase().includes(q));
    if (!rows.length) {
      body.innerHTML = '<tr><td colspan="7" class="empty-state">' +
        (q ? 'No app matches' : v.hour ? 'Waiting for traffic' : 'Nothing recorded yet') + '</td></tr>';
      return;
    }
    body.innerHTML = sortRows(rows, sort.col, sort.dir).map((a) => appRow(a, v, false)).join('');
  }

  /** The note: on the last hour, the DNS cache's state; on a stored tab, that
   *  this router is not being recorded when its Reporting is off, and that the
   *  client filter does not reach stored usage. */
  function renderNote(): void {
    const n = el('appsNote');
    if (!n) return;
    const parts: string[] = [];
    const p = hourPayload();
    if (tab !== 'hour') {
      if (!routerId) {
        parts.push('Select a router to see its stored app usage.');
      } else if (stored && !stored.recording) {
        parts.push('App usage is stored only for routers with Reporting on, and it is off for this router. ' +
          'Turn on Reporting in the router\'s settings on the Devices page to keep its app usage by the hour.');
      }
      if (client) parts.push('Stored usage keeps no clients, so this is every client\'s. The client filter covers the Last hour.');
    } else if (p) {
      if (!p.dnsAt) {
        parts.push('Reading the router\'s DNS cache. Until it arrives, apps are named by port and network owner only.');
      } else if (!p.dnsNames) {
        parts.push('The router\'s DNS cache is empty, so apps are named by port and network owner only. ' +
          'Devices on this network are not using the router for DNS, or use their own encrypted DNS.');
      }
    } else if (client) {
      parts.push('Reading this client\'s last hour.');
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
    const hp = hourPayload();
    const la = hp && hp.apps.find((x) => x.id === openApp);
    const devs: AppDevice[] = (hp && hp.devices && hp.devices[a.id]) || [];
    const via = !la ? '' : la.via === 'name' ? 'by DNS name' : la.via === 'port' ? 'by port' : la.via === 'asn' ? 'by network owner' : '';
    body.innerHTML = sum + (via ? ', <span class="muted-note">named ' + via + '</span>' : '') + '</div>' +
      (devs.length
        ? '<table class="table table-vcenter mb-0"><thead><tr><th>Device</th><th>Address</th><th class="text-end">Rx</th><th class="text-end">Tx</th></tr></thead><tbody>' +
          devs.map((d) => '<tr><td>' + esc(d.name || '-') + (d.mac ? '<div class="muted-note mono">' + esc(d.mac) + '</div>' : '') + '</td>' +
            '<td class="mono">' + esc(d.ip) + '</td><td class="text-end mono au-rx">' + esc(fmtBytes(d.rxBytes)) + '</td>' +
            '<td class="text-end mono au-tx">' + esc(fmtBytes(d.txBytes)) + '</td></tr>').join('') + '</tbody></table>'
        : '<div class="empty-state">No device used it in the last hour.</div>');
  }

  /** The header's controls follow the tab: Manage apps hides the view switch,
   *  the client and the search; the stored tabs disable the client. */
  function renderChrome(): void {
    const managing = tab === 'manage';
    const usage = el('appsUsageBody');
    const man = el('appsManage');
    if (usage) usage.hidden = managing;
    if (man) man.hidden = !managing;
    for (const id of ['appsView', 'appsSearch']) { const n = el(id); if (n) n.hidden = managing; }
    const sel = el<HTMLSelectElement>('appsClient');
    if (sel) {
      sel.hidden = managing || !clientsReadable;
      sel.disabled = tab !== 'hour';
      sel.classList.toggle('active', !!client);
    }
    el('appsView')?.querySelectorAll('[data-appsview]').forEach((b) => {
      const on = (b as HTMLElement).dataset.appsview === view;
      b.classList.toggle('is-on', on);
      b.setAttribute('aria-pressed', on ? 'true' : 'false');
    });
  }

  function render(): void {
    if (!isVisible('apps')) return;
    renderChrome();
    if (tab === 'manage') return;
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

  // ── the catalogue, the clients, one client's hour ─────────────────────────

  /** The categories' names, and for an administrator the Manage apps tab. A
   *  shared page cannot read the catalogue, and reads the categories alone. */
  function loadCatalogue(): void {
    if (!routerId) return;
    const rid = routerId;
    call<AppCatalogue>('/api/apps/catalogue?routerId=' + encodeURIComponent(rid))
      .then((c) => {
        if (rid !== routerId) return;
        catalogue = c;
        setAppCategories(c.categories);
        const t = el('appsManageTab');
        if (t) t.hidden = !c.canEdit;
        manage.setCatalogue(c);
        render();
      })
      .catch(() => call<{ categories: CategoryInfo[] }>('/api/apps/categories?routerId=' + encodeURIComponent(rid))
        .then((c) => { if (rid === routerId) { setAppCategories(c.categories); render(); } })
        .catch(() => undefined));
  }

  function loadClients(): void {
    if (!routerId) return;
    const rid = routerId;
    call<{ clients: Array<{ ip: string; name: string; mac: string; bytes: number }> }>('/api/apps/clients?routerId=' + encodeURIComponent(rid))
      .then((d) => {
        if (rid !== routerId) return;
        const sel = el<HTMLSelectElement>('appsClient');
        if (!sel) return;
        const list = d.clients.slice();
        if (client && !list.some((c) => c.ip === client)) list.unshift({ ip: client, name: '', mac: '', bytes: 0 });
        sel.innerHTML = '<option value="">All clients</option>' + list.map((c) => '<option value="' + esc(c.ip) + '"' +
          (c.ip === client ? ' selected' : '') + '>' + esc((c.name ? c.name + ' (' + c.ip + ')' : c.ip) +
          (c.bytes ? ', ' + fmtBytes(c.bytes) : '')) + '</option>').join('');
      })
      .catch(() => { clientsReadable = false; renderChrome(); });
  }

  /** One client's last hour, fetched again at every reading; one request at a
   *  time, and a reading that arrives during one asks for another after it. */
  function refreshFiltered(): void {
    if (!client || !routerId) return;
    if (fetching) { again = true; return; }
    fetching = true;
    const want = client, rid = routerId;
    call<{ payload: AppUsagePayload | null }>('/api/apps/live?routerId=' + encodeURIComponent(rid) + '&client=' + encodeURIComponent(want))
      .then((d) => { if (client === want && routerId === rid) { filtered = d.payload; if (tab === 'hour') render(); } })
      .catch(() => undefined)
      .finally(() => {
        fetching = false;
        if (again) { again = false; refreshFiltered(); }
      });
  }

  // ── the stored history ───────────────────────────────────────────────────

  function loadHistory(): void {
    stored = null;
    if (tab === 'hour' || tab === 'manage' || !routerId) { render(); return; }
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
    const target = e.target as HTMLElement | null;
    const cat = target?.closest?.('#appsUsageBody [data-cat]') as HTMLElement | null;
    if (cat && cat.dataset.cat !== undefined) {
      // A category tile opens its row in the table; a row opens or closes.
      const id = cat.dataset.cat;
      if (cat.classList.contains('au-tile')) openCats.add(id); else if (openCats.has(id)) openCats.delete(id); else openCats.add(id);
      const v = current();
      if (v) renderTable(v);
      if (cat.classList.contains('au-tile')) el('appsTable')?.scrollIntoView?.({ block: 'nearest', behavior: 'smooth' });
      return;
    }
    const t = target?.closest?.('#page-apps [data-app]') as HTMLElement | null;
    if (t && t.dataset.app) open(t.dataset.app);
  });
  el('appsUnnamedCard')?.addEventListener('click', () => unnamed.open());
  el<HTMLInputElement>('appsSearch')?.addEventListener('input', (e) => {
    query = (e.target as HTMLInputElement).value;
    const v = current();
    if (v) renderTable(v);
  });
  el('appsView')?.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('[data-appsview]') as HTMLElement | null;
    if (!b) return;
    view = b.dataset.appsview === 'categories' ? 'categories' : 'apps';
    render();
  });
  const clientSel = el<HTMLSelectElement>('appsClient');
  // The list is read when it is about to open, so it is the last hour's.
  clientSel?.addEventListener('pointerdown', loadClients);
  clientSel?.addEventListener('focus', loadClients);
  clientSel?.addEventListener('change', () => {
    client = clientSel.value;
    filtered = null;
    render();
    refreshFiltered();
  });

  // The tabs. Last hour is the payload; the other three are the stored history;
  // Manage apps is the administrators' rules.
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
    if (tab === 'manage') loadCatalogue();
    loadHistory();
  });

  socket.on('apps:update', (p) => {
    last = p;
    if (client) refreshFiltered();
    else if (tab === 'hour') render();
  });
  socket.on('router:active', (d) => {
    const was = routerId;
    routerId = (d && d.activeId) || routerId;
    if (routerId !== was) loadCatalogue();
    loadHistory();
  });
  socket.on('router:switched', (d) => {
    routerId = (d && d.activeId) || '';
    last = null;
    client = '';
    filtered = null;
    const sel = el<HTMLSelectElement>('appsClient');
    if (sel) sel.innerHTML = '<option value="">All clients</option>';
    loadCatalogue();
    loadHistory();
  });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'apps') return;
    if (!catalogue) loadCatalogue();
    if (tab === 'hour') { render(); refreshFiltered(); } else loadHistory();
  });
}
