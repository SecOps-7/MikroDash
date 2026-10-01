package server

import (
	"log"

	"mikrodash/internal/collection"
	"mikrodash/internal/routers"
	"mikrodash/internal/store"
)

// Holding a session for every router that needs one — the port of
// `_syncAlertSessions`, and what is left of it after phase 4.3.
//
// ── THIS FILE USED TO WIRE A SECOND POOL ───────────────────────────────────
//
// The live app runs TWO background pools. `internal/routers.Pool` is
// `overviewSessions`, gated on the Devices page. `alertSessions` was the other,
// ALWAYS ON: one session per non-disabled router, whatever anyone is looking at.
// This port had `internal/alertpool` for it. Its absence had cost two things,
// and they are worth keeping written down because they are what any replacement
// must still deliver:
//
//	alerts    `alertwire.Evaluate` is reached from ONE place — the emit closure
//	          in session.go — so with `-alert-dispatch` on, alerts fired only for
//	          the router on screen. An operator would believe the fleet covered.
//	status    non-active routers read Offline until the Devices page was opened,
//	          which is how the operator noticed on 2026-08-29.
//
// ── AND NOW A `session.Session` DELIVERS BOTH ──────────────────────────────
//
// A Session already feeds the alert evaluator and the history recorder at that
// one seam. The only reason a second pool existed is that a Session died when
// its last viewer left. Phase 4.3 gave the manager NAMED HOLDS, so a router that
// needs alerting, recording, or merely a socket is held instead — and the pool
// became a duplicate implementation of a thing this app already had.
//
// `session.Needs` is what keeps that from being a regression: a held session
// runs only what its holders read, and a WARM one runs no collectors at all.
// Measured 2026-09-08, one unwatched router: the pool cost 119-120 commands a
// minute and a held session costs ~127, against the 264-287 an unpruned session
// would.
//
// ── AND THE OVERVIEW POOL WENT THE SAME WAY, 2026-10-01 ────────────────────
//
// `internal/routers.Pool` was the other duplicate: one connection per router the
// Devices page could see, built when somebody opened the page and released after
// they left. While it existed, `warm` was DROPPED for every router the pool had
// answered for - so the pool, which never fed `connTrack`, became the only thing
// holding those routers. They lost their debounced online verdict and wrote no
// connectivity rows, and once the page closed and the pool released them they
// were held by nothing at all. Measured: three routers of four on the dev
// install had no connectivity row in seven days.
//
// Now every enabled router is held `warm` from startup, the session is the only
// connection, and the Devices page reads it. `-no-pool` keeps its name for the
// operators who pass it, and means what it always meant from the outside: do
// not hold connections to routers nobody is watching.

// syncFleetHolds is `_syncAlertSessions()`: hold a session for every
// non-disabled router that needs one, and let go of the ones that do not.
//
// Called wherever the fleet or who is watching it changes: a router added,
// edited, enabled, disabled or deleted, the Devices page focused or left, a
// peek opened or closed. The holds are derived on every call rather than
// tracked, because a second record of who is watching what drifts from the
// first.
func (s *Server) syncFleetHolds() {
	if !s.holdFleet || s.store == nil {
		return
	}
	all, errs := s.store.Routers()
	for _, e := range errs {
		log.Printf("[holds] reading the fleet: %v", e)
	}

	// ── THE ACTIVE ROUTER IS *NOT* TREATED SPECIALLY, AND THAT IS A DELIBERATE
	//    DIVERGENCE ─────────────────────────────────────────────────────────
	//
	// `_syncAlertSessions` passes `activeRouterId` and skips it, because the live
	// app ALWAYS holds a session for the active router — `_routerSessions` has
	// one whether or not a browser is open, so skipping it costs nothing.
	//
	// THIS PORT HAS NO SUCH SESSION. `session.Manager.Acquire` is ref-counted:
	// the session exists while somebody is looking and is torn down when the last
	// viewer leaves. Skipping the active router by id therefore leaves it covered
	// by NOTHING the moment the last browser closes — no status, no alert
	// evaluation, on the one router the install is pointed at.
	//
	// Measured 2026-08-29: with no browser open, `/healthz` reported the active
	// router down because neither the session nor the pool held it.

	// THE SAME RESOLUTION THE PAGE USES (`routers.DefaultIfFor`). Taken raw, a
	// router with no default interface recorded an empty traffic stream — and
	// these are the sessions that run when nobody is watching, so their history
	// simply did not exist.
	global := s.globalDefaultIf()

	for _, r := range all {
		s.declareRecordedInterfaces(r.ID, store.RecordedIfacesFor(r, routers.DefaultIfFor(r.DefaultIf, global)))
		s.declareReporting(r)
		s.declareConnThreshold(r)
		// THE DECLARATIONS ABOVE ARE NOT GATED ON THE MANAGER, and that split is
		// deliberate. They tell the recorder what a series contains, which is
		// true whether or not anything is holding a session; folding them behind
		// the same nil check silently stopped a whole sync from declaring
		// anything, and the only symptom was history recording every interface
		// instead of the default one.
		if s.sessions != nil {
			s.holdOne(r)
		}
	}
}

