package routers

// The background pool's CONNECTIONS — the half of `src/overviewSessions.js`
// that `overview.go` deliberately left out.
//
// `overview.go` holds the decisions (`SyncPool`, and the per-session lifecycle
// as pure state). This holds the sockets: one connection per router nobody is
// looking at, three collectors on it, and their last payloads cached for
// `BuildRow`.
//
// ── CONSTRUCTED BY NOBODY, ON PURPOSE ───────────────────────────────────────
//
// Whether a Go pool may run DURING COEXISTENCE is an operator decision, recorded
// in the port record: Node already runs this pool against the same fleet, so both
// would hold a connection to every router at once. The code is needed under
// every option including "wait for cutover", so it is written and pinned now and
// wired by nothing — the same arrangement as `internal/history.Bucketer`.
//
// ── THREE COLLECTORS, NOT FOURTEEN ──────────────────────────────────────────
//
// `session.Session` starts fourteen on connect and is a full page-serving
// session; the live overview pool runs system, interfaceStatus and dhcpLeases
// (`overviewSessions.js`). Using `Session` for unwatched routers would be the
// wrong object rather than a heavy version of the right one, and the documented
// bottleneck is concurrent API channels on the MikroTik.
//
// ── THE PER-ROUTER COLLECTION CONFIG (#105) IS HONOURED ─────────────────────
//
// `collection.Resolve` gives this pool the router's effective intervals and its
// enabled set, so a router with `dhcpLeases` turned off does not have it polled
// here either. An earlier version of this file passed `0` for every interval and
// started all three unconditionally, because the port had no `resolveCollection`
// at the time; it does now.
//
// **The live "must not CONSTRUCT a disabled collector" rule does not apply
// here, and that is a property of the port rather than an oversight.** On the
// live side eleven of the sixteen open their streams from a `ros.on('connected')`
// handler in the CONSTRUCTOR, so skipping `start()` is not enough and a null stub
// is needed to keep the shape. The Go collectors are inert until `Start()` —
// verified across system, ifstatus, logs, traffic and dns — so a disabled one is
// simply never started, and no stub exists to drift from the real thing.
//
// `Suspend` and `Resume` still act on all three unconditionally, like the
// original: a stop on a stopped collector is a no-op, and tracking "running"
// only creates a second source of truth that can disagree with the collectors.
//
// ── WHAT IS AND IS NOT OBSERVED HERE ────────────────────────────────────────
//
// The ENABLED half is pinned behaviourally: turning `ifStatus` off means
// `/interface/print` never reaches the router, and the mutation that starts it
// anyway dies. Only `ifStatus` of these three is `disableable` — `system` and
// `dhcpLeases` are protected in the registry because other collectors read them
// — so the guards on those two are unreachable BY DESIGN, not by omission.
//
// The INTERVAL half is not observed here, and saying so is more useful than a
// test that looks like it is. Passing `0` instead of `eff.Poll[...]` survives
// every case, because under default settings the resolved interval IS the
// collector's own default and `clampPoll` maps both to the same number. Telling
// them apart needs a non-default interval and a measurement of when the SECOND
// poll happens — a timing test, which is the flakiest kind there is. What the
// numbers are is pinned instead by `internal/collection`'s corpus, which drives
// the live module over 29 cases and kills eight mutations including the clamp
// and the override precedence.
//
// ── EMITS GO NOWHERE ────────────────────────────────────────────────────────
//
// The live pool passes a null io whose `to()` is recursively chainable, because
// a collector reaching three rooms threw on a two-level shim. A Go `Emit` is one
// function with no chaining, so the equivalent is simply a function that does
// nothing — the collectors still cache their payloads in `Last()`, which is the
// only thing read here.
//
// ── MUTATIONS (2026-08-25): FIVE KILLED, TWO EQUIVALENT, ONE FOUND A BUG ────
//
//   store the raw reason unclassified   KILLED — and the failure shows
//                                       "/data/secret.key: …" reaching LastError,
//                                       which is the leak the guard exists for.
//   never notice a drop                 KILLED
//   ignore the excluded set             KILLED
//   summaries unsorted                  KILLED
//   make destroy() wait again           KILLED, by HANGING — see `destroy`. The
//                                       hang IS the finding: it deadlocks the
//                                       mid-dial teardown. A hang is a poor kill
//                                       signal, so run with `-timeout`.
//
//   EQUIVALENT — dropping the `Destroyed()` check after the dial. The connection
//   still gets closed: `OnConnected` carries its own destroyed guard (that is
//   what it is for, pinned in `overview_test.go`) so no collector starts, and the
//   already-closed `stop` channel then takes the loop through `teardown`, which
//   closes it. The explicit check closes it SOONER and is defence in depth, not a
//   distinct behaviour. Kept for that reason, recorded rather than counted.
//
//   EQUIVALENT HERE — making Resume start collectors on a DEAD session. The guard
//   is `OverviewSession.Resume()`'s, pinned by `overview_test.go`; from outside
//   this package "a collector was started against a dead connection" is not
//   observable, and its only effect is a poll that reads `errNotConnected`.
//   Duplicating that assertion here would test the same line twice.

