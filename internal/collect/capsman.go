package collect

// CAPsMAN collector — the manager, its CAPs, and the profiles they are
// provisioned with.
//
// ── A ROUTER CAN BE BOTH ────────────────────────────────────────────────────
//
// A manager that also runs its own radios as a CAP pointed at 127.0.0.1, which
// is exactly how this fleet's AX3 is set up. `role` says which of the four
// states it is in, and the page renders differently for each.
//
// ── JOINING CLIENTS TO CAPs IS THE HARD PART ────────────────────────────────
//
// A client's registration row names an INTERFACE, and only the MASTER interface
// carries the `cap` field — so a virtual AP has to be chased up to its master
// first. Without that, every client on a guest SSID looks like it belongs to the
// manager.
//
// ── PROFILES ARE PROJECTED BY NAME, NEVER SPREAD ────────────────────────────
//
// Field by field, so a proplist widened later cannot silently push a new field —
// a passphrase included — at every browser on the page. The same reasoning as
// the proplists themselves: what is absent is the security property.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// capsIdentitySep joins the parts of a provisioning rule's composite identity.
//
// U+0001, and it must match resource.IdentityOfSeparator exactly or every edit
// is refused as a stale row. Written as an ESCAPE rather than a literal: a
// literal control character is invisible in a diff and lost by anything that
// normalises the file.
const capsIdentitySep = "\u0001"

var (
	capsManagerCmd = routeros.Cmd{Path: "/interface/wifi/capsman/print"}
	capsCapCmd     = routeros.Cmd{Path: "/interface/wifi/cap/print"}
	capsRemoteCmd  = routeros.Cmd{Path: "/interface/wifi/capsman/remote-cap/print"}
	capsProvCmd    = routeros.Cmd{Path: "/interface/wifi/provisioning/print", Args: []string{
		"=.proplist=.id,supported-bands,action,master-configuration,slave-configurations," +
			"name-format,radio-mac,identity-regexp,comment,disabled"}}
	capsRadioCmd = routeros.Cmd{Path: "/interface/wifi/radio/print", Args: []string{
		"=.proplist=radio-mac,interface,cap,disabled"}}
	capsIfaceCmd = routeros.Cmd{Path: "/interface/wifi/print", Args: []string{
		"=.proplist=.id,name,master-interface,radio-mac,cap,disabled"}}
	capsRegCmd = routeros.Cmd{Path: "/interface/wifi/registration-table/print", Args: []string{
		"=.proplist=interface,mac-address,ssid,signal,uptime"}}
)

const (
	capsConfigEvery = 12
	// clientsPerCap caps what travels per CAP. The COUNT is always exact; only
	// the listed rows are bounded.
	clientsPerCap = 200
	// masterDepth bounds the virtual-AP chase. A cycle in `master-interface`
	// would otherwise hang the collector.
	masterDepth = 4
)

type CapsManager struct {
	Enabled                bool     `json:"enabled"`
	Interfaces             []string `json:"interfaces"`
	CaCertificate          string   `json:"caCertificate"`
	Certificate            string   `json:"certificate"`
	RequirePeerCertificate bool     `json:"requirePeerCertificate"`
	UpgradePolicy          string   `json:"upgradePolicy"`
	PackagePath            string   `json:"packagePath"`
}

type CapsCapMode struct {
	Enabled             bool     `json:"enabled"`
	DiscoveryInterfaces []string `json:"discoveryInterfaces"`
	CapsManAddresses    []string `json:"capsManAddresses"`
	CurrentAddress      string   `json:"currentAddress"`
	CurrentIdentity     string   `json:"currentIdentity"`
	Certificate         string   `json:"certificate"`
	SlavesDatapath      string   `json:"slavesDatapath"`
}

type CapsRadio struct {
	RadioMac  string `json:"radioMac"`
	Interface string `json:"interface"`
	Disabled  bool   `json:"disabled"`
}

type CapsClient struct {
	Mac       string   `json:"mac"`
	Interface string   `json:"interface"`
	SSID      string   `json:"ssid"`
	Signal    *float64 `json:"signal"`
	Uptime    string   `json:"uptime"`
}

