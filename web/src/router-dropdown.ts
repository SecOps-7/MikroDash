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
// rendered the dropdown — the markup is extracted verbatim, so it looked
// live — and clicking it did nothing, while the control that worked was hidden.
// A desktop user could not change routers, on any page. Found 2026-08-25 by
// counting the ids in `shell.html`, which no audit had ever scanned.
//
// A custom popover rather than a native select, and the original says why: each
// row carries the router's live status and the list can be searched. The mobile
// nav keeps its native select deliberately — the OS picker is the better control
// on touch.

import { esc, el } from './dom';

export interface DropdownRouter {
  id: string;
  label?: string;
  host?: string;
  disabled?: boolean;
}

/** Only surface the search box once the list is long enough to need it. */
export const DD_SEARCH_MIN = 5;

/**
 * The name a row shows.
 *
 * The suffix strip is not cosmetic: a router's label carries a ` · site` tail in
 * some deployments, and the dropdown is narrow. `host` is the fallback and `?`
 * the last resort — a router with neither still gets a row rather than a blank
 * one nobody can click.
 */
export function rtrLabel(r: DropdownRouter): string {
  return (r.label || r.host || '?').replace(/\s*[·•].*$/, '').trim();
}

/**
 * The rows a query selects.
 *
 * DISABLED ROUTERS ARE DROPPED FIRST, before the query — a disabled router is
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
 * `false` is down, and absent is "not known yet" — which renders a dot with no
 * modifier rather than an "off" one, so a router nobody has connected to yet is
 * not shown as broken.
 */
export function dropdownHtml(
  rows: readonly DropdownRouter[], activeId: string,
  status: Record<string, boolean | undefined>, hl: number,
): string {
  if (!rows.length) return '<div class="rtr-dd-empty">No routers match</div>';
  let html = '';
  rows.forEach((r, i) => {
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
  });
  return html;
}

/** Where the highlight moves. Clamped at both ends — it does not wrap. */
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
 * successful switch — and look correct, because the switch then succeeds anyway.
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
  // REPRODUCED, NOT NEEDED — and that is measured. The live guard is
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
 * Wire the picker. `onChoose` is the caller's switch — this module decides WHICH
 * router, never what switching means.
 */
export function wireRouterDropdown(
  getRouters: () => readonly DropdownRouter[],
  getActiveId: () => string,
  getStatus: () => Record<string, boolean | undefined>,
  onChoose: (id: string) => void,
): { refresh: () => void } {
  const wrap = el('routerSelectWrap');
  const btn = el('routerSelectBtn');
  const label = el('routerSelectLabel');
  const panel = el('routerDropdown');
  const list = el('routerDropdownList');
  const search = el<HTMLInputElement>('routerDropdownSearch');

  let open = false, filter = '', hl = -1;

  const rows = (): DropdownRouter[] => filterRouters(getRouters(), filter);

  function render(): void {
    if (!list) return;
    list.innerHTML = dropdownHtml(rows(), getActiveId(), getStatus(), hl);
  }
  function refreshLabel(): void {
    if (!label) return;
    const r = getRouters().find((x) => x.id === getActiveId());
    label.textContent = r ? rtrLabel(r) : '—';
  }
  function openDd(): void {
    if (open || !wrap) return;
    open = true; filter = ''; hl = -1;
    if (search) search.value = '';
    // The search box appears only once the list is long enough to need it, and
    // the count is of SWITCHABLE routers — a fleet of disabled ones does not
    // earn a search box.
    const many = getRouters().filter((r) => !r.disabled).length >= DD_SEARCH_MIN;
    const box = panel?.querySelector<HTMLElement>('.rtr-dd-search');
    if (box) box.style.display = many ? '' : 'none';
    wrap.classList.add('open');
    btn?.setAttribute('aria-expanded', 'true');
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
