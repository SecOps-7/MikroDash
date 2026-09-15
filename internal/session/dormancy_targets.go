package session

// The one place a collector is suspended or resumed by NAME.
//
// ── WHY A TABLE AT ALL ──────────────────────────────────────────────────────
//
// The live session is a JavaScript object, so the supervisor reaches a collector
// as `session[def.sessionProp]` and needs no table. Go's fields are typed and
// unaddressable by string, so the mapping has to exist somewhere; here, once,
// rather than as a `switch` repeated at every call site.
//
// The PAYLOAD side needs no entry per collector — `dormancy_payload.go` reads
// `emptyKey` off the struct by its json tag, so this table carries only what
// cannot be derived: which accessor holds which key.
//
// ── AND WHY EVERY COLLECTOR, NOT JUST THE ELIGIBLE EIGHTEEN ─────────────────
//
// `ResumeCollector` serves the page-focus path for ALL of them. A collector that
// dormancy never judges still has to pass the enabled check, and having one
// funnel for both questions is the point: the live app routes every resume
// through `_resumeCollector` precisely so "a gate that knows nothing about
// dormancy cannot undo it".

// targetKeys names every entry targets() builds.
//
// A LIST BESIDE A TABLE GOES STALE, so it exists only because the coverage gate
// needs the names without constructing a Session — and
// `TestTargetKeysMatchesTheTable` fails the moment the two disagree.
var targetKeys = []string{
	"dns", "ipAddresses", "bridges", "vlans", "wan", "packages", "routing", "ppp", "vpn",
	"rosusers", "queues", "firewall", "wifi", "capsman", "netwatch", "ifStatus",
	"topology", "wireless", "bandwidth", "talkers",
	// NOT dormancy-eligible, but the page-focus path resumes them, and
	// `ResumeCollector` is now the only way it can. They were MISSING from the
	// first version of this table and the twenty converted call sites in `ws.go`
	// would have silently stopped resuming them — an unknown key is a no-op.
	// `TestEveryCollectorWithAnAudienceIsInTheTable` is what caught it and what
	// stops it happening again.
	"conns", "dhcpLeases", "dhcpNetworks",
	// ── THE LAST TWO UNGATED COLLECTORS, ADDED 2026-09-10 (3.4) ────────────
	//
	// Both declare an audience and neither was in this table, so `applyDemand`
	// never asked about them and nothing could stop them. They ran from connect
	// to teardown on every router, for pages most viewers never open.
	//
	//	logs  held `/log/listen` open — ONE CHANNEL PER ROUTER, for the whole
	//	      life of the session, and channels are the documented bottleneck.
	//	      Its Suspend and Resume were empty methods; they are real now.
	//	ping  ran a ping loop against the router's target. Suspend stopped the
	//	      stream and not the poll, which was dormant only because nothing
	//	      could call it.
	//
	// `traffic` is the third set B stream and is deliberately NOT here: it
	// already gates itself, per interface, on `Watch`/`Unwatch` refcounts that
	// no room can express. See the note in `collect/traffic.go`.
	//
	// Neither is dormancy-eligible, and both hold their answer through a
	// suspend, so neither needs a `refresh`: the resume IS the re-probe. `logs`
	// re-reads `/log/print` on the way back up and `ping` produces its first
	// reading within an interval.
	"logs", "ping",
	// ── NO PAYLOAD AND NO PAGE, AND IT STILL BELONGS HERE ──────────────────
	//
	// `arp` emits nothing: four collectors read its index in memory. It is in
	// this table because `applyDemand` gates by KEY and `keepAliveFor` gives it
	// the rooms of the four consumers — so it runs while any of their pages is
	// open and stops when none is, which is exactly the gating every other
	// collector gets.
	"arp",
}

// collectorTarget is what the supervisor and the resume path need of one
// collector.
type collectorTarget struct {
	// last returns the collector's last payload, or a nil `any` when it has
	// produced none.
	//
	// EACH CLOSURE CONVERTS ITS OWN NIL. A typed nil pointer placed in an
	// interface is NOT a nil interface, so `return s.netwatch.Last()` would hand
	// the supervisor a non-nil `any` wrapping a nil `*NetwatchPayload` — and it
	// would judge a collector that has never reported.
	last    func() any
	suspend func()
	resume  func()
	// refresh asks this collector for a reading NOW, rather than at its next
	// interval.
	//
	// ── IT USED TO BE A TYPE ASSERTION, AND THE ASSERTION COULD NEVER PASS ──
	//
	// `probe` asked whether THIS STRUCT implemented a refresher interface. It has
	// no methods, so the answer was always no, and the refresh half of every
	// dormancy probe never ran -- the
	// collector was resumed and then waited a full cadence for its answer, which
	// on `wifi`'s 300s subscription is five minutes of a page saying nothing.
	//
	// The `prober` assertion beside it is deliberate and documented: nothing
	// implements Probe(). This one was not; the collectors DO implement
	// RefreshNow, and the assertion was simply asking the wrong object. A closure
	// asks the right one, and cannot silently stop matching.
	refresh func()
}

