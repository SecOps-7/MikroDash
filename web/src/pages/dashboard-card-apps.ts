// The Dashboard's Apps Usage card (dc-card-apps): the Apps Usage page's last
// hour, small.
//
// The data is `apps:update`, the payload the page's Last hour tab draws: the
// last sixty minutes in bytes, sent with every reading. It reaches this socket
// through the card's room (`dash-card-apps`) while the card is on the
// Dashboard, gated on the Apps Usage page.
//
// It draws the page's ring (apps.ts's ringSlices and paintRing), with the
// hour's total in its centre, and beside it the busiest apps, each with its
// logo, a bar of its share in its brand colour, and its bytes. As many apps as
// the card's height holds; a narrow card drops the ring (app.css).

import { el, esc, fmtBytes } from '../dom';
import type { Socket } from '../socket';
import type { AppUsagePayload } from '../gen/payloads';
import { badge, hourSpan, paintRing, ringSlices } from './apps';

/** One list row's height in pixels, as app.css sets it: the list shows as
 *  many rows as fit. */
const ROW_PX = 34;

/** The card's span label: "last hour", or the minutes measured so far. Pure. */
export function cardSpan(p: AppUsagePayload): string {
  return hourSpan(p).replace(/^in the /, '');
}

/** The list's rows, at most `rows` of them. Pure. */
export function appsCardRows(p: AppUsagePayload, rows: number): string {
  const list = p.apps.slice(0, Math.max(1, rows));
  if (!list.length) return '<li class="au-card-empty">No app traffic yet. Apps appear here as devices use them.</li>';
  return list.map((a) => {
    const share = Math.max(0, Math.min(100, a.share));
    return '<li class="au-card-row" title="' + esc(a.name + ': ' + fmtBytes(a.totalBytes) + ', ' + share.toFixed(0) + '%') + '">' +
      badge(a, 'sm') +
      '<span class="au-card-name">' + esc(a.name) + '</span>' +
      '<span class="au-card-bytes">' + esc(fmtBytes(a.totalBytes)) + '</span>' +
      '<span class="au-card-bar" style="--app:' + esc(a.color || 'var(--text-muted)') + '"><span style="width:' +
      share.toFixed(1) + '%"></span></span>' +
      '</li>';
  }).join('');
}

export function initAppsCard(socket: Socket): void {
  let last: AppUsagePayload | null = null;
  const arcs = new Map<string, SVGCircleElement>();
  const set = (id: string, v: string): void => { const n = el(id); if (n) n.textContent = v; };

  function render(): void {
    if (!last) return;
    const p = last;
    const total = p.rxBytes + p.txBytes;
    const svg = el('dcAppsRing') as unknown as SVGSVGElement | null;
    if (svg) {
      paintRing(svg, arcs, ringSlices(p.apps.map((a) => ({ id: a.id, color: a.color, total: a.totalBytes })),
        p.unnamedBytes), total);
    }
    set('dcAppsTotal', fmtBytes(total));
    set('dcAppsSpan', cardSpan(p));
    const totals = el('dcAppsTotals');
    if (totals) {
      totals.innerHTML = '<span class="au-rx">&darr; ' + esc(fmtBytes(p.rxBytes)) + '</span>' +
        '<span class="au-tx">&uarr; ' + esc(fmtBytes(p.txBytes)) + '</span>' +
        (total > 0 ? '<span class="au-card-unnamed">' + ((100 * p.unnamedBytes) / total).toFixed(0) + '% unnamed</span>' : '');
    }
    const list = el('dcAppsList');
    if (list) {
      const h = list.clientHeight;
      list.innerHTML = appsCardRows(p, h > 0 ? Math.floor(h / ROW_PX) : 6);
    }
  }

  function clear(): void {
    last = null;
    const svg = el('dcAppsRing');
    if (svg) svg.innerHTML = '';
    arcs.clear();
    set('dcAppsTotal', '-');
    set('dcAppsSpan', '');
    const totals = el('dcAppsTotals');
    if (totals) totals.innerHTML = '';
    const list = el('dcAppsList');
    if (list) list.innerHTML = '<li class="au-card-empty">Waiting for the first reading.</li>';
  }

  socket.on('apps:update', (d) => { if (d) { last = d; render(); } });
  socket.on('router:switched', () => clear());
  // Every payload is drawn: it arrives only while the card is on a Dashboard
  // (its room). A card not laid out yet has no height, so it lists six; a
  // resize, the Dashboard opening and a card added from the Add panel all
  // change the box, and redraw with as many rows as now fit.
  const card = el('dc-card-apps');
  if (card && typeof ResizeObserver !== 'undefined') new ResizeObserver(() => render()).observe(card);
}
