package alertpool

import (
	"log"
	"slices"
	"sync"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/routeros"
)

// Conn is the pool's view of a RouterOS connection.
//
// `Connected` is how a DROP is noticed, for the same reason `internal/routers`
// gives: the live pool learns from the driver's `close` event and go-routeros
// has none, so the loop watches this instead. A session that lost its socket and
// still read "connected" would report a dead router as Online, which is worse
// than reporting it offline.
type Conn interface {
	Do(routeros.Cmd) ([]routeros.Reply, error)
	// Stream is here for ONE collector. `/ping` is a streaming command, so
	// `collect.NewPing` takes a Streamer rather than a Reader, and a pool that
	// could only `Do` would have to drop ping alerts — the commonest alert type
	// there is. The other five need only Do.
	Stream(routeros.Cmd, func(routeros.Reply)) (func(), error)
	Connected() bool
	Close() error
}

// Dialer opens a connection. `routeros.Dial` satisfies it once wrapped.
//
// INJECTED so the pool is testable without hardware. Every rule below — the
// retry, the status transitions, the teardown — is reachable from a test with a
// fake dialer, which is the only way this half gets covered at all.
type Dialer func(routeros.Config) (Conn, error)

// StatusHook is called when a router's Online/Offline state CHANGES.
//
// ── ON CHANGE, NOT ON EVERY DIAL ──────────────────────────────────────────
//
// The live pool keeps `_statusMap` and the page re-renders from it; a hook that
// fired on every retry would push a `router:status` frame every five seconds per
// unreachable router, to every browser. Transition-only is also what
// `internal/history.Connectivity` needs — its own comment says "unconditional
// writes on every reconnect inflate uptime for a flapping link".
type StatusHook func(routerID string, connected bool)

// Pool holds one session per router nobody is watching.
// RecordHook receives the payloads continuous history is built from.
type RecordHook func(routerID, event string, payload any)

type Pool struct {
	dial   Dialer
	retry  time.Duration
	status StatusHook
	// on receives every collector payload, for the alert evaluator. Nil is
	// legitimate: a pool built without it is status-only for the whole fleet,
	// which is what a deployment with alerting off wants.
	on EventHook
	// settings is the install's merged settings, for resolving each router's
	// effective collection config (#105).
	settings map[string]any

	// record is where continuous history goes. WHICH routers it covers is each
	// router's own `ReportingEnabled`, read at build time. Zero unless wired.
	//
	// ── WHY HERE AND NOT ON `internal/routers.Pool` ───────────────────────
	//
	// THIS is the always-on pool: `server.go` syncs it at startup, so it holds a
	// connection to every enabled router whether or not anyone is looking.
	// `routers.Pool` is synced from the Devices page and the routers API only,
	// so it idles until somebody looks at something — measured 2026-08-30, when
	// history wired there recorded nothing after a restart with no browser.
	record RecordHook
	// pendingRebuild names routers whose ENDPOINT changed and whose sessions must
	// therefore be rebuilt on the next Sync. A flag change does not come through
	// here — `PlanSync` sees those in the record and plans the rebuild itself.
	pendingRebuild map[string]bool

	mu       sync.Mutex
	sessions map[string]*poolSession
	// state is the last status reported per router, so the hook fires on change
	// only. Kept separately from `sessions` because a torn-down session must not
	// take its last known state with it — see Sync.
	state map[string]bool
}

func New(dial Dialer, retry time.Duration, status StatusHook, on EventHook,
	settings map[string]any) *Pool {
	if retry <= 0 {
		retry = 5 * time.Second
	}
	return &Pool{
		dial: dial, retry: retry, status: status, on: on, settings: settings,
		sessions: map[string]*poolSession{},
		state:    map[string]bool{},
	}
}