// targets is built per call rather than cached: the collectors are fixed for the
// life of a session, but the closures are cheap and a cached map would be one
// more thing to invalidate when a session is rebuilt.
func (s *Session) targets() map[string]collectorTarget {
	t := map[string]collectorTarget{}
	add := func(key string, last func() any, suspend, resume, refresh func()) {
		t[key] = collectorTarget{last: last, suspend: suspend, resume: resume, refresh: refresh}
	}

	add("dns", func() any {
		if p := s.dns.Last(); p != nil {
			return p
		}
		return nil
	}, s.dns.Suspend, s.dns.Resume, s.dns.RefreshNow)
	add("ipAddresses", func() any {
		if p := s.ipAddresses.Last(); p != nil {
			return p
		}
		return nil
	}, s.ipAddresses.Suspend, s.ipAddresses.Resume, s.ipAddresses.RefreshNow)
	add("bridges", func() any {
		if p := s.bridges.Last(); p != nil {
			return p
		}
		return nil
	}, s.bridges.Suspend, s.bridges.Resume, s.bridges.RefreshNow)
	add("vlans", func() any {
		if p := s.vlans.Last(); p != nil {
			return p
		}
		return nil
	}, s.vlans.Suspend, s.vlans.Resume, s.vlans.RefreshNow)
	add("wan", func() any {
		if p := s.wan.Last(); p != nil {
			return p
		}
		return nil
	}, s.wan.Suspend, s.wan.Resume, s.wan.RefreshNow)
	add("packages", func() any {
		if p := s.packages.Last(); p != nil {
			return p
		}
		return nil
	}, s.packages.Suspend, s.packages.Resume, s.packages.RefreshNow)
	add("routing", func() any {
		if p := s.routing.Last(); p != nil {
			return p
		}
		return nil
	}, s.routing.Suspend, s.routing.Resume, s.routing.RefreshNow)
	add("ppp", func() any {
		if p := s.ppp.Last(); p != nil {
			return p
		}
		return nil
	}, s.ppp.Suspend, s.ppp.Resume, s.ppp.RefreshNow)
	add("vpn", func() any {
		if p := s.vpn.Last(); p != nil {
			return p
		}
		return nil
	}, s.vpn.Suspend, s.vpn.Resume, s.vpn.RefreshNow)
	add("rosusers", func() any {
		if p := s.rosUsers.Last(); p != nil {
			return p
		}
		return nil
	}, s.rosUsers.Suspend, s.rosUsers.Resume, s.rosUsers.RefreshNow)
	add("queues", func() any {
		if p := s.queues.Last(); p != nil {
			return p
		}
		return nil
	}, s.queues.Suspend, s.queues.Resume, s.queues.RefreshNow)
	add("firewall", func() any {
		if p := s.firewall.Last(); p != nil {
			return p
		}
		return nil
	}, s.firewall.Suspend, s.firewall.Resume, s.firewall.RefreshNow)
	add("wifi", func() any {
		if p := s.wifi.Last(); p != nil {
			return p
		}
		return nil
	}, s.wifi.Suspend, s.wifi.Resume, s.wifi.RefreshNow)
	add("capsman", func() any {
		if p := s.capsman.Last(); p != nil {
			return p
		}
		return nil
	}, s.capsman.Suspend, s.capsman.Resume, s.capsman.RefreshNow)
	add("netwatch", func() any {
		if p := s.netwatch.Last(); p != nil {
			return p
		}
		return nil
	}, s.netwatch.Suspend, s.netwatch.Resume, s.netwatch.Tick)
	add("ifStatus", func() any {
		if p := s.ifStatus.Last(); p != nil {
			return p
		}
		return nil
	}, s.ifStatus.Suspend, s.ifStatus.Resume, s.ifStatus.Tick)
	add("topology", func() any {
		if p := s.topology.Last(); p != nil {
			return p
		}
		return nil
	}, s.topology.Suspend, s.topology.Resume, s.topology.Tick)
	add("wireless", func() any {
		if p := s.wireless.Last(); p != nil {
			return p
		}
		return nil
	}, s.wireless.Suspend, s.wireless.Resume, s.wireless.Tick)
	add("bandwidth", func() any {
		if p := s.bandwidth.Last(); p != nil {
			return p
		}
		return nil
	}, s.bandwidth.Suspend, s.bandwidth.Resume, s.bandwidth.Tick)
	add("talkers", func() any {
		if p := s.talkers.Last(); p != nil {
			return p
		}
		return nil
	}, s.talkers.Suspend, s.talkers.Resume, s.talkers.Tick)
	add("conns", func() any {
		if p := s.conns.Last(); p != nil {
			return p
		}
		return nil
	}, s.conns.Suspend, s.conns.Resume, s.conns.Tick)
	add("dhcpLeases", func() any {
		if p := s.dhcpLeases.Last(); p != nil {
			return p
		}
		return nil
	}, s.dhcpLeases.Suspend, s.dhcpLeases.Resume, s.dhcpLeases.RefreshNow)
	add("dhcpNetworks", func() any {
		if p := s.dhcpNetworks.Last(); p != nil {
			return p
		}
		return nil
	}, s.dhcpNetworks.Suspend, s.dhcpNetworks.Resume, s.dhcpNetworks.RefreshNow)
	// `Last()` returns a SLICE here, not a pointer, so the nil-into-interface
	// trap the other entries guard against does not apply — but an empty ring
	// must still read as "nothing reported" rather than as a report of nothing.
	add("logs", func() any {
		if h := s.logs.Last(); len(h) > 0 {
			return h
		}
		return nil
	}, s.logs.Suspend, s.logs.Resume, nil)
	add("ping", func() any {
		if p := s.ping.Last(); p != nil {
			return p
		}
		return nil
	}, s.ping.Suspend, s.ping.Resume, nil)
	add("arp", func() any {
		if ix := s.arp.Last(); ix != nil {
			return ix
		}
		return nil
	}, s.arp.Suspend, s.arp.Resume, s.arp.Tick)
	return t
}

