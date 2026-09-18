package collect

// Wifi Clients collector.
//
//	/interface/wifi/registration-table      who is associated, modern stack
//	/interface/wireless/registration-table  the same, legacy stack
//	/caps-man/registration-table            clients on CAPsMAN-managed radios
//	/interface/wifi|wireless/print          the SSIDs this router BROADCASTS
//
// THREE STACKS, ONE LATCHED MODE. A router answers exactly one of the first two,
// and CAPsMAN can run alongside either. The mode is latched on the first stack
// that answers so the port does not ask a router every tick about a menu it has
// already said it does not have.
//
// THE SSID LIST IS READ FROM THE INTERFACES, NOT FROM THE CLIENTS. An SSID with
// nobody on it is still an SSID, and the registration table only knows about
// networks somebody happens to be using.

import (
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/roscache"
	"mikrodash/internal/routeros"
	"mikrodash/internal/wifiscan"
)

// The four legacy CAPsMAN menus this collector reads are DECLARED IN capsman.go,
// not here. `capsV1RegCmd` was declared in this file until the band join needed
// three more of them, and two files declaring the same menu is how a proplist
// drifts apart from itself — the modern profile menus have sat in wifi.go and
// been read from capsman.go for the same reason.
var (
	wlRegWifiCmd   = routeros.Cmd{Path: "/interface/wifi/registration-table/print"}
	wlRegLegacyCmd = routeros.Cmd{Path: "/interface/wireless/registration-table/print"}
	wlIfaceWifiCmd = routeros.Cmd{Path: "/interface/wifi/print"}
	wlIfaceLegacy  = routeros.Cmd{Path: "/interface/wireless/print"}
)

// WirelessClient is one associated station.
type WirelessClient struct {
	MAC    string `json:"mac"`
	Signal int    `json:"signal"`
	Iface  string `json:"iface"`
	TxRate string `json:"txRate"`
	Band   string `json:"band"`
	// Standard is the 802.11 generation this client NEGOTIATED, which is not the
	// same question as what the radio supports. See WifiStandard.
	Standard string `json:"standard"`
	IP       string `json:"ip"`
	RxRate   string `json:"rxRate"`
	Uptime   string `json:"uptime"`
	SSID     string `json:"ssid"`
	Name     string `json:"name"`
	// Comment is what the operator wrote against this MAC on the DHCP server.
	//
	// A SECOND STRING BECAUSE IT ANSWERS A SECOND QUESTION. `Name` is what the
	// device calls itself — a lease hostname, or a PTR record when there is no
	// lease — while the comment is what somebody decided to call it. The Wi-Fi
	// map offers them as separate lines for that reason, and a device whose
	// hostname is `android-4f2c` is the case the comment exists for.
	Comment string `json:"comment"`
	// Source marks a CAPsMAN row. Absent on local clients, because the live
	// payload omits it there rather than sending an empty string.
	Source string `json:"source,omitempty"`
}

// WirelessSSID is one broadcast network, aggregated across the interfaces
// carrying it.
type WirelessSSID struct {
	SSID   string   `json:"ssid"`
	Ifaces []string `json:"ifaces"`
	Bands  []string `json:"bands"`
	// Disabled only when EVERY interface carrying it is: one radio broadcasting
	// the network is enough for the network to be up.
	Disabled bool `json:"disabled"`
	// Running is the honest answer to "is this on the air right now" — an
	// interface can be enabled and still not running.
	Running bool `json:"running"`
	Clients int  `json:"clients"`
}

type WirelessPayload struct {
	TS               int64            `json:"ts"`
	Clients          []WirelessClient `json:"clients"`
	Mode             string           `json:"mode"`
	PollMs           int              `json:"pollMs"`
	CapsmanAvailable bool             `json:"capsmanAvailable"`
	SSIDs            []WirelessSSID   `json:"ssids"`
	// How many radios take their SSID from a CAPsMAN manager instead of from
	// here — so the card can say so rather than rendering an empty list that
	// looks like a failure.
	SSIDsManagedElsewhere int `json:"ssidsManagedElsewhere"`
}

// wifiGenRank maps one 802.11 token to a Wi-Fi Alliance generation number.
//
// Zero means "older than the Alliance ever named". 802.11a/b/g predate the
// generation numbering and calling them Wi-Fi 1/2/3 would be inventing a label
// nobody prints on a box — the Alliance applied the scheme from 4 onward and
// only ever marketed 4, 5, 6, 6E and 7.
var wifiGenRank = map[string]int{
	"b": 0, "a": 0, "g": 0,
	"n": 4, "an": 4,
	"ac": 5,
	"ax": 6,
	"be": 7,
}

