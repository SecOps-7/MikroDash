// The Apps Usage page: live traffic by application, with each app's logo.
//
// One source, `apps:update`, from internal/collect/apps.go. Everything drawn
// here is that payload; nothing is fetched or inferred in the browser.
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
import { esc, el, pill, fmtMbps, sparkPoints, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import type { PillTone } from '../palette';

const LOGO_BASE = '/vendor/app-icons/';
const RING_R = 80;
const RING_C = 2 * Math.PI * RING_R;
const RING_SLICES = 8;
const TILES = 12;

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

const badge = (a: AppUsage, size: 'sm' | 'md' | 'lg'): string =>
  '<span class="app-badge app-badge-' + size + '" style="--app:' + esc(a.color) + '">' + logoHtml(a, size) + '</span>';

const rates = (rx: number, tx: number): string =>
  '<span class="apps-rx">&darr; ' + esc(fmtMbps(rx)) + '</span> <span class="apps-tx">&uarr; ' + esc(fmtMbps(tx)) + '</span>';

const COLS: SortCol[] = [
  { key: 'name', label: 'App' },
  { key: 'category', label: 'Category' },
  { key: 'devices', label: 'Devices', cls: 'text-end' },
  { key: 'rxMbps', label: 'Rx', cls: 'text-end' },
  { key: 'txMbps', label: 'Tx', cls: 'text-end' },
  { key: 'totalMbps', label: 'Total', cls: 'text-end' },
  { key: 'share', label: 'Share' },
];

export function initAppsPage(socket: Socket, isVisible: (page: string) => boolean): void {
  let last: AppUsagePayload | null = null;
  let query = '';
  let openApp = '';
  const sort: SortState = { col: 'totalMbps', dir: 'desc' };
  const arcs = new Map<string, SVGCircleElement>();

  const setText = (id: string, v: string): void => { const n = el(id); if (n) n.textContent = v; };

  function renderCards(p: AppUsagePayload): void {
    const total = p.rxMbps + p.txMbps;
    setText('appsTotal', fmtMbps(total));
    const rx = el('appsTotalRx'); if (rx) rx.innerHTML = '&darr; ' + esc(fmtMbps(p.rxMbps));
    const tx = el('appsTotalTx'); if (tx) tx.innerHTML = '&uarr; ' + esc(fmtMbps(p.txMbps));
    const active = p.apps.filter((a) => a.totalMbps > 0).length;
    setText('appsCount', String(active));
    setText('appsCountFoot', p.apps.length > active ? (p.apps.length - active) + ' quiet in the last minutes' : 'active now');
    const top = p.apps[0];
    const topEl = el('appsTop');
    if (topEl) topEl.innerHTML = top && top.totalMbps > 0
      ? '<span class="apps-top">' + badge(top, 'sm') + '<span>' + esc(top.name) + '</span></span>' : '-';
    setText('appsTopFoot', top && top.totalMbps > 0 ? fmtMbps(top.totalMbps) + ', ' + top.share.toFixed(0) + '% of traffic' : ' ');
    const unPct = total > 0 ? (100 * p.unnamedMbps) / total : 0;
    setText('appsUnnamed', total > 0 ? unPct.toFixed(0) + '%' : '-');
    setText('appsUnnamedFoot', fmtMbps(p.unnamedMbps) + ' not named');
    const b = el('appsBadge');
    if (b) { b.textContent = String(p.apps.length); b.className = 'card-badge' + (p.apps.length ? ' active-blue' : ''); }
  }

  /** The ring: one arc per busiest app, then "other apps", then unnamed, each
   *  arc an existing element moved rather than redrawn, so it eases. */
  function renderRing(p: AppUsagePayload): void {
    const svg = el('appsRing') as unknown as SVGSVGElement | null;
    const orbit = el('appsOrbit');
    if (!svg || !orbit) return;
    if (!svg.querySelector('.apps-ring-track')) {
      svg.innerHTML = '<circle class="apps-ring-track" cx="100" cy="100" r="' + RING_R + '"/>';
    }
    const total = p.rxMbps + p.txMbps;
    const live = p.apps.filter((a) => a.totalMbps > 0);
    const slices: { id: string; colour: string; v: number }[] = live.slice(0, RING_SLICES)
      .map((a) => ({ id: a.id, colour: a.color, v: a.totalMbps }));
    const rest = live.slice(RING_SLICES).reduce((s, a) => s + a.totalMbps, 0);
    if (rest > 0) slices.push({ id: '~other', colour: 'var(--text-muted)', v: rest });
    if (p.unnamedMbps > 0) slices.push({ id: '~unnamed', colour: 'var(--border)', v: p.unnamedMbps });
    const gap = slices.length > 1 ? 3 : 0;
    let start = 0;
    const keep = new Set<string>();
    for (const s of slices) {
      const len = total > 0 ? (s.v / total) * RING_C : 0;
      let c = arcs.get(s.id);
      if (!c) {
        c = document.createElementNS('http://www.w3.org/2000/svg', 'circle') as SVGCircleElement;
        c.setAttribute('class', 'apps-ring-arc');
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
    setText('appsRingValue', fmtMbps(total));
    setText('appsRingLabel', live.length ? live.length + (live.length === 1 ? ' app' : ' apps') + ' now' : 'all traffic');

    // The logos of the busiest apps, sitting on the ring at the middle of their
    // arc. Percentages, so the orbit scales with the ring.
    let at = 0;
    orbit.innerHTML = live.slice(0, 6).map((a) => {
      const len = total > 0 ? a.totalMbps / total : 0;
      const mid = (at + len / 2) * 2 * Math.PI - Math.PI / 2;
      at += len;
      const x = 50 + 46 * Math.cos(mid);
      const y = 50 + 46 * Math.sin(mid);
      return '<button type="button" class="apps-orbit-item" data-app="' + esc(a.id) + '" style="left:' + x.toFixed(1) +
        '%;top:' + y.toFixed(1) + '%" title="' + esc(a.name + ': ' + fmtMbps(a.totalMbps)) + '">' + badge(a, 'md') + '</button>';
    }).join('');
  }

  function renderTiles(p: AppUsagePayload): void {
    const wrap = el('appsTiles');
    if (!wrap) return;
    const list = p.apps.slice(0, TILES);
    if (!list.length) {
      wrap.innerHTML = '<div class="apps-empty">No app traffic yet. Apps appear here as devices use them.</div>';
      return;
    }
    wrap.innerHTML = list.map((a) => {
      const spark = a.spark.length > 1
        ? '<svg class="apps-tile-spark" viewBox="0 0 120 28" preserveAspectRatio="none" aria-hidden="true">' +
          '<polyline points="' + sparkPoints(a.spark, 120, 28, 2) + '"/></svg>' : '';
      // A quiet app (no traffic this reading, still within its sparkline) is
      // dimmed. Inline, so the class stays one literal the button check reads.
      return '<button type="button" class="apps-tile" data-app="' + esc(a.id) +
        '" style="--app:' + esc(a.color) + (a.totalMbps > 0 ? '' : ';opacity:.55') + '">' +
        '<span class="apps-tile-head">' + badge(a, 'lg') +
          '<span class="apps-tile-id"><span class="apps-tile-name">' + esc(a.name) + '</span>' + catPill(a.category) + '</span></span>' +
        '<span class="apps-tile-rate">' + esc(fmtMbps(a.totalMbps)) + '</span>' +
        '<span class="apps-tile-sub">' + rates(a.rxMbps, a.txMbps) + '</span>' +
        '<span class="apps-tile-who">' + (a.devices ? esc(a.devices + (a.devices === 1 ? ' device' : ' devices')) +
          (a.top ? ', mostly ' + esc(a.top) : '') : 'quiet') + '</span>' +
        spark +
        '<span class="apps-share" aria-hidden="true"><span style="width:' + Math.min(100, a.share).toFixed(1) + '%"></span></span>' +
        '</button>';
    }).join('');
  }

  function renderTable(p: AppUsagePayload): void {
    renderSortHeader('appsThead', COLS, sort, () => renderTable(p));
    const body = el('appsTable');
    if (!body) return;
    const q = query.trim().toLowerCase();
    const rows = p.apps.filter((a) => !q || a.name.toLowerCase().includes(q) || a.category.includes(q));
    if (!rows.length) {
      body.innerHTML = '<tr><td colspan="7" class="empty-state">' + (q ? 'No app matches' : 'Waiting for traffic') + '</td></tr>';
      return;
    }
    body.innerHTML = sortRows(rows, sort.col, sort.dir).map((a) =>
      '<tr class="apps-row" data-app="' + esc(a.id) + '" style="--app:' + esc(a.color) + '">' +
      '<td><span class="apps-name">' + badge(a, 'sm') + '<span>' + esc(a.name) + '</span></span></td>' +
      '<td>' + catPill(a.category) + '</td>' +
      '<td class="text-end mono">' + a.devices + '</td>' +
      '<td class="text-end mono apps-rx">' + esc(fmtMbps(a.rxMbps)) + '</td>' +
      '<td class="text-end mono apps-tx">' + esc(fmtMbps(a.txMbps)) + '</td>' +
      '<td class="text-end mono">' + esc(fmtMbps(a.totalMbps)) + '</td>' +
      '<td><span class="apps-share-cell"><span class="apps-share"><span style="width:' + Math.min(100, a.share).toFixed(1) +
        '%"></span></span><span class="mono">' + a.share.toFixed(1) + '%</span></span></td></tr>').join('');
  }

  function renderUnnamed(p: AppUsagePayload): void {
    const wrap = el('appsUnnamedWrap');
    const list = el('appsUnnamedList');
    if (!wrap || !list) return;
    wrap.hidden = !p.unnamed.length;
    list.innerHTML = p.unnamed.map((o) =>
      '<div class="apps-unnamed-row"><span class="apps-unnamed-org">' + esc(o.org) + '</span>' + rates(o.rxMbps, o.txMbps) + '</div>').join('');
  }

  function renderNote(p: AppUsagePayload): void {
    const n = el('appsNote');
    if (!n) return;
    let text = '';
    if (!p.dnsAt) text = 'Reading the router\'s DNS cache. Until it arrives, apps are named by port and network owner only.';
    else if (!p.dnsNames) text = 'The router\'s DNS cache is empty, so apps are named by port and network owner only. ' +
      'Devices on this network are not using the router for DNS, or use their own encrypted DNS.';
    n.textContent = text;
    n.hidden = !text;
  }

  function renderModal(): void {
    if (!openApp || !last) return;
    const a = last.apps.find((x) => x.id === openApp);
    const title = el('appsModalTitle');
    const body = el('appsModalBody');
    if (!a || !title || !body) return;
    title.innerHTML = '<span class="apps-name">' + badge(a, 'sm') + '<span>' + esc(a.name) + '</span></span>';
    const devs: AppDevice[] = (last.devices && last.devices[a.id]) || [];
    const via = a.via === 'name' ? 'by DNS name' : a.via === 'port' ? 'by port' : a.via === 'asn' ? 'by network owner' : '';
    body.innerHTML =
      '<div class="apps-modal-sum">' + catPill(a.category) + ' <span class="mono">' + esc(fmtMbps(a.totalMbps)) + '</span> ' +
        rates(a.rxMbps, a.txMbps) + (via ? ' <span class="muted-note">named ' + via + '</span>' : '') + '</div>' +
      (devs.length
        ? '<table class="table table-vcenter mb-0"><thead><tr><th>Device</th><th>Address</th><th class="text-end">Rx</th><th class="text-end">Tx</th></tr></thead><tbody>' +
          devs.map((d) => '<tr><td>' + esc(d.name || '-') + (d.mac ? '<div class="muted-note mono">' + esc(d.mac) + '</div>' : '') + '</td>' +
            '<td class="mono">' + esc(d.ip) + '</td><td class="text-end mono apps-rx">' + esc(fmtMbps(d.rxMbps)) + '</td>' +
            '<td class="text-end mono apps-tx">' + esc(fmtMbps(d.txMbps)) + '</td></tr>').join('') + '</tbody></table>'
        : '<div class="empty-state">No device is using it right now.</div>');
  }

  function render(): void {
    if (!last || !isVisible('apps')) return;
    renderCards(last);
    renderRing(last);
    renderTiles(last);
    renderTable(last);
    renderUnnamed(last);
    renderNote(last);
    renderModal();
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
    if (last) renderTable(last);
  });

  // The tabs. Live is this payload; the other three are stored hourly usage,
  // which needs recording turned on for the router.
  let tab = 'live';
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
    const n = el('appsNote');
    if (tab !== 'live' && n) {
      n.textContent = 'Stored usage needs recording, which is not available yet.';
      n.hidden = false;
    } else if (last) {
      renderNote(last);
    }
  });

  socket.on('apps:update', (p) => { last = p; if (tab === 'live') render(); });
  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail === 'apps') render();
  });
}
