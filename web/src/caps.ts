// Who may see what: the install's page toggles, this session's role, and the
// capability attributes scattered through the markup.
//
// ── TWO INPUTS, BOTH MUST SAY YES ───────────────────────────────────────────
//
// A page shows only if the INSTALL allows it and the ROLE grants it. They
// arrive at different times and are stored separately for that reason.
//
// ── UNKNOWN MUST NOT MEAN HIDDEN ────────────────────────────────────────────
//
// `pageAccess` starts null and the role half is SKIPPED while it is, so the nav
// is not blanked during the first paint. The server denies anything the role
// does not allow regardless, so a briefly-extra nav item is cosmetic — whereas a
// blank sidebar looks broken. The same reasoning runs the other way for
// Settings: `settingsAllowed()` permits while caps are unknown so a genuine
// administrator is not bounced out during the gap, and `applyCaps` re-checks
// once the answer is actually known. That re-check is the other half of the
// bargain and is not optional.
//
// ── CAPABILITY-DRIVEN, NOT ROLE-DRIVEN ──────────────────────────────────────
//
// With three roles and per-router scope, "is this person a viewer?" stopped
// answering "may they press this button" — an operator would have passed the old
// viewer check and then collected 403s. Mark an element `data-cap="…"` and it is
// governed here forever; adding a capability means adding an attribute, not
// editing this file.

import { PAGE_NAV_MAP } from './gen/view-presets.js';
import { ALL_NAV_PAGES } from './gen/page-keys.js';
import { applyMyAlertsTab } from './account.js';

export interface Caps {
  pages?: Record<string, boolean>;
  manageSettings?: boolean;
  managePrincipals?: boolean;
  createRouters?: boolean;
  [k: string]: unknown;
}

/** How this module reaches the page router, so it owes nothing to main.ts. */
export interface NavHost {
  current(): string;
  go(page: string): void;
  /**
   * Can THIS BUILD serve the page, or does it still belong to Node?
   *
   * While the port coexists with the live app every page is reachable: the ones
   * this build has not ported are handed to Node, which renders them correctly.
   * With no Node there is nothing to hand off to, and a nav item for an unported
   * page sends the browser to `/` — which now serves this app and lands on the
   * dashboard. The operator reported exactly that: "the Devices page redirects
   * back to the Dashboard page. Same with the Settings page."
   *
   * A page this build cannot serve is not hidden because it is forbidden or
   * switched off; it is hidden because it is NOT HERE YET. Both remaining ones —
   * Settings and Devices — are recorded in `tools/page-mount-audit.js` with what
   * blocks them, and both reappear the moment they mount.
   */
  serves(page: string): boolean;
}

let pageInstall: Record<string, unknown> = {};
let pageAccess: Record<string, boolean> | null = null;
/** Routers is meaningless on a single-router install. Starts true so the nav is
 *  not blanked before the router list has loaded — the same "not known yet means
 *  allow" rule `pageAccess` follows. */
let routersMultiple = true;
let host: NavHost | null = null;
/** The install's display timezone, empty for the browser's own. Read by the
 *  topbar clock; the Reports page keeps its own copy fed from its own payload. */
let displayTimezone = '';

export function getDisplayTimezone(): string {
  return displayTimezone;
}

/** How many peers the Dashboard's VPN card shows. */
let vpnDashTopN = 5;

export function getVpnDashTopN(): number {
  return vpnDashTopN;
}

const caps = (): Caps => ((globalThis as unknown as { _caps?: Caps })._caps) || {};

/**
 * May this session open Settings at all?
 *
 * Deliberately the same condition that shows `#settingsNavItem`, so the nav and
 * the page can never disagree about who Settings is for. Hiding the nav link was
 * never a block on its own — `showPage('settings')` from the console opened the
 * whole admin page. This is defence in depth, not the boundary; the server
 * refuses every write regardless.
 */
/**
 * May this viewer manage principals?
 *
 * UNDEFINED WHILE THE CAPS FETCH IS IN FLIGHT, and that is deliberate rather
 * than a missing default. `applyAuthModeVisibility` treats unknown as NO — "the
 * flash is absence rather than exposure" — so collapsing it to `false` here
 * would be the same answer today and would quietly remove the distinction the
 * moment somebody wrote a caller that wanted to WAIT for it.
 */
