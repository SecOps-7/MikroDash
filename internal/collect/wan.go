package collect

// WAN collector — the uplinks RouterOS considers connected to the internet.
//
//	/interface/detect-internet/state   which interfaces reach the internet
//	/ip/dhcp-client                    lease detail for the ones that have one
//	/ip/route  (dst 0.0.0.0/0)         which uplink is actually carrying traffic
//	/ip/address                        the address each one holds
//	/interface (name,type,running)     physical link or tunnel
//
// THE SET IS RouterOS's, NOT OURS. A WAN here is an interface reporting
// `state=internet`, which is exactly what the Dashboard Network card shows. This
// page adds detail to that set; it does not redefine it. In particular it does
// NOT infer uplinks from default routes — deliberate, because a page that
// disagreed with the card about what counts as a WAN would be worse than one
// that shows nothing.
//
// WHICH MEANS IT SHOWS NOTHING WHEN DETECTION IS OFF, and that is the common
// case: `detect-interface-list` defaults to `none`, so a router nobody has
// configured reports zero rows. `DetectionEnabled` carries that distinction to
// the page, which explains how to switch it on rather than rendering an empty
// table that looks like a fault.
//
// THIS COLLECTOR ONLY READS. Renew and release are separate actions, gated on
// the page and on router:write; no command path here writes anything.

import (
	"encoding/json"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
	"mikrodash/internal/sitedoc"
)

var (
	wanDetectCmd = routeros.Cmd{Path: "/interface/detect-internet/state/print",
		Args: []string{"=.proplist=.id,name,state,state-change-time"}}
	wanDhcpCmd = routeros.Cmd{Path: "/ip/dhcp-client/print", Args: []string{
		"=.proplist=.id,interface,status,address,gateway,primary-dns,secondary-dns," +
			"expires-after,dhcp-server,disabled,invalid"}}
	wanRouteCmd = routeros.Cmd{Path: "/ip/route/print",
		Args: []string{"=.proplist=.id,dst-address,gateway,distance,active,dynamic"}}
	wanAddrCmd = routeros.Cmd{Path: "/ip/address/print",
		Args: []string{"=.proplist=address,interface,disabled"}}
	wanIfaceCmd = routeros.Cmd{Path: "/interface/print",
		Args: []string{"=.proplist=name,type,running"}}
)

// Structure changes when somebody edits the router; only the lease countdown
// and the rates move on a tick, and the rates are borrowed.
const wanConfigEvery = 6

// tunnelTypes — RouterOS reports tunnels as their own interface types.
var tunnelTypes = map[string]bool{
	"wg": true, "wireguard": true, "ipip": true, "gre": true, "eoip": true,
	"l2tp-out": true, "pptp-out": true, "sstp-out": true, "ovpn-out": true,
	"pppoe-out": true, "6to4": true, "ipsec": true,
}

// WANDhcp is the lease detail for an uplink that has one.
type WANDhcp struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	Server       string `json:"server"`
	PrimaryDNS   string `json:"primaryDns"`
	SecondaryDNS string `json:"secondaryDns"`
	ExpiresAfter string `json:"expiresAfter"`
	Invalid      bool   `json:"invalid"`
}

// WANCandidate is one interface the operator could declare an uplink, for the
// picker. Read off the same `/interface/print` the rows are built from, so the
// list costs nothing extra.
type WANCandidate struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Running bool   `json:"running"`
}

