/**
 * The Routers page — the fleet dashboard.
 *
 * Summary cards, a search, and three views of the same rows: a card grid, a
 * sortable list, and a map. The CRUD table for adding and editing routers is NOT
 * here; it lives on the Settings page (`page-settings` in the live index.html
 * holds `rtrAddBtn`/`rtrTable`/`rtrTbody`).
 *
 * ── WHAT IS PORTED SO FAR ──────────────────────────────────────────────────
 *
 * The summary, the search and the GRID. `_rtrView` starts at 'comfortable', so
 * the grid is what a default page shows and what `tools/live-renderer.js routers`
 * currently compares. The list and map views are not ported yet and say so
 * loudly rather than falling back to the grid — a view that silently rendered
 * the wrong one would be a divergence the DOM gate cannot see, because the gate
 * only ever drives the default view.
 *
 * The page is deliberately absent from `web/build.mjs` PAGES and `main.ts`
 * PORTED until all three views and the socket wiring land.
 */

import { esc, el } from '../dom';

/** One row of the `routers:stats` payload. Absent is null, never zero. */
export interface RouterStatsRow {
  id: string;
  label: string;
  host: string;
  isActive: boolean;
  connected: boolean;
  /**
   * Whether either pool has actually LOOKED at this router yet.
   *
   * `connected` is a bool and cannot express "not asked". Rendering its zero
   * value as a red "Offline" is a claim the server never made, and on first
   * open of this page that was every device but the selected one. Treat
   * `!known` as a third state everywhere `connected` is displayed.
   */
  known: boolean;
  lastError: string | null;
  openAlerts: number;
  cpu: number | null;
  uptime: string | null;
  memPct: number | null;
  hddPct: number | null;
  version: string | null;
  boardName: string | null;
  arch: string | null;
  serial: string | null;
  licenseLevel: string | null;
  rxMbps: number | null;
  txMbps: number | null;
  clients: number | null;
  siteId: string | null;
  siteName: string | null;
  // #117: a device may belong to SEVERAL sites. `siteId`/`siteName` above are
  // the server's backward-compatible mirrors of the first entry, kept because
  // the payload still sends them.
  //
  // THE TWO ARRAYS CAN DIFFER IN LENGTH: the server drops a name it cannot
  // resolve but keeps the id, so nothing may zip them.
  siteIds?: string[];
  siteNames?: string[];
  geo: { lat: number; lon: number; source: string; label: string;
         accuracyKm?: number | null; wanIp?: string } | null;
}

type View = 'comfortable' | 'compact' | 'list' | 'map';

let rtrView: View = 'comfortable';
let lastRtrRows: RouterStatsRow[] = [];

/**
 * The sortable columns.
 *
 * `str` compares as TEXT. Everything else sorts numerically with NULL LAST
 * however the column is pointing — an unreachable router has no CPU reading, and
 * burying those at the bottom is more useful than treating them as zero.
 */
const RTL_COLS: Record<string, { str?: boolean }> = {
  connected: {}, label: { str: true }, host: { str: true },
  boardName: { str: true }, version: { str: true },
  openAlerts: {}, cpu: {}, memPct: {}, hddPct: {}, clients: {},
  rxMbps: {}, txMbps: {}, uptime: { str: true },
};

let rtlSort: { key: string; dir: number } = { key: 'label', dir: 1 };

/** Exposed so the gate can drive a column; the live page keeps it in a var. */
export function setSort(key: string, dir: number): void { rtlSort = { key, dir }; }

/**
 * Click a column header.
 *
 * TEXT STARTS ASCENDING (A first); NUMBERS START DESCENDING, since the
 * interesting router is the one with the most of something. Clicking the column
 * already sorted reverses it instead of resetting the direction.
 *
 * Re-renders the LIST directly from the rows already held rather than going
 * through `renderRoutersStats`, exactly as the original does — so a sort does
 * not re-run the search or redraw the summary.
 */
export function sortBy(key: string): void {
  if (!RTL_COLS[key]) return;
  if (rtlSort.key === key) rtlSort.dir *= -1;
  else rtlSort = { key, dir: RTL_COLS[key].str ? 1 : -1 };
  renderRoutersList(lastRtrRows);
}

/** Exposed for the view switcher and for tests; the live page keeps it in a var. */
export function setView(v: View): void { rtrView = v; }
export function getView(): View { return rtrView; }

/**
 * The four fleet totals above the router grid.
 *
 * Counted from the same rows the cards below are drawn from, so the totals
 * cannot disagree with what is on screen — and they inherit the RBAC filter the
 * server already applied rather than claiming a fleet size the viewer cannot
 * see. Alerting is NOT part of the online/offline split: it counts routers with
 * an unresolved alert, which a reachable router can perfectly well have.
 */
/**
 * A device's site membership, the port of `_rtrSiteIds`.
 *
 * The ARRAY WINS OUTRIGHT when present, even when empty — an explicit empty
 * `siteIds` means "no sites", not "no answer", and falling through to the
 * singular there would resurrect a membership just cleared.
 */
export function siteIdsOf(r: { siteIds?: string[]; siteId?: string | null }): string[] {
  if (Array.isArray(r.siteIds)) return r.siteIds;
  return r.siteId ? [r.siteId] : [];
}

/** The display names for those sites, the port of `_rtrSiteNames`. */
export function siteNamesOf(r: { siteNames?: string[]; siteName?: string | null }): string[] {
  if (Array.isArray(r.siteNames)) return r.siteNames;
  return r.siteName ? [r.siteName] : [];
}