export function mayManagePrincipals(): boolean | undefined {
  const c = (globalThis as unknown as { _caps?: Caps })._caps;
  if (!c) return undefined;
  return !!c.managePrincipals;
}

export function settingsAllowed(): boolean {
  const c = (globalThis as unknown as { _caps?: Caps })._caps;
  if (!c) return true;
  return !!(c.manageSettings || c.managePrincipals);
}

export function setRoutersMultiple(multiple: boolean): void {
  routersMultiple = multiple;
  applyPageVisibility();
}

/**
 * Re-run the nav sweep. `pages` is the install's settings payload; omitted, the
 * last one is reused — which is what lets caps arriving later re-run it.
 */
export function applyPageVisibility(pages?: Record<string, unknown>): void {
  if (pages) pageInstall = pages;
  const p = pageInstall;

  applyMyAlertsTab(p.userNotifyEnabled);
  // `!= null` rather than a truthiness test, and `|| ''` after it: an explicitly
  // cleared timezone must REPLACE the previous one, while an absent key must
  // leave it alone. Those are different states and a falsy test collapses them.
  if (p.displayTimezone != null) displayTimezone = String(p.displayTimezone || '');
  // `!= null` for the same reason as the timezone: an absent key must leave the
  // previous value alone, and a cleared one must replace it. The default of 5 is
  // the original's.
  if (p.vpnDashTopN != null) vpnDashTopN = Number(p.vpnDashTopN) || 5;
  // The ping block on the network diagram. Its four stat ids are written by this
  // port already — what was missing is the SECTION being hidden when ping
  // collection is switched off, which left an operator who disabled ping looking
  // at a permanently empty block where the live app shows nothing at all.
  //
  // `!= null` for the same reason as the two above: absent leaves it alone,
  // explicitly false hides it. And `style.display`, not a class, because that is
  // what the live app sets — a class here would not survive the next render of
  // anything that writes the same attribute.
  if (p.pingEnabled != null) {
    const pingSection = document.getElementById('ndPingSection');
    if (pingSection) pingSection.style.display = p.pingEnabled ? '' : 'none';
  }

  const settingKeyFor: Record<string, string> = {};
  for (const k of Object.keys(PAGE_NAV_MAP)) settingKeyFor[PAGE_NAV_MAP[k]!] = k;

  let firstVisible: string | null = null;
  for (const pageName of ALL_NAV_PAGES) {
    const sKey = settingKeyFor[pageName];
    const byInstall = !sKey || p[sKey] !== false;
    const byRole = !pageAccess || !!pageAccess[pageName];
    const byCount = pageName !== 'devices' || routersMultiple;
    // A page this build cannot serve, with no Node behind it, is hidden rather
    // than offered and then bounced. Composed with the other three so the
    // "move off a page that just became hidden" branch below covers it too.
    const byBuild = host ? host.serves(pageName) : true;
    const visible = byInstall && byRole && byCount && byBuild;

    document.querySelectorAll<HTMLElement>('.nav-item[data-page="' + pageName + '"]')
      .forEach((navEl) => { navEl.style.display = visible ? '' : 'none'; });
    if (visible && !firstVisible) firstVisible = pageName;
    // Move off a page that just became hidden. NOT always to the dashboard — a
    // role can deny that too, so fall back to whatever is still reachable.
    if (!visible && host && host.current() === pageName) {
      host.go(firstVisible || 'dashboard');
    }
  }

  // A category with every child hidden is chrome, not navigation. Asked of the
  // DOM rather than recomputed from the map: the loop above has just written
  // every child's display, so the group already holds the answer, and a second
  // computation is one that can disagree with the first.
  document.querySelectorAll<HTMLElement>('.nav-group').forEach((g) => {
    let any = false;
    g.querySelectorAll<HTMLElement>('.nav-item[data-page]').forEach((e) => {
      if (e.style.display !== 'none') any = true;
    });
    g.style.display = any ? '' : 'none';
  });
}