// WifiStandard is the generation a client negotiated, from a RouterOS band.
//
// ── THE VALUE IS NOT A SUFFIX, WHICH IS THE TRAP ────────────────────────────
//
// The obvious reading — take everything after the dash — is wrong twice over,
// and both forms are real. Checked against MikroTik's documentation rather than
// against the one router to hand, because a fixture proves what one radio
// answered and the docs say what a radio MAY answer:
//
//	modern (/interface/wifi/channel band):
//	  2ghz-g 2ghz-n 2ghz-ax 2ghz-be 5ghz-a 5ghz-ac 5ghz-an 5ghz-ax
//	  5ghz-be 6ghz-ax 6ghz-be
//	legacy (/interface/wireless):
//	  2ghz-b/g/n, 5ghz-a/n/ac, 2ghz-onlyn, 5ghz-onlyac
//
// So `5ghz-an` is ONE token meaning a/n, and `5ghz-a/n/ac` is THREE. A suffix
// test also fails on the pair it matters most for: "an" ends in "n" and would
// read as plain 802.11n, which is the same answer by luck, while "be" and "ac"
// share no letters with anything and would be fine. Luck is not a reason.
//
// THE HIGHEST TOKEN WINS. A slash list is what the radio offers, and a client in
// it negotiated the best both ends support — reporting the lowest would label a
// Wi-Fi 5 client as Wi-Fi 4 on every legacy AP.
//
// An empty result is normal and must stay renderable: a CAPsMAN row carries no
// band at all, which is why wlBandOf falls back to the interface name.
func WifiStandard(rawBand string) string {
	s := strings.ToLower(strings.TrimSpace(rawBand))
	if s == "" {
		return ""
	}
	prefix, rest, found := strings.Cut(s, "-")
	if !found {
		return ""
	}
	best := -1
	for _, tok := range strings.Split(rest, "/") {
		tok = strings.TrimPrefix(strings.TrimSpace(tok), "only")
		if rank, ok := wifiGenRank[tok]; ok && rank > best {
			best = rank
		}
	}
	switch {
	case best < 0:
		// A token nothing recognises. Saying nothing beats guessing a
		// generation, because the pill is read as a fact about the client.
		return ""
	case best == 0:
		return "Legacy"
	case best == 6 && strings.HasPrefix(prefix, "6"):
		// 6E IS 802.11ax ON 6 GHZ and nothing else — the band is half the
		// answer, so this is the one case the prefix is consulted for. Wi-Fi 7
		// on 6 GHz stays "Wi-Fi 7"; there is no "7E".
		return "Wi-Fi 6E"
	}
	return "Wi-Fi " + strconv.Itoa(best)
}

// wlBandOf reads the band a client is on.
//
// A CAPsMAN row carries no `band` at all, so the interface NAME is the only
// signal — which is why the fixture rules keep interface names un-anonymised.
func wlBandOf(row routeros.Reply, iface string, capsman bool) string {
	raw := strings.ToLower(row["band"])
	if capsman && raw == "" {
		il := strings.ToLower(iface)
		switch {
		case strings.HasSuffix(il, "-2g") || strings.Contains(il, "2ghz"):
			return "2.4GHz"
		case strings.HasSuffix(il, "-5g") || strings.Contains(il, "5ghz"):
			return "5GHz"
		case strings.HasSuffix(il, "-6g") || strings.Contains(il, "6ghz"):
			return "6GHz"
		}
		return ""
	}
	switch {
	case strings.Contains(raw, "6"):
		return "6GHz"
	case strings.Contains(raw, "5"):
		return "5GHz"
	case strings.Contains(raw, "2"):
		return "2.4GHz"
	}
	return ""
}

