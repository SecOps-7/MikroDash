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
// This module is the RENDERING half. The dialog and the write actions, including
// the self-throttle acknowledgement, are their own piece of work.

import { esc, el, renderSortHeader, fmtBytes, type SortCol } from '../dom';
import type { Socket } from '../socket';

export interface RatePair { up: number | null; down: number | null }
export interface IntPair { up: number | null; down: number | null }

export interface SimpleQueue {
  id: string; order: number; name: string; target: string; parent: string;
  packetMarks: string; priority: string; queueType: string;
  limitAt: RatePair; maxLimit: RatePair; burstLimit: RatePair;
  bytes: IntPair; packets: IntPair; dropped: IntPair; queuedBytes: IntPair;
  disabled: boolean; invalid: boolean; dynamic: boolean; comment: string;
  rateBps: RatePair; rateSource: string | null; rateWindowMs: number | null;
}

export interface TreeQueue {
  id: string; order: number; name: string; parent: string; packetMark: string;
  priority: string; queueType: string;
  limitAt: number | null; maxLimit: number | null; burstLimit: number | null;
  bytes: number | null; packets: number | null; dropped: number | null;
  queuedBytes: number | null;
  disabled: boolean; invalid: boolean; dynamic: boolean; comment: string;
  fasttrackBypassable: boolean;
  rateBps: number | null; rateSource: string | null; rateWindowMs: number | null;
}

export interface QueuesPayload {
  ts: number; pollMs: number;
  simple: SimpleQueue[]; tree: TreeQueue[];
  fasttrack: { state: string; count: number; scoped: boolean };
  stats: string; available: boolean; denied: boolean;
}