type Cap struct {
	Identity      string       `json:"identity"`
	Address       string       `json:"address"`
	BoardName     string       `json:"boardName"`
	Serial        string       `json:"serial"`
	Version       string       `json:"version"`
	BaseMac       string       `json:"baseMac"`
	CommonName    string       `json:"commonName"`
	State         string       `json:"state"`
	ConnectedTime string       `json:"connectedTime"`
	Uptime        string       `json:"uptime"`
	Radios        []CapsRadio  `json:"radios"`
	Clients       []CapsClient `json:"clients"`
	ClientCount   int          `json:"clientCount"`
}

type CapsProvisioning struct {
	ID string `json:"id"`
	// Identity is a COMPOSITE built the way the registry builds one. A
	// provisioning rule has no name and nothing unique about it, so the edit
	// dialog ADDRESSES it by `.id` and IDENTIFIES it by this tuple — an id
	// survives an edit, which makes it the wrong thing to recognise a row by.
	Identity            string   `json:"identity"`
	SupportedBands      []string `json:"supportedBands"`
	Action              string   `json:"action"`
	MasterConfiguration string   `json:"masterConfiguration"`
	SlaveConfigurations []string `json:"slaveConfigurations"`
	NameFormat          string   `json:"nameFormat"`
	RadioMac            string   `json:"radioMac"`
	IdentityRegexp      string   `json:"identityRegexp"`
	Comment             string   `json:"comment"`
	Disabled            bool     `json:"disabled"`
}

type CapsTotals struct {
	Caps          int `json:"caps"`
	CapsOk        int `json:"capsOk"`
	Radios        int `json:"radios"`
	Clients       int `json:"clients"`
	ClientsOnCaps int `json:"clientsOnCaps"`
	ClientsLocal  int `json:"clientsLocal"`
}

type CapsConfigProfile struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	SSID     string `json:"ssid"`
	Mode     string `json:"mode"`
	Country  string `json:"country"`
	HideSsid bool   `json:"hideSsid"`
	Security string `json:"security"`
	Channel  string `json:"channel"`
	Datapath string `json:"datapath"`
	Manager  string `json:"manager"`
	Comment  string `json:"comment"`
	Disabled bool   `json:"disabled"`
}

type CapsSecurityProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AuthTypes string `json:"authTypes"`
	Wps       string `json:"wps"`
	Ft        bool   `json:"ft"`
	Comment   string `json:"comment"`
	Disabled  bool   `json:"disabled"`
}

type CapsChannelProfile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Band               string `json:"band"`
	Frequency          string `json:"frequency"`
	Width              string `json:"width"`
	SecondaryFrequency string `json:"secondaryFrequency"`
	SkipDfsChannels    string `json:"skipDfsChannels"`
	Comment            string `json:"comment"`
	Disabled           bool   `json:"disabled"`
}

type CapsDatapathProfile struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Bridge            string `json:"bridge"`
	VlanID            string `json:"vlanId"`
	ClientIsolation   bool   `json:"clientIsolation"`
	LocalForwarding   bool   `json:"localForwarding"`
	TrafficProcessing string `json:"trafficProcessing"`
	Comment           string `json:"comment"`
	Disabled          bool   `json:"disabled"`
}

type CapsProfiles struct {
	Configuration []CapsConfigProfile   `json:"configuration"`
	Security      []CapsSecurityProfile `json:"security"`
	Channel       []CapsChannelProfile  `json:"channel"`
	Datapath      []CapsDatapathProfile `json:"datapath"`
}

type CapsmanPayload struct {
	TS           int64              `json:"ts"`
	PollMs       int                `json:"pollMs"`
	Role         string             `json:"role"`
	Manager      CapsManager        `json:"manager"`
	Cap          CapsCapMode        `json:"cap"`
	Caps         []Cap              `json:"caps"`
	Provisioning []CapsProvisioning `json:"provisioning"`
	LocalRadios  []CapsRadio        `json:"localRadios"`
	Totals       CapsTotals         `json:"totals"`
	Profiles     CapsProfiles       `json:"profiles"`
	// Available is false on a router running the legacy wireless package, so the
	// page can say so instead of rendering an empty manager panel.
	Available bool `json:"available"`
}

