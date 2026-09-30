// The topbar router picker.
//
// ── WHY THIS EXISTS AS ITS OWN MODULE ───────────────────────────────────────
//
// The live app ships TWO router switchers and shows exactly one at a time:
//
//   topbar dropdown   `routerSelectWrap`, `class="topbar-mobile-hide"`  DESKTOP
//   sidenav <select>  `navRouterSelect` inside `#navRouterWrap`          MOBILE
//
// `#navRouterWrap` is `display:none` (index.html:84) and becomes `display:flex`
// only inside the mobile media query (:1158), where the same query hides the
// dropdown with `.topbar-mobile-hide{display:none !important}`. `app.js:7779`
// says it in as many words: "Mobile nav keeps the native select."
//
// **This port wired the SELECT and nothing else**, so on a desktop browser it
// rendered the dropdown - the markup is extracted verbatim, so it looked
// live - and clicking it did nothing, while the control that worked was hidden.
// A desktop user could not change routers, on any page. Found 2026-08-25 by
// counting the ids in `shell.html`, which no audit had ever scanned.
//
// A custom popover rather than a native select, and the original says why: each
// row carries the router's live status and the list can be searched. The mobile
// nav keeps its native select deliberately - the OS picker is the better control
// on touch.

import { esc, el, lsGet, lsSet, SITE_UNASSIGNED } from './dom';

export interface DropdownRouter {
  id: string;
  label?: string;
  host?: string;
  disabled?: boolean;
  /**
   * The device's sites (#117: a device may be in SEVERAL, or none).
   *
   * IDS ONLY, and the names are resolved separately through the shared
   * `sitesById` cache. Sending a parallel `siteNames` array alongside is how
   * the Devices page once shifted every name onto the wrong id when one
   * membership pointed at a deleted site: the two arrays were compacted
   * differently and zipped by index.
   *
   * `/api/routers` always discloses this key, `[]` included, so an absent
   * array means the caller built the row itself rather than "unknown".
   */
  siteIds?: string[];
}

/** One site the fleet actually spans, for the chip strip. */
export interface SiteChip {
  id: string;
  name: string;
  count: number;
}

/**
 * One rendered block of the list: a site and the rows under it.
 *
 * `site` is null for the devices in no site at all. That group is LAST and it
 * is never omitted when it has members - a device with no site must not be
 * unreachable from a site-shaped control.
 */
export interface DropdownGroup {
  site: SiteChip | null;
  rows: DropdownRouter[];
}

/** Where the picker remembers the chosen chip. Per viewer, per browser. */
export const DD_SITE_KEY = 'mdRouterDropdownSite';

/**
 * The device's sites, defensively.
 *
 * An absent array is "no sites", matching `siteIdsOf` on the Devices page: the
 * server always sends the key, so falling through to some other field would be
 * inventing an answer.
 */
function sitesOf(r: DropdownRouter): string[] {
  return Array.isArray(r.siteIds) ? r.siteIds.filter((x) => !!x) : [];
}

/** Only surface the search box once the list is long enough to need it. */
export const DD_SEARCH_MIN = 5;

/**
 * The name a row shows.
 *
 * The suffix strip is not cosmetic: a router's label carries a ` · site` tail in
 * some deployments, and the dropdown is narrow. `host` is the fallback and `?`
 * the last resort - a router with neither still gets a row rather than a blank
 * one nobody can click.
 */
export function rtrLabel(r: DropdownRouter): string {
  return (r.label || r.host || '?').replace(/\s*[·•].*$/, '').trim();
}

/**
 * The mobile router select's options. ESCAPED like the dropdown: a label is
 * whatever the operator typed, and the select was built by string concatenation
 * with neither the label nor the id escaped.
 */
export function selectOptionsHtml(routers: readonly { id: string; label?: string; name?: string }[]): string {
  return routers.map((r) =>
    '<option value="' + esc(r.id) + '">' + esc(r.label || r.name || r.id) + '</option>').join('');
}

