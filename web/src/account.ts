// The account modal's renderers.
//
// The chip used to navigate to Settings, which is how an ordinary user ended up
// looking at install configuration. It opens this instead: the things a person
// may change about themselves, and nothing they may not.
//
// ── ONLY THE PURE HALF IS HERE ──────────────────────────────────────────────
//
// Four functions that take data and write DOM. The loader and the four write
// actions — change password, revoke other sessions, sign out, log out — are not
// ported yet. Splitting on that line is deliberate: these
// four can be compared against the live ones by DOM equality, which is the
// strongest gate available, and the writes cannot.

import { initUserNotify, loadUserNotify } from './pages/usernotify';
import { el, esc } from './dom.js';

export interface AccessGrants {
  global?: string[];
  sites?: { siteName: string; roles: string[] }[];
  routers?: { routerLabel: string; roles: string[] }[];
}

export interface SessionRow {
  createdAt: number | string;
  expiresAt?: number | string | null;
  current?: boolean;
}

/**
 * A transient message under a form.
 *
 * The clear-after-5s checks that the text is STILL the message it scheduled.
 * Without that, a second action inside the window would have its result wiped by
 * the first one's timer — the operator sees "saved", does something else, and
 * the confirmation for that vanishes a moment later for no visible reason.
 */
export function acctSay(target: HTMLElement | null, ok: boolean, msg: string): void {
  if (!target) return;
  target.textContent = msg;
  target.style.color = ok ? 'var(--accent-green, #4ade80)' : 'var(--accent-red, #f87171)';
  if (ok) setTimeout(() => { if (target.textContent === msg) target.textContent = ''; }, 5000);
}

/** What this account may reach: everything, or per site, or per router. */
export function renderAccess(a: AccessGrants): void {
  const body = el('acct_accessBody');
  if (!body) return;
  const rows: string[] = [];
  if (a.global && a.global.length) {
    rows.push('<div style="margin-bottom:.5rem"><strong style="font-size:.78rem">Everything</strong>' +
              '<div style="font-size:.75rem;color:var(--text-muted)">' + esc(a.global.join(', ')) + '</div></div>');
  }
  (a.sites || []).forEach((s) => {
    rows.push('<div style="margin-bottom:.5rem"><strong style="font-size:.78rem">Site: ' + esc(s.siteName) + '</strong>' +
              '<div style="font-size:.75rem;color:var(--text-muted)">' + esc(s.roles.join(', ')) + '</div></div>');
  });
  (a.routers || []).forEach((r) => {
    rows.push('<div style="margin-bottom:.5rem"><strong style="font-size:.78rem">Router: ' + esc(r.routerLabel) + '</strong>' +
              '<div style="font-size:.75rem;color:var(--text-muted)">' + esc(r.roles.join(', ')) + '</div></div>');
  });
  body.innerHTML = rows.length ? rows.join('')
    : '<span style="color:var(--text-muted);font-size:.78rem">No access granted yet — ask an administrator.</span>';
}

/**
 * The signed-in sessions.
 *
 * `toLocaleString()` with no arguments, exactly as the original: the browser's
 * locale and timezone decide the format. A port that pinned an explicit format
 * would render differently for every user outside the one locale it chose,
 * which is a user-visible change however much tidier the string looks.
 */
export function renderSessions(list: SessionRow[] | null | undefined): void {
  const body = el('acct_sessionsBody');
  if (!body) return;
  if (!list || !list.length) {
    body.innerHTML = '<span style="color:var(--text-muted);font-size:.78rem">No active sessions.</span>';
    return;
  }
  body.innerHTML = list.map((s) => {
    const when = new Date(s.createdAt).toLocaleString();
    const exp = s.expiresAt ? new Date(s.expiresAt).toLocaleString() : 'never';
    return '<div style="display:flex;justify-content:space-between;gap:.7rem;padding:.3rem 0;border-bottom:1px solid var(--border);font-size:.75rem">' +
           '<span>Signed in ' + esc(when) + (s.current ? ' <strong>(this device)</strong>' : '') + '</span>' +
           '<span style="color:var(--text-muted)">expires ' + esc(exp) + '</span></div>';
  }).join('');
}