export interface QueuesCaps { permitted: boolean; routerName: string }

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
  let caps: QueuesCaps = { permitted: false, routerName: '' };
  let tab = 'simple';
  let busy = '';

  // The status line. Eight seconds, then it clears itself. See the Router Users
  // page for why what it writes rarely survives to be read — `render()` owns
  // this element too, and the same collision applies here.
  let statusTimer: ReturnType<typeof setTimeout> | null = null;
  function setStatus(text: string): void {
    const e = el('qActionNote');
    if (!e) return;
    e.textContent = text || '';
    // Marked while a message is showing. render() writes this same element from
    // caps.permitted and runs again on the next payload — the server calls
    // RefreshNow after every write — so unmarked it erased the message in the
    // same tick on a failure, and one round trip later on a success. The 8 s
    // timer never got to expire and the operator saw nothing either way.
    if (text) e.dataset.status = '1'; else delete e.dataset.status;
    if (statusTimer) clearTimeout(statusTimer);
    if (text) {
      statusTimer = setTimeout(() => { e.textContent = ''; delete e.dataset.status; }, 8000);
    }
  }

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

  function actions(row: { id: string; name: string; disabled: boolean; dynamic: boolean },
                   menu: string): string {
    if (row.dynamic) {
      return '<span class="muted-note" title="Created automatically by another RouterOS feature — Kid Control, a DHCP lease, or a PPP profile. Change the feature that creates it.">&#128274; dynamic</span>';
    }
    if (!caps.permitted) return '';
    const b = (act: string, label: string, cls?: string, extra?: string): string =>
      '<button class="ru-act' + (cls ? ' ' + cls : '') + '" data-qact="' + act +
      '" data-id="' + esc(row.id) + '" data-menu="' + menu + '" data-name="' + esc(row.name) + '"' +
      (extra || '') + (busy === row.id ? ' disabled' : '') + '>' + label + '</button>';

    let out = b('edit', 'Edit') + ' ' + b('toggle', row.disabled ? 'Enable' : 'Disable');
    if (menu === 'simple') {
      out += ' ' + b('up', '&uarr;', '', ' title="Move earlier — the first matching queue wins"') +
             b('down', '&darr;', '', ' title="Move later"');
    }
    out += ' ' + b('reset', 'Reset') + ' ' + b('remove', 'Remove', 'danger');
    return out;
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
        (caps.permitted ? ' Use <strong>Add Queue</strong> to create one.' : '')
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

    simpleTb.innerHTML = rows.length ? rows.map((x) => {
      const flags = (x.disabled ? '<span class="wl-band wl-band-24">disabled</span> ' : '') +
                    (x.invalid ? '<span class="wl-band wl-band-24">invalid</span> ' : '');
      return '<tr' + (x.disabled ? ' style="opacity:.62"' : '') + '>' +
        '<td class="q-order">' + (x.order + 1) + '</td>' +
        '<td>' + flags + esc(x.name) + (x.comment ? '<div class="muted-note">' + esc(x.comment) + '</div>' : '') + '</td>' +
        '<td>' + (x.target ? esc(x.target) : dash()) + '</td>' +
        '<td><div style="font-size:.72rem">&uarr; ' + fmtLimit(x.maxLimit.up) + '<br>&darr; ' + fmtLimit(x.maxLimit.down) + '</div></td>' +
        '<td>' + rateCell('s' + x.id, x.rateBps.up, x.rateBps.down, x.rateSource, x.rateWindowMs) + '</td>' +
        '<td>' + actions(x, 'simple') + '</td>' +
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
      return '<tr' + (x.disabled ? ' style="opacity:.62"' : '') + '>' +
        '<td>' + flags + esc(x.name) + (x.comment ? '<div class="muted-note">' + esc(x.comment) + '</div>' : '') + '</td>' +
        '<td>' + esc(x.parent || '—') + ft + '</td>' +
        '<td>' + (x.packetMark ? esc(x.packetMark) : dash()) + '</td>' +
        '<td style="font-size:.72rem">' + fmtLimit(x.maxLimit) + '</td>' +
        '<td>' + rateCell('t' + x.id, x.rateBps, null, x.rateSource, x.rateWindowMs) + '</td>' +
        '<td>' + actions(x, 'tree') + '</td>' +
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
    const add = el('qAddBtn');
    if (add) {
      add.textContent = tab === 'tree' ? '+ Add Tree Queue' : '+ Add Simple Queue';
      add.style.display = caps.permitted ? '' : 'none';
    }
    const note = el('qActionNote');
    // Never clears a message it did not write — see setStatus.
    if (note && !note.dataset.status) {
      note.textContent = !caps.permitted ? 'read-only — you do not have write access to this router'
        : (data && data.stats === 'none') ? 'this router reports no queue statistics' : '';
    }
  }

  // ── Dialog ────────────────────────────────────────────────────────────────

  function setVal(id: string, v: string): void {
    const e = el<HTMLInputElement>(id);
    if (e) e.value = v;
  }
  function show(id: string, on: boolean): void {
    const e = el(id);
    if (e) e.style.display = on ? '' : 'none';
  }

  /** Raw bps back to the suffixed form, so an edit round-trips what was typed. */
  function bpsToShort(bps: number | null | undefined): string {
    if (bps === null || bps === undefined) return '';
    if (bps === 0) return '0';
    if (bps % 1e9 === 0) return (bps / 1e9) + 'G';
    if (bps % 1e6 === 0) return (bps / 1e6) + 'M';
    if (bps % 1e3 === 0) return (bps / 1e3) + 'k';
    return String(bps);
  }

  function formError(msg: string): void {
    const e = el('qf_error');
    if (!e) return;
    e.textContent = msg;
    e.style.display = '';
  }

  function openForm(menu: string, row: SimpleQueue | TreeQueue | null): void {
    const title = el('qf_title');
    if (title) title.textContent = (row ? 'Edit ' : 'Add ') + (menu === 'tree' ? 'Tree Queue' : 'Simple Queue');
    setVal('qf_menu', menu);
    setVal('qf_id', row ? row.id : '');
    setVal('qf_expectedName', row ? row.name : '');
    setVal('qf_ack', '');
    setVal('qf_name', row ? row.name : '');
    setVal('qf_comment', row ? row.comment : '');
    setVal('qf_priority', row ? row.priority : '');
    const dis = el<HTMLInputElement>('qf_disabled');
    if (dis) dis.checked = row ? !!row.disabled : false;

    const simple = menu === 'simple';
    show('qf_targetWrap', simple);
    show('qf_parentWrap', !simple);
    const markLabel = el('qf_markLabel');
    if (markLabel) markLabel.textContent = simple ? 'Packet Marks' : 'Packet Mark';
    const maxHint = el('qf_maxHint');
    if (maxHint) maxHint.textContent = simple ? '(up/down, e.g. 15M/20M)' : '(e.g. 10M)';

    const sq = simple ? (row as SimpleQueue | null) : null;
    const tq = simple ? null : (row as TreeQueue | null);
    setVal('qf_target', sq ? sq.target : '');
    // A tree defaults to `global` on create; a simple queue to empty.
    setVal('qf_parent', tq ? tq.parent : (simple ? '' : 'global'));
    setVal('qf_packetMark', row ? (sq ? sq.packetMarks : (tq ? tq.packetMark : '')) : '');

    // The router answers in raw bps; the form shows the same suffixed form the
    // operator would have typed.
    const lim = bpsToShort;
    setVal('qf_maxLimit', row
      ? (sq ? lim(sq.maxLimit.up) + '/' + lim(sq.maxLimit.down) : lim(tq!.maxLimit))
      : '');
    setVal('qf_limitAt', row
      ? (sq ? lim(sq.limitAt.up) + '/' + lim(sq.limitAt.down) : lim(tq!.limitAt))
      : '');

    show('qf_error', false);
    show('qf_warn', false);
    el('qFormWrap')?.classList.add('open');
  }

  function submit(ack?: string): void {
    const menu = el<HTMLInputElement>('qf_menu')?.value || 'simple';
    const name = (el<HTMLInputElement>('qf_name')?.value || '').trim();
    if (!name) return formError('A queue name is required');
    const val = (id: string) => (el<HTMLInputElement>(id)?.value || '').trim();

    const payload: Record<string, unknown> = {
      menu,
      id: el<HTMLInputElement>('qf_id')?.value || undefined,
      expectedName: el<HTMLInputElement>('qf_expectedName')?.value || undefined,
      name,
      maxLimit: val('qf_maxLimit'),
      limitAt: val('qf_limitAt'),
      priority: val('qf_priority'),
      comment: val('qf_comment'),
      disabled: !!el<HTMLInputElement>('qf_disabled')?.checked,
      ack: ack || undefined,
    };
    if (menu === 'simple') {
      payload.target = val('qf_target');
      payload.packetMarks = val('qf_packetMark');
      if (!payload.target) {
        return formError('A target is required — an address, a subnet, or an interface');
      }
    } else {
      payload.parent = val('qf_parent');
      payload.packetMark = val('qf_packetMark');
      if (!payload.parent) {
        return formError('A parent is required — "global", or an interface name');
      }
    }
    busy = (payload.id as string) || '';
    socket.emit('queue:save', payload);
  }

  // ── Row actions ───────────────────────────────────────────────────────────

  document.addEventListener('click', (e) => {
    const b = (e.target as HTMLElement | null)?.closest?.('[data-qact]');
    if (!b) return;
    const act = b.getAttribute('data-qact') || '';
    const id = b.getAttribute('data-id') || '';
    const menu = b.getAttribute('data-menu') || 'simple';
    const name = b.getAttribute('data-name') || '';
    const list: Array<SimpleQueue | TreeQueue> =
      (menu === 'tree' ? data?.tree : data?.simple) || [];
    const row = list.find((x) => x.id === id);
    if (!row) return;

    if (act === 'edit') { openForm(menu, row); return; }
    if (act === 'up' || act === 'down') {
      busy = id; render();
      // No menu: only simple queues have meaningful order.
      socket.emit('queue:move', { id, expectedName: name, direction: act });
      return;
    }
    if (act === 'toggle') {
      busy = id; render();
      socket.emit('queue:toggle', { id, expectedName: name, menu });
      return;
    }
    if (act === 'reset') {
      busy = id; render();
      socket.emit('queue:resetCounters', { id, expectedName: name, menu });
      return;
    }
    if (act === 'remove') {
      if (!window.confirm('Remove the queue "' + name +
          '"?\n\nTraffic it was limiting will no longer be shaped.')) return;
      busy = id; render();
      socket.emit('queue:remove', { id, expectedName: name, menu });
    }
  });

  el('qAddBtn')?.addEventListener('click', () => {
    if (!caps.permitted) return;
    openForm(tab === 'tree' ? 'tree' : 'simple', null);
  });

  el('qf_save')?.addEventListener('click', () => {
    submit(el<HTMLInputElement>('qf_ack')?.value || undefined);
  });

  socket.on('queues:error', (d: {
    code?: string; message?: string; fingerprint?: string;
    warning?: { address?: string; target?: string; maxLimit?: { up?: number | null; down?: number | null } };
  }) => {
    busy = '';
    const code = d && d.code;

    // THE SELF-THROTTLE PROMPT IS NOT AN ERROR — it is a question, asked once,
    // and answered by re-submitting with the fingerprint the server issued. The
    // fingerprint is recomputed server-side from a fresh read every time, which
    // is what stops an acknowledgement being carried to a harsher queue.
    if (code === 'self-throttle' || code === 'stale-warning') {
      const w = (d && d.warning) || {};
      const cap = w.maxLimit || {};
      const e = el('qf_warn');
      if (!e) return;
      e.innerHTML = '<strong>This queue covers MikroDash\'s own connection to this router.</strong><br>' +
        'MikroDash reaches ' + esc(caps.routerName || 'this router') + ' from <code>' + esc(w.address || '') + '</code>, ' +
        'which is inside <code>' + esc(w.target || '') + '</code>, and this queue caps traffic at <code>' +
        esc(bpsToShort(cap.up) + '/' + bpsToShort(cap.down)) + '</code>.<br>' +
        'The dashboard\'s own polling will be throttled and this page may become slow. ' +
        'You will still be able to edit or remove the queue from its row.' +
        (code === 'stale-warning' ? '<br><em>The values changed since you confirmed, so please confirm again.</em>' : '') +
        '<div style="margin-top:.5rem;display:flex;gap:.5rem;justify-content:flex-end">' +
        '<button class="sbtn sbtn-outline" id="qf_warnCancel" style="padding:.3rem .7rem;font-size:.72rem">Cancel</button>' +
        '<button class="sbtn sbtn-danger" id="qf_warnGo" style="padding:.3rem .7rem;font-size:.72rem">Create anyway</button></div>';
      e.style.display = '';
      setVal('qf_ack', (d && d.fingerprint) || '');
      el('qf_warnCancel')?.addEventListener('click', () => {
        e.style.display = 'none';
        setVal('qf_ack', '');
      });
      el('qf_warnGo')?.addEventListener('click', () => {
        e.style.display = 'none';
        submit(el<HTMLInputElement>('qf_ack')?.value);
      });
      // A TOGGLE that trips the warning has no dialog open, so open one.
      if (!el('qFormWrap')?.classList.contains('open')) el('qFormWrap')?.classList.add('open');
      return;
    }

    const msg: Record<string, string> = {
      denied: 'You do not have write access to this router',
      unavailable: 'Queue collection is not running for this router',
      'bad-request': 'Invalid request',
      'stale-row': 'That queue changed on the router — the page has been refreshed',
      'dynamic-row': 'That queue is created automatically by another RouterOS feature (Kid Control, a DHCP lease, or a PPP profile) and cannot be edited here',
      'limit-above-max': 'Max Limit must be at least as large as Limit At — the router refuses otherwise',
      'router-write-policy': 'The RouterOS user needs write permission for this',
      unsupported: 'This router does not support that command',
    };
    const text = (code && msg[code]) || (d && d.message) || 'Action failed';
    if (el('qFormWrap')?.classList.contains('open')) formError(text); else setStatus(text);
    if (isVisible('queues')) render();
  });

  socket.on('queues:update', (d: QueuesPayload) => {
    if (!d) return;
    data = d;
    busy = '';
    pushHistory(d);
    // The summary updates whether or not the page is showing; the tables only
    // when it is. The asymmetry is the original's.
    renderSummary();
    if (isVisible('queues')) render();
  });

  socket.on('queues:caps', (d: QueuesCaps) => {
    if (!d) return;
    caps = d;
    if (isVisible('queues')) render();
  });

  socket.on('queues:ok', (d: { action?: string; name?: string }) => {
    busy = '';
    el('qFormWrap')?.classList.remove('open');
    const what: Record<string, string> = {
      create: 'Created ', update: 'Updated ', delete: 'Removed ', enable: 'Enabled ',
      disable: 'Disabled ', reset: 'Reset counters for ', move: 'Reordered ',
    };
    setStatus(((d && d.action && what[d.action]) || 'Done: ') + ((d && d.name) || ''));
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
      render();
    });
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    if ((e as CustomEvent).detail !== 'queues') return;
    // Permission is a property of this socket, not of the shared payload.
    socket.emit('queues:caps', {});
    if (data) render();
  });
}