// capField is `identity@base-mac%id`, parsed.
//
// Returns false for anything that is not that shape, so a router reporting the
// field differently degrades to the MAC-prefix fallback rather than inventing a
// CAP called `undefined`.
func capField(v string) (identity, baseMac, id string, ok bool) {
	if v == "" {
		return "", "", "", false
	}
	at := strings.Index(v, "@")
	if at < 1 {
		return "", "", "", false
	}
	identity = v[:at]
	rest := v[at+1:]
	if pct := strings.Index(rest, "%"); pct == -1 {
		baseMac = strings.ToUpper(rest)
	} else {
		baseMac = strings.ToUpper(rest[:pct])
		id = rest[pct+1:]
	}
	if baseMac == "" {
		return "", "", "", false
	}
	return identity, baseMac, id, true
}

// macPrefix is the first five octets — the fallback when a router does not
// report `cap`, because a CAP's radios sit in the same /40 block as its base MAC.
func macPrefix(mac string) string {
	parts := strings.Split(strings.ToUpper(mac), ":")
	if len(parts) >= 5 {
		return strings.Join(parts[:5], ":")
	}
	return ""
}

// BuildCapsmanView joins every table into the CAPsMAN view. Pure, so the join
// can be tested without a router.
func BuildCapsmanView(managerRow, capRow routeros.Reply,
	remoteRows, provRows, radioRows, ifaceRows, regRows []routeros.Reply) CapsmanPayload {

	mgr, capRw := managerRow, capRow

	manager := CapsManager{
		Enabled: rosTruthyC(mgr["enabled"]), Interfaces: splitCsv(mgr["interfaces"]),
		CaCertificate: mgr["ca-certificate"], Certificate: mgr["certificate"],
		RequirePeerCertificate: rosTruthyC(mgr["require-peer-certificate"]),
		UpgradePolicy:          mgr["upgrade-policy"], PackagePath: mgr["package-path"],
	}
	capMode := CapsCapMode{
		Enabled:             rosTruthyC(capRw["enabled"]),
		DiscoveryInterfaces: splitCsv(capRw["discovery-interfaces"]),
		CapsManAddresses:    splitCsv(capRw["caps-man-addresses"]),
		CurrentAddress:      capRw["current-caps-man-address"],
		CurrentIdentity:     capRw["current-caps-man-identity"],
		Certificate:         capRw["certificate"], SlavesDatapath: capRw["slaves-datapath"],
	}

	role := "none"
	switch {
	case manager.Enabled && capMode.Enabled:
		role = "both"
	case manager.Enabled:
		role = "manager"
	case capMode.Enabled:
		role = "cap"
	}

	caps := []Cap{}
	byIdentity := map[string]int{}
	byBaseMac := map[string]int{}
	byPrefix := map[string]int{}
	for _, r := range remoteRows {
		// No identity also drops the `{undefined:''}` junk row.
		if r == nil || r["identity"] == "" {
			continue
		}
		baseMac := strings.ToUpper(r["base-mac"])
		caps = append(caps, Cap{
			Identity: r["identity"], Address: r["address"], BoardName: r["board-name"],
			Serial: r["serial"], Version: r["version"], BaseMac: baseMac,
			CommonName: r["common-name"], State: r["state"],
			ConnectedTime: r["connected-time"], Uptime: r["uptime"],
			Radios: []CapsRadio{}, Clients: []CapsClient{},
		})
		i := len(caps) - 1
		byIdentity[r["identity"]] = i
		if baseMac != "" {
			byBaseMac[baseMac] = i
			if p := macPrefix(baseMac); p != "" {
				byPrefix[p] = i
			}
		}
	}

	capFor := func(capValue, radioMac string) int {
		if identity, baseMac, _, ok := capField(capValue); ok {
			if i, ok := byBaseMac[baseMac]; ok {
				return i
			}
			if i, ok := byIdentity[identity]; ok {
				return i
			}
			return -1
		}
		if p := macPrefix(radioMac); p != "" {
			if i, ok := byPrefix[p]; ok {
				return i
			}
		}
		return -1
	}

	localRadios := []CapsRadio{}
	for _, r := range radioRows {
		if r == nil || r["radio-mac"] == "" {
			continue
		}
		radio := CapsRadio{
			RadioMac: strings.ToUpper(r["radio-mac"]), Interface: r["interface"],
			Disabled: rosTruthyC(r["disabled"]),
		}
		if i := capFor(r["cap"], radio.RadioMac); i >= 0 {
			caps[i].Radios = append(caps[i].Radios, radio)
		} else {
			localRadios = append(localRadios, radio)
		}
	}

	// Interface -> CAP, CHASING VIRTUAL APs UP TO THEIR MASTER. Only the master
	// carries `cap`, so without this every client on a guest SSID would look
	// like it belonged to the manager.
	ifaceByName := map[string]routeros.Reply{}
	names := make([]string, 0, len(ifaceRows))
	for _, r := range ifaceRows {
		if r == nil || r["name"] == "" {
			continue
		}
		if _, seen := ifaceByName[r["name"]]; !seen {
			names = append(names, r["name"])
		}
		ifaceByName[r["name"]] = r
	}
	ifaceCap := map[string]int{}
	for _, name := range names {
		cur := ifaceByName[name]
		for depth := 0; cur != nil && cur["cap"] == "" && cur["master-interface"] != "" && depth < masterDepth; depth++ {
			cur = ifaceByName[cur["master-interface"]]
		}
		if cur != nil {
			if i := capFor(cur["cap"], cur["radio-mac"]); i >= 0 {
				ifaceCap[name] = i
			}
		}
	}

	clientsOnCaps, clientsLocal := 0, 0
	for _, r := range regRows {
		if r == nil || r["mac-address"] == "" {
			continue
		}
		iface := r["interface"]
		client := CapsClient{
			Mac: strings.ToUpper(r["mac-address"]), Interface: iface,
			SSID: r["ssid"], Uptime: r["uptime"],
		}
		if s, ok := r["signal"]; ok && s != "" {
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				client.Signal = &n
			}
		}
		if i, ok := ifaceCap[iface]; ok {
			caps[i].ClientCount++
			if len(caps[i].Clients) < clientsPerCap {
				caps[i].Clients = append(caps[i].Clients, client)
			}
			clientsOnCaps++
		} else {
			clientsLocal++
		}
	}

	radioTotal := len(localRadios)
	for i := range caps {
		radioTotal += len(caps[i].Radios)
		rs := caps[i].Radios
		sort.SliceStable(rs, func(a, b int) bool {
			return Collate(rs[a].Interface, rs[b].Interface) < 0
		})
		// Strongest signal first. A null signal sorts LAST, which is what
		// comparing against -Infinity does on the Node side.
		cl := caps[i].Clients
		sort.SliceStable(cl, func(a, b int) bool {
			return signalOrNegInf(cl[b].Signal) < signalOrNegInf(cl[a].Signal)
		})
	}
	sort.SliceStable(caps, func(a, b int) bool {
		return Collate(caps[a].Identity, caps[b].Identity) < 0
	})

	capsOk := 0
	for _, c := range caps {
		if strings.EqualFold(c.State, "ok") {
			capsOk++
		}
	}

	provisioning := []CapsProvisioning{}
	for _, r := range provRows {
		// `action` ABSENT, not empty: an empty menu's junk row has no keys.
		if r == nil {
			continue
		}
		if _, ok := r["action"]; !ok {
			continue
		}
		provisioning = append(provisioning, CapsProvisioning{
			ID: r[".id"],
			Identity: strings.Join([]string{r["supported-bands"], r["action"],
				r["master-configuration"], r["name-format"]}, capsIdentitySep),
			SupportedBands:      splitCsv(r["supported-bands"]),
			Action:              r["action"],
			MasterConfiguration: r["master-configuration"],
			SlaveConfigurations: splitCsv(r["slave-configurations"]),
			NameFormat:          r["name-format"], RadioMac: r["radio-mac"],
			IdentityRegexp: r["identity-regexp"], Comment: r["comment"],
			Disabled: rosTruthyC(r["disabled"]),
		})
	}

	return CapsmanPayload{
		Role: role, Manager: manager, Cap: capMode,
		Caps: caps, Provisioning: provisioning, LocalRadios: localRadios,
		Totals: CapsTotals{
			Caps: len(caps), CapsOk: capsOk, Radios: radioTotal,
			Clients:       clientsOnCaps + clientsLocal,
			ClientsOnCaps: clientsOnCaps, ClientsLocal: clientsLocal,
		},
	}
}