// WAN is one uplink.
type WAN struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	IsTunnel bool   `json:"isTunnel"`
	// State is what `/interface/detect-internet` says, and it is EMPTY on a
	// manually declared uplink the router does not consider connected. That is
	// the honest reading: the operator said this carries the internet, and the
	// router has not agreed.
	State string `json:"state"`
	// Manual marks a row that is here because the operator said so.
	Manual bool `json:"manual"`
	// The router's own words. Rendered as a duration by the page, which knows
	// the display timezone; converting here would bake in the server's.
	Since         string `json:"since"`
	Running       *bool  `json:"running"`
	Address       string `json:"address"`
	IsPublic      *bool  `json:"isPublic"`
	Gateway       string `json:"gateway"`
	RouteDistance string `json:"routeDistance"`
	// Only one route per distance is active; this is what tells an operator
	// which uplink is carrying traffic rather than merely standing by.
	RouteActive     bool `json:"routeActive"`
	HasDefaultRoute bool `json:"hasDefaultRoute"`
	// null, never 0: "the router did not report this" and "this uplink is idle"
	// must stay tellable apart, or the page shows a confident 0 Mbps on a
	// saturated link during the startup window.
	RxMbps  *float64 `json:"rxMbps"`
	TxMbps  *float64 `json:"txMbps"`
	RxBytes *float64 `json:"rxBytes"`
	TxBytes *float64 `json:"txBytes"`
	Dhcp    *WANDhcp `json:"dhcp"`
}

// WANPayload is the wan:update body.
type WANPayload struct {
	TS               int64  `json:"ts"`
	PollMs           int    `json:"pollMs"`
	Wans             []WAN  `json:"wans"`
	RatesAvailable   bool   `json:"ratesAvailable"`
	ActiveDefaultWan string `json:"activeDefaultWan"`
	// The one address worth showing at the top: a real public one if any uplink
	// holds it, rather than the first tunnel /32 that happens to sort first.
	PublicIP string `json:"publicIp"`
	// Zero rows means detection is switched off far more often than it means the
	// router is offline. The page says which rather than showing an empty table.
	DetectionEnabled bool `json:"detectionEnabled"`
	Available        bool `json:"available"`
	Denied           bool `json:"denied"`
	// UplinkSource is "detect" when the set is RouterOS's, "manual" when it is
	// the operator's. See BuildWanRows.
	UplinkSource string `json:"uplinkSource"`
	// ManualNames is the declared list, WHETHER OR NOT it is in force: the page
	// edits it while the toggle is off, and throwing it away on every switch back
	// to detection would make the toggle destructive.
	ManualNames []string `json:"manualNames"`
	// Interfaces is every interface this router has, for the picker.
	Interfaces []WANCandidate `json:"interfaces"`
}

// isPublicV4 reports whether an address is routable on the public internet.
//
// Only IPv4 is judged. The point is to tell an operator "this is your real
// public address" apart from a CGNAT or tunnel address that looks like one, so
// a wrong answer is worse than no answer — anything unrecognised returns nil
// rather than guessing.
func isPublicV4(cidr string) *bool {
	host, _, _ := strings.Cut(cidr, "/")
	ip, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil || !ip.Is4() {
		return nil
	}
	b := ip.As4()
	a, c := int(b[0]), int(b[1])
	no, yes := false, true
	switch {
	case a == 10, a == 127, a == 0:
		return &no
	case a == 192 && c == 168:
		return &no
	case a == 172 && c >= 16 && c <= 31:
		return &no
	case a == 169 && c == 254: // link-local
		return &no
	case a == 100 && c >= 64 && c <= 127: // CGNAT — looks public, is not
		return &no
	case a >= 224: // multicast and above
		return &no
	}
	return &yes
}