/**
 * The sentinel for "no site at all".
 *
 * A LEADING SPACE, which cannot collide with a real site id — those are
 * `/^[A-Za-z0-9_-]{1,64}$/` — so it needs no separate flag beside the value.
 */
export const RTR_UNASSIGNED = ' unassigned';

/** The site filter's current value. */
export function rtrSiteFilter(): string {
  const el$ = el<HTMLSelectElement>('routersSiteFilter');
  return el$ ? el$.value : '';
}

/**
 * Repopulate the site dropdown from the rows, preserving the selection.
 *
 * Same shape as the interface-type filter: rebuild, restore the value only if it
 * still exists, and mark the control active when it is narrowing.
 *
 * ── IT PAIRS IDS WITH NAMES BY INDEX, AND THAT IS THE LIVE BEHAVIOUR ────────
 *
 * `nm[i] || id` assumes the two arrays line up, and they can fail to: the server
 * sends every id but only the names it could RESOLVE
 * (`.filter(Boolean)`), so a device listing a DELETED site shifts every name
 * after it onto the wrong id.
 *
 * Reproduced rather than corrected. Fixing it here would make this port's
 * dropdown disagree with the live one, which is the line this project does not
 * cross — the defect is reported in `../MikroDash/ToDo.md` §1 with a worked
 * example and a suggested fix at the source.
 */
export function syncRoutersSiteFilter(rows: RouterStatsRow[] | null): void {
  const sel = el<HTMLSelectElement>('routersSiteFilter');
  if (!sel) return;

  const names: Record<string, string> = {};
  let anyLoose = false;
  (rows || []).forEach((r) => {
    const ids = siteIdsOf(r), nm = siteNamesOf(r);
    if (!ids.length) { anyLoose = true; return; }
    // ── ZIPPED BY INDEX, WHICH IS ONLY SAFE BECAUSE THE SERVER PADS ────────
    //
    // One name per id, with '' for a site it could not resolve, so `nm[i]`
    // always belongs to `ids[i]`. `|| id` is what takes the blank out at the
    // point of display — a device still listing a DELETED site shows the raw id
    // rather than a name belonging to somebody else.
    //
    // The server used to DROP unresolvable names instead. That removed an
    // element from the middle of `nm` while leaving `ids` intact, so every name
    // after the first dangling membership attached to the wrong site: a live
    // site appeared under its raw id while the dead one wore a real site's name,
    // and picking it filtered to the deleted site. Filed as
    // ../MikroDash/ToDo.md §1 and fixed on both sides (e76962d).
    //
    // So do not "fix" this zip, and do not reintroduce a filter upstream of it.
    // The two are a matched pair: padding at the source, blanking at the sink.
    ids.forEach((id, i) => { if (!names[id]) names[id] = nm[i] || id; });
  });

  const ids = Object.keys(names).sort((a, b) => String(names[a]).localeCompare(String(names[b])));
  const html = '<option value="">All Sites</option>' +
    ids.map((id) => '<option value="' + esc(id) + '">' + esc(names[id]!) + '</option>').join('') +
    // Only offered when such devices exist, so a fully assigned fleet keeps a
    // clean list.
    (anyLoose ? '<option value="' + RTR_UNASSIGNED + '">Unassigned</option>' : '');

  if (sel.innerHTML !== html) {
    const keep = sel.value;
    sel.innerHTML = html;
    // Restoring an option that no longer exists would silently reset the filter
    // to All Sites while the list still looked filtered.
    sel.value = keep === RTR_UNASSIGNED ? (anyLoose ? keep : '') : (names[keep] ? keep : '');
  }
  sel.classList.toggle('active', !!sel.value);
}

export function renderRoutersSummary(rows: RouterStatsRow[] | null): void {
  const total$ = el('rsTotal');
  const online$ = el('rsOnline');
  const offline$ = el('rsOffline');
  const alerting$ = el('rsAlerting');
  if (!total$ || !online$ || !offline$ || !alerting$) return;

  const total = rows ? rows.length : 0;
  // OFFLINE IS COUNTED, NOT DERIVED. It used to be `total - online`, which
  // silently classified every not-yet-checked router as down: open the page and
  // the Offline tile read the size of the fleet for as long as the pool took to
  // dial. Counting both means the two can legitimately sum to less than Total
  // while the first sweep is still running, which is the honest picture.
  let online = 0, offline = 0, alerting = 0;
  (rows || []).forEach((r) => {
    if (r.connected) online++;
    else if (r.known) offline++;
    if (r.openAlerts > 0) alerting++;
  });

  total$.textContent = String(total);
  online$.textContent = String(online);
  offline$.textContent = String(offline);
  alerting$.textContent = String(alerting);

  // SITES THE FLEET IS SPREAD ACROSS, not sites that exist (#117). A device in
  // two sites contributes to both, which is the point of the number: it answers
  // "how many places am I looking after", and it always agrees with what the
  // filter beside it can usefully select.
  //
  // Guarded separately, as the live renderer guards it: the element arrived with
  // #117 and an older extracted markup would not have it.
  const sites$ = el('rsSites');
  if (sites$) {
    const seen = new Set<string>();
    (rows || []).forEach((r) => siteIdsOf(r).forEach((id) => seen.add(id)));
    sites$.textContent = String(seen.size);
  }

  // Colour only when there is something to say: a red zero reads as a problem.
  offline$.style.color = total - online > 0 ? 'var(--accent-red, #f87171)' : '';
  alerting$.style.color = alerting > 0 ? 'var(--accent-amber, #f59f00)' : '';
}

