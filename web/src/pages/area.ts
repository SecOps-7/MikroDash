// THE ONE MODULE THAT RENDERS A GENERATED PAGE.
//
// `internal/areas` declares a RouterOS menu as a page; `cmd/areagen` writes that
// declaration into `gen/areas.ts` and each page's markup shell. This fills the
// shell: the tabs, the header row, the rows, and the Add slot — for every area
// there is and every area there will be.
//
// ── IT OWNS NO WRITE PATH ───────────────────────────────────────────────────
//
// A row carries `data-id`, `data-identity` and `data-res`, and the table carries
// `data-res-rows`, so clicking one opens the resource engine's own dialog with
// its guards, its read-back, its audit row and its undo. The Add slot is the
// engine's too. There is nothing here to get wrong twice.
//
// ── COLUMNS COME FROM THE DECLARATION, VALUES FROM THE PAYLOAD ──────────────
//
// The header is the declared column order; each cell is that field's value as the
// collector sent it, and a field the router did not answer renders as a dash
// rather than as an empty cell, which reads as a value of "".

import { el, esc, resRow } from '../dom';
import { mountAdds, mountRows } from '../resource';
import { AREAS, type Area } from '../gen/areas';
import type { Socket } from '../socket';
import type { AreaPayload } from '../gen/payloads';

/** The tab showing on each area, by page key. Reset when an area first renders. */
const activeTab: Record<string, number> = {};

/** The last payload per area, so a tab switch redraws without waiting for a tick. */
const latest: Record<string, AreaPayload> = {};

/** Whether this viewer may write each resource, from the engine's schema answer. */
const writable: Record<string, boolean> = {};

function tabIndex(area: Area): number {
  const at = activeTab[area.key] || 0;
  return at < area.tables.length ? at : 0;
}

/** A dash, not an empty cell: "the router said nothing" is not "the value is ''". */
function cell(v: string | undefined): string {
  return v === undefined || v === '' ? '<span style="color:var(--text-muted)">&mdash;</span>' : esc(v);
}

function renderTabs(area: Area): void {
  const host = el('areaTabs-' + area.key);
  if (!host) return;
  // ONE TABLE IS NOT A TAB BAR. A single tab to click is furniture.
  if (area.tables.length < 2) {
    host.innerHTML = '';
    return;
  }
  const at = tabIndex(area);
  host.innerHTML = area.tables.map((t, i) =>
    '<button class="stab' + (i === at ? ' active' : '') + '" type="button" role="tab"' +
    ' aria-selected="' + (i === at ? 'true' : 'false') + '"' +
    ' data-areatab="' + esc(area.key) + '" data-areatabindex="' + i + '">' +
    esc(t.title) + '</button>').join('');
}

/** Point the Add slot at the table on screen, and let the engine fill it. */
function syncAddSlot(area: Area): void {
  const slot = el('areaAdd-' + area.key);
  if (!slot) return;
  slot.setAttribute('data-res-add', area.tables[tabIndex(area)]!.resource);
  document.dispatchEvent(new CustomEvent('mikrodash:resmount'));
}

function render(area: Area): void {
  const body = el('areaBody-' + area.key);
  if (!body) return;
  const at = tabIndex(area);
  const declared = area.tables[at]!;
  const payload = latest[area.key];
  const table = payload?.tables?.[at];

  const badge = el('areaBadge-' + area.key);
  if (badge) badge.textContent = String(table?.rows?.length ?? 0);

  renderTabs(area);

  if (!payload) {
    body.innerHTML = '<div class="empty-state">Waiting&hellip;</div>';
    return;
  }
  // THREE DIFFERENT SENTENCES, because they have three different fixes: the
  // account cannot read the menu, this build has no such menu, or the router
  // genuinely holds none of these.
  if (payload.denied) {
    body.innerHTML = '<div class="empty-state">This router’s MikroDash account cannot read ' +
      esc(declared.resource) + '. RouterOS requires a user group with permission for this menu.</div>';
    return;
  }
  if (table?.unsupported) {
    body.innerHTML = '<div class="empty-state">This router does not have that menu. ' +
      'It may need a package this RouterOS build does not include.</div>';
    return;
  }

  const head = declared.columns.map((c) => '<th>' + esc(columnLabel(c)) + '</th>').join('');
  // A DISABLED OR INVALID ROW IS DIMMED, as the hand-built pages dim theirs:
  // almost every RouterOS menu has `disabled`, and `invalid` is the router saying
  // a row refers to something that is gone. The column still says which.
  const rows = (table?.rows || []).map((r) =>
    '<tr' + (r.values?.disabled === 'true' || r.values?.invalid === 'true' ? ' style="opacity:.55"' : '') +
    resRow(r.id, r.identity, declared.resource) + '>' +
    declared.columns.map((c) => '<td>' + cell(r.values?.[c]) + '</td>').join('') +
    '</tr>').join('');

  body.innerHTML = '<table class="table table-vcenter mb-0">' +
    '<thead><tr>' + head + '</tr></thead>' +
    '<tbody data-res-rows="' + esc(declared.resource) + '">' +
    (rows || '<tr><td colspan="' + declared.columns.length + '" class="empty-state">' +
      'Nothing here yet.' + (writable[declared.resource] ? ' Use <strong>Add</strong> to create one.' : '') +
      '</td></tr>') +
    '</tbody></table>';
  syncAddSlot(area);
}