// parseWirelessClient normalises one registration row.
//
// The field names differ per stack — `signal` on modern wifi, `signal-strength`
// on the legacy one, `rx-signal` on CAPsMAN — so each is tried in turn rather
// than branching on the mode, which would have to be right in three places.
func parseWirelessClient(row routeros.Reply, capsman bool, ip, name string, cb CapsBand) WirelessClient {
	mac := firstNonEmptyStr(row["mac-address"], row["mac"])
	signal := 0
	if v := jsParseInt(firstNonEmptyStr(row["signal"], row["signal-strength"], row["rx-signal"], "0")); v != nil {
		signal = *v
	}
	iface := firstNonEmptyStr(row["interface"], row["ap-interface"])
	c := WirelessClient{
		MAC:      mac,
		Signal:   signal,
		Iface:    iface,
		TxRate:   firstNonEmptyStr(row["tx-rate"], row["tx-rate-set"]),
		Band:     wlBandOf(row, iface, capsman),
		Standard: WifiStandard(row["band"]),
		IP:       ip,
		RxRate:   row["rx-rate"],
		Uptime:   row["uptime"],
		SSID:     row["ssid"],
		Name:     name,
	}
	if capsman {
		c.Source = "capsman"
		// ── WHERE A LEGACY CAPsMAN CLIENT'S BAND COMES FROM ────────────────
		//
		// Not from the row: `/caps-man/registration-table` carries no band, and
		// the interface-name fallback in wlBandOf is no use on v1 either,
		// because `name-format=identity` names an interface after the access
		// point. So the manager's own configuration answers it — see
		// CapsLegacyBands — and the page's Band and Standard columns stopped
		// being empty for every client on a v1 CAP.
		//
		// The generation follows the CHANNEL's band list, which is the same
		// trade WifiStandard already documents for a slash list: the highest
		// token wins, because a client on a radio offering `5ghz-n/ac`
		// negotiated the best both ends support.
		if c.Band == "" {
			c.Band = firstNonEmpty(BandLabel(cb.Raw), BandFromFrequency(cb.Frequency))
		}
		if c.Standard == "" {
			c.Standard = WifiStandard(cb.Raw)
		}
	}
	return c
}

// isWirelessRow drops interface METADATA rows.
//
// Some RouterOS builds answer the registration table with rows describing
// interfaces — including Ethernet ones — which have none of these fields. They
// are not clients and counting them would inflate every SSID.
func isWirelessRow(row routeros.Reply) bool {
	for _, k := range []string{"signal", "signal-strength", "rx-signal", "ssid",
		"tx-rate", "rx-rate", "tx-rate-set"} {
		if row[k] != "" {
			return true
		}
	}
	return false
}

// parseWirelessSSIDs reads the broadcast networks off the interface list.
//
// ONLY name, SSID and state are read. The same rows carry
// `security.passphrase` in clear text, and none of it has any business leaving
// this function — the payload goes to every browser on the page.
func parseWirelessSSIDs(rows []routeros.Reply) ([]WirelessSSID, int) {
	byName := map[string]*WirelessSSID{}
	order := []string{}
	managedElsewhere := 0

	for _, r := range rows {
		// A CAP takes its configuration from the manager, so it genuinely has no
		// local SSID to report. Counting these lets the card say so.
		if r["configuration.manager"] != "" {
			managedElsewhere++
			continue
		}
		ssid := strings.TrimSpace(firstNonEmptyStr(r["configuration.ssid"], r["ssid"]))
		if ssid == "" {
			continue
		}
		iface := strings.TrimSpace(r["name"])
		disabled := r["disabled"] == "true"
		running := r["running"] == "true"

		e := byName[ssid]
		if e == nil {
			e = &WirelessSSID{SSID: ssid, Ifaces: []string{}, Bands: []string{}, Disabled: true}
			byName[ssid] = e
			order = append(order, ssid)
		}
		if iface != "" && !containsString(e.Ifaces, iface) {
			e.Ifaces = append(e.Ifaces, iface)
		}
		if !disabled {
			e.Disabled = false
		}
		if running {
			e.Running = true
		}
	}

	out := make([]WirelessSSID, 0, len(order))
	for _, ssid := range order {
		out = append(out, *byName[ssid])
	}
	sort.SliceStable(out, func(i, j int) bool { return Collate(out[i].SSID, out[j].SSID) < 0 })
	return out, managedElsewhere
}