/** The search box's current query, trimmed and lower-cased. */
export function rtrQuery(): string {
  const box = el<HTMLInputElement>('routersSearch');
  return box ? box.value.trim().toLowerCase() : '';
}

/**
 * Does a row match the query?
 *
 * EVERY TERM MUST MATCH, and three of them are keywords rather than text:
 * `online`, `offline` and `alerting` filter on state, so "offline mikrotik"
 * means both. The haystack is label, host, board and version — deliberately not
 * the serial, which the live version also omits.
 */
export function rtrMatches(r: RouterStatsRow, q: string): boolean {
  if (!q) return true;
  const hay = [r.label, r.host, r.boardName, r.version].join(' ').toLowerCase();
  return q.split(/\s+/).every((term) => {
    if (term === 'online') return !!r.connected;
    // Unknown is not offline: searching `offline` must not list every router
    // the first sweep has yet to reach.
    if (term === 'offline') return r.known && !r.connected;
    if (term === 'alerting') return r.openAlerts > 0;
    return hay.indexOf(term) !== -1;
  });
}

/**
 * The grid's uptime rule, which is NOT `parseUptime` from ../dom.
 *
 * Three differences, every one of them visible:
 *   - SOURCE ORDER is preserved (`match(/\d+[wdhm]/g)`), where parseUptime
 *     forces w, d, h, m.
 *   - A ZERO COMPONENT SURVIVES: "0m" is kept here and dropped there.
 *   - THE FALLBACK IS ESCAPED here and is not there.
 * Reusing the shared helper would have been a silent divergence on a value the
 * router itself supplies.
 */
function gridUptime(raw: string | null): string {
  const parts = raw ? raw.match(/\d+[wdhm]/g) : null;
  if (parts && parts.length) return parts.join(' ');
  return raw ? esc(raw) : '—';
}

/**
 * One usage bar, or an em dash.
 *
 * The thresholds are compared against a possibly-null value, exactly as the
 * original does — `null > 90` is false in JavaScript, so an absent reading takes
 * the base colour. It never reaches the DOM, because the bar is only drawn when
 * the value is present, but computing it the same way keeps the two readable
 * side by side.
 */
function usageBar(label: string, pct: number | null, colour: string, mb: string): string {
  if (pct == null) {
    return '<div class="text-muted ' + mb + '" style="font-size:.75rem">' + label + ' —</div>';
  }
  return '<div class="d-flex align-items-center ' + mb + '">'
    + '<span class="me-2 text-muted" style="width:3rem;font-size:.75rem">' + label + '</span>'
    + '<div class="progress flex-grow-1" style="height:6px">'
    + '<div class="progress-bar" style="width:' + pct + '%;background:' + colour + '"></div></div>'
    + '<span class="ms-2 text-muted" style="font-size:.75rem;width:2.5rem;text-align:right">'
    + pct + '%</span></div>';
}