import (
	"log"
	"sort"
	"strconv"
	"sync"
	"time"

	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/routeros"

	"mikrodash/internal/roslimit"
)

// Conn is the slice of a router connection this pool needs: the collectors' Do,
// and a Close for teardown.
//
// An interface rather than *routeros.Client so a test can drive the whole
// lifecycle — connect, fail, classify, suspend, resume, stop — with no router
// present, which is the only way the retry and teardown races are reachable.
type Conn interface {
	Do(routeros.Cmd) ([]routeros.Reply, error)
	// Connected is how a DROP is noticed. The live pool learns from the driver's
	// `close` event; go-routeros has no event, so the loop watches this instead
	// and re-dials when it goes false. Without it a session that lost its socket
	// would sit "connected" for ever and the Routers page would show stale
	// numbers as current — worse than showing it offline.
	Connected() bool
	Close() error

	// Stream is what the PING half of the history pair needs: `/tool/ping` is a
	// stream, not a poll, because the interval belongs to the router
	// (`=interval=N`). Added 2026-08-30 with continuous history; every concrete
	// conn already had it — `*routeros.Client.Stream` — and only this interface
	// and the test stubs did not name it.
	Stream(routeros.Cmd, func(routeros.Reply)) (func(), error)
}

// Dialer opens a connection. `routeros.Dial` satisfies it once wrapped.
type Dialer func(routeros.Config) (Conn, error)

// RouterConfig is what the pool needs to reach one router and to explain a
// failure. It is deliberately not the db record: this package does no I/O of its
// own beyond RouterOS, and taking the record would drag the store in.
type RouterConfig struct {
	ID    string
	Label string
	Host  string
	Port  int
	// User is carried for the CLASSIFIER's hint, which names the account an
	// operator should check. It is never rendered.
	User        string
	Password    string
	TLS         bool
	InsecureTLS bool
	// Collection is the record's `collection` block. Nil resolves to the
	// fleet defaults, which is what a router that has never been configured
	// gets on the live side too.
	Collection *collection.Router

	// DefaultIf and PingTarget are carried ONLY for the history collectors —
	// see `SetHistoryRouter`. They are the same two values `Session` passes to
	// `NewTraffic` and `NewPing`, so a pooled recording and a page-driven one
	// measure the same interface and the same target rather than quietly
	// producing two different histories for one router.
	DefaultIf  string
	PingTarget string
}

// Summary is one router's contribution to `routers:stats`, matching what the
// live `getSummaries()` returns.
type Summary struct {
	RouterID   string
	Connected  bool
	LastError  string
	System     *collect.SystemPayload
	IfStatus   *collect.IfStatusPayload
	DHCPLeases *collect.LeasesPayload
}

// IdentityHook is called when a router reports what it is. The live pool uses it
// to write model/serial/osVersion back to the router record.
//
// ── IT TAKES AN IDENTITY, NOT A PAYLOAD, AND IT IS CALLED FROM THE COLLECTOR ──
//
// It used to take `*collect.SystemPayload` and be called ONCE from the connect
// loop, off `s.system.Last()`. Both halves were wrong, and silently:
//
//  1. `Last()` is nil at that point — the collectors have only just been
//     started and no tick has run — so the hook was wired and never fired.
//  2. It could not fire again. Model and serial are fixed for the life of a
//     device but the OS VERSION changes on upgrade, and the live comment is
//     explicit that this "must not be write-once".
//
// `collect.System` now owns the hook and the `_lastIdentityKey` dedupe, exactly
// where the live app puts them, and the payload→identity translation
// (`boardName` → `model`, the channel dropped from the version) happens where
// the values are in scope rather than being re-derived by every caller.
type IdentityHook func(routerID string, id collect.Identity)