type poolSession struct {
	r Router

	mu   sync.Mutex
	conn Conn

	// The six collectors the live pool runs when alertsEnabled, and nil when it
	// is not — a status-only session "needs no collectors since the ROS
	// connection events alone provide Online/Offline state"
	// (`alertSessions.js:97`). Built once with the session so a reconnect
	// restarts them rather than rebuilding, matching the page sessions.
	system *collect.System
	ping   *collect.Ping
	// traffic is continuous history's only added collector, built for the
	// router whose own `ReportingEnabled` says so, and for no other.
	traffic  *collect.Traffic
	ifStatus *collect.IfStatus
	vpn      *collect.VPN
	netwatch *collect.Netwatch
	routing  *collect.Routing

	// eff is this router's resolved collection config, held so a reconnect
	// starts the same set the first connect did.
	eff collection.Resolved

	stop chan struct{}
	// stopped guards `close(stop)`. See teardown: the select/default that was
	// here could close twice under two concurrent callers.
	stopped bool
	// done closes when the connect loop has left. Nothing in production waits on
	// it; a test needs to know the goroutine is gone before asserting what it
	// did — the same reason `internal/routers` keeps one.
	done chan struct{}
}

// Sync applies `PlanSync` to the pool.
//
// The DECISION is in sync.go and is pinned against the live `syncSessions` by a
// generated corpus; this is only the doing.
func (p *Pool) Sync(all []Router, activeID string, excluded map[string]bool) {
	p.mu.Lock()
	live := Live{}
	for id, s := range p.sessions {
		live[id] = LiveSession{AlertsEnabled: s.r.AlertsEnabled,
			ReportingEnabled: s.r.ReportingEnabled}
	}
	byID := map[string]Router{}
	for _, r := range all {
		byID[r.ID] = r
	}
	plan := PlanSync(all, activeID, excluded)(live)
	// ── AND ANY ROUTER WHOSE HISTORY ROLE CHANGED ─────────────────────────
	//
	// The history pair is built inside `buildCollectors`, which runs once per
	// session, so switching the recorded router means REBUILDING the two
	// affected sessions — the one that must start recording and the one that
	// must stop. Folding them into the plan keeps a single construction path;
	// starting and stopping collectors on a live session would be a second one,
	// and two paths that must agree is the shape `stripWanIP` and `res:move`
	// both record as a mistake.
	// ── AND ANY ROUTER WHOSE ENDPOINT OR CREDENTIALS CHANGED ──────────────
	//
	// `PlanSync` decides from the ID set and the per-router alert switch, so a
	// session it already holds keeps the `Router` it was BUILT with. Correcting
	// a password therefore never reached this pool: it went on dialling the old
	// one every five seconds, and every attempt is a rejected login in the
	// router's own log — which is how it was found (issue #124).
	//
	// Fed through `pendingRebuild` rather than as a second list, so it inherits
	// the double-listing guard below verbatim. That guard is not decoration: a
	// router in both `Build` and `Rebuild` had its first session overwritten in
	// `p.sessions` and LEAKED, still dialling with nothing able to stop it.
	for id, sess := range p.sessions {
		if nw, ok := byID[id]; ok && !sameConnection(sess.r, nw) {
			// LAZILY BUILT: on an install where nothing has yet marked a
			// router for rebuild the map is nil, and a bare write panics.
			if p.pendingRebuild == nil {
				p.pendingRebuild = map[string]bool{}
			}
			p.pendingRebuild[id] = true
		}
	}

	for id := range p.pendingRebuild {
		// ── `plan.Build` IS IN THIS GUARD, AND LEAVING IT OUT COST A SOCKET ──
		//
		// A router being BUILT is already getting a fresh session with the
		// current history role; asking for a rebuild too puts it in both lists,
		// and `Sync` constructs one session per entry. The second overwrote the
		// first in `p.sessions` and the first was LEAKED — still dialled, still
		// collecting, tracked by nothing that could ever stop it.
		//
		// MEASURED 2026-08-30, and only because a log line said
		// "Mikrotik hAP AX3 connected" twice in the same second: the process held
		// TWO established sockets to the active router and one each to the other
		// two. It happened on every start, because the first `SetHistoryRouter`
		// always moves the target from "" to the active id and always marks it.
		// `plan.Build` holds Routers, not ids, and `Router` has a []byte field so
		// it is not comparable — hence a named helper rather than slices.Contains.
		if _, ok := byID[id]; ok && !buildsRouter(plan.Build, id) &&
			!slices.Contains(plan.Rebuild, id) &&
			!slices.Contains(plan.Drop, id) {
			plan.Rebuild = append(plan.Rebuild, id)
		}
		delete(p.pendingRebuild, id)
	}

	var closing []*poolSession
	for _, id := range append(append([]string{}, plan.Drop...), plan.Rebuild...) {
		if s := p.sessions[id]; s != nil {
			closing = append(closing, s)
			delete(p.sessions, id)
		}
	}
	// ── A DROPPED ROUTER FORGETS ITS STATUS; A REBUILT ONE DOES NOT ───────
	//
	// Drop means this pool is no longer responsible — the router was removed, or
	// an interactive session took it over. Keeping its last state would let a
	// stale Online survive into a future re-add. A REBUILD is the same router
	// with its collectors changed, so its status has not become unknown and
	// clearing it would emit a spurious transition.
	for _, id := range plan.Drop {
		delete(p.state, id)
	}

	var starting []*poolSession
	for _, r := range append(append([]Router{}, plan.Build...), rebuilt(plan, byID)...) {
		s := &poolSession{r: r, stop: make(chan struct{}), done: make(chan struct{})}
		// #105: the router's own resolved config, so a collector the operator
		// turned off for this router is never built or started.
		s.eff = collection.Resolve(p.settings, collection.ParseRouter(r.Collection))
		// ── RECORDING IS THE ROUTER'S OWN SETTING NOW ─────────────────────
		//
		// This was `r.ID == p.historyID`: one router in the fleet recorded, and
		// it was whichever one happened to be active. `PlanSync` rebuilds on a
		// change to the flag, so the whole `SetHistoryRouter` mechanism — the
		// id, its setter, and the both-ends `pendingRebuild` marking it needed —
		// is gone rather than generalised.
		buildCollectors(s, s.eff, p.on, p.record, p.record != nil && r.ReportingEnabled)
		p.sessions[r.ID] = s
		starting = append(starting, s)
	}
	p.mu.Unlock()

	// OUTSIDE THE LOCK. `teardown` closes a socket and `run` dials one; holding
	// the pool lock across either would stall every other router's Sync behind
	// one unreachable device.
	for _, s := range closing {
		s.teardown()
	}
	for _, s := range starting {
		go s.run(p)
	}
}

