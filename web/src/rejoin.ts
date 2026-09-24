/**
 * When a `router:active` means "re-join the page room".
 *
 * ── WHY THIS IS A NAMED FUNCTION AND NOT THREE LINES IN main() ──────────────
 *
 * It was three lines in `main()`, and the bug below lived there unnoticed
 * because nothing could reach it: `main.ts` calls `main()` at module scope, so
 * importing it starts the whole app and no test can ask this question. The
 * opacity was part of the defect, not incidental to it.
 *
 * ── THE BUG, MEASURED 2026-09-07 ────────────────────────────────────────────
 *
 * The old rule was "re-join when the router id CHANGES". That is right for a
 * switch and silently wrong for a reconnect, where the id is the same:
 *
 *   - room membership is per-CONNECTION, so a reconnect starts in no rooms;
 *   - the client re-sends `router:select` on connect, and the server's
 *     `rejoinPage` returns immediately because the new `conn` has an empty
 *     `cn.page` - there is nothing for it to rejoin;
 *   - the id had not changed, so this listener returned early and `page:focus`
 *     was never re-sent.
 *
 * The browser then sat in NO page room on a socket the server considered
 * healthy: every page-scoped card went stale while its collector polled happily
 * into a room nobody was in. Driving the real sequence over a WebSocket gave
 * four `wireless:update` in 75s with `page:focus`, and ZERO in 75s over a
 * reconnect that sent only `router:select`.
 *
 * It is the same failure `TestSelectRouterRejoinsEveryPerSocketSubscription`
 * exists for - "a browser subscribed to nothing … every card goes stale, and the
 * server log shows a healthy session throughout" - reached by the other route.
 */
export interface RejoinState {
  /** The last active router id this listener acted on. Empty before the first. */
  lastId: string;
  /** Set when the socket dropped: this connection's rooms are gone. */
  lostRooms: boolean;
}

/**
 * Whether this `router:active` should re-send `page:focus`, and the state after.
 *
 * Three cases, and the third is the one that regressed:
 *
 *   FIRST connect   no - the code that opened the page joined the room already,
 *                   and re-emitting would be a second join for a room we are in.
 *   SWITCH          yes - the id changed, and the room must be re-joined against
 *                   the NEW router so the role gate is re-applied to it.
 *   RECONNECT       yes, EVEN THOUGH THE ID IS UNCHANGED - the rooms are gone
 *                   with the old connection and only the client can say which
 *                   page to restore.
 *
 * A reconnect that is also the first id this listener has seen still re-joins: a
 * socket that dropped before any `router:active` arrived is in no room either,
 * so "first" cannot be allowed to mask "lost".
 */
export function rejoinDecision(
  activeId: string, state: RejoinState,
): { rejoin: boolean; next: RejoinState } {
  if (!activeId) return { rejoin: false, next: state };
  const rejoin = state.lostRooms;
  if (activeId === state.lastId && !rejoin) {
    return { rejoin: false, next: state };
  }
  const first = !state.lastId;
  return {
    rejoin: rejoin || !first,
    next: { lastId: activeId, lostRooms: false },
  };
}