type poolSession struct {
	cfg RouterConfig

	mu   sync.Mutex
	sess OverviewSession
	conn Conn

	system     *collect.System
	ifStatus   *collect.IfStatus
	dhcpLeases *collect.DHCPLeases
	// traffic and ping are the HISTORY pair, nil unless a recorder was
	// installed. See `SetHistoryRouter`.
	traffic *collect.Traffic
	ping    *collect.Ping
	// historyOn is whether THIS session is the one recording. Held here rather
	// than read back off the pool because `startCollectors` runs on every
	// reconnect and must not need the pool's lock to answer it.
	historyOn bool
	// eff is this router's resolved collection config. Held so `startCollectors`
	// can skip a collector the operator turned off.
	eff collection.Resolved

	stop chan struct{}
	// done closes when the connect loop has left. Nothing in the pool waits on
	// it — see `destroy` — but a test needs to know the goroutine is gone before
	// asserting that it started nothing.
	done chan struct{}
}

// reader is the collectors' view of this session. `Connected` gates every poll,
// so a collector left running across a drop reads nothing rather than erroring
// in a loop.
type reader struct{ s *poolSession }

func (r reader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.s.mu.Lock()
	c := r.s.conn
	up := r.s.sess.Connected
	r.s.mu.Unlock()
	if c == nil || !up {
		return nil, errNotConnected{}
	}
	// The same per-router budget the viewing session and the alert pool take.
	// This pool reaches routers nobody is watching, but it reaches the SAME
	// routers, so a cap that skipped it would not be a cap on the device.
	done := roslimit.Acquire(r.s.cfg.ID)
	defer done()
	return c.Do(cmd)
}

// Stream mirrors Do's guard: a stream opened on a dead connection would sit
// there producing nothing while the collector believed it was running.
func (r reader) Stream(cmd routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	r.s.mu.Lock()
	c := r.s.conn
	up := r.s.sess.Connected
	r.s.mu.Unlock()
	if c == nil || !up {
		return nil, errNotConnected{}
	}
	return c.Stream(cmd, onRow)
}

func (r reader) Connected() bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.conn != nil && r.s.sess.Connected
}

type errNotConnected struct{}

func (errNotConnected) Error() string { return "routers: background session not connected" }

// Pool holds one background session per router nobody has open.
type Pool struct {
	dial     Dialer
	retry    time.Duration
	identity IdentityHook
	// settings is the global settings map, the low half of #105's precedence.
	// Read once per Sync rather than per router, as the live builder does.
	settings map[string]any

	// record is where the history collectors' payloads go, and historyID names
	// the ONE router they run for. Both nil/empty unless the server wired them.
	record    func(routerID, event string, payload any)
	historyID string

	mu        sync.Mutex
	sessions  map[string]*poolSession
	suspended bool
	closed    bool
}

// WithHistory installs the recorder. Nil disables recording entirely, which is
// the default and what every existing caller gets.
func (p *Pool) WithHistory(rec func(routerID, event string, payload any)) *Pool {
	p.mu.Lock()
	p.record = rec
	p.mu.Unlock()
	return p
}

// SetHistoryRouter names the router whose traffic and ping this pool records.
//
// ── WHY THIS IS A CALL AND NOT A FIELD READ AT BUILD TIME ─────────────────
//
// `setActiveRouter` does not re-sync the pool — it writes a settings key and
// returns — and `Sync` does not rebuild a session that already exists. So a
// session built while router A was active would go on recording A forever.
// This starts and stops the two collectors on the sessions that already exist,
// which is the only thing that reacts to an activation in time.
//
// Idempotent: naming the router that is already recording does nothing.
func (p *Pool) SetHistoryRouter(id string) {
	p.mu.Lock()
	if p.closed || p.historyID == id {
		p.mu.Unlock()
		return
	}
	p.historyID = id
	suspended := p.suspended
	list := make([]*poolSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		list = append(list, s)
	}
	p.mu.Unlock()

	for _, s := range list {
		// THE FLAG UNDER THE LOCK, THE START OUTSIDE IT.
		//
		// `Ping.Start` opens a stream synchronously, which goes through
		// `reader.Stream`, which takes this same `s.mu` — so starting a
		// collector while holding it is a self-deadlock. The first version of
		// this did exactly that and hung the package's tests until they were
		// killed. The existing connect path has always released `s.mu` before
		// calling `startCollectors` (see `run`), for the same reason.
		s.mu.Lock()
		s.historyOn = s.cfg.ID == id
		on := s.historyOn && !suspended
		s.mu.Unlock()
		s.setHistoryCollectors(on)
	}
}