func signalOrNegInf(p *float64) float64 {
	if p == nil {
		// The Node side compares against -Infinity so a null signal sorts last.
		return -1e308
	}
	return *p
}

func rosTruthyC(v string) bool { return v == "true" || v == "yes" }

func splitCsv(v string) []string {
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// namedOnly drops the nameless junk row an empty RouterOS menu answers with,
// keeping the slice. `namedRows` in wifiview.go does the same for a map.
func namedOnly(rows []routeros.Reply) []routeros.Reply {
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if r != nil && strings.TrimSpace(r["name"]) != "" {
			out = append(out, r)
		}
	}
	return out
}

type Capsman struct {
	ros    Reader
	emit   Emit
	poll   *pollLoop
	pollMs *pollInterval

	mu       sync.Mutex
	manager  routeros.Reply
	cap      routeros.Reply
	prov     []routeros.Reply
	profiles map[string][]routeros.Reply
	ticks    int
	dirty    bool
	lastFP   string
	last     *CapsmanPayload
	// nil = unprobed, false = this router has no such menu.
	managerAvail, capAvail *bool
}

func NewCapsman(ros Reader, emit Emit, pollMs int) *Capsman {
	// The Node call is clampPoll(pollMs, 10000, 600000, 30000). Reordered for
	// this side's (raw, def, lo, hi).
	ms := clampPoll(pollMs, 10000, 30000, 600000)
	c := &Capsman{ros: ros, emit: emit, pollMs: newPollInterval(ms), dirty: true,
		profiles: map[string][]routeros.Reply{}}
	c.poll = newPollLoop(func() { c.Tick() },
		func() time.Duration { return time.Duration(ms) * time.Millisecond })
	return c
}