// BuildWanRows joins the five tables. Pure and exported so every join here is
// testable without a router.
//
// ── `manual` DECIDES WHICH INTERFACES ARE UPLINKS, AND NOTHING ELSE ─────────
//
// Everything below the set — the address, the lease, which default route is
// carrying traffic, the rates — is the same join either way. Only the question
// "which interfaces am I joining for" has two answers, and this is the one place
// it is asked. A second code path would have been a second set of join rules to
// keep in step.
//
// WHY THE OPERATOR IS ALLOWED TO OVERRULE ROUTEROS. `detect-interface-list`
// defaults to `none`, so the honest answer on a router nobody has configured is
// zero uplinks — and the page then explains how to switch detection on, which
// is a change to the ROUTER for the sake of a dashboard. Some operators will
// not make it, and some routers answer wrongly: a tunnel that reaches the
// internet through another uplink reports `internet` too. Saying so here costs
// nothing and is undone by a toggle.
func BuildWanRows(detectRows, dhcpRows, routeRows, addrRows, ifaceRows []routeros.Reply,
	rates RateSource, manual []string) WANPayload {

	var byName map[string]Rate
	ratesAvailable := false
	if rates != nil {
		byName, ratesAvailable = rates.Rates()
	}

	meta := map[string]routeros.Reply{}
	for _, i := range ifaceRows {
		if i["name"] != "" {
			meta[i["name"]] = i
		}
	}
	addrByIface := map[string]string{}
	for _, a := range addrRows {
		if a["address"] == "" || a["interface"] == "" || boolOf(a["disabled"]) {
			continue
		}
		if _, seen := addrByIface[a["interface"]]; !seen {
			addrByIface[a["interface"]] = a["address"]
		}
	}
	dhcpByIface := map[string]routeros.Reply{}
	for _, d := range dhcpRows {
		if d["interface"] == "" || boolOf(d["disabled"]) {
			continue
		}
		dhcpByIface[d["interface"]] = d
	}

	// Which default route belongs to which uplink.
	//
	// A route's gateway is the NEXT HOP, never our own address — matching them
	// against each other finds nothing, which is how the first version of this
	// silently reported every uplink as standby. Three shapes, in order:
	//
	//	tunnel       the route points at the interface by name  (gateway=WG-SA)
	//	dhcp uplink  the route points at the lease's gateway
	//	static       the route points at some address inside our own subnet
	var defaults []routeros.Reply
	for _, r := range routeRows {
		if r["dst-address"] == "0.0.0.0/0" {
			defaults = append(defaults, r)
		}
	}
	routeFor := func(name, address, dhcpGw string) routeros.Reply {
		for _, r := range defaults {
			if r["gateway"] == name {
				return r
			}
		}
		if dhcpGw != "" {
			for _, r := range defaults {
				if r["gateway"] == dhcpGw {
					return r
				}
			}
		}
		if address != "" {
			if p, err := netip.ParsePrefix(address); err == nil {
				for _, r := range defaults {
					gw, err := netip.ParseAddr(r["gateway"])
					if err == nil && p.Masked().Contains(gw) {
						return r
					}
				}
			}
		}
		return nil
	}

	// The set, and where it came from.
	detectByName := map[string]routeros.Reply{}
	for _, d := range detectRows {
		if d["name"] != "" {
			detectByName[d["name"]] = d
		}
	}
	type pick struct {
		name   string
		manual bool
	}
	picks := []pick{}
	if len(manual) > 0 {
		seen := map[string]bool{}
		for _, n := range manual {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			picks = append(picks, pick{n, true})
		}
	} else {
		for _, d := range detectRows {
			if d["name"] != "" && d["state"] == "internet" {
				picks = append(picks, pick{d["name"], false})
			}
		}
	}

	wans := []WAN{}
	for _, p := range picks {
		name := p.name
		d := detectByName[name]
		m := meta[name]
		typ := m["type"]
		address := addrByIface[name]
		dhcp := dhcpByIface[name]
		// A tunnel's own /32 is not the uplink's gateway; the DHCP client's is
		// authoritative when there is one, and the default route's otherwise.
		route := routeFor(name, address, dhcp["gateway"])

		w := WAN{
			Name: name, Type: typ, IsTunnel: tunnelTypes[typ],
			// The router's own opinion is still reported on a manual row, and
			// the difference is worth seeing: a declared uplink RouterOS does
			// not call `internet` is either a detection list nobody set or a
			// link that is genuinely down.
			State: d["state"], Since: d["state-change-time"],
			Manual: p.manual, Address: address,
		}
		if raw, present := m["running"]; present {
			b := boolOf(raw)
			w.Running = &b
		}
		if address != "" {
			w.IsPublic = isPublicV4(address)
		}
		switch {
		case dhcp["gateway"] != "":
			w.Gateway = dhcp["gateway"]
		case route != nil:
			w.Gateway = route["gateway"]
		}
		if route != nil {
			w.RouteDistance = route["distance"]
			w.RouteActive = boolOf(route["active"])
			w.HasDefaultRoute = true
		}
		if live, ok := byName[name]; ok {
			w.RxMbps, w.TxMbps = live.RxMbps, live.TxMbps
		}
		if dhcp != nil {
			w.Dhcp = &WANDhcp{
				ID: dhcp[".id"], Status: dhcp["status"], Server: dhcp["dhcp-server"],
				PrimaryDNS: dhcp["primary-dns"], SecondaryDNS: dhcp["secondary-dns"],
				ExpiresAfter: dhcp["expires-after"], Invalid: boolOf(dhcp["invalid"]),
			}
		}
		wans = append(wans, w)
	}

	// Active first, then by route distance, then by name — the order an operator
	// reads them in: what is carrying traffic now, then what would take over.
	sort.SliceStable(wans, func(i, j int) bool {
		a, b := wans[i], wans[j]
		if a.RouteActive != b.RouteActive {
			return a.RouteActive
		}
		da, db := distanceOr99(a.RouteDistance), distanceOr99(b.RouteDistance)
		if da != db {
			return da < db
		}
		return Collate(a.Name, b.Name) < 0
	})

	out := WANPayload{Wans: wans, RatesAvailable: ratesAvailable,
		UplinkSource: "detect", ManualNames: []string{},
		Interfaces: []WANCandidate{}}
	if len(manual) > 0 {
		out.UplinkSource = "manual"
		out.ManualNames = append(out.ManualNames, manual...)
	}
	for _, i := range ifaceRows {
		if i["name"] == "" {
			continue
		}
		out.Interfaces = append(out.Interfaces, WANCandidate{
			Name: i["name"], Type: i["type"], Running: boolOf(i["running"]),
		})
	}
	sort.SliceStable(out.Interfaces, func(a, b int) bool {
		return Collate(out.Interfaces[a].Name, out.Interfaces[b].Name) < 0
	})
	for _, w := range wans {
		if w.RouteActive {
			out.ActiveDefaultWan = w.Name
			break
		}
	}
	for _, w := range wans {
		if w.IsPublic != nil && *w.IsPublic {
			out.PublicIP = w.Address
			break
		}
	}
	return out
}