/**
 * Open or close the change-password form.
 *
 * CLOSING CLEARS THE THREE FIELDS. They hold a plaintext password, and leaving
 * them populated means the next person to open the modal on an unlocked screen
 * finds it typed in for them. It also clears the result line, so a stale
 * "changed" does not greet the next attempt.
 *
 * The prompt returns to `flex`, not the empty string. It is a flex row, and
 * clearing the property to its stylesheet value would be equivalent only as
 * long as the stylesheet agrees — the original writes the value, so this does.
 */
export function setPwFormOpen(open: boolean): void {
  const form = el('acct_pwForm');
  const prompt = el('acct_pwPrompt');
  if (!form || !prompt) return;
  form.style.display = open ? '' : 'none';
  prompt.style.display = open ? 'none' : 'flex';
  if (!open) {
    for (const id of ['acct_currentPassword', 'acct_newPassword', 'acct_confirmPassword']) {
      const field = el<HTMLInputElement>(id);
      if (field) field.value = '';
    }
    const r = el('acct_pwResult');
    if (r) r.textContent = '';
  } else {
    el<HTMLInputElement>('acct_currentPassword')?.focus();
  }
}

/**
 * Show or hide the My Alerts section.
 *
 * It lived as a Settings tab until this modal took it over: Settings is
 * install-wide administration, and a personal delivery channel is not — an
 * ordinary user should never need the admin page to reach one.
 *
 * The auth-mode test is `!== 'none'` rather than `=== 'modern'`, and that is not
 * a style choice. `_authMode` is assigned from `/api/auth/status`, which lands
 * after the first `settings:pages`, so an equality test reads `undefined` and
 * hides the section permanently. Excluding only the mode that CANNOT use it is
 * correct at both points in time — 'none' has no user for the channels to
 * belong to.
 *
 * Loaded once, lazily: nobody should pay a request for a panel they never open.
 */
export function applyMyAlertsTab(enabled: unknown): void {
  const section = el('acctMyAlerts');
  if (!section) return;
  const authMode = (globalThis as unknown as { _authMode?: string })._authMode;
  const show = enabled === true && authMode !== 'none';
  section.style.display = show ? '' : 'none';
  // THIS READ `globalThis._loadUserNotify` — the LIVE app's global, published by
  // `window._loadUserNotify = loadUserNotify` in app.js. It was the honest shim
  // while the tab had no port: the panel worked because the Node app's script
  // was still on the page. This port has its own module now, so it calls it
  // directly and the tab no longer depends on the app it is replacing.
  if (show && !section.dataset.loaded) {
    section.dataset.loaded = '1';
    loadUserNotify();
  }
}

/**
 * Fill the modal. Four independent reads, none of which blocks the others.
 *
 * `/api/settings` is asked for the install switch rather than waiting for the
 * `settings:pages` broadcast: that fires on connect and on save, so whether it
 * has landed by the time somebody opens this is a matter of timing — and for a
 * non-admin it is the only signal, with the Settings page now out of reach. The
 * endpoint answers every role; a viewer gets the allowlisted subset, which
 * carries this flag and no credentials.
 *
 * The version is fetched ONCE — guarded on the element still being empty —
 * because it cannot change while the page is open. Same source the About tab
 * uses; non-admins can no longer reach that tab, so this is where they find out
 * what they are running.
 *
 * Every failure is swallowed. A modal that shows three of its four panels beats
 * one that shows an error where the panels should be.
 */
export function loadAccount(): void {
  const nameEl = el('authUsername');
  if (nameEl) {
    const u = el('acct_username');
    if (u) u.textContent = nameEl.textContent;
  }

  void fetch('/api/settings').then((r) => r.json())
    .then((d) => { if (d) applyMyAlertsTab(d.userNotifyEnabled === true); })
    .catch(() => {});
  void fetch('/api/account/access').then((r) => r.json())
    .then((d) => { if (d && d.ok) renderAccess(d.access); })
    .catch(() => {});
  void fetch('/api/account/sessions').then((r) => r.json())
    .then((d) => { if (d && d.ok) renderSessions(d.sessions); })
    .catch(() => {});

  const v = el('acct_version');
  if (v && !v.textContent) {
    void fetch('/healthz').then((r) => r.json())
      .then((d) => { if (d && d.version) v.textContent = 'MikroDash v' + d.version; })
      .catch(() => {});
  }
}