// read latches a menu's absence. Each of the profile menus latches
// INDEPENDENTLY, so a build without one costs a tab rather than a page.
func (c *Capsman) read(cmd routeros.Cmd, avail **bool) []routeros.Reply {
	if avail != nil && *avail != nil && !**avail {
		return nil
	}
	rows, err := c.ros.Do(cmd)
	if err != nil {
		if avail != nil && isAbsentMenu(err) {
			no := false
			*avail = &no
		}
		return nil
	}
	if avail != nil {
		yes := true
		*avail = &yes
	}
	return rows
}

func (c *Capsman) Tick() {
	c.mu.Lock()
	needConfig := c.dirty || c.ticks%capsConfigEvery == 0
	c.mu.Unlock()

	if needConfig {
		mgr := c.read(capsManagerCmd, &c.managerAvail)
		cap_ := c.read(capsCapCmd, &c.capAvail)
		prov := c.read(capsProvCmd, nil)
		cfg := c.read(wifiConfigCmd, nil)
		sec := c.read(wifiSecurityCmd, nil)
		chn := c.read(wifiChannelCmd, nil)
		dpt := c.read(routeros.Cmd{Path: "/interface/wifi/datapath/print", Args: []string{
			"=.proplist=.id,name,bridge,vlan-id,client-isolation,local-forwarding," +
				"traffic-processing,disabled,comment"}}, nil)

		c.mu.Lock()
		c.manager = firstRow(mgr)
		c.cap = firstRow(cap_)
		c.prov = prov
		c.profiles = map[string][]routeros.Reply{
			"configuration": namedOnly(cfg), "security": namedOnly(sec),
			"channel": namedOnly(chn), "datapath": namedOnly(dpt),
		}
		c.dirty = false
		c.mu.Unlock()
	}

	remote := c.read(capsRemoteCmd, nil)
	radios := c.read(capsRadioCmd, nil)
	ifaces := c.read(capsIfaceCmd, nil)
	reg := c.read(capsRegCmd, nil)

	c.mu.Lock()
	c.ticks++
	built := BuildCapsmanView(c.manager, c.cap, remote, c.prov, radios, ifaces, reg)
	built.TS = time.Now().UnixMilli()
	built.PollMs = c.pollMs.ms()
	built.Profiles = c.projectProfiles()
	built.Available = c.managerAvail == nil || *c.managerAvail ||
		c.capAvail == nil || *c.capAvail
	c.last = &built
	fp := capsFingerprintOf(&built)
	changed := fp != c.lastFP
	c.lastFP = fp
	c.mu.Unlock()

	if changed {
		c.emit("page-capsman", "capsman:update", &built)
	}
}