// distanceOr99 reproduces `Number(a.routeDistance || 99)`: an empty distance
// sorts last, and an unparseable one becomes NaN, which loses every comparison
// in JavaScript. NaN has no Go equivalent in a sort, so it takes the same place
// an empty one does.
func distanceOr99(s string) float64 {
	if s == "" {
		return 99
	}
	f, ok := parseJSNumber(s)
	if !ok {
		return math.NaN()
	}
	return f
}

// Wan is the collector.
type Wan struct {
	ros    Reader
	emit   Emit
	rates  RateSource
	pollMs *pollInterval
	// cache coalesces reads shared with another collector; see collect/cache.go.
	cache *roscache.Cache
	// See scheduled.go: subscribes to detect-internet, the live uplink state.
	sched scheduled

	poll *pollLoop

	mu     sync.Mutex
	ifaces []routeros.Reply
	dhcp   []routeros.Reply
	addrs  []routeros.Reply
	ticks  int
	lastFp string
	// nil = unprobed, false = this router has no such menu, stop asking.
	detectAvailable *bool
	denied          bool
	// docs reads the operator's uplink list. Nil outside a live session.
	docs DocSource
	// manual is that list as of the last slow lane. Re-read there rather than
	// every tick: it changes when somebody presses a button, and the button's
	// handler calls RefreshNow.
	manual []string

	last    *WANPayload
	lastErr string
}

