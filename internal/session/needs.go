package session

import (
	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// Which collectors a session runs, given why it is being kept alive.
//
// ── PHASE 4.3c: THE COST THAT MAKES THE MERGE A REGRESSION IF IGNORED ───────
//
// 4.3 replaces the background pools with sessions held for a non-viewer reason.
// Done naively that is a LOSS, and a large one. Measured on 2026-09-08:
//
//	the alert pool holding one unwatched router   119-120 commands/minute
//	a session's connect block                     15 collectors, against the
//	                                              pool's 7
//
// The nine extra are `bridges`, `dhcpLeases`, `dhcpNetworks`, `dns`, `firewall`,
// `logs`, `talkers`, `vlans` and `wan` -- and `wan` polls at two seconds,
// `talkers` at three, `bridges` at five. Roughly seventy-six extra commands a
// minute for a router NOBODY IS WATCHING, on an app whose documented bottleneck
// is concurrent channels on the MikroTik.
//
// So a session held only for alerting must run only what alerting needs. The
// collector set follows the HOLDERS, not the fact that a session exists.
//
// ── PURE, SO THE DECISION CAN BE TESTED WITHOUT A ROUTER ────────────────────
//
// Rows in, verdict out -- the same shape as the write guards and
// `internal/dormancy`, and for the same reason: every other part of this needs a
// live connection to exercise, and this is the part that decides what the app
// costs.

// Reasons is why a session is being kept alive. A viewer is one reason; the
// named holds are the others.
type Reasons struct {
	// Viewer is true while at least one browser has this router selected. It
	// wants everything, because it can navigate to any page.
	Viewer bool
	// Alerts is true for a router whose rules must be evaluated.
	Alerts bool
	// History is true for a router whose traffic and ping are being recorded.
	History bool
	// Devices is true while the Devices page is open, which reads a fixed set
	// per router.
	Devices bool
	// Warm keeps the CONNECTION and runs no collectors at all.
	//
	// ── WHY A REASON THAT WANTS NOTHING ─────────────────────────────────────
	//
	// The Devices page opened with a fleet of red "Offline" cards until the
	// alert pool started dialling every router at startup -- a defect the
	// operator reported twice. What fixed it was not data: it was the
	// CONNECTION, so a card could say "up" the moment the page rendered instead
	// of waiting seconds for a dial.
	//
	// That pool built no collectors at all for a router with alerts and
	// reporting off -- verified live, `roslimit` reported "across 1 router(s)"
	// with three more connected. So the thing worth keeping is a socket and
	// nothing else, and this is that, expressed as a reason rather than as a
	// second engine.
	//
	// It is what let `internal/alertpool` be deleted without the red cards
	// coming back.
	Warm bool
}

// historyFeeds are the two collectors continuous history is written from.
//
// `traffic` supplies per-interface throughput and `ping` the latency series;
// `internal/history` buckets both into the minute rows the tables take. Nothing
// else in the app writes a history row from a collector payload.
var historyFeeds = []string{"traffic", "ping"}

// devicesFeeds are what the Devices page reads per router. It matches what
// `internal/routers` builds for its overview sessions.
var devicesFeeds = []string{"system", "ifStatus", "traffic", "ping", "dhcpLeases"}

// Needs reports whether a collector should run, given why the session is alive.
//
// A VIEWER WANTS EVERYTHING. Not because every page is open, but because any of
// them can be, and the page gates decide the rest from there — that is the
// existing behaviour and this must not change it.
//
// Every other reason wants a NAMED set, because it consumes specific payloads
// and nothing else. That is the whole saving: an alerting router runs six
// collectors instead of fifteen.
func Needs(key string, why Reasons) bool {
	if why.Viewer {
		return true
	}
	// `Warm` is deliberately absent from everything below. It holds the
	// connection and wants no collector, which is the whole point of it.
	if why.Alerts {
		for _, k := range AlertFeeds {
			if k == key {
				return true
			}
		}
	}
	if why.History {
		for _, k := range historyFeeds {
			if k == key {
				return true
			}
		}
	}
	if why.Devices {
		for _, k := range devicesFeeds {
			if k == key {
				return true
			}
		}
	}
	return false
}

// reasons reads the session's current holders. The caller holds s.mu.
func (s *Session) reasonsLocked() Reasons {
	return Reasons{
		Viewer:  s.refs > 0,
		Alerts:  s.holds["alerts"],
		History: s.holds["history"],
		Devices: s.holds["devices"],
		Warm:    s.holds["warm"],
	}
}

// applyReasons brings the running collector set into line with why this session
// is alive.
//
// ── IT DOES NOTHING WHILE A VIEWER IS PRESENT, AND THAT IS THE POINT ────────
//
// `Needs` says a viewer wants everything, which is a statement about what is
// ALLOWED to run, not what should be running now. Page gating decides the rest,
// and it is the reason an idle browser does not poll twenty-two collectors.
// Resuming everything here because a viewer exists would undo all of it.
//
// So the prescriptive case is the other one: with NO viewer, the session runs
// exactly the union of what its holders need, and nothing else. That is where
// the saving is — an alerting router runs six collectors instead of fifteen.
//
// IDEMPOTENT, and called from every transition rather than one: connect, a
// viewer arriving or leaving, a hold taken or dropped. The order those happen in
// is racy — a Retain releases its own viewer reference while the connect
// goroutine is still dialling — so this converges rather than being sequenced.
// NewForTest builds a Session that knows its hub and its router and nothing else.
//
// ── A SEAM, AND WHY THE RULE MOVING HERE REQUIRED ONE ──────────────────────
//
// `Wants` reads room occupancy, so it needs the hub and the router id. A real
// Session gets both from `Manager.Acquire`, which dials a router; a test cannot.
// `internal/server`'s demand tests used to build `&session.Session{}` and pass the
// router id alongside, because the rule lived over there.
//
// The same kind of seam as `Server.idleGrace` and `Cache.StreamTimings`: nothing
// in the binary calls it, and the alternative was to keep the rule in two places
// so that both sides could test it — which is what phase 6.3 exists to undo.
func NewForTest(h *hub.Hub, routerID string) *Session {
	return &Session{h: h, RouterID: routerID}
}

// NewForTestWithExec is NewForTest with every command answered by exec, so a
// test can drive a write path against a scripted router.
func NewForTestWithExec(h *hub.Hub, routerID string,
	exec func(routeros.Cmd) ([]routeros.Reply, error)) *Session {
	s := &Session{h: h, RouterID: routerID, execForTest: exec}
	// IT HAS NO COLLECTORS, so it says so: every collector a write path would
	// refresh reads as switched off, and nothing reaches a nil one.
	s.eff.Store(&collection.Resolved{Enabled: map[string]bool{
		"dns": false, "bridges": false, "vlans": false, "firewall": false,
		"wifi": false, "capsman": false, "ppp": false, "ifStatus": false, "netwatch": false,
	}})
	return s
}

// Wants is THE collector gate, and there is one of it.
//
// ── PHASE 6.3: THIS RULE WAS WRITTEN TWICE ─────────────────────────────────
//
// `internal/server`'s `wantsCollector` asked "is anybody in a room it feeds, or
// does alerting or a hold need it". `applyReasons` here asked "does alerting or a
// hold need it" and ran when there was no viewer — at which point the room term
// is empty and the two questions are the SAME question.
//
// Step 3.4 said the idle gate becomes demand. What happened instead was that the
// page-room half was delivered, a justification was written for keeping this
// half, and the step was recorded as done. It was not: two statements of one rule
// is exactly what this rewrite exists to remove, and the drift went unnoticed
// because each step was checked against the step before it rather than against
// the end state.
//
// So the rule lives here, where the session already holds the hub and its router
// id, and `internal/server` delegates to it. The two callers differ only in when
// they act: the server defers a suspend by a grace period, because a page refresh
// empties every room and refills it a second later; this one runs at the end of
// the session's own idle grace or when a hold changes, which is already late.
func (s *Session) Wants(key string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	why := s.reasonsLocked()
	s.mu.Unlock()
	// ── THE VIEWER TERM IS THE ROOMS' QUESTION, NOT THIS ONE'S ─────────────
	//
	// `Needs` returns true for EVERYTHING while a viewer is present, because it
	// answers "what is this session allowed to run". Asked here unmodified it
	// would want every collector for any browser and undo page gating entirely.
	//
	// The first version of 6.3 handled that with `Needs(key, why) && !why.Viewer`
	// — which SKIPS the holds question whenever a viewer exists, rather than
	// asking it without the viewer term. On the router you are looking at, the
	// alert and history feeds were then gated purely on rooms: opening the
	// Devices page suspended `ifStatus`, and with it four of the six alert rules,
	// on the one router most likely to be watched.
	//
	// `NeededForHolds` had it right and its comment said so — "the viewer half is
	// the caller's question now" — and the rule this replaced called BOTH it and
	// the rooms. Found by a Devices-page bug the operator reported: no WAN RX/TX.
	why.Viewer = false
	if Needs(key, why) {
		return true
	}
	return s.roomsOccupied(collect.DemandRooms(key))
}

// roomsOccupied reports whether any of these rooms still has a viewer.
//
// THE ROOM NAMES ARE PER ROUTER, and `RoomFor` is the one place that convention
// is written down — a joiner using a different prefix sits in a room nobody sends
// to, and the only symptom is a chart that stays empty.
func (s *Session) roomsOccupied(sets ...collect.Rooms) bool {
	if s.h == nil {
		return false
	}
	for _, set := range sets {
		for _, r := range set {
			if r == "" {
				continue
			}
			if s.h.Occupants(RoomFor(s.RouterID, r)) > 0 {
				return true
			}
		}
	}
	return false
}

// applyDemand brings every collector into line with `Wants`.
//
// ── IT STILL RETURNS EARLY FOR A VIEWER, AND THAT IS NOT THE DUPLICATION ───
//
// The first version of 6.3 removed that early return on the grounds that `Wants`
// now covers the viewer case too. A gate caught it, and the gate was right:
// THE ROOMS ARE NOT JOINED YET when this runs from the connect path. A browser
// selects a router, the session dials, the connect block starts the collectors,
// and only then does `page:focus` arrive and put the viewer in a room. Applying
// room-demand in that window suspends everything connect has just started, and
// the page waits for the next focus to bring it back.
//
// So the early return is not a second copy of the rule — it says WHICH APPLIER
// owns the viewer case, and the answer is `internal/server`, which is the side
// that hears about rooms changing. What 6.3 removed is the duplicated RULE:
// `Wants` is the only statement of it now, and both appliers ask it.
func (s *Session) applyDemand() {
	// NOTHING TO PRUNE BEFORE THE LINK IS UP. A hold taken while the session is
	// still dialling finds no collectors running, and the connect path calls this
	// itself once they are -- which is what makes the racy order between Retain
	// and the connect goroutine converge instead of needing to be sequenced.
	if s == nil || !s.Connected() {
		return
	}
	s.mu.Lock()
	why := s.reasonsLocked()
	s.mu.Unlock()
	if why.Viewer {
		return
	}
	targets := s.targets()
	for _, key := range targetKeys {
		t, ok := targets[key]
		if !ok {
			continue
		}
		if s.Wants(key) {
			// THROUGH THE FUNNEL, so the enabled check and the dormancy veto
			// still apply. A collector the operator turned off for this router
			// must not come back because alerting wants it.
			s.ResumeCollector(key)
			continue
		}
		t.suspend()
	}
}

// NeededForHolds reports whether a NON-VIEWER reason wants this collector.
//
// ── PHASE 4.2b: THE VIEWER'S HALF MOVED OUT ────────────────────────────────
//
// `Needs` answers "given why this session is alive, does this collector run",
// and its first line is `if why.Viewer { return true }` -- a viewer wants
// everything, because until now a viewer's demand had no finer expression than
// "somebody is looking at this router".
//
// 4.2b gives it one: a viewer wants the collectors whose ROOMS they occupy. So
// the demand rule asks occupancy for the viewer half and this for the rest, and
// the two are no longer the same question.
//
// THE HOLDS ARE UNCHANGED and deliberately so. Alerting is not in a room and
// never will be; a session held for history or warm has no viewer at all. Those
// consumers cannot be expressed as occupancy and must not be.
func (s *Session) NeededForHolds(key string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	why := s.reasonsLocked()
	s.mu.Unlock()
	// The viewer half is the caller's question now, not this one's.
	why.Viewer = false
	return Needs(key, why)
}