/**
 * Apply a capability set to the whole chrome.
 *
 * IDEMPOTENT BY CONSTRUCTION: every governed element is set from the caps it is
 * given, never toggled relative to its current state. That is what lets the
 * permissions-changed handler and the 403 interceptor both re-run it safely.
 */
export function applyCaps(c: Caps | null | undefined): void {
  (globalThis as unknown as { _caps?: Caps })._caps = c || {};
  const cur = caps();

  // Page access is half of the nav decision — the install toggles are the other
  // half — so hand it over and re-run. Without this the nav showed every page
  // regardless of role: the server denied them, but a Read Only user still saw
  // Reports and Settings in the sidebar.
  if (cur.pages) {
    pageAccess = cur.pages;
    applyPageVisibility();
  }

  // HIDE where the whole surface is off-limits; DISABLE where the control sits
  // inside a page they can still legitimately read.
  document.querySelectorAll<HTMLElement>('[data-cap]').forEach((e) => {
    const allowed = !!cur[e.getAttribute('data-cap') || ''];
    if (e.hasAttribute('data-cap-disable')) {
      (e as HTMLButtonElement).disabled = !allowed;
      if (!allowed) e.title = 'You do not have permission for this';
    } else {
      e.style.display = allowed ? '' : 'none';
    }
  });

  // Pre-existing controls with no data-cap attribute of their own.
  const addRtr = document.getElementById('rtrAddBtn');
  if (addRtr) addRtr.style.display = cur.createRouters ? '' : 'none';
  const saveSett = document.getElementById('settingsSaveBtn') as HTMLButtonElement | null;
  if (saveSett) {
    saveSett.disabled = !cur.manageSettings;
    if (!cur.manageSettings) saveSett.title = 'Administrator access required';
  }
  const settingsNav = document.getElementById('settingsNavItem');
  // Operators still have a reason to open Settings — their own preferences and
  // the read-only view; only hide it from someone who can change nothing.
  if (settingsNav) {
    settingsNav.style.display = (cur.manageSettings || cur.managePrincipals) ? '' : 'none';
  }

  // Caps arrive after the first paint, so someone may already be standing on
  // Settings by the time we learn they may not be.
  if (host && host.current() === 'settings' && !settingsAllowed()) host.go('dashboard');
}

/**
 * Re-ask the server what this session may do.
 *
 * Permissions change while a browser is open — an administrator edits a role,
 * or revokes a grant — and before this nothing refreshed them at runtime, so a
 * session kept its old UI until reload, which reads as the feature not working.
 *
 * THE SERVER SENDS ONLY A NUDGE, NEVER THE CAPS THEMSELVES. Re-asking
 * re-resolves them server-side, so a forged `perms:changed` cannot widen
 * anything — the worst it can do is make a browser ask a question it is allowed
 * to ask.
 */
export function refreshCaps(): Promise<void> {
  return fetch('/api/auth/permissions', { credentials: 'same-origin' })
    .then((r) => r.json())
    .then((d) => { if (d && d.ok) applyCaps(d.caps); })
    .catch(() => {});
}

/**
 * Read the session and apply it. Non-critical: on failure the chip stays hidden
 * and the caps stay unknown, which permits — see the header.
 */
export function initCaps(navHost: NavHost): void {
  host = navHost;
  (globalThis as unknown as { _applyCaps?: typeof applyCaps })._applyCaps = applyCaps;

  void fetch('/api/auth/status')
    .then((r) => r.json())
    .then((d) => {
      (globalThis as unknown as { _authMode?: string })._authMode = d.authMode || 'modern';
      // Now that the mode is known for certain, re-run the parts of the chrome
      // that depend on it.
      applyPageVisibility();
      if (d.authMode !== 'modern') return;
      if (d.session) {
        const nameEl = document.getElementById('authUsername');
        if (nameEl) nameEl.textContent = d.session.username;
        const chip = document.getElementById('authUserChip');
        if (chip) chip.style.display = '';
        applyCaps(d.session.caps);
      }
    })
    .catch(() => { /* non-critical — the chip stays hidden on failure */ });
}