// withClientStats fills in bands and client counts from the live registration
// table.
//
// KEPT APART FROM THE SSID PARSE because the two run on different clocks: the
// SSID list is configuration, re-read every few minutes, while who is connected
// changes constantly. Folding the second into the first froze bands and counts
// at whatever the client table held during that refresh — and at startup the
// refresh completes BEFORE the first client batch, so every SSID was published
// with no bands and a count of zero and stayed that way for the whole cycle.
//
// Returns COPIES: the cached list is configuration truth and is reused on every
// emit, so counting into it in place would accumulate.
func withClientStats(ssids []WirelessSSID, clients []WirelessClient) []WirelessSSID {
	out := make([]WirelessSSID, 0, len(ssids))
	for _, s := range ssids {
		c := s
		c.Bands = []string{}
		c.Clients = 0
		out = append(out, c)
	}
	byIface := map[string]int{}
	bySSID := map[string]int{}
	for i := range out {
		bySSID[out[i].SSID] = i
		for _, iface := range out[i].Ifaces {
			byIface[iface] = i
		}
	}

	for _, c := range clients {
		// INTERFACE FIRST: that is what the association is keyed on, and the one
		// field the registration table is certain to carry. Matching on the
		// client's own ssid field alone means a build that does not report one
		// reads as zero everywhere — indistinguishable from an idle network. The
		// name match stays as the fallback, for the legacy stack and for CAPsMAN
		// rows naming an interface this router does not own.
		idx, ok := byIface[c.Iface]
		if !ok {
			idx, ok = bySSID[c.SSID]
		}
		if !ok {
			continue
		}
		out[idx].Clients++
		if c.Band != "" && !containsString(out[idx].Bands, c.Band) {
			out[idx].Bands = append(out[idx].Bands, c.Band)
		}
	}

	for i := range out {
		sort.Strings(out[i].Bands)
	}
	return out
}

// Wireless is the collector.
type Wireless struct {
	ros    Reader
	emit   Emit
	pollMs *pollInterval
	leases LeaseSource
	// arp is the MAC->IP join. A registration row carries a MAC and never an
	// address, so without this the `ip` field is empty for every client — which
	// is what it was until 2026-09-10.
	arp ARPByMAC
	// ptr is the LAST fallback for a name: reverse DNS on the address ARP found.
	// It is what names a device with a static address and no DHCP lease.
	ptr NameByIP

	// cache coalesces reads shared with another collector; nil outside a live
	// session, which is every test. See collect/cache.go.
	cache *roscache.Cache

	mu     sync.Mutex
	mode   string // wifi | wireless | none, latched on the first stack that answers
	capsOK bool
	// probedCaps is false until one tick has completed — see Tick, where the
	// first payload deliberately carries no CAPsMAN answer.
	probedCaps       bool
	ssids            []WirelessSSID
	managedElsewhere int
	last             *WirelessPayload
	lastFp           string

	// ssidEndpoint latches the interface menu that answered. An empty string
	// means "not probed yet"; `wlNoStack` means neither exists here, and the
	// collector stops asking.
	ssidEndpoint string

	// scanIfaces is the Frequency Analyser's interface catalogue.
	//
	// ── IT LIVES HERE BECAUSE THIS IS THE COLLECTOR THE PAGE RUNS ─────────
	//
	// It was in `Wifi` until 2026-08-29, which put it one page away from every
	// caller: `faOpenBtn` is on the WIRELESS page, `ws.go`'s focus switch
	// resumes the WIRELESS collector for that page, and the catalogue was in the
	// one resumed by `case "wifi"`. So a session that went straight to the page
	// the button is on found an empty catalogue and no button — measured on the
	// hAP AX3, where visiting `wifi` first was what made it appear.
	//
	// Live has it here for the same reason: `listScannableInterfaces` is
	// `src/collectors/wireless.js:391`, not `wifi.js`.
	//
	// AND IT COSTS NO EXTRA ROUTER CHANNEL. `refreshSSIDs` already issues
	// `/interface/wifi/print` with no proplist, so these are rows this collector
	// has in hand; building the catalogue from them adds no command. That is
	// what made moving it the right fix rather than resuming a second collector
	// on wireless focus — `CLAUDE.md`: "more efficient means fewer router
	// channels".
	scanIfaces []wifiscan.Catalogue

	loop  *pollLoop
	sched scheduled
}

const wlNoStack = "-"

