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
// ── IT SHARES `-no-pool` WITH THE OVERVIEW POOL, DELIBERATELY ──────────────
//
// Live gates the two separately, because one is bound to a page and the other is
// not. Here they share a switch because the switch means one thing — "do not
// hold background connections to routers nobody is watching" — and somebody
// passing it wants exactly that from both. Documented rather than assumed; if
// finer control is ever needed, splitting the flag is a small change and this
// comment is where to start.

// warmExclusions is the set of routers that need no WARM hold, because
// something else is already answering for them.
//
// A method rather than a local, so the handover rule below can be asserted
// without driving a whole sync against a fleet.
//
// ── ONLY WARM IS EXCLUDED, AND THAT IS THE WHOLE DISTINCTION ───────────────
//
// `alerts` and `history` are holds for WORK: collectors this app must run
// wherever the router is otherwise held, because the overview pool does not run
// them. `warm` is a hold for a FACT — is this router up — and any source that
// can state the fact makes it redundant.
func (s *Server) warmExclusions() map[string]bool {
	excluded := map[string]bool{}

	// ── THE OVERVIEW POOL'S ROUTERS — ONCE IT HAS ANSWERED FOR THEM ─────────
	//
	// Excluded when the overview session has ANSWERED, not merely when it
	// exists. `syncPool` builds a session per router and `syncFleetHolds` runs
	// immediately after it, so excluding on existence dropped a live, connected
	// hold and handed the router to a pool that had not dialled yet. The Devices
	// page then lost the only source that could answer for those routers, so
	// they showed as not-yet-known for the ~2s the overview pool took to connect
	// — measured on this install, cold open, 130ms to 2150ms. Before `Known`
	// existed they showed as OFFLINE, in red, which is the defect the operator
	// reported twice.
	//
	// The overlap this costs is bounded and short. `Known` goes true on the
	// first connect AND on the first error, so a router that is genuinely down
	// is excluded as soon as the overview pool finds that out, rather than being
	// held by both for ever.
	//
	// THE INTERACTIVE-SESSION CLAUSE IS GONE, and its absence is not an
	// oversight. It excluded routers with a live `Session` because a second pool
	// would have opened a second socket to them. A hold is taken on THAT SAME
	// session — `Retain` adds a reason to it, it does not build anything — so
	// there is nothing left to exclude it from.
	if s.pool != nil {
		for _, sum := range s.pool.Summaries() {
			if sum.Known {
				excluded[sum.RouterID] = true
			}
		}
	}
	return excluded
}

// syncFleetHolds is `_syncAlertSessions()`: hold a session for every
// non-disabled router that needs one, and let go of the ones that do not.
//
// Called from the same places as `syncPool`, because the two answer the same
// question — "who is watching what" — and a change that affects one affects the
// other. The exclusion set is derived on every call rather than tracked, for the
// reason `syncPool` gives: a second record of who is watching what drifts from
// the first.
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

	// THE SAME RESOLUTION THE POOL AND THE PAGE USE. Taken raw, a router with
	// no default interface recorded an empty traffic stream — and these are the
	// sessions that run when nobody is watching, so their history simply did not
	// exist. See `syncPool`.
	global := s.globalDefaultIf()
	warmSkip := s.warmExclusions()

	for _, r := range all {
		s.declareRecordedInterfaces(r.ID, routers.DefaultIfFor(r.DefaultIf, global))
		s.declareReporting(r)
		s.declareConnThreshold(r)
		// THE DECLARATIONS ABOVE ARE NOT GATED ON THE MANAGER, and that split is
		// deliberate. They tell the recorder what a series contains, which is
		// true whether or not anything is holding a session; folding them behind
		// the same nil check silently stopped a whole sync from declaring
		// anything, and the only symptom was history recording every interface
		// instead of the default one.
		if s.sessions != nil {
			s.holdOne(r, warmSkip[r.ID])
		}
	}
}

// holdOne applies the three holds for one router.
//
// BOTH DIRECTIONS MATTER. A router that loses alerting keeps a held session for
// ever unless the hold is dropped, and a held session is a connection and a
// collector set — the exact cost phase 4.3 exists to remove.
func (s *Server) holdOne(r store.Router, warmCovered bool) {
	for _, h := range []struct {
		reason string
		want   bool
	}{
		{"alerts", r.AlertsEnabled && !r.Disabled},
		// PER-ROUTER RECORDING. `store.ReportingOn` is the one reader of that
		// setting; asking the flag directly is how a router whose reporting was
		// never set silently stopped recording.
		{"history", store.ReportingOn(r) && !r.Disabled},
		// ── THE CONNECTION, FOR THE DEVICES PAGE ────────────────────────
		//
		// Every enabled router nothing else answers for. This is the last thing
		// the deleted `alertpool` package was doing: holding a socket so the page
		// can say "up" the moment it renders, instead of a fleet of red Offline
		// cards while the overview pool dials.
		//
		// A warm hold runs NO collectors — see `session.Reasons.Warm` — which is
		// exactly what the pool ran for these routers: `buildCollectors`
		// returned before making any unless the router had alerting or reporting
		// on, and those are the two rows above.
		{"warm", !r.Disabled && !warmCovered},
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
