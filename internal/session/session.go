package session

// One live connection to one router, and the collectors riding it.
//
// Reference-counted, because the thing being conserved is the scarce resource
// the whole project is organised around: "the evidence in #104 points at
// concurrent open channels rather than data volume" (src/collection.js). A
// router with nobody watching it should hold no connection at all, and one with
// six viewers should hold exactly one.
//
// The dormancy behaviour is deliberately simpler than the Node supervisor's for
// now — this drives one collector — but the shape is the same and the finer
// page gate is already here: a collector runs while its page room has an
// occupant and is suspended when the last viewer leaves.

import (
	"context"
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertwire"
	"mikrodash/internal/asn"
	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/connstate"
	"mikrodash/internal/dormancy"
	"mikrodash/internal/geo"
	"mikrodash/internal/historywire"
	"mikrodash/internal/hub"
	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"

	"mikrodash/internal/safe"

	"mikrodash/internal/roslimit"
)

// Session is one router's connection and collectors.
type Session struct {
	// eff is this router's resolved collection config (#105): per-router poll
	// intervals, stream choices and which collectors may run at all. Read it through
	// `conf()`.
	//
	// ── RESOLVED IN Acquire, AND REPLACED WHEN THE ROUTER IS SAVED ───────────
	//
	// It was resolved once and never again, so a collector switched off in the
	// device dialog kept running, and one switched on never started, until the
	// session was rebuilt: for a router held for alerting or history, a restart.
	// The dialog said "Saving briefly reconnects this router", and nothing did.
	// `Manager.ApplyCollection` now swaps it and switches the collectors whose
	// setting changed, without a reconnect (a redial restarts only the collectors
	// already enabled, so it could not have done this).
	//
	// AN atomic.Pointer, because the connect loop, the page-focus path, the
	// stream decision and the save all read or replace it on different goroutines.
	eff atomic.Pointer[collection.Resolved]

	// execForTest, when set, answers every command instead of a connection. Only
	// NewForTestWithExec sets it.
	execForTest func(routeros.Cmd) ([]routeros.Reply, error)

	// primedSys is a one-shot `/system/resource` reading taken for the Devices
	// page when this session runs no system collector of its own, and `priming`
	// is the claim that stops two focuses reading the same router at once. See
	// prime.go.
	primedSys *collect.SystemPayload
	priming   bool
	// primeInflight counts prime reads still with the router. See primeStats.
	primeInflight atomic.Int32

	// sched is the router's ONE scheduler, phase 3.2. It services the cache's
	// demand set, so a collector that has subscribed does not own a timer. Nil
	// until the session is built; started on the first connect and stopped in
	// Release, before the connection closes, because Stop waits for the loop and
	// a fetch must not still be in flight against a socket about to shut.
	sched *roscache.Scheduler

	// roscache coalesces reads two collectors share. See Collectors-Rewrite.md
	// phase 1; nil until Acquire builds the collectors.
	roscache *roscache.Cache

	// dormancy decides which collectors are asleep. Nil until Acquire builds it,
	// and consulted as a VETO by ResumeCollector — see dormancy_targets.go.
	dormancy *dormancy.Supervisor
	// dormancyAt is when the supervisor last judged, in ms. It is the DEBOUNCE
	// for the delivery-driven tick -- see judgeOnDelivery -- and atomic because
	// it is read and written from the scheduler's goroutines.
	dormancyAt atomic.Int64

	RouterID string
	Label    string
	// alertsEnabled is this router's per-device alert switch.
	//
	// ── IT FOLLOWS THE RECORD NOW, AND IT DID NOT ──────────────────────────
	//
	// It was captured when the session was built. The live app re-reads it on
	// every event "in case it was toggled after session creation"; this said the
	// port "gets that for free, since a change to the record rebuilds the
	// session (`collectionFingerprint`)", and nothing here rebuilds it, because
	// that fingerprint does not exist. So turning Alert Monitoring on for a
	// router somebody was watching did nothing until a restart: the setting
	// saved, the holds followed it, and the session went on evaluating with the
	// old answer. Reported 2026-09-20 ("no netwatch alerts"), and reproduced by
	// enabling alerting on a test router: the rules stayed silent until the
	// process came back.
	//
	// `Manager.ApplyAlerts` sets it on save, beside ApplyPingTarget and
	// ApplyCollection, which exist for record fields with the same promise.
	// ATOMIC because the readers are collector goroutines: the emit closure asks
	// it for every payload.
	alertsEnabled atomic.Bool

	h *hub.Hub
	// fleet is the manager's fleet-wide status send; see Manager.SetFleetStatus.
	// Nil on a Session not built by a Manager.
	fleet *atomic.Pointer[func(frame map[string]any)]
	cfg   routeros.Config

	// conn is the fleet's connectivity debounce, captured from the Manager when
	// this session is built. Nil-safe: every method on it guards its own
	// receiver, so a Manager with none simply decides nothing.
	//
	// The ROUTER'S OWN THRESHOLD is not held here. It lives on the tracker, set
	// by `declareConnThreshold` from the fleet sync, because a copy taken at
	// build time is a copy that never hears about a save. See
	// `connstate.Tracker.SetThreshold`.
	conn *connstate.Tracker
	// identity is the Manager's identity writer, bound to this router; nil when
	// none is attached. Every System collector built by newSystem carries it.
	identity collect.IdentityFunc
	// wake interrupts the connect loop's retry sleep.
	//
	// BUFFERED, AND SENT TO WITHOUT BLOCKING, so a signal raised while the loop
	// is dialling rather than sleeping is remembered instead of lost, and no
	// caller can ever block on it. It exists because the auth backoff makes that
	// sleep up to five minutes long: without it, correcting a password would
	// leave the operator waiting out the rest of a sleep, and a teardown would
	// leave the goroutine parked for the same time.
	wake chan struct{}

	mu     sync.Mutex
	client *routeros.Client
	refs   int
	// holds is the NON-VIEWER reasons this session must stay alive, by name.
	//
	// ── PHASE 4.3: WHY A SEPARATE SET AND NOT MORE refs ─────────────────────
	//
	// `refs` counts viewers, and its zero has a meaning the grace timer depends
	// on: "the last browser left, start the countdown". A hold is a different
	// claim -- alerting, history recording, the Devices page -- and it does not
	// expire on a timer, because nobody is coming back to renew it.
	//
	// Folding the two into one counter would make "held for alerting" look like
	// a viewer who never leaves, and the linger timer would never run for a
	// router that has alerting on. Keeping them apart means the grace still
	// governs viewers exactly as it did, and a hold simply vetoes teardown.
	//
	// NAMED rather than counted, so a caller cannot leak a hold by releasing
	// twice, and so a stuck session can be explained: the names say who is
	// keeping it.
	holds map[string]bool
	// linger is the pending idle teardown, armed when the last viewer leaves and
	// stopped when one comes back. Non-nil only while the grace is running.
	linger    *time.Timer
	closed    bool
	connected bool
	// observed is whether the two fields above are an ANSWER yet. See Observed.
	observed bool
	lastErr  string

	dns *collect.DNS
	// areas is ONE collector for every generated page: see internal/areas.
	areas    *collect.Areas
	bridges  *collect.Bridges
	vlans    *collect.Vlans
	wan      *collect.Wan
	packages *collect.Packages
	routing  *collect.Routing
	ifStatus *collect.IfStatus

	dhcpLeases   *collect.DHCPLeases
	dhcpNetworks *collect.DHCPNetworks
	ppp          *collect.PPP
	vpn          *collect.VPN
	netwatch     *collect.Netwatch
	talkers      *collect.Talkers
	ping         *collect.Ping
	rosUsers     *collect.RosUsers
	queues       *collect.Queues
	firewall     *collect.Firewall
	wifi         *collect.Wifi
	capsman      *collect.Capsman
	system       *collect.System
	logs         *collect.Logs
	topology     *collect.Topology
	wireless     *collect.Wireless
	bandwidth    *collect.Bandwidth
	traffic      *collect.Traffic
	conns        *collect.Connections
	// arp has no payload and no page: it is the IP<->MAC join four collectors
	// read in memory. See internal/collect/arp.go.
	arp *collect.ARP

	// payloads counts what the derivation layer produces, for the API
	// Diagnostics card. Its own lock: the emit closure is the hottest path in
	// the session and must not queue behind whatever holds `mu`.
	payloads rollingMin

	// pendingResume holds page-focus resumes that arrived BEFORE the router
	// connection came up. Guarded by mu. See ResumeCollector and replayResumes.
	pendingResume map[string]bool

	// writeMu serialises read-modify-write sequences against this router.
	writeMu sync.Mutex
}

// Connected reports the live state, which the page shows as the router status
// chip.
func (s *Session) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

// Observed reports whether `Connected` and `LastError` are an ANSWER rather than
// Go's zero values.
//
// A Session exists from `Acquire`, before its first dial returns, and the
// Devices page reads it for the router being watched. Without this, that router
// spends its first moments claiming to be offline for no stated reason — the
// same defect `routers.OverviewSession.observed` records at length.
func (s *Session) Observed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.observed
}

// WaitConnected blocks until this session has a live connection, the context is
// done, or the session is closed. It reports whether it is connected.
//
// ── WHY IT EXISTS ──────────────────────────────────────────────────────
//
// `Retain` returns as soon as the DIAL STARTS, which is right for a hold taken
// to keep a router polled and wrong for one taken to read from it now: the read
// runs against a session with no client and answers `not connected`. The fleet
// endpoints hit exactly that, and only routers already kept connected for
// alerting or history ever answered them.
//
// ── WHY A POLL AND NOT A SIGNAL ──────────────────────────────────────
//
// A channel closed on connect has to be re-armed on every reconnect, by the
// connect loop, for one caller that does not otherwise exist in its world. This
// costs a mutex read every 25ms on a request that is already waiting on a TCP
// dial and a RouterOS login.
func (s *Session) WaitConnected(ctx context.Context) bool {
	for {
		s.mu.Lock()
		up, gone := s.connected, s.closed
		s.mu.Unlock()
		if up {
			return true
		}
		if gone {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// DNS is the collector, for the replay a page:focus does and for the
// RefreshNow a write triggers.
func (s *Session) DNS() *collect.DNS { return s.dns }

// Areas is the collector behind every generated page.
func (s *Session) Areas() *collect.Areas { return s.areas }

// Bridges is the collector behind the Bridges page.
func (s *Session) Bridges() *collect.Bridges { return s.bridges }

// Vlans is the collector behind the VLANs page.
func (s *Session) Vlans() *collect.Vlans { return s.vlans }

// Wan is the collector behind the WAN page.
func (s *Session) Wan() *collect.Wan { return s.wan }

// Packages is the package/firmware/update collector.
func (s *Session) Packages() *collect.Packages { return s.packages }

// Routing is the route-table and BGP collector.
func (s *Session) Routing() *collect.Routing { return s.routing }

// DHCPLeases is the lease-table collector. It is also the LeaseIPs behind
// dhcpNetworks' per-subnet lease counts.
func (s *Session) DHCPLeases() *collect.DHCPLeases { return s.dhcpLeases }

// ARP is the IP<->MAC join. No page reads it; four collectors do.
func (s *Session) ARP() *collect.ARP { return s.arp }

// DHCPNetworks is the subnet and pool collector behind the DHCP page's top row.
func (s *Session) DHCPNetworks() *collect.DHCPNetworks { return s.dhcpNetworks }

// PPP is the PPPoE/L2TP session collector behind the PPP page.
func (s *Session) PPP() *collect.PPP { return s.ppp }

// VPN is the WireGuard, PPP-tunnel and IPsec collector behind the VPN page.
func (s *Session) VPN() *collect.VPN { return s.vpn }

// RosUsers is the RouterOS accounts collector behind the Router Users page.
func (s *Session) RosUsers() *collect.RosUsers { return s.rosUsers }

func (s *Session) Queues() *collect.Queues { return s.queues }

func (s *Session) Firewall() *collect.Firewall { return s.firewall }

func (s *Session) Wifi() *collect.Wifi { return s.wifi }

func (s *Session) Capsman() *collect.Capsman { return s.capsman }

// Netwatch is the host-monitoring collector. It has NO page — it feeds a card on
// the Dashboard — so nothing in ws.go resumes or suspends it by page focus, and
// it runs for as long as the router session does.
func (s *Session) Netwatch() *collect.Netwatch { return s.netwatch }

// Ping feeds the Dashboard's latency block. Like netwatch and talkers it has no
// page of its own.
func (s *Session) Ping() *collect.Ping { return s.ping }

// IfStatus is the interface collector. It is also the RateSource behind the
// throughput columns on Bridges, VLANs and WAN.
func (s *Session) IfStatus() *collect.IfStatus { return s.ifStatus }

// System is the resource collector, for the Devices page's stats payload — it
// reads `Last()` off the three collectors the live overview pool also runs.
func (s *Session) System() *collect.System   { return s.system }
func (s *Session) Talkers() *collect.Talkers { return s.talkers }

// Live is a snapshot of the sessions that currently exist.
//
// ── EXISTING IS NOT CONNECTED, AND THE DEVICES PAGE CARES ───────────────────
//
// A session is created before it connects, so the caller must read `Connected()`
// rather than treating presence as health. What PRESENCE decides is different
// and also load-bearing: a router with an interactive session must be EXCLUDED
// from the background pool, or it carries two connections and every up/down
// transition is recorded twice.
// RoomFor is the hub room one router's sub-room resolves to.
//
// The convention is defined by the `emit` closure below and was rebuilt by hand
// in `ws.go` wherever a viewer joined or left. Exported so the two cannot drift:
// a joiner using a different prefix sits in a room nobody sends to, and the only
// symptom is a chart that stays empty.
func RoomFor(routerID, sub string) string { return "router-" + routerID + "-" + sub }

func (m *Manager) Live() map[string]*Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]*Session, len(m.live))
	for id, s := range m.live {
		out[id] = s
	}
	return out
}
func (s *Session) Logs() *collect.Logs           { return s.logs }
func (s *Session) Topology() *collect.Topology   { return s.topology }
func (s *Session) Wireless() *collect.Wireless   { return s.wireless }
func (s *Session) Bandwidth() *collect.Bandwidth { return s.bandwidth }