// holdOne applies the three holds for one router.
//
// BOTH DIRECTIONS MATTER. A router that loses alerting keeps a held session for
// ever unless the hold is dropped, and a held session is a connection and a
// collector set — the exact cost phase 4.3 exists to remove.
func (s *Server) holdOne(r store.Router) {
	for _, h := range []struct {
		reason string
		want   bool
	}{
		{"alerts", r.AlertsEnabled && !r.Disabled},
		// PER-ROUTER RECORDING. `store.ReportingOn` is the one reader of that
		// setting; asking the flag directly is how a router whose reporting was
		// never set silently stopped recording.
		{"history", store.ReportingOn(r) && !r.Disabled},
		// ── THE CONNECTION, FOR EVERY ENABLED ROUTER ────────────────────
		//
		// EVERY enabled router, unconditionally, since 2026-10-01. This hold is
		// what keeps each router observed: its session feeds `connTrack`, so the
		// Online/Offline verdict stays debounced and every outage is written to
		// `connectivity_events` - which the Devices page's connectivity strip
		// draws. It used to be skipped once the overview pool had answered for a
		// router, and that pool never fed `connTrack`; see the header.
		//
		// A warm hold runs NO collectors — see `session.Reasons.Warm`. The cost
		// is one idle API login per enabled router, and no commands.
		{"warm", !r.Disabled},
		// ── THE DEVICES PAGE IS A CONSUMER, AND IT NEVER SAID SO ──────────
		//
		// `Reasons.Devices` and `session.devicesFeeds` have existed since 4.3
		// deleted the pools, and NOTHING EVER TOOK THIS HOLD. The field was read
		// by `reasonsLocked`, the feed list was consulted by `Needs`, and the
		// whole path was dead — declared and never filled, which is the same
		// shape as `topology.ARPIP` and reads exactly as well.
		//
		// It cost nothing while `ifStatus` ran from connect on every session. It
		// started costing when 4.2b gated `ifStatus` on demand: a router nobody
		// is viewing then has no reason to run it, so the page's WAN RX/TX column
		// was empty for every device. Reported by the operator; measured as 3 of
		// 4 routers showing null, and the fourth showing a frozen reading from
		// the single tick its poll-mode start had managed before it was
		// suspended.
		{"devices", !r.Disabled && s.devicesWatched()},
	} {
		if !h.want {
			s.sessions.Drop(r.ID, h.reason)
			continue
		}
		if _, err := s.sessions.Retain(r.ID, h.reason); err != nil {
			// A router that cannot be dialled is not a reason to fail the sync:
			// the next sync tries again.
			log.Printf("[holds] %q for %s: %v", h.reason, r.Label, err)
		}
	}
}

// collectionRaw is the router's #105 block as stored, or nil.
func collectionRaw(r store.Router) []byte {
	if len(r.Collection) == 0 {
		return nil
	}
	return r.Collection
}

// Unused-import guard: `collection` is referenced by the doc above and by the
// session manager this file drives. Kept explicit so a reader looking for where
// #105 is applied finds it named here.
var _ = collection.Resolve

// activeRouterID reads the install's active router.
//
// Its own function because more than one caller needs it, and a second inline
// settings read is a second thing to get wrong. It no longer decides who
// records — that was `SetHistoryRouter`, and it is each router's own setting
// now — but it still answers "which router is this install pointed at".
func (s *Server) activeRouterID() string {
	if s.store == nil {
		return ""
	}
	cfg, err := s.store.Settings()
	if err != nil {
		return ""
	}
	id, _ := cfg["activeRouterId"].(string)
	return id
}