// NewWan builds the collector. `rates` may be nil — `requires` is empty on the
// Node side for the same reason, so switching Interface Rates off degrades the
// rate column rather than blanking the page.
func NewWan(ros Reader, emit Emit, rates RateSource, pollMs int) *Wan {
	w := &Wan{ros: ros, emit: emit, rates: rates, pollMs: newPollInterval(clampPoll(pollMs, 10000, 2000, 60000))}
	w.poll = newPollLoop(func() { w.Tick() }, func() time.Duration {
		return w.pollMs.duration()
	})
	// AFTER the loop: `scheduled` holds it as the no-cache fallback.
	w.sched = scheduled{loop: w.poll, menu: wanDetectCmd.Path, fields: fieldsOf(wanDetectCmd), apply: w.apply,
		cadence: w.pollMs.duration}
	return w
}

// WithDocs attaches the operator's own uplink list. Set once, before Start.
func (w *Wan) WithDocs(d DocSource) *Wan {
	w.docs = d
	return w
}

// readManual re-reads the declared uplinks. Called from the slow lane only.
func (w *Wan) readManual() {
	w.manual = sitedoc.CleanWANUplinks(w.docs.Doc(sitedoc.KindWANUplinks)).Manual()
}

// read latches separately on "absent" and "denied", because the page says
// different things about them: a menu this build does not have is not the same
// as one this API user may not see.
func (w *Wan) read(cmd routeros.Cmd, avail **bool) []routeros.Reply {
	if avail != nil && *avail != nil && !**avail {
		return nil
	}
	// THROUGH THE CACHE. Every menu this collector reads is shared:
	// /interface/detect-internet/state with dhcpNetworks, /ip/route with
	// routing, /ip/address with ifStatus and dhcpNetworks, and
	// /ip/dhcp-client, which is this collector's alone and therefore
	// unaffected. The routing is in the helper because the availability latch
	// below has to keep working whichever collector paid for the read.
	rows, err := readVia(w.cache, w.ros, cmd, w.pollMs.duration())
	if err != nil {
		switch {
		case menuAbsent(err):
			if avail != nil {
				no := false
				*avail = &no
			}
		case menuDenied(err):
			if avail != nil {
				no := false
				*avail = &no
			}
			w.denied = true
		default:
			w.lastErr = err.Error()
		}
		return nil
	}
	if avail != nil {
		yes := true
		*avail = &yes
	}
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// Tick reads what is due this cycle and emits when something changed.
func (w *Wan) Tick() {
	if !w.ros.Connected() {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ticks%wanConfigEvery == 0 {
		// THROUGH THE CACHE: ifStatus reads this menu with a superset of our
		// proplist, so whichever gets there first pays and the other does not.
		w.ifaces, _ = readVia(w.cache, w.ros, wanIfaceCmd, w.pollMs.duration())
		w.dhcp = w.read(wanDhcpCmd, nil)
		w.addrs = w.read(wanAddrCmd, nil)
		w.readManual()
	}
	w.ticks++

	w.applyLocked(w.read(wanDetectCmd, &w.detectAvailable))
}

// apply is what the scheduler calls with the detect-internet state, which is
// this collector's live menu: it is what says whether an uplink is actually
// carrying traffic. The routes are read alongside it, and the interface, DHCP
// and address config keeps its slow lane -- see scheduled.go on why a collector
// subscribes to ONE menu.
func (w *Wan) apply(detect []routeros.Reply, err error) {
	if !w.ros.Connected() {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if err != nil {
		// The latch `read` would have set, derived from what the scheduler hands
		// over: a router without the detect-internet menu must stop being asked
		// on this path too.
		if menuMissing(err) {
			no := false
			w.detectAvailable = &no
		}
		return
	}
	if w.detectAvailable == nil {
		yes := true
		w.detectAvailable = &yes
	}

	if w.ticks%wanConfigEvery == 0 {
		w.ifaces, _ = readVia(w.cache, w.ros, wanIfaceCmd, w.pollMs.duration())
		w.dhcp = w.read(wanDhcpCmd, nil)
		w.addrs = w.read(wanAddrCmd, nil)
		w.readManual()
	}
	w.ticks++
	w.applyLocked(detect)
}

// applyLocked builds and emits. The caller holds the lock.
func (w *Wan) applyLocked(detect []routeros.Reply) {
	routes := w.read(wanRouteCmd, nil)

	built := BuildWanRows(detect, w.dhcp, routes, w.addrs, w.ifaces, w.rates, w.manual)
	built.TS = time.Now().UnixMilli()
	built.PollMs = w.pollMs.ms()
	built.DetectionEnabled = len(detect) > 0
	built.Available = MenuAvailable(w.detectAvailable)
	built.Denied = w.denied
	w.last = &built

	// Byte totals are excluded: they move every tick on a live uplink and would
	// defeat the dirty check on their own. Rates are included, rounded, because
	// they are what changes visibly.
	type fpRow struct {
		N, A, G string
		RA      bool
		RD      string
		Run     *bool
		Rx, Tx  int
		DS, DE  string
	}
	rows := make([]fpRow, 0, len(built.Wans))
	for _, x := range built.Wans {
		r := fpRow{N: x.Name, A: x.Address, G: x.Gateway, RA: x.RouteActive,
			RD: x.RouteDistance, Run: x.Running}
		if x.RxMbps != nil {
			r.Rx = int(math.Round(*x.RxMbps * 10))
		}
		if x.TxMbps != nil {
			r.Tx = int(math.Round(*x.TxMbps * 10))
		}
		if x.Dhcp != nil {
			r.DS, r.DE = x.Dhcp.Status, x.Dhcp.ExpiresAfter
		}
		rows = append(rows, r)
	}
	fp, _ := json.Marshal(struct {
		W []fpRow  `json:"w"`
		D bool     `json:"d"`
		R bool     `json:"r"`
		S string   `json:"s"`
		M []string `json:"m"`
	}{rows, built.DetectionEnabled, built.RatesAvailable,
		built.UplinkSource, built.ManualNames})
	if string(fp) == w.lastFp {
		return
	}
	w.lastFp = string(fp)
	EvWanUpdate.Emit(w.emit, wanRooms.Join(), built)
}

// Last is the most recent payload, replayed on page:focus.
func (w *Wan) Last() *WANPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// RefreshNow re-reads immediately, after an action.
func (w *Wan) RefreshNow() {
	if !w.ros.Connected() {
		return
	}
	w.mu.Lock()
	w.ticks = 0
	w.mu.Unlock()
	w.Tick()
}

func (w *Wan) Start() {
	if w.ros.Connected() && !w.sched.scheduling() {
		w.Tick()
	}
	w.sched.begin()
}

func (w *Wan) Reconnected() {
	w.sched.end()
	w.mu.Lock()
	w.lastFp = ""
	w.ticks = 0
	w.detectAvailable = nil
	w.denied = false
	w.mu.Unlock()
	if !w.sched.scheduling() {
		w.Tick()
	}
	w.sched.begin()
}

func (w *Wan) Suspend() { w.sched.end() }

func (w *Wan) Resume() {
	if w.ros.Connected() {
		w.sched.begin()
	}
}

func (w *Wan) Stop() {
	w.sched.end()
	w.mu.Lock()
	w.lastFp = ""
	w.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (w *Wan) SetPollMs(ms int) {
	w.pollMs.set(ms)
	w.poll.retime()
}

// UseCache feeds BOTH halves: the 1.4 shared-read cache and the subscription.
// Same cache, two uses.
func (w *Wan) UseCache(c *roscache.Cache) {
	w.cache = c
	w.sched.useCache(c)
}
