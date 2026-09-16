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

// ── THE OTHER CAPsMAN ───────────────────────────────────────────────────────
//
// `/caps-man` is RouterOS's first manager, and a router can run it ALONGSIDE
// the modern one rather than instead of it. That is not a corner case: the
// RB5009 this was written against provisions three wifi-qcom CAPs through
// `/interface/wifi/capsman` and a dozen RB951s through `/caps-man`, and until
// now the page showed the three and said nothing about the twelve.
//
// THE TWO TREES SHARE NO FIELD NAMES. `board-name` here is `board` there,
// `signal` is `rx-signal`, `supported-bands` is `hw-supported-modes`, and a CAP
// is joined to its radios through `cap=identity@base-mac%id` on one side and
// through `/caps-man/radio`'s own identity column on the other. So they are read
// and joined separately, and meet only in the payload — one builder branching on
// which tree it was handed would hide a misspelling in whichever branch a fleet
// does not happen to run.
//
// EVERY v1 ROW IS READ-ONLY. `internal/server/resource.go` knows the
// `/interface/wifi` menus and nothing else, so these rows carry no `.id` for the
// edit dialog to address and the page marks them rather than offering an edit
// that would be refused.
//
// The three menus wireless.go and wifi.go also need are declared HERE and read
// through the cache from all three, which is the same arrangement the modern
// profile menus have had since wifi.go declared them.
var (
	capsV1ManagerCmd = routeros.Cmd{Path: "/caps-man/manager/print"}
	// No proplist on the three status menus: they carry no credential, their
	// field names are the half of v1 the documentation does not enumerate, and
	// asking for everything is what lets `firstNonEmptyStr` cover both spellings
	// of a column instead of one guess deciding the page.
	capsV1RemoteCmd = routeros.Cmd{Path: "/caps-man/remote-cap/print"}
	capsV1RadioCmd  = routeros.Cmd{Path: "/caps-man/radio/print"}
	capsV1RegCmd    = routeros.Cmd{Path: "/caps-man/registration-table/print"}
	// The interface menu DOES get one: a manager carries one row per CAP radio
	// plus one per slave configuration, and an inline `configuration.security`
	// override on any of them would drag a passphrase into a payload that goes
	// to every browser on the page.
	capsV1IfaceCmd = routeros.Cmd{Path: "/caps-man/interface/print", Args: []string{
		"=.proplist=.id,name,mac-address,master-interface,radio-mac,configuration," +
			"configuration.ssid,configuration.channel,configuration.security," +
			"configuration.datapath,configuration.country,configuration.hide-ssid," +
			"disabled,running,dynamic,comment"}}
	capsV1ProvCmd = routeros.Cmd{Path: "/caps-man/provisioning/print", Args: []string{
		"=.proplist=.id,hw-supported-modes,action,master-configuration,slave-configurations," +
			"name-format,name-prefix,radio-mac,identity-regexp,common-name-regexp,comment,disabled"}}
	capsV1ConfigCmd = routeros.Cmd{Path: "/caps-man/configuration/print", Args: []string{
		"=.proplist=.id,name,ssid,mode,country,hide-ssid,security,channel,datapath,comment," +
			"channel.band,channel.frequency,channel.width,datapath.bridge,datapath.vlan-id," +
			"security.authentication-types"}}
	capsV1SecurityCmd = routeros.Cmd{Path: "/caps-man/security/print", Args: []string{
		"=.proplist=.id,name,authentication-types,encryption,group-encryption,eap-methods," +
			"tls-mode,comment"}}
	capsV1ChannelCmd = routeros.Cmd{Path: "/caps-man/channel/print", Args: []string{
		"=.proplist=.id,name,band,frequency,width,control-channel-width,extension-channel," +
			"tx-power,comment"}}
	capsV1DatapathCmd = routeros.Cmd{Path: "/caps-man/datapath/print", Args: []string{
		"=.proplist=.id,name,bridge,vlan-id,vlan-mode,client-to-client-forwarding," +
			"local-forwarding,interface-list,comment"}}
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
	// Legacy marks a CAP that reported to `/caps-man` rather than to
	// `/interface/wifi/capsman`. The page says so on the row, because what you
	// can do with it differs: a v1 CAP is configured from the `/caps-man` tree,
	// which this app reads and does not write.
	Legacy bool `json:"legacy"`
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
	Legacy              bool     `json:"legacy"`
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
	Legacy   bool   `json:"legacy"`
}

