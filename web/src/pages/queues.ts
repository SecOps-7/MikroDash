// The Queues page — a port of the Queues IIFE in public/app.js.
//
// Two menus with genuinely different row shapes: /queue/simple caps a target
// bidirectionally and is ORDERED, /queue/tree shapes marked traffic in one
// direction and is not.
//
// ── ORDER IS SEMANTIC ───────────────────────────────────────────────────────
//
// Simple queues are walked in list order and the first match wins, so a queue's
// position changes what it does. The table therefore defaults to router order
// and offers move up/down. Sorting alphabetically by default would misrepresent
// the router — which is why the sort state passed to the header helper is a
// fresh `{col: '', dir: 'asc'}` every time and the callback does nothing.
//
// THAT MAKES FIVE HEADER CELLS LOOK CLICKABLE AND DO NOTHING: `name` and
// `target` here, `name`, `parent` and `packetMark` on the tree table — four
// distinct keys, five cells, because `name` appears in both. The helper gives
// any keyed column `cursor:pointer` and a click handler, which calls the no-op
// below. Reproduced, because it is what the page does; reported as ToDo.md
// item 6, where the fix is to blank the keys. WAN passes a no-op too and gets
// it right by having no keyed columns at all.
//
// ── FASTTRACK ──────────────────────────────────────────────────────────────
//
// A fasttracked connection bypasses simple queues and any tree parented to
// `global`, so a queue can look perfectly configured and do nothing at all. The
// banner says so, and only when a queue is actually affected.
//
// ── WRITES GO THROUGH THE RESOURCE ENGINE ──────────────────────────────────
//
// `simpleQueue` and `queueTree` are registry resources: clicking a row opens the
// engine's edit dialog (with Enable, Disable, Reset Counters and Delete), the Add
// slot and the move arrows are the engine's, and the self-throttle warning is the
// `queueThrottle` guard answered in the engine's own prompt. This module renders.

import { esc, el, renderSortHeader, fmtBytes, resRow, type SortCol } from '../dom';
import { mountAdds, mountRows } from '../resource';
import type { Socket } from '../socket';
import type { QueuesPayload } from '../gen/payloads';

// Order first, and it is not cosmetic — see the header.
//
// EVERY KEY IS BLANK ON PURPOSE. renderSortHeader gives any column with a truthy
// key a pointer cursor and a click listener, and this page passes a no-op
// callback with a throwaway sort state — correct, because the order here is the
// router's. Five of these carried keys anyway, so the header invited a click,
// mutated a state object discarded on the next render, and called a function
// that does nothing. That reads as a broken sort.
//
// Making them sortable is not the fix. A simple queue is first-match-wins, so
// position IS semantics, and a sorted view would misrepresent which rule wins.
const SIMPLE_COLS: SortCol[] = [
  { key: '', label: '#' }, { key: '', label: 'Name' }, { key: '', label: 'Target' },
  { key: '', label: 'Limits' }, { key: '', label: 'Rate' }, { key: '', label: '' },
];
const TREE_COLS: SortCol[] = [
  { key: '', label: 'Name' }, { key: '', label: 'Parent' },
  { key: '', label: 'Packet Mark' },
  { key: '', label: 'Max Limit' }, { key: '', label: 'Rate' }, { key: '', label: '' },
];

const HIST_MAX = 40;