// CollectorEnabled reports whether a collector may run at all. Only the
// install-wide ping switch (Settings, Ping / Latency) makes this false today:
// per-router collector switching was removed on 2026-09-15.
//
// The page-focus RESUME path needs it as much as the connect path does: a
// collector switched off must not come back the moment somebody opens its page,
// which is exactly what an ungated `Resume()` would do. An unknown key reads as
// ENABLED.
func (s *Session) CollectorEnabled(key string) bool {
	v, ok := s.conf().Enabled[key]
	return !ok || v
}
func (s *Session) Traffic() *collect.Traffic   { return s.traffic }
func (s *Session) Conns() *collect.Connections { return s.conns }

// Username is the RouterOS account this process logs in as. selfPath matches
// /user/active rows by it to find where the router sees us from.
//
// UNDER THE LOCK, as the two below are: `Reconfigure` swaps `cfg` while this
// session is running, so an unsynchronised read here is a data race.
func (s *Session) Username() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Username
}

// Host is the address this session was configured to reach the router at.
//
// A RESTORE binds its capability token to it, so a token that leaks off the box
// cannot be redeemed from anywhere else — see internal/backups/restoretoken.go.
func (s *Session) Host() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Host
}

// APIPort is the port this router is actually reached on, not a guess.
//
// fwGuard needs it exactly: a filter rule that spares 8729 still locks us out of
// a router we talk to on 8728, and a guard that assumed the default would stay
// silent on the rule that mattered.
func (s *Session) APIPort() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Port
}

// UsesTLS is whether this session speaks api-ssl rather than api, which is
// what names the /ip/service row a write must not cut (guard/serviceguard.go).
func (s *Session) UsesTLS() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.TLS
}

// globalDefaultIf is the install-wide default interface, the middle rung of the
// precedence `routers.DefaultIfFor` resolves. Read straight off the settings map
// rather than through `store.Merge`: the environment overlay is the server's
// business, and this only needs the stored value to stop being the odd one out.
func globalDefaultIf(cfg store.Settings) string {
	// `defaultIf` — the key the settings table declares. `defaultInterface` is
	// not one, and reading it returned "" on every install.
	v, _ := cfg["defaultIf"].(string)
	return v
}

// defaultIfOr is index.js's fallback: a router record that names no default
// interface still needs one, because the WAN badge reads it.
func defaultIfOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

// reader adapts the session to collect.Reader. The indirection matters: the
// client is REPLACED on a reconnect, and a collector holding the old pointer
// would read from a closed connection for ever.
//
// reader's source names who its commands are counted against (sources.go).
// Empty is the collectors, which is every reader the session hands out; Exec
// builds one naming its caller.
type reader struct {
	s      *Session
	source string
}

func (r reader) Connected() bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.client != nil && r.s.connected
}

// Stream is the half of the connection that keeps a channel open: the cache's
// stream fills (`=interval=` prints), ping, and the log tail's /log/listen.
func (r reader) Stream(cmd routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	r.s.mu.Lock()
	c := r.s.client
	r.s.mu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	// ── COUNTED, THOUGH NOT GATED ───────────────────────────────────────
	//
	// `Do` takes one of `roslimit`'s eight in-flight slots and this takes none,
	// which is correct -- a held channel is not an outstanding command, and
	// gating streams on the command budget would let a page's chart block a
	// collector's read. But it left the app unable to say how many channels it
	// holds on a router, against the bottleneck it documents as concurrent
	// channels.
	//
	// B.4 enables streaming collectors one at a time and measures each. This is
	// the number it reads. An instrument, not a limit: B.0b searched to 24
	// concurrent channels on live hardware and found no ceiling.
	// THE OPENING IS A COMMAND, and it was not counted: the channel was, as a
	// level, but the rate never saw the command that opened it.
	roslimit.Note(r.s.RouterID, cmd.Path, SourceCollectors)
	stop, err := c.Stream(cmd, onRow)
	if err != nil {
		return nil, err
	}
	done := roslimit.StreamOpened(r.s.RouterID)
	return func() {
		stop()
		done()
	}, nil
}

// StreamUntilDone is Stream plus notification that the stream ENDED BY ITSELF,
// and how: see (*routeros.Client).StreamUntilDone.
//
// The frequency scan and the Tools page's diagnostics are its callers: BOUNDED
// commands, unlike the /listen channels Stream serves, and the difference
// between "it ended" and "we stopped it" decides whether a /cancel is written to
// a device that has already finished.
//
// ── COUNTED AS A HELD CHANNEL, AS Stream IS ────────────────────────────────
//
// Not gated on the command budget, for Stream's reason: a channel held for a
// scan or a ten-second torch is not an outstanding command, and taking one of
// the eight slots for it would let a diagnostic hold a collector's read back.
// The level is released exactly once, when the stream ends by itself or when
// stop() returns — whichever comes first.
//
// ── AND COUNTED AS A COMMAND, BY WHO ASKED ─────────────────────────────────
//
// The Tools page's ping, traceroute, torch and bandwidth test, and the WiFi
// frequency scan, reached the router here and never entered the command rate:
// only `reader.Do` noted a command. See sources.go for how the caller is named.
func (s *Session) StreamUntilDone(
	cmd routeros.Cmd, onRow func(routeros.Reply), onDone func(error),
) (func(), error) {
	source := callerSource()
	if s.execForTest != nil {
		// A scripted router answers the whole run at once, then ends it.
		roslimit.Note(s.RouterID, cmd.Path, source)
		rows, err := s.execForTest(cmd)
		for _, row := range rows {
			onRow(row)
		}
		if onDone != nil {
			onDone(err)
		}
		return func() {}, nil
	}
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	roslimit.Note(s.RouterID, cmd.Path, source)
	opened := roslimit.StreamOpened(s.RouterID)
	stop, err := c.StreamUntilDone(cmd, onRow, func(err error) {
		opened()
		if onDone != nil {
			onDone(err)
		}
	})
	if err != nil {
		opened()
		return nil, err
	}
	return func() {
		stop()
		opened()
	}, nil
}

// Do is the one place a request-and-reply command reaches this router, for the
// collectors and for every page and feature (Exec) alike, and it counts the
// command against the reader's source.
func (r reader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	source := r.source
	if source == "" {
		source = SourceCollectors
	}
	if r.s.execForTest != nil {
		// COUNTED AS A ROUTER'S COMMAND WOULD BE: the scripted router is the
		// router here, and a test of what the card counts drives this path.
		roslimit.Note(r.s.RouterID, cmd.Path, source)
		cmd.Finish()
		rows, err := r.s.execForTest(cmd)
		// A scripted `add` answers its new id as a row holding only `ret`, which
		// becomes Ret, as a router's `!done =ret=` does through Client.Do.
		if cmd.Ret != nil && err == nil {
			kept := rows[:0:0]
			for _, row := range rows {
				if id, ok := row["ret"]; ok && len(row) == 1 {
					*cmd.Ret = id
					continue
				}
				kept = append(kept, row)
			}
			rows = kept
		}
		return rows, err
	}
	r.s.mu.Lock()
	c := r.s.client
	r.s.mu.Unlock()
	if c == nil {
		cmd.Finish()
		return nil, errNotConnected
	}
	if cmd.Timeout == 0 {
		cmd.Timeout = 15 * time.Second
	}
	// ── ONE ROUTER'S BUDGET, SHARED WITH THE TWO POOLS ───────────────────
	//
	// Taken AFTER the connection check and the timeout default, so a call that
	// was never going to reach the router does not hold a slot while it fails.
	//
	// ── RELEASED WHEN THE COMMAND IS OVER, NOT WHEN DO RETURNS ──────────────
	//
	// This was `defer done()`. A command past its deadline is still running on
	// the router until Client.Do's `/cancel` ends it, so releasing on return
	// would let the cap be exceeded by exactly the commands a slow router is
	// struggling with. The release rides
	// `Cmd.OnFinished`, and also runs the moment Do returns WITHOUT a timeout: the
	// command is over then, and a Do that never calls Finished still cannot leak
	// the slot. The OnceFunc makes the two one release.
	roslimit.Note(r.s.RouterID, cmd.Path, source)
	release := sync.OnceFunc(roslimit.Acquire(r.s.RouterID))
	rows, err := c.Do(cmd.OnFinished(release))
	if !errors.Is(err, context.DeadlineExceeded) {
		release()
	}
	return rows, err
}

type notConnected struct{}

func (notConnected) Error() string { return "routeros: not connected" }

var errNotConnected = notConnected{}

// Manager hands out sessions and keeps at most one per router.
// DefaultIdleGrace is how long a router's session stays warm after its last
// viewer leaves.
//
// TWO MINUTES IS A JUDGEMENT, not a measurement: long enough that a refresh, a
// tab switch or a short walk away costs nothing, short enough that a browser
// closed and forgotten frees the channel while the operator is still at the
// desk. The cost of being wrong in one direction is one held API channel for two
// minutes; in the other it is the dashboard charts starting from nothing.
const DefaultIdleGrace = 2 * time.Minute

type Manager struct {
	store *store.Store
	h     *hub.Hub

	mu   sync.Mutex
	live map[string]*Session

	// fleetStatus sends a router's `router:status` to every browser that may
	// read that router. See SetFleetStatus and Session.announce.
	fleetStatus atomic.Pointer[func(frame map[string]any)]

	// idleGrace is how long a session outlives its last viewer. Zero means
	// DefaultIdleGrace; tests set it small.
	idleGrace time.Duration
	// onIdle fires after a session has actually been torn down, so the caller
	// can hand the router to the pool at that moment rather than at Release.
	onIdle func(routerID string)

	// alerts evaluates collector payloads into alert rows. NIL WHEN NO HISTORY
	// DATABASE IS CONFIGURED, and nil is inert — `Wire.Evaluate` guards on the
	// receiver, so a deployment without `/data` still serves every page and
	// simply files nothing.
	//
	// IT DOES NOT DISPATCH. See `internal/alertwire`'s header: notifications are
	// step 2 and the switch is the operator's.
	alerts *alertwire.Wire
	// onFired receives what the evaluator produced, for delivery. Nil until the
	// server attaches it, and nil is inert — the rows are still written.
	onFired func(routerID, routerLabel string, fired []alert.Fired)

	// history buckets traffic and ping samples into the minute rows the Reports
	// pages read, and records connectivity transitions.
	//
	// NIL IS INERT, exactly like `alerts`: every method guards on the receiver,
	// so a deployment with no history database — or one running beside the Node
	// app, which is the case that matters — records nothing and serves every
	// page unchanged.
	//
	// IT IS OFF DURING COEXISTENCE and the reason is arithmetic rather than
	// caution: two processes bucketing the same per-second samples into one
	// SQLite file write TWO rows per minute per interface, and Reports averages
	// by minute. The chart would not look broken; it would look plausible and be
	// wrong, which is worse.
	// history is the RECORDER, for traffic and ping rows off the emit seam
	// (`Record`, `Flush`). It no longer carries connectivity: that is `conn`
	// below, which has to run whether or not anything is being recorded.
	history *historywire.Wire
	// conn is the fleet's connectivity debounce — who is OFFLINE, as opposed to
	// whose socket is shut this instant. See internal/connstate.
	conn *connstate.Tracker

	// onIdentity writes what a router reports about ITSELF onto its record.
	// Nil until the server attaches it, and nil is inert. See SetOnIdentity.
	onIdentity func(routerID string, id collect.Identity)

	// docs reads the OPERATOR'S own documents for a router — the declared uplink
	// list, the pinned cabling, the site plan. Nil until the server attaches it,
	// and nil is inert: a collector with no source behaves as it always did.
	// See SetDocSource and internal/sitedoc.
	docs func(routerID, kind string) []byte
}

func NewManager(st *store.Store, h *hub.Hub) *Manager {
	return &Manager{store: st, h: h, live: map[string]*Session{}}
}

// SetAlertWire attaches the evaluator. Called once at startup, before any
// session exists, so no lock is taken around the field itself.
func (m *Manager) SetAlertWire(w *alertwire.Wire) { m.alerts = w }

// SetAlertSink attaches the delivery half.
//
// SEPARATE FROM `SetAlertWire` because they are separate decisions: the wire
// EVALUATES and records, which this port does during coexistence, and the sink
// SENDS, which waits on `-alert-dispatch`. Folding them together would make
// "write the row" and "notify somebody" one switch, and `alert_wire.go` explains
// at length why they are not: "A row filed twice is a duplicate an operator can
// delete. A message sent twice is not."
func (m *Manager) SetAlertSink(fn func(routerID, routerLabel string, fired []alert.Fired)) {
	m.onFired = fn
}

// Announce re-sends one router's status frame.
//
// Called when the DEBOUNCE changes its mind — an outage declared by the ticker
// rather than by an event — because nothing else is happening at that moment:
// the socket closed thirty seconds ago and the loop has long since moved on.
// Without it the fleet badge would wait for the next Devices tick.
func (m *Manager) Announce(routerID string) {
	m.mu.Lock()
	s := m.live[routerID]
	m.mu.Unlock()
	if s != nil {
		s.announce()
	}
}

