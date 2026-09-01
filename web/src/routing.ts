// The address bar, kept in step with the page on screen.
//
// ── WHY THE APP HAD NO URLs ─────────────────────────────────────────────────
//
// Every page is a `.page-view` the SPA shows and hides, so browsing never left
// `/`. Nothing could be bookmarked or linked, the back button did not go back a
// page, and a refresh always landed on the dashboard.
//
// ── ONE GUARD DOES THE WORK OF THREE ────────────────────────────────────────
//
// `sync` compares the target path against the one already in the bar and writes
// nothing when they match. That single test covers three situations that would
// otherwise each need their own:
//
//   - `select()` re-runs `showPage` with the SAME page on every socket
//     reconnect. The path already matches, so history stays clean.
//   - After a `popstate` the browser has ALREADY moved; the path matches, so
//     handling it writes nothing and cannot loop.
//   - `showPage` silently rewrites `settings` to `dashboard` when the operator
//     may not see it. The comparison then corrects the bar, so a deep link to a
//     forbidden page cannot leave the address showing one thing and the screen
//     another.
//
// ── AND WHY `mode` EXISTS ANYWAY ────────────────────────────────────────────
//
// The comparison alone cannot tell a NAVIGATION from a CORRECTION, and they need
// different history. Booting at a forbidden `/settings` is the case that proves
// it: pushing `/home` would leave `/settings` behind as a back target, and going
// back would bounce between the two. A correction REPLACES the entry it is
// fixing; only a real gesture pushes.
import { pagePath, pageForPath } from './gen/pages';

/** How a page change should affect history. */
export type NavMode =
  /** A gesture: a nav click or a keyboard shortcut. Adds an entry. */
  | 'push'
  /** A correction: first paint, or a page the operator may not see. Rewrites
   *  the current entry rather than leaving the wrong one behind. */
  | 'replace'
  /** The browser already moved (popstate). Touch nothing. */
  | 'skip';

/** The page a URL names, or '' when it names none. */
export function pageForURL(pathname: string): string {
  return pageForPath(pathname);
}

/**
 * The page a fresh load should open.
 *
 * `known` is the set this bundle can actually render. A path can name a real
 * page that this build does not have — during a rolling deploy, or a link from a
 * newer version — and rendering nothing would be worse than landing home.
 */
export function initialPage(known: ReadonlySet<string>, fallback: string): string {
  const key = pageForPath(window.location.pathname);
  return key && known.has(key) ? key : fallback;
}

/** Put `key` in the address bar, if it is not already there. */
export function sync(key: string, mode: NavMode): void {
  if (mode === 'skip') return;
  const want = pagePath(key);
  if (window.location.pathname === want) return;
  const h = window.history;
  if (mode === 'replace') h.replaceState({ page: key }, '', want);
  else h.pushState({ page: key }, '', want);
}

/**
 * Answer the back and forward buttons.
 *
 * `onNavigate` is only called when the target DIFFERS from what is shown.
 * `mikrodash:pagechange` carries no "did it actually change" guard and around
 * twenty page modules re-run their entry logic on it, so calling `showPage` for
 * the page already open would make every one of them fire twice.
 *
 * The state object is ignored and the path re-read instead: the entry the
 * document loaded with has `state === null`, and it has to work like any other.
 */
export function initRouting(onNavigate: (key: string) => void, current: () => string): void {
  window.addEventListener('popstate', () => {
    const key = pageForPath(window.location.pathname);
    if (key && key !== current()) onNavigate(key);
  });
}