// rebuilt turns the Rebuild ids back into Routers, using the CURRENT record —
// the whole point of a rebuild is that the record changed.
func rebuilt(plan Plan, byID map[string]Router) []Router {
	out := make([]Router, 0, len(plan.Rebuild))
	for _, id := range plan.Rebuild {
		if r, ok := byID[id]; ok {
			out = append(out, r)
		}
	}
	return out
}

// Status is the pool's view of who is up, for the Routers and Devices pages.
func (p *Pool) Status() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]bool, len(p.state))
	for k, v := range p.state {
		out[k] = v
	}
	return out
}

// Snapshot is what the Devices page can learn from THIS pool, for a router the
// overview pool has not reached.
//
// ── WHY THE DEVICES PAGE READS THE ALERT POOL AT ALL ────────────────────────
//
// This pool is always on: `server.go` syncs it at startup, so it already holds a
// connection to every enabled router before anybody opens a browser. The
// overview pool (`internal/routers`) is synced from the Devices page and idles
// until somebody looks — so on first open, every router but the selected one had
// no source at all and its card claimed "Offline" until the overview pool
// finished dialling, about three seconds later.
//
// Everything here is ALREADY BEING COLLECTED. Reading it costs no extra router
// channel, which is the measure that matters (see CLAUDE.md's "more efficient").
//
// TWO FIELDS, NOT `routers.Summary`'s SIX. `Connected` is available for every
// router the pool holds, including a status-only one; `System` and `IfStatus`
// exist only where alerting is enabled, because a status-only session
// deliberately runs no collectors. There is no `DHCPLeases` — this pool has no
// leases collector — so a card fed from here shows its Clients count as "—"
// until the overview pool arrives, which is the honest rendering of "not read
// yet" and exactly what a null already means on this page.
type Snapshot struct {
	RouterID  string
	Connected bool
	System    *collect.SystemPayload
	IfStatus  *collect.IfStatusPayload
}

