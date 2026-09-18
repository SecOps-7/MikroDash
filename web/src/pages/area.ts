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
//
// ── EVERY HEADER SORTS, AND UNSORTED IS THE ROUTER'S ORDER ──────────────────
//
// A click on a column sorts it ascending, a second descending, and a third
// returns to the order the router sent, which is where every table starts. The
// state is per area and per tab, and lives here rather than in the render, so
// the periodic `area:update` redraws keep it.
//
// EXCEPT AN ORDERED RESOURCE (routing rules, IPsec policies, OSPF interface
// templates): the first row that matches decides, so the router's order IS what
// the table means, and a sorted view of it reads as a different rule set. It
// does not sort at all, as the Queues page's first-match tables do not — the
// operator's choice on 2026-09-18.
//
// ── KEY COLUMNS ARE PILLS, BY KIND ──────────────────────────────────────────
//
// `internal/areas` names a KIND per column (Table.Pills and CommonPills); the
// colours for each kind are here, once, in the hand-built pages' own pill
// styles. `PillKind` is generated from Go's list, so `PILLS` below missing a
// kind, or naming one Go does not have, fails tsc.

import { el, esc, resRow, renderSortHeader, sortRows, type SortCol, type SortState } from '../dom';
import { mountAdds, mountRows } from '../resource';
import { AREAS, type Area, type PillKind } from '../gen/areas';
import { actionBadge } from './firewall';
import type { Socket } from '../socket';
import type { AreaPayload } from '../gen/payloads';

/** The tab showing on each area, by page key. Reset when an area first renders. */
const activeTab: Record<string, number> = {};

/** The last payload per area, so a tab switch redraws without waiting for a tick. */
const latest: Record<string, AreaPayload> = {};

/** The sort on each area's each tab, by `key#tab`. `col: ''` is the router's order. */
const sorts: Record<string, SortState> = {};
/** The sort each table was last DRAWN with. `renderSortHeader` flips the state
 *  before calling back, so this is how a click on a column already sorted
 *  descending is told apart from a first click, and becomes "unsorted". */
const drawn: Record<string, SortState> = {};

function sortFor(area: Area, at: number): SortState {
  const k = area.key + '#' + at;
  return (sorts[k] = sorts[k] || { col: '', dir: 'asc' });
}

/** A value to sort by: a number when the router sent one ("1500", "-3"), so 10
 *  follows 9; the string otherwise; undefined when the router sent nothing, and
 *  `sortRows` puts those first ascending and last descending. */
function sortKey(v: string | undefined): string | number | undefined {
  if (v === undefined || v === '') return undefined;
  return /^-?\d+(\.\d+)?$/.test(v) ? Number(v) : v;
}

/** Whether this viewer may write each resource, from the engine's schema answer. */
const writable: Record<string, boolean> = {};
/** Whether a row can be ADDED to each resource: an OSPF neighbour cannot, so its
 *  empty table must not tell the viewer to use an Add button that is not there. */
const creatable: Record<string, boolean> = {};

function tabIndex(area: Area): number {
  const at = activeTab[area.key] || 0;
  return at < area.tables.length ? at : 0;
}

/** A dash, not an empty cell: "the router said nothing" is not "the value is ''". */
function cell(v: string | undefined): string {
  return v === undefined || v === '' ? '<span style="color:var(--text-muted)">&mdash;</span>' : esc(v);
}

/** A status word's colour, by the vocabulary the router reports. Lower-cased and
 *  without RouterOS's trailing "..." ("searching..."), so both spellings match.
 *  A word not listed is a neutral pill: it is still a state, just not one this
 *  table knows the meaning of. */
const STATE_OK = new Set(['bound', 'full', 'established', 'running', 'synchronized', 'connected']);
const STATE_WARN = new Set(['searching', 'requesting', 'rebinding', 'renewing', 'stopping', 'connecting',
  'init', 'attempt', '2-way', 'exstart', 'exchange', 'loading', 'waiting', 'starting']);
const STATE_BAD = new Set(['error', 'down', 'expired', 'timeout']);

function pill(cls: string, text: string): string {
  return '<span class="vpn-hs-badge ' + cls + '">' + esc(text) + '</span>';
}

/** A flag pill: "yes" in the kind's colour, "no" neutral. RouterOS says yes and
 *  no; the payload carries true and false. */
const flag = (cls: string) => (v: string): string => (v === 'true' ? pill(cls, 'yes') : v === 'false' ? pill('hs-never', 'no') : esc(v));

const PILLS: Record<PillKind, (v: string) => string> = {
  state: (v) => {
    const w = v.toLowerCase().replace(/\.+$/, '');
    return pill(STATE_OK.has(w) ? 'hs-ok' : STATE_WARN.has(w) ? 'hs-warn' : STATE_BAD.has(w) ? 'hs-stale' : 'hs-never', v);
  },
  action: (v) => actionBadge(v),
  good: flag('hs-ok'),
  warn: flag('hs-warn'),
  bad: flag('hs-stale'),
  info: flag('hs-info'),
};

/** One cell: a pill when the declaration names a kind for the column, the plain
 *  value otherwise, and the dash either way when the router sent nothing. */