/** The card grid. */
function renderGrid(rows: RouterStatsRow[], q: string): void {
  const grid = el('routers-grid');
  if (!grid) return;
  if (!rows.length) {
    grid.innerHTML = '<div class="col-12 text-muted text-center py-4">'
      + (q ? 'No routers match that search.' : 'No routers configured.') + '</div>';
    return;
  }

  let html = '';
  rows.forEach((r) => {
    const cpuColour = (r.cpu as number) > 90 ? '#f87171' : (r.cpu as number) > 75 ? '#f59f00' : '#38bdf8';
    const memColour = (r.memPct as number) > 90 ? '#f87171' : (r.memPct as number) > 75 ? '#f59f00' : '#34d399';
    const hddColour = (r.hddPct as number) > 90 ? '#f87171' : (r.hddPct as number) > 75 ? '#f59f00' : '#fb923c';

    const cpuBar = usageBar('CPU', r.cpu, cpuColour, 'mb-1');
    const memBar = usageBar('RAM', r.memPct, memColour, 'mb-1');
    const hddBar = usageBar('Disk', r.hddPct, hddColour, 'mb-2');

    const uptime = gridUptime(r.uptime);
    const rx = r.rxMbps != null
      ? '<span style="color:var(--accent-rx)">&#8595; ' + r.rxMbps.toFixed(2) + ' Mbps</span>' : '—';
    const tx = r.txMbps != null
      ? '<span style="color:var(--accent-tx)">&#8593; ' + r.txMbps.toFixed(2) + ' Mbps</span>' : '—';
    const clients = r.clients != null ? String(r.clients) : '—';

    let footerPills = '';
    const mr = 'margin-right:.3rem';
    if (r.boardName) footerPills += '<span style="display:inline-flex;align-items:center;padding:.1rem .5rem;border-radius:20px;font-size:.7rem;background:rgba(129,140,248,.12);border:1px solid rgba(129,140,248,.3);' + mr + '">' + esc(r.boardName) + '</span>';
    if (r.version) footerPills += '<span style="display:inline-flex;align-items:center;padding:.1rem .5rem;border-radius:20px;font-size:.7rem;background:rgba(56,189,248,.08);border:1px solid rgba(56,189,248,.2);' + mr + '">ROS ' + esc(r.version) + '</span>';
    if (r.arch) footerPills += '<span style="display:inline-flex;align-items:center;padding:.1rem .5rem;border-radius:20px;font-size:.7rem;background:rgba(139,92,246,.1);border:1px solid rgba(139,92,246,.25);' + mr + '">' + esc(r.arch) + '</span>';
    if (r.serial) footerPills += '<span style="display:inline-flex;align-items:center;padding:.1rem .5rem;border-radius:20px;font-size:.7rem;background:rgba(245,158,11,.1);border:1px solid rgba(245,158,11,.25);' + mr + '">SN: ' + esc(r.serial) + '</span>';
    if (r.licenseLevel) footerPills += '<span style="display:inline-flex;align-items:center;padding:.1rem .5rem;border-radius:20px;font-size:.7rem;background:rgba(52,211,153,.1);border:1px solid rgba(52,211,153,.25)">L' + esc(r.licenseLevel) + '</span>';
    const footer = footerPills ? '<div class="mt-2">' + footerPills + '</div>' : '';

    const hostSub = r.host && r.host !== r.label
      ? '<div style="font-size:.72rem;margin-top:.1rem;color:#ec4899">' + esc(r.host) + '</div>' : '';

    // Explain an offline card rather than leaving the user to read container
    // logs. The server sends this already sanitized; esc() it like any other.
    const offlineWhy = !r.connected && r.lastError
      ? '<div style="font-size:.72rem;line-height:1.35;color:#d63939;background:rgba(214,57,57,.08);'
        + 'border:1px solid rgba(214,57,57,.22);border-radius:6px;padding:.35rem .55rem;margin-bottom:.75rem">'
        + esc(r.lastError) + '</div>' : '';

    const activeBadge = r.isActive
      ? '<span class="badge badge-outline text-blue ms-2">active</span>' : '';

    // Compact fits four across where Comfortable fits three — the same cards,
    // more of them in view.
    // ── AND A TIER ABOVE `xl`, WHICH IS ONLY 1200px ───────────────────────
    //
    // Removing the page's `container-xl` cap (issue #122) let the grid have the
    // whole window, but the columns stopped at `xl` — so a 2500px screen still
    // drew three cards, each about 800px wide and mostly empty. Full width and
    // responsive are not the same thing, and the reporter asked for the second.
    //
    // Tabler carries `xxl` (>=1400px) and nothing used it. Comfortable goes to
    // four per row and compact to six, which is where a card stops gaining
    // anything from the extra width.
    html += (rtrView === 'compact'
      ? '<div class="col-md-4 col-xl-3 col-xxl-2">'
      : '<div class="col-md-6 col-xl-4 col-xxl-3">')
      // h-100 so cards in a row match height. Without it a card is only as tall
      // as its content, and one whose identity pills wrap to a second row sat
      // visibly taller than its neighbours — measured at 297px against 275px.
      + '<div class="card h-100">'
      + '<div class="card-header" style="align-items:flex-start">'
      + '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="' + (!r.known ? '#6c7a91' : r.connected ? '#2fb344' : '#d63939') + '" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="me-2" style="flex-shrink:0"><path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/><path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><line x1="12" y1="20" x2="12.01" y2="20"/></svg>'
      + '<div class="me-auto">'
      + '<div class="d-flex align-items-center"><strong class="card-title mb-0 me-1" style="color:inherit">' + esc(r.label) + '</strong>' + activeBadge + '</div>'
      + hostSub
      + '</div>'
      // THREE STATES, not two. `!known` means no pool has reached this router
      // yet, and saying "Offline" about it in red was alarming and wrong.
      + '<span class="badge ms-2 ' + (!r.known ? 'bg-secondary-lt' : r.connected ? 'bg-green-lt' : 'bg-red-lt') + '">'
      + (!r.known ? 'Checking…' : r.connected ? 'Online' : 'Offline') + '</span>'
      + '</div>'
      + '<div class="card-body">'
      + offlineWhy
      + cpuBar + memBar + hddBar
      + '<div class="row g-2 text-center">'
      + '<div class="col-6"><div class="text-muted" style="font-size:.72rem">Uptime</div><div style="font-size:.9rem;font-weight:500;letter-spacing:.02em">' + uptime + '</div></div>'
      + '<div class="col-6"><div class="text-muted" style="font-size:.72rem">Clients</div><div style="font-size:.9rem;font-weight:500;color:#a855f7">' + clients + '</div></div>'
      + '<div class="col-6"><div class="text-muted" style="font-size:.72rem">WAN Rx</div><div style="font-size:.82rem;font-weight:500">' + rx + '</div></div>'
      + '<div class="col-6"><div class="text-muted" style="font-size:.72rem">WAN Tx</div><div style="font-size:.82rem;font-weight:500">' + tx + '</div></div>'
      + '</div>'
      + footer
      + '</div>'
      + '</div>'
      + '</div>';
  });
  grid.innerHTML = html;
}

/**
 * One usage cell in the list.
 *
 * THE WIDTH IS CLAMPED AND THE NUMBER IS NOT, which is the original's behaviour
 * and worth keeping: a router reporting 150% shows a full bar and still says
 * "150%", so the reading is visible rather than quietly trimmed to something
 * plausible.
 */
function rtlBar(pct: number | null, colour: string): string {
  if (pct == null) return '<span class="text-muted">—</span>';
  return '<span class="rtl-bar"><i style="width:' + Math.max(0, Math.min(100, pct))
    + '%;background:' + colour + '"></i></span>' + pct + '%';
}

/**
 * The list view.
 *
 * ── ITS UPTIME RULE IS A THIRD ONE ─────────────────────────────────────────
 *
 * `(match || []).join(' ') || raw` — an empty join is the empty string, which is
 * falsy, so an uptime with no w/d/h/m component falls back to the raw text. It
 * agrees with the grid on "45s" and disagrees on ABSENCE: the grid writes a bare
 * em dash, this writes `<span class="text-muted">—</span>`. Different markup for
 * the same idea, in the same file, and both are reproduced rather than
 * harmonised.
 */
