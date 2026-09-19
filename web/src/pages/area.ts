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

import { el, esc, resRow, renderSortHeader, sortRows, debounce, type SortCol, type SortState } from '../dom';
import { mountAdds, mountRows } from '../resource';
import { AREAS, type Area, type PillKind } from '../gen/areas';
import { actionBadge } from './firewall';
import type { Socket } from '../socket';
import type { AreaPayload, AreaTable, AreaGroupRowsPayload } from '../gen/payloads';

/** The tab showing on each area, by page key. Unset is the first tab, and an
 *  index past the area's tables reads as the first (tabIndex). */
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

// ── A GROUPED TABLE: A SUMMARY, AND ONE GROUP AT A TIME ────────────────────
//
// A table declared with GroupBy (Address Lists, by list) arrives as one row per
// group with its counts; a group's rows are asked for with `area:group` when it
// is opened, read filtered on the router, searched and capped on the server.
// Per area and tab: which group is open ('' for the summary), its search, and
// the last answer for it.
let sock: Socket | null = null;
const openGroup: Record<string, string> = {};
const groupSearch: Record<string, string> = {};
const groupRows: Record<string, AreaGroupRowsPayload> = {};
const groupSorts: Record<string, SortState> = {};
const groupDrawn: Record<string, SortState> = {};

/** Ask for the open group. `refresh` is a fresh router read; without it (a
 *  search) the server filters the rows it read last, because each read of a
 *  large list is seconds of router time. */
function requestGroup(area: Area, at: number, refresh: boolean): void {
  const k = area.key + '#' + at;
  if (!openGroup[k] || !sock) return;
  sock.emit('area:group', { area: area.key, resource: area.tables[at]!.resource,
    group: openGroup[k], search: groupSearch[k] || '', refresh });
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
    // A grouped table counts its entries, not its groups: the pill says how
    // much the page holds, as it does on every other page.
    const n = table?.groupBy ? (table.groups || []).reduce((sum, g) => sum + g.count, 0) : table?.rows?.length ?? 0;
    badge.textContent = String(n);
    badge.className = 'card-badge' + (n > 0 ? ' active-blue' : '');
  }

  renderTabs(area);
  // The open group's bar (back, search) is kept across redraws so the search box
  // keeps its focus; anything else drawn here replaces it.
  if (!(table?.groupBy && openGroup[area.key + '#' + at])) body.removeAttribute('data-open-group');

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

  if (table?.groupBy) {
    renderGrouped(area, at, table, body);
    syncAddSlot(area);
    return;
  }

  drawRows(area, at, table?.rows || [], body);
  syncAddSlot(area);
}

/** A grouped table: its summary, or the one group that is open. */
function renderGrouped(area: Area, at: number, table: AreaTable, body: HTMLElement): void {
  const k = area.key + '#' + at;
  const g = openGroup[k];
  const noun = columnLabel(table.groupBy);
  if (!g) {
    const sort = (groupSorts[k] = groupSorts[k] || { col: '', dir: 'asc' });
    const list = (table.groups || []).map((grp, pos) => ({ grp, pos }));
    const shown = sort.col
      ? sortRows(list.map((x) => ({ ...x, k: sort.col === 'name' ? x.grp.name
        : (x.grp as unknown as Record<string, number>)[sort.col] })), 'k', sort.dir)
      : list;
    const rows = shown.map(({ grp }) =>
      '<tr data-areagroup="' + esc(grp.name) + '" data-areagroupof="' + esc(area.key) + '" style="cursor:pointer"' +
      ' title="Show the entries in ' + esc(grp.name) + '">' +
      '<td>' + pill('hs-info', grp.name) + '</td><td>' + grp.count.toLocaleString() + '</td>' +
      '<td>' + grp.dynamic.toLocaleString() + '</td><td>' + grp.disabled.toLocaleString() + '</td></tr>').join('');
    const declared = area.tables[at]!;
    body.innerHTML = '<table class="table table-vcenter mb-0">' +
      '<thead><tr id="areaThead-' + esc(area.key) + '"></tr></thead><tbody>' +
      (rows || '<tr><td colspan="4" class="empty-state">Nothing here yet.' +
        (writable[declared.resource] && creatable[declared.resource] !== false ? ' Use <strong>Add</strong> to create one.' : '') +
        '</td></tr>') + '</tbody></table>';
    groupDrawn[k] = { col: sort.col, dir: sort.dir };
    renderSortHeader('areaThead-' + area.key, [
      { key: 'name', label: esc(noun) }, { key: 'count', label: 'Entries' },
      { key: 'dynamic', label: 'Dynamic' }, { key: 'disabled', label: 'Disabled' },
    ], sort, () => {
      const was = groupDrawn[k];
      if (was && was.col === sort.col && was.dir === 'desc') sort.col = '';
      render(area);
    });
    return;
  }
  // ONE GROUP. The bar is drawn once per group, so typing in its search box is
  // not interrupted by the answer to what was typed.
  if (body.getAttribute('data-open-group') !== k + '|' + g) {
    body.setAttribute('data-open-group', k + '|' + g);
    body.innerHTML = '<div class="d-flex align-items-center flex-wrap gap-2 px-3 py-2" style="border-bottom:1px solid var(--border)">' +
      '<button class="sbtn sbtn-outline" type="button" data-areagroupback="' + esc(area.key) + '">&larr; All ' +
      esc(noun.toLowerCase()) + 's</button>' + pill('hs-info', g) +
      '<input type="search" class="sform-input" style="max-width:260px;margin-left:auto" id="areaGroupSearch-' +
      esc(area.key) + '" data-areagroupsearch="' + esc(area.key) + '" placeholder="Search" autocomplete="off" value="' +
      esc(groupSearch[k] || '') + '">' +
      '<span id="areaGroupNote-' + esc(area.key) + '" style="color:var(--text-muted);font-size:.75rem"></span></div>' +
      '<div id="areaGroupTable-' + esc(area.key) + '"><div class="empty-state">Loading&hellip;</div></div>';
  }
  const host = el('areaGroupTable-' + area.key);
  const note = el('areaGroupNote-' + area.key);
  const reply = groupRows[k];
  if (!host) return;
  if (!reply || reply.group !== g) {
    host.innerHTML = '<div class="empty-state">Loading&hellip;</div>';
    if (note) note.textContent = '';
    return;
  }
  if (reply.error) {
    host.innerHTML = '<div class="empty-state">The router did not return this ' + esc(noun.toLowerCase()) +
      ': ' + esc(reply.error) + '</div>';
    if (note) note.textContent = '';
    return;
  }
  // THE COUNT IS THE TRUTH, the rows a window on it: 500 of 37,111 says so.
  if (note) {
    note.textContent = reply.total > reply.rows.length
      ? 'Showing ' + reply.rows.length.toLocaleString() + ' of ' + reply.total.toLocaleString() +
        (reply.search ? ' matches' : '') + '. Search to narrow.'
      : reply.total.toLocaleString() + (reply.search ? ' matching' : '') + (reply.total === 1 ? ' entry' : ' entries');
  }
  drawRows(area, at, reply.rows, host);
}