func NewWireless(ros Reader, emit Emit, leases LeaseSource, pollMs int) *Wireless {
	w := &Wireless{
		ros: ros, emit: emit, leases: leases,
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 2000, 600000)),
		ssids:  []WirelessSSID{},
	}
	w.loop = newPollLoop(func() { w.Tick() }, func() time.Duration {
		return w.pollMs.duration()
	})
	// MECHANISM B. The registration table it wants depends on which stack the
	// router runs, and that is latched by the first Tick -- see alignSubscription.
	// The modern menu is the starting guess because it is the probe order's first
	// for the same reason: on RouterOS 7.2x every board in this fleet answered it.
	//
	// fields nil: the registration table has no proplist of its own; both stacks
	// are read whole because the field NAMES differ between them.
	w.sched = scheduled{
		loop: w.loop, menu: wlRegWifiCmd.Path, fields: fieldsOf(wlRegWifiCmd),
		cadence: w.pollMs.duration, apply: w.applyTick,
	}
	return w
}

// applyTick is the scheduled path's tick.
//
// The delivered rows are not used: Tick re-reads the same menu through the
// cache, which the scheduler has just refreshed, so it costs nothing and the
// probe-and-latch rules stay in ONE place instead of being restated here.
func (w *Wireless) applyTick([]routeros.Reply, error) { w.Tick() }

// alignSubscription points the subscription at the stack this router has.
//
// Without it a legacy router would have the scheduler reading
// `/interface/wifi/registration-table` forever, once per poll, and getting a
// refusal every time.
//
// A latched "none" keeps the modern menu: both are absent, Tick re-probes both
// anyway, and inventing a third answer here would only make the demand set
// disagree with what is actually read.
func (w *Wireless) alignSubscription(mode string) {
	menu := wlRegWifiCmd
	if mode == "wireless" {
		menu = wlRegLegacyCmd
	}
	w.sched.resubscribe(menu.Path, w.applyTick)
}

func (w *Wireless) Suspend() { w.sched.end() }

func (w *Wireless) Resume() {
	if w.ros.Connected() {
		w.sched.begin()
	}
}

func (w *Wireless) Start() {
	w.Tick()
	w.sched.begin()
}

func (w *Wireless) Stop() { w.sched.end() }

// Reconnected drops every latch: the usual reason a connection dropped is an
// upgrade, and the router that came back may run a different wireless stack.
func (w *Wireless) Reconnected() {
	w.sched.end()
	w.mu.Lock()
	w.mode, w.ssidEndpoint, w.capsOK = "", "", false
	w.probedCaps = false
	w.probedCaps = false
	w.ssids = []WirelessSSID{}
	w.managedElsewhere = 0
	w.lastFp = ""
	w.mu.Unlock()
	// The live `_reset` clears the PTR cache on a reconnect, and the reason is
	// the router may have come back after a DHCP sweep — addresses move, and a
	// name cached against the old one names the wrong device.
	if w.ptr != nil {
		if c, ok := w.ptr.(*PTRCache); ok {
			c.Reset()
		}
	}
	w.Tick()
	// sched.begin, NOT the raw loop: this collector is scheduled, and
	// scheduled.end stops the loop only when there is no cache. Started
	// directly, the loop outlived every Suspend and Stop after a reconnect and
	// polled for the life of the process (review 2026-09-19). begin starts the
	// loop itself when there is no cache, so the polled path is unchanged.
	w.sched.begin()
}

func (w *Wireless) Last() *WirelessPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// leaseName resolves a client MAC to its DHCP name.
// ipOf is the client's address, which only ARP knows.
func (w *Wireless) ipOf(mac string) string {
	if w.arp == nil {
		return ""
	}
	return w.arp.IPForMAC(mac)
}

// WithARP attaches the MAC→IP join.
func (w *Wireless) WithARP(a ARPByMAC) *Wireless {
	w.arp = a
	return w
}

// WithPTR attaches the reverse-DNS fallback, and subscribes to its answers.
//
// THE CALLBACK IS THE HALF THAT MATTERS. A lookup lands after the tick that
// wanted it, so without this the name would first appear on the NEXT tick —
// which on this collector's cadence is up to five minutes. The live app polls a
// 500ms timer for the same reason; this is told instead of asking.
func (w *Wireless) WithPTR(n NameByIP) *Wireless {
	w.ptr = n
	if c, ok := n.(*PTRCache); ok && c != nil {
		c.OnResolved(w.renameFromPTR)
	}
	return w
}