// Snapshots is one entry per router this pool holds a session for.
//
// Keyed off `sessions` rather than `state`, because `state` deliberately
// OUTLIVES a torn-down session (see Sync) and reporting a router this pool no
// longer watches would be a stale claim rather than a live one.
func (p *Pool) Snapshots() []Snapshot {
	p.mu.Lock()
	sess := make([]*poolSession, 0, len(p.sessions))
	ids := make([]string, 0, len(p.sessions))
	for id, s := range p.sessions {
		ids = append(ids, id)
		sess = append(sess, s)
	}
	state := make(map[string]bool, len(p.state))
	for k, v := range p.state {
		state[k] = v
	}
	p.mu.Unlock()

	out := make([]Snapshot, 0, len(sess))
	for i, s := range sess {
		up, seen := state[ids[i]]
		if !seen {
			// NO OPINION YET. `note` records the first observation whatever it
			// is, including the first `false`, so a missing entry means this
			// session has not finished its first dial — not that the router is
			// down. Reporting `Connected: false` here would put the caller back
			// where it started, calling a router offline on the strength of a
			// zero value.
			continue
		}
		snap := Snapshot{RouterID: ids[i], Connected: up}
		// The collector pointers are set once when the session is BUILT and are
		// not written again, so reading them without the pool lock is safe;
		// `Last()` does its own locking.
		if s.system != nil {
			snap.System = s.system.Last()
		}
		if s.ifStatus != nil {
			snap.IfStatus = s.ifStatus.Last()
		}
		out = append(out, snap)
	}
	return out
}

// note records a status and reports whether it CHANGED.
//
// A router with no entry yet is "unknown", and the first observation is always a
// change — including the first `false`, so a router that is down when the pool
// starts is reported as down rather than sitting silent because false is Go's
// zero value. That distinction is why `state` is a map lookup with `ok` rather
// than a plain read.
func (p *Pool) note(id string, up bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	was, known := p.state[id]
	if known && was == up {
		return false
	}
	p.state[id] = up
	return true
}

func (p *Pool) report(id string, up bool) {
	if !p.note(id, up) {
		return
	}
	if p.status != nil {
		p.status(id, up)
	}
}