// projectProfiles maps each menu FIELD BY FIELD. Never a spread: a proplist
// widened later must not be able to push a new field at every browser.
func (c *Capsman) projectProfiles() CapsProfiles {
	out := CapsProfiles{
		Configuration: []CapsConfigProfile{}, Security: []CapsSecurityProfile{},
		Channel: []CapsChannelProfile{}, Datapath: []CapsDatapathProfile{},
	}
	for _, r := range c.profiles["configuration"] {
		out.Configuration = append(out.Configuration, CapsConfigProfile{
			ID: r[".id"], Name: r["name"], SSID: r["ssid"], Mode: r["mode"],
			Country: r["country"], HideSsid: rosTruthyC(r["hide-ssid"]),
			Security: r["security"], Channel: r["channel"], Datapath: r["datapath"],
			Manager: r["manager"], Comment: r["comment"],
			Disabled: rosTruthyC(r["disabled"]),
		})
	}
	for _, r := range c.profiles["security"] {
		out.Security = append(out.Security, CapsSecurityProfile{
			ID: r[".id"], Name: r["name"], AuthTypes: r["authentication-types"],
			Wps: r["wps"], Ft: rosTruthyC(r["ft"]), Comment: r["comment"],
			Disabled: rosTruthyC(r["disabled"]),
		})
	}
	for _, r := range c.profiles["channel"] {
		out.Channel = append(out.Channel, CapsChannelProfile{
			ID: r[".id"], Name: r["name"], Band: r["band"], Frequency: r["frequency"],
			Width: r["width"], SecondaryFrequency: r["secondary-frequency"],
			SkipDfsChannels: r["skip-dfs-channels"], Comment: r["comment"],
			Disabled: rosTruthyC(r["disabled"]),
		})
	}
	for _, r := range c.profiles["datapath"] {
		out.Datapath = append(out.Datapath, CapsDatapathProfile{
			ID: r[".id"], Name: r["name"], Bridge: r["bridge"], VlanID: r["vlan-id"],
			ClientIsolation:   rosTruthyC(r["client-isolation"]),
			LocalForwarding:   rosTruthyC(r["local-forwarding"]),
			TrafficProcessing: r["traffic-processing"], Comment: r["comment"],
			Disabled: rosTruthyC(r["disabled"]),
		})
	}
	return out
}

// capsFingerprintOf decides whether this tick is worth emitting.
//
// EVERY FIELD THE CONFIGURATION CARD CAN EDIT belongs here. A field left out
// means a save that lands on the router and never reaches the browser, which
// reads as a failed write — `comment` and `slaveConfigurations` were exactly
// that before the card existed.
func capsFingerprintOf(p *CapsmanPayload) string {
	c := make([][]any, 0, len(p.Caps))
	for _, x := range p.Caps {
		ifs := make([]string, 0, len(x.Radios))
		for _, r := range x.Radios {
			ifs = append(ifs, r.Interface)
		}
		c = append(c, []any{x.Identity, x.State, x.Version, x.ConnectedTime, x.ClientCount, ifs})
	}
	pr := make([][]any, 0, len(p.Provisioning))
	for _, x := range p.Provisioning {
		pr = append(pr, []any{x.ID, x.SupportedBands, x.Action, x.MasterConfiguration,
			x.SlaveConfigurations, x.NameFormat, x.RadioMac, x.IdentityRegexp,
			x.Comment, x.Disabled})
	}
	b, _ := json.Marshal(map[string]any{
		"r": p.Role,
		"m": []any{p.Manager.Enabled, p.Manager.Interfaces, p.Cap.Enabled, p.Cap.CurrentIdentity},
		"c": c, "p": pr,
		"f": []any{p.Profiles.Configuration, p.Profiles.Security,
			p.Profiles.Channel, p.Profiles.Datapath},
		"t": p.Totals,
	})
	return string(b)
}

func (c *Capsman) Last() *CapsmanPayload {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

func (c *Capsman) RefreshNow() {
	c.mu.Lock()
	c.dirty = true
	c.mu.Unlock()
	c.Tick()
}

func (c *Capsman) Start() { c.Tick(); c.poll.start() }

func (c *Capsman) Reconnected() {
	c.poll.stop()
	c.mu.Lock()
	c.lastFP = ""
	c.dirty = true
	c.managerAvail, c.capAvail = nil, nil
	c.mu.Unlock()
	c.Tick()
	c.poll.start()
}

func (c *Capsman) Suspend() { c.poll.stop() }
func (c *Capsman) Resume()  { c.poll.start() }

func (c *Capsman) Stop() {
	c.poll.stop()
	c.mu.Lock()
	c.lastFP = ""
	c.mu.Unlock()
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (c *Capsman) SetPollMs(ms int) {
	c.pollMs.set(ms)
	c.poll.retime()
}