// nameOf is the client's name: the DHCP lease first, reverse DNS second.
//
// ── THE ORDER IS THE LIVE ONE AND IT IS NOT ARBITRARY ──────────────────────
//
// A lease name is what the operator's own DHCP server was told the device calls
// itself; a PTR record is what somebody put in a zone file, which on most LANs
// is nothing at all. So the lease wins, and this is asked only for what is left.
//
// `WantPTR` is called on a MISS, which is what makes the read side able to
// return immediately: the answer arrives later and `renameFromPTR` delivers it.
func (w *Wireless) nameOf(mac, ip string) string {
	if name := w.leaseName(mac); name != "" {
		return name
	}
	if w.ptr == nil || ip == "" {
		return ""
	}
	if name := w.ptr.PTRName(ip); name != "" {
		return name
	}
	w.ptr.WantPTR(ip)
	return ""
}

// renameFromPTR fills in names that arrived after the payload went out.
//
// ── IT RE-RENDERS RATHER THAN RE-READING THE ROUTER ────────────────────────
//
// Nothing about the router has changed — only what this process knows about an
// address — so re-ticking would cost a registration-table read for a string
// lookup. The last payload is patched and re-emitted, which is what the live
// `tryResolve` does from `_knownClients`.
//
// THE SLICE IS COPIED. The payload already went to the hub and a browser may be
// rendering it; patching in place would mutate what a viewer is holding. Same
// rule `FoldTraffic` records for its ring.
//
// SILENT WHEN NOTHING CHANGED, so a lookup that lands for a client which has
// since left, or whose name was already filled, costs no frame.
func (w *Wireless) renameFromPTR() {
	w.mu.Lock()
	last := w.last
	if last == nil {
		w.mu.Unlock()
		return
	}
	clients := append([]WirelessClient(nil), last.Clients...)
	changed := false
	for i := range clients {
		if clients[i].Name != "" || clients[i].IP == "" {
			continue
		}
		if name := w.ptr.PTRName(clients[i].IP); name != "" {
			clients[i].Name = name
			changed = true
		}
	}
	if !changed {
		w.mu.Unlock()
		return
	}
	next := *last
	next.TS = time.Now().UnixMilli()
	next.Clients = clients
	next.SSIDs = withClientStats(w.ssids, clients)
	w.last = &next
	w.mu.Unlock()

	// OUTSIDE the lock, like every other emit in this file.
	EvWirelessUpdate.Emit(w.emit, wirelessRooms.Join(), next)
}

func (w *Wireless) lease(mac string) *Lease {
	if w.leases == nil {
		return nil
	}
	p := w.leases.Last()
	if p == nil {
		return nil
	}
	for i := range p.Leases {
		if strings.EqualFold(p.Leases[i].MAC, mac) {
			return &p.Leases[i]
		}
	}
	return nil
}

func (w *Wireless) leaseName(mac string) string {
	if l := w.lease(mac); l != nil {
		return firstNonEmptyStr(l.Name, l.HostName)
	}
	return ""
}

func (w *Wireless) leaseComment(mac string) string {
	if l := w.lease(mac); l != nil {
		return l.Comment
	}
	return ""
}

// RefreshNow re-reads the registration tables past the cache, for a reader that
// needs the clients connected now rather than as of the last shared read (the
// assistant's `list_wifi_clients`). The IfStatus pattern: invalidate, then tick.
//
// `Tick` reads through the cache with a TTL of the poll interval, so without the
// invalidation a refresh could hand back a table up to one interval old, and the
// caller would report it as fresh.
func (w *Wireless) RefreshNow() {
	if !w.ros.Connected() {
		return
	}
	if w.cache != nil {
		for _, cmd := range []routeros.Cmd{wlRegWifiCmd, wlRegLegacyCmd, capsV1RegCmd} {
			w.cache.Invalidate(cmd.Path)
		}
	}
	w.Tick()
}

