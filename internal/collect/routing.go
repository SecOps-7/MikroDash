package collect

// Routing collector: the route table, and BGP sessions where a router has them.
//
// WHAT IS PROVEN AND WHAT IS NOT, STATED UP FRONT. The differential gate drives
// this against the AX3 fixture, and that router runs no BGP — the capture holds
// two commands, `/ip/route/print` and `/ipv6/route/print`, and the golden's
// `peers` is empty with `summary` all zeros. So the ROUTE half is verified
// field-for-field against the Node payload; the BGP half is verified only by the
// unit tests beside it, which drive the pure transforms directly.
//
// The BGP code is here rather than omitted because leaving it out would be a
// silent behavioural gap: a router that does have BGP would show an empty Peers
// table and nothing would say why. None of the three routers this project
// targets can prove it, and that is worth writing down rather than discovering.
//
// FLAG CASE IS LOAD-BEARING, and the manual is the reason. RouterOS prints route
// flags in different cases depending on the menu — the DHCP page documents
// `A - active, D - dynamic, C - connect, S - static, b - bgp, o - ospf` while
// Policy Routing documents `c - connect, s - static` in lower case. So every
// flag is tested in BOTH cases, and the matching property is consulted as well.
// This is not defensive coding; it is two documented spellings of one table.
//
// `flags` DOES NOT cross the wire, and getting here took a round trip. The Node
// original destructured `_flags` while the field was named `flags`, so its
// exclusion silently did nothing and every route carried its whole flags object
// to every viewer; the golden proved the payload carried it, so the port
// reproduced that and reported the mismatch in ../MikroDash/ToDo.md. The live
// app has since stripped it for real, and this is re-synced to the fix.
//
// The field is still COMPUTED and still stored — `active`, `type` and
// `protocol` are derived from it, and routeCounts reads it off the stored rows.
// Only the JSON tag changed, which is the whole of the difference.

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/routeros"
)

const routeProplist = "=.proplist=.id,dst-address,gateway,distance,comment,.flags," +
	"active,static,dynamic,connect,bgp,ospf,disabled"

var (
	routeV4Cmd = routeros.Cmd{Path: "/ip/route/print", Args: []string{routeProplist}}
	routeV6Cmd = routeros.Cmd{Path: "/ipv6/route/print", Args: []string{routeProplist}}
)

// routeHistoryLen is HISTORY_LEN: how many prefix-count samples a peer keeps.
const routeHistoryLen = 60

// routeCap is the most routes the page is sent. A full table on a transit
// router is tens of thousands of rows, and the browser is not the place to
// discover that.
const routeCap = 800

// RouteFlags is what RouterOS says about a row, from the flag string or the
// matching property.
type RouteFlags struct {
	Active   bool `json:"active"`
	Static   bool `json:"static"`
	Dynamic  bool `json:"dynamic"`
	Connect  bool `json:"connect"`
	BGP      bool `json:"bgp"`
	OSPF     bool `json:"ospf"`
	Disabled bool `json:"disabled"`
}

// Route is one row as the page consumes it. Field order matches the Node
// payload's.
//
// `raw` stays unexported: it is the whole RouterOS row and the page is not
// entitled to fields nobody asked for. The id DOES cross the wire — it addresses
// a row, it does not authorise one, and every write re-reads and re-checks
// before touching it.
type Route struct {
	Dst      string     `json:"dst"`
	Gateway  string     `json:"gateway"`
	Distance int        `json:"distance"`
	Active   bool       `json:"active"`
	Comment  string     `json:"comment"`
	Type     string     `json:"type"`
	Protocol string     `json:"protocol"`
	Flags    RouteFlags `json:"-"`
	Family   string     `json:"family"`
	ID       string     `json:"id"`

	raw routeros.Reply
}

type RouteCounts struct {
	Total   int `json:"total"`
	Connect int `json:"connect"`
	Static  int `json:"static"`
	Dynamic int `json:"dynamic"`
	BGP     int `json:"bgp"`
	OSPF    int `json:"ospf"`
}