type CapsSecurityProfile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	AuthTypes string `json:"authTypes"`
	Wps       string `json:"wps"`
	Ft        bool   `json:"ft"`
	Comment   string `json:"comment"`
	Disabled  bool   `json:"disabled"`
	Legacy    bool   `json:"legacy"`
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
	Legacy             bool   `json:"legacy"`
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
	Legacy            bool   `json:"legacy"`
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
	// LegacyAvailable says the `/caps-man` tree answered at all, and
	// LegacyManager that its manager is switched on. Separate questions: a router
	// with the wireless package installed has the menus whether or not anything
	// is managed through them, and the page's Mode tile must not read "Manager"
	// off a tree nobody enabled.
	LegacyAvailable bool `json:"legacyAvailable"`
	LegacyManager   bool `json:"legacyManager"`
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
		if capStateOk(c.State) {
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

// capsV1Master is the interface a `/caps-man` row rides on, or "" for a master.
//
// ── RouterOS SAYS "none", NOT NOTHING ───────────────────────────────────────
//
// `/caps-man/interface` answers `master-interface=none` on a MASTER, which is
// the sentinel RouterOS uses across the API and is not an empty string. Testing
// for empty made every legacy interface read as a virtual AP: the Wifi Networks
// page badged all twenty-four of them "Virtual AP", produced no radios at all
// for them — so the Wi-Fi map's access-point tray was empty — and the AP
// grouping put every slave under "This router".
//
// Found on a live manager, 2026-09-13, from four symptoms with one cause.
//
// ── AND THE CHASE MUST NOT STOP AT A radio-mac ──────────────────────────────
//
// A slave interface has a `radio-mac` of its own: the master's MAC with the
// locally-administered bit set (`76:…` against `74:…`), which belongs to no
// radio and matches nothing in `/caps-man/radio`. Stopping the chase when a row
// has one therefore stopped it on exactly the rows that needed it. The chase now
// follows `master-interface` to the ROOT and reads the radio there.
func capsV1Master(row routeros.Reply) string {
	m := strings.TrimSpace(row["master-interface"])
	if m == "none" {
		return ""
	}
	return m
}

// capsV1Root walks an interface up to the master that owns its radio.
func capsV1Root(ifaces map[string]routeros.Reply, row routeros.Reply) routeros.Reply {
	cur := row
	for depth := 0; cur != nil && depth < masterDepth; depth++ {
		m := capsV1Master(cur)
		if m == "" {
			return cur
		}
		next, ok := ifaces[m]
		if !ok {
			return cur
		}
		cur = next
	}
	return cur
}

// capsV1RadioIdentity is the CAP a `/caps-man/radio` row reported from.
//
// FOUR SPELLINGS, because MikroTik's page prints the column as REMOTE-AP-IDENT
// and the detail view has used `remote-cap-identity` since RouterOS 6. Getting
// it wrong does not cost a column: it detaches every radio from its CAP, so no
// client is attributed and no network knows which access point it is on.
func capsV1RadioIdentity(r routeros.Reply) string {
	return firstNonEmptyStr(r["remote-cap-identity"], r["remote-cap-name"],
		r["remote-ap-identity"], r["remote-ap-ident"])
}

// CapsLegacyAPs maps each `/caps-man` interface name to the CAP broadcasting it.
//
// The same two hops the client join makes — interface to `radio-mac` (chasing a
// virtual AP up to its master, which is the only row that carries one) and
// radio-mac to the identity `/caps-man/radio` reports.
func CapsLegacyAPs(ifaceRows, radioRows []routeros.Reply) map[string]string {
	byRadio := map[string]string{}
	for _, r := range radioRows {
		if mac := strings.ToUpper(r["radio-mac"]); mac != "" {
			if id := capsV1RadioIdentity(r); id != "" {
				byRadio[mac] = id
			}
		}
	}
	ifaces := map[string]routeros.Reply{}
	names := make([]string, 0, len(ifaceRows))
	for _, r := range ifaceRows {
		if n := strings.TrimSpace(r["name"]); n != "" {
			if _, seen := ifaces[n]; !seen {
				names = append(names, n)
			}
			ifaces[n] = r
		}
	}
	out := map[string]string{}
	for _, name := range names {
		root := capsV1Root(ifaces, ifaces[name])
		if root == nil {
			continue
		}
		if id, ok := byRadio[strings.ToUpper(root["radio-mac"])]; ok {
			out[name] = id
		}
	}
	return out
}

// CapsBand is a band as the MANAGER's configuration states it: the RouterOS
// band token and the frequency behind it.
//
// Both spellings are kept because both pages already know how to read one of
// them — `BandLabel` and `WifiStandard` take the token, `BandFromFrequency` the
// number — and a v1 channel profile can carry either. The export this was
// written against has `band=2ghz-onlyn frequency=2437` on one profile and
// `band=5ghz-onlyac` with no frequency on another.
type CapsBand struct {
	Raw       string
	Frequency string
	Width     string
}

// CapsLegacyBands maps each `/caps-man` interface name to the band its
// configuration puts it on.
//
// ── WHY THE MANAGER HAS TO BE ASKED ─────────────────────────────────────────
//
// A `/caps-man/registration-table` row carries no band — see `wlBandOf`, which
// falls back to the interface NAME for exactly this reason — and on v1 the name
// is no help either: `name-format=identity` names a CAP interface after the
// access point, so a client sits on `cap-north-1` and nothing in that string
// says 2.4 GHz. The manager knows, in three hops: the interface names a
// configuration, the configuration names a channel, and the channel carries the
// band.
//
// INLINE OVERRIDES WIN AT EVERY HOP, because that is what RouterOS does with
// them: a configuration may set `channel.band` directly instead of naming a
// profile, and an interface may name a different channel than its configuration.
//
// A VIRTUAL AP INHERITS THROUGH ITS MASTER. Slave configurations produce
// interfaces with a configuration of their own, but one that names no channel —
// they ride the master radio's. The chase is bounded the same way the client
// join's is, for the same reason.
func CapsLegacyBands(ifaceRows, configRows, channelRows []routeros.Reply) map[string]CapsBand {
	configs := namedRows(configRows)
	channels := namedRows(channelRows)

	ifaces := map[string]routeros.Reply{}
	order := make([]string, 0, len(ifaceRows))
	for _, r := range ifaceRows {
		name := strings.TrimSpace(r["name"])
		if name == "" {
			continue
		}
		if _, seen := ifaces[name]; !seen {
			order = append(order, name)
		}
		ifaces[name] = r
	}

	// bandOf reads one interface row without chasing, so the chase below can ask
	// the same question of a master and take the first row that answers.
	bandOf := func(r routeros.Reply) CapsBand {
		cfg := configs[r["configuration"]]
		chanName := firstNonEmpty(r["configuration.channel"], cfg["channel"])
		ch := channels[chanName]
		return CapsBand{
			Raw:       firstNonEmpty(cfg["channel.band"], ch["band"]),
			Frequency: firstNonEmpty(cfg["channel.frequency"], ch["frequency"]),
			// v1 names the operating width `control-channel-width`; see
			// projectLegacyProfiles.
			Width: firstNonEmpty(cfg["channel.width"], ch["width"], ch["control-channel-width"]),
		}
	}

	out := map[string]CapsBand{}
	for _, name := range order {
		b := bandOf(ifaces[name])
		if b.Raw == "" && b.Frequency == "" && b.Width == "" {
			// Nothing of its own: a slave configuration names no channel, so the
			// answer is the master radio's.
			if root := capsV1Root(ifaces, ifaces[name]); root != nil {
				b = bandOf(root)
			}
		}
		if b.Raw != "" || b.Frequency != "" || b.Width != "" {
			out[name] = b
		}
	}
	return out
}

// CapsLegacyView is the `/caps-man` tree's half of the CAPsMAN page, in the
// shapes the modern tree already produces.
type CapsLegacyView struct {
	ManagerEnabled bool
	Caps           []Cap
	ClientsOnCaps  int
	ClientsLocal   int
}

// BuildCapsmanLegacyView joins the legacy manager's tables. Pure, like
// BuildCapsmanView, and deliberately a second function rather than a branch in
// the first — see the header note on the two trees sharing no field names.
//
// ── THE CLIENT JOIN RUNS THE OTHER WAY ROUND ────────────────────────────────
//
// v2 puts `cap=identity@base-mac%id` on the interface and the radio, so the join
// starts at the client and reads the CAP off the row. v1 puts nothing on the
// interface at all: it carries a `radio-mac`, `/caps-man/radio` maps that MAC to
// the identity of the CAP that reported it, and only then is there a CAP. A
// slave interface has no `radio-mac` either, so it is chased to its master
// first — the same chase, one field along.
func BuildCapsmanLegacyView(managerRow routeros.Reply,
	remoteRows, radioRows, ifaceRows, regRows []routeros.Reply) CapsLegacyView {

	view := CapsLegacyView{
		ManagerEnabled: rosTruthyC(managerRow["enabled"]),
		Caps:           []Cap{},
	}

	byIdentity := map[string]int{}
	byPrefix := map[string]int{}
	for _, r := range remoteRows {
		// BOTH SPELLINGS OF THE ONE FIELD EVERY ROW IS KEYED ON. MikroTik's own
		// page prints this column as IDENT and the detail view as `identity`,
		// and a wrong guess here does not degrade a column — it drops every CAP
		// the legacy manager has.
		if r == nil {
			continue
		}
		identity := firstNonEmptyStr(r["identity"], r["ident"])
		if identity == "" {
			continue
		}
		baseMac := strings.ToUpper(firstNonEmptyStr(r["base-mac"], r["mac-address"]))
		view.Caps = append(view.Caps, Cap{
			Identity: identity, Address: r["address"],
			// v1 spells the model `board`; v2 spells it `board-name`. Neither
			// tree carries the other's, so both are asked for rather than one
			// being picked and the column left empty on whichever fleet has the
			// other kind of CAP.
			BoardName: firstNonEmptyStr(r["board-name"], r["board"]),
			Serial:    r["serial"], Version: r["version"], BaseMac: baseMac,
			CommonName: r["common-name"], State: r["state"],
			// v1's remote-cap reports how long the CAP has been connected as
			// `uptime`. It is the same measurement the Connected column shows
			// for a v2 CAP, under the name that tree gives it.
			ConnectedTime: firstNonEmptyStr(r["connected-time"], r["uptime"]),
			Uptime:        r["uptime"],
			Radios:        []CapsRadio{}, Clients: []CapsClient{},
			Legacy: true,
		})
		i := len(view.Caps) - 1
		byIdentity[identity] = i
		if p := macPrefix(baseMac); p != "" {
			byPrefix[p] = i
		}
	}

	// radio-mac -> CAP. The identity column is what v1 offers and the MAC prefix
	// is the fallback, exactly as in the modern join.
	radioCap := map[string]int{}
	for _, r := range radioRows {
		if r == nil || r["radio-mac"] == "" {
			continue
		}
		mac := strings.ToUpper(r["radio-mac"])
		i, ok := byIdentity[capsV1RadioIdentity(r)]
		if !ok {
			if i, ok = byPrefix[macPrefix(mac)]; !ok {
				continue
			}
		}
		radioCap[mac] = i
		view.Caps[i].Radios = append(view.Caps[i].Radios, CapsRadio{
			RadioMac: mac, Interface: r["interface"], Disabled: rosTruthyC(r["disabled"]),
		})
	}

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
		root := capsV1Root(ifaceByName, ifaceByName[name])
		if root == nil {
			continue
		}
		if i, ok := radioCap[strings.ToUpper(root["radio-mac"])]; ok {
			ifaceCap[name] = i
		}
	}

	for _, r := range regRows {
		if r == nil || r["mac-address"] == "" {
			continue
		}
		iface := r["interface"]
		client := CapsClient{
			Mac: strings.ToUpper(r["mac-address"]), Interface: iface,
			SSID: r["ssid"], Uptime: r["uptime"],
		}
		// `rx-signal` is v1's name for what v2 calls `signal`, and it is read
		// with the LEADING-NUMBER parse rather than ParseFloat: some builds
		// append the unit, and `-55dBm` through ParseFloat is an error and an
		// empty signal column, not a dash somebody would notice.
		if v := jsParseInt(firstNonEmptyStr(r["rx-signal"], r["signal"])); v != nil {
			n := float64(*v)
			client.Signal = &n
		}
		i, ok := ifaceCap[iface]
		if !ok {
			view.ClientsLocal++
			continue
		}
		view.Caps[i].ClientCount++
		if len(view.Caps[i].Clients) < clientsPerCap {
			view.Caps[i].Clients = append(view.Caps[i].Clients, client)
		}
		view.ClientsOnCaps++
	}

	for i := range view.Caps {
		rs := view.Caps[i].Radios
		sort.SliceStable(rs, func(a, b int) bool {
			return Collate(rs[a].Interface, rs[b].Interface) < 0
		})
		cl := view.Caps[i].Clients
		sort.SliceStable(cl, func(a, b int) bool {
			return signalOrNegInf(cl[b].Signal) < signalOrNegInf(cl[a].Signal)
		})
	}
	return view
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
	tableCore[CapsmanPayload]
	emit Emit

	// mu guards the config below, which the slow lane replaces and the
	// projections read.
	mu       sync.Mutex
	manager  routeros.Reply
	cap      routeros.Reply
	prov     []routeros.Reply
	profiles map[string][]routeros.Reply
	// The legacy `/caps-man` tree, read behind one probe of its manager menu.
	v1Manager  routeros.Reply
	v1Prov     []routeros.Reply
	v1Profiles map[string][]routeros.Reply

	// nil = unprobed, false = this router has no such menu, stop asking.
	managerAvail, capAvail, v1Avail *bool
}