export function initQueuesPage(socket: Socket, isVisible: (page: string) => boolean): void {
  const simpleTbEl = el('qSimpleTable');
  const treeTbEl = el('qTreeTable');
  // Bails on a page that is not in the document, exactly as the live IIFE does.
  if (!simpleTbEl || !treeTbEl) return;
  const simpleTb: HTMLElement = simpleTbEl;
  const treeTb: HTMLElement = treeTbEl;

  let data: QueuesPayload | null = null;
  let tab = 'simple';
  // Whether this viewer may write each resource, from the engine's schema answer.
  const writable: Record<string, boolean> = {};
  const resKey = (menu: string): string => (menu === 'tree' ? 'queueTree' : 'simpleQueue');

  function q(): string {
    return (el<HTMLInputElement>('qSearch')?.value || '').toLowerCase().trim();
  }

  // The title is NOT escaped here, matching the original. Every caller passes a
  // literal, so nothing router-supplied reaches it.
  function dash(t?: string): string {
    return '<span style="color:var(--text-muted)"' + (t ? ' title="' + t + '"' : '') + '>&mdash;</span>';
  }

  /** bits/sec to something readable. 0 is a real reading; null is not. */
  function fmtBps(bps: number | null | undefined): string | null {
    if (bps === null || bps === undefined) return null;
    if (bps >= 1e9) return (bps / 1e9).toFixed(2) + ' Gb/s';
    if (bps >= 1e6) return (bps / 1e6).toFixed(2) + ' Mb/s';
    if (bps >= 1e3) return (bps / 1e3).toFixed(0) + ' kb/s';
    return Math.round(bps) + ' b/s';
  }

  /** A configured limit. 0 means explicitly unlimited, which is not "unset". */
  function fmtLimit(bps: number | null | undefined): string {
    if (bps === null || bps === undefined) return '&mdash;';
    if (bps === 0) return '<span style="color:var(--text-muted)">unlimited</span>';
    return esc(fmtBps(bps));
  }

  // Rate history for the sparklines, client-side for the reason the VLANs page
  // gives: it is presentation state, and pushing on re-render would forge
  // samples the router never sent.
  const hist: Record<string, { up: number[]; down: number[] }> = {};

  function pushHistory(d: QueuesPayload): void {
    const live: Record<string, boolean> = {};
    (d.simple || []).forEach((x) => {
      live['s' + x.id] = true;
      const h = hist['s' + x.id] || (hist['s' + x.id] = { up: [], down: [] });
      if (x.rateBps && x.rateBps.up !== null) h.up.push(x.rateBps.up);
      if (x.rateBps && x.rateBps.down !== null) h.down.push(x.rateBps.down);
      if (h.up.length > HIST_MAX) h.up.splice(0, h.up.length - HIST_MAX);
      if (h.down.length > HIST_MAX) h.down.splice(0, h.down.length - HIST_MAX);
    });
    (d.tree || []).forEach((x) => {
      live['t' + x.id] = true;
      const h = hist['t' + x.id] || (hist['t' + x.id] = { up: [], down: [] });
      if (x.rateBps !== null) h.up.push(x.rateBps);
      if (h.up.length > HIST_MAX) h.up.splice(0, h.up.length - HIST_MAX);
    });
    // A removed queue must not leave its trend behind for a recreated one.
    Object.keys(hist).forEach((k) => { if (!live[k]) delete hist[k]; });
  }

  // Stroked with currentColor and coloured by class, so the line cannot drift
  // from the value printed beside it.
  function spark(history: number[] | undefined, dir: string): string {
    if (!history || history.length < 2) return '<span class="q-spark-slot"></span>';
    const w = 56, h = 14, pad = 1.5;
    const max = Math.max.apply(null, history) || 1;
    const pts = history.map((v, i) => {
      const x = pad + (i / (history.length - 1)) * (w - pad * 2);
      const y = h - pad - (v / max) * (h - pad * 2);
      return x.toFixed(1) + ',' + y.toFixed(1);
    });
    return '<svg class="q-spark ' + dir + '" width="' + w + '" height="' + h + '" viewBox="0 0 ' + w + ' ' + h + '">' +
      '<polyline points="' + pts.join(' ') + '" fill="none" stroke="currentColor"' +
      ' stroke-width="1.3" stroke-linejoin="round" stroke-linecap="round"/></svg>';
  }

  function rateLine(dir: string, bps: number | null, history: number[]): string {
    const cls = bps ? dir : 'zero';
    return '<div class="q-rate-line">' +
      '<span class="q-rate-arrow ' + cls + '">' + (dir === 'rx' ? '↓' : '↑') + '</span>' +
      '<span class="q-rate-val ' + cls + '">' + esc(fmtBps(bps || 0)) + '</span>' +
      spark(history, dir) +
    '</div>';
  }

  /**
   * The rate cell.
   *
   * null is "the router did not report this", which is not "idle" — the
   * distinction the collector goes to some trouble to preserve, so the page must
   * not throw it away at the last step. The title says how the number was
   * arrived at, including the measurement window.
   */
  function rateCell(key: string, up: number | null, down: number | null,
                    source: string | null, windowMs: number | null): string {
    if (up === null && down === null) {
      return dash(source === null ? 'No measurement yet'
                                  : 'The router reported no rate for this queue');
    }
    const h = hist[key] || { up: [], down: [] };
    const title = source === 'router'
      ? 'Router-reported average (bytes/sec), shown until a window is measured'
      : 'Measured over ' + ((windowMs || 0) / 1000).toFixed(1) + ' s';
    return '<div class="q-rate" title="' + esc(title) + '">' +
      rateLine('tx', up, h.up) +
      (down === null ? '' : rateLine('rx', down, h.down)) + '</div>';
  }

  // Dynamic rows belong to another RouterOS feature; the engine refuses them
  // too, and here they simply do not open a dialog.
  function lockNote(dynamic: boolean): string {
    return dynamic
      ? '<span class="muted-note" title="Created automatically by another RouterOS feature — Kid Control, a DHCP lease, or a PPP profile. Change the feature that creates it.">&#128274; dynamic</span>'
      : '';
  }

  // Move arrows for the simple table only: its order is first-match-wins. Not
  // drawn while searching, where a move would pass rows the operator cannot see.
  function moveCell(at: number, last: number, dynamic: boolean): string {
    if (dynamic || !writable.simpleQueue || q()) return '';
    return '<button class="fw-move" data-res-move="up" title="Move earlier — the first matching queue wins"' +
      (at === 0 ? ' disabled' : '') + '>&#9650;</button>' +
      '<button class="fw-move" data-res-move="down" title="Move later"' +
      (at === last ? ' disabled' : '') + '>&#9660;</button>';
  }

  /**
   * The empty state does real work here.
   *
   * A fresh install has no queues on any router, so this — not a populated table
   * — is what most people see first. Saying "Waiting for data…" forever would be
   * both wrong and unhelpful, so it explains what the tab is for.
   */
  function emptyState(term: string, menu: string): string {
    if (term) return 'No queues match that search.';
    if (!data) return 'Waiting for queue data&hellip;';
    if (data.denied) return 'This router\'s MikroDash account cannot read queues.';
    return menu === 'simple'
      ? 'No simple queues on this router. A simple queue caps the bandwidth of one target &mdash; an address, a subnet, or an interface.' +
        (writable.simpleQueue ? ' Use <strong>Add</strong> to create one.' : '')
      : 'No queue trees on this router. A tree shapes traffic that firewall mangle rules have marked, which makes it the tool for shaping by protocol or application rather than by address.';
  }

  function renderSimple(): void {
    const term = q();
    const rows = (data?.simple || []).filter((x) =>
      !term || (x.name + ' ' + x.target + ' ' + x.comment).toLowerCase().indexOf(term) !== -1);
    // A FRESH state object and a no-op callback, both the original's: this table
    // is not sortable, because its order is the router's.
    renderSortHeader('qSimpleThead', SIMPLE_COLS, { col: '', dir: 'asc' }, () => {});
    const badge = el('qSimpleBadge');
    if (badge) badge.textContent = String((data?.simple || []).length);

    const last = (data?.simple || []).length - 1;
    simpleTb.innerHTML = rows.length ? rows.map((x) => {
      const flags = (x.disabled ? '<span class="wl-band wl-band-24">disabled</span> ' : '') +
                    (x.invalid ? '<span class="wl-band wl-band-24">invalid</span> ' : '');
      return '<tr' + (x.disabled ? ' style="opacity:.62"' : '') +
        (x.dynamic ? '' : resRow(x.id, x.name, 'simpleQueue')) + '>' +
        '<td class="q-order">' + (x.order + 1) + '</td>' +
        '<td>' + flags + esc(x.name) + (x.comment ? '<div class="muted-note">' + esc(x.comment) + '</div>' : '') + '</td>' +
        '<td>' + (x.target ? esc(x.target) : dash()) + '</td>' +
        '<td><div style="font-size:.72rem">&uarr; ' + fmtLimit(x.maxLimit.up) + '<br>&darr; ' + fmtLimit(x.maxLimit.down) + '</div></td>' +
        '<td>' + rateCell('s' + x.id, x.rateBps.up, x.rateBps.down, x.rateSource, x.rateWindowMs) + '</td>' +
        '<td>' + moveCell(x.order, last, x.dynamic) + lockNote(x.dynamic) + '</td>' +
      '</tr>';
    }).join('') : '<tr><td colspan="6" class="empty-state">' + emptyState(term, 'simple') + '</td></tr>';
  }

  function renderTree(): void {
    const term = q();
    const rows = (data?.tree || []).filter((x) =>
      !term || (x.name + ' ' + x.parent + ' ' + x.packetMark).toLowerCase().indexOf(term) !== -1);
    renderSortHeader('qTreeThead', TREE_COLS, { col: '', dir: 'asc' }, () => {});
    const badge = el('qTreeBadge');
    if (badge) badge.textContent = String((data?.tree || []).length);

    const ftActive = !!(data && data.fasttrack && data.fasttrack.state === 'active');
    treeTb.innerHTML = rows.length ? rows.map((x) => {
      const flags = (x.disabled ? '<span class="wl-band wl-band-24">disabled</span> ' : '') +
                    (x.invalid ? '<span class="wl-band wl-band-24">invalid</span> ' : '');
      // Only a global-parented tree is bypassed by FastTrack; one parented to an
      // interface still works, so the chip is per row rather than per table.
      const ft = (ftActive && x.fasttrackBypassable && !x.disabled)
        ? ' <span class="wl-band wl-band-24" title="FastTrack bypasses queue trees parented to global">bypassed</span>' : '';
      return '<tr' + (x.disabled ? ' style="opacity:.62"' : '') + resRow(x.id, x.name, 'queueTree') + '>' +
        '<td>' + flags + esc(x.name) + (x.comment ? '<div class="muted-note">' + esc(x.comment) + '</div>' : '') + '</td>' +
        '<td>' + esc(x.parent || '—') + ft + '</td>' +
        '<td>' + (x.packetMark ? esc(x.packetMark) : dash()) + '</td>' +
        '<td style="font-size:.72rem">' + fmtLimit(x.maxLimit) + '</td>' +
        '<td>' + rateCell('t' + x.id, x.rateBps, null, x.rateSource, x.rateWindowMs) + '</td>' +
        '<td></td>' +
      '</tr>';
    }).join('') : '<tr><td colspan="6" class="empty-state">' + emptyState(term, 'tree') + '</td></tr>';
  }

  function renderFasttrack(): void {
    const card = el('qFtCard'), banner = el('qFtBanner');
    const nCard = el('qNoticeCard'), notice = el('qNotice');
    if (!card || !banner) return;
    const ft = (data && data.fasttrack) || { state: 'unknown', count: 0, scoped: false };

    if (ft.state === 'unknown') {
      card.style.display = 'none';
      // A footnote, not an alarm: we cannot check, which is not the same as bad.
      if (nCard) nCard.style.display = '';
      if (notice) notice.innerHTML = 'Cannot check for a FastTrack rule &mdash; Firewall collection is switched off for this router.';
      return;
    }
    if (nCard) nCard.style.display = 'none';

    // Only warn when something is actually affected. On a router with no queues
    // — which is every router until somebody makes one — this stays hidden.
    const affected = (data?.simple || []).some((x) => !x.disabled && !x.dynamic) ||
                     (data?.tree || []).some((x) => !x.disabled && x.fasttrackBypassable);
    if (ft.state !== 'active' || !affected) { card.style.display = 'none'; return; }

    card.style.display = '';
    // Measured against a live router rather than assumed: with the default
    // FastTrack rule active, a fresh queue on the LAN still counted several
    // megabits within seconds. FastTrack diverts the connections it matches, not
    // all traffic, so the honest claim is "some of it bypasses these queues".
    banner.innerHTML = '<strong>FastTrack is active on this router.</strong> ' +
      'FastTracked connections bypass simple queues and any queue tree parented to <code>global</code>, so a queue here ' +
      'only shapes the traffic FastTrack did not take' +
      (ft.scoped ? ' — and this rule is narrowed, so it takes only part of it.' : ', which can be a small fraction of the total.') +
      ' If a limit looks like it is having no effect, this is usually why. ' +
      'To shape that traffic too, disable the FastTrack rule in <em>IP &rarr; Firewall &rarr; Filter</em>, or exclude the traffic from it.';
  }

  function renderSummary(): void {
    const d = data;
    const set = (id: string, v: string) => { const e = el(id); if (e) e.textContent = v; };
    set('qSumSimple', String((d?.simple || []).length || '—'));
    set('qSumTree', String((d?.tree || []).length || '—'));
    const live = (d?.simple || []).filter((x) => x.rateBps && (x.rateBps.up || x.rateBps.down)).length +
                 (d?.tree || []).filter((x) => x.rateBps).length;
    set('qSumActive', (d && (d.simple || d.tree)) ? String(live) : '—');
    let total = 0, any = false;
    (d?.simple || []).forEach((x) => {
      if (x.bytes.up !== null) { total += x.bytes.up + (x.bytes.down || 0); any = true; }
    });
    (d?.tree || []).forEach((x) => { if (x.bytes !== null) { total += x.bytes; any = true; } });
    set('qSumBytes', any ? fmtBytes(total) : '—');
  }

  function render(): void {
    renderSimple(); renderTree(); renderFasttrack(); renderSummary();
    const note = el('qActionNote');
    if (note) {
      note.textContent = !writable[resKey(tab)] ? 'read-only — you do not have write access to this router'
        : (data && data.stats === 'none') ? 'this router reports no queue statistics' : '';
    }
  }

  /** Point the Add slot at the table now on screen. */
  function syncAddSlot(): void {
    const slot = el('qAddSlot');
    if (!slot) return;
    slot.setAttribute('data-res-add', resKey(tab));
    document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
  }

  mountAdds(socket);
  mountRows(socket);

  // The page draws its arrows and note from `permitted`; every gate is re-checked
  // server-side against a fresh read regardless.
  socket.on('res:schema', (d) => {
    if (!d || (d.key !== 'simpleQueue' && d.key !== 'queueTree')) return;
    writable[d.key] = !!d.permitted;
    if (isVisible('queues')) render();
  });

  socket.on('queues:update', (d) => {
    if (!d) return;
    data = d;
    pushHistory(d);
    // The summary updates whether or not the page is showing; the tables only
    // when it is. The asymmetry is the original's.
    renderSummary();
    if (isVisible('queues')) render();
  });

  const search = el<HTMLInputElement>('qSearch');
  search?.addEventListener('input', () => render());

  document.querySelectorAll('#qTabBar .stab').forEach((b) => {
    b.addEventListener('click', () => {
      tab = b.getAttribute('data-qtab') || 'simple';
      document.querySelectorAll('#qTabBar .stab').forEach((o) => {
        const on = o === b;
        o.classList.toggle('active', on);
        o.setAttribute('aria-selected', on ? 'true' : 'false');
      });
      document.querySelectorAll('#queuesCard .brtab-panel').forEach((pnl) => {
        pnl.classList.toggle('active', pnl.id === 'qtab-' + tab);
      });
      syncAddSlot();
      render();
    });
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'queues') return;
    if (data) render();
  });
}
