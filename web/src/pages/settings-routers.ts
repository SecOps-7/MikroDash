/**
 * Settings → the routers table, and the way into the router modal.
 *
 * ── THIS IS THE MODAL'S ONLY OPENER, WHICH IS WHY IT MATTERS ────────────────
 *
 * `router-modal.ts` and `router-form.ts` have been fully ported and completely
 * unreachable: the Devices page has no edit affordance of its own, so wiring the
 * modal from there put a dialog on a page with no way to open it. The Edit
 * button in these rows is the live app's opener, and this module is what makes
 * two already-tested files reachable.
 *
 * ── THE ROW IS A STRING, AND THE BUTTONS CARRY THEIR OWN ARGUMENTS ──────────
 *
 * `data-rtr-id`, `data-rtr-action` and `data-rtr-label` — one delegated listener
 * on the tbody rather than a handler per row, so a re-render does not leak
 * listeners. `data-rtr-label` exists ONLY for the delete confirmation, which
 * names the router being destroyed; reading the label out of the row's DOM
 * instead would break the moment the first cell's markup changed.
 *
 * ── THE ACTIVE ROUTER CANNOT BE DISABLED ────────────────────────────────────
 *
 * Its toggle is rendered `disabled` with a title saying why. That is a UI
 * courtesy, not the enforcement — the server decides — but it is the difference
 * between a button that explains itself and one that fails.
 */

import { el, esc } from '../dom';

export interface RouterRow {
  id: string;
  label?: string;
  name?: string;
  host?: string;
  model?: string;
  serial?: string;
  osVersion?: string;
  tls?: boolean;
  tlsInsecure?: boolean;
  disabled?: boolean;
  siteId?: string | null;
  siteIds?: string[];
}

export interface SiteName { name?: string }

/** The eight-column colspan of the empty state. Kept beside the row it must match. */
export const ROUTER_TABLE_COLUMNS = 8;

/**
 * One row.
 *
 * `connState` is THREE-VALUED and the third value is not an error: `undefined`
 * means no status has arrived for this router yet, and it renders as an em dash
 * rather than as "Offline". A router nobody has heard from is not the same as
 * one known to be down, and showing the second for the first is how a healthy
 * fleet looks broken on first paint.
 */