// capsmanHeartbeat is how long an unchanged `capsman:update` may be suppressed.
// See packagesHeartbeat.
const capsmanHeartbeat = 10 * time.Second

func NewCapsman(ros Reader, emit Emit, pollMs int) *Capsman {
	c := &Capsman{emit: emit,
		profiles: map[string][]routeros.Reply{}, v1Profiles: map[string][]routeros.Reply{}}
	// Subscribed to the registration table, the clients, which is the only thing
	// here that changes on its own. The manager, CAP, provisioning and profiles
	// are the slow lane, and a write's RefreshNow re-reads them at once.
	c.setup(c, ros, pollMs, tableSpec{
		cmd: capsRegCmd, poll: [3]int{10000, 30000, 600000}, slowEvery: capsConfigEvery, heartbeat: capsmanHeartbeat,
	})
	return c
}

// read runs one menu, latching its absence when given a latch. Each of the
// profile menus latches INDEPENDENTLY, so a build without one costs a tab rather
// than a page.
func (c *Capsman) read(cmd routeros.Cmd, avail **bool) []routeros.Reply {
	rows, _ := readOptional(c.ros.Do, cmd, avail)
	return rows
}

// loadConfig reads the manager, the CAP, the provisioning rules and the four
// profile menus. It is the slow lane.
//
// ── CALLED FROM BOTH PATHS, AND THAT IS A FIX ───────────────────────────────
//
// This block lived only in Tick, and the scheduled path did not run it. The
// consequence was not subtle and no test saw it: `Resume` begins a subscription
// without ticking, so a page blur and refocus left `manager` and `cap` empty for
// good, and the CAPsMAN page reported MODE Off on a router whose manager was
// enabled. Everything else on the page -- CAPs, radios, clients -- kept working,
// which is what made it look fine.
//
// Found by reading the page against the router, not by a failing test.
func (c *Capsman) loadConfig() {
	{
		mgr := c.read(capsManagerCmd, &c.managerAvail)
		cap_ := c.read(capsCapCmd, &c.capAvail)
		prov := c.read(capsProvCmd, nil)
		cfg := c.read(wifiConfigCmd, nil)
		sec := c.read(wifiSecurityCmd, nil)
		chn := c.read(wifiChannelCmd, nil)
		dpt := c.read(routeros.Cmd{Path: "/interface/wifi/datapath/print", Args: []string{
			"=.proplist=.id,name,bridge,vlan-id,client-isolation,local-forwarding," +
				"traffic-processing,disabled,comment"}}, nil)

		// The `/caps-man` tree, on the same cadence. GATED ON ONE PROBE: the
		// manager menu latches absent on a router that has no legacy wireless
		// package, and the other five are not asked at all after that. Reading
		// them unconditionally would cost six refusals a cycle on every modern
		// board in a fleet, which is the kind of waste nothing ever reports.
		var v1Mgr, v1Prov, v1Cfg, v1Sec, v1Chn, v1Dpt []routeros.Reply
		if MenuAvailable(c.v1Avail) {
			v1Mgr = c.read(capsV1ManagerCmd, &c.v1Avail)
			if MenuAvailable(c.v1Avail) {
				v1Prov = c.read(capsV1ProvCmd, nil)
				v1Cfg, _ = readVia(c.cache, c.ros, capsV1ConfigCmd, c.pollMs.duration())
				v1Sec = c.read(capsV1SecurityCmd, nil)
				v1Chn, _ = readVia(c.cache, c.ros, capsV1ChannelCmd, c.pollMs.duration())
				v1Dpt = c.read(capsV1DatapathCmd, nil)
			}
		}

		c.mu.Lock()
		c.manager = firstRow(mgr)
		c.cap = firstRow(cap_)
		c.prov = prov
		c.profiles = map[string][]routeros.Reply{
			"configuration": namedOnly(cfg), "security": namedOnly(sec),
			"channel": namedOnly(chn), "datapath": namedOnly(dpt),
		}
		c.v1Manager = firstRow(v1Mgr)
		c.v1Prov = v1Prov
		c.v1Profiles = map[string][]routeros.Reply{
			"configuration": namedOnly(v1Cfg), "security": namedOnly(v1Sec),
			"channel": namedOnly(v1Chn), "datapath": namedOnly(v1Dpt),
		}
		c.mu.Unlock()
	}
}