export function openAccountModal(): void {
  const modal = el('accountModal');
  if (!modal) return;
  // Collapsed unless asked for: opening the modal to check which routers you
  // can see should not present three empty password boxes.
  setPwFormOpen(false);
  modal.classList.add('open');
  loadAccount();
}

/** Both paths go to /login — a logout whose request failed still ends the
 *  session as far as this browser is concerned, and leaving someone on a
 *  dashboard they believe they have left is worse than a redundant redirect. */
function toLogin(): void {
  void fetch('/api/auth/logout')
    .then(() => { window.location.href = '/login'; })
    .catch(() => { window.location.href = '/login'; });
}

/**
 * Wire the modal. Everything here is a click handler.
 *
 * ── ONE DELIBERATE DIFFERENCE, STATED ───────────────────────────────────────
 *
 * The live password handler reads `cur.value` without checking `cur` exists, so
 * a missing field throws a TypeError inside the click handler — which the
 * browser swallows, leaving the button doing nothing. This returns early
 * instead. The operator-visible result is identical (nothing happens); the port
 * simply does not raise. The fields are in the extracted shell markup, so
 * neither path is reachable in practice.
 */
export function wireAccount(): void {
  // The My Alerts tab. Its own module because the account modal is otherwise
  // about identity — password, session — and personal notification channels are
  // a separate feature that happens to live in the same dialog.
  initUserNotify();
  el('authUserChip')?.addEventListener('click', () => { openAccountModal(); });
  el('acct_pwToggleBtn')?.addEventListener('click', () => { setPwFormOpen(true); });
  el('acct_pwCancelBtn')?.addEventListener('click', () => { setPwFormOpen(false); });

  const pwBtn = el<HTMLButtonElement>('acct_pwSaveBtn');
  pwBtn?.addEventListener('click', () => {
    const cur = el<HTMLInputElement>('acct_currentPassword');
    const nw = el<HTMLInputElement>('acct_newPassword');
    const cf = el<HTMLInputElement>('acct_confirmPassword');
    const out = el('acct_pwResult');
    if (!cur || !nw || !cf) return;
    if (!cur.value || !nw.value) return acctSay(out, false, 'Both passwords are required');
    // Checked here as well as server-side: catching a typo before it is
    // submitted is kinder than changing a password to something unintended.
    if (nw.value !== cf.value) return acctSay(out, false, 'New passwords do not match');
    pwBtn.disabled = true;
    acctSay(out, true, 'Saving…');
    void fetch('/api/account/password', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ currentPassword: cur.value, newPassword: nw.value }),
    })
      .then((r) => r.json())
      .then((d) => {
        pwBtn.disabled = false;
        if (!d.ok) return acctSay(out, false, d.error || 'Failed');
        cur.value = nw.value = cf.value = '';
        acctSay(out, true, d.revokedOtherSessions
          ? '✓ Password changed — signed out of ' + d.revokedOtherSessions + ' other session(s)'
          : '✓ Password changed');
        loadAccount();
      })
      .catch((e) => { pwBtn.disabled = false; acctSay(out, false, String(e)); });
  });

  const revokeBtn = el<HTMLButtonElement>('acct_signOutOthersBtn');
  revokeBtn?.addEventListener('click', () => {
    const out = el('acct_sessionsResult');
    revokeBtn.disabled = true;
    void fetch('/api/account/sessions/revoke-others', { method: 'POST' })
      .then((r) => r.json())
      .then((d) => {
        revokeBtn.disabled = false;
        if (!d.ok) return acctSay(out, false, d.error || 'Failed');
        acctSay(out, true, '✓ Signed out ' + d.revoked + ' other session(s)');
        loadAccount();
      })
      .catch((e) => { revokeBtn.disabled = false; acctSay(out, false, String(e)); });
  });

  el('acct_signOutBtn')?.addEventListener('click', () => { toLogin(); });
  // stopPropagation because this button sits INSIDE the chip, whose own click
  // opens the modal. Without it, signing out also opens the account modal on
  // the way past.
  el('logoutBtn')?.addEventListener('click', (e) => { e.stopPropagation(); toLogin(); });
}
