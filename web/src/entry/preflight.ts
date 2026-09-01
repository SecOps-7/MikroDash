/**
 * The <head> script — everything that must happen BEFORE the body paints.
 *
 * ── WHY IT IS ITS OWN BUNDLE ────────────────────────────────────────────────
 *
 * `app.js` is a module and therefore deferred; it runs after the document is
 * parsed. Both things below have to happen before, so neither can live there:
 *
 *  1. The FADE. `login.js` sets `justLoggedIn` and fades the login page out; this
 *     hides the incoming document so it can fade back in rather than flashing.
 *     Applied a frame late, the flash has already happened.
 *  2. The NAV SHAPE. Grouping is a per-user server-side preference, and the
 *     server's answer arrives over the socket — long after the sidebar paints.
 *     Applying it in `app.js` means the nav paints in the default shape and then
 *     visibly regroups, worst for somebody who chose the flat list and watches
 *     it collapse on every load. `localStorage` is a CACHE of the last known
 *     answer; the server stays the source of truth and `caps.ts` reconciles.
 *
 * The live comment names the alternative and why it is unavailable: "An inline
 * <script> after </nav> would be the obvious alternative and is blocked: the CSP
 * sets script-src 'self' with no 'unsafe-inline'. Inline STYLE is allowed, which
 * is why the open categories arrive as a generated stylesheet rather than as
 * classes — the elements do not exist yet to put classes on."
 *
 * ── IT WAS THE LIVE REPO'S FILE UNTIL 2026-08-28 ────────────────────────────
 *
 * `web/public/preflight.js` was a byte-for-byte copy of `../MikroDash/public/preflight.js`,
 * shipped as a static asset. The operator's instruction — "the port should stand
 * on its own without any lingering JS from the live repo" — is what moved it
 * here. The preflight check drives THIS module and the live file from one
 * harness and compares what each leaves on the document, so the copy could be
 * deleted without taking the behaviour on trust.
 */

// ── 1. The post-login fade ──────────────────────────────────────────────────
//
// `getItem` and not `removeItem`: the flag is consumed by `main.ts`, which is
// what restores the opacity. Clearing it here would hide the document with
// nothing left to say why, and the app would render perfectly and invisibly —
// which is a bug this port shipped once already.
if (sessionStorage.getItem('justLoggedIn')) {
  document.documentElement.style.opacity = '0';
}

// ── 2. The nav shape ────────────────────────────────────────────────────────
try {
  const nav = (JSON.parse(localStorage.getItem('mkd_nav_prefs') || 'null')
    || {}) as { grouped?: boolean; expanded?: unknown };
  const root = document.documentElement;
  // `=== false`, not `!nav.grouped`: an absent preference means GROUPED, which
  // is the default shape. Treating absent as flat would collapse the nav for
  // every browser that has never saved one.
  root.setAttribute('data-nav', nav.grouped === false ? 'flat' : 'grouped');

  // SHAPE-GUARDED, NEVER VOCABULARY-GUARDED. The live comment: "This file holds
  // no list of category names and must not gain one — a copy of the taxonomy in
  // a file with no module system is one nothing could keep honest. Unknown
  // tokens simply match no element."
  //
  // The guard is also what makes the generated stylesheet safe: these keys go
  // straight into a selector, and `^[a-z]{2,20}$` admits nothing that could
  // close the attribute or the rule.
  const open = (Array.isArray(nav.expanded) ? nav.expanded : [])
    .filter((k: unknown): k is string => typeof k === 'string' && /^[a-z]{2,20}$/.test(k));
  if (open.length) {
    const st = document.createElement('style');
    st.id = 'navBoot';
    // LAYOUT ONLY. The live comment: "The tint, the open bar and the chevron
    // ride on .is-open, which app.js adds — restating their colours here would
    // be a second copy free to drift from the stylesheet. What must not flash is
    // rows appearing and disappearing; chrome settling a moment later is not
    // worth that risk."
    st.textContent = open
      .map((k) => '.nav-group[data-cat="' + k + '"]>.nav-group-body{display:flex}')
      .join('');
    document.head.appendChild(st);
  }
} catch {
  // A browser with site data blocked THROWS on `localStorage` access rather than
  // returning null. The nav shape is a convenience; the page must still load.
}