function renderRoutersList(rows: RouterStatsRow[]): void {
  const body = el('routersListBody');
  if (!body) return;
  // Already filtered by the caller; sorting is all that is left to do here.
  const list = (rows || []).slice();

  const col = RTL_COLS[rtlSort.key] || {};
  list.sort((a, b) => {
    const av = (a as unknown as Record<string, unknown>)[rtlSort.key];
    const bv = (b as unknown as Record<string, unknown>)[rtlSort.key];
    if (col.str) {
      return String(av == null ? '' : av).localeCompare(
        String(bv == null ? '' : bv), undefined,
        { numeric: true, sensitivity: 'base' }) * rtlSort.dir;
    }
    // NULL LAST REGARDLESS OF DIRECTION: "no reading" is not a low reading.
    if (av == null && bv == null) return 0;
    if (av == null) return 1;
    if (bv == null) return -1;
    return ((av as number) - (bv as number)) * rtlSort.dir;
  });

  if (!list.length) {
    body.innerHTML = '<tr><td colspan="13" class="text-muted text-center py-3">'
      + (rtrQuery() ? 'No routers match that search.' : 'No routers configured.') + '</td></tr>';
    refreshHeaders();
    return;
  }

  const dash = '<span class="text-muted">—</span>';
  body.innerHTML = list.map((r) => {
    const cpuC = (r.cpu as number) > 90 ? '#f87171' : (r.cpu as number) > 75 ? '#f59f00' : '#38bdf8';
    const memC = (r.memPct as number) > 90 ? '#f87171' : (r.memPct as number) > 75 ? '#f59f00' : '#34d399';
    const hddC = (r.hddPct as number) > 90 ? '#f87171' : (r.hddPct as number) > 75 ? '#f59f00' : '#fb923c';
    const up = r.uptime ? ((r.uptime.match(/\d+[wdhm]/g) || []).join(' ') || r.uptime) : null;
    const alerts = r.openAlerts > 0
      ? '<span style="color:var(--accent-amber,#f59f00);font-weight:600">' + r.openAlerts + '</span>'
      : dash;
    // `rtl-offline` DIMS THE ROW, so an unchecked router must not carry it —
    // see the three states on the card badge above.
    return '<tr class="rtl-row' + (r.connected || !r.known ? '' : ' rtl-offline') + '" data-router-id="' + esc(r.id) + '">'
      + '<td><span class="rtl-dot" style="background:' + (!r.known ? '#6c7a91' : r.connected ? '#34d399' : '#f87171') + '" title="'
        + (!r.known ? 'Checking…' : r.connected ? 'Online' : 'Offline') + '"></span></td>'
      + '<td>' + esc(r.label) + (r.isActive ? ' <span class="badge badge-outline text-blue">active</span>' : '') + '</td>'
      + '<td class="text-muted">' + esc(r.host || '') + '</td>'
      + '<td>' + (r.boardName ? esc(r.boardName) : dash) + '</td>'
      + '<td>' + (r.version ? esc(r.version) : dash) + '</td>'
      + '<td class="rtl-num">' + alerts + '</td>'
      + '<td class="rtl-num">' + rtlBar(r.cpu, cpuC) + '</td>'
      + '<td class="rtl-num">' + rtlBar(r.memPct, memC) + '</td>'
      + '<td class="rtl-num">' + rtlBar(r.hddPct, hddC) + '</td>'
      + '<td class="rtl-num">' + (r.clients != null ? r.clients : dash) + '</td>'
      + '<td class="rtl-num">' + (r.rxMbps != null ? r.rxMbps.toFixed(2) : dash) + '</td>'
      + '<td class="rtl-num">' + (r.txMbps != null ? r.txMbps.toFixed(2) : dash) + '</td>'
      + '<td class="text-muted">' + (up ? esc(up) : dash) + '</td>'
      + '</tr>';
  }).join('');
  refreshHeaders();
}

/** Mark the sorted column in the header. */
function refreshHeaders(): void {
  document.querySelectorAll('.routers-list th[data-sort]').forEach((th) => {
    const e = th as HTMLElement;
    e.className = e.className.replace(/\s*sort-(asc|desc)/g, '');
    if (e.dataset.sort === rtlSort.key) {
      e.className += rtlSort.dir === 1 ? ' sort-asc' : ' sort-desc';
    }
  });
}

// ── the map's string and arithmetic half ────────────────────────────────────
//
// The map is two things in one IIFE. Building and moving the SVG — markers,
// zoom, drag, the popover's position — needs a browser and is gated by
// The live-renderer tool against a running stack. Deciding WHERE a marker
// goes, WHICH routers share a place, and WHAT the popover and tray say is
// arithmetic and string building, gated by the routers-grid check
// against the live functions themselves.
//
// This is that second half. The first is not ported yet.

/** The map's canvas, in the units the projection produces. */
const MAP_W = 1000, MAP_H = 500;
/** Co-location bucket size, about two marker diameters across. */
const MAP_GRID = 6;

/** Equirectangular: longitude straight to x, latitude straight to y. */
export function project(lon: number, lat: number): [number, number] {
  return [(lon + 180) * (MAP_W / 360), (90 - lat) * (MAP_H / 180)];
}

export interface MapGroup { key: string; x: number; y: number; routers: RouterStatsRow[] }