// derive is the registration table, the clients, with the menus that accompany it
// and, on the slow lane, the config.
func (c *Capsman) derive(reg []routeros.Reply, err error, slow bool) (*CapsmanPayload, string) {
	if err != nil {
		return nil, ""
	}
	if slow {
		c.loadConfig()
	}
	return c.build(reg)
}

func (c *Capsman) send(p CapsmanPayload) { EvCapsmanUpdate.Emit(c.emit, capsmanRooms.Join(), p) }

// reset drops every latch, so a router that came back as a different build is
// probed again.
func (c *Capsman) reset() { c.managerAvail, c.capAvail, c.v1Avail = nil, nil, nil }

// build reads the menus that accompany the registration table and derives the payload.
func (c *Capsman) build(reg []routeros.Reply) (*CapsmanPayload, string) {
	remote := c.read(capsRemoteCmd, nil)
	radios := c.read(capsRadioCmd, nil)
	// THROUGH THE CACHE. Four collectors read each of these two menus, more
	// than any other in the tree. `read`'s availability latch is not wanted
	// here (both call sites pass nil), so readVia is the whole of it.
	ifaces, _ := readVia(c.cache, c.ros, capsIfaceCmd, c.pollMs.duration())

	// The legacy tree's four status menus, on the same tick and behind the same
	// latch the config half uses.
	var v1Remote, v1Radios, v1Ifaces, v1Reg []routeros.Reply
	// THE LATCH SET TO TRUE, not merely "not known to be false". `MenuAvailable`
	// treats an unprobed menu as present, which is the right answer for a read
	// and the wrong one for a claim: `mergeLegacy` sets `legacyAvailable`, and a
	// router that has never answered `/caps-man` must not be reported as running
	// a manager it does not have.
	legacy := c.v1Avail != nil && *c.v1Avail
	if legacy {
		v1Remote = c.read(capsV1RemoteCmd, nil)
		v1Radios = c.read(capsV1RadioCmd, nil)
		v1Ifaces, _ = readVia(c.cache, c.ros, capsV1IfaceCmd, c.pollMs.duration())
		v1Reg, _ = readVia(c.cache, c.ros, capsV1RegCmd, c.pollMs.duration())
	}

	c.mu.Lock()
	built := BuildCapsmanView(c.manager, c.cap, remote, c.prov, radios, ifaces, reg)
	built.TS = time.Now().UnixMilli()
	built.PollMs = c.pollMs.ms()
	built.Profiles = c.projectProfiles()
	// EITHER menu present is enough: a router may run the manager, the CAP side,
	// or both, and the page has something to show in all three cases.
	built.Available = MenuAvailable(c.managerAvail) || MenuAvailable(c.capAvail)
	if legacy {
		c.mergeLegacy(&built, v1Remote, v1Radios, v1Ifaces, v1Reg)
	}
	fp := capsFingerprintOf(&built)
	c.mu.Unlock()
	return &built, fp
}