export function renderRouterRow(
  r: RouterRow,
  activeId: string,
  status: Record<string, boolean | undefined>,
  sitesById: Record<string, SiteName>,
): string {
  const isActive = r.id === activeId;
  const activeBadge = isActive ? '<span class="rtr-active-badge">Active</span>' : '';
  const delBtn = '<button class="sbtn sbtn-danger" style="padding:.25rem .6rem;font-size:.68rem" data-rtr-id="' + esc(r.id) + '" data-rtr-label="' + esc(r.label) + '" data-rtr-action="delete" title="Delete">&#128465;</button>';
  const toggleBtn = '<button class="sbtn sbtn-ghost" style="padding:.25rem .6rem;font-size:.68rem"'
    + (isActive ? ' disabled title="Cannot disable the active router"' : '')
    + ' data-rtr-id="' + esc(r.id) + '" data-rtr-action="toggle">'
    + (r.disabled ? 'Enable' : 'Disable') + '</button>';
  const tlsBadge = r.tls
    ? '<span style="font-size:.6rem;padding:.1rem .4rem;border-radius:4px;background:rgba(52,211,153,.1);color:rgba(52,211,153,.9);border:1px solid rgba(52,211,153,.2)">TLS</span>'
    : '<span style="font-size:.6rem;padding:.1rem .4rem;border-radius:4px;background:rgba(251,191,36,.1);color:rgba(251,191,36,.8);border:1px solid rgba(251,191,36,.2)">Unencrypted</span>';
  const certNote = r.tlsInsecure ? ' <span style="font-size:.6rem;color:var(--text-muted)">self-signed</span>' : '';
  const connState = status[r.id];
  const badgeCls = connState === true ? 'rtr-status-badge--on' : connState === false ? 'rtr-status-badge--off' : 'rtr-status-badge--unknown';
  const badgeTxt = connState === true ? 'Online' : connState === false ? 'Offline' : '—';
  const statusCell = r.disabled
    ? '<span class="rtr-status-badge rtr-status-badge--disabled" data-rtr-conn="' + esc(r.id) + '">Disabled</span>'
    : '<span class="rtr-status-badge ' + badgeCls + '" data-rtr-conn="' + esc(r.id) + '">' + badgeTxt + '</span>';
  // Identity is persisted on the router entry rather than read from the live
  // stats feed, so these stay populated while a router is offline or disabled.
  const unknown = '<span style="color:var(--text-muted)">—</span>';
  // Site membership goes UNDER the label rather than in its own column, so the
  // table keeps its eight columns and the empty-state colspan above stays right.
  // A site-less router renders nothing — an explicit "no site" chip on every row
  // would be noise for the installs that never create one.
  const siteNames = (Array.isArray(r.siteIds) ? r.siteIds : (r.siteId ? [r.siteId] : []))
    .map((id) => (sitesById && sitesById[id] ? sitesById[id].name : null))
    .filter(Boolean) as string[];
  const siteChip = siteNames.length
    ? '<div style="margin-top:.15rem;display:flex;flex-wrap:wrap;gap:.2rem">' + siteNames.map((n) =>
        '<span style="font-size:.6rem;padding:.1rem .4rem;border-radius:4px;background:rgba(99,130,190,.12);color:var(--text-muted);border:1px solid var(--border)">' + esc(n) + '</span>').join('') + '</div>'
    : '';
  const modelCell = r.model ? esc(r.model) : unknown;
  const serialCell = r.serial ? '<span class="rtr-host">' + esc(r.serial) + '</span>' : unknown;
  const versionCell = r.osVersion ? '<span class="rtr-ver-pill">' + esc(r.osVersion) + '</span>' : unknown;
  return '<tr' + (r.disabled ? ' style="opacity:.55"' : '') + '>' +
    '<td><div style="font-weight:600;font-size:.76rem">' + esc(r.label) + '</div>' + activeBadge + siteChip + '</td>' +
    '<td>' + statusCell + '</td>' +
    '<td><span class="rtr-host">' + esc(r.host) + '</span></td>' +
    '<td>' + modelCell + '</td>' +
    '<td>' + serialCell + '</td>' +
    '<td>' + versionCell + '</td>' +
    '<td>' + tlsBadge + certNote + '</td>' +
    '<td style="text-align:right;white-space:nowrap">' +
      '<div style="display:flex;gap:.3rem;justify-content:flex-end">' +
        toggleBtn +
        '<button class="sbtn sbtn-ghost" style="padding:.25rem .6rem;font-size:.68rem" data-rtr-id="' + esc(r.id) + '" data-rtr-action="edit">Edit</button>' +
        delBtn +
      '</div>' +
    '</td>' +
    '</tr>';
}

/** The whole tbody, including the empty state that tells a new install what to do. */
export function renderRouterTable(
  routers: RouterRow[],
  activeId: string,
  status: Record<string, boolean | undefined>,
  sitesById: Record<string, SiteName>,
): string {
  if (!routers.length) {
    return '<tr><td colspan="' + ROUTER_TABLE_COLUMNS + '" style="text-align:center;padding:1.2rem;color:var(--text-muted);font-size:.73rem">No routers configured. Click Add Router to get started.</td></tr>';
  }
  return routers.map((r) => renderRouterRow(r, activeId, status, sitesById)).join('');
}

/** The delete confirmation, which names what is about to be destroyed. */
export function deleteRouterPrompt(label: string): string {
  return 'Delete router "' + label + '"?\n\nAll accumulated data (traffic history, ping history, bandwidth, alerts, and connectivity events) for this router will be permanently deleted.\n\nThis cannot be undone.';
}

