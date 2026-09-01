package collect

// Topology collector — the pure half.
//
// The largest collector in the app (1,087 lines on the Node side) and the only
// one that JOINS five other collectors: neighbours, bridge hosts, wireless
// registrations, ARP and DHCP leases all feed one graph. That join is the next
// slice; this file is the vocabulary it needs — device classification, the two
// RouterOS duration parsers, and the MAC granularity the whole graph is keyed
// on.
//
// These are separated deliberately. Every one of them was DERIVED FROM REAL
// HARDWARE on the Node side and each carries a comment saying which device
// taught it what; none of them needs a router to test. Porting them first means
// the graph builder is written against a vocabulary that is already proved,
// rather than debugged through it.

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
)

// TopoNode is one node of the graph — a device, a client, or this router.
type TopoNode struct {
	Key  string `json:"key"`
	Kind string `json:"kind"` // core | neighbor | client
	Name string `json:"name"`
	MAC  string `json:"mac"`
}

// TopoEdge is one link. `inferred` marks an edge this side reasoned out rather
// than read: a neighbour seen through another neighbour's port.
type TopoEdge struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Iface       string `json:"iface"`
	ViaPort     string `json:"viaPort"`
	RemoteIface string `json:"remoteIface"`
	Shared      bool   `json:"shared"`
	Inferred    bool   `json:"inferred"`
	// Only a CLIENT edge carries this key at all: the live payload omits it on
	// infrastructure edges rather than sending false, and the page tests for
	// presence.
	Client bool `json:"client,omitempty"`
	Gone   bool `json:"gone"`
}

// DeviceClass is what a neighbour is, and how confident that is.
type DeviceClass struct {
	Type   string
	Source string // caps | board | platform | unknown
}

// capMatch maps an LLDP capability token to a device type. Ordered: the first
// hit wins.
var capMatch = []struct {
	typ    string
	tokens []string
}{
	{"ap", []string{"wlan-access-point", "wlan-ap", "wlan_ap", "wlanap", "wlan"}},
	{"station", []string{"station-only", "station"}},
	{"phone", []string{"telephone", "phone", "voice"}},
	{"modem", []string{"docsis-cable-device", "docsis"}},
	{"repeater", []string{"repeater"}},
}

// boardMatch maps a board name to a family. Ordered: AP prefixes are tested
// before the generic RouterBOARD ones.
//
// `hAP` IS DELIBERATELY NOT AN AP. It is a router with a radio, and drawing home
// routers as access points would be wrong — which is why `hap` sits in the
// router row even though it starts with the same letters as the AP families.
var boardMatch = []struct {
	typ string
	re  *regexp.Regexp
}{
	{"switch", regexp.MustCompile(`(?i)^(crs|css|fiberbox)`)},
	{"ap", regexp.MustCompile(`(?i)^(cap|wap|map|wsap|audience|sxt|lhg|ldf|disc|groove|metal|qrt|basebox|omnitik|netmetal|cube|ltap|knot|sextant)`)},
	{"router", regexp.MustCompile(`(?i)^(ccr|rb|hap|hex|chateau|powerbox|l0\d|c5\d|d52|stormboard)`)},
}

func matchBoard(board string) string {
	b := strings.TrimSpace(board)
	if b == "" {
		return ""
	}
	for _, m := range boardMatch {
		if m.re.MatchString(b) {
			return m.typ
		}
	}
	return ""
}

// classifyDevice decides what a neighbour row describes.
//
// CAPTURED FROM REAL HARDWARE, and the shape of the code says so: `system-caps`
// is LLDP-only and comes back EMPTY for every MNDP-discovered MikroTik
// neighbour, so the board fallback is the common path rather than an edge case.
// The observed literals are plain lowercase comma lists — "bridge" from a Meraki
// switch, "bridge,router" from a MikroTik router — so this is set membership
// over a token list with unknown tokens tolerated, not an exact-string switch.
//
// `system-caps-enabled` is preferred over `system-caps` because the first is
// what the device DOES and the second is what it merely supports: a CRS switch
// supports routing and enables only bridging, and preferring "enabled" is what
// keeps it a switch.
func classifyDevice(row routeros.Reply) DeviceClass {
	caps := lowerAll(splitList(row["system-caps-enabled"]))
	if len(caps) == 0 {
		caps = lowerAll(splitList(row["system-caps"]))
	}

	if len(caps) > 0 {
		for _, m := range capMatch {
			for _, c := range caps {
				if containsString(m.tokens, c) {
					return DeviceClass{m.typ, "caps"}
				}
			}
		}
		isRouter := containsString(caps, "router")
		isBridge := containsString(caps, "bridge") || containsString(caps, "switch")
		// Both set is the NORMAL case for a MikroTik router seen over LLDP, so
		// the tie breaks on the board rather than on whichever test came first.
		if isRouter && isBridge {
			if byBoard := matchBoard(row["board"]); byBoard != "" {
				return DeviceClass{byBoard, "board"}
			}
			return DeviceClass{"router", "caps"}
		}
		if isRouter {
			return DeviceClass{"router", "caps"}
		}
		if isBridge {
			return DeviceClass{"switch", "caps"}
		}
	}

	if byBoard := matchBoard(row["board"]); byBoard != "" {
		return DeviceClass{byBoard, "board"}
	}

	platform := strings.TrimSpace(row["platform"])
	if platform != "" && !strings.EqualFold(platform, "mikrotik") {
		return DeviceClass{"other", "platform"}
	}
	if platform != "" {
		return DeviceClass{"router", "platform"}
	}
	return DeviceClass{"unknown", "unknown"}
}

var ageParts = regexp.MustCompile(`\d+[wdhms]`)

// parseAgeSec turns a RouterOS age into seconds. Never NaN, and a bare integer
// is already seconds — which is what the neighbour table sends.
func parseAgeSec(v string) *int {
	if v == "" {
		return nil
	}
	s := strings.TrimSpace(v)
	if n, err := strconv.Atoi(s); err == nil {
		return &n
	}
	parts := ageParts.FindAllString(s, -1)
	if parts == nil {
		return nil
	}
	sec := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p[:len(p)-1])
		if err != nil {
			continue
		}
		switch p[len(p)-1] {
		case 'w':
			sec += n * 604800
		case 'd':
			sec += n * 86400
		case 'h':
			sec += n * 3600
		case 'm':
			sec += n * 60
		default:
			sec += n
		}
	}
	return &sec
}

var rttPart = regexp.MustCompile(`([\d.]+)\s*(us|ms|s)?`)

// parseRttMs turns a RouterOS RTT into milliseconds.
//
// SUB-MILLISECOND REPLIES COME BACK IN MICROSECONDS — "413us" — so stripping the
// unit and treating the number as milliseconds turns a 0.4 ms LAN hop into
// 413 ms, which is slow enough to look like a broken link. Identical to the ping
// collector's parse on purpose: two readings of one router must agree.
func parseRttMs(val string) *float64 {
	if val == "" {
		return nil
	}
	m := rttPart.FindStringSubmatch(val)
	if m == nil {
		return nil
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	switch m[2] {
	case "us":
		v = round3(v / 1000)
	case "s":
		v = round3(v * 1000)
	}
	return &v
}

// topoMacPrefix is the first FIVE octets — the granularity at which a device's
// radios and its base address agree, because MikroTik assigns radio MACs as
// base+1, +2 and so on. Keying the graph on the full address would draw one
// access point as three unrelated devices.
//
// NOT the same function as capsman.go's `macPrefix`, and the difference is the
// short-input case: that one returns "" for anything under five octets, because
// a partial address there means "no match" and matching on it would join the
// wrong CAP. Here the original joins whatever it was given, and a graph key of
// "AA:BB" is harmless — it can only collide with itself. Two contracts, two
// functions, rather than one that quietly serves neither.
func topoMacPrefix(mac string) string {
	parts := strings.Split(strings.ToUpper(mac), ":")
	if len(parts) > 5 {
		parts = parts[:5]
	}
	return strings.Join(parts, ":")
}

var ipv4Shape = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)