/**
 * Group routers that resolve to the same place.
 *
 * Several routers behind one WAN address share a coordinate exactly. An earlier
 * live version fanned them onto a small ring so each stayed clickable; on a real
 * fleet that read as three separate SITES rather than one place with three
 * routers in it — the opposite of the truth. They collapse into a single marker
 * carrying the count, and the popover lists what is inside.
 *
 * THE MEMBER SORT IS THE ORIGINAL'S, comparator quirk included: it returns -1 or
 * 1 and never 0, so equal labels are ordered by whatever the engine does with an
 * inconsistent comparator. Reproduced rather than corrected — "stable order so a
 * popover list does not reshuffle every two seconds" is the intent, and changing
 * the comparator changes which order that is.
 */
export function layout(located: RouterStatsRow[]): MapGroup[] {
  const buckets: Record<string, MapGroup> = {};
  const order: string[] = [];
  located.forEach((r) => {
    const geo = r.geo as { lat: number; lon: number };
    const p = project(geo.lon, geo.lat);
    const k = Math.round(p[0] / MAP_GRID) + ':' + Math.round(p[1] / MAP_GRID);
    if (!buckets[k]) { buckets[k] = { key: k, x: 0, y: 0, routers: [] }; order.push(k); }
    buckets[k].routers.push(r);
    buckets[k].x += p[0];
    buckets[k].y += p[1];
  });
  return order.map((k) => {
    const b = buckets[k] as MapGroup;
    b.x /= b.routers.length;
    b.y /= b.routers.length;
    b.routers.sort((a, c) => ((a.label || '') < (c.label || '') ? -1 : 1));
    return b;
  });
}

/** Whether this viewer may manage a router, as the live popover asks. */
function canManage(id: string): boolean {
  const caps = (globalThis as unknown as { _caps?: { routers?: { manageable?: string[] } } })._caps;
  return !!(caps && caps.routers && (caps.routers.manageable || []).indexOf(id) !== -1);
}

/** One router's popover. */
/**
 * The status dot's colour, in the map's CSS variables.
 *
 * THREE STATES. Grey is "no pool has reached this router yet" — see
 * `RouterStatsRow.known`. Both popovers used a red/green ternary on `connected`
 * alone, so opening the map before the overview pool had dialled painted the
 * whole fleet red.
 */
export function dotColour(r: RouterStatsRow): string {
  if (!r.known) return 'var(--accent-muted,#6c7a91)';
  return r.connected ? 'var(--accent-green,#2fb344)' : 'var(--accent-red,#f87171)';
}

export function popHtml(r: RouterStatsRow): string {
  const g = r.geo || ({} as NonNullable<RouterStatsRow['geo']>);
  const up = r.uptime ? String(r.uptime) : '—';
  // Where the position came from, stated plainly and without alarm. The map
  // itself no longer distinguishes them.
  const from = g.source === 'manual' ? 'set here'
    : g.source === 'site' ? 'from its site'
    : (g.wanIp ? 'from ' + esc(g.wanIp) : 'from its WAN address');
  const loc = esc(g.label || 'Unknown')
    + ' <span class="text-muted">(' + from + ')</span>';
  return '<div class="rmp-name"><span class="rtl-dot" style="background:' + dotColour(r)
    + '"></span>' + esc(r.label) + '</div>'
    + '<div class="rmp-grid">'
    + '<span>Host</span><b>' + esc(r.host) + '</b>'
    + '<span>CPU</span><b>' + (r.cpu == null ? '—' : r.cpu + '%') + '</b>'
    + '<span>Uptime</span><b>' + esc(up) + '</b>'
    + '<span>WAN</span><b>&#8595;' + (r.rxMbps == null ? '—' : r.rxMbps)
    + ' &#8593;' + (r.txMbps == null ? '—' : r.txMbps) + ' Mbps</b>'
    + (r.openAlerts ? '<span>Alerts</span><b style="color:var(--accent-amber,#f59f00)">' + r.openAlerts + '</b>' : '')
    + '</div>'
    + '<div class="rmp-loc">' + loc + '</div>'
    + (canManage(r.id) ? '<button type="button" data-open-router="' + esc(r.id) + '">Open settings</button>' : '');
}

/**
 * A cluster's popover.
 *
 * A group of one shows the router. A group of several shows what is IN it — the
 * count on the marker says how many, this says which, with a way into each one's
 * settings. Without it a cluster would be a dead end.
 */
export function groupPopHtml(g: MapGroup): string {
  const first = g.routers[0] as RouterStatsRow;
  if (g.routers.length === 1) return popHtml(first);
  const place = (first.geo && first.geo.label) || 'this location';
  // KNOWN and not connected. Counting `!connected` made a cluster announce
  // "3 offline" the instant the map opened, before anything had been asked.
  const down = g.routers.filter((r) => r.known && !r.connected).length;
  return '<div class="rmp-name">' + g.routers.length + ' routers</div>'
    + '<div class="rmp-loc" style="margin-top:.2rem;padding-top:0;border-top:0">'
    + esc(place)
    + (down ? ' <span style="color:var(--accent-err,#f87171)">— ' + down + ' offline</span>' : '')
    + '</div>'
    + '<div class="rmp-list">' + g.routers.map((r) => {
        const can = canManage(r.id);
        return '<div class="rmp-row"' + (can ? ' data-open-router="' + esc(r.id) + '"' : '')
          + '><span class="rtl-dot" style="background:' + dotColour(r)
          + '"></span><span class="rmp-rl">' + esc(r.label) + '</span>'
          + '<span class="rmp-rh">' + esc(r.host) + '</span></div>';
      }).join('') + '</div>';
}

/**
 * The tray of routers the map cannot place.
 *
 * It explains ITSELF rather than leaving an empty map to be interpreted: a
 * private or unroutable WAN address cannot be geolocated, and the fix is a town
 * on the router or on its site.
 */