// AlertRouter is what the rules need to know about a router, plus the label an
// alert message is addressed with, read off the live session that already holds
// both. `ok` is false when nothing holds this router.
//
// ── READ FROM THE SESSION, NEVER FROM THE STORE ────────────────────────────
//
// The caller is the connectivity verdict, which fires on every connect. Reading
// the record there would mean `store.Routers()` — which decrypts every router's
// password with scrypt — on every reconnect of every router. That cost is
// exactly why the server's old per-router threshold cache existed; the session
// already carries both facts, so nothing needs caching.
func (m *Manager) AlertRouter(routerID string) (alert.Router, string, bool) {
	m.mu.Lock()
	s := m.live[routerID]
	m.mu.Unlock()
	if s == nil {
		return alert.Router{}, "", false
	}
	return alert.Router{ID: s.RouterID, AlertsEnabled: s.alertsEnabled.Load()}, s.Label, true
}

// SetHistoryWire installs the traffic and ping recorder. Nil, or a wire built
// with `enabled` false, records nothing.
func (m *Manager) SetHistoryWire(w *historywire.Wire) { m.history = w }

// SetConnTracker installs the fleet's connectivity debounce. Nil is inert.
func (m *Manager) SetConnTracker(t *connstate.Tracker) { m.conn = t }

// SetOnIdentity attaches the writer for what a router reports about ITSELF:
// model, serial, and the RouterOS version, which changes on every upgrade.
//
// ── THE SESSION REPORTS IT, BECAUSE NOTHING ELSE RUNS FOR A HELD ROUTER ─────
//
// The Devices pool used to be the only reporter, and the pool EXCLUDES every
// router with a live session (`syncPool`). Since phase 4.3 held sessions keep
// every enabled router live, so the pool ran no System collector for any of
// them and the stored version froze: Settings → Devices showed one router on
// `7.24` two upgrades later. A session runs a System collector wherever its
// router is watched or held, and primes one for a warm hold, so it is the one
// place that reaches every router.
//
// CALL IT BEFORE THE FIRST SESSION IS BUILT. A session takes the writer when it
// is built (`identityFor`, in Acquire), and a held session is built by the
// server's first fleet sync — a writer attached after that reaches none of them.
func (m *Manager) SetOnIdentity(fn func(routerID string, id collect.Identity)) { m.onIdentity = fn }

// SetDocSource attaches the reader for the operator's own documents.
//
// ── ATTACH IT BEFORE THE FIRST SESSION IS BUILT ─────────────────────────────
//
// A session takes the reader when it is BUILT, exactly like the identity writer,
// so one attached later reaches no session that already exists — and the held
// sessions are built by the server's first fleet sync. The consequence is quiet:
// a router whose operator declared its uplinks by hand would keep reporting
// whatever `detect-internet` says, with nothing to explain why.
func (m *Manager) SetDocSource(fn func(routerID, kind string) []byte) { m.docs = fn }

// SetFleetStatus attaches the fleet-wide half of `router:status`: the server's
// per-socket send to every browser that may read the router.
//
// READ WHEN A STATUS IS ANNOUNCED, NOT CAPTURED WHEN A SESSION IS BUILT, so a
// held session built before this is attached still uses it. Unset, no status
// leaves a router's own room.
func (m *Manager) SetFleetStatus(fn func(frame map[string]any)) { m.fleetStatus.Store(&fn) }

// docsFor binds the document reader to one router, or returns nil.
func (m *Manager) docsFor(routerID string) collect.DocSource {
	fn := m.docs
	if fn == nil {
		return nil
	}
	return func(kind string) []byte { return fn(routerID, kind) }
}

// identityFor binds the identity writer to one router, or returns nil.
func (m *Manager) identityFor(routerID string) collect.IdentityFunc {
	fn := m.onIdentity
	if fn == nil {
		return nil
	}
	return func(id collect.Identity) { fn(routerID, id) }
}

// newSystem builds a System collector that reports this router's identity.
//
// EVERY System collector a session builds comes through here — the running one
// and the prime's throwaway — so none can be built without the hook. The prime
// matters on its own: a WARM session runs no collectors, and its one prime tick
// is the only reading it takes. TestEverySessionSystemCollectorIsBuiltWithTheIdentityHook
// holds the rule.
func (s *Session) newSystem(r collect.Reader, emit collect.Emit) *collect.System {
	c := collect.NewSystem(r, emit, s.conf().Poll["system"])
	c.SetOnIdentity(s.identity)
	return c
}

// geoLookup is the country join both connections and bandwidth use.
//
// Resolved PER SESSION rather than captured once, because the database is
// loaded at startup and a deployment without it must still serve pages. Nil
// when unavailable, which is precisely the live app's degraded state: countries
// empty, everything else unchanged.
func geoLookup() collect.GeoLookup {
	db, ok := geo.Current()
	if !ok {
		return nil
	}
	return db.Lookuper()
}

// orgLookup is the organisation join, resolved per session for the same reason
// geoLookup is: the database is loaded at startup and a deployment without it
// must still serve pages. Nil when unavailable, which leaves the badges empty
// and everything else unchanged.
func orgLookup() collect.OrgLookup {
	db, ok := asn.Current()
	if !ok {
		return nil
	}
	return db.Lookuper()
}

