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
	"dns", "bridges", "vlans", "wan", "packages", "routing", "ppp", "vpn",
	"rosusers", "queues", "firewall", "wifi", "capsman", "netwatch", "ifStatus",
	"topology", "wireless", "bandwidth", "talkers",
	// NOT dormancy-eligible, but the page-focus path resumes them, and
	// `ResumeCollector` is now the only way it can. They were MISSING from the
	// first version of this table and the twenty converted call sites in `ws.go`
	// would have silently stopped resuming them — an unknown key is a no-op.
	// `TestEveryKeyWsPassesIsInTheTable` is what caught it and what stops it
	// happening again.
	"conns", "dhcpLeases", "dhcpNetworks",
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
}

// targets is built per call rather than cached: the collectors are fixed for the
// life of a session, but the closures are cheap and a cached map would be one
// more thing to invalidate when a session is rebuilt.
func (s *Session) targets() map[string]collectorTarget {
	t := map[string]collectorTarget{}
	add := func(key string, last func() any, suspend, resume func()) {
		t[key] = collectorTarget{last: last, suspend: suspend, resume: resume}
	}

	add("dns", func() any {
		if p := s.dns.Last(); p != nil {
			return p
		}
		return nil
	}, s.dns.Suspend, s.dns.Resume)
	add("bridges", func() any {
		if p := s.bridges.Last(); p != nil {
			return p
		}
		return nil
	}, s.bridges.Suspend, s.bridges.Resume)
	add("vlans", func() any {
		if p := s.vlans.Last(); p != nil {
			return p
		}
		return nil
	}, s.vlans.Suspend, s.vlans.Resume)
	add("wan", func() any {
		if p := s.wan.Last(); p != nil {
			return p
		}
		return nil
	}, s.wan.Suspend, s.wan.Resume)
	add("packages", func() any {
		if p := s.packages.Last(); p != nil {
			return p
		}
		return nil
	}, s.packages.Suspend, s.packages.Resume)
	add("routing", func() any {
		if p := s.routing.Last(); p != nil {
			return p
		}
		return nil
	}, s.routing.Suspend, s.routing.Resume)
	add("ppp", func() any {
		if p := s.ppp.Last(); p != nil {
			return p
		}
		return nil
	}, s.ppp.Suspend, s.ppp.Resume)
	add("vpn", func() any {
		if p := s.vpn.Last(); p != nil {
			return p
		}
		return nil
	}, s.vpn.Suspend, s.vpn.Resume)
	add("rosusers", func() any {
		if p := s.rosUsers.Last(); p != nil {
			return p
		}
		return nil
	}, s.rosUsers.Suspend, s.rosUsers.Resume)
	add("queues", func() any {
		if p := s.queues.Last(); p != nil {
			return p
		}
		return nil
	}, s.queues.Suspend, s.queues.Resume)
	add("firewall", func() any {
		if p := s.firewall.Last(); p != nil {
			return p
		}
		return nil
	}, s.firewall.Suspend, s.firewall.Resume)
	add("wifi", func() any {
		if p := s.wifi.Last(); p != nil {
			return p
		}
		return nil
	}, s.wifi.Suspend, s.wifi.Resume)
	add("capsman", func() any {
		if p := s.capsman.Last(); p != nil {
			return p
		}
		return nil
	}, s.capsman.Suspend, s.capsman.Resume)
	add("netwatch", func() any {
		if p := s.netwatch.Last(); p != nil {
			return p
		}
		return nil
	}, s.netwatch.Suspend, s.netwatch.Resume)
	add("ifStatus", func() any {
		if p := s.ifStatus.Last(); p != nil {
			return p
		}
		return nil
	}, s.ifStatus.Suspend, s.ifStatus.Resume)
	add("topology", func() any {
		if p := s.topology.Last(); p != nil {
			return p
		}
		return nil
	}, s.topology.Suspend, s.topology.Resume)
	add("wireless", func() any {
		if p := s.wireless.Last(); p != nil {
			return p
		}
		return nil
	}, s.wireless.Suspend, s.wireless.Resume)
	add("bandwidth", func() any {
		if p := s.bandwidth.Last(); p != nil {
			return p
		}
		return nil
	}, s.bandwidth.Suspend, s.bandwidth.Resume)
	add("talkers", func() any {
		if p := s.talkers.Last(); p != nil {
			return p
		}
		return nil
	}, s.talkers.Suspend, s.talkers.Resume)
	add("conns", func() any {
		if p := s.conns.Last(); p != nil {
			return p
		}
		return nil
	}, s.conns.Suspend, s.conns.Resume)
	add("dhcpLeases", func() any {
		if p := s.dhcpLeases.Last(); p != nil {
			return p
		}
		return nil
	}, s.dhcpLeases.Suspend, s.dhcpLeases.Resume)
	add("dhcpNetworks", func() any {
		if p := s.dhcpNetworks.Last(); p != nil {
			return p
		}
		return nil
	}, s.dhcpNetworks.Suspend, s.dhcpNetworks.Resume)
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
	if !s.CollectorEnabled(key) {
		return
	}
	// REMEMBERED WHEN THE LINK IS NOT UP YET, because every collector's Resume()
	// opens with `if ros.Connected()` and would otherwise drop this on the floor
	// with nothing to ask again. Recorded rather than returned early: if a
	// collector's resume ever does useful work while disconnected, it still runs.
	if !s.Connected() {
		s.mu.Lock()
		if s.pendingResume == nil {
			s.pendingResume = map[string]bool{}
		}
		s.pendingResume[key] = true
		s.mu.Unlock()
	}
	// A DORMANT COLLECTOR IS NOT REFUSED HERE — IT IS WOKEN.
	//
	// The live app splits these: `_resumeCollector` refuses, and `_wakeForFocus`
	// is called separately by the page-focus path to pre-empt the backoff. This
	// port has ONE caller of the funnel — page focus — so the two collapse: a
	// focus on a sleeping collector is precisely the "cheapest and most timely
	// re-probe there is".
	//
	// The veto still exists and still matters: `WakeForFocus` returns an empty
	// plan for a collector that is awake, and the supervisor's own wake path
	// calls this AFTER clearing the flag, so it falls through. What cannot
	// happen is the thing the live comment warns about — a resume that neither
	// consults nor clears the dormancy state, leaving the supervisor to
	// re-suspend on its next tick.
	if s.dormancy != nil && s.dormancy.IsDormant(key) {
		s.WakeForFocus(key)
		return
	}
	if t, ok := s.targets()[key]; ok {
		t.resume()
	}
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
