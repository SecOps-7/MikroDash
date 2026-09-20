package server

// What the connectivity debounce does once it has made up its mind.
//
// ── THREE CONSUMERS, ONE DECISION ─────────────────────────────────────────
//
// `internal/connstate` decides whether a router is OFFLINE — the device
// dialog's "Offline threshold", thirty seconds by default. Three things want
// that answer and used to get it from three different places, or not at all:
//
//  1. the HISTORY ROW, which is what the debounce was originally for. It is not
//     here: the tracker hands its rows straight to `Wire.RecordConn`, where
//     `-history` and the router's own reporting setting decide.
//  2. the FLEET'S BADGE, which read the live socket instead, so a six-second
//     reconnect painted the Devices page red.
//  3. the ROUTER OFFLINE / ONLINE ALERT, which did not exist. The toggle in
//     Settings has written `notifRouterStatus` since the Node app and no Go code
//     has ever read it.
//
// The field help said all three were debounced. One was.

import "log"

// connVerdict is the tracker's callback: this router is now up, or now down.
//
// ── IT MUST STAY CHEAP, AND THAT RULES OUT THE STORE ───────────────────────
//
// It fires on every successful connect, not only on a change of state, because
// `history.Connectivity` reports status on every connect — including the
// reconnects that write no row. Reading the router record here would mean
// `store.Routers()` and a scrypt decrypt per router on every reconnect of a
// flapping link. The session already holds the label and the alert switch, so
// `AlertRouter` answers from memory.
func (s *Server) connVerdict(routerID string, up bool) {
	// THE BADGE FIRST, because it is the one that must not wait on a network
	// send. `Announce` re-sends the router's status frame, which now carries the
	// debounced verdict beside the live socket state — see Session.announce.
	//
	// Re-announcing on a CONNECT is very nearly redundant: the connect loop
	// announces a moment later anyway. It is not redundant on a declared
	// outage, which arrives from the one-second ticker with nothing else
	// happening, and one path for both beats a branch that has to be right.
	s.sessions.Announce(routerID)

	if s.alerts == nil {
		return
	}
	r, label, ok := s.sessions.AlertRouter(routerID)
	if !ok {
		// Nothing holds this router any more — it was removed, or the fleet
		// holds are off under `-no-pool`. The verdict still reached the badge
		// and the history row; there is simply nobody to address an alert to.
		return
	}
	fired := s.alerts.RouterStatus(r, up)
	if len(fired) == 0 {
		return
	}
	// SAID OUT LOUD, like the dispatcher's own refusals. "The router went down
	// and I was not told" is the report this whole path exists to answer, and a
	// silent success is indistinguishable from a silent nothing.
	log.Printf("[alert] %s is %s", label,
		map[bool]string{true: "reachable again", false: "offline"}[up])
	s.dispatchFired(routerID, label, fired)
}