// Tick reads the registration tables and builds the payload.
func (w *Wireless) Tick() {
	if !w.ros.Connected() {
		return
	}

	clients := []WirelessClient{}
	seen := map[string]bool{}

	// The local stack, latched on whichever answers.
	w.mu.Lock()
	mode := w.mode
	w.mu.Unlock()

	add := func(rows []routeros.Reply, capsman bool, bands map[string]CapsBand) {
		for _, row := range rows {
			if !isWirelessRow(row) {
				continue
			}
			mac := firstNonEmptyStr(row["mac-address"], row["mac"])
			if mac == "" || seen[mac] {
				continue
			}
			seen[mac] = true
			// THE ADDRESS COMES FROM ARP AND FROM NOWHERE ELSE. A `""` sat
			// here from the port until 2026-09-10 and the WiFi Clients page's
			// address line — `wireless.ts` renders it only `if (c.ip)` — never
			// drew once. Measured on the live fleet: 26 clients, 0 addresses.
			// THE ADDRESS COMES FROM ARP AND FROM NOWHERE ELSE, and the name
			// chain needs it: reverse DNS is the last fallback and it resolves
			// an address, not a MAC.
			ip := w.ipOf(mac)
			iface := firstNonEmptyStr(row["interface"], row["ap-interface"])
			c := parseWirelessClient(row, capsman, ip, w.nameOf(mac, ip), bands[iface])
			c.Comment = w.leaseComment(mac)
			clients = append(clients, c)
		}
	}

	switch mode {
	case "wifi":
		rows, err := readVia(w.cache, w.ros, wlRegWifiCmd, w.pollMs.duration())
		if err == nil {
			add(rows, false, nil)
		}
	case "wireless":
		rows, err := readVia(w.cache, w.ros, wlRegLegacyCmd, w.pollMs.duration())
		if err == nil {
			add(rows, false, nil)
		}
	default:
		// Probe. The modern stack first: on RouterOS 7.2x every board in this
		// fleet answered it, including one still on 802.11ac.
		if rows, err := readVia(w.cache, w.ros, wlRegWifiCmd, w.pollMs.duration()); err == nil {
			mode = "wifi"
			add(rows, false, nil)
		} else if rows, err := readVia(w.cache, w.ros, wlRegLegacyCmd, w.pollMs.duration()); err == nil {
			mode = "wireless"
			add(rows, false, nil)
		} else {
			mode = "none"
		}
	}

	// CAPsMAN can run ALONGSIDE either stack, so it is asked regardless of mode.
	// An empty answer is not the same as an absent menu: the first says no
	// clients, the second says this router is not a manager.
	//
	// NOT ON THE FIRST TICK, and that is the live behaviour rather than an
	// optimisation. The Node collector probes `/caps-man` fire-and-forget from
	// start() and builds its first payload before the answer lands, so its first
	// emit always reports `capsmanAvailable: false` and carries no CAPsMAN
	// clients. Probing from the second tick reproduces that sequence exactly,
	// deterministically, and without racing a goroutine — the same treatment the
	// system collector's serial needed, for the same reason.
	w.mu.Lock()
	probed := w.probedCaps
	w.probedCaps = true
	w.mu.Unlock()

	capsOK := false
	if probed {
		if rows, err := readVia(w.cache, w.ros, capsV1RegCmd, w.pollMs.duration()); err == nil {
			capsOK = true
			// The band join costs three more reads, so it is asked for only when
			// there is a client to label. A manager whose legacy CAPs are idle
			// answers this menu with nothing, and nothing is what it costs.
			if len(rows) > 0 {
				add(rows, true, w.capsV1Bands())
			}
		}
	}

	w.refreshSSIDs()

	// Strongest signal first, which is the order the page renders.
	sort.SliceStable(clients, func(i, j int) bool { return clients[i].Signal > clients[j].Signal })

	w.mu.Lock()
	moved := w.mode != mode
	w.mode = mode
	w.capsOK = capsOK
	ssids := withClientStats(w.ssids, clients)
	payload := &WirelessPayload{
		TS: time.Now().UnixMilli(), Clients: clients, Mode: modeOrNone(mode),
		PollMs: w.pollMs.ms(), CapsmanAvailable: capsOK,
		SSIDs: ssids, SSIDsManagedElsewhere: w.managedElsewhere,
	}
	w.last = payload
	w.mu.Unlock()

	// OUTSIDE the lock: resubscribe takes the cache's, and taking the two in
	// this order here and the other order in a delivery is how a deadlock gets
	// built.
	if moved {
		w.alignSubscription(mode)
	}

	EvWirelessUpdate.Emit(w.emit, wirelessRooms.Join(), *payload)
}

func modeOrNone(mode string) string {
	if mode == "" {
		return "none"
	}
	return mode
}