// NewPool builds an empty pool. Nothing connects until Sync is called.
//
// `settings` is the global settings map; a nil one resolves every router to the
// collectors' own defaults, which is what an installation that has changed
// nothing gets.
func NewPool(dial Dialer, retry time.Duration, identity IdentityHook, settings map[string]any) *Pool {
	if retry <= 0 {
		retry = 5 * time.Second
	}
	return &Pool{
		dial:     dial,
		retry:    retry,
		identity: identity,
		settings: settings,
		sessions: map[string]*poolSession{},
	}
}

// Sync brings the pool in line with the fleet, using SyncPool's decision.
//
// `all` is every router that is not disabled; `excluded` is the set the MAIN
// pool already serves. A router that is both tracked and excluded is STOPPED —
// somebody opened it and the interactive session took over.
func (p *Pool) Sync(all []RouterConfig, excluded map[string]bool) PoolAction {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return PoolAction{}
	}
	ids := make([]string, 0, len(all))
	byID := make(map[string]RouterConfig, len(all))
	for _, r := range all {
		ids = append(ids, r.ID)
		byID[r.ID] = r
	}
	tracked := make(map[string]bool, len(p.sessions))
	for id := range p.sessions {
		tracked[id] = true
	}
	act := SyncPool(ids, excluded, tracked)

	stopping := make([]*poolSession, 0, len(act.Stop))
	for _, id := range act.Stop {
		if s := p.sessions[id]; s != nil {
			stopping = append(stopping, s)
			delete(p.sessions, id)
		}
	}
	starting := make([]*poolSession, 0, len(act.Start))
	for _, id := range act.Start {
		s := p.build(byID[id])
		p.sessions[id] = s
		starting = append(starting, s)
	}
	suspended := p.suspended
	p.mu.Unlock()

	// OUTSIDE THE POOL LOCK. Teardown waits for a session's goroutine to leave,
	// and that goroutine takes the SESSION lock — holding the pool lock across it
	// is the deadlock this ordering avoids.
	for _, s := range stopping {
		s.destroy()
	}
	for _, s := range starting {
		if suspended {
			s.mu.Lock()
			s.sess.Suspend()
			s.mu.Unlock()
		}
		go s.run(p)
	}
	return act
}