/**
 * The sites this fleet is spread across, in the order the chips are drawn.
 *
 * ── SITES THE FLEET IS IN, NOT SITES THAT EXIST ────────────────────────────
 *
 * An install can hold a site with no devices in it; a chip for it would filter
 * to an empty list, so it is not offered. This is the Devices page's rule for
 * its own count, and the same sentence explains both.
 *
 * ── A DEVICE IN TWO SITES COUNTS IN BOTH ───────────────────────────────────
 *
 * Because it is genuinely in both, and the counts answer "how many devices
 * would this chip show". They do not sum to the fleet size, and nothing here
 * claims they do - a site is not a partition since #117.
 *
 * ── AN UNRESOLVABLE ID STILL GETS A CHIP, UNDER ITS RAW ID ─────────────────
 *
 * A device can list a site that has been deleted. Dropping it would make that
 * device unreachable from the chips; showing the raw id is what the Devices
 * page does, and it reads as the fault it is rather than as a real place.
 *
 * Disabled routers are excluded throughout: they are not switchable, so a chip
 * whose whole membership is disabled would open an empty list.
 */
export function siteChips(
  routers: readonly DropdownRouter[], names: Readonly<Record<string, string>>,
): SiteChip[] {
  const count: Record<string, number> = {};
  let loose = 0;
  for (const r of routers) {
    if (r.disabled) continue;
    const ids = sitesOf(r);
    if (!ids.length) { loose++; continue; }
    for (const id of ids) count[id] = (count[id] || 0) + 1;
  }
  const out: SiteChip[] = Object.keys(count).map((id) => ({
    id, name: names[id] || id, count: count[id]!,
  }));
  // By NAME, case-insensitively, so the strip reads alphabetically rather than
  // in whatever order the fleet happens to list its devices.
  out.sort((a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  // LAST, always. A device in no site is the one a site-shaped control is most
  // likely to lose, so its chip is never conditional on anything but existing.
  if (loose) out.push({ id: SITE_UNASSIGNED, name: 'No site', count: loose });
  return out;
}

/**
 * The devices one chip shows. `''` is the All chip and filters nothing.
 *
 * A site that has since emptied selects nothing rather than everything - the
 * caller resets the chip to All when it is no longer offered, and this is the
 * honest answer if it somehow does not.
 */
export function routersInSite(
  routers: readonly DropdownRouter[], siteId: string,
): DropdownRouter[] {
  if (!siteId) return routers.slice();
  if (siteId === SITE_UNASSIGNED) return routers.filter((r) => !sitesOf(r).length);
  return routers.filter((r) => sitesOf(r).indexOf(siteId) !== -1);
}

/**
 * The rows arranged into the blocks the list draws.
 *
 * `chips` supplies the ORDER and the names, so the headers and the chip strip
 * cannot disagree about either. A chip with no rows left after the search is
 * dropped: a heading over nothing reads as a site that has gone down.
 *
 * A DEVICE IN TWO SITES APPEARS TWICE, once under each heading. That is not a
 * duplicate row by accident - it is where that device genuinely is, and hiding
 * it from the second site would make the heading lie about its membership.
 */
export function groupRoutersBySite(
  rows: readonly DropdownRouter[], chips: readonly SiteChip[],
): DropdownGroup[] {
  const out: DropdownGroup[] = [];
  for (const c of chips) {
    const inIt = c.id === SITE_UNASSIGNED
      ? rows.filter((r) => !sitesOf(r).length)
      : rows.filter((r) => sitesOf(r).indexOf(c.id) !== -1);
    if (inIt.length) out.push({ site: c.id === SITE_UNASSIGNED ? null : c, rows: inIt });
  }
  // The no-site block carries no SiteChip, so its heading text comes from the
  // renderer. Rebuilt here rather than reusing the chip because a group knows
  // only what it holds.
  const loose = rows.filter((r) => !sitesOf(r).length);
  if (loose.length && !out.some((g) => g.site === null)) {
    out.push({ site: null, rows: loose });
  }
  return out;
}

/**
 * The rows in the order they are DRAWN, which is what the keyboard walks.
 *
 * A device in two sites is in here twice, because the highlight moves over
 * rendered rows and skipping one would make the arrow keys stick.
 */
export function flattenGroups(groups: readonly DropdownGroup[]): DropdownRouter[] {
  const out: DropdownRouter[] = [];
  for (const g of groups) for (const r of g.rows) out.push(r);
  return out;
}

/**
 * The rows a query selects.
 *
 * DISABLED ROUTERS ARE DROPPED FIRST, before the query - a disabled router is
 * not switchable, so matching one would offer a row that does nothing. The
 * query matches label AND host, joined with a space, so typing an address finds
 * a router whose label does not contain it.
 */
export function filterRouters(routers: readonly DropdownRouter[], filter: string): DropdownRouter[] {
  const q = filter.trim().toLowerCase();
  return routers.filter((r) => !r.disabled).filter((r) => {
    if (!q) return true;
    return ((r.label || '') + ' ' + (r.host || '')).toLowerCase().indexOf(q) !== -1;
  });
}

/**
 * The list's markup.
 *
 * `status` is TRISTATE and the three cases are different facts: `true` is up,
 * `false` is down, and absent is "not known yet" - which renders a dot with no
 * modifier rather than an "off" one, so a router nobody has connected to yet is
 * not shown as broken.
 */
export function dropdownHtml(
  groups: readonly DropdownGroup[], activeId: string,
  status: Record<string, boolean | undefined>, hl: number,
): string {
  if (!groups.length || !flattenGroups(groups).length) {
    return '<div class="rtr-dd-empty">No routers match</div>';
  }
  // HEADINGS ONLY WHEN THERE IS SOMETHING TO SEPARATE. One group is either a
  // single-site install or a chip already narrowing to one site, and in both
  // cases a heading over the whole list names what the chip strip or the
  // button already says.
  const heads = groups.length > 1;
  let html = '';
  // ONE COUNTER ACROSS THE GROUPS. `hl` indexes the DRAWN rows, which is what
  // `flattenGroups` produces and what the arrow keys walk - a per-group index
  // would highlight one row per site.
  let i = -1;
  for (const g of groups) {
    if (heads) {
      html += '<div class="rtr-dd-head">'
        + '<span>' + esc(g.site ? g.site.name : 'No site') + '</span>'
        + '<span class="rtr-dd-head-n">' + g.rows.length + '</span>'
        + '</div>';
    }
    for (const r of g.rows) {
      i++;
      const st = status[r.id];
      const dot = st === true ? 'on' : st === false ? 'off' : '';
      const act = r.id === activeId;
      html += '<div class="rtr-dd-item' + (act ? ' active' : '') + (i === hl ? ' hl' : '') + '"'
        + ' role="option" aria-selected="' + (act ? 'true' : 'false') + '" data-rtr="' + esc(r.id) + '">'
        + '<span class="rtr-dd-dot ' + dot + '"></span>'
        + '<span class="rtr-dd-meta"><span class="rtr-dd-name">' + esc(rtrLabel(r)) + '</span>'
        + (r.host ? '<span class="rtr-dd-host">' + esc(r.host) + '</span>' : '') + '</span>'
        + (act ? '<span class="rtr-dd-check">&#10003;</span>' : '')
        + '</div>';
    }
  }
  return html;
}

/**
 * The chip strip.
 *
 * `''` is the All chip. It carries no count: it would be the number of DRAWN
 * rows, not of devices, because a device in two sites is drawn twice - and a
 * number next to "All" that disagrees with the fleet size is worse than none.
 */
export function chipsHtml(chips: readonly SiteChip[], selected: string): string {
  if (chips.length < 2) return '';
  let html = '<button type="button" class="rtr-dd-chip' + (selected ? '' : ' on')
    + '" data-site="">All</button>';
  for (const c of chips) {
    html += '<button type="button" class="rtr-dd-chip' + (c.id === selected ? ' on' : '')
      + '" data-site="' + esc(c.id) + '">' + esc(c.name)
      + '<span class="rtr-dd-chip-n">' + c.count + '</span></button>';
  }
  return html;
}

/** Where the highlight moves. Clamped at both ends - it does not wrap. */
export function nextHighlight(key: string, current: number, count: number): number {
  if (key === 'ArrowDown') return Math.min(count - 1, current + 1);
  if (key === 'ArrowUp') return Math.max(0, current - 1);
  return current;
}

/**
 * The "Switching to …" overlay's state machine.
 *
 * ── THE SECOND FALSE IS THE ONE THAT MATTERS ────────────────────────────────
 *
 * A switch produces TWO `router:status` events with `connected: false` in the
 * ordinary case: the first is the OLD session tearing down, which is normal and
 * must not dismiss anything. The second means the NEW router failed to connect,
 * and then the overlay has to go or the operator is left staring at a spinner
 * with no way to pick a different router.
 *
 * A port that dismissed on the first would close the overlay instantly on every
 * successful switch - and look correct, because the switch then succeeds anyway.
 * A port that never dismissed would trap the operator whenever the new router is
 * unreachable, which is exactly when they most need the picker back.
 *
 * Pure, so both are testable without a DOM or a socket.
 */
export interface SwitchOverlay { open: boolean; falses: number }

export function overlayOnSwitch(): SwitchOverlay {
  return { open: true, falses: 0 };
}

export function overlayOnStatus(s: SwitchOverlay, connected: boolean): SwitchOverlay {
  if (connected) return { open: false, falses: s.falses };
  // REPRODUCED, NOT NEEDED - and that is measured. The live guard is
  // `switchOvl.classList.contains('open')`, and removing it here survives the
  // whole corpus: a status arriving while the overlay is closed can only
  // advance the count, and `overlayOnSwitch` resets the count to zero, so no
  // sequence separates the two. Kept because this is a port and the original
  // has it; the surviving mutation is the honest note that it cannot be
  // observed. Same category as `splitRate`'s `|| 0`.
  if (!s.open) return s;
  const falses = s.falses + 1;
  return { open: falses <= 1, falses };
}

/**
 * Wire the picker. `onChoose` is the caller's switch - this module decides WHICH
 * router, never what switching means.
 */
export function wireRouterDropdown(
  getRouters: () => readonly DropdownRouter[],
  getActiveId: () => string,
  getStatus: () => Record<string, boolean | undefined>,
  onChoose: (id: string) => void,
  // THE SHARED SITE CACHE, not a second copy. `settings-sites.ts` publishes it
  // so the device table and the device modal turn an id into a name from one
  // source; two caches drift, and the symptom is a heading naming a site the
  // device's own form says it is not in.
  getSites: () => Readonly<Record<string, { name?: string }>> = () => ({}),
): { refresh: () => void } {
  const wrap = el('routerSelectWrap');
  const btn = el('routerSelectBtn');
  const label = el('routerSelectLabel');
  const panel = el('routerDropdown');
  const list = el('routerDropdownList');
  const chipBar = el('routerDropdownChips');
  const search = el<HTMLInputElement>('routerDropdownSearch');

  let open = false, filter = '', hl = -1;
  // Remembered per viewer. A fleet of 100 devices across 15 sites is worked one
  // site at a time, and re-picking it on every page load is the thing this
  // whole change is meant to remove.
  let site = String(lsGet<string>(DD_SITE_KEY, ''));

  const siteNames = (): Record<string, string> => {
    const out: Record<string, string> = {};
    const by = getSites();
    for (const id of Object.keys(by)) out[id] = by[id]?.name || id;
    return out;
  };

  const chips = (): SiteChip[] => siteChips(getRouters(), siteNames());

  /** The blocks the list draws: chip first, then the search, then grouped. */
  function groups(): DropdownGroup[] {
    const all = chips();
    const picked = filterRouters(routersInSite(getRouters(), site), filter);
    // WHEN A CHIP IS SELECTED the list is that one site, so it is one group and
    // `dropdownHtml` draws no heading. `groupRoutersBySite` would produce the
    // same single group from the full chip list, but passing only the selected
    // chip says so rather than relying on the filter having emptied the rest.
    if (site) {
      const c = all.find((x) => x.id === site);
      return c ? groupRoutersBySite(picked, [c]) : [{ site: null, rows: picked }];
    }
    return all.length ? groupRoutersBySite(picked, all) : [{ site: null, rows: picked }];
  }

  const rows = (): DropdownRouter[] => flattenGroups(groups());

  function renderChips(): void {
    if (!chipBar) return;
    const all = chips();
    // A CHIP THAT IS NO LONGER OFFERED RESETS TO ALL, rather than filtering to
    // nothing. A site can be deleted, or emptied, or lose its last device to a
    // grant change, while the choice sits in this browser's storage - and an
    // empty picker with no visible reason is the worst of the outcomes.
    if (site && !all.some((c) => c.id === site)) {
      site = '';
      lsSet(DD_SITE_KEY, site);
    }
    const html = chipsHtml(all, site);
    chipBar.innerHTML = html;
    chipBar.style.display = html ? '' : 'none';
  }

  function render(): void {
    if (!list) return;
    list.innerHTML = dropdownHtml(groups(), getActiveId(), getStatus(), hl);
  }
  function refreshLabel(): void {
    if (!label) return;
    const r = getRouters().find((x) => x.id === getActiveId());
    label.textContent = r ? rtrLabel(r) : '-';
  }
  function openDd(): void {
    if (open || !wrap) return;
    open = true; filter = ''; hl = -1;
    if (search) search.value = '';
    // The search box appears only once the list is long enough to need it, and
    // the count is of SWITCHABLE routers - a fleet of disabled ones does not
    // earn a search box.
    const many = getRouters().filter((r) => !r.disabled).length >= DD_SEARCH_MIN;
    const box = panel?.querySelector<HTMLElement>('.rtr-dd-search');
    if (box) box.style.display = many ? '' : 'none';
    wrap.classList.add('open');
    btn?.setAttribute('aria-expanded', 'true');
    // BEFORE `render`, because it can reset a chip that no longer exists and
    // the list must be drawn from the value the strip is showing.
    renderChips();
    render();
    if (many && search) search.focus();
  }
  function closeDd(): void {
    if (!open || !wrap) return;
    open = false;
    wrap.classList.remove('open');
    btn?.setAttribute('aria-expanded', 'false');
  }
  function choose(id: string): void {
    closeDd();
    // Choosing the router already active is a no-op, not a reconnect: the live
    // app guards this and without it every stray click would tear down and
    // rebuild a working session.
    if (!id || id === getActiveId()) return;
    onChoose(id);
  }

  btn?.addEventListener('click', (e) => {
    e.stopPropagation();
    if (open) closeDd(); else openDd();
  });
  list?.addEventListener('click', (e) => {
    const item = (e.target as HTMLElement | null)?.closest?.('[data-rtr]') as HTMLElement | null;
    if (item) choose(item.getAttribute('data-rtr') || '');
  });
  search?.addEventListener('input', () => {
    filter = search.value; hl = -1; render();
  });
  chipBar?.addEventListener('click', (e) => {
    const chip = (e.target as HTMLElement | null)?.closest?.('[data-site]') as HTMLElement | null;
    if (!chip) return;
    // STOPPED, like the button's own handler: the document-level listener below
    // closes the panel on any click, and a chip is inside the panel.
    e.stopPropagation();
    site = chip.getAttribute('data-site') || '';
    lsSet(DD_SITE_KEY, site);
    hl = -1;
    renderChips();
    render();
    // The search is narrowing WITHIN a site now, so the caret goes back to it
    // rather than leaving the operator to click the box again.
    if (search && search.offsetParent !== null) search.focus();
  });
  wrap?.addEventListener('keydown', (e) => {
    const ev = e as KeyboardEvent;
    if (!open) return;
    if (ev.key === 'Escape') { closeDd(); return; }
    const r = rows();
    if (ev.key === 'ArrowDown' || ev.key === 'ArrowUp') {
      ev.preventDefault();
      hl = nextHighlight(ev.key, hl, r.length);
      render();
    } else if (ev.key === 'Enter') {
      ev.preventDefault();
      if (r[hl]) choose(r[hl]!.id);
    }
  });
  // Clicking anywhere else closes it. On `document`, so it fires for a click on
  // a page the dropdown overlays.
  document.addEventListener('click', () => closeDd());

  refreshLabel();
  return { refresh: () => { refreshLabel(); if (open) render(); } };
}