// run is the connect loop: dial, watch, re-dial.
func (s *poolSession) run(p *Pool) {
	defer close(s.done)
	// See routeros.AuthBackoff. This pool dials every router with alerting on,
	// including ones nobody has looked at in weeks, so a rejected credential
	// here is the least likely of the three to be noticed and the most likely to
	// fill a router's log. `sleepOrStop` already returns early on teardown, and
	// a credential change rebuilds the session (see Sync), so nothing else is
	// needed to interrupt it.
	var authBackoff routeros.AuthBackoff
	for {
		select {
		case <-s.stop:
			return
		default:
		}

		c, err := p.dial(routeros.Config{
			Host: s.r.Host, Port: s.r.Port,
			Username: s.r.Username, Password: s.r.Password,
			TLS: s.r.TLS, InsecureTLS: s.r.InsecureTLS,
		})
		if err != nil {
			// NOT LOGGED PER ATTEMPT. An unreachable router retries every five
			// seconds for as long as it is unreachable, and a line each time
			// buries everything else in the log. The status transition is the
			// record that it went down.
			p.report(s.r.ID, false)
			if !sleepOrStop(s.stop, authBackoff.Delay(err, p.retry)) {
				return
			}
			continue
		}

		s.mu.Lock()
		if s.closed() {
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conn = c
		s.mu.Unlock()
		log.Printf("[alertpool] %s connected", s.r.Label)
		authBackoff.Reset() // the run of rejections is over
		p.report(s.r.ID, true)
		// STARTED ON EVERY CONNECT, including a reconnect: the collectors were
		// stopped when the socket dropped, and a session that reconnected
		// without restarting them would report Online and evaluate nothing.
		s.startCollectors(s.eff)

		// Watch for the drop. A poll rather than an event because go-routeros
		// offers none; one second is far below the five-second re-dial, so
		// nothing is gained by making it tighter.
		for c.Connected() {
			if !sleepOrStop(s.stop, time.Second) {
				s.stopCollectors()
				s.mu.Lock()
				s.conn = nil
				s.mu.Unlock()
				_ = c.Close()
				return
			}
		}

		s.stopCollectors()
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
		_ = c.Close()
		log.Printf("[alertpool] %s disconnected", s.r.Label)
		p.report(s.r.ID, false)
		if !sleepOrStop(s.stop, p.retry) {
			return
		}
	}
}

func (s *poolSession) closed() bool {
	select {
	case <-s.stop:
		return true
	default:
		return false
	}
}

// sleepOrStop waits, and reports false if the session was told to stop instead.
//
// A plain `time.Sleep` would make teardown wait out the retry — five seconds per
// router on shutdown, and on every settings change that re-syncs the pool.
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

func (s *poolSession) teardown() {
	// ── THE GUARD IS ATOMIC, AND WAS NOT ────────────────────────────────────
	//
	// This was `select { case <-s.stop: return; default: close(s.stop) }`, which
	// is a check-then-act: two goroutines can both reach `default` and both
	// close, which panics with "close of closed channel" and takes the process.
	//
	// It was safe in practice because both callers — `Sync` and `Close` — remove
	// the session from `p.sessions` under `p.mu` before calling here, so one
	// session reaches teardown once. That is an invariant of the CALLERS. A third
	// caller written later, or a collector that tore down its own session on a
	// close event, would have found the panic instead of the guard.
	//
	// `internal/routers`' `destroy()` already did its check-and-set inside the
	// session mutex. Two sibling pools had two different guard strengths, and the
	// weaker one was safe by accident of who called it.
	//
	// Found by a peer session hitting the same shape in the Node client, where
	// the failure mode is unbounded recursion rather than a panic. Reproduced
	// here with two concurrent teardowns before it was changed.
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return // already told
	}
	s.stopped = true
	close(s.stop)
	s.mu.Unlock()
	// BEFORE the socket goes. A collector mid-poll against a closed connection
	// logs an error for something that is not a fault.
	s.stopCollectors()
	s.mu.Lock()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Close stops every session. For shutdown and for tests.
func (p *Pool) Close() {
	p.mu.Lock()
	all := make([]*poolSession, 0, len(p.sessions))
	for id, s := range p.sessions {
		all = append(all, s)
		delete(p.sessions, id)
	}
	p.mu.Unlock()
	for _, s := range all {
		s.teardown()
	}
}

// WithHistory installs the continuous-history recorder. Nil disables it, which
// is the default and what every caller had before 2026-08-30.
func (p *Pool) WithHistory(rec RecordHook) *Pool {
	p.mu.Lock()
	p.record = rec
	p.mu.Unlock()
	return p
}

// buildsRouter reports whether the plan already builds this router.
func buildsRouter(build []Router, id string) bool {
	for _, r := range build {
		if r.ID == id {
			return true
		}
	}
	return false
}