// build constructs a session and its three collectors. Nothing connects here.
func (p *Pool) build(cfg RouterConfig) *poolSession {
	s := &poolSession{
		cfg:  cfg,
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	r := reader{s}
	// EMITS GO NOWHERE — see the header. `Last()` is the only reader.
	nowhere := func(string, string, any) {}
	// #105: the router's effective intervals and enabled set.
	eff := collection.Resolve(p.settings, cfg.Collection)
	s.eff = eff
	s.system = collect.NewSystem(r, nowhere, eff.Poll["system"])
	// INSTALLED BEFORE Start, and on the collector rather than called from the
	// connect loop — see IdentityHook. A closure over `cfg.ID` rather than over
	// `s`, so nothing here can keep a torn-down session alive.
	if p.identity != nil {
		id := cfg.ID
		hook := p.identity
		s.system.SetOnIdentity(func(ident collect.Identity) { hook(id, ident) })
	}
	s.ifStatus = collect.NewIfStatus(r, nowhere, cfg.ID, eff.Poll["ifStatus"])
	s.dhcpLeases = collect.NewDHCPLeases(r, nowhere, eff.Poll["dhcpLeases"])

	// ── THE HISTORY PAIR ──────────────────────────────────────────────────
	//
	// CONSTRUCTED for every pooled router, STARTED for one. Building a
	// collector allocates a struct and a timer that is not running; it opens
	// nothing. Starting one is what costs a command channel, and that happens
	// only for the router `SetHistoryRouter` names.
	//
	// They exist at all because `internal/historywire` records exactly two
	// payload types — `*collect.TrafficSample` and `*collect.PingPayload` — and
	// the pool's other three collectors produce neither. Without these, a
	// router with no browser attached writes no history, which is measurably
	// what happened: live wrote 60 traffic rows an hour and this port wrote
	// between 5 and 44, tracking whether anyone was looking.
	if p.record != nil {
		// Set at BUILD time as well as in SetHistoryRouter: a router that joins
		// the pool later — reconnecting, or re-enabled — would otherwise never
		// learn it is the one recording until the next activation.
		s.historyOn = cfg.ID == p.historyID
		id := cfg.ID
		rec := p.record
		emit := func(_, event string, payload any) { rec(id, event, payload) }
		s.traffic = collect.NewTraffic(r, emit, cfg.DefaultIf, 5)
		s.ping = collect.NewPing(r, emit, eff.Poll["ping"], cfg.PingTarget)
	}
	return s
}

// setHistoryCollectors starts or stops the pair.
//
// ── THIS POOL IS NOT THE ALWAYS-ON ONE, AND THAT IS THE CATCH ─────────────
//
// MEASURED 2026-08-30: after a restart with no browser open, `syncHistoryRouter`
// ran and this function was never called, because THIS pool had no sessions.
// `syncPool` is called from the Devices page and the routers API and from
// nowhere else, so `routers.Pool` idles until somebody looks at something.
// `internal/alertpool` is the pool that connects to every router at startup —
// the three established sockets on this process are its, not this one's.
//
// So what is wired here records while the Devices page is open and not
// otherwise. It is correct and pinned, and it is NOT by itself the answer to
// LOOP.md 0i; the always-on path is the alertpool, which already holds the
// connection and already runs `Ping`.
//
// THE CALLER MUST NOT HOLD `s.mu`. `Ping.Start` opens its stream synchronously
// through `reader.Stream`, which takes that lock.
//
// NIL-SAFE, because the pair is built only when a recorder was installed: a pool
// with no history wiring has nothing to start, and every existing caller of
// `startCollectors` reaches this with nils.
func (s *poolSession) setHistoryCollectors(on bool) {
	if s.traffic == nil || s.ping == nil {
		return
	}
	if on {
		if s.eff.Enabled["traffic"] {
			s.traffic.Start()
		}
		if s.eff.Enabled["ping"] {
			s.ping.Start()
		}
		return
	}
	s.traffic.Stop()
	s.ping.Stop()
}

// run is one session's connect loop: dial, serve, retry.
func (s *poolSession) run(p *Pool) {
	defer close(s.done)
	for {
		select {
		case <-s.stop:
			return
		default:
		}

		conn, err := p.dial(routeros.Config{
			Host: s.cfg.Host, Port: s.cfg.Port,
			Username: s.cfg.User, Password: s.cfg.Password,
			TLS: s.cfg.TLS, InsecureTLS: s.cfg.InsecureTLS,
		})
		if err != nil {
			// CLASSIFIED AT THE POINT OF STORAGE. `LastError` reaches a browser
			// through the Routers page, so an unclassified reason must never be
			// stored raw — `OnError` substitutes the generic string for us.
			c := routeros.ClassifyError(err, s.cfg.Host, itoa(s.cfg.Port), s.cfg.User, s.cfg.TLS)
			s.mu.Lock()
			s.sess.OnError(c.Reason, c.Classified)
			s.mu.Unlock()
			if c.Hint != "" {
				log.Printf("[overview] %s: %s (%s)", s.cfg.Label, c.Reason, c.Hint)
			}
			select {
			case <-s.stop:
				return
			case <-time.After(p.retry):
			}
			continue
		}

		s.mu.Lock()
		// DESTROYED WHILE DIALLING. The live pool carries the same guard on its
		// `connected` event: without it a removed router opens a connection
		// nothing will ever close and starts collectors that poll for ever.
		if s.sess.Destroyed() {
			s.mu.Unlock()
			_ = conn.Close()
			return
		}
		s.conn = conn
		start := s.sess.OnConnected()
		s.mu.Unlock()

		if start {
			s.startCollectors()
		}
		// SERVE UNTIL THE LINK DROPS OR THE SESSION IS TORN DOWN.
		//
		// Polling `Connected()` rather than waiting on an event is the shape
		// go-routeros leaves available. The interval is short next to any
		// collector's poll, so a drop is noticed before the page could read a
		// stale payload as current.
		const watch = 500 * time.Millisecond
		t := time.NewTicker(watch)
		dropped := false
		for !dropped {
			select {
			case <-s.stop:
				t.Stop()
				s.teardown()
				return
			case <-t.C:
				if !conn.Connected() {
					dropped = true
				}
			}
		}
		t.Stop()

		// A DROP IS NOT AN ERROR, so LastError is left alone: `OnClosed` does not
		// clear it either, because the reason a session failed is what the page
		// has to show while it is down.
		s.mu.Lock()
		s.sess.OnClosed()
		s.conn = nil
		s.mu.Unlock()
		s.stopCollectors()
		_ = conn.Close()

		select {
		case <-s.stop:
			return
		case <-time.After(p.retry):
		}
	}
}

// teardown closes whatever is open. Collectors are stopped by `destroy` before
// it closes `stop`, so they are not stopped twice here.
func (s *poolSession) teardown() {
	s.mu.Lock()
	s.sess.OnClosed()
	c := s.conn
	s.conn = nil
	s.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// startCollectors starts the ones this router has ENABLED.
//
// A disabled collector is simply never started — see the header on why the live
// null-stub is unnecessary here.
func (s *poolSession) startCollectors() {
	if s.eff.Enabled["system"] {
		s.system.Start()
	}
	if s.eff.Enabled["ifStatus"] {
		s.ifStatus.Start()
	}
	if s.eff.Enabled["dhcpLeases"] {
		s.dhcpLeases.Start()
	}
	// AND THE HISTORY PAIR, on reconnect as well as on first connect. A router
	// that dropped and came back would otherwise stop recording silently for as
	// long as it stayed up.
	s.setHistoryCollectors(s.historyOn)
}

// stopCollectors stops all three UNCONDITIONALLY, like the original, which calls
// `stop()` whether or not they were running. A stop on a stopped collector is a
// no-op, and tracking "running" only creates a second source of truth.
func (s *poolSession) stopCollectors() {
	s.system.Stop()
	s.ifStatus.Stop()
	s.dhcpLeases.Stop()
	s.setHistoryCollectors(false)
}

// destroy tears a session down and DOES NOT WAIT for its goroutine.
//
// An earlier version blocked on `<-s.done`, which reads as tidy and is wrong: the
// goroutine may be inside a dial, and a dial takes until the timeout. `Sync` is
// called from the request path, so waiting there would stall the Routers page
// for as long as an unreachable router takes to fail. The live `_stopSession`
// does not wait either — it stops the collectors, calls `ros.stop()` and returns.
//
// What makes that safe is the `destroyed` flag, checked after the dial returns:
// a session torn down mid-dial closes the connection it just opened and exits
// without starting anything. That is the race the flag exists for, and it is why
// this may return early.
func (s *poolSession) destroy() {
	s.mu.Lock()
	already := s.sess.Destroyed()
	s.sess.Destroy()
	s.mu.Unlock()
	if already {
		return
	}
	s.stopCollectors()
	close(s.stop)
}

// Summaries is what the Routers page reads: one entry per tracked router, with
// whatever each collector last saw.
func (p *Pool) Summaries() []Summary {
	p.mu.Lock()
	list := make([]*poolSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		list = append(list, s)
	}
	p.mu.Unlock()

	out := make([]Summary, 0, len(list))
	for _, s := range list {
		s.mu.Lock()
		sum := Summary{
			RouterID:  s.cfg.ID,
			Connected: s.sess.Connected,
			LastError: s.sess.LastError,
		}
		s.mu.Unlock()
		sum.System = s.system.Last()
		sum.IfStatus = s.ifStatus.Last()
		sum.DHCPLeases = s.dhcpLeases.Last()
		out = append(out, sum)
	}
	sortSummaries(out)
	return out
}

// Suspend stops collecting everywhere WITHOUT disconnecting. Suspension is "stop
// collecting", not "drop the sockets", so Resume costs nothing.
func (p *Pool) Suspend() {
	p.mu.Lock()
	p.suspended = true
	list := sessionsOf(p)
	p.mu.Unlock()
	for _, s := range list {
		s.mu.Lock()
		s.sess.Suspend()
		s.mu.Unlock()
		s.stopCollectors()
	}
}

// ReleaseAll drops every background session, leaving the pool usable.
//
// ── WHY SUSPENDING IS NOT ENOUGH ────────────────────────────────────────────
//
// `Suspend` stops collecting and KEEPS the sockets, deliberately, so returning
// to the Devices page is instant. The cost of that was invisible and fleet-wide:
// `syncAlertPool` excludes every router this pool lists in `Summaries()`, and a
// suspended session is still listed -- so once anybody opened Devices, the
// overview pool owned the whole fleet, stopped collecting the moment they left,
// and the alert pool was locked out of all of it.
//
// The result was no alert evaluation and no continuous history for ANY router
// until something else happened to re-run `syncAlertPool`. That contradicts the
// reason the alert pool exists, which `server.go` states plainly: a router
// nobody is watching "is still known to be up and still has its alerts
// evaluated, which is a claim about the whole uptime of the process".
//
// So leaving the page now RELEASES the routers rather than merely going quiet on
// them, and the alert pool takes them back. Returning to Devices re-dials, which
// is what the first visit does anyway -- a cost paid by the person looking at
// the page, instead of a gap in coverage paid by everyone who is not.
func (p *Pool) ReleaseAll() {
	p.mu.Lock()
	p.suspended = true
	list := sessionsOf(p)
	p.sessions = map[string]*poolSession{}
	p.mu.Unlock()
	// Outside the lock, as `Sync` and `Drop` do: destroy reaches a session
	// goroutine that takes the session's own lock.
	for _, s := range list {
		s.destroy()
	}
}

// Resume starts collecting again on every session that is still connected.
func (p *Pool) Resume() {
	p.mu.Lock()
	p.suspended = false
	list := sessionsOf(p)
	p.mu.Unlock()
	for _, s := range list {
		s.mu.Lock()
		start := s.sess.Resume()
		s.mu.Unlock()
		if start {
			s.startCollectors()
		}
	}
}

// Close tears the whole pool down.
func (p *Pool) Close() {
	p.mu.Lock()
	p.closed = true
	list := sessionsOf(p)
	p.sessions = map[string]*poolSession{}
	p.mu.Unlock()
	for _, s := range list {
		s.destroy()
	}
}

// Drop tears down ONE router's background session and leaves the rest alone.
//
// ── WHY THIS EXISTS RATHER THAN A Sync CALL ─────────────────────────────────
//
// It is the imperative form of Sync's own rule that a router which is both
// tracked and excluded gets stopped. A browser has just selected this router, an
// interactive Session is dialling it, and the socket this pool has held since
// the Devices page was last open is now a SECOND connection to one device --
// `Suspend` keeps its sockets deliberately, so navigating away does not release
// it and nothing re-syncs until a router edit or a return to Devices.
//
// **NOT `Sync`.** `SyncPool` computes Start as "every router that is neither
// excluded nor already tracked" (overview.go:69-74), so calling it from a socket
// handler on a pool that has never run -- `p.sessions` empty, so `tracked` empty
// -- would START a background session for the entire fleet and dial every
// router. That is the opposite of the fix, and it is the obvious wrong turn
// here, which is why this method exists to make the right one easy.
//
// Membership is not tracked twice: the next `Sync` re-derives it from the store
// and the live-session set, and re-adds this router once its Session has gone.
func (p *Pool) Drop(routerID string) {
	p.mu.Lock()
	s := p.sessions[routerID]
	delete(p.sessions, routerID)
	p.mu.Unlock()
	// OUTSIDE THE POOL LOCK, for the reason Sync gives at its own destroy call:
	// destroy reaches a session goroutine that takes the session's lock.
	if s != nil {
		s.destroy()
	}
}

// Tracked reports which routers the pool currently holds a session for.
// Suspended reports whether the pool is holding its collectors stopped.
//
// `OverviewSession` already exposes the same fact per session; this is the
// fleet-wide one. It exists because the CALLER decides when to suspend — on the
// last watcher leaving the Devices page — and a caller that gets that wrong
// holds a connection to every router for a page nobody has open, which is
// silent, indefinite, and exactly what the design is meant to prevent.
func (p *Pool) Suspended() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.suspended
}

func (p *Pool) Tracked() map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]bool, len(p.sessions))
	for id := range p.sessions {
		out[id] = true
	}
	return out
}

// itoa keeps the classifier's `port` a string without dragging fmt in for one
// conversion.
func itoa(n int) string { return strconv.Itoa(n) }

// sortSummaries makes the output reproducible. Go's map order is random and the
// live pool iterates a Map in insertion order; nothing downstream depends on the
// order, so sorting is free and stops a log or a diff jittering.
func sortSummaries(v []Summary) {
	sort.Slice(v, func(i, j int) bool { return v[i].RouterID < v[j].RouterID })
}

func sessionsOf(p *Pool) []*poolSession {
	out := make([]*poolSession, 0, len(p.sessions))
	for _, s := range p.sessions {
		out = append(out, s)
	}
	return out
}