// ResumeCollector is the live `_resumeCollector`: THE ONE PLACE A COLLECTOR IS
// RESUMED.
//
// The live comment says why it must be the only one: "Three gates now decide
// whether a collector runs — idle (nobody on this router), page rooms (nobody on
// its page) and dormancy. They are layered, not competing: dormancy is a VETO
// consulted inside _resumeCollector(), which is the only place anything is
// resumed. _idleResume() calling resume() directly is precisely what would wake
// a dormant collector on the next socket join."
//
// It also folds in the enabled check that twenty call sites in `ws.go` were each
// making for themselves — a collector the operator turned off must not come back
// because somebody opened its page.
//
// An unknown key is a no-op rather than a panic: `ws.go` names pages, and a page
// with no collector behind it is a normal thing.
func (s *Session) ResumeCollector(key string) {
	// ── AND NOTHING THIS SESSION HAS NO REASON TO RUN ─────────────────────
	//
	// Phase 4.3c. A session held only for alerting must not be talked back into
	// running `queues` by something that does not know why it exists.
	//
	// MEASURED, and it is why this clause is here rather than argued for: after
	// the connect-time prune, the command rate settled at 147-167 a minute
	// against a 119-120 baseline, and the busiest-menu list showed
	// `/queue/simple` and `/queue/tree` being read on a router nobody was
	// watching. The DORMANCY PROBE had resumed them -- `probe` calls this for any
	// collector due for one, and had no way to know the session did not want it.
	//
	// A viewer is unaffected: `Needs` returns true for everything while one is
	// present, which is the existing behaviour that page gating narrows.
	if !s.mayRun(key) {
		return
	}
	// REMEMBERED WHEN THE LINK IS NOT UP YET. THIRTEEN of the twenty-six
	// collectors open Resume() with `if ros.Connected()` and drop the request on
	// the floor with nothing to ask again -- measured, after an earlier version of
	// this comment claimed "every collector" on no evidence.
	//
	// The latch is unconditional anyway, and deliberately so: which thirteen is an
	// implementation detail of twenty-six separate files, and a collector that
	// grows such a guard later must not silently re-open this hole. Recorded
	// rather than returned early, so a resume that does useful work while
	// disconnected still runs.
	if !s.Connected() {
		s.mu.Lock()
		if s.pendingResume == nil {
			s.pendingResume = map[string]bool{}
		}
		s.pendingResume[key] = true
		s.mu.Unlock()
	}
	// A DORMANT COLLECTOR IS NOT REFUSED HERE — IT IS WOKEN, AND THEN RESUMED.
	//
	// The live app splits these: `_resumeCollector` refuses, and `_wakeForFocus`
	// is called separately by the page-focus path to pre-empt the backoff. Here
	// the two collapse, because everything that reaches this funnel wants the
	// collector running now — a page focus, demand, a replayed resume — and a
	// focus on a sleeping collector is precisely the "cheapest and most timely
	// re-probe there is".
	//
	// ── THE DORMANCY PROBE MUST NOT COME THROUGH HERE ────────────────────────
	//
	// It used to, and on a router somebody was viewing this branch reset the
	// dormancy state on every probe: backoff and all. The collector re-slept
	// about 108s later and was reset again, for ever — the viewed hAP AX3 logged
	// "queues asleep" 497 times in one day. `probe` now takes a reading and
	// leaves the state alone; see dormancy_run.go.
	//
	// AND THIS NO LONGER RETURNS after waking. It relied on the probe's second
	// pass through here to do the resume; with the probe gone, returning would
	// wake a collector a page has just asked for and leave it stopped.
	if s.dormancy != nil && s.dormancy.IsDormant(key) {
		s.WakeForFocus(key)
	}
	if t, ok := s.targets()[key]; ok {
		t.resume()
	}
}