/**
 * Repaint ONE badge in place, on `router:status`.
 *
 * ── WHY IN PLACE AND NOT A RE-RENDER ────────────────────────────────────────
 *
 * A status event arrives per router and can arrive often. Re-rendering the whole
 * tbody would rebuild every row and, more to the point, would throw away any
 * text selection or focus inside the table. The live app updates the one badge;
 * `data-rtr-conn` carries the router id for exactly this lookup, and porting the
 * attribute without this reader is what left the port rendering it and reading
 * nothing — the attr audit is what said so.
 *
 * ── IT DOES NOT PAINT A DISABLED ROW ───────────────────────────────────────
 *
 * The disabled badge carries `data-rtr-conn` too, so an unguarded selector
 * matched it and replaced "Disabled" with "Offline" — or worse, "Online" —
 * leaving a dimmed row with an Enable button and an Online badge until the next
 * full render put it back. A disabled router still has its session torn down and
 * re-established around a switch, and `router:status` is emitted per router
 * rather than only for enabled ones, so it fires in ordinary use.
 *
 * This was reproduced deliberately for two days and reported in
 * ../MikroDash/ToDo.md rather than quietly fixed, because a port that differed
 * from the app it replaces is the worse failure. Upstream fixed it in `d7529e0`
 * and this follows.
 *
 * RECORDING AND PAINTING ARE SEPARATE, which is the point. `main.ts` writes
 * `routerStatus[id]` before calling this, so re-enabling the router re-renders
 * from that map and shows the state it actually had rather than a dash.
 *
 * AN UNKNOWN ROUTER IS NOT PAINTED EITHER, matching the live expression
 * `(_r && !_r.disabled)`: a `find` that misses yields undefined, and the guard
 * is falsy, so nothing is written.
 */
export function updateRouterStatusBadge(routerId: string, connected: boolean): void {
  const row = deps?.routers().find((r) => r.id === routerId);
  if (!row || row.disabled) return;
  const badge = document.querySelector('[data-rtr-conn="' + routerId + '"]');
  if (!badge) return;
  badge.className = 'rtr-status-badge ' + (connected ? 'rtr-status-badge--on' : 'rtr-status-badge--off');
  badge.textContent = connected ? 'Online' : 'Offline';
}

export interface RouterTableDeps {
  routers: () => RouterRow[];
  activeId: () => string;
  status: () => Record<string, boolean | undefined>;
  sitesById: () => Record<string, SiteName>;
  openModal: (r: RouterRow | null) => void;
}

let deps: RouterTableDeps | null = null;

/** Repaint. Called on `routers:update` and on `router:active`, as the live app does. */
export function renderRoutersInto(): void {
  const tbody = el('rtrTbody');
  if (!tbody || !deps) return;
  tbody.innerHTML = renderRouterTable(deps.routers(), deps.activeId(), deps.status(), deps.sitesById());
}

export function initSettingsRoutersTable(d: RouterTableDeps): void {
  deps = d;
  const tbody = el('rtrTbody');
  if (!tbody) return;

  // ONE delegated listener, registered once. The rows are replaced wholesale on
  // every repaint, so a listener per button would be re-attached to new nodes
  // each time and the old ones would go with their rows — which works, and
  // quietly costs a handler per row per repaint until it does not.
  tbody.addEventListener('click', (e) => {
    const btn = (e.target as HTMLElement | null)?.closest?.('[data-rtr-action]') as HTMLElement | null;
    if (!btn) return;
    const action = btn.dataset.rtrAction;
    const id = btn.dataset.rtrId || '';

    if (action === 'edit') {
      const r = d.routers().find((x) => x.id === id);
      // NO FALLBACK to opening an empty modal. `openModal(null)` is the ADD
      // form, and a stale id silently offering to create a router is worse than
      // a button that does nothing.
      if (r) d.openModal(r);
      return;
    }

    if (action === 'toggle') {
      const rr = d.routers().find((x) => x.id === id);
      if (!rr) return;
      // The CURRENT value is read here, not carried in the markup: the row could
      // have been repainted by a `routers:update` between render and click.
      fetch('/api/routers/' + encodeURIComponent(id), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({ disabled: !rr.disabled }),
      })
        .then((res) => res.json())
        .then((j) => { if (!j.ok) alert(j.error || 'Toggle failed'); })
        .catch(() => alert('Network error'));
      return;
    }

    if (action === 'delete') {
      const label = btn.dataset.rtrLabel || id;
      if (!confirm(deleteRouterPrompt(label))) return;
      fetch('/api/routers/' + encodeURIComponent(id), {
        method: 'DELETE',
        credentials: 'same-origin',
      })
        .then((r) => r.json())
        .then((r) => { if (!r.ok) alert('Delete failed: ' + (r.error || 'Unknown error')); })
        .catch((e) => alert('Request failed: ' + e));
    }
  });

  renderRoutersInto();
}