// Acquire returns the session for a router, connecting if this is the first
// caller. The caller must Release exactly once.
func (m *Manager) Acquire(routerID string) (*Session, error) {
	m.mu.Lock()
	if s, ok := m.live[routerID]; ok {
		s.mu.Lock()
		s.refs++
		// SOMEBODY CAME BACK inside the grace. Stopping the timer is what makes
		// the session survive a refresh with its history intact; without it the
		// teardown still fires and takes the collectors out from under the viewer
		// who just arrived.
		if s.linger != nil {
			s.linger.Stop()
			s.linger = nil
		}
		s.mu.Unlock()
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()

	// Read the credential outside the map lock: decryption is scrypt-backed and
	// holding the lock across it would serialise every router's first viewer.
	routers, problems := m.store.Routers()
	for _, p := range problems {
		log.Printf("[session] %v", p)
	}
	var rec *store.Router
	for i := range routers {
		if routers[i].ID == routerID {
			rec = &routers[i]
			break
		}
	}
	if rec == nil {
		return nil, errUnknownRouter
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	// Another caller may have built it while the credential was being read.
	if s, ok := m.live[routerID]; ok {
		s.mu.Lock()
		s.refs++
		if s.linger != nil {
			s.linger.Stop()
			s.linger = nil
		}
		s.mu.Unlock()
		return s, nil
	}

	// #105: the router's effective config. A settings file that cannot be read
	// is not a reason to refuse the router — `Resolve(nil, …)` gives every
	// collector its own default, which is what an installation that has changed
	// nothing gets anyway.
	cfgSettings, err := m.store.Settings()
	if err != nil {
		log.Printf("[session] %s: settings unreadable (%v); using collector defaults", rec.Label, err)
		cfgSettings = nil
	}
	eff := collection.Resolve(cfgSettings, collection.ParseRouter(rec.Collection))

	s := &Session{
		dormancy: dormancy.NewSupervisor(dormancy.Defaults()),
		RouterID: rec.ID,
		Label:    rec.Label,
		h:        m.h,
		fleet:    &m.fleetStatus,
		conn:     m.conn,
		identity: m.identityFor(rec.ID),
		refs:     1,
		holds:    map[string]bool{},
		wake:     make(chan struct{}, 1),
		cfg: routeros.Config{
			Host: rec.Host, Port: rec.Port,
			Username: rec.Username, Password: rec.Password,
			TLS: rec.TLS, InsecureTLS: rec.TLSInsecure,
			// "RouterOS debug" from the Settings page. THIS SITE ONLY, matching
			// live exactly: it has five `new ROS(` call sites — the session, the
			// alert sessions, the overview sessions, a second index one and the
			// connection test — and sets `debug` on precisely one of them,
			// `src/index.js:444`, the page-serving session. The pools here are
			// left untraced for the same reason.
			Debug: rosDebugOn(cfgSettings),
			Label: rec.Label,
		},
	}
	s.alertsEnabled.Store(rec.AlertsEnabled)
	s.eff.Store(&eff)
	room := "router-" + s.RouterID + "-"
	// An EMPTY sub means router-wide, the room `router:status` uses. It exists
	// for chrome: the gauges, the uptime chip and the RouterOS version row are
	// in the top bar, not on a page, so they must reach a viewer who has not
	// opened any particular page. Everything with a page of its own names it.
	// A sub naming SEVERAL rooms, comma separated, delivers ONE copy to the
	// union — interfaceStatus sends its full payload to three rooms and a viewer
	// can be in two of them. socket.io's `.to(a).to(b)` behaves the same way,
	// and looping Broadcast would send that viewer the frame twice.
	emit := hub.NewRelay(func(sub string, e hub.Named, payload any) {
		event := e.Name()
		// ── THE ALERT EVALUATOR SEES EVERY PAYLOAD ──────────────────────────
		//
		// One interception point rather than a call in each collector, which is
		// where the live app does it too: `alerter.evaluateForRouter(routerId,
		// event, data)` sits in the same single emit path.
		//
		// BEFORE the broadcast, and deliberately. A rule that files a row should
		// have done so by the time the browser is told the numbers that caused
		// it — otherwise a client asking for its alert list on receipt of the
		// payload can race the write and see the old set.
		//
		// It costs a map lookup on events with no rule, which is most of them.
		// THE RETURN VALUE IS THE ALERT. It was discarded here until 2026-08-30,
		// which is why nothing was ever sent — LOOP.md 0k.
		// ── AND THE DERIVATION LAYER'S OWN COUNTER, AT THE SAME SEAM ────────
		//
		// One call site rather than one per collector, for the reason the two
		// interceptions below share: a collector that forgot to count would be
		// invisible on the card, and "the number looks a bit low" is not a
		// symptom anybody chases. See internal/session/diagnostics.go.
		s.notePayload()

		fired := m.alerts.Evaluate(alert.Router{
			ID: s.RouterID, AlertsEnabled: s.alertsEnabled.Load(),
		}, event, payload)
		if m.onFired != nil && len(fired) > 0 {
			m.onFired(s.RouterID, s.Label, fired)
		}

		// ── AND SO DOES THE HISTORY RECORDER, AT THE SAME SEAM ──────────────
		//
		// The same single interception point, for the same reason, and here it
		// also closes a hazard the live app had to work around. Its comment:
		// `recordPing` "used to sit on the router-wide emit() alone, so the
		// moment ping:update became page-scoped (issue #108) history would have
		// stopped being written, silently".
		//
		// This closure runs BEFORE any room is chosen, so it sees a page-scoped
		// event exactly as it sees a router-wide one. The live hazard cannot
		// arise here — which is the reason to use the seam rather than an
		// accident of it.
		//
		// Costs a type switch on events with no history, which is most of them.
		m.history.Record(s.RouterID, event, payload)

		if sub == "" {
			m.h.Forward([]string{"router-" + s.RouterID}, e, payload)
			return
		}
		if strings.Contains(sub, ",") {
			subs := strings.Split(sub, ",")
			rooms := make([]string, 0, len(subs))
			for _, one := range subs {
				rooms = append(rooms, room+strings.TrimSpace(one))
			}
			m.h.Forward(rooms, e, payload)
			return
		}
		m.h.Forward([]string{room + sub}, e, payload)
	})
	s.dns = collect.NewDNS(reader{s: s}, emit, s.conf().Poll["dns"])
	// THE OCCUPANCY ORACLE IS A CLOSURE over this session, not a captured hub:
	// within the collector, each area is read only while ITS page is open, and
	// occupancy is a question only the session can answer.
	s.areas = collect.NewAreas(reader{s: s}, emit).WithOccupancy(func(room string) bool {
		return s.roomsOccupied(collect.Rooms{room})
	})
	// Built FIRST, because three other collectors take it as their RateSource.
	// It is the only one they depend on, and it depends on none of them.
	s.ifStatus = collect.NewIfStatus(reader{s: s}, emit, rec.ID, s.conf().Poll["ifStatus"])
	// A REAL RateSource — `s.ifStatus`, built three lines up.
	//
	// This comment said "nil RateSource: interfaceStatus is not ported, so the
	// throughput column renders as an em dash". BOTH HALVES WERE FALSE, and the
	// line above it — "three other collectors take it as their RateSource" —
	// contradicted them in the same function. `internal/collect/ifstatus.go`
	// opens "the port of src/collectors/interfaceStatus.js", and the argument
	// below is not nil.
	//
	// Corrected 2026-08-29. A comment that UNDERSTATES what the code does is the
	// mirror of the `_sendNowLimiter` one fixed the same day: both send a reader
	// looking for work that is already finished.
	s.bridges = collect.NewBridges(reader{s: s}, emit, s.ifStatus, s.conf().Poll["bridges"])
	// Rates from `s.ifStatus` here too. STILL nil lease counts, though — and that
	// half was and remains true, for a reason of its own now that dhcpLeases IS
	// ported: vlans wants
	// per-VLAN client counts (`VlanClients()`), and the lease collector offers
	// addresses (`LeaseIPs()`), which is what dhcpNetworks needs. Joining leases
	// to VLANs is the vlans page's question, not this one's, so the column keeps
	// degrading to 0 until someone answers it.
	s.vlans = collect.NewVlans(reader{s: s}, emit, s.ifStatus, nil, s.conf().Poll["vlans"])
	s.wan = collect.NewWan(reader{s: s}, emit, s.ifStatus, s.conf().Poll["wan"]).
		WithDocs(m.docsFor(rec.ID))

	// ── ONE COALESCING CACHE PER ROUTER ────────────────────────────────────
	//
	// `ifStatus` and `wan` both read `/interface/print`, and `wan`'s proplist is
	// a strict subset of `ifStatus`'s, so whichever ticks first pays for the read
	// and the other is served from it. Both start at connect, so they always
	// overlap.
	//
	// The cache is shared BY ROUTER, not by collector: that is the only key that
	// matches what is scarce, for the same reason `roslimit` is keyed that way.
	// Collectors that have not opted in read directly and are unaffected.
	s.roscache = roscache.New(reader{s: s})
	// The tick is how often the demand set is re-read, not how often any menu
	// is fetched. It bounds how late a newly subscribed menu is picked up, so
	// it wants to be comfortably shorter than the shortest cadence anybody
	// asks for and no shorter than that.
	s.sched = roscache.NewScheduler(s.roscache, 250*time.Millisecond)
	// STARTED HERE, NOT WITH THE COLLECTORS, for two reasons.
	//
	// It reads nothing until something subscribes, and nothing subscribes until a
	// collector's Start, which runs after the first connect — so an idle loop on
	// an unconnected session costs a Demand() call every 250ms and issues no
	// command.
	//
	// And `TestTheBackgroundCollectorCountIsRecorded` counts `s.X.Start()` inside
	// the first-connect block below, with a regex, because that count is the
	// basis of the background-pool decision. A scheduler started there reads as a
	// fifteenth collector. The test caught exactly that, and the fix is placement
	// rather than a looser regex.
	//
	// THE ANCHOR IS ALSO A TRAP AND IT CAUGHT ME TWICE IN ONE EDIT. That test
	// finds its block by searching for the opening line of the first-connect
	// condition, so writing that line out in a comment ANYWHERE ABOVE IT moves
	// the anchor and the test then reads the wrong region. This paragraph
	// deliberately describes it instead of quoting it. The test file records the
	// same trap from the last time somebody hit it.
	s.sched.Start()
	s.packages = collect.NewPackages(reader{s: s}, emit, s.conf().Poll["packages"])
	s.routing = collect.NewRouting(reader{s: s}, emit, s.conf().Poll["routing"])
	// Built BEFORE dhcpNetworks, which takes it as its lease source: a subnet's
	// client count is the leases that fall inside it, and only this collector
	// knows what they are. Unlike vlans, this one is no longer nil.
	s.dhcpLeases = collect.NewDHCPLeases(reader{s: s}, emit, s.conf().Poll["dhcpLeases"])
	// ── BUILT BEFORE ITS FOUR CONSUMERS, AND IT EMITS NOTHING ──────────────
	//
	// The ARP table is the only place the router says which MAC is behind which
	// IP. `conns` and `bandwidth` need IP→MAC to reach a lease the address alone
	// does not find; `wireless` and `topology` need MAC→IP because a
	// registration row and an MNDP neighbour carry no address at all.
	//
	// It takes no `emit` because it has no audience — see internal/collect/arp.go.
	s.arp = collect.NewARP(reader{s: s}, s.conf().Poll["arp"])
	// The WAN interface name is the record's, falling back to "WAN1" inside the
	// collector exactly as index.js does.
	s.dhcpNetworks = collect.NewDHCPNetworks(reader{s: s}, emit, s.dhcpLeases, "", s.conf().Poll["dhcpNetworks"])
	// Its own poll interval, not the shared default: PPP rates are differences
	// between byte counters, so the interval IS the measurement window.
	s.ppp = collect.NewPPP(reader{s: s}, emit, s.conf().Poll["ppp"])
	s.vpn = collect.NewVPN(reader{s: s}, emit, s.conf().Poll["vpn"])
	// NOT suspended by page focus, because it has no page: the Dashboard card it
	// feeds is visible whenever anyone is looking at the router at all. The idle
	// gate in Manager.Release still stops it when the last viewer leaves.
	s.netwatch = collect.NewNetwatch(reader{s: s}, emit, s.conf().Poll["netwatch"])
	// Same reasoning as netwatch: no page of its own, so no page gate. It feeds
	// the Dashboard's Top Talkers card, and the idle gate in Manager.Release is
	// what stops it when the last viewer leaves.
	//
	// THE COUNT COMES FROM SETTINGS, as the live app's does. This passed a
	// literal 0 until 2026-08-29 — "the default is the only reachable value",
	// which was true when the port had no settings write and stopped being true
	// when item 1 of LOOP.md shipped one on 2026-08-28. Nothing failed when that
	// premise expired, which is why the operator found its sibling by using the
	// app: `topN` was hardcoded and "Top Connections N" did nothing.
	s.talkers = collect.NewTalkers(reader{s: s}, emit, s.conf().Poll["talkers"],
		topSetting(cfgSettings, "topTalkersN"))
	// Same again: the latency block is part of the Dashboard's network card, so
	// there is no page to gate on.
	//
	// THE ROUTER'S OWN TARGET. This passed "" — "the live default", on the
	// grounds that nothing could set another — and went on doing so after the
	// router form began writing `pingTarget`. The hAP AX3's record said 9.9.9.9
	// and it pinged 1.1.1.1, while the Devices pool beside it passed
	// `cfg.PingTarget` under a comment calling the two the same value. An empty
	// record still means 1.1.1.1: NewPing's default. A later edit arrives
	// through Manager.ApplyPingTarget.
	s.ping = collect.NewPing(reader{s: s}, emit, s.conf().Poll["ping"], rec.PingTarget)
	// THE USERNAME WE ACTUALLY CONNECT AS is what the lockout guard protects, so
	// it comes from the live config rather than from anything the page sends.
	// The live app also passes whatever routers.json separately holds, because
	// the two can drift — see ResolveSelf. This side has one source today, and
	// the slice is here so adding the second is a one-line change rather than a
	// signature change.
	s.rosUsers = collect.NewRosUsers(reader{s: s}, emit, []string{s.cfg.Username}, s.conf().Poll["rosusers"])
	// The FIREWALL COLLECTOR IS BUILT FIRST because Queues borrows it by
	// reference for its FastTrack banner. Only a SUMMARY crosses that boundary —
	// a reader holding `queues` but not `firewall` learns that FastTrack is on,
	// which is a fact about the Queues page's own correctness, not a firewall
	// listing. Until this was ported the banner reported "cannot say", which is
	// the same degradation the live app applies when Firewall collection is off.
	s.firewall = collect.NewFirewall(reader{s: s}, emit, s.conf().Poll["firewall"])
	s.wifi = collect.NewWifi(reader{s: s}, emit, s.conf().Poll["wifi"])
	s.capsman = collect.NewCapsman(reader{s: s}, emit, s.conf().Poll["capsman"])
	s.queues = collect.NewQueues(reader{s: s}, emit, s.firewall, s.conf().Poll["queues"])
	// NOT gated on page focus, for the same reason as netwatch: these are the
	// dashboard's gauges, and the dashboard is on screen whenever anyone is
	// looking at the router at all. The idle gate in Manager.Release still stops
	// it when the last viewer leaves.
	s.system = s.newSystem(reader{s: s}, emit)
	// The only STREAMING collector: /log/listen pushes an entry as the router
	// writes it. No poll interval, because there is nothing to poll.
	s.logs = collect.NewLogs(reader{s: s}, emit)
	// Built LAST, because it joins three of the others: ifStatus names the
	// bridges, dhcpLeases names the clients, and system fills the core's
	// identity and gauges. Each is optional — a nil one costs exactly the field
	// it feeds, which is what the live app does when a collector is disabled.
	s.topology = collect.NewTopology(reader{s: s}, emit, s.ifStatus, rec.ID, rec.Label, s.conf().Poll["topology"]).
		WithDocs(m.docsFor(rec.ID)).
		WithSources(s.dhcpLeases, s.system).
		// Fills `TopoInput.ARPIP`, which was declared and used at two sites from
		// the port and never set: a neighbour whose own row carries no address
		// had no way to be pinged, and so no status.
		WithARP(s.arp)
	// The lease source names the clients and ARP gives them an address. Both are
	// needed: a registration row has a MAC and nothing else, so a client with no
	// DHCP name shows as its MAC — which is what the live app does on a router
	// that is not the client's DHCP server either.
	s.wireless = collect.NewWireless(reader{s: s}, emit, s.dhcpLeases, s.conf().Poll["wireless"]).
		WithARP(s.arp).
		// The last fallback: reverse DNS on the address ARP found, for a device
		// with a static address and no lease. One cache per session, cleared on
		// reconnect — see internal/collect/ptr.go.
		WithPTR(collect.NewPTRCache())
	// ifStatus names the interface a source arrived on, dhcpLeases names the
	// device, and dhcpNetworks supplies the LAN ranges the source filter uses.
	// Each is optional and costs exactly the field it feeds.
	// ONE READ OF THE CONNECTION TABLE, TWO CONSUMERS. It is the heaviest read
	// this app makes and both of these want it -- and they get one read because
	// they SUBSCRIBE TO THE SAME MENU with identical proplists, so the demand set
	// coalesces them. `ConnTable`, the hand-built snapshot that used to do this,
	// is gone: it was the right mechanism before there was a general one.
	s.conns = collect.NewConnections(reader{s: s}, emit, s.dhcpLeases, s.dhcpNetworks, s.conf().Poll["conns"]).
		// The heavy per-country and per-source indexes are built only when
		// somebody is on the Connections page. The hub's room occupancy is the
		// same question the Node side asks its adapter.
		// "Top Connections N" under Limits. See WithTopN for why this applies at
		// construction rather than instantly.
		WithTopN(topSetting(cfgSettings, "topN")).
		WithDetailed(func() bool { return m.h.Occupants(room+"page-connections") > 0 }).
		// The List tab's rows, only while somebody has that tab open.
		WithListed(func() bool { return m.h.Occupants(room+collect.ConnListRoom) > 0 }).
		WithGeo(geoLookup()).
		WithOrg(orgLookup()).
		// Step 2 of `nameOf`: the lease keyed by the MAC ARP found, when the
		// lease keyed by the address does not exist.
		WithARP(s.arp)
	s.bandwidth = collect.NewBandwidth(reader{s: s}, emit, s.ifStatus, s.dhcpLeases, s.dhcpNetworks, s.conf().Poll["bandwidth"]).
		WithGeo(geoLookup()).
		WithOrg(orgLookup()).
		WithARP(s.arp)
	// The default interface is what the WAN badge watches, so it is always in
	// the stream even when nobody has selected it. Five minutes of history, as
	// the live app keeps.
	//
	// ── THE FALLBACK IS "ether1", NOT "WAN1" ────────────────────────────────
	//
	// This was the third of three answers to one question. The Devices page
	// resolves router → global setting → "ether1"; both background pools took
	// the router's value RAW and streamed nothing without one; and this
	// substituted "WAN1", an interface name that exists on some MikroTik
	// defaults and not on most.
	//
	// So a router with no default interface recorded nothing in the background
	// and, the moment a browser attached, streamed an interface that probably
	// does not exist — which looks identical to a router that is simply quiet.
	// The global setting is honoured here now too, so the badge, the history and
	// the page all name the same interface.
	s.traffic = collect.NewTraffic(reader{s: s}, emit,
		defaultIfOr(rec.DefaultIf, defaultIfOr(globalDefaultIf(cfgSettings), "ether1")), 5)

	// ── WHO SHARES A MENU WITH WHOM ────────────────────────────────────────
	//
	// ONE PLACE, and after every collector is built rather than beside each
	// constructor. Opting in beside the constructor is what an earlier version
	// did, and it only worked for the two collectors built early enough; the
	// rest would have been wired before they existed. Keeping the list here also
	// makes it readable as what it is — the answer to "which collectors can save
	// each other a read" — instead of a line scattered through 150 lines of
	// construction.
	//
	// A collector NOT on this list reads directly and is unaffected. That is the
	// safe default and the reason this can be extended one menu at a time.
	for _, c := range []interface{ UseCache(*roscache.Cache) }{
		// ── SUBSCRIBED: the scheduler decides when these read ────────────────
		//
		// Phase 3.2. Each declares a menu and a cadence and stops owning a timer;
		// `ifStatus` is mechanism A, keeping a residual timer for its rates
		// measurement, which is set B and can never be scheduled.
		s.netwatch, s.talkers, s.dns, s.packages, s.rosUsers, s.queues,
		s.bridges, s.system, s.conns, s.dhcpLeases, s.dhcpNetworks,
		s.wan, s.capsman, s.ppp, s.routing, s.ifStatus, s.topology, s.vpn,
		// Mechanism B: the menu it wants is whichever firewall TABLE is on
		// screen, so its subscription moves when the operator changes tab.
		s.firewall,
		// Also mechanism B: each latches whether this router runs the modern or
		// the legacy wireless stack, and moves its subscription to match.
		s.wifi, s.wireless,
		// The last two, and the two whose recorded obstacle turned out to be
		// wrong rather than merely untried. `bandwidth` coalesces onto the
		// connection menu `conns` already subscribes to -- one read, two
		// deliveries -- and `vlans` is mechanism A with a residual half that
		// reads nothing at all. Every dormancy-eligible collector is now here.
		s.bandwidth, s.vlans,
		// One collector, every generated page. It subscribes to no menu — see
		// NewAreas — so it is here for the cache its shared reads go through.
		s.areas,
		// ── NOT SUBSCRIBED, AND HERE FOR THE CHANNEL ────────────────────────
		//
		// `traffic` schedules nothing: it is set B, an open channel with no
		// result to hold. It takes the cache because B.7's merge made its
		// channel a SHARED one — `ifStatus` holds the same fill for its rates —
		// and `UseCache` is how it is handed the thing to join. Without this it
		// holds no channel at all and every chart is empty, which is why it is
		// in the list rather than beside it.
		s.traffic,
		// Subscribed like any other table, and it feeds no page at all: four
		// collectors read its index in memory.
		s.arp,
	} {
		c.UseCache(s.roscache)
	}
	// ── B.3: AND THE DELIVERY DECISION, ONCE ────────────────────────────────
	//
	// The cache asks this before it subscribes a menu. Set here rather than per
	// collector because this is the only place that knows both halves: which
	// collector owns a menu, and what this router's resolved config says about
	// it. See streammenus.go, and note that the table it consults is EMPTY --
	// B.4 fills it one collector at a time.
	s.roscache.StreamWhen(s.streamsMenu)

	m.live[routerID] = s
	go s.connectLoop()
	return s, nil
}

// Release drops a reference and, when the last viewer of a router goes away,
// starts the idle grace rather than tearing the connection down on the spot.
//
// ── WHY A GRACE AT ALL ──────────────────────────────────────────────────────
//
// The idle gate is what stops a fleet holding one channel per router for ever,
// and that is still its job. But "the last viewer left" and "nobody is looking
// any more" are not the same event, and A PAGE REFRESH IS THE DIFFERENCE: the
// socket closes, the refcount hits zero, and a new socket arrives about a second
// later wanting exactly what was just thrown away.
//
// What was thrown away is not cheap. Traffic and ping accumulate their history
// INSIDE the collector, so tearing the session down is what makes the dashboard
// charts restart from nothing — the router can be re-read, but the last five
// minutes cannot be re-derived from anything.
//
// So the session outlives its last viewer by `idleGrace`. Return inside that
// window and Acquire finds the same Session, with its history, its connection
// and its collectors still warm; stay away and it goes exactly as before.
//
// ── IT STAYS IN `m.live`, AND THAT IS LOAD-BEARING ──────────────────────────
//
// A lingering session is still a live one. `syncFleetHolds` excludes routers that
// appear in `Live()`, so leaving it there is what stops the alert pool opening a
// SECOND connection to a router this session has not let go of yet. The pool
// picks it up when the grace expires, via `onIdle`.
func (m *Manager) Release(routerID string) {
	m.mu.Lock()
	s, ok := m.live[routerID]
	if !ok {
		m.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.refs--
	if s.refs <= 0 {
		// One timer per session: a second Release cannot arrive without an
		// intervening Acquire, which stops this one, but resetting is cheap and
		// makes the invariant local rather than argued.
		if s.linger != nil {
			s.linger.Stop()
		}
		s.linger = time.AfterFunc(m.grace(), func() { m.idleOut(routerID, s) })
	}
	s.mu.Unlock()
	m.mu.Unlock()
	// NO PRUNE HERE, and that is deliberate.
	//
	// A viewer leaving starts the GRACE; it does not mean nobody is coming back.
	// A page refresh, a router switch and a closed tab all look identical from
	// here, and the first two return within seconds. Pruning now would suspend
	// every collector the moment a browser blinked, and the viewer would come
	// back to a page waiting for data -- the exact churn the grace exists to
	// prevent, moved from the connection to the collectors.
	//
	// So the prune waits out the grace with the teardown, in `idleOut`. See
	// there for what happens at the end of it.
}

// AlertFeeds names the collectors whose payloads the alert rules consume.
//
// ── PHASE 4.3b: THE FEED IS DECLARED, NOT INFERRED ──────────────────────────
//
// The six rules read `ping:update`, `vpn:update`, `ifstatus:update`,
// `netwatch:update`, `system:update` and `routing:update`. Four of those
// collectors are started when a session connects. TWO ARE NOT: `vpn` is resumed
// by focusing the VPN page and `routing` by focusing the Dashboard, and nothing
// starts either one because the router has alerting switched on.
//
// They run anyway today, which is why nobody noticed -- `primeAll` gives them a
// payload and the dormancy probe then calls `ResumeCollector`. Verified live on
// 2026-09-08: land on the Dashboard, never open /vpn, and the WireGuard card
// populates and keeps advancing. But that is an ACCIDENT OF TWO OTHER
// MECHANISMS. Turn either off -- or change when a probe fires -- and VPN and BGP
// alerts stop firing for the router somebody is watching, with nothing to say so.
//
// So the dependency is written down. `internal/verify` checks this list against
// the events `alertwire` actually dispatches on, in both directions.
var AlertFeeds = []string{"ping", "vpn", "ifStatus", "netwatch", "system", "routing"}

// NeededForAlerts reports whether this collector must keep running because the
// router has alerting on, regardless of who is looking at what.
//
// This is what makes the feed page-independent. A page blur asks "is anybody
// still watching a room this collector emits to"; alerting is not watching a
// room, so the question does not reach it and the answer would be no.
func (s *Session) NeededForAlerts(key string) bool {
	if s == nil || !s.alertsEnabled.Load() {
		return false
	}
	for _, k := range AlertFeeds {
		if k == key {
			return true
		}
	}
	return false
}

// spokenForLocked reports whether anything still needs this session: a viewer,
// or a non-viewer hold. The caller holds s.mu.
//
// EXTRACTED SO IT CAN BE TESTED. The teardown around it dereferences a fully
// built session -- a client, a scheduler, a history wire -- so a test that drove
// `idleOut` with a hand-made Session panicked before reaching the assertion. The
// DECISION is the part worth pinning, and it is one line.
func (s *Session) spokenForLocked() bool {
	return s.refs > 0 || len(s.holds) > 0
}

// Retain keeps a session alive for a reason that is not a viewer.
//
// ── PHASE 4.3 ───────────────────────────────────────────────────────────────
//
// A Session already feeds the alert evaluator and the history recorder, at one
// interception point in its emit closure -- so it does everything the background
// pools do. The ONLY reason those pools exist is that a Session is reference
// counted against viewers and dies when the last browser leaves.
//
// This is what lets it outlive them. `Retain(id, "alerts")` says the router needs
// its rules evaluated whether or not anybody is looking; the session is built if
// it is not already there, and teardown is vetoed while any hold stands.
//
// IDEMPOTENT BY NAME. Two calls with the same reason are one hold, so a caller
// that re-syncs on every settings change cannot leak.
func (m *Manager) Retain(routerID, reason string) (*Session, error) {
	if reason == "" {
		return nil, errors.New("session: a hold needs a reason")
	}
	s, err := m.Acquire(routerID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.holds == nil {
		s.holds = map[string]bool{}
	}
	s.holds[reason] = true
	s.mu.Unlock()
	// Acquire took a VIEWER reference, and this is not a viewer. Giving it back
	// leaves the hold as the only thing keeping the session, which is what the
	// caller asked for -- and it means an unheld session still lingers and dies
	// exactly as before.
	m.Release(routerID)
	// ── AFTER THE RELEASE, AND THAT ORDER IS THE WHOLE THING ──────────────
	//
	// `applyReasons` does nothing while a viewer is present, and the Acquire
	// above took a viewer reference. Called BEFORE the release it therefore saw
	// refs == 1, concluded a viewer wanted everything, and returned -- leaving a
	// session held for alerting running all fifteen collectors.
	//
	// MEASURED, not reasoned. The command rate for one unwatched router went
	// from 119-120 a minute to 263-311. Every test was green: the tests ask what
	// the code DECIDES, and not one of them could see how much the router was
	// actually being asked.
	s.applyDemand()
	return s, nil
}

// Drop gives up one named hold. The session goes when nothing holds it and no
// viewer has it.
func (m *Manager) Drop(routerID, reason string) {
	m.mu.Lock()
	s, ok := m.live[routerID]
	m.mu.Unlock()
	if !ok {
		return
	}
	s.mu.Lock()
	delete(s.holds, reason)
	empty := len(s.holds) == 0 && s.refs <= 0
	s.mu.Unlock()
	s.applyDemand()
	if !empty {
		return
	}
	// The last hold is gone and no viewer is left, so start the same countdown a
	// departing viewer starts. NOT an immediate teardown: a router that loses
	// alerting and gains a viewer in the same sync should not be rebuilt.
	m.mu.Lock()
	s.mu.Lock()
	if s.linger != nil {
		s.linger.Stop()
	}
	s.linger = time.AfterFunc(m.grace(), func() { m.idleOut(routerID, s) })
	s.mu.Unlock()
	m.mu.Unlock()
}

// Held reports the reasons a session is being kept alive without a viewer.
func (m *Manager) Held(routerID string) []string {
	m.mu.Lock()
	s, ok := m.live[routerID]
	m.mu.Unlock()
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.holds))
	for r := range s.holds {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// sameConnection reports whether two configs reach the same router the same way.
//
// Named to stay clear of `routers.SameEndpoint`, which looks similar and answers
// a different question: that one decides whether a stored password may be reused
// for a connection test, and therefore compares everything EXCEPT the password.
// Here the password is the field that matters most.
//
// `Debug` and `Label` are deliberately NOT compared: neither changes where the
// connection goes or whether it is accepted, and redialling a working router
// because somebody renamed it would drop every collector for nothing.
func sameConnection(a, b routeros.Config) bool {
	return a.Host == b.Host && a.Port == b.Port &&
		a.Username == b.Username && a.Password == b.Password &&
		a.TLS == b.TLS && a.InsecureTLS == b.InsecureTLS
}

// Reconfigure points a LIVE session at changed credentials or a changed address,
// and reports whether anything had to move.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// `Acquire` reads the router record once and captures `cfg`; nothing re-read it
// afterwards. Two comments in this file asserted that a live edit rebuilt the
// session "which is what the live `collectionFingerprint` exists to decide", and
// that mechanism was never ported. So correcting a router's password did not
// reach the connection that was failing on the old one: the session went on
// dialling the stale credential every five seconds, and the only cure was
// restarting the container or deleting the device.
//
// That mattered most at exactly the wrong moment. Issue #124's credential was
// destroyed by a separate bug in `routerUpdate`; the operator's way out is to
// retype the password — and without this, retyping it changed the file and
// nothing else.
//
// ── SWAP AND DROP, RATHER THAN TEAR DOWN AND REBUILD ────────────────────────
//
// `CloseNow` would be the blunt version, and it strands whoever is watching:
// the collectors stop, the room stays joined, and the page sits on numbers that
// never change again. Instead this replaces `cfg` and closes the socket.
// `connectLoop` re-reads `cfg` on every pass, so its next turn dials the new
// endpoint and takes the ordinary reconnect path — the same one a router reboot
// produces, which already restarts the streams. Nobody is disconnected and no
// collector is rebuilt.
//
// A session that is already closed, or that is not live at all, is not an error:
// the record was still written, and the next `Acquire` reads it.
func (m *Manager) Reconfigure(routerID string, cfg routeros.Config) bool {
	m.mu.Lock()
	s, ok := m.live[routerID]
	m.mu.Unlock()
	if !ok {
		return false
	}

	s.mu.Lock()
	if s.closed || sameConnection(s.cfg, cfg) {
		s.mu.Unlock()
		return false
	}
	// THE FIELDS THAT DECIDE THE CONNECTION, and only those. `Debug` and `Label`
	// are this session's own — `Debug` comes from the settings file rather than
	// the router record, and copying the caller's would turn RouterOS tracing on
	// or off as a side effect of an unrelated edit.
	s.cfg.Host, s.cfg.Port = cfg.Host, cfg.Port
	s.cfg.Username, s.cfg.Password = cfg.Username, cfg.Password
	s.cfg.TLS, s.cfg.InsecureTLS = cfg.TLS, cfg.InsecureTLS
	c := s.client
	s.client = nil
	s.connected = false
	s.mu.Unlock()

	// OUTSIDE THE LOCK. Close talks to the driver, and `waitUntilDown` polls
	// `Connected()` from the connect goroutine; holding the session lock across
	// it is the deadlock this ordering avoids.
	if c != nil {
		_ = c.Close()
	}
	// ── AND THE LOOP IS WOKEN, WHICH IS THE WHOLE POINT ON THIS PATH ──────
	//
	// Closing the socket is enough when the loop is CONNECTED, because
	// `waitUntilDown` notices. It is not enough when the loop is between dials,
	// which is exactly where a session with a rejected credential spends its
	// time — and with the auth backoff that sleep reaches five minutes. Without
	// this nudge, correcting a password would appear to do nothing for up to
	// five minutes, on the one screen whose entire purpose is fixing it.
	s.nudge()
	log.Printf("[session] %s: endpoint or credentials changed, reconnecting", s.Label)
	s.announce()
	return true
}

// CloseNow tears a session down AT ONCE, whoever is still holding it.
//
// ── WHY Release CANNOT SERVE BOTH CALLERS ───────────────────────────────────
//
// `Release` answers "one viewer left". The idle grace is right for that: a
// refresh is a viewer leaving and coming back a second later.
//
// Disabling or deleting a router is a different question with a different
// answer. It is an administrative fact, not an absence, and the router must stop
// being polled immediately -- `routers_api.go`'s own comment says so: "A
// disabled router must stop being polled at once rather than at the next idle
// sweep." Those two callers used Release when Release WAS a teardown, and the
// grace silently turned them into a two-minute delay against a router the
// operator had just switched off.
//
// Refs are zeroed rather than decremented, because the question is not how many
// viewers remain. A browser still in the room is told separately
// (`router:disabled`), and its own `releaseRouter` later finds the session gone
// and returns harmlessly.
//
// The teardown itself is `idleOut`, not a copy of it: there is exactly one place
// that flushes history, stops all fourteen collectors, closes the client and
// hands the router back to the pool, and `TestBothTeardownPaths*` reads that one
// place.
func (m *Manager) CloseNow(routerID string) {
	m.mu.Lock()
	s, ok := m.live[routerID]
	if !ok {
		m.mu.Unlock()
		return
	}
	s.mu.Lock()
	if s.linger != nil {
		s.linger.Stop()
		s.linger = nil
	}
	s.refs = 0
	s.mu.Unlock()
	m.mu.Unlock()

	m.idleOut(routerID, s)
}

// grace is the idle window. Zero means the default, so a Manager built by any
// caller that does not care gets the real behaviour.
func (m *Manager) grace() time.Duration {
	if m.idleGrace > 0 {
		return m.idleGrace
	}
	return DefaultIdleGrace
}

// SetIdleGrace overrides the window. For tests, which cannot wait two minutes.
func (m *Manager) SetIdleGrace(d time.Duration) { m.idleGrace = d }

// SetOnIdle registers what to do once a session has actually gone. The server
// points it at `syncFleetHolds`, so the pool reclaims a router at the moment the
// session stops covering it and not a moment before.
func (m *Manager) SetOnIdle(fn func(routerID string)) { m.onIdle = fn }

// idleOut is the deferred half of Release: the teardown, once the grace has
// passed with nobody coming back.
func (m *Manager) idleOut(routerID string, s *Session) {
	m.mu.Lock()
	// THREE WAYS THIS IS ALREADY MOOT, all of them ordinary. The session may
	// have been shut down, or replaced by a later Acquire that built a fresh one
	// under the same id; and the timer may simply have lost the race with an
	// Acquire that took a reference. Identity is compared, not just presence,
	// because a replacement is a different Session that must not be torn down by
	// its predecessor's timer.
	if cur, ok := m.live[routerID]; !ok || cur != s {
		m.mu.Unlock()
		return
	}
	s.mu.Lock()
	// A VIEWER CAME BACK, or something is holding this session for a reason that
	// is not a viewer at all.
	//
	// The second half is phase 4.3: alerting and history recording keep a router
	// alive with nobody looking at it, which is the job the background pools were
	// built to do. Checked HERE rather than at Release, because Release is the
	// viewer's departure and a hold has nothing to say about that -- the grace
	// should still run, and simply find at the end of it that the session is
	// spoken for.
	if s.spokenForLocked() {
		s.linger = nil
		viewer := s.refs > 0
		s.mu.Unlock()
		m.mu.Unlock()
		// ── THE IDLE GATE, AT THE END OF THE GRACE ──────────────────────
		//
		// Two minutes have passed with nobody watching and something still
		// wants this session -- alerting, or history recording. It survives,
		// and NOW it drops to what those actually need: six collectors instead
		// of fifteen, on a connection that stays up because alerts cannot be
		// evaluated without one.
		//
		// A viewer who came back inside the grace is the other case, and it
		// takes nothing away: `refs > 0` means the browser is here and the full
		// set is right.
		if !viewer {
			s.applyDemand()
		}
		return
	}
	s.closed = true
	s.linger = nil
	delete(m.live, routerID)
	c := s.client
	s.mu.Unlock()
	m.mu.Unlock()

	// The connect loop reads `closed` when it wakes, so a session torn down
	// mid-sleep leaves its goroutine parked for the rest of the interval. That
	// was five seconds and is now up to five minutes.
	s.nudge()

	// ── THE LAST MINUTE, BEFORE THE COLLECTORS STOP ───────────────────────
	//
	// A bucket only rolls over when the NEXT minute's first sample arrives, so a
	// session that ends mid-minute leaves that minute unwritten. Flushing here
	// is why `Wire.Flush` exists, and it must come BEFORE the Stop calls below:
	// afterwards no further sample can arrive to roll it over, and the minute is
	// simply lost. Inert when the wire is nil or disabled.
	m.history.Flush(routerID)

	// ── EVERY COLLECTOR THE CONNECT BLOCK STARTED, NOT THE FIRST FIVE ─────
	//
	// This stopped five of the fourteen for most of the port's life, and the
	// nine left running did NOT stop on their own. `pollLoop` is a
	// self-rescheduling `time.Timer`, not a goroutine parked on a channel: it
	// arms the next tick at the end of the current one and never consults the
	// session. So a released session kept nine timers alive FOREVER, each
	// waking every 1–60 seconds to read through a client that had just been
	// closed, for every router that had ever been acquired.
	//
	// It was invisible for the same reason it was survivable — the reads fail
	// quietly and nobody is listening to the payloads — so the cost was a
	// growing pile of timers and a log nobody reads, rather than a symptom.
	//
	// Found while wiring the backup scheduler, which is the first caller that
	// acquires and releases routers NOBODY IS WATCHING, on a timer, forever.
	// It would have been the first thing to make this unbounded.
	//
	// Stopping them is unambiguously safe: `last` is true here, so `s.closed`
	// is set and the session is out of `m.live`. Nothing can resume it, and the
	// next Acquire builds a new one.
	//
	// `TestReleaseStopsEveryCollectorTheConnectBlockStarted` counts both sides
	// out of this file and fails if they ever diverge again.
	s.dns.Stop()
	s.areas.Stop()
	s.bridges.Stop()
	s.vlans.Stop()
	s.wan.Stop()
	s.ifStatus.Stop()
	s.firewall.Stop()
	// vpn is started at connect for ALERTING (4.3b), so it must be stopped here
	// like any other connect-started collector. `TestBothTeardownPathsStopEvery
	// Collector` caught its absence the moment the start was added, which is the
	// whole reason that gate reads the connect block rather than a list.
	s.vpn.Stop()
	s.system.Stop()
	s.logs.Stop()
	s.traffic.Stop()
	s.dhcpNetworks.Stop()
	s.dhcpLeases.Stop()
	s.arp.Stop()
	s.netwatch.Stop()
	s.talkers.Stop()
	s.ping.Stop()
	// BEFORE THE CLOSE. Stop waits for the loop to finish, so no scheduled
	// fetch is still in flight against a connection about to shut.
	if s.sched != nil {
		s.sched.Stop()
	}
	if c != nil {
		_ = c.Close()
	}
	log.Printf("[session] %s released; connection closed", s.Label)

	// LAST, and outside every lock. The router is uncovered from this instant,
	// so this is the moment the pool has to hear about it -- not Release, which
	// is up to a grace period earlier and finds the session still in `Live()`.
	if m.onIdle != nil {
		m.onIdle(routerID)
	}
}

// Shutdown closes every live connection.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	all := make([]*Session, 0, len(m.live))
	for _, s := range m.live {
		all = append(all, s)
	}
	m.live = map[string]*Session{}
	m.mu.Unlock()
	for _, s := range all {
		s.mu.Lock()
		s.closed = true
		// A session lingering out its idle grace still has a timer armed. It
		// would find itself gone from `m.live` and return harmlessly, but
		// stopping it here keeps SIGTERM from leaving a timer holding a whole
		// Session alive until it fires.
		if s.linger != nil {
			s.linger.Stop()
			s.linger = nil
		}
		c := s.client
		s.mu.Unlock()

		// ── THE SAME TWO THINGS Release DOES, AND FOR THE SAME REASONS ────
		//
		// This stopped FIVE of the fourteen the connect block starts, and never
		// flushed. `Release` was fixed on 2026-08-29 and this was not: a test
		// naming one function cannot see its sibling, and the sibling is the path
		// every SIGTERM takes.
		//
		// The flush comes FIRST, before the collectors stop: afterwards no
		// further sample can arrive to roll the open bucket over, so the minute
		// in progress is simply lost — for every router, on every restart,
		// rendering as a quiet minute rather than an error.
		//
		// `TestBothTeardownPathsStopEveryCollector` and
		// `TestBothTeardownPathsFlushHistory` now check both paths.
		m.history.Flush(s.RouterID)

		s.dns.Stop()
		s.areas.Stop()
		s.bridges.Stop()
		s.vlans.Stop()
		s.wan.Stop()
		s.ifStatus.Stop()
		s.firewall.Stop()
		s.vpn.Stop()
		s.system.Stop()
		s.logs.Stop()
		s.traffic.Stop()
		s.dhcpNetworks.Stop()
		s.dhcpLeases.Stop()
		s.arp.Stop()
		s.netwatch.Stop()
		s.talkers.Stop()
		s.ping.Stop()
		if c != nil {
			_ = c.Close()
		}
	}
}

type unknownRouter struct{}

func (unknownRouter) Error() string { return "session: no such router" }

var errUnknownRouter = unknownRouter{}

// connectLoop keeps the connection up for as long as anybody is watching.
//
// The backoff is flat rather than exponential on purpose: a router that is
// rebooting is back in well under a minute, and an exponential backoff that has
// climbed to minutes turns a 30-second reboot into a page that stays blank long
// after the device is answering.
func (s *Session) connectLoop() {
	const retry = 5 * time.Second
	// ── EXCEPT WHEN THE ROUTER IS REJECTING THE CREDENTIAL ────────────────
	//
	// The flat interval above is right for a connection that fails to arrive,
	// for the reason this comment already gives. It is wrong when the router
	// answers and says no: a wrong password does not come right on its own, and
	// every attempt writes a failed login into the router's own log. See
	// routeros.AuthBackoff.
	//
	// Owned by this goroutine alone, which is what lets it be lock-free.
	var authBackoff routeros.AuthBackoff
	// ── CONNECTIVITY IS REPORTED ON TRANSITIONS, AND THAT IS A DIVERGENCE ──
	//
	// The connectivity state machine had NO production caller anywhere in this
	// port. `connectivity_events` therefore stopped being written at the cutover
	// while ping and traffic kept going, and the Reports page — reading a table
	// frozen mid-outage — showed every router Down with ~2% uptime. The machine
	// and its corpus were ported; only the two calls that drive them were
	// missing. `internal/history/connectivity.go` says so in its own header:
	// "NOTHING CONSTRUCTS THIS YET."
	//
	// THE THRESHOLD IS NO LONGER ZERO, and that paragraph used to say it was:
	// rule 4 makes a zero threshold its own branch which writes on every close
	// and needs no `Tick`, and at the cutover there was no ticker to drive one.
	// There is now — `startConnTicker`, one second for the whole fleet — and the
	// router's own `connDownThresholdSec` reaches the machine through
	// `connstate.Tracker.SetThreshold`. What this loop reports is unchanged: an
	// event, debounced elsewhere.
	//
	// But "every close" is what a dial loop produces most of: a router down for a
	// day would write thousands of identical rows. The live app did exactly that
	// — the frozen tail in this install's database is 248 consecutive `connected
	// =0` rows, 30 seconds apart, from the Node app's own shutdown loop. So this
	// reports a TRANSITION rather than an event, giving one row per outage and
	// one per recovery. Rule 1 already does this on the way up; `reportedDown` is
	// the same idea on the way down.
	reportedDown := false
	first := true
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return
		}
		// COPIED UNDER THE LOCK, and re-read on EVERY pass rather than captured
		// once outside the loop. That is what makes `Reconfigure` work: it swaps
		// `cfg` and drops the socket, and this next turn of the loop dials the
		// new endpoint. Hoisting this out would silently restore the bug.
		cfg := s.cfg
		s.mu.Unlock()

		c, err := routeros.Dial(cfg)
		if err != nil {
			s.mu.Lock()
			s.connected = false
			s.observed = true
			// SANITISED AT THE POINT OF STORAGE, not at the point of sending.
			// `lastErr` reaches a browser through `announce` as `router:status`'s
			// reason, and the shell renders that as the banner text — so storing
			// the raw message would mean every future reader of this field has to
			// remember to redact it. The live app stores the safe form too.
			s.lastErr = safe.Message(err.Error())
			s.mu.Unlock()
			s.announce()
			if !reportedDown {
				reportedDown = true
				s.conn.Disconnected(s.RouterID, time.Now().UnixMilli())
			}
			wait := authBackoff.Delay(err, retry)
			// WRITTEN WITHOUT AN else BRANCH, deliberately.
			// TestEverythingSuspendedOnDisconnectIsResumedOnReconnect locates the
			// reconnect block by finding the FIRST closing-brace-else-brace in
			// this file, so an earlier one re-aims it at the wrong block. It
			// failed closed and said "the anchors have moved", which is the check
			// working.
			//
			// And the anchor cannot be spelled out here either: `strings.Index`
			// does not care that it is inside a comment. Writing it out to
			// explain the rule broke the rule, which is the same trap
			// `isTestSource` exists for one level up.
			rejected := ""
			if n := authBackoff.Failures(); n > 1 {
				rejected = " (rejected " + strconv.Itoa(n) + " times)"
			}
			log.Printf("[session] %s: %v%s; retrying in %s", s.Label, err, rejected, wait)
			if !s.sleepOrWake(wait) {
				return
			}
			continue
		}

		s.mu.Lock()
		if s.closed { // released while dialling
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.client = c
		s.connected = true
		s.lastErr = ""
		s.observed = true
		s.mu.Unlock()
		// THE RUN IS OVER. Without this a router that was rejecting logins keeps
		// its climb, so the next unrelated drop waits out a five-minute sleep.
		authBackoff.Reset()
		reportedDown = false
		// The status this returns is discarded: `announce` below already sends
		// `router:status` on every connect, including the reconnects that write
		// no row, and a second emit would double every badge update.
		s.conn.Connected(s.RouterID, time.Now().UnixMilli())
		log.Printf("[session] %s connected", s.Label)
		s.announce()

		if first {
			// THE DORMANCY SUPERVISOR runs alongside the collectors it judges.
			// Wired here rather than in Acquire because it must not judge before
			// anything has reported: its first verdict would be of a fleet of
			// empty payloads.
			//
			// IT IS NO LONGER A GOROUTINE. It rides the scheduler's deliveries
			// instead of a fifteen-second ticker, which is phase 3.3 -- see
			// judgeOnDelivery for what that changed and what it deliberately did
			// not.
			//
			// OUTSIDE the counted block below on purpose — it is not a collector
			// and `TestTheBackgroundCollectorCountIsRecorded` counts that block
			// with a regex.
			s.judgeOnDelivery()

			// ── PHASE 5.2: PRIME, SO NO PAGE WAITS ON ITS FIRST LANDING ──
			//
			// In a goroutine because it SLEEPS between reads -- see
			// primeSpacing -- and this loop must go on to start the collectors.
			// It skips anything that already has a payload, so it costs one pass
			// and races nothing: a collector that produces first simply is not
			// primed.
			//
			// The operator asked for this at app startup AND at router select
			// (2026-09-08). This is the router-select half, and it is also the
			// startup half for any router the background pools hold, because
			// both build their sessions through the same connect path.
			go s.primeAll()

			// ── PHASE 4.3c: PRUNE TO WHAT THE HOLDERS NEED ──────────────
			//
			// The block below starts everything, which is right for a viewer
			// and wrong for a session held only for alerting: fifteen
			// collectors where six are read, and `wan` alone polls every two
			// seconds. This suspends the rest, and does nothing at all when a
			// viewer is present. See applyReasons.
			//
			// AFTER the starts rather than instead of them, so the start list
			// stays the one statement of what a session runs and this is
			// visibly a narrowing of it.
			//
			// ── NOT `defer`, AND THAT COST A DEPLOY TO FIND ─────────────
			//
			// This was written as `defer s.applyDemand()`. A defer runs when
			// the FUNCTION returns, and the function here is `connectLoop` --
			// which is a loop that never returns for the life of the session. So
			// it never ran, and a router held only for alerting kept all fifteen
			// collectors.
			//
			// Every test stayed green: they assert that the call exists, which it
			// did. The command rate is what showed it -- 264-287 a minute against
			// a 119-120 baseline. Placed at the END of the start block, where the
			// deferred version was meant to take effect.

			// #105: EVERY START IS GATED on the router's resolved config, so a
			// collector the operator turned off for this router is never
			// started. The calls keep their literal shape because
			// `TestTheBackgroundCollectorCountIsRecorded` counts them with a
			// regex — the count is the basis of the background-pool decision and
			// must keep being measurable. (Writing that pattern out in this
			// comment made the test count FIFTEEN collectors, one of them named
			// after the placeholder. It is doing its job.)
			//
			// NO NULL-COLLECTOR STUB IS NEEDED, unlike the live side, where 11
			// of 16 open their streams from the constructor and skipping start()
			// is not enough. The Go collectors are inert until Start().
			if s.conf().Enabled["dns"] {
				s.dns.Start()
			}
			if s.conf().Enabled["areas"] {
				s.areas.Start()
			}
			if s.conf().Enabled["bridges"] {
				s.bridges.Start()
			}
			if s.conf().Enabled["vlans"] {
				s.vlans.Start()
			}
			if s.conf().Enabled["wan"] {
				s.wan.Start()
			}
			if s.conf().Enabled["ifStatus"] {
				s.ifStatus.Start()
			}
			// A ONE-SHOT read, not a poll — see Firewall.Start. It is here
			// rather than on page focus because the Queues page's FastTrack
			// banner reads this collector's last payload, and a queues viewer
			// who never opens Firewall would otherwise be told "cannot say".
			if s.conf().Enabled["firewall"] {
				s.firewall.Start()
			}
			// ── PHASE 4.3b: THE ALERT FEED, STARTED BECAUSE ALERTING IS ON ──
			//
			// `vpn` and `routing` are page-gated: nothing starts them at connect,
			// and four of the six alert rules read their payloads. They have been
			// running anyway, by way of `primeAll` and the dormancy probe, which
			// is an accident rather than an intention -- see AlertFeeds.
			//
			// Started here so the reason is the router's alert switch. Gated on
			// it too, so a router with alerting OFF pays nothing: this is the
			// same work the alert pool does for such a router today, moved to the
			// session that was already doing the other four.
			if s.alertsEnabled.Load() {
				if s.conf().Enabled["vpn"] {
					s.vpn.Start()
				}
				if s.conf().Enabled["routing"] {
					// Resume, not Start:  has no Start -- it is a
					// page-gated collector whose lifecycle begins at focus.
					// Resuming it here is what gives alerting its own reason.
					s.routing.Resume()
				}
			}
			// Starts the gauge poll AND the one update check that runs at
			// startup. The check is the only call in the app that leaves the
			// router, so it is rate limited to twelve hours inside the
			// collector rather than being scheduled from here.
			if s.conf().Enabled["system"] {
				s.system.Start()
			}
			if s.conf().Enabled["logs"] {
				s.logs.Start()
			}
			// Started with the connection, not on page focus: the WAN badge is
			// chrome on every page, and the history a chart needs has to be
			// accumulating before somebody opens one.
			if s.conf().Enabled["traffic"] {
				s.traffic.Start()
			}
			// CROSS-PAGE DEPENDENCIES, started with the connection rather than
			// on page focus. The DHCP networks are what tell connections and
			// bandwidth which addresses are LOCAL, and the leases are what name
			// a device on four different pages. Gating them on the DHCP page
			// meant a viewer who never opened it got a Connections page with no
			// sources at all — every address looked external because nothing
			// had said what "internal" was.
			// ── LEASES FIRST, AND THE ORDER IS THE BEHAVIOUR ──────────
			//
			// `dhcpNetworks.Tick` counts each subnet's used addresses by asking
			// `dhcpLeases.UsedLeaseIPs()`. Started the other way round -- which
			// is how this stood until 2026-09-01 -- that call returns nil,
			// because the leases collector has not read anything yet. Nothing
			// errors: every subnet simply gets a lease count of ZERO.
			//
			// AND IT STICKS FOR TEN MINUTES, because both poll every 600s and
			// `Last()` is what a page focus replays. The DHCP page then showed
			// subnet rows and a full lease table with every count reading 0%,
			// which is exactly how the operator described it.
			//
			// The RECONNECT path below already had this right (leases, then
			// networks), which is why a dropped connection "fixed" the page and
			// was the clue that found this: an orange disconnected banner, and
			// correct percentages straight after.
			//
			// The registry cannot express it -- `requires: []` for both, and
			// `requires` gates ENABLEMENT rather than start order -- so the
			// ordering lives here, with a test that reads it.
			if s.conf().Enabled["dhcpLeases"] {
				s.dhcpLeases.Start()
			}
			// ── STARTED WITH THE LEASES, AND FOR THE SAME REASON ────────
			//
			// Four collectors read its index and `conns` asks on its first tick,
			// so it starts here rather than later.
			//
			// GATED LIKE EVERYTHING ELSE, even though `arp` is
			// `disableable: false` in the registry. The gate is uniform on
			// purpose: `system` is non-disableable too and carries the same
			// guard, `CollectorEnabled` resolves an absent key to true, and a
			// collector exempted here would be the one nobody notices when the
			// registry changes its mind.
			if s.conf().Enabled["arp"] {
				s.arp.Start()
			}
			if s.conf().Enabled["dhcpNetworks"] {
				s.dhcpNetworks.Start()
			}
			// THE TWO DASHBOARD-ONLY COLLECTORS. Neither has a page, so neither
			// is reachable from `page:focus` — which meant netwatch was
			// constructed, given an accessor nobody called, and never started at
			// all. It polled only if the connection dropped and came back, so on
			// a healthy router its card stayed empty indefinitely.
			//
			// Found by asking which constructed collectors have no reachable
			// Start or Resume; `internal/session/lifecycle_test.go` now asks that
			// on every run.
			if s.conf().Enabled["netwatch"] {
				s.netwatch.Start()
			}
			if s.conf().Enabled["talkers"] {
				s.talkers.Start()
			}
			if s.conf().Enabled["ping"] {
				s.ping.Start()
			}
			// ── AND THE RESUMES THAT ARRIVED TOO EARLY ──────────────────
			//
			// The page-gated collectors are deliberately absent from the block
			// above: they start when somebody opens their page or dashboard
			// card, not on connect. But the browser asks for that BEFORE this
			// point. `dashcard:focus` is sent as the grid lays out, and
			// `selectRouter` calls `rejoinCards()` immediately after `Acquire`,
			// which returns as soon as the session exists rather than when it
			// has dialled. Every collector's `Resume()` begins `if
			// ros.Connected()`, so those requests were silently dropped and
			// nothing ever asked again.
			//
			// MEASURED 2026-08-31, after the operator reported the Connections
			// card stale with no data after a container rebuild: on a fresh
			// session the card showed "— total" indefinitely, and visiting the
			// Connections page (a resume that arrives when the link IS up) filled
			// it in at once. Connection Flow, Top Countries, Top Ports, Routes by
			// Protocol and BGP Peers were empty for the same reason.
			//
			// The reconnect branch below never had this, which is why the card
			// "eventually recovered": any drop and return restores everything.
			//
			// The live app has no first/reconnect split to get wrong -- its
			// `ros.on('connected')` always ends with `_updateAllPageStreams`,
			// "restore page-aware streams for any pages still open"
			// (src/index.js:685). This is that, for the connect it was missing on.
			s.replayResumes()

			// ── PHASE 4.3c: PRUNE TO WHAT THE HOLDERS NEED ──────────────
			//
			// The block above starts everything, which is right for a viewer and
			// wrong for a session held only for alerting: fifteen collectors
			// where six are read, and `wan` alone polls every two seconds. This
			// suspends the rest, and does nothing at all when a viewer is
			// present. See applyReasons.
			//
			// AFTER the starts and after replayResumes, so it is the last word on
			// what runs -- and NOT `defer`. It was written as one, and a defer
			// runs when the FUNCTION returns: the function is `connectLoop`,
			// which never returns for the life of the session, so it never ran.
			// Every test stayed green, because they assert the call exists and it
			// did. The command rate is what showed it: 264-287 a minute against a
			// 119-120 baseline.
			s.applyDemand()
			first = false
		} else {
			// THE CACHED ROWS CAME FROM A CONNECTION THAT IS GONE. Their age says
			// nothing about the new one, and a collector served from them would
			// render pre-reconnect state as current.
			if s.roscache != nil {
				s.roscache.Reset()
			}

			// #105: GATED LIKE THE STARTS, and for a sharper reason.
			// `Reconnected()` is not a latch-clearing no-op — every collector's
			// version ends `Tick(); loop.start()`, so an ungated one RESTARTS a
			// collector the operator turned off. Reconnects are routine (the
			// usual cause is a router upgrade), so that is not an edge case.
			//
			// The live side gets this for free: a disabled collector there is a
			// null stub, and calling anything on it does nothing.
			// Not Start(): a reconnect must drop every "this menu is absent"
			// latch, because the usual reason a connection dropped is an
			// upgrade, and the router that came back may not be the same build.
			//
			// DORMANCY IS RESET FIRST, for exactly that reason — the live
			// comment: "A reconnect may follow a RouterOS upgrade or a package
			// install, which is exactly the event that turns an 'unknown
			// command' into a working menu." Before the Reconnected() calls, so
			// a collector the supervisor had put to sleep is awake by the time
			// its latch is dropped.
			if s.dormancy != nil {
				s.applyDormancy(s.dormancy.Reset(), s.targets())
			}
			if s.conf().Enabled["dns"] {
				s.dns.Reconnected()
			}
			if s.conf().Enabled["areas"] {
				s.areas.Reconnected()
			}
			if s.conf().Enabled["bridges"] {
				s.bridges.Reconnected()
			}
			if s.conf().Enabled["vlans"] {
				s.vlans.Reconnected()
			}
			if s.conf().Enabled["ifStatus"] {
				s.ifStatus.Reconnected()
			}
			if s.conf().Enabled["wan"] {
				s.wan.Reconnected()
			}
			if s.conf().Enabled["dhcpLeases"] {
				s.dhcpLeases.Reconnected()
			}
			if s.conf().Enabled["arp"] {
				s.arp.Reconnected()
			}
			if s.conf().Enabled["dhcpNetworks"] {
				s.dhcpNetworks.Reconnected()
			}
			// ── PACKAGES AND ROUTING WERE THE TWO THAT WERE MISSING ─────
			//
			// Both are SUSPENDED on the disconnect path below, and until
			// 2026-08-29 neither was resumed here — so after any reconnect they
			// stayed off until somebody focused their page. A viewer already
			// sitting on Routing when the router came back watched a page that
			// never updated again, and had to navigate away and back.
			//
			// Reconnects are not rare and they are not random: the usual cause
			// is a RouterOS upgrade, which is exactly the moment an operator IS
			// watching. The live app restores these — `ros.on('connected')` ends
			// with `_updateAllPageStreams(session, entry)`, "restore page-aware
			// streams for any pages still open after the reconnect"
			// (`src/index.js:685`).
			//
			// Resuming them unconditionally is what the other ten page-gated
			// collectors here already do (ppp, vpn, rosusers, queues, wifi,
			// capsman, topology, wireless, bandwidth, conns). These two were the
			// only page-gated collectors left out, which is what made it an
			// oversight rather than a policy — and the dormancy supervisor is
			// what puts an unwatched collector back to sleep.
			if s.conf().Enabled["packages"] {
				s.packages.Reconnected()
			}
			// RESUME, NOT Reconnected — `Routing` has no `Reconnected` and does
			// not need one. `Reconnected` exists on the collectors that hold an
			// "this menu is absent" latch, which a reboot can invalidate;
			// Routing holds no such verdict (see its struct — no `*OK` fields),
			// only caches its ticks refresh. Resume starts the loop if the
			// client is up, which it is by this point.
			if s.conf().Enabled["routing"] {
				s.routing.Resume()
			}
			if s.conf().Enabled["ppp"] {
				s.ppp.Reconnected()
			}
			if s.conf().Enabled["vpn"] {
				s.vpn.Reconnected()
			}
			if s.conf().Enabled["netwatch"] {
				s.netwatch.Reconnected()
			}
			if s.conf().Enabled["talkers"] {
				s.talkers.Reconnected()
			}
			if s.conf().Enabled["ping"] {
				s.ping.Reconnected()
			}
			if s.conf().Enabled["rosusers"] {
				s.rosUsers.Reconnected()
			}
			if s.conf().Enabled["queues"] {
				s.queues.Reconnected()
			}
			if s.conf().Enabled["firewall"] {
				s.firewall.Reconnected()
			}
			if s.conf().Enabled["wifi"] {
				s.wifi.Reconnected()
			}
			if s.conf().Enabled["capsman"] {
				s.capsman.Reconnected()
			}
			if s.conf().Enabled["system"] {
				s.system.Reconnected()
			}
			// Drops the buffer and reloads: the router that came back may have
			// rebooted, in which case the lines held here describe a different
			// uptime.
			if s.conf().Enabled["logs"] {
				s.logs.Reconnected()
			}
			if s.conf().Enabled["topology"] {
				s.topology.Reconnected()
			}
			if s.conf().Enabled["wireless"] {
				s.wireless.Reconnected()
			}
			if s.conf().Enabled["bandwidth"] {
				s.bandwidth.Reconnected()
			}
			if s.conf().Enabled["traffic"] {
				s.traffic.Reconnected()
			}
			if s.conf().Enabled["conns"] {
				s.conns.Reconnected()
			}
			// ── AND PRUNE, EXACTLY AS THE FIRST CONNECT DOES ────────────
			//
			// The reconnect branch restarts every collector, so a session held
			// only for alerting comes back running all fifteen. This was in the
			// `if first` branch alone, and the consequence was measured rather
			// than reasoned: the router dropped for five seconds at 02:01 and the
			// command rate went from 129 a minute to ~300, where it stayed.
			//
			// Reconnects are not rare -- a RouterOS upgrade causes one, and so
			// does any brief network blip -- so a prune that only runs on the
			// FIRST connect is a prune that stops working the first time anything
			// goes wrong. `TestBothConnectPathsPrune` is the guard, in the shape
			// `TestBothTeardownPathsStopEveryCollector` already uses for the
			// mirror-image mistake.
			s.applyDemand()
		}

		s.waitUntilDown(c)

		s.mu.Lock()
		down := !s.closed
		s.client = nil
		s.connected = false
		s.mu.Unlock()

		// EVERY CHANNEL WENT WITH THE SOCKET. They are tags on one TCP
		// connection, so the router-side channels are gone whether or not a
		// collector called its stop -- and at least one does not, measured
		// 2026-09-09: the open-channel level grew by one per outage on a router
		// that was flapping, so it would have climbed indefinitely. See
		// roslimit.StreamsGone.
		roslimit.StreamsGone(s.RouterID)

		// A DROP IS A TRANSITION whether or not anybody is watching. Recorded
		// here rather than only on the failed redial, so an outage's start is the
		// moment the link went rather than five seconds later.
		if down && !reportedDown {
			reportedDown = true
			s.conn.Disconnected(s.RouterID, time.Now().UnixMilli())
		}

		// ── CLOSE THE CLIENT WE ARE ABANDONING ────────────────────────────
		//
		// Dropping the pointer is not closing the socket, and for most of this
		// port's life that is all this loop did. `Connected()` reports
		// `!closed && fatal == nil`, and in go-routeros v3.0.1 a failing
		// `asyncLoop` calls `closeTags` and NEVER touches the socket. So a
		// protocol error, a parse error or a read error sets `fatal`, this loop
		// redials -- and the previous connection is still established, still
		// logged in, and now unreachable: an fd, two Async goroutines, and a
		// `/user/active` entry the router has no reason to reap.
		//
		// UNCONDITIONAL, AND ON BOTH PATHS, because of a race with `idleOut`:
		// it captures `c := s.client` under the lock, so if the line above has
		// already nil'd it, idleOut closes nothing and the client is orphaned
		// even on a clean teardown. Whoever gets there first wins; `Close` is
		// idempotent (internal/routeros/client.go:386 guards on `c.closed`), so
		// closing twice is a no-op rather than an error.
		//
		// Safe here because `waitUntilDown` returns only when the connection is
		// already down or the session is closing. Any collector still mid-command
		// fails the same way it would have anyway, and every Suspend/Stop below
		// follows immediately.
		// WHY THE LINK WENT, for the log line at the bottom of this block.
		// `Err()` reports the transport failure and nothing else, so reading it
		// on either side of the Close below gives the same answer.
		reason := c.Err()
		_ = c.Close()
		s.dns.Suspend()
		s.areas.Suspend()
		s.bridges.Suspend()
		s.vlans.Suspend()
		s.wan.Suspend()
		s.packages.Suspend()
		s.routing.Suspend()
		s.ifStatus.Suspend()
		s.dhcpLeases.Suspend()
		s.dhcpNetworks.Suspend()
		s.ppp.Suspend()
		s.vpn.Suspend()
		s.rosUsers.Suspend()
		s.queues.Suspend()
		s.firewall.Suspend()
		s.wifi.Suspend()
		s.capsman.Suspend()
		s.system.Suspend()
		s.logs.Stop()
		s.topology.Suspend()
		s.wireless.Suspend()
		s.bandwidth.Suspend()
		s.traffic.Stop()
		s.conns.Suspend()
		if !down {
			return
		}
		s.announce()
		// ── THE REASON IS THE POINT OF THIS LINE ──────────────────────────
		//
		// It read "disconnected; retrying in 5s" and nothing else. Three drops
		// across two routers in one afternoon could not be told apart by it: a
		// router closing the session, a reset in the path between, and a
		// protocol error all produce the same sentence, and the router's own log
		// says only that the API user logged out. The operator asked what caused
		// one and the honest answer was that this app had not written it down —
		// it HAD the error, in `Client.Err()`, and threw it away here.
		//
		// A nil reason is not a gap in the record: it means nothing marked the
		// connection failed, so the close came from this side.
		if reason != nil {
			log.Printf("[session] %s disconnected: %v; retrying in %s", s.Label, reason, retry)
		} else {
			log.Printf("[session] %s disconnected (closed locally, no transport error); retrying in %s",
				s.Label, retry)
		}
		time.Sleep(retry)
	}
}

// sleepOrWake waits out a retry interval, and reports whether the loop should
// carry on. False means the session is closed and the goroutine must leave.
//
// A PLAIN `time.Sleep` WAS ENOUGH AT FIVE SECONDS AND IS NOT AT FIVE MINUTES.
// It made teardown wait out the remainder — harmless at 5s, a goroutine parked
// on a dead session for minutes once the auth backoff climbed — and it made a
// corrected password wait for the same. Both wake this.
func (s *Session) sleepOrWake(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.wake:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed
}

// nudge wakes the connect loop if it is sleeping, and never blocks.
//
// The buffered channel is what makes a signal raised while the loop is BUSY
// still count: it is taken on the next sleep rather than dropped. A second
// nudge before the first is consumed is redundant and correctly discarded.
func (s *Session) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// waitUntilDown polls the connection's own liveness rather than subscribing to
// a close event, because the client reports closure through Connected() and a
// second notification path would be a second thing to keep correct.
func (s *Session) waitUntilDown(c *routeros.Client) {
	for c.Connected() {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(time.Second)
	}
}

// Collection is this router's resolved collection config, for the socket layer
// to send the browser as `collection:config`.
//
// Returned by value. `Resolved` holds three maps, and handing out the session's
// own would let a caller mutate what every collector's interval is read from.
// The maps inside are shared — this is a shallow copy — which is why the socket
// layer only reads them. A deep copy per socket select would be three
// allocations to defend against a caller that does not exist; if one ever does,
// copy there.
func (s *Session) Collection() collection.Resolved { return *s.conf() }

// conf is this router's resolved collection config. See the `eff` field; a
// Session built without one (a test) reads as every collector at its defaults.
func (s *Session) conf() *collection.Resolved {
	if p := s.eff.Load(); p != nil {
		return p
	}
	return &collection.Resolved{}
}

// announce pushes the connection state to everybody watching this router, so a
// reboot shows up as a status chip rather than as a table that quietly stops
// changing.
func (s *Session) announce() {
	frame := StatusFrame(s.conn, s.RouterID, s.Connected(), s.LastError())
	EvRouterStatus.Broadcast(s.h, "router-"+s.RouterID, frame)
	// ── AND EVERY OTHER BROWSER THAT MAY READ THIS ROUTER ────────────────
	//
	// The Settings and Devices tables show every router a principal may read, not
	// only the one whose room a browser happens to be in, so a non-active router
	// going down has to reach them too. `alertPoolStatus` sent both frames for the
	// routers the alert pool held; when that pool was deleted, this became the only
	// sender.
	//
	// IT WAS `BroadcastAll`, which sent every router's state and last error to
	// every signed-in browser, including a viewer whose role grants none of those
	// routers. The hub knows nothing about principals, so the audience is the
	// server's to decide: `fleet` is `Server.sendFleetStatus`, a send per socket
	// whose `visibleRouters` holds this id, as `broadcastRouterList` does for the
	// list. Unset, nothing goes fleet-wide; the room above still reaches this
	// router's own viewers.
	//
	// A browser in this router's room receives both. `main.ts` records the state
	// into `routerStatus` by id, so the repeat is a second write of the same value.
	if s.fleet != nil {
		if fn := s.fleet.Load(); fn != nil {
			(*fn)(frame)
		}
	}
}

// StatusFrame builds a `router:status` payload.
//
// ── ONE BUILDER, BECAUSE THE FRAME CARRIES TWO FACTS NOW ──────────────────
//
// `connected` is the API socket, this instant. It dims the page you are on,
// raises the "RouterOS not connected" banner and decides whether a write can be
// attempted, and all three must react at once: a UI that claims a connection it
// does not have is worse than one that says so early.
//
// `online` is the DEBOUNCED verdict — the device dialog's Offline threshold,
// thirty seconds by default. It is what the FLEET'S badges read, so a router
// that blinks for six seconds does not paint the Devices page red.
//
// Four places send this event: the session's own announce, and three in
// `internal/server/ws.go` — a select that failed to acquire, a select that
// succeeded, and the statuses sent on connect. A frame built by hand in any of
// them would omit `online`, which the browser reads as false: every router
// would go Offline the moment a viewer selected one. Hence a function rather
// than a convention.
//
// Before a router has been observed at all there is no verdict, and "never
// seen" is not "down": `online` falls back to the live value rather than
// reporting a fresh install as a fleet of offline devices.
func StatusFrame(t *connstate.Tracker, routerID string, connected bool, reason string) map[string]any {
	online, known := t.Online(routerID)
	if !known {
		online = connected
	}
	return map[string]any{
		"routerId":  routerID,
		"connected": connected,
		"online":    online,
		"reason":    reason,
	}
}

// InWriteQueue serialises writes to one router.
//
// The Node side does this with a per-router promise chain (_routerWriteQueue in
// src/index.js) and the reason is the same here: every write is read-modify-
// write — read the menu, find the row, check it is still what the operator saw,
// then set it — and two of those interleaving would let one operator's check
// pass against a row the other had already changed.
func (s *Session) InWriteQueue(fn func() error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return fn()
}

// Exec issues one command on the live connection, counted against the feature
// that called it (sources.go).
func (s *Session) Exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	return reader{s: s, source: callerSource()}.Do(cmd)
}

// topSetting reads one of the "how many rows" counts out of the settings file,
// falling back to the GENERATED default rather than a number typed here.
//
// `store.Settings()` is the raw file — it merges nothing — so a settings.json
// that has never had the field written returns nothing for it, and the caller
// must supply the default. `store.Defaults()` is generated from the live
// `src/settings.js`, so `topN` is 5 here because it is 5 there.
//
// NO CLAMP, deliberately. The live collectors take `_cfg.topN` exactly as
// stored; the bounds ([1,50] and [1,20]) are enforced on the settings WRITE, so
// clamping again here would mean two implementations of one rule and would let
// them disagree about a hand-edited file. A non-positive or non-numeric value
// falls through to the default, which is the only value that cannot render.
func topSetting(cfg store.Settings, key string) int {
	pick := func(m map[string]any) (int, bool) {
		v, ok := m[key]
		if !ok {
			return 0, false
		}
		switch n := v.(type) {
		case float64:
			return int(n), int(n) > 0
		case int:
			return n, n > 0
		}
		return 0, false
	}
	if n, ok := pick(cfg); ok {
		return n
	}
	if n, ok := pick(store.Defaults()); ok {
		return n
	}
	return 0 // the collector's own default then applies
}

// rosDebugOn reads the "RouterOS debug" checkbox.
//
// ── A BOOL ONLY, AND THE DIVERGENCE IS DELIBERATE ─────────────────────────
//
// Live is `debug: Settings.load().rosDebug` consumed as `if (this.cfg.debug)` —
// plain truthiness, under which the four characters "false" turn tracing ON.
// That is the same defect class as upstream `dd6173b`, which moved six such
// sites onto one `_isTrue`; this one was not in that sweep because nothing
// writes a string here.
//
// The port reads a bool and nothing else, which differs from live only for a
// value the validated write path cannot produce: `rosDebug` is a checkbox, the
// settings route types it as a boolean, and a hand-edited `"false"` is the only
// way to reach the difference. Choosing the safe direction there means tracing
// stays off, which is also what the operator who typed "false" meant.
//
// Recorded rather than silently matched, because "reproduce the quirk" is this
// port's default and this is a departure from it.
func rosDebugOn(cfg store.Settings) bool {
	v, _ := cfg["rosDebug"].(bool)
	return v
}

// EvRouterStatus is a router's connection state, sent to its own room and,
// through Manager.SetFleetStatus, to every client that may read the router. A map rather than a struct, so its
// TypeScript type is hand-written in web/src/events-hand.ts.
var EvRouterStatus = hub.Declare[map[string]any]("router:status")