// mergeLegacy folds the `/caps-man` tree into the payload the modern one built.
//
// APPENDED AND THEN SORTED ONCE. An operator reads the CAP list by identity, not
// by which of the two managers happens to hold each one; `legacy` on the row is
// what tells them apart where it matters.
//
// EVERY LEGACY ROW ARRIVES WITH AN EMPTY ID, which is what makes it read-only
// without the page needing a branch for it: `resRow` renders no `data-id` for an
// empty one, and the resource engine only opens a row that has one.
//
// Called with c.mu held, like projectProfiles.
func (c *Capsman) mergeLegacy(p *CapsmanPayload, remote, radios, ifaces, reg []routeros.Reply) {
	v1 := BuildCapsmanLegacyView(c.v1Manager, remote, radios, ifaces, reg)

	p.LegacyAvailable = true
	p.LegacyManager = v1.ManagerEnabled
	p.Available = true

	// The role is the UNION of the two trees. A router running only the legacy
	// manager reported "none" before this, which the page renders as MODE Off —
	// a statement about the router that was false on every v1 manager there is.
	if v1.ManagerEnabled {
		switch p.Role {
		case "none":
			p.Role = "manager"
		case "cap":
			p.Role = "both"
		}
	}

	p.Caps = append(p.Caps, v1.Caps...)
	sort.SliceStable(p.Caps, func(a, b int) bool {
		return Collate(p.Caps[a].Identity, p.Caps[b].Identity) < 0
	})

	p.Provisioning = append(p.Provisioning, c.projectLegacyProvisioning()...)
	lp := c.projectLegacyProfiles()
	p.Profiles.Configuration = append(p.Profiles.Configuration, lp.Configuration...)
	p.Profiles.Security = append(p.Profiles.Security, lp.Security...)
	p.Profiles.Channel = append(p.Profiles.Channel, lp.Channel...)
	p.Profiles.Datapath = append(p.Profiles.Datapath, lp.Datapath...)

	for _, cp := range v1.Caps {
		p.Totals.Radios += len(cp.Radios)
	}
	p.Totals.Caps = len(p.Caps)
	p.Totals.CapsOk = 0
	for _, cp := range p.Caps {
		if capStateOk(cp.State) {
			p.Totals.CapsOk++
		}
	}
	p.Totals.ClientsOnCaps += v1.ClientsOnCaps
	p.Totals.ClientsLocal += v1.ClientsLocal
	p.Totals.Clients = p.Totals.ClientsOnCaps + p.Totals.ClientsLocal
}

