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
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/wifiscan"
)

var (
	wlRegWifiCmd    = routeros.Cmd{Path: "/interface/wifi/registration-table/print"}
	wlRegLegacyCmd  = routeros.Cmd{Path: "/interface/wireless/registration-table/print"}
	wlRegCapsmanCmd = routeros.Cmd{Path: "/caps-man/registration-table/print"}
	wlIfaceWifiCmd  = routeros.Cmd{Path: "/interface/wifi/print"}
	wlIfaceLegacy   = routeros.Cmd{Path: "/interface/wireless/print"}
)

// WirelessClient is one associated station.
type WirelessClient struct {
	MAC    string `json:"mac"`
	Signal int    `json:"signal"`
	Iface  string `json:"iface"`
	TxRate string `json:"txRate"`
	Band   string `json:"band"`
	IP     string `json:"ip"`
	RxRate string `json:"rxRate"`
	Uptime string `json:"uptime"`
	SSID   string `json:"ssid"`
	Name   string `json:"name"`
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
func parseWirelessClient(row routeros.Reply, capsman bool, ip, name string) WirelessClient {
	mac := firstNonEmptyStr(row["mac-address"], row["mac"])
	signal := 0
	if v := jsParseInt(firstNonEmptyStr(row["signal"], row["signal-strength"], row["rx-signal"], "0")); v != nil {
		signal = *v
	}
	iface := firstNonEmptyStr(row["interface"], row["ap-interface"])
	c := WirelessClient{
		MAC:    mac,
		Signal: signal,
		Iface:  iface,
		TxRate: firstNonEmptyStr(row["tx-rate"], row["tx-rate-set"]),
		Band:   wlBandOf(row, iface, capsman),
		IP:     ip,
		RxRate: row["rx-rate"],
		Uptime: row["uptime"],
		SSID:   row["ssid"],
		Name:   name,
	}
	if capsman {
		c.Source = "capsman"
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
	leases *DHCPLeases

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

	loop *pollLoop
}

const wlNoStack = "-"

func NewWireless(ros Reader, emit Emit, leases *DHCPLeases, pollMs int) *Wireless {
	w := &Wireless{
		ros: ros, emit: emit, leases: leases,
		pollMs: newPollInterval(clampPoll(pollMs, 5000, 2000, 60000)),
		ssids:  []WirelessSSID{},
	}
	w.loop = newPollLoop(func() { w.Tick() }, func() time.Duration {
		return w.pollMs.duration()
	})
	return w
}

func (w *Wireless) Suspend() { w.loop.stop() }

func (w *Wireless) Resume() {
	if w.ros.Connected() {
		w.loop.start()
	}
}

func (w *Wireless) Start() {
	w.Tick()
	w.loop.start()
}

func (w *Wireless) Stop() { w.loop.stop() }

// Reconnected drops every latch: the usual reason a connection dropped is an
// upgrade, and the router that came back may run a different wireless stack.
func (w *Wireless) Reconnected() {
	w.loop.stop()
	w.mu.Lock()
	w.mode, w.ssidEndpoint, w.capsOK = "", "", false
	w.probedCaps = false
	w.probedCaps = false
	w.ssids = []WirelessSSID{}
	w.managedElsewhere = 0
	w.lastFp = ""
	w.mu.Unlock()
	w.Tick()
	w.loop.start()
}

func (w *Wireless) Last() *WirelessPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last
}

// leaseName resolves a client MAC to its DHCP name.
func (w *Wireless) leaseName(mac string) string {
	if w.leases == nil {
		return ""
	}
	p := w.leases.Last()
	if p == nil {
		return ""
	}
	for _, l := range p.Leases {
		if strings.EqualFold(l.MAC, mac) {
			return firstNonEmptyStr(l.Name, l.HostName)
		}
	}
	return ""
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

	add := func(rows []routeros.Reply, capsman bool) {
		for _, row := range rows {
			if !isWirelessRow(row) {
				continue
			}
			mac := firstNonEmptyStr(row["mac-address"], row["mac"])
			if mac == "" || seen[mac] {
				continue
			}
			seen[mac] = true
			clients = append(clients, parseWirelessClient(row, capsman, "", w.leaseName(mac)))
		}
	}

	switch mode {
	case "wifi":
		rows, err := w.ros.Do(wlRegWifiCmd)
		if err == nil {
			add(rows, false)
		}
	case "wireless":
		rows, err := w.ros.Do(wlRegLegacyCmd)
		if err == nil {
			add(rows, false)
		}
	default:
		// Probe. The modern stack first: on RouterOS 7.2x every board in this
		// fleet answered it, including one still on 802.11ac.
		if rows, err := w.ros.Do(wlRegWifiCmd); err == nil {
			mode = "wifi"
			add(rows, false)
		} else if rows, err := w.ros.Do(wlRegLegacyCmd); err == nil {
			mode = "wireless"
			add(rows, false)
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
		if rows, err := w.ros.Do(wlRegCapsmanCmd); err == nil {
			capsOK = true
			add(rows, true)
		}
	}

	w.refreshSSIDs()

	// Strongest signal first, which is the order the page renders.
	sort.SliceStable(clients, func(i, j int) bool { return clients[i].Signal > clients[j].Signal })

	w.mu.Lock()
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

	w.emit("page-wireless,dash-card-wireless", "wireless:update", payload)
}

func modeOrNone(mode string) string {
	if mode == "" {
		return "none"
	}
	return mode
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

	for _, cmd := range order {
		rows, err := w.ros.Do(cmd)
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
}