export function renderTray(unlocated: RouterStatsRow[]): void {
  const tray = el('rtrMapTray');
  if (!tray) return;
  if (!unlocated.length) { tray.hidden = true; tray.innerHTML = ''; return; }
  tray.hidden = false;
  tray.innerHTML = '<span class="rmt-label">No location ('
    + unlocated.length + '):</span>'
    + unlocated.map((r) => '<span class="rmt-pill" data-open-router="' + esc(r.id) + '" title="'
        + esc(r.host) + '"><span class="rtl-dot" style="background:'
        + (r.connected ? 'var(--accent-green,#2fb344)' : 'var(--accent-red,#f87171)')
        + '"></span>' + esc(r.label) + '</span>').join('')
    + '<span class="rmt-label" style="flex-basis:100%;margin-top:.25rem">'
    + 'Their WAN address is private or unroutable, so it cannot be geolocated. '
    + 'Set a town in the router\u2019s settings, or give its site one.</span>';
}

/**
 * The entry point, and the one the `routers:stats` handler calls.
 *
 * THE SUMMARY COUNTS THE FLEET, NOT THE SEARCH. Totals that moved as you typed
 * would stop answering "how many routers do I have".
 */
export function renderRoutersStats(rows: RouterStatsRow[] | null): void {
  if (rows) lastRtrRows = rows;
  const all = rows || [];

  renderRoutersSummary(all);

  // Rebuilt from the ROWS, not from a site cache: these rows are RBAC-filtered
  // per socket, so the dropdown can only ever offer sites this viewer actually
  // has a device in. The live comment says the same: the site cache it could
  // have used instead comes from an ungated endpoint and would list the whole
  // install. (That cache's name is deliberately not written out here —
  // `announcement-audit` text-scans for `window.<name>` reads and cannot tell a
  // comment from code, so naming it reads as a producer this port dropped.)
  syncRoutersSiteFilter(all);

  const q = rtrQuery();
  const visible = q ? all.filter((r) => rtrMatches(r, q)) : all;

  const shown = el('routersShown');
  if (shown) {
    shown.textContent = visible.length === all.length
      ? '' : visible.length + ' of ' + all.length + ' shown';
  }

  // The rest draws whatever survived the search.
  if (rtrView === 'list') { renderRoutersList(visible); return; }
  if (rtrView === 'map') {
    // THE FULL ROW SET, not `visible`. The live `apply` takes what the search
    // left AND splits it itself — the tray lists routers with no location, and
    // those must still be listed when a search is narrowing the map.
    mapApply(visible);
    return;
  }
  renderGrid(visible, q);
}

/**
 * The SVG half's `apply`, registered at mount.
 *
 * The same one-way rule as `onAfterTransform`: `routers-map.ts` imports this
 * module, so this module cannot import it back. Until it registers, drawing the
 * map is a no-op rather than a throw — the view is only reachable once the page
 * has mounted, and a throw here would take `renderRoutersStats` with it.
 */
let mapApply: (rows: RouterStatsRow[]) => void = () => {};

export function onMapApply(fn: (rows: RouterStatsRow[]) => void): void {
  mapApply = fn;
}

/** The rows the page last received, for a re-render after a search keystroke. */
export function lastRows(): RouterStatsRow[] { return lastRtrRows; }

// ── the map's geometry ──────────────────────────────────────────────────────
//
// The zoom and pan arithmetic, ported ahead of the SVG construction it serves,
// because this is where a subtle error hides and it can be verified NOW: the
// transform it produces is an observable string, so the routers-grid check
// compares it against the live function's. The marker building, the drag and the
// popover positioning are not ported yet and can only be checked in a browser.

const MAP_MIN_SCALE = 1, MAP_MAX_SCALE = 8;

/** The current view. Absolute, not accumulated — `fitToMarkers` sets all three. */
const mapView = { scale: 1, tx: 0, ty: 0 };

/**
 * Keep the map inside its frame.
 *
 * The bounds are asymmetric on purpose: translation is clamped to [-(s-1)*w, 0],
 * so at scale 1 the only legal offset is 0 and the map cannot be dragged away
 * from its frame at all.
 */
export function clampTranslate(s: number, x: number, y: number): [number, number] {
  const svg = el('routersMap');
  const w = (svg && (svg as HTMLElement).clientWidth) || 1000;
  const h = (svg && (svg as HTMLElement).clientHeight) || 500;
  const maxX = (s - 1) * w, maxY = (s - 1) * h;
  return [Math.min(0, Math.max(-maxX, x)), Math.min(0, Math.max(-maxY, y))];
}

/**
 * What `applyTransform` must do AFTER writing the transform.
 *
 * The live `applyTransform` calls `resize()` and `positionPop()` inline; both
 * belong to the SVG half, which lives in `routers-map.ts` because it can only be
 * checked in a browser. A hook rather than an import, because the dependency
 * runs the other way — the SVG module imports this one for `clampTranslate` and
 * `fitToMarkers`, and importing back would be a cycle.
 *
 * EMPTY UNTIL THE MAP MOUNTS, which is correct: with no markers drawn there is
 * nothing to resize and no popover to reposition.
 */
const afterTransform: (() => void)[] = [];

export function onAfterTransform(fn: () => void): void {
  afterTransform.push(fn);
}

/** The shared view. Read by the SVG half, written by both. */
export function mapViewState(): { scale: number; tx: number; ty: number } {
  return mapView;
}

export function setMapView(scale: number, tx: number, ty: number): void {
  mapView.scale = scale;
  mapView.tx = tx;
  mapView.ty = ty;
}