// capStateOk is "this CAP is working", across both trees' vocabularies.
//
// `/interface/wifi/capsman/remote-cap` says `ok`; `/caps-man/remote-cap` says
// `Run`. Counting only the first reported every legacy CAP as not-ok, which is
// the summary tile saying a fleet is down while every row reads Run.
func capStateOk(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "ok", "run":
		return true
	}
	return false
}

// projectLegacyProvisioning is the v1 provisioning table, in the v2 shape.
//
// `hw-supported-modes` is v1's `supported-bands` — the same question ("which
// radios does this rule match") asked in the older vocabulary, so it lands in
// the same field rather than getting a column of its own.
func (c *Capsman) projectLegacyProvisioning() []CapsProvisioning {
	out := []CapsProvisioning{}
	for _, r := range c.v1Prov {
		if r == nil {
			continue
		}
		if _, ok := r["action"]; !ok {
			continue
		}
		out = append(out, CapsProvisioning{
			Identity: strings.Join([]string{r["hw-supported-modes"], r["action"],
				r["master-configuration"], r["name-format"]}, capsIdentitySep),
			SupportedBands:      splitCsv(r["hw-supported-modes"]),
			Action:              r["action"],
			MasterConfiguration: r["master-configuration"],
			SlaveConfigurations: splitCsv(r["slave-configurations"]),
			NameFormat:          firstNonEmpty(r["name-format"], r["name-prefix"]),
			RadioMac:            r["radio-mac"],
			IdentityRegexp:      firstNonEmpty(r["identity-regexp"], r["common-name-regexp"]),
			Comment:             r["comment"],
			Disabled:            rosTruthyC(r["disabled"]),
			Legacy:              true,
		})
	}
	return out
}