// capsV1Bands asks the legacy manager what band each of its CAP interfaces is
// on.
//
// THREE MENUS, ALL CONFIGURATION, ALL SHARED. They change when somebody edits
// the manager and not otherwise, and the CAPsMAN collector reads the same three
// — so through the cache they cost this collector nothing on a tick the other
// one has already served, and one read per cache window when it has not. That is
// the trade CLAUDE.md sets: concurrent channels are the bottleneck, and a menu
// two collectors share is one command either way.
//
// Only reached once `/caps-man/registration-table` has answered, so a router
// with no legacy manager never asks.
func (w *Wireless) capsV1Bands() map[string]CapsBand {
	ttl := w.pollMs.duration()
	ifaces, err := readVia(w.cache, w.ros, capsV1IfaceCmd, ttl)
	if err != nil {
		return nil
	}
	configs, _ := readVia(w.cache, w.ros, capsV1ConfigCmd, ttl)
	channels, _ := readVia(w.cache, w.ros, capsV1ChannelCmd, ttl)
	return CapsLegacyBands(ifaces, configs, channels)
}

// refreshSSIDs re-reads the broadcast networks.
//
// FAILURE IS SILENT and leaves the previous list in place: a card that empties
// itself on one bad poll is worse than a card that is briefly stale. Only when
// NOTHING has ever answered does it latch "no stack here" and stop asking.
func (w *Wireless) refreshSSIDs() {
	w.mu.Lock()
	endpoint := w.ssidEndpoint
	w.mu.Unlock()
	if endpoint == wlNoStack {
		return
	}

	order := []routeros.Cmd{wlIfaceWifiCmd, wlIfaceLegacy}
	if endpoint == wlIfaceWifiCmd.Path {
		order = []routeros.Cmd{wlIfaceWifiCmd}
	} else if endpoint == wlIfaceLegacy.Path {
		order = []routeros.Cmd{wlIfaceLegacy}
	}

	// THROUGH THE CACHE. This collector asks for EVERY field, so it is the one
	// that widens each of these menus' union to the whole row — see roscache's
	// note on widening. That was the operator's call on 2026-09-08: one fewer
	// command per sweep is worth a fatter reply for the other three consumers,
	// because concurrent channels are the bottleneck and bytes are not.
	//
	// The loop dispatches across BOTH stacks from one call site, which is why
	// /interface/wifi/print and /interface/wireless/print cannot be routed in
	// separate commits.
	for _, cmd := range order {
		rows, err := readVia(w.cache, w.ros, cmd, w.pollMs.duration())
		if err != nil {
			continue
		}
		ssids, managed := parseWirelessSSIDs(rows)
		// The same rows, read a second way. `ParseCatalogue` returns nothing for
		// the legacy menu — live's refusal, not an oversight: the legacy scan
		// command differs and there is no device here to verify it against, so
		// the dialog offers nothing rather than a picker that cannot work.
		cat := wifiscan.ParseCatalogue(replies(rows), cmd.Path)
		w.mu.Lock()
		w.ssidEndpoint = cmd.Path
		w.ssids = ssids
		w.managedElsewhere = managed
		w.scanIfaces = cat
		w.mu.Unlock()
		return
	}

	w.mu.Lock()
	if w.ssidEndpoint == "" {
		w.ssidEndpoint = wlNoStack
		w.ssids = []WirelessSSID{}
		w.managedElsewhere = 0
		w.scanIfaces = nil
	}
	w.mu.Unlock()
}

// ScanCatalogue is the interface catalogue and client placement the Frequency
// Analyser's dialog is built from.
//
// COPIES, because the caller is a websocket goroutine and this collector keeps
// polling underneath it.
//
// The client list is ONE ENTRY PER CLIENT, not per interface: the dialog counts
// them, and a scan of a radio drops every one of them plus everyone on the
// virtual APs riding on it. Live's count comes from `_knownClients` the same
// way, and its comment says why the radio's own interface is the wrong thing to
// count — "scanning a radio dropped all 15 clients within 2 seconds and not one
// of them was associated to the radio's own interface".
func (w *Wireless) ScanCatalogue() ([]wifiscan.Catalogue, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	cat := append([]wifiscan.Catalogue(nil), w.scanIfaces...)
	var cli []string
	if w.last != nil {
		cli = make([]string, 0, len(w.last.Clients))
		for _, c := range w.last.Clients {
			if c.Iface != "" {
				cli = append(cli, c.Iface)
			}
		}
	}
	return cat, cli
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (w *Wireless) SetPollMs(ms int) {
	w.pollMs.set(ms)
	w.loop.retime()
	// And the scheduler: a streamed menu reopens at the new interval.
	w.sched.retune()
}

// UseCache routes this collector's shareable reads through a per-router cache.
// Set once, before Start; nil leaves every read direct.
func (w *Wireless) UseCache(c *roscache.Cache) {
	w.cache = c
	w.sched.useCache(c)
}