// Peer is one BGP session.
type Peer struct {
	Key           string `json:"key"`
	PeerType      string `json:"peerType"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	RemoteAddr    string `json:"remoteAddr"`
	RemoteAs      int64  `json:"remoteAs"`
	State         string `json:"state"`
	UptimeSec     int    `json:"uptimeSec"`
	Prefixes      int    `json:"prefixes"`
	PrefixHistory []int  `json:"prefixHistory"`
	MessagesSent  int    `json:"messagesSent"`
	MessagesRecv  int    `json:"messagesRecv"`
	LastError     string `json:"lastError"`
	HoldTime      int    `json:"holdTime"`
	Keepalive     int    `json:"keepalive"`
	Flapping      bool   `json:"flapping"`
}

type PeerSummary struct {
	Total       int `json:"total"`
	Established int `json:"established"`
	Down        int `json:"down"`
}

type RoutingPayload struct {
	TS          int64       `json:"ts"`
	PollMs      int         `json:"pollMs"`
	RouteCounts RouteCounts `json:"routeCounts"`
	Peers       []Peer      `json:"peers"`
	Routes      []Route     `json:"routes"`
	Summary     PeerSummary `json:"summary"`
}

// safeInt is `parseInt(v || '0', 10) || 0`: a leading number wins over trailing
// junk ("64512abc" is 64512) and anything unparseable is zero.
// safeAS parses an AS number. Separate from safeInt and 64-bit on purpose: AS
// numbers are 32-bit UNSIGNED, so the top of the range does not fit a 32-bit
// signed int and strconv.Atoi rejects it outright on such a build.
func safeAS(v string) int64 {
	s := strings.TrimSpace(v)
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.ParseInt(s[:end], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func safeInt(v string) int {
	s := strings.TrimSpace(v)
	end := 0
	if end < len(s) && (s[end] == '-' || s[end] == '+') {
		end++
	}
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[:end])
	if err != nil {
		return 0
	}
	return n
}

func parseRouteFlags(r routeros.Reply) RouteFlags {
	f := r[".flags"]
	if f == "" {
		f = r["flags"]
	}
	has := func(k string) bool { return r[k] == "true" }
	any := func(letters string) bool { return strings.ContainsAny(f, letters) }
	return RouteFlags{
		Active:   any("Aa") || has("active"),
		Static:   any("Ss") || has("static"),
		Dynamic:  strings.Contains(f, "D") || has("dynamic"),
		Connect:  any("Cc") || has("connect"),
		BGP:      any("bB") || has("bgp"),
		OSPF:     any("oO") || has("ospf"),
		Disabled: any("Xx") || has("disabled"),
	}
}

var ipv4Re = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)

func mapRoute(r routeros.Reply, family string) Route {
	flags := parseRouteFlags(r)
	gateway := r["gateway"]

	// A ROW WITH A REAL NEXT HOP AND NO TYPE FLAG IS TREATED AS STATIC.
	// RouterOS does not always flag a manually added route, and the fallback
	// below would otherwise file it under "connect" — the one category the page
	// treats as not editable.
	hasTypeInfo := flags.Static || flags.Dynamic || flags.Connect || flags.BGP || flags.OSPF
	hasRealNexthop := gateway != "" && gateway != "0.0.0.0" && gateway != "::" &&
		(ipv4Re.MatchString(gateway) || strings.Contains(gateway, ":"))
	if !hasTypeInfo && hasRealNexthop {
		flags.Static = true
	}

	typ := "connect"
	switch {
	case flags.Static:
		typ = "static"
	case flags.Dynamic:
		typ = "dynamic"
	}
	protocol := typ
	switch {
	case flags.BGP:
		protocol = "bgp"
	case flags.OSPF:
		protocol = "ospf"
	}
	if family == "" {
		family = "ipv4"
	}

	return Route{
		Dst:      r["dst-address"],
		Gateway:  gateway,
		Distance: safeInt(r["distance"]),
		Active:   flags.Active,
		Comment:  r["comment"],
		Type:     typ,
		Protocol: protocol,
		Flags:    flags,
		Family:   family,
		ID:       r[".id"],
		raw:      r,
	}
}

var (
	hmsRe = regexp.MustCompile(`^(\d+):(\d+):(\d+)$`)
	// ms is an alternative of its own, listed first, so the 930 of "24s930ms" is
	// not read as 930 minutes. A bare number is the legacy seconds spelling.
	durRe    = regexp.MustCompile(`(\d+)(ms|w|d|h|m|s)`)
	digitsRe = regexp.MustCompile(`^\d+$`)
)

// parseUptime reads RouterOS durations into seconds: "01:02:03", "2w1d2h3m4s", "24s930ms"
// (milliseconds dropped) and a bare "90".
func parseUptime(s string) int {
	if s == "" {
		return 0
	}
	if m := hmsRe.FindStringSubmatch(s); m != nil {
		return safeInt(m[1])*3600 + safeInt(m[2])*60 + safeInt(m[3])
	}
	if digitsRe.MatchString(s) {
		return safeInt(s)
	}
	scale := map[string]int{"w": 604800, "d": 86400, "h": 3600, "m": 60, "s": 1, "ms": 0}
	sec := 0
	for _, m := range durRe.FindAllStringSubmatch(s, -1) {
		sec += safeInt(m[1]) * scale[m[2]]
	}
	return sec
}

var ixRe = regexp.MustCompile(`\b(ix|ixp|peering|rs\d|route.server|routeserver)\b`)

// classifyPeer labels a session for the page. The private-ASN ranges are
// RFC 6996's: 64512–65534 and 4200000000–4294967294.
func classifyPeer(remoteAs int64, description, name string) string {
	if (remoteAs >= 64512 && remoteAs <= 65534) ||
		(remoteAs >= 4200000000 && remoteAs <= 4294967294) {
		return "private"
	}
	if ixRe.MatchString(strings.ToLower(description + " " + name)) {
		return "ix"
	}
	return "upstream"
}

func peerKey(p routeros.Reply) string {
	for _, k := range []string{"remote.address", "remote-address", "name"} {
		if v := p[k]; v != "" {
			return v
		}
	}
	return "?"
}

// normalisePeerState folds RouterOS's spellings onto the set the page renders.
func normalisePeerState(s routeros.Reply) string {
	raw := strings.ToLower(s["state"])
	if raw == "" {
		if s["established"] == "true" {
			raw = "established"
		} else {
			raw = "idle"
		}
	}
	for _, c := range []struct{ needle, out string }{
		{"establish", "established"},
		{"active", "active"},
		{"connect", "connect"},
		{"opensent", "opensent"},
		{"openconfirm", "openconfirm"},
		{"idle", "idle"},
	} {
		if strings.Contains(raw, c.needle) {
			return c.out
		}
	}
	return raw
}

type peerFlapState struct {
	lastState  string
	lastChange int64
	window     []int64
}

// Routing is the collector.
type Routing struct {
	tableCore[RoutingPayload]
	emit Emit

	routes map[string]Route
	order  []string // insertion order, so the payload is stable across readings

	sessions      map[string]routeros.Reply
	sessionOrder  []string
	peerCfg       map[string]routeros.Reply
	prefixHistory map[string][]int
	peerState     map[string]*peerFlapState

	now func() int64
	// bgpOnly, when set, skips the route tables. See derive.
	bgpOnly bool
}

// routeConfigEvery is how many polls apart the route tables are re-read.
//
// ── THE FAST/SLOW RULE, WHICH THIS COLLECTOR WAS THE LAST TO APPLY ──────────
//
// The operator's rule (2026-09-08): a collector's LIVE reads keep its poll, and
// its reads of things that rarely change go to "a 30 second cadence or longer".
// Seven collectors already did this — `bridgeConfigEvery`, `vlanConfigEvery`,
// `pppConfigEvery`, `capsConfigEvery`, `dnsConfigEvery`, `wanConfigEvery`,
// `wifiConfigEvery`, plus `system`'s hand-rolled `systemHealthEvery` — and this
// one did not, so it read two whole route tables every ten seconds.
//
// WHICH HALF IS WHICH, and it is not the obvious one. BGP SESSION STATE is the
// live half: it is what flaps, and the peers payload is what the alert rules
// read. The ROUTE TABLES are the slow half — they are large, and on a network
// without a dynamic protocol they do not move at all. Splitting the other way
// round would have kept the cheap read fast and the expensive one slow.
//
// Three at the 10s default is 30 seconds, the floor of the rule rather than the
// 60-120s its neighbours sit at, because a route table IS more volatile than a
// bridge port list — just not ten-seconds volatile.
//
// A WRITE DOES NOT WAIT FOR IT: `RefreshNow` re-reads the routes immediately and
// is what the route write path calls, so an operator never watches their own
// edit take half a minute to appear.
const routeConfigEvery = 3

func NewRouting(ros Reader, emit Emit, pollMs int) *Routing {
	r := &Routing{
		emit:          emit,
		routes:        map[string]Route{},
		sessions:      map[string]routeros.Reply{},
		peerCfg:       map[string]routeros.Reply{},
		prefixHistory: map[string][]int{},
		peerState:     map[string]*peerFlapState{},
		now:           func() int64 { return time.Now().UnixMilli() },
	}
	// Subscribed to the BGP session menu for its CADENCE: `derive` reads the BGP
	// menus itself, and the route tables are the slow lane.
	r.setup(r, ros, pollMs, tableSpec{
		cmd: routingBgpCmd, poll: [3]int{10000, 500, 300000}, slowEvery: routeConfigEvery,
	})
	return r
}

// safeRead is `_safeWrite`: a failure is an empty result, not an error. Every
// menu here is optional — /ipv6/route is absent on a build without IPv6 and the
// BGP menus are absent on most routers — and a routing page that refused to
// render because one of them is missing would be wrong on the majority of them.
// safeRead is routed THROUGH THE CACHE: `wan` reads /ip/route with a proplist
// that is a strict subset of this collector's all-fields read, so whichever
// asks first pays. The three BGP menus have this collector alone.
func (r *Routing) safeRead(cmd routeros.Cmd) []routeros.Reply {
	rows, err := readVia(r.cache, r.ros, cmd, r.pollMs.duration())
	if err != nil {
		return nil
	}
	return rows
}

func (r *Routing) loadRoutes() {
	v4 := r.safeRead(routeV4Cmd)
	v6 := r.safeRead(routeV6Cmd)

	r.routes = map[string]Route{}
	r.order = r.order[:0]
	add := func(rows []routeros.Reply, family, prefix string) {
		for _, row := range rows {
			id := row[".id"]
			if id == "" {
				continue
			}
			key := prefix + id
			if _, seen := r.routes[key]; !seen {
				r.order = append(r.order, key)
			}
			r.routes[key] = mapRoute(row, family)
		}
	}
	add(v4, "ipv4", "")
	add(v6, "ipv6", "v6:")
}

// derive is the BGP sessions it was handed, the connection config behind them and,
// on the slow lane, the route tables. The delivered rows ARE the session menu, so
// it is never read a second time in the same reading.
//
// ── bgpOnly SKIPS THE ROUTE TABLES ────────────────────────────────────
//
// The alert pool runs this collector for a router nobody is looking at, and
// the alert rules read `peers` and nothing else. Reading `/ip/route/print`
// and `/ipv6/route/print` there is load for a payload no page renders — on
// hardware whose documented limit is concurrent API channels, and per
// alert-enabled router.
//
// The live pool passes `bgpOnly: true` for exactly this reason
// (`alertSessions.js`), and says so. Without the option this port would read
// both tables on every alert tick for every router.
// The route tables are the SLOW lane; the BGP menus are the fast one. Tick 0
// reads both, so the first payload is never missing its routes.
func (r *Routing) derive(rows []routeros.Reply, err error, slow bool) (*RoutingPayload, string) {
	// A FAILED READ KEEPS THE LAST PAYLOAD. It read as "no sessions", so a
	// timeout blanked the peers for a tick. A menu that is not there is empty.
	if err != nil && !menuGone(err) {
		return nil, ""
	}
	if !r.bgpOnly && slow {
		r.loadRoutes()
	}
	r.loadBGP(rows)
	return r.build(r.buildPeers()), ""
}

// reset keeps the prefix history and the flap windows: they describe peers, and a
// reconnect is not a peer event.
func (r *Routing) reset() {}

// BGPOnly stops this collector reading the route tables. See Tick.
//
// A SETTER RATHER THAN A CONSTRUCTOR ARGUMENT, matching how `WithDetailed`,
// `WithGeo` and `WithOrg` are done on the other collectors: the page path
// constructs it plainly and only the pool asks for the narrow mode.
func (r *Routing) BGPOnly() *Routing { r.bgpOnly = true; return r }

// loadBGP takes the v7 session rows.
//
// NO LEGACY FALLBACK. It read `/routing/bgp/peer/print` whenever the session
// table was empty, which is every tick on a router without BGP; that menu does
// not exist in RouterOS v7 (7.1.1 onwards), and this app is v7-only, so it was
// one refused command per tick per router.
//
// UNVERIFIED BY ANY FIXTURE — see the package note. None of the three routers
// runs BGP, so what is proven here is the shape of the transform, by the unit
// tests, not that these menus answer as expected on hardware that has them.
// routingBgpCmd is hoisted so the SUBSCRIPTION and the read share one field
// list. The subscription discards its rows -- it is there for cadence -- but it
// still contributes to the menu's proplist union, so declaring it separately
// would be two statements of the same want, drifting apart.
var routingBgpCmd = routeros.Cmd{
	Path: "/routing/bgp/session/print",
	Args: []string{"=.proplist=name,remote.address,remote.as,established,uptime," +
		"prefix-count,local.messages,remote.messages,output.last-notification,hold-time,keepalive-time"},
}

func (r *Routing) loadBGP(rows []routeros.Reply) {
	r.sessions = map[string]routeros.Reply{}
	r.sessionOrder = r.sessionOrder[:0]
	for _, row := range rows {
		k := peerKey(row)
		if k == "?" {
			continue
		}
		if _, seen := r.sessions[k]; !seen {
			r.sessionOrder = append(r.sessionOrder, k)
		}
		r.sessions[k] = row
	}

	cfg := r.safeRead(routeros.Cmd{
		Path: "/routing/bgp/connection/print",
		Args: []string{"=.proplist=name,remote.address,remote-address,remote.as,remote-as,comment"},
	})
	r.peerCfg = map[string]routeros.Reply{}
	for _, row := range cfg {
		addr := row["remote.address"]
		if addr == "" {
			addr = row["remote-address"]
		}
		if addr != "" {
			r.peerCfg[addr] = row
		}
	}
}

// buildPeers turns the session rows into what the page renders, carrying the two
// pieces of state a single read cannot hold: the prefix-count history, and
// whether the session is flapping.
func (r *Routing) buildPeers() []Peer {
	now := r.now()
	peers := []Peer{}

	for _, key := range r.sessionOrder {
		s := r.sessions[key]
		remoteAddr := firstNonEmpty(s["remote.address"], s["remote-address"])
		cfg := r.peerCfg[remoteAddr]
		name := strings.TrimSpace(s["name"])

		// Ghost rows: no address and no meaningful name. A router mid-teardown
		// reports them, and they render as a peer nobody configured.
		if remoteAddr == "" && (name == "" || name == "?") {
			continue
		}

		// int64, not int. A 4-byte ASN above 2^31 cannot be held in a 32-bit int,
		// and strconv.Atoi does not merely truncate it -- it fails, so the value
		// was wrong before any comparison saw it.
		remoteAs := safeAS(firstNonEmpty(s["remote.as"], s["remote-as"], cfg["remote.as"], cfg["remote-as"]))
		prefixes := safeInt(s["prefix-count"])
		state := normalisePeerState(s)

		hist := append(r.prefixHistory[key], prefixes)
		if len(hist) > routeHistoryLen {
			hist = hist[len(hist)-routeHistoryLen:]
		}
		r.prefixHistory[key] = hist

		// FLAP DETECTION: three state CHANGES inside five minutes. Counted only
		// on a change, so a peer that is merely down does not accumulate flaps
		// by sitting still.
		const flapWindow = int64(5 * 60 * 1000)
		const flapThreshold = 3
		ps := r.peerState[key]
		if ps == nil {
			ps = &peerFlapState{lastState: state, lastChange: now}
			r.peerState[key] = ps
		}
		flapping := false
		if ps.lastState != state {
			ps.window = append(ps.window, now)
			kept := ps.window[:0]
			for _, t := range ps.window {
				if now-t < flapWindow {
					kept = append(kept, t)
				}
			}
			ps.window = kept
			flapping = len(ps.window) >= flapThreshold
			ps.lastState = state
			ps.lastChange = now
		}

		peers = append(peers, Peer{
			Key:           key,
			PeerType:      classifyPeer(remoteAs, cfg["comment"], firstNonEmpty(s["name"], cfg["name"])),
			Name:          firstNonEmpty(s["name"], cfg["name"], remoteAddr, "?"),
			Description:   cfg["comment"],
			RemoteAddr:    remoteAddr,
			RemoteAs:      remoteAs,
			State:         state,
			UptimeSec:     parseUptime(s["uptime"]),
			Prefixes:      prefixes,
			PrefixHistory: append([]int{}, hist...),
			// v7's session menu counts BGP messages, not updates: local.messages
			// sent, remote.messages received. The legacy peer menu's update
			// counters are the fallback. Hold and keepalive are durations there
			// ("3m"), which safeInt read as 3 and the hold-timer rule then took
			// for a three-second hold.
			MessagesSent: safeInt(firstNonEmpty(s["local.messages"], s["updates-sent"])),
			MessagesRecv: safeInt(firstNonEmpty(s["remote.messages"], s["updates-received"])),
			LastError:    firstNonEmpty(s["output.last-notification"], s["inactive-reason"], s["last-error"]),
			HoldTime:     parseUptime(s["hold-time"]),
			Keepalive:    parseUptime(s["keepalive-time"]),
			Flapping:     flapping,
		})
	}

	// Prune state for sessions that are gone, or a reconfigured router
	// accumulates history for peers it no longer has.
	live := map[string]bool{}
	for _, p := range peers {
		live[p.Key] = true
	}
	for k := range r.prefixHistory {
		if !live[k] {
			delete(r.prefixHistory, k)
		}
	}
	for k := range r.peerState {
		if !live[k] {
			delete(r.peerState, k)
		}
	}
	return peers
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// RoutingInput is one tick's worth of the outside world, for BuildRouting.
//
// ROUTES ARRIVE AS A SLICE, NOT AS THE COLLECTOR'S MAP-AND-ORDER PAIR. The
// collector keeps `routes` keyed and `order` alongside it so a route can be
// updated in place; the derivation only ever walks them in order, so it takes
// the walk rather than the storage.
type RoutingInput struct {
	Routes []Route
	// Peers may be NIL, which means "this tick learned nothing new about peers"
	// -- a route-only refresh. It is not the same as an empty slice, which means
	// "there are no peers". See BuildRouting.
	Peers []Peer
	// LastPeers is what the previous payload carried, used only to fill a nil
	// Peers. The carried half of `func(prior, in) (out, prior)`.
	LastPeers []Peer
	Now       int64
}

// BuildRouting is the Routing payload, pure.
//
// ── NIL PEERS AND EMPTY PEERS ARE DIFFERENT ANSWERS ────────────────────────
//
// This collector refreshes routes and peers on separate paths, so a tick can
// produce routes with nothing new to say about BGP. Nil means exactly that, and
// the previous payload's peers are carried forward; an empty slice means the
// router reports no peers and the summary must read zero.
//
// Collapsing the two would make a route refresh blank the BGP card, which is a
// card going empty and refilling on a rhythm nobody could explain from the page.
//
// ── THE COUNTS AND THE SUMMARY ARE FUNCTIONS OF THE ROWS ───────────────────
//
// Both were computed inline, so "a route that is both BGP and dynamic counts in
// each" and "a peer that is not established counts as down" needed a collector
// and a tick to test. They need neither.
func BuildRouting(in RoutingInput) *RoutingPayload {
	// Only static and dynamic rows reach the page: a connected route is a
	// property of an interface, and the Interfaces page is where it belongs.
	shown := make([]Route, 0, len(in.Routes))
	for _, rt := range in.Routes {
		if rt.Type == "static" || rt.Type == "dynamic" {
			shown = append(shown, rt)
			if len(shown) == routeCap {
				break
			}
		}
	}

	// THE COUNTS ARE OVER EVERY ROUTE, NOT THE SHOWN ONES. The page displays a
	// filtered, capped list and the counters describe the whole table -- so a
	// router past `routeCap` still reports its real totals rather than the size
	// of the slice it happened to send.
	counts := RouteCounts{Total: len(in.Routes)}
	for _, rt := range in.Routes {
		if rt.Flags.Connect {
			counts.Connect++
		}
		if rt.Flags.Static {
			counts.Static++
		}
		if rt.Flags.Dynamic {
			counts.Dynamic++
		}
		if rt.Flags.BGP {
			counts.BGP++
		}
		if rt.Flags.OSPF {
			counts.OSPF++
		}
	}

	peers := in.Peers
	if peers == nil {
		peers = in.LastPeers
	}
	if peers == nil {
		peers = []Peer{}
	}
	sum := PeerSummary{Total: len(peers)}
	for _, p := range peers {
		if p.State == "established" {
			sum.Established++
		} else {
			sum.Down++
		}
	}

	return &RoutingPayload{
		TS: in.Now,
		// Zero on the Node side too: that page is stream-driven there, so there
		// is no poll interval to report.
		PollMs:      0,
		RouteCounts: counts,
		Peers:       peers,
		Routes:      shown,
		Summary:     sum,
	}
}

func (r *Routing) build(peers []Peer) *RoutingPayload {
	all := make([]Route, 0, len(r.order))
	for _, k := range r.order {
		all = append(all, r.routes[k])
	}
	var lastPeers []Peer
	if last := r.Last(); last != nil {
		lastPeers = last.Peers
	}
	payload := BuildRouting(RoutingInput{
		Routes: all, Peers: peers, LastPeers: lastPeers, Now: r.now(),
	})
	return payload
}

// send is the routing payload. NO FINGERPRINT: every reading is sent.
func (r *Routing) send(p RoutingPayload) {
	// ── ALSO THE DASHBOARD, WHICH IS A DELIBERATE DEPARTURE ─────────────────
	//
	// `dc-card-routes` and `dc-card-bgp` are dashboard cards fed by this event,
	// and `dashboard.ts` has always listened for it. They rendered em dashes
	// because the payload only ever reached `page-routing` -- so a viewer saw
	// the cards fill by opening the Routing page and never otherwise.
	//
	// The Node app had the identical gap: `emit-rooms-audit`'s recorded ledger
	// has this event at `page-routing` twice and nowhere else. This is therefore
	// NOT a port defect being repaired but a behaviour being changed on purpose,
	// which is allowed now that the port is over and was not while it ran.
	//
	// `page-dashboard` rather than a card room because these two cards have no
	// entry in `CARD_ROOMS` -- the grid never sends `dashcard:focus` for them, so
	// there is no room to join. It is the same channel `netwatch:update`,
	// `ping:update` and `talkers:update` already use to reach dashboard cards.
	EvRoutingUpdate.Emit(r.emit, routingRooms.Join(), p)
}