// projectLegacyProfiles is the v1 profile menus, field by field, for the same
// reason projectProfiles is.
func (c *Capsman) projectLegacyProfiles() CapsProfiles {
	out := CapsProfiles{
		Configuration: []CapsConfigProfile{}, Security: []CapsSecurityProfile{},
		Channel: []CapsChannelProfile{}, Datapath: []CapsDatapathProfile{},
	}
	for _, r := range c.v1Profiles["configuration"] {
		out.Configuration = append(out.Configuration, CapsConfigProfile{
			Name: r["name"], SSID: r["ssid"], Mode: r["mode"], Country: r["country"],
			HideSsid: rosTruthyC(r["hide-ssid"]), Security: r["security"],
			Channel: r["channel"], Datapath: r["datapath"], Comment: r["comment"],
			Legacy: true,
		})
	}
	for _, r := range c.v1Profiles["security"] {
		out.Security = append(out.Security, CapsSecurityProfile{
			Name: r["name"], AuthTypes: r["authentication-types"],
			Comment: r["comment"], Legacy: true,
		})
	}
	for _, r := range c.v1Profiles["channel"] {
		out.Channel = append(out.Channel, CapsChannelProfile{
			Name: r["name"], Band: r["band"], Frequency: r["frequency"],
			// v1 names the operating width `control-channel-width`; `width` is
			// the v2 spelling and is asked for so a build that reports both is
			// read the same way.
			Width:   firstNonEmpty(r["width"], r["control-channel-width"]),
			Comment: r["comment"], Legacy: true,
		})
	}
	for _, r := range c.v1Profiles["datapath"] {
		out.Datapath = append(out.Datapath, CapsDatapathProfile{
			Name: r["name"], Bridge: r["bridge"], VlanID: r["vlan-id"],
			// v1 states the POSITIVE of what v2 calls client isolation, and its
			// default is `no` — so an absent field means isolated, which is what
			// the router does.
			ClientIsolation: !rosTruthyC(r["client-to-client-forwarding"]),
			LocalForwarding: rosTruthyC(r["local-forwarding"]),
			Comment:         r["comment"], Legacy: true,
		})
	}
	return out
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
		c = append(c, []any{x.Identity, x.State, x.Version, x.ConnectedTime, x.ClientCount, ifs, x.Legacy})
	}
	pr := make([][]any, 0, len(p.Provisioning))
	for _, x := range p.Provisioning {
		pr = append(pr, []any{x.ID, x.SupportedBands, x.Action, x.MasterConfiguration,
			x.SlaveConfigurations, x.NameFormat, x.RadioMac, x.IdentityRegexp,
			x.Comment, x.Disabled, x.Legacy})
	}
	b, _ := json.Marshal(map[string]any{
		"r": p.Role,
		"m": []any{p.Manager.Enabled, p.Manager.Interfaces, p.Cap.Enabled, p.Cap.CurrentIdentity,
			p.LegacyAvailable, p.LegacyManager},
		"c": c, "p": pr,
		"f": []any{p.Profiles.Configuration, p.Profiles.Security,
			p.Profiles.Channel, p.Profiles.Datapath},
		"t": p.Totals,
	})
	return string(b)
}
