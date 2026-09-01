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
	UpdatesSent   int    `json:"updatesSent"`
	UpdatesRecv   int    `json:"updatesRecv"`
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
	dRe   = regexp.MustCompile(`(\d+)d`)
	hRe   = regexp.MustCompile(`(\d+)h`)
	mRe   = regexp.MustCompile(`(\d+)m`)
	sRe   = regexp.MustCompile(`(\d+)s`)
)

// parseUptime reads RouterOS's two duration spellings: "01:02:03" and "1d2h3m4s".
func parseUptime(s string) int {
	if s == "" {
		return 0
	}
	if m := hmsRe.FindStringSubmatch(s); m != nil {
		return safeInt(m[1])*3600 + safeInt(m[2])*60 + safeInt(m[3])
	}
	sec := 0
	for _, u := range []struct {
		re    *regexp.Regexp
		scale int
	}{{dRe, 86400}, {hRe, 3600}, {mRe, 60}, {sRe, 1}} {
		if m := u.re.FindStringSubmatch(s); m != nil {
			sec += safeInt(m[1]) * u.scale
		}
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
	ros    Reader
	emit   Emit
	pollMs *pollInterval

	routes map[string]Route
	order  []string // insertion order, so the payload is stable across ticks
	last   *RoutingPayload
	loop   *pollLoop

	sessions      map[string]routeros.Reply
	sessionOrder  []string
	peerCfg       map[string]routeros.Reply
	prefixHistory map[string][]int
	peerState     map[string]*peerFlapState

	now func() int64

	// bgpOnly, when set, skips the route tables. See Tick.
	bgpOnly bool
}

func NewRouting(ros Reader, emit Emit, pollMs int) *Routing {
	r := &Routing{
		ros:           ros,
		emit:          emit,
		pollMs:        newPollInterval(clampPoll(pollMs, 10000, 2000, 60000)),
		routes:        map[string]Route{},
		sessions:      map[string]routeros.Reply{},
		peerCfg:       map[string]routeros.Reply{},
		prefixHistory: map[string][]int{},
		peerState:     map[string]*peerFlapState{},
		now:           func() int64 { return time.Now().UnixMilli() },
	}
	r.loop = newPollLoop(func() { r.Tick() }, func() time.Duration {
		return r.pollMs.duration()
	})
	return r
}

func (r *Routing) Suspend() { r.loop.stop() }

func (r *Routing) Resume() {
	if r.ros.Connected() {
		r.loop.start()
	}
}

func (r *Routing) Stop() { r.loop.stop() }

func (r *Routing) Last() *RoutingPayload { return r.last }

// safeRead is `_safeWrite`: a failure is an empty result, not an error. Every
// menu here is optional — /ipv6/route is absent on a build without IPv6 and the
// BGP menus are absent on most routers — and a routing page that refused to
// render because one of them is missing would be wrong on the majority of them.
func (r *Routing) safeRead(cmd routeros.Cmd) []routeros.Reply {
	rows, err := r.ros.Do(cmd)
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

// RefreshNow re-reads the routes and emits, WITHOUT touching the BGP menus.
//
// This is `refreshNow()` on the Node side, and it is the method the fixture was
// captured with — which is why the capture holds two commands and not five. The
// differential gate drives this, for the same reason.
func (r *Routing) RefreshNow() {
	if !r.ros.Connected() {
		return
	}
	r.loadRoutes()
	r.emitPayload(r.buildPeers())
}

// Tick is the poll body: routes AND the BGP menus.
func (r *Routing) Tick() {
	if !r.ros.Connected() {
		return
	}
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
	if !r.bgpOnly {
		r.loadRoutes()
	}
	r.loadBGP()
	r.emitPayload(r.buildPeers())
}

// BGPOnly stops this collector reading the route tables. See Tick.
//
// A SETTER RATHER THAN A CONSTRUCTOR ARGUMENT, matching how `WithDetailed`,
// `WithGeo` and `WithTable` are done on the other collectors: the page path
// constructs it plainly and only the pool asks for the narrow mode.
func (r *Routing) BGPOnly() *Routing { r.bgpOnly = true; return r }

// loadBGP reads the v7 session menu, falling back to the legacy peer menu.
//
// UNVERIFIED BY ANY FIXTURE — see the package note. None of the three routers
// runs BGP, so what is proven here is the shape of the transform, by the unit
// tests, not that these menus answer as expected on hardware that has them.
func (r *Routing) loadBGP() {
	rows := r.safeRead(routeros.Cmd{
		Path: "/routing/bgp/session/print",
		Args: []string{"=.proplist=name,remote.address,remote.as,local.role,established,uptime," +
			"prefix-count,updates-sent,updates-received,last-notification,hold-time,keepalive-time"},
	})
	if len(rows) == 0 {
		rows = r.safeRead(routeros.Cmd{
			Path: "/routing/bgp/peer/print",
			Args: []string{"=.proplist=name,remote-address,remote-as,state,uptime," +
				"prefix-count,updates-sent,updates-received,last-error,inactive-reason,hold-time,keepalive-time"},
		})
	}
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
			UpdatesSent:   safeInt(s["updates-sent"]),
			UpdatesRecv:   safeInt(s["updates-received"]),
			LastError:     firstNonEmpty(s["last-notification"], s["inactive-reason"], s["last-error"]),
			HoldTime:      safeInt(s["hold-time"]),
			Keepalive:     safeInt(s["keepalive-time"]),
			Flapping:      flapping,
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

func (r *Routing) emitPayload(peers []Peer) {
	all := make([]Route, 0, len(r.order))
	for _, k := range r.order {
		all = append(all, r.routes[k])
	}

	// Only static and dynamic rows reach the page: a connected route is a
	// property of an interface, and the Interfaces page is where it belongs.
	shown := make([]Route, 0, len(all))
	for _, rt := range all {
		if rt.Type == "static" || rt.Type == "dynamic" {
			shown = append(shown, rt)
			if len(shown) == routeCap {
				break
			}
		}
	}

	counts := RouteCounts{Total: len(all)}
	for _, rt := range all {
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

	if peers == nil {
		if r.last != nil {
			peers = r.last.Peers
		} else {
			peers = []Peer{}
		}
	}
	sum := PeerSummary{Total: len(peers)}
	for _, p := range peers {
		if p.State == "established" {
			sum.Established++
		} else {
			sum.Down++
		}
	}

	payload := &RoutingPayload{
		TS: r.now(),
		// Zero on the Node side too: that page is stream-driven there, so there
		// is no poll interval to report.
		PollMs:      0,
		RouteCounts: counts,
		Peers:       peers,
		Routes:      shown,
		Summary:     sum,
	}
	r.last = payload
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
	// `page-home` rather than a card room because these two cards have no
	// entry in `CARD_ROOMS` -- the grid never sends `dashcard:focus` for them, so
	// there is no room to join. It is the same channel `netwatch:update`,
	// `ping:update` and `talkers:update` already use to reach dashboard cards.
	r.emit("page-routing,page-home", "routing:update", payload)
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (r *Routing) SetPollMs(ms int) {
	r.pollMs.set(ms)
	r.loop.retime()
}