// mayRun is whether this session may run a collector at all: the operator has not
// switched it off for this router, and something this session exists for wants
// it (see `Needs`).
//
// ONE STATEMENT OF THE RULE, for its two askers. `ResumeCollector` asks before
// starting a collector; the dormancy `probe` asks before taking a reading. The
// probe used to borrow the funnel for this, which is how it came to wake sleeping
// collectors — and a copy of the check beside it would be the second form of one
// rule that eventually drifts.
func (s *Session) mayRun(key string) bool {
	if !s.CollectorEnabled(key) {
		return false
	}
	s.mu.Lock()
	why := s.reasonsLocked()
	s.mu.Unlock()
	return Needs(key, why)
}

// SuspendCollector stops one collector by key, the mirror of ResumeCollector.
//
// ── PHASE 4.2b: WHY THIS DID NOT EXIST UNTIL NOW ───────────────────────────
//
// Suspension was always driven by a hand-written case in `ws.go` that already
// held the collector's own `Suspend` method as a closure, so a by-key form had
// no caller. `applyDemand` asks the question for EVERY collector at once and
// cannot hold twenty closures, so it needs the table -- which has carried both
// halves since 3.3 and only ever exported one.
//
// NO DORMANCY CONSULTATION, deliberately, and that asymmetry is not an
// oversight. `ResumeCollector` has to check dormancy because waking a dormant
// collector without clearing the flag leaves the supervisor to re-suspend it on
// its next tick. Suspending one that is already dormant is simply a no-op, and
// telling the supervisor about it would be this layer forming an opinion about a
// judgement that is not its own.
func (s *Session) SuspendCollector(key string) {
	if s == nil {
		return
	}
	if t, ok := s.targets()[key]; ok && t.suspend != nil {
		t.suspend()
	}
}

// TargetKeys is every collector the session can start and stop by name.
//
// Exported for `applyDemand`, which must ask its question of all of them rather
// than of the ones a switchboard happened to list. That difference is the whole
// point: a collector missing from the old list was silently never gated.
func TargetKeys() []string {
	out := make([]string, len(targetKeys))
	copy(out, targetKeys)
	return out
}

// WakeForFocus is the live `_wakeForFocus`: somebody just opened the page this
// collector feeds.
//
// "That is the cheapest and most timely re-probe there is — a user who has just
// added a netwatch host opens the NetWatch page next — so it pre-empts the
// backoff entirely."
//
// Does NOTHING for a collector that is not dormant, which is the arm a port gets
// wrong by probing unconditionally. Called from `ResumeCollector`, because focus
// is the only thing that resumes and the two questions are asked at the same
// moment.
func (s *Session) WakeForFocus(key string) {
	if s.dormancy == nil {
		return
	}
	s.applyDormancy(s.dormancy.WakeForFocus(key), s.targets())
}

// DormantCollectors is the current sleeping set, for the initial
// `collection:status` a newly attached viewer needs.
//
// Empty, never nil: the payload is always an array, and `dormant: null` makes
// `Array.isArray(st.dormant)` false in `applyCollectionStatus`, which then
// returns WITHOUT clearing the marks a previous payload left.
func (s *Session) DormantCollectors() []string {
	if s.dormancy == nil {
		return []string{}
	}
	return s.dormancy.Dormant()
}

// replayResumes re-applies the page-focus resumes that arrived before the router
// connection was up.
//
// Drained rather than kept: a replayed resume either takes effect now or the
// collector is no longer wanted, and in the second case the dormancy supervisor
// is what puts it back to sleep -- the same safety net the reconnect path relies
// on when it restarts every page-gated collector unconditionally.
func (s *Session) replayResumes() {
	s.mu.Lock()
	keys := make([]string, 0, len(s.pendingResume))
	for k := range s.pendingResume {
		keys = append(keys, k)
	}
	s.pendingResume = nil
	s.mu.Unlock()

	// Deterministic order, so a failure names the same collector twice. Sorted in
	// place rather than importing sort for one call in a file that has no imports.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	for _, k := range keys {
		s.ResumeCollector(k)
	}
}
