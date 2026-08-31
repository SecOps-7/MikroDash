/**
 * Redirect to the login page when a request comes back 401.
 *
 * ── THE DEFECT THIS FIXES ───────────────────────────────────────────────────
 *
 * The live app installs this as the FIRST thing `public/app.js` does, before any
 * other code can call `fetch`. This port had nothing equivalent, and the
 * consequence is what the operator hit on 2026-08-28: with the SPA already open,
 * the server was restarted, every in-memory session died, and every subsequent
 * request answered 401 — so the page sat there doing nothing, with NO LOGIN
 * SCREEN and nothing working. The document was never re-requested, so the
 * server's own 302 on `/next/` never came into it.
 *
 * `main.ts` catches the failure from `loadRouters()` and logs it, which is why
 * this was invisible in development: the console said "no routers are readable
 * by this account", which reads as a permissions problem rather than a dead
 * session.
 *
 * ── 403 IS NOT 401, AND THE DIFFERENCE IS DELIBERATE ────────────────────────
 *
 * The live comment: "403 is handled differently on purpose: it means 'still
 * signed in, but no longer permitted', which a redirect to /login would
 * misreport as a session problem. Instead re-resolve permissions so the UI
 * catches up with whatever changed — a role edited, a grant revoked — rather
 * than failing silently."
 *
 * THROTTLED, because one denied page can fire several requests at once and each
 * must not trigger its own re-resolve.
 *
 * ── ONLY IN MODERN AUTH MODE ────────────────────────────────────────────────
 *
 * With `authMode: none` there is no login page to send anybody to, and a
 * redirect would be a loop. `_authMode` is assigned from `/api/auth/status` by
 * `caps.ts`; until it arrives it is undefined, which is not `'modern'`, so the
 * guard stays out of the way during the first moments of a page load.
 */

const REFRESH_THROTTLE_MS = 3000;
const VERIFY_THROTTLE_MS = 3000;
let lastVerify = 0;

/**
 * Wrap `window.fetch`.
 *
 * Called before anything else in `main.ts`, for the same reason the live app
 * puts it at the top of its file: a request made before the wrapper is in place
 * is a request whose 401 nobody sees.
 */
export function installFetchGuard(refreshCaps: () => void): void {
  const original = globalThis.fetch;
  if (typeof original !== 'function') return;

  let lastRefresh = 0;
  globalThis.fetch = function guarded(...args: Parameters<typeof fetch>) {
    return original.apply(globalThis, args).then((res) => {
      const mode = (globalThis as unknown as { _authMode?: string })._authMode;
      if (res.status === 401 && mode === 'modern') {
        window.location.href = '/login';
      } else if (res.status === 403 && mode === 'modern') {
        const now = Date.now();
        if (now - lastRefresh > REFRESH_THROTTLE_MS) {
          lastRefresh = now;
          refreshCaps();
        }
      }
      // RETURNED UNCHANGED either way. The caller still gets its response and
      // still handles its own error; this adds a reaction, it does not swallow
      // one.
      return res;
    });
  } as typeof fetch;
}

/**
 * Ask whether the session is still there, after a socket handshake was refused.
 *
 * ── WHY THE FETCH GUARD IS NOT ENOUGH ───────────────────────────────────────
 *
 * The server auth-gates the WebSocket upgrade. Once a session dies — expired, or
 * wiped by a container restart — every reconnect attempt is refused and `open`
 * never fires. So there is no `connect` to check on, `session:expired` cannot
 * arrive because it needs a live socket, and the fetch guard above never sees it
 * because a WebSocket handshake is not a fetch. The tab retries forever behind a
 * capped backoff and sits on an empty dashboard until somebody reloads by hand.
 *
 * ── A FAILED HANDSHAKE ALONE DOES NOT MEAN THE SESSION DIED ─────────────────
 *
 * The server may simply be down, and redirecting then would take the operator
 * away from a dashboard that is about to come back. So this ASKS:
 * `/api/auth/status` is public, and if it answers and reports no session the
 * session is genuinely gone. If the fetch itself fails the server is
 * unreachable, which is what the reconnect banner is for — so it does nothing.
 *
 * THROTTLED, because reconnect attempts are frequent and each must not cost a
 * request.
 */
export function verifySessionAfterFailure(): void {
  const mode = (globalThis as unknown as { _authMode?: string })._authMode;
  if (mode !== 'modern') return;
  const now = Date.now();
  if (now - lastVerify < VERIFY_THROTTLE_MS) return;
  lastVerify = now;
  void fetch('/api/auth/status', { credentials: 'same-origin' })
    .then((r) => r.json())
    .then((d: { session?: unknown } | null) => {
      if (d && !d.session) window.location.href = '/login';
    })
    .catch(() => { /* the server is down, which is not a session problem */ });
}
