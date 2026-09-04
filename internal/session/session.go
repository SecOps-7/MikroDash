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
	"log"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertwire"
	"mikrodash/internal/asn"
	"mikrodash/internal/collect"
	"mikrodash/internal/collection"
	"mikrodash/internal/dormancy"
	"mikrodash/internal/geo"
	"mikrodash/internal/historywire"
	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"

	"mikrodash/internal/safe"

	"mikrodash/internal/roslimit"
)

// Session is one router's connection and collectors.
type Session struct {
	// eff is this router's resolved collection config (#105): per-router poll
	// intervals and which collectors may run at all. Resolved once in Acquire.
	//
	// ── AND NOT RE-RESOLVED, WHICH IS A REAL GAP ────────────────────────────
	//
	// This said "a live edit rebuilds the session, which is what the live
	// `collectionFingerprint` exists to decide". THAT MECHANISM WAS NEVER
	// PORTED — there is no fingerprint anywhere in this tree — so editing a
	// router's collection block does not reach a session somebody is watching;
	// it takes effect when the last viewer leaves and the session is rebuilt.
	//
	// `Reconfigure` closes the half of this that silently breaks a router: the
	// ENDPOINT AND CREDENTIALS. The collection block is left, deliberately and
	// visibly, because it needs the collectors restarting rather than the socket
	// redialling, and because a wrong poll interval is not a device that stops
	// answering. Recorded here rather than left as a comment describing
	// something that does not exist.
	eff collection.Resolved

	// dormancy decides which collectors are asleep. Nil until Acquire builds it,
	// and consulted as a VETO by ResumeCollector — see dormancy_targets.go.
	dormancy *dormancy.Supervisor

	RouterID string
	Label    string
	// alertsEnabled is this router's per-device alert switch, captured when the
	// session is built. The live app RE-READS it on every event "in case it was
	// toggled after session creation"; this said the port "gets that for free,
	// since a change to the record rebuilds the session
	// (`collectionFingerprint`)" — and nothing here rebuilds it, because that
	// fingerprint does not exist. Same gap as `eff` above: toggling alerts on a
	// router somebody is watching takes effect when the session is next built.
	alertsEnabled bool

	h   *hub.Hub
	cfg routeros.Config

	mu     sync.Mutex
	client *routeros.Client
	refs   int
	// linger is the pending idle teardown, armed when the last viewer leaves and
	// stopped when one comes back. Non-nil only while the grace is running.
	linger    *time.Timer
	closed    bool
	connected bool
	// observed is whether the two fields above are an ANSWER yet. See Observed.
	observed bool
	lastErr  string

	dns      *collect.DNS
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
	connTable    *collect.ConnTable

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

func (s *Session) LastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// DNS is the collector, for the replay a page:focus does and for the
// RefreshNow a write triggers.
func (s *Session) DNS() *collect.DNS { return s.dns }

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

// CollectorEnabled reports whether this router's config (#105) allows a
// collector to run at all.
//
// The page-focus RESUME path needs it as much as the connect path does: a
// collector the operator turned off must not come back the moment somebody
// opens its page, which is exactly what an ungated `Resume()` would do. An
// unknown key reads as ENABLED, matching the registry's own default for a
// collector nobody made disableable.
func (s *Session) CollectorEnabled(key string) bool {
	v, ok := s.eff.Enabled[key]
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

// reader adapts the session to collect.Reader. The indirection matters: the
// client is REPLACED on a reconnect, and a collector holding the old pointer
// would read from a closed connection for ever.
// defaultIfOr is index.js's fallback: a router record that names no default
// interface still needs one, because the WAN badge reads it.
func defaultIfOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

type reader struct{ s *Session }

func (r reader) Connected() bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return r.s.client != nil && r.s.connected
}

// Stream is the half of the connection that keeps a channel open. Only the logs
// collector uses it — /log/listen is a push channel, and polling /log/print
// instead would mean re-reading the whole buffer and keeping a seen-set.
func (r reader) Stream(cmd routeros.Cmd, onRow func(routeros.Reply)) (func(), error) {
	r.s.mu.Lock()
	c := r.s.client
	r.s.mu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	return c.Stream(cmd, onRow)
}

// StreamUntilDone is Stream plus notification that the stream ENDED BY ITSELF.
//
// The frequency scan is the only caller: it is a BOUNDED command, unlike the
// /listen channels Stream serves, and the difference between "it ended" and "we
// stopped it" decides whether a /cancel is written to a device that has already
// finished scanning.
func (s *Session) StreamUntilDone(
	cmd routeros.Cmd, onRow func(routeros.Reply), onDone func(),
) (func(), error) {
	s.mu.Lock()
	c := s.client
	s.mu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	return c.StreamUntilDone(cmd, onRow, onDone)
}

func (r reader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	r.s.mu.Lock()
	c := r.s.client
	r.s.mu.Unlock()
	if c == nil {
		return nil, errNotConnected
	}
	if cmd.Timeout == 0 {
		cmd.Timeout = 15 * time.Second
	}
	// ── ONE ROUTER'S BUDGET, SHARED WITH THE TWO POOLS ───────────────────
	//
	// Taken AFTER the connection check and the timeout default, so a call that
	// was never going to reach the router does not hold a slot while it fails.
	// Deferred immediately, so an early return or a panic inside Do cannot leak
	// one -- a leaked slot never comes back.
	done := roslimit.Acquire(r.s.RouterID)
	defer done()
	return c.Do(cmd)
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
	history *historywire.Wire
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

// SetHistoryWire installs the history recorder. Nil, or a wire built with
// `enabled` false, records nothing.
func (m *Manager) SetHistoryWire(w *historywire.Wire) { m.history = w }

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
		eff:           eff,
		dormancy:      dormancy.NewSupervisor(dormancy.Defaults()),
		RouterID:      rec.ID,
		Label:         rec.Label,
		alertsEnabled: rec.AlertsEnabled,
		h:             m.h,
		refs:          1,
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
	room := "router-" + s.RouterID + "-"
	// An EMPTY sub means router-wide, the room `router:status` uses. It exists
	// for chrome: the gauges, the uptime chip and the RouterOS version row are
	// in the top bar, not on a page, so they must reach a viewer who has not
	// opened any particular page. Everything with a page of its own names it.
	// A sub naming SEVERAL rooms, comma separated, delivers ONE copy to the
	// union — interfaceStatus sends its full payload to three rooms and a viewer
	// can be in two of them. socket.io's `.to(a).to(b)` behaves the same way,
	// and looping Broadcast would send that viewer the frame twice.
	emit := func(sub, event string, payload any) {
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
		fired := m.alerts.Evaluate(alert.Router{
			ID: s.RouterID, AlertsEnabled: s.alertsEnabled,
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
			m.h.Broadcast("router-"+s.RouterID, event, payload)
			return
		}
		if strings.Contains(sub, ",") {
			subs := strings.Split(sub, ",")
			rooms := make([]string, 0, len(subs))
			for _, one := range subs {
				rooms = append(rooms, room+strings.TrimSpace(one))
			}
			m.h.BroadcastRooms(rooms, event, payload)
			return
		}
		m.h.Broadcast(room+sub, event, payload)
	}
	s.dns = collect.NewDNS(reader{s}, emit, s.eff.Poll["dns"])
	// Built FIRST, because three other collectors take it as their RateSource.
	// It is the only one they depend on, and it depends on none of them.
	s.ifStatus = collect.NewIfStatus(reader{s}, emit, rec.ID, s.eff.Poll["ifStatus"])
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
	s.bridges = collect.NewBridges(reader{s}, emit, s.ifStatus, s.eff.Poll["bridges"])
	// Rates from `s.ifStatus` here too. STILL nil lease counts, though — and that
	// half was and remains true, for a reason of its own now that dhcpLeases IS
	// ported: vlans wants
	// per-VLAN client counts (`VlanClients()`), and the lease collector offers
	// addresses (`LeaseIPs()`), which is what dhcpNetworks needs. Joining leases
	// to VLANs is the vlans page's question, not this one's, so the column keeps
	// degrading to 0 until someone answers it.
	s.vlans = collect.NewVlans(reader{s}, emit, s.ifStatus, nil, s.eff.Poll["vlans"])
	s.wan = collect.NewWan(reader{s}, emit, s.ifStatus, s.eff.Poll["wan"])
	s.packages = collect.NewPackages(reader{s}, emit, s.eff.Poll["packages"])
	s.routing = collect.NewRouting(reader{s}, emit, s.eff.Poll["routing"])
	// Built BEFORE dhcpNetworks, which takes it as its lease source: a subnet's
	// client count is the leases that fall inside it, and only this collector
	// knows what they are. Unlike vlans, this one is no longer nil.
	s.dhcpLeases = collect.NewDHCPLeases(reader{s}, emit, s.eff.Poll["dhcpLeases"])
	// The WAN interface name is the record's, falling back to "WAN1" inside the
	// collector exactly as index.js does.
	s.dhcpNetworks = collect.NewDHCPNetworks(reader{s}, emit, s.dhcpLeases, "", s.eff.Poll["dhcpNetworks"])
	// Its own poll interval, not the shared default: PPP rates are differences
	// between byte counters, so the interval IS the measurement window.
	s.ppp = collect.NewPPP(reader{s}, emit, s.eff.Poll["ppp"])
	s.vpn = collect.NewVPN(reader{s}, emit, s.eff.Poll["vpn"])
	// NOT suspended by page focus, because it has no page: the Dashboard card it
	// feeds is visible whenever anyone is looking at the router at all. The idle
	// gate in Manager.Release still stops it when the last viewer leaves.
	s.netwatch = collect.NewNetwatch(reader{s}, emit, s.eff.Poll["netwatch"])
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
	s.talkers = collect.NewTalkers(reader{s}, emit, s.eff.Poll["talkers"],
		topSetting(cfgSettings, "topTalkersN"))
	// Same again: the latency block is part of the Dashboard's network card, so
	// there is no page to gate on. The target is the live default — the settings
	// write that would let an operator change it is a cutover item, so passing
	// anything else here would imply a choice that cannot yet be made.
	s.ping = collect.NewPing(reader{s}, emit, s.eff.Poll["ping"], "")
	// THE USERNAME WE ACTUALLY CONNECT AS is what the lockout guard protects, so
	// it comes from the live config rather than from anything the page sends.
	// The live app also passes whatever routers.json separately holds, because
	// the two can drift — see ResolveSelf. This side has one source today, and
	// the slice is here so adding the second is a one-line change rather than a
	// signature change.
	s.rosUsers = collect.NewRosUsers(reader{s}, emit, []string{s.cfg.Username}, s.eff.Poll["rosusers"])
	// The FIREWALL COLLECTOR IS BUILT FIRST because Queues borrows it by
	// reference for its FastTrack banner. Only a SUMMARY crosses that boundary —
	// a reader holding `queues` but not `firewall` learns that FastTrack is on,
	// which is a fact about the Queues page's own correctness, not a firewall
	// listing. Until this was ported the banner reported "cannot say", which is
	// the same degradation the live app applies when Firewall collection is off.
	s.firewall = collect.NewFirewall(reader{s}, emit, s.eff.Poll["firewall"])
	s.wifi = collect.NewWifi(reader{s}, emit, s.eff.Poll["wifi"])
	s.capsman = collect.NewCapsman(reader{s}, emit, s.eff.Poll["capsman"])
	s.queues = collect.NewQueues(reader{s}, emit, s.firewall, s.eff.Poll["queues"])
	// NOT gated on page focus, for the same reason as netwatch: these are the
	// dashboard's gauges, and the dashboard is on screen whenever anyone is
	// looking at the router at all. The idle gate in Manager.Release still stops
	// it when the last viewer leaves.
	s.system = collect.NewSystem(reader{s}, emit, s.eff.Poll["system"])
	// The only STREAMING collector: /log/listen pushes an entry as the router
	// writes it. No poll interval, because there is nothing to poll.
	s.logs = collect.NewLogs(reader{s}, emit)
	// Built LAST, because it joins three of the others: ifStatus names the
	// bridges, dhcpLeases names the clients, and system fills the core's
	// identity and gauges. Each is optional — a nil one costs exactly the field
	// it feeds, which is what the live app does when a collector is disabled.
	s.topology = collect.NewTopology(reader{s}, emit, s.ifStatus, rec.ID, rec.Label, s.eff.Poll["topology"]).
		WithSources(s.dhcpLeases, s.system)
	// The lease source names the clients. There is no ARP collector yet, so a
	// client with no DHCP name shows as its MAC — which is what the live app
	// does on a router that is not the client's DHCP server either.
	s.wireless = collect.NewWireless(reader{s}, emit, s.dhcpLeases, s.eff.Poll["wireless"])
	// ifStatus names the interface a source arrived on, dhcpLeases names the
	// device, and dhcpNetworks supplies the LAN ranges the source filter uses.
	// Each is optional and costs exactly the field it feeds.
	// ONE READ OF THE CONNECTION TABLE, TWO CONSUMERS. It is the heaviest read
	// this app makes and both of these want it, so connections reads it and
	// deposits the snapshot, and bandwidth differences the counters from there.
	s.connTable = collect.NewConnTable()
	s.conns = collect.NewConnections(reader{s}, emit, s.connTable, s.dhcpLeases, s.dhcpNetworks, s.eff.Poll["conns"]).
		// The heavy per-country and per-source indexes are built only when
		// somebody is on the Connections page. The hub's room occupancy is the
		// same question the Node side asks its adapter.
		// "Top Connections N" under Limits. See WithTopN for why this applies at
		// construction rather than instantly.
		WithTopN(topSetting(cfgSettings, "topN")).
		WithDetailed(func() bool { return m.h.Occupants(room+"page-connections") > 0 }).
		WithGeo(geoLookup()).
		WithOrg(asn.Lookup)
	s.bandwidth = collect.NewBandwidth(reader{s}, emit, s.ifStatus, s.dhcpLeases, s.dhcpNetworks, s.eff.Poll["bandwidth"]).
		WithTable(s.connTable).
		WithGeo(geoLookup()).
		WithOrg(asn.Lookup)
	// The default interface is what the WAN badge watches, so it is always in
	// the stream even when nobody has selected it. Five minutes of history, as
	// the live app keeps.
	s.traffic = collect.NewTraffic(reader{s}, emit, defaultIfOr(rec.DefaultIf, "WAN1"), 5)

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
// A lingering session is still a live one. `syncAlertPool` excludes routers that
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
// points it at `syncAlertPool`, so the pool reclaims a router at the moment the
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
	if s.refs > 0 {
		s.linger = nil
		s.mu.Unlock()
		m.mu.Unlock()
		return
	}
	s.closed = true
	s.linger = nil
	delete(m.live, routerID)
	c := s.client
	s.mu.Unlock()
	m.mu.Unlock()

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
	s.bridges.Stop()
	s.vlans.Stop()
	s.wan.Stop()
	s.ifStatus.Stop()
	s.firewall.Stop()
	s.system.Stop()
	s.logs.Stop()
	s.traffic.Stop()
	s.dhcpNetworks.Stop()
	s.dhcpLeases.Stop()
	s.netwatch.Stop()
	s.talkers.Stop()
	s.ping.Stop()
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
		s.bridges.Stop()
		s.vlans.Stop()
		s.wan.Stop()
		s.ifStatus.Stop()
		s.firewall.Stop()
		s.system.Stop()
		s.logs.Stop()
		s.traffic.Stop()
		s.dhcpNetworks.Stop()
		s.dhcpLeases.Stop()
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
			log.Printf("[session] %s: %v; retrying in %s", s.Label, err, retry)
			time.Sleep(retry)
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
		log.Printf("[session] %s connected", s.Label)
		s.announce()

		if first {
			// THE DORMANCY SUPERVISOR runs alongside the collectors it judges.
			// Started here rather than in Acquire because it must not tick before
			// anything has reported: its first judgement would be of a fleet of
			// empty payloads. It stops when Release closes the session.
			//
			// OUTSIDE the counted block below on purpose — it is not a collector
			// and `TestTheBackgroundCollectorCountIsRecorded` counts that block
			// with a regex.
			go s.runDormancy()

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
			if s.eff.Enabled["dns"] {
				s.dns.Start()
			}
			if s.eff.Enabled["bridges"] {
				s.bridges.Start()
			}
			if s.eff.Enabled["vlans"] {
				s.vlans.Start()
			}
			if s.eff.Enabled["wan"] {
				s.wan.Start()
			}
			if s.eff.Enabled["ifStatus"] {
				s.ifStatus.Start()
			}
			// A ONE-SHOT read, not a poll — see Firewall.Start. It is here
			// rather than on page focus because the Queues page's FastTrack
			// banner reads this collector's last payload, and a queues viewer
			// who never opens Firewall would otherwise be told "cannot say".
			if s.eff.Enabled["firewall"] {
				s.firewall.Start()
			}
			// Starts the gauge poll AND the one update check that runs at
			// startup. The check is the only call in the app that leaves the
			// router, so it is rate limited to twelve hours inside the
			// collector rather than being scheduled from here.
			if s.eff.Enabled["system"] {
				s.system.Start()
			}
			if s.eff.Enabled["logs"] {
				s.logs.Start()
			}
			// Started with the connection, not on page focus: the WAN badge is
			// chrome on every page, and the history a chart needs has to be
			// accumulating before somebody opens one.
			if s.eff.Enabled["traffic"] {
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
			if s.eff.Enabled["dhcpLeases"] {
				s.dhcpLeases.Start()
			}
			if s.eff.Enabled["dhcpNetworks"] {
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
			if s.eff.Enabled["netwatch"] {
				s.netwatch.Start()
			}
			if s.eff.Enabled["talkers"] {
				s.talkers.Start()
			}
			if s.eff.Enabled["ping"] {
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
			first = false
		} else {
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
			if s.eff.Enabled["dns"] {
				s.dns.Reconnected()
			}
			if s.eff.Enabled["bridges"] {
				s.bridges.Reconnected()
			}
			if s.eff.Enabled["vlans"] {
				s.vlans.Reconnected()
			}
			if s.eff.Enabled["ifStatus"] {
				s.ifStatus.Reconnected()
			}
			if s.eff.Enabled["wan"] {
				s.wan.Reconnected()
			}
			if s.eff.Enabled["dhcpLeases"] {
				s.dhcpLeases.Reconnected()
			}
			if s.eff.Enabled["dhcpNetworks"] {
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
			if s.eff.Enabled["packages"] {
				s.packages.Reconnected()
			}
			// RESUME, NOT Reconnected — `Routing` has no `Reconnected` and does
			// not need one. `Reconnected` exists on the collectors that hold an
			// "this menu is absent" latch, which a reboot can invalidate;
			// Routing holds no such verdict (see its struct — no `*OK` fields),
			// only caches its ticks refresh. Resume starts the loop if the
			// client is up, which it is by this point.
			if s.eff.Enabled["routing"] {
				s.routing.Resume()
			}
			if s.eff.Enabled["ppp"] {
				s.ppp.Reconnected()
			}
			if s.eff.Enabled["vpn"] {
				s.vpn.Reconnected()
			}
			if s.eff.Enabled["netwatch"] {
				s.netwatch.Reconnected()
			}
			if s.eff.Enabled["talkers"] {
				s.talkers.Reconnected()
			}
			if s.eff.Enabled["ping"] {
				s.ping.Reconnected()
			}
			if s.eff.Enabled["rosusers"] {
				s.rosUsers.Reconnected()
			}
			if s.eff.Enabled["queues"] {
				s.queues.Reconnected()
			}
			if s.eff.Enabled["firewall"] {
				s.firewall.Reconnected()
			}
			if s.eff.Enabled["wifi"] {
				s.wifi.Reconnected()
			}
			if s.eff.Enabled["capsman"] {
				s.capsman.Reconnected()
			}
			if s.eff.Enabled["system"] {
				s.system.Reconnected()
			}
			// Drops the buffer and reloads: the router that came back may have
			// rebooted, in which case the lines held here describe a different
			// uptime.
			if s.eff.Enabled["logs"] {
				s.logs.Reconnected()
			}
			if s.eff.Enabled["topology"] {
				s.topology.Reconnected()
			}
			if s.eff.Enabled["wireless"] {
				s.wireless.Reconnected()
			}
			if s.eff.Enabled["bandwidth"] {
				s.bandwidth.Reconnected()
			}
			if s.eff.Enabled["traffic"] {
				s.traffic.Reconnected()
			}
			if s.eff.Enabled["conns"] {
				s.conns.Reconnected()
			}
		}

		s.waitUntilDown(c)

		s.mu.Lock()
		down := !s.closed
		s.client = nil
		s.connected = false
		s.mu.Unlock()

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
		_ = c.Close()
		s.dns.Suspend()
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
		log.Printf("[session] %s disconnected; retrying in %s", s.Label, retry)
		time.Sleep(retry)
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
func (s *Session) Collection() collection.Resolved { return s.eff }

// announce pushes the connection state to everybody watching this router, so a
// reboot shows up as a status chip rather than as a table that quietly stops
// changing.
func (s *Session) announce() {
	s.h.Broadcast("router-"+s.RouterID, "router:status", map[string]any{
		"routerId":  s.RouterID,
		"connected": s.Connected(),
		"reason":    s.LastError(),
	})
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

// Exec issues one command on the live connection.
func (s *Session) Exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	return reader{s}.Do(cmd)
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