/** A table of rows with the engine's attributes: sortable headers, pills, the
 *  dimmed disabled rows and, for an ordered resource, the move arrows. */
function drawRows(area: Area, at: number, list: AreaPayload['tables'][number]['rows'], host: HTMLElement): void {
  const declared = area.tables[at]!;
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

  host.innerHTML = '<table class="table table-vcenter mb-0">' +
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
/**
 * A router switch. Every table here holds the LEFT router's rows, groups and
 * permissions, and would draw them under the new router's name until its first
 * payload. Forgotten, and each area redrawn as waiting; the permissions are
 * asked again on `router:switched` (resource.ts), and until then nothing is
 * writable.
 */
export function resetAreaPages(): void {
  for (const m of [latest, openGroup, groupSearch, groupRows, writable, creatable] as Record<string, unknown>[]) {
    for (const k of Object.keys(m)) delete m[k];
  }
  for (const area of AREAS) render(area);
}

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
  sock = socket;
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

  // A GROUP OPENED, OR THE SUMMARY BACK.
  document.addEventListener('click', (ev) => {
    const t = ev.target as HTMLElement | null;
    const row = t?.closest?.('[data-areagroup]');
    const back = t?.closest?.('[data-areagroupback]');
    const key = row?.getAttribute('data-areagroupof') || back?.getAttribute('data-areagroupback') || '';
    const area = AREAS.find((a) => a.key === key);
    if (!area) return;
    const k = area.key + '#' + tabIndex(area);
    openGroup[k] = row ? row.getAttribute('data-areagroup') || '' : '';
    groupSearch[k] = '';
    delete groupRows[k];
    render(area);
    requestGroup(area, tabIndex(area), true);
  });

  // THE SEARCH RUNS ON THE SERVER, a moment after typing stops.
  const searchFor: Record<string, () => void> = {};
  document.addEventListener('input', (ev) => {
    const box = ev.target as HTMLInputElement | null;
    const key = box?.getAttribute?.('data-areagroupsearch');
    const area = key ? AREAS.find((a) => a.key === key) : undefined;
    if (!area || !box) return;
    groupSearch[area.key + '#' + tabIndex(area)] = box.value;
    (searchFor[area.key] = searchFor[area.key] || debounce(() => requestGroup(area, tabIndex(area), false), 300))();
  });

  // AN ANSWER COUNTS ONLY FOR THE GROUP AND SEARCH ON SCREEN: a slow answer to
  // an earlier keystroke must not overwrite the one after it.
  socket.on('area:grouprows', (d) => {
    const area = AREAS.find((a) => a.key === d.area);
    if (!area) return;
    const at = area.tables.findIndex((t) => t.resource === d.resource);
    const k = area.key + '#' + at;
    if (at < 0 || openGroup[k] !== d.group || (groupSearch[k] || '') !== d.search) return;
    groupRows[k] = d;
    if (isVisible(area.key) && tabIndex(area) === at) render(area);
  });

  // AFTER A WRITE, the open group is read again: an edited comment changes no
  // count, so no area:update would come to refresh it.
  socket.on('res:ok', (d) => {
    for (const area of AREAS) {
      const at = tabIndex(area);
      if (area.tables[at]?.resource === d?.resource && isVisible(area.key)) requestGroup(area, at, true);
    }
  });

  socket.on('area:update', (d) => {
    if (!d || !d.area) return;
    const area = AREAS.find((a) => a.key === d.area);
    if (!area) return;
    latest[d.area] = d;
    if (isVisible(d.area)) {
      render(area);
      // The summary moved, so the open group may have too.
      requestGroup(area, tabIndex(area), true);
    }
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