function valueCell(kind: PillKind | undefined, v: string | undefined): string {
  if (v === undefined || v === '' || !kind) return cell(v);
  return PILLS[kind](v);
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

  // BLUE WHEN THERE IS SOMETHING TO COUNT, as every hand-built page's count pill
  // is (`active-blue`), and the plain pill for a zero.
  const badge = el('areaBadge-' + area.key);
  if (badge) {
    const n = table?.rows?.length ?? 0;
    badge.textContent = String(n);
    badge.className = 'card-badge' + (n > 0 ? ' active-blue' : '');
  }

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

  // A SETTINGS MENU (a singleton) is one row with no id of its own, so it is
  // drawn as a card — a label and a value per field — rather than as a one-row
  // table. Every line carries the row's engine attributes, so clicking any of
  // them opens the one form, which edits the one row.
  if (table?.singleton) {
    const r = table.rows?.[0];
    body.innerHTML = r
      ? '<table class="table table-vcenter mb-0"><tbody data-res-rows="' + esc(declared.resource) + '">' +
        declared.columns.map((c) =>
          '<tr' + resRow(r.id, r.identity, declared.resource) + '>' +
          '<th style="width:34%;font-weight:500;color:var(--text-muted)">' + esc(columnLabel(c)) + '</th>' +
          '<td>' + valueCell(declared.pills[c], r.values?.[c]) + '</td></tr>').join('') +
        '</tbody></table>'
      : '<div class="empty-state">The router did not return these settings.</div>';
    syncAddSlot(area);
    return;
  }

  // AN ORDERED RESOURCE (routing rules) gets the reorder arrows the Firewall page
  // draws, named `data-res-move` so the resource engine owns the move: the first
  // rule that matches decides, so position is part of what a row does. A viewer
  // who may not write gets none. Its headers carry no sort key, so they do not
  // sort, and the table is always in the router's order.
  const arrows = declared.ordered && writable[declared.resource];
  const sort = sortFor(area, at);
  const sorted = !declared.ordered && sort.col !== '';
  const cols: SortCol[] = [
    ...(arrows ? [{ label: '', style: 'width:1%' }] : []),
    ...declared.columns.map((c) => declared.ordered
      ? { label: esc(columnLabel(c)) }
      : { key: c, label: esc(columnLabel(c)) }),
  ];
  const list = table?.rows || [];
  const last = list.length - 1;
  const move = (pos: number): string => '<td style="white-space:nowrap">' +
    '<button class="fw-move" data-res-move="up" title="Move up"' + (pos === 0 ? ' disabled' : '') + '>&#9650;</button>' +
    '<button class="fw-move" data-res-move="down" title="Move down"' + (pos === last ? ' disabled' : '') + '>&#9660;</button></td>';
  const shown = sorted
    ? sortRows(list.map((r, pos) => ({ k: sortKey(r.values?.[sort.col]), r, pos })), 'k', sort.dir)
    : list.map((r, pos) => ({ r, pos }));
  // A DISABLED OR INVALID ROW IS DIMMED, as the hand-built pages dim theirs:
  // almost every RouterOS menu has `disabled`, and `invalid` is the router saying
  // a row refers to something that is gone. The column still says which.
  const rows = shown.map(({ r, pos }) =>
    '<tr' + (r.values?.disabled === 'true' || r.values?.invalid === 'true' ? ' style="opacity:.55"' : '') +
    resRow(r.id, r.identity, declared.resource) + '>' + (arrows ? move(pos) : '') +
    declared.columns.map((c) => '<td>' + valueCell(declared.pills[c], r.values?.[c]) + '</td>').join('') +
    '</tr>').join('');

  body.innerHTML = '<table class="table table-vcenter mb-0">' +
    '<thead><tr id="areaThead-' + esc(area.key) + '"></tr></thead>' +
    '<tbody data-res-rows="' + esc(declared.resource) + '">' +
    (rows || '<tr><td colspan="' + (declared.columns.length + (arrows ? 1 : 0)) + '" class="empty-state">' +
      'Nothing here yet.' + (writable[declared.resource] && creatable[declared.resource] !== false
        ? ' Use <strong>Add</strong> to create one.' : '') +
      '</td></tr>') +
    '</tbody></table>';
  drawn[area.key + '#' + at] = { col: sort.col, dir: sort.dir };
  renderSortHeader('areaThead-' + area.key, cols, sort, () => {
    // The helper has already moved the state: a click on the column that was
    // drawn descending has just flipped it to ascending, and means "unsorted".
    const was = drawn[area.key + '#' + at];
    if (was && was.col === sort.col && was.dir === 'desc') sort.col = '';
    render(area);
  });
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
 *
 * ── CALLED BY main.ts BEFORE wireNav, AND ONLY THERE ────────────────────────
 *
 * `wireNav` binds a click listener to each `.nav-item` that exists when it runs.
 * This was called from `initAreaPages`, two hundred lines later, so every
 * generated entry appeared in the nav and did nothing when clicked: the pages
 * were reachable only by URL, which is how every check had opened them. Found
 * on 2026-09-18 clicking Address Lists. `web/test/area-nav-wired.test.ts` pins
 * the order.
 */
export function mountAreaNav(): void {
  for (const area of AREAS) {
    if (document.querySelector('.nav-item[data-page="' + area.key + '"]')) continue;
    const group = document.querySelector('#navgrp-' + area.navGroup);
    if (!group) continue;
    const a = document.createElement('a');
    a.className = 'nav-item';
    a.setAttribute('data-page', area.key);
    a.setAttribute('data-cat', area.navGroup);
    a.setAttribute('href', '#');
    // THE ICON IS THE AREA'S OWN, declared beside it in internal/areas and
    // generated into gen/areas.ts. It is written unescaped because it is markup,
    // and that is safe only because it is a build-time constant: nothing a
    // router sends ever reaches it. The title is data-shaped, so it is escaped.
    a.innerHTML = '<span class="nav-icon"><svg viewBox="0 0 24 24">' + area.icon +
      '</svg></span><span class="nav-label">' + esc(area.title) + '</span>';
    group.appendChild(a);
  }
}

export function initAreaPages(socket: Socket, isVisible: (page: string) => boolean): void {
  if (AREAS.length === 0) return;
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
    creatable[d.key] = d.creatable !== false;
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