func isIPv4(v string) bool {
	if !ipv4Shape.MatchString(v) {
		return false
	}
	for _, o := range strings.Split(v, ".") {
		n, err := strconv.Atoi(o)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

func containsString(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

// ── the graph ────────────────────────────────────────────────────────────────
//
// Three node shapes, not one with optional fields. The core carries `cpuLoad`
// and `memPct`; a neighbour carries none of the client fields; a client carries
// `attrib`, `vlans` and its association. A single struct with `omitempty` would
// drop a false or a zero that the payload states explicitly, and a single struct
// without it would add keys to nodes that never had them — either way the page
// would receive a shape the live app never sends.

// TopoCore is this router.
type TopoCore struct {
	Key         string   `json:"key"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Identity    string   `json:"identity"`
	MAC         string   `json:"mac"`
	IP          string   `json:"ip"`
	IP6         string   `json:"ip6"`
	Type        string   `json:"type"`
	TypeSource  string   `json:"typeSource"`
	Caps        []string `json:"caps"`
	CapsEnabled []string `json:"capsEnabled"`
	Platform    string   `json:"platform"`
	Board       string   `json:"board"`
	Version     string   `json:"version"`
	SoftwareID  string   `json:"softwareId"`
	Description string   `json:"description"`
	Uptime      string   `json:"uptime"`
	AgeSec      *int     `json:"ageSec"`
	Via         []string `json:"via"`
	Running     []string `json:"running"`
	Ifaces      []string `json:"ifaces"`
	RemoteIface string   `json:"remoteIface"`
	IPv6        bool     `json:"ipv6"`
	Port        string   `json:"port"`
	Parent      *string  `json:"parent"`
	Gone        bool     `json:"gone"`
	FirstSeen   int64    `json:"firstSeen"`
	LastSeen    int64    `json:"lastSeen"`
	RTT         *float64 `json:"rtt"`
	Loss        *float64 `json:"loss"`
	PingTS      *int64   `json:"pingTs"`
	CPULoad     *float64 `json:"cpuLoad"`
	MemPct      *float64 `json:"memPct"`
	Status      string   `json:"status"`
	ClientCount int      `json:"clientCount"`
}

// TopoNeighbor is one discovered device.
type TopoNeighbor struct {
	Key         string   `json:"key"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Identity    string   `json:"identity"`
	MAC         string   `json:"mac"`
	IP          string   `json:"ip"`
	IP6         string   `json:"ip6"`
	Type        string   `json:"type"`
	TypeSource  string   `json:"typeSource"`
	Caps        []string `json:"caps"`
	CapsEnabled []string `json:"capsEnabled"`
	Platform    string   `json:"platform"`
	Board       string   `json:"board"`
	Version     string   `json:"version"`
	SoftwareID  string   `json:"softwareId"`
	Description string   `json:"description"`
	Uptime      string   `json:"uptime"`
	AgeSec      *int     `json:"ageSec"`
	Via         []string `json:"via"`
	Running     []string `json:"running"`
	Ifaces      []string `json:"ifaces"`
	RemoteIface string   `json:"remoteIface"`
	IPv6        bool     `json:"ipv6"`
	Gone        bool     `json:"gone"`
	FirstSeen   int64    `json:"firstSeen"`
	LastSeen    int64    `json:"lastSeen"`
	RTT         *float64 `json:"rtt"`
	Loss        *float64 `json:"loss"`
	PingTS      *int64   `json:"pingTs"`
	Status      string   `json:"status"`
	Port        string   `json:"port"`
	Parent      *string  `json:"parent"`
	ClientCount int      `json:"clientCount"`
}

// TopoClient is one MAC from the bridge table that is not already a node.
type TopoClient struct {
	Key         string   `json:"key"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Identity    string   `json:"identity"`
	MAC         string   `json:"mac"`
	IP          string   `json:"ip"`
	IP6         string   `json:"ip6"`
	Type        string   `json:"type"`
	TypeSource  string   `json:"typeSource"`
	Caps        []string `json:"caps"`
	CapsEnabled []string `json:"capsEnabled"`
	Platform    string   `json:"platform"`
	Board       string   `json:"board"`
	Version     string   `json:"version"`
	SoftwareID  string   `json:"softwareId"`
	Description string   `json:"description"`
	Uptime      string   `json:"uptime"`
	AgeSec      *int     `json:"ageSec"`
	Via         []string `json:"via"`
	Running     []string `json:"running"`
	Ifaces      []string `json:"ifaces"`
	RemoteIface string   `json:"remoteIface"`
	IPv6        bool     `json:"ipv6"`
	Port        string   `json:"port"`
	Parent      string   `json:"parent"`
	Attrib      string   `json:"attrib"`
	Vlans       []int    `json:"vlans"`
	VlanNames   []string `json:"vlanNames"`
	SSID        string   `json:"ssid"`
	Signal      string   `json:"signal"`
	Gone        bool     `json:"gone"`
	FirstSeen   int64    `json:"firstSeen"`
	LastSeen    int64    `json:"lastSeen"`
	RTT         *float64 `json:"rtt"`
	Loss        *float64 `json:"loss"`
	PingTS      *int64   `json:"pingTs"`
	Status      string   `json:"status"`
}

type TopoVlan struct {
	VID  int    `json:"vid"`
	Name string `json:"name"`
}

type TopoDiscovery struct {
	Protocol      []string `json:"protocol"`
	Mode          string   `json:"mode"`
	InterfaceList string   `json:"interfaceList"`
	Interval      string   `json:"interval"`
}

type TopologyPayload struct {
	TS int64 `json:"ts"`
	// NO `omitempty`. The live payload carries `routerId: this.rid` on every
	// emit, and an omitted key is not the same as an empty one to a client
	// deciding whether a payload belongs to the router it is watching.
	//
	// It WAS omitempty, and `NewTopology` never received an id — so the field was
	// silently absent from every topology payload this port has ever sent. The
	// tag hid the missing constructor argument: with it, an unset field looks
	// like a deliberate omission rather than a bug.
	//
	// Found by the live-socket-diff tool on 2026-08-28, comparing the payload
	// shapes both servers actually emit.
	RouterID         string         `json:"routerId"`
	PollMs           int            `json:"pollMs"`
	Discovery        *TopoDiscovery `json:"discovery"`
	PermissionDenied bool           `json:"permissionDenied"`
	PingDenied       bool           `json:"pingDenied"`
	NeighborCount    int            `json:"neighborCount"`
	Vlans            []TopoVlan     `json:"vlans"`
	ClientCount      int            `json:"clientCount"`
	ClientsTruncated int            `json:"clientsTruncated"`
	Nodes            []any          `json:"nodes"`
	Edges            []TopoEdge     `json:"edges"`
}

// TopoAssoc is one wireless registration.
type TopoAssoc struct{ Iface, SSID, Signal, Uptime string }

// hostEntry is one MAC of the bridge table, in the order the router listed it.
// A slice rather than a map because the client tier's ORDER follows this table,
// and Go's map iteration would shuffle it on every build.
type hostEntry struct {
	MAC  string
	Port string
}

// TopoInput is everything the graph is built from. Explicit, so the join is a
// pure function of stated inputs rather than of collector state — which is what
// makes it testable against a golden without a router.
type TopoInput struct {
	Now      int64
	Rows     []routeros.Reply // /ip/neighbor
	Hosts    []hostEntry      // bridge MAC table, in order, first writer per MAC
	HostVlan map[string][]int
	Assoc    map[string]TopoAssoc
	// IfaceRadio maps a bridge port (a wifi interface) to the radio behind it,
	// following master-interface for virtual APs.
	IfaceRadio map[string]string
	// CapByPrefix maps a radio's five-octet prefix to a managed CAP's base MAC.
	CapByPrefix map[string]string
	VlanNames   map[int]string
	Bridges     map[string]bool
	Label       string
	Host        string
	PollMs      int
	ShowClients bool
	Discovery   *TopoDiscovery
	PingDenied  bool

	// Seen and Ping are CARRIED ACROSS BUILDS and are read-write. They are the
	// only state the graph keeps: which devices have been here and how each
	// answers a ping. Both belong to the collector; they are passed in rather
	// than closed over so the join stays a function of its inputs.
	Seen map[string]*TopoSeen
	Ping map[string]*TopoPing

	// Optional joins. Each is a function rather than a collector so this file
	// depends on none of them: a nil one degrades exactly one field, which is
	// what the Node original does when a collector is disabled.
	LeaseName func(mac string) (name, ip string)
	ARPIP     func(mac string) string
	Core      *TopoCoreInfo
}

// TopoSeen is one device's history. `Node` is the last good shape, kept so a
// departure can be drawn from it rather than from nothing.
type TopoSeen struct {
	FirstSeen int64
	LastSeen  int64
	Node      *TopoNeighbor
}

// TopoPing is one device's recent answers. `Window` is the last PING_WINDOW
// results, which is what makes `Loss` a percentage rather than a boolean.
type TopoPing struct {
	Window []int
	RTT    *float64
	Loss   *float64
	TS     int64
}

// TopoCoreInfo is what the system collector knows about this router.
type TopoCoreInfo struct {
	Identity string
	Board    string
	Version  string
	Uptime   string
	CPULoad  *float64
	MemPct   *float64
}

// topoRetainMs is how long a departed device stays on the map. An outage that
// removes a device instantly is indistinguishable from a device that was never
// there, which is the opposite of what a network map is for.
const topoRetainMs = 300000

// topoPingWindow is how many results the loss percentage is computed over.
const topoPingWindow = 5

const topoMaxClients = 400

// pickIfaces prefers PHYSICAL interfaces over bridges.
//
// A device reported on both a bridge and its member port is the same device on
// one cable, and drawing it against the bridge loses the port — which is the
// thing the map is for. With only bridges to choose from, the bridges are used.
func pickIfaces(raw string, bridges map[string]bool) []string {
	all := splitList(raw)
	if len(all) < 2 {
		return all
	}
	physical := []string{}
	for _, n := range all {
		if bridges[n] || bridgeNameRe.MatchString(n) {
			continue
		}
		physical = append(physical, n)
	}
	if len(physical) > 0 {
		return physical
	}
	return all
}

var bridgeNameRe = regexp.MustCompile(`(?i)^(bridge|br[-_])`)

// BuildTopology is the whole graph, pure.
//
// ORDER IS PART OF THE CONTRACT. Nodes come out core-first, then neighbours in
// the order the router listed them, then clients in bridge-table order; edges
// follow the same walk. JavaScript's Map preserves insertion order and the page
// renders in the order it receives, so a Go map iteration here would reshuffle
// the map on every poll.
func BuildTopology(in TopoInput) *TopologyPayload {
	now := in.Now

	// ── nodes from the neighbour table ──
	byKey := map[string]*TopoNeighbor{}
	order := []string{}
	for _, r := range in.Rows {
		mac := strings.ToUpper(strings.TrimSpace(r["mac-address"]))
		// RouterOS ids look like "*3". The punctuation is stripped because the
		// key is persisted as an object key by the layout endpoint, whose
		// validator keeps its charset tight — '*' would be silently rejected
		// there, and a MAC-less neighbour could never keep a dragged position.
		id := nonAlnum.ReplaceAllString(firstNonEmptyStr(r[".id"], r["id"]), "")
		key := mac
		if key == "" && id != "" {
			key = "id:" + id
		}
		if key == "" {
			continue
		}

		ifaces := pickIfaces(r["interface"], in.Bridges)

		// A device heard on several interfaces is still ONE device.
		if prev, ok := byKey[key]; ok {
			for _, i := range ifaces {
				if !containsString(prev.Ifaces, i) {
					prev.Ifaces = append(prev.Ifaces, i)
				}
			}
			continue
		}

		cls := classifyDevice(r)
		// The neighbour table does not always carry an address — MNDP rows often
		// have none — so ARP is the fallback. Without it a device is on the map
		// with no way to ping it, which also costs it its status.
		arpIP := ""
		if in.ARPIP != nil {
			arpIP = in.ARPIP(mac)
		}
		ip := firstNonEmptyStr(r["address"], r["address4"], arpIP)
		name := firstNonEmptyStr(r["identity"], r["board"], mac, ip, key)

		byKey[key] = &TopoNeighbor{
			Key: key, Kind: "neighbor", Name: name,
			Identity: r["identity"], MAC: mac, IP: ip, IP6: r["address6"],
			Type: cls.Type, TypeSource: cls.Source,
			Caps: splitList(r["system-caps"]), CapsEnabled: splitList(r["system-caps-enabled"]),
			Platform: r["platform"], Board: r["board"], Version: r["version"],
			SoftwareID: r["software-id"], Description: r["system-description"],
			Uptime: r["uptime"], AgeSec: parseAgeSec(r["age"]),
			Via: splitList(r["discovered-by"]), Running: splitList(r["running"]),
			Ifaces: ifaces, RemoteIface: r["interface-name"],
			IPv6:      r["ipv6"] == "true",
			Gone:      false,
			FirstSeen: now, LastSeen: now,
			Status: "unknown",
		}
		order = append(order, key)
	}

	// ── retention ──
	//
	// A device that stops answering DISAPPEARS from /ip/neighbor, and a map that
	// simply drops it says nothing happened. Departed devices are kept for five
	// minutes, drawn from their last good shape and marked `gone` with status
	// `down`, so an outage is visible for long enough to notice.
	if in.Seen != nil {
		for _, key := range order {
			seen, ok := in.Seen[key]
			if !ok {
				seen = &TopoSeen{FirstSeen: now}
				in.Seen[key] = seen
			}
			seen.LastSeen = now
			seen.Node = byKey[key]
			byKey[key].FirstSeen = seen.FirstSeen
		}
		// Sorted, because Go map iteration is random and the retained tier's
		// order would otherwise reshuffle the map on every poll for no reason.
		retained := []string{}
		for key, seen := range in.Seen {
			if _, live := byKey[key]; live {
				continue
			}
			if now-seen.LastSeen > topoRetainMs || seen.Node == nil {
				delete(in.Seen, key)
				delete(in.Ping, key)
				continue
			}
			retained = append(retained, key)
		}
		sort.Strings(retained)
		for _, key := range retained {
			seen := in.Seen[key]
			ghost := *seen.Node
			ghost.Gone = true
			ghost.AgeSec = nil
			ghost.Status = "down"
			ghost.LastSeen = seen.LastSeen
			byKey[key] = &ghost
			order = append(order, key)
		}
	}

	// ── ports and parentage, before clients ──
	//
	// Client attribution reads `port` to find the switch fronting a cable, so
	// this cannot be deferred.
	resolveParents(byKey, order, in.Hosts)

	for _, key := range order {
		n := byKey[key]
		if p := in.Ping[key]; p != nil {
			n.RTT, n.Loss = p.RTT, p.Loss
			if p.TS != 0 {
				ts := p.TS
				n.PingTS = &ts
			}
		}
		n.Status = topoStatusFor(n, in.Ping[key])
	}

	// ── the core ──
	// The identity comes from the system collector when there is one, and falls
	// back to the router's configured label — a core node headed "Router" is
	// correct but useless on a map of several routers.
	var ci TopoCoreInfo
	if in.Core != nil {
		ci = *in.Core
	}
	core := &TopoCore{
		Key: "core", Kind: "core",
		// THE ROUTER'S OWN LABEL, NOT ITS RouterOS IDENTITY. Every other node
		// here is named from `/ip/neighbor`, which carries `identity`; the core
		// has no such source, because nothing in this app reads
		// `/system/identity` and the system payload has never carried one.
		//
		// This used to try `ci.Identity` first — a branch no producer could
		// satisfy, so the label was reached by accident rather than by choice.
		// Removed to match `d9da7b1` upstream, where the same dead branch was
		// taken out. If it ever needs to change, put an identity on the system
		// payload and prefer it here.
		Name:       firstNonEmptyStr(in.Label, "Router"),
		Identity:   "",
		Type:       "router",
		TypeSource: "self",
		IP:         in.Host,
		Caps:       []string{}, CapsEnabled: []string{},
		Platform: "MikroTik",
		Board:    ci.Board, Version: ci.Version, Uptime: ci.Uptime,
		AgeSec: intpTopo(0),
		Via:    []string{}, Running: []string{}, Ifaces: []string{},
		Parent:   nil,
		LastSeen: now,
		CPULoad:  ci.CPULoad, MemPct: ci.MemPct,
		Status: "up",
	}

	clients := buildTopoClients(in, byKey, order, now)

	perParent := map[string]int{}
	for _, c := range clients {
		perParent[c.Parent]++
	}
	core.ClientCount = perParent["core"]
	for _, key := range order {
		byKey[key].ClientCount = perParent[key]
	}

	nodes := make([]any, 0, 1+len(order)+len(clients))
	nodes = append(nodes, core)
	for _, key := range order {
		nodes = append(nodes, byKey[key])
	}
	for _, c := range clients {
		nodes = append(nodes, c)
	}

	// ── edges ──
	//
	// A node with a parent hangs off that parent; everything else hangs off the
	// core. ONLY THE CORE'S OWN EDGES CARRY AN `iface`, because only those match
	// a router interface whose throughput the router can measure — attaching a
	// port's rate to a downstream link would double-count it.
	perPort := map[string]int{}
	for _, key := range order {
		n := byKey[key]
		if n.Parent != nil {
			continue
		}
		p := n.Port
		if p == "" && len(n.Ifaces) > 0 {
			p = n.Ifaces[0]
		}
		perPort[p]++
	}

	edges := []TopoEdge{}
	for _, key := range order {
		n := byKey[key]
		if n.Parent != nil {
			if _, ok := byKey[*n.Parent]; ok {
				edges = append(edges, TopoEdge{
					ID: *n.Parent + ">" + n.Key, From: *n.Parent, To: n.Key,
					ViaPort: n.Port, RemoteIface: n.RemoteIface,
					Inferred: true, Gone: n.Gone,
				})
				continue
			}
		}
		// Prefer the physical port over the arrival interface: a tagged device
		// arrives on a VLAN, but the cable it is reachable over is the bridge
		// port — which is also the interface whose throughput matches the link
		// being drawn. Falls back to the arrival interface when the MAC is not
		// in the bridge table (a routed or non-bridged link).
		list := []string{}
		switch {
		case n.Port != "":
			list = []string{n.Port}
		case len(n.Ifaces) > 0:
			list = n.Ifaces
		default:
			list = []string{""}
		}
		for _, i := range list {
			shared := n.Port
			if shared == "" {
				shared = i
			}
			edges = append(edges, TopoEdge{
				ID: i + "|" + n.Key, From: "core", To: n.Key, Iface: i,
				ViaPort: n.Port, RemoteIface: n.RemoteIface,
				Shared: perPort[shared] > 1, Inferred: false, Gone: n.Gone,
			})
		}
	}
	for _, c := range clients {
		edges = append(edges, TopoEdge{
			ID: "c|" + c.Parent + ">" + c.Key, From: c.Parent, To: c.Key,
			ViaPort: c.Port,
			// Client links are drawn uniformly. HOW the parent was decided is
			// recorded on the node (`attrib`) and shown in the detail panel, but
			// not on the canvas: purple is reserved for an inferred link between
			// INFRASTRUCTURE, where it carries real meaning.
			Inferred: false, Client: true, Gone: false,
		})
	}

	// Only the VLANs clients were actually seen on, so the filter never offers an
	// option that would match nothing.
	seenVlan := map[int]bool{}
	vids := []int{}
	for _, c := range clients {
		for _, v := range c.Vlans {
			if !seenVlan[v] {
				seenVlan[v] = true
				vids = append(vids, v)
			}
		}
	}
	sort.Ints(vids)
	vlans := make([]TopoVlan, 0, len(vids))
	for _, v := range vids {
		name := in.VlanNames[v]
		if name == "" {
			name = strconv.Itoa(v)
		}
		vlans = append(vlans, TopoVlan{VID: v, Name: name})
	}

	return &TopologyPayload{
		TS: now, PollMs: in.PollMs, Discovery: in.Discovery, PingDenied: in.PingDenied,
		NeighborCount: len(order), Vlans: vlans,
		ClientCount: len(clients), ClientsTruncated: topoTruncated(in, byKey, order),
		Nodes: nodes, Edges: edges,
	}
}

var nonAlnum = regexp.MustCompile(`[^A-Za-z0-9]`)

// resolveParents works out which devices sit BEHIND another device.
//
// THE SIGNAL IS LLDP'S LINK-LOCALITY. LLDP frames use a reserved multicast
// destination a conformant bridge must not forward, so a device discovered via
// LLDP is necessarily attached to that port directly. MNDP and CDP are ordinary
// frames a switch happily passes along, so a device seen only via those is
// somewhere further out.
//
// Therefore on a given physical port at most one device can be the direct
// neighbour, and it is the LLDP one. Anything else on that port is behind it.
// With NO LLDP device on the port there is nothing to attribute the others to —
// an unmanaged switch is invisible by definition — so they stay on the core and
// the port is flagged `shared` rather than inventing a hierarchy.
func resolveParents(byKey map[string]*TopoNeighbor, order []string, hosts []hostEntry) {
	// The bridge host table wins over the arrival interface: /ip/neighbor reports
	// the interface a frame arrived on, which for a tagged device is the VLAN,
	// and two devices on one cable would then never be grouped.
	hostPort := map[string]string{}
	for _, h := range hosts {
		if _, ok := hostPort[h.MAC]; !ok {
			hostPort[h.MAC] = h.Port
		}
	}
	for _, key := range order {
		n := byKey[key]
		p := ""
		if n.MAC != "" {
			p = hostPort[n.MAC]
		}
		if p == "" && len(n.Ifaces) > 0 {
			p = n.Ifaces[0]
		}
		n.Port = p
		n.Parent = nil
	}

	byPort := map[string][]string{}
	portOrder := []string{}
	for _, key := range order {
		n := byKey[key]
		if n.Port == "" {
			continue
		}
		if _, ok := byPort[n.Port]; !ok {
			portOrder = append(portOrder, n.Port)
		}
		byPort[n.Port] = append(byPort[n.Port], key)
	}

	for _, port := range portOrder {
		group := byPort[port]
		if len(group) < 2 {
			continue
		}
		direct := []string{}
		for _, key := range group {
			if containsString(byKey[key].Via, "lldp") {
				direct = append(direct, key)
			}
		}
		// EXACTLY ONE direct neighbour is the only unambiguous case. Zero means
		// an invisible (unmanaged) switch; more than one means something is
		// forwarding LLDP that should not be. Both stay flat rather than guess.
		if len(direct) != 1 {
			continue
		}
		parent := direct[0]
		for _, key := range group {
			if key == parent {
				continue
			}
			p := parent
			byKey[key].Parent = &p
		}
	}

	// A device cannot be its own ancestor. Not reachable from the rule above, but
	// a stale retained node could carry an old parent, so verify rather than
	// trust.
	for _, key := range order {
		n := byKey[key]
		seen := map[string]bool{n.Key: true}
		p := n.Parent
		for p != nil {
			if seen[*p] {
				n.Parent = nil
				break
			}
			seen[*p] = true
			up, ok := byKey[*p]
			if !ok {
				break
			}
			p = up.Parent
		}
		if n.Parent != nil {
			if _, ok := byKey[*n.Parent]; !ok {
				n.Parent = nil
			}
		}
	}
}

func topoTruncated(in TopoInput, byKey map[string]*TopoNeighbor, order []string) int {
	// Recomputed rather than carried out of buildTopoClients, which returns only
	// the list. Cheap, and it keeps that function returning one thing.
	if !in.ShowClients {
		return 0
	}
	infra := topoInfraMacs(byKey, order)
	n := 0
	for _, h := range in.Hosts {
		if infra[h.MAC] || infra[topoMacPrefix(h.MAC)] {
			continue
		}
		n++
	}
	if n > topoMaxClients {
		return n - topoMaxClients
	}
	return 0
}

func topoInfraMacs(byKey map[string]*TopoNeighbor, order []string) map[string]bool {
	infra := map[string]bool{}
	for _, key := range order {
		if mac := byKey[key].MAC; mac != "" {
			infra[mac] = true
			infra[topoMacPrefix(mac)] = true
		}
	}
	return infra
}

// buildTopoClients builds the client tier from the bridge MAC table.
//
// Attribution comes from the port a MAC is bridged on, which answers all four
// cases without guessing:
//
//	a wifi interface whose radio belongs to a managed AP  -> that AP
//	a wifi interface whose radio is one of ours           -> this router
//	a physical port fronted by a discovered switch        -> that switch
//	any other physical port                               -> this router
//
// Infrastructure MACs are excluded: a discovered neighbour is already a node,
// and it appears in the host table as well.
func buildTopoClients(in TopoInput, byKey map[string]*TopoNeighbor, order []string, now int64) []*TopoClient {
	if !in.ShowClients || len(in.Hosts) == 0 {
		return []*TopoClient{}
	}
	infra := topoInfraMacs(byKey, order)

	// Which discovered device fronts each port, so a client there is attributed
	// to it rather than to the router. Only UNPARENTED devices are candidates —
	// something already known to sit behind a switch is not the thing at the
	// front of the cable.
	//
	// A LONE device on a port fronts it whatever protocol found it: there is no
	// second candidate, so nothing to disambiguate. LLDP is only needed to pick
	// between several, where it identifies the directly-attached one. Without
	// this, a neighbour speaking only CDP or MNDP could never own a client even
	// when it is demonstrably the only thing on that port.
	onPort := map[string][]string{}
	portOrder := []string{}
	for _, key := range order {
		n := byKey[key]
		if n.Port == "" || n.Parent != nil {
			continue
		}
		if _, ok := onPort[n.Port]; !ok {
			portOrder = append(portOrder, n.Port)
		}
		onPort[n.Port] = append(onPort[n.Port], key)
	}
	switchOnPort := map[string]string{}
	for _, port := range portOrder {
		list := onPort[port]
		if len(list) == 1 {
			switchOnPort[port] = list[0]
			continue
		}
		direct := []string{}
		for _, key := range list {
			if containsString(byKey[key].Via, "lldp") {
				direct = append(direct, key)
			}
		}
		if len(direct) == 1 {
			switchOnPort[port] = direct[0]
		}
	}

	out := []*TopoClient{}
	for _, h := range in.Hosts {
		mac, port := h.MAC, h.Port
		if infra[mac] || infra[topoMacPrefix(mac)] {
			continue
		}
		if len(out) >= topoMaxClients {
			continue
		}

		assoc, hasAssoc := in.Assoc[mac]
		radio := in.IfaceRadio[port]
		parent := "core"
		wireless := false
		// How the parent was decided, so the page can tell an OBSERVED
		// association from a DEDUCED one:
		//   radio  - the client associated with this AP's radio (observed)
		//   port   - it merely shares a port with that device (deduced)
		//   direct - straight into a router port (observed)
		attrib := "direct"

		if radio != "" {
			wireless = true
			// A managed AP's radio resolves to that AP — but only if it is on
			// the map; otherwise the client belongs to the router fronting it.
			if base, ok := in.CapByPrefix[topoMacPrefix(radio)]; ok {
				for _, key := range order {
					if topoMacPrefix(byKey[key].MAC) == topoMacPrefix(base) {
						parent = key
						attrib = "radio"
						break
					}
				}
			}
		} else if p, ok := switchOnPort[port]; ok {
			parent = p
			attrib = "port"
		}
		if hasAssoc {
			wireless = true
		}

		vlans := append([]int{}, in.HostVlan[mac]...)
		sort.Ints(vlans)
		vlanNames := make([]string, 0, len(vlans))
		for _, v := range vlans {
			name := in.VlanNames[v]
			if name == "" {
				name = strconv.Itoa(v)
			}
			vlanNames = append(vlanNames, name)
		}

		typ, typSrc := "wired-client", "bridge"
		if wireless {
			typ, typSrc = "wifi-client", "assoc"
		}

		// A client is a MAC until something names it. The DHCP lease is the only
		// name most devices ever offer, and ARP is the only address for one that
		// has a static configuration.
		name, leaseIP := "", ""
		if in.LeaseName != nil {
			name, leaseIP = in.LeaseName(mac)
		}
		ip := ""
		if in.ARPIP != nil {
			ip = in.ARPIP(mac)
		}
		if ip == "" {
			ip = leaseIP
		}
		out = append(out, &TopoClient{
			Key: mac, Kind: "client",
			Name:     firstNonEmptyStr(name, ip, mac),
			Identity: name, MAC: mac, IP: ip,
			Type: typ, TypeSource: typSrc,
			Caps: []string{}, CapsEnabled: []string{},
			Uptime: assoc.Uptime,
			Via:    []string{}, Running: []string{},
			Ifaces: []string{port},
			Port:   port, Parent: parent, Attrib: attrib,
			Vlans: vlans, VlanNames: vlanNames,
			SSID: assoc.SSID, Signal: assoc.Signal,
			FirstSeen: now, LastSeen: now,
			Status: "up",
		})
	}
	return out
}

// topoStatusFor is the traffic light on a node.
//
// A ping result outranks an age, because it is a measurement rather than an
// inference. With neither, "unknown" — NOT "up", which would claim a device is
// reachable on no evidence at all.
func topoStatusFor(n *TopoNeighbor, p *TopoPing) string {
	if n.Gone {
		return "down"
	}
	// A PING RESULT OUTRANKS AN AGE, because it is a measurement rather than an
	// inference: an age says the router heard a broadcast recently, a ping says
	// the device answered us. Loss first, then latency — a link losing packets
	// is worth flagging even when the replies that do arrive are fast.
	if p != nil && len(p.Window) > 0 {
		if p.Loss != nil && *p.Loss >= 100 {
			return "down"
		}
		if p.Loss != nil && *p.Loss > 0 {
			return "warn"
		}
		if p.RTT != nil && *p.RTT > 100 {
			return "warn"
		}
		return "up"
	}
	if n.AgeSec != nil {
		if *n.AgeSec > 90 {
			return "warn"
		}
		return "up"
	}
	return "unknown"
}

// intpTopo is a local int pointer helper: `ageSec: 0` on the core is a stated
// zero, not an absent value, and the payload distinguishes the two.
func intpTopo(n int) *int { return &n }

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ── the collector ────────────────────────────────────────────────────────────

var (
	topoNeighborCmd = routeros.Cmd{Path: "/ip/neighbor/print"}
	topoSettingsCmd = routeros.Cmd{Path: "/ip/neighbor/discovery-settings/print"}
	topoHostsCmd    = routeros.Cmd{Path: "/interface/bridge/host/print", Args: []string{
		"?local=false", "=.proplist=mac-address,on-interface,bridge,vid"}}
	topoVlanCmd  = routeros.Cmd{Path: "/interface/vlan/print", Args: []string{"=.proplist=name,vlan-id"}}
	topoWifiCmd  = routeros.Cmd{Path: "/interface/wifi/print", Args: []string{"=.proplist=name,radio-mac,master-interface,disabled"}}
	topoRegCmd   = routeros.Cmd{Path: "/interface/wifi/registration-table/print", Args: []string{"=.proplist=mac-address,interface,ssid,signal,uptime"}}
	topoCapsCmd  = routeros.Cmd{Path: "/interface/wifi/capsman/remote-cap/print", Args: []string{"=.proplist=identity,address,board-name,state"}}
	topoWlCmd    = routeros.Cmd{Path: "/interface/wireless/print", Args: []string{"=.proplist=name,mac-address,master-interface,disabled"}}
	topoWlRegCmd = routeros.Cmd{Path: "/interface/wireless/registration-table/print", Args: []string{"=.proplist=mac-address,interface,ssid,signal-strength,uptime"}}
)

// Topology is the collector.
type Topology struct {
	ros      Reader
	emit     Emit
	pollMs   *pollInterval
	label    string
	routerID string
	rates    RateSource

	mu        sync.Mutex
	last      *TopologyPayload
	discovery *TopoDiscovery
	vlanNames map[int]string
	seen      map[string]*TopoSeen
	ping      map[string]*TopoPing
	// lastIn is the last input Tick built a payload from, so the ping loop can
	// rebuild the graph without re-reading the router. Nil until the first
	// successful Tick.
	lastIn *TopoInput
	// pingCursor walks the target list one device per step rather than pinging
	// everything at once: a burst of two dozen pings is exactly the kind of
	// concurrent work small hardware does badly.
	pingCursor int
	pingDenied bool

	// leases and core are the optional joins, held as interfaces so this
	// collector depends on neither type. A nil one degrades one field.
	leases *DHCPLeases
	sys    *System

	loop     *pollLoop
	pingLoop *pollLoop
}

// topoPingStep is the gap between one device's probe and the next. Twenty-four
// targets at three seconds is a full sweep every seventy-two seconds, which is
// well inside the five-minute retention window.
const topoPingStep = 3 * time.Second

// topoMaxPingTargets bounds the sweep. A map with two hundred devices must not
// turn the router into a ping generator.
const topoMaxPingTargets = 24

// NewTopology builds the collector. `label` names the core node when no system
// payload is available, matching the original's fallback chain.
func NewTopology(ros Reader, emit Emit, rates RateSource, routerID, label string, pollMs int) *Topology {
	t := &Topology{
		ros: ros, emit: emit, rates: rates, routerID: routerID, label: label,
		pollMs:    newPollInterval(clampPoll(pollMs, 30000, 5000, 300000)),
		vlanNames: map[int]string{},
		seen:      map[string]*TopoSeen{},
		ping:      map[string]*TopoPing{},
	}
	t.loop = newPollLoop(func() { t.Tick() }, func() time.Duration {
		return t.pollMs.duration()
	})
	t.pingLoop = newPollLoop(func() { t.pingNext() }, func() time.Duration {
		return topoPingStep
	})
	return t
}

// WithSources attaches the optional joins: DHCP leases name the clients, and the
// system collector fills the core's identity and gauges. Both are optional, and
// a missing one costs exactly the fields it feeds.
func (t *Topology) WithSources(leases *DHCPLeases, sys *System) *Topology {
	t.leases = leases
	t.sys = sys
	return t
}

// leaseName resolves a client MAC to its DHCP name and address.
func (t *Topology) leaseName(mac string) (string, string) {
	if t.leases == nil {
		return "", ""
	}
	p := t.leases.Last()
	if p == nil {
		return "", ""
	}
	for _, l := range p.Leases {
		if strings.EqualFold(l.MAC, mac) {
			return firstNonEmptyStr(l.Name, l.HostName, l.Comment), l.IP
		}
	}
	return "", ""
}

// coreInfo is what the system collector knows about this router.
func (t *Topology) coreInfo() *TopoCoreInfo {
	if t.sys == nil {
		return nil
	}
	p := t.sys.Last()
	if p == nil {
		return nil
	}
	cpu := float64(p.CPULoad)
	mem := float64(p.MemPct)
	return &TopoCoreInfo{
		Board: p.BoardName, Version: p.Version, Uptime: p.UptimeRaw,
		CPULoad: &cpu, MemPct: &mem,
	}
}

// pingNext probes ONE device and rebuilds.
//
// One at a time, on a three second step, walking the list. The alternative —
// pinging every device each tick — is a burst of concurrent work on hardware
// whose documented limit is concurrency, to measure something that changes
// slowly.
func (t *Topology) pingNext() {
	if !t.ros.Connected() {
		return
	}
	t.mu.Lock()
	denied, last := t.pingDenied, t.last
	t.mu.Unlock()
	if denied || last == nil {
		return
	}

	targets := []struct{ key, ip string }{}
	for _, n := range last.Nodes {
		switch v := n.(type) {
		case *TopoNeighbor:
			if !v.Gone && isIPv4(v.IP) {
				targets = append(targets, struct{ key, ip string }{v.Key, v.IP})
			}
		}
		if len(targets) >= topoMaxPingTargets {
			break
		}
	}
	if len(targets) == 0 {
		return
	}

	t.mu.Lock()
	tgt := targets[t.pingCursor%len(targets)]
	t.pingCursor = (t.pingCursor + 1) % len(targets)
	t.mu.Unlock()

	rows, err := t.ros.Do(routeros.Cmd{Path: "/tool/ping", Args: []string{
		"=address=" + tgt.ip, "=count=1", "=interval=1"}})
	if err != nil {
		// /tool/ping needs the "test" policy. A user without it can still have a
		// map; it just has no per-device latency, and the payload says so rather
		// than showing every device as unknown for ever.
		if menuDenied(err) {
			t.mu.Lock()
			t.pingDenied = true
			t.mu.Unlock()
			t.pingLoop.stop()
			t.republish()
			return
		}
		// A single unreachable host must never stop the loop.
		t.recordPing(tgt.key, false, nil)
		t.republish()
		return
	}

	var row routeros.Reply
	for _, r := range rows {
		row = r // the LAST row: /tool/ping's final sentence carries the result
	}
	rtt := parseRttMs(row["time"])
	// received=0 is an explicit timeout; a missing time with no counters means
	// the reply was not usable either way.
	replied := rtt != nil && row["received"] != "0"
	t.recordPing(tgt.key, replied, rtt)
	t.republish()
}

// recordPing folds one result into the device's window.
func (t *Topology) recordPing(key string, replied bool, rtt *float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rec := t.ping[key]
	if rec == nil {
		rec = &TopoPing{}
		t.ping[key] = rec
	}
	v := 0
	if replied {
		v = 1
	}
	rec.Window = append(rec.Window, v)
	if len(rec.Window) > topoPingWindow {
		rec.Window = rec.Window[len(rec.Window)-topoPingWindow:]
	}
	if replied {
		rec.RTT = rtt
	} else {
		rec.RTT = nil
	}
	sum := 0
	for _, w := range rec.Window {
		sum += w
	}
	loss := math.Round((1 - float64(sum)/float64(len(rec.Window))) * 100)
	rec.Loss = &loss
	rec.TS = time.Now().UnixMilli()
}

func (t *Topology) Suspend() {
	t.loop.stop()
	t.pingLoop.stop()
}

func (t *Topology) Resume() {
	if !t.ros.Connected() {
		return
	}
	t.loop.start()
	t.mu.Lock()
	denied := t.pingDenied
	t.mu.Unlock()
	if !denied {
		t.pingLoop.start()
	}
}

func (t *Topology) Start() {
	t.Tick()
	t.loop.start()
	t.pingLoop.start()
}

func (t *Topology) Stop() {
	t.loop.stop()
	t.pingLoop.stop()
}

func (t *Topology) Reconnected() {
	t.loop.stop()
	t.pingLoop.stop()
	t.mu.Lock()
	t.discovery = nil
	// The seen and ping histories describe the network as it was BEFORE the
	// drop. Keeping them would draw devices as present on the strength of a
	// connection that no longer exists.
	t.seen = map[string]*TopoSeen{}
	t.ping = map[string]*TopoPing{}
	t.pingDenied = false
	t.mu.Unlock()
	t.Tick()
	t.loop.start()
	t.pingLoop.start()
}

func (t *Topology) Last() *TopologyPayload {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last
}

// bridgeNames is the set of bridge interfaces, from the rate source's last
// payload. Without one every interface looks physical, which only costs the
// bridge-vs-port preference in pickIfaces.
func (t *Topology) bridgeNames() map[string]bool {
	out := map[string]bool{}
	if t.rates == nil {
		return out
	}
	// The RateSource interface carries rates, not types, so this reads the
	// ifStatus payload directly when one is available.
	if s, ok := t.rates.(interface{ Last() *IfStatusPayload }); ok {
		if p := s.Last(); p != nil {
			for _, i := range p.Interfaces {
				if i.Type == "bridge" && i.Name != "" {
					out[i.Name] = true
				}
			}
		}
	}
	return out
}

// Tick reads everything the graph is built from and builds it.
//
// FIVE READS, SEQUENTIALLY, on one channel. The Node original throttles the
// annotation tables to once per poll interval and streams the neighbour table;
// this side polls all five, because the documented bottleneck is CONCURRENT
// channels rather than request count, and one sequential pass holds exactly one.
func (t *Topology) Tick() {
	if !t.ros.Connected() {
		return
	}

	rows, err := t.ros.Do(topoNeighborCmd)
	if err != nil {
		// A user without the policy for /ip/neighbor cannot have a topology at
		// all. Reported on the payload rather than logged and forgotten.
		if menuDenied(err) {
			t.mu.Lock()
			denied := &TopologyPayload{
				TS: time.Now().UnixMilli(), RouterID: t.routerID, PollMs: t.pollMs.ms(),
				PermissionDenied: true, Vlans: []TopoVlan{},
				Nodes: []any{}, Edges: []TopoEdge{},
			}
			t.last = denied
			t.mu.Unlock()
			t.emit("page-network-topology", "topology:update", denied)
		}
		return
	}

	hosts, hostVlan := t.readHosts()
	ifaceRadio, capByPrefix, assoc := t.readWifi()

	t.mu.Lock()
	if t.discovery == nil {
		t.mu.Unlock()
		t.readDiscovery()
		t.readVlans()
		t.mu.Lock()
	}
	vlanNames := t.vlanNames
	discovery := t.discovery
	t.mu.Unlock()

	t.mu.Lock()
	seen, ping, pingDenied := t.seen, t.ping, t.pingDenied
	t.mu.Unlock()

	in := TopoInput{
		Now: time.Now().UnixMilli(), Rows: rows,
		Hosts: hosts, HostVlan: hostVlan, Assoc: assoc,
		IfaceRadio: ifaceRadio, CapByPrefix: capByPrefix,
		VlanNames: vlanNames, Bridges: t.bridgeNames(),
		Label: t.label, PollMs: t.pollMs.ms(), ShowClients: true,
		Discovery: discovery, PingDenied: pingDenied,
		Seen: seen, Ping: ping,
		LeaseName: t.leaseName, Core: t.coreInfo(),
	}
	payload := BuildTopology(in)

	// KEPT SO THE PING LOOP CAN REBUILD WITHOUT ASKING THE ROUTER AGAIN.
	// `BuildTopology` is pure, and `rows`, `hosts` and the wifi tables above are
	// the only parts of this input that cost a command -- everything else is
	// already in memory. See `republish`.
	t.mu.Lock()
	t.lastIn = &in
	t.mu.Unlock()

	// SET HERE rather than inside BuildTopology, which is pure and takes only what
	// the topology itself is built from. The router id is the COLLECTOR's, not
	// the graph's.
	payload.RouterID = t.routerID

	t.mu.Lock()
	t.last = payload
	t.mu.Unlock()
	t.emit("page-network-topology", "topology:update", payload)
}

// republish rebuilds the graph from the LAST READ and emits it, without asking
// the router for anything.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// `pingNext` used to call `Tick()`, and Tick is five commands: /ip/neighbor, the
// bridge host table and the wifi registration tables. At `topoPingStep` that ran
// every THREE SECONDS while the page was open, against a collector whose
// configured interval is thirty. The operator set 30s and got 3s, and the
// expensive reads were the ones being repeated.
//
// The ping result still has to reach the browser promptly -- a map whose
// latency and up/down state lagged by half a minute would be a worse page. So
// the split is by COST rather than by data: the structure comes from the router
// on the poll interval, and everything derived in memory is republished as often
// as the ping loop turns.
//
// `BuildTopology` is pure, so this is honest rather than a cache trick: it is
// the same function over the same rows, with fresh ping state, `Seen` and clock.
// Status included -- `topoStatusFor` runs inside the build, so a device going
// quiet still flips within one ping step.
//
// LINK RATES ARE NOT HERE, and do not need to be. They already reach the page
// independently on `ifstatus:update`, which the browser receives router-wide;
// `web/src/pages/topology.ts` keeps its own `rates` map and re-renders on it.
// That is the fast, independently-governed half, and it costs no extra command
// because ifStatus reads every interface in ONE bulk call.
func (t *Topology) republish() {
	t.mu.Lock()
	if t.lastIn == nil {
		// No successful Tick yet: nothing to rebuild from. The first Tick will
		// publish, so dropping this is right rather than merely tolerable.
		t.mu.Unlock()
		return
	}
	in := *t.lastIn // a copy, so the fresh fields below cannot race the next Tick
	in.Now = time.Now().UnixMilli()
	in.Seen, in.Ping, in.PingDenied = t.seen, t.ping, t.pingDenied
	t.mu.Unlock()

	// Re-read in memory, because both can move between structure polls: uptime
	// ticks, and an interface can go down.
	in.Core = t.coreInfo()
	in.Bridges = t.bridgeNames()
	in.PollMs = t.pollMs.ms()

	payload := BuildTopology(in)
	payload.RouterID = t.routerID

	t.mu.Lock()
	t.last = payload
	t.mu.Unlock()
	t.emit("page-network-topology", "topology:update", payload)
}

// readHosts is the bridge MAC table.
//
// FIRST WRITER WINS FOR THE PORT: a MAC seen on several VLANs of one port yields
// the same port anyway, and a genuinely moving MAC is not something to chase
// here. VLANs ACCUMULATE, because a trunked device legitimately appears on more
// than one.
func (t *Topology) readHosts() ([]hostEntry, map[string][]int) {
	rows, err := t.ros.Do(topoHostsCmd)
	if err != nil {
		// A router with no bridge, or a user without the policy: the map still
		// works, it just falls back to the arrival interface.
		return nil, map[string][]int{}
	}
	seen := map[string]bool{}
	hosts := []hostEntry{}
	vlans := map[string][]int{}
	for _, r := range rows {
		mac := strings.ToUpper(strings.TrimSpace(r["mac-address"]))
		port := r["on-interface"]
		if mac == "" || port == "" {
			continue
		}
		if !seen[mac] {
			seen[mac] = true
			hosts = append(hosts, hostEntry{MAC: mac, Port: port})
		}
		if vid := jsParseInt(r["vid"]); vid != nil {
			if !containsInt(vlans[mac], *vid) {
				vlans[mac] = append(vlans[mac], *vid)
			}
		}
	}
	return hosts, vlans
}

// readWifi resolves radios, managed CAPs and associations.
//
// Everything here is BEST EFFORT: a router with no wireless, or a user without
// the policy, yields wired-only attribution rather than an error. The legacy
// stack is tried only when the modern menu is absent, matching the original.
func (t *Topology) readWifi() (map[string]string, map[string]string, map[string]TopoAssoc) {
	ifaceRadio := map[string]string{}
	capByPrefix := map[string]string{}
	assoc := map[string]TopoAssoc{}

	ifaces, err := t.ros.Do(topoWifiCmd)
	legacy := false
	var reg []routeros.Reply
	if err == nil {
		reg, _ = t.ros.Do(topoRegCmd)
	} else {
		ifaces, err = t.ros.Do(topoWlCmd)
		if err != nil {
			return ifaceRadio, capByPrefix, assoc
		}
		legacy = true
		reg, _ = t.ros.Do(topoWlRegCmd)
	}

	// radio-mac per interface, FOLLOWING master-interface for virtual APs: a
	// multi-SSID interface carries no radio of its own, so without this chain
	// every client on a virtual AP is misattributed to the router.
	type radioRow struct{ mac, master string }
	raw := map[string]radioRow{}
	names := []string{}
	for _, i := range ifaces {
		if i["name"] == "" {
			continue
		}
		names = append(names, i["name"])
		raw[i["name"]] = radioRow{
			mac:    strings.ToUpper(firstNonEmptyStr(i["radio-mac"], i["mac-address"])),
			master: i["master-interface"],
		}
	}
	var radioOf func(name string, depth int) string
	radioOf = func(name string, depth int) string {
		r, ok := raw[name]
		if !ok || depth > 4 {
			return ""
		}
		if r.mac != "" {
			return r.mac
		}
		if r.master != "" {
			return radioOf(r.master, depth+1)
		}
		return ""
	}
	for _, name := range names {
		ifaceRadio[name] = radioOf(name, 0)
	}

	if !legacy {
		caps, err := t.ros.Do(topoCapsCmd)
		if err == nil {
			// A managed AP's radios are not its base MAC but a small offset from
			// it (base+1, +2 …), so the match is on the first five octets.
			for _, c := range caps {
				base := strings.ToUpper(strings.Split(c["address"], "%")[0])
				if base != "" {
					capByPrefix[topoMacPrefix(base)] = base
				}
			}
		}
	}

	for _, w := range reg {
		mac := strings.ToUpper(w["mac-address"])
		if mac == "" {
			continue
		}
		assoc[mac] = TopoAssoc{
			Iface: w["interface"], SSID: w["ssid"],
			Signal: firstNonEmptyStr(w["signal"], w["signal-strength"]),
			Uptime: w["uptime"],
		}
	}
	return ifaceRadio, capByPrefix, assoc
}

// readDiscovery and readVlans change only when the operator edits the config, so
// they ride along with the first build after a connect rather than every tick.
func (t *Topology) readDiscovery() {
	rows, err := t.ros.Do(topoSettingsCmd)
	if err != nil || len(rows) == 0 {
		return
	}
	r := rows[0]
	t.mu.Lock()
	t.discovery = &TopoDiscovery{
		Protocol:      splitList(r["protocol"]),
		Mode:          r["mode"],
		InterfaceList: r["discover-interface-list"],
		Interval:      r["discover-interval"],
	}
	t.mu.Unlock()
}

func (t *Topology) readVlans() {
	rows, err := t.ros.Do(topoVlanCmd)
	if err != nil {
		return // no VLANs configured, or not permitted: ids alone still work
	}
	m := map[int]string{}
	for _, r := range rows {
		if vid := jsParseInt(r["vlan-id"]); vid != nil && r["name"] != "" {
			m[*vid] = r["name"]
		}
	}
	t.mu.Lock()
	t.vlanNames = m
	t.mu.Unlock()
}

func containsInt(hay []int, n int) bool {
	for _, v := range hay {
		if v == n {
			return true
		}
	}
	return false
}

// SetPollMs applies a new poll period to a running collector.
// See `System.SetPollMs` for why both halves are needed.
func (t *Topology) SetPollMs(ms int) {
	t.pollMs.set(ms)
	t.loop.retime()
}