export function applyTransform(): void {
  const svg = el('routersMap');
  if (svg) {
    svg.style.transform = 'translate(' + mapView.tx + 'px,' + mapView.ty + 'px) scale('
      + mapView.scale + ')';
  }
  for (const fn of afterTransform) fn();
}

/**
 * Frame every marker.
 *
 * THE PADDING IS IN MAP UNITS, not pixels, so a single marker does not end up
 * filling the card. The scale is the tighter of the two axes, clamped to the
 * zoom range, and the translation centres the bounding box before the same
 * clamp the drag uses is applied to it.
 */
export function fitToMarkers(pts: { x: number; y: number }[]): void {
  if (!pts.length) return;
  const svg = el('routersMap');
  const w = (svg && (svg as HTMLElement).clientWidth) || 1000;
  const h = (svg && (svg as HTMLElement).clientHeight) || 500;
  const ux = w / MAP_W, uy = h / MAP_H;              // px per map unit
  let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  pts.forEach((p) => {
    minX = Math.min(minX, p.x); maxX = Math.max(maxX, p.x);
    minY = Math.min(minY, p.y); maxY = Math.max(maxY, p.y);
  });
  const pad = 45;
  minX -= pad; maxX += pad; minY -= pad; maxY += pad;
  const s = Math.min(MAP_MAX_SCALE, Math.max(MAP_MIN_SCALE,
    Math.min(w / ((maxX - minX) * ux), h / ((maxY - minY) * uy))));
  mapView.scale = s;
  mapView.tx = w / 2 - ((minX + maxX) / 2) * ux * s;
  mapView.ty = h / 2 - ((minY + maxY) / 2) * uy * s;
  const c = clampTranslate(mapView.scale, mapView.tx, mapView.ty);
  mapView.tx = c[0]; mapView.ty = c[1];
  applyTransform();
}

// ── the view switcher and the socket wiring ─────────────────────────────────

const VIEW_KEY = 'mikrodash_routers_view';

/**
 * Show one view and hide the others, then redraw.
 *
 * AN UNKNOWN STORED VALUE FALLS THROUGH TO THE CARD GRID, so a downgrade that no
 * longer knows 'map' degrades rather than showing nothing — the live comment
 * says exactly that, and it is why the tests below feed it rubbish.
 *
 * Re-renders from the rows already held, so switching view is instant rather
 * than waiting out the two-second refresh.
 */
export function applyView(v: string): void {
  // The temporary fallback that rewrote 'map' to 'comfortable' is GONE as of
  // 2026-08-29: the SVG half is ported (`routers-map.ts`), so a stored 'map' now
  // shows the map the way the live app does. That fallback was a knowing
  // divergence and it existed only while the view could not be drawn.
  rtrView = v as View;
  const isList = v === 'list', isMap = v === 'map';
  const grid = el('routers-grid'), wrap = el('routersListWrap'), mapw = el('routersMapWrap');
  if (grid) grid.hidden = isList || isMap;
  if (wrap) wrap.hidden = !isList;
  if (mapw) mapw.hidden = !isMap;
  const sel = el<HTMLSelectElement>('routersView');
  if (sel) sel.value = v;
  renderRoutersStats(lastRtrRows);
}

/**
 * Wire the page up.
 *
 * The three listeners are each delegated or debounced the way the original is,
 * and the reasons are the original's:
 *
 *   - the SORT listener sits on the thead, because the tbody is rebuilt on every
 *     refresh and the headers are not;
 *   - the SEARCH re-renders from rows already in hand, so there is no round trip
 *     and the two-second refresh cannot wipe what was typed;
 *   - the VIEW is persisted, so a reload comes back where you left it.
 *
 * `localStorage` is wrapped in try/catch on both read and write, as the original
 * wraps it: a browser with site data blocked throws on access rather than
 * returning null, and an unreadable preference must not stop the page loading.
 */
export function mountRouters(socket: { on(ev: string, cb: (d: unknown) => void): void }): void {
  // THE SVG HALF IS MOUNTED BY `main.ts`, NOT FROM HERE.
  //
  // A `import('./routers-map')` here worked and was wrong twice over: the
  // dependency runs the other way (that module imports this one), so a static
  // import back would be a cycle — and the dynamic form that avoided the cycle
  // resolved on a later microtask, so the map mounted at an unpredictable time
  // relative to the first `routers:stats`. The routers-grid check is what
  // exposed it, by finishing its run and tearing down its fake `document` before
  // the import's `.then` fired.
  //
  // `main.ts` imports both and mounts both, which is neither cyclic nor
  // deferred.
  socket.on('routers:stats', (rows) => {
    renderRoutersStats(rows as RouterStatsRow[]);
  });

  const head = document.querySelector('.routers-list thead');
  if (head) {
    head.addEventListener('click', (e) => {
      const t = e.target as HTMLElement | null;
      const th = t && t.closest ? t.closest('th[data-sort]') as HTMLElement | null : null;
      if (th && th.dataset.sort) sortBy(th.dataset.sort);
    });
  }

  const search = el<HTMLInputElement>('routersSearch');
  if (search) search.addEventListener('input', () => renderRoutersStats(lastRtrRows));

  const sel = el<HTMLSelectElement>('routersView');
  if (sel) {
    sel.addEventListener('change', () => {
      applyView(sel.value);
      try { localStorage.setItem(VIEW_KEY, sel.value); } catch { /* site data blocked */ }
    });
  }

  let saved = 'comfortable';
  try { saved = localStorage.getItem(VIEW_KEY) || 'comfortable'; } catch { /* site data blocked */ }
  applyView(saved);
}