/**
 * A column header from a field name: `nextPool` reads as "Next Pool".
 *
 * The RESOURCE's own labels are the better source and the browser does not have
 * them — `res:schema` carries them, but only once a form has been opened, which
 * is after the table has been drawn. Splitting the camelCase name is the honest
 * approximation, and it is what the field is called in the tool catalogue too.
 */
function columnLabel(name: string): string {
  const spaced = name.replace(/([a-z0-9])([A-Z])/g, '$1 $2');
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/**
 * The nav entry for each area, composed into its declared group.
 *
 * GENERATED RATHER THAN WRITTEN INTO shell.html, because the shell is a fixed
 * document and an area is not: forty areas would be forty hand-edits of one file,
 * and the entry would be the half somebody forgets. `applyPageVisibility` then
 * hides or shows it exactly as it does a hand-built page's.
 */
function mountNav(): void {
  for (const area of AREAS) {
    if (document.querySelector('.nav-item[data-page="' + area.key + '"]')) continue;
    const group = document.querySelector('#navgrp-' + area.navGroup);
    if (!group) continue;
    const a = document.createElement('a');
    a.className = 'nav-item';
    a.setAttribute('data-page', area.key);
    a.setAttribute('data-cat', area.navGroup);
    a.setAttribute('href', '#');
    a.innerHTML = '<span class="nav-icon"><svg viewBox="0 0 24 24">' +
      '<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 10h18"/><path d="M9 10v10"/>' +
      '</svg></span><span class="nav-label">' + esc(area.title) + '</span>';
    group.appendChild(a);
  }
}

export function initAreaPages(socket: Socket, isVisible: (page: string) => boolean): void {
  if (AREAS.length === 0) return;
  mountNav();
  mountAdds(socket);
  mountRows(socket);

  document.addEventListener('click', (ev) => {
    const b = (ev.target as HTMLElement | null)?.closest?.('[data-areatab]');
    if (!b) return;
    const key = b.getAttribute('data-areatab') || '';
    const area = AREAS.find((a) => a.key === key);
    if (!area) return;
    activeTab[key] = Number(b.getAttribute('data-areatabindex') || 0);
    render(area);
  });

  socket.on('area:update', (d) => {
    if (!d || !d.area) return;
    const area = AREAS.find((a) => a.key === d.area);
    if (!area) return;
    latest[d.area] = d;
    if (isVisible(d.area)) render(area);
  });

  // The page draws its empty state from `permitted`; every gate is re-checked
  // server-side against a fresh read regardless.
  socket.on('res:schema', (d) => {
    if (!d || !d.key) return;
    const owns = AREAS.some((a) => a.tables.some((t) => t.resource === d.key));
    if (!owns) return;
    writable[d.key] = !!d.permitted;
    for (const area of AREAS) {
      if (isVisible(area.key)) render(area);
    }
  });

  document.addEventListener('mikrodash:pagechange', (e) => {
    const key = (e as CustomEvent).detail;
    const area = AREAS.find((a) => a.key === key);
    if (!area) return;
    render(area);
    syncAddSlot(area);
  });
}
